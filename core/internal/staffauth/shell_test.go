// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package staffauth

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gablelbm/gable/pkg/middleware"
)

const (
	loopbackTarget = "http://127.0.0.1:41873/auth/callback"
	testVerifier   = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQR-._~0123"
)

func TestReturnTargetsAreTheRegisteredSchemeOrLoopbackOnly(t *testing.T) {
	for _, ok := range []string{
		"gable://auth/callback",
		"http://127.0.0.1:41873/auth/callback",
		"http://[::1]:8080/auth/callback",
		"http://127.0.0.1:1/auth/callback",
		"http://127.0.0.1:65535/auth/callback",
	} {
		if !ValidateReturnTarget(ok) {
			t.Errorf("%q refused, want accepted", ok)
		}
	}
	for _, bad := range []string{
		"", "https://127.0.0.1:41873/auth/callback", "http://localhost:41873/auth/callback",
		"http://127.0.0.1/auth/callback", "http://127.0.0.1:0/auth/callback", "http://127.0.0.1:65536/auth/callback",
		"http://127.0.0.1:041873/auth/callback", "http://127.0.0.1:41873/auth/callback/", "http://127.0.0.1:41873/other",
		"http://127.0.0.1:41873/auth/callback?x=1", "http://127.0.0.1:41873/auth/callback#f",
		"http://user@127.0.0.1:41873/auth/callback", "http://0.0.0.0:41873/auth/callback",
		"http://127.0.0.1.evil.example:41873/auth/callback", "http://evil.example/auth/callback",
		"gable://auth/callback?x=1", "gable://auth/other", "gable://other/callback", "gable://auth/callback/",
		"gable:auth/callback", "https://evil.example/", "javascript:alert(1)", "//127.0.0.1:41873/auth/callback",
	} {
		if ValidateReturnTarget(bad) {
			t.Errorf("%q accepted, want refused", bad)
		}
	}
}

func shellStartPath(method, verifier, target string) string {
	q := url.Values{}
	q.Set("client", "shell")
	q.Set("code_challenge", challengeOf(verifier))
	q.Set("code_challenge_method", "S256")
	q.Set("return_to", target)
	return "/api/v1/auth/start/" + method + "?" + q.Encode()
}

func (g *redirectRig) shellStart(t *testing.T, method, verifier, target string) string {
	t.Helper()
	w := g.get(shellStartPath(method, verifier, target), nil)
	if w.Code != http.StatusFound {
		t.Fatalf("shell start = %d: %s", w.Code, w.Body)
	}
	return cookieNamed(w, StateCookieName).Value
}

// shellCode runs the callback for a started shell sign in and returns the one
// time code from the shell's return address.
func (g *redirectRig) shellCode(t *testing.T, state, secret string) string {
	t.Helper()
	w := g.callback(state, state, "u1", secret)
	loc, err := url.Parse(w.Header().Get("Location"))
	if w.Code != http.StatusSeeOther || err != nil || !strings.HasPrefix(w.Header().Get("Location"), loopbackTarget+"?") || loc.Query().Get("code") == "" {
		t.Fatalf("shell callback = %d to %q", w.Code, w.Header().Get("Location"))
	}
	if cookieNamed(w, middleware.SessionCookieName) != nil {
		t.Fatal("a shell's callback set a session cookie")
	}
	return loc.Query().Get("code")
}

func (g *redirectRig) postJSON(path, body string) *httptest.ResponseRecorder {
	return post(g.mux, path, body, nil)
}

func (g *redirectRig) redeem(code, verifier string) *httptest.ResponseRecorder {
	b, _ := json.Marshal(map[string]string{"code": code, "code_verifier": verifier})
	return g.postJSON("/api/v1/auth/shell/redeem", string(b))
}

func (g *redirectRig) refreshTokens(refresh string) *httptest.ResponseRecorder {
	b, _ := json.Marshal(map[string]string{"refresh_token": refresh})
	return g.postJSON("/api/v1/auth/shell/refresh", string(b))
}

func tokensOf(t *testing.T, w *httptest.ResponseRecorder) shellTokensBody {
	t.Helper()
	var b shellTokensBody
	if w.Code != http.StatusOK || json.Unmarshal(w.Body.Bytes(), &b) != nil || b.AccessToken == "" || b.RefreshToken == "" {
		t.Fatalf("tokens = %d %s", w.Code, w.Body)
	}
	return b
}

func (g *redirectRig) signedInShell(t *testing.T) shellTokensBody {
	t.Helper()
	state := g.shellStart(t, "google", testVerifier, loopbackTarget)
	code := g.shellCode(t, state, "sec-"+state[:6])
	return tokensOf(t, g.redeem(code, testVerifier))
}

func (g *redirectRig) liveShellSessions(t *testing.T) int {
	t.Helper()
	var n int
	if err := g.svc.store.db.Pool.QueryRow(context.Background(),
		`SELECT count(*) FROM staff_sessions WHERE sub = $1 AND client_kind = 'shell' AND revoked_at IS NULL`, Subject("p", "u1")).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func TestShellStartRefusesBadParametersAndFilesTheShellLeg(t *testing.T) {
	g := newRedirectRig(t, okDir(), 0, "google")
	for name, path := range map[string]string{
		"foreign target":  shellStartPath("google", testVerifier, "https://evil.example/auth/callback"),
		"no challenge":    "/api/v1/auth/start/google?client=shell&return_to=" + url.QueryEscape(loopbackTarget),
		"plain method":    strings.Replace(shellStartPath("google", testVerifier, loopbackTarget), "S256", "plain", 1),
		"stray parameter": shellStartPath("google", testVerifier, loopbackTarget) + "&x=1",
		"web with query":  "/api/v1/auth/start/google?x=1",
	} {
		if w := g.get(path, nil); w.Code != http.StatusBadRequest || cookieNamed(w, StateCookieName) != nil {
			t.Errorf("%s: %d", name, w.Code)
		}
	}
	state := g.shellStart(t, "google", testVerifier, loopbackTarget)
	var kind, challenge, target string
	if err := g.svc.store.db.Pool.QueryRow(context.Background(),
		`SELECT client_kind, code_challenge, return_target FROM pending_sign_ins WHERE state_hash = $1`, hashID(state)).Scan(&kind, &challenge, &target); err != nil {
		t.Fatal(err)
	}
	if kind != "shell" || challenge != challengeOf(testVerifier) || target != loopbackTarget {
		t.Fatalf("row = %q %q %q", kind, challenge, target)
	}
}

func TestShellSignInEndToEndAndTheTokenKindsStayApart(t *testing.T) {
	g := newRedirectRig(t, okDir(), 0, "google")
	state := g.shellStart(t, "google", testVerifier, loopbackTarget)
	code := g.shellCode(t, state, "sec-1")
	var stored []byte
	_ = g.svc.store.db.Pool.QueryRow(context.Background(), `SELECT code_hash FROM pending_sign_ins WHERE state_hash = $1`, hashID(state)).Scan(&stored)
	if len(stored) != 32 || string(stored) == code {
		t.Fatal("the row must hold only the hash of the code")
	}
	tok := tokensOf(t, g.redeem(code, testVerifier))
	if !strings.HasPrefix(tok.AccessToken, middleware.ShellTokenPrefix) || tok.Sub != Subject("p", "u1").String() || len(tok.Roles) == 0 {
		t.Fatalf("tokens = %+v", tok)
	}
	if _, ok := familyOf(tok.RefreshToken); !ok {
		t.Fatal("the refresh token does not name its family")
	}
	ctx := context.Background()
	if _, err := g.svc.ResolveShell(ctx, tok.AccessToken); err != nil {
		t.Fatalf("the access token does not resolve: %v", err)
	}
	if _, err := g.svc.Resolve(ctx, tok.AccessToken); err == nil {
		t.Fatal("a shell token resolved as a cookie id")
	}
	if _, err := g.svc.ResolveShell(ctx, tok.RefreshToken); err == nil {
		t.Fatal("a refresh token resolved as an access token")
	}
	// Through the real auth layer: the bearer works with no CSRF header, and
	// the same value in the cookie does not.
	am, err := middleware.NewAuthMiddleware(ctx, middleware.AuthConfig{Sessions: g.svc}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	var seen *middleware.UserClaims
	h := am.Handler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = middleware.ClaimsFromContext(r.Context())
		w.WriteHeader(http.StatusOK)
	}))
	do := func(mut func(*http.Request)) int {
		r := httptest.NewRequest(http.MethodPost, "/api/v1/orders", nil)
		mut(r)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w.Code
	}
	if c := do(func(r *http.Request) { r.Header.Set("Authorization", "Bearer "+tok.AccessToken) }); c != http.StatusOK || seen == nil || seen.Subject != tok.Sub {
		t.Fatalf("bearer = %d, claims %v", c, seen)
	}
	if c := do(func(r *http.Request) {
		r.AddCookie(&http.Cookie{Name: middleware.SessionCookieName, Value: tok.AccessToken})
		r.Header.Set(middleware.CSRFHeaderName, "1")
	}); c != http.StatusUnauthorized {
		t.Fatalf("a shell token in the cookie = %d, want 401", c)
	}
	if c := do(func(r *http.Request) { r.Header.Set("Authorization", "Bearer "+middleware.ShellTokenPrefix+"unknown") }); c != http.StatusUnauthorized {
		t.Fatalf("an unknown shell token = %d, want 401", c)
	}
	// A valid shell bearer beside an invalid cookie is refused, not passed over.
	if c := do(func(r *http.Request) {
		r.Header.Set("Authorization", "Bearer "+tok.AccessToken)
		r.AddCookie(&http.Cookie{Name: middleware.SessionCookieName, Value: "nope"})
	}); c != http.StatusUnauthorized {
		t.Fatalf("valid bearer, invalid cookie = %d, want 401", c)
	}
}

func TestRedeemNeedsTheVerifierTheCodeWasStartedWith(t *testing.T) {
	g := newRedirectRig(t, okDir(), 0, "google")
	state := g.shellStart(t, "google", testVerifier, loopbackTarget)
	code := g.shellCode(t, state, "sec-1")
	other := strings.Repeat("z", 43)
	for name, w := range map[string]*httptest.ResponseRecorder{
		"wrong verifier": g.redeem(code, other),
		"short verifier": g.redeem(code, "short"),
		"wrong code":     g.redeem("not-the-code", testVerifier),
		"empty":          g.redeem("", ""),
	} {
		if w.Code != http.StatusUnauthorized {
			t.Errorf("%s = %d, want 401", name, w.Code)
		}
	}
	if g.liveShellSessions(t) != 0 {
		t.Fatal("a refused redemption issued a session")
	}
	// A wrong verifier burned nothing: the right one still works, once.
	if w := g.redeem(code, testVerifier); w.Code != http.StatusOK {
		t.Fatalf("the right verifier after refusals = %d", w.Code)
	}
	if w := g.redeem(code, testVerifier); w.Code != http.StatusUnauthorized {
		t.Fatalf("a reused code = %d, want 401", w.Code)
	}
	if g.liveShellSessions(t) != 1 {
		t.Fatalf("sessions = %d, want 1", g.liveShellSessions(t))
	}
}

func TestRedeemRefusesAnExpiredCodeAndARemovedMember(t *testing.T) {
	g := newRedirectRig(t, okDir(), 0, "google")
	state := g.shellStart(t, "google", testVerifier, loopbackTarget)
	code := g.shellCode(t, state, "sec-1")
	g.clk.t = g.clk.t.Add(codeTTL + time.Second)
	if w := g.redeem(code, testVerifier); w.Code != http.StatusUnauthorized {
		t.Fatalf("an expired code = %d", w.Code)
	}
	g.clk.t = g.clk.t.Add(-codeTTL)
	state = g.shellStart(t, "google", testVerifier, loopbackTarget)
	code = g.shellCode(t, state, "sec-2")
	g.dir.mu.Lock()
	g.dir.found = false // removed between the callback and the redemption
	g.dir.mu.Unlock()
	if w := g.redeem(code, testVerifier); w.Code != http.StatusUnauthorized || g.liveShellSessions(t) != 0 {
		t.Fatalf("a removed member = %d, sessions %d", w.Code, g.liveShellSessions(t))
	}
}

func TestShellCallbackRefusalReturnsToTheShellsOwnTarget(t *testing.T) {
	d := okDir()
	d.found = false
	g := newRedirectRig(t, d, 0, "google")
	state := g.shellStart(t, "google", testVerifier, loopbackTarget)
	w := g.callback(state, state, "u1", "sec-1")
	if w.Code != http.StatusSeeOther || w.Header().Get("Location") != loopbackTarget+"?error=refused" || cookieNamed(w, middleware.SessionCookieName) != nil {
		t.Fatalf("callback = %d to %q", w.Code, w.Header().Get("Location"))
	}
	var n int
	_ = g.svc.store.db.Pool.QueryRow(context.Background(), `SELECT count(*) FROM pending_sign_ins WHERE code_hash IS NOT NULL`).Scan(&n)
	if n != 0 {
		t.Fatal("a refused sign in filed a code")
	}
}

func TestRefreshRotatesAndAReusedTokenRevokesTheFamily(t *testing.T) {
	g := newRedirectRig(t, okDir(), 0, "google")
	first := g.signedInShell(t)
	second := tokensOf(t, g.refreshTokens(first.RefreshToken))
	if second.AccessToken == first.AccessToken || second.RefreshToken == first.RefreshToken {
		t.Fatal("a refresh must rotate both tokens")
	}
	f1, _ := familyOf(first.RefreshToken)
	if f2, _ := familyOf(second.RefreshToken); f1 != f2 {
		t.Fatal("rotation changed the family")
	}
	ctx := context.Background()
	if _, err := g.svc.ResolveShell(ctx, first.AccessToken); err == nil {
		t.Fatal("the rotated access token still resolves")
	}
	if _, err := g.svc.ResolveShell(ctx, second.AccessToken); err != nil {
		t.Fatalf("the new access token does not resolve: %v", err)
	}
	// The old refresh token again: reuse. The whole family dies.
	if w := g.refreshTokens(first.RefreshToken); w.Code != http.StatusUnauthorized {
		t.Fatalf("a reused refresh token = %d", w.Code)
	}
	if _, err := g.svc.ResolveShell(ctx, second.AccessToken); err == nil {
		t.Fatal("the family survived a reuse")
	}
	if w := g.refreshTokens(second.RefreshToken); w.Code != http.StatusUnauthorized {
		t.Fatalf("the revoked family refreshed = %d", w.Code)
	}
	if g.liveShellSessions(t) != 0 {
		t.Fatal("live sessions remain after a reuse")
	}
	if w := g.refreshTokens("garbage"); w.Code != http.StatusUnauthorized {
		t.Fatalf("garbage refresh token = %d", w.Code)
	}
}

// Three refreshes of one refresh token at pool size 4 never leave two live
// tokens: at most one rotates, and a loser revokes the family.
func TestThreeRefreshesOnOneTokenNeverLeaveTwoLive(t *testing.T) {
	for round := 0; round < 10; round++ {
		g := newRedirectRig(t, okDir(), 4, "google")
		_, _ = g.svc.store.db.Pool.Exec(context.Background(), `DELETE FROM staff_sessions WHERE sub = $1`, Subject("p", "u1"))
		first := g.signedInShell(t)
		var mu sync.Mutex
		var won []shellTokensBody
		var wg sync.WaitGroup
		for i := 0; i < 3; i++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				w := g.refreshTokens(first.RefreshToken)
				if w.Code == http.StatusOK {
					var b shellTokensBody
					_ = json.Unmarshal(w.Body.Bytes(), &b)
					mu.Lock()
					won = append(won, b)
					mu.Unlock()
				}
			}()
		}
		wg.Wait()
		live := 0
		for _, b := range won {
			if _, err := g.svc.ResolveShell(context.Background(), b.AccessToken); err == nil {
				live++
			}
		}
		if len(won) > 1 || live > 1 || g.liveShellSessions(t) > 1 {
			t.Fatalf("round %d: %d rotations, %d live tokens, %d live rows", round, len(won), live, g.liveShellSessions(t))
		}
		if _, err := g.svc.ResolveShell(context.Background(), first.AccessToken); err == nil {
			t.Fatalf("round %d: the old access token survived", round)
		}
	}
}

// Three redemptions of one code at pool size 4 issue exactly one session.
func TestThreeRedemptionsOfOneCodeIssueOneSession(t *testing.T) {
	for round := 0; round < 10; round++ {
		g := newRedirectRig(t, okDir(), 4, "google")
		_, _ = g.svc.store.db.Pool.Exec(context.Background(), `DELETE FROM staff_sessions WHERE sub = $1`, Subject("p", "u1"))
		state := g.shellStart(t, "google", testVerifier, loopbackTarget)
		code := g.shellCode(t, state, "sec-1")
		var mu sync.Mutex
		ok := 0
		var wg sync.WaitGroup
		for i := 0; i < 3; i++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				if g.redeem(code, testVerifier).Code == http.StatusOK {
					mu.Lock()
					ok++
					mu.Unlock()
				}
			}()
		}
		wg.Wait()
		if ok != 1 || g.liveShellSessions(t) != 1 {
			t.Fatalf("round %d: %d redemptions succeeded, %d sessions, want 1 and 1", round, ok, g.liveShellSessions(t))
		}
	}
}

func TestShellSignOutRevokesTheSessionAndOnlyAShellToken(t *testing.T) {
	g := newRedirectRig(t, okDir(), 0, "google")
	tok := g.signedInShell(t)
	signout := func(token string) int {
		return post(g.mux, "/api/v1/auth/shell/signout", "", func(r *http.Request) { r.Header.Set("Authorization", "Bearer "+token) }).Code
	}
	if signout(tok.RefreshToken) != http.StatusNoContent || g.liveShellSessions(t) != 1 {
		t.Fatal("a refresh token signed the session out")
	}
	if signout(tok.AccessToken) != http.StatusNoContent {
		t.Fatal("sign out did not answer 204")
	}
	if _, err := g.svc.ResolveShell(context.Background(), tok.AccessToken); err == nil || g.liveShellSessions(t) != 0 {
		t.Fatal("the shell session survived a sign out")
	}
	if w := g.refreshTokens(tok.RefreshToken); w.Code != http.StatusUnauthorized {
		t.Fatalf("a signed out shell refreshed = %d", w.Code)
	}
}

func TestShellRoutesAreLimitedPerAddress(t *testing.T) {
	g := newRedirectRig(t, okDir(), 0, "google")
	g.h.shellByAddress = newLimiter(3, time.Hour)
	for i := 0; i < 3; i++ {
		if w := g.redeem("x", testVerifier); w.Code == http.StatusTooManyRequests {
			t.Fatalf("attempt %d limited early", i)
		}
	}
	w := g.refreshTokens("x")
	if w.Code != http.StatusTooManyRequests || w.Header().Get("Retry-After") == "" {
		t.Fatalf("past the limit = %d, Retry-After %q", w.Code, w.Header().Get("Retry-After"))
	}
}

func TestTheMagicLinkIsAWebMethodOnly(t *testing.T) {
	g := newRedirectRig(t, okDir(), 0, "magic_link")
	w := post2(g.mux, "/api/v1/auth/start/magic_link", `{"email":"a@x.example","client":"shell"}`)
	if w.Code != http.StatusBadRequest || len(g.dir.magicSent) != 0 {
		t.Fatalf("a shell magic link start = %d, links %v", w.Code, g.dir.magicSent)
	}
}
