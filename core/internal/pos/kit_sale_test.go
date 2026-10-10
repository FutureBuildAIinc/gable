// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package pos_test

// A kit sale completes and explodes (ADR 0005 sections 2.6 and 14.2 C2-5,
// second review P1-3): the kit line priced whole, one component line per
// component at zero, and the completion's invoice lines keep the parent to
// component relation, so the components stock out and carry the cost of
// goods sold while the kit line carries none.

import (
	"context"
	"net/http"
	"testing"

	"github.com/gablelbm/gable/internal/testutil"
)

func TestAKitSaleCompletesAndExplodes(t *testing.T) {
	testutil.LockOutboxTables(t)
	f := newFixture(t, testutil.RequireDB(t))
	saleID := f.startSale(&f.customerID)
	if r := f.addLine(saleID, map[string]any{"product_id": f.kitID.String(), "quantity": "2"}); r.status != http.StatusOK {
		t.Fatalf("kit line = %d: %s", r.status, r.raw)
	}
	// the kit at 11.00 x2 = 2200, tax 195, total 2395
	r := f.completeSale(saleID, tender("cash", 2395))
	if r.status != http.StatusOK {
		t.Fatalf("kit sale complete = %d, want 200: %s", r.status, r.raw)
	}
	invoiceID := str(t, r.body, "invoice_id")
	inv := f.do("GET", "/api/v1/invoices/"+invoiceID, nil)
	if inv.status != http.StatusOK {
		t.Fatalf("get invoice = %d: %s", inv.status, inv.raw)
	}
	lines := inv.body["lines"].([]any)
	if len(lines) != 2 {
		t.Fatalf("%d invoice lines, want 2 (the kit line and its component)", len(lines))
	}
	kit := lines[0].(map[string]any)
	comp := lines[1].(map[string]any)
	if str(t, kit, "line_type") != "kit" || str(t, comp, "line_type") != "component" {
		t.Fatalf("invoice line types = %s, %s, want kit and component", kit["line_type"], comp["line_type"])
	}
	if comp["parent_line_id"] == nil || str(t, comp, "parent_line_id") != str(t, kit, "id") {
		t.Errorf("the component's parent is not the kit's own invoice line")
	}
	// the kit bills whole (2200), the component nothing
	if got := num(t, kit, "line_total_cents"); got != 2200 {
		t.Errorf("kit line_total_cents = %d, want 2200", got)
	}
	if got := num(t, comp, "line_total_cents"); got != 0 {
		t.Errorf("component line_total_cents = %d, want 0", got)
	}
	// COGS from the components only: 4 units at 3.25 = 1300, the kit at none
	if got := num(t, kit, "cost_cents"); got != 0 {
		t.Errorf("kit cost_cents = %d, want 0 (the kit line carries no cost)", got)
	}
	if got := num(t, comp, "cost_cents"); got != 1300 {
		t.Errorf("component cost_cents = %d, want 1300 (4 x 3.25)", got)
	}
	// the stock that left is the component's: 100 - 4
	if got := f.stock(); got != "96.0000/0.0000" {
		t.Errorf("stock = %s, want 96/0 (the components left, not the kit)", got)
	}
	_, legs := f.legsFor(invoiceID)
	if legs["5010"].debit != 1300 || legs["1030"].credit != 1300 {
		t.Errorf("COGS legs = %+v %+v, want 1300 each from the components", legs["5010"], legs["1030"])
	}
	if got := f.accountBalance("1010"); got != 2395 {
		t.Errorf("cash balance = %d, want 2395", got)
	}
	f.assertARInvariants(t)
}

// RULE (fourth review P1-1): a returned kit line restocks its components at
// the cost the sale relieved for them; the kit product itself has no
// inventory row and never restocks. A component line of a kit sale carries
// no price of its own, so its restock is refused: the kit line carries the
// return.
func TestAKitReturnRestocksItsComponentsAtTheirCost(t *testing.T) {
	testutil.LockOutboxTables(t)
	f := newFixture(t, testutil.RequireDB(t))
	f.openTill(0)
	saleID := f.startSale(&f.customerID)
	if r := f.addLine(saleID, map[string]any{"product_id": f.kitID.String(), "quantity": "2"}); r.status != http.StatusOK {
		t.Fatalf("kit line = %d: %s", r.status, r.raw)
	}
	if r := f.completeSale(saleID, tender("cash", 2395)); r.status != http.StatusOK {
		t.Fatalf("kit sale complete = %d: %s", r.status, r.raw)
	}
	// the average moves after the sale: the components still come back at
	// the 3.25 the sale relieved, not today's 9.00
	mustExec(t, f.db, `UPDATE products SET average_unit_cost = 9.00 WHERE id = $1`, f.productID)
	t.Cleanup(func() {
		mustExec(t, f.db, `UPDATE products SET average_unit_cost = 3.25 WHERE id = $1`, f.productID)
	})

	// the component line's restock is refused: the kit line carries it
	sale := f.getSale(t, saleID)
	lines := sale["lines"].([]any)
	kitLine := lines[0].(map[string]any)
	compLine := lines[1].(map[string]any)
	if str(t, compLine, "line_type") != "component" {
		t.Fatalf("second line = %s, want the component", str(t, compLine, "line_type"))
	}

	// one kit of two components back: the refund is the kit's own price
	r := f.returnOn(t, saleID, str(t, kitLine, "id"), "1")
	if r.status != http.StatusCreated {
		t.Fatalf("kit return = %d, want 201: %s", r.status, r.raw)
	}
	rComp := f.returnOn(t, saleID, str(t, compLine, "id"), "1")
	if rComp.status != http.StatusBadRequest {
		t.Fatalf("component restock = %d, want 400: %s", rComp.status, rComp.raw)
	}
	_, _, fields := errorOf(t, rComp)
	if len(fields) == 0 || fields[0] != "lines[0].restock" {
		t.Errorf("fields = %v, want lines[0].restock", fields)
	}
	for key, want := range map[string]int64{"subtotal_cents": -1100, "tax_cents": -98, "total_cents": -1198} {
		if got := num(t, r.body, key); got != want {
			t.Errorf("kit return %s = %d, want %d", key, got, want)
		}
	}
	// the components are back on hand (2 of the 4 that left), at their own
	// cost: DR 1030 / CR 5010 650 (2 x 3.25), never today's 9.00
	if got := f.stock(); got != "98.0000/0.0000" {
		t.Errorf("stock = %s, want 98/0 (the kit's components back)", got)
	}
	memoID := str(t, r.body, "credit_memo_id")
	_, legs := f.legsFor(memoID)
	if legs["1030"].debit != 650 || legs["5010"].credit != 650 {
		t.Errorf("restock legs = 1030 %+v, 5010 %+v, want 650 each (the components at the cost the sale relieved)", legs["1030"], legs["5010"])
	}
	// the kit line's memo line names the kit's own invoice line
	kitInvLine := f.scalar(`SELECT invoice_line_id::text FROM pos_line_items WHERE id = $1`, str(t, kitLine, "id")).(string)
	var memoInvLine *string
	if err := f.db.Pool.QueryRow(context.Background(),
		`SELECT invoice_line_id::text FROM credit_memo_lines WHERE credit_memo_id = $1`, memoID).Scan(&memoInvLine); err != nil {
		t.Fatal(err)
	}
	if memoInvLine == nil || *memoInvLine != kitInvLine {
		t.Errorf("the memo line names invoice line %v, want the kit's own %s", memoInvLine, kitInvLine)
	}
	if got := f.accountBalance("1010"); got != 2395-1198 {
		t.Errorf("cash balance = %d, want %d (the sale in, the kit return out)", got, 2395-1198)
	}
	f.assertARInvariants(t)
}
