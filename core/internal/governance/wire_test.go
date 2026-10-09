// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package governance_test

// The governance module on the wire contract (ADR 0001), tested end to end:
// a real Postgres, the real handler on a real mux behind the real idempotency
// middleware, requests as JSON and responses read back as JSON. Nothing here
// touches a module type, so each test states a wire fact.

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gablelbm/gable/internal/governance"
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
	svc := governance.NewService(governance.NewRepository(db), governance.NewTemplateAIProvider()).
		WithOutbox(outbox.NewWriter(db, "")).WithTxRunner(db)
	mux := http.NewServeMux()
	governance.NewHandler(svc).RegisterRoutes(mux)
	f.srv = httptest.NewServer(middleware.Idempotency(db)(mux))
	t.Cleanup(f.srv.Close)
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

func (f *fixture) createRFC(t *testing.T) (id, number string, rev int64) {
	t.Helper()
	r := f.do("POST", "/api/v1/governance/rfcs", map[string]any{
		"title":             "Wire RFC " + uuid.NewString()[:6],
		"problem_statement": "the problem",
		"proposed_solution": "the solution",
	})
	if r.status != http.StatusCreated {
		t.Fatalf("create = %d: %s", r.status, r.raw)
	}
	if loc := r.header.Get("Location"); !strings.HasPrefix(loc, "/api/v1/governance/rfcs/") {
		t.Errorf("Location = %q", loc)
	}
	id, _ = r.body["id"].(string)
	number, _ = r.body["number"].(string)
	if !strings.HasPrefix(number, "RFC-") {
		t.Errorf("number = %q, want an RFC- document number", number)
	}
	rev = bodyNum(t, r.body, "revision")
	if rev != 1 {
		t.Errorf("a create starts at revision 1, got %d", rev)
	}
	if r.body["status"] != "draft" {
		t.Errorf("status = %v, want draft", r.body["status"])
	}
	t.Cleanup(func() { dropRFC(t, f.db, id) })
	return id, number, rev
}

func dropRFC(t *testing.T, db *database.DB, id string) {
	t.Helper()
	_, _ = db.Pool.Exec(context.Background(), `DELETE FROM events_outbox WHERE entity_type = 'rfc' AND entity_id = $1`, id)
	_, _ = db.Pool.Exec(context.Background(), `DELETE FROM rfcs WHERE id = $1`, id)
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

func eventsFor(t *testing.T, db *database.DB, id string) []string {
	t.Helper()
	rows, err := db.Pool.Query(context.Background(),
		`SELECT type FROM events_outbox WHERE entity_type = 'rfc' AND entity_id = $1 ORDER BY position`, id)
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

// RULE: the create shape carries the number, the lowercase status, the
// generated content, the revision and the ETag; field validation is one 400;
// a create carrying content, status or revision is refused; rfc.created is
// written exactly once, and an idempotent replay returns the first response.
func TestWire_CreateRFC(t *testing.T) {
	f := newFixture(t)
	r := f.do("POST", "/api/v1/governance/rfcs", map[string]any{
		"title": "Validation RFC", "problem_statement": "p", "proposed_solution": "s",
	})
	if r.status != http.StatusCreated {
		t.Fatalf("create = %d: %s", r.status, r.raw)
	}
	if c, _ := r.body["content"].(string); !strings.Contains(c, "Validation RFC") {
		t.Errorf("content = %q, want the generated body", c)
	}
	if etag := r.header.Get("ETag"); etag != `"1"` {
		t.Errorf("ETag = %q", etag)
	}
	id, _ := r.body["id"].(string)
	t.Cleanup(func() { dropRFC(t, f.db, id) })

	r = f.do("POST", "/api/v1/governance/rfcs", map[string]any{"title": "", "problem_statement": "", "proposed_solution": ""})
	if r.status != http.StatusBadRequest {
		t.Fatalf("validation = %d", r.status)
	}
	code, _, details := errorOf(t, r)
	if code != "validation_failed" || len(details) != 3 {
		t.Errorf("code=%q details=%v", code, details)
	}
	for _, field := range []string{"content", "status", "revision"} {
		r = f.do("POST", "/api/v1/governance/rfcs", map[string]any{
			"title": "T", "problem_statement": "p", "proposed_solution": "s", field: "x",
		})
		if r.status != http.StatusBadRequest {
			t.Errorf("%s on create = %d, want 400", field, r.status)
		}
	}

	// Idempotent replay: the same key returns the first response and makes
	// one row and one event.
	key := "wire-" + uuid.NewString()
	first := f.do("POST", "/api/v1/governance/rfcs", map[string]any{
		"title": "Idem", "problem_statement": "p", "proposed_solution": "s",
	}, "Idempotency-Key", key)
	if first.status != http.StatusCreated {
		t.Fatalf("idem create = %d", first.status)
	}
	idemID, _ := first.body["id"].(string)
	t.Cleanup(func() { dropRFC(t, f.db, idemID) })
	second := f.do("POST", "/api/v1/governance/rfcs", map[string]any{
		"title": "Idem", "problem_statement": "p", "proposed_solution": "s",
	}, "Idempotency-Key", key)
	if second.status != http.StatusCreated || second.body["id"] != idemID {
		t.Fatalf("replay = %d %v", second.status, second.body["id"])
	}
	if second.header.Get("Idempotency-Replayed") != "true" {
		t.Error("the replay is not marked")
	}
	if ev := eventsFor(t, f.db, idemID); len(ev) != 1 {
		t.Errorf("events = %v, want one rfc.created", ev)
	}
	conflict := f.do("POST", "/api/v1/governance/rfcs", map[string]any{
		"title": "Different", "problem_statement": "p", "proposed_solution": "s",
	}, "Idempotency-Key", key)
	if conflict.status != http.StatusUnprocessableEntity {
		t.Errorf("key reuse with another body = %d, want 422", conflict.status)
	}
}

// RULE: the list is the cursor envelope; the status filter filters; an
// uppercase or unknown value is refused; the cursor walks every fixture row
// once; include=total carries the count. A legacy row with NULL content no
// longer breaks the list (the base 500).
func TestWire_ListFilterAndCursor(t *testing.T) {
	f := newFixture(t)
	id1, _, _ := f.createRFC(t)
	id2, _, _ := f.createRFC(t)

	if _, err := f.db.Pool.Exec(context.Background(),
		`INSERT INTO rfcs (title, status, problem_statement, proposed_solution, number) VALUES ($1, 'draft', 'p', 's', 'RFC-999001')`,
		"Legacy NULL content "+uuid.NewString()[:6]); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = f.db.Pool.Exec(context.Background(), `DELETE FROM rfcs WHERE number = 'RFC-999001'`)
	})

	r := f.do("GET", "/api/v1/governance/rfcs", nil)
	if r.status != http.StatusOK {
		t.Fatalf("list = %d: %s", r.status, r.raw)
	}
	if _, ok := r.body["items"].([]any); !ok {
		t.Fatalf("items is %T, want an array (the NULL content row must not break the list)", r.body["items"])
	}

	r = f.do("GET", "/api/v1/governance/rfcs?status=review", nil)
	if r.status != http.StatusOK {
		t.Fatalf("filtered = %d", r.status)
	}
	for _, it := range r.body["items"].([]any) {
		if it.(map[string]any)["status"] != "review" {
			t.Errorf("the status filter served a %v row", it.(map[string]any)["status"])
		}
	}
	r = f.do("GET", "/api/v1/governance/rfcs?status=DRAFT", nil)
	if r.status != http.StatusBadRequest {
		t.Fatalf("uppercase status = %d, want 400", r.status)
	}
	r = f.do("GET", "/api/v1/governance/rfcs?author=x", nil)
	if r.status != http.StatusBadRequest {
		t.Fatalf("unknown parameter = %d", r.status)
	}
	if code, _, _ := errorOf(t, r); code != "unsupported_query_parameter" {
		t.Errorf("code = %q", code)
	}

	// The cursor walks the two fixture rows once.
	r = f.do("GET", "/api/v1/governance/rfcs?limit=1", nil)
	cur, _ := r.body["next_cursor"].(string)
	if cur == "" {
		t.Fatal("next_cursor is empty with more rows to serve")
	}
	seen := map[string]bool{}
	pages := 0
	for cur != "" && pages < 100 {
		for _, it := range r.body["items"].([]any) {
			seen[it.(map[string]any)["id"].(string)] = true
		}
		r = f.do("GET", "/api/v1/governance/rfcs?limit=1&cursor="+cur, nil)
		if r.status != http.StatusOK {
			t.Fatalf("page = %d: %s", r.status, r.raw)
		}
		cur, _ = r.body["next_cursor"].(string)
		pages++
	}
	for _, it := range r.body["items"].([]any) {
		seen[it.(map[string]any)["id"].(string)] = true
	}
	if !seen[id1] || !seen[id2] {
		t.Errorf("the walk missed a fixture row (seen %v)", seen)
	}
	r = f.do("GET", "/api/v1/governance/rfcs?status=draft,review&include=total", nil)
	if num := bodyNum(t, r.body, "total"); num < 2 {
		t.Errorf("total = %d", num)
	}
}

// RULE: updates take the revision (428 without, 409 stale, the new revision
// and ETag on success); status moves through the transitions route with each
// allowed edge and the forbidden ones as 409 invalid_state_transition, each
// transition's event in order; an approved RFC refuses an edit.
func TestWire_UpdateAndTransitions(t *testing.T) {
	f := newFixture(t)
	id, _, _ := f.createRFC(t)

	r := f.do("PUT", "/api/v1/governance/rfcs/"+id, map[string]any{"title": "No Precondition"})
	if r.status != http.StatusPreconditionRequired {
		t.Fatalf("no precondition = %d, want 428", r.status)
	}
	r = f.do("PUT", "/api/v1/governance/rfcs/"+id, map[string]any{"title": "Stale"}, "If-Match", `"5"`)
	if r.status != http.StatusConflict {
		t.Fatalf("stale = %d, want 409", r.status)
	}
	if code, _, _ := errorOf(t, r); code != "stale_revision" {
		t.Errorf("code = %q", code)
	}
	r = f.do("PUT", "/api/v1/governance/rfcs/"+id, map[string]any{"title": "Edited", "revision": 1})
	if r.status != http.StatusOK {
		t.Fatalf("update = %d: %s", r.status, r.raw)
	}
	if bodyNum(t, r.body, "revision") != 2 || r.header.Get("ETag") != `"2"` {
		t.Errorf("revision = %v ETag = %q", r.body["revision"], r.header.Get("ETag"))
	}
	// status on the update is refused, naming the transitions route.
	r = f.do("PUT", "/api/v1/governance/rfcs/"+id, map[string]any{"status": "review", "revision": 2})
	if r.status != http.StatusBadRequest {
		t.Fatalf("status on update = %d, want 400", r.status)
	}
	if _, _, details := errorOf(t, r); details[0]["field"] != "status" ||
		!strings.Contains(details[0]["message"].(string), "transitions") {
		t.Errorf("details = %v", details)
	}

	// Transitions.
	r = f.do("POST", "/api/v1/governance/rfcs/"+id+"/transitions", map[string]any{"to": "approved", "revision": 2})
	if r.status != http.StatusConflict {
		t.Fatalf("draft -> approved = %d, want 409", r.status)
	}
	if code, _, _ := errorOf(t, r); code != "invalid_state_transition" {
		t.Errorf("code = %q", code)
	}
	r = f.do("POST", "/api/v1/governance/rfcs/"+id+"/transitions", map[string]any{"to": "review", "revision": 2})
	if r.status != http.StatusOK || r.body["status"] != "review" {
		t.Fatalf("draft -> review = %d %v", r.status, r.body["status"])
	}
	r = f.do("POST", "/api/v1/governance/rfcs/"+id+"/transitions", map[string]any{"to": "approved", "revision": 3})
	if r.status != http.StatusOK || r.body["status"] != "approved" {
		t.Fatalf("review -> approved = %d %v", r.status, r.body["status"])
	}
	// Approved is terminal, and an edit is refused with the blocker.
	r = f.do("POST", "/api/v1/governance/rfcs/"+id+"/transitions", map[string]any{"to": "review", "revision": 4})
	if r.status != http.StatusConflict {
		t.Fatalf("approved -> review = %d, want 409", r.status)
	}
	r = f.do("PUT", "/api/v1/governance/rfcs/"+id, map[string]any{"title": "Nope"}, "If-Match", `"4"`)
	if r.status != http.StatusConflict {
		t.Fatalf("edit of an approved RFC = %d, want 409", r.status)
	}
	if _, _, details := errorOf(t, r); len(details) != 1 || details[0]["code"] != "rfc_not_editable" {
		t.Errorf("details = %v", details)
	}
	ev := eventsFor(t, f.db, id)
	want := []string{"rfc.created", "rfc.updated", "rfc.review", "rfc.approved"}
	if strings.Join(ev, ",") != strings.Join(want, ",") {
		t.Errorf("events = %v, want %v", ev, want)
	}

	r = f.do("GET", "/api/v1/governance/rfcs/"+uuid.NewString(), nil)
	if r.status != http.StatusNotFound {
		t.Errorf("unknown id = %d", r.status)
	}
	r = f.do("GET", "/api/v1/governance/rfcs/not-a-uuid", nil)
	if r.status != http.StatusBadRequest {
		t.Errorf("malformed id = %d", r.status)
	}
}

// RULE: a governance key (module scope governance:read / governance:write)
// reads the RFCs and is refused a write with the read scope alone.
func TestWire_MachineKeyScope(t *testing.T) {
	f := newFixture(t)
	techSvc := techadmin.NewService(techadmin.NewRepository(f.db)).WithTxRunner(f.db)
	raw, key, err := techSvc.GenerateKey(context.Background(), "gov key", []string{"governance:read"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = f.db.Pool.Exec(context.Background(), `DELETE FROM api_keys WHERE id = $1`, key.ID)
	})
	validator := govKeyValidator{svc: techSvc}
	auth := middleware.NewMachineKeyAuth(validator, nil, nil, nil)
	svc := governance.NewService(governance.NewRepository(f.db), governance.NewTemplateAIProvider()).WithTxRunner(f.db)
	mux := http.NewServeMux()
	governance.NewHandler(svc).RegisterRoutes(mux)
	srv := httptest.NewServer(auth.Handler(mux))
	defer srv.Close()

	do := func(method, path string, body any) int {
		t.Helper()
		var rdr io.Reader
		if body != nil {
			payload, _ := json.Marshal(body)
			rdr = bytes.NewReader(payload)
		}
		req, _ := http.NewRequest(method, srv.URL+path, rdr)
		req.Header.Set("Authorization", "Bearer "+raw)
		if rdr != nil {
			req.Header.Set("Content-Type", "application/json")
		}
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		res.Body.Close()
		return res.StatusCode
	}
	if c := do("GET", "/api/v1/governance/rfcs", nil); c != http.StatusOK {
		t.Errorf("governance:read on the list = %d, want 200", c)
	}
	if c := do("POST", "/api/v1/governance/rfcs", map[string]any{"title": "K", "problem_statement": "p", "proposed_solution": "s"}); c != http.StatusForbidden {
		t.Errorf("governance:read on a create = %d, want 403", c)
	}
}

type govKeyValidator struct {
	svc *techadmin.Service
}

func (v govKeyValidator) ValidateKey(ctx context.Context, rawKey string) (middleware.KeyPrincipal, error) {
	k, err := v.svc.ValidateKey(ctx, rawKey)
	if err != nil {
		if errors.Is(err, techadmin.ErrInvalidKey) {
			return middleware.KeyPrincipal{}, middleware.ErrInvalidMachineKey
		}
		return middleware.KeyPrincipal{}, err
	}
	return middleware.KeyPrincipal{ID: k.ID.String(), Scopes: k.Scopes}, nil
}

// RULE: the lifecycle's other half: review may reject, a rejected RFC may be
// reopened to draft, and each edge writes its own event in order.
func TestWire_RejectedAndReopenEdges(t *testing.T) {
	f := newFixture(t)
	id, _, _ := f.createRFC(t)

	mustTransition := func(to string, rev int64) {
		t.Helper()
		r := f.do("POST", "/api/v1/governance/rfcs/"+id+"/transitions", map[string]any{"to": to, "revision": rev})
		if r.status != http.StatusOK || r.body["status"] != to {
			t.Fatalf("transition to %s = %d %v: %s", to, r.status, r.body["status"], r.raw)
		}
	}
	mustTransition("review", 1)
	mustTransition("rejected", 2)
	mustTransition("draft", 3) // the reopen
	// A reopened draft refuses a jump to approved, like a fresh one.
	r := f.do("POST", "/api/v1/governance/rfcs/"+id+"/transitions", map[string]any{"to": "approved", "revision": 4})
	if r.status != http.StatusConflict {
		t.Fatalf("draft -> approved after reopen = %d, want 409", r.status)
	}
	if code, _, _ := errorOf(t, r); code != "invalid_state_transition" {
		t.Errorf("code = %q", code)
	}
	ev := eventsFor(t, f.db, id)
	want := []string{"rfc.created", "rfc.review", "rfc.rejected", "rfc.reopened"}
	if strings.Join(ev, ",") != strings.Join(want, ",") {
		t.Errorf("events = %v, want %v", ev, want)
	}
}

// RULE: a transition is a write (ADR 0001 section 11): 428 without a
// precondition, a weak If-Match honoured, header and body revision in
// disagreement refused, stale refused with 409.
func TestWire_TransitionsPreconditions(t *testing.T) {
	f := newFixture(t)
	id, _, _ := f.createRFC(t)

	r := f.do("POST", "/api/v1/governance/rfcs/"+id+"/transitions", map[string]any{"to": "review"})
	if r.status != http.StatusPreconditionRequired {
		t.Fatalf("no precondition = %d, want 428: %s", r.status, r.raw)
	}
	if code, _, _ := errorOf(t, r); code != "precondition_required" {
		t.Errorf("code = %q", code)
	}
	r = f.do("POST", "/api/v1/governance/rfcs/"+id+"/transitions", map[string]any{"to": "review"}, "If-Match", `W/"1"`)
	if r.status != http.StatusOK {
		t.Fatalf("weak If-Match = %d: %s", r.status, r.raw)
	}
	if bodyNum(t, r.body, "revision") != 2 {
		t.Errorf("revision = %v, want 2", r.body["revision"])
	}
	// The next edge: header says 2, body says 3: a disagreement is a 400.
	r = f.do("POST", "/api/v1/governance/rfcs/"+id+"/transitions",
		map[string]any{"to": "approved", "revision": 3}, "If-Match", `"2"`)
	if r.status != http.StatusBadRequest {
		t.Fatalf("header/body disagreement = %d, want 400: %s", r.status, r.raw)
	}
	r = f.do("POST", "/api/v1/governance/rfcs/"+id+"/transitions", map[string]any{"to": "approved"}, "If-Match", `"1"`)
	if r.status != http.StatusConflict {
		t.Fatalf("stale If-Match = %d, want 409", r.status)
	}
	if code, _, _ := errorOf(t, r); code != "stale_revision" {
		t.Errorf("code = %q", code)
	}
}

// RULE (ADR 0002 section 5): a valid key's scope refusal writes one audit row
// attributed to the key, naming the scope it lacked.
func TestWire_MachineKeyRefusalWritesAuditRow(t *testing.T) {
	f := newFixture(t)
	techSvc := techadmin.NewService(techadmin.NewRepository(f.db)).WithTxRunner(f.db)
	raw, key, err := techSvc.GenerateKey(context.Background(), "gov refusal key", []string{"governance:read"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = f.db.Pool.Exec(context.Background(), `DELETE FROM audit_log WHERE actor_id = $1`, key.ID)
		_, _ = f.db.Pool.Exec(context.Background(), `DELETE FROM api_keys WHERE id = $1`, key.ID)
	})
	validator := govKeyValidator{svc: techSvc}
	auth := middleware.NewMachineKeyAuth(validator, audit.NewLogger(f.db), nil, nil)
	svc := governance.NewService(governance.NewRepository(f.db), governance.NewTemplateAIProvider()).WithTxRunner(f.db)
	mux := http.NewServeMux()
	governance.NewHandler(svc).RegisterRoutes(mux)
	srv := httptest.NewServer(auth.Handler(mux))
	defer srv.Close()

	get := func(method, path string) int {
		req, _ := http.NewRequest(method, srv.URL+path, nil)
		req.Header.Set("Authorization", "Bearer "+raw)
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		res.Body.Close()
		return res.StatusCode
	}
	if c := get("GET", "/api/v1/governance/rfcs"); c != http.StatusOK {
		t.Fatalf("governance:read on the list = %d, want 200", c)
	}
	if c := get("POST", "/api/v1/governance/rfcs"); c != http.StatusForbidden {
		t.Fatalf("governance:read on a create = %d, want 403", c)
	}

	var scope string
	err = f.db.Pool.QueryRow(context.Background(),
		`SELECT changes->>'scope' FROM audit_log
		  WHERE action = 'key.scope_refused' AND actor_kind = 'key' AND actor_id = $1`, key.ID,
	).Scan(&scope)
	if err != nil {
		t.Fatalf("no key.scope_refused audit row for the key: %v", err)
	}
	if scope != "governance:write" {
		t.Errorf("refused scope = %q, want governance:write", scope)
	}
}

// RULE: limit, cursor and the status filter are validated, never clamped or
// ignored; a valid status value no row holds serves an empty page whose items
// are [] in the bytes, never null.
func TestWire_ListStrictness(t *testing.T) {
	f := newFixture(t)
	for _, tc := range []struct {
		url   string
		field string
		code  string
	}{
		{"/api/v1/governance/rfcs?limit=0", "limit", "validation_failed"},
		{"/api/v1/governance/rfcs?limit=999999", "limit", "validation_failed"},
		{"/api/v1/governance/rfcs?cursor=garbage", "cursor", "bad_request"},
		{"/api/v1/governance/rfcs?status=APPROVED", "status", "validation_failed"},
		{"/api/v1/governance/rfcs?flavour=x", "flavour", "unsupported_query_parameter"},
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

	// rejected is a valid status the seed does not use, so the page is empty;
	// this test's own rejected RFCs are dropped by createRFC's cleanup.
	empty := f.do("GET", "/api/v1/governance/rfcs?status=rejected", nil)
	if empty.status != http.StatusOK {
		t.Fatalf("empty page = %d: %s", empty.status, empty.raw)
	}
	if items, ok := empty.body["items"].([]any); !ok || len(items) != 0 {
		t.Errorf("empty page items = %#v (%s), want []", empty.body["items"], empty.raw)
	}
	if !bytes.Contains(empty.raw, []byte(`"items":[]`)) {
		t.Errorf("empty page body %s does not carry items as []", empty.raw)
	}
}
