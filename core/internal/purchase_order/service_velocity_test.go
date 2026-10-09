// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package purchase_order_test

// The reorder payload of ADR 0008 section 10.3 through the real scan and
// the real database: the stored recommendation carries on_hand, allocated,
// available, on_order, backordered, velocity with its lookback, and
// lead_time_days with its source (the vendor's measured lead time when it
// has one, else the setting default), and the run writes one
// reorder.recommended event per recommendation, at the end of the run's
// transaction. The dry run stores nothing. The formula's numbers (point =
// ceil(velocity x lead x 1.5), qty = ceil(velocity x 30)) are pinned end to
// end by the serve wiring test TestBranchWall_PORefreshReorderTargets.

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/gablelbm/gable/internal/inventory"
	"github.com/gablelbm/gable/internal/product"
	"github.com/gablelbm/gable/internal/purchase_order"
	"github.com/gablelbm/gable/internal/testutil"
	"github.com/gablelbm/gable/internal/vendor"
	"github.com/gablelbm/gable/pkg/branchctx"
	"github.com/gablelbm/gable/pkg/database"
	"github.com/gablelbm/gable/pkg/outbox"
	"github.com/google/uuid"
)

type reorderFixture struct {
	t       *testing.T
	db      *database.DB
	svc     *purchase_order.Service
	branch  uuid.UUID
	yard    uuid.UUID
	product uuid.UUID
	vendor  uuid.UUID
}

func newReorderFixture(t *testing.T) *reorderFixture {
	t.Helper()
	db := testutil.RequireDB(t)
	f := &reorderFixture{t: t, db: db, branch: uuid.New(), yard: uuid.New(), product: uuid.New(), vendor: uuid.New()}
	ctx := context.Background()
	must := func(sql string, args ...any) {
		t.Helper()
		if _, err := db.Pool.Exec(ctx, sql, args...); err != nil {
			t.Fatalf("%s: %v", sql, err)
		}
	}
	must(`INSERT INTO locations (id, type, code, parent_id, branch_id) VALUES ($1, 'BRANCH', $2, NULL, $1)`, f.branch, "ro-"+f.branch.String()[:6])
	must(`INSERT INTO locations (id, type, code, parent_id, branch_id) VALUES ($1, 'YARD', $2, $3, $3)`, f.yard, "ro-"+f.yard.String()[:6], f.branch)
	must(`INSERT INTO vendors (id, name) VALUES ($1, $2)`, f.vendor, "ro-"+f.vendor.String()[:6])
	t.Cleanup(func() {
		_, _ = db.Pool.Exec(ctx, `DELETE FROM events_outbox WHERE entity_id = $1`, f.product)
		_, _ = db.Pool.Exec(ctx, `DELETE FROM reorder_recommendations WHERE product_id = $1`, f.product)
		_, _ = db.Pool.Exec(ctx, `DELETE FROM reorder_runs WHERE branch_id = $1`, f.branch)
		_, _ = db.Pool.Exec(ctx, `DELETE FROM stock_level_dirty WHERE product_id = $1`, f.product)
		_, _ = db.Pool.Exec(ctx, `DELETE FROM stock_levels WHERE product_id = $1`, f.product)
		_, _ = db.Pool.Exec(ctx, `DELETE FROM inventory WHERE product_id = $1`, f.product)
		_, _ = db.Pool.Exec(ctx, `DELETE FROM products WHERE id = $1`, f.product)
		_, _ = db.Pool.Exec(ctx, `DELETE FROM vendors WHERE id = $1`, f.vendor)
		_, _ = db.Pool.Exec(ctx, `DELETE FROM locations WHERE parent_id = $1`, f.branch)
		_, _ = db.Pool.Exec(ctx, `DELETE FROM locations WHERE id = $1`, f.branch)
	})

	repo := purchase_order.NewRepository(db)
	invSvc := inventory.NewService(inventory.NewRepository(db))
	prodSvc := product.NewService(product.NewRepository(db))
	vendSvc := vendor.NewService(vendor.NewRepository(db))
	f.svc = purchase_order.NewService(repo, db, nil, invSvc, prodSvc, vendSvc).
		WithVelocityRepo(purchase_order.NewVelocityRepository(db)).
		WithOutbox(outbox.NewWriter(db, ""))
	return f
}

// seedStockAndDemand gives the product 2 on hand (1 allocated), 90 units of
// demand in the 90 day lookback at the branch, and a sent purchase order of
// 50 on order. vendorLeadDays > 0 gives the vendor a measured lead time.
func (f *reorderFixture) seedStockAndDemand(t *testing.T, vendorLeadDays float64) {
	t.Helper()
	ctx := context.Background()
	must := func(sql string, args ...any) {
		t.Helper()
		if _, err := f.db.Pool.Exec(ctx, sql, args...); err != nil {
			t.Fatalf("%s: %v", sql, err)
		}
	}
	must(`INSERT INTO products (id, sku, description, uom_primary, base_price, reorder_point, reorder_qty, vendor_id)
		VALUES ($1, $2, 'reorder', 'EA', 1, 5, 10, $3)`, f.product, "RO-"+f.product.String()[:8], f.vendor)
	if vendorLeadDays > 0 {
		must(`UPDATE vendors SET average_lead_time_days = $2 WHERE id = $1`, f.vendor, vendorLeadDays)
	}
	must(`INSERT INTO inventory (product_id, location_id, location, quantity, allocated) VALUES ($1, $2, 'ro', 2, 1)`,
		f.product, f.yard)
	// Demand: one order line of 90 units, 30 days old.
	cust, order, line := uuid.New(), uuid.New(), uuid.New()
	t.Cleanup(func() {
		_, _ = f.db.Pool.Exec(ctx, `DELETE FROM order_lines WHERE order_id = $1`, order)
		_, _ = f.db.Pool.Exec(ctx, `DELETE FROM orders WHERE id = $1`, order)
		_, _ = f.db.Pool.Exec(ctx, `DELETE FROM customers WHERE id = $1`, cust)
	})
	must(`INSERT INTO customers (id, name, account_number, primary_branch_id) VALUES ($1, 'ro cust', $2, $3)`,
		cust, "RO"+cust.String()[:8], f.branch)
	must(`INSERT INTO orders (id, customer_id, branch_id, status, total_amount, delivery_type, currency, created_at)
		VALUES ($1, $2, $3, 'CONFIRMED', 10, 'PICKUP', 'USD', now() - interval '30 days')`, order, cust, f.branch)
	must(`INSERT INTO order_lines (id, order_id, product_id, quantity, uom, price_uom, uom_qty, price_uom_qty, unit_price, line_total, description, created_at)
		VALUES ($1, $2, $3, 90, 'EA', 'EA', 1, 1, 1, 90, 'ro', now() - interval '30 days')`, line, order, f.product)
	// On order: a sent purchase order of 50, none received.
	po, poline := uuid.New(), uuid.New()
	t.Cleanup(func() {
		_, _ = f.db.Pool.Exec(ctx, `DELETE FROM purchase_order_lines WHERE po_id = $1`, po)
		_, _ = f.db.Pool.Exec(ctx, `DELETE FROM purchase_orders WHERE id = $1`, po)
	})
	must(`INSERT INTO purchase_orders (id, vendor_id, status, source, branch_id) VALUES ($1, $2, 'SENT', 'MANUAL', $3)`,
		po, f.vendor, f.branch)
	must(`INSERT INTO purchase_order_lines (id, po_id, product_id, description, quantity, unit_cost, line_total, uom, price_uom, uom_qty, price_uom_qty, stock_uom, stock_quantity, position)
		VALUES ($1, $2, $3, 'ro', 50, 1, 50, 'EA', 'EA', 1, 1, 'EA', 50, 1)`, poline, po, f.product)
}

// notOnOrder marks the seeded purchase order received, so its 50 units stop
// counting as on order and the product's need is real.
func (f *reorderFixture) notOnOrder(t *testing.T) {
	t.Helper()
	if _, err := f.db.Pool.Exec(context.Background(), `UPDATE purchase_orders SET status = 'RECEIVED'`); err != nil {
		t.Fatal(err)
	}
}

// lastEvent returns the newest event of a type for an entity.
func (f *reorderFixture) lastEvent(t *testing.T, eventType string, entity uuid.UUID) map[string]any {
	t.Helper()
	var data []byte
	if err := f.db.Pool.QueryRow(context.Background(),
		`SELECT data FROM events_outbox WHERE type = $1 AND entity_id = $2 ORDER BY position DESC LIMIT 1`,
		eventType, entity).Scan(&data); err != nil {
		t.Fatalf("no %s event for %s: %v", eventType, entity, err)
	}
	var payload map[string]any
	if err := json.Unmarshal(data, &payload); err != nil {
		t.Fatal(err)
	}
	return payload
}

// TestReorderPayloadCarriesOnOrderVelocityAndLeadTime: the run's event
// carries the whole 10.3 payload with its sources. Due is decided by
// available + on_order - backordered <= reorder_point: 1 available + 50 on
// order against a point of 5 is NOT due, and the on order quantity is what
// the base commit's engine ignored (the live failure test covers that
// route); with the purchase order received, the product is due and stored.
func TestReorderPayloadCarriesOnOrderVelocityAndLeadTime(t *testing.T) {
	testutil.LockOutboxTables(t)
	f := newReorderFixture(t)
	f.seedStockAndDemand(t, 0)
	f.notOnOrder(t)
	ctx := branchctx.WithSystem(context.Background())

	res, err := f.svc.RefreshReorderTargets(ctx, false, 90)
	if err != nil {
		t.Fatalf("refresh: %v", err)
	}
	if res.RecommendationsNew < 1 {
		t.Fatalf("recommendations_new = %d, want at least 1 (1 available against a point of 5)", res.RecommendationsNew)
	}
	var n int
	if err := f.db.Pool.QueryRow(ctx,
		`SELECT COUNT(*) FROM reorder_recommendations WHERE product_id = $1 AND branch_id = $2 AND status = 'OPEN'`,
		f.product, f.branch).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("%d open recommendations stored for the product and branch, want 1", n)
	}

	payload := f.lastEvent(t, "reorder.recommended", f.product)
	for _, field := range []string{"on_hand", "allocated", "available", "on_order", "backordered", "velocity", "lookback_days", "lead_time_days", "lead_time_source", "suggested_quantity", "unit", "reorder_point", "reorder_quantity"} {
		if _, ok := payload[field]; !ok {
			t.Errorf("the reorder.recommended payload does not carry %q: %v", field, payload)
		}
	}
	if payload["on_hand"] != "2" || payload["available"] != "1" || payload["on_order"] != "0" {
		t.Errorf("on_hand %v available %v on_order %v, want 2, 1, 0", payload["on_hand"], payload["available"], payload["on_order"])
	}
	// Velocity: 90 units over 90 days = 1 a day, on the wire's shortest
	// exact decimal form.
	if payload["velocity"] != "1" {
		t.Errorf("velocity %v, want 1 (90 units over the 90 day lookback)", payload["velocity"])
	}
	if payload["lookback_days"] != float64(90) {
		t.Errorf("lookback_days %v, want 90", payload["lookback_days"])
	}
	// No vendor lead time: the setting default (7) with source default.
	if payload["lead_time_days"] != "7" || payload["lead_time_source"] != "default" {
		t.Errorf("lead_time_days %v source %v, want 7 and default", payload["lead_time_days"], payload["lead_time_source"])
	}
	if payload["unit"] != "EA" {
		t.Errorf("unit %v, want EA", payload["unit"])
	}
}

// TestReorderPayloadLeadTimeFromVendor: a vendor with a measured lead time
// feeds it with its source.
func TestReorderPayloadLeadTimeFromVendor(t *testing.T) {
	testutil.LockOutboxTables(t)
	f := newReorderFixture(t)
	f.seedStockAndDemand(t, 12)
	f.notOnOrder(t)
	ctx := branchctx.WithSystem(context.Background())

	if _, err := f.svc.RefreshReorderTargets(ctx, false, 90); err != nil {
		t.Fatalf("refresh: %v", err)
	}
	payload := f.lastEvent(t, "reorder.recommended", f.product)
	if payload["lead_time_days"] != "12" || payload["lead_time_source"] != "measured" {
		t.Errorf("lead_time_days %v source %v, want 12 and measured", payload["lead_time_days"], payload["lead_time_source"])
	}
}

// TestRefreshReorderTargetsDryRunStoresNothing: a dry run writes no stock
// level target and stores no recommendation.
func TestRefreshReorderTargetsDryRunStoresNothing(t *testing.T) {
	f := newReorderFixture(t)
	f.seedStockAndDemand(t, 0)
	f.notOnOrder(t)
	ctx := branchctx.WithSystem(context.Background())

	if _, err := f.svc.RefreshReorderTargets(ctx, true, 90); err != nil {
		t.Fatalf("refresh: %v", err)
	}
	var n int
	if err := f.db.Pool.QueryRow(ctx,
		`SELECT COUNT(*) FROM reorder_recommendations WHERE product_id = $1`, f.product).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Fatalf("the dry run stored %d recommendations, want none", n)
	}
	// The dry run writes no stock level row for a product and branch that
	// had none: the targets are the refresh's to write, and it did not.
	var rows int
	if err := f.db.Pool.QueryRow(ctx,
		`SELECT COUNT(*) FROM stock_levels WHERE product_id = $1 AND branch_id = $2`,
		f.product, f.branch).Scan(&rows); err != nil {
		t.Fatal(err)
	}
	if rows != 0 {
		t.Fatalf("the dry run wrote %d stock level rows, want none", rows)
	}
}
