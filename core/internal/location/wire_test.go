// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package location_test

// The location and branch routes on the wire contract (ADR 0001, ADR 0006
// 7.2): the list envelopes with the cursor walk and include=total, the error
// envelope, and the revision precondition on the writes. These tests state
// wire facts against a real Postgres; the branch wall itself is proved in
// the module's service tests and the branch middleware's own suite.

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/gablelbm/gable/internal/location"
	"github.com/gablelbm/gable/internal/testutil"
	"github.com/gablelbm/gable/pkg/database"
	"github.com/gablelbm/gable/pkg/middleware"
	"github.com/google/uuid"
)

type fixture struct {
	t   *testing.T
	db  *database.DB
	srv *httptest.Server
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	t.Setenv("AUTH_MODE", "dev")
	db := testutil.RequireDB(t)
	f := &fixture{t: t, db: db}
	svc := location.NewService(location.NewRepository(db))
	h := location.NewHandler(svc, location.NewUserRepository(db))
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)
	f.srv = httptest.NewServer(middleware.Idempotency(db)(mux))
	t.Cleanup(func() { f.srv.Close() })
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
	req.Header.Set("X-Request-ID", "req-loc-wire")
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

func (f *fixture) createBranch(name string) resp {
	return f.do("POST", "/api/v1/branches", map[string]any{
		"code": name, "name": name, "type": "branch",
	}, "Idempotency-Key", uuid.NewString())
}

// TestBranchListEnvelopeAndRevision: the branches list is the envelope with
// a cursor walk, and the branch write carries the revision precondition
// (428 without one, 409 stale, 200 with the new revision and ETag). Each
// half failed on the base commit, which answered a bare array and took
// unconditional writes.
func TestBranchListEnvelopeAndRevision(t *testing.T) {
	f := newFixture(t)
	created := f.createBranch("WIRE-branch-" + uuid.NewString()[:8])
	if created.status != http.StatusCreated {
		t.Fatalf("create: %d %s", created.status, created.raw)
	}
	id := created.body["id"].(string)
	defer func() {
		_, _ = f.db.Pool.Exec(context.Background(), `DELETE FROM locations WHERE id = $1`, id)
	}()

	list := f.do("GET", "/api/v1/branches?limit=1&include=total", nil)
	if list.status != http.StatusOK {
		t.Fatalf("list: %d %s", list.status, list.raw)
	}
	if list.body["items"] == nil || list.body["total"] == nil {
		t.Fatalf("branches list is not the envelope with include=total: %s", list.raw)
	}
	if list.body["limit"] != json.Number("1") {
		t.Fatalf("limit = %v", list.body["limit"])
	}
	found := false
	for _, it := range list.body["items"].([]any) {
		if it.(map[string]any)["id"] == id {
			found = true
			if it.(map[string]any)["type"] != "branch" {
				t.Fatalf("type = %v, want lowercase branch", it.(map[string]any)["type"])
			}
		}
	}
	if !found {
		t.Fatalf("the created branch is not on the first page: %s", list.raw)
	}

	// The write's preconditions.
	put := func(body map[string]any, headers ...string) resp {
		if body == nil {
			body = map[string]any{}
		}
		return f.do("PUT", "/api/v1/branches/"+id, body, headers...)
	}
	body := map[string]any{"code": created.body["code"], "name": "Renamed Yard", "active": true}
	if res := put(body); res.status != http.StatusPreconditionRequired {
		t.Fatalf("no precondition: %d, want 428", res.status)
	}
	stale := map[string]any{"code": created.body["code"], "name": "Renamed Yard", "active": true, "revision": 99}
	if res := put(stale); res.status != http.StatusConflict {
		t.Fatalf("stale revision: %d, want 409", res.status)
	}
	body["revision"] = created.body["revision"]
	res := put(body)
	if res.status != http.StatusOK {
		t.Fatalf("good revision: %d %s", res.status, res.raw)
	}
	if res.body["revision"] != json.Number("2") || res.header.Get("ETag") != `"2"` {
		t.Fatalf("write answered revision %v ETag %q", res.body["revision"], res.header.Get("ETag"))
	}
	if res.body["name"] != "Renamed Yard" {
		t.Fatalf("name = %v", res.body["name"])
	}
}

// TestLocationsListAndTree: the locations list is the envelope, and a tree
// read of a branch the caller may target answers the envelope with the
// branch's rows.
func TestLocationsListAndTree(t *testing.T) {
	f := newFixture(t)
	created := f.createBranch("WIRE-tree-" + uuid.NewString()[:8])
	if created.status != http.StatusCreated {
		t.Fatalf("create: %d %s", created.status, created.raw)
	}
	id := created.body["id"].(string)
	defer func() {
		_, _ = f.db.Pool.Exec(context.Background(), `DELETE FROM locations WHERE id = $1`, id)
	}()

	list := f.do("GET", "/api/v1/locations?limit=2", nil)
	if list.status != http.StatusOK || list.body["items"] == nil {
		t.Fatalf("locations list is not the envelope: %d %s", list.status, list.raw)
	}
	// An unsupported parameter is refused.
	if res := f.do("GET", "/api/v1/locations?verbose=1", nil); res.status != http.StatusBadRequest {
		t.Fatalf("unknown parameter: %d, want 400", res.status)
	}

	tree := f.do("GET", "/api/v1/branches/"+id+"/tree", nil)
	if tree.status != http.StatusOK || tree.body["items"] == nil {
		t.Fatalf("tree is not the envelope: %d %s", tree.status, tree.raw)
	}
	if len(tree.body["items"].([]any)) != 1 {
		t.Fatalf("the tree of a bare branch carries %d rows, want 1", len(tree.body["items"].([]any)))
	}

	// The error envelope on a 404.
	missing := f.do("GET", "/api/v1/locations/"+uuid.NewString(), nil)
	if missing.status != http.StatusNotFound {
		t.Fatalf("missing location: %d", missing.status)
	}
	if errBody := missing.body["error"].(map[string]any); errBody["code"] != "not_found" {
		t.Fatalf("code = %v", errBody["code"])
	}
}

func fieldNames(res resp) map[string]bool {
	out := map[string]bool{}
	errBody, _ := res.body["error"].(map[string]any)
	details, _ := errBody["details"].([]any)
	for _, d := range details {
		out[d.(map[string]any)["field"].(string)] = true
	}
	return out
}

// TestLocationCreateAndValidation: the create answers revision 1 with its
// ETag and Location and every optional field as null, one 400 names every
// offending field, and a body field the route does not declare is refused.
func TestLocationCreateAndValidation(t *testing.T) {
	f := newFixture(t)
	branch := f.createBranch("WIRE-loc-" + uuid.NewString()[:8])
	branchID := branch.body["id"].(string)
	defer func() {
		_, _ = f.db.Pool.Exec(context.Background(), `DELETE FROM locations WHERE parent_id = $1 OR id = $1`, branchID)
	}()

	yard := f.do("POST", "/api/v1/locations", map[string]any{
		"code": "WY-" + uuid.NewString()[:6], "type": "yard", "name": "Wire yard", "parent_id": branchID,
	}, "Idempotency-Key", uuid.NewString())
	if yard.status != http.StatusCreated {
		t.Fatalf("create: %d %s", yard.status, yard.raw)
	}
	if yard.body["type"] != "yard" || yard.body["revision"] != json.Number("1") || yard.header.Get("ETag") != `"1"` {
		t.Fatalf("shape: type=%v revision=%v ETag=%q", yard.body["type"], yard.body["revision"], yard.header.Get("ETag"))
	}
	if loc := yard.header.Get("Location"); loc != "/api/v1/locations/"+yard.body["id"].(string) {
		t.Fatalf("Location = %q", loc)
	}
	for _, field := range []string{"description", "address", "city", "phone", "default_tax_rate"} {
		if v, ok := yard.body[field]; !ok || v != nil {
			t.Errorf("%s = %v (present=%v), want present as null", field, v, ok)
		}
	}
	if yard.body["branch_id"] != branchID {
		t.Fatalf("branch_id = %v, want the denormalised branch", yard.body["branch_id"])
	}

	bad := f.do("POST", "/api/v1/locations", map[string]any{"type": "YARD", "parent_id": "not-a-uuid"}, "Idempotency-Key", uuid.NewString())
	if bad.status != http.StatusBadRequest {
		t.Fatalf("validation: %d, want 400", bad.status)
	}
	for _, want := range []string{"type", "code", "parent_id"} {
		if !fieldNames(bad)[want] {
			t.Errorf("details miss %q: %s", want, bad.raw)
		}
	}
	for name, body := range map[string]map[string]any{
		"a revision on create":   {"code": "X", "type": "yard", "revision": 1},
		"the legacy uppercase":   {"code": "X", "type": "BIN"},
		"a field never declared": {"code": "X", "type": "bin", "is_default": true},
	} {
		if res := f.do("POST", "/api/v1/locations", body, "Idempotency-Key", uuid.NewString()); res.status != http.StatusBadRequest {
			t.Errorf("%s: %d, want 400 (%s)", name, res.status, res.raw)
		}
	}

	// A branch create takes no type or parent: the route owns the type.
	noName := f.do("POST", "/api/v1/branches", map[string]any{"code": "NONAME"}, "Idempotency-Key", uuid.NewString())
	if noName.status != http.StatusBadRequest || !fieldNames(noName)["name"] {
		t.Fatalf("a branch without a name: %d %s, want 400 naming name", noName.status, noName.raw)
	}
}

// TestLocationRevisionAndArchive: the PUT names the revision (428, 409, weak
// and strong If-Match), the type and parent are fixed at create (a body that
// names one is a 400), an omitted active keeps the stored value, and the
// delete archives at the revision. A branch route refuses a row that is not
// a branch.
func TestLocationRevisionAndArchive(t *testing.T) {
	f := newFixture(t)
	branch := f.createBranch("WIRE-rev-" + uuid.NewString()[:8])
	branchID := branch.body["id"].(string)
	defer func() {
		_, _ = f.db.Pool.Exec(context.Background(), `DELETE FROM locations WHERE parent_id = $1 OR id = $1`, branchID)
	}()
	bin := f.do("POST", "/api/v1/locations", map[string]any{
		"code": "WB-" + uuid.NewString()[:6], "type": "bin", "parent_id": branchID,
	}, "Idempotency-Key", uuid.NewString())
	id := bin.body["id"].(string)
	path := "/api/v1/locations/" + id

	body := map[string]any{"code": bin.body["code"], "name": "Renamed"}
	if res := f.do("PUT", path, body); res.status != http.StatusPreconditionRequired {
		t.Fatalf("no revision: %d, want 428", res.status)
	}
	if res := f.do("PUT", path, map[string]any{"code": "X", "type": "yard"}, "If-Match", `"1"`); res.status != http.StatusBadRequest {
		t.Fatalf("a type on update: %d, want 400", res.status)
	}
	if res := f.do("PUT", path, map[string]any{"code": "X", "parent_id": branchID}, "If-Match", `"1"`); res.status != http.StatusBadRequest {
		t.Fatalf("a parent on update: %d, want 400", res.status)
	}
	ok := f.do("PUT", path, body, "If-Match", `W/"1"`)
	if ok.status != http.StatusOK || ok.body["revision"] != json.Number("2") || ok.body["active"] != true {
		t.Fatalf("weak If-Match update: %d %s (an omitted active keeps true)", ok.status, ok.raw)
	}
	if res := f.do("PUT", path, body, "If-Match", `"1"`); res.status != http.StatusConflict {
		t.Fatalf("stale: %d, want 409", res.status)
	}
	off := f.do("PUT", path, map[string]any{"code": bin.body["code"], "active": false}, "If-Match", `"2"`)
	if off.status != http.StatusOK || off.body["active"] != false {
		t.Fatalf("deactivate: %d %s", off.status, off.raw)
	}
	keep := f.do("PUT", path, map[string]any{"code": bin.body["code"], "name": "again"}, "If-Match", `"3"`)
	if keep.status != http.StatusOK || keep.body["active"] != false {
		t.Fatalf("an omitted active must keep the stored false: %d %s", keep.status, keep.raw)
	}

	// A branch route never acts on a bin.
	for _, call := range [][]string{{"PUT", "/api/v1/branches/" + id}, {"DELETE", "/api/v1/branches/" + id}, {"GET", "/api/v1/branches/" + id}} {
		var rb any
		if call[0] == "PUT" {
			rb = map[string]any{"code": "x", "revision": 4}
		}
		if res := f.do(call[0], call[1], rb, "If-Match", `"4"`); res.status != http.StatusNotFound {
			t.Errorf("%s %s on a bin: %d, want 404", call[0], call[1], res.status)
		}
	}

	if res := f.do("DELETE", path, nil); res.status != http.StatusPreconditionRequired {
		t.Fatalf("delete with no revision: %d, want 428", res.status)
	}
	if res := f.do("DELETE", path, nil, "If-Match", `"1"`); res.status != http.StatusConflict {
		t.Fatalf("delete stale: %d, want 409", res.status)
	}
	if res := f.do("DELETE", path, nil, "If-Match", `"4"`); res.status != http.StatusNoContent {
		t.Fatalf("delete: %d, want 204", res.status)
	}
	if res := f.do("DELETE", "/api/v1/locations/"+uuid.NewString(), nil, "If-Match", `"1"`); res.status != http.StatusNotFound {
		t.Fatalf("delete unknown: %d, want 404", res.status)
	}
}

// TestLocationListCursorWalk: the list walks every row once under a small
// limit, include=total counts the rows, a malformed cursor is a 400 naming
// cursor, and the branch list refuses an include_inactive that is not a
// boolean.
func TestLocationListCursorWalk(t *testing.T) {
	f := newFixture(t)
	var ids []string
	for i := 0; i < 3; i++ {
		b := f.createBranch("WIRE-walk-" + uuid.NewString()[:8])
		ids = append(ids, b.body["id"].(string))
	}
	defer func() {
		for _, id := range ids {
			_, _ = f.db.Pool.Exec(context.Background(), `DELETE FROM locations WHERE id = $1`, id)
		}
	}()
	total := f.do("GET", "/api/v1/locations?limit=1&include=total", nil).body["total"]
	seen := map[string]bool{}
	cursor := ""
	pages := 0
	for {
		url := "/api/v1/locations?limit=50"
		if cursor != "" {
			url += "&cursor=" + cursor
		}
		res := f.do("GET", url, nil)
		for _, it := range res.body["items"].([]any) {
			id := it.(map[string]any)["id"].(string)
			if seen[id] {
				t.Fatalf("row %s served twice", id)
			}
			seen[id] = true
		}
		pages++
		next, _ := res.body["next_cursor"].(string)
		if next == "" || pages > 50 {
			break
		}
		cursor = next
	}
	if json.Number(strconv.Itoa(len(seen))) != total {
		t.Fatalf("the walk served %d rows while include=total says %v", len(seen), total)
	}
	for _, id := range ids {
		if !seen[id] {
			t.Errorf("created branch %s is missing from the walk", id)
		}
	}
	if res := f.do("GET", "/api/v1/locations?cursor=garbage", nil); res.status != http.StatusBadRequest || !fieldNames(res)["cursor"] {
		t.Fatalf("bad cursor: %d %s", res.status, res.raw)
	}
	if res := f.do("GET", "/api/v1/branches?include_inactive=maybe", nil); res.status != http.StatusBadRequest {
		t.Fatalf("include_inactive=maybe: %d, want 400", res.status)
	}
}
