// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package pos_test

// A return reverses COGS at the cost the sale relieved (ADR 0005 section
// 8.4, second review P1-6): a credit memo line that restocks takes its
// source invoice line's unit_cost, not today's average, so the inventory
// account never overstates a returned unit after the average moved.

import (
	"context"
	"net/http"
	"testing"

	"github.com/gablelbm/gable/internal/testutil"
	"github.com/google/uuid"
)

func TestAReturnReversesCOGSAtTheSalesCost(t *testing.T) {
	testutil.LockOutboxTables(t)
	f := newFixture(t, testutil.RequireDB(t))
	saleID, body := f.saleOf("2", tender("cash", 1198))
	lineID := body.body["lines"].([]any)[0].(map[string]any)["id"].(string)
	// the average moves after the sale: the returned unit still comes back
	// at the 3.25 the sale relieved, not today's 9.00
	mustExec(t, f.db, `UPDATE products SET average_unit_cost = 9.00 WHERE id = $1`, f.productID)
	t.Cleanup(func() {
		mustExec(t, f.db, `UPDATE products SET average_unit_cost = 3.25 WHERE id = $1`, f.productID)
	})
	r := f.returnOn(t, saleID, lineID, "1")
	if r.status != http.StatusCreated {
		t.Fatalf("return = %d: %s", r.status, r.raw)
	}
	memoID := str(t, r.body, "credit_memo_id")
	// the memo line names the invoice line it reverses
	invoiceID := str(t, f.getSale(t, saleID), "invoice_id")
	invLine := f.scalar(`SELECT l.id::text FROM invoice_lines l WHERE l.invoice_id = $1 AND l.position = 0`, invoiceID).(string)
	var memoInvLine *string
	if err := f.db.Pool.QueryRow(context.Background(),
		`SELECT invoice_line_id::text FROM credit_memo_lines WHERE credit_memo_id = $1`, memoID).Scan(&memoInvLine); err != nil {
		t.Fatal(err)
	}
	if memoInvLine == nil || *memoInvLine != invLine {
		t.Errorf("the memo line names invoice line %v, want the sale's own %s", memoInvLine, invLine)
	}
	// the entry relieves at 3.25, not 9.00
	_, legs := f.legsFor(memoID)
	if legs["1030"].debit != 325 || legs["5010"].credit != 325 {
		t.Errorf("restock legs = 1030 %+v, 5010 %+v, want 325 each (the cost the sale relieved)", legs["1030"], legs["5010"])
	}
	if got := f.stock(); got != "99.0000/0.0000" {
		t.Errorf("stock = %s, want 99/0", got)
	}
	f.assertARInvariants(t)
}

// RULE (fourth review P1-2): the return's link to its invoice line follows
// the stored invoice line id, never a position. A cart line removed before
// completion leaves the sale's positions gapped while the invoice numbers its
// own lines from zero, so a position match linked (and costed) the wrong
// line, or none: the return relieved COGS at today's average and its memo
// line named no invoice line.
func TestAReturnAfterARemovedLineLinksItsOwnInvoiceLine(t *testing.T) {
	testutil.LockOutboxTables(t)
	f := newFixture(t, testutil.RequireDB(t))
	f.openTill(0)
	// A second product, dearer than the first, so a wrong link shows in the
	// money and not only in the id.
	nails := uuid.New()
	mustExec(t, f.db, `INSERT INTO products (id, sku, description, uom_primary, base_price, average_unit_cost) VALUES ($1, $2, 'nails', 'PCS', 5.50, 12.00)`,
		nails, "NAIL-"+uuid.NewString()[:8])
	t.Cleanup(func() { mustExec(t, f.db, `DELETE FROM products WHERE id = $1`, nails) })

	saleID := f.startSale(&f.customerID)
	add := func(product uuid.UUID) {
		if r := f.addLine(saleID, map[string]any{"product_id": product.String(), "quantity": "2"}); r.status != http.StatusOK {
			t.Fatalf("add line = %d: %s", r.status, r.raw)
		}
	}
	add(f.productID) // position 0
	add(nails)       // position 1, removed below
	add(f.productID) // position 2: the line the return names
	lines := f.getSale(t, saleID)["lines"].([]any)
	studs2 := lines[2].(map[string]any)["id"].(string)
	removed := lines[1].(map[string]any)["id"].(string)
	if r := f.do("DELETE", "/api/v1/pos/transactions/"+saleID+"/items/"+removed, nil,
		"X-Test-Role", "cashier", "X-Test-Sub", mustUUID(t)); r.status != http.StatusOK {
		t.Fatalf("remove line = %d: %s", r.status, r.raw)
	}
	// total: 4 x 5.50 = 2200 + 195 tax = 2395
	if r := f.completeSale(saleID, tender("cash", 2395)); r.status != http.StatusOK {
		t.Fatalf("complete = %d: %s", r.status, r.raw)
	}
	// the average of both products moves after the sale
	mustExec(t, f.db, `UPDATE products SET average_unit_cost = 9.00 WHERE id IN ($1, $2)`, f.productID, nails)
	t.Cleanup(func() {
		mustExec(t, f.db, `UPDATE products SET average_unit_cost = 3.25 WHERE id = $1`, f.productID)
		mustExec(t, f.db, `UPDATE products SET average_unit_cost = 12.00 WHERE id = $1`, nails)
	})
	r := f.returnOn(t, saleID, studs2, "2")
	if r.status != http.StatusCreated {
		t.Fatalf("return = %d: %s", r.status, r.raw)
	}
	memoID := str(t, r.body, "credit_memo_id")
	// the entry relieves at the sale's own cost (never the nails line at
	// 12.00 through a position match, never today's 9.00 average)
	_, legs := f.legsFor(memoID)
	if legs["1030"].debit != 650 || legs["5010"].credit != 650 {
		t.Errorf("restock legs = 1030 %+v, 5010 %+v, want 650 each (2 at the 3.25 the sale relieved)", legs["1030"], legs["5010"])
	}
	// the stored link: the sale line's own invoice line, and the memo line
	// names it
	var invLine string
	var cost int64
	if err := f.db.Pool.QueryRow(context.Background(),
		`SELECT l.invoice_line_id::text, ROUND(il.unit_cost * 10000) FROM pos_line_items l
		 JOIN invoice_lines il ON il.id = l.invoice_line_id WHERE l.id = $1`, studs2).Scan(&invLine, &cost); err != nil {
		t.Fatalf("the sale line carries no stored invoice line link: %v", err)
	}
	var memoInvLine *string
	if err := f.db.Pool.QueryRow(context.Background(),
		`SELECT invoice_line_id::text FROM credit_memo_lines WHERE credit_memo_id = $1`, memoID).Scan(&memoInvLine); err != nil {
		t.Fatal(err)
	}
	if memoInvLine == nil || *memoInvLine != invLine {
		t.Errorf("the memo line names invoice line %v, want the sale line's own %s", memoInvLine, invLine)
	}
	if got := f.stock(); got != "98.0000/0.0000" {
		t.Errorf("stock = %s, want 98/0 (the 2 back on hand)", got)
	}
	f.assertARInvariants(t)
}
