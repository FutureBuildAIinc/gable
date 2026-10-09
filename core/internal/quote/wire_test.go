// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package quote_test

// The quote module on the wire contract (ADR 0001), tested end to end: a
// real Postgres, the real handler on a real mux, requests as JSON and
// responses read back as JSON. Nothing here touches a quote type, so each
// test states a wire fact, not an implementation. The four live failures the
// refactor inputs name for quotes (a missing unit of measure answered 500, a
// status filter that filtered nothing, offset paging that ignored its own
// limit, and error bodies that dropped the cause) each have a test that
// failed against the base commit.

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"

	"github.com/gablelbm/gable/internal/order"
	"github.com/gablelbm/gable/internal/platform/httpx"
	"github.com/gablelbm/gable/internal/quote"
	"github.com/gablelbm/gable/internal/testutil"
	"github.com/gablelbm/gable/pkg/database"
	"github.com/gablelbm/gable/pkg/middleware"
	"github.com/gablelbm/gable/pkg/outbox"
	"github.com/google/uuid"
)

type fixture struct {
	t             *testing.T
	db            *database.DB
	srv           *httptest.Server
	customerID    uuid.UUID
	productID     uuid.UUID
	product14     uuid.UUID // the 2x4x14 of the units tests, when they built it
	randomProduct uuid.UUID // the random length product of the tally tests
	sku           string
}

// newFixture builds the module the way serve does (repository, service with
// the outbox, handler) behind the global idempotency layer, with a customer
// and a product of its own.
func newFixture(t *testing.T, db *database.DB) *fixture {
	t.Helper()
	t.Setenv("AUTH_MODE", "dev")
	ctx := context.Background()
	f := &fixture{t: t, db: db, customerID: uuid.New(), productID: uuid.New(), sku: "WIRE-" + uuid.NewString()[:8]}
	branch := `(SELECT value::uuid FROM system_settings WHERE key = 'default_branch_id')`
	if _, err := db.Pool.Exec(ctx, `INSERT INTO customers (id, name, account_number, primary_branch_id)
		VALUES ($1, 'Wire Test Co', $2, `+branch+`)`, f.customerID, "WIRE-"+uuid.NewString()[:8]); err != nil {
		t.Fatalf("seed customer: %v", err)
	}
	if _, err := db.Pool.Exec(ctx, `INSERT INTO products (id, sku, description, uom_primary, base_price,
			board_thickness_in, board_width_in, board_length_ft)
		VALUES ($1, $2, '2x4x8 SPF', 'PCS', 5.5, 2, 4, 8)`, f.productID, f.sku); err != nil {
		t.Fatalf("seed product: %v", err)
	}
	// The 2x4x8's unit set (ADR 0006 section 3.2's worked set): PCS (1, 1),
	// LF (8, 1) sold by the foot, BF (1, 0.1875) and MBF (1, 187.5) priced,
	// MBF bought. The stocking row and the defaults came with the insert.
	if _, err := db.Pool.Exec(ctx, `INSERT INTO product_units (product_id, uom, unit_qty, stock_qty, sell, purchase, price) VALUES
		($1, 'LF', 8, 1, TRUE, FALSE, FALSE),
		($1, 'BF', 1, 0.1875, FALSE, FALSE, TRUE),
		($1, 'MBF', 1, 187.5, FALSE, TRUE, TRUE)`, f.productID); err != nil {
		t.Fatalf("seed unit set: %v", err)
	}
	if _, err := db.Pool.Exec(ctx, `UPDATE products SET purchase_uom = 'MBF' WHERE id = $1`, f.productID); err != nil {
		t.Fatalf("seed purchase default: %v", err)
	}

	repo := quote.NewRepository(db)
	// The convert creates the order in one act (ADR 0005 5.8): the real
	// order service over the same database, as serve wires it.
	orderSvc := order.NewService(order.NewRepository(db)).
		WithOutbox(outbox.NewWriter(db, "")).WithTxRunner(db)
	svc := quote.NewService(repo).WithOutbox(outbox.NewWriter(db, "")).WithTxRunner(db).WithOrderCreator(orderSvc)
	mux := http.NewServeMux()
	quote.NewHandler(svc).RegisterRoutes(mux)
	f.srv = httptest.NewServer(middleware.Idempotency(db)(mux))

	t.Cleanup(func() {
		f.srv.Close()
		_, _ = db.Pool.Exec(ctx, `DELETE FROM events_outbox WHERE entity_type = 'quote' AND entity_id IN (SELECT id FROM quotes WHERE customer_id = $1)`, f.customerID)
		_, _ = db.Pool.Exec(ctx, `DELETE FROM quotes WHERE customer_id = $1`, f.customerID)
		_, _ = db.Pool.Exec(ctx, `DELETE FROM products WHERE id = $1`, f.productID)
		_, _ = db.Pool.Exec(ctx, `DELETE FROM customers WHERE id = $1`, f.customerID)
	})
	return f
}

type resp struct {
	status int
	header http.Header
	body   map[string]any
	raw    []byte
}

func (f *fixture) do(method, path string, body any, headers ...string) resp {
	f.t.Helper()
	var rdr io.Reader
	switch b := body.(type) {
	case nil:
	case string:
		rdr = strings.NewReader(b)
	default:
		raw, err := json.Marshal(b)
		if err != nil {
			f.t.Fatal(err)
		}
		rdr = bytes.NewReader(raw)
	}
	req, err := http.NewRequest(method, f.srv.URL+path, rdr)
	if err != nil {
		f.t.Fatal(err)
	}
	if rdr != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	req.Header.Set("X-Request-ID", "req-wire-test")
	for i := 0; i+1 < len(headers); i += 2 {
		req.Header.Set(headers[i], headers[i+1])
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		f.t.Fatal(err)
	}
	defer res.Body.Close()
	raw, _ := io.ReadAll(res.Body)
	out := resp{status: res.StatusCode, header: res.Header, raw: raw}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	_ = dec.Decode(&out.body)
	return out
}

func (f *fixture) line(qty string) map[string]any {
	return map[string]any{
		"product_id": f.productID.String(), "sku": f.sku, "description": "2x4x8 SPF",
		"quantity": qty, "uom": "PCS", "unit_price_ten_thousandths": 55000,
	}
}

// nonStockLine is a line that names no product: its units are the
// catalogue's, not a product set's.
func (f *fixture) nonStockLine(qty, uom string) map[string]any {
	return map[string]any{
		"sku": "SPECIAL-" + f.sku, "description": "special order",
		"quantity": qty, "uom": uom, "unit_price_ten_thousandths": 55000,
	}
}

func (f *fixture) createBody(lines ...map[string]any) map[string]any {
	if len(lines) == 0 {
		lines = []map[string]any{f.line("10")}
	}
	return map[string]any{"customer_id": f.customerID.String(), "delivery_type": "pickup", "lines": lines}
}

func (f *fixture) create(lines ...map[string]any) resp {
	f.t.Helper()
	r := f.do("POST", "/api/v1/quotes", f.createBody(lines...))
	if r.status != http.StatusCreated {
		f.t.Fatalf("create = %d: %s", r.status, r.raw)
	}
	return r
}

func str(t *testing.T, m map[string]any, key string) string {
	t.Helper()
	s, ok := m[key].(string)
	if !ok {
		t.Fatalf("%s is %T (%v), want a string", key, m[key], m[key])
	}
	return s
}

func num(t *testing.T, m map[string]any, key string) int64 {
	t.Helper()
	n, ok := m[key].(json.Number)
	if !ok {
		t.Fatalf("%s is %T (%v), want an integer", key, m[key], m[key])
	}
	v, err := n.Int64()
	if err != nil {
		t.Fatalf("%s = %s, want an integer: %v", key, n, err)
	}
	return v
}

func errorOf(t *testing.T, r resp) (code, message string, details []map[string]any) {
	t.Helper()
	e, ok := r.body["error"].(map[string]any)
	if !ok {
		t.Fatalf("no error envelope in %s", r.raw)
	}
	code, _ = e["code"].(string)
	message, _ = e["message"].(string)
	if ds, ok := e["details"].([]any); ok {
		for _, d := range ds {
			details = append(details, d.(map[string]any))
		}
	}
	meta, _ := r.body["meta"].(map[string]any)
	if meta["request_id"] != "req-wire-test" {
		t.Errorf("meta.request_id = %v, want the request's id", meta["request_id"])
	}
	return code, message, details
}

func eventsFor(t *testing.T, db *database.DB, quoteID string) []string {
	return eventsForEntity(t, db, "quote", quoteID)
}

func eventsForEntity(t *testing.T, db *database.DB, entityType, entityID string) []string {
	t.Helper()
	rows, err := db.Pool.Query(context.Background(),
		`SELECT type FROM events_outbox WHERE entity_type = $1 AND entity_id = $2 ORDER BY position`, entityType, entityID)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var types []string
	for rows.Next() {
		var ty string
		if err := rows.Scan(&ty); err != nil {
			t.Fatal(err)
		}
		types = append(types, ty)
	}
	return types
}

// RULE (ADR 0001 sections 6, 7, 7a, 8, 11, 12): a created quote carries a
// document number from the sequence, a lowercase status, integer _cents
// money, the scaled unit price, the conversion pair, a revision and its ETag,
// and the create writes quote.created exactly once.
func TestWire_CreateShape(t *testing.T) {
	testutil.LockOutboxTables(t)
	f := newFixture(t, testutil.RequireDB(t))

	r := f.create()
	q := r.body
	if !regexp.MustCompile(`^Q-\d{6,}$`).MatchString(str(t, q, "number")) {
		t.Errorf("number = %v, want Q- and at least six digits", q["number"])
	}
	if str(t, q, "status") != "draft" {
		t.Errorf("status = %v, want draft", q["status"])
	}
	if _, legacy := q["state"]; legacy {
		t.Error("the legacy state field is still on the wire")
	}
	if num(t, q, "revision") != 1 || r.header.Get("ETag") != `"1"` {
		t.Errorf("revision = %v, ETag = %q, want 1 and \"1\"", q["revision"], r.header.Get("ETag"))
	}
	if num(t, q, "total_cents") != 5500 {
		t.Errorf("total_cents = %v, want 5500 (10 at 5.5000)", q["total_cents"])
	}
	for _, legacy := range []string{"total_amount", "freight_amount", "margin_total", "exposure_dollars"} {
		if _, ok := q[legacy]; ok {
			t.Errorf("legacy float money field %s is still on the wire", legacy)
		}
	}
	if str(t, q, "delivery_type") != "pickup" {
		t.Errorf("delivery_type = %v, want pickup", q["delivery_type"])
	}
	if !strings.HasPrefix(r.header.Get("Location"), "/api/v1/quotes/") {
		t.Errorf("Location = %q", r.header.Get("Location"))
	}
	if !strings.HasSuffix(str(t, q, "created_at"), "Z") || !regexp.MustCompile(`\.\d{6}Z$`).MatchString(str(t, q, "created_at")) {
		t.Errorf("created_at = %v, want RFC 3339 UTC at microsecond precision", q["created_at"])
	}
	if q["expires_at"] != nil || q["sent_at"] != nil {
		t.Errorf("unset optional fields must be present as null: expires_at=%v sent_at=%v", q["expires_at"], q["sent_at"])
	}

	lines := q["lines"].([]any)
	l := lines[0].(map[string]any)
	if str(t, l, "quantity") != "10" || str(t, l, "uom") != "PCS" || str(t, l, "price_uom") != "PCS" {
		t.Errorf("line quantity/uom/price_uom = %v/%v/%v", l["quantity"], l["uom"], l["price_uom"])
	}
	if num(t, l, "unit_price_ten_thousandths") != 55000 || num(t, l, "line_total_cents") != 5500 {
		t.Errorf("line price/total = %v/%v", l["unit_price_ten_thousandths"], l["line_total_cents"])
	}
	if str(t, l, "uom_qty") != "1" || str(t, l, "price_uom_qty") != "1" {
		t.Errorf("a line whose units agree carries the pair as 1 and 1, got %v and %v", l["uom_qty"], l["price_uom_qty"])
	}
	for _, legacy := range []string{"unit_price", "line_total", "unit_cost"} {
		if _, ok := l[legacy]; ok {
			t.Errorf("legacy line field %s is still on the wire", legacy)
		}
	}

	id := str(t, q, "id")
	if got := eventsFor(t, f.db, id); len(got) != 1 || got[0] != "quote.created" {
		t.Errorf("events = %v, want exactly [quote.created]", got)
	}

	// A read carries the same number, revision and ETag.
	g := f.do("GET", "/api/v1/quotes/"+id, nil)
	if g.status != 200 || str(t, g.body, "number") != str(t, q, "number") || g.header.Get("ETag") != `"1"` {
		t.Errorf("GET = %d number=%v ETag=%q", g.status, g.body["number"], g.header.Get("ETag"))
	}
}

// Document numbers are distinct and ascending.
func TestWire_NumbersAreSequential(t *testing.T) {
	testutil.LockOutboxTables(t)
	f := newFixture(t, testutil.RequireDB(t))
	a := str(t, f.create().body, "number")
	b := str(t, f.create().body, "number")
	var na, nb int
	fmt.Sscanf(a, "Q-%d", &na)
	fmt.Sscanf(b, "Q-%d", &nb)
	if nb <= na {
		t.Errorf("numbers %s then %s are not ascending", a, b)
	}
}

// RULE (ADR 0001 section 7a): the extension is the quantity times the unit
// price times price_uom_qty over uom_qty, rounded once to cents half away
// from zero; freight joins the total on a delivery and is cleared on a pickup.
func TestWire_MoneyIsExact(t *testing.T) {
	testutil.LockOutboxTables(t)
	f := newFixture(t, testutil.RequireDB(t))

	// 187.5 PCS priced at 500.00 per MBF, 187.5 PCS = 1 MBF: exactly 50000 cents.
	lumber := f.line("187.5")
	lumber["unit_price_ten_thousandths"] = 5000000
	lumber["price_uom"] = "MBF"
	lumber["uom_qty"] = "187.5"
	lumber["price_uom_qty"] = "1"
	// One piece of the same lumber: 266.67 cents rounds to 267.
	piece := f.line("1")
	piece["unit_price_ten_thousandths"] = 5000000
	piece["price_uom"] = "MBF"
	piece["uom_qty"] = "187.5"
	piece["price_uom_qty"] = "1"

	body := f.createBody(lumber, piece)
	body["delivery_type"] = "delivery"
	body["freight_cents"] = 12500
	r := f.do("POST", "/api/v1/quotes", body)
	if r.status != 201 {
		t.Fatalf("create = %d: %s", r.status, r.raw)
	}
	lines := r.body["lines"].([]any)
	if got := num(t, lines[0].(map[string]any), "line_total_cents"); got != 50000 {
		t.Errorf("lumber line total = %d, want 50000", got)
	}
	if got := num(t, lines[1].(map[string]any), "line_total_cents"); got != 267 {
		t.Errorf("one piece total = %d, want 267 (266.67 rounded once)", got)
	}
	if got := num(t, r.body, "total_cents"); got != 50000+267+12500 {
		t.Errorf("total_cents = %d, want %d", got, 50000+267+12500)
	}
	if str(t, lines[0].(map[string]any), "uom_qty") != "187.5" || str(t, lines[0].(map[string]any), "price_uom") != "MBF" {
		t.Errorf("the conversion pair did not round trip: %v", lines[0])
	}

	// A pickup clears freight rather than billing it.
	pick := f.createBody()
	pick["freight_cents"] = 9900
	pr := f.do("POST", "/api/v1/quotes", pick)
	if pr.status != 201 || num(t, pr.body, "freight_cents") != 0 || num(t, pr.body, "total_cents") != 5500 {
		t.Errorf("pickup freight/total = %v/%v, want 0/5500 (%s)", pr.body["freight_cents"], pr.body["total_cents"], pr.raw)
	}
}

// LIVE FAILURE 1 (inputs section 3, item 1): a quote line without a unit of
// measure was a 500 from a database enum cast. A line that names a product
// has a unit (the product's own, the server's default); only a line with
// neither a uom nor a product is a 400 naming lines[i].uom, and every other
// field error rides in the same response.
func TestWire_MissingUomIs400NamingTheField(t *testing.T) {
	testutil.LockOutboxTables(t)
	f := newFixture(t, testutil.RequireDB(t))

	special := map[string]any{
		"sku": "SPECIAL-1", "description": "special order", "quantity": "2", "unit_price_ten_thousandths": 10000,
	}
	bad := f.line("oops")
	r := f.do("POST", "/api/v1/quotes", f.createBody(special, bad))
	if r.status != 400 {
		t.Fatalf("status = %d, want 400: %s", r.status, r.raw)
	}
	code, _, details := errorOf(t, r)
	if code != "validation_failed" {
		t.Errorf("code = %q, want validation_failed", code)
	}
	fields := map[string]bool{}
	for _, d := range details {
		fields[fmt.Sprint(d["field"])] = true
	}
	if !fields["lines[0].uom"] {
		t.Errorf("details = %v, want an entry naming lines[0].uom", details)
	}
	if !fields["lines[1].quantity"] {
		t.Errorf("details = %v, want the second line's bad quantity collected in the same pass", details)
	}
	var n int
	if err := f.db.Pool.QueryRow(context.Background(), `SELECT count(*) FROM quotes WHERE customer_id = $1`, f.customerID).Scan(&n); err != nil || n != 0 {
		t.Errorf("a refused create left %d quotes (err %v)", n, err)
	}

	// A line with a product and no uom takes the product's unit, and its
	// price_uom follows the uom.
	line := f.line("10")
	delete(line, "uom")
	ok := f.do("POST", "/api/v1/quotes", f.createBody(line))
	if ok.status != 201 {
		t.Fatalf("a product line without uom = %d, want 201: %s", ok.status, ok.raw)
	}
	got := ok.body["lines"].([]any)[0].(map[string]any)
	if str(t, got, "uom") != "PCS" || str(t, got, "price_uom") != "PCS" || str(t, got, "uom_qty") != "1" || str(t, got, "price_uom_qty") != "1" {
		t.Errorf("defaulted unit = %v, want PCS priced per PCS at 1 to 1", got)
	}
	if num(t, got, "line_total_cents") != 5500 {
		t.Errorf("line_total_cents = %d, want 5500", num(t, got, "line_total_cents"))
	}

	// A price_uom that differs from the uom is resolved from the product's
	// set: the pair the server stores (ADR 0006 section 3.3, which replaces
	// the pair the client had to send).
	line = f.line("10")
	delete(line, "uom")
	line["price_uom"] = "MBF"
	r = f.do("POST", "/api/v1/quotes", f.createBody(line))
	if r.status != 201 {
		t.Fatalf("a product line priced per MBF with no pair = %d, want 201: %s", r.status, r.raw)
	}
	got = r.body["lines"].([]any)[0].(map[string]any)
	if str(t, got, "uom") != "PCS" || str(t, got, "price_uom") != "MBF" ||
		str(t, got, "uom_qty") != "187.5" || str(t, got, "price_uom_qty") != "1" {
		t.Errorf("the resolved pair = %v, want PCS against MBF at 187.5 to 1", got)
	}
	if str(t, got, "stock_uom") != "PCS" || str(t, got, "stock_quantity") != "10" {
		t.Errorf("the stocking fields = %v, want 10 PCS of stock", got)
	}
}

func TestWire_ValidationRules(t *testing.T) {
	testutil.LockOutboxTables(t)
	f := newFixture(t, testutil.RequireDB(t))

	cases := []struct {
		name      string
		mutate    func(body map[string]any, line map[string]any)
		wantField string
	}{
		{"unknown unit of measure", func(b, l map[string]any) { l["uom"] = "pcs" }, "lines[0].uom"},
		{"float unit price", func(b, l map[string]any) { l["unit_price_ten_thousandths"] = 5.5 }, "lines[0].unit_price_ten_thousandths"},
		{"missing unit price", func(b, l map[string]any) { delete(l, "unit_price_ten_thousandths") }, "lines[0].unit_price_ten_thousandths"},
		{"negative unit price", func(b, l map[string]any) { l["unit_price_ten_thousandths"] = -1 }, "lines[0].unit_price_ten_thousandths"},
		{"quantity as a JSON number", func(b, l map[string]any) { l["quantity"] = 10 }, "lines[0].quantity"},
		{"zero quantity", func(b, l map[string]any) { l["quantity"] = "0" }, "lines[0].quantity"},
		{"zero conversion side", func(b, l map[string]any) { l["uom_qty"] = "0"; l["price_uom_qty"] = "1" }, "lines[0].uom_qty"},
		{"price unit differs with no pair", func(b, l map[string]any) {
			// A non stock line carries the client's pair: without it the
			// two units have no conversion (a product line's pair resolves
			// from its set, ADR 0006 section 3.3).
			delete(l, "product_id")
			l["sku"], l["description"] = "SPECIAL", "special order"
			l["price_uom"] = "MBF"
		}, "lines[0].uom_qty"},
		{"unknown product", func(b, l map[string]any) { l["product_id"] = uuid.NewString() }, "lines[0].product_id"},
		{"missing customer", func(b, l map[string]any) { delete(b, "customer_id") }, "customer_id"},
		{"unknown customer", func(b, l map[string]any) { b["customer_id"] = uuid.NewString() }, "customer_id"},
		{"unknown delivery type", func(b, l map[string]any) { b["delivery_type"] = "PICKUP" }, "delivery_type"},
		{"float freight", func(b, l map[string]any) { b["freight_cents"] = 12.5 }, "freight_cents"},
		{"negative freight", func(b, l map[string]any) { b["freight_cents"] = -1 }, "freight_cents"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			line := f.line("10")
			body := f.createBody(line)
			c.mutate(body, line)
			r := f.do("POST", "/api/v1/quotes", body)
			if r.status != 400 {
				t.Fatalf("status = %d, want 400: %s", r.status, r.raw)
			}
			code, _, details := errorOf(t, r)
			if code != "validation_failed" {
				t.Errorf("code = %q, want validation_failed", code)
			}
			named := false
			for _, d := range details {
				if d["field"] == c.wantField {
					named = true
				}
			}
			if !named {
				t.Errorf("details = %v, want one naming %s", details, c.wantField)
			}
		})
	}

	// A body the route cannot consume is bad_request, not validation_failed.
	for name, body := range map[string]any{
		"malformed json":     `{"customer_id":`,
		"unknown body field": map[string]any{"customer_id": f.customerID.String(), "lines": []any{}, "state": "SENT"},
		"legacy float price": map[string]any{"customer_id": f.customerID.String(), "lines": []any{map[string]any{"unit_price": 5.5}}},
		"status on create":   map[string]any{"customer_id": f.customerID.String(), "lines": []any{}, "status": "sent"},
		"empty body":         ``,
	} {
		r := f.do("POST", "/api/v1/quotes", body)
		if r.status != 400 {
			t.Errorf("%s: status = %d, want 400: %s", name, r.status, r.raw)
			continue
		}
		if code, _, _ := errorOf(t, r); code != "bad_request" {
			t.Errorf("%s: code = %q, want bad_request", name, code)
		}
	}
}

// LIVE FAILURE 2 (inputs section 3, item 2): ?status=sent returned drafts
// because the filter was never implemented. It filters, in lowercase, and a
// value or a parameter the route does not support is a 400 naming it.
func TestWire_StatusFilterFilters(t *testing.T) {
	testutil.LockOutboxTables(t)
	f := newFixture(t, testutil.RequireDB(t))
	cust := "&customer_id=" + f.customerID.String()

	draft := str(t, f.create().body, "id")
	sent := f.create()
	sentID := str(t, sent.body, "id")
	if r := f.do("POST", "/api/v1/quotes/"+sentID+"/transitions", map[string]any{"to": "sent", "revision": 1}); r.status != 200 {
		t.Fatalf("transition = %d: %s", r.status, r.raw)
	}

	ids := func(path string) []string {
		r := f.do("GET", path, nil)
		if r.status != 200 {
			t.Fatalf("%s = %d: %s", path, r.status, r.raw)
		}
		var out []string
		for _, it := range r.body["items"].([]any) {
			m := it.(map[string]any)
			if m["status"] == nil {
				t.Fatalf("list item without status: %v", m)
			}
			out = append(out, str(t, m, "id")+":"+str(t, m, "status"))
		}
		return out
	}
	if got := ids("/api/v1/quotes?status=sent" + cust); len(got) != 1 || got[0] != sentID+":sent" {
		t.Errorf("status=sent returned %v, want only the sent quote", got)
	}
	if got := ids("/api/v1/quotes?status=draft" + cust); len(got) != 1 || got[0] != draft+":draft" {
		t.Errorf("status=draft returned %v, want only the draft quote", got)
	}
	if got := ids("/api/v1/quotes?status=draft,sent" + cust); len(got) != 2 {
		t.Errorf("status=draft,sent returned %v, want both", got)
	}
	if got := ids("/api/v1/quotes?status=accepted" + cust); len(got) != 0 {
		t.Errorf("status=accepted returned %v, want none", got)
	}

	for path, field := range map[string]string{
		"/api/v1/quotes?status=SENT":   "status", // the wire is lowercase
		"/api/v1/quotes?status=bogus":  "status",
		"/api/v1/quotes?status=":       "status",
		"/api/v1/quotes?customer_id=x": "customer_id",
	} {
		r := f.do("GET", path, nil)
		code, _, details := errorOf(t, r)
		if r.status != 400 || code != "validation_failed" || len(details) == 0 || details[0]["field"] != field {
			t.Errorf("%s = %d %s %v, want a 400 validation_failed naming %s", path, r.status, code, details, field)
		}
	}
	for _, path := range []string{"/api/v1/quotes?foo=1", "/api/v1/quotes?offset=10", "/api/v1/quotes/" + draft + "?x=1"} {
		r := f.do("GET", path, nil)
		code, _, details := errorOf(t, r)
		if r.status != 400 || code != "unsupported_query_parameter" || len(details) == 0 {
			t.Errorf("%s = %d %s %v, want a 400 unsupported_query_parameter", path, r.status, code, details)
		}
	}
}

// LIVE FAILURE 3 (inputs section 3, item 3): offset paging that clamped and
// ignored its own parameters, with null for an empty page. The list is the
// cursor envelope: items never null, a cursor that walks every row once,
// limit validated, total only on request.
func TestWire_ListEnvelopeAndCursor(t *testing.T) {
	testutil.LockOutboxTables(t)
	f := newFixture(t, testutil.RequireDB(t))
	cust := "&customer_id=" + f.customerID.String()

	want := map[string]bool{}
	for i := 0; i < 5; i++ {
		want[str(t, f.create().body, "id")] = true
	}

	seen := map[string]bool{}
	path := "/api/v1/quotes?limit=2" + cust
	pages := 0
	for path != "" {
		r := f.do("GET", path, nil)
		if r.status != 200 {
			t.Fatalf("%s = %d: %s", path, r.status, r.raw)
		}
		if _, hasTotal := r.body["total"]; hasTotal {
			t.Error("total present without ?include=total")
		}
		if num(t, r.body, "limit") != 2 {
			t.Errorf("limit echo = %v, want 2", r.body["limit"])
		}
		items := r.body["items"].([]any)
		if len(items) > 2 {
			t.Fatalf("page of %d items under limit=2", len(items))
		}
		for _, it := range items {
			id := str(t, it.(map[string]any), "id")
			if seen[id] {
				t.Errorf("quote %s served twice", id)
			}
			seen[id] = true
		}
		pages++
		if next, ok := r.body["next_cursor"].(string); ok {
			path = "/api/v1/quotes?limit=2&cursor=" + next + cust
		} else {
			if r.body["next_cursor"] != nil {
				t.Errorf("next_cursor = %v, want null on the last page", r.body["next_cursor"])
			}
			path = ""
		}
		if pages > 10 {
			t.Fatal("the cursor never ended")
		}
	}
	if len(seen) != len(want) {
		t.Errorf("walked %d quotes, want %d", len(seen), len(want))
	}
	for id := range want {
		if !seen[id] {
			t.Errorf("quote %s never served", id)
		}
	}
	if pages != 3 {
		t.Errorf("pages = %d, want 3 (2+2+1)", pages)
	}

	// include=total counts the filtered set.
	r := f.do("GET", "/api/v1/quotes?include=total&limit=1"+cust, nil)
	if num(t, r.body, "total") != 5 {
		t.Errorf("total = %v, want 5", r.body["total"])
	}

	// An empty page is [], never null.
	empty := f.do("GET", "/api/v1/quotes?status=expired"+cust, nil)
	if items, ok := empty.body["items"].([]any); !ok || len(items) != 0 {
		t.Errorf("empty page items = %#v (%s), want []", empty.body["items"], empty.raw)
	}
	if !bytes.Contains(empty.raw, []byte(`"items":[]`)) {
		t.Errorf("empty page body %s does not carry items as []", empty.raw)
	}

	// limit and cursor are validated, not clamped or ignored.
	for _, q := range []string{"limit=0", "limit=201", "limit=abc", "limit=-1", "cursor=garbage", "include=nope"} {
		r := f.do("GET", "/api/v1/quotes?"+q, nil)
		if r.status != 400 {
			t.Errorf("?%s = %d, want 400: %s", q, r.status, r.raw)
			continue
		}
		errorOf(t, r)
	}
	// A cursor minted for another ordering is refused.
	foreign, err := httpx.MintCursor("events.position", "1")
	if err != nil {
		t.Fatal(err)
	}
	if r := f.do("GET", "/api/v1/quotes?cursor="+foreign, nil); r.status != 400 {
		t.Errorf("a foreign cursor = %d, want 400: %s", r.status, r.raw)
	}
}

// LIVE FAILURE 4 (inputs section 3, item 7): error bodies of six shapes that
// dropped the cause. Every refusal is the one envelope, with the handler's
// message and the right status.
func TestWire_ErrorsAreOneEnvelope(t *testing.T) {
	testutil.LockOutboxTables(t)
	f := newFixture(t, testutil.RequireDB(t))
	id := str(t, f.create().body, "id")

	cases := []struct {
		name         string
		method, path string
		body         any
		headers      []string
		status       int
		code         string
		message      string
	}{
		{"unknown quote", "GET", "/api/v1/quotes/" + uuid.NewString(), nil, nil, 404, "not_found", "quote not found"},
		{"malformed id", "GET", "/api/v1/quotes/not-a-uuid", nil, nil, 400, "bad_request", "invalid quote id"},
		{"unknown quote file", "GET", "/api/v1/quotes/" + uuid.NewString() + "/file", nil, nil, 404, "not_found", "quote not found"},
		{"no stored file", "GET", "/api/v1/quotes/" + id + "/file", nil, nil, 404, "not_found", "no original file is stored for this quote"},
		{"transition target unknown", "POST", "/api/v1/quotes/" + id + "/transitions", map[string]any{"to": "SENT", "revision": 1}, nil, 400, "validation_failed", "one or more fields failed validation"},
		{"transition not allowed", "POST", "/api/v1/quotes/" + id + "/transitions", map[string]any{"to": "draft", "revision": 1}, nil, 409, "invalid_state_transition", "cannot transition from draft to draft"},
		{"transition without a revision", "POST", "/api/v1/quotes/" + id + "/transitions", map[string]any{"to": "sent"}, nil, 428, "precondition_required", "this write needs If-Match or a body revision"},
		{"stale revision", "POST", "/api/v1/quotes/" + id + "/transitions", map[string]any{"to": "sent", "revision": 7}, nil, 409, "stale_revision", ""},
		{"transition on an unknown quote", "POST", "/api/v1/quotes/" + uuid.NewString() + "/transitions", map[string]any{"to": "sent", "revision": 1}, nil, 404, "not_found", "quote not found"},
		{"removed state route", "PUT", "/api/v1/quotes/" + id + "/state", map[string]any{"state": "SENT"}, nil, 404, "", ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r := f.do(c.method, c.path, c.body, c.headers...)
			if r.status != c.status {
				t.Fatalf("status = %d, want %d: %s", r.status, c.status, r.raw)
			}
			if c.code == "" {
				return
			}
			code, message, _ := errorOf(t, r)
			if code != c.code {
				t.Errorf("code = %q, want %q", code, c.code)
			}
			if c.message != "" && message != c.message {
				t.Errorf("message = %q, want the handler's own %q", message, c.message)
			}
		})
	}
}

// RULE (ADR 0001 section 11): updates and transitions need the client's
// revision (If-Match or the body), a mismatch is 409 stale_revision, a write
// with neither is 428, and a good write returns the new revision and ETag.
func TestWire_RevisionPreconditions(t *testing.T) {
	testutil.LockOutboxTables(t)
	f := newFixture(t, testutil.RequireDB(t))
	id := str(t, f.create().body, "id")
	path := "/api/v1/quotes/" + id

	edit := func(qty string) map[string]any {
		b := f.createBody(f.line(qty))
		return b
	}

	if r := f.do("PUT", path, edit("20")); r.status != 428 {
		t.Errorf("PUT with no precondition = %d, want 428: %s", r.status, r.raw)
	}
	if r := f.do("PUT", path, edit("20"), "If-Match", `"9"`); r.status != 409 {
		t.Errorf("PUT with a stale If-Match = %d, want 409: %s", r.status, r.raw)
	} else if code, _, _ := errorOf(t, r); code != "stale_revision" {
		t.Errorf("code = %q, want stale_revision", code)
	}
	if r := f.do("PUT", path, edit("20"), "If-Match", "*"); r.status != 400 {
		t.Errorf("PUT with If-Match * = %d, want 400", r.status)
	}

	ok := f.do("PUT", path, edit("20"), "If-Match", `"1"`)
	if ok.status != 200 {
		t.Fatalf("PUT = %d: %s", ok.status, ok.raw)
	}
	if num(t, ok.body, "revision") != 2 || ok.header.Get("ETag") != `"2"` || num(t, ok.body, "total_cents") != 11000 {
		t.Errorf("after PUT revision=%v ETag=%q total=%v, want 2, \"2\", 11000", ok.body["revision"], ok.header.Get("ETag"), ok.body["total_cents"])
	}

	// The same stale write again: the revision moved on.
	if r := f.do("PUT", path, edit("30"), "If-Match", `"1"`); r.status != 409 {
		t.Errorf("replayed stale PUT = %d, want 409", r.status)
	}

	// The body's revision works as the precondition too, and so does the weak form.
	body := edit("40")
	body["revision"] = 2
	if r := f.do("PUT", path, body); r.status != 200 || num(t, r.body, "revision") != 3 {
		t.Errorf("PUT with a body revision = %d: %s", r.status, r.raw)
	}
	if r := f.do("PUT", path, edit("50"), "If-Match", `W/"3"`); r.status != 200 || num(t, r.body, "revision") != 4 {
		t.Errorf("PUT with a weak If-Match = %d: %s", r.status, r.raw)
	}
	// Header and body that disagree are refused.
	body = edit("60")
	body["revision"] = 4
	if r := f.do("PUT", path, body, "If-Match", `"3"`); r.status != 400 {
		t.Errorf("PUT with disagreeing precondition = %d, want 400", r.status)
	}

	// A PUT cannot move the lifecycle, and only drafts are editable.
	if r := f.do("POST", path+"/transitions", map[string]any{"to": "sent"}, "If-Match", `"4"`); r.status != 200 || num(t, r.body, "revision") != 5 {
		t.Fatalf("transition = %d: %s", r.status, r.raw)
	}
	r := f.do("PUT", path, edit("70"), "If-Match", `"5"`)
	if r.status != 409 {
		t.Fatalf("PUT on a sent quote = %d, want 409: %s", r.status, r.raw)
	}
	if code, msg, _ := errorOf(t, r); code != "conflict" || msg != "only draft quotes can be edited" {
		t.Errorf("PUT on a sent quote = %s %q", code, msg)
	}
}

// Line identity survives an edit: a portal customer's note stays with the line.
func TestWire_PutKeepsCustomerNotes(t *testing.T) {
	testutil.LockOutboxTables(t)
	f := newFixture(t, testutil.RequireDB(t))

	r := f.create()
	id := str(t, r.body, "id")
	lineID := str(t, r.body["lines"].([]any)[0].(map[string]any), "id")
	if _, err := f.db.Pool.Exec(context.Background(),
		`UPDATE quote_lines SET customer_note = 'cut to length' WHERE id = $1`, lineID); err != nil {
		t.Fatal(err)
	}

	line := f.line("12")
	line["id"] = lineID
	put := f.do("PUT", "/api/v1/quotes/"+id, f.createBody(line), "If-Match", `"1"`)
	if put.status != 200 {
		t.Fatalf("PUT = %d: %s", put.status, put.raw)
	}
	got := put.body["lines"].([]any)[0].(map[string]any)
	if str(t, got, "id") != lineID || str(t, got, "customer_note") != "cut to length" {
		t.Errorf("line after edit = %v, want the same id and its note", got)
	}
}

// A create has no revision to precondition on: a body revision is refused
// rather than accepted and ignored.
func TestWire_CreateRefusesARevision(t *testing.T) {
	testutil.LockOutboxTables(t)
	f := newFixture(t, testutil.RequireDB(t))

	body := f.createBody()
	body["revision"] = 1
	r := f.do("POST", "/api/v1/quotes", body)
	if r.status != 400 {
		t.Fatalf("create with a revision = %d, want 400: %s", r.status, r.raw)
	}
	_, _, details := errorOf(t, r)
	if len(details) != 1 || details[0]["field"] != "revision" {
		t.Errorf("details = %v, want one entry naming revision", details)
	}
	var n int
	if err := f.db.Pool.QueryRow(context.Background(), `SELECT count(*) FROM quotes WHERE customer_id = $1`, f.customerID).Scan(&n); err != nil || n != 0 {
		t.Errorf("a refused create left %d quotes (err %v)", n, err)
	}
}

// price_uom is a unit code, not free text: one to six capital letters (a price
// per M or per CWT is real, so it is not limited to the sale unit enum). On a
// line that names a product the code must also be a price unit of the
// product's set (ADR 0006 section 7.4); the free vocabulary is a non stock
// line's.
func TestWire_PriceUomIsAUnitCode(t *testing.T) {
	testutil.LockOutboxTables(t)
	f := newFixture(t, testutil.RequireDB(t))

	for _, bad := range []string{"<script>", "mbf", "TOOLONGUNIT", "M BF", "M1"} {
		line := f.nonStockLine("10", "EA")
		line["price_uom"] = bad
		line["uom_qty"], line["price_uom_qty"] = "1000", "1"
		r := f.do("POST", "/api/v1/quotes", f.createBody(line))
		if r.status != 400 {
			t.Errorf("price_uom %q = %d, want 400: %s", bad, r.status, r.raw)
			continue
		}
		_, _, details := errorOf(t, r)
		if len(details) != 1 || details[0]["field"] != "lines[0].price_uom" {
			t.Errorf("price_uom %q: details = %v, want lines[0].price_uom", bad, details)
		}
	}
	for _, good := range []string{"M", "CWT", "MBF"} {
		line := f.nonStockLine("10", "EA")
		line["price_uom"] = good
		line["uom_qty"], line["price_uom_qty"] = "1000", "1"
		if r := f.do("POST", "/api/v1/quotes", f.createBody(line)); r.status != 201 {
			t.Errorf("price_uom %q = %d, want 201: %s", good, r.status, r.raw)
		}
	}
	// On a product line the price unit must be a price unit of the set: the
	// fixture's 2x4x8 prices in PCS, BF and MBF, not M.
	line := f.line("10")
	line["price_uom"] = "M"
	line["uom_qty"], line["price_uom_qty"] = "1000", "1"
	r := f.do("POST", "/api/v1/quotes", f.createBody(line))
	if r.status != 400 {
		t.Fatalf("a price unit outside the product's set = %d, want 400: %s", r.status, r.raw)
	}
	_, _, details := errorOf(t, r)
	if len(details) != 1 || details[0]["field"] != "lines[0].price_uom" {
		t.Errorf("details = %v, want lines[0].price_uom", details)
	}
}

// A PUT replaces the header and the lines; a field it does not apply is a 400
// naming it, never a silent drop.
func TestWire_PutRefusesFieldsItDoesNotApply(t *testing.T) {
	testutil.LockOutboxTables(t)
	f := newFixture(t, testutil.RequireDB(t))
	id := str(t, f.create().body, "id")

	for field, value := range map[string]any{
		"source": "ai", "margin_total_cents": 1200, "original_file": "aGVsbG8=", "original_filename": "list.pdf",
		"original_content_type": "application/pdf", "parse_map": []any{}, "branch_id": uuid.NewString(),
	} {
		body := f.createBody()
		body[field] = value
		r := f.do("PUT", "/api/v1/quotes/"+id, body, "If-Match", `"1"`)
		if r.status != 400 {
			t.Errorf("PUT carrying %s = %d, want 400: %s", field, r.status, r.raw)
			continue
		}
		_, _, details := errorOf(t, r)
		if len(details) != 1 || details[0]["field"] != field {
			t.Errorf("PUT carrying %s: details = %v, want one entry naming it", field, details)
		}
	}
	// Nothing was applied: the revision is where it was.
	g := f.do("GET", "/api/v1/quotes/"+id, nil)
	if num(t, g.body, "revision") != 1 {
		t.Errorf("a refused PUT moved the revision to %v", g.body["revision"])
	}
	// The create accepts the same fields.
	body := f.createBody()
	body["source"] = "ai"
	body["margin_total_cents"] = 1200
	if r := f.do("POST", "/api/v1/quotes", body); r.status != 201 {
		t.Errorf("create with source and margin = %d: %s", r.status, r.raw)
	}
}

// RULE (ADR 0001 section 11 and ADR 0003): a transition is a POST to
// /transitions, each one writes its own event, the lifecycle timestamps are
// stamped, and a transition the lifecycle forbids is 409.
func TestWire_TransitionsAndEvents(t *testing.T) {
	testutil.LockOutboxTables(t)
	f := newFixture(t, testutil.RequireDB(t))
	id := str(t, f.create().body, "id")
	path := "/api/v1/quotes/" + id + "/transitions"

	sent := f.do("POST", path, map[string]any{"to": "sent", "revision": 1})
	if sent.status != 200 || str(t, sent.body, "status") != "sent" || sent.body["sent_at"] == nil {
		t.Fatalf("to sent = %d: %s", sent.status, sent.raw)
	}
	if num(t, sent.body, "revision") != 2 || sent.header.Get("ETag") != `"2"` {
		t.Errorf("revision after transition = %v ETag %q", sent.body["revision"], sent.header.Get("ETag"))
	}
	rej := f.do("POST", path, map[string]any{"to": "rejected", "revision": 2})
	if rej.status != 200 || rej.body["rejected_at"] == nil {
		t.Fatalf("to rejected = %d: %s", rej.status, rej.raw)
	}
	reopen := f.do("POST", path, map[string]any{"to": "draft", "revision": 3})
	if reopen.status != 200 || str(t, reopen.body, "status") != "draft" {
		t.Fatalf("reopen = %d: %s", reopen.status, reopen.raw)
	}
	f.do("POST", path, map[string]any{"to": "sent", "revision": 4})
	acc := f.do("POST", path, map[string]any{"to": "accepted", "revision": 5})
	if acc.status != 200 || acc.body["accepted_at"] == nil {
		t.Fatalf("to accepted = %d: %s", acc.status, acc.raw)
	}
	// Accepted is terminal.
	if r := f.do("POST", path, map[string]any{"to": "sent", "revision": 6}); r.status != 409 {
		t.Errorf("transition out of accepted = %d, want 409", r.status)
	}

	want := []string{"quote.created", "quote.sent", "quote.rejected", "quote.reopened", "quote.sent", "quote.accepted"}
	got := eventsFor(t, f.db, id)
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Errorf("events = %v, want %v", got, want)
	}

	// The event carries a small summary and the quote's entity.
	var data []byte
	if err := f.db.Pool.QueryRow(context.Background(),
		`SELECT data FROM events_outbox WHERE entity_id = $1 AND type = 'quote.sent' ORDER BY position LIMIT 1`, id).Scan(&data); err != nil {
		t.Fatal(err)
	}
	var d map[string]any
	_ = json.Unmarshal(data, &d)
	if d["status"] != "sent" || d["from_status"] != "draft" || d["number"] == nil || d["total_cents"] == nil {
		t.Errorf("quote.sent data = %s", data)
	}
}

// convert marks the quote accepted and hands back the order payload, on the
// same revision precondition as any transition.
// RULE (ADR 0005 5.8): the convert accepts the quote and creates the order
// in one transaction, answering 201 with the order and its Location; the
// conversion pair crosses without loss (the R1-15 refusal is lifted); a
// quote that already has an order not cancelled is 409 already_converted.
func TestWire_Convert(t *testing.T) {
	testutil.LockOutboxTables(t)
	f := newFixture(t, testutil.RequireDB(t))
	// 1 PCS at 500.00 per MBF, 187.5 PCS to 1 MBF: the line an order could
	// not carry before cycle 2.
	mbf := f.line("187.5")
	mbf["price_uom"] = "MBF"
	mbf["uom_qty"] = "187.5"
	mbf["price_uom_qty"] = "1"
	mbf["unit_price_ten_thousandths"] = 5000000
	created := f.create(f.line("10"), mbf)
	id := str(t, created.body, "id")

	if r := f.do("POST", "/api/v1/quotes/"+id+"/convert", nil); r.status != 428 {
		t.Errorf("convert with no precondition = %d, want 428", r.status)
	}
	r := f.do("POST", "/api/v1/quotes/"+id+"/convert", nil, "If-Match", `"1"`)
	if r.status != 201 {
		t.Fatalf("convert = %d: %s", r.status, r.raw)
	}
	if loc := r.header.Get("Location"); !strings.HasPrefix(loc, "/api/v1/orders/") {
		t.Errorf("Location = %q, want the created order's path", loc)
	}
	if status := str(t, r.body, "status"); status != "draft" {
		t.Errorf("the created order's status = %q, want draft", status)
	}
	if q := str(t, r.body, "quote_id"); q != id {
		t.Errorf("the order's quote_id = %q, want the converted quote", q)
	}
	lines := r.body["lines"].([]any)
	if len(lines) != 2 {
		t.Fatalf("the order carries %d lines, want the plain and the MBF line", len(lines))
	}
	mbfLine := lines[1].(map[string]any)
	if str(t, mbfLine, "quantity") != "187.5" || str(t, mbfLine, "price_uom") != "MBF" ||
		str(t, mbfLine, "uom_qty") != "187.5" || str(t, mbfLine, "price_uom_qty") != "1" ||
		num(t, mbfLine, "unit_price_ten_thousandths") != 5000000 {
		t.Errorf("the MBF line crossed as %s", r.raw)
	}
	if src := str(t, mbfLine, "price_source"); src != "quote" {
		t.Errorf("price_source = %q, want quote (the price kept exactly)", src)
	}
	if tot := num(t, mbfLine, "line_total_cents"); tot != 50000 {
		t.Errorf("the MBF line's extension = %d, want 50000 (a whole MBF at 500.00)", tot)
	}
	g := f.do("GET", "/api/v1/quotes/"+id, nil)
	if str(t, g.body, "status") != "accepted" {
		t.Errorf("quote status after convert = %v, want accepted", g.body["status"])
	}
	orderID := str(t, r.body, "id")
	quoteEvents := eventsFor(t, f.db, id)
	if fmt.Sprint(quoteEvents) != "[quote.created quote.accepted]" {
		t.Errorf("quote events = %v", quoteEvents)
	}
	orderEvents := eventsForEntity(t, f.db, "order", orderID)
	if fmt.Sprint(orderEvents) != "[order.created]" {
		t.Errorf("order events = %v, want one order.created", orderEvents)
	}
	// Converting twice: the quote already has an order.
	if r := f.do("POST", "/api/v1/quotes/"+id+"/convert", nil, "If-Match", `"2"`); r.status != 409 {
		t.Errorf("second convert = %d, want 409: %s", r.status, r.raw)
	}
}

// RULE (ADR 0005 5.8 and section 1): a stocked line sold in another unit
// than its product's stocking unit is refused with unit_not_stock_unit until
// cycle 3's conversions land, and the refusal leaves the quote exactly as it
// was with no order.
func TestWire_ConvertRefusesANonStockUnit(t *testing.T) {
	testutil.LockOutboxTables(t)
	f := newFixture(t, testutil.RequireDB(t))
	// The fixture's product stocks in PCS; the line is sold by the foot, a
	// sale unit of the set that is not the stocking unit.
	stocked := f.line("8")
	stocked["uom"] = "LF"
	created := f.create(stocked)
	id := str(t, created.body, "id")

	c := f.do("POST", "/api/v1/quotes/"+id+"/convert", nil, "If-Match", `"1"`)
	if c.status != 409 {
		t.Fatalf("convert = %d, want 409: %s", c.status, c.raw)
	}
	code, _, details := errorOf(t, c)
	if code != "conflict" || len(details) != 1 || details[0]["code"] != "unit_not_stock_unit" {
		t.Errorf("code=%q details=%v, want conflict with unit_not_stock_unit", code, details)
	}
	g := f.do("GET", "/api/v1/quotes/"+id, nil)
	if str(t, g.body, "status") != "draft" || num(t, g.body, "revision") != 1 || g.body["accepted_at"] != nil {
		t.Errorf("a refused convert changed the quote: %s", g.raw)
	}
	if n := countOrders(t, f.db, id); n != 0 {
		t.Errorf("%d orders exist for the refused quote", n)
	}
	if got := eventsFor(t, f.db, id); fmt.Sprint(got) != "[quote.created]" {
		t.Errorf("events = %v, want only quote.created", got)
	}
}

func countOrders(t *testing.T, db *database.DB, quoteID string) int {
	t.Helper()
	var n int
	if err := db.Pool.QueryRow(context.Background(),
		`SELECT count(*) FROM orders WHERE quote_id = $1`, quoteID).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

// A line with a 1 to 1 pair in its own unit converts with exact cents: the
// price per sale unit rounded half away from zero, once.
func TestWire_ConvertExactCentsOnOneToOneLines(t *testing.T) {
	testutil.LockOutboxTables(t)
	f := newFixture(t, testutil.RequireDB(t))

	odd := f.line("3")
	odd["unit_price_ten_thousandths"] = 13725 // 1.3725 rounds to 137 cents
	half := f.line("1")
	half["unit_price_ten_thousandths"] = 12350 // 1.2350 rounds half up to 124
	id := str(t, f.create(odd, half).body, "id")

	c := f.do("POST", "/api/v1/quotes/"+id+"/convert", nil, "If-Match", `"1"`)
	if c.status != 201 {
		t.Fatalf("convert = %d: %s", c.status, c.raw)
	}
	// The scale 4 price crosses exactly: the order line keeps 1.3725 and
	// 1.2350 and the extension is rounded once.
	lines := c.body["lines"].([]any)
	if got := num(t, lines[0].(map[string]any), "unit_price_ten_thousandths"); got != 13725 {
		t.Errorf("first line price = %d, want 13725 kept exactly", got)
	}
	if got := num(t, lines[1].(map[string]any), "unit_price_ten_thousandths"); got != 12350 {
		t.Errorf("second line price = %d, want 12350 kept exactly", got)
	}
	if got := num(t, lines[0].(map[string]any), "line_total_cents"); got != 412 {
		t.Errorf("first line extension = %d, want 412 (3 x 1.3725 rounded once)", got)
	}
	if got := num(t, lines[1].(map[string]any), "line_total_cents"); got != 124 {
		t.Errorf("second line extension = %d, want 124 (1.2350 rounded once)", got)
	}
}

// Idempotency rides the existing middleware: the same create sent twice with
// one Idempotency-Key returns the first response and makes one quote and one
// event.
func TestWire_IdempotentCreate(t *testing.T) {
	testutil.LockOutboxTables(t)
	f := newFixture(t, testutil.RequireDB(t))
	key := "wire-" + uuid.NewString()

	body := f.createBody()
	first := f.do("POST", "/api/v1/quotes", body, "Idempotency-Key", key)
	second := f.do("POST", "/api/v1/quotes", body, "Idempotency-Key", key)
	if first.status != 201 || second.status != 201 {
		t.Fatalf("statuses = %d, %d: %s", first.status, second.status, second.raw)
	}
	if !bytes.Equal(first.raw, second.raw) || second.header.Get("Idempotency-Replayed") != "true" {
		t.Errorf("the replay differs from the first response (replayed=%q)", second.header.Get("Idempotency-Replayed"))
	}
	var n int
	if err := f.db.Pool.QueryRow(context.Background(), `SELECT count(*) FROM quotes WHERE customer_id = $1`, f.customerID).Scan(&n); err != nil || n != 1 {
		t.Errorf("%d quotes made (err %v), want 1", n, err)
	}
	if got := eventsFor(t, f.db, str(t, first.body, "id")); len(got) != 1 {
		t.Errorf("events = %v, want one quote.created", got)
	}
	// The same key with a different body is refused, not replayed.
	other := f.createBody(f.line("99"))
	if r := f.do("POST", "/api/v1/quotes", other, "Idempotency-Key", key); r.status != 422 {
		t.Errorf("reused key with another body = %d, want 422", r.status)
	}
}

func TestWire_AnalyticsAreCents(t *testing.T) {
	testutil.LockOutboxTables(t)
	f := newFixture(t, testutil.RequireDB(t))
	f.create()
	r := f.do("GET", "/api/v1/quotes/analytics", nil)
	if r.status != 200 {
		t.Fatalf("analytics = %d: %s", r.status, r.raw)
	}
	for _, k := range []string{"total_quote_value_cents", "total_accepted_value_cents", "avg_margin_accepted_cents", "avg_margin_rejected_cents"} {
		num(t, r.body, k)
	}
	for _, legacy := range []string{"total_quote_value", "total_accepted_value", "avg_margin_accepted"} {
		if _, ok := r.body[legacy]; ok {
			t.Errorf("legacy float money field %s is still on the wire", legacy)
		}
	}
	trend := r.body["trend_data"].([]any)
	if len(trend) == 0 {
		t.Fatal("no trend data")
	}
	num(t, trend[0].(map[string]any), "total_value_cents")
}
