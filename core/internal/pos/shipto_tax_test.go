// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package pos_test

// The ship to part of CP-10 at the counter: a delivered counter sale names
// a ship to and is taxed at its zone's rate (the invoice carrying the
// delivery type, the ship to and the zone it was taxed under), a pickup
// keeps the branch rate, and a ship to without the customer named or
// without the delivery flag is refused.

import (
	"context"
	"net/http"
	"testing"

	"github.com/gablelbm/gable/internal/taxzone"
	"github.com/gablelbm/gable/internal/testutil"
	"github.com/google/uuid"
)

// zoneFor seeds one postal zone through the zone tables and answers its id.
func zoneFor(t *testing.T, f *fixture, code string, parts ...taxzone.RatePart) uuid.UUID {
	t.Helper()
	testutil.LockTaxZones(t)
	// The zone tables start empty: a zone an earlier run left behind could
	// answer the lookup with a lower rate and win the lowest combined rule.
	mustExec(t, f.db, `TRUNCATE tax_zone_boundaries, tax_zone_rates, tax_zones`)
	repo := taxzone.NewRepository(f.db)
	z, err := repo.Create(context.Background(), &taxzone.Draft{
		Country: "US", JurisdictionCode: "Z-" + code, JurisdictionType: "CTY", JurisdictionName: "The county",
		ZoneKind: taxzone.KindPostal, Sourcing: taxzone.SourcingDestination,
		EffectiveFrom: "2020-01-01", DataSource: "dealer", DataVersion: "1",
		Rates:      parts,
		Boundaries: []taxzone.Boundary{{Country: "US", PostalLow: "98100", PostalHigh: "98199"}},
	})
	if err != nil {
		t.Fatalf("create zone: %v", err)
	}
	t.Cleanup(func() { _ = repo.Delete(context.Background(), z.ID) })
	return z.ID
}

func zonePart(name, rate string) taxzone.RatePart {
	return taxzone.RatePart{Name: name, Rate: rate, EffectiveFrom: "2020-01-01"}
}

// RULE (CP-10): a delivered counter sale is sourced to its ship-to's zone,
// and the invoice records the delivery type, the ship to and the zone it was
// taxed under, frozen at the sale.
func TestDeliveredCounterSaleTakesTheShipToZoneRate(t *testing.T) {
	testutil.LockOutboxTables(t)
	f := newFixture(t, testutil.RequireDB(t))
	zoneID := zoneFor(t, f, "98150",
		zonePart(taxzone.PartState, "0.035000"), zonePart(taxzone.PartCity, "0.015000"))

	shipTo := uuid.New()
	mustExec(t, f.db, `INSERT INTO customer_ship_tos (id, customer_id, code, name, line1, city, region, postal_code, country, is_default, is_active)
		VALUES ($1, $2, 'SITE', 'The site', '1 Golden Way', 'Tarnwick', 'WA', '98150', 'US', TRUE, TRUE)`,
		shipTo, f.customerID)
	// The ship to is left behind: the invoice that shipped to it references it.
	f.ensureTill()
	r := f.do("POST", "/api/v1/pos/transactions", map[string]any{
		"register_id": f.register, "customer_id": f.customerID.String(),
		"ship_to_id": shipTo.String(), "delivery_type": "delivery",
	}, "X-Test-Role", "cashier", "X-Test-Sub", mustUUID(t))
	if r.status != http.StatusCreated {
		t.Fatalf("start delivered sale = %d: %s", r.status, r.raw)
	}
	saleID := str(t, r.body, "id")
	if str(t, r.body, "delivery_type") != "delivery" || str(t, r.body, "ship_to_id") != shipTo.String() {
		t.Errorf("delivery_type = %q ship_to_id = %q", r.body["delivery_type"], r.body["ship_to_id"])
	}

	if r := f.addLine(saleID, f.productLine("10")); r.status != http.StatusOK {
		t.Fatalf("add line = %d: %s", r.status, r.raw)
	}
	// 5500 taxable at the zone's 0.05 is 275; the branch's 0.08875 would
	// have been 488, so the arm that answered is the test.
	r = f.completeSale(saleID, tender("cash", 5500+275))
	if r.status != http.StatusOK {
		t.Fatalf("complete delivered sale = %d: %s", r.status, r.raw)
	}
	if got := num(t, r.body, "tax_cents"); got != 275 {
		t.Errorf("tax_cents = %d, want 275 (the zone's 0.05, not the branch's 0.08875)", got)
	}

	// The invoice: the sale's own delivery type and ship to, the zone and
	// its parts frozen beside the rate.
	inv := f.do("GET", "/api/v1/invoices/"+str(t, r.body, "invoice_id"), nil)
	if inv.status != http.StatusOK {
		t.Fatalf("get invoice = %d: %s", inv.status, inv.raw)
	}
	if str(t, inv.body, "delivery_type") != "delivery" || str(t, inv.body, "origin") != "pos" {
		t.Errorf("invoice delivery_type = %q origin = %q", inv.body["delivery_type"], inv.body["origin"])
	}
	var (
		src    string
		zone   *uuid.UUID
		parts  *string
		shipID *uuid.UUID
	)
	if err := f.db.Pool.QueryRow(context.Background(),
		`SELECT tax_source, tax_zone_id, tax_rate_parts::text, ship_to_id FROM invoices WHERE id = $1`,
		str(t, r.body, "invoice_id")).Scan(&src, &zone, &parts, &shipID); err != nil {
		t.Fatalf("read the invoice's zone record: %v", err)
	}
	if src != "ZONE_RATE" || zone == nil || *zone != zoneID {
		t.Errorf("invoice tax = %s %v, want ZONE_RATE %s", src, zone, zoneID)
	}
	if shipID == nil || *shipID != shipTo {
		t.Errorf("invoice ship_to_id = %v, want the sale's ship to", shipID)
	}
	if parts == nil || !strContains(*parts, "STATE") || !strContains(*parts, "0.035000") {
		t.Errorf("invoice tax_rate_parts = %v, want the frozen parts", parts)
	}

	// The sale row keeps the frozen ship to for the record.
	var snapshot *string
	if err := f.db.Pool.QueryRow(context.Background(),
		`SELECT ship_to_snapshot::text FROM pos_transactions WHERE id = $1`, saleID).Scan(&snapshot); err != nil || snapshot == nil {
		t.Errorf("sale ship_to_snapshot = %v %v, want the frozen snapshot", snapshot, err)
	}
}

// RULE (CP-10): a pickup keeps the branch rate, and its invoice says pickup.
func TestPickupCounterSaleKeepsTheBranchRate(t *testing.T) {
	testutil.LockOutboxTables(t)
	f := newFixture(t, testutil.RequireDB(t))
	f.openTill(5000)
	saleID := f.startSale(&f.customerID)
	if r := f.addLine(saleID, f.productLine("10")); r.status != http.StatusOK {
		t.Fatalf("add line = %d: %s", r.status, r.raw)
	}
	// 5500 at the branch's 0.08875 is 488.
	r := f.completeSale(saleID, tender("cash", 5500+488))
	if r.status != http.StatusOK {
		t.Fatalf("complete pickup sale = %d: %s", r.status, r.raw)
	}
	if got := num(t, r.body, "tax_cents"); got != 488 {
		t.Errorf("tax_cents = %d, want 488 (the branch rate)", got)
	}
	var src string
	var zone *uuid.UUID
	if err := f.db.Pool.QueryRow(context.Background(),
		`SELECT tax_source, tax_zone_id FROM invoices WHERE id = $1`, str(t, r.body, "invoice_id")).Scan(&src, &zone); err != nil {
		t.Fatal(err)
	}
	if src != "BRANCH_RATE" || zone != nil {
		t.Errorf("pickup invoice tax = %s %v, want BRANCH_RATE and no zone", src, zone)
	}
}

// RULE (CP-10): a ship to is named on a delivery of a named customer; the
// walk-in customer and a pickup are both refused naming ship_to_id.
func TestShipToOnACounterSaleIsRefusedWithoutCustomerOrDelivery(t *testing.T) {
	f := newFixture(t, testutil.RequireDB(t))
	f.ensureTill()
	shipTo := uuid.New()
	mustExec(t, f.db, `INSERT INTO customer_ship_tos (id, customer_id, code, name, line1, city, region, postal_code, is_default, is_active)
		VALUES ($1, $2, 'SITE', 'The site', '1 Golden Way', 'Tarnwick', 'WA', '98150', TRUE, TRUE)`,
		shipTo, f.customerID)
	r := f.do("POST", "/api/v1/pos/transactions", map[string]any{
		"register_id": f.register, "ship_to_id": shipTo.String(), "delivery_type": "delivery",
	}, "X-Test-Role", "cashier", "X-Test-Sub", mustUUID(t))
	if r.status != http.StatusBadRequest {
		t.Errorf("ship to without customer = %d, want 400", r.status)
	}
	r = f.do("POST", "/api/v1/pos/transactions", map[string]any{
		"register_id": f.register, "customer_id": f.customerID.String(), "ship_to_id": shipTo.String(),
	}, "X-Test-Role", "cashier", "X-Test-Sub", mustUUID(t))
	if r.status != http.StatusBadRequest {
		t.Errorf("ship to without delivery = %d, want 400", r.status)
	}
	r = f.do("POST", "/api/v1/pos/transactions", map[string]any{
		"register_id": f.register, "customer_id": f.customerID.String(),
		"ship_to_id": uuid.NewString(), "delivery_type": "delivery",
	}, "X-Test-Role", "cashier", "X-Test-Sub", mustUUID(t))
	if r.status != http.StatusBadRequest {
		t.Errorf("a ship to of another customer = %d, want 400", r.status)
	}
}

// RULE (CP-10): the ship to rides the customer attach too, so the counter
// flow that names the account can name its delivery in the same breath.
func TestShipToNamedOnTheCustomerAttach(t *testing.T) {
	testutil.LockOutboxTables(t)
	f := newFixture(t, testutil.RequireDB(t))
	zoneFor(t, f, "98150", zonePart(taxzone.PartState, "0.050000"))

	shipTo := uuid.New()
	mustExec(t, f.db, `INSERT INTO customer_ship_tos (id, customer_id, code, name, line1, city, region, postal_code, country, is_default, is_active)
		VALUES ($1, $2, 'SITE', 'The site', '1 Golden Way', 'Tarnwick', 'WA', '98150', 'US', TRUE, TRUE)`,
		shipTo, f.customerID)

	f.ensureTill()
	saleID := f.startSale(nil)
	r := f.do("PUT", "/api/v1/pos/transactions/"+saleID+"/customer", map[string]any{
		"customer_id": f.customerID.String(), "ship_to_id": shipTo.String(), "delivery_type": "delivery",
	}, "X-Test-Role", "cashier", "X-Test-Sub", mustUUID(t))
	if r.status != http.StatusOK {
		t.Fatalf("attach with ship to = %d: %s", r.status, r.raw)
	}
	if str(t, r.body, "delivery_type") != "delivery" || str(t, r.body, "ship_to_id") != shipTo.String() {
		t.Errorf("delivery_type = %q ship_to_id = %q", r.body["delivery_type"], r.body["ship_to_id"])
	}

	// A ship to of another customer is refused naming the field.
	other := uuid.New()
	if r := f.do("PUT", "/api/v1/pos/transactions/"+saleID+"/customer", map[string]any{
		"customer_id": f.customerID.String(), "ship_to_id": other.String(), "delivery_type": "delivery",
	}, "X-Test-Role", "cashier", "X-Test-Sub", mustUUID(t)); r.status != http.StatusBadRequest {
		t.Errorf("attach with a foreign ship to = %d, want 400", r.status)
	}
}


// RULE (CP-10, the GLM read's P2-2): the delivery answer follows the customer
// now standing on the sale. A detach clears it, and a plain re attach resets
// to a pickup: the departed customer's ship to must not stay the sale's
// place of receipt.
func TestDetachClearsTheDepartedCustomersShipTo(t *testing.T) {
	testutil.LockOutboxTables(t)
	f := newFixture(t, testutil.RequireDB(t))
	zoneFor(t, f, "98150", zonePart(taxzone.PartState, "0.050000"))

	shipTo := uuid.New()
	mustExec(t, f.db, `INSERT INTO customer_ship_tos (id, customer_id, code, name, line1, city, region, postal_code, country, is_default, is_active)
		VALUES ($1, $2, 'SITE', 'The site', '1 Golden Way', 'Tarnwick', 'WA', '98150', 'US', TRUE, TRUE)`,
		shipTo, f.customerID)
	t.Cleanup(func() { mustExec(t, f.db, `DELETE FROM customer_ship_tos WHERE id = $1`, shipTo) })

	f.ensureTill()
	saleID := f.startSale(nil)
	attach := func(body map[string]any) resp {
		return f.do("PUT", "/api/v1/pos/transactions/"+saleID+"/customer", body, "X-Test-Role", "cashier", "X-Test-Sub", mustUUID(t))
	}
	r := attach(map[string]any{
		"customer_id": f.customerID.String(), "ship_to_id": shipTo.String(), "delivery_type": "delivery",
	})
	if r.status != http.StatusOK {
		t.Fatalf("attach with ship to = %d: %s", r.status, r.raw)
	}
	if str(t, r.body, "delivery_type") != "delivery" || str(t, r.body, "ship_to_id") != shipTo.String() {
		t.Fatalf("delivery_type = %q ship_to_id = %q", r.body["delivery_type"], r.body["ship_to_id"])
	}

	// The detach: the walk-in sale is a pickup with no ship to.
	r = attach(map[string]any{"customer_id": nil})
	if r.status != http.StatusOK {
		t.Fatalf("detach = %d: %s", r.status, r.raw)
	}
	if str(t, r.body, "delivery_type") != "pickup" || r.body["ship_to_id"] != nil {
		t.Errorf("after the detach delivery_type = %q ship_to_id = %v, want pickup and null", r.body["delivery_type"], r.body["ship_to_id"])
	}

	// A delivered attach again, then a plain re attach naming the account
	// alone: the sale resets to a pickup, the earlier ship to gone.
	if r := attach(map[string]any{
		"customer_id": f.customerID.String(), "ship_to_id": shipTo.String(), "delivery_type": "delivery",
	}); r.status != http.StatusOK {
		t.Fatalf("re attach with ship to = %d: %s", r.status, r.raw)
	}
	r = attach(map[string]any{"customer_id": f.customerID.String()})
	if r.status != http.StatusOK {
		t.Fatalf("plain re attach = %d: %s", r.status, r.raw)
	}
	if str(t, r.body, "delivery_type") != "pickup" || r.body["ship_to_id"] != nil {
		t.Errorf("after a plain re attach delivery_type = %q ship_to_id = %v, want pickup and null", r.body["delivery_type"], r.body["ship_to_id"])
	}
}

func strContains(haystack, needle string) bool {
	return len(haystack) >= len(needle) && (haystack == needle || strIndexOf(haystack, needle) >= 0)
}

func strIndexOf(haystack, needle string) int {
	for i := 0; i+len(needle) <= len(haystack); i++ {
		if haystack[i:i+len(needle)] == needle {
			return i
		}
	}
	return -1
}

// RULE (CP-10, debt read P2-1): naming or changing the ship to on a sale that
// already has lines retaxes the sale, so the total the screen shows is the
// total the completion posts. The same holds for the switch back to a pickup.
func TestNamingTheShipToRetaxesTheLinesAlreadyRung(t *testing.T) {
	testutil.LockOutboxTables(t)
	f := newFixture(t, testutil.RequireDB(t))
	zoneFor(t, f, "98150", zonePart(taxzone.PartState, "0.050000"))

	shipTo := uuid.New()
	mustExec(t, f.db, `INSERT INTO customer_ship_tos (id, customer_id, code, name, line1, city, region, postal_code, country, is_default, is_active)
		VALUES ($1, $2, 'SITE', 'The site', '1 Golden Way', 'Tarnwick', 'WA', '98150', 'US', TRUE, TRUE)`,
		shipTo, f.customerID)
	t.Cleanup(func() { mustExec(t, f.db, `DELETE FROM customer_ship_tos WHERE id = $1`, shipTo) })

	f.ensureTill()
	saleID := f.startSale(nil)
	attach := func(body map[string]any) resp {
		return f.do("PUT", "/api/v1/pos/transactions/"+saleID+"/customer", body, "X-Test-Role", "cashier", "X-Test-Sub", mustUUID(t))
	}
	if r := attach(map[string]any{"customer_id": f.customerID.String()}); r.status != http.StatusOK {
		t.Fatalf("attach = %d: %s", r.status, r.raw)
	}
	if r := f.addLine(saleID, f.productLine("10")); r.status != http.StatusOK {
		t.Fatalf("add line = %d: %s", r.status, r.raw)
	}
	pickupTax := num(t, f.getSale(t, saleID), "tax_cents")

	r := attach(map[string]any{
		"customer_id": f.customerID.String(), "ship_to_id": shipTo.String(), "delivery_type": "delivery",
	})
	if r.status != http.StatusOK {
		t.Fatalf("attach with ship to = %d: %s", r.status, r.raw)
	}
	// 5500 taxable at the zone's 0.05 is 275.
	if got := num(t, r.body, "tax_cents"); got != 275 {
		t.Errorf("tax_cents after naming the ship to = %d, want 275", got)
	}
	if got := num(t, r.body, "total_cents"); got != 5500+275 {
		t.Errorf("total_cents after naming the ship to = %d, want %d", got, 5500+275)
	}

	// Back to a pickup: the branch rate answers again.
	r = attach(map[string]any{"customer_id": f.customerID.String()})
	if r.status != http.StatusOK {
		t.Fatalf("attach back to pickup = %d: %s", r.status, r.raw)
	}
	if got := num(t, r.body, "tax_cents"); got != pickupTax {
		t.Errorf("tax_cents after the switch back = %d, want the pickup figure %d", got, pickupTax)
	}
}
