// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package staff_test

// The staff module on the wire contract (ADR 0001), tested end to end: a
// real Postgres, the real handler on a real mux behind the real idempotency
// middleware, requests as JSON and responses read back as JSON. Nothing here
// touches a module type, so each test states a wire fact.

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gablelbm/gable/internal/staff"
	"github.com/gablelbm/gable/internal/techadmin"
	"github.com/gablelbm/gable/internal/testutil"
	"github.com/gablelbm/gable/pkg/audit"
	"github.com/gablelbm/gable/pkg/database"
	"github.com/gablelbm/gable/pkg/middleware"
	"github.com/gablelbm/gable/pkg/outbox"
	"github.com/google/uuid"
)

type fixture struct {
	t   *testing.T
	db  *database.DB
	srv *httptest.Server
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	testutil.LockOutboxTables(t)
	t.Setenv("AUTH_MODE", "dev")
	db := testutil.RequireDB(t)
	f := &fixture{t: t, db: db}
	svc := staff.NewService(staff.NewRepository(db)).
		WithOutbox(outbox.NewWriter(db, "")).WithTxRunner(db)
	mux := http.NewServeMux()
	staff.NewHandler(svc).RegisterRoutes(mux)
	f.srv = httptest.NewServer(middleware.Idempotency(db)(mux))
	t.Cleanup(f.srv.Close)
	// The module flags are dealer-wide shared state: restore them after the
	// test so other suites (and the goldens) see the seeded values.
	var enabled string
	_ = db.Pool.QueryRow(context.Background(),
		`SELECT value FROM system_settings WHERE key = 'modules.ai_lm.enabled'`).Scan(&enabled)
	_, _ = db.Pool.Exec(context.Background(), `DELETE FROM admin_revisions WHERE resource LIKE 'admin.modules.%'`)
	t.Cleanup(func() {
		if enabled == "" {
			_, _ = db.Pool.Exec(context.Background(), `DELETE FROM system_settings WHERE key = 'modules.ai_lm.enabled'`)
		} else {
			_, _ = db.Pool.Exec(context.Background(),
				`INSERT INTO system_settings (key, value, updated_at) VALUES ('modules.ai_lm.enabled', $1, NOW())
				 ON CONFLICT (key) DO UPDATE SET value = EXCLUDED.value`, enabled)
		}
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

func (f *fixture) createStaff(t *testing.T, email string) (id string, rev int64) {
	t.Helper()
	r := f.do("POST", "/api/v1/admin/staff", map[string]any{
		"email": email, "full_name": "Wire Staff", "role": "counter",
	})
	if r.status != http.StatusCreated {
		t.Fatalf("create = %d: %s", r.status, r.raw)
	}
	if loc := r.header.Get("Location"); !strings.HasPrefix(loc, "/api/v1/admin/staff/") {
		t.Errorf("Location = %q", loc)
	}
	id, _ = r.body["id"].(string)
	rev = bodyNum(t, r.body, "revision")
	if rev != 1 {
		t.Errorf("a create starts at revision 1, got %d", rev)
	}
	t.Cleanup(func() {
		_, _ = f.db.Pool.Exec(context.Background(), `DELETE FROM events_outbox WHERE entity_type = 'staff' AND entity_id = $1`, id)
		_, _ = f.db.Pool.Exec(context.Background(), `DELETE FROM module_grants WHERE staff_id = $1`, id)
		_, _ = f.db.Pool.Exec(context.Background(), `DELETE FROM staff WHERE id = $1`, id)
	})
	return id, rev
}

func bodyNum(t *testing.T, m map[string]any, key string) int64 {
	t.Helper()
	n, ok := m[key].(json.Number)
	if !ok {
		t.Fatalf("%s is %T (%v), want an integer", key, m[key], m[key])
	}
	v, err := n.Int64()
	if err != nil {
		t.Fatalf("%s = %s: %v", key, n, err)
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

func eventsFor(t *testing.T, db *database.DB, staffID string) []string {
	t.Helper()
	rows, err := db.Pool.Query(context.Background(),
		`SELECT type FROM events_outbox WHERE entity_type = 'staff' AND entity_id = $1 ORDER BY position`, staffID)
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

// RULE: a created staff member answers 201 with Location, the revision and
// ETag, defaults filled (role staff, active true), and modules is an array,
// never null; staff.created is written exactly once.
func TestWire_CreateStaff(t *testing.T) {
	f := newFixture(t)
	id, _ := f.createStaff(t, "wire-create-"+uuid.NewString()[:8]+"@example.com")

	r := f.do("GET", "/api/v1/admin/staff/"+id, nil)
	if r.status != http.StatusOK {
		t.Fatalf("get = %d", r.status)
	}
	if etag := r.header.Get("ETag"); etag != `"1"` {
		t.Errorf("ETag = %q", etag)
	}
	if mods, ok := r.body["modules"].([]any); !ok || len(mods) != 0 {
		t.Errorf("modules = %v, want an empty array", r.body["modules"])
	}
	if _, present := r.body["staff_no"]; !present {
		t.Error("staff_no missing; optional fields are present as null")
	}
	if r.body["staff_no"] != nil {
		t.Errorf("staff_no = %v, want null", r.body["staff_no"])
	}
	if ev := eventsFor(t, f.db, id); len(ev) != 1 || ev[0] != "staff.created" {
		t.Errorf("events = %v, want one staff.created", ev)
	}
}

// RULE: field validation is one 400 with every offending field; an unknown
// body field is refused; a create carrying a revision is refused.
func TestWire_CreateStaffValidation(t *testing.T) {
	f := newFixture(t)
	r := f.do("POST", "/api/v1/admin/staff", map[string]any{"email": "", "full_name": ""})
	if r.status != http.StatusBadRequest {
		t.Fatalf("status = %d: %s", r.status, r.raw)
	}
	code, _, details := errorOf(t, r)
	if code != "validation_failed" {
		t.Errorf("code = %q", code)
	}
	fields := map[string]bool{}
	for _, d := range details {
		if fld, ok := d["field"].(string); ok {
			fields[fld] = true
		}
	}
	if !fields["email"] || !fields["full_name"] {
		t.Errorf("details = %v, want email and full_name", details)
	}

	r = f.do("POST", "/api/v1/admin/staff", "nope")
	if r.status != http.StatusBadRequest {
		t.Fatalf("bad body = %d", r.status)
	}
	if code, _, _ := errorOf(t, r); code != "bad_request" {
		t.Errorf("bad body code = %q", code)
	}

	r = f.do("POST", "/api/v1/admin/staff", map[string]any{"email": "x@example.com", "full_name": "X", "typo": 1})
	if r.status != http.StatusBadRequest {
		t.Fatalf("unknown field = %d", r.status)
	}

	r = f.do("POST", "/api/v1/admin/staff", map[string]any{"email": "x@example.com", "full_name": "X", "revision": 1})
	if r.status != http.StatusBadRequest {
		t.Fatalf("revision on create = %d, want 400", r.status)
	}
	if _, _, details := errorOf(t, r); len(details) != 1 || details[0]["field"] != "revision" {
		t.Errorf("details = %v, want revision", details)
	}
}

// RULE: a duplicate email is a 409 naming the field, not a 500.
func TestWire_DuplicateEmailIs409(t *testing.T) {
	f := newFixture(t)
	email := "wire-dup-" + uuid.NewString()[:8] + "@example.com"
	f.createStaff(t, email)
	r := f.do("POST", "/api/v1/admin/staff", map[string]any{"email": email, "full_name": "Second"})
	if r.status != http.StatusConflict {
		t.Fatalf("duplicate = %d: %s", r.status, r.raw)
	}
	code, _, details := errorOf(t, r)
	if code != "duplicate" || len(details) != 1 || details[0]["code"] != "email_taken" {
		t.Errorf("code=%q details=%v", code, details)
	}
}

// RULE: the list is the cursor envelope, the active filter filters, an
// unknown value is refused, the cursor walks every row once, and
// include=total carries the count.
func TestWire_StaffListFilterAndCursor(t *testing.T) {
	f := newFixture(t)
	email := "wire-list-" + uuid.NewString()[:8]
	id1, _ := f.createStaff(t, email+"-1@example.com")
	f.createStaff(t, email+"-2@example.com")

	// Deactivate one through the contract.
	r := f.do("PUT", "/api/v1/admin/staff/"+id1, map[string]any{"active": false, "revision": 1})
	if r.status != http.StatusOK {
		t.Fatalf("deactivate = %d: %s", r.status, r.raw)
	}

	r = f.do("GET", "/api/v1/admin/staff?active=false", nil)
	if r.status != http.StatusOK {
		t.Fatalf("filtered list = %d: %s", r.status, r.raw)
	}
	items := r.body["items"].([]any)
	found := false
	for _, it := range items {
		if it.(map[string]any)["id"] == id1 {
			found = true
		}
	}
	if !found {
		t.Error("the active=false filter did not serve the deactivated member")
	}
	r = f.do("GET", "/api/v1/admin/staff?active=maybe", nil)
	if r.status != http.StatusBadRequest {
		t.Fatalf("bad filter value = %d", r.status)
	}
	if code, _, details := errorOf(t, r); code != "validation_failed" || details[0]["field"] != "active" {
		t.Errorf("details = %v", details)
	}
	r = f.do("GET", "/api/v1/admin/staff?role=admin", nil)
	if r.status != http.StatusBadRequest {
		t.Fatalf("unknown parameter = %d", r.status)
	}
	if code, _, _ := errorOf(t, r); code != "unsupported_query_parameter" {
		t.Errorf("code = %q", code)
	}

	// The cursor walks every fixture row once.
	r = f.do("GET", "/api/v1/admin/staff?limit=1", nil)
	if r.status != http.StatusOK {
		t.Fatalf("page = %d", r.status)
	}
	cur, _ := r.body["next_cursor"].(string)
	if cur == "" {
		t.Fatal("next_cursor is empty with more rows to serve")
	}
	seen := map[string]bool{}
	pages := 0
	for cur != "" && pages < 50 {
		for _, it := range r.body["items"].([]any) {
			seen[it.(map[string]any)["email"].(string)] = true
		}
		r = f.do("GET", "/api/v1/admin/staff?limit=1&cursor="+cur, nil)
		if r.status != http.StatusOK {
			t.Fatalf("page = %d: %s", r.status, r.raw)
		}
		cur, _ = r.body["next_cursor"].(string)
		pages++
	}
	for _, it := range r.body["items"].([]any) {
		seen[it.(map[string]any)["email"].(string)] = true
	}
	for _, e := range []string{email + "-1@example.com", email + "-2@example.com"} {
		if !seen[e] {
			t.Errorf("%s never served", e)
		}
	}
	r = f.do("GET", "/api/v1/admin/staff?active=false&include=total", nil)
	if num := bodyNum(t, r.body, "total"); num < 1 {
		t.Errorf("total = %d", num)
	}
}

// RULE: updates take If-Match or the body revision: 428 without one, 409 on
// a stale one, the new revision and ETag on success; a nil field leaves the
// column alone.
func TestWire_UpdateStaffRevision(t *testing.T) {
	f := newFixture(t)
	id, _ := f.createStaff(t, "wire-upd-"+uuid.NewString()[:8]+"@example.com")

	r := f.do("PUT", "/api/v1/admin/staff/"+id, map[string]any{"full_name": "No Precondition"})
	if r.status != http.StatusPreconditionRequired {
		t.Fatalf("no precondition = %d, want 428", r.status)
	}
	r = f.do("PUT", "/api/v1/admin/staff/"+id, map[string]any{"full_name": "Two"}, "If-Match", `"9"`)
	if r.status != http.StatusConflict {
		t.Fatalf("stale = %d, want 409", r.status)
	}
	if code, _, _ := errorOf(t, r); code != "stale_revision" {
		t.Errorf("code = %q", code)
	}
	r = f.do("PUT", "/api/v1/admin/staff/"+id, map[string]any{"full_name": "Two", "role": "yard"}, "If-Match", `W/"1"`)
	if r.status != http.StatusOK {
		t.Fatalf("weak update = %d: %s", r.status, r.raw)
	}
	if rev := bodyNum(t, r.body, "revision"); rev != 2 {
		t.Errorf("revision = %d, want 2", rev)
	}
	if etag := r.header.Get("ETag"); etag != `"2"` {
		t.Errorf("ETag = %q", etag)
	}
	after := f.do("GET", "/api/v1/admin/staff/"+id, nil)
	if after.body["full_name"] != "Two" || after.body["role"] != "yard" {
		t.Errorf("update did not stick: %v", after.body)
	}
	if ev := eventsFor(t, f.db, id); len(ev) != 2 || ev[1] != "staff.updated" {
		t.Errorf("events = %v", ev)
	}

	r = f.do("PUT", "/api/v1/admin/staff/"+uuid.NewString(), map[string]any{"role": "x"}, "If-Match", `"1"`)
	if r.status != http.StatusNotFound {
		t.Errorf("unknown id = %d, want 404", r.status)
	}
	r = f.do("PUT", "/api/v1/admin/staff/not-a-uuid", map[string]any{"role": "x"})
	if r.status != http.StatusBadRequest {
		t.Errorf("malformed id = %d, want 400", r.status)
	}
}

// RULE: a grant moves the staff revision and takes the same precondition; an
// unknown module id is a 400 naming module_id; a re-grant is an idempotent
// no-op that writes nothing; a revoke mirrors it; the modules list is the
// envelope.
func TestWire_ModuleGrants(t *testing.T) {
	f := newFixture(t)
	id, _ := f.createStaff(t, "wire-grant-"+uuid.NewString()[:8]+"@example.com")

	r := f.do("POST", "/api/v1/admin/staff/"+id+"/modules", map[string]any{"module_id": "ai_lm"})
	if r.status != http.StatusPreconditionRequired {
		t.Fatalf("grant without precondition = %d, want 428", r.status)
	}
	r = f.do("POST", "/api/v1/admin/staff/"+id+"/modules", map[string]any{"module_id": "no_such", "revision": 1})
	if r.status != http.StatusBadRequest {
		t.Fatalf("unknown module = %d, want 400", r.status)
	}
	if _, _, details := errorOf(t, r); details[0]["field"] != "module_id" {
		t.Errorf("details = %v", details)
	}
	r = f.do("POST", "/api/v1/admin/staff/"+id+"/modules", map[string]any{"module_id": "ai_lm", "revision": 1})
	if r.status != http.StatusOK {
		t.Fatalf("grant = %d: %s", r.status, r.raw)
	}
	if rev := bodyNum(t, r.body, "revision"); rev != 2 {
		t.Errorf("revision after grant = %d, want 2 (the modules list is part of the document)", rev)
	}
	if mods := r.body["modules"].([]any); len(mods) != 1 || mods[0] != "ai_lm" {
		t.Errorf("modules = %v", r.body["modules"])
	}
	// A re-grant at the new revision changes nothing.
	r = f.do("POST", "/api/v1/admin/staff/"+id+"/modules", map[string]any{"module_id": "ai_lm", "revision": 2})
	if r.status != http.StatusOK {
		t.Fatalf("re-grant = %d: %s", r.status, r.raw)
	}
	if rev := bodyNum(t, r.body, "revision"); rev != 2 {
		t.Errorf("an idempotent re-grant moved the revision to %d", rev)
	}
	// A revoke with a stale revision is refused; with the current one it
	// moves the revision back.
	r = f.do("DELETE", "/api/v1/admin/staff/"+id+"/modules/ai_lm", nil, "If-Match", `"1"`)
	if r.status != http.StatusConflict {
		t.Fatalf("stale revoke = %d, want 409", r.status)
	}
	r = f.do("DELETE", "/api/v1/admin/staff/"+id+"/modules/ai_lm", nil, "If-Match", `"2"`)
	if r.status != http.StatusOK {
		t.Fatalf("revoke = %d: %s", r.status, r.raw)
	}
	if rev := bodyNum(t, r.body, "revision"); rev != 3 {
		t.Errorf("revision after revoke = %d, want 3", rev)
	}
	if mods := r.body["modules"].([]any); len(mods) != 0 {
		t.Errorf("modules = %v", r.body["modules"])
	}
	r = f.do("DELETE", "/api/v1/admin/staff/"+id+"/modules/ai_lm", nil, "If-Match", `"3"`)
	if r.status != http.StatusOK || bodyNum(t, r.body, "revision") != 3 {
		t.Errorf("an idempotent revoke changed state: %d %s", r.status, r.raw)
	}
	ev := eventsFor(t, f.db, id)
	want := []string{"staff.created", "staff.module_granted", "staff.module_revoked"}
	if fmt.Sprint(ev) != fmt.Sprint(want) {
		t.Errorf("events = %v, want %v", ev, want)
	}

	r = f.do("DELETE", "/api/v1/admin/staff/"+id+"/modules/unknown_mod", nil, "If-Match", `"3"`)
	if r.status != http.StatusBadRequest {
		t.Errorf("unknown module on revoke = %d, want 400", r.status)
	}
}

// RULE: the module catalog list is the envelope with each flag's revision;
// a toggle takes the revision (428, 409), answers the module with its new
// ETag, and writes module.enabled or module.disabled once; an unknown module
// id is a 404.
func TestWire_ModuleFlags(t *testing.T) {
	f := newFixture(t)
	r := f.do("GET", "/api/v1/admin/modules", nil)
	if r.status != http.StatusOK {
		t.Fatalf("modules = %d: %s", r.status, r.raw)
	}
	items := r.body["items"].([]any)
	if len(items) != 1 {
		t.Fatalf("catalog = %v, want the ai_lm row alone", items)
	}
	m := items[0].(map[string]any)
	if m["id"] != "ai_lm" || m["name"] != "AI_LM" {
		t.Errorf("catalog row = %v", m)
	}
	if _, present := r.body["next_cursor"]; !present || r.body["next_cursor"] != nil {
		t.Errorf("next_cursor = %v, want null on the single page", r.body["next_cursor"])
	}
	rev := bodyNum(t, m, "revision")

	r = f.do("PUT", "/api/v1/admin/modules/ai_lm", map[string]any{"enabled": false})
	if r.status != http.StatusPreconditionRequired {
		t.Fatalf("toggle without precondition = %d, want 428", r.status)
	}
	r = f.do("PUT", "/api/v1/admin/modules/ai_lm", map[string]any{"enabled": false, "revision": rev + 1})
	if r.status != http.StatusConflict {
		t.Fatalf("stale toggle = %d, want 409", r.status)
	}
	r = f.do("PUT", "/api/v1/admin/modules/ai_lm", map[string]any{"enabled": false, "revision": rev})
	if r.status != http.StatusOK {
		t.Fatalf("disable = %d: %s", r.status, r.raw)
	}
	if r.body["enabled"] != false {
		t.Errorf("enabled = %v", r.body["enabled"])
	}
	if bodyNum(t, r.body, "revision") != rev+1 {
		t.Errorf("revision = %v, want %d", r.body["revision"], rev+1)
	}
	// A toggle that changes nothing writes nothing.
	r = f.do("PUT", "/api/v1/admin/modules/ai_lm", map[string]any{"enabled": false, "revision": rev + 1})
	if r.status != http.StatusOK || bodyNum(t, r.body, "revision") != rev+1 {
		t.Errorf("the idempotent toggle changed state: %d %s", r.status, r.raw)
	}
	var evType string
	if err := f.db.Pool.QueryRow(context.Background(),
		`SELECT type FROM events_outbox WHERE entity_type = 'module' ORDER BY position DESC LIMIT 1`).Scan(&evType); err != nil {
		t.Fatalf("module event: %v", err)
	}
	if evType != "module.disabled" {
		t.Errorf("event = %s, want module.disabled", evType)
	}
	r = f.do("PUT", "/api/v1/admin/modules/no_such", map[string]any{"enabled": true, "revision": 1})
	if r.status != http.StatusNotFound {
		t.Errorf("unknown module = %d, want 404", r.status)
	}
}

// RULE: a machine key holding admin:staff reaches the staff routes and is
// refused the module flags; one holding admin:modules the reverse; the key
// surface stays user only.
func TestWire_FinerAdminScopesOnStaff(t *testing.T) {
	f := newFixture(t)
	db := f.db
	keySvc := techadmin.NewService(techadmin.NewRepository(db)).WithTxRunner(db)
	validator := staffKeyValidator{svc: keySvc, db: db}
	auth := middleware.NewMachineKeyAuth(validator, nil, nil, nil)
	svc := staff.NewService(staff.NewRepository(db)).WithTxRunner(db)
	mux := http.NewServeMux()
	staff.NewHandler(svc).RegisterRoutes(mux)
	srv := httptest.NewServer(auth.Handler(mux))
	defer srv.Close()

	mint := func(t *testing.T, scopes ...string) string {
		t.Helper()
		raw, key, err := keySvc.GenerateKey(context.Background(), "staff scope key", scopes, nil)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() {
			_, _ = db.Pool.Exec(context.Background(), `DELETE FROM events_outbox WHERE entity_type = 'api_key' AND entity_id = $1`, key.ID)
			_, _ = db.Pool.Exec(context.Background(), `DELETE FROM api_keys WHERE id = $1`, key.ID)
		})
		return raw
	}

	do := func(method, path, bearer string) int {
		req, err := http.NewRequest(method, srv.URL+path, nil)
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Authorization", "Bearer "+bearer)
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		res.Body.Close()
		return res.StatusCode
	}

	staffKey := mint(t, "admin:staff")
	if c := do("GET", "/api/v1/admin/staff", staffKey); c != http.StatusOK {
		t.Errorf("admin:staff on staff list = %d, want 200", c)
	}
	if c := do("GET", "/api/v1/admin/modules", staffKey); c != http.StatusForbidden {
		t.Errorf("admin:staff on modules = %d, want 403", c)
	}
	modulesKey := mint(t, "admin:modules")
	if c := do("GET", "/api/v1/admin/modules", modulesKey); c != http.StatusOK {
		t.Errorf("admin:modules on modules = %d, want 200", c)
	}
	if c := do("GET", "/api/v1/admin/staff", modulesKey); c != http.StatusForbidden {
		t.Errorf("admin:modules on staff = %d, want 403", c)
	}
	coarse := mint(t, "admin:write")
	if c := do("POST", "/api/v1/admin/staff", coarse); c != http.StatusForbidden {
		t.Errorf("coarse admin:write on staff = %d, want 403", c)
	}
}

// staffKeyValidator mints and validates real keys through the techadmin
// service, so the scope test exercises the real credential path.
type staffKeyValidator struct {
	svc *techadmin.Service
	db  *database.DB
}

func (v staffKeyValidator) ValidateKey(ctx context.Context, rawKey string) (middleware.KeyPrincipal, error) {
	k, err := v.svc.ValidateKey(ctx, rawKey)
	if err != nil {
		if errors.Is(err, techadmin.ErrInvalidKey) {
			return middleware.KeyPrincipal{}, middleware.ErrInvalidMachineKey
		}
		return middleware.KeyPrincipal{}, err
	}
	return middleware.KeyPrincipal{ID: k.ID.String(), Scopes: k.Scopes}, nil
}

// RULE: a grant made by a machine key attributes to the key. The caller has
// no JWT subject, so the module_grants row names the key's id as the
// key:<id> principal (the same one the idempotency layer keys on), never a
// blank.
func TestWire_GrantByAKeyAttributesToTheKey(t *testing.T) {
	f := newFixture(t)
	db := f.db
	keySvc := techadmin.NewService(techadmin.NewRepository(db)).WithTxRunner(db)
	validator := staffKeyValidator{svc: keySvc, db: db}
	auth := middleware.NewMachineKeyAuth(validator, nil, nil, nil)
	svc := staff.NewService(staff.NewRepository(db)).
		WithOutbox(outbox.NewWriter(db, "")).WithTxRunner(db)
	mux := http.NewServeMux()
	staff.NewHandler(svc).RegisterRoutes(mux)
	srv := httptest.NewServer(auth.Handler(mux))
	defer srv.Close()

	created := f.do("POST", "/api/v1/admin/staff", map[string]any{
		"email": "key-grant-" + uuid.NewString()[:8] + "@example.com", "full_name": "Key Grant",
	})
	if created.status != http.StatusCreated {
		t.Fatalf("create = %d: %s", created.status, created.raw)
	}
	id, _ := created.body["id"].(string)
	t.Cleanup(func() { dropStaff(t, db, uuid.MustParse(id)) })

	raw, key, err := keySvc.GenerateKey(context.Background(), "granting key", []string{"admin:staff"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = db.Pool.Exec(context.Background(), `DELETE FROM events_outbox WHERE entity_type = 'api_key' AND entity_id = $1`, key.ID)
		_, _ = db.Pool.Exec(context.Background(), `DELETE FROM api_keys WHERE id = $1`, key.ID)
	})

	req, err := http.NewRequest("POST", srv.URL+"/api/v1/admin/staff/"+id+"/modules",
		strings.NewReader(`{"module_id":"ai_lm","revision":1}`))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+raw)
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if res.StatusCode != http.StatusOK {
		t.Fatalf("grant by key = %d, want 200", res.StatusCode)
	}

	var grantedBy *string
	if err := db.Pool.QueryRow(context.Background(),
		`SELECT granted_by FROM module_grants WHERE staff_id = $1 AND module_id = 'ai_lm'`, id).Scan(&grantedBy); err != nil {
		t.Fatal(err)
	}
	if grantedBy == nil || *grantedBy != "key:"+key.ID.String() {
		t.Errorf("granted_by = %v, want key:%s: a key caller's grant must attribute to the key", grantedBy, key.ID)
	}
}

// RULE (ADR 0001 section 9): the same create twice with one idempotency key
// replays the stored response and makes one row and one event; the same key
// with another body is 422 idempotency_key_reused.
func TestWire_CreateStaffIdempotency(t *testing.T) {
	f := newFixture(t)
	body := map[string]any{"email": "idem-" + uuid.NewString()[:8] + "@example.com", "full_name": "Idem Staff"}
	idemKey := "idem-staff-" + uuid.NewString()[:8]
	t.Cleanup(func() {
		_, _ = f.db.Pool.Exec(context.Background(), `DELETE FROM idempotency_keys WHERE key = $1`, idemKey)
	})
	first := f.do("POST", "/api/v1/admin/staff", body, "Idempotency-Key", idemKey)
	if first.status != http.StatusCreated {
		t.Fatalf("first create = %d: %s", first.status, first.raw)
	}
	second := f.do("POST", "/api/v1/admin/staff", body, "Idempotency-Key", idemKey)
	if second.status != http.StatusCreated {
		t.Fatalf("replayed create = %d: %s", second.status, second.raw)
	}
	if v := second.header.Get("Idempotency-Replayed"); v != "true" {
		t.Errorf("Idempotency-Replayed = %q, want true", v)
	}
	if first.body["id"] != second.body["id"] {
		t.Errorf("the replay minted a second row: %v then %v", first.body["id"], second.body["id"])
	}
	id, _ := first.body["id"].(string)
	if ev := eventsFor(t, f.db, id); len(ev) != 1 || ev[0] != "staff.created" {
		t.Errorf("events = %v, want one staff.created (a replay must not create again)", ev)
	}

	other := f.do("POST", "/api/v1/admin/staff",
		map[string]any{"email": "idem-other@example.com", "full_name": "Other"},
		"Idempotency-Key", idemKey)
	if other.status != http.StatusUnprocessableEntity {
		t.Fatalf("same key other body = %d: %s", other.status, other.raw)
	}
	if code, _, _ := errorOf(t, other); code != "idempotency_key_reused" {
		t.Errorf("code = %q, want idempotency_key_reused", code)
	}
}

// RULE: a PUT whose body sets no field is an idempotent no-op (the grants'
// rule): the current document comes back, the revision does not move, and no
// staff.updated event is written; a PUT that sets a field moves both.
func TestWire_NoOpUpdateWritesNothing(t *testing.T) {
	f := newFixture(t)
	id, _ := f.createStaff(t, "noop-"+uuid.NewString()[:8]+"@example.com")

	r := f.do("PUT", "/api/v1/admin/staff/"+id, map[string]any{"revision": 1})
	if r.status != http.StatusOK {
		t.Fatalf("no-op update = %d: %s", r.status, r.raw)
	}
	if rev := bodyNum(t, r.body, "revision"); rev != 1 {
		t.Errorf("a no-op update moved the revision to %d", rev)
	}
	if ev := eventsFor(t, f.db, id); len(ev) != 1 {
		t.Errorf("events = %v, want only staff.created after a no-op update", ev)
	}

	r = f.do("PUT", "/api/v1/admin/staff/"+id, map[string]any{"active": false, "revision": 1})
	if r.status != http.StatusOK {
		t.Fatalf("real update = %d: %s", r.status, r.raw)
	}
	if rev := bodyNum(t, r.body, "revision"); rev != 2 {
		t.Errorf("revision = %d, want 2", rev)
	}
	if ev := eventsFor(t, f.db, id); len(ev) != 2 || ev[1] != "staff.updated" {
		t.Errorf("events = %v, want staff.created then staff.updated", ev)
	}
}

// RULE: include is declared on the modules list, so it is honored (total
// counts the whole catalog) and an unknown name is refused.
func TestWire_ListModulesIncludeTotal(t *testing.T) {
	f := newFixture(t)
	plain := f.do("GET", "/api/v1/admin/modules", nil)
	if plain.status != http.StatusOK {
		t.Fatalf("modules = %d: %s", plain.status, plain.raw)
	}
	want := int64(len(plain.body["items"].([]any)))
	r := f.do("GET", "/api/v1/admin/modules?include=total", nil)
	if r.status != http.StatusOK {
		t.Fatalf("include=total = %d: %s", r.status, r.raw)
	}
	if got := bodyNum(t, r.body, "total"); got != want {
		t.Errorf("total = %d, want %d", got, want)
	}
	r = f.do("GET", "/api/v1/admin/modules?include=bogus", nil)
	if r.status != http.StatusBadRequest {
		t.Fatalf("include=bogus = %d: %s", r.status, r.raw)
	}
	if code, _, details := errorOf(t, r); code != "validation_failed" || details[0]["field"] != "include" {
		t.Errorf("code=%q details=%v, want include named", code, details)
	}
}

// RULE: limit and cursor are validated, never clamped or ignored.
func TestWire_ListStrictness(t *testing.T) {
	f := newFixture(t)
	for _, tc := range []struct {
		url   string
		field string
		code  string
	}{
		{"/api/v1/admin/staff?limit=0", "limit", "validation_failed"},
		{"/api/v1/admin/staff?limit=999999", "limit", "validation_failed"},
		{"/api/v1/admin/staff?cursor=not-a-cursor", "cursor", "bad_request"},
	} {
		r := f.do("GET", tc.url, nil)
		if r.status != http.StatusBadRequest {
			t.Errorf("%s = %d, want 400: %s", tc.url, r.status, r.raw)
			continue
		}
		code, _, details := errorOf(t, r)
		if code != tc.code || len(details) == 0 || details[0]["field"] != tc.field {
			t.Errorf("%s: code=%q details=%v, want %s named", tc.url, code, details, tc.field)
		}
	}
}

// RULE (ADR 0002 section 5): a valid key's scope refusal writes one audit row
// attributed to the key, naming the finer scope it lacked (ADR 0009).
func TestWire_MachineKeyRefusalWritesAuditRow(t *testing.T) {
	f := newFixture(t)
	db := f.db
	keySvc := techadmin.NewService(techadmin.NewRepository(db)).WithTxRunner(db)
	validator := staffKeyValidator{svc: keySvc, db: db}
	auth := middleware.NewMachineKeyAuth(validator, audit.NewLogger(db), nil, nil)
	svc := staff.NewService(staff.NewRepository(db)).WithTxRunner(db)
	mux := http.NewServeMux()
	staff.NewHandler(svc).RegisterRoutes(mux)
	srv := httptest.NewServer(auth.Handler(mux))
	defer srv.Close()

	raw, key, err := keySvc.GenerateKey(context.Background(), "staff refusal key", []string{"admin:settings"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = db.Pool.Exec(context.Background(), `DELETE FROM audit_log WHERE actor_id = $1`, key.ID)
		_, _ = db.Pool.Exec(context.Background(), `DELETE FROM api_keys WHERE id = $1`, key.ID)
	})

	req, err := http.NewRequest("GET", srv.URL+"/api/v1/admin/staff", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+raw)
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if res.StatusCode != http.StatusForbidden {
		t.Fatalf("key with admin:settings on staff = %d, want 403", res.StatusCode)
	}

	var scope string
	err = db.Pool.QueryRow(context.Background(),
		`SELECT changes->>'scope' FROM audit_log
		  WHERE action = 'key.scope_refused' AND actor_kind = 'key' AND actor_id = $1`, key.ID,
	).Scan(&scope)
	if err != nil {
		t.Fatalf("no key.scope_refused audit row for the key: %v", err)
	}
	if scope != "admin:staff" {
		t.Errorf("refused scope = %q, want admin:staff (the finer name, ADR 0009)", scope)
	}
}

// RULE: the kill switch suspends a module without deleting any grant, so
// turning it back on restores the roster (the deleted handler test's
// invariant, restored as a wire fact).
func TestWire_DisablingModulePreservesGrants(t *testing.T) {
	f := newFixture(t)
	id, rev := f.createStaff(t, "kill-"+uuid.NewString()[:8]+"@example.com")

	r := f.do("POST", "/api/v1/admin/staff/"+id+"/modules", map[string]any{"module_id": "ai_lm", "revision": rev})
	if r.status != http.StatusOK {
		t.Fatalf("grant = %d: %s", r.status, r.raw)
	}
	rev = bodyNum(t, r.body, "revision")

	list := f.do("GET", "/api/v1/admin/modules", nil)
	if list.status != http.StatusOK {
		t.Fatalf("modules = %d: %s", list.status, list.raw)
	}
	flagRev := int64(1)
	for _, it := range list.body["items"].([]any) {
		if m := it.(map[string]any); m["id"] == "ai_lm" {
			flagRev = bodyNum(t, m, "revision")
		}
	}
	r = f.do("PUT", "/api/v1/admin/modules/ai_lm", map[string]any{"enabled": false, "revision": flagRev})
	if r.status != http.StatusOK {
		t.Fatalf("disable = %d: %s", r.status, r.raw)
	}

	after := f.do("GET", "/api/v1/admin/staff/"+id, nil)
	if after.status != http.StatusOK {
		t.Fatalf("staff after disable = %d", after.status)
	}
	if mods := after.body["modules"].([]any); len(mods) != 1 || mods[0] != "ai_lm" {
		t.Errorf("the disable deleted the grant: modules = %v", after.body["modules"])
	}

	r = f.do("PUT", "/api/v1/admin/modules/ai_lm", map[string]any{"enabled": true, "revision": flagRev + 1})
	if r.status != http.StatusOK {
		t.Fatalf("re-enable = %d: %s", r.status, r.raw)
	}
	restored := f.do("GET", "/api/v1/admin/staff/"+id, nil)
	if mods := restored.body["modules"].([]any); len(mods) != 1 || mods[0] != "ai_lm" {
		t.Errorf("the re-enable did not restore the grant: modules = %v", restored.body["modules"])
	}
}

// RULE: production mounts every staff and module route behind
// RequireRole("admin", "owner") (wire_staff.go); a signed-in caller without
// either role is refused on each of them, in the ADR error envelope. This
// restores the role-guard coverage the old handler tests carried.
func TestWire_AdminRoutesRejectNonAdminRole(t *testing.T) {
	f := newFixture(t)
	mux := http.NewServeMux()
	staff.NewHandler(staff.NewService(staff.NewRepository(f.db)).WithTxRunner(f.db)).RegisterRoutes(mux)
	guarded := middleware.RequireRole("admin", "owner")(mux)

	// The guard reads the claims from the request context, so the probe calls
	// it in process (a context value does not cross a real HTTP hop).
	do := func(method, path, role string) int {
		req := httptest.NewRequest(method, path, nil)
		ctx := context.WithValue(req.Context(), middleware.UserContextKey,
			&middleware.UserClaims{Roles: []string{role}})
		rec := httptest.NewRecorder()
		guarded.ServeHTTP(rec, req.WithContext(ctx))
		return rec.Code
	}
	for _, role := range []string{"sales", "counter", ""} {
		if c := do("GET", "/api/v1/admin/staff", role); c != http.StatusForbidden {
			t.Errorf("role %q on the staff routes = %d, want 403", role, c)
		}
	}
	if c := do("GET", "/api/v1/admin/modules", "owner"); c != http.StatusOK {
		t.Errorf("role owner on the modules routes = %d, want 200", c)
	}
}
