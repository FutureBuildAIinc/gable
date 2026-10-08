// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package inventory_test

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gablelbm/gable/internal/inventory"
	"github.com/gablelbm/gable/internal/testutil"
	"github.com/gablelbm/gable/pkg/branchctx"
	"github.com/gablelbm/gable/pkg/middleware"
	"github.com/google/uuid"
)

// A stock route's body names locations that stand for branches (ADR 0007
// section 2.3): a caller bound to one branch may not adjust or move stock at
// another branch's location.
func TestStockRoutes_PayloadLocationBranchRule(t *testing.T) {
	db := testutil.RequireDB(t)
	ctx := context.Background()

	ownBranch, otherBranch, ownYard, otherYard := uuid.New(), uuid.New(), uuid.New(), uuid.New()
	for _, r := range []struct {
		id     uuid.UUID
		typ    string
		parent any
	}{{ownBranch, "BRANCH", nil}, {otherBranch, "BRANCH", nil}, {ownYard, "WAREHOUSE", ownBranch}, {otherYard, "WAREHOUSE", otherBranch}} {
		if _, err := db.Pool.Exec(ctx, `INSERT INTO locations (id, type, code, parent_id) VALUES ($1, $2, $3, $4)`,
			r.id, r.typ, "sr-"+r.id.String()[:8], r.parent); err != nil {
			t.Fatalf("seed location: %v", err)
		}
	}
	productID := uuid.New()
	if _, err := db.Pool.Exec(ctx, `INSERT INTO products (id, sku, description, uom_primary, base_price) VALUES ($1, $2, 'stock', 'PCS', 1)`,
		productID, "SR-"+productID.String()[:8]); err != nil {
		t.Fatalf("seed product: %v", err)
	}
	t.Cleanup(func() {
		_, _ = db.Pool.Exec(ctx, `DELETE FROM inventory WHERE product_id = $1`, productID)
		_, _ = db.Pool.Exec(ctx, `DELETE FROM products WHERE id = $1`, productID)
		_, _ = db.Pool.Exec(ctx, `DELETE FROM locations WHERE id IN ($1, $2)`, ownYard, otherYard)
		_, _ = db.Pool.Exec(ctx, `DELETE FROM locations WHERE id IN ($1, $2)`, ownBranch, otherBranch)
	})

	h := inventory.NewHandler(inventory.NewService(inventory.NewRepository(db))).WithBranchGuard(middleware.NewBranchGuard(db))
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)

	post := func(path, body string, bc *branchctx.Context) int {
		req := httptest.NewRequest("POST", path, strings.NewReader(body)).WithContext(branchctx.With(ctx, bc))
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		return rec.Code
	}
	bound := &branchctx.Context{UserSub: "u", BranchID: &ownBranch}
	admin := &branchctx.Context{UserSub: "boss", IsAdmin: true}
	adjust := func(loc uuid.UUID) string {
		return fmt.Sprintf(`{"product_id":%q,"location_id":%q,"quantity":5,"reason":"t"}`, productID, loc)
	}

	if got := post("/api/v1/inventory/adjust", adjust(otherYard), bound); got != http.StatusForbidden {
		t.Errorf("bound caller, foreign location: %d, want 403", got)
	}
	if got := post("/api/v1/inventory/adjust", adjust(ownYard), bound); got != http.StatusOK {
		t.Errorf("bound caller, own location: %d, want 200", got)
	}
	if got := post("/api/v1/inventory/adjust", adjust(otherYard), admin); got != http.StatusOK {
		t.Errorf("admin across branches, any location: %d, want 200", got)
	}

	move := func(from, to uuid.UUID) string {
		return fmt.Sprintf(`{"product_id":%q,"from_location_id":%q,"to_location_id":%q,"quantity":1,"reason":"t"}`, productID, from, to)
	}
	for name, body := range map[string]string{
		"foreign to foreign": move(otherYard, otherYard),
		"own to foreign":     move(ownYard, otherYard),
		"foreign to own":     move(otherYard, ownYard),
	} {
		if got := post("/api/v1/inventory/transfer", body, bound); got != http.StatusForbidden {
			t.Errorf("bound caller, transfer %s: %d, want 403", name, got)
		}
	}
}
