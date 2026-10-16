// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package marketcheck

import (
	"encoding/json"
	"net/http"
	"sync"
	"time"

	"github.com/gablelbm/gable/internal/platform/httpx"
	"github.com/gablelbm/gable/pkg/middleware"
	"github.com/google/uuid"
)

// Handler serves the settings and the run.
type Handler struct {
	svc      *Service
	mu       sync.Mutex
	inflight map[string]bool // callers with a run in progress
}

// NewHandler builds the handler.
func NewHandler(s *Service) *Handler { return &Handler{svc: s, inflight: map[string]bool{}} }

// callerKey names the caller for the one run at a time rule.
func callerKey(r *http.Request) string {
	if c := middleware.ClaimsFromContext(r.Context()); c != nil && c.Subject != "" {
		return "user:" + c.Subject
	}
	return "anonymous"
}

// RegisterRoutes mounts the three routes. settingsGuard walls the Tech Admin
// settings; runGuard walls the run (the pricing rules' walls).
func (h *Handler) RegisterRoutes(mux *http.ServeMux, settingsGuard, runGuard func(http.Handler) http.Handler) {
	wrap := func(g func(http.Handler) http.Handler, f http.HandlerFunc) http.Handler {
		if g == nil {
			return f
		}
		return g(f)
	}
	mux.Handle("GET /api/v1/admin/market-check/settings", wrap(settingsGuard, h.getSettings))
	mux.Handle("PUT /api/v1/admin/market-check/settings", wrap(settingsGuard, h.putSettings))
	mux.Handle("POST /api/v1/pricing/market-check", wrap(runGuard, h.run))
}

func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}

func (h *Handler) getSettings(w http.ResponseWriter, r *http.Request) {
	if _, err := httpx.StrictQuery(r); err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	out, err := h.svc.Settings(r.Context())
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

func (h *Handler) putSettings(w http.ResponseWriter, r *http.Request) {
	if _, err := httpx.StrictQuery(r); err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	var in SettingsInput
	if err := httpx.DecodeJSON(r, &in); err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	out, err := h.svc.SaveSettings(r.Context(), in)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

type runRequest struct {
	ProductIDs []string `json:"product_ids"`
}

func (h *Handler) run(w http.ResponseWriter, r *http.Request) {
	if _, err := httpx.StrictQuery(r); err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	var in runRequest
	if err := httpx.DecodeJSON(r, &in); err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	ids := make([]uuid.UUID, 0, len(in.ProductIDs))
	for _, s := range in.ProductIDs {
		id, err := uuid.Parse(s)
		if err != nil {
			httpx.WriteError(w, r, invalidField("product_ids", "each entry must be a product id"))
			return
		}
		ids = append(ids, id)
	}
	// The run waits on the flow up to one batch timeout, longer than the
	// server's own write timeout: this one response gets a deadline past it.
	_ = http.NewResponseController(w).SetWriteDeadline(time.Now().Add(h.svc.BatchTimeout() + 10*time.Second))
	key := callerKey(r)
	h.mu.Lock()
	if h.inflight[key] {
		h.mu.Unlock()
		httpx.WriteError(w, r, httpx.RateLimited("A market check is already running for you; wait for it to finish"))
		return
	}
	h.inflight[key] = true
	h.mu.Unlock()
	defer func() {
		h.mu.Lock()
		delete(h.inflight, key)
		h.mu.Unlock()
	}()
	out, err := h.svc.Run(r.Context(), ids)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}
