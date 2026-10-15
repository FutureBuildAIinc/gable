// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package dashboard

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/gablelbm/gable/internal/testutil"
	"github.com/google/uuid"
)

// alertsWorld seeds two yards (the first with a zone under it) and a product
// with a reorder point of 150, and returns what a leg needs.
type alertsWorld struct {
	yardA, zoneA, yardB, product uuid.UUID
	sku                          string
	repo                         *PostgresRepository
}

func newAlertsWorld(t *testing.T) *alertsWorld {
	t.Helper()
	db := testutil.RequireDB(t)
	ctx := context.Background()
	w := &alertsWorld{yardA: uuid.New(), zoneA: uuid.New(), yardB: uuid.New(), product: uuid.New(), repo: NewRepository(db)}
	exec := func(q string, args ...any) {
		t.Helper()
		if _, err := db.Pool.Exec(ctx, q, args...); err != nil {
			t.Fatalf("seed: %v\n%s", err, q)
		}
	}
	exec(`INSERT INTO locations (id, type, code, name, branch_id) VALUES ($1, 'BRANCH', $2, 'Alerts Yard A', $1)`, w.yardA, "AYA-"+w.yardA.String()[:8])
	exec(`INSERT INTO locations (id, type, code, parent_id, branch_id) VALUES ($1, 'ZONE', $2, $3, $3)`, w.zoneA, "AZA-"+w.zoneA.String()[:8], w.yardA)
	exec(`INSERT INTO locations (id, type, code, name, branch_id) VALUES ($1, 'BRANCH', $2, 'Alerts Yard B', $1)`, w.yardB, "AYB-"+w.yardB.String()[:8])
	w.sku = "ALY-" + w.product.String()[:8]
	exec(`INSERT INTO products (id, sku, description, uom_primary, base_price, reorder_point) VALUES ($1, $2, 'Alert yards hanger', 'EA', 4.00, 150)`, w.product, w.sku)
	t.Cleanup(func() {
		bg := context.Background()
		_, _ = db.Pool.Exec(bg, `DELETE FROM inventory WHERE product_id = $1`, w.product)
		_, _ = db.Pool.Exec(bg, `DELETE FROM products WHERE id = $1`, w.product)
		_, _ = db.Pool.Exec(bg, `DELETE FROM locations WHERE id = $1`, w.zoneA)
		_, _ = db.Pool.Exec(bg, `DELETE FROM locations WHERE id IN ($1, $2)`, w.yardA, w.yardB)
	})
	return w
}

func (w *alertsWorld) stock(t *testing.T, loc *uuid.UUID, qty int) {
	t.Helper()
	db := testutil.RequireDB(t)
	if _, err := db.Pool.Exec(context.Background(),
		`INSERT INTO inventory (product_id, location_id, location, quantity, allocated) VALUES ($1, $2, 'row', $3, 0)`,
		w.product, loc, qty); err != nil {
		t.Fatalf("stock: %v", err)
	}
}

// alerts reads every alert for the SKU as the unwalled caller (an
// administrator with no branch header).
func (w *alertsWorld) alerts(t *testing.T) []InventoryAlert {
	t.Helper()
	all, err := w.repo.GetInventoryAlerts(context.Background(), nil, nil, 100000)
	if err != nil {
		t.Fatalf("alerts: %v", err)
	}
	var out []InventoryAlert
	for _, a := range all {
		if a.SKU == w.sku {
			out = append(out, a)
		}
	}
	return out
}

// Two yards low on one product read as two rows for an unwalled caller, each
// the yard's own sum with the yard as its location.
func TestGetInventoryAlerts_KeepsYardsApartForAnUnwalledCaller(t *testing.T) {
	w := newAlertsWorld(t)
	w.stock(t, &w.yardA, 100)
	w.stock(t, &w.zoneA, 32) // yard A: 132 in all
	w.stock(t, &w.yardB, 40)

	got := map[string]int{}
	for _, a := range w.alerts(t) {
		got[a.LocationID] = a.CurrentQty
	}
	if len(got) != 2 || got[w.yardA.String()] != 132 || got[w.yardB.String()] != 40 {
		t.Fatalf("alerts by yard = %v, want yard A 132 and yard B 40", got)
	}
}

// A legacy row with no location stays its own alert beside a yard's.
func TestGetInventoryAlerts_ALegacyRowStaysItsOwnAlert(t *testing.T) {
	w := newAlertsWorld(t)
	w.stock(t, nil, 5)
	w.stock(t, &w.yardA, 20)

	got := map[string]int{}
	for _, a := range w.alerts(t) {
		got[a.LocationID] = a.CurrentQty
	}
	if len(got) != 2 || got[""] != 5 || got[w.yardA.String()] != 20 {
		t.Fatalf("alerts = %v, want the legacy row 5 (no yard) and yard A 20", got)
	}
}

// Each alert names its yard, so two rows for one SKU can be told apart on the
// dashboard.
func TestGetInventoryAlerts_NamesTheYard(t *testing.T) {
	w := newAlertsWorld(t)
	w.stock(t, &w.yardA, 20)
	w.stock(t, &w.yardB, 30)

	names := map[string]string{}
	for _, a := range w.alerts(t) {
		raw, err := json.Marshal(a)
		if err != nil {
			t.Fatal(err)
		}
		var m map[string]any
		_ = json.Unmarshal(raw, &m)
		name, _ := m["yard_name"].(string)
		names[a.LocationID] = name
	}
	if names[w.yardA.String()] != "Alerts Yard A" || names[w.yardB.String()] != "Alerts Yard B" {
		t.Fatalf("yard names = %v, want each alert to carry its yard's name", names)
	}
}
