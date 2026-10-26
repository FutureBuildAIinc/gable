// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package order_test

// The ship to part of CP-10 on the order path: a delivery order whose ship
// to sits in a zoned postal area is taxed at the zone's combined rate and
// records the zone it was taxed under; a delivery whose postal area has no
// zone keeps the typed ship-to rate; a pickup keeps the branch rate.

import (
	"context"
	"net/http"
	"testing"

	"github.com/gablelbm/gable/internal/taxzone"
	"github.com/gablelbm/gable/internal/testutil"
	"github.com/google/uuid"
)

// seedZone makes one postal zone covering 98100-98199 through the zone
// tables, with the parts summing to the named combined rate.
func seedZone(t *testing.T, f *fixture, rate1, rate2 string) uuid.UUID {
	t.Helper()
	repo := taxzone.NewRepository(f.db)
	parts := []taxzone.RatePart{{Name: taxzone.PartState, Rate: rate1, EffectiveFrom: "2020-01-01"}}
	if rate2 != "" {
		parts = append(parts, taxzone.RatePart{Name: taxzone.PartCity, Rate: rate2, EffectiveFrom: "2020-01-01"})
	}
	z, err := repo.Create(context.Background(), &taxzone.Draft{
		Country: "US", JurisdictionCode: "Z-98", JurisdictionType: "CTY", JurisdictionName: "The county",
		ZoneKind: taxzone.KindPostal, Sourcing: taxzone.SourcingDestination,
		EffectiveFrom: "2020-01-01", DataSource: "dealer", DataVersion: "1",
		Rates:      parts,
		Boundaries: []taxzone.Boundary{{Country: "US", PostalLow: "98100", PostalHigh: "98199"}},
	})
	if err != nil {
		t.Fatalf("create zone: %v", err)
	}
	return z.ID
}

func TestDeliveryOrderTakesTheShipToZoneRate(t *testing.T) {
	f := newFixture(t, testutil.RequireDB(t))
	zoneID := seedZone(t, f, "0.035000", "0.015000")

	// A ship to with no typed rate, inside the zone's range.
	shipTo := uuid.New()
	if _, err := f.db.Pool.Exec(context.Background(), `INSERT INTO customer_ship_tos (id, customer_id, code, name, line1, city, region, postal_code, country, is_default, is_active)
		VALUES ($1, $2, 'SITE', 'The site', '1 Golden Way', 'Tarnwick', 'WA', '98150', 'US', TRUE, TRUE)`,
		shipTo, f.customerID); err != nil {
		t.Fatal(err)
	}

	r := f.do("POST", "/api/v1/orders", map[string]any{
		"customer_id": f.customerID.String(), "delivery_type": "delivery", "ship_to_id": shipTo.String(),
		"lines": []map[string]any{f.line("10")},
	})
	if r.status != http.StatusCreated {
		t.Fatalf("delivery create = %d: %s", r.status, r.raw)
	}
	if src := str(t, r.body, "tax_source"); src != "zone_rate" {
		t.Errorf("tax_source = %q, want zone_rate", src)
	}
	if pct := str(t, r.body, "tax_rate_percent"); pct != "5" {
		t.Errorf("tax_rate_percent = %q, want 5 (the zone's combined rate)", pct)
	}

	// The order row keeps the zone and the parts frozen at the sale.
	var (
		src   string
		zone  *uuid.UUID
		parts *string
	)
	if err := f.db.Pool.QueryRow(context.Background(),
		`SELECT tax_source, tax_zone_id, tax_rate_parts::text FROM orders WHERE id = $1`, str(t, r.body, "id")).
		Scan(&src, &zone, &parts); err != nil {
		t.Fatal(err)
	}
	if src != "ZONE_RATE" || zone == nil || *zone != zoneID {
		t.Errorf("order tax = %s %v, want ZONE_RATE %s", src, zone, zoneID)
	}
	if parts == nil {
		t.Error("order tax_rate_parts is empty, want the frozen parts")
	}
}

func TestDeliveryOrderOutsideAnyZoneKeepsTheTypedRate(t *testing.T) {
	f := newFixture(t, testutil.RequireDB(t))
	seedZone(t, f, "0.035000", "0.015000")

	// A typed 12 percent rate on a postal area the zone does not cover: the
	// dealer's deliberate override answers when no zone does.
	shipTo := uuid.New()
	if _, err := f.db.Pool.Exec(context.Background(), `INSERT INTO customer_ship_tos (id, customer_id, code, name, line1, city, region, postal_code, country, tax_rate, is_default, is_active)
		VALUES ($1, $2, 'SITE', 'The site', '1 Golden Way', 'Tarnwick', 'WA', '99999', 'US', 0.120000, TRUE, TRUE)`,
		shipTo, f.customerID); err != nil {
		t.Fatal(err)
	}
	r := f.do("POST", "/api/v1/orders", map[string]any{
		"customer_id": f.customerID.String(), "delivery_type": "delivery", "ship_to_id": shipTo.String(),
		"lines": []map[string]any{f.line("10")},
	})
	if r.status != http.StatusCreated {
		t.Fatalf("delivery create = %d: %s", r.status, r.raw)
	}
	if src := str(t, r.body, "tax_source"); src != "ship_to_rate" {
		t.Errorf("tax_source = %q, want ship_to_rate (no zone covers 99999)", src)
	}
}
