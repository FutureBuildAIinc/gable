// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package staffauth

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"github.com/gablelbm/gable/internal/platform/httpx"
	"github.com/gablelbm/gable/pkg/middleware"
)

type shellTokensBody struct {
	AccessToken  string   `json:"access_token"`
	RefreshToken string   `json:"refresh_token"`
	ExpiresIn    int      `json:"expires_in"`
	Sub          string   `json:"sub"`
	Roles        []string `json:"roles"`
}

func (h *Handler) writeShellTokens(w http.ResponseWriter, t ShellTokens) {
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, shellTokensBody{
		AccessToken: t.Access, RefreshToken: t.Refresh, ExpiresIn: t.ExpiresIn,
		Sub: t.Identity.Sub.String(), Roles: t.Identity.Roles,
	})
}

func (h *Handler) shellAllowed(w http.ResponseWriter, r *http.Request) bool {
	if ok, d := h.shellByAddress.allow(h.cfg.Trusted.Of(r)); !ok {
		h.limited(w, r, d)
		return false
	}
	return true
}

func (h *Handler) shellFailure(w http.ResponseWriter, r *http.Request, err error) {
	switch {
	case errors.Is(err, ErrRefused):
		httpx.WriteError(w, r, httpx.Unauthorized(refusedMessage))
	case errors.Is(err, ErrUnavailable):
		httpx.WriteError(w, r, httpx.Unavailable("sign in is unavailable"))
	default:
		httpx.WriteError(w, r, err)
	}
}

// ShellRedeem is POST /api/v1/auth/shell/redeem: the one time code and its
// PKCE verifier for the shell's access and refresh tokens.
func (h *Handler) ShellRedeem(w http.ResponseWriter, r *http.Request) {
	if !h.shellAllowed(w, r) {
		return
	}
	var body struct {
		Code         string `json:"code"`
		CodeVerifier string `json:"code_verifier"`
	}
	r.Body = http.MaxBytesReader(w, r.Body, 8<<10)
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		httpx.WriteError(w, r, httpx.BadRequest("request body must be {code, code_verifier}"))
		return
	}
	t, err := h.svc.Redeem(r.Context(), body.Code, body.CodeVerifier)
	if err != nil {
		h.shellFailure(w, r, err)
		return
	}
	h.writeShellTokens(w, t)
}

// ShellRefresh is POST /api/v1/auth/shell/refresh: rotates both tokens.
func (h *Handler) ShellRefresh(w http.ResponseWriter, r *http.Request) {
	if !h.shellAllowed(w, r) {
		return
	}
	var body struct {
		RefreshToken string `json:"refresh_token"`
	}
	r.Body = http.MaxBytesReader(w, r.Body, 8<<10)
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		httpx.WriteError(w, r, httpx.BadRequest("request body must be {refresh_token}"))
		return
	}
	t, err := h.svc.ShellRefresh(r.Context(), body.RefreshToken)
	if err != nil {
		h.shellFailure(w, r, err)
		return
	}
	h.writeShellTokens(w, t)
}

// ShellSignOut is POST /api/v1/auth/shell/signout: revokes the session
// behind the bearer access token. It answers 204 whatever the token held.
func (h *Handler) ShellSignOut(w http.ResponseWriter, r *http.Request) {
	if !h.shellAllowed(w, r) {
		return
	}
	if v, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer "); ok && middleware.IsShellToken(v) {
		h.svc.ShellSignOut(r.Context(), v)
	}
	w.WriteHeader(http.StatusNoContent)
}
