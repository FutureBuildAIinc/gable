// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package pos_test

// CP-14 A1 at the counter. A sale on a Canadian branch is priced GST and PST
// each rounded on its own, in the running estimate and at the completion
// alike, and its invoice freezes the parts with their cents; the source
// stays BRANCH_RATE. A Canadian branch no zone covers refuses the completion
// with tax_parts_not_configured. A Canadian delivery to a ship-to no zone
// covers is refused in the clerk's words (item 365).

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/gablelbm/gable/internal/pos"
	"github.com/gablelbm/gable/internal/taxzone"
	"github.com/gablelbm/gable/internal/testutil"
	"github.com/google/uuid"
)

func (f *fixture) seedBCZone() {
	f.t.Helper()
	mustExec(f.t, f.db, `TRUNCATE tax_zone_boundaries, tax_zone_rates, tax_zones`)
	f.t.Cleanup(func() { mustExec(f.t, f.db, `TRUNCATE tax_zone_boundaries, tax_zone_rates, tax_zones`) })
	if _, err := taxzone.NewRepository(f.db).Create(context.Background(), &taxzone.Draft{
		Country: "CA", JurisdictionCode: "PROV-BC", JurisdictionType: "PROV", JurisdictionName: "British Columbia",
		ProvinceCode: "BC", ZoneKind: taxzone.KindProvince, Sourcing: taxzone.SourcingBoth,
		EffectiveFrom: "2020-01-01", DataSource: "dealer", DataVersion: "1",
		Rates: []taxzone.RatePart{
			{Name: taxzone.PartGoodsTax, Rate: "0.050000", EffectiveFrom: "2020-01-01"},
			{Name: taxzone.PartProvincialSales, Rate: "0.070000", EffectiveFrom: "2020-01-01"},
		},
	}); err != nil {
		f.t.Fatalf("create the BC zone: %v", err)
	}
}

func (f *fixture) placeBranch(country, state, zip string) {
	f.t.Helper()
	mustExec(f.t, f.db, `UPDATE locations SET country_code = $2, state = $3, zip = $4 WHERE id = $1`, f.branchID, country, state, zip)
}

func TestCanadianCounterSaleChargesGSTAndPSTAndFreezesTheParts(t *testing.T) {
	testutil.LockOutboxTables(t)
	f := newCounterFixture(t, testutil.RequireDB(t))
	f.seedBCZone()
	f.placeBranch("CA", "BC", "V1X 7K2")

	// 5 at 5.50 is 27.50: GST 138 and PST 193, 331, against 330 combined.
	saleID := f.startSale(nil)
	if r := f.addLine(saleID, f.productLine("5")); r.status != http.StatusOK {
		t.Fatalf("add line = %d: %s", r.status, r.raw)
	}
	if got := num(t, f.getSale(t, saleID), "tax_cents"); got != 331 {
		t.Errorf("running tax = %d, want 331", got)
	}
	r := f.completeSale(saleID, tender("cash", 2750+331))
	if r.status != http.StatusOK {
		t.Fatalf("complete = %d: %s", r.status, r.raw)
	}
	if lines, _ := f.getSale(t, saleID)["tax_lines"].([]any); len(lines) != 2 {
		t.Errorf("the completed sale's tax_lines = %v", f.getSale(t, saleID)["tax_lines"])
	}
	var (
		taxCents int64
		src      string
		raw      []byte
	)
	if err := f.db.Pool.QueryRow(context.Background(), `
		SELECT ROUND(i.tax_amount * 100)::bigint, i.tax_source, i.tax_rate_parts
		FROM pos_transactions s JOIN invoices i ON i.id = s.invoice_id WHERE s.id = $1`, saleID).Scan(&taxCents, &src, &raw); err != nil {
		t.Fatal(err)
	}
	var snap taxzone.Snapshot
	if err := json.Unmarshal(raw, &snap); err != nil {
		t.Fatalf("parts %q: %v", raw, err)
	}
	cents := map[string]int64{}
	for _, p := range snap.Parts {
		if p.Cents != nil {
			cents[p.Name] = *p.Cents
		}
	}
	if taxCents != 331 || src != "BRANCH_RATE" || snap.Province != "BC" ||
		cents[taxzone.PartGoodsTax] != 138 || cents[taxzone.PartProvincialSales] != 193 {
		t.Errorf("invoice = %d %s %+v, want 331 BRANCH_RATE with GST 138 and PST 193", taxCents, src, snap)
	}
}

func TestCanadianCounterWithNoZoneRefusesTheSale(t *testing.T) {
	testutil.LockOutboxTables(t)
	f := newCounterFixture(t, testutil.RequireDB(t))
	f.seedBCZone()
	f.placeBranch("CA", "AB", "T2P 1J9")

	saleID := f.startSale(nil)
	if r := f.addLine(saleID, f.productLine("5")); r.status != http.StatusOK {
		t.Fatalf("add line = %d: %s", r.status, r.raw)
	}
	r := f.completeSale(saleID, tender("cash", 5000))
	if r.status != http.StatusConflict || !strings.Contains(string(r.raw), taxzone.BlockerPartsNotConfigured) ||
		!strings.Contains(string(r.raw), "Alberta") {
		t.Fatalf("complete = %d, want 409 %s naming Alberta: %s", r.status, taxzone.BlockerPartsNotConfigured, r.raw)
	}
}

func TestCanadianCounterDeliveryToAnUnzonedShipToIsRefusedInTheClerksWords(t *testing.T) {
	testutil.LockOutboxTables(t)
	db := testutil.RequireDB(t)
	f := newCounterFixture(t, db)
	f.seedBCZone()
	f.placeBranch("CA", "BC", "V1X 7K2")

	shipTo := uuid.New()
	mustExec(t, db, `INSERT INTO customer_ship_tos (id, customer_id, code, name, line1, city, region, postal_code, country, tax_rate, is_default, is_active)
		VALUES ($1, $2, 'SITE', 'The site', '1 Prairie Way', 'Calgary', 'AB', 'T2P 1J9', 'CA', 0.050000, TRUE, TRUE)`, shipTo, f.customerID)
	t.Cleanup(func() { mustExec(t, db, `DELETE FROM customer_ship_tos WHERE id = $1`, shipTo) })

	svc := pos.NewService(db, pos.NewRepository(db), nil)
	sale := &pos.Sale{BranchID: f.branchID, DeliveryType: "delivery", ShipToID: &shipTo}
	_, err := svc.ResolveSaleTax(context.Background(), sale, f.customerID)
	h, ok := err.(interface{ Error() string })
	want := "No tax zone covers Alberta: ask an admin to add one, or take the sale as a pickup."
	if !ok || !strings.Contains(h.Error(), want) {
		t.Fatalf("err = %v, want the clerk's words %q", err, want)
	}
}
