// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package portal

import (
	"encoding/json"
	"net/http"
	"time"

	"github.com/gablelbm/gable/pkg/httputil"
	"github.com/gablelbm/gable/pkg/middleware"
	"github.com/google/uuid"
)

// The pro portal's core handlers (Parts 1, 4 and 7 of the parity plan's M35):
// /me, /today, the order timeline and site instructions, the scheduling
// reads and holds, and the capability guard every route names.

// requireCapability resolves the caller's role to its capability set and
// refuses with 403 when the named capability is absent. It is the ONLY guard
// a portal route checks: never a role string, so adding a role or reshaping
// what a role may do changes capabilities.go alone.
func requireCapability(w http.ResponseWriter, r *http.Request, capability string) bool {
	claims := portalClaims(r)
	if claims == nil || !CapabilitiesFor(claims.Role).Has(capability) {
		httputil.RespondError(w, r, "Your role cannot do this", http.StatusForbidden, nil)
		return false
	}
	return true
}

// portalClaims reads the portal claims from the request context.
func portalClaims(r *http.Request) *middleware.PortalClaims {
	claims, _ := r.Context().Value(middleware.PortalClaimsKey).(*middleware.PortalClaims)
	return claims
}

// HandleMe serves GET /api/portal/v1/me: the user, the account, the yards and
// the server resolved capabilities.
func (h *Handler) HandleMe(w http.ResponseWriter, r *http.Request) {
	claims := portalClaims(r)
	if claims == nil {
		httputil.RespondError(w, r, "Unauthorized", http.StatusUnauthorized, nil)
		return
	}
	me, err := h.svc.Me(r.Context(), claims.CustomerID, claims.CustomerUserID, claims.Role)
	if err != nil {
		portalWriteError(w, r, "Failed to load the account", err, http.StatusInternalServerError)
		return
	}
	portalWriteJSON(w, me)
}

// HandleToday serves GET /api/portal/v1/today with the same cache primitives
// the lists carry: ETag, If-None-Match and X-Portal-Latest-Change.
func (h *Handler) HandleToday(w http.ResponseWriter, r *http.Request) {
	claims := portalClaims(r)
	if claims == nil {
		httputil.RespondError(w, r, "Unauthorized", http.StatusUnauthorized, nil)
		return
	}
	var since *time.Time
	if raw := r.URL.Query().Get("since"); raw != "" {
		t, err := time.Parse(time.RFC3339Nano, raw)
		if err != nil {
			httputil.RespondError(w, r, "Invalid since: expected an RFC3339 timestamp", http.StatusBadRequest, err)
			return
		}
		since = &t
	}

	result, err := h.svc.GetTodayBoard(r.Context(), claims.CustomerID, CapabilitiesFor(claims.Role), since)
	if err != nil {
		portalWriteError(w, r, "Failed to load today", err, statusForPortalError(err, http.StatusInternalServerError))
		return
	}

	w.Header().Set("ETag", result.ETag)
	w.Header().Set("Cache-Control", "private, no-cache, must-revalidate")
	if result.LatestUpdatedAt != nil {
		w.Header().Set("X-Portal-Latest-Change", result.LatestUpdatedAt.UTC().Format(time.RFC3339Nano))
	}
	if etagMatches(r.Header.Get("If-None-Match"), result.ETag) {
		w.WriteHeader(http.StatusNotModified)
		return
	}
	portalWriteJSON(w, result.Today)
}

// HandleOrderTimeline serves GET /api/portal/v1/orders/{id}/timeline.
func (h *Handler) HandleOrderTimeline(w http.ResponseWriter, r *http.Request) {
	customerID := getPortalCustomerID(r)
	orderID, err := uuid.Parse(r.PathValue("id"))
	if err != nil {
		httputil.RespondError(w, r, "Invalid order ID", http.StatusBadRequest, err)
		return
	}
	timeline, err := h.svc.GetOrderTimeline(r.Context(), orderID, customerID)
	if err != nil {
		portalWriteError(w, r, "Order not found", err, statusForPortalError(err, http.StatusNotFound))
		return
	}
	portalWriteJSON(w, timeline)
}

// HandleSetDeliveryInstructions serves PUT
// /api/portal/v1/orders/{id}/delivery-instructions (the edit_delivery_
// instructions capability, on an unfulfilled order).
func (h *Handler) HandleSetDeliveryInstructions(w http.ResponseWriter, r *http.Request) {
	if !requireCapability(w, r, CapEditDeliveryInstruction) {
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxBodySize)
	customerID := getPortalCustomerID(r)
	orderID, err := uuid.Parse(r.PathValue("id"))
	if err != nil {
		httputil.RespondError(w, r, "Invalid order ID", http.StatusBadRequest, err)
		return
	}
	var req SetDeliveryInstructionsRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		portalWriteError(w, r, "Invalid request body", err, http.StatusBadRequest)
		return
	}
	updated, err := h.svc.SetOrderSiteInstructions(r.Context(), orderID, customerID, req.Instructions)
	if err != nil {
		portalWriteError(w, r, err.Error(), err, statusForPortalError(err, http.StatusInternalServerError))
		return
	}
	portalWriteJSON(w, updated)
}

// --- Scheduling (PE-08) -------------------------------------------------------

// HandleListScheduleWindows serves GET /api/portal/v1/schedule/windows with
// yard_id required and kind, from and to optional (from defaults to today,
// to fourteen days out, the range capped at 62 days).
func (h *Handler) HandleListScheduleWindows(w http.ResponseWriter, r *http.Request) {
	customerID := getPortalCustomerID(r)
	yardID, err := uuid.Parse(r.URL.Query().Get("yard_id"))
	if err != nil {
		httputil.RespondError(w, r, "Invalid or missing yard_id", http.StatusBadRequest, err)
		return
	}
	from := r.URL.Query().Get("from")
	to := r.URL.Query().Get("to")

	views, err := h.svc.ListOpenScheduleWindows(r.Context(), customerID, yardID, r.URL.Query().Get("kind"), from, to)
	if err != nil {
		portalWriteError(w, r, "Failed to list schedule windows", err, statusForPortalError(err, http.StatusBadRequest))
		return
	}
	portalWriteJSON(w, views)
}

// HandleCreateScheduleHold serves POST /api/portal/v1/schedule/holds: hold a
// window at checkout or on an open order (the schedule capability).
func (h *Handler) HandleCreateScheduleHold(w http.ResponseWriter, r *http.Request) {
	if !requireCapability(w, r, CapSchedule) {
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxBodySize)
	customerID := getPortalCustomerID(r)

	var req ScheduleWindowRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		portalWriteError(w, r, "Invalid request body", err, http.StatusBadRequest)
		return
	}
	if req.WindowID == uuid.Nil || req.Date == "" {
		httputil.RespondError(w, r, "window_id and date are required", http.StatusBadRequest, nil)
		return
	}

	dto, err := h.svc.CreateScheduleHold(r.Context(), customerID, getPortalUserID(r), req)
	if err != nil {
		portalWriteError(w, r, err.Error(), err, statusForPortalError(err, http.StatusInternalServerError))
		return
	}
	portalWriteJSONStatus(w, http.StatusCreated, dto)
}

// HandleGetScheduleHold serves GET /api/portal/v1/schedule/holds/{id}.
func (h *Handler) HandleGetScheduleHold(w http.ResponseWriter, r *http.Request) {
	customerID := getPortalCustomerID(r)
	holdID, err := uuid.Parse(r.PathValue("id"))
	if err != nil {
		httputil.RespondError(w, r, "Invalid hold ID", http.StatusBadRequest, err)
		return
	}
	dto, err := h.svc.GetScheduleHold(r.Context(), customerID, holdID)
	if err != nil {
		portalWriteError(w, r, "Hold not found", err, statusForPortalError(err, http.StatusNotFound))
		return
	}
	portalWriteJSON(w, dto)
}

// HandleChangeScheduleHold serves PUT /api/portal/v1/schedule/holds/{id}.
func (h *Handler) HandleChangeScheduleHold(w http.ResponseWriter, r *http.Request) {
	if !requireCapability(w, r, CapSchedule) {
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxBodySize)
	customerID := getPortalCustomerID(r)
	holdID, err := uuid.Parse(r.PathValue("id"))
	if err != nil {
		httputil.RespondError(w, r, "Invalid hold ID", http.StatusBadRequest, err)
		return
	}
	var req ScheduleWindowRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		portalWriteError(w, r, "Invalid request body", err, http.StatusBadRequest)
		return
	}
	if req.WindowID == uuid.Nil || req.Date == "" {
		httputil.RespondError(w, r, "window_id and date are required", http.StatusBadRequest, nil)
		return
	}

	dto, err := h.svc.ChangeScheduleHold(r.Context(), customerID, getPortalUserID(r), holdID, req)
	if err != nil {
		portalWriteError(w, r, err.Error(), err, statusForPortalError(err, http.StatusInternalServerError))
		return
	}
	portalWriteJSON(w, dto)
}

// HandleReleaseScheduleHold serves DELETE /api/portal/v1/schedule/holds/{id}.
func (h *Handler) HandleReleaseScheduleHold(w http.ResponseWriter, r *http.Request) {
	if !requireCapability(w, r, CapSchedule) {
		return
	}
	customerID := getPortalCustomerID(r)
	holdID, err := uuid.Parse(r.PathValue("id"))
	if err != nil {
		httputil.RespondError(w, r, "Invalid hold ID", http.StatusBadRequest, err)
		return
	}
	if err := h.svc.ReleaseScheduleHold(r.Context(), customerID, holdID); err != nil {
		portalWriteError(w, r, err.Error(), err, statusForPortalError(err, http.StatusInternalServerError))
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
