// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package serve

// The pricing walls drive their routes with the production composition: the
// role guard, the branch middleware (P1-1, review r1 argon: without it
// BranchFromContext is nil on every pricing route, so CheckPayloadBranch
// refuses every branch-checked write for human users and BranchIDForQuery
// never answers on reads), and the handler. The calculate read answers the
// counter's roles (P1-4), and the query branch_id passes the payload branch
// rule.

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gablelbm/gable/internal/customer"
	"github.com/gablelbm/gable/internal/pricing"
	"github.com/gablelbm/gable/internal/product"
	"github.com/gablelbm/gable/internal/testutil"
	"github.com/gablelbm/gable/pkg/middleware"
	"github.com/gablelbm/gable/pkg/outbox"
	"github.com/google/uuid"
)

func newPricingWallFixture(t *testing.T) (*branchWall, func(method, path, role, sub, branch string, body string) (int, []byte)) {
	t.Helper()
	testutil.LockOutboxTables(t)
	db := testutil.RequireDB(t)
	t.Setenv("AUTH_MODE", "dev")
	// The branch middleware reads the switch at construction.
	setSetting(t, db, "multi_branch_enabled", "true")

	wall := newBranchWall(db)
	customerSvc := customer.NewService(customer.NewRepository(db)).WithTxRunner(db)
	productSvc := product.NewService(product.NewRepository(db))
	pricingRepo := pricing.NewRepository(db)
	resolveRepo := pricing.NewResolveRepository(db)
	pricingSvc := pricing.NewService(pricingRepo).
		WithRepository(resolveRepo).
		WithTxRunner(db).
		WithOutbox(outbox.NewWriter(db, ""))
	catalogSvc := pricing.NewCatalogService(pricing.NewCatalogRepository(db)).
		WithTxRunner(db).
		WithOutbox(outbox.NewWriter(db, ""))

	mux := http.NewServeMux()
	pricing.NewHandler(pricingSvc, customerSvc, productSvc).
		WithBranchGuard(wall.guard).
		RegisterRoutes(mux, pricingRulesCustomerWall(wall.keyWall, wall.mw, db, "admin", "owner"))
	pricing.NewCatalogHandler(catalogSvc).
		WithEngine(pricingSvc).
		WithBranchGuard(wall.guard).
		RegisterCatalogRoutes(mux, pricingCatalogWall(wall.keyWall, wall.mw, db))

	// Claims travel inside the server (a context never crosses the wire):
	// the test names its role and subject with headers, exactly as the pin
	// fixture's pinClaims does.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if role := r.Header.Get("X-Test-Role"); role != "" {
			claims := &middleware.UserClaims{Role: role, Roles: []string{role}}
			claims.RegisteredClaims.Subject = r.Header.Get("X-Test-Sub")
			r = r.WithContext(context.WithValue(r.Context(), middleware.UserContextKey, claims))
		}
		mux.ServeHTTP(w, r)
	}))
	t.Cleanup(srv.Close)

	call := func(method, path, role, sub, branch, body string) (int, []byte) {
		req, err := http.NewRequest(method, srv.URL+path, strings.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Content-Type", "application/json")
		if role != "" {
			req.Header.Set("X-Test-Role", role)
			req.Header.Set("X-Test-Sub", sub)
		}
		if branch != "" {
			req.Header.Set("X-Branch-Id", branch)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		raw := make([]byte, 4096)
		n, _ := resp.Body.Read(raw)
		return resp.StatusCode, raw[:n]
	}
	return wall, call
}

// P1-1 and P1-4 (review r1 argon): with the branch middleware mounted, the
// calculate read answers the counter's roles and honours the payload branch
// rule on its query branch_id; the catalogue reads wall to the caller's
// context branch and the catalogue writes pass the payload rule.
func TestPricingWalls_CalculateRolesAndBranchPayload(t *testing.T) {
	wall, call := newPricingWallFixture(t)
	ctx := context.Background()
	branchA, branchB := uuid.New(), uuid.New()
	for _, b := range []uuid.UUID{branchA, branchB} {
		if _, err := wall.db.Pool.Exec(ctx,
			`INSERT INTO locations (id, type, code) VALUES ($1, 'BRANCH', $2)`, b, "pw-"+b.String()[:8]); err != nil {
			t.Fatalf("seed branch: %v", err)
		}
	}
	t.Cleanup(func() {
		_, _ = wall.db.Pool.Exec(ctx, `DELETE FROM locations WHERE id IN ($1, $2)`, branchA, branchB)
	})

	// P1-4: a sales user reaches the calculate read (currently 403: the
	// 5-role dispatch sat in pricingCatalogWall, which never mounts it).
	status, body := call("GET", "/api/v1/pricing/calculate?customer_id="+uuid.NewString()+"&product_id="+uuid.NewString(), "sales", "pw-sales", "", "")
	if status == http.StatusForbidden {
		t.Errorf("sales on calculate: 403 %s, want the module's own answer", body)
	}
	// An off-role user is still refused on the create (a granted header keeps
	// the branch middleware's own 400 out of the answer, so the refusal is
	// the role's).
	if status, body := call("POST", "/api/v1/pricing/rules", "sales", "pw-sales", branchA.String(), `{}`); status != http.StatusForbidden {
		t.Fatalf("sales on POST rules = %d, want 403; body: %s", status, body)
	}

	// P1-4: the query branch_id passes the payload branch rule. A user
	// granted only branch A naming branch B on the calculate read is refused.
	sub := "pw-user-" + uuid.NewString()[:6]
	if _, err := wall.db.Pool.Exec(ctx,
		`INSERT INTO user_locations (user_sub, branch_id, is_home, granted_by) VALUES ($1, $2, TRUE, 'test')`,
		sub, branchA); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = wall.db.Pool.Exec(ctx, `DELETE FROM user_locations WHERE user_sub = $1`, sub)
	})
	status, body = call("GET", "/api/v1/pricing/calculate?customer_id="+uuid.NewString()+"&product_id="+uuid.NewString()+"&branch_id="+branchB.String(),
		"sales", sub, branchA.String(), "")
	if status != http.StatusForbidden {
		t.Errorf("calculate with a foreign query branch = %d %s, want 403", status, body)
	}
	status, body = call("GET", "/api/v1/pricing/cost?vendor_id="+uuid.NewString()+"&product_id="+uuid.NewString()+"&branch_id="+branchB.String(),
		"finance", sub, branchA.String(), "")
	if status != http.StatusForbidden {
		t.Errorf("cost with a foreign query branch = %d %s, want 403, body was: %s", status, body, body)
	}

	// P1-1: an administrator working in branch A's context replaces branch A's
	// slice of a product's price list. Without the branch middleware the
	// context is nil and the payload rule refuses every human write.
	prod := uuid.New()
	if _, err := wall.db.Pool.Exec(ctx, `
		INSERT INTO products (id, sku, description, uom_primary, base_price)
		VALUES ($1, 'PW-' || substr(md5(random()::text), 1, 12), 'pricing wall product', 'EA', 10)`, prod); err != nil {
		t.Fatalf("seed product: %v", err)
	}
	var rev int64
	if err := wall.db.Pool.QueryRow(ctx, `SELECT revision FROM products WHERE id = $1`, prod).Scan(&rev); err != nil {
		t.Fatal(err)
	}
	status, body = call("PUT", "/api/v1/pricing/products/"+prod.String()+"/prices?branch_id="+branchA.String(),
		"admin", "pw-admin", branchA.String(),
		`{"prices":[{"unit_price_ten_thousandths":80000}],"revision":`+strings.TrimSpace(strings.ReplaceAll(revString(rev), `"`, ``))+`}`)
	if status != http.StatusOK {
		t.Errorf("admin's branch A slice PUT through the branch middleware = %d %s, want 200", status, body)
	}

	// P1-1: a finance user with X-Branch-Id=A reading the vendor costs sees
	// none of branch B's rows. Seed one for B and one for A.
	vendor := uuid.New()
	if _, err := wall.db.Pool.Exec(ctx,
		`INSERT INTO vendors (id, name) VALUES ($1, 'pw vendor') ON CONFLICT (id) DO NOTHING`, vendor); err != nil {
		t.Fatalf("seed vendor: %v", err)
	}
	var vendorLevel string
	if err := wall.db.Pool.QueryRow(ctx, `
		INSERT INTO vendor_price_levels (vendor_id, code, name, is_default)
		VALUES ($1, $2, 'pw list', TRUE) RETURNING id::text`,
		vendor, "PW"+strings.ToUpper(strings.ReplaceAll(uuid.NewString()[:6], "-", ""))).Scan(&vendorLevel); err != nil {
		t.Fatalf("seed vendor level: %v", err)
	}
	for _, bc := range []struct {
		id   uuid.UUID
		cost string
	}{{branchA, "1.0000"}, {branchB, "2.0000"}} {
		if _, err := wall.db.Pool.Exec(ctx, `
			INSERT INTO vendor_product_costs (vendor_id, vendor_price_level_id, product_id, branch_id, purchase_uom, unit_cost, effective_from)
			VALUES ($1, $2, $3, $4, 'EA', $5, '2020-01-01')`,
			vendor, vendorLevel, prod, bc.id, bc.cost); err != nil {
			t.Fatalf("seed cost: %v", err)
		}
	}
	status, body = call("GET", "/api/v1/pricing/vendor-costs?product_id="+prod.String(), "finance", sub, branchA.String(), "")
	if status != http.StatusOK {
		t.Fatalf("finance vendor costs read: %d %s", status, body)
	}
	if strings.Contains(string(body), branchB.String()) {
		t.Errorf("branch A finance user saw branch B's vendor cost row: %s", body)
	}
	if !strings.Contains(string(body), branchA.String()) {
		t.Errorf("branch A finance user missed their own vendor cost row: %s", body)
	}

	// P1-4: a branch A user reading branch B's contract by id reads it as
	// missing (the walled single-row reads answer the foreign row the way a
	// missing row answers).
	cust := uuid.New()
	if _, err := wall.db.Pool.Exec(ctx, `
		INSERT INTO customers (id, name, account_number, primary_branch_id)
		VALUES ($1, 'pw customer', $2, $3) ON CONFLICT (id) DO NOTHING`,
		cust, "PWC-"+cust.String()[:8], branchB); err != nil {
		t.Fatalf("seed customer: %v", err)
	}
	t.Cleanup(func() {
		_, _ = wall.db.Pool.Exec(ctx, `DELETE FROM customer_contracts WHERE customer_id = $1`, cust)
		_, _ = wall.db.Pool.Exec(ctx, `DELETE FROM customers WHERE id = $1`, cust)
		_, _ = wall.db.Pool.Exec(ctx, `DELETE FROM vendor_product_costs WHERE vendor_id = $1`, vendor)
		_, _ = wall.db.Pool.Exec(ctx, `DELETE FROM vendor_price_levels WHERE vendor_id = $1`, vendor)
		_, _ = wall.db.Pool.Exec(ctx, `DELETE FROM vendors WHERE id = $1`, vendor)
		_, _ = wall.db.Pool.Exec(ctx, `DELETE FROM product_prices WHERE product_id = $1`, prod)
		_, _ = wall.db.Pool.Exec(ctx, `DELETE FROM products WHERE id = $1`, prod)
	})
	status, _ = call("POST", "/api/v1/pricing/contracts",
		"admin", "pw-admin", branchB.String(),
		`{"customer_id":"`+cust.String()+`","product_id":"`+prod.String()+`","branch_id":"`+branchB.String()+`","contract_price_ten_thousandths":1}`)
	if status != http.StatusCreated {
		t.Fatalf("the admin's branch B contract: %d", status)
	}
	var contractID string
	if err := wall.db.Pool.QueryRow(ctx,
		`SELECT id::text FROM customer_contracts WHERE product_id = $1 AND branch_id = $2`, prod, branchB).Scan(&contractID); err != nil {
		t.Fatal(err)
	}
	status, _ = call("GET", "/api/v1/pricing/contracts/"+contractID, "finance", sub, branchA.String(), "")
	if status != http.StatusNotFound {
		t.Errorf("branch A user's read of branch B's contract = %d, want 404", status)
	}
	var vendorCostBID string
	if err := wall.db.Pool.QueryRow(ctx,
		`SELECT id::text FROM vendor_product_costs WHERE product_id = $1 AND branch_id = $2`, prod, branchB).Scan(&vendorCostBID); err != nil {
		t.Fatal(err)
	}
	status, _ = call("GET", "/api/v1/pricing/vendor-costs/"+vendorCostBID, "finance", sub, branchA.String(), "")
	if status != http.StatusNotFound {
		t.Errorf("branch A user's read of branch B's vendor cost = %d, want 404", status)
	}
}

func revString(rev int64) string {
	return pricingFormatRev(rev)
}

func pricingFormatRev(rev int64) string {
	digits := ""
	if rev == 0 {
		return "0"
	}
	for rev > 0 {
		digits = string(rune('0'+rev%10)) + digits
		rev /= 10
	}
	return `"` + digits + `"`
}
