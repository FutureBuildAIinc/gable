// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package staff

import (
	"encoding/json"
	"net/http"

	"github.com/gablelbm/gable/internal/platform/httpx"
	"github.com/gablelbm/gable/pkg/middleware"
	"github.com/google/uuid"
)

// cursorScope names the staff list's ordering: created_at then id, newest
// first. moduleCursorScope names the module catalog's: id ascending. A
// cursor minted for any other ordering is refused (ADR 0001 section 2).
const (
	cursorScope       = "staff.created_at_id_desc"
	moduleCursorScope = "admin_modules.id_asc"
)

type Handler struct {
	svc *Service
}

func NewHandler(svc *Service) *Handler {
	return &Handler{svc: svc}
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeStaff(w http.ResponseWriter, status int, st *Staff) {
	httpx.WriteRevisionETag(w, st.Revision)
	writeJSON(w, status, st)
}

func writeModule(w http.ResponseWriter, status int, m *Module) {
	httpx.WriteRevisionETag(w, m.Revision)
	writeJSON(w, status, m)
}

// noQuery refuses every query parameter: these routes declare none.
func noQuery(r *http.Request) error {
	_, err := httpx.StrictQuery(r)
	return err
}

// pathID reads the {id} path value; a malformed one is a 400 naming id.
func pathID(r *http.Request) (uuid.UUID, error) {
	id, err := uuid.Parse(r.PathValue("id"))
	if err != nil {
		return uuid.Nil, httpx.BadRequest("invalid staff id",
			httpx.FieldError{Field: "id", Message: "must be a UUID"})
	}
	return id, nil
}

// --- Staff CRUD ---

func (h *Handler) ListStaff(w http.ResponseWriter, r *http.Request) {
	q, err := httpx.StrictQuery(r, "cursor", "limit", "include", "active")
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
	if vals := q["active"]; len(vals) > 0 {
		v := &httpx.Validator{}
		switch vals[0] {
		case "true":
			b := true
			f.Active = &b
		case "false":
			b := false
			f.Active = &b
		default:
			v.Check(false, "active", "must be true or false")
		}
		if err := v.Err(); err != nil {
			httpx.WriteError(w, r, err)
			return
		}
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

	items, hasMore, total, err := h.svc.ListStaff(r.Context(), f, wantTotal)
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

func cursorError() error {
	return httpx.BadRequest("cursor keyset is malformed",
		httpx.FieldError{Field: "cursor", Message: "cursor keyset is malformed"})
}

func (h *Handler) GetStaff(w http.ResponseWriter, r *http.Request) {
	if err := noQuery(r); err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	id, err := pathID(r)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	st, err := h.svc.GetStaff(r.Context(), id)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	writeStaff(w, http.StatusOK, st)
}

func (h *Handler) CreateStaff(w http.ResponseWriter, r *http.Request) {
	if err := noQuery(r); err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	var in CreateStaffInput
	if err := httpx.DecodeJSON(r, &in); err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	parsed, err := in.Parse()
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	st, err := h.svc.CreateStaff(r.Context(), parsed)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	w.Header().Set("Location", "/api/v1/admin/staff/"+st.ID.String())
	writeStaff(w, http.StatusCreated, st)
}

func (h *Handler) UpdateStaff(w http.ResponseWriter, r *http.Request) {
	if err := noQuery(r); err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	id, err := pathID(r)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	var in UpdateStaffInput
	if err := httpx.DecodeJSON(r, &in); err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	parsed, revision, err := in.Parse()
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	st, err := h.svc.UpdateStaff(r.Context(), id, parsed,
		Precondition{IfMatch: r.Header.Get("If-Match"), Revision: revision})
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	writeStaff(w, http.StatusOK, st)
}

// --- Module grants ---

func (h *Handler) GrantModule(w http.ResponseWriter, r *http.Request) {
	if err := noQuery(r); err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	id, err := pathID(r)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	var req GrantModuleInput
	if err := httpx.DecodeJSON(r, &req); err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	moduleID, revision, err := req.Parse()
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	st, err := h.svc.GrantModule(r.Context(), id, moduleID, requesterSub(r),
		Precondition{IfMatch: r.Header.Get("If-Match"), Revision: revision})
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	writeStaff(w, http.StatusOK, st)
}

func (h *Handler) RevokeModule(w http.ResponseWriter, r *http.Request) {
	if err := noQuery(r); err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	id, err := pathID(r)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	moduleID := r.PathValue("module_id")
	if !IsKnownModule(moduleID) {
		httpx.WriteError(w, r, httpx.BadRequest("unknown module",
			httpx.FieldError{Field: "module_id", Message: "must be one of: " + moduleIDsJoined()}))
		return
	}
	st, err := h.svc.RevokeModule(r.Context(), id, moduleID,
		Precondition{IfMatch: r.Header.Get("If-Match")})
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	writeStaff(w, http.StatusOK, st)
}

func moduleIDsJoined() string {
	ids := ""
	for i, id := range KnownModuleIDs() {
		if i > 0 {
			ids += ", "
		}
		ids += id
	}
	return ids
}

// --- Global module enable flag ---

func (h *Handler) ListModules(w http.ResponseWriter, r *http.Request) {
	q, err := httpx.StrictQuery(r, "cursor", "limit", "include")
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	page, err := httpx.ParseListQuery(r, moduleCursorScope)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	// include is declared, so it is honored: total counts the whole catalog,
	// like every list route (ADR 0001 section 1).
	var opts []httpx.ListOption
	if vals := q["include"]; len(vals) > 0 {
		set, ierr := httpx.ParseInclude(vals[0])
		if ierr != nil {
			httpx.WriteError(w, r, ierr)
			return
		}
		if set.Has(httpx.IncludeTotal) {
			opts = append(opts, httpx.WithTotal(int64(len(knownModules))))
		}
	}
	mods, err := h.svc.ListModules(r.Context())
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	// The catalog is small and ordered by id; the envelope's paging is a
	// slice of it, and the cursor resumes after the last id served.
	start := 0
	if len(page.Key) == 1 {
		for i, m := range mods {
			if m.ID > page.Key[0] {
				start = i
				break
			}
			start = i + 1
		}
	} else if len(page.Key) > 1 {
		httpx.WriteError(w, r, httpx.BadRequest("cursor keyset is malformed",
			httpx.FieldError{Field: "cursor", Message: "cursor keyset is malformed"}))
		return
	}
	end := start + page.Limit
	if end > len(mods) {
		end = len(mods)
	}
	items := mods[start:end]
	next := ""
	if end < len(mods) && len(items) > 0 {
		next, err = httpx.MintCursor(moduleCursorScope, items[len(items)-1].ID)
		if err != nil {
			httpx.WriteError(w, r, err)
			return
		}
	}
	httpx.WriteList(w, items, next, page.Limit, opts...)
}

func (h *Handler) SetModuleEnabled(w http.ResponseWriter, r *http.Request) {
	if err := noQuery(r); err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	moduleID := r.PathValue("id")
	var req SetModuleEnabledInput
	if err := httpx.DecodeJSON(r, &req); err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	enabled, revision, err := req.Parse()
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	m, err := h.svc.SetModuleEnabled(r.Context(), moduleID, enabled,
		Precondition{IfMatch: r.Header.Get("If-Match"), Revision: revision})
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	writeModule(w, http.StatusOK, m)
}

// requesterSub returns the JWT subject of the calling admin for grant
// attribution, the machine key's id prefixed "key:" when a key made the call
// (the same principal the idempotency layer keys on, so one caller reads one
// way everywhere), or "" in dev mode (no auth claims).
func requesterSub(r *http.Request) string {
	if claims := middleware.ClaimsFromContext(r.Context()); claims != nil {
		if claims.Subject != "" {
			return claims.Subject
		}
		return claims.Email
	}
	if id, ok := middleware.KeyIDFromContext(r.Context()); ok {
		return "key:" + id
	}
	return ""
}
