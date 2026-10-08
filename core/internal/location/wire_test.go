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
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
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
		_, _ = f.db.Pool.Exec(f.t.Context(), `DELETE FROM locations WHERE id = $1`, id)
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
		_, _ = f.db.Pool.Exec(f.t.Context(), `DELETE FROM locations WHERE id = $1`, id)
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
