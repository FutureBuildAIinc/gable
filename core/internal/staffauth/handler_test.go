// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package staffauth

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gablelbm/gable/pkg/middleware"
)

const ownOrigin = "https://gable.example"

// stack builds the real chain pieces: the auth middleware over the service's
// resolver, the sign in routes, and a guarded route that echoes the claims.
func stack(t *testing.T, dir *fakeDir) (http.Handler, *Service, *Handler) {
	t.Helper()
	svc, _, _ := newSvc(t, dir)
	h := NewHandler(svc, HandlerConfig{Origin: ownOrigin})
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)
	mux.HandleFunc("POST /api/v1/orders", func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusOK) })
	mux.HandleFunc("GET /api/v1/orders", func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusOK) })
	am, err := middleware.NewAuthMiddleware(context.Background(), middleware.AuthConfig{
		PublicPaths: []string{"/api/v1/auth/signin", "/api/v1/auth/refresh", "/api/v1/auth/signout"},
		Sessions:    svc,
	}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	return am.Handler(mux), svc, h
}

func post(h http.Handler, path, body string, mut func(*http.Request)) *httptest.ResponseRecorder {
	r := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
	r.Header.Set("Content-Type", "application/json")
	r.RemoteAddr = "203.0.113.9:4000"
	if mut != nil {
		mut(r)
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}

func csrf(r *http.Request) { r.Header.Set(middleware.CSRFHeaderName, "1") }

const goodBody = `{"email":"a@x.example","password":"pw"}`

func sessionCookie(t *testing.T, w *httptest.ResponseRecorder) *http.Cookie {
	t.Helper()
	for _, c := range w.Result().Cookies() {
		if c.Name == middleware.SessionCookieName {
			return c
		}
	}
	t.Fatalf("no session cookie in %v", w.Header())
	return nil
}

func TestSignInSetsTheCookieWithItsAttributes(t *testing.T) {
	h, _, _ := stack(t, okDir())
	w := post(h, "/api/v1/auth/signin", goodBody, csrf)
	if w.Code != http.StatusOK {
		t.Fatalf("status %d: %s", w.Code, w.Body)
	}
	c := sessionCookie(t, w)
	if !c.HttpOnly || !c.Secure || c.SameSite != http.SameSiteStrictMode || c.Path != "/api" || c.Domain != "" || c.Value == "" {
		t.Fatalf("cookie = %+v", c)
	}
	raw := w.Header().Get("Set-Cookie")
	if strings.Contains(strings.ToLower(raw), "domain=") {
		t.Fatalf("Set-Cookie carries a Domain: %s", raw)
	}
}

func TestSignInIgnoresAPresentedSessionID(t *testing.T) {
	h, _, _ := stack(t, okDir())
	first := sessionCookie(t, post(h, "/api/v1/auth/signin", goodBody, csrf))
	second := sessionCookie(t, post(h, "/api/v1/auth/signin", goodBody, func(r *http.Request) {
		csrf(r)
		r.AddCookie(first)
	}))
	if second.Value == first.Value {
		t.Fatal("a presented session id was reused")
	}
}

func TestSessionWorksThenSignOutEndsItOnEveryRoute(t *testing.T) {
	h, _, _ := stack(t, okDir())
	c := sessionCookie(t, post(h, "/api/v1/auth/signin", goodBody, csrf))
	withIt := func(r *http.Request) { r.AddCookie(c); csrf(r) }
	if w := post(h, "/api/v1/orders", "{}", withIt); w.Code != http.StatusOK {
		t.Fatalf("guarded write = %d", w.Code)
	}
	if w := post(h, "/api/v1/auth/signout", "", withIt); w.Code != http.StatusNoContent {
		t.Fatalf("signout = %d", w.Code)
	}
	for _, path := range []string{"/api/v1/orders", "/api/v1/auth/session"} {
		r := httptest.NewRequest(http.MethodGet, path, nil)
		r.AddCookie(c)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code != http.StatusUnauthorized {
			t.Fatalf("GET %s after sign out = %d, want 401", path, w.Code)
		}
	}
}

func TestEveryRefusalGivesTheSameAnswer(t *testing.T) {
	var first string
	for name, mutate := range map[string]func(*fakeDir){
		"non member": func(d *fakeDir) { d.found = false },
		"blocked":    func(d *fakeDir) { d.user.Status = false },
		"mfa":        func(d *fakeDir) { d.user.MFA = true },
		"owner":      func(d *fakeDir) { d.member.Roles = []string{"owner"} },
	} {
		dir := okDir()
		mutate(dir)
		h, _, _ := stack(t, dir)
		w := post(h, "/api/v1/auth/signin", goodBody, csrf)
		if w.Code != http.StatusUnauthorized {
			t.Fatalf("%s: status %d", name, w.Code)
		}
		// strip the request id, which differs per request
		body := w.Body.String()
		if i := strings.Index(body, `"meta"`); i > 0 {
			body = body[:i]
		}
		if first == "" {
			first = body
		} else if body != first {
			t.Fatalf("%s: answer differs: %q vs %q", name, body, first)
		}
		if len(w.Result().Cookies()) != 0 {
			t.Fatalf("%s: a cookie was set on a refusal", name)
		}
	}
}

// A cross site form post (no custom header, a foreign Origin) is refused; the
// same post with the header, or from Gable's own origin, is admitted.
func TestSignInRefusesCrossSiteForms(t *testing.T) {
	h, _, _ := stack(t, okDir())
	for name, mut := range map[string]func(*http.Request){
		"no header no origin": nil,
		"foreign origin":      func(r *http.Request) { r.Header.Set("Origin", "https://evil.example") },
	} {
		if w := post(h, "/api/v1/auth/signin", goodBody, mut); w.Code != http.StatusUnauthorized {
			t.Errorf("%s: status %d, want 401", name, w.Code)
		}
	}
	if w := post(h, "/api/v1/auth/signin", goodBody, func(r *http.Request) { r.Header.Set("Origin", ownOrigin) }); w.Code != http.StatusOK {
		t.Errorf("own origin: status %d", w.Code)
	}
	if w := post(h, "/api/v1/auth/signin", goodBody, csrf); w.Code != http.StatusOK {
		t.Errorf("custom header: status %d", w.Code)
	}
}

func TestSignOutAndRefreshNeedTheHeader(t *testing.T) {
	h, _, _ := stack(t, okDir())
	c := sessionCookie(t, post(h, "/api/v1/auth/signin", goodBody, csrf))
	for _, path := range []string{"/api/v1/auth/signout", "/api/v1/auth/refresh"} {
		if w := post(h, path, "", func(r *http.Request) { r.AddCookie(c) }); w.Code != http.StatusUnauthorized {
			t.Errorf("%s without the header = %d, want 401", path, w.Code)
		}
	}
}

func TestSignInLimitsPerEmailAndPerAddress(t *testing.T) {
	svc, _, _ := newSvc(t, okDir())
	h := NewHandler(svc, HandlerConfig{Origin: ownOrigin, PerEmail: 3, PerAddress: 5, Window: time.Hour})
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)
	attempt := func(email, addr string) *httptest.ResponseRecorder {
		return post(mux, "/api/v1/auth/signin", `{"email":"`+email+`","password":"pw"}`, func(r *http.Request) { csrf(r); r.RemoteAddr = addr })
	}
	for i := 0; i < 3; i++ {
		if w := attempt("one@x.example", "198.51.100.1:1"); w.Code == http.StatusTooManyRequests {
			t.Fatalf("attempt %d limited early", i)
		}
	}
	w := attempt("one@x.example", "198.51.100.1:1")
	if w.Code != http.StatusTooManyRequests || w.Header().Get("Retry-After") == "" {
		t.Fatalf("fourth attempt on one email = %d, Retry-After %q", w.Code, w.Header().Get("Retry-After"))
	}
	// a different email from the same address is still counted against the address
	for i := 0; i < 2; i++ {
		_ = attempt("two"+string(rune('a'+i))+"@x.example", "198.51.100.1:1")
	}
	if w := attempt("three@x.example", "198.51.100.1:1"); w.Code != http.StatusTooManyRequests {
		t.Fatalf("sixth attempt from one address = %d, want 429", w.Code)
	}
	// another address is unaffected
	if w := attempt("four@x.example", "198.51.100.2:1"); w.Code == http.StatusTooManyRequests {
		t.Fatal("limit leaked across addresses")
	}
}

// Three sign ins of one person at once at pool size 4 each get their own
// session id and row.
func TestConcurrentSignInsIssueDistinctSessions(t *testing.T) {
	dir := okDir()
	svc, _, _ := newSvc(t, dir)
	ctx := context.Background()
	var mu sync.Mutex
	seen := map[string]bool{}
	var wg sync.WaitGroup
	for i := 0; i < 3; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			id, _, err := svc.SignIn(ctx, "a@x.example", "pw", "web")
			if err != nil {
				t.Error(err)
				return
			}
			mu.Lock()
			seen[id] = true
			mu.Unlock()
		}()
	}
	wg.Wait()
	if len(seen) != 3 || rowCount(t, svc) != 3 {
		t.Fatalf("ids %d rows %d, want 3 and 3", len(seen), rowCount(t, svc))
	}
}
