// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package signinmethods

import (
	"encoding/json"
	"net/http"
	"strings"

	"github.com/gablelbm/gable/internal/platform/httpx"
)

// Guards are the role guards the admin routes mount behind: the dealer's
// admin roles alone (admin and owner) read and write the allowlist.
type Guards struct {
	Admin func(http.Handler) http.Handler
}

// Handler serves the auth config, the admin allowlist routes and the start
// route. The config and start routes are public (they answer before any sign
// in; serve.go lists them on the auth layer's public paths).
type Handler struct {
	svc      *Service
	authMode string
}

// NewHandler takes the service and the auth mode the server runs in ("dev"
// or "production"), published by the config route.
func NewHandler(svc *Service, authMode string) *Handler {
	mode := "production"
	if strings.EqualFold(authMode, "dev") {
		mode = "dev"
	}
	return &Handler{svc: svc, authMode: mode}
}

func guarded(g func(http.Handler) http.Handler, h http.HandlerFunc) http.HandlerFunc {
	if g == nil {
		return h
	}
	return func(w http.ResponseWriter, r *http.Request) { g(h).ServeHTTP(w, r) }
}

func (h *Handler) RegisterRoutes(mux *http.ServeMux, g Guards) {
	mux.HandleFunc("GET /api/v1/auth/config", h.GetConfig)
	mux.HandleFunc("POST /api/v1/auth/start/{method}", h.Start)
	mux.HandleFunc("GET /api/v1/auth/methods", guarded(g.Admin, h.GetAllowlist))
	mux.HandleFunc("PUT /api/v1/auth/methods", guarded(g.Admin, h.PutAllowlist))
}

func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}

// GetConfig answers the public auth config: the auth mode and the dealer's
// allowed methods, in the stored order, nothing else.
func (h *Handler) GetConfig(w http.ResponseWriter, r *http.Request) {
	if _, err := httpx.StrictQuery(r); err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	cfg, err := h.svc.Config(r.Context(), h.authMode)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, cfg)
}

// GetAllowlist answers the admin read.
func (h *Handler) GetAllowlist(w http.ResponseWriter, r *http.Request) {
	if _, err := httpx.StrictQuery(r); err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	out, err := h.svc.Allowlist(r.Context())
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

type putMethod struct {
	Method        string  `json:"method"`
	OIDCIssuerURL *string `json:"oidc_issuer_url"`
	OIDCClientID  *string `json:"oidc_client_id"`
	OIDCLabel     *string `json:"oidc_label"`
}

type putRequest struct {
	Methods []putMethod `json:"methods"`
}

// PutAllowlist replaces the allowlist. The order of the request's methods is
// the order the door renders them.
func (h *Handler) PutAllowlist(w http.ResponseWriter, r *http.Request) {
	if _, err := httpx.StrictQuery(r); err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	var req putRequest
	if err := httpx.DecodeJSON(r, &req); err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	in := make([]Input, 0, len(req.Methods))
	for _, m := range req.Methods {
		item := Input{Method: strings.TrimSpace(m.Method)}
		if m.OIDCIssuerURL != nil {
			item.OIDCIssuerURL = strings.TrimSpace(*m.OIDCIssuerURL)
		}
		if m.OIDCClientID != nil {
			item.OIDCClientID = strings.TrimSpace(*m.OIDCClientID)
		}
		if m.OIDCLabel != nil {
			item.OIDCLabel = strings.TrimSpace(*m.OIDCLabel)
		}
		in = append(in, item)
	}
	out, err := h.svc.Update(r.Context(), in)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

// Start is the route a non form method starts from. The answers are plain on
// purpose: a method the dealer does not allow is refused, and a method that
// is allowed but not built on this server yet says so, never a crash.
func (h *Handler) Start(w http.ResponseWriter, r *http.Request) {
	if _, err := httpx.StrictQuery(r); err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	method := r.PathValue("method")
	switch h.svc.Start(r.Context(), method) {
	case StartNotKnown:
		httpx.WriteError(w, r, httpx.NotFound("the sign in method "+method+" is not one this server knows"))
	case StartNotAllowed:
		httpx.WriteError(w, r, httpx.Forbidden("sign in by "+method+" is not allowed for this dealer"))
	case StartNoRoute:
		httpx.WriteError(w, r, httpx.BadRequest("email and password sign in is the form at the door; it has no start route"))
	case StartByNavigation:
		w.Header().Set("Allow", http.MethodGet)
		httpx.WriteError(w, r, httpx.MethodNotAllowed("sign in by "+method+" starts by navigating to this address (GET)"))
	case StartNotImplemented:
		httpx.WriteError(w, r, &httpx.Error{
			Status:  http.StatusNotImplemented,
			Code:    "not_implemented",
			Message: "sign in by " + method + " is not available on this server yet",
			// The message names no table, file or driver: it is written for
			// the client by construction, so WriteError keeps it verbatim
			// instead of the fixed internal one.
			Operator: true,
		})
	}
}
