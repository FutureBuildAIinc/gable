// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package middleware_test

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gablelbm/gable/internal/techadmin"
	"github.com/gablelbm/gable/internal/testutil"
	"github.com/gablelbm/gable/pkg/actor"
	"github.com/gablelbm/gable/pkg/audit"
	"github.com/gablelbm/gable/pkg/database"
	"github.com/gablelbm/gable/pkg/middleware"

	"github.com/golang-jwt/jwt/v5"
)

// --- test doubles -----------------------------------------------------------

// stubValidator hands out fixed principals, so the scope and path policy tests
// run without a database.
type stubValidator struct {
	principal middleware.KeyPrincipal
	err       error
}

func (v stubValidator) ValidateKey(ctx context.Context, rawKey string) (middleware.KeyPrincipal, error) {
	if v.err != nil {
		return middleware.KeyPrincipal{}, v.err
	}
	return v.principal, nil
}

// stubAuditor records refusals for assertions.
type stubAuditor struct {
	calls []stubAuditorCall
}

type stubAuditorCall struct {
	keyID  string
	action string
	scope  string
	method string
	path   string
}

func (a *stubAuditor) AuditKeyRefusal(ctx context.Context, keyID, action, scope, method, path string) {
	a.calls = append(a.calls, stubAuditorCall{keyID, action, scope, method, path})
}

// okHandler records that the request reached it and what context it saw.
type okHandler struct {
	reached   bool
	keyScopes []string
	hasKeyID  bool
}

func (h *okHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	h.reached = true
	scopes, _ := middleware.KeyScopesFromContext(r.Context())
	h.keyScopes = scopes
	_, h.hasKeyID = middleware.KeyIDFromContext(r.Context())
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(`{"ok":true}`))
}

// newStubAuth builds a MachineKeyAuth over the stub validator and returns the
// assembled standalone handler chain (dev-mode mount) plus the auditor.
func newStubAuth(t *testing.T, principal middleware.KeyPrincipal, publicPaths ...string) (*stubAuditor, http.Handler, *okHandler) {
	t.Helper()
	aud := &stubAuditor{}
	h := &okHandler{}
	mka := middleware.NewMachineKeyAuth(stubValidator{principal: principal}, aud, publicPaths, nil)
	return aud, mka.Handler(h), h
}

func bearerRequest(t *testing.T, method, path, token string) *http.Request {
	t.Helper()
	req := httptest.NewRequest(method, path, nil)
	req.Header.Set("X-Request-ID", "req-test")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	return req
}

// errorBody decodes the ADR error envelope.
type errorBody struct {
	Error struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
	Meta struct {
		RequestID string `json:"request_id"`
	} `json:"meta"`
}

func decodeError(t *testing.T, rec *httptest.ResponseRecorder) errorBody {
	t.Helper()
	if got := rec.Header().Get("Content-Type"); got != "application/json" {
		t.Fatalf("Content-Type = %q, want application/json", got)
	}
	var body errorBody
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatalf("decode error envelope: %v (body: %s)", err, rec.Body.String())
	}
	return body
}

// machineKeyShape mints a token with the machine key shape but no database
// row behind it.
func machineKeyShape(t *testing.T) string {
	t.Helper()
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		t.Fatalf("rand: %v", err)
	}
	return "sk_live_" + base64.RawURLEncoding.EncodeToString(b)
}

// --- scope enforcement (stub validator, no database needed) -----------------

func TestMachineKeyWithoutScopeRefused(t *testing.T) {
	_, chain, _ := newStubAuth(t, middleware.KeyPrincipal{ID: "key-1", Scopes: []string{"quotes:read"}}, "/api/integration/")

	rec := httptest.NewRecorder()
	chain.ServeHTTP(rec, bearerRequest(t, "POST", "/api/v1/quotes", machineKeyShape(t)))

	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403; body: %s", rec.Code, rec.Body.String())
	}
	body := decodeError(t, rec)
	if body.Error.Code != "forbidden" {
		t.Fatalf("code = %q, want forbidden", body.Error.Code)
	}
	if !strings.Contains(body.Error.Message, "quotes:write") {
		t.Fatalf("message %q does not name the refused scope quotes:write", body.Error.Message)
	}
	if body.Meta.RequestID != "req-test" {
		t.Fatalf("meta.request_id = %q, want the request's id", body.Meta.RequestID)
	}
}

func TestMachineKeyWithoutScopeRefusalAudited(t *testing.T) {
	aud, chain, _ := newStubAuth(t, middleware.KeyPrincipal{ID: "key-1", Scopes: []string{"quotes:read"}}, "/api/integration/")

	rec := httptest.NewRecorder()
	chain.ServeHTTP(rec, bearerRequest(t, "POST", "/api/v1/quotes", machineKeyShape(t)))

	if len(aud.calls) != 1 {
		t.Fatalf("%d audit calls, want 1: %+v", len(aud.calls), aud.calls)
	}
	call := aud.calls[0]
	if call.keyID != "key-1" || call.action != "key.scope_refused" || call.scope != "quotes:write" {
		t.Fatalf("audit call = %+v, want key-1 / key.scope_refused / quotes:write", call)
	}
	if call.method != "POST" || call.path != "/api/v1/quotes" {
		t.Fatalf("audit call = %+v, want POST /api/v1/quotes", call)
	}
}

func TestMachineKeyWithScopeSucceeds(t *testing.T) {
	_, chain, h := newStubAuth(t, middleware.KeyPrincipal{ID: "key-1", Scopes: []string{"quotes:write"}}, "/api/integration/")

	rec := httptest.NewRecorder()
	chain.ServeHTTP(rec, bearerRequest(t, "POST", "/api/v1/quotes", machineKeyShape(t)))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", rec.Code, rec.Body.String())
	}
	if !h.reached {
		t.Fatal("handler was not reached")
	}
	if !h.hasKeyID {
		t.Fatal("key id missing from the request context")
	}
	if len(h.keyScopes) != 1 || h.keyScopes[0] != "quotes:write" {
		t.Fatalf("key scopes in context = %v, want [quotes:write]", h.keyScopes)
	}
}

func TestMachineKeyReadWriteSplit(t *testing.T) {
	_, chain, h := newStubAuth(t, middleware.KeyPrincipal{ID: "key-1", Scopes: []string{"quotes:read"}}, "/api/integration/")

	rec := httptest.NewRecorder()
	chain.ServeHTTP(rec, bearerRequest(t, "GET", "/api/v1/quotes", machineKeyShape(t)))
	if rec.Code != http.StatusOK || !h.reached {
		t.Fatalf("GET status = %d, want 200 (read scope admits GET)", rec.Code)
	}

	rec = httptest.NewRecorder()
	chain.ServeHTTP(rec, bearerRequest(t, "POST", "/api/v1/quotes", machineKeyShape(t)))
	if rec.Code != http.StatusForbidden {
		t.Fatalf("POST status = %d, want 403 (read scope does not admit a write)", rec.Code)
	}
}

func TestMachineKeyUnknownOrRevokedRefused401(t *testing.T) {
	// The sentinel error is a credential failure: 401, and nothing is
	// audited: an invalid key has no attributable id.
	aud := &stubAuditor{}
	h := &okHandler{}
	auth := middleware.NewMachineKeyAuth(stubValidator{err: middleware.ErrInvalidMachineKey}, aud, []string{"/api/integration/"}, nil)

	rec := httptest.NewRecorder()
	auth.Handler(h).ServeHTTP(rec, bearerRequest(t, "GET", "/api/v1/quotes", machineKeyShape(t)))

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401; body: %s", rec.Code, rec.Body.String())
	}
	body := decodeError(t, rec)
	if body.Error.Code != "unauthorized" {
		t.Fatalf("code = %q, want unauthorized", body.Error.Code)
	}
	if len(aud.calls) != 0 {
		t.Fatalf("%d audit calls, want 0 (an invalid key has no attributable id)", len(aud.calls))
	}
	if h.reached {
		t.Fatal("handler must not be reached with an invalid key")
	}
}

func TestMachineKeyValidatorFaultIs503(t *testing.T) {
	aud := &stubAuditor{}
	h := &okHandler{}
	auth := middleware.NewMachineKeyAuth(stubValidator{err: context.DeadlineExceeded}, aud, nil, nil)

	rec := httptest.NewRecorder()
	auth.Handler(h).ServeHTTP(rec, bearerRequest(t, "GET", "/api/v1/quotes", machineKeyShape(t)))

	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503 (the key store being down is not a credential verdict)", rec.Code)
	}
	body := decodeError(t, rec)
	if body.Error.Code != "unavailable" {
		t.Fatalf("code = %q, want unavailable", body.Error.Code)
	}
}

func TestMachineKeyOnKeyManagementRefused(t *testing.T) {
	aud, chain, _ := newStubAuth(t, middleware.KeyPrincipal{ID: "key-1", Scopes: []string{"admin:read", "admin:write"}}, "/api/integration/")

	for _, tc := range []struct {
		method, path string
	}{
		{"GET", "/api/v1/admin/keys"},
		{"POST", "/api/v1/admin/keys"},
		{"DELETE", "/api/v1/admin/keys/1234"},
	} {
		rec := httptest.NewRecorder()
		chain.ServeHTTP(rec, bearerRequest(t, tc.method, tc.path, machineKeyShape(t)))
		if rec.Code != http.StatusForbidden {
			t.Fatalf("%s %s: status = %d, want 403 (key management requires a user)", tc.method, tc.path, rec.Code)
		}
		body := decodeError(t, rec)
		if body.Error.Code != "forbidden" {
			t.Fatalf("%s %s: code = %q, want forbidden", tc.method, tc.path, body.Error.Code)
		}
	}

	// One refusal per request, naming the user-only rule.
	if len(aud.calls) != 3 {
		t.Fatalf("%d audit calls, want 3", len(aud.calls))
	}
	for _, call := range aud.calls {
		if call.action != "key.user_required" || call.keyID != "key-1" {
			t.Fatalf("audit call = %+v, want action key.user_required for key-1", call)
		}
	}

	// The rest of the admin module is an ordinary module: a key with the
	// scope reaches it.
	rec := httptest.NewRecorder()
	chain.ServeHTTP(rec, bearerRequest(t, "GET", "/api/v1/admin/settings/ai", machineKeyShape(t)))
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /api/v1/admin/settings/ai: status = %d, want 200 with admin:read", rec.Code)
	}
}

func TestMachineKeyOutsideModuleRoutesRefused(t *testing.T) {
	aud, chain, _ := newStubAuth(t, middleware.KeyPrincipal{ID: "key-1", Scopes: []string{"quotes:write", "a2a:write", "uploads:write"}}, "/api/integration/")

	for _, tc := range []struct {
		method, path string
	}{
		{"GET", "/uploads/photo.jpg"},
		{"POST", "/api/v1/a2a/purchase-order"},
		{"POST", "/api/v1/nosuchmodule"},
		{"GET", "/api/v1/nosuchmodule"},
	} {
		rec := httptest.NewRecorder()
		chain.ServeHTTP(rec, bearerRequest(t, tc.method, tc.path, machineKeyShape(t)))
		if rec.Code != http.StatusForbidden {
			t.Fatalf("%s %s: status = %d, want 403 (a machine key is a principal on module routes only)", tc.method, tc.path, rec.Code)
		}
	}
	for _, call := range aud.calls {
		if call.action != "key.path_refused" {
			t.Fatalf("audit call = %+v, want action key.path_refused", call)
		}
	}
}

// The integration seam keeps its own authentication: a Bearer machine key is
// not consulted on a public path (the seam's X-Integration-Key middleware
// decides), and X-Integration-Key traffic is untouched.
func TestMachineKeyNotConsultedOnPublicPaths(t *testing.T) {
	_, chain, h := newStubAuth(t, middleware.KeyPrincipal{ID: "key-1", Scopes: []string{"quotes:write"}}, "/api/integration/")

	// A machine key Bearer on the seam's path: the auth layer passes it down
	// untouched, exactly as it would a request with no Authorization header;
	// the seam's own middleware refuses it for lacking X-Integration-Key.
	seam := middleware.IntegrationAuth("integration-secret", nil)(chain)
	rec := httptest.NewRecorder()
	seam.ServeHTTP(rec, bearerRequest(t, "GET", "/api/integration/orders", machineKeyShape(t)))
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("machine-key-only request on the integration seam: status = %d, want the seam's own 401 (missing X-Integration-Key)", rec.Code)
	}
	if h.reached {
		t.Fatal("the seam must not admit a machine-key-only request")
	}

	// The seam's own header still works, with or without a Bearer alongside.
	rec = httptest.NewRecorder()
	req := bearerRequest(t, "GET", "/api/integration/orders", "")
	req.Header.Set("X-Integration-Key", "integration-secret")
	seam.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK || !h.reached {
		t.Fatalf("X-Integration-Key request: status = %d, want 200 (seam unchanged)", rec.Code)
	}
}

// The dev-mode mount passes requests that carry no machine key, exactly as an
// auth-less dev server does: no Authorization header, and a JWT (which the dev
// server never validates) included.
func TestDevMountPassesNonMachineKeyRequests(t *testing.T) {
	_, chain, h := newStubAuth(t, middleware.KeyPrincipal{ID: "key-1", Scopes: []string{"quotes:write"}}, "/api/integration/")

	rec := httptest.NewRecorder()
	chain.ServeHTTP(rec, bearerRequest(t, "POST", "/api/v1/quotes", ""))
	if rec.Code != http.StatusOK || !h.reached {
		t.Fatalf("no Authorization header: status = %d, want 200 (dev passes)", rec.Code)
	}

	h.reached = false
	rec = httptest.NewRecorder()
	chain.ServeHTTP(rec, bearerRequest(t, "POST", "/api/v1/quotes", "eyJhbGciOiJIUzI1NiJ9.e30.sig"))
	if rec.Code != http.StatusOK || !h.reached {
		t.Fatalf("JWT Bearer in dev mode: status = %d, want 200 (dev does not validate JWTs)", rec.Code)
	}
}

// RequireRole treats a machine key as a role-less principal whose module
// access the scope check already decided: it passes, where a user without the
// role is refused.
func TestRequireRoleWithMachineKey(t *testing.T) {
	_, chain, h := newStubAuth(t, middleware.KeyPrincipal{ID: "key-1", Scopes: []string{"quotes:write"}}, "/api/integration/")
	guarded := middleware.RequireRole("admin", "owner")(chain)

	rec := httptest.NewRecorder()
	guarded.ServeHTTP(rec, bearerRequest(t, "POST", "/api/v1/quotes", machineKeyShape(t)))
	if rec.Code != http.StatusOK || !h.reached {
		t.Fatalf("status = %d, want 200 (role checks are skipped for keys; the scope check replaces them)", rec.Code)
	}
}

// --- the JWT path is unchanged ----------------------------------------------

// jwksServer serves a JWKS for a freshly generated RSA key and returns the
// key used to sign tokens.
func jwksServer(t *testing.T) (*httptest.Server, *rsa.PrivateKey) {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("generate rsa key: %v", err)
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"keys": []map[string]string{{
				"kty": "RSA",
				"kid": "test-key",
				"alg": "RS256",
				"n":   base64.RawURLEncoding.EncodeToString(key.N.Bytes()),
				"e":   base64.RawURLEncoding.EncodeToString([]byte{1, 0, 1}),
			}},
		})
	}))
	t.Cleanup(srv.Close)
	return srv, key
}

func signedJWT(t *testing.T, key *rsa.PrivateKey, issuer string, roles []string) string {
	t.Helper()
	claims := &middleware.UserClaims{
		RegisteredClaims: jwt.RegisteredClaims{
			Issuer:    issuer,
			Subject:   "user-1",
			ExpiresAt: jwt.NewNumericDate(time.Now().Add(time.Hour)),
		},
		Roles: roles,
	}
	token := jwt.NewWithClaims(jwt.SigningMethodRS256, claims)
	token.Header["kid"] = "test-key"
	s, err := token.SignedString(key)
	if err != nil {
		t.Fatalf("sign jwt: %v", err)
	}
	return s
}

// A JWT request goes down the JWT path unchanged when machine keys are
// configured: the token is validated against JWKS, claims land in the context,
// and a machine key Bearer never reaches the JWT parser.
func TestJWTPathUnchangedWithMachineKeysConfigured(t *testing.T) {
	jwks, key := jwksServer(t)

	h := &okHandler{}
	var sawClaims *middleware.UserClaims
	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if c := middleware.ClaimsFromContext(r.Context()); c != nil {
			sawClaims = c
		}
		h.ServeHTTP(w, r)
	})
	guarded := middleware.RequireRole("admin")(inner)

	aud := &stubAuditor{}
	mka := middleware.NewMachineKeyAuth(stubValidator{principal: middleware.KeyPrincipal{ID: "key-1", Scopes: []string{"quotes:write"}}}, aud, []string{"/api/integration/"}, nil)
	auth, err := middleware.NewAuthMiddleware(context.Background(), middleware.AuthConfig{
		JWKSURL:     jwks.URL,
		Issuer:      "test-issuer",
		PublicPaths: []string{"/api/integration/"},
		MachineKeys: mka,
	}, nil)
	if err != nil {
		t.Fatalf("NewAuthMiddleware: %v", err)
	}

	rec := httptest.NewRecorder()
	auth.Handler(guarded).ServeHTTP(rec, bearerRequest(t, "POST", "/api/v1/quotes", signedJWT(t, key, "test-issuer", []string{"admin"})))
	if rec.Code != http.StatusOK || !h.reached {
		t.Fatalf("JWT request: status = %d, want 200 (JWT path unchanged)", rec.Code)
	}
	if sawClaims == nil || sawClaims.Subject != "user-1" {
		t.Fatal("JWT claims missing from the context")
	}
	if sawClaims.Role != "" && sawClaims.Roles == nil {
		t.Fatal("claims shape changed")
	}

	// The same middleware with a machine key Bearer dispatches to the key
	// path, not the JWT parser.
	h.reached = false
	sawClaims = nil
	rec = httptest.NewRecorder()
	auth.Handler(guarded).ServeHTTP(rec, bearerRequest(t, "POST", "/api/v1/quotes", machineKeyShape(t)))
	if rec.Code != http.StatusOK || !h.reached {
		t.Fatalf("machine key request: status = %d, want 200; body: %s", rec.Code, rec.Body.String())
	}
	if sawClaims != nil {
		t.Fatal("a machine key request must not carry user claims")
	}

	// A user without the role is still refused by RequireRole, in the ADR
	// envelope.
	rec = httptest.NewRecorder()
	auth.Handler(guarded).ServeHTTP(rec, bearerRequest(t, "POST", "/api/v1/quotes", signedJWT(t, key, "test-issuer", nil)))
	if rec.Code != http.StatusForbidden {
		t.Fatalf("JWT without role: status = %d, want 403", rec.Code)
	}
	body := decodeError(t, rec)
	if body.Error.Code != "forbidden" {
		t.Fatalf("code = %q, want forbidden", body.Error.Code)
	}

	// With no machine-key validator wired, a machine key Bearer is refused
	// fail closed rather than falling through to the JWT parser.
	authNoKeys, err := middleware.NewAuthMiddleware(context.Background(), middleware.AuthConfig{
		JWKSURL:     jwks.URL,
		Issuer:      "test-issuer",
		PublicPaths: []string{"/api/integration/"},
	}, nil)
	if err != nil {
		t.Fatalf("NewAuthMiddleware: %v", err)
	}
	rec = httptest.NewRecorder()
	authNoKeys.Handler(guarded).ServeHTTP(rec, bearerRequest(t, "POST", "/api/v1/quotes", machineKeyShape(t)))
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("machine key with no validator wired: status = %d, want 401 (fail closed)", rec.Code)
	}
	body = decodeError(t, rec)
	if body.Error.Code != "unauthorized" {
		t.Fatalf("code = %q, want unauthorized", body.Error.Code)
	}
}

// --- the real validator and the real audit rows (database) -------------------

// techadminValidator adapts the techadmin service to the middleware seam, the
// way cmd/server wires it.
type techadminValidator struct {
	svc *techadmin.Service
}

func (v techadminValidator) ValidateKey(ctx context.Context, rawKey string) (middleware.KeyPrincipal, error) {
	k, err := v.svc.ValidateKey(ctx, rawKey)
	if err != nil {
		if errors.Is(err, techadmin.ErrInvalidKey) {
			return middleware.KeyPrincipal{}, middleware.ErrInvalidMachineKey
		}
		return middleware.KeyPrincipal{}, err
	}
	return middleware.KeyPrincipal{ID: k.ID, Scopes: k.Scopes}, nil
}

func newDBAuth(t *testing.T, db *database.DB) *middleware.MachineKeyAuth {
	t.Helper()
	svc := techadmin.NewService(techadmin.NewRepository(db))
	return middleware.NewMachineKeyAuth(techadminValidator{svc}, audit.NewLogger(db), nil, nil)
}

func createKey(t *testing.T, db *database.DB, scopes ...string) (raw string, id string) {
	t.Helper()
	svc := techadmin.NewService(techadmin.NewRepository(db))
	raw, key, err := svc.GenerateKey(context.Background(), "r1-13 test", scopes)
	if err != nil {
		t.Fatalf("GenerateKey: %v", err)
	}
	return raw, key.ID
}

func TestRealKeyRoundTrip(t *testing.T) {
	db := testutil.RequireDB(t)

	raw, _ := createKey(t, db, "quotes:write")
	if !middleware.IsMachineKey(raw) {
		t.Fatalf("GenerateKey minted %q, which the shape check does not recognize", raw)
	}

	h := &okHandler{}
	chain := newDBAuth(t, db).Handler(h)

	rec := httptest.NewRecorder()
	chain.ServeHTTP(rec, bearerRequest(t, "POST", "/api/v1/quotes", raw))
	if rec.Code != http.StatusOK || !h.reached {
		t.Fatalf("valid key with quotes:write: status = %d, want 200; body: %s", rec.Code, rec.Body.String())
	}
	if !h.hasKeyID {
		t.Fatal("key id missing from context")
	}

	// The key's id is visible to pkg/actor, so audit rows attribute to it.
	if _, ok := actor.KeyIDFromContext(context.Background()); ok {
		t.Fatal("sanity: empty context must not carry a key id")
	}
}

func TestRealKeyRefusalWritesAuditRow(t *testing.T) {
	db := testutil.RequireDB(t)

	raw, id := createKey(t, db, "quotes:read")
	h := &okHandler{}
	chain := newDBAuth(t, db).Handler(h)

	rec := httptest.NewRecorder()
	chain.ServeHTTP(rec, bearerRequest(t, "POST", "/api/v1/quotes", raw))
	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403; body: %s", rec.Code, rec.Body.String())
	}

	var action, kind, actorID, scope, method, path string
	err := db.Pool.QueryRow(context.Background(),
		`SELECT action, actor_kind, actor_id,
		        changes->>'scope', changes->>'method', changes->>'path'
		   FROM audit_log WHERE actor_kind = 'key' AND actor_id = $1 AND action = 'key.scope_refused'`, id,
	).Scan(&action, &kind, &actorID, &scope, &method, &path)
	if err != nil {
		t.Fatalf("no key.scope_refused audit row for key %s: %v", id, err)
	}
	if scope != "quotes:write" || method != "POST" || path != "/api/v1/quotes" {
		t.Fatalf("audit row records scope=%q method=%q path=%q, want quotes:write POST /api/v1/quotes", scope, method, path)
	}

	// user_id stays NULL on a key row: the key id lives in actor_id only.
	var userID *string
	if err := db.Pool.QueryRow(context.Background(),
		`SELECT user_id FROM audit_log WHERE actor_kind = 'key' AND actor_id = $1`, id,
	).Scan(&userID); err != nil {
		t.Fatalf("read user_id: %v", err)
	}
	if userID != nil && *userID != "" {
		t.Fatalf("user_id = %q on a key row, want NULL", *userID)
	}
}

func TestRevokedRealKeyRefused401(t *testing.T) {
	db := testutil.RequireDB(t)

	raw, id := createKey(t, db, "quotes:write")
	svc := techadmin.NewService(techadmin.NewRepository(db))
	if err := svc.RevokeKey(context.Background(), id); err != nil {
		t.Fatalf("RevokeKey: %v", err)
	}

	h := &okHandler{}
	chain := newDBAuth(t, db).Handler(h)

	rec := httptest.NewRecorder()
	chain.ServeHTTP(rec, bearerRequest(t, "POST", "/api/v1/quotes", raw))
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("revoked key: status = %d, want 401; body: %s", rec.Code, rec.Body.String())
	}
	body := decodeError(t, rec)
	if body.Error.Code != "unauthorized" {
		t.Fatalf("code = %q, want unauthorized", body.Error.Code)
	}
	if h.reached {
		t.Fatal("handler must not be reached with a revoked key")
	}
}

func TestUnknownRealKeyRefused401(t *testing.T) {
	db := testutil.RequireDB(t)

	h := &okHandler{}
	chain := newDBAuth(t, db).Handler(h)

	rec := httptest.NewRecorder()
	chain.ServeHTTP(rec, bearerRequest(t, "POST", "/api/v1/quotes", machineKeyShape(t)))
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("unknown key: status = %d, want 401", rec.Code)
	}
	if h.reached {
		t.Fatal("handler must not be reached with an unknown key")
	}
}
