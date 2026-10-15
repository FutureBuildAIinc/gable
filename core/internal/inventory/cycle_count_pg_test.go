// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package inventory_test

import (
	"context"
	"encoding/json"
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

// CORRECTNESS (item 130, N20): a cycle count counts by location. A yard that
// holds a product on its own row and in a zone is counted row by row: a count
// naming a location sets that row only, a count naming a location the product
// was not in adds it (a found pallet), and an absolute count that names no
// location on a yard holding the product in more than one location is refused
// in words, never guessed onto the oldest row (which turned a count of 410
// against 112 and 300 into a yard total of 710).
func TestCycleCount_CountsByLocationAndRefusesAGuess(t *testing.T) {
	db := testutil.RequireDB(t)
	ctx := context.Background()

	yard, zone, zone2, product := uuid.New(), uuid.New(), uuid.New(), uuid.New()
	exec := func(q string, args ...any) {
		t.Helper()
		if _, err := db.Pool.Exec(ctx, q, args...); err != nil {
			t.Fatalf("seed: %v\n%s", err, q)
		}
	}
	exec(`INSERT INTO locations (id, type, code, branch_id) VALUES ($1, 'BRANCH', $2, $1)`, yard, "CC-"+yard.String()[:8])
	exec(`INSERT INTO locations (id, type, code, parent_id, branch_id) VALUES ($1, 'ZONE', $2, $3, $3)`, zone, "CZ-"+zone.String()[:8], yard)
	exec(`INSERT INTO locations (id, type, code, parent_id, branch_id) VALUES ($1, 'ZONE', $2, $3, $3)`, zone2, "CY-"+zone2.String()[:8], yard)
	exec(`INSERT INTO products (id, sku, description, uom_primary, base_price) VALUES ($1, $2, 'count test', 'EA', 1)`, product, "CC-"+product.String()[:8])
	t.Cleanup(func() {
		bg := context.Background()
		_, _ = db.Pool.Exec(bg, `DELETE FROM inventory WHERE product_id = $1`, product)
		_, _ = db.Pool.Exec(bg, `DELETE FROM products WHERE id = $1`, product)
		_, _ = db.Pool.Exec(bg, `DELETE FROM locations WHERE id IN ($1, $2)`, zone, zone2)
		_, _ = db.Pool.Exec(bg, `DELETE FROM locations WHERE id = $1`, yard)
	})
	exec(`INSERT INTO inventory (product_id, location_id, location, quantity, allocated, created_at) VALUES ($1, $2, 'yard', 112, 0, now() - interval '1 hour')`, product, yard)
	exec(`INSERT INTO inventory (product_id, location_id, location, quantity, allocated) VALUES ($1, $2, 'zone', 300, 0)`, product, zone)

	mux := http.NewServeMux()
	inventory.NewHandler(inventory.NewService(inventory.NewRepository(db))).
		WithBranchGuard(middleware.NewBranchGuard(db)).RegisterRoutes(mux)
	bound := &branchctx.Context{UserSub: "u", BranchID: &yard}
	post := func(body string) (int, string) {
		req := httptest.NewRequest("POST", "/api/v1/inventory/adjust", strings.NewReader(body)).WithContext(branchctx.With(ctx, bound))
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		return rec.Code, rec.Body.String()
	}
	qty := func(loc uuid.UUID) (float64, bool) {
		var q float64
		err := db.Pool.QueryRow(ctx, `SELECT quantity::float8 FROM inventory WHERE product_id = $1 AND location_id = $2`, product, loc).Scan(&q)
		return q, err == nil
	}
	count := func(loc *uuid.UUID, n float64) string {
		if loc == nil {
			return fmt.Sprintf(`{"product_id":%q,"quantity":%g,"reason":"count","is_delta":false}`, product, n)
		}
		return fmt.Sprintf(`{"product_id":%q,"location_id":%q,"quantity":%g,"reason":"count","is_delta":false}`, product, *loc, n)
	}

	// A count naming the zone sets the zone's row and no other.
	if code, body := post(count(&zone, 298)); code != http.StatusOK {
		t.Fatalf("zone count = %d: %s", code, body)
	}
	if z, _ := qty(zone); z != 298 {
		t.Errorf("zone row = %g, want 298", z)
	}
	if y, _ := qty(yard); y != 112 {
		t.Errorf("yard row = %g, want 112 untouched", y)
	}
	// A count naming a location the product was not in adds it.
	if code, body := post(count(&zone2, 7)); code != http.StatusOK {
		t.Fatalf("found pallet count = %d: %s", code, body)
	}
	if z2, ok := qty(zone2); !ok || z2 != 7 {
		t.Errorf("found pallet row = %g (exists %v), want 7", z2, ok)
	}
	// An absolute count naming no location, on a yard holding the product in
	// three locations, is refused in words and writes nothing.
	code, body := post(count(nil, 410))
	if code != http.StatusUnprocessableEntity {
		t.Fatalf("count with no location = %d, want 422: %s", code, body)
	}
	var e struct {
		Error struct{ Code, Message string } `json:"error"`
	}
	if err := json.Unmarshal([]byte(body), &e); err != nil || e.Error.Code != "location_required" || !strings.Contains(e.Error.Message, "location") {
		t.Errorf("refusal = %s", body)
	}
	if y, _ := qty(yard); y != 112 {
		t.Errorf("yard row = %g after a refused count, want 112", y)
	}
}
