// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package serve

// The branch wall through serve's own mount methods, the real role guards and
// the real BranchMiddleware (grants read from user_locations, the
// multi_branch_enabled switch read from system_settings). Only authentication
// is a stand-in: it turns two headers into the claims a JWT would carry. A
// route that loses its guard in wire_branch_wall.go, or loses the branch
// middleware, fails here.

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gablelbm/gable/internal/customer"
	"github.com/gablelbm/gable/internal/inventory"
	"github.com/gablelbm/gable/internal/location"
	"github.com/gablelbm/gable/internal/purchase_order"
	"github.com/gablelbm/gable/internal/quote"
	"github.com/gablelbm/gable/internal/testutil"
	"github.com/gablelbm/gable/pkg/database"
	"github.com/gablelbm/gable/pkg/middleware"
	"github.com/gablelbm/gable/pkg/outbox"
	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"
)

func setSetting(t *testing.T, db *database.DB, key, value string) {
	t.Helper()
	ctx := context.Background()
	var old *string
	_ = db.Pool.QueryRow(ctx, `SELECT value FROM system_settings WHERE key = $1`, key).Scan(&old)
	if _, err := db.Pool.Exec(ctx, `INSERT INTO system_settings (key, value) VALUES ($1, $2)
		ON CONFLICT (key) DO UPDATE SET value = EXCLUDED.value`, key, value); err != nil {
		t.Fatalf("set %s: %v", key, err)
	}
	t.Cleanup(func() {
		if old != nil {
			_, _ = db.Pool.Exec(ctx, `UPDATE system_settings SET value = $2 WHERE key = $1`, key, *old)
		}
	})
}

func asRole(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		role := r.Header.Get("X-Test-Role")
		claims := &middleware.UserClaims{RegisteredClaims: jwt.RegisteredClaims{Subject: r.Header.Get("X-Test-Sub")}, Role: role, Roles: []string{role}}
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), middleware.UserContextKey, claims)))
	})
}

type wallFixture struct {
	srv              *httptest.Server
	branchA, branchB uuid.UUID
	yardA, yardB     uuid.UUID
	productID        uuid.UUID
	db               *database.DB
}

func newWallFixture(t *testing.T, db *database.DB, multiBranch bool) *wallFixture {
	t.Helper()
	ctx := context.Background()
	f := &wallFixture{db: db, branchA: uuid.New(), branchB: uuid.New(), yardA: uuid.New(), yardB: uuid.New(), productID: uuid.New()}
	for _, r := range []struct {
		id     uuid.UUID
		typ    string
		parent any
	}{{f.branchA, "BRANCH", nil}, {f.branchB, "BRANCH", nil}, {f.yardA, "YARD", f.branchA}, {f.yardB, "YARD", f.branchB}} {
		if _, err := db.Pool.Exec(ctx, `INSERT INTO locations (id, type, code, parent_id) VALUES ($1, $2, $3, $4)`,
			r.id, r.typ, "wl-"+r.id.String()[:8], r.parent); err != nil {
			t.Fatalf("seed location: %v", err)
		}
	}
	if _, err := db.Pool.Exec(ctx, `INSERT INTO products (id, sku, description, uom_primary, base_price) VALUES ($1, $2, 'wall', 'PCS', 1)`,
		f.productID, "WL-"+f.productID.String()[:8]); err != nil {
		t.Fatalf("seed product: %v", err)
	}
	for _, sub := range []string{"u-a"} {
		if _, err := db.Pool.Exec(ctx, `INSERT INTO user_locations (user_sub, branch_id, is_home, granted_by) VALUES ($1, $2, TRUE, 'test')`, sub, f.branchA); err != nil {
			t.Fatalf("seed grant: %v", err)
		}
	}
	t.Cleanup(func() {
		_, _ = db.Pool.Exec(ctx, `DELETE FROM user_locations WHERE user_sub = 'u-a'`)
		_, _ = db.Pool.Exec(ctx, `DELETE FROM inventory WHERE product_id = $1`, f.productID)
		_, _ = db.Pool.Exec(ctx, `DELETE FROM products WHERE id = $1`, f.productID)
		_, _ = db.Pool.Exec(ctx, `DELETE FROM locations WHERE parent_id IN ($1, $2)`, f.branchA, f.branchB)
		_, _ = db.Pool.Exec(ctx, `DELETE FROM locations WHERE id IN ($1, $2)`, f.branchA, f.branchB)
		_, _ = db.Pool.Exec(ctx, `DELETE FROM locations WHERE name = 'wall branch'`)
	})

	setSetting(t, db, "multi_branch_enabled", fmt.Sprint(multiBranch))
	setSetting(t, db, "default_branch_required", "false")
	wall := newBranchWall(db) // reads the settings just written

	mux := http.NewServeMux()
	wall.locations(mux, location.NewHandler(location.NewService(location.NewRepository(db)), location.NewUserRepository(db), middleware.RequireRole("admin", "owner")))
	wall.inventory(mux, inventory.NewService(inventory.NewRepository(db)))
	wall.customers(mux, customer.NewService(customer.NewRepository(db)).WithOutbox(outbox.NewWriter(db, "")).WithTxRunner(db))
	wall.quotes(mux, quote.NewService(quote.NewRepository(db)).WithOutbox(outbox.NewWriter(db, "")).WithTxRunner(db))
	wall.purchaseOrders(mux, purchase_order.NewHandler(purchase_order.NewService(purchase_order.NewRepository(db), db, nil, nil, nil, nil), nil))
	f.srv = httptest.NewServer(asRole(mux))
	t.Cleanup(f.srv.Close)
	return f
}

// call sends one request as role/sub, with an optional X-Branch-Id.
func (f *wallFixture) call(t *testing.T, method, path, body, role, sub, branchHeader string) int {
	t.Helper()
	req, err := http.NewRequest(method, f.srv.URL+path, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Test-Role", role)
	req.Header.Set("X-Test-Sub", sub)
	if branchHeader != "" {
		req.Header.Set("X-Branch-Id", branchHeader)
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	return res.StatusCode
}

func TestBranchWall_ServeWiring(t *testing.T) {
	db := testutil.RequireDB(t)
	f := newWallFixture(t, db, true)
	A, B := f.branchA.String(), f.branchB.String()

	adjust := func(loc uuid.UUID) string {
		return fmt.Sprintf(`{"product_id":%q,"location_id":%q,"quantity":5,"reason":"t"}`, f.productID, loc)
	}
	transfer := func(from, to uuid.UUID) string {
		return fmt.Sprintf(`{"product_id":%q,"from_location_id":%q,"to_location_id":%q,"quantity":1,"reason":"t"}`, f.productID, from, to)
	}
	quoteBody := func(branch string) string {
		return fmt.Sprintf(`{"branch_id":%q,"customer_id":%q,"delivery_type":"pickup","lines":[{"product_id":%q,"sku":"wall","description":"x","quantity":"1","uom":"PCS","unit_price_ten_thousandths":100}]}`, branch, uuid.New(), f.productID)
	}
	receive := func(loc uuid.UUID) string {
		return fmt.Sprintf(`{"lines":[{"line_id":%q,"qty_received":1,"location_id":%q}]}`, uuid.New(), loc)
	}
	create := func(typ string, parent any) string {
		p := "null"
		if id, ok := parent.(uuid.UUID); ok {
			p = fmt.Sprintf("%q", id)
		}
		return fmt.Sprintf(`{"type":%q,"code":"c-%s","name":"wall branch","parent_id":%s}`, typ, uuid.NewString()[:8], p)
	}
	const ok = http.StatusOK
	const no = http.StatusForbidden

	// Stock: a warehouse user granted only A.
	for _, c := range []struct {
		name, path, body, header string
		want                     int
	}{
		{"adjust foreign yard, header A", "/api/v1/inventory/adjust", adjust(f.yardB), A, no},
		{"adjust foreign yard, no header", "/api/v1/inventory/adjust", adjust(f.yardB), "", no},
		{"adjust own yard", "/api/v1/inventory/adjust", adjust(f.yardA), A, ok},
		{"transfer own to foreign", "/api/v1/inventory/transfer", transfer(f.yardA, f.yardB), A, no},
		{"transfer foreign to own", "/api/v1/inventory/transfer", transfer(f.yardB, f.yardA), A, no},
		{"transfer foreign to foreign", "/api/v1/inventory/transfer", transfer(f.yardB, f.yardB), A, no},
	} {
		if got := f.call(t, "POST", c.path, c.body, "warehouse", "u-a", c.header); got != c.want {
			t.Errorf("warehouse %s: %d, want %d", c.name, got, c.want)
		}
	}

	// Quotes: a sales user granted only A.
	if got := f.call(t, "POST", "/api/v1/quotes", quoteBody(B), "sales", "u-a", A); got != no {
		t.Errorf("sales quote at foreign branch, header A: %d, want 403", got)
	}
	if got := f.call(t, "POST", "/api/v1/quotes", quoteBody(B), "sales", "u-a", ""); got != no {
		t.Errorf("sales quote at ungranted branch, no header: %d, want 403", got)
	}

	// Customers: a sales user granted only A may not create a customer at B.
	customerBody := func(branch string) string {
		return fmt.Sprintf(`{"account_number":"WALL-%s","name":"wall","primary_branch_id":%q}`, uuid.NewString()[:8], branch)
	}
	t.Cleanup(func() {
		_, _ = db.Pool.Exec(context.Background(), `DELETE FROM customer_branches WHERE branch_id IN ($1, $2)`, f.branchA, f.branchB)
		_, _ = db.Pool.Exec(context.Background(), `DELETE FROM events_outbox WHERE entity_type = 'customer' AND branch_id IN ($1, $2)`, f.branchA, f.branchB)
		_, _ = db.Pool.Exec(context.Background(), `DELETE FROM customers WHERE account_number LIKE 'WALL-%'`)
	})
	if got := f.call(t, "POST", "/api/v1/customers", customerBody(B), "sales", "u-a", A); got != no {
		t.Errorf("sales customer at foreign branch, header A: %d, want 403", got)
	}
	if got := f.call(t, "POST", "/api/v1/customers", customerBody(B), "sales", "u-a", ""); got != no {
		t.Errorf("sales customer at ungranted branch, no header: %d, want 403", got)
	}
	if got := f.call(t, "POST", "/api/v1/customers", customerBody(A), "sales", "u-a", A); got != http.StatusCreated {
		t.Errorf("sales customer at own branch: %d, want 201", got)
	}
	if got := f.call(t, "POST", "/api/v1/customers", customerBody(B), "admin", "boss", ""); got != http.StatusCreated {
		t.Errorf("admin customer at any branch: %d, want 201", got)
	}

	// Purchase order receipt: refused for a granted user, passed to the module
	// (which then finds no such PO) for the user within grants and for an admin.
	if got := f.call(t, "POST", "/api/v1/purchase-orders/"+uuid.NewString()+"/receive", receive(f.yardB), "purchasing", "u-a", A); got != no {
		t.Errorf("purchasing receive into foreign yard: %d, want 403", got)
	}
	if got := f.call(t, "POST", "/api/v1/purchase-orders/"+uuid.NewString()+"/receive", receive(f.yardA), "purchasing", "u-a", A); got == no {
		t.Errorf("purchasing receive into own yard refused")
	}
	if got := f.call(t, "POST", "/api/v1/purchase-orders/"+uuid.NewString()+"/receive", receive(f.yardB), "admin", "boss", ""); got == no {
		t.Errorf("admin receive into any yard refused")
	}

	// Location create: behind the branch middleware, parent_id held to the
	// caller's branches, BRANCH for admin or owner only.
	for _, role := range []string{"warehouse", "sales"} {
		if got := f.call(t, "POST", "/api/v1/locations", create("BIN", f.yardB), role, "u-a", A); got != no {
			t.Errorf("%s create under a foreign branch, header A: %d, want 403", role, got)
		}
		if got := f.call(t, "POST", "/api/v1/locations", create("BIN", f.branchB), role, "u-a", ""); got != no {
			t.Errorf("%s create under an ungranted branch, no header: %d, want 403", role, got)
		}
		if got := f.call(t, "POST", "/api/v1/locations", create("BIN", f.branchA), role, "u-a", A); got != http.StatusCreated {
			t.Errorf("%s create under own branch: %d, want 201", role, got)
		}
		if got := f.call(t, "POST", "/api/v1/locations", create("BRANCH", nil), role, "u-a", A); got != no {
			t.Errorf("%s create type BRANCH: %d, want 403", role, got)
		}
	}
	if got := f.call(t, "POST", "/api/v1/locations", create("BIN", f.branchB), "admin", "boss", ""); got != http.StatusCreated {
		t.Errorf("admin create under any branch: %d, want 201", got)
	}
	if got := f.call(t, "POST", "/api/v1/locations", create("BRANCH", nil), "admin", "boss", ""); got != http.StatusCreated {
		t.Errorf("admin create type BRANCH: %d, want 201", got)
	}
}

// With multi_branch_enabled off the deployment is single branch and the
// middleware treats every caller as an administrator with no context branch,
// so a bound caller reaches any location (the intended single branch
// behaviour, not a hole: there is one branch).
func TestBranchWall_SwitchOffAdmitsBoundCaller(t *testing.T) {
	db := testutil.RequireDB(t)
	f := newWallFixture(t, db, false)
	body := fmt.Sprintf(`{"product_id":%q,"location_id":%q,"quantity":5,"reason":"t"}`, f.productID, f.yardB)
	if got := f.call(t, "POST", "/api/v1/inventory/adjust", body, "warehouse", "u-a", f.branchA.String()); got != http.StatusOK {
		t.Errorf("switch off, bound warehouse caller adjusting another branch's yard: %d, want 200", got)
	}
}
