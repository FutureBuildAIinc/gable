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

// countWorld is a yard, optionally a zone under it, and one product.
type countWorld struct {
	t                  *testing.T
	yard, zone, prod   uuid.UUID
	mux                *http.ServeMux
	count              func(bc *branchctx.Context, body string) (int, string)
	rows, onYard, sum  func() float64
	cleanupGrantedSubs []string
}

func newCountWorld(t *testing.T) *countWorld {
	t.Helper()
	db := testutil.RequireDB(t)
	ctx := context.Background()
	w := &countWorld{t: t, yard: uuid.New(), zone: uuid.New(), prod: uuid.New()}
	exec := func(q string, args ...any) {
		t.Helper()
		if _, err := db.Pool.Exec(ctx, q, args...); err != nil {
			t.Fatalf("seed: %v\n%s", err, q)
		}
	}
	exec(`INSERT INTO locations (id, type, code, branch_id) VALUES ($1, 'BRANCH', $2, $1)`, w.yard, "CX-"+w.yard.String()[:8])
	exec(`INSERT INTO locations (id, type, code, parent_id, branch_id) VALUES ($1, 'ZONE', $2, $3, $3)`, w.zone, "CZ-"+w.zone.String()[:8], w.yard)
	exec(`INSERT INTO products (id, sku, description, uom_primary, base_price) VALUES ($1, $2, 'count ctx', 'EA', 1)`, w.prod, "CX-"+w.prod.String()[:8])
	t.Cleanup(func() {
		bg := context.Background()
		_, _ = db.Pool.Exec(bg, `DELETE FROM user_locations WHERE branch_id = $1`, w.yard)
		_, _ = db.Pool.Exec(bg, `DELETE FROM inventory WHERE product_id = $1`, w.prod)
		_, _ = db.Pool.Exec(bg, `DELETE FROM products WHERE id = $1`, w.prod)
		_, _ = db.Pool.Exec(bg, `DELETE FROM locations WHERE id = $1`, w.zone)
		_, _ = db.Pool.Exec(bg, `DELETE FROM locations WHERE id = $1`, w.yard)
	})
	w.mux = http.NewServeMux()
	inventory.NewHandler(inventory.NewService(inventory.NewRepository(db))).
		WithBranchGuard(middleware.NewBranchGuard(db)).RegisterRoutes(w.mux)
	w.count = func(bc *branchctx.Context, body string) (int, string) {
		req := httptest.NewRequest("POST", "/api/v1/inventory/adjust", strings.NewReader(body))
		if bc != nil {
			req = req.WithContext(branchctx.With(ctx, bc))
		}
		rec := httptest.NewRecorder()
		w.mux.ServeHTTP(rec, req)
		return rec.Code, rec.Body.String()
	}
	w.rows = func() float64 {
		var n float64
		_ = db.Pool.QueryRow(ctx, `SELECT count(*) FROM inventory WHERE product_id = $1`, w.prod).Scan(&n)
		return n
	}
	w.onYard = func() float64 {
		var q float64
		_ = db.Pool.QueryRow(ctx, `SELECT COALESCE(sum(quantity),0)::float8 FROM inventory WHERE product_id = $1 AND location_id = $2`, w.prod, w.yard).Scan(&q)
		return q
	}
	w.sum = func() float64 {
		var q float64
		_ = db.Pool.QueryRow(ctx, `SELECT COALESCE(sum(quantity),0)::float8 FROM inventory WHERE product_id = $1`, w.prod).Scan(&q)
		return q
	}
	return w
}

func (w *countWorld) stock(loc uuid.UUID, qty float64, allocated float64, oldest bool) {
	w.t.Helper()
	db := testutil.RequireDB(w.t)
	when := "now()"
	if oldest {
		when = "now() - interval '1 hour'"
	}
	if _, err := db.Pool.Exec(context.Background(),
		`INSERT INTO inventory (product_id, location_id, location, quantity, allocated, created_at) VALUES ($1, $2, 'row', $3, $4, `+when+`)`,
		w.prod, loc, qty, allocated); err != nil {
		w.t.Fatalf("stock: %v", err)
	}
}

func (w *countWorld) countBody(qty float64, isDelta bool) string {
	return fmt.Sprintf(`{"product_id":%q,"quantity":%g,"reason":"count","is_delta":%v}`, w.prod, qty, isDelta)
}

// CORRECTNESS (item 130, N20, review round 2): an absolute count that names no
// location is never written where the caller has no branch context, even for a
// product held in one location: it would create a location-less row beside the
// real one (50 counted as 45 became 95). The same count from a caller bound to
// the yard, which holds the product in one row, still lands on that row.
func TestCount_NoLocationIsRefusedWithoutABranchContext(t *testing.T) {
	granted := "u-granted-" + uuid.NewString()[:8]
	callers := map[string]func(w *countWorld) *branchctx.Context{
		"administrator with no header": func(*countWorld) *branchctx.Context {
			return &branchctx.Context{UserSub: "boss", IsAdmin: true}
		},
		"unbound key": func(*countWorld) *branchctx.Context { return nil },
		"granted user with no header": func(w *countWorld) *branchctx.Context {
			db := testutil.RequireDB(w.t)
			if _, err := db.Pool.Exec(context.Background(),
				`INSERT INTO user_locations (user_sub, branch_id) VALUES ($1, $2)`, granted, w.yard); err != nil {
				w.t.Fatalf("grant: %v", err)
			}
			return &branchctx.Context{UserSub: granted}
		},
	}
	for name, mk := range callers {
		t.Run(name, func(t *testing.T) {
			w := newCountWorld(t)
			w.stock(w.yard, 50, 0, true)
			code, body := w.count(mk(w), w.countBody(45, false))
			if code != http.StatusUnprocessableEntity {
				t.Fatalf("count with no location = %d, want 422: %s", code, body)
			}
			var e struct {
				Error struct{ Code string } `json:"error"`
			}
			if json.Unmarshal([]byte(body), &e) != nil || e.Error.Code != "location_required" {
				t.Errorf("refusal = %s", body)
			}
			if w.rows() != 1 || w.sum() != 50 {
				t.Errorf("rows = %g, total = %g after a refused count, want 1 row of 50", w.rows(), w.sum())
			}
		})
	}
	t.Run("caller bound to the yard, one row", func(t *testing.T) {
		w := newCountWorld(t)
		w.stock(w.yard, 50, 0, true)
		if code, body := w.count(&branchctx.Context{UserSub: "u", BranchID: &w.yard}, w.countBody(45, false)); code != http.StatusOK {
			t.Fatalf("bound count = %d: %s", code, body)
		}
		if w.sum() != 45 || w.rows() != 1 {
			t.Errorf("total = %g over %g rows, want 45 over 1", w.sum(), w.rows())
		}
	})
}

// A delta naming no location that the oldest row cannot take is refused in
// words (422), never a bare 500, though the yard as a whole holds enough.
func TestAdjust_ADeltaThatTheOldestRowCannotTakeIsRefusedInWords(t *testing.T) {
	w := newCountWorld(t)
	w.stock(w.yard, 0, 0, true)
	w.stock(w.zone, 300, 0, false)
	code, body := w.count(&branchctx.Context{UserSub: "u", BranchID: &w.yard}, w.countBody(-5, true))
	if code != http.StatusUnprocessableEntity {
		t.Fatalf("delta = %d, want 422: %s", code, body)
	}
	var e struct {
		Error struct{ Code, Message string } `json:"error"`
	}
	if err := json.Unmarshal([]byte(body), &e); err != nil || e.Error.Code != "insufficient_stock" || !strings.Contains(e.Error.Message, "location") {
		t.Errorf("refusal = %s", body)
	}
	if w.sum() != 300 {
		t.Errorf("total = %g after a refused delta, want 300", w.sum())
	}
}

// A count below the allocated figure is refused in words before it is
// written: allocated stock is promised to orders.
func TestCount_BelowTheAllocatedFigureIsRefusedInWords(t *testing.T) {
	w := newCountWorld(t)
	w.stock(w.yard, 50, 40, true)
	body := fmt.Sprintf(`{"product_id":%q,"location_id":%q,"quantity":10,"reason":"count","is_delta":false}`, w.prod, w.yard)
	code, out := w.count(&branchctx.Context{UserSub: "u", BranchID: &w.yard}, body)
	if code != http.StatusUnprocessableEntity {
		t.Fatalf("count under allocated = %d, want 422: %s", code, out)
	}
	var e struct {
		Error struct{ Code, Message string } `json:"error"`
	}
	if err := json.Unmarshal([]byte(out), &e); err != nil || e.Error.Code != "below_allocated" || !strings.Contains(e.Error.Message, "allocated") {
		t.Errorf("refusal = %s", out)
	}
	if w.sum() != 50 {
		t.Errorf("total = %g after a refused count, want 50", w.sum())
	}
}
