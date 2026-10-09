// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package invoice_test

// Cases the independent review of C2-3 found: voids the base cannot have
// exercised (an invoice with no gl_entry_id, a deposit application, a closed
// short order), cumulative billing after a void, and restock cost residues.

import (
	"context"
	"fmt"
	"testing"

	"github.com/gablelbm/gable/internal/testutil"
	"github.com/google/uuid"
)

// An invoice written by the legacy path (a counter account charge, or any
// invoice from before C2-2) has no gl_entry_id but a posted INVOICE entry whose
// source reference is the invoice: the void reverses THAT entry, so the ledger and
// the subledger move together. An invoice with money and no entry at all is refused.
func TestVoidReversesTheLegacyEntryOfAnInvoiceWithNoGLEntryID(t *testing.T) {
	testutil.LockOutboxTables(t)
	db := testutil.RequireDB(t)
	f := newFixture(t, db)
	ctx := context.Background()

	// the counter's account charge path: CreateInvoice + PostInvoiceToLedger in one transaction
	var legacy *legacyDoc
	err := db.RunInTx(ctx, func(ctx context.Context) error {
		var err error
		legacy, err = makeLegacy(ctx, f)
		return err
	})
	if err != nil {
		t.Fatalf("legacy invoice: %v", err)
	}
	id := legacy.id
	// the counter's own invoices are refused (TestVoidOfACounterInvoice...);
	// the case here is the entry lookup, so the invoice is reclassified as an
	// order's, as an invoice from before C2-2 would be.
	mustExec(t, db, `UPDATE invoices SET origin = 'ORDER' WHERE id = $1`, id)
	if n, _ := f.entryLegs(id); n != 1 {
		t.Fatalf("%d entries for the legacy invoice, want the one SyncInvoice posted", n)
	}
	if countOf(t, db, `SELECT count(*) FROM invoices WHERE id = $1 AND gl_entry_id IS NULL`, id) != 1 {
		t.Fatal("the legacy invoice has a gl_entry_id: the case is not the one under test")
	}
	before := f.balance()
	v := f.voidInvoice(id, rev(t, f.getInvoice(id)), "counter charge voided")
	if v.status != 200 {
		t.Fatalf("void = %d: %s", v.status, v.raw)
	}
	if n, legs := f.reversalLegs(id); n != 1 || legs["1020"].credit == 0 {
		t.Errorf("reversal entries %d legs %v, want one reversing the receivable", n, legs)
	}
	if f.balance() != before-legacy.totalCents {
		t.Errorf("balance %d, want %d (the subledger credited back)", f.balance(), before-legacy.totalCents)
	}

	// no entry at all: refused, nothing moves
	orphan, _ := f.invoice("1")
	mustExec(t, db, `UPDATE invoices SET gl_entry_id = NULL WHERE id = $1`, orphan)
	mustExec(t, db, `UPDATE gl_journal_entries SET source = 'MANUAL' WHERE source_ref_id = $1`, orphan)
	bal := f.balance()
	r := f.voidInvoice(orphan, rev(t, f.getInvoice(orphan)), "no entry")
	if _, blockers, _ := errorOf(t, r); r.status != 409 || fmt.Sprint(blockers) != "[no_ledger_entry]" {
		t.Errorf("void with no entry = %d %v, want 409 no_ledger_entry", r.status, blockers)
	}
	if f.balance() != bal {
		t.Error("a refused void moved the subledger")
	}
}

// ADR 0005 section 11: the counter sale void (the sale, its payments and its
// invoice together) is C2-5's. Until then an invoice of the counter (origin
// pos) is refused 409 counter_sale, since voiding the invoice alone would leave
// the sale's stock moves and its pos_transactions row standing.
func TestVoidOfACounterInvoiceIsRefusedUntilTheCounterSaleVoid(t *testing.T) {
	testutil.LockOutboxTables(t)
	db := testutil.RequireDB(t)
	f := newFixture(t, db)
	ctx := context.Background()

	var counter *legacyDoc
	if err := db.RunInTx(ctx, func(ctx context.Context) error {
		var err error
		counter, err = makeLegacy(ctx, f)
		return err
	}); err != nil {
		t.Fatalf("counter invoice: %v", err)
	}
	if got := f.getInvoice(counter.id).body["origin"]; got != "pos" {
		t.Fatalf("origin = %v, want pos: the case is not the one under test", got)
	}
	bal := f.balance()
	r := f.voidInvoice(counter.id, rev(t, f.getInvoice(counter.id)), "void the counter charge")
	if _, blockers, _ := errorOf(t, r); r.status != 409 || fmt.Sprint(blockers) != "[counter_sale]" {
		t.Errorf("void of a counter invoice = %d %v, want 409 counter_sale: %s", r.status, blockers, r.raw)
	}
	if n, _ := f.reversalLegs(counter.id); n != 0 || f.balance() != bal {
		t.Errorf("a refused void left %d reversal entries and moved the balance %d to %d", n, bal, f.balance())
	}
	if str(t, f.getInvoice(counter.id).body, "status") == "void" {
		t.Error("the refused invoice is void")
	}
}

type legacyDoc struct {
	id         string
	totalCents int64
}

// A deposit applied to the invoice is an application: the void is refused until C2-4.
func TestVoidIsRefusedWhileADepositIsApplied(t *testing.T) {
	testutil.LockOutboxTables(t)
	db := testutil.RequireDB(t)
	f := newFixture(t, db)
	invID, _ := f.invoice("1")
	dep := uuid.New()
	mustExec(t, db, `INSERT INTO customer_deposits (id, customer_id, amount, applied_amount, status) VALUES ($1, $2, 10, 10, 'APPLIED')`, dep, f.customerID)
	mustExec(t, db, `INSERT INTO customer_deposit_applications (deposit_id, customer_id, amount, invoice_id) VALUES ($1, $2, 10, $3)`, dep, f.customerID, invID)
	t.Cleanup(func() {
		_, _ = db.Pool.Exec(context.Background(), `DELETE FROM customer_deposit_applications WHERE deposit_id = $1`, dep)
		_, _ = db.Pool.Exec(context.Background(), `DELETE FROM customer_deposits WHERE id = $1`, dep)
	})
	r := f.voidInvoice(invID, rev(t, f.getInvoice(invID)), "deposit applied")
	if _, blockers, _ := errorOf(t, r); r.status != 409 || fmt.Sprint(blockers) != "[has_applications]" {
		t.Errorf("void with a deposit applied = %d %v, want has_applications", r.status, blockers)
	}
}

// A void of an invoice whose order was closed short is refused: the closed
// remainder must not come back to life.
func TestVoidIsRefusedOnAClosedShortOrder(t *testing.T) {
	testutil.LockOutboxTables(t)
	db := testutil.RequireDB(t)
	f := newFixture(t, db)
	orderID, r := f.confirmedOrder(f.pickupLine("10"))
	line := f.do("GET", "/api/v1/orders/"+orderID, nil).body["lines"].([]any)[0].(map[string]any)["id"].(string)
	inv, r2 := f.fulfil(orderID, r, []map[string]any{{"order_line_id": line, "quantity": "6"}})
	c := f.do("POST", "/api/v1/orders/"+orderID+"/transitions", map[string]any{"to": "fulfilled", "revision": r2, "reason": "customer took the rest elsewhere"})
	if c.status != 200 {
		t.Fatalf("close short = %d: %s", c.status, c.raw)
	}
	v := f.voidInvoice(inv, rev(t, f.getInvoice(inv)), "wrong")
	if _, blockers, _ := errorOf(t, v); v.status != 409 || fmt.Sprint(blockers) != "[order_closed_short]" {
		t.Errorf("void on a closed short order = %d %v, want 409 order_closed_short", v.status, blockers)
	}
	if o := f.do("GET", "/api/v1/orders/"+orderID, nil); str(t, o.body, "status") != "fulfilled" {
		t.Errorf("a refused void reopened the order: %v", o.body["status"])
	}
}

// RULE (ADR 0005 2.4 and 6.2): voiding a piece that is not the last leaves the
// live pieces of the line summing to the line's own total to the cent. A line of 3
// at 1.2520 (376) billed 1 (125), 2 (251), the first voided and re-billed for 1 would
// have summed 377 against cumulative pricing from fulfilled quantity.
func TestLivePiecesOfALineSumToItsTotalAfterAVoid(t *testing.T) {
	testutil.LockOutboxTables(t)
	db := testutil.RequireDB(t)
	f := newFixture(t, db)
	product := uuid.New()
	mustExec(t, db, `INSERT INTO products (id, sku, description, uom_primary, base_price, average_unit_cost) VALUES ($1, $2, 'odd price', 'PCS', 1.2520, 1)`, product, "OD-"+product.String()[:8])
	mustExec(t, db, `INSERT INTO inventory (product_id, location_id, location, quantity, allocated) VALUES ($1, $2, 'Y', 50, 0)`, product, f.yardID)
	t.Cleanup(func() {
		_, _ = db.Pool.Exec(context.Background(), `DELETE FROM inventory WHERE product_id = $1`, product)
		_, _ = db.Pool.Exec(context.Background(), `DELETE FROM invoice_lines WHERE product_id = $1`, product)
		_, _ = db.Pool.Exec(context.Background(), `DELETE FROM order_lines WHERE product_id = $1`, product)
	})
	orderID, r := f.confirmedOrder(map[string]any{"product_id": product.String(), "quantity": "3"})
	line := f.do("GET", "/api/v1/orders/"+orderID, nil).body["lines"].([]any)[0].(map[string]any)["id"].(string)
	a, r2 := f.fulfil(orderID, r, []map[string]any{{"order_line_id": line, "quantity": "1"}})
	b, r3 := f.fulfil(orderID, r2, []map[string]any{{"order_line_id": line, "quantity": "2"}})
	_ = r3
	if v := f.voidInvoice(a, rev(t, f.getInvoice(a)), "first piece"); v.status != 200 {
		t.Fatalf("void = %d: %s", v.status, v.raw)
	}
	o := f.do("GET", "/api/v1/orders/"+orderID, nil)
	c, _ := f.fulfil(orderID, rev(t, o), nil)
	total := func(id string) int64 {
		return num(t, f.getInvoice(id).body["lines"].([]any)[0].(map[string]any), "line_total_cents")
	}
	if sum := total(b) + total(c); sum != 376 {
		t.Errorf("live pieces %d + %d = %d, want the line's 376", total(b), total(c), sum)
	}
}

// RULE (ADR 0005 8.4): restocking an invoice line in parts brings back its whole
// cost: a line of 3 that cost 100 restocked one unit at a time returns 100, not 99.
func TestRestockInPartsReturnsTheWholeCost(t *testing.T) {
	testutil.LockOutboxTables(t)
	db := testutil.RequireDB(t)
	f := newFixture(t, db)
	mustExec(t, db, `UPDATE products SET average_unit_cost = 0.3333 WHERE id = $1`, f.productID)
	invID, _ := f.invoice("3")
	line := f.firstLineID(invID)
	if c := num(t, f.getInvoice(invID).body["lines"].([]any)[0].(map[string]any), "cost_cents"); c != 100 {
		t.Fatalf("line cost = %d, want 100", c)
	}
	var back int64
	for i := 0; i < 3; i++ {
		cm := f.createCredit(f.creditBody(invID, returnLine(line, "-1", true)))
		r := f.postCredit(str(t, cm.body, "id"), 1)
		if r.status != 200 {
			t.Fatalf("post = %d: %s", r.status, r.raw)
		}
		back += -num(t, r.body["lines"].([]any)[0].(map[string]any), "cost_cents")
	}
	if back != 100 {
		t.Errorf("cost restored in three parts = %d, want 100", back)
	}
}
