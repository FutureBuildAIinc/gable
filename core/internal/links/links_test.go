// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package links_test

// The link resolver (ADR 0007 section 8) on the wire: GET
// /api/v1/links/<module>/{id} answers every frontend's record URL for a
// record the caller can see, through the module's own read, so a record the
// caller cannot see is a 404; the record segment is the number when the
// entity has one; links.agent comes from the template and is null without
// it; desk links are absolute when GABLE_PUBLIC_URL is set and relative
// otherwise.

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gablelbm/gable/internal/account"
	"github.com/gablelbm/gable/internal/customer"
	"github.com/gablelbm/gable/internal/drafts"
	"github.com/gablelbm/gable/internal/gl"
	"github.com/gablelbm/gable/internal/invoice"
	"github.com/gablelbm/gable/internal/links"
	"github.com/gablelbm/gable/internal/order"
	"github.com/gablelbm/gable/internal/product"
	"github.com/gablelbm/gable/internal/quote"
	"github.com/gablelbm/gable/internal/testutil"
	"github.com/gablelbm/gable/pkg/actor"
	"github.com/gablelbm/gable/pkg/audit"
	"github.com/gablelbm/gable/pkg/database"
	"github.com/gablelbm/gable/pkg/middleware"
	"github.com/gablelbm/gable/pkg/outbox"
	"github.com/google/uuid"
)

// fixture wires the resolver the way serve does, over real module services
// (quotes, orders, customers, products) and the quotes draft kind; every
// entity resolves through its module's own read.
type fixture struct {
	t          *testing.T
	db         *database.DB
	srv        *httptest.Server
	customerID uuid.UUID
	productID  uuid.UUID
	sku        string
}

func newFixture(t *testing.T, db *database.DB, settings links.Settings) *fixture {
	t.Helper()
	t.Setenv("AUTH_MODE", "dev")
	ctx := context.Background()
	f := &fixture{t: t, db: db, customerID: uuid.New(), productID: uuid.New(), sku: "LINK-" + uuid.NewString()[:8]}
	branch := `(SELECT value::uuid FROM system_settings WHERE key = 'default_branch_id')`
	if _, err := db.Pool.Exec(ctx, `INSERT INTO customers (id, name, account_number, primary_branch_id)
		VALUES ($1, 'Links Wire Co', $2, `+branch+`)`, f.customerID, "LINKC-"+uuid.NewString()[:8]); err != nil {
		t.Fatalf("seed customer: %v", err)
	}
	if _, err := db.Pool.Exec(ctx, `INSERT INTO products (id, sku, description, uom_primary, base_price)
		VALUES ($1, $2, '2x4x8 SPF', 'PCS', 5.5)`, f.productID, f.sku); err != nil {
		t.Fatalf("seed product: %v", err)
	}
	if _, err := db.Pool.Exec(ctx, `UPDATE locations SET default_tax_rate = 0.088750
		WHERE id = `+branch); err != nil {
		t.Fatalf("seed branch rate: %v", err)
	}

	quoteSvc := quote.NewService(quote.NewRepository(db)).
		WithOutbox(outbox.NewWriter(db, "")).
		WithTxRunner(db).
		WithAudit(audit.NewLogger(db)).
		WithBranchGuard(middleware.NewBranchGuard(db))
	orderSvc := order.NewService(order.NewRepository(db)).
		WithOutbox(outbox.NewWriter(db, "")).WithTxRunner(db).WithAuditLog(audit.NewLogger(db))
	customerSvc := customer.NewService(customer.NewRepository(db))
	productSvc := product.NewService(product.NewRepository(db))
	glSvc := gl.NewService(gl.NewRepository(db), nil, slog.Default())
	invoiceSvc := invoice.NewService(invoice.NewRepository(db), glSvc, account.NewService(account.NewRepository(db), db, slog.Default()), db)
	quoteKind := quote.NewDraftKind(quoteSvc)
	orderKind := order.NewDraftKind(orderSvc)
	draftsRepo := drafts.NewRepository(db)
	registry, err := drafts.NewRegistry(quoteKind, orderKind)
	if err != nil {
		t.Fatal(err)
	}
	hub := drafts.NewHub(draftsRepo, 50_000_000, nil)
	t.Cleanup(hub.Stop)
	draftsSvc := drafts.NewService(draftsRepo, registry).
		WithOutbox(outbox.NewWriter(db, "")).
		WithTxRunner(db).
		WithAudit(audit.NewLogger(db)).
		WithBranchGuard(middleware.NewBranchGuard(db)).
		WithFeed(hub)
	draftsHandler := drafts.NewHandler(draftsSvc).WithFeedHandler(
		drafts.NewFeedHandler(draftsSvc, draftsRepo, hub, drafts.DefaultFeedSettings(), nil))

	quoteRow, _ := links.RowFor("quotes", false)
	orderRow, _ := links.RowFor("orders", false)
	invoiceRow, _ := links.RowFor("invoices", false)
	customerRow, _ := links.RowFor("customers", false)
	productRow, _ := links.RowFor("products", false)
	quoteDraftRow, _ := links.RowFor("quotes", true)
	orderDraftRow, _ := links.RowFor("orders", true)
	handler := links.NewHandler(settings,
		links.Entity{Row: quoteRow, Resolve: func(ctx context.Context, raw string) (uuid.UUID, string, error) {
			q, err := quoteSvc.GetQuoteByIDOrNumber(ctx, raw)
			if err != nil {
				return uuid.Nil, "", err
			}
			return q.ID, q.Number, nil
		}},
		links.Entity{Row: orderRow, Resolve: func(ctx context.Context, raw string) (uuid.UUID, string, error) {
			o, err := orderSvc.GetOrderByIDOrNumber(ctx, raw)
			if err != nil {
				return uuid.Nil, "", err
			}
			return o.ID, o.Number, nil
		}},
		links.Entity{Row: invoiceRow, Resolve: func(ctx context.Context, raw string) (uuid.UUID, string, error) {
			inv, err := invoiceSvc.GetInvoiceByIDOrNumber(ctx, raw)
			if err != nil {
				return uuid.Nil, "", err
			}
			return inv.ID, inv.Number, nil
		}},
		links.Entity{Row: customerRow, Resolve: func(ctx context.Context, raw string) (uuid.UUID, string, error) {
			c, err := customerSvc.Get(ctx, uuid.MustParse(raw))
			if err != nil {
				return uuid.Nil, "", err
			}
			return c.ID, "", nil
		}},
		links.Entity{Row: productRow, Resolve: func(ctx context.Context, raw string) (uuid.UUID, string, error) {
			p, err := productSvc.GetProduct(ctx, uuid.MustParse(raw))
			if err != nil {
				return uuid.Nil, "", err
			}
			return p.ID, "", nil
		}},
		links.Entity{Row: quoteDraftRow, Resolve: func(ctx context.Context, raw string) (uuid.UUID, string, error) {
			d, err := draftsSvc.Get(ctx, "quotes", uuid.MustParse(raw))
			if err != nil {
				return uuid.Nil, "", err
			}
			return d.ID, "", nil
		}},
		links.Entity{Row: orderDraftRow, Resolve: func(ctx context.Context, raw string) (uuid.UUID, string, error) {
			d, err := draftsSvc.Get(ctx, "orders", uuid.MustParse(raw))
			if err != nil {
				return uuid.Nil, "", err
			}
			return d.ID, "", nil
		}},
	)

	mux := http.NewServeMux()
	branchMw := middleware.NewBranchMiddleware(db).Handler
	quote.RegisterDraftRoutes(mux, draftsHandler, quoteKind, branchMw)
	quote.NewHandler(quoteSvc).RegisterRoutes(mux, branchMw)
	order.NewHandler(orderSvc).RegisterRoutes(mux, branchMw)
	links.RegisterAll(mux, handler, links.Guards{
		Quotes:      branchMw,
		Orders:      branchMw,
		Invoices:    branchMw,
		Customers:   branchMw,
		Products:    branchMw,
		DraftQuotes: branchMw,
		DraftOrders: branchMw,
	})
	f.srv = httptest.NewServer(actor.Middleware(middleware.Idempotency(db)(mux)))

	t.Cleanup(func() {
		f.srv.Close()
		_, _ = db.Pool.Exec(ctx, `DELETE FROM draft_events WHERE module='quotes'`)
		_, _ = db.Pool.Exec(ctx, `DELETE FROM drafts WHERE module='quotes'`)
		_, _ = db.Pool.Exec(ctx, `DELETE FROM audit_log WHERE entity_type='draft'`)
		_, _ = db.Pool.Exec(ctx, `DELETE FROM events_outbox WHERE entity_type IN ('draft','quote')`)
		_, _ = db.Pool.Exec(ctx, `DELETE FROM quotes WHERE customer_id = $1`, f.customerID)
		_, _ = db.Pool.Exec(ctx, `DELETE FROM order_lines WHERE order_id IN (SELECT id FROM orders WHERE customer_id = $1)`, f.customerID)
		_, _ = db.Pool.Exec(ctx, `DELETE FROM orders WHERE customer_id = $1`, f.customerID)
		_, _ = db.Pool.Exec(ctx, `DELETE FROM products WHERE id = $1`, f.productID)
		_, _ = db.Pool.Exec(ctx, `DELETE FROM customers WHERE id = $1`, f.customerID)
	})
	return f
}

func (f *fixture) do(method, path string, body any) linkResp {
	f.t.Helper()
	var rdr io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			f.t.Fatal(err)
		}
		rdr = strings.NewReader(string(raw))
	}
	req, err := http.NewRequest(method, f.srv.URL+path, rdr)
	if err != nil {
		f.t.Fatal(err)
	}
	if rdr != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		f.t.Fatal(err)
	}
	defer res.Body.Close()
	raw, _ := io.ReadAll(res.Body)
	out := linkResp{status: res.StatusCode, raw: raw}
	dec := json.NewDecoder(strings.NewReader(string(raw)))
	dec.UseNumber()
	_ = dec.Decode(&out.body)
	return out
}

type linkResp struct {
	status int
	body   map[string]any
	raw    []byte
}

func (f *fixture) quotePayload() map[string]any {
	return map[string]any{
		"customer_id":   f.customerID.String(),
		"delivery_type": "pickup",
		"lines": []map[string]any{{
			"product_id": f.productID.String(), "sku": f.sku, "description": "2x4x8 SPF",
			"quantity": "10", "uom": "PCS", "unit_price_ten_thousandths": 55000,
		}},
	}
}

// TestLinksResolveEveryEntity pins the resolver for each entity: quotes by
// number and by UUID, orders, customers, products and a quote draft, each
// answering the desk, the front door and the app links, with the record
// segment the number when the entity has one and the UUID otherwise.
func TestLinksResolveEveryEntity(t *testing.T) {
	f := newFixture(t, testutil.RequireDB(t), links.Settings{})

	// A quote, an order and a quote draft exist.
	q := f.do("POST", "/api/v1/quotes", f.quotePayload())
	if q.status != http.StatusCreated {
		t.Fatalf("create quote = %d: %s", q.status, q.raw)
	}
	quoteID, number := linkStr(t, q.body, "id"), linkStr(t, q.body, "number")
	o := f.do("POST", "/api/v1/orders", map[string]any{
		"customer_id": f.customerID.String(), "delivery_type": "pickup",
		"lines": []map[string]any{{"product_id": f.productID.String(), "quantity": "10"}}})
	if o.status != http.StatusCreated {
		t.Fatalf("create order = %d: %s", o.status, o.raw)
	}
	orderID := linkStr(t, o.body, "id")
	d := f.do("POST", "/api/v1/drafts/quotes", map[string]any{"payload": f.quotePayload()})
	if d.status != http.StatusCreated {
		t.Fatalf("create draft = %d: %s", d.status, d.raw)
	}
	draftID := linkStr(t, d.body, "id")

	// The quote by number: the exit test line.
	r := f.do("GET", "/api/v1/links/quotes/"+number, nil)
	if r.status != http.StatusOK {
		t.Fatalf("resolve quote by number = %d: %s", r.status, r.raw)
	}
	assertAnswer(t, r.body, "quote", "quotes", quoteID, number, "quotes/"+number)

	// The quote by UUID answers the number links (the canonical segment is
	// the number).
	r = f.do("GET", "/api/v1/links/quotes/"+quoteID, nil)
	if r.status != http.StatusOK {
		t.Fatalf("resolve quote by UUID = %d: %s", r.status, r.raw)
	}
	assertAnswer(t, r.body, "quote", "quotes", quoteID, number, "quotes/"+number)

	// The order: the SO- number is the segment.
	orderNumber := linkStr(t, o.body, "number")
	r = f.do("GET", "/api/v1/links/orders/"+orderID, nil)
	if r.status != http.StatusOK {
		t.Fatalf("resolve order = %d: %s", r.status, r.raw)
	}
	assertAnswer(t, r.body, "order", "orders", orderID, orderNumber, "orders/"+orderNumber)

	// The customer: no number, the UUID is the segment and number is null.
	r = f.do("GET", "/api/v1/links/customers/"+f.customerID.String(), nil)
	if r.status != http.StatusOK {
		t.Fatalf("resolve customer = %d: %s", r.status, r.raw)
	}
	assertAnswer(t, r.body, "customer", "customers", f.customerID.String(), "",
		"accounts/"+f.customerID.String())

	// The product: the inventory area.
	r = f.do("GET", "/api/v1/links/products/"+f.productID.String(), nil)
	if r.status != http.StatusOK {
		t.Fatalf("resolve product = %d: %s", r.status, r.raw)
	}
	assertAnswer(t, r.body, "product", "products", f.productID.String(), "",
		"inventory/"+f.productID.String())

	// A quote draft: the draft's UUID, the desk's drafts area.
	r = f.do("GET", "/api/v1/links/drafts/quotes/"+draftID, nil)
	if r.status != http.StatusOK {
		t.Fatalf("resolve draft = %d: %s", r.status, r.raw)
	}
	assertAnswer(t, r.body, "draft", "quotes", draftID, "",
		"quotes/drafts/"+draftID)

	// A record that names nothing is a 404, never a confirmation.
	if r := f.do("GET", "/api/v1/links/quotes/Q-999999", nil); r.status != http.StatusNotFound {
		t.Errorf("resolve an unseen number = %d, want 404", r.status)
	}
	if r := f.do("GET", "/api/v1/links/quotes/"+uuid.NewString(), nil); r.status != http.StatusNotFound {
		t.Errorf("resolve an unseen UUID = %d, want 404", r.status)
	}

	// A well formed number of another entity is a 400 naming id.
	if r := f.do("GET", "/api/v1/links/quotes/SO-000123", nil); r.status != http.StatusBadRequest {
		t.Errorf("an order number in the quotes link slot = %d, want 400", r.status)
	}
}

// TestLinksSettings pins the settings: desk links absolute when
// GABLE_PUBLIC_URL is set and relative without it, and links.agent from the
// template and null without it.
func TestLinksSettings(t *testing.T) {
	db := testutil.RequireDB(t)

	f := newFixture(t, db, links.Settings{})
	q := f.do("POST", "/api/v1/quotes", f.quotePayload())
	number := linkStr(t, q.body, "number")
	r := f.do("GET", "/api/v1/links/quotes/"+number, nil)
	if r.status != http.StatusOK {
		t.Fatalf("relative resolve = %d: %s", r.status, r.raw)
	}
	rel := r.body["links"].(map[string]any)
	if rel["desk"] != "/quotes/"+number || rel["front_door"] != "/?open=quotes/"+number {
		t.Errorf("relative links = %v", rel)
	}
	if rel["app"] != "gable://quotes/"+number {
		t.Errorf("app link = %v", rel["app"])
	}
	if rel["agent"] != nil {
		t.Errorf("agent link without a template = %v, want null", rel["agent"])
	}
	if rel["portal"] != nil {
		t.Errorf("portal link = %v, want null (no portal record screens yet)", rel["portal"])
	}

	f2 := newFixture(t, db, links.Settings{
		PublicURL:        "https://gable.example.com",
		AgentURLTemplate: "https://agent.example.com/{entity}/{number}",
	})
	q2 := f2.do("POST", "/api/v1/quotes", f2.quotePayload())
	number2 := linkStr(t, q2.body, "number")
	r = f2.do("GET", "/api/v1/links/quotes/"+number2, nil)
	if r.status != http.StatusOK {
		t.Fatalf("absolute resolve = %d: %s", r.status, r.raw)
	}
	abs := r.body["links"].(map[string]any)
	if abs["desk"] != "https://gable.example.com/quotes/"+number2 {
		t.Errorf("absolute desk link = %v", abs["desk"])
	}
	if abs["front_door"] != "https://gable.example.com/?open=quotes/"+number2 {
		t.Errorf("absolute front door link = %v", abs["front_door"])
	}
	if abs["agent"] != "https://agent.example.com/quote/"+number2 {
		t.Errorf("agent link from the template = %v", abs["agent"])
	}
}

// TestTableCoversRegisteredRoutes pins the generated file's drift rule on
// the Go side: every row of the declarative table spells the route
// RegisterAll registers, so api/links.json (written from the table) and the
// route census cannot drift apart.
func TestTableCoversRegisteredRoutes(t *testing.T) {
	want := map[string]bool{
		"GET /api/v1/links/quotes/{id}":        false,
		"GET /api/v1/links/orders/{id}":        false,
		"GET /api/v1/links/invoices/{id}":      false,
		"GET /api/v1/links/customers/{id}":     false,
		"GET /api/v1/links/products/{id}":      false,
		"GET /api/v1/links/drafts/quotes/{id}": false,
		"GET /api/v1/links/drafts/orders/{id}": false,
	}
	for _, row := range links.Table() {
		var pattern string
		if row.DraftKind {
			pattern = "GET /api/v1/links/drafts/" + row.Module + "/{id}"
		} else {
			pattern = "GET /api/v1/links/" + row.Module + "/{id}"
		}
		if _, ok := want[pattern]; !ok {
			t.Errorf("the table's row %v spells %q, which RegisterAll does not register", row, pattern)
			continue
		}
		want[pattern] = true
	}
	for pattern, seen := range want {
		if !seen {
			t.Errorf("the table names no row for the registered route %q", pattern)
		}
	}
}

// assertAnswer checks one resolver answer: the entity and module, the id
// and number, and the three built-in links the record path derives (the
// desk's origin-relative path, the front door's open form and the app
// scheme).
func assertAnswer(t *testing.T, body map[string]any, entity, module, id, number, recordPath string) {
	t.Helper()
	if body["entity"] != entity || body["module"] != module || body["id"] != id {
		t.Errorf("answer identity = %v/%v/%v, want %s/%s/%s", body["entity"], body["module"], body["id"], entity, module, id)
	}
	if number == "" {
		if body["number"] != nil {
			t.Errorf("answer number = %v, want null", body["number"])
		}
	} else if body["number"] != number {
		t.Errorf("answer number = %v, want %s", body["number"], number)
	}
	ls := body["links"].(map[string]any)
	if ls["desk"] != "/"+recordPath {
		t.Errorf("desk link = %v, want /%s", ls["desk"], recordPath)
	}
	if ls["front_door"] != "/?open="+recordPath {
		t.Errorf("front door link = %v, want /?open=%s", ls["front_door"], recordPath)
	}
	if ls["app"] != "gable://"+recordPath {
		t.Errorf("app link = %v, want gable://%s", ls["app"], recordPath)
	}
}

func linkStr(t *testing.T, m map[string]any, key string) string {
	t.Helper()
	s, ok := m[key].(string)
	if !ok {
		t.Fatalf("%s is %T (%v), want a string", key, m[key], m[key])
	}
	return s
}
