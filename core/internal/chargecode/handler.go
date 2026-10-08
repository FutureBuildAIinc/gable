// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package chargecode

import (
	"encoding/json"
	"net/http"

	"github.com/gablelbm/gable/internal/platform/httpx"
	"github.com/google/uuid"
)

type Handler struct {
	service *Service
}

func NewHandler(service *Service) *Handler { return &Handler{service: service} }

func (h *Handler) RegisterRoutes(mux *http.ServeMux, roleGuard ...func(http.Handler) http.Handler) {
	guard := func(handler http.HandlerFunc) http.HandlerFunc {
		if len(roleGuard) > 0 && roleGuard[0] != nil {
			return func(w http.ResponseWriter, r *http.Request) {
				roleGuard[0](handler).ServeHTTP(w, r)
			}
		}
		return handler
	}
	// The charge codes master is a plain collection, not a growing document
	// list: no cursor, no keyset, the whole (small) table in one array. The
	// strict query guard and the error envelope are the contract's.
	mux.HandleFunc("GET /api/v1/charge-codes", guard(h.HandleList))
	mux.HandleFunc("POST /api/v1/charge-codes", guard(h.HandleCreate))
	mux.HandleFunc("GET /api/v1/charge-codes/{id}", guard(h.HandleGet))
	mux.HandleFunc("PUT /api/v1/charge-codes/{id}", guard(h.HandleUpdate))
}

func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}

func pathID(r *http.Request) (uuid.UUID, error) {
	id, err := uuid.Parse(r.PathValue("id"))
	if err != nil {
		return uuid.Nil, httpx.BadRequest("invalid charge code id",
			httpx.FieldError{Field: "id", Message: "must be a UUID"})
	}
	return id, nil
}

// HandleList answers the whole master, active only unless include_inactive is
// set. is a strict query name of its own (not the platform's include=).
func (h *Handler) HandleList(w http.ResponseWriter, r *http.Request) {
	q, err := httpx.StrictQuery(r, "include_inactive")
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	includeInactive := false
	if vals := q["include_inactive"]; len(vals) > 0 {
		switch vals[0] {
		case "true":
			includeInactive = true
		case "false":
		default:
			httpx.WriteError(w, r, httpx.BadRequest("include_inactive must be true or false",
				httpx.FieldError{Field: "include_inactive", Message: "must be true or false"}))
			return
		}
	}
	codes, err := h.service.List(r.Context(), includeInactive)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, codes)
}

func (h *Handler) HandleCreate(w http.ResponseWriter, r *http.Request) {
	if _, err := httpx.StrictQuery(r); err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	var req Request
	if err := httpx.DecodeJSON(r, &req); err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	draft, err := req.Parse(false)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	c, err := h.service.Create(r.Context(), draft)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	w.Header().Set("Location", "/api/v1/charge-codes/"+c.ID.String())
	httpx.WriteRevisionETag(w, c.Revision)
	writeJSON(w, http.StatusCreated, c)
}

func (h *Handler) HandleGet(w http.ResponseWriter, r *http.Request) {
	if _, err := httpx.StrictQuery(r); err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	id, err := pathID(r)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	c, err := h.service.Get(r.Context(), id)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	httpx.WriteRevisionETag(w, c.Revision)
	writeJSON(w, http.StatusOK, c)
}

func (h *Handler) HandleUpdate(w http.ResponseWriter, r *http.Request) {
	if _, err := httpx.StrictQuery(r); err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	id, err := pathID(r)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	var req Request
	if err := httpx.DecodeJSON(r, &req); err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	draft, err := req.Parse(true)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	c, err := h.service.Update(r.Context(), id, draft, r.Header.Get("If-Match"))
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	httpx.WriteRevisionETag(w, c.Revision)
	writeJSON(w, http.StatusOK, c)
}
