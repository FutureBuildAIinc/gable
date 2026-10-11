// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package crm_test

// The crm module on the wire contract (ADR 0001), tested end to end: a real
// Postgres, the real handler on a real mux behind the real idempotency
// middleware, requests as JSON and responses read back as JSON. Nothing here
// touches a module type, so each test states a wire fact.

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gablelbm/gable/internal/crm"
	"github.com/gablelbm/gable/internal/testutil"
	"github.com/gablelbm/gable/pkg/audit"
	"github.com/gablelbm/gable/pkg/branchctx"
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
	branch   uuid.UUID
	otherBr  uuid.UUID
	prefix   string
}

// newFixture builds the module the way serve does (repository, service with
// the outbox and the database as transaction runner and the audit logger,
// handler) behind the global idempotency layer. A request header X-Test-Branch
// puts a branch wall on the request's context, as the branch middleware does.
func newFixture(t *testing.T) *fixture {
	t.Helper()
	t.Setenv("AUTH_MODE", "dev")
	db := testutil.RequireDB(t)
	f := &fixture{t: t, db: db, prefix: "WIRE-" + uuid.NewString()[:8] + "-"}
	ctx := context.Background()
	if err := db.Pool.QueryRow(ctx,
		`SELECT value::uuid FROM system_settings WHERE key = 'default_branch_id'`).Scan(&f.branch); err != nil {
		t.Fatal(err)
	}
	f.customer = f.seedCustomer(f.branch, "A")
	f.otherBr = uuid.New()
	if _, err := db.Pool.Exec(ctx,
		`INSERT INTO locations (id, type, code, name) VALUES ($1, 'BRANCH', $2, $3)`,
		f.otherBr, "wb-"+f.otherBr.String()[:8], "wire branch "+f.otherBr.String()[:8]); err != nil {
		t.Fatal(err)
	}
	f.other = f.seedCustomer(f.otherBr, "B")

	svc := crm.NewService(crm.NewRepository(db)).
		WithOutbox(outbox.NewWriter(db, "")).
		WithTxRunner(db).
		WithAudit(audit.NewLogger(db))
	mux := http.NewServeMux()
	crm.NewHandler(svc).RegisterRoutes(mux)
	wallMux := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		bc := &branchctx.Context{UserSub: r.Header.Get("X-Test-Sub"), IsAdmin: r.Header.Get("X-Test-Admin") == "1"}
		if b := r.Header.Get("X-Test-Branch"); b != "" {
			id := uuid.MustParse(b)
			bc.BranchID = &id
		}
		mux.ServeHTTP(w, r.WithContext(branchctx.With(r.Context(), bc)))
	})
	f.srv = httptest.NewServer(middleware.Idempotency(db)(wallMux))

	t.Cleanup(func() {
		f.srv.Close()
		c := context.Background()
		for _, id := range []uuid.UUID{f.customer, f.other} {
			_, _ = db.Pool.Exec(c, `DELETE FROM events_outbox WHERE entity_type = 'activity' AND entity_id IN (SELECT id FROM crm_activities WHERE customer_id = $1)`, id)
			_, _ = db.Pool.Exec(c, `DELETE FROM audit_log WHERE entity_type = 'activity' AND entity_id IN (SELECT id FROM crm_activities WHERE customer_id = $1)`, id)
			_, _ = db.Pool.Exec(c, `DELETE FROM crm_activities WHERE customer_id = $1`, id)
			_, _ = db.Pool.Exec(c, `DELETE FROM customer_branches WHERE customer_id = $1`, id)
			_, _ = db.Pool.Exec(c, `DELETE FROM customers WHERE id = $1`, id)
		}
		_, _ = db.Pool.Exec(c, `DELETE FROM locations WHERE id = $1`, f.otherBr)
	})
	return f
}

func (f *fixture) seedCustomer(branch uuid.UUID, tag string) uuid.UUID {
	f.t.Helper()
	id := uuid.New()
	ctx := context.Background()
	if _, err := f.db.Pool.Exec(ctx,
		`INSERT INTO customers (id, name, account_number, primary_branch_id)
		 VALUES ($1, $2, $3, $4)`, id, "Wire "+tag+" "+id.String()[:8], f.prefix+tag+id.String()[:8], branch); err != nil {
		f.t.Fatal(err)
	}
	if _, err := f.db.Pool.Exec(ctx,
		`INSERT INTO customer_branches (customer_id, branch_id) VALUES ($1, $2)`, id, branch); err != nil {
		f.t.Fatal(err)
	}
	return id
}

type resp struct {
	status int
	header http.Header
	body   map[string]any
	raw    []byte
}

func (f *fixture) do(t *testing.T, method, path, body string, hdr map[string]string) resp {
	t.Helper()
	var rdr io.Reader
	if body != "" {
		rdr = bytes.NewReader([]byte(body))
	}
	req, err := http.NewRequest(method, f.srv.URL+path, rdr)
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

func (f *fixture) createActivity(t *testing.T, desc string, hdr map[string]string) resp {
	t.Helper()
	return f.do(t, http.MethodPost, "/api/v1/customers/"+f.customer.String()+"/activities",
		`{"activity_type":"call","description":"`+desc+`"}`, hdr)
}

// The create shape: lowercase status vocabulary, optional fields present as
// null, revision and the ETag, Location, timestamps at microsecond precision.
func TestCreate_Shape(t *testing.T) {
	f := newFixture(t)
	res := f.createActivity(t, "first contact", nil)
	if res.status != http.StatusCreated {
		t.Fatalf("create = %d %s", res.status, res.raw)
	}
	if loc := res.header.Get("Location"); !strings.HasPrefix(loc, "/api/v1/activities/") {
		t.Errorf("Location = %q", loc)
	}
	if etag := res.header.Get("ETag"); etag != `"1"` {
		t.Errorf("ETag = %q, want \"1\"", etag)
	}
	if res.body["activity_type"] != "call" {
		t.Errorf("activity_type = %v, want call", res.body["activity_type"])
	}
	if v, ok := res.body["contact_id"]; !ok || v != nil {
		t.Errorf("contact_id = %v (present %v), want null present", v, ok)
	}
	if v, ok := res.body["logged_by"]; !ok || v != nil {
		t.Errorf("logged_by = %v (present %v), want null present", v, ok)
	}
	if res.body["revision"] != float64(1) {
		t.Errorf("revision = %v, want 1", res.body["revision"])
	}
	for _, field := range []string{"activity_date", "created_at", "updated_at"} {
		s, _ := res.body[field].(string)
		if !strings.HasSuffix(s, "Z") || !strings.Contains(s, ".") {
			t.Errorf("%s = %q, want an RFC 3339 UTC timestamp with a fraction", field, s)
		}
	}
	if _, unexpected := res.body["customer_id"].(string); !unexpected {
		t.Error("customer_id missing from the response")
	}
}

// One 400 with every offending field, full paths, and a body the route
// cannot consume is bad_request; an unknown body field is refused.
func TestCreate_FieldValidation(t *testing.T) {
	f := newFixture(t)
	res := f.do(t, http.MethodPost, "/api/v1/customers/"+f.customer.String()+"/activities",
		`{"activity_type":"CALL","description":"  ","contact_id":"not-a-uuid","customer_id":"00000000-0000-0000-0000-000000000001"}`, nil)
	if res.status != http.StatusBadRequest {
		t.Fatalf("validation = %d %s", res.status, res.raw)
	}
	env := res.body["error"].(map[string]any)
	if env["code"] != "validation_failed" {
		t.Errorf("code = %v", env["code"])
	}
	details := env["details"].([]any)
	fields := map[string]bool{}
	for _, d := range details {
		fields[d.(map[string]any)["field"].(string)] = true
	}
	for _, want := range []string{"activity_type", "description", "contact_id", "customer_id"} {
		if !fields[want] {
			t.Errorf("details = %v, want a %s entry", details, want)
		}
	}
	if meta, ok := res.body["meta"].(map[string]any); !ok || meta["request_id"] == nil {
		t.Error("meta.request_id missing")
	}

	res = f.do(t, http.MethodPost, "/api/v1/customers/"+f.customer.String()+"/activities", `not-json`, nil)
	if res.status != http.StatusBadRequest || res.body["error"].(map[string]any)["code"] != "bad_request" {
		t.Errorf("an unparseable body = %d %s, want 400 bad_request", res.status, res.raw)
	}
	res = f.do(t, http.MethodPost, "/api/v1/customers/"+f.customer.String()+"/activities",
		`{"activity_type":"call","description":"x","smoke":"signal"}`, nil)
	if res.status != http.StatusBadRequest {
		t.Errorf("an unknown body field = %d, want 400", res.status)
	}
	// The description bound is characters, not bytes: 4000 multi-byte
	// letters pass.
	res = f.do(t, http.MethodPost, "/api/v1/customers/"+f.customer.String()+"/activities",
		`{"activity_type":"call","description":"`+strings.Repeat("é", 4000)+`"}`, nil)
	if res.status != http.StatusCreated {
		t.Errorf("a 4000 character description of multi-byte letters = %d %s, want 201", res.status, res.raw)
	}
}

// The list: the envelope, a filter that filters, an unknown parameter
// refused, an uppercase filter value refused, the cursor walks every row
// once, limit validated, include=total, an empty page is [].
func TestList_EnvelopeFiltersCursor(t *testing.T) {
	f := newFixture(t)
	f.createActivity(t, "one", nil)
	f.createActivity(t, "two", nil)
	f.do(t, http.MethodPost, "/api/v1/customers/"+f.customer.String()+"/activities",
		`{"activity_type":"note","description":"three"}`, nil)

	base := "/api/v1/customers/" + f.customer.String() + "/activities"
	res := f.do(t, http.MethodGet, base+"?activity_type=call&limit=1", "", nil)
	if res.status != http.StatusOK {
		t.Fatalf("list = %d %s", res.status, res.raw)
	}
	items := res.body["items"].([]any)
	if len(items) != 1 || items[0].(map[string]any)["description"] != "two" {
		t.Errorf("filtered items = %v, want the newest call only", items)
	}
	if res.body["limit"] != float64(1) {
		t.Errorf("limit echo = %v", res.body["limit"])
	}
	next, _ := res.body["next_cursor"].(string)
	if next == "" {
		t.Fatal("no next_cursor with another page present")
	}
	// Walk every row once with the cursor.
	seen := map[string]bool{}
	cursor := ""
	for {
		q := base + "?activity_type=call&limit=1"
		if cursor != "" {
			q += "&cursor=" + cursor
		}
		page := f.do(t, http.MethodGet, q, "", nil)
		for _, it := range page.body["items"].([]any) {
			id := it.(map[string]any)["id"].(string)
			if seen[id] {
				t.Fatalf("activity %s served twice", id)
			}
			seen[id] = true
		}
		nc, _ := page.body["next_cursor"].(string)
		if nc == "" {
			break
		}
		cursor = nc
	}
	if len(seen) != 2 {
		t.Errorf("the cursor walk served %d activities, want 2", len(seen))
	}

	// A cursor minted for another ordering is refused.
	if res = f.do(t, http.MethodGet, base+"?cursor=garbage", "", nil); res.status != http.StatusBadRequest {
		t.Errorf("a malformed cursor = %d, want 400", res.status)
	}
	if res = f.do(t, http.MethodGet, base+"?limit=0", "", nil); res.status != http.StatusBadRequest {
		t.Errorf("limit 0 = %d, want 400", res.status)
	}
	if res = f.do(t, http.MethodGet, base+"?activity_type=CALL", "", nil); res.status != http.StatusBadRequest {
		t.Errorf("an uppercase filter value = %d, want 400", res.status)
	}
	if res = f.do(t, http.MethodGet, base+"?status=sent", "", nil); res.status != http.StatusBadRequest {
		t.Errorf("an unknown parameter = %d, want 400", res.status)
	} else if code := res.body["error"].(map[string]any)["code"]; code != "unsupported_query_parameter" {
		t.Errorf("code = %v, want unsupported_query_parameter", code)
	}
	res = f.do(t, http.MethodGet, base+"?include=total&activity_type=call", "", nil)
	if res.body["total"] != float64(2) {
		t.Errorf("include=total = %v, want 2", res.body["total"])
	}

	res = f.do(t, http.MethodGet, "/api/v1/customers/"+f.other.String()+"/activities", "", nil)
	if raw := string(res.raw); !strings.Contains(raw, `"items":[]`) {
		t.Errorf("an empty page = %s, want items []", raw)
	}
}

// Revision: 428 without a precondition, 409 stale_revision, If-Match strong
// and weak, header and body disagreeing, the new revision and ETag on
// success.
func TestUpdate_Revision(t *testing.T) {
	f := newFixture(t)
	created := f.createActivity(t, "to edit", nil)
	id := created.body["id"].(string)
	body := `{"activity_type":"note","description":"edited"}`

	if res := f.do(t, http.MethodPut, "/api/v1/activities/"+id, body, nil); res.status != http.StatusPreconditionRequired {
		t.Errorf("no precondition = %d, want 428", res.status)
	}
	if res := f.do(t, http.MethodPut, "/api/v1/activities/"+id, body, map[string]string{"If-Match": `"9"`}); res.status != http.StatusConflict {
		t.Errorf("stale If-Match = %d, want 409", res.status)
	} else if code := res.body["error"].(map[string]any)["code"]; code != "stale_revision" {
		t.Errorf("code = %v, want stale_revision", code)
	}
	if res := f.do(t, http.MethodPut, "/api/v1/activities/"+id, body, map[string]string{"If-Match": `W/"1"`}); res.status != http.StatusOK {
		t.Errorf("weak If-Match = %d, want 200", res.status)
	}
	// The weak write moved the revision to 2.
	if res := f.do(t, http.MethodPut, "/api/v1/activities/"+id, body, map[string]string{"If-Match": `"1"`}); res.status != http.StatusConflict {
		t.Errorf("an If-Match behind the body = %d, want 409", res.status)
	}
	res := f.do(t, http.MethodPut, "/api/v1/activities/"+id,
		`{"activity_type":"note","description":"again","revision":2}`, nil)
	if res.status != http.StatusOK {
		t.Fatalf("body revision = %d %s, want 200", res.status, res.raw)
	}
	if res.body["revision"] != float64(3) || res.header.Get("ETag") != `"3"` {
		t.Errorf("after write revision = %v, ETag = %q; want 3", res.body["revision"], res.header.Get("ETag"))
	}
	if res := f.do(t, http.MethodPut, "/api/v1/activities/"+id,
		`{"activity_type":"note","description":"clash","revision":9}`, map[string]string{"If-Match": `"3"`}); res.status != http.StatusBadRequest {
		t.Errorf("header and body disagreeing = %d, want 400", res.status)
	}

	// The update answers the row, not the echo of the body: omitted fields
	// read back their stored values, customer_id included.
	res = f.do(t, http.MethodGet, "/api/v1/activities/"+id, "", nil)
	if res.body["customer_id"] != f.customer.String() {
		t.Errorf("customer_id after update = %v", res.body["customer_id"])
	}

	// The delete takes the same precondition, by If-Match alone.
	if res := f.do(t, http.MethodDelete, "/api/v1/activities/"+id, "", nil); res.status != http.StatusPreconditionRequired {
		t.Errorf("delete without precondition = %d, want 428", res.status)
	}
	if res := f.do(t, http.MethodDelete, "/api/v1/activities/"+id, "", map[string]string{"If-Match": `"3"`}); res.status != http.StatusNoContent {
		t.Errorf("delete = %d, want 204", res.status)
	}
	if res := f.do(t, http.MethodGet, "/api/v1/activities/"+id, "", nil); res.status != http.StatusNotFound {
		t.Errorf("get after delete = %d, want 404", res.status)
	}
}

// The branch wall on every route that carries a path id: a second branch's
// request for the first branch's activity is a 404, and a create on an
// invisible customer is a 404.
func TestBranchWall_OnEveryRoute(t *testing.T) {
	f := newFixture(t)
	created := f.createActivity(t, "walled", nil)
	id := created.body["id"].(string)

	for _, r := range []struct {
		method, path, body string
	}{
		{http.MethodGet, "/api/v1/activities/" + id, ""},
		{http.MethodPut, "/api/v1/activities/" + id, `{"activity_type":"note","description":"x"}`},
		{http.MethodDelete, "/api/v1/activities/" + id, ""},
	} {
		h := map[string]string{"X-Test-Branch": f.otherBr.String()}
		if r.method != http.MethodGet {
			h["If-Match"] = `"1"`
		}
		res := f.do(t, r.method, r.path, r.body, h)
		if res.status != http.StatusNotFound {
			t.Errorf("%s behind the wall = %d, want 404", r.method, res.status)
		}
	}
	if res := f.do(t, http.MethodPost, "/api/v1/customers/"+f.customer.String()+"/activities",
		`{"activity_type":"call","description":"no"}`, map[string]string{"X-Test-Branch": f.otherBr.String()}); res.status != http.StatusNotFound {
		t.Errorf("create behind the wall = %d, want 404", res.status)
	}
	if res := f.do(t, http.MethodGet, "/api/v1/customers/"+f.customer.String()+"/activities",
		"", map[string]string{"X-Test-Branch": f.otherBr.String()}); res.status != http.StatusOK || len(res.body["items"].([]any)) != 0 {
		t.Errorf("list behind the wall = %d %s, want an empty page", res.status, res.raw)
	}
}

// The mutation writes its audit row and its event, in one transaction.
func TestMutation_AuditRowAndEvent(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	created := f.createActivity(t, "audited", nil)
	id := created.body["id"].(string)

	count := func(sql string) int {
		f.t.Helper()
		var n int
		if err := f.db.Pool.QueryRow(ctx, sql, id).Scan(&n); err != nil {
			f.t.Fatal(err)
		}
		return n
	}
	if n := count(`SELECT count(*) FROM audit_log WHERE entity_type = 'activity' AND entity_id = $1 AND action = 'activity.created'`); n != 1 {
		t.Errorf("%d activity.created audit rows, want 1", n)
	}
	if n := count(`SELECT count(*) FROM events_outbox WHERE entity_type = 'activity' AND entity_id = $1 AND type = 'activity.created'`); n != 1 {
		t.Errorf("%d activity.created events, want 1", n)
	}

	f.do(t, http.MethodPut, "/api/v1/activities/"+id,
		`{"activity_type":"note","description":"after","revision":1}`, nil)
	if n := count(`SELECT count(*) FROM audit_log WHERE entity_type = 'activity' AND entity_id = $1 AND action = 'activity.updated'`); n != 1 {
		t.Errorf("%d activity.updated audit rows, want 1", n)
	}
	if n := count(`SELECT count(*) FROM events_outbox WHERE entity_type = 'activity' AND entity_id = $1 AND type = 'activity.updated'`); n != 1 {
		t.Errorf("%d activity.updated events, want 1", n)
	}
	f.do(t, http.MethodDelete, "/api/v1/activities/"+id, "", map[string]string{"If-Match": `"2"`})
	if n := count(`SELECT count(*) FROM audit_log WHERE entity_type = 'activity' AND entity_id = $1 AND action = 'activity.deleted'`); n != 1 {
		t.Errorf("%d activity.deleted audit rows, want 1", n)
	}
	if n := count(`SELECT count(*) FROM events_outbox WHERE entity_type = 'activity' AND entity_id = $1 AND type = 'activity.deleted'`); n != 1 {
		t.Errorf("%d activity.deleted events, want 1", n)
	}
}

// Idempotency through the existing middleware: the same create twice with
// one key returns the first response and makes one row and one event; the
// same key with another body is 422.
func TestCreate_IdempotentReplay(t *testing.T) {
	f := newFixture(t)
	key := map[string]string{"Idempotency-Key": "crm-wire-" + uuid.NewString()}
	first := f.do(t, http.MethodPost, "/api/v1/customers/"+f.customer.String()+"/activities",
		`{"activity_type":"call","description":"once"}`, key)
	if first.status != http.StatusCreated {
		t.Fatalf("first = %d %s", first.status, first.raw)
	}
	second := f.do(t, http.MethodPost, "/api/v1/customers/"+f.customer.String()+"/activities",
		`{"activity_type":"call","description":"once"}`, key)
	if second.status != http.StatusCreated {
		t.Fatalf("replay = %d %s", second.status, second.raw)
	}
	if second.header.Get("Idempotency-Replayed") != "true" {
		t.Error("the replay is not marked")
	}
	if second.body["id"] != first.body["id"] {
		t.Errorf("replay id = %v, want the first %v", second.body["id"], first.body["id"])
	}
	var n int
	if err := f.db.Pool.QueryRow(context.Background(),
		`SELECT count(*) FROM crm_activities WHERE customer_id = $1 AND description = 'once'`, f.customer).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Errorf("%d rows after a replayed create, want 1", n)
	}
	res := f.do(t, http.MethodPost, "/api/v1/customers/"+f.customer.String()+"/activities",
		`{"activity_type":"call","description":"different"}`, key)
	if res.status != http.StatusUnprocessableEntity {
		t.Errorf("the same key with another body = %d, want 422", res.status)
	}
}

// A malformed path id is a 400 naming id; an unknown activity is a 404 with
// the handler's message.
func TestGet_Errors(t *testing.T) {
	f := newFixture(t)
	res := f.do(t, http.MethodGet, "/api/v1/activities/not-a-uuid", "", nil)
	if res.status != http.StatusBadRequest {
		t.Fatalf("bad id = %d, want 400", res.status)
	}
	env := res.body["error"].(map[string]any)
	d := env["details"].([]any)[0].(map[string]any)
	if d["field"] != "id" {
		t.Errorf("details = %v, want the field id", d)
	}
	res = f.do(t, http.MethodGet, "/api/v1/activities/"+uuid.NewString(), "", nil)
	if res.status != http.StatusNotFound || res.body["error"].(map[string]any)["code"] != "not_found" {
		t.Errorf("unknown activity = %d %s, want 404 not_found", res.status, res.raw)
	}
}
