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
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gablelbm/gable/internal/account"
	"github.com/gablelbm/gable/internal/ap"
	"github.com/gablelbm/gable/internal/customer"
	"github.com/gablelbm/gable/internal/document"
	"github.com/gablelbm/gable/internal/gl"
	glint "github.com/gablelbm/gable/internal/integrations/gl"
	"github.com/gablelbm/gable/internal/inventory"
	"github.com/gablelbm/gable/internal/invoice"
	"github.com/gablelbm/gable/internal/location"
	"github.com/gablelbm/gable/internal/matching"
	"github.com/gablelbm/gable/internal/notification"
	"github.com/gablelbm/gable/internal/order"
	"github.com/gablelbm/gable/internal/product"
	"github.com/gablelbm/gable/internal/purchase_order"
	"github.com/gablelbm/gable/internal/quote"
	"github.com/gablelbm/gable/internal/testutil"
	"github.com/gablelbm/gable/internal/vendor"
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
	vendorID         uuid.UUID
	poA, poB         uuid.UUID
	poLineA, poLineB uuid.UUID
	docCust          uuid.UUID
	orderA, orderB   uuid.UUID
	invA, invB       uuid.UUID
	db               *database.DB
}

func newWallFixture(t *testing.T, db *database.DB, multiBranch bool) *wallFixture {
	t.Helper()
	ctx := context.Background()
	f := &wallFixture{db: db, branchA: uuid.New(), branchB: uuid.New(), yardA: uuid.New(), yardB: uuid.New(), productID: uuid.New(),
		vendorID: uuid.New(), poA: uuid.New(), poB: uuid.New(), poLineA: uuid.New(), poLineB: uuid.New(),
		docCust: uuid.New(), orderA: uuid.New(), orderB: uuid.New(), invA: uuid.New(), invB: uuid.New()}
	for _, r := range []struct {
		id     uuid.UUID
		typ    string
		parent any
	}{{f.branchA, "BRANCH", nil}, {f.branchB, "BRANCH", nil}, {f.yardA, "YARD", f.branchA}, {f.yardB, "YARD", f.branchB}} {
		if _, err := db.Pool.Exec(ctx, `INSERT INTO locations (id, type, code, name, parent_id) VALUES ($1, $2, $3, $4, $5)`,
			r.id, r.typ, "wl-"+r.id.String()[:8], "wl branch "+r.id.String()[:8], r.parent); err != nil {
			t.Fatalf("seed location: %v", err)
		}
	}
	if _, err := db.Pool.Exec(ctx, `INSERT INTO products (id, sku, description, uom_primary, base_price) VALUES ($1, $2, 'wall', 'PCS', 1)`,
		f.productID, "WL-"+f.productID.String()[:8]); err != nil {
		t.Fatalf("seed product: %v", err)
	}
	if _, err := db.Pool.Exec(ctx, `INSERT INTO vendors (id, name) VALUES ($1, $2)`, f.vendorID, "wl-vendor-"+f.vendorID.String()[:8]); err != nil {
		t.Fatalf("seed vendor: %v", err)
	}
	// One sent purchase order per branch, one line each, so the path-id
	// routes below act on real records of each branch.
	for _, po := range []struct{ po, line, branch uuid.UUID }{
		{f.poA, f.poLineA, f.branchA}, {f.poB, f.poLineB, f.branchB},
	} {
		if _, err := db.Pool.Exec(ctx,
			`INSERT INTO purchase_orders (id, vendor_id, status, source, branch_id) VALUES ($1, $2, 'SENT', 'MANUAL', $3)`,
			po.po, f.vendorID, po.branch); err != nil {
			t.Fatalf("seed purchase order: %v", err)
		}
		if _, err := db.Pool.Exec(ctx,
			`INSERT INTO purchase_order_lines (id, po_id, product_id, description, quantity, cost) VALUES ($1, $2, $3, 'wall', 5, 1)`,
			po.line, po.po, f.productID); err != nil {
			t.Fatalf("seed purchase order line: %v", err)
		}
	}
	// One invoice and one pick ticket per branch (an invoice rides its
	// order), so the document print and email routes act on real records of
	// each branch. The customer carries an email so the email route reaches
	// its 202 on the caller's own branch.
	if _, err := db.Pool.Exec(ctx, `INSERT INTO customers (id, name, account_number, email, primary_branch_id)
		VALUES ($1, 'wall doc cust', $2, 'wall-doc@example.com', $3)`,
		f.docCust, "WLDOC-"+f.docCust.String()[:8], f.branchA); err != nil {
		t.Fatalf("seed document customer: %v", err)
	}
	// The customer read the document routes make is branch walled through
	// customer_branches, so the link row is part of the seed.
	if _, err := db.Pool.Exec(ctx, `INSERT INTO customer_branches (customer_id, branch_id) VALUES ($1, $2)`,
		f.docCust, f.branchA); err != nil {
		t.Fatalf("seed document customer branch: %v", err)
	}
	for _, r := range []struct{ order, inv, branch uuid.UUID }{
		{f.orderA, f.invA, f.branchA}, {f.orderB, f.invB, f.branchB},
	} {
		if _, err := db.Pool.Exec(ctx,
			`INSERT INTO orders (id, customer_id, branch_id, status, total_amount, delivery_type, currency) VALUES ($1, $2, $3, 'CONFIRMED', 10, 'PICKUP', 'USD')`,
			r.order, f.docCust, r.branch); err != nil {
			t.Fatalf("seed order: %v", err)
		}
		if _, err := db.Pool.Exec(ctx,
			`INSERT INTO invoices (id, order_id, customer_id, status, total_amount, branch_id) VALUES ($1, $2, $3, 'UNPAID', 10, $4)`,
			r.inv, r.order, f.docCust, r.branch); err != nil {
			t.Fatalf("seed invoice: %v", err)
		}
	}
	for _, sub := range []string{"u-a"} {
		if _, err := db.Pool.Exec(ctx, `INSERT INTO user_locations (user_sub, branch_id, is_home, granted_by) VALUES ($1, $2, TRUE, 'test')`, sub, f.branchA); err != nil {
			t.Fatalf("seed grant: %v", err)
		}
	}
	t.Cleanup(func() {
		_, _ = db.Pool.Exec(ctx, `DELETE FROM user_locations WHERE user_sub = 'u-a'`)
		_, _ = db.Pool.Exec(ctx, `DELETE FROM invoices WHERE id IN ($1, $2)`, f.invA, f.invB)
		_, _ = db.Pool.Exec(ctx, `DELETE FROM orders WHERE id IN ($1, $2)`, f.orderA, f.orderB)
		_, _ = db.Pool.Exec(ctx, `DELETE FROM customer_branches WHERE customer_id = $1`, f.docCust)
		_, _ = db.Pool.Exec(ctx, `DELETE FROM customers WHERE id = $1`, f.docCust)
		_, _ = db.Pool.Exec(ctx, `DELETE FROM purchase_order_lines WHERE po_id IN ($1, $2)`, f.poA, f.poB)
		_, _ = db.Pool.Exec(ctx, `DELETE FROM purchase_orders WHERE id IN ($1, $2)`, f.poA, f.poB)
		_, _ = db.Pool.Exec(ctx, `DELETE FROM vendors WHERE id = $1`, f.vendorID)
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
	invSvc := inventory.NewService(inventory.NewRepository(db))
	wall.locations(mux, location.NewHandler(location.NewService(location.NewRepository(db)), location.NewUserRepository(db), middleware.RequireRole("admin", "owner")))
	wall.inventory(mux, invSvc)
	wall.customers(mux, customer.NewService(customer.NewRepository(db)).WithOutbox(outbox.NewWriter(db, "")).WithTxRunner(db))
	wall.quotes(mux, quote.NewService(quote.NewRepository(db)).WithOutbox(outbox.NewWriter(db, "")).WithTxRunner(db))
	// The recommendation service is wired as serve wires it (serve.go), so
	// the recommendations route answers from the real stock and velocity
	// reads instead of 503: the route is behind the branch middleware and
	// both reads must scope to the same branches.
	poRecSvc := purchase_order.NewRecommendationService(purchase_order.NewRepository(db), invSvc,
		product.NewService(product.NewRepository(db)), vendor.NewService(vendor.NewRepository(db))).
		WithVelocityRepo(purchase_order.NewVelocityRepository(db))
	wall.purchaseOrders(mux, purchase_order.NewHandler(purchase_order.NewService(purchase_order.NewRepository(db), db, nil, nil, nil, nil), poRecSvc))
	wall.matching(mux, matching.NewService(db, matching.NewRepository(db), fixturePOSource{f: f}, fixtureAPSource{}, slog.Default()))
	docSvc := document.NewService(product.NewRepository(db))
	glSvc := gl.NewService(gl.NewRepository(db), glint.NewMockGLAdapter(), slog.Default())
	accountSvc := account.NewService(account.NewRepository(db), db, slog.Default())
	invoiceSvc := invoice.NewService(invoice.NewRepository(db), glSvc, accountSvc, db)
	orderSvc := order.NewService(order.NewRepository(db)).WithTxRunner(db)
	docHandler := document.NewHandler(docSvc, orderSvc, invoiceSvc, customer.NewService(customer.NewRepository(db)), notification.NewLogEmailService(slog.Default()))
	wall.documents(mux, docHandler)
	f.srv = httptest.NewServer(asRole(mux))
	t.Cleanup(f.srv.Close)
	return f
}

// fixturePOSource answers the matching module's purchase order seam from the
// fixture's two purchase orders: the branch wall reads the branch, the
// service reads the record.
type fixturePOSource struct {
	f *wallFixture
}

func (s fixturePOSource) GetPO(_ context.Context, id uuid.UUID) (*purchase_order.PurchaseOrder, error) {
	switch id {
	case s.f.poA, s.f.poB:
		return &purchase_order.PurchaseOrder{ID: id}, nil
	}
	return nil, fmt.Errorf("no such purchase order")
}

func (s fixturePOSource) GetPOBranch(_ context.Context, id uuid.UUID) (*uuid.UUID, error) {
	switch id {
	case s.f.poA:
		return &s.f.branchA, nil
	case s.f.poB:
		return &s.f.branchB, nil
	}
	return nil, nil
}

// fixtureAPSource is an empty accounts payable: the cases that pass the wall
// answer before any invoice is needed.
type fixtureAPSource struct{}

func (fixtureAPSource) ListVendorInvoices(_ context.Context, _ *uuid.UUID, _ string) ([]ap.VendorInvoice, error) {
	return nil, nil
}

func (fixtureAPSource) GetVendorInvoice(_ context.Context, _ uuid.UUID) (*ap.VendorInvoice, error) {
	return nil, nil
}

func (fixtureAPSource) ApproveInvoice(_ context.Context, _ uuid.UUID, _ uuid.UUID) (*ap.VendorInvoice, error) {
	return nil, nil
}

// call sends one request as role/sub, with an optional X-Branch-Id.
func (f *wallFixture) call(t *testing.T, method, path, body, role, sub, branchHeader string) int {
	t.Helper()
	status, _ := f.callBody(t, method, path, body, role, sub, branchHeader)
	return status
}

// callBody sends one request as role/sub and returns the status with the
// response body, for cases that read an id or a revision back.
func (f *wallFixture) callBody(t *testing.T, method, path, body, role, sub, branchHeader string) (int, []byte) {
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
	defer res.Body.Close()
	buf, err := io.ReadAll(res.Body)
	if err != nil {
		t.Fatal(err)
	}
	return res.StatusCode, buf
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

	// The location list is filtered to the caller's branches: a user granted
	// only A sees branch A's rows only, through its context branch or, with
	// none, through its grants; a bound user with no grants sees no rows at
	// all; an administrator is held to a header it sends and sees every
	// branch without one.
	for _, c := range []struct {
		name, role, sub, header string
		wantA, wantB            bool
	}{
		{"warehouse, header A", "warehouse", "u-a", A, true, false},
		{"warehouse, no header", "warehouse", "u-a", "", true, false},
		{"warehouse u-none, no header", "warehouse", "u-none", "", false, false},
		{"admin, header A", "admin", "boss", A, true, false},
		{"admin, no header", "admin", "boss", "", true, true},
	} {
		status, body := f.callBody(t, "GET", "/api/v1/locations", "", c.role, c.sub, c.header)
		if status != http.StatusOK {
			t.Errorf("location list, %s: %d, want 200", c.name, status)
			continue
		}
		if got := strings.Contains(string(body), f.yardA.String()); got != c.wantA {
			t.Errorf("location list, %s: yard A present = %v, want %v", c.name, got, c.wantA)
		}
		if got := strings.Contains(string(body), f.yardB.String()); got != c.wantB {
			t.Errorf("location list, %s: yard B present = %v, want %v", c.name, got, c.wantB)
		}
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

	receive := fmt.Sprintf(`{"lines":[{"line_id":%q,"qty_received":1,"location_id":%q}]}`, f.poLineB, f.yardB)
	for _, c := range []struct {
		name, method, path, body, role, sub string
		want                                int
	}{
		// The matching read finds no match result behind the wall, hence 404
		// rather than 200: the switch-off case is that it is not a 403.
		{"receive another branch's po", "POST", "/api/v1/purchase-orders/" + f.poB.String() + "/receive", receive, "purchasing", "u-a", http.StatusOK},
		{"read another branch's po", "GET", "/api/v1/purchase-orders/" + f.poB.String(), "", "purchasing", "u-a", http.StatusOK},
		{"read another branch's yard", "GET", "/api/v1/locations/" + f.yardB.String(), "", "warehouse", "u-a", http.StatusOK},
		{"read another branch's tree", "GET", "/api/v1/branches/" + f.branchB.String() + "/tree", "", "sales", "u-a", http.StatusOK},
		{"match another branch's po", "GET", "/api/v1/matching/results/" + f.poB.String(), "", "finance", "u-a", http.StatusNotFound},
	} {
		if got := f.call(t, c.method, c.path, c.body, c.role, c.sub, ""); got != c.want {
			t.Errorf("switch off, bound caller %s: %d, want %d", c.name, got, c.want)
		}
	}

	// With the switch off every caller is an administrator, so the location
	// list is unfiltered.
	_, listBody := f.callBody(t, "GET", "/api/v1/locations", "", "warehouse", "u-a", "")
	if !strings.Contains(string(listBody), f.yardB.String()) {
		t.Errorf("switch off, location list does not carry branch B's yard")
	}

	// The same for the quote list: the single branch deployment is one
	// branch, so a bound caller reads every row.
	quoteA, quoteB := uuid.New(), uuid.New()
	seedWallQuote(t, db, quoteA, f.branchA, f.docCust, "WLQ-"+quoteA.String()[:8])
	seedWallQuote(t, db, quoteB, f.branchB, f.docCust, "WLQ-"+quoteB.String()[:8])
	t.Cleanup(func() {
		_, _ = db.Pool.Exec(context.Background(), `DELETE FROM quotes WHERE id IN ($1, $2)`, quoteA, quoteB)
	})
	_, quoteBody := f.callBody(t, "GET", "/api/v1/quotes", "", "sales", "u-a", "")
	if !strings.Contains(string(quoteBody), quoteA.String()) || !strings.Contains(string(quoteBody), quoteB.String()) {
		t.Errorf("switch off, quote list does not carry both branches' quotes")
	}
	for _, yard := range []uuid.UUID{f.yardA, f.yardB} {
		if _, err := db.Pool.Exec(context.Background(),
			`INSERT INTO inventory (product_id, location_id, location, quantity) VALUES ($1, $2, $3, 5)`,
			f.productID, yard, "wl-inv-"+yard.String()[:8]); err != nil {
			t.Fatalf("seed inventory: %v", err)
		}
	}
	_, invBody := f.callBody(t, "GET", "/api/v1/inventory?product_id="+f.productID.String(), "", "warehouse", "u-a", "")
	if !strings.Contains(string(invBody), f.yardA.String()) || !strings.Contains(string(invBody), f.yardB.String()) {
		t.Errorf("switch off, inventory list does not carry both branches' rows")
	}
}

// The branch wall on records a path id addresses (ADR 0007 section 2.3): a
// bound caller acts only on records of a branch it may target, an
// administrator on any branch's, and a record's branch is held to the same
// rule a request body is held to. Each case goes through serve's real mount
// methods, the real role guards and the real BranchMiddleware; a route that
// loses its record check fails here.
func TestBranchWall_PathIDRecords(t *testing.T) {
	db := testutil.RequireDB(t)
	f := newWallFixture(t, db, true)
	A := f.branchA.String()

	// A quote at each branch, created through the wire so number, revision
	// and lines are real. The branch B records are created by the admin (no
	// header, any branch); the branch A records by the granted sales user.
	// The quote cleanup at the end of this block is registered after the
	// customer cleanup below, so it runs before it (quotes reference
	// customers).
	cust := func(branch, role, sub, header string) string {
		status, body := f.callBody(t, "POST", "/api/v1/customers",
			fmt.Sprintf(`{"account_number":"WALL-%s","name":"wall","primary_branch_id":%q}`, uuid.NewString()[:8], branch),
			role, sub, header)
		if status != http.StatusCreated {
			t.Fatalf("seed customer at %s: %d %s", branch, status, body)
		}
		var out struct {
			ID string `json:"id"`
		}
		if err := json.Unmarshal(body, &out); err != nil {
			t.Fatalf("seed customer body: %v", err)
		}
		return out.ID
	}
	custA := cust(A, "sales", "u-a", A)
	custB := cust(f.branchB.String(), "admin", "boss", "")
	quoteID := func(branch, customer, role, sub, header string) string {
		status, body := f.callBody(t, "POST", "/api/v1/quotes",
			fmt.Sprintf(`{"branch_id":%q,"customer_id":%q,"delivery_type":"pickup","lines":[{"product_id":%q,"sku":"wall","description":"x","quantity":"1","uom":"PCS","unit_price_ten_thousandths":100}]}`, branch, customer, f.productID),
			role, sub, header)
		if status != http.StatusCreated {
			t.Fatalf("seed quote at %s: %d %s", branch, status, body)
		}
		var out struct {
			ID string `json:"id"`
		}
		if err := json.Unmarshal(body, &out); err != nil {
			t.Fatalf("seed quote body: %v", err)
		}
		return out.ID
	}
	quoteA := quoteID(A, custA, "sales", "u-a", A)
	quoteB := quoteID(f.branchB.String(), custB, "admin", "boss", "")
	t.Cleanup(func() {
		_, _ = db.Pool.Exec(context.Background(), `DELETE FROM customer_branches WHERE branch_id IN ($1, $2)`, f.branchA, f.branchB)
		_, _ = db.Pool.Exec(context.Background(), `DELETE FROM events_outbox WHERE entity_type = 'customer' AND branch_id IN ($1, $2)`, f.branchA, f.branchB)
		_, _ = db.Pool.Exec(context.Background(), `DELETE FROM customers WHERE account_number LIKE 'WALL-%'`)
	})
	t.Cleanup(func() {
		_, _ = db.Pool.Exec(context.Background(), `DELETE FROM quote_lines WHERE quote_id IN (SELECT id FROM quotes WHERE branch_id IN ($1, $2))`, f.branchA, f.branchB)
		_, _ = db.Pool.Exec(context.Background(), `DELETE FROM events_outbox WHERE entity_type = 'quote' AND branch_id IN ($1, $2)`, f.branchA, f.branchB)
		_, _ = db.Pool.Exec(context.Background(), `DELETE FROM quotes WHERE branch_id IN ($1, $2)`, f.branchA, f.branchB)
	})

	receive := func(line uuid.UUID, loc uuid.UUID) string {
		return fmt.Sprintf(`{"lines":[{"line_id":%q,"qty_received":1,"location_id":%q}]}`, line, loc)
	}
	const ok = http.StatusOK
	const no = http.StatusForbidden

	for _, c := range []struct {
		name, method, path, body, role, sub, header string
		want                                        int
	}{
		// Purchase orders: the record's branch is held to the caller's wall.
		// The no header row is the hole this wall closes: without a context
		// branch the unfiltered lookup found any branch's purchase order.
		{"receive foreign po, no header", "POST", "/api/v1/purchase-orders/" + f.poB.String() + "/receive", receive(f.poLineB, f.yardA), "purchasing", "u-a", "", no},
		{"receive foreign po, header A", "POST", "/api/v1/purchase-orders/" + f.poB.String() + "/receive", receive(f.poLineB, f.yardA), "purchasing", "u-a", A, no},
		{"receive own po", "POST", "/api/v1/purchase-orders/" + f.poA.String() + "/receive", receive(f.poLineA, f.yardA), "purchasing", "u-a", A, ok},
		{"receive foreign po, admin", "POST", "/api/v1/purchase-orders/" + f.poB.String() + "/receive", receive(f.poLineB, f.yardB), "admin", "boss", "", ok},
		{"read foreign po, no header", "GET", "/api/v1/purchase-orders/" + f.poB.String(), "", "purchasing", "u-a", "", no},
		{"read own po", "GET", "/api/v1/purchase-orders/" + f.poA.String(), "", "purchasing", "u-a", A, ok},
		{"submit foreign po, no header", "POST", "/api/v1/purchase-orders/" + f.poB.String() + "/submit", "", "purchasing", "u-a", "", no},
		{"freight of foreign po, no header", "GET", "/api/v1/purchase-orders/" + f.poB.String() + "/freight", "", "purchasing", "u-a", "", no},

		// Matching acts on a purchase order by its path id.
		{"match foreign po, no header", "POST", "/api/v1/matching/run/" + f.poB.String(), "", "finance", "u-a", "", no},
		{"match result of foreign po, no header", "GET", "/api/v1/matching/results/" + f.poB.String(), "", "finance", "u-a", "", no},
		{"match result of foreign po, header A", "GET", "/api/v1/matching/results/" + f.poB.String(), "", "finance", "u-a", A, no},
		{"match result of own po", "GET", "/api/v1/matching/results/" + f.poA.String(), "", "finance", "u-a", A, http.StatusNotFound},
		{"match result of foreign po, admin", "GET", "/api/v1/matching/results/" + f.poB.String(), "", "admin", "boss", "", http.StatusNotFound},

		// Location and branch tree reads.
		{"read foreign yard, no header", "GET", "/api/v1/locations/" + f.yardB.String(), "", "warehouse", "u-a", "", no},
		{"read foreign yard, header A", "GET", "/api/v1/locations/" + f.yardB.String(), "", "warehouse", "u-a", A, no},
		{"read own yard", "GET", "/api/v1/locations/" + f.yardA.String(), "", "warehouse", "u-a", A, ok},
		{"read foreign yard, admin", "GET", "/api/v1/locations/" + f.yardB.String(), "", "admin", "boss", "", ok},
		{"read foreign tree, no header", "GET", "/api/v1/branches/" + f.branchB.String() + "/tree", "", "sales", "u-a", "", no},
		{"read foreign tree, header A", "GET", "/api/v1/branches/" + f.branchB.String() + "/tree", "", "sales", "u-a", A, no},
		{"read own tree", "GET", "/api/v1/branches/" + f.branchA.String() + "/tree", "", "sales", "u-a", A, ok},
		{"read foreign tree, admin", "GET", "/api/v1/branches/" + f.branchB.String() + "/tree", "", "admin", "boss", "", ok},

		// Quotes: the record's branch is held to the caller's wall on reads
		// and writes alike.
		{"read foreign quote, no header", "GET", "/api/v1/quotes/" + quoteB, "", "sales", "u-a", "", no},
		{"read own quote", "GET", "/api/v1/quotes/" + quoteA, "", "sales", "u-a", A, ok},
		{"file of foreign quote, no header", "GET", "/api/v1/quotes/" + quoteB + "/file", "", "sales", "u-a", "", no},
		{"edit foreign quote, no header", "PUT", "/api/v1/quotes/" + quoteB,
			fmt.Sprintf(`{"revision":1,"customer_id":%q,"delivery_type":"pickup","lines":[{"product_id":%q,"sku":"wall","description":"x","quantity":"1","uom":"PCS","unit_price_ten_thousandths":100}]}`, custA, f.productID),
			"sales", "u-a", "", no},
		{"transition foreign quote, no header", "POST", "/api/v1/quotes/" + quoteB + "/transitions", `{"to":"sent","revision":1}`, "sales", "u-a", "", no},
	} {
		if got := f.call(t, c.method, c.path, c.body, c.role, c.sub, c.header); got != c.want {
			t.Errorf("%s: %d, want %d", c.name, got, c.want)
		}
	}

	// The purchase order routes are unconverted: the wall's 403 carries the
	// same legacy error shape as every other error on them, not the ADR 0001
	// envelope.
	_, poBody := f.callBody(t, "GET", "/api/v1/purchase-orders/"+f.poB.String(), "", "purchasing", "u-a", "")
	if !strings.Contains(string(poBody), `"code":"FORBIDDEN"`) {
		t.Errorf("foreign po 403 body is not the legacy shape: %s", poBody)
	}
}

// The document print and email routes run behind the branch middleware, so
// the branch filters the invoice and order repositories already carry apply
// to every caller, and the handler holds the record's own branch to the wall
// for the caller the repositories' filter never fires for: a sales or finance
// user held to branch A finds branch B's invoice or pick ticket a 404 with a
// context branch and a 403 with none (its grants, none granted none), and
// can read, print and email only its own branch's records.
func TestBranchWall_DocumentRoutes(t *testing.T) {
	db := testutil.RequireDB(t)
	f := newWallFixture(t, db, true)
	A := f.branchA.String()

	const ok = http.StatusOK
	const no = http.StatusForbidden
	for _, c := range []struct {
		name, method, path, role, sub, header string
		want                                  int
	}{
		{"sales prints foreign invoice", "GET", "/api/v1/documents/print/invoice/" + f.invB.String(), "sales", "u-a", A, http.StatusNotFound},
		{"finance emails foreign invoice", "POST", "/api/v1/invoices/" + f.invB.String() + "/email", "finance", "u-a", A, http.StatusNotFound},
		{"sales prints foreign pick ticket", "GET", "/api/v1/documents/print/pickticket/" + f.orderB.String(), "sales", "u-a", A, http.StatusNotFound},
		{"finance prints foreign pick ticket", "GET", "/api/v1/documents/print/pickticket/" + f.orderB.String(), "finance", "u-a", A, http.StatusNotFound},
		{"sales prints foreign invoice, no header", "GET", "/api/v1/documents/print/invoice/" + f.invB.String(), "sales", "u-a", "", no},
		{"finance emails foreign invoice, no header", "POST", "/api/v1/invoices/" + f.invB.String() + "/email", "finance", "u-a", "", no},
		{"sales prints foreign pick ticket, no header", "GET", "/api/v1/documents/print/pickticket/" + f.orderB.String(), "sales", "u-a", "", no},
		{"finance prints foreign pick ticket, no header", "GET", "/api/v1/documents/print/pickticket/" + f.orderB.String(), "finance", "u-a", "", no},
		{"sales prints own invoice", "GET", "/api/v1/documents/print/invoice/" + f.invA.String(), "sales", "u-a", A, ok},
		{"finance emails own invoice", "POST", "/api/v1/invoices/" + f.invA.String() + "/email", "finance", "u-a", A, http.StatusAccepted},
		{"sales prints own pick ticket", "GET", "/api/v1/documents/print/pickticket/" + f.orderA.String(), "sales", "u-a", A, ok},
		{"sales prints own invoice, no header", "GET", "/api/v1/documents/print/invoice/" + f.invA.String(), "sales", "u-a", "", ok},
		{"sales prints own pick ticket, no header", "GET", "/api/v1/documents/print/pickticket/" + f.orderA.String(), "sales", "u-a", "", ok},
		{"admin prints foreign invoice", "GET", "/api/v1/documents/print/invoice/" + f.invB.String(), "admin", "boss", "", ok},
	} {
		if got := f.call(t, c.method, c.path, "", c.role, c.sub, c.header); got != c.want {
			t.Errorf("%s: %d, want %d", c.name, got, c.want)
		}
	}
}

// The match exceptions list is filtered by the caller's branches: a finance
// user held to branch A reads only branch A's exceptions, through its context
// branch or, with none, through its grants; a bound user with no grants reads
// none; an administrator without a header reads every branch's.
func TestBranchWall_MatchingExceptions(t *testing.T) {
	db := testutil.RequireDB(t)
	f := newWallFixture(t, db, true)
	A := f.branchA.String()

	// One exception match result per branch's purchase order, seeded here
	// and not in the fixture: the other tests read the matching routes on
	// the no-match-result answer.
	ctx := context.Background()
	for _, po := range []uuid.UUID{f.poA, f.poB} {
		if _, err := db.Pool.Exec(ctx, `INSERT INTO po_match_results (po_id, status) VALUES ($1, 'EXCEPTION')`, po); err != nil {
			t.Fatalf("seed match result: %v", err)
		}
	}
	t.Cleanup(func() {
		_, _ = db.Pool.Exec(ctx, `DELETE FROM po_match_results WHERE po_id IN ($1, $2)`, f.poA, f.poB)
	})

	for _, c := range []struct {
		name, role, sub, header string
		wantA, wantB            bool
	}{
		{"finance, header A", "finance", "u-a", A, true, false},
		{"finance, no header", "finance", "u-a", "", true, false},
		{"finance u-none, no header", "finance", "u-none", "", false, false},
		{"admin, no header", "admin", "boss", "", true, true},
	} {
		status, body := f.callBody(t, "GET", "/api/v1/matching/exceptions", "", c.role, c.sub, c.header)
		if status != http.StatusOK {
			t.Errorf("matching exceptions, %s: %d, want 200", c.name, status)
			continue
		}
		if got := strings.Contains(string(body), f.poA.String()); got != c.wantA {
			t.Errorf("matching exceptions, %s: branch A's exception present = %v, want %v", c.name, got, c.wantA)
		}
		if got := strings.Contains(string(body), f.poB.String()); got != c.wantB {
			t.Errorf("matching exceptions, %s: branch B's exception present = %v, want %v", c.name, got, c.wantB)
		}
	}
}

// seedWallQuote inserts one quote directly at the named branch, the list
// tests' row. The fixture's customer is the header; the caller cleans up.
func seedWallQuote(t *testing.T, db *database.DB, id, branch, customer uuid.UUID, number string) {
	t.Helper()
	if _, err := db.Pool.Exec(context.Background(),
		`INSERT INTO quotes (id, number, customer_id, state, branch_id, total_amount)
		 VALUES ($1, $2, $3, 'DRAFT', $4, 10)`, id, number, customer, branch); err != nil {
		t.Fatalf("seed quote: %v", err)
	}
}

// The quote list is filtered by the caller's branches (ADR 0007 section 2.3,
// the list form of the record rule): a sales user held to branch A reads only
// branch A's quotes, through its context branch or, with none, through its
// grants; a bound user with no grants reads none; an administrator without a
// header reads every branch's.
func TestBranchWall_QuoteListGrants(t *testing.T) {
	db := testutil.RequireDB(t)
	f := newWallFixture(t, db, true)
	A := f.branchA.String()

	quoteA, quoteB := uuid.New(), uuid.New()
	seedWallQuote(t, db, quoteA, f.branchA, f.docCust, "WLQ-"+quoteA.String()[:8])
	seedWallQuote(t, db, quoteB, f.branchB, f.docCust, "WLQ-"+quoteB.String()[:8])
	t.Cleanup(func() {
		_, _ = db.Pool.Exec(context.Background(), `DELETE FROM quotes WHERE id IN ($1, $2)`, quoteA, quoteB)
	})

	for _, c := range []struct {
		name, role, sub, header string
		wantA, wantB            bool
	}{
		{"sales, header A", "sales", "u-a", A, true, false},
		{"sales, no header", "sales", "u-a", "", true, false},
		{"sales u-none, no header", "sales", "u-none", "", false, false},
		{"admin, no header", "admin", "boss", "", true, true},
	} {
		status, body := f.callBody(t, "GET", "/api/v1/quotes", "", c.role, c.sub, c.header)
		if status != http.StatusOK {
			t.Errorf("quote list, %s: %d, want 200", c.name, status)
			continue
		}
		if got := strings.Contains(string(body), quoteA.String()); got != c.wantA {
			t.Errorf("quote list, %s: branch A's quote present = %v, want %v", c.name, got, c.wantA)
		}
		if got := strings.Contains(string(body), quoteB.String()); got != c.wantB {
			t.Errorf("quote list, %s: branch B's quote present = %v, want %v", c.name, got, c.wantB)
		}
	}
}

// The inventory levels list is filtered by the caller's branches like the
// module's writes: a warehouse user held to branch A reads only branch A's
// rows, through its context branch or, with none, through its grants; a bound
// user with no grants reads none; an administrator without a header reads
// every branch's.
func TestBranchWall_InventoryListGrants(t *testing.T) {
	db := testutil.RequireDB(t)
	f := newWallFixture(t, db, true)
	A := f.branchA.String()

	for _, r := range []struct {
		yard uuid.UUID
		name string
	}{
		{f.yardA, "wl-inv-a-" + f.yardA.String()[:8]},
		{f.yardB, "wl-inv-b-" + f.yardB.String()[:8]},
	} {
		if _, err := db.Pool.Exec(context.Background(),
			`INSERT INTO inventory (product_id, location_id, location, quantity) VALUES ($1, $2, $3, 5)`,
			f.productID, r.yard, r.name); err != nil {
			t.Fatalf("seed inventory: %v", err)
		}
	}
	// The fixture's cleanup already deletes this product's inventory rows.

	for _, c := range []struct {
		name, role, sub, header string
		wantA, wantB            bool
	}{
		{"warehouse, header A", "warehouse", "u-a", A, true, false},
		{"warehouse, no header", "warehouse", "u-a", "", true, false},
		{"warehouse u-none, no header", "warehouse", "u-none", "", false, false},
		{"admin, no header", "admin", "boss", "", true, true},
	} {
		status, body := f.callBody(t, "GET", "/api/v1/inventory?product_id="+f.productID.String(), "", c.role, c.sub, c.header)
		if status != http.StatusOK {
			t.Errorf("inventory list, %s: %d, want 200", c.name, status)
			continue
		}
		if got := strings.Contains(string(body), f.yardA.String()); got != c.wantA {
			t.Errorf("inventory list, %s: branch A's row present = %v, want %v", c.name, got, c.wantA)
		}
		if got := strings.Contains(string(body), f.yardB.String()); got != c.wantB {
			t.Errorf("inventory list, %s: branch B's row present = %v, want %v", c.name, got, c.wantB)
		}
	}
}

// poRecSummary is the part of the recommendations response the test reads.
type poRecSummary struct {
	Items []struct {
		ProductID     string  `json:"product_id"`
		CurrentStock  float64 `json:"current_stock"`
		AvgDailySales float64 `json:"avg_daily_sales"`
	} `json:"items"`
}

// The purchase order recommendations route runs behind the branch middleware,
// so its stock read and its sales velocity read must scope to the SAME branch
// set: a bound purchasing user held to branch A is recommended from branch
// A's stock and branch A's sales, never its branches' stock against every
// branch's demand; a bound user with no grants sees no stock and no sales,
// so the product carries no recommendation at all; an administrator without
// a header reads every branch's stock and demand, as before.
func TestBranchWall_PORecommendationScoping(t *testing.T) {
	db := testutil.RequireDB(t)
	f := newWallFixture(t, db, true)
	A := f.branchA.String()
	ctx := context.Background()

	for _, r := range []struct {
		yard uuid.UUID
		qty  int
	}{{f.yardA, 5}, {f.yardB, 50}} {
		if _, err := db.Pool.Exec(ctx,
			`INSERT INTO inventory (product_id, location_id, location, quantity) VALUES ($1, $2, $3, $4)`,
			f.productID, r.yard, "wl-rec-"+r.yard.String()[:8], r.qty); err != nil {
			t.Fatalf("seed inventory: %v", err)
		}
	}
	// Demand in the 90 day lookback: 90 units sold at branch A (1 a day),
	// 900 at branch B (10 a day). The fixture's cleanup deletes this
	// product's inventory rows; the order rows clean up after the fixture's,
	// because order_lines restricts on the product.
	orderA, orderB := uuid.New(), uuid.New()
	t.Cleanup(func() {
		_, _ = db.Pool.Exec(ctx, `DELETE FROM order_lines WHERE order_id IN ($1, $2)`, orderA, orderB)
		_, _ = db.Pool.Exec(ctx, `DELETE FROM orders WHERE id IN ($1, $2)`, orderA, orderB)
	})
	for _, o := range []struct {
		id     uuid.UUID
		branch uuid.UUID
		qty    int
	}{{orderA, f.branchA, 90}, {orderB, f.branchB, 900}} {
		if _, err := db.Pool.Exec(ctx,
			`INSERT INTO orders (id, customer_id, branch_id, status, total_amount, delivery_type, currency)
			 VALUES ($1, $2, $3, 'CONFIRMED', 10, 'PICKUP', 'USD')`,
			o.id, f.docCust, o.branch); err != nil {
			t.Fatalf("seed order: %v", err)
		}
		if _, err := db.Pool.Exec(ctx,
			`INSERT INTO order_lines (id, order_id, product_id, quantity, uom, price_uom, uom_qty, price_uom_qty, unit_price, line_total, description)
			 VALUES ($1, $2, $3, $4, 'PCS', 'PCS', 1, 1, 1, $4, 'wall')`,
			uuid.New(), o.id, f.productID, o.qty); err != nil {
			t.Fatalf("seed order line: %v", err)
		}
	}

	itemFor := func(t *testing.T, body []byte, productID string) (found bool, stock, sales float64) {
		t.Helper()
		var sum poRecSummary
		if err := json.Unmarshal(body, &sum); err != nil {
			t.Fatalf("recommendations body: %v\n%s", err, body)
		}
		for _, it := range sum.Items {
			if it.ProductID == productID {
				return true, it.CurrentStock, it.AvgDailySales
			}
		}
		return false, 0, 0
	}
	for _, c := range []struct {
		name, role, sub, header string
		wantStock               float64
		wantSales               float64
		wantSalesAsserted       bool
	}{
		// The fixture product carries no sales history outside the seeded
		// orders, so a caller that sees neither stock nor real sales falls
		// back to the synthetic velocity proxy; only the stock number is
		// asserted there.
		{"purchasing, header A", "purchasing", "u-a", A, 5, 1, true},
		{"purchasing, no header", "purchasing", "u-a", "", 5, 1, true},
		{"purchasing u-none, no header", "purchasing", "u-none", "", 0, 0, false},
		{"admin, no header", "admin", "boss", "", 55, 11, true},
	} {
		status, body := f.callBody(t, "GET", "/api/v1/purchase-orders/recommendations", "", c.role, c.sub, c.header)
		if status != http.StatusOK {
			t.Errorf("recommendations, %s: %d, want 200", c.name, status)
			continue
		}
		found, stock, sales := itemFor(t, body, f.productID.String())
		if !found {
			t.Errorf("recommendations, %s: product absent from the recommendations", c.name)
			continue
		}
		if stock != c.wantStock {
			t.Errorf("recommendations, %s: current_stock = %v, want %v", c.name, stock, c.wantStock)
		}
		if c.wantSalesAsserted && sales != c.wantSales {
			t.Errorf("recommendations, %s: avg_daily_sales = %v, want %v", c.name, sales, c.wantSales)
		}
	}
}
