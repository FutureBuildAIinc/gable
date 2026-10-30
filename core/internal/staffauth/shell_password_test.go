// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package staffauth

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gablelbm/gable/internal/appwrite"
)

const shellSignInPath = "/api/v1/auth/shell/signin"

// pwRig is the shell's email and password door with its audit recorder and
// the dealer's allowlist switch.
type pwRig struct {
	dir     *fakeDir
	svc     *Service
	rec     *auditRec
	h       *Handler
	mux     *http.ServeMux
	allowed bool
}

func newPwRig(t *testing.T, dir *fakeDir) *pwRig {
	t.Helper()
	svc, rec, _ := newSvc(t, dir)
	g := &pwRig{dir: dir, svc: svc, rec: rec, allowed: true}
	g.h = NewHandler(svc, HandlerConfig{Origin: ownOrigin}).
		WithEmailPasswordGuard(func(context.Context) bool { return g.allowed })
	g.mux = http.NewServeMux()
	g.h.RegisterRoutes(g.mux)
	return g
}

func pwBody(email, password string) string {
	b, _ := json.Marshal(map[string]string{"email": email, "password": password})
	return string(b)
}

func (g *pwRig) shell(body string, mut func(*http.Request)) *httptest.ResponseRecorder {
	return post(g.mux, shellSignInPath, body, mut)
}

func (g *pwRig) web(body string) *httptest.ResponseRecorder {
	return post(g.mux, "/api/v1/auth/signin", body, csrf)
}

// stripMeta drops the request id, which differs per request.
func stripMeta(body string) string {
	if i := strings.Index(body, `"meta"`); i > 0 {
		return body[:i]
	}
	return body
}

func TestShellPasswordSignInAnswersAShellPairAndNoCookie(t *testing.T) {
	g := newPwRig(t, okDir())
	w := g.shell(goodBody, nil)
	tok := tokensOf(t, w)
	if !strings.HasPrefix(tok.AccessToken, "gst_") || tok.Sub != Subject("p", "u1").String() || len(tok.Roles) != 2 {
		t.Fatalf("pair = %+v", tok)
	}
	if got := w.Header().Values("Set-Cookie"); len(got) != 0 {
		t.Fatalf("the shell door set a cookie: %v", got)
	}
	if w.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("Cache-Control = %q", w.Header().Get("Cache-Control"))
	}
}

func TestShellPasswordPairResolvesRotatesAndSignsOut(t *testing.T) {
	g := newPwRig(t, okDir())
	ctx := context.Background()
	tok := tokensOf(t, g.shell(goodBody, nil))
	c, err := g.svc.ResolveShell(ctx, tok.AccessToken)
	if err != nil || c.Subject != tok.Sub {
		t.Fatalf("resolve = %+v, %v", c, err)
	}
	if _, err := g.svc.Resolve(ctx, tok.AccessToken); err == nil {
		t.Fatal("a shell token resolved as a cookie session")
	}
	next, err := g.svc.ShellRefresh(ctx, tok.RefreshToken)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := g.svc.ShellRefresh(ctx, tok.RefreshToken); !errors.Is(err, ErrRefused) {
		t.Fatalf("reused refresh = %v", err)
	}
	if _, err := g.svc.ResolveShell(ctx, next.Access); err == nil {
		t.Fatal("a reuse left the family live")
	}
	again := tokensOf(t, g.shell(goodBody, nil))
	g.svc.ShellSignOut(ctx, again.AccessToken)
	if _, err := g.svc.ResolveShell(ctx, again.AccessToken); err == nil {
		t.Fatal("sign out left the session live")
	}
}

// Condition 1: no browser page may sign in here or read the pair. Every
// browser POST carries an Origin, Gable's own included; the shell's own post
// carries none.
func TestShellPasswordSignInRefusesAnyOrigin(t *testing.T) {
	for _, origin := range []string{"https://evil.example", ownOrigin, "null"} {
		g := newPwRig(t, okDir())
		w := httptest.NewRecorder()
		// The global CORS layer has already answered an allowed origin.
		w.Header().Set("Access-Control-Allow-Origin", origin)
		w.Header().Set("Access-Control-Allow-Credentials", "true")
		r := httptest.NewRequest(http.MethodPost, shellSignInPath, strings.NewReader(goodBody))
		r.RemoteAddr = "203.0.113.9:4000"
		r.Header.Set("Origin", origin)
		g.mux.ServeHTTP(w, r)
		if w.Code != http.StatusUnauthorized || strings.Contains(w.Body.String(), "access_token") {
			t.Fatalf("%s: %d %s", origin, w.Code, w.Body)
		}
		if w.Header().Get("Access-Control-Allow-Origin") != "" || w.Header().Get("Access-Control-Allow-Credentials") != "" {
			t.Fatalf("%s: the answer still allows a page to read it: %v", origin, w.Header())
		}
		if g.dir.sessionsMade != 0 || len(g.rec.entries) != 0 {
			t.Fatalf("%s: credentials were tried", origin)
		}
	}
}

// Condition 4: the allowlist guard runs before any credential is read.
func TestShellPasswordSignInNeedsTheDealersAllowlist(t *testing.T) {
	g := newPwRig(t, okDir())
	g.allowed = false
	w := g.shell(goodBody, nil)
	if w.Code != http.StatusForbidden || g.dir.sessionsMade != 0 {
		t.Fatalf("status %d, appwrite sessions %d", w.Code, g.dir.sessionsMade)
	}
	if web := g.web(goodBody); stripMeta(web.Body.String()) != stripMeta(w.Body.String()) {
		t.Fatalf("the doors answer differently: web %s, shell %s", web.Body, w.Body)
	}
}

// Condition 3: every refusal is one fixed answer, the same at both doors, so
// neither tells a wrong password from an unknown address.
func TestShellPasswordRefusalsAnswerOneFixedMessage(t *testing.T) {
	webAnswer := ""
	{
		dir := okDir()
		dir.signInErr = &appwrite.Error{StatusCode: http.StatusUnauthorized, Type: "user_invalid_credentials"}
		g := newPwRig(t, dir)
		webAnswer = stripMeta(g.web(goodBody).Body.String())
	}
	for name, mutate := range map[string]func(*fakeDir){
		"bad credentials": func(d *fakeDir) {
			d.signInErr = &appwrite.Error{StatusCode: http.StatusUnauthorized, Type: "user_invalid_credentials"}
		},
		"unknown address": func(d *fakeDir) {
			d.signInErr = &appwrite.Error{StatusCode: http.StatusUnauthorized, Type: "user_invalid_credentials"}
		},
		"blocked at appwrite": func(d *fakeDir) {
			d.signInErr = &appwrite.Error{StatusCode: http.StatusUnauthorized, Type: "user_blocked"}
		},
		"non member":    func(d *fakeDir) { d.found = false },
		"unconfirmed":   func(d *fakeDir) { d.member.Confirmed = false },
		"blocked":       func(d *fakeDir) { d.user.Status = false },
		"unverified":    func(d *fakeDir) { d.user.EmailVerification = false },
		"mfa":           func(d *fakeDir) { d.user.MFA = true },
		"owner":         func(d *fakeDir) { d.member.Roles = []string{"owner"} },
		"no gable role": func(d *fakeDir) { d.member.Roles = []string{"someone-else"} },
	} {
		dir := okDir()
		mutate(dir)
		g := newPwRig(t, dir)
		email := "a@x.example"
		if name == "unknown address" {
			email = "nobody@x.example"
		}
		w := g.shell(pwBody(email, "pw"), nil)
		if w.Code != http.StatusUnauthorized {
			t.Fatalf("%s: status %d", name, w.Code)
		}
		if got := stripMeta(w.Body.String()); got != webAnswer {
			t.Fatalf("%s: shell answered %s, web answers %s", name, got, webAnswer)
		}
		if rowCount(t, g.svc) != 0 {
			t.Fatalf("%s: a refused sign in left a session", name)
		}
	}
}

// Condition 2: one budget per email and per address across both doors.
func TestShellAndWebSignInShareOneBudgetPerEmail(t *testing.T) {
	for _, order := range []string{"web then shell", "shell then web"} {
		g := newPwRig(t, okDir())
		first, then := g.web, func(b string) *httptest.ResponseRecorder { return g.shell(b, nil) }
		if order == "shell then web" {
			first, then = then, first
		}
		for i := 0; i < 8; i++ {
			// Varied passwords: the budget is per email whatever is typed.
			first(pwBody("a@x.example", fmt.Sprintf("pw%d", i)))
		}
		w := then(pwBody("A@X.example ", "pw"))
		if w.Code != http.StatusTooManyRequests || w.Header().Get("Retry-After") == "" {
			t.Fatalf("%s: ninth attempt = %d, Retry-After %q", order, w.Code, w.Header().Get("Retry-After"))
		}
	}
}

func TestShellAndWebSignInShareOneBudgetPerAddress(t *testing.T) {
	g := newPwRig(t, okDir())
	for i := 0; i < 20; i++ {
		if i%2 == 0 {
			g.web(pwBody(fmt.Sprintf("p%d@x.example", i), "pw"))
		} else {
			g.shell(pwBody(fmt.Sprintf("p%d@x.example", i), "pw"), nil)
		}
	}
	if w := g.shell(pwBody("fresh@x.example", "pw"), nil); w.Code != http.StatusTooManyRequests {
		t.Fatalf("the shell's twenty first attempt from one address = %d", w.Code)
	}
}

// Condition 4: the same audit rows, the password in none of them.
func TestShellPasswordAuditRowsNeverHoldThePassword(t *testing.T) {
	const secret = "correct-horse-battery-staple"
	dir := okDir()
	g := newPwRig(t, dir)
	tokensOf(t, g.shell(pwBody("a@x.example", secret), nil))
	dir.signInErr = &appwrite.Error{StatusCode: http.StatusUnauthorized}
	g.shell(pwBody("a@x.example", secret+"-wrong"), nil)
	dir.signInErr = nil
	dir.found = false
	g.shell(pwBody("a@x.example", secret), nil)

	g.rec.mu.Lock()
	defer g.rec.mu.Unlock()
	if len(g.rec.entries) != 3 {
		t.Fatalf("audit rows = %d, want 3", len(g.rec.entries))
	}
	for _, e := range g.rec.entries {
		raw, _ := json.Marshal(e)
		if strings.Contains(string(raw), secret) || strings.Contains(string(raw), "correct-horse") {
			t.Fatalf("an audit row holds the password: %s", raw)
		}
		if e.Changes["client_kind"] != "shell" || e.Changes["method"] != "email_password" {
			t.Fatalf("row %s changes = %v", e.Action, e.Changes)
		}
	}
	ok, bad, nonMember := g.rec.entries[0], g.rec.entries[1], g.rec.entries[2]
	if ok.Action != "staff.sign_in" || ok.UserID != Subject("p", "u1").String() {
		t.Fatalf("success row = %+v", ok)
	}
	if bad.Action != "staff.sign_in_refused" || bad.Changes["reason"] != "bad_credentials" || bad.Changes["email"] != "a@x.example" {
		t.Fatalf("refusal row = %+v", bad)
	}
	if nonMember.Changes["reason"] != "not_a_member" {
		t.Fatalf("non member row = %+v", nonMember)
	}
}

func TestShellPasswordSignInEndsTheAppwriteSession(t *testing.T) {
	dir := okDir()
	g := newPwRig(t, dir)
	tokensOf(t, g.shell(goodBody, nil))
	dir.found = false
	g.shell(goodBody, nil)
	if len(dir.ended) != 2 {
		t.Fatalf("appwrite sessions ended = %d, want 2", len(dir.ended))
	}
}

func TestShellPasswordSignInAnswersUnavailableWhenAppwriteIsDown(t *testing.T) {
	dir := okDir()
	dir.signInErr = errors.New("dial tcp: connection refused")
	g := newPwRig(t, dir)
	w := g.shell(goodBody, nil)
	if w.Code != http.StatusServiceUnavailable || rowCount(t, g.svc) != 0 || len(g.rec.entries) != 0 {
		t.Fatalf("status %d, rows %d, audit %d", w.Code, rowCount(t, g.svc), len(g.rec.entries))
	}
}

func TestShellPasswordSignInRefusesABadBody(t *testing.T) {
	g := newPwRig(t, okDir())
	if w := g.shell("not json", nil); w.Code != http.StatusBadRequest {
		t.Fatalf("status %d", w.Code)
	}
	if w := g.shell(pwBody("", ""), nil); w.Code != http.StatusUnauthorized || g.dir.sessionsMade != 0 {
		t.Fatalf("empty credentials = %d", w.Code)
	}
}
