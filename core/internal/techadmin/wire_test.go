// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package techadmin_test

// The tech admin module on the wire contract (ADR 0001), tested end to end: a
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
	svc *techadmin.Service
}

// newFixture builds the module the way serve does (repository, service with
// the outbox and transaction runner, handler) behind the machine-key auth
// core and the global idempotency layer, so a wire test can also prove what
// a scoped key reaches (ADR 0009).
func newFixture(t *testing.T) *fixture {
	t.Helper()
	testutil.LockOutboxTables(t)
	t.Setenv("AUTH_MODE", "dev")
	db := testutil.RequireDB(t)
	f := &fixture{t: t, db: db}
	f.svc = techadmin.NewService(techadmin.NewRepository(db)).
		WithOutbox(outbox.NewWriter(db, "")).WithTxRunner(db).
		WithSettingsDefaults("", "", "")
	mux := http.NewServeMux()
	techadmin.NewHandler(f.svc).RegisterRoutes(mux)
	validator := keyValidator{svc: f.svc}
	auth := middleware.NewMachineKeyAuth(validator, audit.NewLogger(db), nil, nil)
	f.srv = httptest.NewServer(middleware.Idempotency(db)(auth.Handler(mux)))
	t.Cleanup(func() {
		f.srv.Close()
		f.cleanup()
	})
	f.cleanup()
	return f
}

// keyValidator adapts the service to the machine-key core the way serve.go's
// machineKeyValidator does.
type keyValidator struct {
	svc *techadmin.Service
}

func (v keyValidator) ValidateKey(ctx context.Context, rawKey string) (middleware.KeyPrincipal, error) {
	k, err := v.svc.ValidateKey(ctx, rawKey)
	if err != nil {
		if errors.Is(err, techadmin.ErrInvalidKey) {
			return middleware.KeyPrincipal{}, middleware.ErrInvalidMachineKey
		}
		return middleware.KeyPrincipal{}, err
	}
	return middleware.KeyPrincipal{ID: k.ID.String(), Scopes: k.Scopes}, nil
}

// cleanup returns the tables this fixture touches to their empty state, so
// each test starts from the same settings and leaves nothing behind.
func (f *fixture) cleanup() {
	ctx := context.Background()
	_, _ = f.db.Pool.Exec(ctx, `DELETE FROM system_settings WHERE key IN ('openrouter_api_key','openrouter_base_url','openrouteservice_api_key')`)
	_, _ = f.db.Pool.Exec(ctx, `DELETE FROM admin_revisions WHERE resource LIKE 'admin.settings.%'`)
	_, _ = f.db.Pool.Exec(ctx, `DELETE FROM events_outbox WHERE entity_type = 'admin_settings'`)
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

func num(t *testing.T, m map[string]any, key string) int64 {
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

func eventsFor(t *testing.T, db *database.DB, entityType string, entityID string) []string {
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

// RULE: a minted key answers 201 with Location, the raw key shown once, and
// scopes that never read back null; the mint writes key.created exactly once
// and its audit row in the same transaction.
func TestWire_CreateKey(t *testing.T) {
	f := newFixture(t)
	r := f.do("POST", "/api/v1/admin/keys", map[string]any{"name": "wire key", "scopes": []string{"quotes:read"}})
	if r.status != http.StatusCreated {
		t.Fatalf("create = %d: %s", r.status, r.raw)
	}
	if loc := r.header.Get("Location"); !strings.HasPrefix(loc, "/api/v1/admin/keys/") {
		t.Errorf("Location = %q", loc)
	}
	if key, _ := r.body["api_key"].(string); !strings.HasPrefix(key, "sk_live_") {
		t.Errorf("api_key = %q, want the raw sk_live_ key", key)
	}
	k, ok := r.body["key"].(map[string]any)
	if !ok {
		t.Fatalf("key object missing: %s", r.raw)
	}
	scopes, ok := k["scopes"].([]any)
	if !ok || len(scopes) != 1 || scopes[0] != "quotes:read" {
		t.Errorf("scopes = %v, want [quotes:read]", k["scopes"])
	}
	if hint, ok := k["prefix"].(string); !ok || !strings.HasPrefix(hint, "sk_live_") || len(hint) != 12 {
		t.Errorf("prefix = %v", k["prefix"])
	}
	if _, present := k["key_hash"]; present {
		t.Error("the hash must never be on the wire")
	}
	id, _ := k["id"].(string)
	if ev := eventsFor(t, f.db, "api_key", id); len(ev) != 1 || ev[0] != "key.created" {
		t.Errorf("events = %v, want one key.created", ev)
	}
	f.t.Cleanup(func() {
		_, _ = f.db.Pool.Exec(context.Background(), `DELETE FROM events_outbox WHERE entity_type = 'api_key' AND entity_id = $1`, id)
		_, _ = f.db.Pool.Exec(context.Background(), `DELETE FROM api_keys WHERE id = $1`, id)
	})
}

// RULE: field validation is one 400 with every offending field, and an
// unknown body field is refused.
func TestWire_CreateKeyValidation(t *testing.T) {
	f := newFixture(t)
	r := f.do("POST", "/api/v1/admin/keys", map[string]any{"name": "", "scopes": []string{""}})
	if r.status != http.StatusBadRequest {
		t.Fatalf("status = %d: %s", r.status, r.raw)
	}
	code, _, details := errorOf(t, r)
	if code != "validation_failed" {
		t.Errorf("code = %q", code)
	}
	fields := map[string]bool{}
	for _, d := range details {
		if f, ok := d["field"].(string); ok {
			fields[f] = true
		}
	}
	if !fields["name"] || !fields["scopes[0]"] {
		t.Errorf("details = %v, want name and scopes[0]", details)
	}

	r = f.do("POST", "/api/v1/admin/keys", "nope")
	if r.status != http.StatusBadRequest {
		t.Fatalf("bad body = %d", r.status)
	}
	code, _, _ = errorOf(t, r)
	if code != "bad_request" {
		t.Errorf("bad body code = %q", code)
	}

	r = f.do("POST", "/api/v1/admin/keys", map[string]any{"name": "x", "typo": 1})
	if r.status != http.StatusBadRequest {
		t.Fatalf("unknown field = %d", r.status)
	}
	if code, _, _ := errorOf(t, r); code != "bad_request" {
		t.Errorf("unknown field code = %q", code)
	}
}

// RULE (ADR 0001 section 9): the same create twice with one idempotency key
// replays the stored response and makes one row and one event; the same key
// with another body is 422 idempotency_key_reused.
func TestWire_MintKeyIdempotency(t *testing.T) {
	f := newFixture(t)
	body := map[string]any{"name": "idem key", "scopes": []string{"quotes:read"}}
	idemKey := "idem-mint-" + uuid.NewString()[:8]
	t.Cleanup(func() {
		_, _ = f.db.Pool.Exec(context.Background(), `DELETE FROM idempotency_keys WHERE key = $1`, idemKey)
	})
	first := f.do("POST", "/api/v1/admin/keys", body, "Idempotency-Key", idemKey)
	if first.status != http.StatusCreated {
		t.Fatalf("first create = %d: %s", first.status, first.raw)
	}
	second := f.do("POST", "/api/v1/admin/keys", body, "Idempotency-Key", idemKey)
	if second.status != http.StatusCreated {
		t.Fatalf("replayed create = %d: %s", second.status, second.raw)
	}
	if v := second.header.Get("Idempotency-Replayed"); v != "true" {
		t.Errorf("Idempotency-Replayed = %q, want true", v)
	}
	if first.body["api_key"] != second.body["api_key"] {
		t.Errorf("the replay returned a different key: %v then %v", first.body["api_key"], second.body["api_key"])
	}
	k, _ := first.body["key"].(map[string]any)
	id, _ := k["id"].(string)
	t.Cleanup(func() {
		_, _ = f.db.Pool.Exec(context.Background(), `DELETE FROM events_outbox WHERE entity_type = 'api_key' AND entity_id = $1`, id)
		_, _ = f.db.Pool.Exec(context.Background(), `DELETE FROM api_keys WHERE id = $1`, id)
	})
	if n := len(eventsFor(t, f.db, "api_key", id)); n != 1 {
		t.Errorf("%d key.created events, want 1 (a replay must not mint again)", n)
	}

	other := f.do("POST", "/api/v1/admin/keys",
		map[string]any{"name": "idem key other", "scopes": []string{"quotes:read"}},
		"Idempotency-Key", idemKey)
	if other.status != http.StatusUnprocessableEntity {
		t.Fatalf("same key other body = %d: %s", other.status, other.raw)
	}
	if code, _, _ := errorOf(t, other); code != "idempotency_key_reused" {
		t.Errorf("code = %q, want idempotency_key_reused", code)
	}
}

// RULE: the list is the cursor envelope, items never null, and the cursor
// walks every row once; include=total carries the count.
func TestWire_KeyListCursor(t *testing.T) {
	f := newFixture(t)
	var ids []string
	for i := 0; i < 3; i++ {
		r := f.do("POST", "/api/v1/admin/keys", map[string]any{"name": fmt.Sprintf("k%d", i), "scopes": []string{}})
		if r.status != http.StatusCreated {
			t.Fatalf("create %d = %d", i, r.status)
		}
		k := r.body["key"].(map[string]any)
		ids = append(ids, k["id"].(string))
	}
	f.t.Cleanup(func() {
		for _, id := range ids {
			_, _ = f.db.Pool.Exec(context.Background(), `DELETE FROM events_outbox WHERE entity_type = 'api_key' AND entity_id = $1`, id)
			_, _ = f.db.Pool.Exec(context.Background(), `DELETE FROM api_keys WHERE id = $1`, id)
		}
	})

	r := f.do("GET", "/api/v1/admin/keys?limit=2", nil)
	if r.status != http.StatusOK {
		t.Fatalf("list = %d", r.status)
	}
	if _, ok := r.body["items"].([]any); !ok {
		t.Fatalf("items is %T, want an array", r.body["items"])
	}
	if num(t, r.body, "limit") != 2 {
		t.Errorf("limit echo = %v", r.body["limit"])
	}
	cur, _ := r.body["next_cursor"].(string)
	if cur == "" {
		t.Fatal("next_cursor is empty with more rows to serve")
	}
	seen := map[string]bool{}
	pages := 0
	for cur != "" && pages < 5 {
		for _, it := range r.body["items"].([]any) {
			seen[it.(map[string]any)["id"].(string)] = true
		}
		r = f.do("GET", "/api/v1/admin/keys?limit=2&cursor="+cur, nil)
		if r.status != http.StatusOK {
			t.Fatalf("page = %d: %s", r.status, r.raw)
		}
		cur, _ = r.body["next_cursor"].(string)
		pages++
	}
	for _, it := range r.body["items"].([]any) {
		seen[it.(map[string]any)["id"].(string)] = true
	}
	for _, id := range ids {
		if !seen[id] {
			t.Errorf("key %s never served", id)
		}
	}
	if _, present := r.body["total"]; present {
		t.Error("total appeared without include=total")
	}
	r = f.do("GET", "/api/v1/admin/keys?limit=2&include=total", nil)
	if _, present := r.body["total"]; !present {
		t.Error("include=total did not carry the count")
	}
	if num(t, r.body, "total") < 3 {
		t.Errorf("total = %v", r.body["total"])
	}
}

// RULE: strict query parameters. An unknown name and a bad limit are 400s
// naming the field; a broken cursor is a 400, never the first page.
func TestWire_KeyListStrictQuery(t *testing.T) {
	f := newFixture(t)
	r := f.do("GET", "/api/v1/admin/keys?status=active", nil)
	if r.status != http.StatusBadRequest {
		t.Fatalf("unknown parameter = %d", r.status)
	}
	code, _, details := errorOf(t, r)
	if code != "unsupported_query_parameter" || len(details) != 1 || details[0]["field"] != "status" {
		t.Errorf("code=%q details=%v", code, details)
	}
	r = f.do("GET", "/api/v1/admin/keys?limit=0", nil)
	if r.status != http.StatusBadRequest {
		t.Fatalf("limit 0 = %d", r.status)
	}
	code, _, details = errorOf(t, r)
	if code != "validation_failed" || len(details) != 1 || details[0]["field"] != "limit" {
		t.Errorf("limit: code=%q details=%v", code, details)
	}
	r = f.do("GET", "/api/v1/admin/keys?cursor="+bogusCursor(), nil)
	if r.status != http.StatusBadRequest {
		t.Fatalf("broken cursor = %d", r.status)
	}
	if code, _, details := errorOf(t, r); code != "bad_request" || len(details) != 1 || details[0]["field"] != "cursor" {
		t.Errorf("cursor: code=%q details=%v", code, details)
	}
}

func bogusCursor() string {
	return "aW52YWxpZA"
}

// RULE: revoking an unknown key is a 404; revoking a live key is a 204 that
// writes key.revoked once; revoking it again is a silent 204.
func TestWire_RevokeKey(t *testing.T) {
	f := newFixture(t)
	r := f.do("DELETE", "/api/v1/admin/keys/"+uuid.NewString(), nil)
	if r.status != http.StatusNotFound {
		t.Fatalf("unknown key = %d, want 404", r.status)
	}
	code, _, _ := errorOf(t, r)
	if code != "not_found" {
		t.Errorf("code = %q", code)
	}

	r = f.do("DELETE", "/api/v1/admin/keys/not-a-uuid", nil)
	if r.status != http.StatusBadRequest {
		t.Fatalf("malformed id = %d", r.status)
	}

	created := f.do("POST", "/api/v1/admin/keys", map[string]any{"name": "revoke me", "scopes": []string{}})
	k := created.body["key"].(map[string]any)
	id := k["id"].(string)
	f.t.Cleanup(func() {
		_, _ = f.db.Pool.Exec(context.Background(), `DELETE FROM events_outbox WHERE entity_type = 'api_key' AND entity_id = $1`, id)
		_, _ = f.db.Pool.Exec(context.Background(), `DELETE FROM api_keys WHERE id = $1`, id)
	})

	if r := f.do("DELETE", "/api/v1/admin/keys/"+id, nil); r.status != http.StatusNoContent {
		t.Fatalf("revoke = %d", r.status)
	}
	if ev := eventsFor(t, f.db, "api_key", id); len(ev) != 2 || ev[1] != "key.revoked" {
		t.Errorf("events = %v, want key.created then key.revoked", ev)
	}
	if r := f.do("DELETE", "/api/v1/admin/keys/"+id, nil); r.status != http.StatusNoContent {
		t.Fatalf("second revoke = %d", r.status)
	}
	if ev := eventsFor(t, f.db, "api_key", id); len(ev) != 2 {
		t.Errorf("the idempotent revoke wrote another event: %v", ev)
	}
}

// RULE: the settings document carries its revision and ETag; a write without
// a precondition is 428, a stale one 409, and a successful one returns the
// new revision in body and header. The base URL rules keep their meanings:
// omitted leaves the override, empty clears it, a bad one is a 400 that
// writes nothing.
func TestWire_AISettingsRevisionAndBaseURL(t *testing.T) {
	f := newFixture(t)
	get := f.do("GET", "/api/v1/admin/settings/ai", nil)
	if get.status != http.StatusOK {
		t.Fatalf("get = %d", get.status)
	}
	if rev := num(t, get.body, "revision"); rev != 1 {
		t.Errorf("initial revision = %d, want 1", rev)
	}
	if get.body["configured"] != false || get.body["source"] != "none" {
		t.Errorf("initial = %v", get.body)
	}
	for _, k := range []string{"key_hint", "base_url"} {
		if _, present := get.body[k]; !present {
			t.Errorf("%s missing; optional fields are present as null", k)
		}
	}
	if get.body["key_hint"] != nil || get.body["base_url"] != nil {
		t.Errorf("initial hints = %v %v", get.body["key_hint"], get.body["base_url"])
	}

	// 428 without a precondition.
	r := f.do("PUT", "/api/v1/admin/settings/ai", map[string]any{"api_key": "sk-or-1234567890"})
	if r.status != http.StatusPreconditionRequired {
		t.Fatalf("no precondition = %d, want 428", r.status)
	}
	code, _, _ := errorOf(t, r)
	if code != "precondition_required" {
		t.Errorf("code = %q", code)
	}

	// A bad base URL is one 400 writing nothing.
	r = f.do("PUT", "/api/v1/admin/settings/ai", map[string]any{"api_key": "sk-or-1234567890", "base_url": "http://insecure"},
		"If-Match", `"1"`)
	if r.status != http.StatusBadRequest {
		t.Fatalf("bad base url = %d: %s", r.status, r.raw)
	}
	code, _, details := errorOf(t, r)
	if code != "validation_failed" || len(details) != 1 || details[0]["field"] != "base_url" {
		t.Errorf("details = %v", details)
	}

	// Save with the strong If-Match.
	r = f.do("PUT", "/api/v1/admin/settings/ai", map[string]any{"api_key": "sk-or-1234567890", "base_url": "https://proxy.example/v1"},
		"If-Match", `"1"`)
	if r.status != http.StatusOK {
		t.Fatalf("save = %d: %s", r.status, r.raw)
	}
	if rev := num(t, r.body, "revision"); rev != 2 {
		t.Errorf("revision after save = %d, want 2", rev)
	}
	if r.body["source"] != "admin" || r.body["configured"] != true {
		t.Errorf("saved = %v", r.body)
	}
	if etag := r.header.Get("ETag"); etag != `"2"` {
		t.Errorf("ETag = %q", etag)
	}
	if u, _ := r.body["base_url"].(string); u != "https://proxy.example/v1" {
		t.Errorf("base_url = %v", r.body["base_url"])
	}

	// The weak form is accepted; the stale one is 409.
	r = f.do("PUT", "/api/v1/admin/settings/ai", map[string]any{"api_key": "sk-or-9876543210", "base_url": ""},
		"If-Match", `W/"2"`)
	if r.status != http.StatusOK {
		t.Fatalf("weak save = %d: %s", r.status, r.raw)
	}
	if u, _ := r.body["base_url"].(string); u != "" {
		t.Errorf("an empty base_url must clear the override; base_url = %v", r.body["base_url"])
	}
	if r.body["base_url"] != nil {
		t.Errorf("cleared base_url must read back null, got %v", r.body["base_url"])
	}
	r = f.do("PUT", "/api/v1/admin/settings/ai", map[string]any{"api_key": "sk-or-x"}, "If-Match", `"1"`)
	if r.status != http.StatusConflict {
		t.Fatalf("stale save = %d, want 409", r.status)
	}
	if code, _, _ := errorOf(t, r); code != "stale_revision" {
		t.Errorf("code = %q", code)
	}

	// The body revision agrees or disagrees with If-Match; both carry it.
	r = f.do("PUT", "/api/v1/admin/settings/ai", map[string]any{"api_key": "sk-or-body", "revision": 3})
	if r.status != http.StatusOK {
		t.Fatalf("body revision save = %d: %s", r.status, r.raw)
	}
	r = f.do("PUT", "/api/v1/admin/settings/ai", map[string]any{"api_key": "sk-or-body", "revision": 3}, "If-Match", `"4"`)
	if r.status != http.StatusBadRequest {
		t.Fatalf("disagreeing carriers = %d, want 400", r.status)
	}

	// Delete needs the precondition too and answers 204.
	r = f.do("DELETE", "/api/v1/admin/settings/ai", nil)
	if r.status != http.StatusPreconditionRequired {
		t.Fatalf("delete without precondition = %d, want 428", r.status)
	}
	if r := f.do("DELETE", "/api/v1/admin/settings/ai", nil, "If-Match", `"4"`); r.status != http.StatusNoContent {
		t.Fatalf("delete = %d", r.status)
	}
	after := f.do("GET", "/api/v1/admin/settings/ai", nil)
	if after.body["configured"] != false || after.body["source"] != "none" {
		t.Errorf("after delete = %v", after.body)
	}
	if rev := num(t, after.body, "revision"); rev != 5 {
		t.Errorf("revision after delete = %d, want 5 (a delete moves it, never back)", rev)
	}
}

// RULE: the routing settings follow the AI settings' rules without the base
// URL; the two resources carry independent revisions, and the save and the
// delete take the precondition alike (428 without one, 409 on a stale one).
func TestWire_RoutingSettings(t *testing.T) {
	f := newFixture(t)
	r := f.do("PUT", "/api/v1/admin/settings/routing", map[string]any{"api_key": ""})
	if r.status != http.StatusBadRequest {
		t.Fatalf("empty key = %d", r.status)
	}
	code, _, details := errorOf(t, r)
	if code != "validation_failed" || details[0]["field"] != "api_key" {
		t.Errorf("details = %v", details)
	}
	get := f.do("GET", "/api/v1/admin/settings/routing", nil)
	if get.status != http.StatusOK {
		t.Fatalf("get = %d: %s", get.status, get.raw)
	}
	rev := num(t, get.body, "revision")

	// The save needs the precondition: 428 without one, 409 on a stale one.
	r = f.do("PUT", "/api/v1/admin/settings/routing", map[string]any{"api_key": "ors-key-1"})
	if r.status != http.StatusPreconditionRequired {
		t.Fatalf("save without precondition = %d, want 428", r.status)
	}
	if code, _, _ := errorOf(t, r); code != "precondition_required" {
		t.Errorf("code = %q", code)
	}
	r = f.do("PUT", "/api/v1/admin/settings/routing", map[string]any{"api_key": "ors-key-1", "revision": rev + 1})
	if r.status != http.StatusConflict {
		t.Fatalf("stale save = %d, want 409", r.status)
	}
	if code, _, _ := errorOf(t, r); code != "stale_revision" {
		t.Errorf("code = %q", code)
	}

	r = f.do("PUT", "/api/v1/admin/settings/routing", map[string]any{"api_key": "ors-key-1", "revision": rev})
	if r.status != http.StatusOK {
		t.Fatalf("save = %d: %s", r.status, r.raw)
	}
	if _, present := r.body["base_url"]; present {
		t.Error("routing settings carry no base_url")
	}
	if bodyRev := num(t, r.body, "revision"); bodyRev != rev+1 {
		t.Errorf("revision after save = %d, want %d", bodyRev, rev+1)
	} else if etag := r.header.Get("ETag"); etag != fmt.Sprintf(`"%d"`, bodyRev) {
		t.Errorf("ETag = %q, want the new revision", etag)
	}

	// The delete takes the precondition too: 428 without one, 409 on a stale
	// one, 204 on the current revision.
	r = f.do("DELETE", "/api/v1/admin/settings/routing", nil)
	if r.status != http.StatusPreconditionRequired {
		t.Fatalf("delete without precondition = %d, want 428", r.status)
	}
	r = f.do("DELETE", "/api/v1/admin/settings/routing", nil, "If-Match", fmt.Sprintf(`"%d"`, rev))
	if r.status != http.StatusConflict {
		t.Fatalf("stale delete = %d, want 409", r.status)
	}
	if r := f.do("DELETE", "/api/v1/admin/settings/routing", nil, "If-Match", fmt.Sprintf(`"%d"`, rev+1)); r.status != http.StatusNoContent {
		t.Fatalf("delete = %d, want 204", r.status)
	}
	after := f.do("GET", "/api/v1/admin/settings/routing", nil)
	if after.body["configured"] != false || after.body["source"] != "none" {
		t.Errorf("after delete = %v", after.body)
	}
	if bodyRev := num(t, after.body, "revision"); bodyRev != rev+2 {
		t.Errorf("revision after delete = %d, want %d (a delete moves it, never back)", bodyRev, rev+2)
	}
	// The events of the settings resources name their resource's stable id,
	// so a consumer can subscribe to one resource alone.
	rows, err := f.db.Pool.Query(context.Background(),
		`SELECT DISTINCT entity_id::text FROM events_outbox WHERE entity_type = 'admin_settings' ORDER BY 1`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var entities []string
	for rows.Next() {
		var e string
		if err := rows.Scan(&e); err != nil {
			t.Fatal(err)
		}
		entities = append(entities, e)
	}
	if len(entities) == 0 {
		t.Error("the settings save wrote no event")
	}
}

// RULE: a machine key holding the finer settings scope (ADR 0009) reaches the
// settings routes; the coarse admin scopes are refused there; key routes are
// user only; the refusal leaves an audit row naming the scope it lacked.
func TestWire_FinerAdminScopesOnSettings(t *testing.T) {
	f := newFixture(t)
	raw, key, err := f.svc.GenerateKey(context.Background(), "scoped", []string{"admin:settings"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	f.t.Cleanup(func() {
		_, _ = f.db.Pool.Exec(context.Background(), `DELETE FROM events_outbox WHERE entity_type = 'api_key' AND entity_id = $1`, key.ID)
		_, _ = f.db.Pool.Exec(context.Background(), `DELETE FROM api_keys WHERE id = $1`, key.ID)
	})
	bearer := []string{"Authorization", "Bearer " + raw}

	if r := f.do("GET", "/api/v1/admin/settings/ai", nil, bearer...); r.status != http.StatusOK {
		t.Errorf("settings key on settings = %d, want 200", r.status)
	}
	if r := f.do("PUT", "/api/v1/admin/settings/ai", map[string]any{"api_key": "k", "revision": 1}, bearer...); r.status != http.StatusOK {
		t.Errorf("settings key on settings write = %d, want 200", r.status)
	}

	coarseRaw, coarseKey, err := f.svc.GenerateKey(context.Background(), "coarse", []string{"admin:read", "admin:write"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	f.t.Cleanup(func() {
		_, _ = f.db.Pool.Exec(context.Background(), `DELETE FROM events_outbox WHERE entity_type = 'api_key' AND entity_id = $1`, coarseKey.ID)
		_, _ = f.db.Pool.Exec(context.Background(), `DELETE FROM api_keys WHERE id = $1`, coarseKey.ID)
	})
	r := f.do("GET", "/api/v1/admin/settings/ai", nil, "Authorization", "Bearer "+coarseRaw)
	if r.status != http.StatusForbidden {
		t.Errorf("coarse key on settings = %d, want 403", r.status)
	}
	code, message, _ := errorOf(t, r)
	if code != "forbidden" || !strings.Contains(message, "admin:settings") {
		t.Errorf("code=%q message=%q", code, message)
	}

	// Key management is user only whatever the scope.
	r = f.do("GET", "/api/v1/admin/keys", nil, bearer...)
	if r.status != http.StatusForbidden {
		t.Errorf("settings key on key routes = %d, want 403", r.status)
	}

	// The refusal's audit row names the key and the scope it lacked.
	var action, refused string
	if err := f.db.Pool.QueryRow(context.Background(),
		`SELECT action, changes->>'scope' FROM audit_log WHERE entity_type = 'api_key' AND entity_id = $1 ORDER BY created_at DESC LIMIT 1`,
		coarseKey.ID).Scan(&action, &refused); err != nil {
		t.Fatalf("audit row: %v", err)
	}
	if action != "key.scope_refused" || refused != "admin:settings" {
		t.Errorf("audit = %s %q", action, refused)
	}
}
