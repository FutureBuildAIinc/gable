// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package pos_test

import (
	"context"
	"errors"
	"net/http"
	"testing"

	"github.com/gablelbm/gable/internal/platform/httpx"
	"github.com/gablelbm/gable/internal/pos"
	"github.com/gablelbm/gable/internal/testutil"
	"github.com/google/uuid"
)

// The manager's resolutions of the offline exception queue (ADR 0011 3.4
// and section 7, slice O4).

// wantRefused asserts a resolution answered 409 with the blocker.
func wantRefused(t *testing.T, err error, blocker string) {
	t.Helper()
	var hx *httpx.Error
	if !errors.As(err, &hx) || hx.Status != http.StatusConflict {
		t.Fatalf("resolution = %v, want 409 %s", err, blocker)
	}
	for _, d := range hx.Details {
		if d.Code == blocker {
			return
		}
	}
	t.Fatalf("resolution blockers = %v, want %s", hx.Details, blocker)
}

// exceptionState reads a row's status, revision and posted sale.
func exceptionState(t *testing.T, f *fixture, id uuid.UUID) (status string, revision int64, posted *uuid.UUID) {
	t.Helper()
	if err := f.db.Pool.QueryRow(context.Background(), `SELECT status, revision, posted_sale_id FROM pos_offline_exceptions
		WHERE id = $1`, id).Scan(&status, &revision, &posted); err != nil {
		t.Fatal(err)
	}
	return status, revision, posted
}

// wantNothingPosted asserts a sale id holds no sale row.
func wantNothingPosted(t *testing.T, f *fixture, sale *pos.SignedOfflineSale) {
	t.Helper()
	if n := countOf(t, f.db, `SELECT count(*) FROM pos_transactions WHERE id = $1`, sale.ClientID); n != 0 {
		t.Fatalf("the sale posted")
	}
}

// wantPosted asserts the resolution posted the sale, completed with its
// invoice, and the row names it.
func wantPosted(t *testing.T, f *fixture, e *pos.OfflineException, sale *pos.SignedOfflineSale) {
	t.Helper()
	if e.Status != "resolved" || e.PostedSaleID == nil || e.PostedSaleID.String() != sale.ClientID {
		t.Fatalf("row = %s posted %v, want resolved naming %s", e.Status, e.PostedSaleID, sale.ClientID)
	}
	if n := countOf(t, f.db, `SELECT count(*) FROM pos_transactions WHERE id = $1 AND status = 'COMPLETED'
		AND invoice_id IS NOT NULL`, sale.ClientID); n != 1 {
		t.Fatalf("the sale is not completed with its invoice")
	}
	if n := countOf(t, f.db, `SELECT count(*) FROM audit_log WHERE entity_id = $1 AND action = 'pos.offline_exception.resolved'
		AND user_id = $2`, e.ID, offlineManager); n != 1 {
		t.Errorf("%d resolution audit rows naming the manager, want 1", n)
	}
	if n := countOf(t, f.db, `SELECT count(*) FROM events_outbox WHERE entity_id = $1 AND type = 'pos_offline.exception_resolved'`,
		e.ID); n != 1 {
		t.Errorf("%d resolution events, want 1", n)
	}
}

// RULE (ADR 0011 3.4 "Retry"): a retry before the cause is fixed is refused
// with the live check's own code, writes nothing and leaves the row open at
// its revision; after an inventory adjustment the retry posts through the
// completion's body, the row resolved naming the sale, and a replay of the
// item answers duplicate.
func TestOfflineResolve_RetryAfterTheStockIsFixed(t *testing.T) {
	f := newFixture(t, testutil.RequireDB(t))
	offlinePolicyOn(t, f)
	b := newOfflineBench(t, f)
	raw, sale := b.build(t, saleSpec{lines: []offlineLine{line(f.productID, "101")}})
	id := *wantOutcome(t, b.sync(t, raw), sale, pos.OfflineRefused, "insufficient_stock").ExceptionID
	_, rev, _ := exceptionState(t, f, id)

	_, err := resolveAs(t, f, id, pos.ResolveRetry, offlineManager)
	wantRefused(t, err, "insufficient_stock")
	if st, r, _ := exceptionState(t, f, id); st != "open" || r != rev {
		t.Fatalf("a refused retry left the row %s at %d, want open at %d", st, r, rev)
	}
	wantNothingPosted(t, f, sale)

	mustExec(t, f.db, `UPDATE inventory SET quantity = quantity + 10 WHERE product_id = $1`, f.productID)
	e := mustResolve(t, f, id, pos.ResolveRetry)
	wantPosted(t, f, e, sale)
	if got := f.stock(); got != "9.0000/0.0000" {
		t.Errorf("stock = %s, want 9 (110 less the 101 sold)", got)
	}
	if e.Revision != rev+1 {
		t.Errorf("revision = %d, want %d", e.Revision, rev+1)
	}
	wantOutcome(t, b.sync(t, raw), sale, pos.OfflineDuplicate, "")
	if n := countOf(t, f.db, `SELECT count(*) FROM pos_transactions WHERE id = $1`, sale.ClientID); n != 1 {
		t.Errorf("%d sale rows, want 1", n)
	}
}

// RULE (ADR 0011 3.4 "Accept"): a price mismatch the manager checked posts at
// the recorded figures (the invoice is the signed total, the tax the
// recorded one, OFFLINE_RATE); a stale snapshot accepted posts the same way.
func TestOfflineResolve_AcceptPostsAtTheRecordedFigures(t *testing.T) {
	f := newFixture(t, testutil.RequireDB(t))
	offlinePolicyOn(t, f)
	b := newOfflineBench(t, f)
	raw, sale := b.build(t, saleSpec{lines: []offlineLine{line(f.productID, "1")}}, func(s *pos.SignedOfflineSale) {
		s.Lines[0].UnitPriceTenThousandths = 10000
		s.Lines[0].LineTotalCents = 100
		s.SubtotalCents, s.TaxCents, s.TotalCents = 100, 9, 109
		s.Tenders[0].AmountCents = 109
		s.QueueTotalCents = 109
	})
	id := *wantOutcome(t, b.sync(t, raw), sale, pos.OfflineRefused, "price_unverified").ExceptionID
	e := mustResolve(t, f, id, pos.ResolveAccept)
	wantPosted(t, f, e, sale)
	if n := countOf(t, f.db, `SELECT count(*) FROM invoices i JOIN pos_transactions t ON t.invoice_id = i.id
		WHERE t.id = $1 AND ROUND(i.total_amount * 100) = 109 AND ROUND(i.tax_amount * 100) = 9 AND i.tax_source = 'OFFLINE_RATE'`,
		sale.ClientID); n != 1 {
		t.Errorf("the accepted sale's invoice is not the recorded 1.09 with 0.09 tax at OFFLINE_RATE")
	}

	mustExec(t, f.db, `UPDATE pos_offline_snapshots SET created_at = NOW() - interval '2 hours' WHERE id = $1`, b.snapID)
	raw2, sale2 := b.build(t, saleSpec{lines: []offlineLine{line(f.productID, "1")}})
	id2 := *wantOutcome(t, b.sync(t, raw2), sale2, pos.OfflineRefused, "snapshot_stale").ExceptionID
	wantPosted(t, f, mustResolve(t, f, id2, pos.ResolveAccept), sale2)
}

// RULE (ADR 0011 3.4 "Late post"): a sale whose drawer was counted before it
// arrived posts on the earliest open business date with no till session
// (the closed one kept in offline_session_id), the counted drawer's figures
// unchanged, and one adjustment moves the cash the count booked as an over
// back to cash (debit over/short, credit cash).
func TestOfflineResolve_LatePostOfACountedDrawer(t *testing.T) {
	f := newFixture(t, testutil.RequireDB(t))
	offlinePolicyOn(t, f)
	f.allowAnyVariance()
	b := newOfflineBench(t, f)
	raw, sale := b.build(t, saleSpec{lines: []offlineLine{line(f.productID, "1")}})
	// the drawer is counted with the sale's cash in it: an over
	if r := closeBenchTill(f, b, sale.TotalCents); r.status != http.StatusOK {
		t.Fatalf("close = %d: %s", r.status, r.raw)
	}
	id := *wantOutcome(t, b.sync(t, raw), sale, pos.OfflineRefused, "session_closed").ExceptionID
	expected := f.scalar(`SELECT expected_by_method::text FROM till_sessions WHERE id = $1`, b.session)

	if _, err := resolveAs(t, f, id, pos.ResolveRetry, offlineManager); err == nil {
		t.Fatalf("a retry posted into a counted drawer")
	}
	e := mustResolve(t, f, id, pos.ResolveLatePost)
	wantPosted(t, f, e, sale)
	if n := countOf(t, f.db, `SELECT count(*) FROM pos_transactions WHERE id = $1 AND till_session_id IS NULL
		AND offline_session_id = $2`, sale.ClientID, b.session); n != 1 {
		t.Errorf("the late post is attached to a session, or does not keep the closed one")
	}
	if n := countOf(t, f.db, `SELECT count(*) FROM invoices i JOIN pos_transactions t ON t.invoice_id = i.id
		WHERE t.id = $1 AND i.invoice_date = $2::date`, sale.ClientID, b.snap.TillSession.BusinessDate); n != 1 {
		t.Errorf("the late post's invoice is not on the session's own (open) business date")
	}
	if got := f.scalar(`SELECT expected_by_method::text FROM till_sessions WHERE id = $1`, b.session); got != expected {
		t.Errorf("the counted drawer moved: %v, was %v", got, expected)
	}
	var debit, credit int64
	if err := f.db.Pool.QueryRow(context.Background(), `SELECT
		COALESCE(ROUND(SUM(l.debit) FILTER (WHERE a.code = '5030') * 100), 0)::bigint,
		COALESCE(ROUND(SUM(l.credit) FILTER (WHERE a.code = '1010') * 100), 0)::bigint
		FROM gl_journal_entries je JOIN gl_journal_lines l ON l.journal_entry_id = je.id JOIN gl_accounts a ON a.id = l.account_id
		WHERE je.source_ref_id = $1 AND je.memo LIKE 'Offline late post%'`, sale.ClientID).Scan(&debit, &credit); err != nil {
		t.Fatal(err)
	}
	if debit != sale.TotalCents || credit != sale.TotalCents {
		t.Errorf("the drawer adjustment = debit over/short %d, credit cash %d, want %d each", debit, credit, sale.TotalCents)
	}
	if bal := f.accountBalance("5030"); bal != 0 {
		t.Errorf("over/short = %d after the count and the late post, want 0", bal)
	}
}

// RULE (ADR 0011 3.4 "Late post"): a sale of a closed branch day posts on the
// next open day; one of a closed fiscal period on the first open date after
// it; the invoice keeps the sale's own day on the sale row's provenance.
func TestOfflineResolve_LatePostOfAClosedDayAndPeriod(t *testing.T) {
	f := newFixture(t, testutil.RequireDB(t))
	offlinePolicyOn(t, f)
	b := newOfflineBench(t, f)
	b.sessionAt(t, "2019-03-14 09:00")
	periodID := uuid.New()
	mustExec(t, f.db, `INSERT INTO gl_fiscal_periods (id, name, start_date, end_date, status)
		VALUES ($1, 'closed for an offline late post', '2019-03-14', '2019-03-14', 'CLOSED')`, periodID)
	t.Cleanup(func() {
		_, _ = f.db.Pool.Exec(context.Background(), `DELETE FROM gl_fiscal_periods WHERE id = $1`, periodID)
		_, _ = f.db.Pool.Exec(context.Background(), `DELETE FROM till_day_closes WHERE branch_id = $1`, f.branchID)
	})
	raw, sale := b.build(t, saleSpec{lines: []offlineLine{line(f.productID, "1")}})
	id := *wantOutcome(t, b.sync(t, raw), sale, pos.OfflineRefused, "period_closed").ExceptionID
	// the day after is a closed branch day too: the post goes past both
	mustExec(t, f.db, `INSERT INTO till_day_closes (branch_id, business_date, deposit_slip_ref, closed_by,
		cash_to_deposit_cents, checks_to_deposit_cents, card_total_cents, totals)
		VALUES ($1, '2019-03-15', 'slip', 'test', 0, 0, 0, '{}'::jsonb)`, f.branchID)
	wantPosted(t, f, mustResolve(t, f, id, pos.ResolveLatePost), sale)
	if n := countOf(t, f.db, `SELECT count(*) FROM invoices i JOIN pos_transactions t ON t.invoice_id = i.id
		WHERE t.id = $1 AND i.invoice_date = '2019-03-16'`, sale.ClientID); n != 1 {
		t.Errorf("the late post is not on the first open date after the closed period and the closed day")
	}

	// a sale of the closed day itself is refused day_closed at sync
	raw2, sale2 := b.build(t, saleSpec{lines: []offlineLine{line(f.productID, "1")}}, func(s *pos.SignedOfflineSale) {})
	mustExec(t, f.db, `DELETE FROM gl_fiscal_periods WHERE id = $1`, periodID)
	mustExec(t, f.db, `INSERT INTO till_day_closes (branch_id, business_date, deposit_slip_ref, closed_by,
		cash_to_deposit_cents, checks_to_deposit_cents, card_total_cents, totals)
		VALUES ($1, '2019-03-14', 'slip', 'test', 0, 0, 0, '{}'::jsonb)`, f.branchID)
	id2 := *wantOutcome(t, b.sync(t, raw2), sale2, pos.OfflineRefused, "day_closed").ExceptionID
	wantPosted(t, f, mustResolve(t, f, id2, pos.ResolveLatePost), sale2)
	if n := countOf(t, f.db, `SELECT count(*) FROM invoices i JOIN pos_transactions t ON t.invoice_id = i.id
		WHERE t.id = $1 AND i.invoice_date = '2019-03-16'`, sale2.ClientID); n != 1 {
		t.Errorf("the day closed sale is not on the next open day")
	}
}

// RULE (ADR 0011 3.4 "Write off"): nothing posts, no stock moves, the row is
// resolved with the manager's reason, and a replay is answered from the row.
func TestOfflineResolve_WriteOffPostsNothing(t *testing.T) {
	f := newFixture(t, testutil.RequireDB(t))
	offlinePolicyOn(t, f)
	b := newOfflineBench(t, f)
	raw, sale := b.build(t, saleSpec{lines: []offlineLine{line(f.productID, "101")}})
	id := *wantOutcome(t, b.sync(t, raw), sale, pos.OfflineRefused, "insufficient_stock").ExceptionID
	e := mustResolve(t, f, id, pos.ResolveWriteOff)
	if e.Status != "resolved" || e.PostedSaleID != nil || e.ResolutionReason == nil || *e.ResolutionReason == "" {
		t.Fatalf("written off row = %s posted %v, want resolved with a reason and no sale", e.Status, e.PostedSaleID)
	}
	wantNothingPosted(t, f, sale)
	if got := f.stock(); got != "100.0000/0.0000" {
		t.Errorf("stock = %s, want 100", got)
	}
	wantOutcome(t, b.sync(t, raw), sale, pos.OfflineRefused, "insufficient_stock")
	if _, err := resolveAs(t, f, id, pos.ResolveRetry, offlineManager); err == nil {
		t.Errorf("a written off sale was retried")
	}
}

// RULE (ADR 0011 3.4 "Post as a separate sale", "Settled replaced cart"): a
// sale whose replaced cart was completed live is refused again on retry; the
// manager posts it as a separate sale, which clears its link with an audit
// row and leaves the live cart as it is.
func TestOfflineResolve_PostAsASeparateSale(t *testing.T) {
	f := newFixture(t, testutil.RequireDB(t))
	offlinePolicyOn(t, f)
	b := newOfflineBench(t, f)
	cart := f.startSale(nil)
	if r := f.addLine(cart, f.productLine("1")); r.status != http.StatusOK {
		t.Fatalf("add line = %d: %s", r.status, r.raw)
	}
	if r := f.completeSale(cart, tender("cash", num(t, f.getSale(t, cart), "total_cents"))); r.status != http.StatusOK {
		t.Fatalf("live complete = %d: %s", r.status, r.raw)
	}
	raw, sale := b.build(t, saleSpec{lines: []offlineLine{line(f.productID, "1")}}, func(s *pos.SignedOfflineSale) {
		s.ReplacesSaleID = &cart
	})
	id := *wantOutcome(t, b.sync(t, raw), sale, pos.OfflineRefused, "replaced_sale_settled").ExceptionID
	_, err := resolveAs(t, f, id, pos.ResolveRetry, offlineManager)
	wantRefused(t, err, "replaced_sale_settled")
	e := mustResolve(t, f, id, pos.ResolvePostSeparate)
	wantPosted(t, f, e, sale)
	if n := countOf(t, f.db, `SELECT count(*) FROM pos_transactions WHERE id = $1 AND replaces_sale_id IS NULL`, sale.ClientID); n != 1 {
		t.Errorf("the separate sale still names the cart")
	}
	if n := countOf(t, f.db, `SELECT count(*) FROM pos_transactions WHERE id = $1 AND status = 'COMPLETED'`, cart); n != 1 {
		t.Errorf("the live cart changed")
	}
	if n := countOf(t, f.db, `SELECT count(*) FROM audit_log WHERE entity_id = $1 AND action = 'pos.offline_sale.link_cleared'
		AND changes->>'replaces_sale_id' = $2`, sale.ClientID, cart); n != 1 {
		t.Errorf("%d link cleared audit rows, want 1", n)
	}
	if got := f.stock(); got != "98.0000/0.0000" {
		t.Errorf("stock = %s, want 98 (the live sale and the separate one)", got)
	}
}

// RULE (ADR 0011 3.4 "Restate the stock figures"): a sale refused because the
// product's stocking unit changed after the snapshot is refused again on
// retry; the restatement converts the line's recorded stock quantity, never
// the sale quantity, through the product's current unit set, posts, moves the
// converted stock and writes an audit row with both figures; when the old
// pair is not exact at scale 4 the engine's rule refuses it 409
// stock_unit_changed and the row stays open.
func TestOfflineResolve_RestateTheStockFigures(t *testing.T) {
	f := newFixture(t, testutil.RequireDB(t))
	offlinePolicyOn(t, f)
	b := newOfflineBench(t, f)
	raw, sale := b.build(t, saleSpec{lines: []offlineLine{line(f.productID, "2")}})
	// the product now stocks in EA, and three PCS make one EA: two PCS are
	// not an exact figure of EA at scale 4
	mustExec(t, f.db, `INSERT INTO product_units (product_id, uom, unit_qty, stock_qty, sell, purchase, price)
		VALUES ($1, 'EA', 1, 1, TRUE, TRUE, TRUE)
		ON CONFLICT (product_id, uom) DO UPDATE SET unit_qty = 1, stock_qty = 1`, f.productID)
	mustExec(t, f.db, `UPDATE products SET uom_primary = 'EA' WHERE id = $1`, f.productID)
	mustExec(t, f.db, `UPDATE product_units SET unit_qty = 3, stock_qty = 1 WHERE product_id = $1 AND uom = 'PCS'`, f.productID)
	id := *wantOutcome(t, b.sync(t, raw), sale, pos.OfflineRefused, "stock_unit_changed").ExceptionID
	if _, err := resolveAs(t, f, id, pos.ResolveRetry, offlineManager); err == nil {
		t.Fatalf("a retry was offered for a changed stocking unit")
	}
	// the conversion is not exact: the engine's rule refuses
	_, err := resolveAs(t, f, id, pos.ResolveRestate, offlineManager)
	wantRefused(t, err, "stock_unit_changed")
	if st, _, _ := exceptionState(t, f, id); st != "open" {
		t.Fatalf("a refused restatement left the row %s", st)
	}
	wantNothingPosted(t, f, sale)

	mustExec(t, f.db, `UPDATE product_units SET unit_qty = 2, stock_qty = 1 WHERE product_id = $1 AND uom = 'PCS'`, f.productID)
	e := mustResolve(t, f, id, pos.ResolveRestate)
	wantPosted(t, f, e, sale)
	if got := f.stock(); got != "99.0000/0.0000" {
		t.Errorf("stock = %s, want 99 (two PCS restated as one EA)", got)
	}
	if n := countOf(t, f.db, `SELECT count(*) FROM pos_line_items WHERE transaction_id = $1 AND stock_uom = 'EA'
		AND stock_quantity = 1 AND quantity = 2`, sale.ClientID); n != 1 {
		t.Errorf("the line does not carry the restated stock figures beside its own quantity")
	}
	if n := countOf(t, f.db, `SELECT count(*) FROM audit_log WHERE entity_id = $1 AND action = 'pos.offline_sale.stock_restated'
		AND changes->>'old_stock_uom' = 'PCS' AND changes->>'new_stock_uom' = 'EA' AND (changes->>'new_stock_quantity')::numeric = 1 AND (changes->>'old_stock_quantity')::numeric = 2`,
		sale.ClientID); n != 1 {
		t.Errorf("%d restatement audit rows with both figures, want 1", n)
	}
}

// RULE (ADR 0011 section 7 "A sequence reused and accepted"): the accepted
// sale posts with its device sequence left null on the sale row and kept on
// its exception row.
func TestOfflineResolve_AReusedSequenceAccepted(t *testing.T) {
	f := newFixture(t, testutil.RequireDB(t))
	offlinePolicyOn(t, f)
	b := newOfflineBench(t, f)
	r1, s1 := b.build(t, saleSpec{lines: []offlineLine{line(f.productID, "1")}})
	wantOutcome(t, b.sync(t, r1), s1, pos.OfflinePosted, "")
	b.seq, b.head = 0, "GENESIS"
	r2, s2 := b.build(t, saleSpec{lines: []offlineLine{line(f.productID, "2")}})
	id := *wantOutcome(t, b.sync(t, r2), s2, pos.OfflineRefused, "sequence_reused").ExceptionID
	wantPosted(t, f, mustResolve(t, f, id, pos.ResolveAccept), s2)
	if n := countOf(t, f.db, `SELECT count(*) FROM pos_transactions WHERE id = $1 AND device_seq IS NULL`, s2.ClientID); n != 1 {
		t.Errorf("the accepted reused sale carries a device sequence")
	}
	if n := countOf(t, f.db, `SELECT count(*) FROM pos_offline_exceptions WHERE id = $1 AND device_seq = 1`, id); n != 1 {
		t.Errorf("the exception row lost its sequence")
	}
	if got := f.stock(); got != "97.0000/0.0000" {
		t.Errorf("stock = %s, want 97", got)
	}
}

// RULE (ADR 0011 3.4, the chain): a held item whose predecessor never came is
// a manager's item; the accept advances the mark over the missing number,
// leaves a sequence_gap item naming it and restarts the chain, and the held
// item is re-offered at once and posts (relinked); the register sells on;
// the missing sale arriving later is late_after_gap, closes the gap as found,
// holds the till close and is retried.
func TestOfflineResolve_AHeldItemAcceptedRestartsTheChain(t *testing.T) {
	f := newFixture(t, testutil.RequireDB(t))
	offlinePolicyOn(t, f)
	b := newOfflineBench(t, f)
	r1, s1 := b.build(t, saleSpec{lines: []offlineLine{line(f.productID, "1")}})
	r2, s2 := b.build(t, saleSpec{lines: []offlineLine{line(f.productID, "1")}})
	held := *wantOutcome(t, b.sync(t, r2), s2, pos.OfflineHeld, "broken_chain").ExceptionID
	wantBlocker(t, closeBenchTill(f, b, 0), http.StatusConflict, "offline_exception_open")

	e := mustResolve(t, f, held, pos.ResolveAccept)
	if e.Status != "relinked" || e.PostedSaleID == nil || e.PostedSaleID.String() != s2.ClientID {
		t.Fatalf("the accepted held row = %s posted %v, want relinked naming the second sale", e.Status, e.PostedSaleID)
	}
	if seq, head := b.deviceMark(t); seq != 2 || head == "RESTART" {
		t.Errorf("mark = %d head %s, want 2 at the second's digest", seq, head)
	}
	if got := b.exceptions(t); len(got) != 2 || got[0] != "1:sequence_gap/resolved" || got[1] != "2:broken_chain/relinked" {
		t.Errorf("rows = %v, want the resolved gap and the relinked held row", got)
	}
	r3, s3 := b.build(t, saleSpec{lines: []offlineLine{line(f.productID, "1")}})
	wantOutcome(t, b.sync(t, r3), s3, pos.OfflinePosted, "")

	late := *wantOutcome(t, b.sync(t, r1), s1, pos.OfflineRefused, "late_after_gap").ExceptionID
	if n := countOf(t, f.db, `SELECT count(*) FROM pos_offline_exceptions WHERE register_id = $1 AND reason_code = 'sequence_gap'
		AND status = 'found'`, b.register); n != 1 {
		t.Errorf("the gap was not closed as found")
	}
	wantBlocker(t, closeBenchTill(f, b, 0), http.StatusConflict, "offline_exception_open")
	wantPosted(t, f, mustResolve(t, f, late, pos.ResolveRetry), s1)
	if got := f.stock(); got != "97.0000/0.0000" {
		t.Errorf("stock = %s, want 97 (three sales)", got)
	}
	if r := closeBenchTill(f, b, s1.TotalCents+s2.TotalCents+s3.TotalCents); r.status != http.StatusOK {
		t.Fatalf("close once resolved = %d: %s", r.status, r.raw)
	}
}

// RULE (ADR 0011 3.4): a held item written off posts nothing; the mark moves
// over the missing number (a sequence_gap item) and over the item itself,
// whose verified digest is the new head, so the next sale posts in order.
func TestOfflineResolve_AHeldItemWrittenOff(t *testing.T) {
	f := newFixture(t, testutil.RequireDB(t))
	offlinePolicyOn(t, f)
	b := newOfflineBench(t, f)
	_, _ = b.build(t, saleSpec{lines: []offlineLine{line(f.productID, "1")}})
	r2, s2 := b.build(t, saleSpec{lines: []offlineLine{line(f.productID, "1")}})
	head2 := b.head
	held := *wantOutcome(t, b.sync(t, r2), s2, pos.OfflineHeld, "broken_chain").ExceptionID
	e := mustResolve(t, f, held, pos.ResolveWriteOff)
	if e.Status != "resolved" || e.PostedSaleID != nil {
		t.Fatalf("written off held row = %s posted %v", e.Status, e.PostedSaleID)
	}
	wantNothingPosted(t, f, s2)
	if seq, head := b.deviceMark(t); seq != 2 || head != head2 {
		t.Errorf("mark = %d, want 2 at the written off item's digest", seq)
	}
	r3, s3 := b.build(t, saleSpec{lines: []offlineLine{line(f.productID, "1")}})
	wantOutcome(t, b.sync(t, r3), s3, pos.OfflinePosted, "")
	if got := f.stock(); got != "99.0000/0.0000" {
		t.Errorf("stock = %s, want 99", got)
	}
}

// RULE (ADR 0011 3.4 "Who resolves it", MONEY-CHECKLIST 13): the sale's own
// cashier never resolves it, compared as people (the roster row the
// manager's email names is the cashier); an action the reason does not take
// is refused; a stale revision is refused; nothing is written by any.
func TestOfflineResolve_WhoMayResolveAndWhat(t *testing.T) {
	f := newFixture(t, testutil.RequireDB(t))
	offlinePolicyOn(t, f)
	b := newOfflineBench(t, f)
	email := "offline-cashier-" + uuid.NewString()[:8] + "@example.com"
	mustExec(t, f.db, `INSERT INTO staff (id, email, full_name, staff_no, role, active) VALUES ($1, $2, 'Offline Cashier', $3, 'owner', TRUE)`,
		b.cashier, email, "OFF-"+uuid.NewString()[:6])
	t.Cleanup(func() { _, _ = f.db.Pool.Exec(context.Background(), `DELETE FROM staff WHERE id = $1`, b.cashier) })
	id, sale := refusedForStock(t, f, b)
	_, rev, _ := exceptionState(t, f, id)

	for _, actor := range []string{b.cashier.String(), email} {
		_, err := resolveAs(t, f, id, pos.ResolveWriteOff, actor)
		var hx *httpx.Error
		if !errors.As(err, &hx) || hx.Status != http.StatusForbidden {
			t.Errorf("the cashier as %s resolved its own sale: %v", actor, err)
		}
	}
	_, err := resolveAs(t, f, id, pos.ResolveAccept, offlineManager)
	wantRefused(t, err, "resolution_not_allowed")
	stale := rev - 1
	_, err = f.service.ResolveOfflineException(context.Background(), id,
		&pos.OfflineResolveIn{Action: pos.ResolveWriteOff, Reason: "stale", Revision: &stale}, "", offlineManager)
	var hx *httpx.Error
	if !errors.As(err, &hx) || (hx.Status != http.StatusPreconditionFailed && hx.Status != http.StatusConflict) {
		t.Errorf("a stale revision = %v, want refused", err)
	}
	if st, r, _ := exceptionState(t, f, id); st != "open" || r != rev {
		t.Errorf("a refused resolution wrote: %s at %d", st, r)
	}
	wantNothingPosted(t, f, sale)
}
