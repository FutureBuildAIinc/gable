// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package project_test

// The project module on the wire contract (ADR 0001), tested end to end: a
// real Postgres, the real handler on a real mux behind the real portal
// idempotency layer with the portal chain's customer claims, requests as
// JSON and responses read back as JSON. Nothing here touches a module type,
// so each test states a wire fact.

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gablelbm/gable/internal/project"
	"github.com/gablelbm/gable/internal/testutil"
	"github.com/gablelbm/gable/pkg/audit"
	"github.com/gablelbm/gable/pkg/database"
	"github.com/gablelbm/gable/pkg/middleware"
	"github.com/gablelbm/gable/pkg/outbox"
	"github.com/google/uuid"
)

type fixture struct {
	t        *testing.T
	db       *database.DB
	srv      *httptest.Server
	customer uuid.UUID
	other    uuid.UUID
	prefix   string
}

// newFixture builds the module the way serve does (repository, service with
// the outbox and the database as transaction runner and the audit logger,
// handler) behind the portal idempotency layer, and injects the portal
// chain's customer claims, as the portal auth middleware does.
func newFixture(t *testing.T, customer uuid.UUID) *fixture {
	t.Helper()
	t.Setenv("AUTH_MODE", "dev")
	db := testutil.RequireDB(t)
	f := &fixture{t: t, db: db, customer: customer, prefix: "PWIRE-" + uuid.NewString()[:8] + "-"}
	svc := project.NewService(project.NewRepository(db)).
		WithOutbox(outbox.NewWriter(db, "")).
		WithTxRunner(db).
		WithAudit(audit.NewLogger(db))
	mux := http.NewServeMux()
	project.NewHandler(svc).RegisterRoutes(mux, func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			claims := &middleware.PortalClaims{CustomerID: customer, Role: "Admin"}
			ctx := context.WithValue(r.Context(), middleware.PortalClaimsKey, claims)
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	})
	withClaims := func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			claims := &middleware.PortalClaims{CustomerID: customer, Role: "Admin"}
			ctx := context.WithValue(r.Context(), middleware.PortalClaimsKey, claims)
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
	f.srv = httptest.NewServer(withClaims(middleware.IdempotencyForPortalAuth(db)(mux)))
	t.Cleanup(func() {
		f.srv.Close()
		c := context.Background()
		for _, id := range []uuid.UUID{f.customer, f.other} {
			_, _ = db.Pool.Exec(c, `DELETE FROM events_outbox WHERE entity_type = 'project' AND entity_id IN (SELECT id FROM projects WHERE customer_id = $1)`, id)
			_, _ = db.Pool.Exec(c, `DELETE FROM audit_log WHERE entity_type = 'project' AND entity_id IN (SELECT id FROM projects WHERE customer_id = $1)`, id)
			_, _ = db.Pool.Exec(c, `DELETE FROM projects WHERE customer_id = $1`, id)
			_, _ = db.Pool.Exec(c, `DELETE FROM customers WHERE id = $1`, id)
		}
	})
	return f
}

// seedCustomer creates a customer to hang projects off.
func seedCustomer(t *testing.T, db *database.DB, prefix, tag string) uuid.UUID {
	t.Helper()
	id := uuid.New()
	if _, err := db.Pool.Exec(context.Background(),
		`INSERT INTO customers (id, name, account_number, primary_branch_id)
		 VALUES ($1, $2, $3, (SELECT value::uuid FROM system_settings WHERE key = 'default_branch_id'))`,
		id, "Proj Wire "+tag, prefix+tag+id.String()[:8]); err != nil {
		t.Fatal(err)
	}
	return id
}

type resp struct {
	status int
	header http.Header
	body   map[string]any
	raw    []byte
}

func do(t *testing.T, srv *httptest.Server, method, path, body string, hdr map[string]string) resp {
	t.Helper()
	var rdr io.Reader
	if body != "" {
		rdr = bytes.NewReader([]byte(body))
	}
	req, err := http.NewRequest(method, srv.URL+path, rdr)
	if err != nil {
		t.Fatal(err)
	}
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	raw, _ := io.ReadAll(res.Body)
	out := resp{status: res.StatusCode, header: res.Header, raw: raw}
	if strings.Contains(res.Header.Get("Content-Type"), "json") && len(raw) > 0 {
		_ = json.Unmarshal(raw, &out.body)
	}
	return out
}

func newBothFixtures(t *testing.T) (mine, other *fixture) {
	t.Helper()
	db := testutil.RequireDB(t)
	prefix := "PWIRE-" + uuid.NewString()[:8] + "-"
	mine = newFixture(t, seedCustomer(t, db, prefix, "A"))
	mine.other = seedCustomer(t, db, prefix, "B")
	other = &fixture{t: t, db: db, srv: mine.srv, customer: mine.other, other: mine.customer, prefix: prefix}
	return mine, other
}

// asOther runs a request as the second customer.
func (f *fixture) as(t *testing.T, customer uuid.UUID, method, path, body string, hdr map[string]string) resp {
	t.Helper()
	db := testutil.RequireDB(t)
	svc := project.NewService(project.NewRepository(db)).
		WithOutbox(outbox.NewWriter(db, "")).WithTxRunner(db).WithAudit(audit.NewLogger(db))
	mux := http.NewServeMux()
	project.NewHandler(svc).RegisterRoutes(mux, func(next http.Handler) http.Handler { return next })
	withClaims := func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			claims := &middleware.PortalClaims{CustomerID: customer, Role: "Admin"}
			ctx := context.WithValue(r.Context(), middleware.PortalClaimsKey, claims)
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
	srv := httptest.NewServer(withClaims(middleware.IdempotencyForPortalAuth(db)(mux)))
	t.Cleanup(srv.Close)
	return do(t, srv, method, path, body, hdr)
}

// The create shape: lowercase status, revision and the ETag, Location,
// optional fields present as null.
func TestCreate_Shape(t *testing.T) {
	f, _ := newBothFixtures(t)
	res := do(t, f.srv, http.MethodPost, "/api/portal/v1/projects", `{"name":"Ridge roof"}`, nil)
	if res.status != http.StatusCreated {
		t.Fatalf("create = %d %s", res.status, res.raw)
	}
	if loc := res.header.Get("Location"); !strings.HasPrefix(loc, "/api/portal/v1/projects/") {
		t.Errorf("Location = %q", loc)
	}
	if etag := res.header.Get("ETag"); etag != `"1"` {
		t.Errorf("ETag = %q, want \"1\"", etag)
	}
	if res.body["status"] != "active" {
		t.Errorf("status = %v, want active", res.body["status"])
	}
	if res.body["revision"] != float64(1) {
		t.Errorf("revision = %v, want 1", res.body["revision"])
	}
	if res.body["customer_id"] != f.customer.String() {
		t.Errorf("customer_id = %v", res.body["customer_id"])
	}
	for _, field := range []string{"created_at", "updated_at"} {
		s, _ := res.body[field].(string)
		if !strings.HasSuffix(s, "Z") || !strings.Contains(s, ".") {
			t.Errorf("%s = %q, want an RFC 3339 UTC timestamp with a fraction", field, s)
		}
	}
}

// One 400 with every offending field; an unknown body field refused; a body
// the route cannot consume is bad_request.
func TestCreate_FieldValidation(t *testing.T) {
	f, _ := newBothFixtures(t)
	res := do(t, f.srv, http.MethodPost, "/api/portal/v1/projects",
		`{"name":"  ","status":"Active"}`, nil)
	if res.status != http.StatusBadRequest {
		t.Fatalf("validation = %d %s", res.status, res.raw)
	}
	env := res.body["error"].(map[string]any)
	if env["code"] != "validation_failed" {
		t.Errorf("code = %v", env["code"])
	}
	fields := map[string]bool{}
	for _, d := range env["details"].([]any) {
		fields[d.(map[string]any)["field"].(string)] = true
	}
	for _, want := range []string{"name", "status"} {
		if !fields[want] {
			t.Errorf("details = %v, want a %s entry", env["details"], want)
		}
	}
	if res = do(t, f.srv, http.MethodPost, "/api/portal/v1/projects", `not-json`, nil); res.status != http.StatusBadRequest ||
		res.body["error"].(map[string]any)["code"] != "bad_request" {
		t.Errorf("an unparseable body = %d %s, want 400 bad_request", res.status, res.raw)
	}
	if res = do(t, f.srv, http.MethodPost, "/api/portal/v1/projects", `{"name":"x","smoke":"signal"}`, nil); res.status != http.StatusBadRequest {
		t.Errorf("an unknown body field = %d, want 400", res.status)
	}
	// The name bound is characters, not bytes: 255 multi-byte letters pass.
	if res = do(t, f.srv, http.MethodPost, "/api/portal/v1/projects",
		`{"name":"`+strings.Repeat("é", 255)+`"}`, nil); res.status != http.StatusCreated {
		t.Errorf("a 255 character name of multi-byte letters = %d %s, want 201", res.status, res.raw)
	}
}

// The list: the envelope, a filter that filters, an unknown parameter
// refused, an uppercase status refused, the cursor walks every row once,
// include=total, an empty page is [].
func TestList_EnvelopeFiltersCursor(t *testing.T) {
	f, _ := newBothFixtures(t)
	for _, name := range []string{"one", "two", "three"} {
		if res := do(t, f.srv, http.MethodPost, "/api/portal/v1/projects", `{"name":"`+name+`"}`, nil); res.status != http.StatusCreated {
			t.Fatalf("create %s = %d %s", name, res.status, res.raw)
		}
	}
	res := do(t, f.srv, http.MethodGet, "/api/portal/v1/projects?limit=2", "", nil)
	if res.status != http.StatusOK {
		t.Fatalf("list = %d %s", res.status, res.raw)
	}
	if len(res.body["items"].([]any)) != 2 || res.body["limit"] != float64(2) {
		t.Errorf("page = %v", res.raw)
	}
	next, _ := res.body["next_cursor"].(string)
	if next == "" {
		t.Fatal("no next_cursor with another page present")
	}
	seen := map[string]bool{}
	cursor := ""
	for {
		q := "/api/portal/v1/projects?limit=2"
		if cursor != "" {
			q += "&cursor=" + cursor
		}
		page := do(t, f.srv, http.MethodGet, q, "", nil)
		for _, it := range page.body["items"].([]any) {
			id := it.(map[string]any)["id"].(string)
			if seen[id] {
				t.Fatalf("project %s served twice", id)
			}
			seen[id] = true
		}
		nc, _ := page.body["next_cursor"].(string)
		if nc == "" {
			break
		}
		cursor = nc
	}
	if len(seen) != 3 {
		t.Errorf("the cursor walk served %d projects, want 3", len(seen))
	}

	// Complete one so the status filter has something to filter.
	created := do(t, f.srv, http.MethodPost, "/api/portal/v1/projects", `{"name":"done job"}`, nil)
	id := created.body["id"].(string)
	do(t, f.srv, http.MethodPut, "/api/portal/v1/projects/"+id, `{"status":"completed"}`,
		map[string]string{"If-Match": `"1"`})
	res = do(t, f.srv, http.MethodGet, "/api/portal/v1/projects?status=completed", "", nil)
	items := res.body["items"].([]any)
	if len(items) != 1 || items[0].(map[string]any)["name"] != "done job" {
		t.Errorf("status filter = %v", res.raw)
	}
	if res = do(t, f.srv, http.MethodGet, "/api/portal/v1/projects?status=ACTIVE", "", nil); res.status != http.StatusBadRequest {
		t.Errorf("an uppercase status = %d, want 400", res.status)
	}
	if res = do(t, f.srv, http.MethodGet, "/api/portal/v1/projects?foo=1", "", nil); res.status != http.StatusBadRequest {
		t.Errorf("an unknown parameter = %d, want 400", res.status)
	} else if code := res.body["error"].(map[string]any)["code"]; code != "unsupported_query_parameter" {
		t.Errorf("code = %v, want unsupported_query_parameter", code)
	}
	if res = do(t, f.srv, http.MethodGet, "/api/portal/v1/projects?include=total", "", nil); res.body["total"] != float64(4) {
		t.Errorf("include=total = %v, want 4", res.body["total"])
	}
}

// Revision: 428 without a precondition, 409 stale_revision, If-Match strong
// and weak, the new revision and ETag on success.
func TestUpdate_Revision(t *testing.T) {
	f, _ := newBothFixtures(t)
	created := do(t, f.srv, http.MethodPost, "/api/portal/v1/projects", `{"name":"Ridge roof"}`, nil)
	id := created.body["id"].(string)
	body := `{"name":"Ridge roof two"}`

	if res := do(t, f.srv, http.MethodPut, "/api/portal/v1/projects/"+id, body, nil); res.status != http.StatusPreconditionRequired {
		t.Errorf("no precondition = %d, want 428", res.status)
	}
	if res := do(t, f.srv, http.MethodPut, "/api/portal/v1/projects/"+id, body, map[string]string{"If-Match": `"9"`}); res.status != http.StatusConflict {
		t.Errorf("stale If-Match = %d, want 409", res.status)
	}
	res := do(t, f.srv, http.MethodPut, "/api/portal/v1/projects/"+id, body, map[string]string{"If-Match": `W/"1"`})
	if res.status != http.StatusOK || res.body["revision"] != float64(2) || res.header.Get("ETag") != `"2"` {
		t.Errorf("weak If-Match = %d %s", res.status, res.raw)
	}
	if res = do(t, f.srv, http.MethodPut, "/api/portal/v1/projects/"+id,
		`{"name":"x","revision":9}`, map[string]string{"If-Match": `"2"`}); res.status != http.StatusBadRequest {
		t.Errorf("header and body disagreeing = %d, want 400", res.status)
	}
}

// A PUT that changes nothing is not a write: the row answers 200 with its
// current revision, and no audit row or event is recorded for it.
func TestUpdate_NoChangeIsNotAWrite(t *testing.T) {
	f, _ := newBothFixtures(t)
	created := do(t, f.srv, http.MethodPost, "/api/portal/v1/projects", `{"name":"as is"}`, nil)
	id := created.body["id"].(string)

	res := do(t, f.srv, http.MethodPut, "/api/portal/v1/projects/"+id, `{}`, map[string]string{"If-Match": `"1"`})
	if res.status != http.StatusOK {
		t.Fatalf("empty update = %d %s, want 200", res.status, res.raw)
	}
	if res.body["revision"] != float64(1) || res.header.Get("ETag") != `"1"` {
		t.Errorf("after an empty update revision = %v, ETag = %q; want 1", res.body["revision"], res.header.Get("ETag"))
	}
	if res.body["name"] != "as is" {
		t.Errorf("name after an empty update = %v", res.body["name"])
	}

	for _, c := range []struct{ table string }{
		{"audit_log"}, {"events_outbox"},
	} {
		var n int
		if err := f.db.Pool.QueryRow(context.Background(),
			`SELECT count(*) FROM `+c.table+` WHERE entity_type = 'project' AND entity_id = $1`, id).Scan(&n); err != nil {
			t.Fatal(err)
		}
		if n != 1 {
			t.Errorf("%d rows in %s for the project after an empty update, want 1 (the create's)", n, c.table)
		}
	}
}

// The customer scoping: another customer's project is a 404 on read and
// write, and its list never shows.
func TestCustomerScoping_AnotherCustomerIs404(t *testing.T) {
	f, _ := newBothFixtures(t)
	created := do(t, f.srv, http.MethodPost, "/api/portal/v1/projects", `{"name":"mine only"}`, nil)
	id := created.body["id"].(string)

	if res := f.as(t, f.other, http.MethodGet, "/api/portal/v1/projects/"+id, "", nil); res.status != http.StatusNotFound {
		t.Errorf("other's read = %d, want 404", res.status)
	}
	if res := f.as(t, f.other, http.MethodPut, "/api/portal/v1/projects/"+id, `{"name":"steal"}`,
		map[string]string{"If-Match": `"1"`}); res.status != http.StatusNotFound {
		t.Errorf("other's write = %d, want 404", res.status)
	}
	if res := f.as(t, f.other, http.MethodGet, "/api/portal/v1/projects", "", nil); len(res.body["items"].([]any)) != 0 {
		t.Errorf("other's list = %s, want empty", res.raw)
	}
}

// The dashboard aggregates the project's orders, deliveries and invoices,
// with totals in cents and lowercase statuses.
func TestDashboard_Items(t *testing.T) {
	f, _ := newBothFixtures(t)
	ctx := context.Background()
	created := do(t, f.srv, http.MethodPost, "/api/portal/v1/projects", `{"name":"with docs"}`, nil)
	id := created.body["id"].(string)
	pid, _ := uuid.Parse(id)

	// An order on the project with a document total.
	var orderID uuid.UUID
	if err := f.db.Pool.QueryRow(ctx, `
		INSERT INTO orders (customer_id, status, total_amount, branch_id, project_id, currency)
		VALUES ($1, 'CONFIRMED', 123.45, (SELECT value::uuid FROM system_settings WHERE key = 'default_branch_id'), $2, 'USD')
		RETURNING id`, f.customer, pid).Scan(&orderID); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = f.db.Pool.Exec(ctx, `DELETE FROM orders WHERE id = $1`, orderID) })

	res := do(t, f.srv, http.MethodGet, "/api/portal/v1/projects/"+id, "", nil)
	if res.status != http.StatusOK {
		t.Fatalf("dashboard = %d %s", res.status, res.raw)
	}
	orders := res.body["orders"].([]any)
	if len(orders) != 1 {
		t.Fatalf("orders = %v", res.raw)
	}
	order := orders[0].(map[string]any)
	if order["type"] != "order" || order["status"] != "confirmed" {
		t.Errorf("order summary = %v", order)
	}
	if order["total_cents"] != float64(12345) {
		t.Errorf("total_cents = %v, want 12345", order["total_cents"])
	}
	if v, ok := order["total_amount"]; ok {
		t.Errorf("the float total_amount survived: %v", v)
	}
	for _, key := range []string{"deliveries", "invoices"} {
		if arr, ok := res.body[key].([]any); !ok || len(arr) != 0 {
			t.Errorf("%s = %v, want an empty array", key, res.body[key])
		}
	}
	if res.header.Get("ETag") != `"1"` {
		t.Errorf("the dashboard carries no project ETag: %q", res.header.Get("ETag"))
	}
}

// The mutation writes its audit row and its event, in one transaction.
func TestMutation_AuditRowAndEvent(t *testing.T) {
	f, _ := newBothFixtures(t)
	created := do(t, f.srv, http.MethodPost, "/api/portal/v1/projects", `{"name":"audited"}`, nil)
	id := created.body["id"].(string)
	ctx := context.Background()

	count := func(sql string) int {
		f.t.Helper()
		var n int
		if err := f.db.Pool.QueryRow(ctx, sql, id).Scan(&n); err != nil {
			f.t.Fatal(err)
		}
		return n
	}
	if n := count(`SELECT count(*) FROM audit_log WHERE entity_type = 'project' AND entity_id = $1 AND action = 'project.created'`); n != 1 {
		t.Errorf("%d project.created audit rows, want 1", n)
	}
	if n := count(`SELECT count(*) FROM events_outbox WHERE entity_type = 'project' AND entity_id = $1 AND type = 'project.created'`); n != 1 {
		t.Errorf("%d project.created events, want 1", n)
	}
	do(t, f.srv, http.MethodPut, "/api/portal/v1/projects/"+id, `{"status":"completed"}`, map[string]string{"If-Match": `"1"`})
	if n := count(`SELECT count(*) FROM audit_log WHERE entity_type = 'project' AND entity_id = $1 AND action = 'project.updated' AND changes @> '{"changed":["status"]}'::jsonb`); n != 1 {
		t.Errorf(`%d project.updated audit rows carrying changed ["status"], want 1`, n)
	}
	if n := count(`SELECT count(*) FROM events_outbox WHERE entity_type = 'project' AND entity_id = $1 AND type = 'project.updated'`); n != 1 {
		t.Errorf("%d project.updated events, want 1", n)
	}
}

// A name-only PUT leaves a stored status the wire does not vocabulary alone:
// projects.status is a free VARCHAR, and a legacy row can hold a value the
// update's storage map does not know ('On Hold '). The write touches the
// status only when the body names it, so a rename never blanks or renames
// what it did not name, and the stored spelling survives byte identical.
func TestUpdate_NameOnlyKeepsOddStoredStatus(t *testing.T) {
	f, _ := newBothFixtures(t)
	created := do(t, f.srv, http.MethodPost, "/api/portal/v1/projects", `{"name":"before"}`, nil)
	id := created.body["id"].(string)
	ctx := context.Background()
	if _, err := f.db.Pool.Exec(ctx, `UPDATE projects SET status = 'On Hold ' WHERE id = $1`, id); err != nil {
		t.Fatalf("seed an odd stored status: %v", err)
	}

	res := do(t, f.srv, http.MethodPut, "/api/portal/v1/projects/"+id, `{"name":"after"}`, map[string]string{"If-Match": `"1"`})
	if res.status != http.StatusOK {
		t.Fatalf("name-only update = %d %s, want 200", res.status, res.raw)
	}
	if res.body["name"] != "after" {
		t.Errorf("name after the update = %v, want after", res.body["name"])
	}
	var stored string
	if err := f.db.Pool.QueryRow(ctx, `SELECT status FROM projects WHERE id = $1`, id).Scan(&stored); err != nil {
		t.Fatal(err)
	}
	if stored != "On Hold " {
		t.Errorf("stored status after a name-only update = %q, want \"On Hold \" untouched", stored)
	}
}

// The list's status filter matches case insensitively, as the read does:
// the column is a free VARCHAR and rows can be stored Active, active or
// COMPLETED, all of which read back lowercase. An exact-match filter would
// show one spelling of a status and hide another of the same one.
func TestList_StatusFilterMatchesCaseInsensitively(t *testing.T) {
	f, _ := newBothFixtures(t)
	ctx := context.Background()
	names := map[string]string{
		"Active":    "title case",
		"active":    "lower case",
		"COMPLETED": "shouted done",
	}
	for stored, name := range names {
		created := do(t, f.srv, http.MethodPost, "/api/portal/v1/projects", `{"name":"`+name+`"}`, nil)
		id := created.body["id"].(string)
		if _, err := f.db.Pool.Exec(ctx, `UPDATE projects SET status = $1 WHERE id = $2`, stored, id); err != nil {
			t.Fatalf("seed a %s row: %v", stored, err)
		}
	}

	countNamed := func(status string) int {
		res := do(t, f.srv, http.MethodGet, "/api/portal/v1/projects?status="+status+"&include=total", "", nil)
		if res.status != http.StatusOK {
			t.Fatalf("list status=%s = %d %s", status, res.status, res.raw)
		}
		if got := res.body["total"]; got != float64(len(res.body["items"].([]any))) {
			t.Errorf("status=%s total = %v but %d items served", status, got, len(res.body["items"].([]any)))
		}
		return len(res.body["items"].([]any))
	}
	if n := countNamed("active"); n != 2 {
		t.Errorf("status=active served %d projects, want 2 (Active and active both read back active)", n)
	}
	if n := countNamed("completed"); n != 1 {
		t.Errorf("status=completed served %d projects, want 1 (COMPLETED reads back completed)", n)
	}
}

// Idempotency through the portal layer: the same create twice with one key
// returns the first response and makes one row; the same key with another
// body is 422.
func TestCreate_IdempotentReplay(t *testing.T) {
	f, _ := newBothFixtures(t)
	key := map[string]string{"Idempotency-Key": "proj-wire-" + uuid.NewString()}
	first := do(t, f.srv, http.MethodPost, "/api/portal/v1/projects", `{"name":"once"}`, key)
	if first.status != http.StatusCreated {
		t.Fatalf("first = %d %s", first.status, first.raw)
	}
	second := do(t, f.srv, http.MethodPost, "/api/portal/v1/projects", `{"name":"once"}`, key)
	if second.status != http.StatusCreated || second.header.Get("Idempotency-Replayed") != "true" {
		t.Fatalf("replay = %d %s", second.status, second.raw)
	}
	if second.body["id"] != first.body["id"] {
		t.Errorf("replay id = %v, want the first %v", second.body["id"], first.body["id"])
	}
	var n int
	if err := f.db.Pool.QueryRow(context.Background(),
		`SELECT count(*) FROM projects WHERE customer_id = $1 AND name = 'once'`, f.customer).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Errorf("%d rows after a replayed create, want 1", n)
	}
	if res := do(t, f.srv, http.MethodPost, "/api/portal/v1/projects", `{"name":"different"}`, key); res.status != http.StatusUnprocessableEntity {
		t.Errorf("the same key with another body = %d, want 422", res.status)
	}
}
