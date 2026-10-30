// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package pos

// The zone window is read on the branch's own business day, never the
// server's clock (MONEY-CHECKLIST item 11): a rate that changes overnight
// must bite when the branch's day says so, even when the UTC day is a day
// behind.

import (
	"context"
	"testing"
	"time"

	"github.com/gablelbm/gable/internal/taxzone"
	"github.com/gablelbm/gable/internal/testutil"
	"github.com/gablelbm/gable/pkg/database"
	"github.com/google/uuid"
)

func TestResolveSaleTaxReadsTheZoneOnTheBranchBusinessDay(t *testing.T) {
	testutil.LockOutboxTables(t)
	testutil.LockTaxZones(t)
	db := testutil.RequireDB(t)
	ctx := context.Background()

	// A branch fourteen hours ahead of UTC: at the frozen moment the UTC
	// day is 2026-06-01 and the branch's own day is already 2026-06-02.
	var branchID uuid.UUID
	mustExecZ(t, db, `INSERT INTO locations (id, type, code, name, timezone, default_tax_rate)
		VALUES (uuid_generate_v4(), 'BRANCH', 'KITI-`+uuid.NewString()[:8]+`', 'Kiritimati test branch', 'Pacific/Kiritimati', 0.088750)`)
	if err := db.Pool.QueryRow(ctx,
		`SELECT id FROM locations WHERE name = 'Kiritimati test branch' ORDER BY id DESC LIMIT 1`).Scan(&branchID); err != nil {
		t.Fatalf("branch: %v", err)
	}

	customerID := uuid.New()
	mustExecZ(t, db, `INSERT INTO customers (id, name, account_number, primary_branch_id)
		VALUES ($1, 'Zone Day Co', 'ZDAY-`+uuid.NewString()[:8]+`', $2)`, customerID, branchID)
	t.Cleanup(func() {
		mustExecZ(t, db, `DELETE FROM customer_ship_tos WHERE customer_id = $1`, customerID)
		mustExecZ(t, db, `DELETE FROM customers WHERE id = $1`, customerID)
		mustExecZ(t, db, `DELETE FROM locations WHERE id = $1`, branchID)
	})
	shipTo := uuid.New()
	mustExecZ(t, db, `INSERT INTO customer_ship_tos (id, customer_id, code, name, line1, city, region, postal_code, country, is_default, is_active)
		VALUES ($1, $2, 'SITE', 'The site', '1 Day Way', 'Kiritimati', '', '98150', 'KI', TRUE, TRUE)`, shipTo, customerID)

	repo := taxzone.NewRepository(db)
	if _, err := repo.Create(ctx, &taxzone.Draft{
		Country: "KI", JurisdictionCode: "KI-ZONE", JurisdictionType: "STA", JurisdictionName: "The zone",
		ZoneKind: taxzone.KindPostal, Sourcing: taxzone.SourcingDestination,
		EffectiveFrom: "2020-01-01", DataSource: "dealer", DataVersion: "1",
		Rates: []taxzone.RatePart{
			{Name: taxzone.PartState, Rate: "0.030000", EffectiveFrom: "2020-01-01", EffectiveTo: "2026-06-01"},
			{Name: taxzone.PartState, Rate: "0.070000", EffectiveFrom: "2026-06-02"},
		},
		Boundaries: []taxzone.Boundary{{Country: "KI", PostalLow: "98100", PostalHigh: "98199"}},
	}); err != nil {
		t.Fatalf("zone: %v", err)
	}
	t.Cleanup(func() { mustExecZ(t, db, `TRUNCATE tax_zone_boundaries, tax_zone_rates, tax_zones`) })

	svc := NewService(db, NewRepository(db), nil)
	// 2026-06-01 12:00 UTC is 2026-06-02 02:00 in Kiritimati.
	svc.now = func() time.Time { return time.Date(2026, 6, 1, 12, 0, 0, 0, time.UTC) }

	sale := &Sale{BranchID: branchID, DeliveryType: "delivery", ShipToID: &shipTo}
	resolved, err := svc.ResolveSaleTax(ctx, sale, customerID)
	if err != nil {
		t.Fatalf("ResolveSaleTax: %v", err)
	}
	if resolved.Source != "ZONE_RATE" {
		t.Fatalf("source = %s, want ZONE_RATE", resolved.Source)
	}
	if resolved.Rate == nil || *resolved.Rate != "0.070000" {
		got := "<nil>"
		if resolved.Rate != nil {
			got = *resolved.Rate
		}
		t.Fatalf("rate = %s, want 0.070000: the zone window must be read on the branch's business day 2026-06-02, not the UTC day 2026-06-01", got)
	}
}

func mustExecZ(t *testing.T, db *database.DB, sql string, args ...any) {
	t.Helper()
	if _, err := db.Pool.Exec(context.Background(), sql, args...); err != nil {
		t.Fatalf("exec: %v", err)
	}
}
