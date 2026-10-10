// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package millwork

import (
	"encoding/json"
	"net/http"
	"strings"

	"github.com/gablelbm/gable/internal/platform/httpx"
	"github.com/gablelbm/gable/pkg/apps"
	"github.com/google/uuid"
)

// optionsScope names the list's ordering: created_at then id, newest first.
// A cursor minted for any other ordering is refused (ADR 0001 section 2).
const optionsScope = "millwork_options.created_at_id_desc"

type Handler struct {
	service *Service
}

func NewHandler(service *Service) *Handler { return &Handler{service: service} }

// RegisterRoutes mounts millwork routes. mux is the apps.Router surface —
// the app registry passes a gated router so routes 404 (app_disabled) when
// the millwork app is disabled; *http.ServeMux also satisfies it.
func (h *Handler) RegisterRoutes(mux apps.Router, roleGuard ...func(http.Handler) http.Handler) {
	guard := func(handler http.HandlerFunc) http.HandlerFunc {
		if len(roleGuard) > 0 && roleGuard[0] != nil {
			return func(w http.ResponseWriter, r *http.Request) {
				roleGuard[0](handler).ServeHTTP(w, r)
			}
		}
		return handler
	}

	mux.HandleFunc("POST /api/v1/millwork/options", guard(h.handleCreateOption))
	mux.HandleFunc("GET /api/v1/millwork/options", guard(h.handleListOptions))
	mux.HandleFunc("GET /api/v1/millwork/options/{id}", guard(h.handleGetOption))
}

func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}

func pathID(r *http.Request) (uuid.UUID, error) {
	id, err := uuid.Parse(r.PathValue("id"))
	if err != nil {
		return uuid.Nil, httpx.BadRequest("invalid millwork option id",
			httpx.FieldError{Field: "id", Message: "must be a UUID"})
	}
	return id, nil
}

func noQuery(r *http.Request) error {
	_, err := httpx.StrictQuery(r)
	return err
}

func cursorError() error {
	return httpx.BadRequest("cursor keyset is malformed",
		httpx.FieldError{Field: "cursor", Message: "cursor keyset is malformed"})
}

func (h *Handler) handleListOptions(w http.ResponseWriter, r *http.Request) {
	q, err := httpx.StrictQuery(r, "cursor", "limit", "include", "category")
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	v := &httpx.Validator{}
	category := ""
	if vals := q["category"]; len(vals) == 0 {
		v.Check(false, "category", "is required")
	} else if len(vals) > 1 {
		v.Check(false, "category", "parameter is repeated")
	} else {
		category = vals[0]
		v.Check(strings.TrimSpace(category) != "", "category", "is required")
	}
	if err := v.Err(); err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	page, err := httpx.ParseListQuery(r, optionsScope)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	f := ListFilter{Limit: page.Limit}
	wantTotal := false
	if vals := q["include"]; len(vals) > 0 {
		if len(vals) > 1 {
			httpx.WriteError(w, r, httpx.BadRequest("include is repeated",
				httpx.FieldError{Field: "include", Message: "parameter is repeated"}))
			return
		}
		set, ierr := httpx.ParseInclude(vals[0])
		if ierr != nil {
			httpx.WriteError(w, r, ierr)
			return
		}
		wantTotal = set.Has(httpx.IncludeTotal)
	}
	if page.Key != nil {
		if len(page.Key) != 2 {
			httpx.WriteError(w, r, cursorError())
			return
		}
		at, terr := httpx.ParseKeyTime(page.Key[0])
		id, uerr := httpx.ParseKeyUUID(page.Key[1])
		if terr != nil || uerr != nil {
			httpx.WriteError(w, r, cursorError())
			return
		}
		t := at
		f.AfterTime, f.AfterID = &t, id
	}
	f.Category = category
	items, hasMore, total, err := h.service.List(r.Context(), f, wantTotal)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	next := ""
	if len(items) > 0 {
		last := items[len(items)-1]
		if next, err = mintNext(hasMore, last.CreatedAt, last.ID); err != nil {
			httpx.WriteError(w, r, err)
			return
		}
	}
	var opts []httpx.ListOption
	if total != nil {
		opts = append(opts, httpx.WithTotal(*total))
	}
	httpx.WriteList(w, items, next, f.Limit, opts...)
}

func mintNext(hasMore bool, created httpx.Timestamp, id uuid.UUID) (string, error) {
	if !hasMore {
		return "", nil
	}
	return httpx.MintCursor(optionsScope, httpx.FormatKeyTime(created.Time), id.String())
}

func (h *Handler) handleGetOption(w http.ResponseWriter, r *http.Request) {
	if err := noQuery(r); err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	id, err := pathID(r)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	o, err := h.service.Get(r.Context(), id)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	httpx.WriteRevisionETag(w, o.Revision)
	writeJSON(w, http.StatusOK, o)
}

func (h *Handler) handleCreateOption(w http.ResponseWriter, r *http.Request) {
	if err := noQuery(r); err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	var req Request
	if err := httpx.DecodeJSON(r, &req); err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	draft, err := req.Parse()
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	o, err := h.service.Create(r.Context(), draft)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	w.Header().Set("Location", "/api/v1/millwork/options/"+o.ID.String())
	httpx.WriteRevisionETag(w, o.Revision)
	writeJSON(w, http.StatusCreated, o)
}
