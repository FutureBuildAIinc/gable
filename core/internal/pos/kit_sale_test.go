// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package pos_test

// A kit sale completes and explodes (ADR 0005 sections 2.6 and 14.2 C2-5,
// second review P1-3): the kit line priced whole, one component line per
// component at zero, and the completion's invoice lines keep the parent to
// component relation, so the components stock out and carry the cost of
// goods sold while the kit line carries none.

import (
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
