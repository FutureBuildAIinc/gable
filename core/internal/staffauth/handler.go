// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package staffauth

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/gablelbm/gable/internal/platform/httpx"
	"github.com/gablelbm/gable/pkg/clientip"
	"github.com/gablelbm/gable/pkg/middleware"
)

// refusedMessage is the one fixed answer every sign in refusal gives, so the
// response never tells a stranger which check failed.
const refusedMessage = "sign in failed"

// HandlerConfig are the handler's settings.
type HandlerConfig struct {
	// Origin is Gable's own public origin (scheme and host), the value a
	// browser sends as Origin from Gable's own pages.
	Origin  string
	Trusted clientip.Trusted
	// Per address and per email attempts in the window (zero uses defaults).
	PerAddress int
	PerEmail   int
	Window     time.Duration
	// Insecure drops the Secure attribute of the session cookie. It exists
	// only for tests that run over plain http.
	Insecure bool
	// SignInFloor is the least time a password answer (200, 401 or 503) takes
	// from the start of the attempt. Zero uses DefaultSignInFloor; negative
	// turns the floor off (tests only).
	SignInFloor time.Duration
}

// DefaultSignInFloor sits above Appwrite's slow path (a password hash), so an
// unknown address, which Appwrite answers without hashing, takes as long as a
// wrong password.
const DefaultSignInFloor = time.Second

// Handler serves the staff sign in, refresh and sign out routes.
type Handler struct {
	svc          *Service
	cfg          HandlerConfig
	byAddress    *limiter
	byEmail      *limiter
	cookieSecure bool

	// The redirect methods' start routes carry limits of their own, per
	// client address and per email (the callback shares the address one).
	startByAddress *limiter
	startByEmail   *limiter
	shellByAddress *limiter
	allowed        func(ctx context.Context, method string) bool

	// emailPasswordAllowed, when set, is consulted before any credential is
	// read: the dealer's sign in allowlist must hold email_password.
	emailPasswordAllowed func(ctx context.Context) bool
}

// WithEmailPasswordGuard gates the sign in on the dealer's sign in method
// allowlist, as the portal login is gated.
func (h *Handler) WithEmailPasswordGuard(fn func(ctx context.Context) bool) *Handler {
	h.emailPasswordAllowed = fn
	return h
}

// NewHandler returns the handler.
func NewHandler(svc *Service, cfg HandlerConfig) *Handler {
	if cfg.PerAddress <= 0 {
		cfg.PerAddress = 20
	}
	if cfg.PerEmail <= 0 {
		cfg.PerEmail = 8
	}
	if cfg.Window <= 0 {
		cfg.Window = 15 * time.Minute
	}
	return &Handler{
		svc: svc, cfg: cfg,
		byAddress: newLimiter(cfg.PerAddress, cfg.Window),
		byEmail:   newLimiter(cfg.PerEmail, cfg.Window),

		startByAddress: newLimiter(cfg.PerAddress, cfg.Window),
		startByEmail:   newLimiter(cfg.PerEmail, cfg.Window),
		shellByAddress: newLimiter(cfg.PerAddress, cfg.Window),
		cookieSecure:   !cfg.Insecure,
	}
}

// RegisterRoutes mounts the routes. Every one but the session read is a
// public path of the auth layer (they read the cookie themselves).
func (h *Handler) RegisterRoutes(mux *http.ServeMux) {
	mux.HandleFunc("POST /api/v1/auth/signin", h.SignIn)
	mux.HandleFunc("POST /api/v1/auth/refresh", h.Refresh)
	mux.HandleFunc("POST /api/v1/auth/signout", h.SignOut)
	mux.HandleFunc("GET /api/v1/auth/session", h.Session)
	// The redirect methods (ADR 0012 section 4): the provider starts by
	// navigation, the magic link start, and the callback every method ends at.
	mux.HandleFunc("GET /api/v1/auth/start/{method}", h.StartRedirect)
	mux.HandleFunc("POST /api/v1/auth/start/magic_link", h.StartMagicLink)
	mux.HandleFunc("GET /api/v1/auth/callback", h.Callback)
	// The shell's token path (ADR 0012 section 3): redeem the one time code,
	// rotate the refresh token, sign out by the access token. Not cookie
	// requests, so no CSRF header; each is limited per client address.
	mux.HandleFunc("POST /api/v1/auth/shell/signin", h.ShellSignIn)
	mux.HandleFunc("POST /api/v1/auth/shell/redeem", h.ShellRedeem)
	mux.HandleFunc("POST /api/v1/auth/shell/refresh", h.ShellRefresh)
	mux.HandleFunc("POST /api/v1/auth/shell/signout", h.ShellSignOut)
}

type identityBody struct {
	Sub   string   `json:"sub"`
	Roles []string `json:"roles"`
}

func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}

// sameSiteIntent reports whether a state changing request came from Gable's
// own page: it carries the custom header a cross site form cannot set, or an
// Origin that is Gable's own.
func (h *Handler) sameSiteIntent(r *http.Request) bool {
	if r.Header.Get(middleware.CSRFHeaderName) != "" {
		return true
	}
	origin := r.Header.Get("Origin")
	if origin == "" || h.cfg.Origin == "" {
		return false
	}
	a, err1 := url.Parse(origin)
	b, err2 := url.Parse(h.cfg.Origin)
	return err1 == nil && err2 == nil && strings.EqualFold(a.Scheme, b.Scheme) && strings.EqualFold(a.Host, b.Host)
}

func (h *Handler) setCookie(w http.ResponseWriter, id string, maxAge int) {
	http.SetCookie(w, &http.Cookie{
		Name: middleware.SessionCookieName, Value: id, Path: "/api", MaxAge: maxAge,
		HttpOnly: true, Secure: h.cookieSecure, SameSite: http.SameSiteStrictMode,
	})
}

func (h *Handler) limited(w http.ResponseWriter, r *http.Request, d time.Duration) {
	secs := int(d.Seconds()) + 1
	w.Header().Set("Retry-After", strconv.Itoa(secs))
	httpx.WriteError(w, r, httpx.RateLimited(refusedMessage))
}

// passwordAttempt is the step both email and password doors share, so they
// answer alike: the dealer's allowlist before any credential is read, the
// body, then one budget per client address and per email across both doors.
// It answers the refusal itself and reports false.
//
// On success it also returns the attempt's start, taken at its end: past the
// guard, the body and both limits, right before the service call. The floor
// that start feeds must not be run out by work before it, or a client that
// sends its body late would wait out the floor before the credential check
// begins. The allowlist 403, the 400 and the 429 answer before Appwrite and
// are the same for every address, so they carry no floor.
func (h *Handler) passwordAttempt(w http.ResponseWriter, r *http.Request) (email, password string, begun time.Time, ok bool) {
	if h.emailPasswordAllowed != nil && !h.emailPasswordAllowed(r.Context()) {
		httpx.WriteError(w, r, httpx.Forbidden("Sign in with email and password is not allowed for this dealer"))
		return "", "", time.Time{}, false
	}
	var body struct {
		Email    string `json:"email"`
		Password string `json:"password"`
	}
	r.Body = http.MaxBytesReader(w, r.Body, 8<<10)
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		httpx.WriteError(w, r, httpx.BadRequest("request body must be {email, password}"))
		return "", "", time.Time{}, false
	}
	if ok, d := h.byAddress.allow(h.cfg.Trusted.Of(r)); !ok {
		h.limited(w, r, d)
		return "", "", time.Time{}, false
	}
	if ok, d := h.byEmail.allow(strings.ToLower(strings.TrimSpace(body.Email))); !ok {
		h.limited(w, r, d)
		return "", "", time.Time{}, false
	}
	return body.Email, body.Password, time.Now(), true
}

// holdFloor waits until the sign in floor has passed since begun, so every
// password answer takes at least that long. It never shortens a slower one
// and gives up if the request is cancelled.
func (h *Handler) holdFloor(r *http.Request, begun time.Time) {
	floor := h.cfg.SignInFloor
	if floor == 0 {
		floor = DefaultSignInFloor
	}
	if floor < 0 {
		return
	}
	wait := floor - time.Since(begun)
	if wait <= 0 {
		return
	}
	t := time.NewTimer(wait)
	defer t.Stop()
	select {
	case <-t.C:
	case <-r.Context().Done():
	}
}

// SignIn is POST /api/v1/auth/signin. A presented session id is ignored and a
// fresh one is issued every time.
func (h *Handler) SignIn(w http.ResponseWriter, r *http.Request) {
	if !h.sameSiteIntent(r) {
		httpx.WriteError(w, r, httpx.Unauthorized(refusedMessage))
		return
	}
	email, password, begun, ok := h.passwordAttempt(w, r)
	if !ok {
		return
	}
	id, who, err := h.svc.SignIn(r.Context(), email, password, "web")
	h.holdFloor(r, begun)
	switch {
	case errors.Is(err, ErrRefused):
		httpx.WriteError(w, r, httpx.Unauthorized(refusedMessage))
	case errors.Is(err, ErrUnavailable):
		httpx.WriteError(w, r, httpx.Unavailable("sign in is unavailable"))
	case err != nil:
		httpx.WriteError(w, r, err)
	default:
		h.setCookie(w, id, int(h.svc.set.AbsoluteLimit.Seconds()))
		writeJSON(w, http.StatusOK, identityBody{Sub: who.Sub.String(), Roles: who.Roles})
	}
}

func (h *Handler) cookieID(r *http.Request) string {
	if c, err := r.Cookie(middleware.SessionCookieName); err == nil {
		return c.Value
	}
	return ""
}

// Refresh is POST /api/v1/auth/refresh: it re-reads the membership now and
// answers the roles the session holds.
func (h *Handler) Refresh(w http.ResponseWriter, r *http.Request) {
	if r.Header.Get(middleware.CSRFHeaderName) == "" {
		httpx.WriteError(w, r, httpx.Unauthorized(refusedMessage))
		return
	}
	who, err := h.svc.Refresh(r.Context(), h.cookieID(r))
	switch {
	case errors.Is(err, ErrNoSession):
		httpx.WriteError(w, r, httpx.Unauthorized("invalid session"))
	case err != nil:
		httpx.WriteError(w, r, err)
	default:
		writeJSON(w, http.StatusOK, identityBody{Sub: who.Sub.String(), Roles: who.Roles})
	}
}

// SignOut is POST /api/v1/auth/signout: it revokes the session and clears the
// cookie. It answers 204 whatever the cookie held.
func (h *Handler) SignOut(w http.ResponseWriter, r *http.Request) {
	if r.Header.Get(middleware.CSRFHeaderName) == "" {
		httpx.WriteError(w, r, httpx.Unauthorized(refusedMessage))
		return
	}
	if id := h.cookieID(r); id != "" {
		h.svc.SignOut(r.Context(), id)
	}
	h.setCookie(w, "", -1)
	w.WriteHeader(http.StatusNoContent)
}

// Session is GET /api/v1/auth/session: the display data of the signed in
// person, read from the claims the auth layer set. The auth layer answers 401
// first, so a request that reaches here has valid claims.
func (h *Handler) Session(w http.ResponseWriter, r *http.Request) {
	c := middleware.ClaimsFromContext(r.Context())
	if c == nil {
		httpx.WriteError(w, r, httpx.Unauthorized("no session"))
		return
	}
	writeJSON(w, http.StatusOK, identityBody{Sub: c.Subject, Roles: c.Roles})
}
