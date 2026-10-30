// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package pos_test

import (
	"context"
	"net/http"
	"testing"

	"github.com/gablelbm/gable/internal/scale"
	"github.com/gablelbm/gable/internal/testutil"
)

// weighedCart builds a sale holding one line weighed out on a ticket, and
// answers the sale id, the line id and the line's ticket number.
func (f *fixture) weighedCart(t *testing.T, vehicle string) (saleID, lineID, ticketNumber string) {
	t.Helper()
	productID := f.bulkProduct(t, "TON", "30.00", "")
	f.scale.SetWeightStableMilli(30000000, scale.UnitPound)
	in := f.weighIn(vehicle)
	saleID = f.startSale(nil)
	added := f.addLine(saleID, map[string]any{"product_id": productID.String(), "quantity": "1"})
	lineID = str(t, firstLineOf(t, added.body), "id")
	f.scale.SetWeightStableMilli(41000000, scale.UnitPound)
	out := f.do("POST", "/api/v1/pos/scale/weigh-out", map[string]any{
		"sale_id": saleID, "line_id": lineID, "weigh_in_ticket_id": str(t, in.body, "id"),
		"revision": rev(t, added)}, "X-Test-Role", "cashier", "X-Test-Sub", mustUUID(t))
	if out.status != http.StatusOK {
		t.Fatalf("weigh out = %d: %s", out.status, out.raw)
	}
	return saleID, lineID, str(t, out.body["ticket"].(map[string]any), "ticket_number")
}

// replaceOf is the line the counter sends to the replace route: the cart
// line as it stands (its id, product and quantity) with the changes laid over.
func (f *fixture) replaceOf(t *testing.T, saleID string, changes map[string]any) map[string]any {
	t.Helper()
	line := firstLineOf(t, f.getSale(t, saleID))
	out := map[string]any{"id": line["id"], "product_id": line["product_id"], "quantity": line["quantity"]}
	for k, v := range changes {
		out[k] = v
	}
	return out
}

// requireWeightKept checks the sale's only line still carries the weight
// block and that the ticket still names the line.
func (f *fixture) requireWeightKept(t *testing.T, saleID, lineID, ticketNumber, how string) {
	t.Helper()
	line := firstLineOf(t, f.getSale(t, saleID))
	if got := str(t, line, "id"); got != lineID {
		t.Errorf("%s: the line id changed to %q", how, got)
	}
	if got := str(t, line, "weight_source"); got != "scale" {
		t.Errorf("%s: weight_source = %q, want scale", how, got)
	}
	if got := str(t, line, "scale_ticket_number"); got != ticketNumber {
		t.Errorf("%s: scale_ticket_number = %q, want %q", how, got, ticketNumber)
	}
	if got := str(t, line, "weight_net_lb"); got != "11000" {
		t.Errorf("%s: weight_net_lb = %q, want 11000", how, got)
	}
	if got := str(t, line, "quantity"); got != "5.5000" && got != "5.5" {
		t.Errorf("%s: quantity = %q, want the scale's 5.5 tons", how, got)
	}
	var linked string
	if err := f.db.Pool.QueryRow(context.Background(),
		`SELECT COALESCE(sale_line_id::text, '') FROM scale_tickets WHERE ticket_number = $1`, ticketNumber).Scan(&linked); err != nil {
		t.Fatal(err)
	}
	if linked != lineID {
		t.Errorf("%s: the ticket names line %q, want %q", how, linked, lineID)
	}
}

// A price override sent through the replace by id route rebuilds the line;
// the rebuilt line keeps the weight block and the ticket keeps its line.
func TestWeighedLineKeepsItsWeightThroughAPriceOverride(t *testing.T) {
	db := testutil.RequireDB(t)
	f := newFixture(t, db, withScale())
	saleID, lineID, ticket := f.weighedCart(t, "TRUCK-R1")
	r := f.addLine(saleID, f.replaceOf(t, saleID, map[string]any{"unit_price_ten_thousandths": 250000,
		"override_reason": "contractor match"}))
	if r.status != http.StatusOK {
		t.Fatalf("price override on the weighed line = %d: %s", r.status, r.raw)
	}
	f.requireWeightKept(t, saleID, lineID, ticket, "price override")
}

// The quantity, unit and product of a weighed line are the scale's on the
// replace route as they are on the plus button.
func TestWeighedLineRefusesAReplaceOfItsQuantityUnitOrProduct(t *testing.T) {
	db := testutil.RequireDB(t)
	f := newFixture(t, db, withScale())
	saleID, lineID, ticket := f.weighedCart(t, "TRUCK-R2")
	other := f.bulkProduct(t, "TON", "31.00", "")
	for name, body := range map[string]map[string]any{
		"quantity": {"quantity": "1"},
		"uom":      {"uom": "LB"},
		"product":  {"product_id": other.String()},
	} {
		r := f.addLine(saleID, f.replaceOf(t, saleID, body))
		if r.status != http.StatusBadRequest {
			t.Errorf("replace of a weighed line's %s = %d, want 400: %s", name, r.status, r.raw)
		}
	}
	f.requireWeightKept(t, saleID, lineID, ticket, "refused replaces")
}

// Naming the account after the weigh out reprices the line; the weight block
// and the ticket's line survive the reprice.
func TestWeighedLineKeepsItsWeightWhenTheCustomerIsAttached(t *testing.T) {
	db := testutil.RequireDB(t)
	f := newFixture(t, db, withScale())
	saleID, lineID, ticket := f.weighedCart(t, "TRUCK-R3")
	r := f.attach(saleID, map[string]any{"customer_id": f.customerID.String()})
	if r.status != http.StatusOK {
		t.Fatalf("attach = %d: %s", r.status, r.raw)
	}
	f.requireWeightKept(t, saleID, lineID, ticket, "customer attach")
}
