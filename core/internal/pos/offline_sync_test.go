// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package pos_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"testing"

	"github.com/gablelbm/gable/internal/platform/httpx"
	"github.com/gablelbm/gable/internal/pos"
	"github.com/gablelbm/gable/internal/testutil"
	"github.com/gablelbm/gable/pkg/branchctx"
	"github.com/google/uuid"
)

// RULE (ADR 0011 section 4, section 7 "Policy off"): a branch with no
// offline policy answers 409 money_never_queues and writes nothing.
func TestOfflineSync_PolicyOffAnswersMoneyNeverQueues(t *testing.T) {
	f := newFixture(t, testutil.RequireDB(t))
	offlinePolicyOn(t, f)
	b := newOfflineBench(t, f)
	mustExec(t, f.db, `DELETE FROM pos_offline_policy WHERE branch_id = $1`, f.branchID)
	raw, _ := b.build(t, saleSpec{lines: []offlineLine{line(f.productID, "1")}})
	_, err := b.trySync(raw)
	var hx *httpx.Error
	if !errors.As(err, &hx) || hx.Status != http.StatusConflict || hx.Code != httpx.CodeMoneyNeverQueues {
		t.Fatalf("sync at a branch with no policy = %v, want 409 money_never_queues", err)
	}
	if n := countOf(t, f.db, `SELECT count(*) FROM pos_sync_log WHERE register_id = $1`, b.register); n != 0 {
		t.Errorf("the refused batch wrote %d sync log rows", n)
	}
	if n := b.postedCount(t); n != 0 {
		t.Errorf("the refused batch posted %d sales", n)
	}
}

// RULE (ADR 0011 sections 2, 3 and 7 "Replay twice"): a batch of three
// posts each sale once, through the completion's body: one invoice dated the
// session's business day with tax source OFFLINE_RATE, the stock out once,
// the payment once; replayed, it answers three duplicates and moves nothing;
// the same id with a changed signature answers replay_mismatch.
func TestOfflineSync_ReplayTwicePostsOnce(t *testing.T) {
	f := newFixture(t, testutil.RequireDB(t))
	offlinePolicyOn(t, f)
	b := newOfflineBench(t, f)
	var items []json.RawMessage
	var sales []*pos.SignedOfflineSale
	for _, q := range []string{"1", "2", "3"} {
		raw, s := b.build(t, saleSpec{lines: []offlineLine{line(f.productID, q)}})
		items, sales = append(items, raw), append(sales, s)
	}
	res := b.sync(t, items...)
	for _, s := range sales {
		o := wantOutcome(t, res, s, pos.OfflinePosted, "")
		if o.InvoiceNumber == nil || o.Number == nil {
			t.Errorf("sale %d posted without its numbers", s.DeviceSeq)
		}
	}
	if got := f.stock(); got != "94.0000/0.0000" {
		t.Errorf("stock = %s, want 94 (six out once)", got)
	}
	if n := countOf(t, f.db, `SELECT count(*) FROM invoices i JOIN pos_transactions t ON t.invoice_id = i.id
		WHERE t.register_id = $1 AND i.tax_source = 'OFFLINE_RATE'
		AND i.invoice_date = (SELECT (opened_at AT TIME ZONE 'America/New_York')::date FROM till_sessions WHERE id = $2)`,
		b.register, b.session); n != 3 {
		t.Errorf("%d OFFLINE_RATE invoices on the session's business day, want 3", n)
	}
	if n := countOf(t, f.db, `SELECT count(*) FROM payments p JOIN pos_tenders tn ON tn.payment_id = p.id
		JOIN pos_transactions t ON t.id = tn.transaction_id WHERE t.register_id = $1`, b.register); n != 3 {
		t.Errorf("%d payments, want 3", n)
	}
	if seq, _ := b.deviceMark(t); seq != 3 || res.ConfirmedSeq != 3 {
		t.Errorf("mark = %d, confirmed = %d, want 3 and 3", seq, res.ConfirmedSeq)
	}
	if n := countOf(t, f.db, `SELECT count(*) FROM audit_log WHERE action = 'pos.offline_sale.posted'
		AND entity_id IN (SELECT id FROM pos_transactions WHERE register_id = $1)`, b.register); n != 3 {
		t.Errorf("%d posted audit rows, want 3", n)
	}
	if n := countOf(t, f.db, `SELECT count(*) FROM events_outbox WHERE type = 'pos_transaction.completed'
		AND (data->>'offline')::boolean AND entity_id IN (SELECT id FROM pos_transactions WHERE register_id = $1)`, b.register); n != 3 {
		t.Errorf("%d completion events say offline, want 3", n)
	}
	entries := f.counterEntries(t)

	again := b.sync(t, items...)
	for _, s := range sales {
		wantOutcome(t, again, s, pos.OfflineDuplicate, "")
	}
	if got := f.stock(); got != "94.0000/0.0000" {
		t.Errorf("stock after the replay = %s, want 94", got)
	}
	if got := f.counterEntries(t); got != entries {
		t.Errorf("the replay moved the ledger: %d entries, want %d", got, entries)
	}

	// the first sale again, its signature changed
	var tampered pos.SignedOfflineSale
	if err := json.Unmarshal(items[0], &tampered); err != nil {
		t.Fatal(err)
	}
	tampered.DeviceSig = tampered.DeviceSig[:10] + "AAAA" + tampered.DeviceSig[14:]
	raw, _ := json.Marshal(tampered)
	third := b.sync(t, raw)
	wantOutcome(t, third, &tampered, pos.OfflineRefused, "replay_mismatch")
	if seq, _ := b.deviceMark(t); seq != 3 {
		t.Errorf("the mismatch moved the mark to %d", seq)
	}
	if got := f.counterEntries(t); got != entries {
		t.Errorf("the mismatch moved the ledger")
	}
}

// RULE (ADR 0011 3.4 and 7 "The chain over refused items"): a batch of three
// whose second is refused for stock posts the first and third with their
// links verified, the second's row records the money and its session, and
// the mark ends at the third.
func TestOfflineSync_ChainRunsOverARefusedItem(t *testing.T) {
	f := newFixture(t, testutil.RequireDB(t))
	offlinePolicyOn(t, f)
	b := newOfflineBench(t, f)
	r1, s1 := b.build(t, saleSpec{lines: []offlineLine{line(f.productID, "1")}})
	r2, s2 := b.build(t, saleSpec{lines: []offlineLine{line(f.productID, "500")}})
	r3, s3 := b.build(t, saleSpec{lines: []offlineLine{line(f.productID, "1")}})
	res := b.sync(t, r1, r2, r3)
	wantOutcome(t, res, s1, pos.OfflinePosted, "")
	wantOutcome(t, res, s2, pos.OfflineRefused, "insufficient_stock")
	wantOutcome(t, res, s3, pos.OfflinePosted, "")
	if seq, head := b.deviceMark(t); seq != 3 || head != b.head {
		t.Errorf("mark = %d head %s, want 3 at the third's digest", seq, head)
	}
	var cash, total int64
	var session string
	if err := f.db.Pool.QueryRow(context.Background(), `SELECT cash_cents, total_cents, till_session_id::text
		FROM pos_offline_exceptions WHERE sale_id = $1`, s2.ClientID).Scan(&cash, &total, &session); err != nil {
		t.Fatal(err)
	}
	if cash != s2.TotalCents || total != s2.TotalCents || session != b.session.String() {
		t.Errorf("the refused row holds cash %d total %d session %s, want %d %d %s", cash, total, session,
			s2.TotalCents, s2.TotalCents, b.session)
	}
	if n := countOf(t, f.db, `SELECT count(*) FROM pos_transactions WHERE id = $1`, s2.ClientID); n != 0 {
		t.Errorf("the refused sale left a sale row")
	}
	if n := countOf(t, f.db, `SELECT count(*) FROM audit_log WHERE action = 'pos.offline_sale.refused'
		AND entity_id IN (SELECT id FROM pos_offline_exceptions WHERE sale_id = $1)`, s2.ClientID); n != 1 {
		t.Errorf("%d refused audit rows, want 1", n)
	}
	// replayed after the stock is fixed, the refused item answers refused
	// again: only its exception's resolution posts it (section 2)
	mustExec(t, f.db, `UPDATE inventory SET quantity = 1000 WHERE product_id = $1`, f.productID)
	again := b.sync(t, r2)
	wantOutcome(t, again, s2, pos.OfflineRefused, "insufficient_stock")
	if n := countOf(t, f.db, `SELECT count(*) FROM pos_transactions WHERE id = $1`, s2.ClientID); n != 0 {
		t.Errorf("the replay posted the refused sale behind its exception")
	}
	if n := countOf(t, f.db, `SELECT count(*) FROM pos_sync_log WHERE register_id = $1 AND refused_count = 1`, b.register); n != 2 {
		t.Errorf("%d sync log rows with one refused, want 2", n)
	}
}

// RULE (ADR 0011 6.3, money checklist 15): the sync is held to the
// register's branch: a caller held to another branch is refused by name, and
// nothing of the batch is written.
func TestOfflineSync_AnotherBranchIsRefused(t *testing.T) {
	f := newFixture(t, testutil.RequireDB(t))
	offlinePolicyOn(t, f)
	b := newOfflineBench(t, f)
	raw, sale := b.build(t, saleSpec{lines: []offlineLine{line(f.productID, "1")}})
	elsewhere := uuid.New()
	ctx := branchctx.With(context.Background(), &branchctx.Context{BranchID: &elsewhere, UserSub: "offline-sync-other"})
	_, err := f.service.SyncOffline(ctx, &pos.OfflineBatch{BatchID: uuid.NewString(), RegisterID: b.register,
		DeviceEpoch: 1, Items: []json.RawMessage{raw}}, "offline-sync-other")
	var hx *httpx.Error
	if !errors.As(err, &hx) || hx.Status != http.StatusForbidden {
		t.Fatalf("a sync held to another branch = %v, want 403", err)
	}
	if n := countOf(t, f.db, `SELECT count(*) FROM pos_transactions WHERE id = $1`, sale.ClientID); n != 0 {
		t.Errorf("the refused sync posted the sale")
	}
	if n := countOf(t, f.db, `SELECT count(*) FROM pos_offline_exceptions WHERE register_id = $1`, b.register); n != 0 {
		t.Errorf("the refused sync wrote %d exception rows", n)
	}
	if n := countOf(t, f.db, `SELECT count(*) FROM pos_sync_log WHERE register_id = $1`, b.register); n != 0 {
		t.Errorf("the refused sync wrote %d log rows", n)
	}
}
