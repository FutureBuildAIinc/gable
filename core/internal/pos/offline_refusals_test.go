// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package pos_test

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/gablelbm/gable/internal/pos"
	"github.com/gablelbm/gable/internal/testutil"
	"github.com/google/uuid"
)

// sessionAt moves the bench's session to open at a time (a timestamp in the
// branch's zone) and issues a fresh snapshot for it.
func (b *offlineBench) sessionAt(t *testing.T, local string) {
	t.Helper()
	mustExec(t, b.f.db, `UPDATE till_sessions SET opened_at = ($2::timestamp AT TIME ZONE 'America/New_York') WHERE id = $1`,
		b.session, local)
	b.snap = b.baseSnapshot(t)
	b.issue(t)
}

// refusedCase is one refusal row of ADR 0011 section 3.4: what the test does
// to the world or to the signed sale, and the code the queue must carry.
type refusedCase struct {
	name   string
	reason string
	spec   func(f *fixture, b *offlineBench) saleSpec
	before func(t *testing.T, f *fixture, b *offlineBench)
	edit   func(s *pos.SignedOfflineSale)
	// raw, when set, changes the signed item after signing
	raw func(t *testing.T, raw json.RawMessage) json.RawMessage
}

// RULE (ADR 0011 sections 3.4 and 7 "Refused syncs"): every refusal row
// writes one exception row naming the reason, posts nothing (no sale row, no
// invoice, no payment, no stock move), and the item is answered refused.
func TestOfflineSync_EachRefusalGoesToTheQueueAndPostsNothing(t *testing.T) {
	db := testutil.RequireDB(t)
	one := func(f *fixture, b *offlineBench) saleSpec {
		return saleSpec{lines: []offlineLine{line(f.productID, "1")}}
	}
	cases := []refusedCase{
		{name: "stock short", reason: "insufficient_stock", spec: func(f *fixture, b *offlineBench) saleSpec {
			return saleSpec{lines: []offlineLine{line(f.productID, "101")}}
		}},
		{name: "session closed", reason: "session_closed", before: func(t *testing.T, f *fixture, b *offlineBench) {
			mustExec(t, f.db, `UPDATE till_sessions SET status = 'CLOSED', closed_at = NOW() WHERE id = $1`, b.session)
		}},
		{name: "day closed", reason: "day_closed", before: func(t *testing.T, f *fixture, b *offlineBench) {
			mustExec(t, f.db, `INSERT INTO till_day_closes (branch_id, business_date, deposit_slip_ref, closed_by,
				cash_to_deposit_cents, checks_to_deposit_cents, card_total_cents, totals)
				VALUES ($1, $2::date, 'slip', 'test', 0, 0, 0, '{}'::jsonb)`, f.branchID, b.snap.TillSession.BusinessDate)
			t.Cleanup(func() {
				_, _ = f.db.Pool.Exec(context.Background(), `DELETE FROM till_day_closes WHERE branch_id = $1`, f.branchID)
			})
		}},
		{name: "fiscal period closed", reason: "period_closed", before: func(t *testing.T, f *fixture, b *offlineBench) {
			b.sessionAt(t, "2019-03-14 09:00")
			id := uuid.New()
			mustExec(t, f.db, `INSERT INTO gl_fiscal_periods (id, name, start_date, end_date, status)
				VALUES ($1, 'closed for an offline test', '2019-03-14', '2019-03-14', 'CLOSED')`, id)
			t.Cleanup(func() {
				_, _ = f.db.Pool.Exec(context.Background(), `DELETE FROM gl_fiscal_periods WHERE id = $1`, id)
			})
		}},
		{name: "price unverified", reason: "price_unverified", edit: func(s *pos.SignedOfflineSale) {
			// a lower price, every figure after it made to agree
			s.Lines[0].UnitPriceTenThousandths = 10000
			s.Lines[0].LineTotalCents = 100
			s.SubtotalCents, s.TaxCents, s.TotalCents = 100, 9, 109
			s.Tenders[0].AmountCents = 109
			s.QueueTotalCents = 109
		}},
		{name: "tax figure", reason: "tax_unverified", edit: func(s *pos.SignedOfflineSale) {
			s.TaxCents--
			s.TotalCents--
			s.Tenders[0].AmountCents--
		}},
		{name: "tax rate", reason: "tax_unverified", edit: func(s *pos.SignedOfflineSale) {
			s.TaxRate = "0.050000"
		}},
		{name: "taxable flag carried false", reason: "tax_flag_unverified", edit: func(s *pos.SignedOfflineSale) {
			s.Lines[0].Taxable = false
			s.TotalCents -= s.TaxCents
			s.Tenders[0].AmountCents -= s.TaxCents
			s.TaxCents = 0
		}},
		{name: "sale over the cap", reason: "cap_exceeded", before: func(t *testing.T, f *fixture, b *offlineBench) {
			b.snap.Policy.MaxSaleCents = 100
			b.issue(t)
		}},
		{name: "bad signature", reason: "bad_signature", raw: func(t *testing.T, raw json.RawMessage) json.RawMessage {
			var m map[string]any
			if err := json.Unmarshal(raw, &m); err != nil {
				t.Fatal(err)
			}
			m["picked_up_by"] = "someone else"
			out, _ := json.Marshal(m)
			return out
		}},
		{name: "revoked device", reason: "device_revoked", before: func(t *testing.T, f *fixture, b *offlineBench) {
			mustExec(t, f.db, `UPDATE pos_register_devices SET revoked_at = NOW(), revoked_by = 'manager' WHERE id = $1`, b.deviceID)
		}},
		{name: "unknown snapshot", reason: "snapshot_unknown", edit: func(s *pos.SignedOfflineSale) {
			s.SnapshotID = uuid.NewString()
		}},
		{name: "stale snapshot", reason: "snapshot_stale", before: func(t *testing.T, f *fixture, b *offlineBench) {
			mustExec(t, f.db, `UPDATE pos_offline_snapshots SET created_at = NOW() - interval '2 hours' WHERE id = $1`, b.snapID)
		}},
		{name: "card tender", reason: "tender_not_offline", spec: func(f *fixture, b *offlineBench) saleSpec {
			return saleSpec{lines: []offlineLine{line(f.productID, "1")}, tenders: []pos.SignedSaleTender{{Method: "card"}}}
		}},
		{name: "store credit tender", reason: "tender_not_offline", spec: func(f *fixture, b *offlineBench) saleSpec {
			return saleSpec{customer: uuidPtr(f.customerID), lines: []offlineLine{line(f.productID, "1")},
				tenders: []pos.SignedSaleTender{{Method: "store_credit"}}}
		}},
		{name: "check not allowed at the branch", reason: "tender_not_offline", before: func(t *testing.T, f *fixture, b *offlineBench) {
			b.snap.Policy.AllowedTenders = []string{"cash"}
			b.issue(t)
		}, spec: func(f *fixture, b *offlineBench) saleSpec {
			return saleSpec{lines: []offlineLine{line(f.productID, "1")}, tenders: []pos.SignedSaleTender{{Method: "check"}}}
		}},
		{name: "tenders that do not add up", reason: "tender_mismatch", edit: func(s *pos.SignedOfflineSale) {
			s.Tenders[0].AmountCents += 500
		}},
		{name: "two account legs", reason: "one_account_leg", spec: func(f *fixture, b *offlineBench) saleSpec {
			return saleSpec{customer: uuidPtr(f.customerID), lines: []offlineLine{line(f.productID, "2")},
				tenders: []pos.SignedSaleTender{{Method: "account", AmountCents: 500}, {Method: "account", AmountCents: 589}}}
		}},
		{name: "session of another register", reason: "out_of_wall", before: func(t *testing.T, f *fixture, b *offlineBench) {
			other := "REG-" + uuid.NewString()[:8]
			mustExec(t, f.db, `INSERT INTO pos_registers (id, location_id, name, branch_id) VALUES ($1, $2, 'other', $3)`,
				other, f.yardID, f.branchID)
			mustExec(t, f.db, `UPDATE till_sessions SET register_id = $2 WHERE id = $1`, b.session, other)
		}},
		{name: "cashier not the session's", reason: "cashier_mismatch", edit: func(s *pos.SignedOfflineSale) {
			s.CashierID = uuid.NewString()
		}},
		{name: "kit line", reason: "price_unverified", spec: func(f *fixture, b *offlineBench) saleSpec {
			return saleSpec{lines: []offlineLine{line(f.kitID, "1")}}
		}},
		{name: "stocking unit changed since the snapshot", reason: "stock_unit_changed", before: func(t *testing.T, f *fixture, b *offlineBench) {
			mustExec(t, f.db, `UPDATE products SET uom_primary = 'EA' WHERE id = $1`, f.productID)
		}},
		{name: "confirmed mark the server never gave", reason: "queue_mark_unverified", edit: func(s *pos.SignedOfflineSale) {
			s.ConfirmedMark = 7
		}},
		{name: "queue count below the unanswered sales", reason: "queue_mark_unverified", edit: func(s *pos.SignedOfflineSale) {
			s.QueueCount = 0
		}},
		{name: "queue over its count cap", reason: "cap_exceeded", before: func(t *testing.T, f *fixture, b *offlineBench) {
			b.snap.Policy.MaxQueueCount = 3
			b.issue(t)
		}, edit: func(s *pos.SignedOfflineSale) {
			s.QueueCount = 4
		}},
		{name: "clock outside the session", reason: "clock_unverified", edit: func(s *pos.SignedOfflineSale) {
			s.SoldAt = "2001-01-01T00:00:00Z"
		}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f := newFixture(t, db)
			offlinePolicyOn(t, f)
			b := newOfflineBench(t, f)
			if c.before != nil {
				c.before(t, f, b)
			}
			spec := one(f, b)
			if c.spec != nil {
				spec = c.spec(f, b)
			}
			var edits []func(*pos.SignedOfflineSale)
			if c.edit != nil {
				edits = append(edits, c.edit)
			}
			raw, sale := b.build(t, spec, edits...)
			if c.raw != nil {
				raw = c.raw(t, raw)
			}
			entries := f.counterEntries(t)
			stock := f.stock()
			res := b.sync(t, raw)
			o := wantOutcome(t, res, sale, pos.OfflineRefused, c.reason)
			if o.ExceptionID == nil {
				t.Fatal("the refusal names no exception row")
			}
			if n := countOf(t, f.db, `SELECT count(*) FROM pos_offline_exceptions WHERE sale_id = $1 AND reason_code = $2
				AND status = 'open'`, sale.ClientID, c.reason); n != 1 {
				t.Errorf("%d open %s rows, want 1", n, c.reason)
			}
			if n := countOf(t, f.db, `SELECT count(*) FROM pos_transactions WHERE id = $1`, sale.ClientID); n != 0 {
				t.Errorf("the refused sale left a sale row")
			}
			if got := f.counterEntries(t); got != entries {
				t.Errorf("the refusal moved the ledger: %d entries, want %d", got, entries)
			}
			if got := f.stock(); got != stock {
				t.Errorf("the refusal moved the stock: %s, want %s", got, stock)
			}
			if n := countOf(t, f.db, `SELECT count(*) FROM pos_tenders WHERE transaction_id = $1`, sale.ClientID); n != 0 {
				t.Errorf("the refusal wrote %d tenders", n)
			}
			// a verified in order refusal advances the mark; a bad copy does not
			seq, _ := b.deviceMark(t)
			want := int64(1)
			if c.reason == "bad_signature" {
				want = 0
			}
			if seq != want {
				t.Errorf("mark = %d, want %d", seq, want)
			}
		})
	}
}

// RULE (ADR 0011 5.4 and 7 "The price key is the customer's"): a sale for
// account A priced under account B's key, and a walk-in sale priced under an
// account key, are refused price_unverified and post nothing; a line the
// sheet carries no row of the account for prices at its level's row, marked
// level price, and posts.
func TestOfflineSync_ThePriceKeyIsTheCustomers(t *testing.T) {
	f := newFixture(t, testutil.RequireDB(t))
	offlinePolicyOn(t, f)
	b := newOfflineBench(t, f)
	other := uuid.New()
	mustExec(t, f.db, `INSERT INTO customers (id, name, account_number, primary_branch_id) VALUES ($1, 'Other Co', $2, $3)`,
		other, "OFF-"+uuid.NewString()[:8], f.branchID)
	t.Cleanup(func() { _, _ = f.db.Pool.Exec(context.Background(), `DELETE FROM customers WHERE id = $1`, other) })
	// B's own key prices lower than A's
	b.snap.Accounts = append(b.snap.Accounts, pos.OfflineSnapshotAccount{CustomerID: other.String(), AccountNumber: "OFF-2",
		Name: "Other Co", Currency: "USD", CreditLimitCents: 10000000, OfflineRoomCents: 10000000, PriceKey: "customer:" + other.String()})
	b.snap.PriceSheet = append(b.snap.PriceSheet, pos.OfflineSnapshotPrice{ProductID: f.productID.String(),
		PriceKey: "customer:" + other.String(), PriceUOM: "PCS", UOMQty: "1", PriceUOMQty: "1", UnitPriceTenThousandths: 30000, PriceSource: "contract"})
	b.issue(t)

	underB := func(s *pos.SignedOfflineSale) {
		s.Lines[0].PriceKey = "customer:" + other.String()
		s.Lines[0].UnitPriceTenThousandths = 30000
		s.Lines[0].LineTotalCents = 300
		s.SubtotalCents, s.TaxCents, s.TotalCents = 300, 27, 327
		s.Tenders[0].AmountCents = 327
	}
	rA, sA := b.build(t, saleSpec{customer: uuidPtr(f.customerID), lines: []offlineLine{line(f.productID, "1")}}, underB)
	res := b.sync(t, rA)
	wantOutcome(t, res, sA, pos.OfflineRefused, "price_unverified")

	walkInUnderA := func(s *pos.SignedOfflineSale) {
		s.Lines[0].PriceKey = "customer:" + f.customerID.String()
		s.Lines[0].UnitPriceTenThousandths = 50000
		s.Lines[0].LineTotalCents = 500
		s.SubtotalCents, s.TaxCents, s.TotalCents = 500, 44, 544
		s.Tenders[0].AmountCents = 544
	}
	rW, sW := b.build(t, saleSpec{lines: []offlineLine{line(f.productID, "1")}}, walkInUnderA)
	res = b.sync(t, rW)
	wantOutcome(t, res, sW, pos.OfflineRefused, "price_unverified")

	// the level fallback: B has no row of its own for the product once its
	// row goes, and B's level row prices it
	level := "level:other"
	b.snap.PriceSheet = b.snap.PriceSheet[:len(b.snap.PriceSheet)-1]
	b.snap.Accounts[1].LevelKey = &level
	b.snap.PriceSheet = append(b.snap.PriceSheet, pos.OfflineSnapshotPrice{ProductID: f.productID.String(),
		PriceKey: level, PriceUOM: "PCS", UOMQty: "1", PriceUOMQty: "1", UnitPriceTenThousandths: 48000, PriceSource: "level"})
	b.issue(t)
	rL, sL := b.build(t, saleSpec{customer: uuidPtr(other), lines: []offlineLine{line(f.productID, "1")}})
	if !sL.Lines[0].LevelFallback || sL.Lines[0].PriceKey != level {
		t.Fatalf("the bench did not price at the level: %+v", sL.Lines[0])
	}
	// a device that marked the fallback line as the account's own is refused
	rX, sX := b.build(t, saleSpec{customer: uuidPtr(other), lines: []offlineLine{line(f.productID, "1")}},
		func(s *pos.SignedOfflineSale) { s.Lines[0].LevelFallback = false })
	res = b.sync(t, rL, rX)
	wantOutcome(t, res, sL, pos.OfflinePosted, "")
	wantOutcome(t, res, sX, pos.OfflineRefused, "price_unverified")
	var basis string
	if err := f.db.Pool.QueryRow(context.Background(), `SELECT price_basis FROM pos_line_items WHERE transaction_id = $1`,
		sL.ClientID).Scan(&basis); err != nil {
		t.Fatal(err)
	}
	if basis != "level_fallback" {
		t.Errorf("the fallback line's basis = %q, want level_fallback", basis)
	}
}

// RULE (ADR 0011 5.4 and 7 "The stock figures"): a line sold in a unit other
// than the stocking unit issues the converted stock quantity and books its
// cost on that figure; a device whose stock figures are not the snapshot's
// conversion is refused.
func TestOfflineSync_StockMovesByTheConvertedFigure(t *testing.T) {
	f := newFixture(t, testutil.RequireDB(t))
	offlinePolicyOn(t, f)
	b := newOfflineBench(t, f)
	r1, s1 := b.build(t, saleSpec{lines: []offlineLine{{product: f.productID, qty: "2", uom: "BUNDLE"}}})
	if s1.Lines[0].StockQuantity != "48" {
		t.Fatalf("the bench converted 2 BUNDLE to %s PCS, want 48", s1.Lines[0].StockQuantity)
	}
	r2, s2 := b.build(t, saleSpec{lines: []offlineLine{{product: f.productID, qty: "1", uom: "BUNDLE"}}},
		func(s *pos.SignedOfflineSale) { s.Lines[0].StockQuantity = "1" })
	res := b.sync(t, r1, r2)
	wantOutcome(t, res, s1, pos.OfflinePosted, "")
	wantOutcome(t, res, s2, pos.OfflineRefused, "stock_unverified")
	if got := f.stock(); got != "52.0000/0.0000" {
		t.Errorf("stock = %s, want 52 (48 PCS out for 2 BUNDLE)", got)
	}
	var cost int64
	if err := f.db.Pool.QueryRow(context.Background(), `SELECT COALESCE(SUM(ROUND(il.cost * 100)), 0)::bigint FROM invoice_lines il
		JOIN pos_transactions t ON t.invoice_id = il.invoice_id WHERE t.id = $1`, s1.ClientID).Scan(&cost); err != nil {
		t.Fatal(err)
	}
	if cost != 48*325 {
		t.Errorf("cost of goods = %d, want %d (48 at 3.25)", cost, 48*325)
	}
}
