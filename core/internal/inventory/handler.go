// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package inventory

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"

	"github.com/gablelbm/gable/pkg/httputil"
	"github.com/gablelbm/gable/pkg/middleware"
	"github.com/google/uuid"
)

type Handler struct {
	service *Service
	guard   LocationGuard // optional; nil leaves a payload location unchecked (unit tests)
}

// LocationGuard applies the payload branch rule (ADR 0007 section 2.3) to a
// location id a body names. *middleware.BranchGuard satisfies it.
type LocationGuard interface {
	CheckPayloadLocation(ctx context.Context, locationID uuid.UUID) error
}

// WithBranchGuard makes the stock routes refuse a location whose branch the
// caller may not target. Without it a payload location is not checked, so
// serve always sets it.
func (h *Handler) WithBranchGuard(g LocationGuard) *Handler {
	h.guard = g
	return h
}

// checkLocations answers 403 and returns false when a location the body names
// sits in a branch the caller may not target.
func (h *Handler) checkLocations(w http.ResponseWriter, r *http.Request, field string, ids ...*uuid.UUID) bool {
	if h.guard == nil {
		return true
	}
	for _, id := range ids {
		if id == nil {
			continue
		}
		err := h.guard.CheckPayloadLocation(r.Context(), *id)
		if errors.Is(err, middleware.ErrPayloadBranchRefused) {
			httputil.RespondError(w, r, field+" is in a branch this caller may not target", http.StatusForbidden, err)
			return false
		}
		if err != nil {
			httputil.RespondError(w, r, "branch access lookup failed", http.StatusInternalServerError, err)
			return false
		}
	}
	return true
}

func NewHandler(service *Service) *Handler {
	return &Handler{service: service}
}

func (h *Handler) RegisterRoutes(mux *http.ServeMux, roleGuard ...func(http.Handler) http.Handler) {
	guard := func(handler http.HandlerFunc) http.HandlerFunc {
		if len(roleGuard) > 0 && roleGuard[0] != nil {
			return func(w http.ResponseWriter, r *http.Request) {
				roleGuard[0](handler).ServeHTTP(w, r)
			}
		}
		return handler
	}

	mux.HandleFunc("POST /api/v1/inventory/adjust", guard(h.AdjustStock))
	mux.HandleFunc("POST /api/v1/inventory/transfer", guard(h.MoveStock))
	mux.HandleFunc("GET /api/v1/inventory", guard(h.ListInventory))
}

func (h *Handler) AdjustStock(w http.ResponseWriter, r *http.Request) {
	var req StockAdjustmentRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		httputil.RespondError(w, r, "Invalid input", http.StatusBadRequest, err)
		return
	}

	if !h.checkLocations(w, r, "location_id", req.LocationID) {
		return
	}

	if err := h.service.AdjustStock(r.Context(), req); err != nil {
		slog.Error("AdjustStock failed", "error", err)
		httputil.RespondError(w, r, "Internal Server Error", http.StatusInternalServerError, err)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	w.Write([]byte(`{"status":"ok"}`))
}

func (h *Handler) MoveStock(w http.ResponseWriter, r *http.Request) {
	var req StockMovementRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		httputil.RespondError(w, r, "Invalid input", http.StatusBadRequest, err)
		return
	}

	if !h.checkLocations(w, r, "from_location_id or to_location_id", req.FromLocationID, &req.ToLocationID) {
		return
	}

	if err := h.service.MoveStock(r.Context(), req); err != nil {
		slog.Error("MoveStock failed", "error", err)
		httputil.RespondError(w, r, "Internal Server Error", http.StatusInternalServerError, err)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	w.Write([]byte(`{"status":"ok"}`))
}

func (h *Handler) ListInventory(w http.ResponseWriter, r *http.Request) {
	prodID := r.URL.Query().Get("product_id")
	if prodID == "" {
		httputil.RespondError(w, r, "product_id required", http.StatusBadRequest, nil)
		return
	}

	items, err := h.service.ListByProduct(r.Context(), prodID)
	if err != nil {
		slog.Error("ListByProduct failed", "error", err)
		httputil.RespondError(w, r, "Internal Server Error", http.StatusInternalServerError, err)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(items)
}
