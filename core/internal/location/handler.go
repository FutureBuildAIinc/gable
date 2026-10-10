// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package location

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"time"

	"github.com/gablelbm/gable/internal/platform/httpx"
	"github.com/gablelbm/gable/pkg/httputil"
	"github.com/gablelbm/gable/pkg/middleware"
	"github.com/google/uuid"
)

type Handler struct {
	service     *Service
	userRepo    UserRepository
	adminGuards []func(http.Handler) http.Handler // applied to admin-only routes
	guard       LocationGuard                     // optional; see WithBranchWall
	branchMw    func(http.Handler) http.Handler   // optional; see WithBranchWall
	keyWall     *middleware.KeyBranchWall         // optional; see WithKeyBranchWall
}

// NewHandler constructs the location handler. userRepo and adminGuards may be
// nil; the handler will fall back to plain authenticated access in that case
// (useful for legacy callers and tests).
// LocationGuard applies the payload branch rule (ADR 0007 section 2.3) to a
// location or branch a request names, in the body or behind a path id.
// *middleware.BranchGuard satisfies it.
type LocationGuard interface {
	CheckPayloadLocation(ctx context.Context, locationID uuid.UUID) error
	CheckPayloadBranch(ctx context.Context, branch uuid.UUID) error
}

// WithBranchWall puts the branch wall on the routes that reach across a
// branch's tree (ADR 0007 section 2.3): POST /api/v1/locations and the two
// by-id reads, GET /api/v1/locations/{id} and GET /api/v1/branches/{id}/tree,
// run behind branchMw after their role guard; a parent_id must sit in a
// branch the caller may target; a read of a location or tree the caller may
// not target is a 403; and a BRANCH may be created by an administrator or
// owner only. Without it nothing is branch scoped, so serve always sets it.
func (h *Handler) WithBranchWall(g LocationGuard, branchMw func(http.Handler) http.Handler) *Handler {
	h.guard, h.branchMw = g, branchMw
	return h
}

// WithKeyBranchWall puts the key branch wall on the routes that mount no
// branch middleware (ADR 0007 section 5.5): the by id location writes, the
// branch directory verbs and the user grant routes hold a branch bound key
// to its pin, refusing it another branch with the key.branch_refused row.
// The branch reads the wall narrows are /branches/{id}/users (refused for
// another branch) and /users/{sub}/branches (filtered to the pin), and the
// known users list (/users, every branch's subs) refuses a bound key
// outright. Without it nothing changes, so serve always sets it.
func (h *Handler) WithKeyBranchWall(w *middleware.KeyBranchWall) *Handler {
	h.keyWall = w
	return h
}

func NewHandler(service *Service, userRepo UserRepository, adminGuards ...func(http.Handler) http.Handler) *Handler {
	return &Handler{
		service:     service,
		userRepo:    userRepo,
		adminGuards: adminGuards,
	}
}

// RegisterRoutes attaches all location, branch, and user-location endpoints
// to the supplied mux. The first variadic guard is applied to non-admin
// endpoints (existing behavior); admin-only endpoints use h.adminGuards.
func (h *Handler) RegisterRoutes(mux *http.ServeMux, roleGuard ...func(http.Handler) http.Handler) {
	guard := func(handler http.HandlerFunc) http.HandlerFunc {
		if len(roleGuard) > 0 && roleGuard[0] != nil {
			return func(w http.ResponseWriter, r *http.Request) {
				roleGuard[0](handler).ServeHTTP(w, r)
			}
		}
		return handler
	}
	adminGuard := func(handler http.HandlerFunc) http.HandlerFunc {
		// Compose admin guards if present; otherwise fall back to roleGuard.
		if len(h.adminGuards) == 0 {
			return guard(handler)
		}
		var h2 http.Handler = handler
		for i := len(h.adminGuards) - 1; i >= 0; i-- {
			if h.adminGuards[i] != nil {
				h2 = h.adminGuards[i](h2)
			}
		}
		return h2.ServeHTTP
	}

	// The key branch wall on the routes that mount no branch middleware: the
	// by id location writes hold the row's branch to a bound key's pin, the
	// directory create refuses one outright, the directory writes and the
	// branch users read hold the path branch to it, and the grant routes hold
	// the body or path branch to it (ADR 0007 section 5.5).
	wall := func(mw func(http.Handler) http.Handler) func(http.HandlerFunc) http.HandlerFunc {
		if mw == nil || h.keyWall == nil {
			return func(hf http.HandlerFunc) http.HandlerFunc { return hf }
		}
		return func(hf http.HandlerFunc) http.HandlerFunc { return mw(hf).ServeHTTP }
	}
	locRowWall := wall(h.keyWall.LocationRowBranch())
	branchIdWall := wall(h.keyWall.NamedBranch("id"))
	bodyBranchWall := wall(h.keyWall.BodyBranch())
	branchIdPathWall := wall(h.keyWall.NamedBranch("branch_id"))
	directoryWall := wall(h.keyWall.RefuseBound("the branch directory is outside a branch bound key"))
	knownUsersWall := wall(h.keyWall.RefuseBound("the known users list spans every branch"))

	// Legacy / shared location endpoints.
	create := http.HandlerFunc(h.CreateLocation)
	read := http.HandlerFunc(h.GetLocation)
	list := http.HandlerFunc(h.ListLocations)
	if h.branchMw != nil {
		create = h.branchMw(create).ServeHTTP
		read = h.branchMw(read).ServeHTTP
		list = h.branchMw(list).ServeHTTP
	}
	mux.HandleFunc("POST /api/v1/locations", guard(create))
	mux.HandleFunc("GET /api/v1/locations", guard(list))
	mux.HandleFunc("GET /api/v1/locations/{id}", guard(read))
	mux.HandleFunc("PUT /api/v1/locations/{id}", adminGuard(locRowWall(h.UpdateLocation)))
	mux.HandleFunc("DELETE /api/v1/locations/{id}", adminGuard(locRowWall(h.DeleteLocation)))

	// Branch CRUD.
	mux.HandleFunc("GET /api/v1/branches", guard(h.ListBranches))
	mux.HandleFunc("POST /api/v1/branches", adminGuard(directoryWall(h.CreateBranch)))
	mux.HandleFunc("GET /api/v1/branches/{id}", guard(h.GetBranch))
	mux.HandleFunc("PUT /api/v1/branches/{id}", adminGuard(branchIdWall(h.UpdateBranch)))
	mux.HandleFunc("DELETE /api/v1/branches/{id}", adminGuard(branchIdWall(h.DeleteBranch)))
	tree := http.HandlerFunc(h.GetBranchTree)
	if h.branchMw != nil {
		tree = h.branchMw(tree).ServeHTTP
	}
	mux.HandleFunc("GET /api/v1/branches/{id}/tree", guard(tree))

	// User-branch grants.
	if h.userRepo != nil {
		mux.HandleFunc("GET /api/v1/me/branches", guard(h.ListMyBranches))
		mux.HandleFunc("GET /api/v1/users", adminGuard(knownUsersWall(h.ListKnownUsers)))
		mux.HandleFunc("GET /api/v1/users/{sub}/branches", adminGuard(h.ListUserBranches))
		mux.HandleFunc("POST /api/v1/users/{sub}/branches", adminGuard(bodyBranchWall(h.GrantUserBranch)))
		mux.HandleFunc("DELETE /api/v1/users/{sub}/branches/{branch_id}", adminGuard(branchIdPathWall(h.RevokeUserBranch)))
		mux.HandleFunc("PUT /api/v1/users/{sub}/home-branch", adminGuard(bodyBranchWall(h.SetHomeBranch)))
		mux.HandleFunc("GET /api/v1/branches/{id}/users", adminGuard(branchIdWall(h.ListBranchUsers)))
	}
}

// ---------- location endpoints ----------

// createLocationRequest is the create payload: the wire fields as strings and
// pointers, decoded strictly (an unknown field is a 400, never a silent
// no-op), with `active` as a *bool so "not mentioned" (true) can be told
// apart from an explicit false.
type createLocationRequest struct {
	ParentID            *string  `json:"parent_id"`
	Path                string   `json:"path"`
	Type                string   `json:"type"`
	Code                string   `json:"code"`
	Description         *string  `json:"description"`
	Name                *string  `json:"name"`
	Address             *string  `json:"address"`
	City                *string  `json:"city"`
	State               *string  `json:"state"`
	Zip                 *string  `json:"zip"`
	Phone               *string  `json:"phone"`
	TaxJurisdictionCode *string  `json:"tax_jurisdiction_code"`
	DefaultTaxRate      *float64 `json:"default_tax_rate"`
	Timezone            *string  `json:"timezone"`
	Active              *bool    `json:"active"`
}

// resolve parses the request into the row to persist: the type from its
// lowercase wire name (forceType fills it when the route itself owns the
// type, as the branch route does), the parent when sent, and active
// defaulting to true when the caller did not mention it.
func (req createLocationRequest) resolve(v *httpx.Validator, forceType LocationType) Location {
	loc := Location{
		Path:                req.Path,
		Code:                req.Code,
		Description:         req.Description,
		Name:                req.Name,
		Address:             req.Address,
		City:                req.City,
		State:               req.State,
		Zip:                 req.Zip,
		Phone:               req.Phone,
		TaxJurisdictionCode: req.TaxJurisdictionCode,
		DefaultTaxRate:      req.DefaultTaxRate,
		Timezone:            req.Timezone,
		Active:              req.Active == nil || *req.Active,
	}
	if forceType != "" {
		loc.Type = forceType
	} else if req.Type != "" {
		if t, ok := ParseLocationType(req.Type); ok {
			loc.Type = t
		} else {
			v.Check(false, "type", "must be one of: branch, zone, aisle, rack, shelf, bin, yard")
		}
	} else {
		v.Check(false, "type", "is required")
	}
	v.Required("code", req.Code)
	if req.ParentID != nil && *req.ParentID != "" {
		if id, ok := v.UUID("parent_id", req.ParentID, true); ok {
			loc.ParentID = &id
		}
	}
	return loc
}

// locationUpdateRequest is the PUT body: the mutable slice of a location, in
// the same wire shape as the create.
type locationUpdateRequest struct {
	Path                string   `json:"path"`
	Code                string   `json:"code"`
	Description         *string  `json:"description"`
	Name                *string  `json:"name"`
	Address             *string  `json:"address"`
	City                *string  `json:"city"`
	State               *string  `json:"state"`
	Zip                 *string  `json:"zip"`
	Phone               *string  `json:"phone"`
	TaxJurisdictionCode *string  `json:"tax_jurisdiction_code"`
	DefaultTaxRate      *float64 `json:"default_tax_rate"`
	Timezone            *string  `json:"timezone"`
	Active              *bool    `json:"active"`
	Revision            *int64   `json:"revision"`
}

func writeLocJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}

func (h *Handler) CreateLocation(w http.ResponseWriter, r *http.Request) {
	var req createLocationRequest
	if err := httpx.DecodeJSON(r, &req); err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	v := &httpx.Validator{}
	loc := req.resolve(v, "")
	if err := v.Err(); err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	if loc.Type == LocTypeBranch && !callerMayCreateBranch(r) {
		httpx.WriteError(w, r, httpx.Forbidden("creating a branch requires the admin or owner role"))
		return
	}
	if loc.ParentID != nil && h.guard != nil {
		err := h.guard.CheckPayloadLocation(r.Context(), *loc.ParentID)
		if errors.Is(err, middleware.ErrPayloadBranchRefused) {
			httpx.WriteError(w, r, httpx.Forbidden("parent_id is in a branch this caller may not target"))
			return
		}
		if err != nil {
			httpx.WriteError(w, r, err)
			return
		}
	}
	if err := h.service.CreateLocation(r.Context(), &loc); err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	w.Header().Set("Location", "/api/v1/locations/"+loc.ID.String())
	httpx.WriteRevisionETag(w, loc.Revision)
	writeLocJSON(w, http.StatusCreated, loc)
}

// ListLocations serves GET /api/v1/locations. Behind the branch wall the
// list covers only the caller's branches: with a context branch that
// branch's rows, with no context branch the rows of the branches granted to
// the user (none granted, none listed), and an administrator without a
// header, an unbound key and the single-branch switch see every location,
// as before. The branch switcher reads /me/branches, so it does not ride on
// this list.
const locationsOrdering = "locations.created_at_id_desc"

func cursorError() error {
	return httpx.BadRequest("cursor keyset is malformed",
		httpx.FieldError{Field: "cursor", Message: "cursor keyset is malformed"})
}

// parsePage reads the shared list parameters: the strict guard on the route's
// names, the page, and the keyset position when a cursor was sent.
func parsePage(r *http.Request, scope string, allowed ...string) (after *time.Time, afterID *uuid.UUID, limit int, err error) {
	names := append([]string{"cursor", "limit", "include"}, allowed...)
	q, err := httpx.StrictQuery(r, names...)
	if err != nil {
		return nil, nil, 0, err
	}
	page, err := httpx.ParseListQuery(r, scope)
	if err != nil {
		return nil, nil, 0, err
	}
	if page.Key != nil {
		if len(page.Key) != 2 {
			return nil, nil, 0, cursorError()
		}
		at, terr := httpx.ParseKeyTime(page.Key[0])
		id, uerr := httpx.ParseKeyUUID(page.Key[1])
		if terr != nil || uerr != nil {
			return nil, nil, 0, cursorError()
		}
		after, afterID = &at, &id
	}
	wantTotal := false
	if vals := q["include"]; len(vals) > 0 {
		if len(vals) > 1 {
			return nil, nil, 0, httpx.BadRequest("include parameter is repeated",
				httpx.FieldError{Field: "include", Message: "parameter is repeated"})
		}
		set, ierr := httpx.ParseInclude(vals[0], httpx.IncludeTotal)
		if ierr != nil {
			return nil, nil, 0, ierr
		}
		wantTotal = set.Has(httpx.IncludeTotal)
	}
	if wantTotal {
		return after, afterID, page.Limit, errWantTotal{}
	}
	return after, afterID, page.Limit, nil
}

// errWantTotal is the marker parsePage returns when include=total was asked.
type errWantTotal struct{}

func (errWantTotal) Error() string { return "include=total" }

func writePage[T any](w http.ResponseWriter, r *http.Request, items []T, more bool, scope string, lastCreatedAt func() (time.Time, uuid.UUID), total *int64, limit int) {
	next := ""
	if more && len(items) > 0 {
		at, id := lastCreatedAt()
		var err error
		next, err = httpx.MintCursor(scope, httpx.FormatKeyTime(at), id.String())
		if err != nil {
			httpx.WriteError(w, r, err)
			return
		}
	}
	opts := []httpx.ListOption{}
	if total != nil {
		opts = append(opts, httpx.WithTotal(*total))
	}
	httpx.WriteList(w, items, next, limit, opts...)
}

func (h *Handler) ListLocations(w http.ResponseWriter, r *http.Request) {
	after, afterID, limit, perr := parsePage(r, locationsOrdering)
	wantTotal := errors.Is(perr, errWantTotal{})
	if perr != nil && !wantTotal {
		httpx.WriteError(w, r, perr)
		return
	}
	branches, err := h.listScope(r.Context())
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	locs, more, err := h.service.ListLocationsPage(r.Context(), ListScope{Branches: branches}, after, afterID, limit)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	var total *int64
	if wantTotal {
		n, err := h.service.CountLocations(r.Context(), ListScope{Branches: branches})
		if err != nil {
			httpx.WriteError(w, r, err)
			return
		}
		total = &n
	}
	writePage(w, r, locs, more, locationsOrdering, func() (time.Time, uuid.UUID) {
		last := locs[len(locs)-1]
		return last.CreatedAt.Time, last.ID
	}, total, limit)
}

// listScope resolves the branch ids a caller's location list covers; nil is
// every branch and an empty slice is no branches. A request that did not run
// the branch middleware, an administrator (the single-branch switch makes
// every caller one) and an unbound key cover every branch; a bound caller is
// held to its context branch, or to its granted branches when the middleware
// left no context branch, so a user with no grants lists nothing.
func (h *Handler) listScope(ctx context.Context) ([]uuid.UUID, error) {
	bc := middleware.BranchFromContext(ctx)
	if bc == nil {
		return nil, nil
	}
	if bc.BranchID != nil {
		return []uuid.UUID{*bc.BranchID}, nil
	}
	if bc.IsAdmin || bc.UserSub == "" {
		return nil, nil
	}
	if h.userRepo == nil {
		return nil, nil
	}
	grants, err := h.userRepo.ListUserBranches(ctx, bc.UserSub)
	if err != nil {
		return nil, err
	}
	ids := make([]uuid.UUID, 0, len(grants))
	for _, g := range grants {
		ids = append(ids, g.ID)
	}
	return ids, nil
}

func (h *Handler) GetLocation(w http.ResponseWriter, r *http.Request) {
	if _, err := httpx.StrictQuery(r); err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	id, err := uuid.Parse(r.PathValue("id"))
	if err != nil {
		httpx.WriteError(w, r, httpx.BadRequest("id is not a UUID",
			httpx.FieldError{Field: "id", Message: "must be a UUID in lowercase hyphenated form"}))
		return
	}
	// The record branch rule (ADR 0007 section 2.3): a location of a branch
	// the caller may not target is a 403. A location that does not exist
	// belongs to no branch and passes; the service answers for it.
	if h.guard != nil {
		err := h.guard.CheckPayloadLocation(r.Context(), id)
		if errors.Is(err, middleware.ErrPayloadBranchRefused) {
			httpx.WriteError(w, r, httpx.Forbidden("location is in a branch this caller may not target"))
			return
		}
		if err != nil {
			httpx.WriteError(w, r, err)
			return
		}
	}
	loc, err := h.service.GetLocation(r.Context(), id)
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			httpx.WriteError(w, r, httpx.NotFound("no such location"))
			return
		}
		httpx.WriteError(w, r, err)
		return
	}
	httpx.WriteRevisionETag(w, loc.Revision)
	writeLocJSON(w, http.StatusOK, loc)
}

func (h *Handler) UpdateLocation(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(r.PathValue("id"))
	if err != nil {
		httpx.WriteError(w, r, httpx.BadRequest("id is not a UUID",
			httpx.FieldError{Field: "id", Message: "must be a UUID in lowercase hyphenated form"}))
		return
	}
	var req locationUpdateRequest
	if err := httpx.DecodeJSON(r, &req); err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	current, err := h.service.GetLocation(r.Context(), id)
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			httpx.WriteError(w, r, httpx.NotFound("no such location"))
			return
		}
		httpx.WriteError(w, r, err)
		return
	}
	if err := httpx.CheckRevision(current.Revision, r.Header.Get("If-Match"), req.Revision); err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	loc := *current
	loc.Path, loc.Code, loc.Description = req.Path, req.Code, req.Description
	loc.Name, loc.Address, loc.City, loc.State, loc.Zip, loc.Phone = req.Name, req.Address, req.City, req.State, req.Zip, req.Phone
	loc.TaxJurisdictionCode, loc.DefaultTaxRate, loc.Timezone = req.TaxJurisdictionCode, req.DefaultTaxRate, req.Timezone
	if req.Active != nil {
		loc.Active = *req.Active // omitted keeps the stored value
	}
	if loc.Code == "" {
		loc.Code = current.Code
	}
	if err := h.service.UpdateLocation(r.Context(), &loc, current.Revision); err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	httpx.WriteRevisionETag(w, loc.Revision)
	writeLocJSON(w, http.StatusOK, loc)
}

func (h *Handler) DeleteLocation(w http.ResponseWriter, r *http.Request) {
	if _, err := httpx.StrictQuery(r); err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	id, err := uuid.Parse(r.PathValue("id"))
	if err != nil {
		httpx.WriteError(w, r, httpx.BadRequest("id is not a UUID",
			httpx.FieldError{Field: "id", Message: "must be a UUID in lowercase hyphenated form"}))
		return
	}
	// The precondition arrives as If-Match or as a body revision; an empty
	// body is the caller sending the header alone.
	var bodyRevision *int64
	if raw, rerr := io.ReadAll(r.Body); rerr == nil && len(raw) > 0 {
		r.Body = io.NopCloser(bytes.NewReader(raw))
		var body struct {
			Revision *int64 `json:"revision"`
		}
		if err := httpx.DecodeJSON(r, &body); err != nil {
			httpx.WriteError(w, r, err)
			return
		}
		bodyRevision = body.Revision
	}
	current, err := h.service.GetLocation(r.Context(), id)
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			httpx.WriteError(w, r, httpx.NotFound("no such location"))
			return
		}
		httpx.WriteError(w, r, err)
		return
	}
	if err := httpx.CheckRevision(current.Revision, r.Header.Get("If-Match"), bodyRevision); err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	if err := h.service.DeleteLocation(r.Context(), id, current.Revision); err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// ---------- branch endpoints ----------

const branchesOrdering = "branches.created_at_id_desc"

func (h *Handler) ListBranches(w http.ResponseWriter, r *http.Request) {
	after, afterID, limit, perr := parsePage(r, branchesOrdering, "include_inactive")
	wantTotal := errors.Is(perr, errWantTotal{})
	if perr != nil && !wantTotal {
		httpx.WriteError(w, r, perr)
		return
	}
	includeInactive := false
	if vals := r.URL.Query()["include_inactive"]; len(vals) > 0 {
		switch vals[0] {
		case "true":
			includeInactive = true
		case "false":
			includeInactive = false
		default:
			httpx.WriteError(w, r, httpx.BadRequest("include_inactive is not a boolean",
				httpx.FieldError{Field: "include_inactive", Message: "must be true or false"}))
			return
		}
		if len(vals) > 1 {
			httpx.WriteError(w, r, httpx.BadRequest("include_inactive parameter is repeated",
				httpx.FieldError{Field: "include_inactive", Message: "parameter is repeated"}))
			return
		}
	}
	branches, more, err := h.service.ListBranchesPage(r.Context(), includeInactive, after, afterID, limit)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	var total *int64
	if wantTotal {
		n, err := h.service.CountBranches(r.Context(), includeInactive)
		if err != nil {
			httpx.WriteError(w, r, err)
			return
		}
		total = &n
	}
	writePage(w, r, branches, more, branchesOrdering, func() (time.Time, uuid.UUID) {
		last := branches[len(branches)-1]
		return last.CreatedAt.Time, last.ID
	}, total, limit)
}

func (h *Handler) CreateBranch(w http.ResponseWriter, r *http.Request) {
	var req createLocationRequest
	if err := httpx.DecodeJSON(r, &req); err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	v := &httpx.Validator{}
	loc := req.resolve(v, LocTypeBranch)
	loc.ParentID = nil
	v.Required("name", deref(loc.Name))
	if err := v.Err(); err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	if err := h.service.CreateLocation(r.Context(), &loc); err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	w.Header().Set("Location", "/api/v1/branches/"+loc.ID.String())
	httpx.WriteRevisionETag(w, loc.Revision)
	writeLocJSON(w, http.StatusCreated, loc)
}

// deref reads a pointer string field for the legacy projections that still
// carry plain strings.
func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

func (h *Handler) GetBranch(w http.ResponseWriter, r *http.Request) {
	if _, err := httpx.StrictQuery(r); err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	id, err := uuid.Parse(r.PathValue("id"))
	if err != nil {
		httpx.WriteError(w, r, httpx.BadRequest("id is not a UUID",
			httpx.FieldError{Field: "id", Message: "must be a UUID in lowercase hyphenated form"}))
		return
	}
	// Reference data like the branch list, so unwalled (PR 39 decision).
	loc, err := h.service.GetLocation(r.Context(), id)
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			httpx.WriteError(w, r, httpx.NotFound("no such branch"))
			return
		}
		httpx.WriteError(w, r, err)
		return
	}
	if loc.Type != LocTypeBranch {
		httpx.WriteError(w, r, httpx.NotFound("no such branch"))
		return
	}
	httpx.WriteRevisionETag(w, loc.Revision)
	writeLocJSON(w, http.StatusOK, loc)
}

// requireBranch answers 404 for a path id that is not a branch, so the
// branch routes never act on a zone, a yard or a bin.
func (h *Handler) requireBranch(w http.ResponseWriter, r *http.Request) bool {
	id, err := uuid.Parse(r.PathValue("id"))
	if err != nil {
		return true // the shared handler answers the malformed id
	}
	isBranch, err := h.service.IsBranch(r.Context(), id)
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			httpx.WriteError(w, r, httpx.NotFound("no such branch"))
			return false
		}
		httpx.WriteError(w, r, err)
		return false
	}
	if !isBranch {
		httpx.WriteError(w, r, httpx.NotFound("no such branch"))
		return false
	}
	return true
}

func (h *Handler) UpdateBranch(w http.ResponseWriter, r *http.Request) {
	// Reuses UpdateLocation; the type column is not mutable from this endpoint.
	if h.requireBranch(w, r) {
		h.UpdateLocation(w, r)
	}
}

func (h *Handler) DeleteBranch(w http.ResponseWriter, r *http.Request) {
	// Soft-archive via DeleteLocation (sets active=false).
	if h.requireBranch(w, r) {
		h.DeleteLocation(w, r)
	}
}

func (h *Handler) GetBranchTree(w http.ResponseWriter, r *http.Request) {
	if _, err := httpx.StrictQuery(r); err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	id, err := uuid.Parse(r.PathValue("id"))
	if err != nil {
		httpx.WriteError(w, r, httpx.BadRequest("id is not a UUID",
			httpx.FieldError{Field: "id", Message: "must be a UUID in lowercase hyphenated form"}))
		return
	}
	// The record branch rule (ADR 0007 section 2.3): the path id is the
	// branch itself, so a tree the caller may not target is a 403.
	if h.guard != nil {
		err := h.guard.CheckPayloadBranch(r.Context(), id)
		if errors.Is(err, middleware.ErrPayloadBranchRefused) {
			httpx.WriteError(w, r, httpx.Forbidden("branch is outside the branches this caller may target"))
			return
		}
		if err != nil {
			httpx.WriteError(w, r, err)
			return
		}
	}
	tree, err := h.service.GetBranchTree(r.Context(), id)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	httpx.WriteList(w, tree, "", 0)
}

// ---------- user-branch endpoints ----------

func (h *Handler) ListMyBranches(w http.ResponseWriter, r *http.Request) {
	claims := middleware.ClaimsFromContext(r.Context())
	if claims == nil {
		// Dev mode without auth: return all active branches so the UI is usable.
		all, _, err := h.service.ListBranchesPage(r.Context(), false, nil, nil, 200)
		if err != nil {
			httpx.WriteError(w, r, err)
			return
		}
		out := make([]BranchSummary, 0, len(all))
		for i, b := range all {
			out = append(out, BranchSummary{
				ID:       b.ID,
				Code:     b.Code,
				Name:     deref(b.Name),
				Active:   b.Active,
				IsHome:   i == 0,
				Timezone: deref(b.Timezone),
			})
		}
		writeJSON(w, http.StatusOK, out)
		return
	}
	branches, err := h.userRepo.ListUserBranches(r.Context(), claims.Subject)
	if err != nil {
		httputil.RespondError(w, r, "failed to list user branches", http.StatusInternalServerError, err)
		return
	}
	if branches == nil {
		branches = []BranchSummary{}
	}
	writeJSON(w, http.StatusOK, branches)
}

func (h *Handler) ListUserBranches(w http.ResponseWriter, r *http.Request) {
	sub := r.PathValue("sub")
	if sub == "" {
		httputil.RespondError(w, r, "user sub required", http.StatusBadRequest, nil)
		return
	}
	branches, err := h.userRepo.ListUserBranches(r.Context(), sub)
	if err != nil {
		httputil.RespondError(w, r, "failed to list user branches", http.StatusInternalServerError, err)
		return
	}
	if branches == nil {
		branches = []BranchSummary{}
	}
	// A branch bound key sees the pin's branch only (ADR 0007 section 5.5):
	// the list is narrowed to it, as every list the key reaches is.
	if pin, ok := middleware.KeyBranchPin(r.Context()); ok {
		kept := make([]BranchSummary, 0, len(branches))
		for _, b := range branches {
			if b.ID == pin {
				kept = append(kept, b)
			}
		}
		branches = kept
	}
	writeJSON(w, http.StatusOK, branches)
}

type grantUserBranchRequest struct {
	BranchID uuid.UUID `json:"branch_id"`
	IsHome   bool      `json:"is_home"`
}

func (h *Handler) GrantUserBranch(w http.ResponseWriter, r *http.Request) {
	sub := r.PathValue("sub")
	if sub == "" {
		httputil.RespondError(w, r, "user sub required", http.StatusBadRequest, nil)
		return
	}
	var req grantUserBranchRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		httputil.RespondError(w, r, "invalid input", http.StatusBadRequest, err)
		return
	}
	if req.BranchID == uuid.Nil {
		httputil.RespondError(w, r, "branch_id required", http.StatusBadRequest, nil)
		return
	}
	// Verify target is actually a branch.
	isBranch, err := h.service.IsBranch(r.Context(), req.BranchID)
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			httputil.RespondError(w, r, "branch not found", http.StatusNotFound, err)
			return
		}
		httputil.RespondError(w, r, "failed to validate branch", http.StatusInternalServerError, err)
		return
	}
	if !isBranch {
		httputil.RespondError(w, r, "id is not a branch", http.StatusBadRequest, nil)
		return
	}

	grantedBy := ""
	if c := middleware.ClaimsFromContext(r.Context()); c != nil {
		grantedBy = c.Subject
	}
	if err := h.userRepo.GrantUserBranch(r.Context(), UserLocation{
		UserSub:   sub,
		BranchID:  req.BranchID,
		IsHome:    req.IsHome,
		GrantedBy: grantedBy,
	}); err != nil {
		httputil.RespondError(w, r, "failed to grant", http.StatusInternalServerError, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *Handler) RevokeUserBranch(w http.ResponseWriter, r *http.Request) {
	sub := r.PathValue("sub")
	branchID, err := uuid.Parse(r.PathValue("branch_id"))
	if err != nil {
		httputil.RespondError(w, r, "invalid branch_id", http.StatusBadRequest, err)
		return
	}
	if err := h.userRepo.RevokeUserBranch(r.Context(), sub, branchID); err != nil {
		if errors.Is(err, ErrNotFound) {
			httputil.RespondError(w, r, "grant not found", http.StatusNotFound, err)
			return
		}
		httputil.RespondError(w, r, "failed to revoke", http.StatusInternalServerError, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

type setHomeBranchRequest struct {
	BranchID uuid.UUID `json:"branch_id"`
}

func (h *Handler) SetHomeBranch(w http.ResponseWriter, r *http.Request) {
	sub := r.PathValue("sub")
	var req setHomeBranchRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		httputil.RespondError(w, r, "invalid input", http.StatusBadRequest, err)
		return
	}
	if req.BranchID == uuid.Nil {
		httputil.RespondError(w, r, "branch_id required", http.StatusBadRequest, nil)
		return
	}
	if err := h.userRepo.SetHomeBranch(r.Context(), sub, req.BranchID); err != nil {
		if errors.Is(err, ErrNotFound) {
			httputil.RespondError(w, r, "grant not found", http.StatusNotFound, err)
			return
		}
		httputil.RespondError(w, r, "failed to set home", http.StatusInternalServerError, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// ListKnownUsers returns the union of distinct user_subs from user_locations
// + audit_log. Used by the admin branch-assignment UI to populate the user
// picker without a dedicated users table.
func (h *Handler) ListKnownUsers(w http.ResponseWriter, r *http.Request) {
	users, err := h.userRepo.ListKnownUsers(r.Context())
	if err != nil {
		httputil.RespondError(w, r, "failed to list users", http.StatusInternalServerError, err)
		return
	}
	if users == nil {
		users = []string{}
	}
	writeJSON(w, http.StatusOK, users)
}

func (h *Handler) ListBranchUsers(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(r.PathValue("id"))
	if err != nil {
		httputil.RespondError(w, r, "invalid id", http.StatusBadRequest, err)
		return
	}
	users, err := h.userRepo.ListBranchUsers(r.Context(), id)
	if err != nil {
		httputil.RespondError(w, r, "failed to list branch users", http.StatusInternalServerError, err)
		return
	}
	if users == nil {
		users = []UserLocation{}
	}
	writeJSON(w, http.StatusOK, users)
}

func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}

// callerMayCreateBranch reports whether the caller may create a BRANCH through
// the location route: a user holding admin or owner, or no claims at all in dev
// mode. A machine key has no claims but is not a user, so it is refused: a
// branch is made through the admin only POST /api/v1/branches.
func callerMayCreateBranch(r *http.Request) bool {
	claims := middleware.ClaimsFromContext(r.Context())
	if claims == nil {
		_, isKey := middleware.KeyIDFromContext(r.Context())
		return !isKey
	}
	return middleware.ClaimsHaveAnyRole(claims, "admin", "owner")
}
