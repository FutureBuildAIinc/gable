// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package taxzone

import (
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"github.com/gablelbm/gable/internal/platform/httpx"
	"github.com/google/uuid"
)

// Handler exposes the zone admin endpoints. Roles admin, owner and finance
// guard every route, as the tax routes do.
type Handler struct {
	repo *Repository
}

func NewHandler(repo *Repository) *Handler { return &Handler{repo: repo} }

func (h *Handler) RegisterRoutes(mux *http.ServeMux, roleGuard func(http.Handler) http.Handler) {
	wrap := func(handler http.HandlerFunc) http.HandlerFunc {
		if roleGuard == nil {
			return handler
		}
		return func(w http.ResponseWriter, r *http.Request) { roleGuard(handler).ServeHTTP(w, r) }
	}
	mux.HandleFunc("GET /api/v1/tax/zones", wrap(h.list))
	mux.HandleFunc("POST /api/v1/tax/zones", wrap(h.create))
	mux.HandleFunc("GET /api/v1/tax/zones/{id}", wrap(h.get))
	mux.HandleFunc("POST /api/v1/tax/zones/{id}/close", wrap(h.close))
	mux.HandleFunc("DELETE /api/v1/tax/zones/{id}", wrap(h.delete))
}

func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}

func pathID(r *http.Request) (uuid.UUID, error) {
	id, err := uuid.Parse(r.PathValue("id"))
	if err != nil {
		return uuid.Nil, httpx.BadRequest("invalid tax zone id",
			httpx.FieldError{Field: "id", Message: "must be a UUID"})
	}
	return id, nil
}

func (h *Handler) list(w http.ResponseWriter, r *http.Request) {
	q, err := httpx.StrictQuery(r, "country", "jurisdiction_type")
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	country := ""
	if vals := q["country"]; len(vals) > 0 {
		country = strings.TrimSpace(vals[0])
	}
	jtype := ""
	if vals := q["jurisdiction_type"]; len(vals) > 0 {
		jtype = strings.TrimSpace(vals[0])
	}
	if jtype != "" && !contains(jurisdictionTypes, strings.ToUpper(jtype)) {
		httpx.WriteError(w, r, httpx.BadRequest("jurisdiction_type must be STA, CTY, CIT, STJ or PROV",
			httpx.FieldError{Field: "jurisdiction_type", Message: "must be STA, CTY, CIT, STJ or PROV"}))
		return
	}
	zones, err := h.repo.List(r.Context(), country, strings.ToUpper(jtype))
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, zones)
}

func (h *Handler) create(w http.ResponseWriter, r *http.Request) {
	if _, err := httpx.StrictQuery(r); err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	var req createRequest
	if err := httpx.DecodeJSON(r, &req); err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	draft, err := req.parse()
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	z, err := h.repo.Create(r.Context(), draft)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	w.Header().Set("Location", "/api/v1/tax/zones/"+z.ID.String())
	writeJSON(w, http.StatusCreated, z)
}

func (h *Handler) get(w http.ResponseWriter, r *http.Request) {
	if _, err := httpx.StrictQuery(r); err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	id, err := pathID(r)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	z, err := h.repo.Get(r.Context(), id)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, z)
}

func (h *Handler) close(w http.ResponseWriter, r *http.Request) {
	if _, err := httpx.StrictQuery(r); err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	id, err := pathID(r)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	var req closeRequest
	if err := httpx.DecodeJSON(r, &req); err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	on, err := parseDate("effective_to", req.EffectiveTo, true)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	if err := h.repo.Close(r.Context(), id, on); err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	z, err := h.repo.Get(r.Context(), id)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, z)
}

func (h *Handler) delete(w http.ResponseWriter, r *http.Request) {
	if _, err := httpx.StrictQuery(r); err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	id, err := pathID(r)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	// A zone a document has used is never deleted, only closed: the sale
	// that used it must stay explainable from the records.
	n, err := h.repo.UsedBy(r.Context(), id)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	if n > 0 {
		err := httpx.Conflict("tax zone in use")
		err.Details = []httpx.FieldError{{Field: "id",
			Message: "documents were taxed under this zone: close it instead of deleting it"}}
		httpx.WriteError(w, r, err)
		return
	}
	if err := h.repo.Delete(r.Context(), id); err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func contains(list []string, v string) bool {
	for _, s := range list {
		if s == v {
			return true
		}
	}
	return false
}

func parseDate(field, s string, required bool) (string, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		if required {
			return "", httpx.BadRequest(field+" is required, as YYYY-MM-DD",
				httpx.FieldError{Field: field, Message: "is required, as YYYY-MM-DD"})
		}
		return "", nil
	}
	if _, err := time.Parse("2006-01-02", s); err != nil {
		return "", httpx.BadRequest(field+" must be YYYY-MM-DD",
			httpx.FieldError{Field: field, Message: "must be YYYY-MM-DD"})
	}
	return s, nil
}
