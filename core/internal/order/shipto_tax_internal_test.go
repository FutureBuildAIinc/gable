// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package order

// The zone window is read on the branch's own business day, never the
// server's clock (MONEY-CHECKLIST item 11), on the order path too.

import (
	"context"
	"testing"
	"time"

	"github.com/gablelbm/gable/internal/taxzone"
	"github.com/gablelbm/gable/internal/testutil"
	"github.com/gablelbm/gable/pkg/database"
	"github.com/google/uuid"
)

func TestTaxInputsReadTheZoneOnTheBranchBusinessDay(t *testing.T) {
	db := testutil.RequireDB(t)
	ctx := context.Background()

	var branchID uuid.UUID
	if err := db.Pool.QueryRow(ctx,
		`INSERT INTO locations (id, type, code, name, timezone, default_tax_rate)
		 VALUES (uuid_generate_v4(), 'BRANCH', $1, 'Kiritimati order branch', 'Pacific/Kiritimati', 0.088750) RETURNING id`,
		"OKITI-"+uuid.NewString()[:8]).Scan(&branchID); err != nil {
		t.Fatalf("branch: %v", err)
	}
	customerID := uuid.New()
	mustExecO(t, db, `INSERT INTO customers (id, name, account_number, primary_branch_id)
		VALUES ($1, 'Order Zone Day Co', $2, $3)`, customerID, "OZDAY-"+uuid.NewString()[:8], branchID)
	shipTo := uuid.New()
	mustExecO(t, db, `INSERT INTO customer_ship_tos (id, customer_id, code, name, line1, city, region, postal_code, country, is_default, is_active)
		VALUES ($1, $2, 'SITE', 'The site', '1 Day Way', 'Kiritimati', '', '98150', 'KI', TRUE, TRUE)`, shipTo, customerID)
	t.Cleanup(func() {
		mustExecO(t, db, `DELETE FROM customer_ship_tos WHERE customer_id = $1`, customerID)
		mustExecO(t, db, `DELETE FROM customers WHERE id = $1`, customerID)
		mustExecO(t, db, `DELETE FROM locations WHERE id = $1`, branchID)
	})

	repo := taxzone.NewRepository(db)
	if _, err := repo.Create(ctx, &taxzone.Draft{
		Country: "KI", JurisdictionCode: "KI-OZONE", JurisdictionType: "STA", JurisdictionName: "The zone",
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
	t.Cleanup(func() { mustExecO(t, db, `TRUNCATE tax_zone_boundaries, tax_zone_rates, tax_zones`) })

	svc := NewService(NewRepository(db))
	// 2026-06-01 12:00 UTC is 2026-06-02 02:00 in Kiritimati.
	svc.now = func() time.Time { return time.Date(2026, 6, 1, 12, 0, 0, 0, time.UTC) }

	o := &Order{OrderSummary: OrderSummary{BranchID: branchID, DeliveryType: DeliveryDelivery, ShipToID: &shipTo}}
	inputs, _, err := svc.taxInputs(ctx, o, false)
	if err != nil {
		t.Fatalf("taxInputs: %v", err)
	}
	if inputs.ZoneRate == nil || *inputs.ZoneRate != "0.070000" {
		got := "<nil>"
		if inputs.ZoneRate != nil {
			got = *inputs.ZoneRate
		}
		t.Fatalf("zone rate = %s, want 0.070000: the zone window must be read on the branch's business day 2026-06-02, not the UTC day 2026-06-01", got)
	}
}

func mustExecO(t *testing.T, db *database.DB, sql string, args ...any) {
	t.Helper()
	if _, err := db.Pool.Exec(context.Background(), sql, args...); err != nil {
		t.Fatalf("exec: %v", err)
	}
}
