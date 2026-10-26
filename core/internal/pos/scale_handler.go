// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package pos

// The scale routes (CP-19): the current reading, the weigh in and weigh
// out, the ticket list and its void, the manual weight entry on a line, and
// the closed list of manual reasons. The simulator's control route mounts
// only when the counter was wired with the simulated scale.

import (
	"net/http"

	"github.com/gablelbm/gable/internal/platform/httpx"
	"github.com/gablelbm/gable/internal/scale"
	"github.com/google/uuid"
)

// scaleSim, when set, is the simulated scale behind the wired driver, so the
// demo and the end to end spec drive the deck.
func (h *Handler) WithScaleSimulator(sim *scale.Simulator) *Handler { h.scaleSim = sim; return h }

type weighInRequest struct {
	RegisterID string `json:"register_id"`
	VehicleRef string `json:"vehicle_ref"`
}

// WeighIn is POST /pos/scale/weigh-in.
func (h *Handler) WeighIn(w http.ResponseWriter, r *http.Request) {
	var req weighInRequest
	if err := httpx.DecodeJSON(r, &req); err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	ticket, err := h.service.WeighIn(r.Context(), req.RegisterID, req.VehicleRef, actorFrom(r))
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	w.Header().Set("Location", "/api/v1/pos/scale/tickets/"+ticket.ID.String())
	writeJSON(w, http.StatusCreated, ticket)
}

type weighOutRequest struct {
	SaleID          string  `json:"sale_id"`
	LineID          string  `json:"line_id"`
	WeighInTicketID *string `json:"weigh_in_ticket_id"`
	VehicleRef      string  `json:"vehicle_ref"`
	Revision        *int64  `json:"revision"`
}

// WeighOut is POST /pos/scale/weigh-out: the loaded truck against its weigh
// in, the net on the line, the ticket on both. It answers the sale and the
// weigh out ticket together.
func (h *Handler) WeighOut(w http.ResponseWriter, r *http.Request) {
	var req weighOutRequest
	if err := httpx.DecodeJSON(r, &req); err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	v := &httpx.Validator{}
	saleID, err := uuid.Parse(req.SaleID)
	v.Check(err == nil, "sale_id", "must be a UUID")
	lineID, err2 := uuid.Parse(req.LineID)
	v.Check(err2 == nil, "line_id", "must be a UUID")
	in := WeighOutInput{SaleID: saleID, LineID: lineID, VehicleRef: req.VehicleRef}
	if req.WeighInTicketID != nil {
		ticketID, err3 := uuid.Parse(*req.WeighInTicketID)
		v.Check(err3 == nil, "weigh_in_ticket_id", "must be a UUID")
		in.WeighInTicketID = &ticketID
	}
	if err := v.Err(); err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	sale, ticket, err := h.service.WeighOut(r.Context(), in, r.Header.Get("If-Match"), req.Revision, actorFrom(r))
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	httpx.WriteRevisionETag(w, sale.Revision)
	writeJSON(w, http.StatusOK, map[string]any{"sale": sale, "ticket": ticket})
}

type manualWeightRequest struct {
	WeightLb string  `json:"weight_lb"`
	TareLb   *string `json:"tare_lb"`
	Reason   string  `json:"reason"`
	Note     string  `json:"note"`
	Revision *int64  `json:"revision"`
}

// EnterLineWeight is POST /pos/transactions/{id}/items/{itemId}/weight: the
// manual weight entry with its reason.
func (h *Handler) EnterLineWeight(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(r.PathValue("id"))
	if err != nil {
		httpx.WriteError(w, r, httpx.BadRequest("one or more fields failed validation", httpx.FieldError{Field: "id", Message: "must be a UUID"}))
		return
	}
	itemID, err := uuid.Parse(r.PathValue("itemId"))
	if err != nil {
		httpx.WriteError(w, r, httpx.BadRequest("one or more fields failed validation", httpx.FieldError{Field: "itemId", Message: "must be a UUID"}))
		return
	}
	var req manualWeightRequest
	if err := httpx.DecodeJSON(r, &req); err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	v := &httpx.Validator{}
	weight, err := httpx.ParseQuantity(req.WeightLb)
	v.Check(err == nil, "weight_lb", "must be a decimal of at most four fraction digits")
	in := ManualWeightInput{WeightLb: weight, Reason: req.Reason, Note: req.Note}
	if req.TareLb != nil {
		tare, terr := httpx.ParseQuantity(*req.TareLb)
		v.Check(terr == nil, "tare_lb", "must be a decimal of at most four fraction digits")
		in.TareLb = &tare
	}
	if err := v.Err(); err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	sale, err := h.service.EnterManualWeight(r.Context(), id, itemID, in, r.Header.Get("If-Match"), req.Revision, actorFrom(r))
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	httpx.WriteRevisionETag(w, sale.Revision)
	writeJSON(w, http.StatusOK, sale)
}

// GetScaleStatus is GET /pos/scale.
func (h *Handler) GetScaleStatus(w http.ResponseWriter, r *http.Request) {
	status, err := h.service.ScaleStatus(r.Context(), r.URL.Query().Get("register_id"))
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, status)
}

// ListScaleTickets is GET /pos/scale/tickets.
func (h *Handler) ListScaleTickets(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	tickets, err := h.service.ListScaleTickets(r.Context(), q.Get("register_id"), q.Get("status"), q.Get("vehicle_ref"))
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": tickets})
}

// GetScaleTicket is GET /pos/scale/tickets/{id}.
func (h *Handler) GetScaleTicket(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(r.PathValue("id"))
	if err != nil {
		httpx.WriteError(w, r, httpx.BadRequest("one or more fields failed validation", httpx.FieldError{Field: "id", Message: "must be a UUID"}))
		return
	}
	ticket, err := h.service.GetScaleTicket(r.Context(), id)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	httpx.WriteRevisionETag(w, ticket.Revision)
	writeJSON(w, http.StatusOK, ticket)
}

type voidTicketRequest struct {
	Reason   string `json:"reason"`
	Revision *int64 `json:"revision"`
}

// VoidScaleTicket is POST /pos/scale/tickets/{id}/void.
func (h *Handler) VoidScaleTicket(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(r.PathValue("id"))
	if err != nil {
		httpx.WriteError(w, r, httpx.BadRequest("one or more fields failed validation", httpx.FieldError{Field: "id", Message: "must be a UUID"}))
		return
	}
	var req voidTicketRequest
	if err := httpx.DecodeJSON(r, &req); err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	ticket, err := h.service.VoidScaleTicket(r.Context(), id, req.Reason, r.Header.Get("If-Match"), req.Revision, actorFrom(r))
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	httpx.WriteRevisionETag(w, ticket.Revision)
	writeJSON(w, http.StatusOK, ticket)
}

// ListManualWeightReasons is GET /pos/scale/manual-reasons: the closed list
// the entry dialog offers.
func (h *Handler) ListManualWeightReasons(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"items": ManualWeightReasons})
}

type simulatorReadingRequest struct {
	WeightLb *string `json:"weight_lb"`
	Unit     string  `json:"unit"`
	Motion   *bool   `json:"motion"`
	Status   string  `json:"status"`
}

// DriveScaleSimulator is PUT /pos/scale/simulator, mounted only when the
// wired scale is the simulator: it puts a load on the deck, pins the motion
// flag or forces a status, so the end to end spec walks the dealer's steps
// with no hardware.
func (h *Handler) DriveScaleSimulator(w http.ResponseWriter, r *http.Request) {
	var req simulatorReadingRequest
	if err := httpx.DecodeJSON(r, &req); err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	unit := scale.UnitPound
	if req.Unit != "" {
		unit = scale.Unit(req.Unit)
		if unit != scale.UnitPound && unit != scale.UnitKilogram {
			httpx.WriteError(w, r, httpx.BadRequest("one or more fields failed validation",
				httpx.FieldError{Field: "unit", Message: "is lb or kg"}))
			return
		}
	}
	if req.WeightLb != nil {
		q, err := httpx.ParseQuantity(*req.WeightLb)
		if err != nil {
			httpx.WriteError(w, r, httpx.BadRequest("one or more fields failed validation",
				httpx.FieldError{Field: "weight_lb", Message: "must be a decimal of at most four fraction digits"}))
			return
		}
		// The wire quantity is scale 4; the simulator holds milli.
		h.scaleSim.SetWeightStableMilli(int64(q)/10, unit)
	}
	if req.Motion != nil {
		h.scaleSim.SetMotion(*req.Motion)
	}
	switch req.Status {
	case "":
	case "none":
		h.scaleSim.SetStatus(scale.StatusNone)
	case "over_capacity":
		h.scaleSim.SetStatus(scale.StatusOverCapacity)
	case "under_capacity":
		h.scaleSim.SetStatus(scale.StatusUnderCapacity)
	case "zero_error":
		h.scaleSim.SetStatus(scale.StatusZeroError)
	case "initial_zero_error":
		h.scaleSim.SetStatus(scale.StatusInitialZeroError)
	case "tare_error":
		h.scaleSim.SetStatus(scale.StatusTareError)
	default:
		httpx.WriteError(w, r, httpx.BadRequest("one or more fields failed validation",
			httpx.FieldError{Field: "status", Message: "is not a scale status"}))
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}
