// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package project

import (
	"encoding/json"
	"net/http"
	"strings"

	"github.com/gablelbm/gable/internal/platform/httpx"
	"github.com/gablelbm/gable/pkg/middleware"
	"github.com/google/uuid"
)

// projectsScope names the list's ordering: created_at then id, newest first.
// A cursor minted for any other ordering is refused (ADR 0001 section 2).
const projectsScope = "projects.created_at_id_desc"

type Handler struct {
	service *Service
}

func NewHandler(service *Service) *Handler { return &Handler{service: service} }

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

func writeProject(w http.ResponseWriter, status int, p *Project) {
	httpx.WriteRevisionETag(w, p.Revision)
	writeJSON(w, status, p)
}

func cursorError() error {
	return httpx.BadRequest("cursor keyset is malformed",
		httpx.FieldError{Field: "cursor", Message: "cursor keyset is malformed"})
}

// customerID reads the portal chain's customer: the project routes are the
// portal's own surface and every read and write is scoped to it.
func customerID(r *http.Request) (uuid.UUID, error) {
	claims, ok := r.Context().Value(middleware.PortalClaimsKey).(*middleware.PortalClaims)
	if !ok || claims == nil || claims.CustomerID == uuid.Nil {
		return uuid.Nil, httpx.Unauthorized("the portal chain identifies the customer")
	}
	return claims.CustomerID, nil
}

func listParams(r *http.Request, q map[string][]string) (limit int, after *httpx.Timestamp, afterID uuid.UUID, wantTotal bool, err error) {
	page, err := httpx.ParseListQuery(r, projectsScope)
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
	q, err := httpx.StrictQuery(r, "cursor", "limit", "include", "status")
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
	if vals := q["status"]; len(vals) > 0 {
		if len(vals) > 1 {
			v.Check(false, "status", "parameter is repeated")
		} else if s, ok := ParseProjectStatus(vals[0]); ok {
			f.Status = &s
		} else {
			v.Check(false, "status", "must be one of: "+strings.Join(ProjectStatusNames(), ", "))
		}
	}
	if err := v.Err(); err != nil {
		return f, false, err
	}
	return f, wantTotal, nil
}

// RegisterRoutes registers the project API endpoints behind the portal
// chain's auth middleware.
func (h *Handler) RegisterRoutes(mux *http.ServeMux, authMw func(http.Handler) http.Handler) {
	mux.Handle("GET /api/portal/v1/projects", authMw(http.HandlerFunc(h.HandleListProjects)))
	mux.Handle("GET /api/portal/v1/projects/{id}", authMw(http.HandlerFunc(h.HandleGetProject)))
	mux.Handle("POST /api/portal/v1/projects", authMw(http.HandlerFunc(h.HandleCreateProject)))
	mux.Handle("PUT /api/portal/v1/projects/{id}", authMw(http.HandlerFunc(h.HandleUpdateProject)))
}

func (h *Handler) HandleListProjects(w http.ResponseWriter, r *http.Request) {
	customerID, err := customerID(r)
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
	return httpx.MintCursor(projectsScope, httpx.FormatKeyTime(created.Time), id.String())
}

func (h *Handler) HandleGetProject(w http.ResponseWriter, r *http.Request) {
	customerID, err := customerID(r)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	if err := noQuery(r); err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	id, err := pathID(r, "id", "project")
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	dashboard, err := h.service.Dashboard(r.Context(), id, customerID)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	httpx.WriteRevisionETag(w, dashboard.Project.Revision)
	writeJSON(w, http.StatusOK, dashboard)
}

func (h *Handler) HandleCreateProject(w http.ResponseWriter, r *http.Request) {
	customerID, err := customerID(r)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	if err := noQuery(r); err != nil {
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
	p, err := h.service.Create(r.Context(), customerID, draft)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	w.Header().Set("Location", "/api/portal/v1/projects/"+p.ID.String())
	writeProject(w, http.StatusCreated, p)
}

func (h *Handler) HandleUpdateProject(w http.ResponseWriter, r *http.Request) {
	customerID, err := customerID(r)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	if err := noQuery(r); err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	id, err := pathID(r, "id", "project")
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
	p, err := h.service.Update(r.Context(), id, customerID, draft,
		Precondition{IfMatch: r.Header.Get("If-Match"), Revision: draft.Revision})
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	writeProject(w, http.StatusOK, p)
}
