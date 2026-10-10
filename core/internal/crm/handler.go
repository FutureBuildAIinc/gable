// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package crm

import (
	"encoding/json"
	"net/http"
	"strings"

	"github.com/gablelbm/gable/internal/platform/httpx"
	"github.com/google/uuid"
)

// activitiesScope names the list's ordering: created_at then id, newest
// first. A cursor minted for any other ordering is refused (ADR 0001
// section 2).
const activitiesScope = "crm_activities.created_at_id_desc"

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

	mux.HandleFunc("GET /api/v1/customers/{customerId}/activities", guard(h.HandleListActivities))
	mux.HandleFunc("POST /api/v1/customers/{customerId}/activities", guard(h.HandleCreateActivity))
	mux.HandleFunc("GET /api/v1/activities/{id}", guard(h.HandleGetActivity))
	mux.HandleFunc("PUT /api/v1/activities/{id}", guard(h.HandleUpdateActivity))
	mux.HandleFunc("DELETE /api/v1/activities/{id}", guard(h.HandleDeleteActivity))
}

func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}

func pathID(r *http.Request, name, what string) (uuid.UUID, error) {
	id, err := uuid.Parse(r.PathValue(name))
	if err != nil {
		return uuid.Nil, httpx.BadRequest("invalid "+what+" id",
			httpx.FieldError{Field: "id", Message: "must be a UUID"})
	}
	return id, nil
}

func noQuery(r *http.Request) error {
	_, err := httpx.StrictQuery(r)
	return err
}

func writeActivity(w http.ResponseWriter, status int, a *Activity) {
	httpx.WriteRevisionETag(w, a.Revision)
	writeJSON(w, status, a)
}

func cursorError() error {
	return httpx.BadRequest("cursor keyset is malformed",
		httpx.FieldError{Field: "cursor", Message: "cursor keyset is malformed"})
}

// listParams reads the platform's list parameters (cursor, limit, include)
// and the keyset position; the caller has already run StrictQuery.
func listParams(r *http.Request, q map[string][]string) (limit int, after *httpx.Timestamp, afterID uuid.UUID, wantTotal bool, err error) {
	page, err := httpx.ParseListQuery(r, activitiesScope)
	if err != nil {
		return 0, nil, uuid.Nil, false, err
	}
	if vals := q["include"]; len(vals) > 0 {
		if len(vals) > 1 {
			return 0, nil, uuid.Nil, false, httpx.BadRequest("include is repeated",
				httpx.FieldError{Field: "include", Message: "parameter is repeated"})
		}
		set, ierr := httpx.ParseInclude(vals[0])
		if ierr != nil {
			return 0, nil, uuid.Nil, false, ierr
		}
		wantTotal = set.Has(httpx.IncludeTotal)
	}
	if page.Key != nil {
		if len(page.Key) != 2 {
			return 0, nil, uuid.Nil, false, cursorError()
		}
		at, terr := httpx.ParseKeyTime(page.Key[0])
		id, uerr := httpx.ParseKeyUUID(page.Key[1])
		if terr != nil || uerr != nil {
			return 0, nil, uuid.Nil, false, cursorError()
		}
		ts := httpx.TimestampOf(at)
		after, afterID = &ts, id
	}
	return page.Limit, after, afterID, wantTotal, nil
}

func parseListFilter(r *http.Request) (f ListFilter, wantTotal bool, err error) {
	q, err := httpx.StrictQuery(r, "cursor", "limit", "include", "activity_type", "contact_id")
	if err != nil {
		return f, false, err
	}
	limit, at, id, wantTotal, err := listParams(r, q)
	if err != nil {
		return f, false, err
	}
	f.Limit, f.AfterID = limit, id
	if at != nil {
		t := at.Time
		f.AfterTime = &t
	}
	v := &httpx.Validator{}
	if vals := q["activity_type"]; len(vals) > 0 {
		if len(vals) > 1 {
			v.Check(false, "activity_type", "parameter is repeated")
		} else if t, ok := ParseActivityType(vals[0]); ok {
			f.Type = &t
		} else {
			v.Check(false, "activity_type", "must be one of: "+strings.Join(ActivityTypeNames(), ", "))
		}
	}
	if vals := q["contact_id"]; len(vals) > 0 {
		if len(vals) > 1 {
			v.Check(false, "contact_id", "parameter is repeated")
		} else if id, ok := v.UUID("contact_id", &vals[0], true); ok {
			f.ContactID = &id
		}
	}
	if err := v.Err(); err != nil {
		return f, false, err
	}
	return f, wantTotal, nil
}

func (h *Handler) HandleListActivities(w http.ResponseWriter, r *http.Request) {
	customerID, err := pathID(r, "customerId", "customer")
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	f, wantTotal, err := parseListFilter(r)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	items, hasMore, total, err := h.service.List(r.Context(), customerID, f, wantTotal)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	next := ""
	if len(items) > 0 {
		last := items[len(items)-1]
		if next, err = nextCursor(hasMore, last.CreatedAt, last.ID); err != nil {
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

func nextCursor(hasMore bool, created httpx.Timestamp, id uuid.UUID) (string, error) {
	if !hasMore {
		return "", nil
	}
	return httpx.MintCursor(activitiesScope, httpx.FormatKeyTime(created.Time), id.String())
}

func (h *Handler) HandleCreateActivity(w http.ResponseWriter, r *http.Request) {
	if err := noQuery(r); err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	customerID, err := pathID(r, "customerId", "customer")
	if err != nil {
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
	a, err := h.service.Create(r.Context(), customerID, draft)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	w.Header().Set("Location", "/api/v1/activities/"+a.ID.String())
	writeActivity(w, http.StatusCreated, a)
}

func (h *Handler) HandleGetActivity(w http.ResponseWriter, r *http.Request) {
	if err := noQuery(r); err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	id, err := pathID(r, "id", "activity")
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	a, err := h.service.Get(r.Context(), id)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	writeActivity(w, http.StatusOK, a)
}

func (h *Handler) HandleUpdateActivity(w http.ResponseWriter, r *http.Request) {
	if err := noQuery(r); err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	id, err := pathID(r, "id", "activity")
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
	a, err := h.service.Update(r.Context(), id, draft, Precondition{IfMatch: r.Header.Get("If-Match"), Revision: draft.Revision})
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	writeActivity(w, http.StatusOK, a)
}

// HandleDeleteActivity deletes on the client's revision, carried by If-Match
// (a DELETE has no body).
func (h *Handler) HandleDeleteActivity(w http.ResponseWriter, r *http.Request) {
	if err := noQuery(r); err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	id, err := pathID(r, "id", "activity")
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	if err := h.service.Delete(r.Context(), id, Precondition{IfMatch: r.Header.Get("If-Match")}); err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
