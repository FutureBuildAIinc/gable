// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package unit_test

// The unit catalogue on the wire contract (ADR 0001 and ADR 0006 section
// 2), tested end to end: a real Postgres, the real handler on a real mux
// behind the real idempotency middleware, requests as JSON and responses
// read back as JSON. Nothing here touches a module type, so each test
// states a wire fact. The catalogue list of ADR 0006 section 9.2: create,
// deactivate, a locked system row's PUT refused naming the field, the
// revision preconditions, the filters, and the `units` machine key scope.

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gablelbm/gable/internal/testutil"
	"github.com/gablelbm/gable/internal/unit"
	"github.com/gablelbm/gable/pkg/database"
	"github.com/gablelbm/gable/pkg/middleware"
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
	repo := unit.NewRepository(db)
	svc := unit.NewService(repo)
	mux := http.NewServeMux()
	unit.NewHandler(svc).RegisterRoutes(mux, pass, pass)
	f.srv = httptest.NewServer(middleware.Idempotency(db)(mux))
	t.Cleanup(func() {
		f.srv.Close()
		// The fixture's units are dealer rows the test made: remove them,
		// and restore the seeded row the update test renames, so the suite
		// reruns against the same database.
		_, _ = db.Pool.Exec(context.Background(),
			`DELETE FROM units WHERE code IN ('SKID', 'CRIB')`)
		_, _ = db.Pool.Exec(context.Background(),
			`UPDATE units SET name = 'Thousand board feet', revision = 1 WHERE code = 'MBF'`)
	})
	return f
}

// pass is a guard that admits every caller: the wire tests exercise the
// catalogue's own rules, not the role guard.
func pass(h http.Handler) http.Handler { return h }

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
	req.Header.Set("X-Request-ID", "req-unit-wire")
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

func (f *fixture) createUnit(overrides map[string]any) resp {
	f.t.Helper()
	body := map[string]any{
		"code": fmt.Sprintf("U%05d", 1000+len(overrides)),
		"name": "Dealer unit",
	}
	for k, v := range overrides {
		body[k] = v
	}
	return f.do("POST", "/api/v1/units", body)
}

func TestUnitCatalogueCreateAndRead(t *testing.T) {
	f := newFixture(t)

	// Create: 201, Location, the seeded shape on the wire, revision 1.
	res := f.createUnit(map[string]any{
		"code": "SKID", "name": "Skid", "dimension": "count",
		"std_unit_qty": "1", "std_ref_qty": "144",
	})
	if res.status != http.StatusCreated {
		f.t.Fatalf("create = %d: %s", res.status, res.raw)
	}
	if loc := res.header.Get("Location"); loc != "/api/v1/units/SKID" {
		f.t.Errorf("Location = %s", loc)
	}
	for field, want := range map[string]any{
		"code": "SKID", "name": "Skid", "dimension": "count",
		"std_unit_qty": "1", "std_ref_qty": "144",
		"is_system": false, "is_active": true, "revision": json.Number("1"),
	} {
		if got := res.body[field]; got != want {
			f.t.Errorf("%s = %v; want %v", field, got, want)
		}
	}
	if _, has := res.body["created_at"]; !has {
		f.t.Errorf("created_at is present on the wire")
	}

	// A duplicate code is a 409 duplicate naming the field.
	dup := f.createUnit(map[string]any{"code": "SKID", "name": "Again", "dimension": "count"})
	if dup.status != http.StatusConflict || dup.body["error"].(map[string]any)["code"] != "duplicate" {
		f.t.Errorf("a duplicate code is a 409 duplicate, got %d: %s", dup.status, dup.raw)
	}

	// The read by code serves the same row with its ETag.
	get := f.do("GET", "/api/v1/units/SKID", nil)
	if get.status != http.StatusOK || get.body["name"] != "Skid" {
		f.t.Fatalf("get = %d: %s", get.status, get.raw)
	}
	if get.header.Get("ETag") == "" {
		f.t.Errorf("the read carries the revision's ETag")
	}
	// The GAL row of the seed, its standard size in lowest terms.
	gal := f.do("GET", "/api/v1/units/GAL", nil)
	if gal.status != http.StatusOK || gal.body["dimension"] != "volume" ||
		gal.body["std_unit_qty"] != "576" || gal.body["std_ref_qty"] != "77" ||
		gal.body["is_system"] != true {
		f.t.Errorf("the seeded GAL row reads back canonical, got %d: %s", gal.status, gal.raw)
	}

	// Validation in one 400: a bad code, a bad dimension, half a pair.
	bad := f.createUnit(map[string]any{"code": "toolongcode", "dimension": "mass", "std_unit_qty": "1"})
	if bad.status != http.StatusBadRequest {
		f.t.Fatalf("a bad create is a 400, got %d: %s", bad.status, bad.raw)
	}
	details := bad.body["error"].(map[string]any)["details"].([]any)
	fields := map[string]bool{}
	for _, d := range details {
		fields[d.(map[string]any)["field"].(string)] = true
	}
	for _, want := range []string{"code", "dimension", "std_ref_qty"} {
		if !fields[want] {
			f.t.Errorf("the 400 names %s; it carries %v", want, fields)
		}
	}
	// A body carrying is_system is refused naming it.
	sys := f.createUnit(map[string]any{"code": "NOSY", "name": "No system", "dimension": "count", "is_system": true})
	if sys.status != http.StatusBadRequest || !strings.Contains(string(sys.raw), "is_system") {
		f.t.Errorf("is_system in a create is a 400 naming it, got %d: %s", sys.status, sys.raw)
	}
}

func TestUnitCatalogueUpdateRules(t *testing.T) {
	f := newFixture(t)
	created := f.createUnit(map[string]any{"code": "CRIB", "name": "Crib", "dimension": "count"})
	if created.status != http.StatusCreated {
		f.t.Fatalf("create = %d: %s", created.status, created.raw)
	}

	// The precondition: a PUT with neither If-Match nor a body revision is
	// a 428; a stale one is a 409 stale_revision.
	noPre := f.do("PUT", "/api/v1/units/CRIB", map[string]any{"name": "Renamed"})
	if noPre.status != http.StatusPreconditionRequired {
		f.t.Errorf("a PUT without a precondition is a 428, got %d", noPre.status)
	}
	stale := f.do("PUT", "/api/v1/units/CRIB", map[string]any{"name": "Renamed", "revision": 99})
	if stale.status != http.StatusConflict {
		f.t.Errorf("a stale revision is a 409, got %d", stale.status)
	}

	// A plain edit: name and is_active (deactivation), the new revision
	// and ETag on the answer.
	edit := f.do("PUT", "/api/v1/units/CRIB", map[string]any{
		"name": "Crib of 120", "is_active": false, "revision": 1,
	})
	if edit.status != http.StatusOK {
		f.t.Fatalf("edit = %d: %s", edit.status, edit.raw)
	}
	if edit.body["name"] != "Crib of 120" || edit.body["is_active"] != false || edit.body["revision"] != json.Number("2") {
		f.t.Errorf("the edit answers the new row: %s", edit.raw)
	}

	// A locked system row's PUT is refused naming the field.
	dim := f.do("PUT", "/api/v1/units/MBF", map[string]any{"dimension": "count", "revision": 1})
	if dim.status != http.StatusBadRequest || !strings.Contains(string(dim.raw), "dimension") {
		f.t.Errorf("a system unit's dimension change is a 400 naming it, got %d: %s", dim.status, dim.raw)
	}
	std := f.do("PUT", "/api/v1/units/MBF", map[string]any{"std_unit_qty": "1", "std_ref_qty": "1200", "revision": 1})
	if std.status != http.StatusBadRequest || !strings.Contains(string(std.raw), "std_unit_qty") {
		f.t.Errorf("a system unit's standard size change is a 400 naming it, got %d: %s", std.status, std.raw)
	}
	// A system unit's name still edits (deactivation only applies to
	// deletes; the name is the dealer's label).
	renamed := f.do("PUT", "/api/v1/units/MBF", map[string]any{"name": "Thousand board feet, custom label", "revision": 1})
	if renamed.status != http.StatusOK || renamed.body["name"] != "Thousand board feet, custom label" {
		t.Errorf("a system unit's name edits, got %d: %s", renamed.status, renamed.raw)
	}

	// A system unit PUT whole, carrying the stored standard size unchanged,
	// is served: only a change is refused, like the dimension branch beside
	// it, which compares first.
	whole := f.do("PUT", "/api/v1/units/MBF", map[string]any{
		"name": "Thousand board feet", "std_unit_qty": "1", "std_ref_qty": "1000", "revision": 2,
	})
	if whole.status != http.StatusOK || whole.body["name"] != "Thousand board feet" ||
		whole.body["std_unit_qty"] != "1" || whole.body["std_ref_qty"] != "1000" {
		t.Errorf("a system unit PUT with the stored standard size is served, got %d: %s", whole.status, whole.raw)
	}

	// A body carrying code is refused naming it, and an unknown unit is a
	// 404.
	withCode := f.do("PUT", "/api/v1/units/CRIB", map[string]any{"code": "OTHER", "revision": 2})
	if withCode.status != http.StatusBadRequest || !strings.Contains(string(withCode.raw), "code") {
		f.t.Errorf("code in a PUT is a 400 naming it, got %d: %s", withCode.status, withCode.raw)
	}
	if missing := f.do("GET", "/api/v1/units/ZZZZZZ", nil); missing.status != http.StatusNotFound {
		f.t.Errorf("an unknown unit is a 404, got %d", missing.status)
	}
}

func TestUnitCatalogueList(t *testing.T) {
	f := newFixture(t)

	res := f.do("GET", "/api/v1/units?limit=5", nil)
	if res.status != http.StatusOK {
		f.t.Fatalf("list = %d: %s", res.status, res.raw)
	}
	items := res.body["items"].([]any)
	if len(items) != 5 {
		f.t.Fatalf("the page holds the limit, got %d", len(items))
	}
	// Ordering units.code: the first codes are alphabetical.
	if items[0].(map[string]any)["code"] != "BAG" || items[1].(map[string]any)["code"] != "BF" {
		f.t.Errorf("the list orders by code, got %v", items)
	}
	if res.body["next_cursor"] == nil {
		f.t.Fatalf("another page exists, so next_cursor is a string")
	}
	// The cursor walks every row once.
	seen := map[string]bool{}
	cursor := ""
	pages := 0
	for {
		path := "/api/v1/units?limit=7"
		if cursor != "" {
			path += "&cursor=" + cursor
		}
		page := f.do("GET", path, nil)
		if page.status != http.StatusOK {
			f.t.Fatalf("page = %d: %s", page.status, page.raw)
		}
		for _, it := range page.body["items"].([]any) {
			code := it.(map[string]any)["code"].(string)
			if seen[code] {
				t.Fatalf("code %s repeats across pages", code)
			}
			seen[code] = true
		}
		next, _ := page.body["next_cursor"].(string)
		if next == "" {
			break
		}
		cursor = next
		pages++
		if pages > 10 {
			t.Fatal("the cursor does not terminate")
		}
	}
	if len(seen) < 23 {
		t.Errorf("the seeded catalogue holds at least 23 units, walked %d", len(seen))
	}

	// The filters filter; an unknown value or parameter is a 400.
	board := f.do("GET", "/api/v1/units?dimension=board_measure", nil)
	if board.status != http.StatusOK {
		t.Fatalf("dimension filter = %d", board.status)
	}
	for _, it := range board.body["items"].([]any) {
		if it.(map[string]any)["dimension"] != "board_measure" {
			t.Errorf("the dimension filter filters, got %v", it)
		}
	}
	if bad := f.do("GET", "/api/v1/units?dimension=BOARD_MEASURE", nil); bad.status != http.StatusBadRequest {
		t.Errorf("an uppercase dimension value is a 400, got %d", bad.status)
	}
	if bad := f.do("GET", "/api/v1/units?active=true", nil); bad.status != http.StatusBadRequest {
		t.Errorf("an unknown parameter is a 400, got %d", bad.status)
	}
	// include=total.
	withTotal := f.do("GET", "/api/v1/units?include=total&limit=1", nil)
	if withTotal.status != http.StatusOK || withTotal.body["total"] == nil {
		t.Errorf("include=total carries the count, got %s", withTotal.raw)
	}
	// An empty filtered page is [] in the bytes.
	empty := f.do("GET", "/api/v1/units?dimension=count&is_active=false", nil)
	if empty.status != http.StatusOK || !strings.Contains(string(empty.raw), `"items":[]`) {
		t.Errorf("an empty page is [] in the bytes, got %s", empty.raw)
	}
}

// TestUnitsMachineKeyScope pins the module's place in the machine key
// vocabulary (ADR 0006 2.3): `units` joins the module scope table, so
// units:read and units:write exist (ADR 0002). The end to end refusal with
// its audit row is pinned by the machine key golden group; the census test
// holds the vocabulary against the registered routes.
func TestUnitsMachineKeyScope(t *testing.T) {
	if middleware.ModuleScopePolicyFor("units") != middleware.ModuleScopeAllowed {
		t.Fatal("units is a module of the machine key vocabulary, so units:read and units:write exist")
	}
	modules := middleware.MachineKeyModules()
	found := false
	for _, m := range modules {
		if m == "units" {
			found = true
		}
	}
	if !found {
		t.Fatal("units is listed by MachineKeyModules")
	}
}
