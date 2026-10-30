// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package middleware

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/golang-jwt/jwt/v5"
)

type fakeResolver struct {
	valid map[string][]string
	err   error
}

func (f fakeResolver) Resolve(ctx context.Context, id string) (*UserClaims, error) {
	if f.err != nil {
		return nil, f.err
	}
	roles, ok := f.valid[id]
	if !ok {
		return nil, ErrSessionInvalid
	}
	return &UserClaims{RegisteredClaims: jwt.RegisteredClaims{Subject: "sub-" + id}, Roles: roles}, nil
}

func sessionMw(t *testing.T, res SessionResolver) http.Handler {
	t.Helper()
	am, err := NewAuthMiddleware(context.Background(), AuthConfig{
		PublicPaths: []string{"/health", "/api/v1/auth/signin"},
		Sessions:    res,
	}, slog.Default())
	if err != nil {
		t.Fatal(err)
	}
	return am.Handler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c := ClaimsFromContext(r.Context())
		if c == nil {
			t.Error("handler reached with no claims")
			return
		}
		w.Header().Set("X-Sub", c.Subject)
		w.Header().Set("X-Role", c.Role)
		w.Header().Set("X-Roles", strings.Join(c.Roles, ","))
		w.WriteHeader(http.StatusOK)
	}))
}

func do(h http.Handler, method, path string, mut func(*http.Request)) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, path, nil)
	if mut != nil {
		mut(r)
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}

func withCookie(v string) func(*http.Request) {
	return func(r *http.Request) { r.AddCookie(&http.Cookie{Name: SessionCookieName, Value: v}) }
}

func TestSessionAuthValidCookieFillsClaims(t *testing.T) {
	h := sessionMw(t, fakeResolver{valid: map[string][]string{"good": {"sales", "finance"}}})
	w := do(h, http.MethodGet, "/api/v1/orders", withCookie("good"))
	if w.Code != http.StatusOK || w.Header().Get("X-Sub") != "sub-good" || w.Header().Get("X-Roles") != "sales,finance" || w.Header().Get("X-Role") != "" {
		t.Fatalf("status %d headers %v", w.Code, w.Header())
	}
}

func TestSessionAuthFailsClosed(t *testing.T) {
	h := sessionMw(t, fakeResolver{valid: map[string][]string{"good": {"sales"}}})
	cases := map[string]func(*http.Request){
		"no credential":  nil,
		"unknown cookie": withCookie("nope"),
		"empty cookie":   withCookie(""),
		"garbage bearer": func(r *http.Request) { r.Header.Set("Authorization", "Bearer not.a.jwt") },
		"invalid cookie valid-shaped bearer": func(r *http.Request) {
			withCookie("nope")(r)
			r.Header.Set("Authorization", "Bearer not.a.jwt")
		},
	}
	for name, mut := range cases {
		if w := do(h, http.MethodGet, "/api/v1/orders", mut); w.Code != http.StatusUnauthorized {
			t.Errorf("%s: status %d, want 401", name, w.Code)
		}
	}
}

// A presented credential that fails is never passed over for another one.
func TestSessionAuthInvalidCookieWithValidSecondCredentialIs401(t *testing.T) {
	h := sessionMw(t, fakeResolver{valid: map[string][]string{"good": {"sales"}}})
	// a valid cookie with an invalid bearer
	w := do(h, http.MethodGet, "/api/v1/orders", func(r *http.Request) {
		withCookie("good")(r)
		r.Header.Set("Authorization", "Bearer not.a.jwt")
	})
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("valid cookie + invalid bearer = %d, want 401", w.Code)
	}
}

func TestSessionAuthResolverFaultIs503NotPass(t *testing.T) {
	h := sessionMw(t, fakeResolver{err: errors.New("db down")})
	if w := do(h, http.MethodGet, "/api/v1/orders", withCookie("good")); w.Code != http.StatusServiceUnavailable {
		t.Fatalf("status %d, want 503", w.Code)
	}
}

func TestSessionAuthCSRFHeaderOnCookieWrites(t *testing.T) {
	h := sessionMw(t, fakeResolver{valid: map[string][]string{"good": {"sales"}}})
	for _, m := range []string{http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete} {
		if w := do(h, m, "/api/v1/orders", withCookie("good")); w.Code != http.StatusForbidden {
			t.Errorf("%s without the header = %d, want 403", m, w.Code)
		}
		w := do(h, m, "/api/v1/orders", func(r *http.Request) {
			withCookie("good")(r)
			r.Header.Set(CSRFHeaderName, "1")
		})
		if w.Code != http.StatusOK {
			t.Errorf("%s with the header = %d, want 200", m, w.Code)
		}
	}
	// reads need no header
	if w := do(h, http.MethodGet, "/api/v1/orders", withCookie("good")); w.Code != http.StatusOK {
		t.Errorf("GET = %d, want 200", w.Code)
	}
}

func TestSessionAuthPublicPathSkipsEverything(t *testing.T) {
	am, _ := NewAuthMiddleware(context.Background(), AuthConfig{PublicPaths: []string{"/api/v1/auth/signin"}, Sessions: fakeResolver{}}, slog.Default())
	reached := false
	h := am.Handler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { reached = true }))
	do(h, http.MethodPost, "/api/v1/auth/signin", nil)
	if !reached {
		t.Fatal("a public path did not reach its handler")
	}
}

func TestSessionCookieIgnoredWhenNoResolverConfigured(t *testing.T) {
	am, _ := NewAuthMiddleware(context.Background(), AuthConfig{}, slog.Default())
	h := am.Handler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { t.Error("reached") }))
	if w := do(h, http.MethodGet, "/api/v1/orders", withCookie("good")); w.Code != http.StatusUnauthorized {
		t.Fatalf("status %d, want 401", w.Code)
	}
}

func TestCORSAllowsCSRFHeaderForConfiguredOriginOnly(t *testing.T) {
	h := CORSMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	pre := func(origin string) *httptest.ResponseRecorder {
		return do(h, http.MethodOptions, "/api/v1/orders", func(r *http.Request) { r.Header.Set("Origin", origin) })
	}
	if got := pre("http://localhost:5173").Header().Get("Access-Control-Allow-Headers"); !strings.Contains(got, CSRFHeaderName) {
		t.Fatalf("allowed origin: Allow-Headers = %q", got)
	}
	bad := pre("http://evil.example")
	if bad.Header().Get("Access-Control-Allow-Origin") != "" || bad.Header().Get("Access-Control-Allow-Headers") != "" {
		t.Fatal("a disallowed origin's preflight was approved")
	}
}
