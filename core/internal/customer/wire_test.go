// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package customer_test

// The customer module on the wire contract (ADR 0001, ADR 0005 section 7),
// tested end to end: a real Postgres, the real handler on a real mux behind
// the real idempotency layer, requests as JSON and responses read back as
// JSON. Nothing here touches a customer type, so each test states a wire fact.
// The requirement tags are the inputs document's, as ADR 0005 section 14.1
// names them for this item: IN-1.4 (one list envelope), IN-2.5 (customer.updated
// on a credit limit change), IN-3.2 (filters filter, unknown ones are a 400),
// IN-3.7 (one error envelope), IN-5 (stable record routes).

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

	"github.com/gablelbm/gable/internal/customer"
	"github.com/gablelbm/gable/internal/platform/httpx"
	"github.com/gablelbm/gable/internal/testutil"
	"github.com/gablelbm/gable/pkg/branchctx"
	"github.com/gablelbm/gable/pkg/database"
	"github.com/gablelbm/gable/pkg/middleware"
	"github.com/gablelbm/gable/pkg/outbox"
	"github.com/google/uuid"
)

type fixture struct {
	t      *testing.T
	db     *database.DB
	srv    *httptest.Server
	prefix string // every account number this test makes starts with it
	branch uuid.UUID
}

// newServer builds the module the way serve does (repository, service with the
// outbox and the database as transaction runner, handler) behind the global
// idempotency layer. A request header X-Test-Branch puts a branch wall on the
// request's context, as the branch middleware does.
func newFixture(t *testing.T, db *database.DB) *fixture {
	t.Helper()
	t.Setenv("AUTH_MODE", "dev")
	f := &fixture{t: t, db: db, prefix: "WIRE-" + uuid.NewString()[:8] + "-"}
	if err := db.Pool.QueryRow(context.Background(),
		`SELECT value::uuid FROM system_settings WHERE key = 'default_branch_id'`).Scan(&f.branch); err != nil {
		t.Fatal(err)
	}

	svc := customer.NewService(customer.NewRepository(db)).WithOutbox(outbox.NewWriter(db, "")).WithTxRunner(db)
	mux := http.NewServeMux()
	customer.NewHandler(svc).RegisterRoutes(mux)
	wallMux := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if b := r.Header.Get("X-Test-Branch"); b != "" {
			id := uuid.MustParse(b)
			r = r.WithContext(branchctx.With(r.Context(), &branchctx.Context{BranchID: &id}))
		}
		mux.ServeHTTP(w, r)
	})
	f.srv = httptest.NewServer(middleware.Idempotency(db)(wallMux))

	t.Cleanup(func() {
		f.srv.Close()
		ctx := context.Background()
		ids := `SELECT id FROM customers WHERE account_number LIKE $1`
		pat := f.prefix + "%"
		_, _ = db.Pool.Exec(ctx, `DELETE FROM events_outbox WHERE entity_type = 'customer' AND entity_id IN (`+ids+`)`, pat)
		_, _ = db.Pool.Exec(ctx, `DELETE FROM orders WHERE customer_id IN (`+ids+`)`, pat)
		_, _ = db.Pool.Exec(ctx, `DELETE FROM customer_ship_tos WHERE customer_id IN (`+ids+`)`, pat)
		_, _ = db.Pool.Exec(ctx, `DELETE FROM customer_contacts WHERE customer_id IN (`+ids+`)`, pat)
		_, _ = db.Pool.Exec(ctx, `DELETE FROM customer_branches WHERE customer_id IN (`+ids+`)`, pat)
		_, _ = db.Pool.Exec(ctx, `DELETE FROM customers WHERE account_number LIKE $1`, pat)
		_, _ = db.Pool.Exec(ctx, `DELETE FROM payment_terms WHERE code LIKE $1`, strings.ToUpper(f.prefix)+"%")
		_, _ = db.Pool.Exec(ctx, `DELETE FROM sales_team WHERE name LIKE $1`, f.prefix+"%")
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

// fields lists the field of each error detail.
func fields(details []map[string]any) []string {
	var out []string
	for _, d := range details {
		out = append(out, fmt.Sprint(d["field"]))
	}
	return out
}

func hasField(details []map[string]any, field string) bool {
	for _, d := range details {
		if d["field"] == field {
			return true
		}
	}
	return false
}

func hasBlocker(details []map[string]any, code string) bool {
	for _, d := range details {
		if d["code"] == code {
			return true
		}
	}
	return false
}

// events returns the events of the entity, oldest first, as type and data.
func (f *fixture) events(entityID string) (types []string, data []map[string]any) {
	f.t.Helper()
	rows, err := f.db.Pool.Query(context.Background(),
		`SELECT type, data FROM events_outbox WHERE entity_type = 'customer' AND entity_id = $1 ORDER BY position`, entityID)
	if err != nil {
		f.t.Fatal(err)
	}
	defer rows.Close()
	for rows.Next() {
		var ty string
		var raw []byte
		if err := rows.Scan(&ty, &raw); err != nil {
			f.t.Fatal(err)
		}
		var d map[string]any
		dec := json.NewDecoder(bytes.NewReader(raw))
		dec.UseNumber()
		if err := dec.Decode(&d); err != nil {
			f.t.Fatal(err)
		}
		types, data = append(types, ty), append(data, d)
	}
	return types, data
}

var seq int

// account is a unique account number of this fixture.
func (f *fixture) account() string {
	seq++
	return fmt.Sprintf("%s%03d", f.prefix, seq)
}

func (f *fixture) body(extra map[string]any) map[string]any {
	b := map[string]any{"account_number": f.account(), "name": "Wire Test Co"}
	for k, v := range extra {
		b[k] = v
	}
	return b
}

func (f *fixture) create(extra ...map[string]any) resp {
	f.t.Helper()
	var e map[string]any
	if len(extra) > 0 {
		e = extra[0]
	}
	r := f.do("POST", "/api/v1/customers", f.body(e))
	if r.status != http.StatusCreated {
		f.t.Fatalf("create = %d: %s", r.status, r.raw)
	}
	return r
}

func ifMatch(rev int64) string { return fmt.Sprintf(`"%d"`, rev) }

func (f *fixture) termsID(code string) string {
	f.t.Helper()
	var id string
	if err := f.db.Pool.QueryRow(context.Background(), `SELECT id::text FROM payment_terms WHERE code = $1`, code).Scan(&id); err != nil {
		f.t.Fatal(err)
	}
	return id
}

// RULE (ADR 0001 sections 6, 7, 11, 12; ADR 0005 7.4): a created customer
// carries a lowercase tier, integer _cents money, null for what is unset, its
// terms, a revision and its ETag, and the create writes customer.created once.
// IN-5: the record route is /api/v1/customers/{id}, as before.
func TestWire_CreateShape(t *testing.T) {
	testutil.LockOutboxTables(t)
	f := newFixture(t, testutil.RequireDB(t))

	r := f.create(map[string]any{"email": "buyer@example.com", "tier": "gold"})
	c := r.body
	if str(t, c, "tier") != "gold" {
		t.Errorf("tier = %v, want gold (lowercase)", c["tier"])
	}
	if num(t, c, "revision") != 1 || r.header.Get("ETag") != `"1"` {
		t.Errorf("revision = %v, ETag = %q, want 1 and \"1\"", c["revision"], r.header.Get("ETag"))
	}
	if !strings.HasPrefix(r.header.Get("Location"), "/api/v1/customers/") {
		t.Errorf("Location = %q", r.header.Get("Location"))
	}
	if num(t, c, "balance_cents") != 0 {
		t.Errorf("balance_cents = %v, want 0", c["balance_cents"])
	}
	for _, null := range []string{"phone", "address", "price_level_id", "price_level", "salesperson_id", "salesperson_name", "credit_limit_cents", "currency"} {
		if v, ok := c[null]; !ok || v != nil {
			t.Errorf("%s = %v (present %v), want present as null", null, v, ok)
		}
	}
	for _, legacy := range []string{"credit_limit", "balance_due", "payment_terms_code"} {
		if _, ok := c[legacy]; ok {
			t.Errorf("legacy field %s is still on the wire", legacy)
		}
	}
	if str(t, c, "effective_currency") != "USD" {
		t.Errorf("effective_currency = %v, want the dealer default USD", c["effective_currency"])
	}
	if c["po_required"] != false || c["is_active"] != true {
		t.Errorf("po_required = %v, is_active = %v, want false and true", c["po_required"], c["is_active"])
	}
	terms := c["payment_terms"].(map[string]any)
	if str(t, terms, "code") != "NET30" || str(t, c, "payment_terms_id") != str(t, terms, "id") {
		t.Errorf("payment_terms = %v, want the default NET30 row", terms)
	}
	if !regexp.MustCompile(`\.\d{6}Z$`).MatchString(str(t, c, "created_at")) {
		t.Errorf("created_at = %v, want RFC 3339 UTC at microsecond precision", c["created_at"])
	}

	id := str(t, c, "id")
	types, data := f.events(id)
	if len(types) != 1 || types[0] != "customer.created" {
		t.Fatalf("events = %v, want exactly [customer.created]", types)
	}
	if data[0]["number"] != c["account_number"] || data[0]["revision"] == nil {
		t.Errorf("event data = %v", data[0])
	}

	g := f.do("GET", "/api/v1/customers/"+id, nil)
	if g.status != 200 || g.header.Get("ETag") != `"1"` || str(t, g.body, "account_number") != str(t, c, "account_number") {
		t.Errorf("GET = %d ETag=%q", g.status, g.header.Get("ETag"))
	}
}

// Validation collects every offending field into one 400 with its path, a
// body the route cannot consume is bad_request, and an unknown field is
// refused (the legacy float fields included).
func TestWire_CreateValidation(t *testing.T) {
	testutil.LockOutboxTables(t)
	f := newFixture(t, testutil.RequireDB(t))

	r := f.do("POST", "/api/v1/customers", map[string]any{
		"tier": "GOLD", "email": "not an email", "credit_limit_cents": -5, "currency": "usd",
		"price_level_id": "nope", "payment_terms_id": uuid.NewString(),
	})
	if r.status != 400 {
		t.Fatalf("status = %d: %s", r.status, r.raw)
	}
	code, _, details := errorOf(t, r)
	if code != "validation_failed" {
		t.Errorf("code = %q, want validation_failed", code)
	}
	for _, want := range []string{"account_number", "name", "tier", "email", "credit_limit_cents", "currency", "price_level_id"} {
		if !hasField(details, want) {
			t.Errorf("no detail names %s: %v", want, fields(details))
		}
	}

	// A well formed reference to nothing is a 400 naming the field, never a 500.
	r = f.do("POST", "/api/v1/customers", f.body(map[string]any{"payment_terms_id": uuid.NewString()}))
	if _, _, d := errorOf(t, r); r.status != 400 || !hasField(d, "payment_terms_id") {
		t.Errorf("unknown payment_terms_id = %d: %s", r.status, r.raw)
	}
	r = f.do("POST", "/api/v1/customers", f.body(map[string]any{"salesperson_id": uuid.NewString()}))
	if _, _, d := errorOf(t, r); r.status != 400 || !hasField(d, "salesperson_id") {
		t.Errorf("unknown salesperson_id = %d: %s", r.status, r.raw)
	}
	r = f.do("POST", "/api/v1/customers", f.body(map[string]any{"price_level_id": uuid.NewString()}))
	if _, _, d := errorOf(t, r); r.status != 400 || !hasField(d, "price_level_id") {
		t.Errorf("unknown price_level_id = %d: %s", r.status, r.raw)
	}

	for name, body := range map[string]any{
		"unknown field":      f.body(map[string]any{"balance_due": 5}),
		"legacy credit":      f.body(map[string]any{"credit_limit": 100.5}),
		"revision on create": f.body(map[string]any{"revision": 1}),
		"not json":           "{nope",
		"float cents":        f.body(map[string]any{"credit_limit_cents": 12.5}),
	} {
		r := f.do("POST", "/api/v1/customers", body)
		if r.status != 400 {
			t.Errorf("%s = %d, want 400: %s", name, r.status, r.raw)
			continue
		}
		errorOf(t, r)
	}

	// A currency the dealer has not enabled is refused, one it has is taken.
	if r := f.do("POST", "/api/v1/customers", f.body(map[string]any{"currency": "EUR"})); r.status != 400 {
		t.Errorf("an unenabled currency = %d, want 400: %s", r.status, r.raw)
	}
	if r := f.do("POST", "/api/v1/customers", f.body(map[string]any{"currency": "USD"})); r.status != 201 || r.body["currency"] != "USD" {
		t.Errorf("the enabled currency = %d: %s", r.status, r.raw)
	}

	// A duplicate account number is a 409 duplicate, not a 500.
	first := f.create()
	dup := f.do("POST", "/api/v1/customers", map[string]any{"account_number": first.body["account_number"], "name": "Again"})
	if dup.status != 409 {
		t.Fatalf("duplicate account number = %d: %s", dup.status, dup.raw)
	}
	if code, _, d := errorOf(t, dup); code != "duplicate" || !hasBlocker(d, "account_number_taken") {
		t.Errorf("duplicate = %s %v", code, d)
	}
}

// IN-1.4, IN-3.2: one list envelope, filters that filter, unknown or
// uppercase values and unknown parameters refused, the cursor walks every row
// once, and an empty page is [] in the bytes.
func TestWire_ListEnvelopeFiltersAndCursor(t *testing.T) {
	testutil.LockOutboxTables(t)
	f := newFixture(t, testutil.RequireDB(t))
	q := "&q=" + f.prefix

	want := map[string]bool{}
	for i := 0; i < 5; i++ {
		extra := map[string]any{"tier": "retail"}
		if i%2 == 0 {
			extra["tier"] = "silver"
		}
		if i == 4 {
			extra["is_active"] = false
		}
		want[str(t, f.create(extra).body, "id")] = true
	}

	seen := map[string]bool{}
	path := "/api/v1/customers?limit=2" + q
	pages := 0
	for path != "" {
		r := f.do("GET", path, nil)
		if r.status != 200 {
			t.Fatalf("%s = %d: %s", path, r.status, r.raw)
		}
		if _, hasTotal := r.body["total"]; hasTotal {
			t.Error("total present without ?include=total")
		}
		items := r.body["items"].([]any)
		if len(items) > 2 || num(t, r.body, "limit") != 2 {
			t.Fatalf("page of %d items under limit=2", len(items))
		}
		for _, it := range items {
			id := str(t, it.(map[string]any), "id")
			if seen[id] {
				t.Errorf("customer %s served twice", id)
			}
			seen[id] = true
		}
		pages++
		if next, ok := r.body["next_cursor"].(string); ok {
			path = "/api/v1/customers?limit=2&cursor=" + next + q
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
	if len(seen) != len(want) || pages != 3 {
		t.Errorf("walked %d customers in %d pages, want 5 in 3", len(seen), pages)
	}

	if r := f.do("GET", "/api/v1/customers?include=total&limit=1"+q, nil); num(t, r.body, "total") != 5 {
		t.Errorf("total = %v, want 5", r.body["total"])
	}

	// The filters filter.
	count := func(query string) int {
		t.Helper()
		r := f.do("GET", "/api/v1/customers?limit=200"+q+query, nil)
		if r.status != 200 {
			t.Fatalf("%s = %d: %s", query, r.status, r.raw)
		}
		return len(r.body["items"].([]any))
	}
	if n := count("&tier=silver"); n != 3 {
		t.Errorf("tier=silver = %d customers, want 3", n)
	}
	if n := count("&tier=retail"); n != 2 {
		t.Errorf("tier=retail = %d customers, want 2", n)
	}
	if n := count("&is_active=false"); n != 1 {
		t.Errorf("is_active=false = %d customers, want 1", n)
	}
	if n := count("&is_active=true"); n != 4 {
		t.Errorf("is_active=true = %d customers, want 4", n)
	}
	if n := count("&tier=gold"); n != 0 {
		t.Errorf("tier=gold = %d customers, want 0", n)
	}

	// A salesperson filter.
	var spID string
	if err := f.db.Pool.QueryRow(context.Background(),
		`INSERT INTO sales_team (name, email) VALUES ($1, $2) RETURNING id::text`, f.prefix+"rep", f.prefix+"rep@example.com").Scan(&spID); err != nil {
		t.Fatal(err)
	}
	one := str(t, f.create(map[string]any{"salesperson_id": spID}).body, "id")
	r := f.do("GET", "/api/v1/customers?salesperson_id="+spID, nil)
	if items := r.body["items"].([]any); len(items) != 1 || str(t, items[0].(map[string]any), "id") != one {
		t.Errorf("salesperson_id filter = %s", r.raw)
	}

	// The search term matches name and account number, case blind, and a wildcard is a literal.
	if r := f.do("GET", "/api/v1/customers?limit=200&q="+strings.ToLower(f.prefix), nil); len(r.body["items"].([]any)) != 6 {
		t.Errorf("a lowercase q served %d customers, want 6 (case blind)", len(r.body["items"].([]any)))
	}
	if r := f.do("GET", "/api/v1/customers?q=%25", nil); len(r.body["items"].([]any)) != 0 {
		t.Errorf("q=%% matched rows: it must search for a literal percent sign")
	}

	// An empty page is [], never null.
	empty := f.do("GET", "/api/v1/customers?q="+f.prefix+"nobody", nil)
	if !bytes.Contains(empty.raw, []byte(`"items":[]`)) {
		t.Errorf("empty page body %s does not carry items as []", empty.raw)
	}

	// Refused: an unknown parameter, the legacy offset, an uppercase or unknown value, bad paging.
	for _, bad := range []string{
		"nope=1", "offset=0", "tier=GOLD", "tier=platinumm", "is_active=yes", "salesperson_id=x",
		"limit=0", "limit=201", "limit=abc", "cursor=garbage", "include=nope", "q=" + strings.Repeat("x", 101),
	} {
		r := f.do("GET", "/api/v1/customers?"+bad, nil)
		if r.status != 400 {
			t.Errorf("?%s = %d, want 400: %s", bad, r.status, r.raw)
			continue
		}
		errorOf(t, r)
	}
	if r := f.do("GET", "/api/v1/customers?offset=0", nil); true {
		if code, _, _ := errorOf(t, r); code != "unsupported_query_parameter" {
			t.Errorf("offset code = %q, want unsupported_query_parameter", code)
		}
	}
}

// IN-3.7: every refusal is the one envelope with the right status and the
// handler's own message.
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
		{"unknown customer", "GET", "/api/v1/customers/" + uuid.NewString(), nil, nil, 404, "not_found", "customer not found"},
		{"malformed id", "GET", "/api/v1/customers/not-a-uuid", nil, nil, 400, "bad_request", "invalid customer id"},
		{"unknown contact", "GET", "/api/v1/contacts/" + uuid.NewString(), nil, nil, 404, "not_found", "contact not found"},
		{"unknown ship-to", "GET", "/api/v1/ship-tos/" + uuid.NewString(), nil, nil, 404, "not_found", "ship-to not found"},
		{"unknown terms", "GET", "/api/v1/payment-terms/" + uuid.NewString(), nil, nil, 404, "not_found", "payment terms not found"},
		{"ship-tos of an unknown customer", "GET", "/api/v1/customers/" + uuid.NewString() + "/ship-tos", nil, nil, 404, "not_found", "customer not found"},
		{"contacts of an unknown customer", "GET", "/api/v1/customers/" + uuid.NewString() + "/contacts", nil, nil, 404, "not_found", "customer not found"},
		{"edit without a revision", "PUT", "/api/v1/customers/" + id, map[string]any{"account_number": "X", "name": "Y"}, nil, 428, "precondition_required", "this write needs If-Match or a body revision"},
		{"edit an unknown customer", "PUT", "/api/v1/customers/" + uuid.NewString(), map[string]any{"account_number": "X", "name": "Y", "revision": 1}, nil, 404, "not_found", "customer not found"},
		{"a query on a route with none", "GET", "/api/v1/customers/" + id + "?x=1", nil, nil, 400, "unsupported_query_parameter", ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r := f.do(c.method, c.path, c.body, c.headers...)
			if r.status != c.status {
				t.Fatalf("status = %d, want %d: %s", r.status, c.status, r.raw)
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

// RULE (ADR 0001 section 11): an edit needs the client's revision (If-Match
// or the body), a mismatch is 409 stale_revision, a good write returns the new
// revision and ETag, and header and body that disagree are a 400.
func TestWire_RevisionPreconditions(t *testing.T) {
	testutil.LockOutboxTables(t)
	f := newFixture(t, testutil.RequireDB(t))
	created := f.create().body
	id, acct := str(t, created, "id"), str(t, created, "account_number")
	path := "/api/v1/customers/" + id
	edit := func(name string) map[string]any { return map[string]any{"account_number": acct, "name": name} }

	if r := f.do("PUT", path, edit("A")); r.status != 428 {
		t.Errorf("PUT with no precondition = %d, want 428: %s", r.status, r.raw)
	}
	if r := f.do("PUT", path, edit("A"), "If-Match", `"9"`); r.status != 409 {
		t.Errorf("PUT with a stale If-Match = %d, want 409", r.status)
	} else if code, _, _ := errorOf(t, r); code != "stale_revision" {
		t.Errorf("code = %q, want stale_revision", code)
	}
	if r := f.do("PUT", path, edit("A"), "If-Match", "*"); r.status != 400 {
		t.Errorf("PUT with If-Match * = %d, want 400", r.status)
	}
	ok := f.do("PUT", path, edit("A"), "If-Match", `"1"`)
	if ok.status != 200 || num(t, ok.body, "revision") != 2 || ok.header.Get("ETag") != `"2"` || str(t, ok.body, "name") != "A" {
		t.Fatalf("PUT = %d: %s (ETag %q)", ok.status, ok.raw, ok.header.Get("ETag"))
	}
	if r := f.do("PUT", path, edit("B"), "If-Match", `"1"`); r.status != 409 {
		t.Errorf("replayed stale PUT = %d, want 409", r.status)
	}
	body := edit("C")
	body["revision"] = 2
	if r := f.do("PUT", path, body); r.status != 200 || num(t, r.body, "revision") != 3 {
		t.Errorf("PUT with a body revision = %d: %s", r.status, r.raw)
	}
	if r := f.do("PUT", path, edit("D"), "If-Match", `W/"3"`); r.status != 200 || num(t, r.body, "revision") != 4 {
		t.Errorf("PUT with a weak If-Match = %d: %s", r.status, r.raw)
	}
	body = edit("E")
	body["revision"] = 4
	if r := f.do("PUT", path, body, "If-Match", `"3"`); r.status != 400 {
		t.Errorf("disagreeing precondition = %d, want 400", r.status)
	}
	// A PUT cannot move the customer's branch.
	body = edit("F")
	body["primary_branch_id"] = uuid.NewString()
	if r := f.do("PUT", path, body, "If-Match", `"4"`); r.status != 400 {
		t.Errorf("PUT with primary_branch_id = %d, want 400", r.status)
	}
	// A PUT replaces the header: what it leaves out takes its default.
	if r := f.do("PUT", path, map[string]any{"account_number": acct, "name": "G", "tier": "gold", "po_required": true}, "If-Match", `"4"`); r.status != 200 {
		t.Fatalf("PUT = %d: %s", r.status, r.raw)
	}
	r := f.do("PUT", path, edit("H"), "If-Match", `"5"`)
	if r.body["tier"] != "retail" || r.body["po_required"] != false {
		t.Errorf("a PUT that leaves tier and po_required out gave tier=%v po_required=%v, want their defaults", r.body["tier"], r.body["po_required"])
	}
}

// IN-2.5 and the credit limit rule: null is no limit, zero is a limit of
// nothing, and the two are different on the wire and in storage.
func TestWire_CreditLimitNullVersusZero(t *testing.T) {
	testutil.LockOutboxTables(t)
	f := newFixture(t, testutil.RequireDB(t))
	ctx := context.Background()

	none := f.create().body
	zero := f.create(map[string]any{"credit_limit_cents": 0}).body
	some := f.create(map[string]any{"credit_limit_cents": 123456}).body
	if none["credit_limit_cents"] != nil {
		t.Errorf("no limit given: credit_limit_cents = %v, want null", none["credit_limit_cents"])
	}
	if num(t, zero, "credit_limit_cents") != 0 {
		t.Errorf("a limit of 0: credit_limit_cents = %v, want 0", zero["credit_limit_cents"])
	}
	if num(t, some, "credit_limit_cents") != 123456 {
		t.Errorf("credit_limit_cents = %v, want 123456", some["credit_limit_cents"])
	}
	for id, wantNull := range map[string]bool{str(t, none, "id"): true, str(t, zero, "id"): false} {
		var isNull bool
		if err := f.db.Pool.QueryRow(ctx, `SELECT credit_limit IS NULL FROM customers WHERE id = $1`, id).Scan(&isNull); err != nil || isNull != wantNull {
			t.Errorf("stored credit_limit IS NULL = %v (%v), want %v", isNull, err, wantNull)
		}
	}

	// An edit from a limit to none, and back, moves the wire value and writes
	// customer.updated with the new limit (IN-2.5).
	id, acct := str(t, some, "id"), str(t, some, "account_number")
	r := f.do("PUT", "/api/v1/customers/"+id, map[string]any{"account_number": acct, "name": "Wire Test Co", "credit_limit_cents": nil}, "If-Match", `"1"`)
	if r.status != 200 || r.body["credit_limit_cents"] != nil {
		t.Fatalf("clearing the limit = %d: %s", r.status, r.raw)
	}
	r = f.do("PUT", "/api/v1/customers/"+id, map[string]any{"account_number": acct, "name": "Wire Test Co", "credit_limit_cents": 5000}, "If-Match", `"2"`)
	if r.status != 200 || num(t, r.body, "credit_limit_cents") != 5000 {
		t.Fatalf("setting the limit = %d: %s", r.status, r.raw)
	}
	types, data := f.events(id)
	if len(types) != 3 || types[1] != "customer.updated" || types[2] != "customer.updated" {
		t.Fatalf("events = %v, want created then two updated", types)
	}
	if data[1]["credit_limit_cents"] != nil {
		t.Errorf("event 2 credit_limit_cents = %v, want null", data[1]["credit_limit_cents"])
	}
	if n, _ := data[2]["credit_limit_cents"].(json.Number); n.String() != "5000" {
		t.Errorf("event 3 credit_limit_cents = %v, want 5000", data[2]["credit_limit_cents"])
	}
	if data[2]["part"] != "header" || !containsStr(data[2]["changed"], "credit_limit_cents") {
		t.Errorf("event 3 part=%v changed=%v, want header naming credit_limit_cents", data[2]["part"], data[2]["changed"])
	}
	// A negative limit is refused.
	if r := f.do("PUT", "/api/v1/customers/"+id, map[string]any{"account_number": acct, "name": "Wire Test Co", "credit_limit_cents": -1}, "If-Match", `"3"`); r.status != 400 {
		t.Errorf("negative credit limit = %d, want 400", r.status)
	}
}

func containsStr(v any, want string) bool {
	list, _ := v.([]any)
	for _, x := range list {
		if x == want {
			return true
		}
	}
	return false
}

// ADR 0005 4.2: the currency override cannot change while the customer has an
// open document, and can once it has none. 409 conflict, blocker open_documents.
func TestWire_CurrencyChangeRefusedWithAnOpenOrder(t *testing.T) {
	testutil.LockOutboxTables(t)
	f := newFixture(t, testutil.RequireDB(t))
	ctx := context.Background()
	c := f.create().body
	id, acct := str(t, c, "id"), str(t, c, "account_number")
	put := func(rev int64, currency any) resp {
		return f.do("PUT", "/api/v1/customers/"+id, map[string]any{"account_number": acct, "name": "Wire Test Co", "currency": currency}, "If-Match", ifMatch(rev))
	}

	var orderID uuid.UUID
	if err := f.db.Pool.QueryRow(ctx, `INSERT INTO orders (customer_id, status, total_amount, branch_id)
		VALUES ($1, 'CONFIRMED', 10, (SELECT value::uuid FROM system_settings WHERE key = 'default_branch_id')) RETURNING id`, id).Scan(&orderID); err != nil {
		t.Fatal(err)
	}
	r := put(1, "USD")
	if r.status != 409 {
		t.Fatalf("changing the currency with an open order = %d, want 409: %s", r.status, r.raw)
	}
	if code, _, d := errorOf(t, r); code != "conflict" || !hasBlocker(d, "open_documents") {
		t.Errorf("refusal = %s %v, want conflict with blocker open_documents", code, d)
	}
	if got := f.do("GET", "/api/v1/customers/"+id, nil); got.body["currency"] != nil || num(t, got.body, "revision") != 1 {
		t.Errorf("a refused change moved the customer: %s", got.raw)
	}

	// A fulfilled order no longer holds the currency.
	if _, err := f.db.Pool.Exec(ctx, `UPDATE orders SET status = 'FULFILLED' WHERE id = $1`, orderID); err != nil {
		t.Fatal(err)
	}
	if r := put(1, "USD"); r.status != 200 || r.body["currency"] != "USD" {
		t.Fatalf("changing the currency with none open = %d: %s", r.status, r.raw)
	}
	// An open invoice holds it again; so does an unchanged value not.
	if _, err := f.db.Pool.Exec(ctx, `INSERT INTO invoices (order_id, customer_id, status, total_amount, subtotal, tax_amount, branch_id)
		VALUES ($1, $2, 'UNPAID', 10, 10, 0, (SELECT value::uuid FROM system_settings WHERE key = 'default_branch_id'))`, orderID, id); err != nil {
		t.Fatal(err)
	}
	if r := put(2, nil); r.status != 409 {
		t.Errorf("clearing the override with an open invoice = %d, want 409", r.status)
	}
	if r := put(2, "USD"); r.status != 200 {
		t.Errorf("an edit that keeps the override with an open invoice = %d, want 200: %s", r.status, r.raw)
	}
	_, _ = f.db.Pool.Exec(ctx, `DELETE FROM invoices WHERE customer_id = $1`, id)
}

// ADR 0005 4.2: the enabled list holds one code until the ledger groups by
// currency, whoever writes the setting.
func TestCurrencySettingsRefuseASecondEnabledCode(t *testing.T) {
	db := testutil.RequireDB(t)
	ctx := context.Background()
	for _, bad := range []string{"USD,EUR", "usd", "", "US"} {
		if _, err := db.Pool.Exec(ctx, `UPDATE system_settings SET value = $1 WHERE key = 'currency.enabled'`, bad); err == nil {
			t.Errorf("currency.enabled = %q was accepted", bad)
			_, _ = db.Pool.Exec(ctx, `UPDATE system_settings SET value = 'USD' WHERE key = 'currency.enabled'`)
		}
	}
	if _, err := db.Pool.Exec(ctx, `UPDATE system_settings SET value = 'eur' WHERE key = 'currency.default'`); err == nil {
		t.Error("currency.default = eur was accepted")
		_, _ = db.Pool.Exec(ctx, `UPDATE system_settings SET value = 'USD' WHERE key = 'currency.default'`)
	}
	var enabled string
	if err := db.Pool.QueryRow(ctx, `SELECT value FROM system_settings WHERE key = 'currency.enabled'`).Scan(&enabled); err != nil || enabled != "USD" {
		t.Errorf("currency.enabled = %q (%v), want USD", enabled, err)
	}
}

// The PO required flag is stored and read, on create and on edit.
func TestWire_POrequiredFlag(t *testing.T) {
	testutil.LockOutboxTables(t)
	f := newFixture(t, testutil.RequireDB(t))
	c := f.create(map[string]any{"po_required": true}).body
	if c["po_required"] != true {
		t.Errorf("po_required = %v, want true", c["po_required"])
	}
	id, acct := str(t, c, "id"), str(t, c, "account_number")
	r := f.do("PUT", "/api/v1/customers/"+id, map[string]any{"account_number": acct, "name": "Wire Test Co", "po_required": false}, "If-Match", `"1"`)
	if r.status != 200 || r.body["po_required"] != false {
		t.Errorf("clearing po_required = %d: %s", r.status, r.raw)
	}
	var stored bool
	if err := f.db.Pool.QueryRow(context.Background(), `SELECT po_required FROM customers WHERE id = $1`, id).Scan(&stored); err != nil || stored {
		t.Errorf("stored po_required = %v (%v), want false", stored, err)
	}
}

// Payment terms: assigning a customer's terms, the terms event part, and an
// inactive row that cannot be newly assigned.
func TestWire_CustomerTerms(t *testing.T) {
	testutil.LockOutboxTables(t)
	f := newFixture(t, testutil.RequireDB(t))
	net60 := f.termsID("NET60")
	c := f.create(map[string]any{"payment_terms_id": net60}).body
	if c["payment_terms"].(map[string]any)["code"] != "NET60" {
		t.Errorf("payment_terms = %v, want NET60", c["payment_terms"])
	}
	id, acct := str(t, c, "id"), str(t, c, "account_number")

	// Changing only the terms is a customer.updated of part terms.
	r := f.do("PUT", "/api/v1/customers/"+id, map[string]any{"account_number": acct, "name": "Wire Test Co", "payment_terms_id": f.termsID("COD")}, "If-Match", `"1"`)
	if r.status != 200 || r.body["payment_terms"].(map[string]any)["code"] != "COD" {
		t.Fatalf("changing the terms = %d: %s", r.status, r.raw)
	}
	types, data := f.events(id)
	if len(types) != 2 || data[1]["part"] != "terms" {
		t.Errorf("events = %v %v, want an update of part terms", types, data)
	}

	// Terms that are switched off cannot be newly assigned, but a customer that holds them keeps them.
	term := f.do("POST", "/api/v1/payment-terms", map[string]any{"code": strings.ToUpper(f.prefix) + "OFF", "name": "Off", "kind": "net_days", "net_days": 10})
	if term.status != 201 {
		t.Fatalf("create terms = %d: %s", term.status, term.raw)
	}
	termID := str(t, term.body, "id")
	holder := f.create(map[string]any{"payment_terms_id": termID}).body
	off := f.do("PUT", "/api/v1/payment-terms/"+termID, map[string]any{"code": term.body["code"], "name": "Off", "kind": "net_days", "net_days": 10, "is_active": false}, "If-Match", `"1"`)
	if off.status != 200 {
		t.Fatalf("deactivate = %d: %s", off.status, off.raw)
	}
	if r := f.do("POST", "/api/v1/customers", f.body(map[string]any{"payment_terms_id": termID})); r.status != 400 || !hasField(detailsOf(t, r), "payment_terms_id") {
		t.Errorf("assigning inactive terms = %d: %s", r.status, r.raw)
	}
	keep := f.do("PUT", "/api/v1/customers/"+str(t, holder, "id"), map[string]any{"account_number": holder["account_number"], "name": "Kept", "payment_terms_id": termID}, "If-Match", `"1"`)
	if keep.status != 200 {
		t.Errorf("a customer keeping its now inactive terms = %d: %s", keep.status, keep.raw)
	}
}

func detailsOf(t *testing.T, r resp) []map[string]any {
	t.Helper()
	_, _, d := errorOf(t, r)
	return d
}

// The payment terms master on the wire: the seeded rows, create and edit per
// kind, validation, the duplicate code, deactivation, and the filters.
func TestWire_PaymentTermsMaster(t *testing.T) {
	testutil.LockOutboxTables(t)
	f := newFixture(t, testutil.RequireDB(t))
	code := func(s string) string { return strings.ToUpper(f.prefix) + s }

	seeded := f.do("GET", "/api/v1/payment-terms?limit=200", nil)
	have := map[string]bool{}
	for _, it := range seeded.body["items"].([]any) {
		have[str(t, it.(map[string]any), "code")] = true
	}
	for _, c := range []string{"NET30", "NET60", "NET90", "DUE_ON_RECEIPT", "COD"} {
		if !have[c] {
			t.Errorf("the seeded terms %s are missing", c)
		}
	}

	create := func(body map[string]any) resp { return f.do("POST", "/api/v1/payment-terms", body) }
	net := create(map[string]any{"code": code("N15"), "name": "Net 15 two ten", "kind": "net_days", "net_days": 15, "discount_percent": "2", "discount_days": 10})
	if net.status != 201 || net.header.Get("ETag") != `"1"` || !strings.HasPrefix(net.header.Get("Location"), "/api/v1/payment-terms/") {
		t.Fatalf("create net_days = %d: %s", net.status, net.raw)
	}
	if net.body["kind"] != "net_days" || net.body["discount_percent"] != "2" || num(t, net.body, "discount_days") != 10 || net.body["day_of_month"] != nil {
		t.Errorf("net_days terms = %s", net.raw)
	}
	dom := create(map[string]any{"code": code("D25"), "name": "25th of next month", "kind": "day_of_month", "day_of_month": 25})
	if dom.status != 201 || num(t, dom.body, "day_of_month") != 25 || dom.body["net_days"] != nil || dom.body["discount_percent"] != nil {
		t.Errorf("day_of_month terms = %d: %s", dom.status, dom.raw)
	}
	if r := create(map[string]any{"code": code("DUE"), "name": "On receipt", "kind": "due_on_receipt"}); r.status != 201 {
		t.Errorf("due_on_receipt terms = %d: %s", r.status, r.raw)
	}

	for name, c := range map[string]struct {
		body  map[string]any
		field string
	}{
		"net_days needs net_days":        {map[string]any{"code": code("V1"), "name": "x", "kind": "net_days"}, "net_days"},
		"day_of_month needs a day":       {map[string]any{"code": code("V2"), "name": "x", "kind": "day_of_month"}, "day_of_month"},
		"day 32":                         {map[string]any{"code": code("V3"), "name": "x", "kind": "day_of_month", "day_of_month": 32}, "day_of_month"},
		"day 0":                          {map[string]any{"code": code("V4"), "name": "x", "kind": "day_of_month", "day_of_month": 0}, "day_of_month"},
		"receipt takes no days":          {map[string]any{"code": code("V5"), "name": "x", "kind": "due_on_receipt", "net_days": 5}, "net_days"},
		"a discount needs its days":      {map[string]any{"code": code("V6"), "name": "x", "kind": "net_days", "net_days": 30, "discount_percent": "2"}, "discount_percent"},
		"discount days need the percent": {map[string]any{"code": code("V7"), "name": "x", "kind": "net_days", "net_days": 30, "discount_days": 10}, "discount_percent"},
		"discount over 100":              {map[string]any{"code": code("V8"), "name": "x", "kind": "net_days", "net_days": 30, "discount_percent": "100.5", "discount_days": 10}, "discount_percent"},
		"a float discount":               {map[string]any{"code": code("V9"), "name": "x", "kind": "net_days", "net_days": 30, "discount_percent": 2.5, "discount_days": 10}, "discount_percent"},
		"an uppercase kind":              {map[string]any{"code": code("VA"), "name": "x", "kind": "NET_DAYS", "net_days": 30}, "kind"},
		"a lowercase code":               {map[string]any{"code": "net 30!", "name": "x", "kind": "net_days", "net_days": 30}, "code"},
		"no name":                        {map[string]any{"code": code("VB"), "kind": "net_days", "net_days": 30}, "name"},
	} {
		r := create(c.body)
		if r.status != 400 {
			t.Errorf("%s = %d, want 400: %s", name, r.status, r.raw)
			continue
		}
		if _, _, d := errorOf(t, r); !hasField(d, c.field) {
			t.Errorf("%s: no detail names %s: %v", name, c.field, fields(d))
		}
	}

	if r := create(map[string]any{"code": code("N15"), "name": "again", "kind": "net_days", "net_days": 1}); r.status != 409 {
		t.Errorf("a duplicate code = %d, want 409: %s", r.status, r.raw)
	} else if c, _, d := errorOf(t, r); c != "duplicate" || !hasBlocker(d, "terms_code_taken") {
		t.Errorf("duplicate = %s %v", c, d)
	}

	// Edit on a revision; the code is fixed.
	id := str(t, net.body, "id")
	put := func(rev int64, body map[string]any) resp {
		return f.do("PUT", "/api/v1/payment-terms/"+id, body, "If-Match", ifMatch(rev))
	}
	if r := f.do("PUT", "/api/v1/payment-terms/"+id, map[string]any{"code": code("N15"), "name": "x", "kind": "net_days", "net_days": 20}); r.status != 428 {
		t.Errorf("PUT without a revision = %d, want 428", r.status)
	}
	if r := put(7, map[string]any{"code": code("N15"), "name": "x", "kind": "net_days", "net_days": 20}); r.status != 409 {
		t.Errorf("stale PUT = %d, want 409", r.status)
	}
	if r := put(1, map[string]any{"code": code("OTHER"), "name": "x", "kind": "net_days", "net_days": 20}); r.status != 400 || !hasField(detailsOf(t, r), "code") {
		t.Errorf("changing the code = %d: %s", r.status, r.raw)
	}
	ok := put(1, map[string]any{"code": code("N15"), "name": "Net 20", "kind": "net_days", "net_days": 20})
	if ok.status != 200 || num(t, ok.body, "revision") != 2 || num(t, ok.body, "net_days") != 20 || ok.body["discount_percent"] != nil {
		t.Errorf("PUT = %d: %s", ok.status, ok.raw)
	}
	// Deactivate through is_active; the list filters on it.
	put(2, map[string]any{"code": code("N15"), "name": "Net 20", "kind": "net_days", "net_days": 20, "is_active": false})
	inactive := f.do("GET", "/api/v1/payment-terms?is_active=false&limit=200", nil)
	found := false
	for _, it := range inactive.body["items"].([]any) {
		m := it.(map[string]any)
		if m["is_active"] != false {
			t.Errorf("is_active=false served an active row: %v", m["code"])
		}
		found = found || m["code"] == code("N15")
	}
	if !found {
		t.Error("the deactivated terms are missing from is_active=false")
	}
	if r := f.do("GET", "/api/v1/payment-terms?kind=day_of_month&limit=200", nil); len(r.body["items"].([]any)) < 1 {
		t.Errorf("kind=day_of_month served nothing")
	} else {
		for _, it := range r.body["items"].([]any) {
			if it.(map[string]any)["kind"] != "day_of_month" {
				t.Errorf("kind filter served %v", it.(map[string]any)["kind"])
			}
		}
	}
	for _, bad := range []string{"kind=NET_DAYS", "kind=weekly", "is_active=1", "x=1", "offset=0"} {
		if r := f.do("GET", "/api/v1/payment-terms?"+bad, nil); r.status != 400 {
			t.Errorf("?%s = %d, want 400", bad, r.status)
		}
	}
	// Terms are never deleted: there is no DELETE route.
	if r := f.do("DELETE", "/api/v1/payment-terms/"+id, nil); r.status != http.StatusMethodNotAllowed && r.status != http.StatusNotFound {
		t.Errorf("DELETE on terms = %d, want the router's 405", r.status)
	}
}

// ADR 0005 7.3: ship-to addresses are an entity, many per customer.
func TestWire_ShipTos(t *testing.T) {
	testutil.LockOutboxTables(t)
	f := newFixture(t, testutil.RequireDB(t))
	c := f.create().body
	cid := str(t, c, "id")
	base := "/api/v1/customers/" + cid + "/ship-tos"

	first := f.do("POST", base, map[string]any{
		"code": "YARD", "name": "Lakeshore job", "line1": "12 Lake Rd", "line2": "Gate 3", "city": "Springfield",
		"region": "ST", "postal_code": "12345", "country": "US", "phone": "555-0100",
		"delivery_instructions": "Call before arriving", "tax_rate_percent": "8.875",
	})
	if first.status != 201 || first.header.Get("ETag") != `"1"` || !strings.HasPrefix(first.header.Get("Location"), "/api/v1/ship-tos/") {
		t.Fatalf("create ship-to = %d: %s", first.status, first.raw)
	}
	s := first.body
	if str(t, s, "customer_id") != cid || str(t, s, "tax_rate_percent") != "8.875" || str(t, s, "country") != "US" {
		t.Errorf("ship-to = %s", first.raw)
	}
	if s["is_default"] != true || s["is_active"] != true {
		t.Errorf("the customer's first ship-to must be the default: is_default=%v is_active=%v", s["is_default"], s["is_active"])
	}
	var stored string
	if err := f.db.Pool.QueryRow(context.Background(), `SELECT tax_rate::text FROM customer_ship_tos WHERE id = $1`, s["id"]).Scan(&stored); err != nil || stored != "0.088750" {
		t.Errorf("stored tax_rate = %q (%v), want 0.088750", stored, err)
	}

	second := f.do("POST", base, map[string]any{"code": "SITE2", "name": "Second site", "line1": "9 Hill St"})
	if second.status != 201 || second.body["is_default"] != false || second.body["tax_rate_percent"] != nil || second.body["line2"] != nil {
		t.Fatalf("second ship-to = %d: %s", second.status, second.raw)
	}
	// Naming a default moves the default; there is at most one.
	third := f.do("POST", base, map[string]any{"code": "SITE3", "name": "Third", "line1": "1 Pine", "is_default": true})
	if third.status != 201 || third.body["is_default"] != true {
		t.Fatalf("a new default = %d: %s", third.status, third.raw)
	}
	var defaults int
	if err := f.db.Pool.QueryRow(context.Background(), `SELECT count(*) FROM customer_ship_tos WHERE customer_id = $1 AND is_default`, cid).Scan(&defaults); err != nil || defaults != 1 {
		t.Errorf("%d default ship-tos (%v), want exactly 1", defaults, err)
	}
	if g := f.do("GET", "/api/v1/ship-tos/"+str(t, s, "id"), nil); g.body["is_default"] != false || num(t, g.body, "revision") != 2 {
		t.Errorf("the previous default = %s, want is_default false at revision 2", g.raw)
	}

	// Validation: every offending field in one 400.
	bad := f.do("POST", base, map[string]any{"country": "usa", "tax_rate_percent": "101"})
	if bad.status != 400 {
		t.Fatalf("invalid ship-to = %d: %s", bad.status, bad.raw)
	}
	for _, want := range []string{"code", "name", "line1", "country", "tax_rate_percent"} {
		if !hasField(detailsOf(t, bad), want) {
			t.Errorf("no detail names %s", want)
		}
	}
	if r := f.do("POST", base, map[string]any{"code": "YARD", "name": "dup", "line1": "x"}); r.status != 409 {
		t.Errorf("a duplicate code = %d, want 409: %s", r.status, r.raw)
	} else if c, _, d := errorOf(t, r); c != "duplicate" || !hasBlocker(d, "ship_to_code_taken") {
		t.Errorf("duplicate = %s %v", c, d)
	}
	if r := f.do("POST", base, map[string]any{"code": "X", "name": "x", "line1": "x", "is_default": true, "is_active": false}); r.status != 400 {
		t.Errorf("an inactive default = %d, want 400", r.status)
	}
	if r := f.do("POST", "/api/v1/customers/"+uuid.NewString()+"/ship-tos", map[string]any{"code": "X", "name": "x", "line1": "x"}); r.status != 404 {
		t.Errorf("a ship-to of an unknown customer = %d, want 404", r.status)
	}

	// Edit on a revision, deactivate, and the code is unique per customer only.
	sid := str(t, second.body, "id")
	put := func(rev int64, body map[string]any) resp {
		return f.do("PUT", "/api/v1/ship-tos/"+sid, body, "If-Match", ifMatch(rev))
	}
	if r := f.do("PUT", "/api/v1/ship-tos/"+sid, map[string]any{"code": "SITE2", "name": "n", "line1": "l"}); r.status != 428 {
		t.Errorf("PUT without a revision = %d, want 428", r.status)
	}
	if r := put(5, map[string]any{"code": "SITE2", "name": "n", "line1": "l"}); r.status != 409 {
		t.Errorf("stale PUT = %d, want 409", r.status)
	}
	ok := put(1, map[string]any{"code": "SITE2", "name": "Renamed", "line1": "9 Hill St", "is_active": false})
	if ok.status != 200 || str(t, ok.body, "name") != "Renamed" || ok.body["is_active"] != false || num(t, ok.body, "revision") != 2 {
		t.Fatalf("PUT = %d: %s", ok.status, ok.raw)
	}
	other := str(t, f.create().body, "id")
	if r := f.do("POST", "/api/v1/customers/"+other+"/ship-tos", map[string]any{"code": "YARD", "name": "same code", "line1": "x"}); r.status != 201 {
		t.Errorf("the same code on another customer = %d, want 201: %s", r.status, r.raw)
	}

	// The list: newest first, is_active filters, the cursor walks, scoped to the customer.
	all := f.do("GET", base+"?include=total", nil)
	if all.status != 200 || num(t, all.body, "total") != 3 || len(all.body["items"].([]any)) != 3 {
		t.Errorf("list = %d: %s", all.status, all.raw)
	}
	if r := f.do("GET", base+"?is_active=false", nil); len(r.body["items"].([]any)) != 1 {
		t.Errorf("is_active=false = %s", r.raw)
	}
	walk := f.do("GET", base+"?limit=2", nil)
	next, _ := walk.body["next_cursor"].(string)
	if next == "" || len(walk.body["items"].([]any)) != 2 {
		t.Fatalf("first page = %s", walk.raw)
	}
	if last := f.do("GET", base+"?limit=2&cursor="+next, nil); len(last.body["items"].([]any)) != 1 || last.body["next_cursor"] != nil {
		t.Errorf("last page = %s", last.raw)
	}
	for _, q := range []string{"x=1", "offset=0", "is_active=maybe", "limit=0", "cursor=zzz"} {
		if r := f.do("GET", base+"?"+q, nil); r.status != 400 {
			t.Errorf("?%s = %d, want 400", q, r.status)
		}
	}

	// customer.updated of part ship_to for each write.
	types, data := f.events(cid)
	var parts []string
	for i, ty := range types {
		if ty == "customer.updated" && data[i]["part"] == "ship_to" {
			parts = append(parts, fmt.Sprint(data[i]["action"]))
		}
	}
	if fmt.Sprint(parts) != "[created created created updated]" {
		t.Errorf("ship_to events = %v, want three creates then an update", parts)
	}
}

// ADR 0005 7.4: contact order authority and the PO flag are stored and read.
func TestWire_Contacts(t *testing.T) {
	testutil.LockOutboxTables(t)
	f := newFixture(t, testutil.RequireDB(t))
	cid := str(t, f.create().body, "id")
	base := "/api/v1/customers/" + cid + "/contacts"

	plain := f.do("POST", base, map[string]any{"first_name": "Pat", "last_name": "Lee"})
	if plain.status != 201 || plain.header.Get("ETag") != `"1"` || !strings.HasPrefix(plain.header.Get("Location"), "/api/v1/contacts/") {
		t.Fatalf("create contact = %d: %s", plain.status, plain.raw)
	}
	if plain.body["can_place_orders"] != true || plain.body["order_limit_cents"] != nil || plain.body["role"] != nil || plain.body["is_active"] != true {
		t.Errorf("contact defaults = %s", plain.raw)
	}
	limited := f.do("POST", base, map[string]any{
		"first_name": "Sam", "last_name": "Ray", "role": "Buyer", "email": "sam@example.com",
		"can_place_orders": true, "order_limit_cents": 50000, "is_primary": true,
	})
	if limited.status != 201 || num(t, limited.body, "order_limit_cents") != 50000 || limited.body["role"] != "Buyer" {
		t.Fatalf("limited contact = %d: %s", limited.status, limited.raw)
	}
	var stored string
	if err := f.db.Pool.QueryRow(context.Background(), `SELECT order_limit::text FROM customer_contacts WHERE id = $1`, limited.body["id"]).Scan(&stored); err != nil || stored != "500.00" {
		t.Errorf("stored order_limit = %q (%v), want 500.00", stored, err)
	}
	none := f.do("POST", base, map[string]any{"first_name": "Kim", "last_name": "Fox", "can_place_orders": false, "order_limit_cents": 0})
	if none.status != 201 || none.body["can_place_orders"] != false || num(t, none.body, "order_limit_cents") != 0 {
		t.Errorf("a contact who may place no orders = %d: %s", none.status, none.raw)
	}

	bad := f.do("POST", base, map[string]any{"role": "Wizard", "email": "x", "order_limit_cents": -1, "revision": 1})
	if bad.status != 400 {
		t.Fatalf("invalid contact = %d: %s", bad.status, bad.raw)
	}
	for _, want := range []string{"first_name", "last_name", "role", "email", "order_limit_cents", "revision"} {
		if !hasField(detailsOf(t, bad), want) {
			t.Errorf("no detail names %s", want)
		}
	}
	if r := f.do("POST", base, map[string]any{"first_name": "A", "last_name": "B", "customer_id": uuid.NewString()}); r.status != 400 {
		t.Errorf("a customer_id in the body = %d, want 400 (the path names the customer)", r.status)
	}

	// Edit on a revision.
	id := str(t, limited.body, "id")
	put := func(rev int64, body map[string]any) resp {
		return f.do("PUT", "/api/v1/contacts/"+id, body, "If-Match", ifMatch(rev))
	}
	up := map[string]any{"first_name": "Sam", "last_name": "Ray", "can_place_orders": false}
	if r := f.do("PUT", "/api/v1/contacts/"+id, up); r.status != 428 {
		t.Errorf("PUT without a revision = %d, want 428", r.status)
	}
	if r := put(4, up); r.status != 409 {
		t.Errorf("stale PUT = %d, want 409", r.status)
	}
	ok := put(1, up)
	if ok.status != 200 || ok.body["can_place_orders"] != false || ok.body["order_limit_cents"] != nil || num(t, ok.body, "revision") != 2 {
		t.Fatalf("PUT = %d: %s", ok.status, ok.raw)
	}
	if g := f.do("GET", "/api/v1/contacts/"+id, nil); g.status != 200 || g.header.Get("ETag") != `"2"` {
		t.Errorf("GET contact = %d ETag %q", g.status, g.header.Get("ETag"))
	}

	// The list is the customer's, an envelope, and walks.
	l := f.do("GET", base+"?include=total&limit=2", nil)
	if l.status != 200 || num(t, l.body, "total") != 3 || len(l.body["items"].([]any)) != 2 || l.body["next_cursor"] == nil {
		t.Errorf("list = %s", l.raw)
	}
	other := str(t, f.create().body, "id")
	if r := f.do("GET", "/api/v1/customers/"+other+"/contacts", nil); !bytes.Contains(r.raw, []byte(`"items":[]`)) {
		t.Errorf("another customer's contacts = %s, want an empty list", r.raw)
	}

	// Delete needs the revision (If-Match, a DELETE has no body).
	if r := f.do("DELETE", "/api/v1/contacts/"+id, nil); r.status != 428 {
		t.Errorf("DELETE without a revision = %d, want 428", r.status)
	}
	if r := f.do("DELETE", "/api/v1/contacts/"+id, nil, "If-Match", `"1"`); r.status != 409 {
		t.Errorf("stale DELETE = %d, want 409", r.status)
	}
	if r := f.do("DELETE", "/api/v1/contacts/"+id, nil, "If-Match", `"2"`); r.status != 204 || len(r.raw) != 0 {
		t.Errorf("DELETE = %d %q, want 204 and no body", r.status, r.raw)
	}
	if r := f.do("GET", "/api/v1/contacts/"+id, nil); r.status != 404 {
		t.Errorf("GET after DELETE = %d, want 404", r.status)
	}

	// customer.updated of part contact for each write.
	types, data := f.events(cid)
	var actions []string
	for i, ty := range types {
		if ty == "customer.updated" && data[i]["part"] == "contact" {
			actions = append(actions, fmt.Sprint(data[i]["action"]))
		}
	}
	if fmt.Sprint(actions) != "[created created created updated deleted]" {
		t.Errorf("contact events = %v", actions)
	}
}

// PATCH salesperson takes a revision and an explicit key; null unassigns.
func TestWire_Salesperson(t *testing.T) {
	testutil.LockOutboxTables(t)
	f := newFixture(t, testutil.RequireDB(t))
	var sp string
	if err := f.db.Pool.QueryRow(context.Background(),
		`INSERT INTO sales_team (name, email) VALUES ($1, $2) RETURNING id::text`, f.prefix+"rep", f.prefix+"rep@example.com").Scan(&sp); err != nil {
		t.Fatal(err)
	}
	id := str(t, f.create().body, "id")
	path := "/api/v1/customers/" + id + "/salesperson"

	if r := f.do("PATCH", path, map[string]any{"salesperson_id": sp}); r.status != 428 {
		t.Errorf("PATCH without a revision = %d, want 428", r.status)
	}
	if r := f.do("PATCH", path, map[string]any{"revision": 1}); r.status != 400 || !hasField(detailsOf(t, r), "salesperson_id") {
		t.Errorf("PATCH without the key = %d: %s", r.status, r.raw)
	}
	if r := f.do("PATCH", path, map[string]any{"salesperson_id": uuid.NewString(), "revision": 1}); r.status != 400 || !hasField(detailsOf(t, r), "salesperson_id") {
		t.Errorf("PATCH with an unknown salesperson = %d: %s", r.status, r.raw)
	}
	ok := f.do("PATCH", path, map[string]any{"salesperson_id": sp}, "If-Match", `"1"`)
	if ok.status != 200 || str(t, ok.body, "salesperson_id") != sp || num(t, ok.body, "revision") != 2 || ok.header.Get("ETag") != `"2"` {
		t.Fatalf("assign = %d: %s", ok.status, ok.raw)
	}
	if ok.body["salesperson_name"] != f.prefix+"rep" {
		t.Errorf("salesperson_name = %v", ok.body["salesperson_name"])
	}
	cleared := f.do("PATCH", path, map[string]any{"salesperson_id": nil, "revision": 2})
	if cleared.status != 200 || cleared.body["salesperson_id"] != nil || cleared.body["salesperson_name"] != nil {
		t.Errorf("unassign = %d: %s", cleared.status, cleared.raw)
	}
	types, data := f.events(id)
	if len(types) != 3 || !containsStr(data[1]["changed"], "salesperson_id") {
		t.Errorf("events = %v %v", types, data)
	}
}

// The escalation policy: lowercase vocabulary, a revision on the write, and
// the old rules (an auto policy needs a signed agreement, the threshold is
// above 0 and at most 50, a reference without a date is stamped).
func TestWire_EscalationPolicy(t *testing.T) {
	testutil.LockOutboxTables(t)
	f := newFixture(t, testutil.RequireDB(t))
	id := str(t, f.create().body, "id")
	path := "/api/v1/customers/" + id + "/escalation-policy"

	g := f.do("GET", path, nil)
	if g.status != 200 || g.body["policy"] != "flag_for_requote" || g.body["threshold_percent"] != "5" || g.body["agreement_signed_at"] != nil || g.header.Get("ETag") != `"1"` {
		t.Fatalf("GET = %d: %s (ETag %q)", g.status, g.raw, g.header.Get("ETag"))
	}
	if r := f.do("GET", "/api/v1/customers/"+uuid.NewString()+"/escalation-policy", nil); r.status != 404 {
		t.Errorf("GET on an unknown customer = %d, want 404", r.status)
	}
	put := func(rev int64, body map[string]any) resp { return f.do("PUT", path, body, "If-Match", ifMatch(rev)) }

	if r := f.do("PUT", path, map[string]any{"policy": "require_ack", "threshold_percent": "5"}); r.status != 428 {
		t.Errorf("PUT without a revision = %d, want 428", r.status)
	}
	if r := put(3, map[string]any{"policy": "require_ack", "threshold_percent": "5"}); r.status != 409 {
		t.Errorf("stale PUT = %d, want 409", r.status)
	}
	if r := put(1, map[string]any{"policy": "auto_escalate", "threshold_percent": "5"}); r.status != 400 || !hasField(detailsOf(t, r), "agreement_signed_at") {
		t.Errorf("an auto policy without an agreement = %d: %s", r.status, r.raw)
	}
	for _, thr := range []string{"0", "50.0001", "-1"} {
		if r := put(1, map[string]any{"policy": "require_ack", "threshold_percent": thr}); r.status != 400 || !hasField(detailsOf(t, r), "threshold_percent") {
			t.Errorf("threshold %s = %d: %s", thr, r.status, r.raw)
		}
	}
	if r := put(1, map[string]any{"policy": "REQUIRE_ACK", "threshold_percent": "5"}); r.status != 400 || !hasField(detailsOf(t, r), "policy") {
		t.Errorf("an uppercase policy = %d: %s", r.status, r.raw)
	}
	ok := put(1, map[string]any{"policy": "auto_escalate", "threshold_percent": "7.5", "agreement_signed_at": "2030-01-02T03:04:05Z", "agreement_ref": "AGR-1"})
	if ok.status != 200 || ok.body["policy"] != "auto_escalate" || ok.body["threshold_percent"] != "7.5" || ok.body["agreement_ref"] != "AGR-1" ||
		num(t, ok.body, "revision") != 2 || ok.header.Get("ETag") != `"2"` || !strings.HasPrefix(str(t, ok.body, "agreement_signed_at"), "2030-01-02T03:04:05") {
		t.Fatalf("PUT = %d: %s", ok.status, ok.raw)
	}
	var stored string
	if err := f.db.Pool.QueryRow(context.Background(), `SELECT price_escalation_policy FROM customers WHERE id = $1`, id).Scan(&stored); err != nil || stored != "AUTO_ESCALATE" {
		t.Errorf("stored policy = %q (%v): the pricing scanner reads the uppercase vocabulary", stored, err)
	}
	stamped := put(2, map[string]any{"policy": "flag_for_requote", "threshold_percent": "5", "agreement_ref": "AGR-2"})
	if stamped.status != 200 || stamped.body["agreement_signed_at"] == nil {
		t.Errorf("a reference without a date = %d: %s, want it stamped", stamped.status, stamped.raw)
	}
	types, data := f.events(id)
	if types[len(types)-1] != "customer.updated" || data[len(data)-1]["part"] != "escalation_policy" {
		t.Errorf("events = %v, want an update of part escalation_policy last", types)
	}
}

// Price levels are a list on the envelope.
func TestWire_PriceLevels(t *testing.T) {
	f := newFixture(t, testutil.RequireDB(t))
	r := f.do("GET", "/api/v1/price_levels", nil)
	if r.status != 200 {
		t.Fatalf("GET = %d: %s", r.status, r.raw)
	}
	if _, ok := r.body["items"].([]any); !ok {
		t.Fatalf("not an envelope: %s", r.raw)
	}
	for _, bad := range []string{"offset=0", "x=1", "limit=0"} {
		if r := f.do("GET", "/api/v1/price_levels?"+bad, nil); r.status != 400 {
			t.Errorf("?%s = %d, want 400", bad, r.status)
		}
	}
	var name string
	if err := f.db.Pool.QueryRow(context.Background(), `SELECT name FROM price_levels ORDER BY created_at DESC, id DESC LIMIT 1`).Scan(&name); err == nil {
		items := r.body["items"].([]any)
		if len(items) == 0 || str(t, items[0].(map[string]any), "name") != name {
			t.Errorf("price levels are not newest first: %s", r.raw)
		}
		if _, ok := items[0].(map[string]any)["multiplier"].(json.Number); !ok {
			t.Errorf("multiplier = %v, want a number", items[0].(map[string]any)["multiplier"])
		}
	}
}

// The branch wall (recipe step 6): a second branch's request for the first
// branch's customer is a 404 on every route, and its list excludes it.
func TestWire_BranchWall(t *testing.T) {
	testutil.LockOutboxTables(t)
	f := newFixture(t, testutil.RequireDB(t))
	ctx := context.Background()
	var other uuid.UUID
	if err := f.db.Pool.QueryRow(ctx, `INSERT INTO locations (name, code, type)
		VALUES ($1, $2, 'YARD') RETURNING id`, f.prefix+"branch", strings.ToUpper(f.prefix[:9])).Scan(&other); err != nil {
		t.Skipf("cannot make a second branch: %v", err)
	}
	t.Cleanup(func() { _, _ = f.db.Pool.Exec(ctx, `DELETE FROM locations WHERE id = $1`, other) })

	c := f.create().body
	cid, acct := str(t, c, "id"), str(t, c, "account_number")
	ship := f.do("POST", "/api/v1/customers/"+cid+"/ship-tos", map[string]any{"code": "A", "name": "a", "line1": "a"})
	contact := f.do("POST", "/api/v1/customers/"+cid+"/contacts", map[string]any{"first_name": "A", "last_name": "B"})
	wall := []string{"X-Test-Branch", other.String()}

	for name, c := range map[string]struct {
		method, path string
		body         any
		headers      []string
	}{
		"get":         {"GET", "/api/v1/customers/" + cid, nil, nil},
		"put":         {"PUT", "/api/v1/customers/" + cid, map[string]any{"account_number": acct, "name": "x", "revision": 1}, nil},
		"salesperson": {"PATCH", "/api/v1/customers/" + cid + "/salesperson", map[string]any{"salesperson_id": nil, "revision": 1}, nil},
		"policy get":  {"GET", "/api/v1/customers/" + cid + "/escalation-policy", nil, nil},
		"policy put":  {"PUT", "/api/v1/customers/" + cid + "/escalation-policy", map[string]any{"policy": "require_ack", "threshold_percent": "5", "revision": 1}, nil},
		"ship-tos":    {"GET", "/api/v1/customers/" + cid + "/ship-tos", nil, nil},
		"ship-to add": {"POST", "/api/v1/customers/" + cid + "/ship-tos", map[string]any{"code": "Z", "name": "z", "line1": "z"}, nil},
		"ship-to get": {"GET", "/api/v1/ship-tos/" + str(t, ship.body, "id"), nil, nil},
		"ship-to put": {"PUT", "/api/v1/ship-tos/" + str(t, ship.body, "id"), map[string]any{"code": "A", "name": "a", "line1": "a", "revision": 1}, nil},
		"contacts":    {"GET", "/api/v1/customers/" + cid + "/contacts", nil, nil},
		"contact add": {"POST", "/api/v1/customers/" + cid + "/contacts", map[string]any{"first_name": "Z", "last_name": "Z"}, nil},
		"contact get": {"GET", "/api/v1/contacts/" + str(t, contact.body, "id"), nil, nil},
		"contact put": {"PUT", "/api/v1/contacts/" + str(t, contact.body, "id"), map[string]any{"first_name": "A", "last_name": "B", "revision": 1}, nil},
		"contact del": {"DELETE", "/api/v1/contacts/" + str(t, contact.body, "id"), nil, []string{"If-Match", `"1"`}},
	} {
		h := append(append([]string{}, wall...), c.headers...)
		if r := f.do(c.method, c.path, c.body, h...); r.status != 404 {
			t.Errorf("%s from another branch = %d, want 404: %s", name, r.status, r.raw)
		}
	}
	list := f.do("GET", "/api/v1/customers?q="+f.prefix, nil, wall...)
	if !bytes.Contains(list.raw, []byte(`"items":[]`)) {
		t.Errorf("another branch's list = %s, want it empty", list.raw)
	}
	// Nothing the other branch tried changed the customer.
	if g := f.do("GET", "/api/v1/customers/"+cid, nil, "X-Test-Branch", f.branch.String()); g.status != 200 || num(t, g.body, "revision") != 1 {
		t.Errorf("the owning branch's read = %d: %s", g.status, g.raw)
	}
	// A create that names another branch than the request's wall is refused.
	if r := f.do("POST", "/api/v1/customers", f.body(map[string]any{"primary_branch_id": f.branch.String()}), wall...); r.status != 400 || !hasField(detailsOf(t, r), "primary_branch_id") {
		t.Errorf("a create outside the wall = %d: %s", r.status, r.raw)
	}
}

// A replayed create returns the first response and makes one row and one event.
func TestWire_IdempotentCreate(t *testing.T) {
	testutil.LockOutboxTables(t)
	f := newFixture(t, testutil.RequireDB(t))
	key := "wire-" + uuid.NewString()
	body := f.body(nil)
	first := f.do("POST", "/api/v1/customers", body, "Idempotency-Key", key)
	second := f.do("POST", "/api/v1/customers", body, "Idempotency-Key", key)
	if first.status != 201 || second.status != 201 {
		t.Fatalf("statuses = %d, %d: %s", first.status, second.status, second.raw)
	}
	if !bytes.Equal(first.raw, second.raw) || second.header.Get("Idempotency-Replayed") != "true" {
		t.Errorf("the replay differs from the first response")
	}
	var n int
	if err := f.db.Pool.QueryRow(context.Background(), `SELECT count(*) FROM customers WHERE account_number = $1`, body["account_number"]).Scan(&n); err != nil || n != 1 {
		t.Errorf("%d customers made (%v), want 1", n, err)
	}
	if types, _ := f.events(str(t, first.body, "id")); len(types) != 1 {
		t.Errorf("events = %v, want one customer.created", types)
	}
	other := f.body(nil)
	if r := f.do("POST", "/api/v1/customers", other, "Idempotency-Key", key); r.status != 422 {
		t.Errorf("a reused key with another body = %d, want 422", r.status)
	}
}

// ADR 0002: the new route segments are in the machine key vocabulary, so a key
// holding ship-tos:read or payment-terms:write reaches them; the first segment
// of every customer route is a module the vocabulary names.
func TestMachineKeyVocabularyNamesEveryCustomerRoute(t *testing.T) {
	for _, module := range []string{"customers", "contacts", "ship-tos", "payment-terms", "price_levels"} {
		if middleware.ModuleScopePolicyFor(module) != middleware.ModuleScopeAllowed {
			t.Errorf("%s is not in the machine key scope vocabulary", module)
		}
	}
	_ = httpx.CodeConflict
}

// Every route sits behind the guard the module is registered with, and the
// writes of the payment terms master behind the second, narrower one.
func TestRegisterRoutes_GuardsWrapEveryRoute(t *testing.T) {
	svc := customer.NewService(nil)
	h := customer.NewHandler(svc)
	record := func(label string, seen map[string]string) func(http.Handler) http.Handler {
		return func(next http.Handler) http.Handler {
			return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				seen[r.Method+" "+r.URL.Path] = label
				w.WriteHeader(http.StatusTeapot)
			})
		}
	}
	seen := map[string]string{}
	mux := http.NewServeMux()
	h.RegisterRoutes(mux, record("sales", seen), record("finance", seen))

	id := uuid.NewString()
	for _, route := range []struct{ method, path, want string }{
		{"GET", "/api/v1/customers", "sales"},
		{"POST", "/api/v1/customers", "sales"},
		{"GET", "/api/v1/customers/" + id, "sales"},
		{"PUT", "/api/v1/customers/" + id, "sales"},
		{"PATCH", "/api/v1/customers/" + id + "/salesperson", "sales"},
		{"GET", "/api/v1/customers/" + id + "/escalation-policy", "sales"},
		{"PUT", "/api/v1/customers/" + id + "/escalation-policy", "sales"},
		{"GET", "/api/v1/price_levels", "sales"},
		{"GET", "/api/v1/customers/" + id + "/ship-tos", "sales"},
		{"POST", "/api/v1/customers/" + id + "/ship-tos", "sales"},
		{"GET", "/api/v1/ship-tos/" + id, "sales"},
		{"PUT", "/api/v1/ship-tos/" + id, "sales"},
		{"GET", "/api/v1/payment-terms", "sales"},
		{"GET", "/api/v1/payment-terms/" + id, "sales"},
		{"POST", "/api/v1/payment-terms", "finance"},
		{"PUT", "/api/v1/payment-terms/" + id, "finance"},
		{"GET", "/api/v1/customers/" + id + "/contacts", "sales"},
		{"POST", "/api/v1/customers/" + id + "/contacts", "sales"},
		{"GET", "/api/v1/contacts/" + id, "sales"},
		{"PUT", "/api/v1/contacts/" + id, "sales"},
		{"DELETE", "/api/v1/contacts/" + id, "sales"},
	} {
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, httptest.NewRequest(route.method, route.path, nil))
		if rec.Code != http.StatusTeapot || seen[route.method+" "+route.path] != route.want {
			t.Errorf("%s %s: status %d, guarded by %q, want the %s guard", route.method, route.path, rec.Code, seen[route.method+" "+route.path], route.want)
		}
	}
}
