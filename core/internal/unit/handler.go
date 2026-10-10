// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package unit

import (
	"encoding/json"
	"net/http"
	"strconv"
	"strings"

	"github.com/gablelbm/gable/internal/platform/httpx"
)

// Handler serves the unit catalogue routes (ADR 0006 2.3): the list with
// its dimension and is_active filters, the two reads and the two writes.
type Handler struct {
	service *Service
}

func NewHandler(service *Service) *Handler { return &Handler{service: service} }

// RegisterRoutes adds the catalogue's routes: read for every desk role,
// write for admin and owner. The two guards are composed by the caller.
func (h *Handler) RegisterRoutes(mux *http.ServeMux, read, write func(http.Handler) http.Handler) {
	mux.Handle("GET /api/v1/units", read(http.HandlerFunc(h.handleList)))
	mux.Handle("POST /api/v1/units", write(http.HandlerFunc(h.handleCreate)))
	mux.Handle("GET /api/v1/units/{code}", read(http.HandlerFunc(h.handleGet)))
	mux.Handle("PUT /api/v1/units/{code}", write(http.HandlerFunc(h.handleUpdate)))
}

// unitsOrdering is the list's ordering scope: units.code ascending, the
// ordering the catalogue's bounded code keyset serves.
const unitsOrdering = "units.code_asc"

func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}

func writeUnitError(w http.ResponseWriter, r *http.Request, err error) {
	httpx.WriteError(w, r, err)
}

func cursorError() error {
	return httpx.BadRequest("cursor keyset is malformed",
		httpx.FieldError{Field: "cursor", Message: "cursor keyset is malformed"})
}

// handleList serves GET /api/v1/units: the list envelope of ADR 0001,
// ordered by code, with the dimension and is_active filters.
func (h *Handler) handleList(w http.ResponseWriter, r *http.Request) {
	q, err := httpx.StrictQuery(r, "cursor", "limit", "include", "dimension", "is_active")
	if err != nil {
		writeUnitError(w, r, err)
		return
	}
	page, err := httpx.ParseListQuery(r, unitsOrdering)
	if err != nil {
		writeUnitError(w, r, err)
		return
	}

	f := ListFilter{Limit: page.Limit}
	if vals := q["dimension"]; len(vals) > 0 {
		if len(vals) > 1 {
			writeUnitError(w, r, httpx.BadRequest("dimension parameter is repeated",
				httpx.FieldError{Field: "dimension", Message: "parameter is repeated"}))
			return
		}
		dim, ok := ParseDimension(vals[0])
		if !ok {
			writeUnitError(w, r, httpx.BadRequest("dimension is not one the catalogue holds",
				httpx.FieldError{Field: "dimension", Message: "must be one of: count, length, area, volume, weight, board_measure"}))
			return
		}
		f.Dimension = &dim
	}
	if vals := q["is_active"]; len(vals) > 0 {
		if len(vals) > 1 {
			writeUnitError(w, r, httpx.BadRequest("is_active parameter is repeated",
				httpx.FieldError{Field: "is_active", Message: "parameter is repeated"}))
			return
		}
		b, perr := strconv.ParseBool(vals[0])
		if perr != nil || (vals[0] != "true" && vals[0] != "false") {
			writeUnitError(w, r, httpx.BadRequest("is_active must be true or false",
				httpx.FieldError{Field: "is_active", Message: "must be true or false"}))
			return
		}
		f.IsActive = &b
	}
	if page.Key != nil {
		if len(page.Key) != 1 || strings.TrimSpace(page.Key[0]) == "" {
			writeUnitError(w, r, cursorError())
			return
		}
		after := page.Key[0]
		f.After = &after
	}

	items, more, err := h.service.ListUnits(r.Context(), f)
	if err != nil {
		writeUnitError(w, r, err)
		return
	}
	next := ""
	if more && len(items) > 0 {
		next, err = httpx.MintCursor(unitsOrdering, items[len(items)-1].Code)
		if err != nil {
			writeUnitError(w, r, err)
			return
		}
	}
	opts := []httpx.ListOption{}
	if vals := q["include"]; len(vals) > 0 {
		set, ierr := httpx.ParseInclude(vals[0], httpx.IncludeTotal)
		if ierr != nil {
			writeUnitError(w, r, ierr)
			return
		}
		if set.Has(httpx.IncludeTotal) {
			total, terr := h.service.CountUnits(r.Context(), f)
			if terr != nil {
				writeUnitError(w, r, terr)
				return
			}
			opts = append(opts, httpx.WithTotal(total))
		}
	}
	httpx.WriteList(w, items, next, page.Limit, opts...)
}

// handleGet serves GET /api/v1/units/{code}.
func (h *Handler) handleGet(w http.ResponseWriter, r *http.Request) {
	code := r.PathValue("code")
	if !codeRule.MatchString(code) {
		writeUnitError(w, r, httpx.BadRequest("code is not a unit code",
			httpx.FieldError{Field: "code", Message: "must be one to six capital letters"}))
		return
	}
	u, err := h.service.GetUnit(r.Context(), code)
	if err != nil {
		writeUnitError(w, r, err)
		return
	}
	httpx.WriteRevisionETag(w, u.Revision)
	writeJSON(w, http.StatusOK, u)
}

// handleCreate serves POST /api/v1/units: 201 with Location.
func (h *Handler) handleCreate(w http.ResponseWriter, r *http.Request) {
	draft, err := ParseCreate(r)
	if err != nil {
		writeUnitError(w, r, err)
		return
	}
	u, err := h.service.Create(r.Context(), draft)
	if err != nil {
		writeUnitError(w, r, err)
		return
	}
	w.Header().Set("Location", "/api/v1/units/"+u.Code)
	httpx.WriteRevisionETag(w, u.Revision)
	writeJSON(w, http.StatusCreated, u)
}

// handleUpdate serves PUT /api/v1/units/{code}: the revision precondition
// (If-Match or the body revision), then the editable fields.
func (h *Handler) handleUpdate(w http.ResponseWriter, r *http.Request) {
	code := r.PathValue("code")
	if !codeRule.MatchString(code) {
		writeUnitError(w, r, httpx.BadRequest("code is not a unit code",
			httpx.FieldError{Field: "code", Message: "must be one to six capital letters"}))
		return
	}
	draft, revision, err := ParseUpdate(r)
	if err != nil {
		writeUnitError(w, r, err)
		return
	}
	u, err := h.service.Update(r.Context(), code, draft, r.Header.Get("If-Match"), revision)
	if err != nil {
		writeUnitError(w, r, err)
		return
	}
	httpx.WriteRevisionETag(w, u.Revision)
	writeJSON(w, http.StatusOK, u)
}
