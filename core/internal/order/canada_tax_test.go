// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package order_test

// CP-14 A1 on the order path. A Canadian branch's pickup takes the branch's
// own zone: GST and PST are each rounded on their own and frozen with their
// cents, the source stays branch_rate. A Canadian branch no zone covers is
// refused tax_parts_not_configured. A Canadian delivery takes its
// destination's zone, and a ship-to no zone covers is refused whatever rate
// is typed on it, in words that name the province and the two ways forward
// (item 365). A US branch with a zone in its own area never reads it.

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/gablelbm/gable/internal/invoice"
	"github.com/gablelbm/gable/internal/taxzone"
	"github.com/gablelbm/gable/internal/testutil"
	"github.com/gablelbm/gable/pkg/branchctx"
	"github.com/google/uuid"
)

// seedBCZone makes the BC province zone, GST 5 and PST 7.
func seedBCZone(t *testing.T, f *fixture) uuid.UUID {
	t.Helper()
	if _, err := f.db.Pool.Exec(context.Background(), `TRUNCATE tax_zone_boundaries, tax_zone_rates, tax_zones`); err != nil {
		t.Fatalf("clear the zone tables: %v", err)
	}
	t.Cleanup(func() {
		_, _ = f.db.Pool.Exec(context.Background(), `TRUNCATE tax_zone_boundaries, tax_zone_rates, tax_zones`)
	})
	province := "BC"
	z, err := taxzone.NewRepository(f.db).Create(context.Background(), &taxzone.Draft{
		Country: "CA", JurisdictionCode: "PROV-BC", JurisdictionType: "PROV", JurisdictionName: "British Columbia",
		ProvinceCode: province, ZoneKind: taxzone.KindProvince, Sourcing: taxzone.SourcingBoth,
		EffectiveFrom: "2020-01-01", DataSource: "dealer", DataVersion: "1",
		Rates: []taxzone.RatePart{
			{Name: taxzone.PartGoodsTax, Rate: "0.050000", EffectiveFrom: "2020-01-01"},
			{Name: taxzone.PartProvincialSales, Rate: "0.070000", EffectiveFrom: "2020-01-01"},
		},
	})
	if err != nil {
		t.Fatalf("create the BC zone: %v", err)
	}
	return z.ID
}

// placeBranch puts the fixture's branch at a country, province and postal code.
func placeBranch(t *testing.T, f *fixture, country, state, zip string) {
	t.Helper()
	if _, err := f.db.Pool.Exec(context.Background(),
		`UPDATE locations SET country_code = $2, state = $3, zip = $4 WHERE id = $1`, f.branchID, country, state, zip); err != nil {
		t.Fatal(err)
	}
}

type frozenParts struct {
	Country  string `json:"country"`
	Province string `json:"province"`
	Parts    []struct {
		Name  string `json:"name"`
		Rate  string `json:"rate"`
		Cents *int64 `json:"cents"`
	} `json:"parts"`
}

func orderParts(t *testing.T, f *fixture, id string) (string, frozenParts) {
	t.Helper()
	var src string
	var raw []byte
	if err := f.db.Pool.QueryRow(context.Background(),
		`SELECT tax_source, tax_rate_parts FROM orders WHERE id = $1`, id).Scan(&src, &raw); err != nil {
		t.Fatal(err)
	}
	var p frozenParts
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &p); err != nil {
			t.Fatal(err)
		}
	}
	return src, p
}

func partCents(p frozenParts) map[string]int64 {
	out := map[string]int64{}
	for _, x := range p.Parts {
		if x.Cents != nil {
			out[x.Name] = *x.Cents
		}
	}
	return out
}

func TestCanadianPickupOrderChargesGSTAndPSTEachRoundedOnItsOwn(t *testing.T) {
	f := newFixture(t, testutil.RequireDB(t))
	seedBCZone(t, f)
	placeBranch(t, f, "CA", "BC", "V1X 7K2")

	// 5 PCS at 5.50 is 27.50: GST 1.375 to 1.38, PST 1.925 to 1.93, 3.31,
	// one cent over the combined 12 percent rounded once (3.30).
	r := f.create(f.line("5"))
	if r.status != http.StatusCreated {
		t.Fatalf("create = %d: %s", r.status, r.raw)
	}
	if got := num(t, r.body, "tax_cents"); got != 331 {
		t.Errorf("tax_cents = %d, want 331 (138 + 193)", got)
	}
	if got := num(t, r.body, "total_cents"); got != 2750+331 {
		t.Errorf("total_cents = %d, want %d", got, 2750+331)
	}
	if src := str(t, r.body, "tax_source"); src != "branch_rate" {
		t.Errorf("tax_source = %q, want branch_rate (the place of supply is the branch)", src)
	}
	if pct := str(t, r.body, "tax_rate_percent"); pct != "12" {
		t.Errorf("tax_rate_percent = %q, want 12", pct)
	}
	// The wire's tax_lines: GST then PST, each with its own cents (CP-14 A1).
	lines, _ := r.body["tax_lines"].([]any)
	if len(lines) != 2 {
		t.Fatalf("tax_lines = %v", r.body["tax_lines"])
	}
	gst, pst := lines[0].(map[string]any), lines[1].(map[string]any)
	if gst["label"] != "GST" || gst["rate_percent"] != "5" || num(t, gst, "cents") != 138 ||
		pst["label"] != "PST" || pst["rate_percent"] != "7" || num(t, pst, "cents") != 193 || gst["exempt"] != false {
		t.Errorf("tax_lines = %v", lines)
	}
	if got := f.do("GET", "/api/v1/orders/"+str(t, r.body, "id"), nil); len(got.body["tax_lines"].([]any)) != 2 {
		t.Errorf("the read's tax_lines = %v", got.body["tax_lines"])
	}
	src, parts := orderParts(t, f, str(t, r.body, "id"))
	if src != "BRANCH_RATE" || parts.Country != "CA" || parts.Province != "BC" {
		t.Fatalf("frozen = %s %+v", src, parts)
	}
	if c := partCents(parts); c[taxzone.PartGoodsTax] != 138 || c[taxzone.PartProvincialSales] != 193 {
		t.Errorf("part cents = %v, want GST 138 and PST 193", c)
	}
}

func TestCanadianBranchWithNoZoneIsRefused(t *testing.T) {
	f := newFixture(t, testutil.RequireDB(t))
	seedBCZone(t, f)
	placeBranch(t, f, "CA", "AB", "T2P 1J9")

	r := f.do("POST", "/api/v1/orders", f.createBody())
	if r.status != http.StatusConflict {
		t.Fatalf("create = %d, want 409: %s", r.status, r.raw)
	}
	_, msg, d := errorOf(t, r)
	if len(d) == 0 || d[0]["code"] != taxzone.BlockerPartsNotConfigured {
		t.Errorf("details = %v, want the blocker %s", d, taxzone.BlockerPartsNotConfigured)
	}
	if !strings.Contains(msg, "Alberta") {
		t.Errorf("message = %q, want it to name Alberta", msg)
	}
}

func TestCanadianDeliveryTakesTheDestinationZone(t *testing.T) {
	f := newFixture(t, testutil.RequireDB(t))
	zoneID := seedBCZone(t, f)
	placeBranch(t, f, "CA", "BC", "V1X 7K2")

	shipTo := uuid.New()
	if _, err := f.db.Pool.Exec(context.Background(), `INSERT INTO customer_ship_tos (id, customer_id, code, name, line1, city, region, postal_code, country, tax_rate, is_default, is_active)
		VALUES ($1, $2, 'SITE', 'The site', '1 Lake Way', 'Vernon', 'BC', 'V1T 1A1', 'CA', 0.050000, TRUE, TRUE)`,
		shipTo, f.customerID); err != nil {
		t.Fatal(err)
	}
	r := f.do("POST", "/api/v1/orders", map[string]any{
		"customer_id": f.customerID.String(), "branch_id": f.branchID.String(), "delivery_type": "delivery", "ship_to_id": shipTo.String(),
		"lines": []map[string]any{f.line("10")},
	})
	if r.status != http.StatusCreated {
		t.Fatalf("delivery create = %d: %s", r.status, r.raw)
	}
	if src := str(t, r.body, "tax_source"); src != "zone_rate" {
		t.Errorf("tax_source = %q, want zone_rate", src)
	}
	if got := num(t, r.body, "tax_cents"); got != 660 {
		t.Errorf("tax_cents = %d, want 660 (275 + 385, the zone's parts, not the typed 5 percent)", got)
	}
	var zone *uuid.UUID
	if err := f.db.Pool.QueryRow(context.Background(), `SELECT tax_zone_id FROM orders WHERE id = $1`, str(t, r.body, "id")).Scan(&zone); err != nil {
		t.Fatal(err)
	}
	if zone == nil || *zone != zoneID {
		t.Errorf("tax_zone_id = %v, want %s", zone, zoneID)
	}
	_, parts := orderParts(t, f, str(t, r.body, "id"))
	if c := partCents(parts); c[taxzone.PartGoodsTax] != 275 || c[taxzone.PartProvincialSales] != 385 {
		t.Errorf("part cents = %v", c)
	}
}

func TestCanadianDeliveryToAShipToNoZoneCoversIsRefused(t *testing.T) {
	f := newFixture(t, testutil.RequireDB(t))
	seedBCZone(t, f)
	placeBranch(t, f, "CA", "BC", "V1X 7K2")

	// A typed rate does not rescue it: the place of supply is Alberta, and
	// neither the typed rate nor the branch's BC zone is Alberta's tax.
	shipTo := uuid.New()
	if _, err := f.db.Pool.Exec(context.Background(), `INSERT INTO customer_ship_tos (id, customer_id, code, name, line1, city, region, postal_code, country, tax_rate, is_default, is_active)
		VALUES ($1, $2, 'SITE', 'The site', '1 Prairie Way', 'Calgary', 'AB', 'T2P 1J9', 'CA', 0.050000, TRUE, TRUE)`,
		shipTo, f.customerID); err != nil {
		t.Fatal(err)
	}
	r := f.do("POST", "/api/v1/orders", map[string]any{
		"customer_id": f.customerID.String(), "branch_id": f.branchID.String(), "delivery_type": "delivery", "ship_to_id": shipTo.String(),
		"lines": []map[string]any{f.line("10")},
	})
	if r.status != http.StatusConflict {
		t.Fatalf("delivery create = %d, want 409: %s", r.status, r.raw)
	}
	_, msg, d := errorOf(t, r)
	want := "No tax zone covers Alberta: ask an admin to add one, or take the sale as a pickup."
	if msg != want || len(d) == 0 || d[0]["code"] != taxzone.BlockerPartsNotConfigured || d[0]["message"] != want {
		t.Errorf("refusal = %q %v, want %q with the blocker", msg, d, want)
	}
}

func TestUSBranchNeverReadsItsOwnZone(t *testing.T) {
	f := newFixture(t, testutil.RequireDB(t))
	// A US postal zone over the branch's own postal code, at another rate:
	// a US pickup keeps the branch's rate and the one rounding.
	seedZone(t, f, "0.035000", "0.015000")
	placeBranch(t, f, "US", "WA", "98150")

	r := f.create(f.line("5"))
	if r.status != http.StatusCreated {
		t.Fatalf("create = %d: %s", r.status, r.raw)
	}
	if src := str(t, r.body, "tax_source"); src != "branch_rate" {
		t.Errorf("tax_source = %q, want branch_rate", src)
	}
	if v, ok := r.body["tax_lines"]; !ok || v != nil {
		t.Errorf("a US order's tax_lines = %v (present %v), want null", v, ok)
	}
	// 27.50 at 8.875 percent is 2.440625: 244.
	if got := num(t, r.body, "tax_cents"); got != 244 {
		t.Errorf("tax_cents = %d, want 244", got)
	}
	src, parts := orderParts(t, f, str(t, r.body, "id"))
	if src != "BRANCH_RATE" || len(parts.Parts) != 0 {
		t.Errorf("a US pickup froze parts: %s %+v", src, parts)
	}
}

func TestCanadianFulfilmentInvoiceFreezesTheParts(t *testing.T) {
	testutil.LockOutboxTables(t)
	db := testutil.RequireDB(t)
	f := newFixture(t, db)
	f.serveWith(f.withStock(), f.withMoney())
	f.cleanMoney()
	f.stock(f.productID, "10")
	f.setCost(f.productID, "3.2500")
	seedBCZone(t, f)
	placeBranch(t, f, "CA", "BC", "V1X 7K2")

	r := f.create(f.line("5"))
	id := str(t, r.body, "id")
	r = f.transition(id, 1, "confirmed")
	if str(t, r.body, "status") != "confirmed" {
		t.Fatalf("confirm = %s", r.raw)
	}
	r = f.fulfil(id, revision(t, r), map[string]any{"picked_up_by": "Counter customer"})
	if r.status != 201 {
		t.Fatalf("fulfil = %d: %s", r.status, r.raw)
	}
	inv := invoiceIDOf(t, r)
	read, err := invoice.NewRepository(db).GetInvoice(branchctx.WithSystem(context.Background()), uuid.MustParse(inv))
	if err != nil {
		t.Fatal(err)
	}
	if len(read.TaxLines) != 2 || read.TaxLines[0].Label != "GST" || read.TaxLines[0].Cents != 138 || read.TaxLines[1].Cents != 193 {
		t.Errorf("the invoice's tax_lines = %+v", read.TaxLines)
	}
	var (
		taxCents int64
		src      string
		raw      []byte
	)
	if err := db.Pool.QueryRow(context.Background(),
		`SELECT ROUND(tax_amount * 100)::bigint, tax_source, tax_rate_parts FROM invoices WHERE id = $1`, inv).Scan(&taxCents, &src, &raw); err != nil {
		t.Fatal(err)
	}
	var parts frozenParts
	if err := json.Unmarshal(raw, &parts); err != nil {
		t.Fatalf("invoice parts %q: %v", raw, err)
	}
	if taxCents != 331 || src != "BRANCH_RATE" || partCents(parts)[taxzone.PartGoodsTax] != 138 || partCents(parts)[taxzone.PartProvincialSales] != 193 {
		t.Errorf("invoice tax = %d %s %+v, want 331 BRANCH_RATE with GST 138 and PST 193", taxCents, src, parts)
	}
}
