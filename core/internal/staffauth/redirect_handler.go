// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package staffauth

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"github.com/gablelbm/gable/internal/platform/httpx"
)

// StateCookieName carries the raw state from the start route to the callback.
const StateCookieName = "gable_signin_state"

// stateCookieMaxAge matches the pending row's lifetime, in seconds.
const stateCookieMaxAge = int(pendingTTL / 1e9)

// refusedLocation is where a browser is sent after any callback refusal. It
// is one fixed address whatever the reason, so the page tells a stranger
// nothing.
const refusedLocation = "/?sign_in=failed"

// magicLinkReply is the one answer the magic link start gives.
const magicLinkReply = "if that address belongs to a member, a sign in link is on its way"

// WithAllowed sets the dealer allowlist check the start routes consult: a
// method the dealer has not allowed is refused. Without it every start is
// refused (fail closed).
func (h *Handler) WithAllowed(f func(ctx context.Context, method string) bool) *Handler {
	h.allowed = f
	return h
}

func (h *Handler) methodAllowed(r *http.Request, method string) bool {
	return h.allowed != nil && h.allowed(r.Context(), method)
}

// setStateCookie sets the single use state's carrier: HttpOnly, Secure and
// SameSite=Lax (the return is a cross site top level navigation, on which a
// Strict cookie is not sent), on Path=/api so it rides to the callback.
func (h *Handler) setStateCookie(w http.ResponseWriter, state string, maxAge int) {
	http.SetCookie(w, &http.Cookie{
		Name: StateCookieName, Value: state, Path: "/api", MaxAge: maxAge,
		HttpOnly: true, Secure: h.cookieSecure, SameSite: http.SameSiteLaxMode,
	})
}

// StartRedirect is GET /api/v1/auth/start/{method} for google, microsoft and
// oidc: it files the pending sign in, sets the state cookie and sends the
// browser to Appwrite's provider redirect.
func (h *Handler) StartRedirect(w http.ResponseWriter, r *http.Request) {
	method := r.PathValue("method")
	if _, ok := providerOf(method); !ok {
		httpx.WriteError(w, r, httpx.NotFound("the sign in method "+method+" is not started by navigation"))
		return
	}
	shell, err := shellStartOf(r)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	if !h.methodAllowed(r, method) {
		httpx.WriteError(w, r, httpx.Forbidden("sign in by "+method+" is not allowed for this dealer"))
		return
	}
	if ok, d := h.startByAddress.allow(h.cfg.Trusted.Of(r)); !ok {
		h.limited(w, r, d)
		return
	}
	state, location, err := h.svc.StartRedirect(r.Context(), method, shell)
	switch {
	case errors.Is(err, ErrUnavailable):
		httpx.WriteError(w, r, httpx.Unavailable("sign in is unavailable"))
	case err != nil:
		httpx.WriteError(w, r, err)
	default:
		h.setStateCookie(w, state, stateCookieMaxAge)
		w.Header().Set("Cache-Control", "no-store")
		http.Redirect(w, r, location, http.StatusFound)
	}
}

// StartMagicLink is POST /api/v1/auth/start/magic_link. It answers the same
// body and sets a state cookie of the same shape whether or not a link was
// sent. It carries the sign in route's same site check and the start limits.
func (h *Handler) StartMagicLink(w http.ResponseWriter, r *http.Request) {
	if !h.sameSiteIntent(r) {
		httpx.WriteError(w, r, httpx.Unauthorized(refusedMessage))
		return
	}
	if !h.methodAllowed(r, "magic_link") {
		httpx.WriteError(w, r, httpx.Forbidden("sign in by magic_link is not allowed for this dealer"))
		return
	}
	var body struct {
		Email  string `json:"email"`
		Client string `json:"client"`
	}
	r.Body = http.MaxBytesReader(w, r.Body, 8<<10)
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		httpx.WriteError(w, r, httpx.BadRequest("request body must be {email}"))
		return
	}
	if body.Client != "" {
		httpx.WriteError(w, r, httpx.BadRequest("the magic link is a web sign in method"))
		return
	}
	if ok, d := h.startByAddress.allow(h.cfg.Trusted.Of(r)); !ok {
		h.limited(w, r, d)
		return
	}
	if ok, d := h.startByEmail.allow(strings.ToLower(strings.TrimSpace(body.Email))); !ok {
		h.limited(w, r, d)
		return
	}
	state, err := h.svc.StartMagicLink(r.Context(), body.Email)
	switch {
	case errors.Is(err, ErrUnavailable):
		httpx.WriteError(w, r, httpx.Unavailable("sign in is unavailable"))
	case err != nil:
		httpx.WriteError(w, r, err)
	default:
		h.setStateCookie(w, state, stateCookieMaxAge)
		writeJSON(w, http.StatusOK, map[string]string{"message": magicLinkReply})
	}
}

// Callback is GET /api/v1/auth/callback, where Appwrite sends the browser
// with the user id and one time secret. It answers Referrer-Policy:
// no-referrer, clears the state cookie whatever happens, and redirects at
// once: to the desk with the Strict session cookie set, or to one fixed
// address on any refusal.
func (h *Handler) Callback(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Referrer-Policy", "no-referrer")
	w.Header().Set("Cache-Control", "no-store")
	q := r.URL.Query()
	cookieState := ""
	if c, err := r.Cookie(StateCookieName); err == nil {
		cookieState = c.Value
	}
	h.setStateCookie(w, "", -1)
	if ok, d := h.startByAddress.allow("callback:" + h.cfg.Trusted.Of(r)); !ok {
		h.limited(w, r, d)
		return
	}
	res, err := h.svc.Callback(r.Context(), cookieState, q.Get("state"), q.Get("userId"), q.Get("secret"), q.Get("failure") != "")
	switch {
	case errors.Is(err, ErrRefused):
		if res.ShellRedirect != "" {
			http.Redirect(w, r, res.ShellRedirect, http.StatusSeeOther)
			return
		}
		http.Redirect(w, r, refusedLocation, http.StatusSeeOther)
	case errors.Is(err, ErrUnavailable):
		httpx.WriteError(w, r, httpx.Unavailable("sign in is unavailable"))
	case err != nil:
		httpx.WriteError(w, r, err)
	case res.ShellRedirect != "":
		// A shell never gets a cookie: it gets its one time code at its own
		// return target.
		http.Redirect(w, r, res.ShellRedirect, http.StatusSeeOther)
	default:
		h.setCookie(w, res.SessionID, int(h.svc.set.AbsoluteLimit.Seconds()))
		http.Redirect(w, r, "/", http.StatusSeeOther)
	}
}

// shellStartOf reads a shell's extra start parameters. With no `client`
// parameter the start is a web start and takes no query at all.
func shellStartOf(r *http.Request) (*ShellStart, error) {
	q := r.URL.Query()
	if len(q) == 0 {
		return nil, nil
	}
	if q.Get("client") != "shell" {
		return nil, httpx.BadRequest("unknown query parameter")
	}
	for k := range q {
		switch k {
		case "client", "code_challenge", "code_challenge_method", "return_to":
		default:
			return nil, httpx.BadRequest("unknown query parameter " + k)
		}
	}
	st, err := NewShellStart(q.Get("code_challenge"), q.Get("code_challenge_method"), q.Get("return_to"))
	if err != nil {
		return nil, httpx.BadRequest("the shell start needs an S256 code_challenge and a registered return_to")
	}
	return st, nil
}
