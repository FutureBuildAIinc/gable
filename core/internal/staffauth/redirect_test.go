// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package staffauth

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gablelbm/gable/internal/appwrite"
	"github.com/gablelbm/gable/internal/testutil"
	"github.com/gablelbm/gable/pkg/middleware"
)

// redirectDir is the fake Appwrite for the redirect methods. A one time
// secret is accepted once; a second exchange of it is a 401, as Appwrite
// answers.
type redirectDir struct {
	*fakeDir
	exchanges int
	spent     map[string]bool
	magicSent []string
	magicURLs []string
}

func newRedirectDir(d *fakeDir) *redirectDir {
	return &redirectDir{fakeDir: d, spent: map[string]bool{}}
}

func (r *redirectDir) OAuth2TokenURL(provider, success, failure string) string {
	return "https://aw.example/v1/account/tokens/oauth2/" + provider +
		"?success=" + url.QueryEscape(success) + "&failure=" + url.QueryEscape(failure)
}

func (r *redirectDir) CreateSessionFromToken(ctx context.Context, userID, secret string) (appwrite.Session, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.spent[secret] {
		return appwrite.Session{}, &appwrite.Error{StatusCode: 401, Type: "user_invalid_token"}
	}
	r.spent[secret] = true
	r.exchanges++
	return appwrite.Session{ID: "aws-tok", UserID: userID, Secret: "aws-secret"}, nil
}

func (r *redirectDir) FindUserByEmail(ctx context.Context, email string) (appwrite.User, bool, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if strings.EqualFold(email, r.user.Email) {
		return r.user, true, nil
	}
	return appwrite.User{}, false, nil
}

func (r *redirectDir) CreateMagicURLToken(ctx context.Context, userID, email, redirectURL string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.magicSent = append(r.magicSent, email)
	r.magicURLs = append(r.magicURLs, redirectURL)
	return nil
}

func (r *redirectDir) exchangeCount() int { r.mu.Lock(); defer r.mu.Unlock(); return r.exchanges }

type redirectRig struct {
	dir *redirectDir
	svc *Service
	clk *clock
	mux *http.ServeMux
	h   *Handler
}

func newRedirectRig(t *testing.T, d *fakeDir, maxConns int, allowed ...string) *redirectRig {
	t.Helper()
	db := testutil.RequireDB(t)
	if maxConns > 0 {
		db = testutil.RequireDBMaxConns(t, int32(maxConns))
	}
	dir := newRedirectDir(d)
	clk := &clock{t: time.Now().UTC()}
	svc := NewService(dir, NewStore(db), &auditRec{}, Settings{ProjectID: "p", TeamID: "t"}, nil).WithRedirects(dir, ownOrigin)
	svc.now = clk.now
	t.Cleanup(func() {
		_, _ = db.Pool.Exec(context.Background(), `DELETE FROM staff_sessions WHERE sub = $1`, Subject("p", "u1"))
		_, _ = db.Pool.Exec(context.Background(), `DELETE FROM pending_sign_ins`)
	})
	ok := map[string]bool{}
	for _, m := range allowed {
		ok[m] = true
	}
	h := NewHandler(svc, HandlerConfig{SignInFloor: -1, Origin: ownOrigin}).WithAllowed(func(_ context.Context, m string) bool { return ok[m] })
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)
	return &redirectRig{dir: dir, svc: svc, clk: clk, mux: mux, h: h}
}

func (g *redirectRig) get(path string, mut func(*http.Request)) *httptest.ResponseRecorder {
	r := httptest.NewRequest(http.MethodGet, path, nil)
	r.RemoteAddr = "203.0.113.9:4000"
	if mut != nil {
		mut(r)
	}
	w := httptest.NewRecorder()
	g.mux.ServeHTTP(w, r)
	return w
}

func cookieNamed(w *httptest.ResponseRecorder, name string) *http.Cookie {
	for _, c := range w.Result().Cookies() {
		if c.Name == name {
			return c
		}
	}
	return nil
}

// start runs a provider start and returns the raw state from the cookie.
func (g *redirectRig) start(t *testing.T, method string) (state string, w *httptest.ResponseRecorder) {
	t.Helper()
	w = g.get("/api/v1/auth/start/"+method, nil)
	if w.Code != http.StatusFound {
		t.Fatalf("start %s = %d: %s", method, w.Code, w.Body)
	}
	c := cookieNamed(w, StateCookieName)
	if c == nil || c.Value == "" {
		t.Fatal("no state cookie")
	}
	return c.Value, w
}

func (g *redirectRig) callback(state, cookie, user, secret string) *httptest.ResponseRecorder {
	q := url.Values{}
	if state != "" {
		q.Set("state", state)
	}
	if user != "" {
		q.Set("userId", user)
	}
	if secret != "" {
		q.Set("secret", secret)
	}
	return g.get(CallbackPath+"?"+q.Encode(), func(r *http.Request) {
		if cookie != "" {
			r.AddCookie(&http.Cookie{Name: StateCookieName, Value: cookie})
		}
	})
}

func (g *redirectRig) pendingUsed(t *testing.T, state string) (used bool, sub *string) {
	t.Helper()
	err := g.svc.store.db.Pool.QueryRow(context.Background(),
		`SELECT used, sub::text FROM pending_sign_ins WHERE state_hash = $1`, hashID(state)).Scan(&used, &sub)
	if err != nil {
		t.Fatal(err)
	}
	return used, sub
}

func TestStartRedirectSendsTheBrowserToAppwriteWithGablesCallbackOnly(t *testing.T) {
	g := newRedirectRig(t, okDir(), 0, "google")
	state, w := g.start(t, "google")
	loc, err := url.Parse(w.Header().Get("Location"))
	if err != nil || loc.Host != "aw.example" || !strings.HasSuffix(loc.Path, "/oauth2/google") {
		t.Fatalf("Location = %q", w.Header().Get("Location"))
	}
	for _, key := range []string{"success", "failure"} {
		u, err := url.Parse(loc.Query().Get(key))
		if err != nil || u.Scheme+"://"+u.Host != ownOrigin || u.Path != CallbackPath || u.Query().Get("state") != state {
			t.Fatalf("%s URL = %q, want Gable's callback carrying the state", key, loc.Query().Get(key))
		}
	}
	if loc.Query().Get("failure") == loc.Query().Get("success") {
		t.Fatal("the failure URL must be told apart from the success URL")
	}
	c := cookieNamed(w, StateCookieName)
	if !c.HttpOnly || !c.Secure || c.SameSite != http.SameSiteLaxMode || c.Path != "/api" || c.Domain != "" || c.MaxAge <= 0 {
		t.Fatalf("state cookie = %+v, want HttpOnly Secure Lax Path=/api, no Domain, short lived", c)
	}
	if used, _ := g.pendingUsed(t, state); used {
		t.Fatal("a fresh pending sign in is already used")
	}
}

func TestStartRefusesAMethodTheDealerDidNotAllow(t *testing.T) {
	g := newRedirectRig(t, okDir(), 0, "google")
	if w := g.get("/api/v1/auth/start/microsoft", nil); w.Code != http.StatusForbidden || cookieNamed(w, StateCookieName) != nil {
		t.Fatalf("a disallowed method = %d", w.Code)
	}
	if w := g.get("/api/v1/auth/start/passkey", nil); w.Code != http.StatusNotFound {
		t.Fatalf("a method not started by navigation = %d, want 404", w.Code)
	}
	var n int
	_ = g.svc.store.db.Pool.QueryRow(context.Background(), `SELECT count(*) FROM pending_sign_ins`).Scan(&n)
	if n != 0 {
		t.Fatalf("%d pending rows filed for refused starts", n)
	}
}

func TestCallbackSignsTheMemberInAndSpendsTheState(t *testing.T) {
	g := newRedirectRig(t, okDir(), 0, "google")
	state, _ := g.start(t, "google")
	w := g.callback(state, state, "u1", "one-time")
	if w.Code != http.StatusSeeOther || w.Header().Get("Location") != "/" {
		t.Fatalf("callback = %d to %q", w.Code, w.Header().Get("Location"))
	}
	if w.Header().Get("Referrer-Policy") != "no-referrer" {
		t.Fatalf("Referrer-Policy = %q", w.Header().Get("Referrer-Policy"))
	}
	s := cookieNamed(w, middleware.SessionCookieName)
	if s == nil || !s.HttpOnly || !s.Secure || s.SameSite != http.SameSiteStrictMode || s.Path != "/api" || s.Domain != "" {
		t.Fatalf("session cookie = %+v", s)
	}
	if sc := cookieNamed(w, StateCookieName); sc == nil || sc.MaxAge >= 0 {
		t.Fatalf("state cookie after the callback = %+v, want cleared", sc)
	}
	if got := g.dir.ended; len(got) != 1 || got[0] != "aws-tok" {
		t.Fatalf("appwrite sessions ended = %v, want the one the exchange opened", got)
	}
	used, sub := g.pendingUsed(t, state)
	if !used || sub == nil || *sub != Subject("p", "u1").String() {
		t.Fatalf("pending used = %v sub = %v, want used and naming the person", used, sub)
	}
	if _, err := g.svc.Resolve(context.Background(), s.Value); err != nil {
		t.Fatalf("the issued session does not resolve: %v", err)
	}
}

func TestCallbackRefusesAMissingWrongOrReusedStateBeforeAnyExchange(t *testing.T) {
	g := newRedirectRig(t, okDir(), 0, "google")
	state, _ := g.start(t, "google")
	other, _ := g.start(t, "google")
	for name, w := range map[string]*httptest.ResponseRecorder{
		"no cookie":      g.callback(state, "", "u1", "s1"),
		"no state":       g.callback("", state, "u1", "s2"),
		"cookie differs": g.callback(state, other, "u1", "s3"),
		"never filed":    g.callback("not-a-state", "not-a-state", "u1", "s4"),
	} {
		if w.Code != http.StatusSeeOther || w.Header().Get("Location") != refusedLocation || cookieNamed(w, middleware.SessionCookieName) != nil {
			t.Errorf("%s: %d to %q", name, w.Code, w.Header().Get("Location"))
		}
	}
	if n := g.dir.exchangeCount(); n != 0 {
		t.Fatalf("%d exchanges happened before the state check passed", n)
	}
	if used, _ := g.pendingUsed(t, state); used {
		t.Fatal("a refused callback spent the state")
	}
	if w := g.callback(state, state, "u1", "s5"); w.Header().Get("Location") != "/" {
		t.Fatalf("the genuine callback after the refusals = %q", w.Header().Get("Location"))
	}
	again := g.callback(state, state, "u1", "s6")
	if again.Header().Get("Location") != refusedLocation || g.dir.exchangeCount() != 1 {
		t.Fatalf("a reused state: %q, %d exchanges, want a refusal and one exchange", again.Header().Get("Location"), g.dir.exchangeCount())
	}
}

func TestCallbackRefusesAnExpiredState(t *testing.T) {
	g := newRedirectRig(t, okDir(), 0, "google")
	state, _ := g.start(t, "google")
	g.clk.t = g.clk.t.Add(pendingTTL + time.Second)
	if w := g.callback(state, state, "u1", "s1"); w.Header().Get("Location") != refusedLocation {
		t.Fatalf("an expired state = %q", w.Header().Get("Location"))
	}
	if g.dir.exchangeCount() != 0 {
		t.Fatal("an expired state reached Appwrite")
	}
}

func TestCallbackRefusesAReplayedSecretAndAProviderFailure(t *testing.T) {
	g := newRedirectRig(t, okDir(), 0, "google")
	a, _ := g.start(t, "google")
	if w := g.callback(a, a, "u1", "same-secret"); w.Header().Get("Location") != "/" {
		t.Fatal("first use refused")
	}
	b, _ := g.start(t, "google")
	if w := g.callback(b, b, "u1", "same-secret"); w.Header().Get("Location") != refusedLocation {
		t.Fatalf("a replayed secret = %q, want the refusal", w.Header().Get("Location"))
	}
	c, _ := g.start(t, "google")
	w := g.get(CallbackPath+"?state="+c+"&failure=1", func(r *http.Request) { r.AddCookie(&http.Cookie{Name: StateCookieName, Value: c}) })
	if w.Header().Get("Location") != refusedLocation {
		t.Fatalf("a provider failure = %q", w.Header().Get("Location"))
	}
}

func TestCallbackRefusesWhoAppwriteAdmitsButGableDoesNot(t *testing.T) {
	for name, mutate := range map[string]func(*fakeDir){
		"non member":    func(d *fakeDir) { d.found = false },
		"unconfirmed":   func(d *fakeDir) { d.member.Confirmed = false },
		"no gable role": func(d *fakeDir) { d.member.Roles = []string{"something-else"} },
		"unverified":    func(d *fakeDir) { d.user.EmailVerification = false },
	} {
		d := okDir()
		mutate(d)
		g := newRedirectRig(t, d, 0, "google")
		state, _ := g.start(t, "google")
		w := g.callback(state, state, "u1", "s")
		if w.Header().Get("Location") != refusedLocation || cookieNamed(w, middleware.SessionCookieName) != nil {
			t.Errorf("%s: %d to %q", name, w.Code, w.Header().Get("Location"))
		}
		if len(g.dir.ended) != 1 {
			t.Errorf("%s: the appwrite session the exchange opened was not ended (%v)", name, g.dir.ended)
		}
		if rowCount(t, g.svc) != 0 {
			t.Errorf("%s: a core session was issued", name)
		}
	}
}

// Three callbacks on one state at pool size 4: exactly one exchange happens,
// one session is issued, and the others are refused.
func TestThreeCallbacksOnOneStateExchangeOnce(t *testing.T) {
	for round := 0; round < 10; round++ {
		g := newRedirectRig(t, okDir(), 4, "google")
		_, _ = g.svc.store.db.Pool.Exec(context.Background(), `DELETE FROM staff_sessions WHERE sub = $1`, Subject("p", "u1"))
		state, _ := g.start(t, "google")
		var mu sync.Mutex
		granted, refused := 0, 0
		var wg sync.WaitGroup
		for i := 0; i < 3; i++ {
			wg.Add(1)
			secret := "secret-" + string(rune('a'+i))
			go func() {
				defer wg.Done()
				w := g.callback(state, state, "u1", secret)
				mu.Lock()
				defer mu.Unlock()
				if w.Header().Get("Location") == "/" {
					granted++
				} else {
					refused++
				}
			}()
		}
		wg.Wait()
		if granted != 1 || refused != 2 || g.dir.exchangeCount() != 1 || rowCount(t, g.svc) != 1 {
			t.Fatalf("round %d: granted %d refused %d exchanges %d sessions %d, want 1, 2, 1, 1",
				round, granted, refused, g.dir.exchangeCount(), rowCount(t, g.svc))
		}
	}
}

func TestMagicLinkAnswersTheSameForEveryone(t *testing.T) {
	post := func(g *redirectRig, email string) *httptest.ResponseRecorder {
		return post2(g.mux, "/api/v1/auth/start/magic_link", `{"email":"`+email+`"}`)
	}
	d := okDir()
	g := newRedirectRig(t, d, 0, "magic_link")
	member := post(g, "a@x.example")
	stranger := post(g, "nobody@x.example")
	if member.Code != http.StatusOK || stranger.Code != member.Code || member.Body.String() != stranger.Body.String() {
		t.Fatalf("member %d %q vs stranger %d %q", member.Code, member.Body, stranger.Code, stranger.Body)
	}
	for _, w := range []*httptest.ResponseRecorder{member, stranger} {
		c := cookieNamed(w, StateCookieName)
		if c == nil || c.SameSite != http.SameSiteLaxMode || c.Path != "/api" || !c.HttpOnly || c.Value == "" {
			t.Fatalf("state cookie = %+v, want the same shape for member and stranger", c)
		}
	}
	if len(g.dir.magicSent) != 1 || g.dir.magicSent[0] != "a@x.example" {
		t.Fatalf("links sent = %v, want one, to the member", g.dir.magicSent)
	}
	// A non member and an unverified member get no link and no user is made.
	for name, mutate := range map[string]func(*fakeDir){
		"non member": func(d *fakeDir) { d.found = false },
		"unverified": func(d *fakeDir) { d.user.EmailVerification = false },
	} {
		d := okDir()
		mutate(d)
		g := newRedirectRig(t, d, 0, "magic_link")
		w := post(g, "a@x.example")
		if w.Code != http.StatusOK || w.Body.String() != member.Body.String() || len(g.dir.magicSent) != 0 {
			t.Errorf("%s: %d, links %v", name, w.Code, g.dir.magicSent)
		}
	}
}

func TestMagicLinkEndsAtTheCallbackInTheBrowserThatAsked(t *testing.T) {
	g := newRedirectRig(t, okDir(), 0, "magic_link")
	w := post2(g.mux, "/api/v1/auth/start/magic_link", `{"email":"a@x.example"}`)
	state := cookieNamed(w, StateCookieName).Value
	link, err := url.Parse(g.dir.magicURLs[0])
	if err != nil || link.Path != CallbackPath || link.Query().Get("state") != state {
		t.Fatalf("the link target = %q, want Gable's callback with the state", g.dir.magicURLs[0])
	}
	// Opened in another browser (no state cookie): refused, nothing spent.
	if other := g.callback(state, "", "u1", "magic-1"); other.Header().Get("Location") != refusedLocation {
		t.Fatalf("another browser = %q", other.Header().Get("Location"))
	}
	if ok := g.callback(state, state, "u1", "magic-2"); ok.Header().Get("Location") != "/" || cookieNamed(ok, middleware.SessionCookieName) == nil {
		t.Fatalf("the asking browser = %q", ok.Header().Get("Location"))
	}
}

func TestMagicLinkStartNeedsSameSiteIntentAndAnAllowedMethod(t *testing.T) {
	g := newRedirectRig(t, okDir(), 0, "magic_link")
	cross := httptest.NewRequest(http.MethodPost, "/api/v1/auth/start/magic_link", strings.NewReader(`{"email":"a@x.example"}`))
	cross.Header.Set("Origin", "https://evil.example")
	w := httptest.NewRecorder()
	g.mux.ServeHTTP(w, cross)
	if w.Code != http.StatusUnauthorized || len(g.dir.magicSent) != 0 {
		t.Fatalf("a cross site form post = %d, links %v", w.Code, g.dir.magicSent)
	}
	off := newRedirectRig(t, okDir(), 0, "google")
	if w := post2(off.mux, "/api/v1/auth/start/magic_link", `{"email":"a@x.example"}`); w.Code != http.StatusForbidden || len(off.dir.magicSent) != 0 {
		t.Fatalf("a method the dealer did not allow = %d", w.Code)
	}
}

func post2(h http.Handler, path, body string) *httptest.ResponseRecorder {
	return post(h, path, body, func(r *http.Request) { r.Header.Set("Origin", ownOrigin) })
}

func TestStartLimitsPerAddressAndPerEmail(t *testing.T) {
	g := newRedirectRig(t, okDir(), 0, "google", "magic_link")
	g.h.startByEmail = newLimiter(2, time.Hour)
	g.h.startByAddress = newLimiter(4, time.Hour)
	for i := 0; i < 2; i++ {
		if w := post2(g.mux, "/api/v1/auth/start/magic_link", `{"email":"x@x.example"}`); w.Code != http.StatusOK {
			t.Fatalf("attempt %d = %d", i, w.Code)
		}
	}
	w := post2(g.mux, "/api/v1/auth/start/magic_link", `{"email":"x@x.example"}`)
	if w.Code != http.StatusTooManyRequests || w.Header().Get("Retry-After") == "" {
		t.Fatalf("third on one email = %d, Retry-After %q", w.Code, w.Header().Get("Retry-After"))
	}
	for i := 0; i < 2; i++ {
		g.get("/api/v1/auth/start/google", nil)
	}
	if w := g.get("/api/v1/auth/start/google", nil); w.Code != http.StatusTooManyRequests {
		t.Fatalf("past the address limit = %d", w.Code)
	}
}

type fixedVersion struct {
	v   string
	err error
}

func (f fixedVersion) Version(ctx context.Context) (string, error) { return f.v, f.err }

// The redirect methods stay closed until a version at or above the floor has
// been read; a version that cannot be read keeps them closed.
func TestRedirectsStayClosedUntilTheVersionIsRead(t *testing.T) {
	g := newRedirectRig(t, okDir(), 0, "google")
	g.svc.WithFloorWatch(fixedVersion{err: errors.New("unreachable")}, time.Hour)
	if w := g.get("/api/v1/auth/start/google", nil); w.Code != http.StatusServiceUnavailable {
		t.Fatalf("an unreadable version = %d, want 503", w.Code)
	}
	g.svc.WithFloorWatch(fixedVersion{v: "1.9.0"}, time.Hour)
	if w := g.get("/api/v1/auth/start/google", nil); w.Code != http.StatusServiceUnavailable {
		t.Fatalf("a version below the floor = %d, want 503", w.Code)
	}
	g.svc.WithFloorWatch(fixedVersion{v: "2.1.0"}, time.Hour)
	if w := g.get("/api/v1/auth/start/google", nil); w.Code != http.StatusFound {
		t.Fatalf("a version at the floor = %d, want the redirect", w.Code)
	}
}
