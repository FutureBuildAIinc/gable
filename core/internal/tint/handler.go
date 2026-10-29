// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package tint

import (
	"encoding/json"
	"net/http"

	"github.com/gablelbm/gable/internal/platform/httpx"
	"github.com/google/uuid"
)

// Handler serves the tint catalogue routes: reads for every desk role (the
// counter needs them mid sale), writes for admin and owner. The two guards
// are composed by the caller.
type Handler struct {
	service *Service
}

func NewHandler(service *Service) *Handler { return &Handler{service: service} }

// RegisterRoutes adds the catalogue's routes under /api/v1/tint. The counter
// guard is the counter's own write: the dialogue offers saving a colour to the
// counter, so the cashier may write a saved colour (the branch wall holds it
// to the customer it names), while the catalogue itself stays with admin and
// owner.
func (h *Handler) RegisterRoutes(mux *http.ServeMux, read, write, counter func(http.Handler) http.Handler) {
	mux.Handle("GET /api/v1/tint/systems", read(http.HandlerFunc(h.handleListSystems)))
	mux.Handle("POST /api/v1/tint/systems", write(http.HandlerFunc(h.handleCreateSystem)))
	mux.Handle("GET /api/v1/tint/bases", read(http.HandlerFunc(h.handleListBases)))
	mux.Handle("POST /api/v1/tint/bases", write(http.HandlerFunc(h.handleCreateBase)))
	mux.Handle("GET /api/v1/tint/colorants", read(http.HandlerFunc(h.handleListColorants)))
	mux.Handle("POST /api/v1/tint/colorants", write(http.HandlerFunc(h.handleCreateColorant)))
	mux.Handle("GET /api/v1/tint/colors", read(http.HandlerFunc(h.handleListColors)))
	mux.Handle("POST /api/v1/tint/colors", write(http.HandlerFunc(h.handleCreateColor)))
	mux.Handle("GET /api/v1/tint/formulas", read(http.HandlerFunc(h.handleListFormulas)))
	mux.Handle("POST /api/v1/tint/formulas", write(http.HandlerFunc(h.handleCreateFormula)))
	mux.Handle("GET /api/v1/tint/formulas/{id}", read(http.HandlerFunc(h.handleGetFormula)))
	mux.Handle("POST /api/v1/tint/formulas/{id}/rescale", read(http.HandlerFunc(h.handleRescale)))
	mux.Handle("GET /api/v1/tint/saved-colors", read(http.HandlerFunc(h.handleListSavedColors)))
	mux.Handle("POST /api/v1/tint/saved-colors", counter(http.HandlerFunc(h.handleCreateSavedColor)))
	mux.Handle("DELETE /api/v1/tint/saved-colors/{id}", write(http.HandlerFunc(h.handleDeleteSavedColor)))
	mux.Handle("GET /api/v1/tint/charge", read(http.HandlerFunc(h.handleCharge)))
}

func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}

func uuidParam(r *http.Request, name string) (uuid.UUID, bool) {
	raw := r.URL.Query().Get(name)
	if raw == "" {
		return uuid.Nil, false
	}
	id, err := uuid.Parse(raw)
	if err != nil {
		return uuid.Nil, false
	}
	return id, true
}

func (h *Handler) handleListSystems(w http.ResponseWriter, r *http.Request) {
	if _, err := httpx.StrictQuery(r); err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	items, err := h.service.ListSystems(r.Context())
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	httpx.WriteList(w, items, "", 200)
}

func (h *Handler) handleCreateSystem(w http.ResponseWriter, r *http.Request) {
	var req systemRequest
	if err := httpx.DecodeJSON(r, &req); err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	draft, err := req.parse()
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	out, err := h.service.CreateSystem(r.Context(), draft)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	w.Header().Set("Location", "/api/v1/tint/systems/"+out.Code)
	writeJSON(w, http.StatusCreated, out)
}

func (h *Handler) handleListBases(w http.ResponseWriter, r *http.Request) {
	if _, err := httpx.StrictQuery(r, "tint_system_id", "product_id"); err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	systemID, _ := uuidParam(r, "tint_system_id")
	productID, _ := uuidParam(r, "product_id")
	items, err := h.service.ListBases(r.Context(), nilOrID(systemID), nilOrID(productID))
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	httpx.WriteList(w, items, "", 200)
}

func nilOrID(id uuid.UUID) *uuid.UUID {
	if id == uuid.Nil {
		return nil
	}
	return &id
}

func (h *Handler) handleCreateBase(w http.ResponseWriter, r *http.Request) {
	var req baseRequest
	if err := httpx.DecodeJSON(r, &req); err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	draft, err := req.parse()
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	out, err := h.service.CreateBase(r.Context(), draft)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	w.Header().Set("Location", "/api/v1/tint/bases/"+out.Code)
	writeJSON(w, http.StatusCreated, out)
}

func (h *Handler) handleListColorants(w http.ResponseWriter, r *http.Request) {
	if _, err := httpx.StrictQuery(r, "tint_system_id"); err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	systemID, _ := uuidParam(r, "tint_system_id")
	items, err := h.service.ListColorants(r.Context(), nilOrID(systemID))
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	httpx.WriteList(w, items, "", 200)
}

func (h *Handler) handleCreateColorant(w http.ResponseWriter, r *http.Request) {
	var req colorantRequest
	if err := httpx.DecodeJSON(r, &req); err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	draft, err := req.parse()
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	out, err := h.service.CreateColorant(r.Context(), draft)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, out)
}

func (h *Handler) handleListColors(w http.ResponseWriter, r *http.Request) {
	if _, err := httpx.StrictQuery(r, "tint_system_id", "q"); err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	systemID, _ := uuidParam(r, "tint_system_id")
	items, err := h.service.ListColors(r.Context(), nilOrID(systemID), r.URL.Query().Get("q"))
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	httpx.WriteList(w, items, "", 200)
}

func (h *Handler) handleCreateColor(w http.ResponseWriter, r *http.Request) {
	var req colorRequest
	if err := httpx.DecodeJSON(r, &req); err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	draft, err := req.parse()
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	out, err := h.service.CreateColor(r.Context(), draft)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, out)
}

func (h *Handler) handleListFormulas(w http.ResponseWriter, r *http.Request) {
	if _, err := httpx.StrictQuery(r, "color_id", "base_id", "container_size_code"); err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	colorID, _ := uuidParam(r, "color_id")
	baseID, _ := uuidParam(r, "base_id")
	items, err := h.service.ListFormulas(r.Context(), nilOrID(colorID), nilOrID(baseID),
		r.URL.Query().Get("container_size_code"))
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	httpx.WriteList(w, items, "", 200)
}

func (h *Handler) handleCreateFormula(w http.ResponseWriter, r *http.Request) {
	var req formulaRequest
	if err := httpx.DecodeJSON(r, &req); err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	draft, err := req.parse()
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	out, err := h.service.CreateFormula(r.Context(), draft)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	w.Header().Set("Location", "/api/v1/tint/formulas/"+out.ID.String())
	writeJSON(w, http.StatusCreated, out)
}

func (h *Handler) handleGetFormula(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(r.PathValue("id"))
	if err != nil {
		httpx.WriteError(w, r, httpx.BadRequest("id must be a UUID"))
		return
	}
	out, err := h.service.GetFormula(r.Context(), id)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

// rescaleView is the rescale's answer: the source formula, the rescaled
// components, and whether any of them was rounded (the counter's cue that
// the value is a derived approximation it may override).
type rescaleView struct {
	Formula        Formula    `json:"formula"`
	TargetVolumeOZ int        `json:"target_volume_oz"`
	Components     []Rescaled `json:"components"`
	AnyRounded     bool       `json:"any_rounded"`
}

func (h *Handler) handleRescale(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(r.PathValue("id"))
	if err != nil {
		httpx.WriteError(w, r, httpx.BadRequest("id must be a UUID"))
		return
	}
	var req rescaleRequest
	if err := httpx.DecodeJSON(r, &req); err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	target, err := req.parse()
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	f, comps, rounded, err := h.service.RescaleFormula(r.Context(), id, target)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, rescaleView{Formula: *f, TargetVolumeOZ: target,
		Components: comps, AnyRounded: rounded})
}

func (h *Handler) handleListSavedColors(w http.ResponseWriter, r *http.Request) {
	if _, err := httpx.StrictQuery(r, "customer_id", "job_note"); err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	customerID, _ := uuidParam(r, "customer_id")
	items, err := h.service.ListSavedColors(r.Context(), nilOrID(customerID), r.URL.Query().Get("job_note"))
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	httpx.WriteList(w, items, "", 200)
}

func (h *Handler) handleCreateSavedColor(w http.ResponseWriter, r *http.Request) {
	var req savedColorRequest
	if err := httpx.DecodeJSON(r, &req); err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	draft, err := req.parse()
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	out, err := h.service.CreateSavedColor(r.Context(), draft)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, out)
}

func (h *Handler) handleDeleteSavedColor(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(r.PathValue("id"))
	if err != nil {
		httpx.WriteError(w, r, httpx.BadRequest("id must be a UUID"))
		return
	}
	if err := h.service.DeleteSavedColor(r.Context(), id); err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *Handler) handleCharge(w http.ResponseWriter, r *http.Request) {
	if _, err := httpx.StrictQuery(r); err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	out, err := h.service.Charge(r.Context())
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}
