// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package pos_test

import (
	"context"
	"testing"

	"github.com/gablelbm/gable/internal/pos"
	"github.com/gablelbm/gable/internal/testutil"
	"github.com/google/uuid"
)

// The counter's on hand is the caller's yard's figure, counting every location
// under the yard as the inventory module's branch rule does, and the whole
// dealer's only when the request names no yard.
func TestCounterStockIsTheCallersYards(t *testing.T) {
	db := testutil.RequireDB(t)
	ctx := context.Background()
	repo := pos.NewRepository(db)
	exec := func(sql string, args ...any) {
		t.Helper()
		if _, err := db.Pool.Exec(ctx, sql, args...); err != nil {
			t.Fatalf("%s: %v", sql, err)
		}
	}
	yardA, yardB, binA1, binA2, binB := uuid.New(), uuid.New(), uuid.New(), uuid.New(), uuid.New()
	product := uuid.New()
	sku := "CSB-" + product.String()[:8]
	exec(`INSERT INTO locations (id, type, code, name) VALUES ($1, 'BRANCH', $2, 'stock yard A'), ($3, 'BRANCH', $4, 'stock yard B')`,
		yardA, "CSB-A-"+sku[4:], yardB, "CSB-B-"+sku[4:])
	exec(`INSERT INTO locations (id, type, code, parent_id, branch_id) VALUES
		($1, 'YARD', $4, $7, $7), ($2, 'YARD', $5, $7, $7), ($3, 'YARD', $6, $8, $8)`,
		binA1, binA2, binB, "CSB-A1-"+sku[4:], "CSB-A2-"+sku[4:], "CSB-B1-"+sku[4:], yardA, yardB)
	exec(`INSERT INTO products (id, sku, description, uom_primary, base_price, average_unit_cost) VALUES ($1, $2, 'counter stock test', 'PCS', 5.5, 3.25)`,
		product, sku)
	exec(`INSERT INTO inventory (product_id, location_id, location, quantity, allocated) VALUES
		($1, $2, 'A1', 100, 10), ($1, $3, 'A2', 50, 0), ($1, $4, 'B1', 7, 0)`, product, binA1, binA2, binB)
	t.Cleanup(func() {
		_, _ = db.Pool.Exec(ctx, `DELETE FROM inventory WHERE product_id = $1`, product)
		_, _ = db.Pool.Exec(ctx, `DELETE FROM products WHERE id = $1`, product)
		_, _ = db.Pool.Exec(ctx, `DELETE FROM locations WHERE id IN ($1, $2, $3)`, binA1, binA2, binB)
		_, _ = db.Pool.Exec(ctx, `DELETE FROM locations WHERE id IN ($1, $2)`, yardA, yardB)
	})

	// quantities are in ten thousandths on the wire
	const unit = 10000
	search := func(c context.Context) int64 {
		t.Helper()
		got, err := repo.SearchProducts(c, sku, 5)
		if err != nil || len(got) != 1 {
			t.Fatalf("search: %v, %d rows", err, len(got))
		}
		return int64(got[0].InStock)
	}
	catalog := func(c context.Context) int64 {
		t.Helper()
		all, err := repo.GetProductCatalog(c)
		if err != nil {
			t.Fatal(err)
		}
		for _, p := range all {
			if p.ProductID == product {
				return int64(p.InStock)
			}
		}
		t.Fatal("product missing from the catalogue")
		return 0
	}
	for name, read := range map[string]func(context.Context) int64{"search": search, "catalogue": catalog} {
		if got := read(pinnedTo(yardA)); got != 140*unit {
			t.Errorf("%s at yard A = %d, want 140 (both its locations, net of allocated)", name, got)
		}
		if got := read(pinnedTo(yardB)); got != 7*unit {
			t.Errorf("%s at yard B = %d, want 7", name, got)
		}
		if got := read(ctx); got != 147*unit {
			t.Errorf("%s with no yard = %d, want the whole dealer's 147", name, got)
		}
	}
}
