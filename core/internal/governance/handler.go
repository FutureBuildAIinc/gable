// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package governance

import (
	"encoding/json"
	"net/http"
	"strings"

	"github.com/gablelbm/gable/internal/platform/httpx"
	"github.com/gablelbm/gable/pkg/apps"
	"github.com/google/uuid"
)

// cursorScope names the list's ordering: created_at then id, newest first. A
// cursor minted for any other ordering is refused (ADR 0001 section 2).
const cursorScope = "rfcs.created_at_id_desc"

type Handler struct {
	service *Service
}

func NewHandler(service *Service) *Handler {
	return &Handler{service: service}
}

// RegisterRoutes mounts governance routes. mux is the apps.Router surface:
// the app registry passes a gated router so routes 404 (app_disabled) when
// the governance app is disabled; *http.ServeMux also satisfies it.
func (h *Handler) RegisterRoutes(mux apps.Router, roleGuard ...func(http.Handler) http.Handler) {
	guard := func(handler http.HandlerFunc) http.HandlerFunc {
		if len(roleGuard) > 0 && roleGuard[0] != nil {
			return func(w http.ResponseWriter, r *http.Request) {
				roleGuard[0](handler).ServeHTTP(w, r)
			}
		}
		return handler
	}

	mux.HandleFunc("POST /api/v1/governance/rfcs", guard(h.HandleCreateRFC))
	mux.HandleFunc("GET /api/v1/governance/rfcs", guard(h.HandleListRFCs))
	mux.HandleFunc("GET /api/v1/governance/rfcs/{id}", guard(h.HandleGetRFC))
	mux.HandleFunc("PUT /api/v1/governance/rfcs/{id}", guard(h.HandleUpdateRFC))
	mux.HandleFunc("POST /api/v1/governance/rfcs/{id}/transitions", guard(h.HandleTransition))
}

func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}

func writeRFC(w http.ResponseWriter, status int, rfc *RFC) {
	httpx.WriteRevisionETag(w, rfc.Revision)
	writeJSON(w, status, rfc)
}

// noQuery refuses every query parameter: these routes declare none.
func noQuery(r *http.Request) error {
	_, err := httpx.StrictQuery(r)
	return err
}

func pathID(r *http.Request) (uuid.UUID, error) {
	id, err := uuid.Parse(r.PathValue("id"))
	if err != nil {
		return uuid.Nil, httpx.BadRequest("invalid rfc id",
			httpx.FieldError{Field: "id", Message: "must be a UUID"})
	}
	return id, nil
}

func cursorError() error {
	return httpx.BadRequest("cursor keyset is malformed",
		httpx.FieldError{Field: "cursor", Message: "cursor keyset is malformed"})
}

func (h *Handler) HandleCreateRFC(w http.ResponseWriter, r *http.Request) {
	if err := noQuery(r); err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	var input CreateRFCInput
	if err := httpx.DecodeJSON(r, &input); err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	parsed, err := input.Parse()
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	rfc, err := h.service.DraftRFC(r.Context(), parsed)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	w.Header().Set("Location", "/api/v1/governance/rfcs/"+rfc.ID.String())
	writeRFC(w, http.StatusCreated, rfc)
}

func (h *Handler) HandleListRFCs(w http.ResponseWriter, r *http.Request) {
	q, err := httpx.StrictQuery(r, "cursor", "limit", "include", "status")
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	page, err := httpx.ParseListQuery(r, cursorScope)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	f := ListFilter{Limit: page.Limit}
	v := &httpx.Validator{}
	if vals := q["status"]; len(vals) > 0 {
		if len(vals) > 1 {
			v.Check(false, "status", "parameter is repeated; send a comma separated list")
		} else {
			seen := map[RFCStatus]bool{}
			for _, name := range strings.Split(vals[0], ",") {
				st, ok := ParseStatus(name)
				if !ok {
					v.Check(false, "status", "must be a comma separated list of: "+strings.Join(statusNames, ", "))
					break
				}
				if !seen[st] {
					seen[st] = true
					f.Statuses = append(f.Statuses, st)
				}
			}
		}
	}
	if err := v.Err(); err != nil {
		httpx.WriteError(w, r, err)
		return
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
		f.AfterAt, f.AfterID = &at, id
	}
	wantTotal := false
	if vals := q["include"]; len(vals) > 0 {
		set, ierr := httpx.ParseInclude(vals[0])
		if ierr != nil {
			httpx.WriteError(w, r, ierr)
			return
		}
		wantTotal = set.Has(httpx.IncludeTotal)
	}

	items, hasMore, total, err := h.service.ListRFCs(r.Context(), f, wantTotal)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	next := ""
	if hasMore && len(items) > 0 {
		last := items[len(items)-1]
		next, err = httpx.MintCursor(cursorScope, httpx.FormatKeyTime(last.CreatedAt.Time), last.ID.String())
		if err != nil {
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

func (h *Handler) HandleGetRFC(w http.ResponseWriter, r *http.Request) {
	if err := noQuery(r); err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	id, err := pathID(r)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	rfc, err := h.service.GetRFC(r.Context(), id)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	writeRFC(w, http.StatusOK, rfc)
}

func (h *Handler) HandleUpdateRFC(w http.ResponseWriter, r *http.Request) {
	if err := noQuery(r); err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	id, err := pathID(r)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	var input UpdateRFCInput
	if err := httpx.DecodeJSON(r, &input); err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	parsed, revision, err := input.Parse()
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	rfc, err := h.service.UpdateRFC(r.Context(), id, parsed,
		Precondition{IfMatch: r.Header.Get("If-Match"), Revision: revision})
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	writeRFC(w, http.StatusOK, rfc)
}

// TransitionRequest is the body of POST /rfcs/{id}/transitions.
type TransitionRequest struct {
	To       *string         `json:"to"`
	Revision json.RawMessage `json:"revision"`
}

func (h *Handler) HandleTransition(w http.ResponseWriter, r *http.Request) {
	if err := noQuery(r); err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	id, err := pathID(r)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	var req TransitionRequest
	if err := httpx.DecodeJSON(r, &req); err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	v := &httpx.Validator{}
	var to RFCStatus
	if req.To == nil {
		v.Check(false, "to", "is required")
	} else if st, ok := ParseStatus(*req.To); ok {
		to = st
	} else {
		v.Check(false, "to", "must be one of: "+strings.Join(statusNames, ", "))
	}
	var revision *int64
	if n, ok := v.Int("revision", req.Revision, false); ok {
		v.Check(n >= 1, "revision", "must be a revision number, 1 or more")
		revision = &n
	}
	if err := v.Err(); err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	rfc, err := h.service.Transition(r.Context(), id, to,
		Precondition{IfMatch: r.Header.Get("If-Match"), Revision: revision})
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	writeRFC(w, http.StatusOK, rfc)
}
