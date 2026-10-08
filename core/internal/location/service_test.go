// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package location

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gablelbm/gable/pkg/middleware"
	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"
)

// Locations are the branch hierarchy: a branch row is the tenant boundary that
// tax rates, inventory and the branch-scoped middleware all key on, and the
// non-branch rows are the bin/rack tree beneath it. Getting the shape wrong
// produces orphan rows whose branch_id trigger has nothing to derive from.
//
// Tests are CORRECTNESS unless labelled CHARACTERIZATION.

// --- fakes ---------------------------------------------------------------

type fakeRepo struct {
	locations map[uuid.UUID]*Location
	branches  []Location
	tree      []Location

	created []Location
	updated []Location
	deleted []uuid.UUID

	err                error
	listBranchesArgs   []bool
	isBranchResult     bool
	isBranchErr        error
	branchTreeArgument uuid.UUID
}

func newFakeRepo() *fakeRepo {
	return &fakeRepo{locations: map[uuid.UUID]*Location{}}
}

func (f *fakeRepo) CreateLocation(_ context.Context, loc *Location) error {
	if f.err != nil {
		return f.err
	}
	if loc.ID == uuid.Nil {
		loc.ID = uuid.New()
	}
	f.created = append(f.created, *loc)
	f.locations[loc.ID] = loc
	return nil
}

func (f *fakeRepo) GetLocation(_ context.Context, id uuid.UUID) (*Location, error) {
	if f.err != nil {
		return nil, f.err
	}
	loc, ok := f.locations[id]
	if !ok {
		return nil, ErrNotFound
	}
	return loc, nil
}

func (f *fakeRepo) UpdateLocation(_ context.Context, loc *Location) error {
	if f.err != nil {
		return f.err
	}
	f.updated = append(f.updated, *loc)
	f.locations[loc.ID] = loc
	return nil
}

func (f *fakeRepo) DeleteLocation(_ context.Context, id uuid.UUID) error {
	if f.err != nil {
		return f.err
	}
	if _, ok := f.locations[id]; !ok {
		return ErrNotFound
	}
	f.deleted = append(f.deleted, id)
	f.locations[id].Active = false
	return nil
}

func (f *fakeRepo) ListLocations(context.Context) ([]Location, error) {
	if f.err != nil {
		return nil, f.err
	}
	out := make([]Location, 0, len(f.locations))
	for _, l := range f.locations {
		out = append(out, *l)
	}
	return out, nil
}

func (f *fakeRepo) ListBranches(_ context.Context, includeInactive bool) ([]Location, error) {
	f.listBranchesArgs = append(f.listBranchesArgs, includeInactive)
	if f.err != nil {
		return nil, f.err
	}
	if includeInactive {
		return f.branches, nil
	}
	var active []Location
	for _, b := range f.branches {
		if b.Active {
			active = append(active, b)
		}
	}
	return active, nil
}

func (f *fakeRepo) GetBranchTree(_ context.Context, branchID uuid.UUID) ([]Location, error) {
	f.branchTreeArgument = branchID
	if f.err != nil {
		return nil, f.err
	}
	return f.tree, nil
}

func (f *fakeRepo) IsBranch(context.Context, uuid.UUID) (bool, error) {
	if f.isBranchErr != nil {
		return false, f.isBranchErr
	}
	return f.isBranchResult, nil
}

var _ Repository = (*fakeRepo)(nil)

type fakeUserRepo struct {
	branches []BranchSummary
	users    []UserLocation
	known    []string
	grants   []UserLocation
	revokes  []struct {
		sub    string
		branch uuid.UUID
	}
	homeCalls []struct {
		sub    string
		branch uuid.UUID
	}
	err error
}

func (f *fakeUserRepo) ListUserBranches(context.Context, string) ([]BranchSummary, error) {
	return f.branches, f.err
}

func (f *fakeUserRepo) ListBranchUsers(context.Context, uuid.UUID) ([]UserLocation, error) {
	return f.users, f.err
}

func (f *fakeUserRepo) GrantUserBranch(_ context.Context, ul UserLocation) error {
	if f.err != nil {
		return f.err
	}
	f.grants = append(f.grants, ul)
	return nil
}

func (f *fakeUserRepo) RevokeUserBranch(_ context.Context, sub string, branchID uuid.UUID) error {
	if f.err != nil {
		return f.err
	}
	f.revokes = append(f.revokes, struct {
		sub    string
		branch uuid.UUID
	}{sub, branchID})
	return nil
}

func (f *fakeUserRepo) SetHomeBranch(_ context.Context, sub string, branchID uuid.UUID) error {
	if f.err != nil {
		return f.err
	}
	f.homeCalls = append(f.homeCalls, struct {
		sub    string
		branch uuid.UUID
	}{sub, branchID})
	return nil
}

func (f *fakeUserRepo) UserHasBranch(context.Context, string, uuid.UUID) (bool, error) {
	return len(f.branches) > 0, f.err
}

func (f *fakeUserRepo) CountUserBranches(context.Context, string) (int, error) {
	return len(f.branches), f.err
}

func (f *fakeUserRepo) ListKnownUsers(context.Context) ([]string, error) {
	return f.known, f.err
}

var _ UserRepository = (*fakeUserRepo)(nil)

func uuidPtr(u uuid.UUID) *uuid.UUID { return &u }

// --- CreateLocation validation ------------------------------------------

// CORRECTNESS: the hierarchy invariants. A branch is a root; anything else
// must hang off a parent, because the denormalised branch_id column is derived
// from the parent by a database trigger — a parentless bin can never be
// attributed to a branch.
func TestCreateLocation_HierarchyRules(t *testing.T) {
	parent := uuid.New()

	tests := []struct {
		name    string
		loc     Location
		wantErr string
	}{
		{
			name: "a branch at the root is valid",
			loc:  Location{Type: LocTypeBranch, Code: "VAN", Name: "Vancouver"},
		},
		{
			name:    "a branch with a parent is refused",
			loc:     Location{Type: LocTypeBranch, Code: "VAN", Name: "Vancouver", ParentID: uuidPtr(parent)},
			wantErr: "root-level",
		},
		{
			name:    "a branch without a name is refused",
			loc:     Location{Type: LocTypeBranch, Code: "VAN"},
			wantErr: "branch name is required",
		},
		{
			name: "a bin under a parent is valid",
			loc:  Location{Type: LocTypeBin, Code: "B2", ParentID: uuidPtr(parent)},
		},
		{
			name:    "a bin with no parent is refused",
			loc:     Location{Type: LocTypeBin, Code: "B2"},
			wantErr: "require a parent_id",
		},
		{
			name:    "a yard with no parent is refused too",
			loc:     Location{Type: LocTypeYard, Code: "Y1"},
			wantErr: "require a parent_id",
		},
		{
			name:    "no code is refused",
			loc:     Location{Type: LocTypeBranch, Name: "Vancouver"},
			wantErr: "location code is required",
		},
		{
			name:    "no type is refused",
			loc:     Location{Code: "VAN", Name: "Vancouver"},
			wantErr: "location type is required",
		},
		{
			// The type is not validated against the known set, so an
			// unrecognised type falls into the "needs a parent" branch.
			name:    "an unknown type without a parent is refused",
			loc:     Location{Type: LocationType("WAREHOUSE"), Code: "W1"},
			wantErr: "require a parent_id",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			repo := newFakeRepo()
			loc := tc.loc
			err := NewService(repo).CreateLocation(context.Background(), &loc)

			if tc.wantErr == "" {
				if err != nil {
					t.Fatalf("CreateLocation: %v", err)
				}
				if len(repo.created) != 1 {
					t.Fatalf("persisted %d locations, want 1", len(repo.created))
				}
				return
			}
			if err == nil {
				t.Fatalf("CreateLocation succeeded, want an error mentioning %q", tc.wantErr)
			}
			if !strings.Contains(err.Error(), tc.wantErr) {
				t.Errorf("error = %q, want it to mention %q", err, tc.wantErr)
			}
			if len(repo.created) != 0 {
				t.Errorf("persisted %d locations despite the validation failure", len(repo.created))
			}
		})
	}
}

// CORRECTNESS: a branch created without an explicit path gets its name as the
// path, so the hierarchy has a root string to build descendants from. An
// explicit path must be left alone.
func TestCreateLocation_BranchPathDefaultsToTheName(t *testing.T) {
	repo := newFakeRepo()
	loc := Location{Type: LocTypeBranch, Code: "VAN", Name: "Vancouver Yard"}
	if err := NewService(repo).CreateLocation(context.Background(), &loc); err != nil {
		t.Fatalf("CreateLocation: %v", err)
	}
	if loc.Path != "Vancouver Yard" {
		t.Errorf("Path = %q, want the branch name", loc.Path)
	}

	repo2 := newFakeRepo()
	explicit := Location{Type: LocTypeBranch, Code: "VAN", Name: "Vancouver Yard", Path: "West/Vancouver"}
	if err := NewService(repo2).CreateLocation(context.Background(), &explicit); err != nil {
		t.Fatalf("CreateLocation: %v", err)
	}
	if explicit.Path != "West/Vancouver" {
		t.Errorf("Path = %q, want the explicit path preserved", explicit.Path)
	}
}

// CORRECTNESS: the service must persist the Active flag it is handed. Forcing
// it true — which is what `if !loc.Active { loc.Active = true }` does, because
// Go's zero value for a bool is indistinguishable from an explicit false —
// makes an inactive location impossible to create, so an importer bringing in
// a decommissioned yard silently reactivates it.
//
// The "default to active unless explicitly false" rule lives at the HTTP
// boundary instead, where the request DTO carries a *bool; see
// TestCreateLocation_DefaultsToActive below.
func TestCreateLocation_MustAllowCreatingAnInactiveLocation(t *testing.T) {
	repo := newFakeRepo()
	loc := Location{Type: LocTypeBranch, Code: "OLD", Name: "Decommissioned Yard", Active: false}
	if err := NewService(repo).CreateLocation(context.Background(), &loc); err != nil {
		t.Fatalf("CreateLocation: %v", err)
	}
	if loc.Active {
		t.Error("Active = true; an explicitly inactive location was reactivated")
	}
	if repo.created[0].Active {
		t.Error("the persisted row was reactivated")
	}
}

// CORRECTNESS: a create request that does not mention "active" defaults to an
// active location — the behaviour every caller relies on — while an explicit
// `"active": false` is honoured. Both halves are asserted through the HTTP
// layer because that is the only place the two cases are distinguishable: the
// request DTO carries a *bool, the model a plain bool.
func TestCreateLocation_DefaultsToActive(t *testing.T) {
	repo := newFakeRepo()
	mux := newTestMux(repo, nil)

	rec := do(t, mux, http.MethodPost, "/api/v1/locations",
		`{"type":"BRANCH","code":"VAN","name":"Vancouver"}`)
	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201 (body %s)", rec.Code, rec.Body.String())
	}
	if len(repo.created) != 1 {
		t.Fatalf("persisted %d locations, want 1", len(repo.created))
	}
	if !repo.created[0].Active {
		t.Error("a location created with no explicit active flag was not active")
	}
	var echoed Location
	if err := json.Unmarshal(rec.Body.Bytes(), &echoed); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !echoed.Active {
		t.Error("the response did not report the location as active")
	}

	rec = do(t, mux, http.MethodPost, "/api/v1/locations",
		`{"type":"BRANCH","code":"OLD","name":"Decommissioned Yard","active":false}`)
	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201 (body %s)", rec.Code, rec.Body.String())
	}
	if len(repo.created) != 2 {
		t.Fatalf("persisted %d locations, want 2", len(repo.created))
	}
	if repo.created[1].Active {
		t.Error(`"active": false was overridden; an inactive location cannot be created`)
	}
}

// CORRECTNESS: the same omitted-vs-explicit-false rule applies to the branch
// endpoint, which is the route the admin UI actually posts to.
func TestCreateBranch_ActiveDefaultAndExplicitFalse(t *testing.T) {
	repo := newFakeRepo()
	mux := newTestMux(repo, nil)

	if rec := do(t, mux, http.MethodPost, "/api/v1/branches",
		`{"code":"VAN","name":"Vancouver"}`); rec.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201 (body %s)", rec.Code, rec.Body.String())
	}
	if rec := do(t, mux, http.MethodPost, "/api/v1/branches",
		`{"code":"OLD","name":"Closed Yard","active":false}`); rec.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201 (body %s)", rec.Code, rec.Body.String())
	}
	if len(repo.created) != 2 {
		t.Fatalf("persisted %d branches, want 2", len(repo.created))
	}
	if !repo.created[0].Active {
		t.Error("the branch created without an active flag was not active")
	}
	if repo.created[1].Active {
		t.Error(`"active": false was overridden on the branch endpoint`)
	}
}

func TestCreateLocation_PropagatesRepositoryFailure(t *testing.T) {
	repo := newFakeRepo()
	repo.err = errors.New("insert failed")
	loc := Location{Type: LocTypeBranch, Code: "VAN", Name: "Vancouver"}
	if err := NewService(repo).CreateLocation(context.Background(), &loc); err == nil {
		t.Fatal("want an error when the insert fails")
	}
}

// --- UpdateLocation validation ------------------------------------------

// CORRECTNESS: an update must identify the row it edits and keep a code. An
// update with no id would otherwise become a blind UPDATE.
func TestUpdateLocation_Validation(t *testing.T) {
	tests := []struct {
		name    string
		loc     Location
		wantErr string
	}{
		{"valid", Location{ID: uuid.New(), Code: "VAN"}, ""},
		{"no id", Location{Code: "VAN"}, "location id is required"},
		{"no code", Location{ID: uuid.New()}, "location code is required"},
		{"neither", Location{}, "location id is required"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			repo := newFakeRepo()
			loc := tc.loc
			err := NewService(repo).UpdateLocation(context.Background(), &loc)
			if tc.wantErr == "" {
				if err != nil {
					t.Fatalf("UpdateLocation: %v", err)
				}
				if len(repo.updated) != 1 {
					t.Fatalf("persisted %d updates, want 1", len(repo.updated))
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("error = %v, want it to mention %q", err, tc.wantErr)
			}
			if len(repo.updated) != 0 {
				t.Errorf("wrote %d updates despite the validation failure", len(repo.updated))
			}
		})
	}
}

// CHARACTERIZATION: UpdateLocation does not re-check the hierarchy rules that
// CreateLocation enforces. A branch can be given a parent, and a bin can have
// its parent cleared, through this path. The doc comment says type and
// parent_id "are not mutable here", but nothing in the service enforces that —
// it is left to the SQL, which this test cannot see.
func TestUpdateLocation_DoesNotRevalidateTheHierarchy(t *testing.T) {
	repo := newFakeRepo()
	parent := uuid.New()
	loc := Location{ID: uuid.New(), Code: "VAN", Type: LocTypeBranch, ParentID: &parent}

	if err := NewService(repo).UpdateLocation(context.Background(), &loc); err != nil {
		t.Fatalf("UpdateLocation rejected a parented branch (%v); if hierarchy validation has been added, this characterization test should become a rejection test", err)
	}
	if len(repo.updated) != 1 || repo.updated[0].ParentID == nil {
		t.Fatalf("the parented branch was not written through: %+v", repo.updated)
	}
}

// --- read-through methods ------------------------------------------------

// CORRECTNESS: include_inactive is a real filter and must reach the
// repository, not be swallowed — an archived branch showing up in a branch
// picker lets a user post transactions to a closed location.
func TestListBranches_PassesTheIncludeInactiveFlag(t *testing.T) {
	repo := newFakeRepo()
	repo.branches = []Location{
		{ID: uuid.New(), Type: LocTypeBranch, Code: "VAN", Name: "Vancouver", Active: true},
		{ID: uuid.New(), Type: LocTypeBranch, Code: "OLD", Name: "Closed Yard", Active: false},
	}
	svc := NewService(repo)

	active, err := svc.ListBranches(context.Background(), false)
	if err != nil {
		t.Fatalf("ListBranches: %v", err)
	}
	if len(active) != 1 || active[0].Code != "VAN" {
		t.Fatalf("got %+v, want only the active branch", active)
	}

	all, err := svc.ListBranches(context.Background(), true)
	if err != nil {
		t.Fatalf("ListBranches: %v", err)
	}
	if len(all) != 2 {
		t.Fatalf("got %d branches with include_inactive, want 2", len(all))
	}
	if len(repo.listBranchesArgs) != 2 || repo.listBranchesArgs[0] || !repo.listBranchesArgs[1] {
		t.Errorf("repository received %v, want [false true]", repo.listBranchesArgs)
	}
}

func TestGetLocation_NotFoundIsSentinel(t *testing.T) {
	svc := NewService(newFakeRepo())
	_, err := svc.GetLocation(context.Background(), uuid.New())
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("error = %v, want it to wrap ErrNotFound so the handler can map it to 404", err)
	}
}

func TestGetBranchTree_PassesTheBranchID(t *testing.T) {
	repo := newFakeRepo()
	id := uuid.New()
	repo.tree = []Location{{ID: uuid.New(), Type: LocTypeAisle, Code: "A1"}}

	got, err := NewService(repo).GetBranchTree(context.Background(), id)
	if err != nil {
		t.Fatalf("GetBranchTree: %v", err)
	}
	if repo.branchTreeArgument != id {
		t.Errorf("repository asked about %s, want %s", repo.branchTreeArgument, id)
	}
	if len(got) != 1 {
		t.Errorf("got %d nodes, want 1", len(got))
	}
}

func TestDeleteLocation_PropagatesNotFound(t *testing.T) {
	repo := newFakeRepo()
	if err := NewService(repo).DeleteLocation(context.Background(), uuid.New()); !errors.Is(err, ErrNotFound) {
		t.Fatalf("error = %v, want ErrNotFound", err)
	}

	id := uuid.New()
	repo.locations[id] = &Location{ID: id, Active: true}
	if err := NewService(repo).DeleteLocation(context.Background(), id); err != nil {
		t.Fatalf("DeleteLocation: %v", err)
	}
	if repo.locations[id].Active {
		t.Error("delete is documented as a soft archive; the row is still active")
	}
}

// --- IsBranch ------------------------------------------------------------

func TestIsBranchHelper(t *testing.T) {
	branch := Location{Type: LocTypeBranch}
	if !branch.IsBranch() {
		t.Error("a BRANCH row must report IsBranch")
	}
	for _, typ := range []LocationType{LocTypeZone, LocTypeAisle, LocTypeRack, LocTypeShelf, LocTypeBin, LocTypeYard, ""} {
		l := Location{Type: typ}
		if l.IsBranch() {
			t.Errorf("type %q reported IsBranch", typ)
		}
	}
}

// --- HTTP layer ----------------------------------------------------------

func newTestMux(repo Repository, userRepo UserRepository) *http.ServeMux {
	mux := http.NewServeMux()
	NewHandler(NewService(repo), userRepo).RegisterRoutes(mux)
	return mux
}

func do(t *testing.T, mux *http.ServeMux, method, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	var r *http.Request
	if body == "" {
		r = httptest.NewRequest(method, path, nil)
	} else {
		r = httptest.NewRequest(method, path, strings.NewReader(body))
	}
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, r)
	return rec
}

// CORRECTNESS: the branch endpoint forces Type=BRANCH and clears any parent
// the caller sent, so a client cannot create a mislabelled sub-location
// through the branch route.
func TestCreateBranch_ForcesTypeAndClearsParent(t *testing.T) {
	repo := newFakeRepo()
	mux := newTestMux(repo, nil)

	body := `{"code":"VAN","name":"Vancouver","type":"BIN","parent_id":"` + uuid.NewString() + `"}`
	rec := do(t, mux, http.MethodPost, "/api/v1/branches", body)

	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201 (body %s)", rec.Code, rec.Body.String())
	}
	if len(repo.created) != 1 {
		t.Fatalf("persisted %d locations, want 1", len(repo.created))
	}
	got := repo.created[0]
	if got.Type != LocTypeBranch {
		t.Errorf("Type = %q, want BRANCH regardless of what the client sent", got.Type)
	}
	if got.ParentID != nil {
		t.Errorf("ParentID = %v, want nil: a branch is always a root", got.ParentID)
	}
}

// CORRECTNESS: a non-branch row must not be reachable through the branch
// endpoint, and the refusal must be a 404 so the endpoint does not become a
// probe for the existence of arbitrary location ids.
func TestGetBranch_NonBranchIs404(t *testing.T) {
	repo := newFakeRepo()
	id := uuid.New()
	repo.locations[id] = &Location{ID: id, Type: LocTypeBin, Code: "B1"}

	rec := do(t, newTestMux(repo, nil), http.MethodGet, "/api/v1/branches/"+id.String(), "")
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404 for a non-branch row", rec.Code)
	}
}

func TestGetBranch_BranchIs200(t *testing.T) {
	repo := newFakeRepo()
	id := uuid.New()
	rate := 0.12
	repo.locations[id] = &Location{ID: id, Type: LocTypeBranch, Code: "VAN", Name: "Vancouver", DefaultTaxRate: &rate, Active: true}

	rec := do(t, newTestMux(repo, nil), http.MethodGet, "/api/v1/branches/"+id.String(), "")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	var got Location
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got.DefaultTaxRate == nil || *got.DefaultTaxRate != 0.12 {
		t.Errorf("DefaultTaxRate = %v, want 0.12 — invoice tax resolution reads this", got.DefaultTaxRate)
	}
}

// CORRECTNESS: a missing location is a 404, not a 500.
func TestGetLocationHandler_NotFoundIs404(t *testing.T) {
	rec := do(t, newTestMux(newFakeRepo(), nil), http.MethodGet, "/api/v1/locations/"+uuid.NewString(), "")
	if rec.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404", rec.Code)
	}
}

func TestLocationHandlers_MalformedIDs(t *testing.T) {
	mux := newTestMux(newFakeRepo(), &fakeUserRepo{})
	for _, tc := range []struct{ method, path, body string }{
		{http.MethodGet, "/api/v1/locations/not-a-uuid", ""},
		{http.MethodPut, "/api/v1/locations/not-a-uuid", `{"code":"X"}`},
		{http.MethodDelete, "/api/v1/locations/not-a-uuid", ""},
		{http.MethodGet, "/api/v1/branches/not-a-uuid", ""},
		{http.MethodGet, "/api/v1/branches/not-a-uuid/tree", ""},
		{http.MethodDelete, "/api/v1/users/someone/branches/not-a-uuid", ""},
		{http.MethodGet, "/api/v1/branches/not-a-uuid/users", ""},
	} {
		rec := do(t, mux, tc.method, tc.path, tc.body)
		if rec.Code != http.StatusBadRequest {
			t.Errorf("%s %s = %d, want 400", tc.method, tc.path, rec.Code)
		}
	}
}

// CORRECTNESS: an invalid create body must be a client error and must not
// reach the repository.
func TestCreateLocationHandler_InvalidBodyIs400(t *testing.T) {
	repo := newFakeRepo()
	mux := newTestMux(repo, nil)

	for _, body := range []string{"{not json", `{"code":"","type":"BRANCH"}`, `{"code":"B1","type":"BIN"}`} {
		rec := do(t, mux, http.MethodPost, "/api/v1/locations", body)
		if rec.Code != http.StatusBadRequest {
			t.Errorf("body %s => %d, want 400", body, rec.Code)
		}
	}
	if len(repo.created) != 0 {
		t.Errorf("persisted %d locations from invalid bodies", len(repo.created))
	}
}

// --- user-branch grants --------------------------------------------------

// CORRECTNESS: a grant must target an actual branch. Granting a user a bin
// would put a non-branch id into the branch-scoping middleware, which then
// filters every query by an id no row carries.
func TestGrantUserBranch_RejectsANonBranchTarget(t *testing.T) {
	repo := newFakeRepo()
	repo.isBranchResult = false
	userRepo := &fakeUserRepo{}
	mux := newTestMux(repo, userRepo)

	body := `{"branch_id":"` + uuid.NewString() + `"}`
	rec := do(t, mux, http.MethodPost, "/api/v1/users/auth0%7C123/branches", body)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
	if len(userRepo.grants) != 0 {
		t.Errorf("granted %d branches despite the target not being a branch", len(userRepo.grants))
	}
}

// CORRECTNESS: a valid grant records who granted it, from the auth context —
// that is the audit trail for a permission change.
func TestGrantUserBranch_RecordsTheGrantor(t *testing.T) {
	repo := newFakeRepo()
	repo.isBranchResult = true
	userRepo := &fakeUserRepo{}
	mux := newTestMux(repo, userRepo)

	branchID := uuid.New()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/users/auth0%7C123/branches",
		strings.NewReader(`{"branch_id":"`+branchID.String()+`","is_home":true}`))
	req = req.WithContext(context.WithValue(req.Context(), middleware.UserContextKey,
		&middleware.UserClaims{RegisteredClaims: jwt.RegisteredClaims{Subject: "admin-sub"}}))

	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want 204 (body %s)", rec.Code, rec.Body.String())
	}
	if len(userRepo.grants) != 1 {
		t.Fatalf("recorded %d grants, want 1", len(userRepo.grants))
	}
	g := userRepo.grants[0]
	if g.UserSub != "auth0|123" {
		t.Errorf("UserSub = %q, want the URL-decoded subject auth0|123", g.UserSub)
	}
	if g.BranchID != branchID {
		t.Errorf("BranchID = %s, want %s", g.BranchID, branchID)
	}
	if !g.IsHome {
		t.Error("IsHome was not carried through")
	}
	if g.GrantedBy != "admin-sub" {
		t.Errorf("GrantedBy = %q, want the authenticated admin's subject", g.GrantedBy)
	}
}

// CORRECTNESS: a grant with no branch id is a client error.
func TestGrantUserBranch_RequiresABranchID(t *testing.T) {
	repo := newFakeRepo()
	repo.isBranchResult = true
	userRepo := &fakeUserRepo{}
	mux := newTestMux(repo, userRepo)

	for _, body := range []string{`{}`, `{"branch_id":"00000000-0000-0000-0000-000000000000"}`, "{not json"} {
		rec := do(t, mux, http.MethodPost, "/api/v1/users/sub/branches", body)
		if rec.Code != http.StatusBadRequest {
			t.Errorf("body %s => %d, want 400", body, rec.Code)
		}
	}
	if len(userRepo.grants) != 0 {
		t.Error("an invalid grant reached the repository")
	}
}

// CORRECTNESS: empty list responses must be [] and not null across the
// user-branch endpoints, which feed pickers in the admin UI.
func TestUserBranchEndpoints_EmptyListsAreArrays(t *testing.T) {
	mux := newTestMux(newFakeRepo(), &fakeUserRepo{})

	for _, path := range []string{
		"/api/v1/users/sub/branches",
		"/api/v1/users",
		"/api/v1/branches/" + uuid.NewString() + "/users",
	} {
		rec := do(t, mux, http.MethodGet, path, "")
		if rec.Code != http.StatusOK {
			t.Fatalf("GET %s = %d, want 200", path, rec.Code)
		}
		if got := strings.TrimSpace(rec.Body.String()); got != "[]" {
			t.Errorf("GET %s body = %s, want []", path, got)
		}
	}
}

// CHARACTERIZATION: with no auth claims on the request — which is the state of
// the demo and staging deployments, where AUTH_MODE=dev leaves claims nil —
// /me/branches returns every active branch and marks the first one home. This
// is a deliberate dev-mode affordance; it is pinned so that it cannot silently
// become production behaviour.
func TestListMyBranches_UnauthenticatedReturnsAllActiveBranches(t *testing.T) {
	repo := newFakeRepo()
	repo.branches = []Location{
		{ID: uuid.New(), Type: LocTypeBranch, Code: "VAN", Name: "Vancouver", Active: true, Timezone: "America/Vancouver"},
		{ID: uuid.New(), Type: LocTypeBranch, Code: "CAL", Name: "Calgary", Active: true},
		{ID: uuid.New(), Type: LocTypeBranch, Code: "OLD", Name: "Closed", Active: false},
	}
	userRepo := &fakeUserRepo{branches: []BranchSummary{{Code: "SHOULD-NOT-BE-USED"}}}

	rec := do(t, newTestMux(repo, userRepo), http.MethodGet, "/api/v1/me/branches", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	var got []BranchSummary
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("got %d branches, want the 2 active ones", len(got))
	}
	if !got[0].IsHome {
		t.Error("the first branch was not flagged home")
	}
	if got[1].IsHome {
		t.Error("more than one branch was flagged home")
	}
	if got[0].Timezone != "America/Vancouver" {
		t.Errorf("Timezone = %q, want it carried into the summary", got[0].Timezone)
	}
}

// CORRECTNESS: with claims present, the grants table is authoritative and the
// "all branches" fallback must not fire.
func TestListMyBranches_AuthenticatedUsesTheGrantsTable(t *testing.T) {
	repo := newFakeRepo()
	repo.branches = []Location{{ID: uuid.New(), Type: LocTypeBranch, Code: "SHOULD-NOT-APPEAR", Active: true}}
	granted := uuid.New()
	userRepo := &fakeUserRepo{branches: []BranchSummary{{ID: granted, Code: "VAN", Name: "Vancouver", Active: true, IsHome: true}}}

	req := httptest.NewRequest(http.MethodGet, "/api/v1/me/branches", nil)
	req = req.WithContext(context.WithValue(req.Context(), middleware.UserContextKey,
		&middleware.UserClaims{RegisteredClaims: jwt.RegisteredClaims{Subject: "user-1"}}))

	rec := httptest.NewRecorder()
	newTestMux(repo, userRepo).ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	var got []BranchSummary
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(got) != 1 || got[0].ID != granted {
		t.Fatalf("got %+v, want only the granted branch", got)
	}
}

// CORRECTNESS: the user-branch routes only exist when a user repository was
// supplied. Registering them against a nil repository would panic on the first
// request.
func TestRegisterRoutes_UserRoutesRequireAUserRepository(t *testing.T) {
	mux := newTestMux(newFakeRepo(), nil)

	rec := do(t, mux, http.MethodGet, "/api/v1/me/branches", "")
	if rec.Code != http.StatusNotFound {
		t.Errorf("GET /api/v1/me/branches = %d, want 404 when no user repository is wired", rec.Code)
	}
}

// CORRECTNESS: admin-only routes must go through the admin guards, and the
// non-admin routes through the ordinary guard. Mixing them up would let a
// warehouse user rewrite the branch hierarchy.
func TestRegisterRoutes_AdminGuardCoversMutatingBranchRoutes(t *testing.T) {
	var adminHits int
	adminGuard := func(http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			adminHits++
			w.WriteHeader(http.StatusForbidden)
		})
	}
	roleGuard := func(next http.Handler) http.Handler { return next }

	mux := http.NewServeMux()
	NewHandler(NewService(newFakeRepo()), &fakeUserRepo{}, adminGuard).RegisterRoutes(mux, roleGuard)

	id := uuid.NewString()
	adminOnly := []struct{ method, path string }{
		{http.MethodPut, "/api/v1/locations/" + id},
		{http.MethodDelete, "/api/v1/locations/" + id},
		{http.MethodPost, "/api/v1/branches"},
		{http.MethodPut, "/api/v1/branches/" + id},
		{http.MethodDelete, "/api/v1/branches/" + id},
		{http.MethodGet, "/api/v1/users"},
		{http.MethodGet, "/api/v1/users/sub/branches"},
		{http.MethodPost, "/api/v1/users/sub/branches"},
		{http.MethodDelete, "/api/v1/users/sub/branches/" + id},
		{http.MethodPut, "/api/v1/users/sub/home-branch"},
		{http.MethodGet, "/api/v1/branches/" + id + "/users"},
	}

	for _, r := range adminOnly {
		rec := do(t, mux, r.method, r.path, "{}")
		if rec.Code != http.StatusForbidden {
			t.Errorf("%s %s = %d, want 403: this route is not behind the admin guard", r.method, r.path, rec.Code)
		}
	}
	if adminHits != len(adminOnly) {
		t.Errorf("admin guard ran %d times, want %d", adminHits, len(adminOnly))
	}

	// Read-only routes must NOT be behind the admin guard.
	before := adminHits
	for _, r := range []struct{ method, path string }{
		{http.MethodGet, "/api/v1/locations"},
		{http.MethodGet, "/api/v1/branches"},
		{http.MethodGet, "/api/v1/me/branches"},
	} {
		rec := do(t, mux, r.method, r.path, "")
		if rec.Code == http.StatusForbidden {
			t.Errorf("%s %s was blocked by the admin guard; it should be readable by any authenticated role", r.method, r.path)
		}
	}
	if adminHits != before {
		t.Errorf("the admin guard ran on a read-only route")
	}
}
