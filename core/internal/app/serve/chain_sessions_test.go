// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package serve

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gablelbm/gable/internal/staffauth"
	"github.com/gablelbm/gable/pkg/middleware"
)

type neverSession struct{}

func (neverSession) Resolve(context.Context, string) (*middleware.UserClaims, error) {
	return nil, middleware.ErrSessionInvalid
}

// A core with Appwrite settings and no JWKS runs the real chain with the
// session middleware mounted: a request with no credential, or with a session
// that does not resolve, is answered 401 before any handler runs (ADR 0012
// section 2, fail closed), and a public path still answers.
func TestChainWithSessionsAndNoJWKSFailsClosed(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	am, err := middleware.NewAuthMiddleware(context.Background(), middleware.AuthConfig{
		PublicPaths: publicAPIPaths,
		Sessions:    neverSession{},
	}, logger)
	if err != nil {
		t.Fatal(err)
	}
	reached := false
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/v1/orders", func(w http.ResponseWriter, r *http.Request) { reached = true })
	mux.HandleFunc("GET /health", func(w http.ResponseWriter, r *http.Request) {})
	h := buildChain(ChainDeps{Mux: mux, Logger: logger, AuthMw: am})

	for name, mut := range map[string]func(*http.Request){
		"no credential": nil,
		"dead cookie":   func(r *http.Request) { r.AddCookie(&http.Cookie{Name: middleware.SessionCookieName, Value: "x"}) },
	} {
		r := httptest.NewRequest(http.MethodGet, "/api/v1/orders", nil)
		if mut != nil {
			mut(r)
		}
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code != http.StatusUnauthorized || reached {
			t.Fatalf("%s: status %d, handler reached %v", name, w.Code, reached)
		}
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/health", nil))
	if w.Code != http.StatusOK {
		t.Fatalf("public path = %d", w.Code)
	}
}

// The sign in routes are public paths of the auth layer, so they answer
// before a session exists.
func TestStaffSessionRoutesArePublicPaths(t *testing.T) {
	want := map[string]bool{"/api/v1/auth/signin": false, "/api/v1/auth/refresh": false, "/api/v1/auth/signout": false,
		"/api/v1/auth/start/": false, "/api/v1/auth/callback": false, "/api/v1/auth/shell/": false}
	for _, p := range publicAPIPaths {
		if _, ok := want[p]; ok {
			want[p] = true
		}
	}
	for p, ok := range want {
		if !ok {
			t.Errorf("%s is not a public path", p)
		}
	}
	if len(publicAPIPaths) > 0 {
		for _, p := range publicAPIPaths {
			if p == "/api/v1/auth/session" {
				t.Error("the session read must go through the auth layer")
			}
		}
	}
}

// The shell's email and password door through the real chain: a public path
// that reaches its own handler with no session, and whose answer carries no
// CORS allowance even for an origin the CORS layer allows, so no page can
// read it (CP-13 shell password, item 340).
func TestShellPasswordDoorIsPublicAndAnswersNoPage(t *testing.T) {
	t.Setenv("CORS_ORIGINS", "https://desk.example")
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	am, err := middleware.NewAuthMiddleware(context.Background(), middleware.AuthConfig{
		PublicPaths: publicAPIPaths,
		Sessions:    neverSession{},
	}, logger)
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	staffauth.NewHandler(staffauth.NewService(nil, nil, nil, staffauth.Settings{}, logger), staffauth.HandlerConfig{}).RegisterRoutes(mux)
	h := buildChain(ChainDeps{Mux: mux, Logger: logger, AuthMw: am})

	// No session and a bad body: the handler's own 400, not the auth layer's 401.
	r := httptest.NewRequest(http.MethodPost, "/api/v1/auth/shell/signin", strings.NewReader("not json"))
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("no Origin, bad body = %d, want the handler's 400", w.Code)
	}

	r = httptest.NewRequest(http.MethodPost, "/api/v1/auth/shell/signin", strings.NewReader(`{"email":"a@x.example","password":"pw"}`))
	r.Header.Set("Origin", "https://desk.example")
	w = httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != http.StatusUnauthorized || w.Header().Get("Access-Control-Allow-Origin") != "" || w.Header().Get("Access-Control-Allow-Credentials") != "" {
		t.Fatalf("an allowed page's POST = %d with %v", w.Code, w.Header())
	}
}
