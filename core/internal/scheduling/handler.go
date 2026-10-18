// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package scheduling

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"

	"github.com/gablelbm/gable/pkg/httputil"
	"github.com/google/uuid"
)

// Handler serves the dealer side window routes. It mounts behind the role and
// branch wall serve wires (a yard manager's guard), and holds the yard a
// write names to the caller's branch wall the same way the location module
// does: a caller held to branch A cannot define branch B's windows.
type Handler struct {
	svc   *Service
	guard BranchGuard
}

// BranchGuard holds a body-named location to the caller's branch wall;
// *middleware.BranchGuard satisfies it.
type BranchGuard interface {
	CheckPayloadLocation(ctx context.Context, locationID uuid.UUID) error
}

// WithBranchWall attaches the branch wall guard for the yard a body names.
func (h *Handler) WithBranchWall(guard BranchGuard) *Handler {
	h.guard = guard
	return h
}

// NewHandler creates the handler.
func NewHandler(svc *Service) *Handler {
	return &Handler{svc: svc}
}

// RegisterRoutes mounts the window routes behind the caller's guard.
func (h *Handler) RegisterRoutes(mux *http.ServeMux, guard func(http.Handler) http.Handler) {
	mux.Handle("GET /api/v1/scheduling/windows", guard(http.HandlerFunc(h.handleList)))
	mux.Handle("POST /api/v1/scheduling/windows", guard(http.HandlerFunc(h.handleCreate)))
	mux.Handle("GET /api/v1/scheduling/windows/{id}", guard(http.HandlerFunc(h.handleGet)))
	mux.Handle("PUT /api/v1/scheduling/windows/{id}", guard(http.HandlerFunc(h.handleUpdate)))
	mux.Handle("DELETE /api/v1/scheduling/windows/{id}", guard(http.HandlerFunc(h.handleDelete)))
}

func (h *Handler) handleList(w http.ResponseWriter, r *http.Request) {
	var yardID *uuid.UUID
	if raw := r.URL.Query().Get("yard_id"); raw != "" {
		id, err := uuid.Parse(raw)
		if err != nil {
			httputil.RespondError(w, r, "Invalid yard_id", http.StatusBadRequest, err)
			return
		}
		yardID = &id
	}
	windows, err := h.svc.List(r.Context(), yardID, r.URL.Query().Get("kind"))
	if err != nil {
		httputil.RespondError(w, r, "Failed to list windows", http.StatusBadRequest, err)
		return
	}
	writeJSON(w, windows)
}

func (h *Handler) handleCreate(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
	var req WindowRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		httputil.RespondError(w, r, "Invalid request body", http.StatusBadRequest, err)
		return
	}
	if h.guard != nil && req.YardID != nil {
		if err := h.guard.CheckPayloadLocation(r.Context(), *req.YardID); err != nil {
			httputil.RespondError(w, r, "This yard is outside your branch reach", http.StatusForbidden, err)
			return
		}
	}
	window, err := h.svc.Create(r.Context(), req)
	if err != nil {
		httputil.RespondError(w, r, err.Error(), http.StatusBadRequest, err)
		return
	}
	writeJSONStatus(w, http.StatusCreated, window)
}

func (h *Handler) handleGet(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(r.PathValue("id"))
	if err != nil {
		httputil.RespondError(w, r, "Invalid window ID", http.StatusBadRequest, err)
		return
	}
	window, err := h.svc.Get(r.Context(), id)
	if err != nil {
		status := http.StatusInternalServerError
		if errors.Is(err, ErrWindowNotFound) {
			status = http.StatusNotFound
		}
		httputil.RespondError(w, r, "Window not found", status, err)
		return
	}
	writeJSON(w, window)
}

func (h *Handler) handleUpdate(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
	id, err := uuid.Parse(r.PathValue("id"))
	if err != nil {
		httputil.RespondError(w, r, "Invalid window ID", http.StatusBadRequest, err)
		return
	}
	var req WindowRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		httputil.RespondError(w, r, "Invalid request body", http.StatusBadRequest, err)
		return
	}
	window, err := h.svc.Update(r.Context(), id, req)
	if err != nil {
		status := http.StatusBadRequest
		if errors.Is(err, ErrWindowNotFound) {
			status = http.StatusNotFound
		}
		httputil.RespondError(w, r, err.Error(), status, err)
		return
	}
	writeJSON(w, window)
}

func (h *Handler) handleDelete(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(r.PathValue("id"))
	if err != nil {
		httputil.RespondError(w, r, "Invalid window ID", http.StatusBadRequest, err)
		return
	}
	if err := h.svc.Deactivate(r.Context(), id); err != nil {
		status := http.StatusInternalServerError
		if errors.Is(err, ErrWindowNotFound) {
			status = http.StatusNotFound
		}
		httputil.RespondError(w, r, "Failed to deactivate window", status, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func writeJSON(w http.ResponseWriter, data any) {
	writeJSONStatus(w, http.StatusOK, data)
}

func writeJSONStatus(w http.ResponseWriter, status int, data any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(data)
}
