// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package pos_test

import (
	"context"
	"net/http"
	"testing"

	"github.com/gablelbm/gable/internal/pos"
	"github.com/gablelbm/gable/internal/testutil"
	"github.com/google/uuid"
)

// RULE (ADR 0011 3.1, 3.4 and 7 "No re-decision"): after the snapshot the
// account goes on hold, its limit drops, its terms go to due on receipt, the
// customer's and the database's check limits drop, the ID rule tightens, a
// purchase order and a named buyer become required, the price and the tax
// rate change; the sale still posts at its recorded figures and no override
// is spent.
func TestOfflineSync_NoReDecision(t *testing.T) {
	f := newFixture(t, testutil.RequireDB(t))
	offlinePolicyOn(t, f)
	b := newOfflineBench(t, f)
	// 4 at 5.00 under the customer's own key: 2000 and 178 tax
	raw, sale := b.build(t, saleSpec{customer: uuidPtr(f.customerID), lines: []offlineLine{line(f.productID, "4")},
		tenders: []pos.SignedSaleTender{{Method: "account", AmountCents: 1178}, {Method: "check", AmountCents: 1000}}})
	if sale.TotalCents != 2178 {
		t.Fatalf("the bench made %d, want 2178", sale.TotalCents)
	}
	mustExec(t, f.db, `UPDATE customers SET credit_hold = TRUE, credit_limit = 0.01, check_limit = 1.00, po_required = TRUE,
		named_buyer_required = TRUE, payment_terms_id = (SELECT id FROM payment_terms WHERE code = 'DUE_ON_RECEIPT')
		WHERE id = $1`, f.customerID)
	mustExec(t, f.db, `INSERT INTO system_settings (key, value) VALUES ('pos.check_limit_cents', '100')
		ON CONFLICT (key) DO UPDATE SET value = EXCLUDED.value`)
	t.Cleanup(func() {
		_, _ = f.db.Pool.Exec(context.Background(), `DELETE FROM system_settings WHERE key = 'pos.check_limit_cents'`)
	})
	setIDRule(t, f, "0")
	mustExec(t, f.db, `UPDATE products SET base_price = 9.99 WHERE id = $1`, f.productID)
	mustExec(t, f.db, `UPDATE locations SET default_tax_rate = 0.200000 WHERE id = $1`, f.branchID)

	res := b.sync(t, raw)
	o := wantOutcome(t, res, sale, pos.OfflinePosted, "")
	var total, tax int64
	var source string
	if err := f.db.Pool.QueryRow(context.Background(), `SELECT ROUND(total_amount * 100)::bigint, ROUND(tax_amount * 100)::bigint,
		tax_source FROM invoices WHERE id = $1`, *o.InvoiceID).Scan(&total, &tax, &source); err != nil {
		t.Fatal(err)
	}
	if total != 2178 || tax != 178 || source != "OFFLINE_RATE" {
		t.Errorf("invoice total %d tax %d source %s, want 2178 178 OFFLINE_RATE", total, tax, source)
	}
	if n := countOf(t, f.db, `SELECT count(*) FROM pos_account_overrides WHERE sale_id = $1`, sale.ClientID); n != 0 {
		t.Errorf("%d overrides on the offline sale, want none", n)
	}
}

// RULE (ADR 0011 6.2 and 7 "Caps across batches"): two batches under one
// snapshot whose account tenders are each under the room and together over
// it: the sale that crosses the room is cap_exceeded.
func TestOfflineSync_TheAccountRoomAcrossBatches(t *testing.T) {
	f := newFixture(t, testutil.RequireDB(t))
	offlinePolicyOn(t, f)
	b := newOfflineBench(t, f)
	b.snap.Accounts[0].OfflineRoomCents = 1500
	b.issue(t)
	onAccount := saleSpec{customer: uuidPtr(f.customerID), lines: []offlineLine{line(f.productID, "2")},
		tenders: []pos.SignedSaleTender{{Method: "account"}}}
	r1, s1 := b.build(t, onAccount)
	wantOutcome(t, b.sync(t, r1), s1, pos.OfflinePosted, "")
	r2, s2 := b.build(t, onAccount)
	wantOutcome(t, b.sync(t, r2), s2, pos.OfflineRefused, "cap_exceeded")
	var account int64
	if err := f.db.Pool.QueryRow(context.Background(), `SELECT account_cents FROM pos_offline_exceptions WHERE sale_id = $1`,
		s2.ClientID).Scan(&account); err != nil || account != 1089 {
		t.Errorf("the refused row holds %d on account (%v), want 1089", account, err)
	}
}

// RULE (ADR 0011 6.2 and 7 "Room across snapshots"): a sale queued under
// one snapshot and a sale under a newer one that, together, cross the room:
// the second is cap_exceeded once both sync, because the first was recorded
// after the newer snapshot was issued.
func TestOfflineSync_TheAccountRoomAcrossSnapshots(t *testing.T) {
	f := newFixture(t, testutil.RequireDB(t))
	offlinePolicyOn(t, f)
	b := newOfflineBench(t, f)
	b.snap.Accounts[0].OfflineRoomCents = 1500
	b.issue(t)
	onAccount := saleSpec{customer: uuidPtr(f.customerID), lines: []offlineLine{line(f.productID, "2")},
		tenders: []pos.SignedSaleTender{{Method: "account"}}}
	r1, s1 := b.build(t, onAccount)
	// forced past the drain rule: the test issues the newer snapshot itself
	b.issue(t)
	r2, s2 := b.build(t, onAccount)
	res := b.sync(t, r1, r2)
	wantOutcome(t, res, s1, pos.OfflinePosted, "")
	wantOutcome(t, res, s2, pos.OfflineRefused, "cap_exceeded")
}

// RULE (ADR 0011 6.2 and 7 "Two registers on one account"): each register's
// room is its own, so two registers may each take the whole room on one
// account.
func TestOfflineSync_TwoRegistersOnOneAccount(t *testing.T) {
	f := newFixture(t, testutil.RequireDB(t))
	offlinePolicyOn(t, f)
	b1 := newOfflineBench(t, f)
	b2 := newOfflineBench(t, f, "REG-"+uuid.NewString()[:8])
	onAccount := saleSpec{customer: uuidPtr(f.customerID), lines: []offlineLine{line(f.productID, "2")},
		tenders: []pos.SignedSaleTender{{Method: "account"}}}
	for _, b := range []*offlineBench{b1, b2} {
		b.snap.Accounts[0].OfflineRoomCents = 1500
		b.issue(t)
		r, s := b.build(t, onAccount)
		wantOutcome(t, b.sync(t, r), s, pos.OfflinePosted, "")
	}
}

// RULE (ADR 0011 3.3 and 7 "Business day"): a sale made in an earlier
// business day's session posts with that session's date on the invoice and
// on the journal, not the sync's.
func TestOfflineSync_TheSalesOwnBusinessDay(t *testing.T) {
	f := newFixture(t, testutil.RequireDB(t))
	offlinePolicyOn(t, f)
	b := newOfflineBench(t, f)
	b.sessionAt(t, "2019-03-15 09:00")
	raw, sale := b.build(t, saleSpec{lines: []offlineLine{line(f.productID, "1")}})
	o := wantOutcome(t, b.sync(t, raw), sale, pos.OfflinePosted, "")
	var invoiceDate, entryDate string
	if err := f.db.Pool.QueryRow(context.Background(), `SELECT i.invoice_date::text, e.entry_date::text FROM invoices i
		JOIN gl_journal_entries e ON e.source_ref_id = i.id WHERE i.id = $1 LIMIT 1`, *o.InvoiceID).Scan(&invoiceDate, &entryDate); err != nil {
		t.Fatal(err)
	}
	if invoiceDate != "2019-03-15" || entryDate != "2019-03-15" {
		t.Errorf("invoice dated %s, entry dated %s, want the session's day for both", invoiceDate, entryDate)
	}
}

// RULE (ADR 0011 7 "The drawer and the Z report"): an offline sale posted
// into an open session moves that till report's expected cash by its cash
// tender, less the change.
func TestOfflineSync_ThePostedSaleIsInTheDrawer(t *testing.T) {
	f := newFixture(t, testutil.RequireDB(t))
	offlinePolicyOn(t, f)
	b := newOfflineBench(t, f)
	before := offlineDrawerCash(t, f, b)
	raw, sale := b.build(t, saleSpec{lines: []offlineLine{line(f.productID, "1")},
		tenders: []pos.SignedSaleTender{{Method: "cash", AmountCents: 1000}}})
	wantOutcome(t, b.sync(t, raw), sale, pos.OfflinePosted, "")
	if got := offlineDrawerCash(t, f, b) - before; got != sale.TotalCents {
		t.Errorf("expected cash moved by %d, want %d (the sale, the change out of the cash)", got, sale.TotalCents)
	}
}

func offlineDrawerCash(t *testing.T, f *fixture, b *offlineBench) int64 {
	t.Helper()
	r := f.do("GET", "/api/v1/pos/till/"+b.session.String()+"/report", nil, "X-Test-Role", "admin", "X-Test-Sub", uuid.NewString())
	if r.status != http.StatusOK {
		t.Fatalf("till report = %d: %s", r.status, r.raw)
	}
	m, _ := r.body["expected_by_method"].(map[string]any)
	if _, ok := m["CASH"]; !ok {
		return 0
	}
	return num(t, m, "CASH")
}

// RULE (CP-13 OW, ADR 0011 3.4): a sale to an account with open warnings
// that ask to be acknowledged posts (nobody at an offline register could read
// them), and leaves one sold_past_warning row naming every warning, the
// oldest in warning_alert_id, with one audit row per warning; the warnings
// stay open.
func TestOfflineSync_SoldPastAnOpenWarning(t *testing.T) {
	f := newFixture(t, testutil.RequireDB(t))
	offlinePolicyOn(t, f)
	b := newOfflineBench(t, f)
	first, second := uuid.New(), uuid.New()
	mustExec(t, f.db, `INSERT INTO customer_alerts (id, customer_id, kind, text, show_at_counter, acknowledge_required,
		created_by, created_at) VALUES ($1, $3, 'warning', 'check the yard first', TRUE, TRUE, 'office', NOW() - interval '1 day'),
		($2, $3, 'warning', 'ask for the new PO book', TRUE, TRUE, 'office', NOW())`, first, second, f.customerID)
	raw, sale := b.build(t, saleSpec{customer: uuidPtr(f.customerID), lines: []offlineLine{line(f.productID, "1")}})
	wantOutcome(t, b.sync(t, raw), sale, pos.OfflinePosted, "")
	var oldest string
	var ids []string
	if err := f.db.Pool.QueryRow(context.Background(), `SELECT warning_alert_id::text,
		ARRAY(SELECT jsonb_array_elements_text(warning_alert_ids)) FROM pos_offline_exceptions
		WHERE sale_id = $1 AND reason_code = 'sold_past_warning' AND posted_sale_id = $1`, sale.ClientID).Scan(&oldest, &ids); err != nil {
		t.Fatal(err)
	}
	if oldest != first.String() || len(ids) != 2 {
		t.Errorf("the warning row names %s and %v, want the oldest first and both", oldest, ids)
	}
	if n := countOf(t, f.db, `SELECT count(*) FROM audit_log WHERE action = 'pos.offline_sale.sold_past_warning' AND entity_id = $1`,
		sale.ClientID); n != 2 {
		t.Errorf("%d warning audit rows, want 2", n)
	}
	if n := countOf(t, f.db, `SELECT count(*) FROM customer_alerts WHERE customer_id = $1 AND ended_at IS NULL`, f.customerID); n != 2 {
		t.Errorf("%d warnings still open, want 2", n)
	}
}

// RULE (ADR 0011 3.1, 5.4 and 7 "A kit line and a tallied line"): a tallied
// line sells offline, its quantity the tally's linear feet; the server
// stores the tally rows with the line and moves the linear feet; a tally
// whose feet are not its rows' is refused.
func TestOfflineSync_ATalliedLineKeepsItsRows(t *testing.T) {
	random := uuid.New()
	f := newFixture(t, testutil.RequireDB(t), func(f *fixture) {
		// made before the fixture registers its cleanup, so it goes after it
		mustExec(t, f.db, `INSERT INTO products (id, sku, description, uom_primary, base_price, average_unit_cost,
			random_length, board_thickness_in, board_width_in) VALUES ($1, $2, '2x4 random length', 'LF', 1.00, 0.40, TRUE, 2, 4)`,
			random, "RL-"+uuid.NewString()[:8])
		mustExec(t, f.db, `INSERT INTO inventory (product_id, location_id, location, quantity, allocated) VALUES ($1, $2, 'Y', 1000, 0)`,
			random, f.yardID)
		t.Cleanup(func() {
			ctx := context.Background()
			_, _ = f.db.Pool.Exec(ctx, `DELETE FROM inventory WHERE product_id = $1`, random)
			_, _ = f.db.Pool.Exec(ctx, `DELETE FROM product_units WHERE product_id = $1`, random)
			_, _ = f.db.Pool.Exec(ctx, `DELETE FROM products WHERE id = $1`, random)
		})
	})
	offlinePolicyOn(t, f)
	b := newOfflineBench(t, f)
	two, four := "2", "4"
	b.snap.Products = append(b.snap.Products, pos.OfflineSnapshotProduct{ID: random.String(), SKU: "RL", Description: "2x4 random length",
		Taxable: true, RandomLength: true, BoardThicknessIn: &two, BoardWidthIn: &four, StockUOM: "LF", SaleUOM: "LF", PriceUOM: "LF",
		UnitSet: []pos.OfflineSnapshotUnitRow{{UOM: "LF", UnitQty: "1", StockQty: "1", Sell: true, Price: true}}})
	b.snap.PriceSheet = append(b.snap.PriceSheet, pos.OfflineSnapshotPrice{ProductID: random.String(), PriceKey: "walkin",
		PriceUOM: "LF", UOMQty: "1", PriceUOMQty: "1", UnitPriceTenThousandths: 10000, PriceSource: "list"})
	b.issue(t)
	bf := "93.3333"
	tally := func(feet string) func(*pos.SignedOfflineSale) {
		return func(s *pos.SignedOfflineSale) {
			s.Lines[0].Tally = &pos.SignedTally{ThicknessIn: &two, WidthIn: &four, LinearFeet: feet, BoardFeet: &bf,
				Rows: []pos.SignedTallyRow{{Pieces: 10, LengthFT: "8"}, {Pieces: 5, LengthFT: "12"}}}
		}
	}
	r1, s1 := b.build(t, saleSpec{lines: []offlineLine{line(random, "140")}}, tally("140"))
	r2, s2 := b.build(t, saleSpec{lines: []offlineLine{line(random, "150")}}, tally("150"))
	res := b.sync(t, r1, r2)
	wantOutcome(t, res, s1, pos.OfflinePosted, "")
	wantOutcome(t, res, s2, pos.OfflineRefused, "price_unverified")
	if n := countOf(t, f.db, `SELECT count(*) FROM line_tally_rows r JOIN pos_line_items l ON l.id = r.pos_line_item_id
		WHERE l.transaction_id = $1`, s1.ClientID); n != 2 {
		t.Errorf("%d tally rows stored, want 2", n)
	}
	var onHand string
	if err := f.db.Pool.QueryRow(context.Background(), `SELECT quantity::text FROM inventory WHERE product_id = $1`, random).Scan(&onHand); err != nil {
		t.Fatal(err)
	}
	if onHand != "860.0000" {
		t.Errorf("on hand = %s, want 860 (140 LF out)", onHand)
	}
}
