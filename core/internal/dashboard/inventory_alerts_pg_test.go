// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package dashboard

import (
	"context"
	"testing"

	"github.com/gablelbm/gable/internal/testutil"
	"github.com/google/uuid"
)

// CORRECTNESS (item 130, N20): an inventory row sits at one location, the yard
// itself or a zone or bin under it, so a yard's stock is the sum of its own row
// and every location whose branch_id is that yard. The alert compares that sum
// with the reorder point and reports it. One yard holds 112 on its own row and
// 300 in a zone (412 in all) against a reorder point of 150: no alert. Cut the
// zone to 20 (132) and the alert reports 132, not 112.
//
// Skips cleanly when Postgres is unreachable (testutil.RequireDB).
func TestGetInventoryAlerts_SumsTheYardsOwnRowAndItsZones(t *testing.T) {
	db := testutil.RequireDB(t)
	repo := NewRepository(db)
	ctx := context.Background()

	yard, zone, otherYard, product := uuid.New(), uuid.New(), uuid.New(), uuid.New()
	exec := func(q string, args ...any) {
		t.Helper()
		if _, err := db.Pool.Exec(ctx, q, args...); err != nil {
			t.Fatalf("seed: %v\n%s", err, q)
		}
	}
	exec(`INSERT INTO locations (id, type, code, branch_id) VALUES ($1, 'BRANCH', $2, $1)`, yard, "IA-"+yard.String()[:8])
	exec(`INSERT INTO locations (id, type, code, parent_id, branch_id) VALUES ($1, 'ZONE', $2, $3, $3)`, zone, "IZ-"+zone.String()[:8], yard)
	exec(`INSERT INTO locations (id, type, code, branch_id) VALUES ($1, 'BRANCH', $2, $1)`, otherYard, "IB-"+otherYard.String()[:8])
	sku := "ALERT-" + product.String()[:8]
	exec(`INSERT INTO products (id, sku, description, uom_primary, base_price, reorder_point) VALUES ($1, $2, 'Alert test hanger', 'EA', 4.00, 150)`, product, sku)
	t.Cleanup(func() {
		bg := context.Background()
		_, _ = db.Pool.Exec(bg, `DELETE FROM inventory WHERE product_id = $1`, product)
		_, _ = db.Pool.Exec(bg, `DELETE FROM products WHERE id = $1`, product)
		_, _ = db.Pool.Exec(bg, `DELETE FROM locations WHERE id = $1`, zone)
		for _, y := range []uuid.UUID{yard, otherYard} {
			_, _ = db.Pool.Exec(bg, `DELETE FROM locations WHERE id = $1`, y)
		}
	})
	exec(`INSERT INTO inventory (product_id, location_id, location, quantity, allocated) VALUES ($1, $2, 'yard', 112, 0)`, product, yard)
	exec(`INSERT INTO inventory (product_id, location_id, location, quantity, allocated) VALUES ($1, $2, 'zone', 300, 0)`, product, zone)

	alertFor := func(branch uuid.UUID) *InventoryAlert {
		t.Helper()
		alerts, err := repo.GetInventoryAlerts(ctx, &branch, nil, 10000)
		if err != nil {
			t.Fatalf("alerts: %v", err)
		}
		for i := range alerts {
			if alerts[i].SKU == sku {
				return &alerts[i]
			}
		}
		return nil
	}

	if a := alertFor(yard); a != nil {
		t.Fatalf("yard holds 412 against a reorder point of 150, want no alert, got %+v", *a)
	}

	exec(`UPDATE inventory SET quantity = 20 WHERE product_id = $1 AND location_id = $2`, product, zone)
	a := alertFor(yard)
	if a == nil {
		t.Fatal("yard holds 132 against a reorder point of 150, want a low stock alert")
	}
	if a.CurrentQty != 132 || a.AlertType != "LOW_STOCK" {
		t.Fatalf("alert = %+v, want current 132 and LOW_STOCK", *a)
	}
	if a.LocationID != yard.String() {
		t.Fatalf("alert location = %q, want the yard %s", a.LocationID, yard)
	}
}
