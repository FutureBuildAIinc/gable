// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package serve

// The contractor's customer quotes through serve's own wiring and its real
// middleware chain: the loop end to end on the wire (build, send, the customer
// selects and signs off with a code, the contractor confirms, the dealer's
// order exists at the dealer's prices on the project, all three see the
// status, the approval record is complete), and the two price walls held by
// wire tests on every route that touches the data.

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"regexp"
	"sort"
	"strings"
	"sync"
	"testing"

	"github.com/gablelbm/gable/internal/app/orderwire"
	"github.com/gablelbm/gable/internal/customer"
	"github.com/gablelbm/gable/internal/customerquote"
	"github.com/gablelbm/gable/internal/document"
	"github.com/gablelbm/gable/internal/inventory"
	"github.com/gablelbm/gable/internal/order"
	"github.com/gablelbm/gable/internal/portal"
	"github.com/gablelbm/gable/internal/pricing"
	"github.com/gablelbm/gable/internal/product"
	"github.com/gablelbm/gable/internal/quote"
	"github.com/gablelbm/gable/internal/testutil"
	"github.com/gablelbm/gable/pkg/audit"
	"github.com/gablelbm/gable/pkg/clientip"
	"github.com/gablelbm/gable/pkg/database"
	"github.com/gablelbm/gable/pkg/middleware"
	"github.com/gablelbm/gable/pkg/outbox"
	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"
	"github.com/prometheus/client_golang/prometheus"
)

type cqSyncBuf struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *cqSyncBuf) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}
func (s *cqSyncBuf) String() string { s.mu.Lock(); defer s.mu.Unlock(); return s.b.String() }

// cqMail records what the notifier is asked to send.
type cqMail struct {
	down  bool
	mu    sync.Mutex
	links []string
	codes []string
}

func (m *cqMail) SendLink(_ context.Context, _ customerquote.Contact, _, _, link string) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.links = append(m.links, link)
	return true, nil
}

func (m *cqMail) SendCode(_ context.Context, _, _, _, code string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.codes = append(m.codes, code)
	return nil
}

func (m *cqMail) Channels() customerquote.CodeChannels {
	m.mu.Lock()
	defer m.mu.Unlock()
	return customerquote.CodeChannels{Email: !m.down, SMS: !m.down}
}

// setDown makes the server unable to send a code, as with a log only sender.
func (m *cqMail) setDown(v bool) { m.mu.Lock(); m.down = v; m.mu.Unlock() }

func (m *cqMail) lastCode() string {
	m.mu.Lock()
	defer m.mu.Unlock()
	if len(m.codes) == 0 {
		return ""
	}
	return m.codes[len(m.codes)-1]
}

type cqStack struct {
	t      *testing.T
	db     *database.DB
	srv    *httptest.Server
	logs   *cqSyncBuf
	mail   *cqMail
	svc    *customerquote.Service
	branch uuid.UUID
	custA  uuid.UUID
	custB  uuid.UUID
	projA  uuid.UUID
	projB  uuid.UUID
	catLum uuid.UUID
	stud   uuid.UUID
	ply    uuid.UUID
	nail   uuid.UUID
}

func newCQStack(t *testing.T, multiBranch ...bool) *cqStack {
	t.Helper()
	testutil.LockOutboxTables(t)
	db := testutil.RequireDB(t)
	if len(multiBranch) > 0 && multiBranch[0] {
		setSetting(t, db, "multi_branch_enabled", "true")
		setSetting(t, db, "default_branch_required", "false")
	}
	ctx := context.Background()
	s := &cqStack{t: t, db: db, logs: &cqSyncBuf{}, mail: &cqMail{}}
	must := func(sql string, args ...any) {
		t.Helper()
		if _, err := db.Pool.Exec(ctx, sql, args...); err != nil {
			t.Fatalf("fixture %q: %v", sql, err)
		}
	}
	if err := db.Pool.QueryRow(ctx, `SELECT value::uuid FROM system_settings WHERE key = 'default_branch_id'`).Scan(&s.branch); err != nil {
		t.Fatal(err)
	}
	s.custA, s.custB, s.projA, s.projB = uuid.New(), uuid.New(), uuid.New(), uuid.New()
	for _, c := range []struct {
		id   uuid.UUID
		name string
	}{{s.custA, "Wire Contractor A"}, {s.custB, "Wire Contractor B"}} {
		must(`INSERT INTO customers (id, name, account_number, primary_branch_id) VALUES ($1, $2, $3, $4)`, c.id, c.name, "CQW-"+c.id.String()[:8], s.branch)
	}
	must(`INSERT INTO customer_branches (customer_id, branch_id) VALUES ($1, $3), ($2, $3)`, s.custA, s.custB, s.branch)
	must(`INSERT INTO projects (id, customer_id, name, status) VALUES ($1, $2, 'Maple Street Deck', 'Active'), ($3, $4, 'Other Job', 'Active')`, s.projA, s.custA, s.projB, s.custB)
	s.catLum = uuid.New()
	must(`INSERT INTO product_categories (id, name, slug, path) VALUES ($1, 'cqw lumber', $2::text, replace($2::text, '-', '_')::ltree)`, s.catLum, "cqw_"+s.catLum.String()[:8])
	s.stud, s.ply, s.nail = uuid.New(), uuid.New(), uuid.New()
	for _, p := range []struct {
		id   uuid.UUID
		name string
		base string
	}{{s.stud, "2x4 stud", "5.50"}, {s.ply, "plywood sheet", "41.25"}, {s.nail, "box of nails", "12.00"}} {
		must(`INSERT INTO products (id, sku, description, uom_primary, base_price, category_id) VALUES ($1, $2, $3, 'EA', $4::numeric, $5)`,
			p.id, "CQW-"+p.id.String()[:8], p.name, p.base, s.catLum)
	}
	// The order service refuses an order whose branch has no tax rate.
	var oldRate *string
	if err := db.Pool.QueryRow(ctx, `SELECT default_tax_rate::text FROM locations WHERE id = $1`, s.branch).Scan(&oldRate); err != nil {
		t.Fatal(err)
	}
	must(`UPDATE locations SET default_tax_rate = 0.07 WHERE id = $1`, s.branch)
	t.Cleanup(func() {
		_, _ = db.Pool.Exec(ctx, `UPDATE locations SET default_tax_rate = $2::numeric WHERE id = $1`, s.branch, oldRate)
	})
	t.Cleanup(func() {
		testutil.DeleteCustomerQuotes(t, db, s.custA, s.custB)
		cs := []uuid.UUID{s.custA, s.custB}
		_, _ = db.Pool.Exec(ctx, `DELETE FROM pro_portal_share_tokens WHERE dealer_quote_id IN (SELECT id FROM quotes WHERE customer_id = ANY($1))`, cs)
		_, _ = db.Pool.Exec(ctx, `DELETE FROM events_outbox WHERE entity_type = 'quote' AND entity_id IN (SELECT id FROM quotes WHERE customer_id = ANY($1))`, cs)
		_, _ = db.Pool.Exec(ctx, `DELETE FROM audit_log WHERE entity_type = 'quote' AND entity_id IN (SELECT id FROM quotes WHERE customer_id = ANY($1))`, cs)
		_, _ = db.Pool.Exec(ctx, `DELETE FROM quote_lines WHERE quote_id IN (SELECT id FROM quotes WHERE customer_id = ANY($1))`, cs)
		_, _ = db.Pool.Exec(ctx, `DELETE FROM quotes WHERE customer_id = ANY($1)`, cs)
		_, _ = db.Pool.Exec(ctx, `DELETE FROM events_outbox WHERE entity_id IN (SELECT id FROM orders WHERE customer_id = ANY($1))`, cs)
		_, _ = db.Pool.Exec(ctx, `DELETE FROM order_lines WHERE order_id IN (SELECT id FROM orders WHERE customer_id = ANY($1))`, cs)
		_, _ = db.Pool.Exec(ctx, `DELETE FROM orders WHERE customer_id = ANY($1)`, cs)
		_, _ = db.Pool.Exec(ctx, `DELETE FROM customer_branches WHERE customer_id = ANY($1)`, cs)
		_, _ = db.Pool.Exec(ctx, `DELETE FROM projects WHERE customer_id = ANY($1)`, cs)
		_, _ = db.Pool.Exec(ctx, `DELETE FROM customers WHERE id = ANY($1)`, cs)
		_, _ = db.Pool.Exec(ctx, `DELETE FROM products WHERE id = ANY($1)`, []uuid.UUID{s.stud, s.ply, s.nail})
		_, _ = db.Pool.Exec(ctx, `DELETE FROM product_categories WHERE id = $1`, s.catLum)
	})

	logger := slog.New(slog.NewTextHandler(s.logs, &slog.HandlerOptions{Level: slog.LevelDebug}))
	prev := slog.Default()
	slog.SetDefault(logger)
	t.Cleanup(func() { slog.SetDefault(prev) })

	productSvc := product.NewService(product.NewRepository(db))
	customerSvc := customer.NewService(customer.NewRepository(db))
	pricingSvc := pricing.NewService(pricing.NewRepository(db))
	inventorySvc := inventory.NewService(inventory.NewRepository(db))
	orderSvc := order.NewService(order.NewRepository(db)).WithOutbox(outbox.NewWriter(db, "")).WithTxRunner(db).
		WithPriceEngine(&orderwire.PriceEngineAdapter{Pricing: pricingSvc, Customers: customerSvc})
	portalSvc := portal.NewService(portal.NewRepository(db), "cq-wire-test-secret", logger, pricingSvc, customerSvc, inventorySvc, orderSvc, productSvc)

	mux := http.NewServeMux()
	chain := func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			who := r.Header.Get("X-Test-Contractor")
			claims := &middleware.PortalClaims{Role: "Buyer", Name: "Buyer " + who, Email: who + "@contractor.example", CustomerUserID: uuid.New()}
			switch who {
			case "a":
				claims.CustomerID = s.custA
			case "b":
				claims.CustomerID = s.custB
			case "viewer":
				claims.CustomerID, claims.Role = s.custA, "View-Only"
			default:
				http.Error(w, "unauthorized", http.StatusUnauthorized)
				return
			}
			next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), middleware.PortalClaimsKey, claims)))
		})
	}
	wall := newBranchWall(db)
	quoteSvc := quote.NewService(quote.NewRepository(db)).WithOutbox(outbox.NewWriter(db, "")).WithTxRunner(db)
	s.svc = wireCustomerQuotes(mux, customerQuoteDeps{
		DB: db, Logger: logger, Portal: portalSvc, Pricing: pricingSvc, Customers: customerSvc, Products: productSvc,
		Email: nil, Docs: document.NewService(product.NewRepository(db)), PublicURL: "https://quotes.example", Trusted: clientip.Trusted{},
		Quotes: quoteSvc, Audit: audit.NewLogger(db), Wall: wall,
	}, chain)
	s.svc.WithNotifier(s.mail).WithLimits(10000, 10000, 10000)
	wall.orders(mux, orderSvc)

	h := buildChain(ChainDeps{Mux: mux, DB: db, Logger: logger, AuditLog: audit.NewLogger(db)})
	s.srv = httptest.NewServer(deskClaims(h))
	t.Cleanup(s.srv.Close)
	return s
}

type cqResp struct {
	status int
	body   []byte
	header http.Header
}

func (s *cqStack) do(method, path string, headers map[string]string, body any) cqResp {
	s.t.Helper()
	var rd io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			s.t.Fatal(err)
		}
		rd = bytes.NewReader(b)
	}
	req, err := http.NewRequest(method, s.srv.URL+path, rd)
	if err != nil {
		s.t.Fatal(err)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		s.t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return cqResp{status: resp.StatusCode, body: b, header: resp.Header}
}

var (
	asA      = map[string]string{"X-Test-Contractor": "a"}
	asB      = map[string]string{"X-Test-Contractor": "b"}
	asViewer = map[string]string{"X-Test-Contractor": "viewer"}
	asDesk   = map[string]string{"X-Test-Role": "sales", "X-Test-Sub": "desk-user-1", "X-Test-Email": deskEmail}
)

func (s *cqStack) quoteBody(hide bool, requireCode bool) map[string]any {
	return map[string]any{
		"title":            "Maple Street deck",
		"project_id":       s.projA.String(),
		"job_site_address": "12 Maple Street",
		"end_customer":     map[string]any{"name": "Pat Homeowner", "email": "pat@homeowner.example", "phone": "555 010 2020"},
		"site_contact":     map[string]any{"name": "Sam Foreman", "phone": "555 010 3030"},
		"markup":           map[string]any{"kind": "markup", "bps": 2500},
		"hide_line_prices": hide,
		"tax":              map[string]any{"mode": "site_rate", "rate_bps": 800},
		"payment_terms":    "Net 15",
		"deposit_bps":      2500,
		"require_code":     requireCode,
		"customer_note":    "Thank you for the work.",
		"lines": []any{
			map[string]any{"product_id": s.stud.String(), "quantity": "100"},
			map[string]any{"product_id": s.ply.String(), "quantity": "10"},
			map[string]any{"product_id": s.nail.String(), "quantity": "4"},
		},
		"labour": []any{
			map[string]any{"description": "Framing crew", "rate_type": "hourly", "rate_cents": 6500, "hours": "16"},
			map[string]any{"description": "Own travel", "rate_type": "flat", "rate_cents": 12000, "contractor_only": true},
		},
		"overhead": []any{
			map[string]any{"label": "Permit", "amount_type": "flat", "value": 15000},
			map[string]any{"label": "Own insurance share", "amount_type": "percent", "value": 300, "contractor_only": true},
		},
	}
}

func jsonOf(t *testing.T, b []byte) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatalf("not JSON: %v\n%s", err, b)
	}
	return m
}

// create sends the quote and returns its id, the raw link token and the body.
func (s *cqStack) createAndSend(body map[string]any) (id, token string) {
	s.t.Helper()
	r := s.do("POST", "/api/portal/v1/customer-quotes", asA, body)
	if r.status != 201 {
		s.t.Fatalf("create: %d %s", r.status, r.body)
	}
	id = jsonOf(s.t, r.body)["id"].(string)
	r = s.do("POST", "/api/portal/v1/customer-quotes/"+id+"/send", asA, nil)
	if r.status != 200 {
		s.t.Fatalf("send: %d %s", r.status, r.body)
	}
	path := jsonOf(s.t, r.body)["link"].(map[string]any)["path"].(string)
	return id, strings.TrimPrefix(path, "/q/")
}

func (s *cqStack) pub(token string) string { return "/api/public/v1/customer-quotes/" + token }

// keysOf collects every object key at any depth.
func keysOf(v any, into map[string]bool) {
	switch t := v.(type) {
	case map[string]any:
		for k, c := range t {
			into[k] = true
			keysOf(c, into)
		}
	case []any:
		for _, c := range t {
			keysOf(c, into)
		}
	}
}

func assertKeysWithin(t *testing.T, what string, body []byte, allowed []string) {
	t.Helper()
	var v any
	if err := json.Unmarshal(body, &v); err != nil {
		t.Fatalf("%s: not JSON: %v", what, err)
	}
	got := map[string]bool{}
	keysOf(v, got)
	ok := map[string]bool{}
	for _, a := range allowed {
		ok[a] = true
	}
	var extra []string
	for k := range got {
		if !ok[k] {
			extra = append(extra, k)
		}
	}
	sort.Strings(extra)
	if len(extra) > 0 {
		t.Errorf("%s carries fields outside the allowlist: %v\n%s", what, extra, body)
	}
}

// assertNoNumbers fails when any of the numbers appears as a whole number in the bytes.
func assertNoNumbers(t *testing.T, what string, body []byte, nums ...int64) {
	t.Helper()
	for _, n := range nums {
		re := regexp.MustCompile(fmt.Sprintf(`(^|[^0-9.])%d([^0-9]|$)`, n))
		if re.Match(body) {
			t.Errorf("%s carries the number %d: %s", what, n, truncate(body))
		}
	}
}

func assertNoStrings(t *testing.T, what string, body []byte, ss ...string) {
	t.Helper()
	low := bytes.ToLower(body)
	for _, x := range ss {
		if bytes.Contains(low, []byte(strings.ToLower(x))) {
			t.Errorf("%s carries %q: %s", what, x, truncate(body))
		}
	}
}

func truncate(b []byte) string {
	if len(b) > 600 {
		return string(b[:600]) + "..."
	}
	return string(b)
}

var publicKeys = []string{
	"reference", "title", "status", "contractor", "name", "has_logo", "customer_name", "job_site_address", "valid_until", "note",
	"payment_terms", "hide_line_prices", "groups", "id", "label", "lines", "group_id", "description", "quantity", "uom", "price_cents",
	"selection", "extras", "kind", "totals", "subtotal_cents", "tax_cents", "total_cents", "deposit_cents", "tax_mode", "tax_rate_bps",
	"tax_note", "requires_code", "can_decide", "notice", "approval", "decision", "signed_by", "at", "fulfilment",
}

var dealerKeys = []string{
	"id", "reference", "contractor", "customer_id", "name", "project", "job_site_address", "customer", "email", "phone", "site_contact",
	"status", "selections", "approved", "declined", "pending", "materials", "sku", "description", "quantity", "uom", "dealer_cost_cents",
	"selection", "material_cents", "order", "number", "total_cents", "dealer_accepted_at", "updated_at", "approvals", "step",
	"actor_kind", "actor_name", "occurred_at", "snapshot_sha256", "items", "next_cursor", "limit",
}

// The contractor's private figures of the fixture quote (see quoteBody): the
// dealer's cost, the markup, the contractor only lines and the customer's sell
// prices. Price wall 2 keeps the first group from the customer; price wall 1
// keeps the second from the dealer.
var (
	costNumbers     = []int64{55000, 41250, 4800, 550000, 412500, 120000, 101050, 2500, 12000, 18909, 125354}
	costStrings     = []string{"unit_cost", "cost_cents", "markup", "margin", "\"bps\":", "contractor_only", "Own travel", "Own insurance", "dealer", "applied", "override"}
	sellNumbers     = []int64{68750, 51563, 6000, 126313, 245313, 255418, 10105, 63855, 104000, 15000, 5100, 6500}
	sellOnlyStrings = []string{"Framing crew", "Permit", "Own travel", "Own insurance", "sell_cents", "markup", "margin", "labour", "overhead", "\"bps\":", "contractor_only"}
)

// The order is the dealer's own document and carries the dealer's own margin
// on the sale, so the word "margin" is not a leak there; the contractor's
// words and fields are.
var orderForbidden = []string{"Framing crew", "Permit", "Own travel", "Own insurance", "sell_cents", "contractor_only", "markup", "labour", "overhead", "\"bps\":"}

func TestWire_TheLoopEndToEnd(t *testing.T) {
	s := newCQStack(t)
	ctx := context.Background()

	// The contractor turns on the verification code for their account and builds the quote.
	r := s.do("PUT", "/api/portal/v1/customer-quotes/settings", asA, map[string]any{
		"display_name": "Wire Builders", "payment_terms": "Net 15", "deposit_bps": 2500,
		"tax": map[string]any{"mode": "site_rate", "rate_bps": 800}, "markup": map[string]any{"kind": "markup", "bps": 2500}, "require_code": true})
	if r.status != 200 {
		t.Fatalf("settings: %d %s", r.status, r.body)
	}
	body := s.quoteBody(false, true)
	r = s.do("POST", "/api/portal/v1/customer-quotes", asA, body)
	if r.status != 201 || r.header.Get("ETag") == "" {
		t.Fatalf("create: %d %s", r.status, r.body)
	}
	q := jsonOf(t, r.body)
	id := q["id"].(string)
	totals := q["totals"].(map[string]any)
	if totals["total_cents"].(float64) != 255418 || totals["margin_cents"].(float64) != 125354 {
		t.Fatalf("contractor totals %+v", totals)
	}
	if q["status"] != "draft" {
		t.Fatalf("status %v", q["status"])
	}

	// Preview is exactly the customer's page, before anything is sent.
	r = s.do("GET", "/api/portal/v1/customer-quotes/"+id+"/preview", asA, nil)
	if r.status != 200 {
		t.Fatalf("preview: %d %s", r.status, r.body)
	}
	assertKeysWithin(t, "preview", r.body, publicKeys)

	// Send: the link is returned once, the mailer is handed it.
	r = s.do("POST", "/api/portal/v1/customer-quotes/"+id+"/send", asA, nil)
	if r.status != 200 || r.header.Get("Cache-Control") != "no-store" {
		t.Fatalf("send: %d %s", r.status, r.body)
	}
	link := jsonOf(t, r.body)["link"].(map[string]any)
	token := strings.TrimPrefix(link["path"].(string), "/q/")
	if len(token) != 43 || !strings.HasPrefix(link["url"].(string), "https://quotes.example/q/") {
		t.Fatalf("link %v", link)
	}
	r = s.do("GET", "/api/portal/v1/customer-quotes/"+id, asA, nil)
	if bytes.Contains(r.body, []byte(token)) {
		t.Error("the token is readable again after it was issued")
	}

	// The customer opens the page, no account, no cookie.
	r = s.do("GET", s.pub(token), nil, nil)
	if r.status != 200 || r.header.Get("Cache-Control") != "no-store" || r.header.Get("Referrer-Policy") != "no-referrer" || r.header.Get("Set-Cookie") != "" {
		t.Fatalf("public view: %d headers %v\n%s", r.status, r.header, r.body)
	}
	view := jsonOf(t, r.body)
	lines := view["lines"].([]any)
	if len(lines) != 3 || view["requires_code"] != true || view["contractor"].(map[string]any)["name"] != "Wire Builders" {
		t.Fatalf("view %s", r.body)
	}
	var choices []any
	for _, l := range lines {
		choices = append(choices, map[string]any{"line_id": l.(map[string]any)["id"], "state": "approved"})
	}
	r = s.do("PUT", s.pub(token)+"/selections", nil, map[string]any{"choices": choices})
	if r.status != 200 {
		t.Fatalf("selections: %d %s", r.status, r.body)
	}

	// Acceptance is refused without the code; the code is requested and entered.
	sign := map[string]any{"full_name": "Pat Homeowner", "email": "pat@homeowner.example", "signature": map[string]any{"kind": "typed", "text": "Pat Homeowner"}}
	r = s.do("POST", s.pub(token)+"/accept", nil, sign)
	if r.status != 400 || !bytes.Contains(r.body, []byte("code_required")) {
		t.Fatalf("accept without a code: %d %s", r.status, r.body)
	}
	r = s.do("POST", s.pub(token)+"/code", nil, map[string]any{"channel": "email"})
	if r.status != 202 {
		t.Fatalf("code: %d %s", r.status, r.body)
	}
	sign["code"] = "000000"
	if s.mail.lastCode() == "000000" {
		sign["code"] = "111111"
	}
	r = s.do("POST", s.pub(token)+"/accept", nil, sign)
	if r.status != 400 || !bytes.Contains(r.body, []byte("code_invalid")) {
		t.Fatalf("accept with a wrong code: %d %s", r.status, r.body)
	}
	sign["code"] = s.mail.lastCode()
	r = s.do("POST", s.pub(token)+"/accept", nil, sign)
	if r.status != 200 {
		t.Fatalf("accept: %d %s", r.status, r.body)
	}
	if jsonOf(t, r.body)["status"] != "approved" {
		t.Fatalf("accept answered %s", r.body)
	}

	// The dealer already sees the project; the order does not exist yet.
	r = s.do("GET", "/api/v1/pro-projects/"+id, asDesk, nil)
	if r.status != 200 {
		t.Fatalf("dealer detail: %d %s", r.status, r.body)
	}
	d := jsonOf(t, r.body)
	if d["order"] != nil || d["status"] != "approved" {
		t.Fatalf("dealer before confirmation: %s", r.body)
	}

	// The contractor confirms: the dealer's order appears at the dealer's prices on the project.
	r = s.do("POST", "/api/portal/v1/customer-quotes/"+id+"/confirm", asA, nil)
	if r.status != 200 {
		t.Fatalf("confirm: %d %s", r.status, r.body)
	}
	conf := jsonOf(t, r.body)
	orderID := conf["order"].(map[string]any)["id"].(string)
	var onProject int
	var unitStud, unitPly string
	_ = s.db.Pool.QueryRow(ctx, `SELECT count(*) FROM orders WHERE id = $1 AND project_id = $2 AND customer_id = $3`, orderID, s.projA, s.custA).Scan(&onProject)
	_ = s.db.Pool.QueryRow(ctx, `SELECT unit_price::text FROM order_lines WHERE order_id = $1 AND product_id = $2`, orderID, s.stud).Scan(&unitStud)
	_ = s.db.Pool.QueryRow(ctx, `SELECT unit_price::text FROM order_lines WHERE order_id = $1 AND product_id = $2`, orderID, s.ply).Scan(&unitPly)
	if onProject != 1 || unitStud != "5.5000" || unitPly != "41.2500" {
		t.Fatalf("the order: on project %d, stud %s, ply %s (want the dealer's prices, never the quote's)", onProject, unitStud, unitPly)
	}
	r = s.do("GET", "/api/v1/orders/"+orderID, asDesk, nil)
	if r.status != 200 {
		t.Fatalf("the dealer's order: %d %s", r.status, r.body)
	}
	assertNoNumbers(t, "the converted order", r.body, sellNumbers...)
	assertNoStrings(t, "the converted order", r.body, orderForbidden...)

	// The dealer accepts.
	r = s.do("POST", "/api/v1/pro-projects/"+id+"/accept", asDesk, nil)
	if r.status != 200 || jsonOf(t, r.body)["dealer_accepted_at"] == nil {
		t.Fatalf("dealer accept: %d %s", r.status, r.body)
	}

	// Each of the three sees the status: the customer (fulfilment only), the contractor and the dealer.
	if _, err := s.db.Pool.Exec(ctx, `UPDATE orders SET status = 'FULFILLED' WHERE id = $1`, orderID); err != nil {
		t.Fatal(err)
	}
	cust := jsonOf(t, s.do("GET", s.pub(token), nil, nil).body)
	if cust["fulfilment"] != "fulfilled" || cust["status"] != "confirmed" {
		t.Errorf("customer sees %v / %v", cust["status"], cust["fulfilment"])
	}
	con := jsonOf(t, s.do("GET", "/api/portal/v1/customer-quotes/"+id, asA, nil).body)
	if con["order"].(map[string]any)["fulfilment"] != "fulfilled" || con["status"] != "confirmed" {
		t.Errorf("contractor sees %v", con["order"])
	}
	dlr := jsonOf(t, s.do("GET", "/api/v1/pro-projects/"+id, asDesk, nil).body)
	if dlr["order"].(map[string]any)["status"] != "fulfilled" {
		t.Errorf("dealer sees %v", dlr["order"])
	}

	// The approval record is complete, in order, for the contractor and the dealer.
	cl := jsonOf(t, s.do("GET", "/api/portal/v1/customer-quotes/"+id+"/approvals", asA, nil).body)["items"].([]any)
	var steps []string
	for _, e := range cl {
		steps = append(steps, e.(map[string]any)["step"].(string))
	}
	if strings.Join(steps, ",") != "customer_approved,contractor_confirmed,dealer_accepted" {
		t.Errorf("contractor's record %v", steps)
	}
	first := cl[0].(map[string]any)
	if first["code_verified"] != true || first["snapshot_sha256"] == "" || first["source_address"] == "" || first["token_hash"] != customerquote.HashToken(token) {
		t.Errorf("the customer's entry %v", first)
	}
	da := dlr["approvals"].([]any)
	if len(da) != 3 {
		t.Errorf("dealer's record has %d steps", len(da))
	}
}

func TestWire_PriceWall2_NothingOfTheContractorsCostOrMarkupReachesTheCustomer(t *testing.T) {
	s := newCQStack(t)
	for _, hide := range []bool{false, true} {
		id, token := s.createAndSend(s.quoteBody(hide, false))
		base := s.pub(token)
		check := func(what string, r cqResp) {
			t.Helper()
			if r.status != 200 {
				t.Fatalf("%s: %d %s", what, r.status, r.body)
			}
			assertKeysWithin(t, what, r.body, publicKeys)
			assertNoNumbers(t, what, r.body, costNumbers...)
			assertNoStrings(t, what, r.body, costStrings...)
			if hide {
				var v map[string]any
				_ = json.Unmarshal(r.body, &v)
				for _, section := range []string{"lines", "extras"} {
					for _, l := range v[section].([]any) {
						if _, has := l.(map[string]any)["price_cents"]; has {
							t.Errorf("%s: a line price shows although the contractor shows a lump sum: %v", what, l)
						}
					}
				}
				// The lump sum is still there.
				if v["totals"].(map[string]any)["total_cents"].(float64) != 255418 {
					t.Errorf("%s: the lump sum is missing: %v", what, v["totals"])
				}
			}
		}
		view := s.do("GET", base, nil, nil)
		check("view", view)
		var lines []any
		for _, l := range jsonOf(t, view.body)["lines"].([]any) {
			lines = append(lines, map[string]any{"line_id": l.(map[string]any)["id"], "state": "approved"})
		}
		check("selections", s.do("PUT", base+"/selections", nil, map[string]any{"choices": lines}))
		check("accept", s.do("POST", base+"/accept", nil, map[string]any{"full_name": "Pat Homeowner", "phone": "555 010 2020",
			"signature": map[string]any{"kind": "drawn", "png_base64": base64.StdEncoding.EncodeToString(tinyPNG)}}))
		check("view after accept", s.do("GET", base, nil, nil))

		// The customer PDF is built from the same allowlist: its text carries none of it either.
		pdf := s.do("GET", base+"/pdf", nil, nil)
		if pdf.status != 200 || !bytes.HasPrefix(pdf.body, []byte("%PDF")) {
			t.Fatalf("pdf: %d", pdf.status)
		}
		for _, bad := range []string{"$550.00", "$412.50", "$48.00", "$1,010.50", "Own travel", "Own insurance", "$120.00", "$1,253.54", "markup", "margin"} {
			if bytes.Contains(pdf.body, []byte(bad)) {
				t.Errorf("hide=%v: the customer PDF carries %q", hide, bad)
			}
		}
		if !bytes.Contains(pdf.body, []byte("Maple Street deck")) || !bytes.Contains(pdf.body, []byte("$2,554.18")) {
			t.Errorf("hide=%v: the customer PDF lacks the quote or its total", hide)
		}
		if hide && bytes.Contains(pdf.body, []byte("$687.50")) {
			t.Error("the PDF shows a line price although the contractor shows a lump sum")
		}
		if !hide && !bytes.Contains(pdf.body, []byte("$687.50")) {
			t.Error("the PDF lacks the studs' price")
		}

		// A declined quote's page is clean too.
		id2, token2 := s.createAndSend(s.quoteBody(hide, false))
		_ = id2
		dec := s.do("POST", s.pub(token2)+"/decline", nil, map[string]any{"full_name": "Pat Homeowner", "reason": "no"})
		check("decline", dec)
		_ = id
	}
}

var tinyPNG = []byte{0x89, 'P', 'N', 'G', 0x0d, 0x0a, 0x1a, 0x0a, 0, 0, 0, 0x0d, 'I', 'H', 'D', 'R', 0, 0, 0, 1, 0, 0, 0, 1, 8, 6, 0, 0, 0, 0x1f, 0x15, 0xc4, 0x89}

func TestWire_PriceWall1_NothingOfTheContractorsMarkupReachesTheDealer(t *testing.T) {
	s := newCQStack(t)
	id, token := s.createAndSend(s.quoteBody(false, false))
	// Draft: the contractor's own work is not shared.
	rd := s.do("POST", "/api/portal/v1/customer-quotes", asA, s.quoteBody(false, false))
	draftID := jsonOf(t, rd.body)["id"].(string)
	if r := s.do("GET", "/api/v1/pro-projects/"+draftID, asDesk, nil); r.status != 404 {
		t.Errorf("a draft is visible to the dealer: %d", r.status)
	}

	var lines []any
	for _, l := range jsonOf(t, s.do("GET", s.pub(token), nil, nil).body)["lines"].([]any) {
		lines = append(lines, map[string]any{"line_id": l.(map[string]any)["id"], "state": "approved"})
	}
	s.do("PUT", s.pub(token)+"/selections", nil, map[string]any{"choices": lines})
	if r := s.do("POST", s.pub(token)+"/accept", nil, map[string]any{"full_name": "Pat Homeowner", "phone": "555 010 2020",
		"signature": map[string]any{"kind": "typed", "text": "Pat Homeowner"}}); r.status != 200 {
		t.Fatalf("accept: %d %s", r.status, r.body)
	}
	conf := s.do("POST", "/api/portal/v1/customer-quotes/"+id+"/confirm", asA, nil)
	if conf.status != 200 {
		t.Fatalf("confirm: %d %s", conf.status, conf.body)
	}
	orderID := jsonOf(t, conf.body)["order"].(map[string]any)["id"].(string)

	check := func(what string, r cqResp, keyed bool) {
		t.Helper()
		if r.status != 200 {
			t.Fatalf("%s: %d %s", what, r.status, r.body)
		}
		if keyed {
			assertKeysWithin(t, what, r.body, dealerKeys)
		}
		assertNoNumbers(t, what, r.body, sellNumbers...)
		if keyed {
			assertNoStrings(t, what, r.body, sellOnlyStrings...)
		} else {
			assertNoStrings(t, what, r.body, orderForbidden...)
		}
	}
	list := s.do("GET", "/api/v1/pro-projects", asDesk, nil)
	check("pipeline list", list, true)
	if !bytes.Contains(list.body, []byte(id)) || bytes.Contains(list.body, []byte(draftID)) {
		t.Errorf("the pipeline does not hold the sent quote and only it: %s", truncate(list.body))
	}
	detail := s.do("GET", "/api/v1/pro-projects/"+id, asDesk, nil)
	check("project detail", detail, true)
	// What the dealer is given, by name (ruling 5a).
	d := jsonOf(t, detail.body)
	if d["project"].(map[string]any)["name"] != "Maple Street Deck" || d["job_site_address"] != "12 Maple Street" ||
		d["customer"].(map[string]any)["name"] != "Pat Homeowner" || d["site_contact"].(map[string]any)["name"] != "Sam Foreman" ||
		d["selections"].(map[string]any)["approved"].(float64) != 3 {
		t.Errorf("shared project fields: %s", detail.body)
	}
	if d["material_cents"].(float64) != 101050 {
		t.Errorf("material pipeline at dealer prices: %v", d["material_cents"])
	}
	check("dealer accept", s.do("POST", "/api/v1/pro-projects/"+id+"/accept", asDesk, nil), true)
	check("the converted order", s.do("GET", "/api/v1/orders/"+orderID, asDesk, nil), false)
	check("the order list", s.do("GET", "/api/v1/orders", asDesk, nil), false)
	// The dealer's cost is the order's price: studs at the dealer's $5.50.
	r := s.do("GET", "/api/v1/orders/"+orderID, asDesk, nil)
	if !bytes.Contains(r.body, []byte(`"unit_price_ten_thousandths":55000`)) {
		t.Errorf("the order is not at the dealer's price: %s", truncate(r.body))
	}
}

func TestWire_Tenancy_AnotherContractorSeesNothing(t *testing.T) {
	s := newCQStack(t)
	id, token := s.createAndSend(s.quoteBody(false, false))
	paths := []struct{ method, path string }{
		{"GET", "/api/portal/v1/customer-quotes/" + id}, {"PUT", "/api/portal/v1/customer-quotes/" + id},
		{"GET", "/api/portal/v1/customer-quotes/" + id + "/preview"}, {"GET", "/api/portal/v1/customer-quotes/" + id + "/pdf"},
		{"GET", "/api/portal/v1/customer-quotes/" + id + "/approvals"}, {"POST", "/api/portal/v1/customer-quotes/" + id + "/send"},
		{"POST", "/api/portal/v1/customer-quotes/" + id + "/link/rotate"}, {"POST", "/api/portal/v1/customer-quotes/" + id + "/link/revoke"},
		{"POST", "/api/portal/v1/customer-quotes/" + id + "/withdraw"}, {"POST", "/api/portal/v1/customer-quotes/" + id + "/revise"},
		{"POST", "/api/portal/v1/customer-quotes/" + id + "/confirm"},
	}
	for _, p := range paths {
		var body any
		if p.method == "PUT" {
			body = s.quoteBody(false, false)
		}
		r := s.do(p.method, p.path, asB, body)
		if r.status != 404 {
			t.Errorf("%s %s by another contractor: %d %s", p.method, p.path, r.status, truncate(r.body))
		}
	}
	list := s.do("GET", "/api/portal/v1/customer-quotes", asB, nil)
	if list.status != 200 || bytes.Contains(list.body, []byte(id)) {
		t.Errorf("another contractor's list: %d %s", list.status, truncate(list.body))
	}
	for _, kind := range []string{"quotes", "customers", "projects"} {
		r := s.do("GET", "/api/portal/v1/customer-quotes/export?format=csv&kind="+kind, asB, nil)
		if r.status != 200 || bytes.Contains(r.body, []byte("Pat Homeowner")) || bytes.Contains(r.body, []byte("Maple Street")) {
			t.Errorf("another contractor's %s export: %d %s", kind, r.status, truncate(r.body))
		}
	}
	// B cannot quote on A's project, and A's link is no door to B's data.
	body := s.quoteBody(false, false)
	r := s.do("POST", "/api/portal/v1/customer-quotes", asB, body)
	if r.status != 400 {
		t.Errorf("filing a quote on another contractor's project: %d %s", r.status, truncate(r.body))
	}
	if r := s.do("GET", "/api/portal/v1/customer-quotes/"+id, asA, nil); r.status != 200 {
		t.Errorf("the owner lost the quote: %d", r.status)
	}
	// A role without the capabilities is refused; no session is refused.
	if r := s.do("GET", "/api/portal/v1/customer-quotes/"+id, asViewer, nil); r.status != 403 {
		t.Errorf("a View-Only role: %d", r.status)
	}
	if r := s.do("GET", "/api/portal/v1/customer-quotes/"+id, nil, nil); r.status != 401 {
		t.Errorf("no session: %d", r.status)
	}
	_ = token
}

func TestWire_PublicRoutes_UnknownExpiredAndMalformedLinksAreRefusedWithoutAnyDetail(t *testing.T) {
	s := newCQStack(t)
	_, token := s.createAndSend(s.quoteBody(false, false))
	unknown := strings.Repeat("Q", 43)
	for _, tc := range []struct {
		name, path string
	}{{"unknown", unknown}, {"malformed", "short"}, {"too long", token + token}} {
		r := s.do("GET", s.pub(tc.path), nil, nil)
		if r.status != 404 {
			t.Errorf("%s token: %d %s", tc.name, r.status, r.body)
		}
		if !bytes.Contains(r.body, []byte(`"code":"not_found"`)) || r.header.Get("Cache-Control") != "no-store" {
			t.Errorf("%s token: %s %v", tc.name, r.body, r.header)
		}
	}
	// A link that was valid and is no more answers 410 with the same bare body.
	var id string
	_ = s.db.Pool.QueryRow(context.Background(), `SELECT quote_id::text FROM pro_portal_share_tokens WHERE token_hash = $1`, customerquote.HashToken(token)).Scan(&id)
	if _, err := s.db.Pool.Exec(context.Background(), `UPDATE pro_portal_share_tokens SET expires_at = now() - interval '1 minute' WHERE token_hash = $1`, customerquote.HashToken(token)); err != nil {
		t.Fatal(err)
	}
	r := s.do("GET", s.pub(token), nil, nil)
	if r.status != 410 || !bytes.Contains(r.body, []byte("link_unavailable")) {
		t.Errorf("expired link: %d %s", r.status, r.body)
	}
	for _, p := range []string{"/pdf", "/logo"} {
		if r := s.do("GET", s.pub(token)+p, nil, nil); r.status != 410 {
			t.Errorf("expired link %s: %d", p, r.status)
		}
	}
	_ = id
}

func TestWire_TheTokenNeverReachesALogLineOrAMetricsLabel(t *testing.T) {
	s := newCQStack(t)
	id, token := s.createAndSend(s.quoteBody(false, false))
	// Success, failure, a 500 path and a panic-free miss, through the real chain.
	s.do("GET", s.pub(token), nil, nil)
	s.do("GET", s.pub(token)+"/pdf", nil, nil)
	s.do("POST", s.pub(token)+"/accept", nil, map[string]any{"full_name": "x"})
	s.do("POST", s.pub(token)+"/accept", nil, "not an object")
	s.do("POST", s.pub(token)+"/code", nil, map[string]any{"channel": "email"})
	s.do("PUT", s.pub(token)+"/selections", nil, map[string]any{"choices": []any{}})
	s.do("GET", s.pub(token+"x"), nil, nil)
	s.do("POST", "/api/portal/v1/customer-quotes/"+id+"/link/rotate", asA, nil)
	s.do("GET", s.pub(token), nil, nil)
	logs := s.logs.String()
	if strings.Contains(logs, token) {
		t.Fatalf("the token reached the server log:\n%s", logs)
	}
	if !strings.Contains(logs, "/api/public/v1/customer-quotes/:token") {
		t.Errorf("the access log lost the route:\n%s", logs)
	}
	mfs, err := prometheus.DefaultGatherer.Gather()
	if err != nil {
		t.Fatal(err)
	}
	for _, mf := range mfs {
		for _, m := range mf.GetMetric() {
			for _, l := range m.GetLabel() {
				if strings.Contains(l.GetValue(), token) {
					t.Fatalf("the token is a metrics label: %s{%s=%s}", mf.GetName(), l.GetName(), l.GetValue())
				}
			}
		}
	}
}

func TestWire_Export_TheContractorGetsTheirOwnRecordsAsCSVAndJSON(t *testing.T) {
	s := newCQStack(t)
	id, _ := s.createAndSend(s.quoteBody(false, false))
	r := s.do("GET", "/api/portal/v1/customer-quotes/export", asA, nil)
	if r.status != 200 || !strings.Contains(r.header.Get("Content-Disposition"), "attachment") {
		t.Fatalf("json export: %d %v", r.status, r.header)
	}
	ex := jsonOf(t, r.body)
	if len(ex["customers"].([]any)) != 1 || len(ex["projects"].([]any)) != 1 || len(ex["quotes"].([]any)) != 1 {
		t.Fatalf("export %s", truncate(r.body))
	}
	q := ex["quotes"].([]any)[0].(map[string]any)
	if q["id"] != id || q["totals"].(map[string]any)["margin_cents"].(float64) != 125354 {
		t.Errorf("the contractor's export lacks their own margin: %v", q["totals"])
	}
	for kind, want := range map[string]string{"quotes": "CQ-", "customers": "Pat Homeowner", "projects": "Maple Street Deck"} {
		r := s.do("GET", "/api/portal/v1/customer-quotes/export?format=csv&kind="+kind, asA, nil)
		if r.status != 200 || !strings.HasPrefix(r.header.Get("Content-Type"), "text/csv") || !bytes.Contains(r.body, []byte(want)) {
			t.Errorf("csv %s: %d %s", kind, r.status, truncate(r.body))
		}
	}
	if r := s.do("GET", "/api/portal/v1/customer-quotes/export?format=xml", asA, nil); r.status != 400 {
		t.Errorf("an unknown format: %d", r.status)
	}
}

func logoPNG(t *testing.T) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, 24, 24))
	for x := 0; x < 24; x++ {
		for y := 0; y < 24; y++ {
			img.Set(x, y, color.RGBA{R: uint8(x * 10), G: uint8(y * 10), B: 120, A: 255})
		}
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func TestWire_Logo_TheContractorsMarkReachesTheCustomersPageAndPDF(t *testing.T) {
	s := newCQStack(t)
	put := func(ctype string, body []byte) int {
		req, _ := http.NewRequest("PUT", s.srv.URL+"/api/portal/v1/customer-quotes/settings/logo", bytes.NewReader(body))
		req.Header.Set("Content-Type", ctype)
		req.Header.Set("X-Test-Contractor", "a")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		return resp.StatusCode
	}
	if code := put("image/gif", []byte("GIF89a")); code != 415 {
		t.Errorf("a gif logo: %d", code)
	}
	if code := put("image/png", []byte("not a png")); code != 400 {
		t.Errorf("a broken png: %d", code)
	}
	if code := put("image/png", logoPNG(t)); code != 204 {
		t.Fatalf("logo upload: %d", code)
	}
	_, token := s.createAndSend(s.quoteBody(false, false))
	view := jsonOf(t, s.do("GET", s.pub(token), nil, nil).body)
	if view["contractor"].(map[string]any)["has_logo"] != true {
		t.Error("the page does not say the contractor has a logo")
	}
	lg := s.do("GET", s.pub(token)+"/logo", nil, nil)
	if lg.status != 200 || lg.header.Get("Content-Type") != "image/png" || lg.header.Get("X-Content-Type-Options") != "nosniff" {
		t.Errorf("logo: %d %v", lg.status, lg.header)
	}
	pdf := s.do("GET", s.pub(token)+"/pdf", nil, nil)
	if pdf.status != 200 || !bytes.Contains(pdf.body, []byte("/Image")) {
		t.Errorf("the customer PDF does not carry the contractor's logo: %d", pdf.status)
	}
	// The mark can be removed again.
	if r := s.do("DELETE", "/api/portal/v1/customer-quotes/settings/logo", asA, nil); r.status != 204 {
		t.Errorf("delete logo: %d", r.status)
	}
	if r := s.do("GET", s.pub(token)+"/logo", nil, nil); r.status != 404 {
		t.Errorf("logo after delete: %d", r.status)
	}
}

// dealerQuote seeds a sent dealer quote to contractor A with a margin the
// contractor must never see.
func (s *cqStack) dealerQuote(state string) uuid.UUID {
	s.t.Helper()
	ctx := context.Background()
	id := uuid.New()
	if _, err := s.db.Pool.Exec(ctx, `INSERT INTO quotes (id, customer_id, state, total_amount, freight_amount, margin_total, expires_at, branch_id, delivery_type, source)
		VALUES ($1, $2, $3::quote_state, 775.00, 25.00, 3333.33, now() + interval '10 days', $4, 'PICKUP', 'manual')`, id, s.custA, state, s.branch); err != nil {
		s.t.Fatal(err)
	}
	if _, err := s.db.Pool.Exec(ctx, `INSERT INTO quote_lines (id, quote_id, product_id, sku, description, quantity, uom, unit_price, line_total, position)
		VALUES ($1, $2, $3, 'CQW-SKU', '2x4 stud', 100, 'EA', 7.5000, 750.00, 0)`, uuid.New(), id, s.stud); err != nil {
		s.t.Fatal(err)
	}
	return id
}

var dealerQuoteKeys = []string{"number", "status", "dealer_name", "customer_name", "expires_at", "can_decide", "lines", "description",
	"quantity", "uom", "price_uom", "unit_price_ten_thousandths", "line_total_cents", "freight_cents", "total_cents"}

func TestWire_DealerQuoteLink_AcceptFromALinkWithNoLogin(t *testing.T) {
	s := newCQStack(t)
	ctx := context.Background()
	qid := s.dealerQuote("SENT")
	path := "/api/v1/quotes/" + qid.String() + "/share-link"

	// The dealer's desk issues the link; other roles and anonymous callers cannot.
	if r := s.do("POST", path, map[string]string{"X-Test-Role": "warehouse", "X-Test-Sub": "w"}, nil); r.status != 403 {
		t.Errorf("a warehouse role issued a link: %d %s", r.status, r.body)
	}
	if r := s.do("POST", "/api/v1/quotes/"+uuid.NewString()+"/share-link", asDesk, nil); r.status != 404 {
		t.Errorf("a quote that does not exist: %d %s", r.status, r.body)
	}
	if r := s.do("POST", "/api/v1/quotes/"+s.dealerQuote("DRAFT").String()+"/share-link", asDesk, nil); r.status != 409 {
		t.Errorf("a draft quote: %d %s", r.status, r.body)
	}
	r := s.do("POST", path, asDesk, map[string]any{"send_to": "buyer@contractor.example"})
	if r.status != 200 || r.header.Get("Cache-Control") != "no-store" {
		t.Fatalf("issue: %d %s", r.status, r.body)
	}
	link := jsonOf(t, r.body)
	token := strings.TrimPrefix(link["path"].(string), "/dq/")
	if len(token) != 43 || !strings.HasPrefix(link["url"].(string), "https://quotes.example/dq/") {
		t.Fatalf("link %v", link)
	}

	// The contractor opens it, no account: the allowlist, and none of the dealer's margin.
	pub := "/api/public/v1/dealer-quotes/" + token
	r = s.do("GET", pub, nil, nil)
	if r.status != 200 || r.header.Get("Cache-Control") != "no-store" || r.header.Get("Set-Cookie") != "" {
		t.Fatalf("view: %d %v %s", r.status, r.header, r.body)
	}
	assertKeysWithin(t, "the dealer quote page", r.body, dealerQuoteKeys)
	assertNoNumbers(t, "the dealer quote page", r.body, 333333, 3333)
	assertNoStrings(t, "the dealer quote page", r.body, "margin", "unit_cost", "average_unit_cost", "CQW-SKU")
	v := jsonOf(t, r.body)
	if v["status"] != "open" || v["can_decide"] != true || v["total_cents"].(float64) != 77500 || len(v["lines"].([]any)) != 1 {
		t.Fatalf("view %s", r.body)
	}

	// One kind of token opens one kind of target.
	_, customerToken := s.createAndSend(s.quoteBody(false, false))
	if r := s.do("GET", "/api/public/v1/dealer-quotes/"+customerToken, nil, nil); r.status != 404 {
		t.Errorf("a customer quote token on the dealer quote page: %d", r.status)
	}
	if r := s.do("GET", s.pub(token), nil, nil); r.status != 404 {
		t.Errorf("a dealer quote token on the customer quote page: %d", r.status)
	}

	// Accept: a name is required; the quote moves through its own lifecycle; the audit log keeps who and from where.
	if r := s.do("POST", pub+"/accept", nil, map[string]any{"full_name": ""}); r.status != 400 {
		t.Errorf("accept without a name: %d %s", r.status, r.body)
	}
	r = s.do("POST", pub+"/accept", nil, map[string]any{"full_name": "Pat Contractor"})
	if r.status != 200 || jsonOf(t, r.body)["status"] != "accepted" {
		t.Fatalf("accept: %d %s", r.status, r.body)
	}
	assertKeysWithin(t, "the accepted dealer quote", r.body, dealerQuoteKeys)
	var state string
	_ = s.db.Pool.QueryRow(ctx, `SELECT state::text FROM quotes WHERE id = $1`, qid).Scan(&state)
	if state != "ACCEPTED" {
		t.Errorf("the quote is %s", state)
	}
	var who, token_hash string
	if err := s.db.Pool.QueryRow(ctx, `SELECT changes->>'decided_by', changes->>'token_hash' FROM audit_log WHERE entity_type = 'quote' AND entity_id = $1 AND action = 'quote.accepted_from_link'`, qid).Scan(&who, &token_hash); err != nil {
		t.Fatalf("no audit row: %v", err)
	}
	if who != "Pat Contractor" || token_hash != customerquote.HashToken(token) {
		t.Errorf("audit: %q %q", who, token_hash)
	}
	if r := s.do("POST", pub+"/accept", nil, map[string]any{"full_name": "Pat Contractor"}); r.status != 409 {
		t.Errorf("accepting twice: %d %s", r.status, r.body)
	}
	if r := s.do("POST", pub+"/decline", nil, map[string]any{"full_name": "Pat Contractor"}); r.status != 409 {
		t.Errorf("declining an accepted quote: %d %s", r.status, r.body)
	}

	// A new link replaces the old; the old is gone (410), never logged.
	r = s.do("POST", path, asDesk, nil)
	if r.status != 409 {
		t.Errorf("a link for an accepted quote: %d %s", r.status, r.body)
	}
	if strings.Contains(s.logs.String(), token) || strings.Contains(s.logs.String(), customerToken) {
		t.Errorf("a token reached the log")
	}
}

func TestWire_DealerQuoteLink_DeclineAndReissue(t *testing.T) {
	s := newCQStack(t)
	qid := s.dealerQuote("SENT")
	path := "/api/v1/quotes/" + qid.String() + "/share-link"
	first := strings.TrimPrefix(jsonOf(t, s.do("POST", path, asDesk, nil).body)["path"].(string), "/dq/")
	second := strings.TrimPrefix(jsonOf(t, s.do("POST", path, asDesk, nil).body)["path"].(string), "/dq/")
	if first == second {
		t.Fatal("the same token was issued twice")
	}
	if r := s.do("GET", "/api/public/v1/dealer-quotes/"+first, nil, nil); r.status != 410 {
		t.Errorf("the replaced link: %d", r.status)
	}
	r := s.do("POST", "/api/public/v1/dealer-quotes/"+second+"/decline", nil, map[string]any{"full_name": "Pat Contractor", "reason": "too dear"})
	if r.status != 200 || jsonOf(t, r.body)["status"] != "declined" {
		t.Fatalf("decline: %d %s", r.status, r.body)
	}
	var state string
	_ = s.db.Pool.QueryRow(context.Background(), `SELECT state::text FROM quotes WHERE id = $1`, qid).Scan(&state)
	if state != "REJECTED" {
		t.Errorf("the quote is %s", state)
	}
}

// deskEmail is the desk user's work address. It must appear in no response a
// contractor or a customer can read.
const deskEmail = "dana.deskwell@dealer-staff.example"

// deskClaims is asRole with the desk user's email on the claims.
func deskClaims(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		role := r.Header.Get("X-Test-Role")
		claims := &middleware.UserClaims{RegisteredClaims: jwt.RegisteredClaims{Subject: r.Header.Get("X-Test-Sub")},
			Email: r.Header.Get("X-Test-Email"), Role: role, Roles: []string{role}}
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), middleware.UserContextKey, claims)))
	})
}

func TestWire_Codes_RefusedWhenTheServerCannotSendThem(t *testing.T) {
	s := newCQStack(t)
	const sentence = "Verification codes need a real email or text sender on this server."
	body := s.quoteBody(false, true)
	id, token := s.createAndSend(body) // while codes can be sent

	s.mail.setDown(true)
	r := s.do("PUT", "/api/portal/v1/customer-quotes/settings", asA, map[string]any{"tax": map[string]any{"mode": "at_checkout", "rate_bps": 0},
		"markup": map[string]any{"kind": "markup", "bps": 0}, "require_code": true})
	if r.status != 409 || !strings.Contains(string(r.body), sentence) {
		t.Fatalf("the toggle: %d %s", r.status, r.body)
	}
	r = s.do("PUT", "/api/portal/v1/customer-quotes/settings", asA, map[string]any{"tax": map[string]any{"mode": "at_checkout", "rate_bps": 0},
		"markup": map[string]any{"kind": "markup", "bps": 0}, "require_code": false})
	if r.status != 200 {
		t.Fatalf("turning it off: %d %s", r.status, r.body)
	}
	pub := s.pub(token)
	view := s.do("GET", pub, nil, nil)
	assertKeysWithin(t, "the blocked page", view.body, publicKeys)
	if n, _ := jsonOf(t, view.body)["notice"].(string); !strings.Contains(n, "cannot be accepted") {
		t.Errorf("the page's notice: %s", view.body)
	}
	if r := s.do("POST", pub+"/code", nil, map[string]any{"channel": "email"}); r.status != 409 || !strings.Contains(string(r.body), sentence) {
		t.Errorf("a code request: %d %s", r.status, r.body)
	}
	if r := s.do("POST", pub+"/accept", nil, map[string]any{"full_name": "Pat Homeowner", "email": "pat@homeowner.example",
		"signature": map[string]any{"kind": "typed", "text": "Pat Homeowner"}, "code": "123456"}); r.status != 409 || !strings.Contains(string(r.body), sentence) {
		t.Errorf("an acceptance: %d %s", r.status, r.body)
	}
	if len(s.mail.codes) != 0 {
		t.Errorf("a code was sent: %v", s.mail.codes)
	}
	_ = id
}

func TestWire_TheDesksEmailReachesNoContractorCustomerOrPdfResponse(t *testing.T) {
	s := newCQStack(t)
	ctx := context.Background()
	if _, err := s.db.Pool.Exec(ctx, `INSERT INTO staff (email, full_name) VALUES ($1, 'Dana Deskwell') ON CONFLICT (email) DO UPDATE SET full_name = 'Dana Deskwell'`, deskEmail); err != nil {
		t.Fatal(err)
	}
	var oldTrading string
	_ = s.db.Pool.QueryRow(ctx, `SELECT trading_name FROM dealer_profile WHERE id = 1`).Scan(&oldTrading)
	if _, err := s.db.Pool.Exec(ctx, `UPDATE dealer_profile SET trading_name = 'Granite Test Yard' WHERE id = 1`); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = s.db.Pool.Exec(ctx, `DELETE FROM staff WHERE email = $1`, deskEmail)
		_, _ = s.db.Pool.Exec(ctx, `UPDATE dealer_profile SET trading_name = $1 WHERE id = 1`, oldTrading)
	})

	id, token := s.createAndSend(s.quoteBody(false, false))
	var lines []any
	for _, l := range jsonOf(t, s.do("GET", s.pub(token), nil, nil).body)["lines"].([]any) {
		lines = append(lines, map[string]any{"line_id": l.(map[string]any)["id"], "state": "approved"})
	}
	s.do("PUT", s.pub(token)+"/selections", nil, map[string]any{"choices": lines})
	s.do("POST", s.pub(token)+"/accept", nil, map[string]any{"full_name": "Pat Homeowner", "phone": "555 010 2020", "signature": map[string]any{"kind": "typed", "text": "Pat Homeowner"}})
	s.do("POST", "/api/portal/v1/customer-quotes/"+id+"/confirm", asA, nil)
	if r := s.do("POST", "/api/v1/pro-projects/"+id+"/accept", asDesk, nil); r.status != 200 {
		t.Fatalf("dealer accept: %d %s", r.status, r.body)
	}
	// The dealer's own quote link, issued and accepted by the same desk user.
	dq := s.dealerQuote("SENT")
	issue := s.do("POST", "/api/v1/quotes/"+dq.String()+"/share-link", asDesk, nil)
	dtoken := strings.TrimPrefix(jsonOf(t, issue.body)["path"].(string), "/dq/")

	// The record keeps the desk user's id and display name, not the address.
	var ref, name string
	if err := s.db.Pool.QueryRow(ctx, `SELECT actor_ref, actor_name FROM pro_customer_quote_approval_log WHERE quote_id = $1 AND step = 'dealer_accepted'`, id).Scan(&ref, &name); err != nil {
		t.Fatal(err)
	}
	if ref != "desk-user-1" || name != "Dana Deskwell" {
		t.Errorf("the record holds %q and %q", ref, name)
	}

	local := strings.Split(deskEmail, "@")[0]
	responses := map[string]cqResp{
		"contractor quote":     s.do("GET", "/api/portal/v1/customer-quotes/"+id, asA, nil),
		"contractor list":      s.do("GET", "/api/portal/v1/customer-quotes", asA, nil),
		"contractor approvals": s.do("GET", "/api/portal/v1/customer-quotes/"+id+"/approvals", asA, nil),
		"contractor preview":   s.do("GET", "/api/portal/v1/customer-quotes/"+id+"/preview", asA, nil),
		"contractor pdf":       s.do("GET", "/api/portal/v1/customer-quotes/"+id+"/pdf", asA, nil),
		"export json":          s.do("GET", "/api/portal/v1/customer-quotes/export", asA, nil),
		"export csv":           s.do("GET", "/api/portal/v1/customer-quotes/export?format=csv&kind=quotes", asA, nil),
		"public view":          s.do("GET", s.pub(token), nil, nil),
		"public pdf":           s.do("GET", s.pub(token)+"/pdf", nil, nil),
		"dealer quote page":    s.do("GET", "/api/public/v1/dealer-quotes/"+dtoken, nil, nil),
		"link issue answer":    issue,
	}
	for what, r := range responses {
		if r.status != 200 {
			t.Errorf("%s: %d", what, r.status)
			continue
		}
		if bytes.Contains(r.body, []byte(deskEmail)) || bytes.Contains(r.body, []byte(local)) || bytes.Contains(r.body, []byte("dealer-staff")) {
			t.Errorf("%s carries the desk user's address: %s", what, truncate(r.body))
		}
	}
	// The contractor reads who accepted: the display name and the dealer's business name.
	items := jsonOf(t, responses["contractor approvals"].body)["items"].([]any)
	last := items[len(items)-1].(map[string]any)
	if last["step"] != "dealer_accepted" || last["actor_name"] != "Dana Deskwell, Granite Test Yard" || last["actor_contact"] != "" {
		t.Errorf("the contractor's view of the dealer step: %v", last)
	}
	// The dealer's own desk sees the display name too, not the address.
	det := s.do("GET", "/api/v1/pro-projects/"+id, asDesk, nil)
	if bytes.Contains(det.body, []byte(deskEmail)) || !bytes.Contains(det.body, []byte("Dana Deskwell")) {
		t.Errorf("the desk's view: %s", truncate(det.body))
	}
}

func TestWire_TheDealersPipelineIsHeldToTheCallersBranches(t *testing.T) {
	s := newCQStack(t, true)
	ctx := context.Background()
	other := uuid.New()
	for _, q := range []struct {
		sql  string
		args []any
	}{
		{`INSERT INTO locations (id, type, code, name) VALUES ($1, 'BRANCH', $2, 'cq other branch')`, []any{other, "cqo-" + other.String()[:8]}},
		{`INSERT INTO user_locations (user_sub, branch_id, is_home, granted_by) VALUES ('u-cq-other', $1, TRUE, 'test'), ('u-cq-home', $2, TRUE, 'test')`, []any{other, s.branch}},
	} {
		if _, err := s.db.Pool.Exec(ctx, q.sql, q.args...); err != nil {
			t.Fatal(err)
		}
	}
	t.Cleanup(func() {
		_, _ = s.db.Pool.Exec(ctx, `DELETE FROM user_locations WHERE user_sub IN ('u-cq-other', 'u-cq-home')`)
		_, _ = s.db.Pool.Exec(ctx, `DELETE FROM locations WHERE id = $1`, other)
	})
	id, token := s.createAndSend(s.quoteBody(false, false))
	var lines []any
	for _, l := range jsonOf(t, s.do("GET", s.pub(token), nil, nil).body)["lines"].([]any) {
		lines = append(lines, map[string]any{"line_id": l.(map[string]any)["id"], "state": "approved"})
	}
	s.do("PUT", s.pub(token)+"/selections", nil, map[string]any{"choices": lines})
	s.do("POST", s.pub(token)+"/accept", nil, map[string]any{"full_name": "Pat Homeowner", "phone": "555 010 2020", "signature": map[string]any{"kind": "typed", "text": "Pat Homeowner"}})
	if r := s.do("POST", "/api/portal/v1/customer-quotes/"+id+"/confirm", asA, nil); r.status != 200 {
		t.Fatalf("confirm: %d %s", r.status, r.body)
	}

	foreign := map[string]string{"X-Test-Role": "sales", "X-Test-Sub": "u-cq-other"}
	home := map[string]string{"X-Test-Role": "sales", "X-Test-Sub": "u-cq-home"}
	list := s.do("GET", "/api/v1/pro-projects", foreign, nil)
	if list.status != 200 || bytes.Contains(list.body, []byte(id)) || bytes.Contains(list.body, []byte("pat@homeowner.example")) {
		t.Errorf("another branch's list: %d %s", list.status, truncate(list.body))
	}
	if r := s.do("GET", "/api/v1/pro-projects/"+id, foreign, nil); r.status != 404 {
		t.Errorf("another branch's project: %d %s", r.status, truncate(r.body))
	}
	if r := s.do("POST", "/api/v1/pro-projects/"+id+"/accept", foreign, nil); r.status != 404 {
		t.Errorf("another branch's acceptance: %d %s", r.status, truncate(r.body))
	}
	var accepted *string
	_ = s.db.Pool.QueryRow(ctx, `SELECT dealer_accepted_at::text FROM pro_customer_quotes WHERE id = $1`, id).Scan(&accepted)
	if accepted != nil {
		t.Error("another branch's user recorded the dealer's acceptance")
	}
	// The branch that holds the contractor sees it.
	if r := s.do("GET", "/api/v1/pro-projects", home, nil); r.status != 200 || !bytes.Contains(r.body, []byte(id)) {
		t.Errorf("the home branch's list: %d %s", r.status, truncate(r.body))
	}
	if r := s.do("GET", "/api/v1/pro-projects/"+id, home, nil); r.status != 200 {
		t.Errorf("the home branch's project: %d", r.status)
	}
	if r := s.do("POST", "/api/v1/pro-projects/"+id+"/accept", home, nil); r.status != 200 {
		t.Errorf("the home branch's acceptance: %d %s", r.status, r.body)
	}
}
