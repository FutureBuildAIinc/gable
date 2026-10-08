// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package matching

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"

	"github.com/gablelbm/gable/pkg/httputil"
	"github.com/gablelbm/gable/pkg/middleware"
	"github.com/google/uuid"
)

// Handler handles PO matching HTTP endpoints.
type Handler struct {
	service *Service
	guard   BranchGuard // optional; nil leaves a path id unchecked (unit tests)
}

// NewHandler creates a new matching handler.
func NewHandler(service *Service) *Handler {
	return &Handler{service: service}
}

// BranchGuard applies the payload branch rule (ADR 0007 section 2.3) to a
// branch a path id addresses. *middleware.BranchGuard satisfies it.
type BranchGuard interface {
	CheckPayloadBranch(ctx context.Context, branch uuid.UUID) error
}

// WithBranchGuard makes the purchase order routes refuse a purchase order of
// a branch the caller may not target. Without it a path id is not checked,
// so serve always sets it.
func (h *Handler) WithBranchGuard(g BranchGuard) *Handler {
	h.guard = g
	return h
}

// RegisterRoutes registers matching API routes.
// roleGuard protects all endpoints; pass middleware.RequireRole("admin","owner","finance") in production.
func (h *Handler) RegisterRoutes(mux *http.ServeMux, roleGuard ...func(http.Handler) http.Handler) {
	guard := func(handler http.HandlerFunc) http.HandlerFunc {
		if len(roleGuard) > 0 && roleGuard[0] != nil {
			return func(w http.ResponseWriter, r *http.Request) {
				roleGuard[0](handler).ServeHTTP(w, r)
			}
		}
		return handler
	}

	mux.HandleFunc("POST /api/v1/matching/run/{po_id}", guard(h.RunMatch))
	mux.HandleFunc("GET /api/v1/matching/results/{po_id}", guard(h.GetMatchResult))
	mux.HandleFunc("GET /api/v1/matching/exceptions", guard(h.ListExceptions))
	mux.HandleFunc("GET /api/v1/matching/config", guard(h.GetConfig))
	mux.HandleFunc("PUT /api/v1/matching/config", guard(h.UpdateConfig))
}

// checkPOBranch holds a purchase order addressed by the path id to the
// caller's branch wall (ADR 0007 section 2.3): the record's branch must be
// one the caller may target. A purchase order that does not exist belongs to
// no branch and passes; the service answers for it. A refusal is a 403 in
// the legacy error shape these unconverted routes carry. It reports whether
// the request may proceed.
func (h *Handler) checkPOBranch(w http.ResponseWriter, r *http.Request, poID uuid.UUID) bool {
	if h.guard == nil {
		return true
	}
	branch, err := h.service.GetPOBranch(r.Context(), poID)
	if err != nil {
		httputil.RespondError(w, r, "branch access lookup failed", http.StatusInternalServerError, err)
		return false
	}
	if branch == nil {
		return true
	}
	if err := h.guard.CheckPayloadBranch(r.Context(), *branch); err != nil {
		if errors.Is(err, middleware.ErrPayloadBranchRefused) {
			httputil.RespondError(w, r, "purchase order is in a branch this caller may not target", http.StatusForbidden, err)
			return false
		}
		httputil.RespondError(w, r, "branch access lookup failed", http.StatusInternalServerError, err)
		return false
	}
	return true
}

func (h *Handler) RunMatch(w http.ResponseWriter, r *http.Request) {
	poID, err := uuid.Parse(r.PathValue("po_id"))
	if err != nil {
		httputil.RespondError(w, r, "Invalid PO ID", http.StatusBadRequest, err)
		return
	}
	if !h.checkPOBranch(w, r, poID) {
		return
	}

	result, err := h.service.RunMatch(r.Context(), poID)
	if err != nil {
		httputil.RespondError(w, r, "failed to run PO match", http.StatusInternalServerError, err)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(result)
}

func (h *Handler) GetMatchResult(w http.ResponseWriter, r *http.Request) {
	poID, err := uuid.Parse(r.PathValue("po_id"))
	if err != nil {
		httputil.RespondError(w, r, "Invalid PO ID", http.StatusBadRequest, err)
		return
	}
	if !h.checkPOBranch(w, r, poID) {
		return
	}

	result, err := h.service.GetMatchResult(r.Context(), poID)
	if err != nil {
		httputil.RespondError(w, r, "match result not found", http.StatusNotFound, err)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(result)
}

func (h *Handler) ListExceptions(w http.ResponseWriter, r *http.Request) {
	exceptions, err := h.service.ListExceptions(r.Context())
	if err != nil {
		httputil.RespondError(w, r, "failed to list match exceptions", http.StatusInternalServerError, err)
		return
	}

	if exceptions == nil {
		exceptions = []MatchException{}
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(exceptions)
}

func (h *Handler) GetConfig(w http.ResponseWriter, r *http.Request) {
	cfg, err := h.service.GetConfig(r.Context())
	if err != nil {
		httputil.RespondError(w, r, "failed to get match config", http.StatusInternalServerError, err)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(cfg)
}

func (h *Handler) UpdateConfig(w http.ResponseWriter, r *http.Request) {
	var req UpdateMatchConfigRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		httputil.RespondError(w, r, "Invalid request body", http.StatusBadRequest, err)
		return
	}

	cfg, err := h.service.UpdateConfig(r.Context(), req)
	if err != nil {
		httputil.RespondError(w, r, "failed to update match config", http.StatusInternalServerError, err)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(cfg)
}
