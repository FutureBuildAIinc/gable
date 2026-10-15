// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package purchase_order

import (
	"context"
	"strings"
	"testing"

	"github.com/gablelbm/gable/internal/product"
	"github.com/gablelbm/gable/internal/testutil"
	"github.com/gablelbm/gable/internal/vendor"
	"github.com/gablelbm/gable/pkg/branchctx"
	"github.com/google/uuid"
)

// A product at its point with no reorder quantity set gets no line, and the
// run's record names the product ("no reorder quantity set"), so a person
// reading the run knows why nothing was ordered.
func TestRunCreateReorders_RecordsAProductWithNoReorderQuantity(t *testing.T) {
	db := testutil.RequireDB(t)
	ctx := context.Background()
	yard, vend, prod := uuid.New(), uuid.New(), uuid.New()
	sku := "RQ-" + prod.String()[:8]
	exec := func(q string, args ...any) {
		t.Helper()
		if _, err := db.Pool.Exec(ctx, q, args...); err != nil {
			t.Fatalf("seed: %v", err)
		}
	}
	exec(`INSERT INTO locations (id, type, code) VALUES ($1, 'BRANCH', $2)`, yard, "RQ-"+yard.String()[:8])
	exec(`INSERT INTO vendors (id, name) VALUES ($1, 'run record vendor')`, vend)
	exec(`INSERT INTO products (id, sku, description, uom_primary, base_price, vendor_id, reorder_point, reorder_qty)
	      VALUES ($1, $2, 'run record', 'EA', 1, $3, 5, 0)`, prod, sku, vend)
	exec(`INSERT INTO inventory (product_id, location_id, location, quantity, allocated) VALUES ($1, $2, 'row', 5, 0)`, prod, yard)
	t.Cleanup(func() {
		_, _ = db.Pool.Exec(ctx, `DELETE FROM inventory WHERE product_id = $1`, prod)
		_, _ = db.Pool.Exec(ctx, `DELETE FROM purchase_order_lines WHERE po_id IN (SELECT id FROM purchase_orders WHERE vendor_id = $1 OR branch_id = $2)`, vend, yard)
		_, _ = db.Pool.Exec(ctx, `DELETE FROM purchase_orders WHERE vendor_id = $1 OR branch_id = $2`, vend, yard)
		_, _ = db.Pool.Exec(ctx, `DELETE FROM products WHERE id = $1`, prod)
		_, _ = db.Pool.Exec(ctx, `DELETE FROM vendors WHERE id = $1`, vend)
		_, _ = db.Pool.Exec(ctx, `DELETE FROM locations WHERE id = $1`, yard)
	})

	svc := NewService(NewRepository(db), db, nil, nil,
		product.NewService(product.NewRepository(db)), vendor.NewService(vendor.NewRepository(db)))
	sch := newSchedulerWithSettings(svc, &fakeSettings{})
	sch.dryRun = false
	sch.runCreateReorders(branchctx.WithSystem(ctx))

	var status, msg string
	var skipped int
	if err := db.Pool.QueryRow(ctx,
		`SELECT status, COALESCE(error_message, ''), products_skipped FROM reorder_runs
		  WHERE job = 'create_reorders' ORDER BY started_at DESC LIMIT 1`).Scan(&status, &msg, &skipped); err != nil {
		t.Fatalf("read run: %v", err)
	}
	if status != "SUCCESS" || !strings.Contains(msg, sku) || !strings.Contains(msg, "no reorder quantity set") || skipped < 1 {
		t.Fatalf("run = status %q, message %q, skipped %d; want SUCCESS naming %s with \"no reorder quantity set\"", status, msg, skipped, sku)
	}
}
