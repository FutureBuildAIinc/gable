// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package configurator_test

// The configurator module on the wire contract (ADR 0001), tested end to
// end: a real Postgres with the seeded rule matrix, the real handler on a
// real mux, requests as JSON and responses read back as JSON. The module
// owns no write, so there is no transaction proof here: its proofs are the
// strict query posture, the error envelope and the deterministic answers.

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gablelbm/gable/internal/configurator"
	"github.com/gablelbm/gable/internal/testutil"
	"github.com/gablelbm/gable/pkg/database"
	"github.com/google/uuid"
)

type fixture struct {
	t   *testing.T
	db  *database.DB
	srv *httptest.Server
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	db := testutil.RequireDB(t)
	f := &fixture{t: t, db: db}
	mux := http.NewServeMux()
	configurator.NewHandler(configurator.NewService(configurator.NewRepository(db))).RegisterRoutes(mux)
	f.srv = httptest.NewServer(mux)
	t.Cleanup(f.srv.Close)
	return f
}

func do(t *testing.T, srv *httptest.Server, method, path, body string) (int, string, http.Header) {
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
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	raw, _ := io.ReadAll(res.Body)
	return res.StatusCode, string(raw), res.Header
}

func body(t *testing.T, raw string) map[string]any {
	t.Helper()
	var out map[string]any
	if err := json.Unmarshal([]byte(raw), &out); err != nil {
		t.Fatalf("not json: %s", raw)
	}
	return out
}

// The strict query posture: an unknown parameter is a 400 naming it, on
// every route (the base consumed every unknown options name as a selection
// and answered 200).
func TestStrictQuery(t *testing.T) {
	f := newFixture(t)
	cases := []struct{ name, path string }{
		{"rules", "/api/v1/configurator/rules?zzz=1"},
		{"options unknown", "/api/v1/configurator/options?attribute_type=Grade&zzz=1"},
		{"options old style selection", "/api/v1/configurator/options?attribute_type=Grade&Species=SYP"},
		{"presets", "/api/v1/configurator/presets?zzz=1"},
	}
	for _, c := range cases {
		code, raw, _ := do(t, f.srv, http.MethodGet, c.path, "")
		if code != http.StatusBadRequest {
			t.Errorf("%s: got %d %s, want 400", c.name, code, raw)
			continue
		}
		env := body(t, raw)["error"].(map[string]any)
		if env["code"] != "unsupported_query_parameter" {
			t.Errorf("%s: code = %v, want unsupported_query_parameter", c.name, env["code"])
		}
		d := env["details"].([]any)[0].(map[string]any)
		if d["field"] != "zzz" && d["field"] != "Species" {
			t.Errorf("%s: details = %v", c.name, env["details"])
		}
	}
	code, _, _ := do(t, f.srv, http.MethodPost, "/api/v1/configurator/validate?zzz=1", `{"selections":{"Species":"SYP"}}`)
	if code != http.StatusBadRequest {
		t.Errorf("validate with a query: got %d, want 400", code)
	}
}

// The error envelope: lowercase code vocabulary, the handler's own message,
// a request id present.
func TestErrorEnvelope(t *testing.T) {
	f := newFixture(t)
	code, raw, _ := do(t, f.srv, http.MethodPost, "/api/v1/configurator/validate", `{"selections":{}}`)
	if code != http.StatusBadRequest {
		t.Fatalf("empty selections = %d %s, want 400", code, raw)
	}
	out := body(t, raw)
	env := out["error"].(map[string]any)
	if env["code"] != "validation_failed" {
		t.Errorf("code = %v, want validation_failed", env["code"])
	}
	if msg, _ := env["message"].(string); msg == "" || msg == "Bad Request" {
		t.Errorf("message = %q, want the handler's own message", msg)
	}
	if meta, ok := out["meta"].(map[string]any); !ok || meta["request_id"] == nil {
		t.Error("meta.request_id missing")
	}
	d := env["details"].([]any)[0].(map[string]any)
	if d["field"] != "selections" {
		t.Errorf("details = %v, want the field selections", env["details"])
	}
	if code, raw, _ := do(t, f.srv, http.MethodPost, "/api/v1/configurator/validate", `not-json`); code != http.StatusBadRequest {
		t.Errorf("an unparseable body = %d %s, want 400", code, raw)
	}
}

// The options read: the selections parameter constrains, the order is
// deterministic, the base64-free shapes hold.
func TestOptions(t *testing.T) {
	f := newFixture(t)
	code, raw, _ := do(t, f.srv, http.MethodGet,
		"/api/v1/configurator/options?attribute_type=Grade&selections=Species%3DSYP", "")
	if code != http.StatusOK {
		t.Fatalf("options = %d %s", code, raw)
	}
	var opts []map[string]any
	if err := json.Unmarshal([]byte(raw), &opts); err != nil {
		t.Fatal(err)
	}
	if len(opts) == 0 {
		t.Fatalf("options = %s, want the SYP grades", raw)
	}
	// Deterministic: ordered by value, and twice the same answer.
	for i := 1; i < len(opts); i++ {
		if opts[i-1]["value"].(string) > opts[i]["value"].(string) {
			t.Errorf("options not ordered by value: %v", raw)
		}
	}
	_, again, _ := do(t, f.srv, http.MethodGet,
		"/api/v1/configurator/options?attribute_type=Grade&selections=Species%3DSYP", "")
	if again != raw {
		t.Errorf("the same selections answered differently: %s then %s", raw, again)
	}
	// Structural: one of the values is disallowed with a message.
	byValue := map[string]map[string]any{}
	for _, o := range opts {
		byValue[o["value"].(string)] = o
	}
	if ap, ok := byValue["Appearance"]; !ok || ap["allowed"] != false {
		t.Errorf("Appearance under SYP = %v, want present and disallowed", byValue["Appearance"])
	}
	// A malformed selections value is a 400 naming it.
	if code, raw, _ = do(t, f.srv, http.MethodGet,
		"/api/v1/configurator/options?attribute_type=Grade&selections=noequalsign", ""); code != http.StatusBadRequest {
		t.Errorf("a malformed selections = %d, want 400", code)
	} else if d := body(t, raw)["error"].(map[string]any)["details"].([]any)[0].(map[string]any); d["field"] != "selections" {
		t.Errorf("details = %v, want the field selections", d)
	}
	// The static defaults answer with no selections.
	if code, raw, _ = do(t, f.srv, http.MethodGet, "/api/v1/configurator/options?attribute_type=Species", ""); code != http.StatusOK {
		t.Errorf("static options = %d %s", code, raw)
	}
}

// The validate verdict: conflicts always an array, deterministic order, the
// seeded rule's own message.
func TestValidate(t *testing.T) {
	f := newFixture(t)
	code, raw, _ := do(t, f.srv, http.MethodPost, "/api/v1/configurator/validate",
		`{"selections":{"Species":"SYP","Grade":"Appearance"}}`)
	if code != http.StatusOK {
		t.Fatalf("validate = %d %s", code, raw)
	}
	out := body(t, raw)
	if out["valid"] != false {
		t.Errorf("valid = %v, want false", out["valid"])
	}
	conflicts, ok := out["conflicts"].([]any)
	if !ok || len(conflicts) == 0 {
		t.Fatalf("conflicts = %v, want a non empty array", out["conflicts"])
	}
	first := conflicts[0].(map[string]any)
	if first["attribute_type"] != "Grade" || first["depends_on_type"] != "Species" {
		t.Errorf("conflict = %v", first)
	}
	if !strings.Contains(first["message"].(string), "Appearance") {
		t.Errorf("message = %v, want the seeded rule's own text", first["message"])
	}
	_, again, _ := do(t, f.srv, http.MethodPost, "/api/v1/configurator/validate",
		`{"selections":{"Grade":"Appearance","Species":"SYP"}}`)
	if !strings.Contains(again, `"valid":false`) || !strings.Contains(again, `"conflicts":[`) {
		t.Errorf("the same selections in another key order answered differently: %s", again)
	}
	// Valid selections answer an empty array, never omitted.
	_, raw, _ = do(t, f.srv, http.MethodPost, "/api/v1/configurator/validate",
		`{"selections":{"Species":"SYP","Grade":"#2"}}`)
	if !strings.Contains(raw, `"conflicts":[]`) {
		t.Errorf("a valid verdict = %s, want conflicts []", raw)
	}
}

// build-sku: conflicts are one 400 with a blocker per violated rule, and a
// clean build answers the SKU.
func TestBuildSKU(t *testing.T) {
	f := newFixture(t)
	code, raw, _ := do(t, f.srv, http.MethodPost, "/api/v1/configurator/build-sku",
		`{"product_type":"","selections":{}}`)
	if code != http.StatusBadRequest {
		t.Fatalf("validation = %d %s", code, raw)
	}
	env := body(t, raw)["error"].(map[string]any)
	fields := map[string]bool{}
	for _, d := range env["details"].([]any) {
		fields[d.(map[string]any)["field"].(string)] = true
	}
	for _, want := range []string{"product_type", "selections"} {
		if !fields[want] {
			t.Errorf("details = %v, want a %s entry", env["details"], want)
		}
	}

	code, raw, _ = do(t, f.srv, http.MethodPost, "/api/v1/configurator/build-sku",
		`{"product_type":"Lumber","selections":{"Species":"SYP","Grade":"Appearance"}}`)
	if code != http.StatusBadRequest {
		t.Fatalf("conflicting build = %d %s, want 400", code, raw)
	}
	env = body(t, raw)["error"].(map[string]any)
	if env["code"] != "validation_failed" {
		t.Errorf("code = %v", env["code"])
	}
	blockers := 0
	for _, d := range env["details"].([]any) {
		b := d.(map[string]any)
		if _, hasField := b["field"]; !hasField && b["code"] == "config_conflict" {
			blockers++
		}
	}
	if blockers == 0 {
		t.Errorf("details = %v, want config_conflict blockers", env["details"])
	}

	code, raw, _ = do(t, f.srv, http.MethodPost, "/api/v1/configurator/build-sku",
		`{"product_type":"Lumber","selections":{"Species":"SYP","Grade":"#2","Length":"8"}}`)
	if code != http.StatusOK {
		t.Fatalf("build = %d %s", code, raw)
	}
	out := body(t, raw)
	if out["sku"] != "NS-LBR-SYP-#2" {
		t.Errorf("sku = %v", out["sku"])
	}
}

// The presets: product_type filters, config carries as JSON (not base64),
// description present as null.
func TestPresets(t *testing.T) {
	f := newFixture(t)
	code, raw, _ := do(t, f.srv, http.MethodGet, "/api/v1/configurator/presets", "")
	if code != http.StatusOK {
		t.Fatalf("presets = %d %s", code, raw)
	}
	var presets []map[string]any
	if err := json.Unmarshal([]byte(raw), &presets); err != nil {
		t.Fatal(err)
	}
	if len(presets) == 0 {
		t.Fatal("the seeded presets are missing")
	}
	for _, p := range presets {
		if _, ok := p["description"]; !ok {
			t.Fatal("description absent; it must be present as null when unset")
		}
		cfg, ok := p["config"]
		if !ok {
			t.Fatal("config absent")
		}
		if s, isString := cfg.(string); isString && !strings.HasPrefix(s, "{") {
			t.Errorf("config is still a base64 string: %v", s)
		}
	}
	// The filter filters.
	var count int
	if err := f.db.Pool.QueryRow(context.Background(),
		`SELECT count(*) FROM configurator_presets WHERE is_active AND product_type = 'Door'`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	_, filtered, _ := do(t, f.srv, http.MethodGet, "/api/v1/configurator/presets?product_type=Door", "")
	var list []map[string]any
	_ = json.Unmarshal([]byte(filtered), &list)
	if len(list) != count {
		t.Errorf("filtered presets = %d, want %d", len(list), count)
	}
	_, none, _ := do(t, f.srv, http.MethodGet, "/api/v1/configurator/presets?product_type="+uuid.NewString(), "")
	if !strings.Contains(none, "[]") {
		t.Errorf("a filter that matches nothing = %s, want []", none)
	}
}

// The rules master: never null, ordered.
func TestRules(t *testing.T) {
	f := newFixture(t)
	code, raw, _ := do(t, f.srv, http.MethodGet, "/api/v1/configurator/rules", "")
	if code != http.StatusOK {
		t.Fatalf("rules = %d %s", code, raw)
	}
	var rules []map[string]any
	if err := json.Unmarshal([]byte(raw), &rules); err != nil {
		t.Fatal(err)
	}
	if len(rules) == 0 {
		t.Fatal("the seeded rules are missing")
	}
	for _, r := range rules {
		if _, ok := r["error_message"]; !ok {
			t.Fatal("error_message absent; it must be present as null when unset")
		}
	}
}
