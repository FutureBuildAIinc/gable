// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package location

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"

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
	mux.HandleFunc("PUT /api/v1/locations/{id}", adminGuard(h.UpdateLocation))
	mux.HandleFunc("DELETE /api/v1/locations/{id}", adminGuard(h.DeleteLocation))

	// Branch CRUD.
	mux.HandleFunc("GET /api/v1/branches", guard(h.ListBranches))
	mux.HandleFunc("POST /api/v1/branches", adminGuard(h.CreateBranch))
	mux.HandleFunc("GET /api/v1/branches/{id}", guard(h.GetBranch))
	mux.HandleFunc("PUT /api/v1/branches/{id}", adminGuard(h.UpdateBranch))
	mux.HandleFunc("DELETE /api/v1/branches/{id}", adminGuard(h.DeleteBranch))
	tree := http.HandlerFunc(h.GetBranchTree)
	if h.branchMw != nil {
		tree = h.branchMw(tree).ServeHTTP
	}
	mux.HandleFunc("GET /api/v1/branches/{id}/tree", guard(tree))

	// User-branch grants.
	if h.userRepo != nil {
		mux.HandleFunc("GET /api/v1/me/branches", guard(h.ListMyBranches))
		mux.HandleFunc("GET /api/v1/users", adminGuard(h.ListKnownUsers))
		mux.HandleFunc("GET /api/v1/users/{sub}/branches", adminGuard(h.ListUserBranches))
		mux.HandleFunc("POST /api/v1/users/{sub}/branches", adminGuard(h.GrantUserBranch))
		mux.HandleFunc("DELETE /api/v1/users/{sub}/branches/{branch_id}", adminGuard(h.RevokeUserBranch))
		mux.HandleFunc("PUT /api/v1/users/{sub}/home-branch", adminGuard(h.SetHomeBranch))
		mux.HandleFunc("GET /api/v1/branches/{id}/users", adminGuard(h.ListBranchUsers))
	}
}

// ---------- location endpoints ----------

// createLocationRequest is the create payload. It exists only so that "active"
// can be told apart from "active": false: Location.Active is a plain bool whose
// zero value is the same as an explicit false, which is why the default has to
// be resolved here, at the JSON boundary, rather than in the service. The outer
// *bool shadows the embedded Location.Active during decoding because
// encoding/json prefers the shallower field of the same name.
type createLocationRequest struct {
	Location
	Active *bool `json:"active"`
}

// resolve returns the location to persist, defaulting active to true when the
// caller did not mention it and honouring an explicit false when they did.
func (req createLocationRequest) resolve() Location {
	loc := req.Location
	loc.Active = req.Active == nil || *req.Active
	return loc
}

func (h *Handler) CreateLocation(w http.ResponseWriter, r *http.Request) {
	var req createLocationRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		httputil.RespondError(w, r, "Invalid input", http.StatusBadRequest, err)
		return
	}
	loc := req.resolve()
	if loc.Type == LocTypeBranch && !callerMayCreateBranch(r) {
		httputil.RespondError(w, r, "type BRANCH requires the admin or owner role", http.StatusForbidden, nil)
		return
	}
	if loc.ParentID != nil && h.guard != nil {
		err := h.guard.CheckPayloadLocation(r.Context(), *loc.ParentID)
		if errors.Is(err, middleware.ErrPayloadBranchRefused) {
			httputil.RespondError(w, r, "parent_id is in a branch this caller may not target", http.StatusForbidden, err)
			return
		}
		if err != nil {
			httputil.RespondError(w, r, "branch access lookup failed", http.StatusInternalServerError, err)
			return
		}
	}
	if err := h.service.CreateLocation(r.Context(), &loc); err != nil {
		httputil.RespondError(w, r, "failed to create location", http.StatusBadRequest, err)
		return
	}
	writeJSON(w, http.StatusCreated, loc)
}

// ListLocations serves GET /api/v1/locations. Behind the branch wall the
// list covers only the caller's branches: with a context branch that
// branch's rows, with no context branch the rows of the branches granted to
// the user, and an administrator without a header, an unbound key and the
// single-branch switch see every location, as before. The branch switcher
// reads /me/branches, so it does not ride on this list.
func (h *Handler) ListLocations(w http.ResponseWriter, r *http.Request) {
	branches, err := h.listScope(r.Context())
	if err != nil {
		httputil.RespondError(w, r, "branch access lookup failed", http.StatusInternalServerError, err)
		return
	}
	locs, err := h.service.ListLocationsIn(r.Context(), branches)
	if err != nil {
		httputil.RespondError(w, r, "failed to list locations", http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, locs)
}

// listScope resolves the branch ids a caller's location list covers; nil is
// every branch. A request that did not run the branch middleware, an
// administrator (the single-branch switch makes every caller one) and an
// unbound key cover every branch; a bound caller is held to its context
// branch, or to its granted branches when the middleware left no context
// branch.
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
	id, err := uuid.Parse(r.PathValue("id"))
	if err != nil {
		httputil.RespondError(w, r, "invalid id", http.StatusBadRequest, err)
		return
	}
	// The record branch rule (ADR 0007 section 2.3): a location of a branch
	// the caller may not target is a 403, in the legacy error shape these
	// unconverted routes carry. A location that does not exist belongs to no
	// branch and passes; the service answers for it.
	if h.guard != nil {
		err := h.guard.CheckPayloadLocation(r.Context(), id)
		if errors.Is(err, middleware.ErrPayloadBranchRefused) {
			httputil.RespondError(w, r, "location is in a branch this caller may not target", http.StatusForbidden, err)
			return
		}
		if err != nil {
			httputil.RespondError(w, r, "branch access lookup failed", http.StatusInternalServerError, err)
			return
		}
	}
	loc, err := h.service.GetLocation(r.Context(), id)
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			httputil.RespondError(w, r, "not found", http.StatusNotFound, err)
			return
		}
		httputil.RespondError(w, r, "failed to fetch location", http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, loc)
}

func (h *Handler) UpdateLocation(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(r.PathValue("id"))
	if err != nil {
		httputil.RespondError(w, r, "invalid id", http.StatusBadRequest, err)
		return
	}
	var loc Location
	if err := json.NewDecoder(r.Body).Decode(&loc); err != nil {
		httputil.RespondError(w, r, "invalid input", http.StatusBadRequest, err)
		return
	}
	loc.ID = id
	if err := h.service.UpdateLocation(r.Context(), &loc); err != nil {
		if errors.Is(err, ErrNotFound) {
			httputil.RespondError(w, r, "not found", http.StatusNotFound, err)
			return
		}
		httputil.RespondError(w, r, "failed to update location", http.StatusBadRequest, err)
		return
	}
	writeJSON(w, http.StatusOK, loc)
}

func (h *Handler) DeleteLocation(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(r.PathValue("id"))
	if err != nil {
		httputil.RespondError(w, r, "invalid id", http.StatusBadRequest, err)
		return
	}
	if err := h.service.DeleteLocation(r.Context(), id); err != nil {
		if errors.Is(err, ErrNotFound) {
			httputil.RespondError(w, r, "not found", http.StatusNotFound, err)
			return
		}
		httputil.RespondError(w, r, "failed to delete location", http.StatusInternalServerError, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// ---------- branch endpoints ----------

func (h *Handler) ListBranches(w http.ResponseWriter, r *http.Request) {
	includeInactive := r.URL.Query().Get("include_inactive") == "true"
	branches, err := h.service.ListBranches(r.Context(), includeInactive)
	if err != nil {
		httputil.RespondError(w, r, "failed to list branches", http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, branches)
}

func (h *Handler) CreateBranch(w http.ResponseWriter, r *http.Request) {
	var req createLocationRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		httputil.RespondError(w, r, "invalid input", http.StatusBadRequest, err)
		return
	}
	loc := req.resolve()
	loc.Type = LocTypeBranch
	loc.ParentID = nil
	if err := h.service.CreateLocation(r.Context(), &loc); err != nil {
		httputil.RespondError(w, r, "failed to create branch", http.StatusBadRequest, err)
		return
	}
	writeJSON(w, http.StatusCreated, loc)
}

func (h *Handler) GetBranch(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(r.PathValue("id"))
	if err != nil {
		httputil.RespondError(w, r, "invalid id", http.StatusBadRequest, err)
		return
	}
	loc, err := h.service.GetLocation(r.Context(), id)
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			httputil.RespondError(w, r, "not found", http.StatusNotFound, err)
			return
		}
		httputil.RespondError(w, r, "failed to fetch branch", http.StatusInternalServerError, err)
		return
	}
	if loc.Type != LocTypeBranch {
		httputil.RespondError(w, r, "not a branch", http.StatusNotFound, nil)
		return
	}
	writeJSON(w, http.StatusOK, loc)
}

func (h *Handler) UpdateBranch(w http.ResponseWriter, r *http.Request) {
	// Reuses UpdateLocation; the type column is not mutable from this endpoint.
	h.UpdateLocation(w, r)
}

func (h *Handler) DeleteBranch(w http.ResponseWriter, r *http.Request) {
	// Soft-archive via DeleteLocation (sets active=false).
	h.DeleteLocation(w, r)
}

func (h *Handler) GetBranchTree(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(r.PathValue("id"))
	if err != nil {
		httputil.RespondError(w, r, "invalid id", http.StatusBadRequest, err)
		return
	}
	// The record branch rule (ADR 0007 section 2.3): the path id is the
	// branch itself, so a tree the caller may not target is a 403, in the
	// legacy error shape these unconverted routes carry.
	if h.guard != nil {
		err := h.guard.CheckPayloadBranch(r.Context(), id)
		if errors.Is(err, middleware.ErrPayloadBranchRefused) {
			httputil.RespondError(w, r, "branch is outside the branches this caller may target", http.StatusForbidden, err)
			return
		}
		if err != nil {
			httputil.RespondError(w, r, "branch access lookup failed", http.StatusInternalServerError, err)
			return
		}
	}
	tree, err := h.service.GetBranchTree(r.Context(), id)
	if err != nil {
		httputil.RespondError(w, r, "failed to fetch tree", http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, tree)
}

// ---------- user-branch endpoints ----------

func (h *Handler) ListMyBranches(w http.ResponseWriter, r *http.Request) {
	claims := middleware.ClaimsFromContext(r.Context())
	if claims == nil {
		// Dev mode without auth: return all active branches so the UI is usable.
		all, err := h.service.ListBranches(r.Context(), false)
		if err != nil {
			httputil.RespondError(w, r, "failed to list branches", http.StatusInternalServerError, err)
			return
		}
		out := make([]BranchSummary, 0, len(all))
		for i, b := range all {
			out = append(out, BranchSummary{
				ID:       b.ID,
				Code:     b.Code,
				Name:     b.Name,
				Active:   b.Active,
				IsHome:   i == 0,
				Timezone: b.Timezone,
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
