// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package signinmethods_test

// The sign in method routes on the wire contract: the public config answers
// before any sign in with exactly the allowlist type and the dealer's order,
// the admin routes are held to the dealer's admin roles, a bad replacement
// is refused, the start route refuses a disallowed method plainly and says
// so for an allowed one that is not built yet, and the portal login refuses
// email and password once the dealer drops that method. The tests put the
// allowlist back to the default as they found it.

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/gablelbm/gable/internal/portal"
	"github.com/gablelbm/gable/internal/signinmethods"
	"github.com/gablelbm/gable/internal/testutil"
	"github.com/gablelbm/gable/pkg/audit"
	"github.com/gablelbm/gable/pkg/database"
)

type fixture struct {
	t   *testing.T
	db  *database.DB
	srv *httptest.Server
	svc *signinmethods.Service
}

// guardStub is a role guard stand-in: allow decides pass or refuse, exactly
// the two answers the real RequireRole gives a caller.
type guardStub struct{ allow bool }

func (g guardStub) guard(h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !g.allow {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusForbidden)
			_, _ = w.Write([]byte(`{"error":{"code":"forbidden","message":"refused by the test guard"}}`))
			return
		}
		h.ServeHTTP(w, r)
	})
}

func newFixture(t *testing.T, maxConns int32) *fixture {
	t.Helper()
	var db *database.DB
	if maxConns > 0 {
		db = testutil.RequireDBMaxConns(t, maxConns)
	} else {
		db = testutil.RequireDB(t)
	}
	ctx := context.Background()
	// Start and leave the allowlist at the safe default: email and password
	// alone, the state an install is in before any admin touch. The reset
	// puts the default row back before deleting the rest, so the guard
	// trigger never refuses it.
	reset := func() {
		_, _ = db.Pool.Exec(context.Background(),
			`INSERT INTO dealer_signin_methods (method, sort_order) VALUES ('email_password', 1) ON CONFLICT (method) DO NOTHING`)
		_, _ = db.Pool.Exec(context.Background(), `DELETE FROM dealer_signin_methods WHERE method <> 'email_password'`)
	}
	if _, err := db.Pool.Exec(ctx, `DELETE FROM dealer_signin_methods`); err == nil {
		t.Fatal("deleting every row was allowed; the guard trigger is missing")
	}
	reset()
	t.Cleanup(reset)
	repo, err := signinmethods.NewRepository(db)
	if err != nil {
		t.Fatal(err)
	}
	svc, err := signinmethods.NewService(repo, db, audit.NewLogger(db))
	if err != nil {
		t.Fatal(err)
	}
	f := &fixture{t: t, db: db, svc: svc}
	f.build(guardStub{allow: true})
	return f
}

// build mounts the routes on a fresh server under the given admin guard.
func (f *fixture) build(g guardStub) *httptest.Server {
	f.t.Helper()
	if f.srv != nil {
		f.srv.Close()
	}
	mux := http.NewServeMux()
	signinmethods.NewHandler(f.svc, "production").RegisterRoutes(mux, signinmethods.Guards{Admin: g.guard})
	f.srv = httptest.NewServer(mux)
	f.t.Cleanup(f.srv.Close)
	return f.srv
}

type resp struct {
	status int
	body   map[string]any
}

func (f *fixture) do(method, path string, body string) resp {
	f.t.Helper()
	var rd io.Reader
	if body != "" {
		rd = strings.NewReader(body)
	}
	req, err := http.NewRequest(method, f.srv.URL+path, rd)
	if err != nil {
		f.t.Fatal(err)
	}
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	httpResp, err := http.DefaultClient.Do(req)
	if err != nil {
		f.t.Fatal(err)
	}
	defer httpResp.Body.Close()
	raw, _ := io.ReadAll(httpResp.Body)
	out := resp{status: httpResp.StatusCode}
	if len(raw) > 0 {
		dec := map[string]any{}
		if err := json.Unmarshal(raw, &dec); err != nil {
			f.t.Fatalf("%s %s: the body is not a JSON object: %v (%s)", method, path, err, string(raw))
		}
		out.body = dec
	}
	return out
}

func keys(m map[string]any) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sortStrings(out)
	return out
}

func sortStrings(s []string) {
	for i := 1; i < len(s); i++ {
		for j := i; j > 0 && s[j] < s[j-1]; j-- {
			s[j], s[j-1] = s[j-1], s[j]
		}
	}
}

func eqStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// TestConfigBeforeSignIn: the public config answers with exactly the
// allowlist type, in the dealer's stored order, and carries no field the
// type does not name.
func TestConfigBeforeSignIn(t *testing.T) {
	f := newFixture(t, 0)

	got := f.do("GET", "/api/v1/auth/config", "")
	if got.status != http.StatusOK {
		t.Fatalf("the config answered %d, want 200", got.status)
	}
	if !eqStrings(keys(got.body), []string{"auth_mode", "methods"}) {
		t.Errorf("the config body keys = %v, want exactly auth_mode and methods", keys(got.body))
	}
	if got.body["auth_mode"] != "production" {
		t.Errorf("the auth mode = %v, want production", got.body["auth_mode"])
	}
	methods := got.body["methods"].([]any)
	if len(methods) != 1 {
		t.Fatalf("the default allowlist holds %d methods, want one", len(methods))
	}
	first := methods[0].(map[string]any)
	if !eqStrings(keys(first), []string{"client_id", "issuer_url", "kind", "label", "method", "start_url"}) {
		t.Errorf("the method keys = %v, want exactly the allowlist method type", keys(first))
	}
	if first["method"] != "email_password" || first["kind"] != "form" {
		t.Errorf("the default method = %v (%v), want email_password (form)", first["method"], first["kind"])
	}
	if first["start_url"] != "/api/portal/v1/login" {
		t.Errorf("the form method's start_url = %v, want the portal login", first["start_url"])
	}

	// An admin replacement sets the order the door renders; the config
	// answers in that order, exactly.
	put := f.do("PUT", "/api/v1/auth/methods",
		`{"methods":[{"method":"google"},{"method":"email_password"},{"method":"oidc","oidc_issuer_url":"https://id.example.com","oidc_client_id":"gable","oidc_label":"Dealer SSO"}]}`)
	if put.status != http.StatusOK {
		t.Fatalf("the replacement answered %d: %v", put.status, put.body)
	}
	got = f.do("GET", "/api/v1/auth/config", "")
	methods = got.body["methods"].([]any)
	var order []string
	for _, m := range methods {
		order = append(order, m.(map[string]any)["method"].(string))
	}
	if !eqStrings(order, []string{"google", "email_password", "oidc"}) {
		t.Fatalf("the config order = %v, want the stored order", order)
	}
	oidc := methods[2].(map[string]any)
	if oidc["issuer_url"] != "https://id.example.com" || oidc["client_id"] != "gable" || oidc["label"] != "Dealer SSO" {
		t.Errorf("the oidc view = %v, want the issuer, client id and label carried", oidc)
	}

	// A tenant created before the allowlist existed signs in by its current
	// method: the service answers allowed for it.
	if !f.svc.Allowed(context.Background(), signinmethods.MethodEmailPassword) {
		t.Error("email and password sign in is not allowed after the replacement that keeps it")
	}
}

// TestAdminGuardRefusesUpdate: a caller outside the dealer's admin roles is
// refused the read and the write with Gable's 403 shape.
func TestAdminGuardRefusesUpdate(t *testing.T) {
	f := newFixture(t, 0)
	f.build(guardStub{allow: false})

	if got := f.do("GET", "/api/v1/auth/methods", ""); got.status != http.StatusForbidden {
		t.Errorf("the admin read answered %d, want 403", got.status)
	}
	got := f.do("PUT", "/api/v1/auth/methods", `{"methods":[{"method":"google"}]}`)
	if got.status != http.StatusForbidden {
		t.Errorf("the admin write answered %d, want 403", got.status)
	}
	if errVal, ok := got.body["error"].(map[string]any); !ok || errVal["code"] != "forbidden" {
		t.Errorf("the refusal body = %v, want the Gable error envelope with code forbidden", got.body)
	}
}

// TestPutValidation: an unknown method and an empty list are refused, and
// the oidc fields are held to the oidc method.
func TestPutValidation(t *testing.T) {
	f := newFixture(t, 0)

	if got := f.do("PUT", "/api/v1/auth/methods", `{"methods":[{"method":"sms"}]}`); got.status != http.StatusBadRequest {
		t.Errorf("an unknown method answered %d, want 400", got.status)
	}
	if got := f.do("PUT", "/api/v1/auth/methods", `{"methods":[]}`); got.status != http.StatusBadRequest {
		t.Errorf("an empty list answered %d, want 400", got.status)
	}
	if got := f.do("PUT", "/api/v1/auth/methods", `{"methods":[{"method":"oidc","oidc_issuer_url":"http://insecure.example.com","oidc_client_id":"g"}]}`); got.status != http.StatusBadRequest {
		t.Errorf("a non https issuer answered %d, want 400", got.status)
	}
	if got := f.do("PUT", "/api/v1/auth/methods", `{"methods":[{"method":"google","oidc_client_id":"g"}]}`); got.status != http.StatusBadRequest {
		t.Errorf("oidc fields on another method answered %d, want 400", got.status)
	}
	// The refused writes changed nothing.
	if got := f.do("GET", "/api/v1/auth/config", ""); len(got.body["methods"].([]any)) != 1 {
		t.Error("a refused write changed the allowlist")
	}
}

// TestStartRoute: the start route refuses a disallowed method plainly, and
// says not available yet for an allowed one the server has not built.
func TestStartRoute(t *testing.T) {
	f := newFixture(t, 0)
	put := f.do("PUT", "/api/v1/auth/methods", `{"methods":[{"method":"email_password"},{"method":"google"}]}`)
	if put.status != http.StatusOK {
		t.Fatalf("the replacement answered %d", put.status)
	}

	if got := f.do("POST", "/api/v1/auth/start/passkey", ""); got.status != http.StatusForbidden {
		t.Errorf("a disallowed method answered %d, want 403", got.status)
	}
	got := f.do("POST", "/api/v1/auth/start/google", "")
	if got.status != http.StatusNotImplemented {
		t.Errorf("an allowed but unbuilt method answered %d, want 501", got.status)
	}
	errVal := got.body["error"].(map[string]any)
	if errVal["code"] != "not_implemented" || !strings.Contains(errVal["message"].(string), "not available on this server yet") {
		t.Errorf("the refusal = %v, want the plain not available yet answer", errVal)
	}
	if got := f.do("POST", "/api/v1/auth/start/sms", ""); got.status != http.StatusNotFound {
		t.Errorf("an unknown method answered %d, want 404", got.status)
	}
	if got := f.do("POST", "/api/v1/auth/start/email_password", ""); got.status != http.StatusBadRequest {
		t.Errorf("the form method's start answered %d, want 400", got.status)
	}
}

// TestPortalLoginRefusedWhenMethodDisallowed: the email and password login,
// the one server sign in path, refuses the method once the dealer's
// allowlist drops it.
func TestPortalLoginRefusedWhenMethodDisallowed(t *testing.T) {
	f := newFixture(t, 0)
	// The dealer drops email and password sign in from its allowlist.
	put := f.do("PUT", "/api/v1/auth/methods", `{"methods":[{"method":"google"}]}`)
	if put.status != http.StatusOK {
		t.Fatalf("dropping email_password answered %d", put.status)
	}
	mux := http.NewServeMux()
	ph := portal.NewHandler(nil).WithEmailPasswordGuard(func(ctx context.Context) bool {
		return f.svc.Allowed(ctx, signinmethods.MethodEmailPassword)
	})
	mux.HandleFunc("POST /api/portal/v1/login", ph.HandleLogin)
	srv := httptest.NewServer(mux)
	defer srv.Close()

	body := `{"email":"someone@example.com","password":"whatever"}`
	httpResp, err := http.Post(srv.URL+"/api/portal/v1/login", "application/json", strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	httpResp.Body.Close()
	if httpResp.StatusCode != http.StatusForbidden {
		t.Fatalf("the login with email_password disallowed answered %d, want 403", httpResp.StatusCode)
	}
}

// TestUpdateIsAudited: the replacement writes an audit row holding the
// before and after, the way Gable audits settings changes.
func TestUpdateIsAudited(t *testing.T) {
	f := newFixture(t, 0)
	put := f.do("PUT", "/api/v1/auth/methods", `{"methods":[{"method":"email_password"},{"method":"passkey"}]}`)
	if put.status != http.StatusOK {
		t.Fatalf("the replacement answered %d", put.status)
	}
	var n int
	if err := f.db.Pool.QueryRow(context.Background(),
		`SELECT count(*) FROM audit_log WHERE action = 'signin_methods.update'`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n == 0 {
		t.Error("the replacement wrote no audit row")
	}
}

// TestReplaceConcurrency: the replace transaction runs at pool size 4 with
// three contenders, so a transaction that reaches for a second connection
// deadlocks the test instead of passing on a roomy pool, and the allowlist
// is never empty however the contenders interleave.
func TestReplaceConcurrency(t *testing.T) {
	f := newFixture(t, 4)
	var wg sync.WaitGroup
	lists := [][]signinmethods.Input{
		{{Method: "email_password"}, {Method: "google"}},
		{{Method: "microsoft"}, {Method: "email_password"}},
		{{Method: "magic_link"}, {Method: "email_password"}, {Method: "passkey"}},
	}
	for _, list := range lists {
		wg.Add(1)
		go func(list []signinmethods.Input) {
			defer wg.Done()
			if _, err := f.svc.Update(context.Background(), list); err != nil {
				t.Errorf("a contender's update failed: %v", err)
			}
		}(list)
	}
	wg.Wait()
	ctx := context.Background()
	if !f.svc.Allowed(ctx, signinmethods.MethodEmailPassword) {
		t.Error("the allowlist lost email and password after the contenders")
	}
}
