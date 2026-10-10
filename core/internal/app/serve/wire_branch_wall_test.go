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
	"github.com/gablelbm/gable/internal/chargecode"
	"github.com/gablelbm/gable/internal/crm"
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
	"github.com/gablelbm/gable/pkg/audit"
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
	srv                *httptest.Server
	branchA, branchB   uuid.UUID
	yardA, yardB       uuid.UUID
	productID          uuid.UUID
	vendorID           uuid.UUID
	poA, poB           uuid.UUID
	poLineA, poLineB   uuid.UUID
	docCust            uuid.UUID
	orderA, orderB     uuid.UUID
	invA, invB         uuid.UUID
	memoA, memoB       uuid.UUID
	crmCustA, crmCustB uuid.UUID
	actA, actB         uuid.UUID
	db                 *database.DB
}

func newWallFixture(t *testing.T, db *database.DB, multiBranch bool) *wallFixture {
	t.Helper()
	// Callers take the shared outbox lock before this fixture: the wall's
	// route calls (a location create, a product write) run through the serve
	// wiring, which records outbox events, and must not interleave with
	// another package's feed assertions. The lock cannot live here: a caller
	// that already holds it (the orders wall) would open a second session and
	// self-deadlock on the advisory lock.
	ctx := context.Background()
	f := &wallFixture{db: db, branchA: uuid.New(), branchB: uuid.New(), yardA: uuid.New(), yardB: uuid.New(), productID: uuid.New(),
		vendorID: uuid.New(), poA: uuid.New(), poB: uuid.New(), poLineA: uuid.New(), poLineB: uuid.New(),
		docCust: uuid.New(), orderA: uuid.New(), orderB: uuid.New(), invA: uuid.New(), invB: uuid.New(), memoA: uuid.New(), memoB: uuid.New(),
		crmCustA: uuid.New(), crmCustB: uuid.New(), actA: uuid.New(), actB: uuid.New()}
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
	// One posted credit memo per branch, so the credit memo path-id routes act
	// on real records of each branch; branch A carries a tax rate so a free
	// credit memo can be created there.
	if _, err := db.Pool.Exec(ctx, `UPDATE locations SET default_tax_rate = 0.05 WHERE id = $1`, f.branchA); err != nil {
		t.Fatalf("branch rate: %v", err)
	}
	for _, m := range []struct{ id, inv, branch uuid.UUID }{{f.memoA, f.invA, f.branchA}, {f.memoB, f.invB, f.branchB}} {
		if _, err := db.Pool.Exec(ctx, `INSERT INTO credit_memos (id, invoice_id, customer_id, branch_id, currency, reason_code, reason, amount, status, number,
				memo_date, subtotal, tax_amount, total_amount, tax_rate)
			VALUES ($1, $2, $3, $4, 'USD', 'OTHER', 'wall', 1, 'OPEN', credit_memo_next_number(), CURRENT_DATE, -1, 0, -1, 0)`,
			m.id, m.inv, f.docCust, m.branch); err != nil {
			t.Fatalf("seed credit memo: %v", err)
		}
	}
	for _, sub := range []string{"u-a"} {
		if _, err := db.Pool.Exec(ctx, `INSERT INTO user_locations (user_sub, branch_id, is_home, granted_by) VALUES ($1, $2, TRUE, 'test')`, sub, f.branchA); err != nil {
			t.Fatalf("seed grant: %v", err)
		}
	}
	// One customer and one logged activity per branch, so the crm routes act
	// on real records of each branch.
	for _, r := range []struct{ cust, act, branch uuid.UUID }{
		{f.crmCustA, f.actA, f.branchA}, {f.crmCustB, f.actB, f.branchB},
	} {
		if _, err := db.Pool.Exec(ctx, `INSERT INTO customers (id, name, account_number, primary_branch_id)
			VALUES ($1, 'wall crm cust', $2, $3)`, r.cust, "WLCRM-"+r.cust.String()[:8], r.branch); err != nil {
			t.Fatalf("seed crm customer: %v", err)
		}
		if _, err := db.Pool.Exec(ctx, `INSERT INTO customer_branches (customer_id, branch_id) VALUES ($1, $2)`, r.cust, r.branch); err != nil {
			t.Fatalf("seed crm customer branch: %v", err)
		}
		if _, err := db.Pool.Exec(ctx, `INSERT INTO crm_activities (id, customer_id, activity_type, description, activity_date)
			VALUES ($1, $2, 'CALL', 'wall', now())`, r.act, r.cust); err != nil {
			t.Fatalf("seed activity: %v", err)
		}
	}
	t.Cleanup(func() {
		_, _ = db.Pool.Exec(ctx, `DELETE FROM events_outbox WHERE entity_type = 'activity' AND entity_id IN (SELECT id FROM crm_activities WHERE customer_id IN ($1, $2))`, f.crmCustA, f.crmCustB)
		_, _ = db.Pool.Exec(ctx, `DELETE FROM audit_log WHERE entity_type = 'activity' AND entity_id IN (SELECT id FROM crm_activities WHERE customer_id IN ($1, $2))`, f.crmCustA, f.crmCustB)
		_, _ = db.Pool.Exec(ctx, `DELETE FROM crm_activities WHERE customer_id IN ($1, $2)`, f.crmCustA, f.crmCustB)
		_, _ = db.Pool.Exec(ctx, `DELETE FROM customer_branches WHERE customer_id IN ($1, $2)`, f.crmCustA, f.crmCustB)
		_, _ = db.Pool.Exec(ctx, `DELETE FROM customers WHERE id IN ($1, $2)`, f.crmCustA, f.crmCustB)
		_, _ = db.Pool.Exec(ctx, `DELETE FROM user_locations WHERE user_sub = 'u-a'`)
		_, _ = db.Pool.Exec(ctx, `DELETE FROM events_outbox WHERE entity_id IN (SELECT id FROM credit_memos WHERE customer_id = $1)`, f.docCust)
		_, _ = db.Pool.Exec(ctx, `DELETE FROM credit_memo_lines WHERE credit_memo_id IN (SELECT id FROM credit_memos WHERE customer_id = $1)`, f.docCust)
		_, _ = db.Pool.Exec(ctx, `DELETE FROM credit_memos WHERE customer_id = $1`, f.docCust)
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
	wall.products(mux, product.NewHandler(product.NewService(product.NewRepository(db))))
	wall.chargeCodes(mux, chargecode.NewService(chargecode.NewRepository(db)))
	wall.customers(mux, customer.NewService(customer.NewRepository(db)).WithOutbox(outbox.NewWriter(db, "")).WithTxRunner(db))
	wall.quotes(mux, quote.NewService(quote.NewRepository(db)).WithOutbox(outbox.NewWriter(db, "")).WithTxRunner(db))
	// The crm mount, driven here as serve wires it: the activity routes
	// behind the branch middleware, their writes one transaction with the
	// audit row and the activity.* event.
	wall.crm(mux, crm.NewService(crm.NewRepository(db)).
		WithOutbox(outbox.NewWriter(db, "")).WithTxRunner(db).WithAudit(audit.NewLogger(db)))
	// The recommendation service is wired as serve wires it (serve.go), so
	// the recommendations route answers from the real stock and velocity
	// reads instead of 503: the route is behind the branch middleware and
	// both reads must scope to the same branches.
	poRecSvc := purchase_order.NewRecommendationService(purchase_order.NewRepository(db), invSvc,
		product.NewService(product.NewRepository(db)), vendor.NewService(vendor.NewRepository(db))).
		WithVelocityRepo(purchase_order.NewVelocityRepository(db))
	// The PO service wires its velocity repo too: the refresh-reorder-targets
	// route shares the same compute-from-every-branch path the cron runs
	// (serve.go wires the velocity repo on the PO service; the recommendation
	// service has its own copy). The product service is what the recompute
	// reads products from and writes the recomputed targets to; the wire test
	// for refresh-reorder-targets needs it on the PO service too.
	poProductSvc := product.NewService(product.NewRepository(db))
	poSvc := purchase_order.NewService(purchase_order.NewRepository(db), db, nil, nil, poProductSvc, nil).
		WithVelocityRepo(purchase_order.NewVelocityRepository(db))
	wall.purchaseOrders(mux, purchase_order.NewHandler(poSvc, poRecSvc))
	wall.matching(mux, matching.NewService(db, matching.NewRepository(db), fixturePOSource{f: f}, fixtureAPSource{}, slog.Default()))
	docSvc := document.NewService(product.NewRepository(db))
	glSvc := gl.NewService(gl.NewRepository(db), glint.NewMockGLAdapter(), slog.Default())
	accountSvc := account.NewService(db, glSvc, slog.Default())
	invoiceSvc := invoice.NewService(invoice.NewRepository(db), glSvc, accountSvc, db)
	orderSvc := order.NewService(order.NewRepository(db)).WithTxRunner(db)
	docHandler := document.NewHandler(docSvc, orderSvc, invoiceSvc, customer.NewService(customer.NewRepository(db)), notification.NewLogEmailService(slog.Default()))
	wall.documents(mux, docHandler)
	wall.invoices(mux, invoice.NewService(invoice.NewRepository(db), glSvc, accountSvc, db).WithOutbox(outbox.NewWriter(db, "")).WithOrders(orderSvc).WithStock(invSvc))
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
	status, buf := f.callHdr(t, method, path, body, role, sub, branchHeader, nil)
	return status, buf
}

// callHdr sends one request as role/sub with extra request headers, for the
// routes whose writes carry their revision as If-Match.
func (f *wallFixture) callHdr(t *testing.T, method, path, body, role, sub, branchHeader string, hdr map[string]string) (int, []byte) {
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
	for k, v := range hdr {
		req.Header.Set(k, v)
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

// callHdrStatus sends one request as callHdr and returns only the status.
func (f *wallFixture) callHdrStatus(t *testing.T, method, path, body, role, sub, branchHeader string, hdr map[string]string) int {
	t.Helper()
	status, _ := f.callHdr(t, method, path, body, role, sub, branchHeader, hdr)
	return status
}

func TestBranchWall_ServeWiring(t *testing.T) {
	testutil.LockOutboxTables(t) // the route calls record outbox events
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
		return fmt.Sprintf(`{"type":%q,"code":"c-%s","name":"wall branch","parent_id":%s}`, strings.ToLower(typ), uuid.NewString()[:8], p)
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
	testutil.LockOutboxTables(t) // the route calls record outbox events
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
	testutil.LockOutboxTables(t) // the route calls record outbox events
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

// The crm mount behind the real branch middleware: every route addresses a
// customer by path or an activity of one, and the repository holds each read
// and write to the customer's branches, so a caller held to branch A works its
// own branch's records and finds branch B's activity a 404 on the reads and
// the writes, its create on branch B's customer a 404, and that customer's
// list an empty page.
func TestBranchWall_CrmRoutes(t *testing.T) {
	testutil.LockOutboxTables(t) // the route calls record outbox events
	db := testutil.RequireDB(t)
	f := newWallFixture(t, db, true)
	A := f.branchA.String()
	match := map[string]string{"If-Match": `"1"`}

	// The caller's own branch: every route answers.
	if got := f.call(t, "GET", "/api/v1/activities/"+f.actA.String(), "", "sales", "u-a", A); got != http.StatusOK {
		t.Errorf("own activity read: %d, want 200", got)
	}
	if got := f.callHdrStatus(t, "PUT", "/api/v1/activities/"+f.actA.String(),
		`{"activity_type":"note","description":"own"}`, "sales", "u-a", A, match); got != http.StatusOK {
		t.Errorf("own activity update: %d, want 200", got)
	}
	if got := f.callHdrStatus(t, "DELETE", "/api/v1/activities/"+f.actA.String(), "", "sales", "u-a", A,
		map[string]string{"If-Match": `"2"`}); got != http.StatusNoContent {
		t.Errorf("own activity delete: %d, want 204", got)
	}
	if got := f.call(t, "POST", "/api/v1/customers/"+f.crmCustA.String()+"/activities",
		`{"activity_type":"call","description":"own branch"}`, "sales", "u-a", A); got != http.StatusCreated {
		t.Errorf("create on the caller's customer: %d, want 201", got)
	}

	// The second branch: the path-id routes are 404s, the create on the
	// invisible customer is a 404, and its list is an empty page.
	for _, c := range []struct {
		name, method, path, body string
		hdr                      map[string]string
		want                     int
	}{
		{"read foreign activity", "GET", "/api/v1/activities/" + f.actB.String(), "", nil, http.StatusNotFound},
		{"update foreign activity", "PUT", "/api/v1/activities/" + f.actB.String(),
			`{"activity_type":"note","description":"no"}`, match, http.StatusNotFound},
		{"delete foreign activity", "DELETE", "/api/v1/activities/" + f.actB.String(), "", match, http.StatusNotFound},
	} {
		if got := f.callHdrStatus(t, c.method, c.path, c.body, "sales", "u-a", A, c.hdr); got != c.want {
			t.Errorf("crm %s: %d, want %d", c.name, got, c.want)
		}
	}
	if got := f.call(t, "POST", "/api/v1/customers/"+f.crmCustB.String()+"/activities",
		`{"activity_type":"call","description":"no"}`, "sales", "u-a", A); got != http.StatusNotFound {
		t.Errorf("crm create on the foreign customer: %d, want 404", got)
	}
	status, body := f.callBody(t, "GET", "/api/v1/customers/"+f.crmCustB.String()+"/activities", "", "sales", "u-a", A)
	if status != http.StatusOK || !strings.Contains(string(body), `"items":[]`) {
		t.Errorf("crm list of the foreign customer = %d %s, want 200 with an empty page", status, body)
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
	testutil.LockOutboxTables(t) // the route calls record outbox events
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
	testutil.LockOutboxTables(t) // the route calls record outbox events
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

	// The pager total counts the same filtered set the page draws from: with
	// include=total and a one row page, the total agrees with what the page
	// carries in every arm (a one row total is a one row page and the last
	// one, so no next cursor; an exact total only for the arms whose filtered
	// set is this fixture's rows alone, a lower bound for the admin, whose
	// set includes other tests' quotes).
	for _, c := range []struct {
		name, role, sub, header string
		wantTotal               int
		wantTotalExact          bool
	}{
		{"sales, header A", "sales", "u-a", A, 1, true},
		{"sales, no header", "sales", "u-a", "", 1, true},
		{"sales u-none, no header", "sales", "u-none", "", 0, true},
		{"admin, no header", "admin", "boss", "", 2, false},
	} {
		status, body := f.callBody(t, "GET", "/api/v1/quotes?include=total&limit=1", "", c.role, c.sub, c.header)
		if status != http.StatusOK {
			t.Errorf("quote page total, %s: %d, want 200", c.name, status)
			continue
		}
		var page struct {
			Items      []json.RawMessage `json:"items"`
			NextCursor *string           `json:"next_cursor"`
			Total      *int64            `json:"total"`
		}
		if err := json.Unmarshal(body, &page); err != nil {
			t.Fatalf("quote page total, %s: body: %v\n%s", c.name, err, body)
		}
		if page.Total == nil {
			t.Errorf("quote page total, %s: no total on the include=total page", c.name)
			continue
		}
		total := int(*page.Total)
		if total < c.wantTotal || (c.wantTotalExact && total != c.wantTotal) {
			t.Errorf("quote page total, %s: total = %d, want %d", c.name, total, c.wantTotal)
		}
		wantPage := total
		if wantPage > 1 {
			wantPage = 1
		}
		if len(page.Items) != wantPage {
			t.Errorf("quote page total, %s: page carries %d items, want %d (the total's page at limit 1)", c.name, len(page.Items), wantPage)
			continue
		}
		if total > len(page.Items) && page.NextCursor == nil {
			t.Errorf("quote page total, %s: total %d over a %d row page with no next cursor", c.name, total, len(page.Items))
		}
		if total == len(page.Items) && page.NextCursor != nil {
			t.Errorf("quote page total, %s: total %d is the whole set but a next cursor was minted", c.name, total)
		}
	}
}

// invLevelPage is the envelope the inventory levels list answers (ADR 0006
// 7.2), with the one row shape the wall test reads.
type invLevelPage struct {
	Items []struct {
		LocationID *string `json:"location_id"`
		Location   string  `json:"location_name"`
		Available  string  `json:"available"`
		UOM        string  `json:"uom"`
		ProductID  string  `json:"product_id"`
	} `json:"items"`
	NextCursor *string `json:"next_cursor"`
	Limit      int     `json:"limit"`
	// Total answers include=total (the page and the count carry the same
	// three arm predicate as the row page itself; the wall test asserts it
	// agrees with what the page's rows would give).
	Total *int `json:"total,omitempty"`
}

// The inventory levels list is the contract's envelope (ADR 0006 7.2) and is
// filtered by the caller's branches like the module's writes: a warehouse user
// held to branch A reads only branch A's rows, through its context branch or,
// with none, through its grants; a bound user with no grants reads none (an
// empty page, still the envelope); an administrator without a header reads
// every branch's. Every row carries `available` and `uom` beside the
// quantities, in the product's stocking unit.
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
			`INSERT INTO inventory (product_id, location_id, location, quantity, allocated) VALUES ($1, $2, $3, 5, 2)`,
			f.productID, r.yard, r.name); err != nil {
			t.Fatalf("seed inventory: %v", err)
		}
	}
	// The fixture's cleanup already deletes this product's inventory rows.
	// One legacy row with only the deprecated location text and no
	// location_id: it has no branch on its joined location, so no scoped arm
	// matches it, and it stays visible to an administrator without a header
	// and to callers with no branch context only (the contract row names the
	// choice; C4-1 migrates legacy rows onto locations).
	legacyName := "wl-inv-legacy-" + f.productID.String()[:8]
	if _, err := db.Pool.Exec(context.Background(),
		`INSERT INTO inventory (product_id, location, quantity) VALUES ($1, $2, 7)`,
		f.productID, legacyName); err != nil {
		t.Fatalf("seed legacy inventory: %v", err)
	}

	list := func(role, sub, header string) invLevelPage {
		t.Helper()
		status, body := f.callBody(t, "GET", "/api/v1/inventory?product_id="+f.productID.String(), "", role, sub, header)
		if status != http.StatusOK {
			t.Fatalf("inventory list as %s/%s: %d %s", role, sub, status, body)
		}
		var page invLevelPage
		if err := json.Unmarshal(body, &page); err != nil {
			t.Fatalf("inventory list body is not the envelope: %v\n%s", err, body)
		}
		if page.Limit == 0 {
			t.Fatalf("inventory list carries no limit: %s", body)
		}
		return page
	}
	// listTotal is the same call with include=total; the count must hold the
	// same three arm predicate as the row page, so the answer agrees with the
	// page's row count for every caller.
	listTotal := func(role, sub, header string) (invLevelPage, int) {
		t.Helper()
		status, body := f.callBody(t, "GET",
			"/api/v1/inventory?product_id="+f.productID.String()+"&include=total",
			"", role, sub, header)
		if status != http.StatusOK {
			t.Fatalf("inventory list+total as %s/%s: %d %s", role, sub, status, body)
		}
		var page invLevelPage
		if err := json.Unmarshal(body, &page); err != nil {
			t.Fatalf("inventory list+total body is not the envelope: %v\n%s", err, body)
		}
		if page.Total == nil {
			t.Fatalf("inventory list+total as %s/%s: total missing: %s", role, sub, body)
		}
		return page, *page.Total
	}
	rowAt := func(page invLevelPage, yard string) bool {
		for _, it := range page.Items {
			if it.LocationID != nil && *it.LocationID == yard {
				if it.Available != "3" || it.UOM != "PCS" || it.ProductID != f.productID.String() {
					t.Errorf("row at %s carries available %q uom %q product %q, want 3 / PCS / the seeded product",
						yard, it.Available, it.UOM, it.ProductID)
				}
				return true
			}
		}
		return false
	}
	legacyIn := func(page invLevelPage) bool {
		for _, it := range page.Items {
			if it.LocationID == nil && it.Location == legacyName {
				return true
			}
		}
		return false
	}

	for _, c := range []struct {
		name, role, sub, header string
		wantRows                int
		wantA, wantB            bool
		wantLegacy              bool
	}{
		{"warehouse, header A", "warehouse", "u-a", A, 1, true, false, false},
		{"warehouse, no header", "warehouse", "u-a", "", 1, true, false, false},
		{"warehouse u-none, no header", "warehouse", "u-none", "", 0, false, false, false},
		{"admin, no header", "admin", "boss", "", 3, true, true, true},
	} {
		page := list(c.role, c.sub, c.header)
		if len(page.Items) != c.wantRows {
			t.Errorf("inventory list, %s: %d rows, want %d", c.name, len(page.Items), c.wantRows)
		}
		if got := rowAt(page, f.yardA.String()); got != c.wantA {
			t.Errorf("inventory list, %s: branch A's row present = %v, want %v", c.name, got, c.wantA)
		}
		if got := rowAt(page, f.yardB.String()); got != c.wantB {
			t.Errorf("inventory list, %s: branch B's row present = %v, want %v", c.name, got, c.wantB)
		}
		if got := legacyIn(page); got != c.wantLegacy {
			t.Errorf("inventory list, %s: legacy row present = %v, want %v", c.name, got, c.wantLegacy)
		}
		// include=total must hold the same three arm predicate as the row
		// page: the count agrees with the page's row count for every caller.
		// A drift in either side (the page starts matching the wrong rows,
		// the count stops filtering) shows up as a mismatch.
		_, total := listTotal(c.role, c.sub, c.header)
		if total != c.wantRows {
			t.Errorf("inventory count, %s: total = %d, want %d (the page's row count, the count and the page share the wall)", c.name, total, c.wantRows)
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

// quoteAnalytics is the part of the analytics response the test reads.
type quoteAnalytics struct {
	TotalQuotes          int   `json:"total_quotes"`
	DraftCount           int   `json:"draft_count"`
	TotalQuoteValueCents int64 `json:"total_quote_value_cents"`
	TrendData            []struct {
		Created int `json:"created"`
	} `json:"trend_data"`
}

// The quote analytics route reads the same table as the quote list, so its
// three queries carry the same three arm branch predicate (ADR 0007 section
// 2.3): a caller held to branch A counts branch A's quotes only, through its
// context branch or, with none, through its grants; a bound user with no
// grants counts none; an administrator without a header counts every
// branch's, and the other packages' quotes mean its count is at least the
// fixture's two, never assumed exact.
func TestBranchWall_QuoteAnalytics(t *testing.T) {
	db := testutil.RequireDB(t)
	f := newWallFixture(t, db, true)
	A := f.branchA.String()

	quoteA, quoteB := uuid.New(), uuid.New()
	seedWallQuote(t, db, quoteA, f.branchA, f.docCust, "WLQ-"+quoteA.String()[:8])
	seedWallQuote(t, db, quoteB, f.branchB, f.docCust, "WLQ-"+quoteB.String()[:8])
	t.Cleanup(func() {
		_, _ = db.Pool.Exec(context.Background(), `DELETE FROM quotes WHERE id IN ($1, $2)`, quoteA, quoteB)
	})

	read := func(t *testing.T, body []byte) quoteAnalytics {
		t.Helper()
		var a quoteAnalytics
		if err := json.Unmarshal(body, &a); err != nil {
			t.Fatalf("analytics body: %v\n%s", err, body)
		}
		return a
	}
	trendCreated := func(a quoteAnalytics) int {
		n := 0
		for _, d := range a.TrendData {
			n += d.Created
		}
		return n
	}
	for _, c := range []struct {
		name, role, sub, header string
		wantTotal               int
		wantTotalLowerBound     bool
	}{
		{"sales, header A", "sales", "u-a", A, 1, false},
		{"sales, no header", "sales", "u-a", "", 1, false},
		{"sales u-none, no header", "sales", "u-none", "", 0, false},
		{"admin, no header", "admin", "boss", "", 2, true},
	} {
		status, body := f.callBody(t, "GET", "/api/v1/quotes/analytics", "", c.role, c.sub, c.header)
		if status != http.StatusOK {
			t.Errorf("quote analytics, %s: %d, want 200", c.name, status)
			continue
		}
		a := read(t, body)
		if c.wantTotalLowerBound {
			if a.TotalQuotes < c.wantTotal {
				t.Errorf("quote analytics, %s: total_quotes = %d, want at least %d", c.name, a.TotalQuotes, c.wantTotal)
			}
			continue
		}
		if a.TotalQuotes != c.wantTotal {
			t.Errorf("quote analytics, %s: total_quotes = %d, want %d", c.name, a.TotalQuotes, c.wantTotal)
		}
		if a.DraftCount != c.wantTotal {
			t.Errorf("quote analytics, %s: draft_count = %d, want %d", c.name, a.DraftCount, c.wantTotal)
		}
		if want := int64(c.wantTotal) * 1000; a.TotalQuoteValueCents != want {
			t.Errorf("quote analytics, %s: total_quote_value_cents = %d, want %d", c.name, a.TotalQuoteValueCents, want)
		}
		if got := trendCreated(a); got != c.wantTotal {
			t.Errorf("quote analytics, %s: trend created total = %d, want %d", c.name, got, c.wantTotal)
		}
	}
}

// poRefreshSummary is the part of the refresh-reorder-targets response the
// test reads. The full set the service returns lives in service.go's
// RefreshResult; the test pins the count and the proposal product set.
type poRefreshSummary struct {
	DryRun          bool `json:"dry_run"`
	ProductsUpdated int  `json:"products_updated"`
	ProductsSkipped int  `json:"products_skipped"`
}

// The purchase order refresh-reorder-targets route runs behind the branch
// middleware, so before this fix a bound purchasing user's refresh would
// compute from the caller's branches' sales and write the resulting targets
// onto a per product field that is shared across every branch, while the
// scheduler (context.Background, no branch context) wrote targets from every
// branch's sales. The handler now strips the BranchContext the middleware
// set and marks the resulting context as a system caller, the seam the
// scheduler already is, so the velocity read sees every branch's data and a
// bound user's refresh writes the same targets an administrator's would.
// The cron path is identical: Scheduler.runRefresh calls the same service
// with context.Background, which is arm 3. The test pins both: as
// purchasing with header A and as admin with no header, the same proposal
// set is returned (the fixture's seed sets branch A to 90 units and branch
// B to 900 in the lookback window, so the fixture product is in the
// proposal set on both arms; the proposal counts match exactly); the
// write mode lands the recomputed target on the products row, and an
// admin's write equals a bound user's write.
func TestBranchWall_PORefreshReorderTargets(t *testing.T) {
	testutil.LockOutboxTables(t) // the refresh writes product.updated events
	db := testutil.RequireDB(t)
	f := newWallFixture(t, db, true)
	A := f.branchA.String()
	ctx := context.Background()

	// Reset the fixture product's targets so the proposal set is the one
	// the recompute would write from scratch (no leftover baseline from an
	// earlier test or migration).
	if _, err := db.Pool.Exec(ctx, `UPDATE products SET reorder_point = 0, reorder_qty = 0 WHERE id = $1`, f.productID); err != nil {
		t.Fatalf("reset product reorder targets: %v", err)
	}
	t.Cleanup(func() {
		_, _ = db.Pool.Exec(ctx, `UPDATE products SET reorder_point = 0, reorder_qty = 0 WHERE id = $1`, f.productID)
	})

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

	readSummary := func(t *testing.T, body []byte) poRefreshSummary {
		t.Helper()
		var s poRefreshSummary
		if err := json.Unmarshal(body, &s); err != nil {
			t.Fatalf("refresh body: %v\n%s", err, body)
		}
		return s
	}
	readProduct := func(t *testing.T) (reorderPoint, reorderQty float64) {
		t.Helper()
		if err := db.Pool.QueryRow(ctx,
			`SELECT reorder_point, reorder_qty FROM products WHERE id = $1`,
			f.productID).Scan(&reorderPoint, &reorderQty); err != nil {
			t.Fatalf("read product: %v", err)
		}
		return
	}

	// Bound purchasing with header A: dry_run=true first so we can read the
	// proposal set without committing. The fixture product's recomputed
	// target uses every branch's sales (90 + 900 = 990 units over 90 days,
	// 11 units/day, lead time 7 days, 1.5 safety => point = ceil(11*7*1.5)
	// = 116; qty = ceil(11*30) = 330). The proposal set must contain the
	// fixture product; the count must match.
	status, body := f.callBody(t, "POST", "/api/v1/purchase-orders/refresh-reorder-targets",
		`{"dry_run":true,"lookback_days":90}`, "purchasing", "u-a", A)
	if status != http.StatusOK {
		t.Fatalf("refresh dry-run, purchasing header A: %d, want 200: %s", status, body)
	}
	dryRunPurchasing := readSummary(t, body)
	if dryRunPurchasing.DryRun != true {
		t.Errorf("refresh dry-run, purchasing header A: dry_run = %v, want true", dryRunPurchasing.DryRun)
	}
	if dryRunPurchasing.ProductsUpdated < 1 {
		t.Errorf("refresh dry-run, purchasing header A: products_updated = %d, want at least 1 (the fixture product)",
			dryRunPurchasing.ProductsUpdated)
	}

	// Dry run must not have written the products row.
	if pp, pq := readProduct(t); pp != 0 || pq != 0 {
		t.Errorf("refresh dry-run wrote the products row: point = %v, qty = %v, want both 0", pp, pq)
	}

	// Administrator with no header: dry-run proposal set and count must
	// match the bound purchasing user's proposal set and count exactly
	// (the recompute sees every branch's data on both arms now, so both
	// produce the same proposal).
	status, body = f.callBody(t, "POST", "/api/v1/purchase-orders/refresh-reorder-targets",
		`{"dry_run":true,"lookback_days":90}`, "admin", "boss", "")
	if status != http.StatusOK {
		t.Fatalf("refresh dry-run, admin no header: %d, want 200: %s", status, body)
	}
	dryRunAdmin := readSummary(t, body)
	if dryRunAdmin.ProductsUpdated != dryRunPurchasing.ProductsUpdated {
		t.Errorf("refresh dry-run: admin products_updated = %d, want %d (matches the bound purchasing arm)",
			dryRunAdmin.ProductsUpdated, dryRunPurchasing.ProductsUpdated)
	}
	if dryRunAdmin.ProductsSkipped != dryRunPurchasing.ProductsSkipped {
		t.Errorf("refresh dry-run: admin products_skipped = %d, want %d (matches the bound purchasing arm)",
			dryRunAdmin.ProductsSkipped, dryRunPurchasing.ProductsSkipped)
	}

	// The bound write: a bound purchasing user with header A must write
	// the same target an administrator's write would. The point for the
	// fixture product is ceil(11*7*1.5) = 116 and the qty is ceil(11*30)
	// = 330. After both writes the products row carries these numbers.
	if _, err := db.Pool.Exec(ctx, `UPDATE products SET reorder_point = 0, reorder_qty = 0 WHERE id = $1`, f.productID); err != nil {
		t.Fatalf("reset before bound write: %v", err)
	}
	status, body = f.callBody(t, "POST", "/api/v1/purchase-orders/refresh-reorder-targets",
		`{"dry_run":false,"lookback_days":90}`, "purchasing", "u-a", A)
	if status != http.StatusOK {
		t.Fatalf("refresh write, purchasing header A: %d, want 200: %s", status, body)
	}
	boundPoint, boundQty := readProduct(t)

	if _, err := db.Pool.Exec(ctx, `UPDATE products SET reorder_point = 0, reorder_qty = 0 WHERE id = $1`, f.productID); err != nil {
		t.Fatalf("reset before admin write: %v", err)
	}
	status, body = f.callBody(t, "POST", "/api/v1/purchase-orders/refresh-reorder-targets",
		`{"dry_run":false,"lookback_days":90}`, "admin", "boss", "")
	if status != http.StatusOK {
		t.Fatalf("refresh write, admin no header: %d, want 200: %s", status, body)
	}
	adminPoint, adminQty := readProduct(t)

	if boundPoint != adminPoint {
		t.Errorf("refresh write: bound purchasing wrote reorder_point = %v, admin wrote %v, want equal",
			boundPoint, adminPoint)
	}
	if boundQty != adminQty {
		t.Errorf("refresh write: bound purchasing wrote reorder_qty = %v, admin wrote %v, want equal",
			boundQty, adminQty)
	}
	// Pin the actual numbers too: any future change to the lead time
	// default or the safety factor will surface here.
	if wantPoint, wantQty := 116.0, 330.0; adminPoint != wantPoint || adminQty != wantQty {
		t.Errorf("refresh write: admin wrote point = %v, qty = %v, want %v, %v (every branch's sales: 90 + 900 = 990 over 90 days)",
			adminPoint, adminQty, wantPoint, wantQty)

	}
}

// TestBranchWall_CatalogReads: a product read sums stock over the caller's
// branches only (ADR 0007 section 2.3, ADR 0006 7.1), and a branch read by id
// is held to the record rule: a branch the caller may not target is a 403.
func TestBranchWall_CatalogReads(t *testing.T) {
	testutil.LockOutboxTables(t) // the route calls record outbox events
	db := testutil.RequireDB(t)
	f := newWallFixture(t, db, true)
	A, B := f.branchA.String(), f.branchB.String()
	for _, r := range []struct {
		yard           uuid.UUID
		qty, allocated int
	}{{f.yardA, 10, 2}, {f.yardB, 100, 5}} {
		if _, err := db.Pool.Exec(context.Background(),
			`INSERT INTO inventory (product_id, location, location_id, quantity, allocated) VALUES ($1, 'wl', $2, $3, $4)`,
			f.productID, r.yard, r.qty, r.allocated); err != nil {
			t.Fatalf("seed stock: %v", err)
		}
	}
	stock := func(role, sub, header string) [3]string {
		status, body := f.callBody(t, "GET", "/api/v1/products/"+f.productID.String(), "", role, sub, header)
		if status != http.StatusOK {
			t.Fatalf("%s product read: %d %s", role, status, body)
		}
		var v struct{ OnHand, Allocated, Available string }
		var raw map[string]any
		if err := json.Unmarshal(body, &raw); err != nil {
			t.Fatal(err)
		}
		v.OnHand, v.Allocated, v.Available = raw["on_hand"].(string), raw["allocated"].(string), raw["available"].(string)
		return [3]string{v.OnHand, v.Allocated, v.Available}
	}
	if got := stock("warehouse", "u-a", A); got != [3]string{"10", "2", "8"} {
		t.Errorf("a branch A caller sums %v, want its own stock 10/2/8", got)
	}
	if got := stock("warehouse", "u-a", ""); got != [3]string{"10", "2", "8"} {
		t.Errorf("a bound caller with no header sums %v, want its granted branches 10/2/8", got)
	}
	if got := stock("admin", "boss", ""); got != [3]string{"110", "7", "103"} {
		t.Errorf("an administrator with no header sums %v, want every branch 110/7/103", got)
	}
	if got := stock("admin", "boss", B); got != [3]string{"100", "5", "95"} {
		t.Errorf("an administrator in branch B sums %v, want 100/5/95", got)
	}

	// A branch read by id stays unwalled, as the branch list is (PR 39's
	// decision): the desk's Branch Users page reads a branch other than the
	// one the administrator works in.
	for _, c := range []struct{ role, sub, header string }{
		{"admin", "boss", A}, {"admin", "boss", ""}, {"sales", "u-a", A},
	} {
		if got := f.call(t, "GET", "/api/v1/branches/"+B, "", c.role, c.sub, c.header); got != http.StatusOK {
			t.Errorf("%s with header %q reading branch B: %d, want 200", c.role, c.header, got)
		}
	}

	// PR 40's kit component routes sit behind the product wall: a header for
	// a branch the caller is not granted is refused, the caller's own is not.
	kit := "/api/v1/products/" + f.productID.String() + "/kit-components"
	if got := f.call(t, "GET", kit, "", "sales", "u-a", B); got != http.StatusForbidden {
		t.Errorf("kit components under an ungranted branch header: %d, want 403", got)
	}
	if got := f.call(t, "GET", kit, "", "sales", "u-a", A); got != http.StatusOK {
		t.Errorf("kit components under the caller's own branch: %d, want 200", got)
	}
	// A branch route refuses a path id that is not a branch.
	if got := f.call(t, "PUT", "/api/v1/branches/"+f.yardA.String(), `{"code":"x","revision":1}`, "admin", "boss", ""); got != http.StatusNotFound {
		t.Errorf("branch update of a yard: %d, want 404", got)
	}
}

// The charge code master through serve's wiring: every pricing role reads it,
// and a write takes the narrower guard, so a sales user cannot create or edit
// a code (and with it the revenue account a charge line posts to).
func TestBranchWall_ChargeCodeWritesTakeTheFinanceGuard(t *testing.T) {
	testutil.LockOutboxTables(t) // the fixture's route calls record outbox events
	db := testutil.RequireDB(t)
	f := newWallFixture(t, db, true)
	const missing = "/api/v1/charge-codes/00000000-0000-0000-0000-000000000001"
	for _, role := range []string{"admin", "owner", "sales", "finance"} {
		if got := f.call(t, "GET", "/api/v1/charge-codes", "", role, "u-a", ""); got != http.StatusOK {
			t.Errorf("%s lists charge codes: %d, want 200", role, got)
		}
	}
	// warehouse prices inventory but never writes or reads charge codes, so
	// the read guard refuses it as surely as the write guard does.
	if got := f.call(t, "GET", "/api/v1/charge-codes", "", "warehouse", "u-a", ""); got != http.StatusForbidden {
		t.Errorf("warehouse lists charge codes: %d, want 403", got)
	}
	for _, c := range []struct{ method, path string }{{"POST", "/api/v1/charge-codes"}, {"PUT", missing}} {
		if got := f.call(t, c.method, c.path, `{}`, "sales", "u-a", ""); got != http.StatusForbidden {
			t.Errorf("sales %s %s: %d, want 403", c.method, c.path, got)
		}
		if got := f.call(t, c.method, c.path, `{}`, "warehouse", "u-a", ""); got != http.StatusForbidden {
			t.Errorf("warehouse %s %s: %d, want 403", c.method, c.path, got)
		}
		// Past the guard an empty body is refused by the handler, not the role.
		if got := f.call(t, c.method, c.path, `{}`, "finance", "u-a", ""); got == http.StatusForbidden {
			t.Errorf("finance %s %s: 403, want the guard to admit finance", c.method, c.path)
		}
	}
}

// The invoice and credit memo routes through serve's wiring: every read is held
// to the caller's wall (a context branch, else the caller's grants, else every
// branch for an administrator), a record outside it is a 404 on every route
// that names it, the list covers the caller's branches only, and a branch a
// credit memo create names is held to the payload branch rule (403 naming
// branch_id).
func TestBranchWall_InvoiceAndCreditMemoRoutes(t *testing.T) {
	testutil.LockOutboxTables(t)
	db := testutil.RequireDB(t)
	f := newWallFixture(t, db, true)
	A, B := f.branchA.String(), f.branchB.String()

	const ok = http.StatusOK
	const no = http.StatusForbidden
	const gone = http.StatusNotFound
	free := func(branch string) string {
		b := ""
		if branch != "" {
			b = fmt.Sprintf(`"branch_id":%q,`, branch)
		}
		return fmt.Sprintf(`{%s"customer_id":%q,"reason_code":"price_adjustment","reason":"wall","lines":[{"line_type":"charge","charge_code":"ADJUST","quantity":"-1","unit_price_ten_thousandths":1000}]}`, b, f.docCust)
	}
	for _, c := range []struct {
		name, method, path, body, role, sub, header string
		want                                        int
	}{
		// reads of a record by id
		{"get own invoice", "GET", "/api/v1/invoices/" + f.invA.String(), "", "sales", "u-a", A, ok},
		{"get foreign invoice, header A", "GET", "/api/v1/invoices/" + f.invB.String(), "", "sales", "u-a", A, gone},
		{"get foreign invoice, no header", "GET", "/api/v1/invoices/" + f.invB.String(), "", "sales", "u-a", "", gone},
		{"get own invoice, no header", "GET", "/api/v1/invoices/" + f.invA.String(), "", "sales", "u-a", "", ok},
		{"get foreign invoice, admin", "GET", "/api/v1/invoices/" + f.invB.String(), "", "admin", "boss", "", ok},
		{"get own credit memo", "GET", "/api/v1/credit-memos/" + f.memoA.String(), "", "finance", "u-a", A, ok},
		{"get foreign credit memo, header A", "GET", "/api/v1/credit-memos/" + f.memoB.String(), "", "finance", "u-a", A, gone},
		{"get foreign credit memo, no header", "GET", "/api/v1/credit-memos/" + f.memoB.String(), "", "finance", "u-a", "", gone},
		{"get foreign credit memo, admin", "GET", "/api/v1/credit-memos/" + f.memoB.String(), "", "admin", "boss", "", ok},
		// writes that address a record by path id
		{"void foreign invoice, no header", "POST", "/api/v1/invoices/" + f.invB.String() + "/transitions", `{"to":"void","revision":1,"reason":"x"}`, "finance", "u-a", "", gone},
		{"void foreign invoice, header A", "POST", "/api/v1/invoices/" + f.invB.String() + "/transitions", `{"to":"void","revision":1,"reason":"x"}`, "finance", "u-a", A, gone},
		{"void own invoice with a credit memo, header A", "POST", "/api/v1/invoices/" + f.invA.String() + "/transitions", `{"to":"void","revision":1,"reason":"x"}`, "finance", "u-a", A, http.StatusConflict},
		{"post foreign credit memo, no header", "POST", "/api/v1/credit-memos/" + f.memoB.String() + "/transitions", `{"to":"open","revision":1}`, "finance", "u-a", "", gone},
		{"void foreign credit memo, header A", "POST", "/api/v1/credit-memos/" + f.memoB.String() + "/transitions", `{"to":"void","revision":1,"reason":"x"}`, "finance", "u-a", A, gone},
		{"edit foreign credit memo, no header", "PUT", "/api/v1/credit-memos/" + f.memoB.String(), strings.Replace(free(""), "{", `{"revision":1,`, 1), "sales", "u-a", "", gone},
		// the payload branch rule on the create
		{"create naming a foreign branch, header A", "POST", "/api/v1/credit-memos", free(B), "sales", "u-a", A, no},
		{"create naming a foreign branch, no header", "POST", "/api/v1/credit-memos", free(B), "sales", "u-a", "", no},
		{"create naming its own branch", "POST", "/api/v1/credit-memos", free(A), "sales", "u-a", A, http.StatusCreated},
		{"create naming its own branch, no header", "POST", "/api/v1/credit-memos", free(A), "sales", "u-a", "", http.StatusCreated},
		{"create naming a foreign branch, admin", "POST", "/api/v1/credit-memos", free(B), "admin", "boss", "", http.StatusConflict}, // branch B has no tax rate: a refusal, not the wall
		{"create against a foreign invoice, header A", "POST", "/api/v1/credit-memos", fmt.Sprintf(`{"invoice_id":%q,"reason_code":"return","reason":"x","lines":[{"line_type":"charge","charge_code":"ADJUST","quantity":"-1","unit_price_ten_thousandths":1000}]}`, f.invB), "sales", "u-a", A, http.StatusBadRequest},
		// a role the module does not serve
		{"warehouse reads an invoice", "GET", "/api/v1/invoices/" + f.invA.String(), "", "warehouse", "u-a", A, no},
	} {
		if got := f.call(t, c.method, c.path, c.body, c.role, c.sub, c.header); got != c.want {
			t.Errorf("%s: %d, want %d", c.name, got, c.want)
		}
	}

	// the lists cover the caller's branches only
	for _, c := range []struct {
		name, path, role, sub, header string
		wantA, wantB                  bool
		idA, idB                      string
	}{
		{"invoices, header A", "/api/v1/invoices?limit=200", "sales", "u-a", A, true, false, f.invA.String(), f.invB.String()},
		{"invoices, no header", "/api/v1/invoices?limit=200", "sales", "u-a", "", true, false, f.invA.String(), f.invB.String()},
		{"invoices, a user with no grants", "/api/v1/invoices?limit=200", "sales", "u-none", "", false, false, f.invA.String(), f.invB.String()},
		{"invoices, admin", "/api/v1/invoices?limit=200&customer_id=" + f.docCust.String(), "admin", "boss", "", true, true, f.invA.String(), f.invB.String()},
		{"credit memos, header A", "/api/v1/credit-memos?limit=200", "finance", "u-a", A, true, false, f.memoA.String(), f.memoB.String()},
		{"credit memos, no header", "/api/v1/credit-memos?limit=200", "finance", "u-a", "", true, false, f.memoA.String(), f.memoB.String()},
		{"credit memos, a user with no grants", "/api/v1/credit-memos?limit=200", "finance", "u-none", "", false, false, f.memoA.String(), f.memoB.String()},
		{"credit memos, admin", "/api/v1/credit-memos?limit=200&customer_id=" + f.docCust.String(), "admin", "boss", "", true, true, f.memoA.String(), f.memoB.String()},
	} {
		status, body := f.callBody(t, "GET", c.path, "", c.role, c.sub, c.header)
		if status != http.StatusOK {
			t.Errorf("%s: %d, want 200", c.name, status)
			continue
		}
		if got := strings.Contains(string(body), c.idA); got != c.wantA {
			t.Errorf("%s: branch A's record present = %v, want %v", c.name, got, c.wantA)
		}
		if got := strings.Contains(string(body), c.idB); got != c.wantB {
			t.Errorf("%s: branch B's record present = %v, want %v", c.name, got, c.wantB)
		}
	}
}
