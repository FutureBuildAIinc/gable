// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package project

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
)

// These are the tests the concrete-*Repository gap used to block. Service now
// takes the Repository interface declared in repository.go, so the load-modify-
// save half of UpdateProject, the dashboard assembly and the customer scoping
// the service hands down to the WHERE clause are all reachable.
//
// The scoping itself is enforced in SQL (`WHERE id = $1 AND customer_id = $2`);
// what these tests prove is that the service passes the session's customer down
// unchanged on every path, which is the half a fake can see. A service that
// dropped or swapped the customer id would still return a plausible project.
//
// Tests are CORRECTNESS unless labelled CHARACTERIZATION.

// getCall records the (projectID, customerID) pair a scoped repository read was
// invoked with, so a test can assert the customer scope rather than only the
// answer.
type getCall struct{ projectID, customerID uuid.UUID }

// String keeps failure messages readable — uuid.UUID is a [16]byte array, so
// the default %v formatting prints the raw bytes.
func (g getCall) String() string {
	return "{project:" + g.projectID.String() + " customer:" + g.customerID.String() + "}"
}

// fakeProjects is an in-memory Repository.
type fakeProjects struct {
	projects map[uuid.UUID]Project

	orders     []ProjectItem
	deliveries []ProjectItem
	invoices   []ProjectItem

	createErr   error
	getErr      error
	listErr     error
	updateErr   error
	entitiesErr error

	created      []Project
	updated      []Project
	listedFor    []uuid.UUID
	gets         []getCall
	entityLookup []getCall
}

var _ Repository = (*fakeProjects)(nil)

func newFakeProjects() *fakeProjects {
	return &fakeProjects{projects: map[uuid.UUID]Project{}}
}

func (f *fakeProjects) CreateProject(_ context.Context, p Project) error {
	if f.createErr != nil {
		return f.createErr
	}
	f.created = append(f.created, p)
	f.projects[p.ID] = p
	return nil
}

func (f *fakeProjects) GetProject(_ context.Context, id, customerID uuid.UUID) (*Project, error) {
	f.gets = append(f.gets, getCall{id, customerID})
	if f.getErr != nil {
		return nil, f.getErr
	}
	// Mirror the SQL: the row is only visible to its owner.
	p, ok := f.projects[id]
	if !ok || p.CustomerID != customerID {
		return nil, errors.New("project not found")
	}
	return &p, nil
}

func (f *fakeProjects) ListProjects(_ context.Context, customerID uuid.UUID) ([]Project, error) {
	f.listedFor = append(f.listedFor, customerID)
	if f.listErr != nil {
		return nil, f.listErr
	}
	out := make([]Project, 0)
	for _, p := range f.projects {
		if p.CustomerID == customerID {
			out = append(out, p)
		}
	}
	return out, nil
}

func (f *fakeProjects) UpdateProject(_ context.Context, p Project) error {
	f.updated = append(f.updated, p)
	if f.updateErr != nil {
		return f.updateErr
	}
	f.projects[p.ID] = p
	return nil
}

func (f *fakeProjects) GetProjectEntities(_ context.Context, projectID, customerID uuid.UUID) ([]ProjectItem, []ProjectItem, []ProjectItem, error) {
	f.entityLookup = append(f.entityLookup, getCall{projectID, customerID})
	if f.entitiesErr != nil {
		return nil, nil, nil, f.entitiesErr
	}
	return f.orders, f.deliveries, f.invoices, nil
}

// --- CreateProject -------------------------------------------------------

// CORRECTNESS: a new project belongs to the session's customer and starts
// Active. The status is not caller-supplied — CreateProjectRequest has no
// status field — so "Active" being written here is the invariant the update
// whitelist then defends.
func TestCreateProject_WritesTheSessionCustomerAndStartsActive(t *testing.T) {
	repo := newFakeProjects()
	customer := uuid.New()

	before := time.Now().Add(-time.Second)
	p, err := NewService(repo).CreateProject(context.Background(), customer, CreateProjectRequest{Name: "Maple Street"})
	if err != nil {
		t.Fatalf("CreateProject: %v", err)
	}
	after := time.Now().Add(time.Second)

	if len(repo.created) != 1 {
		t.Fatalf("CreateProject reached persistence %d times, want 1", len(repo.created))
	}
	stored := repo.created[0]

	if stored.CustomerID != customer {
		t.Errorf("persisted customer_id = %s, want the session customer %s", stored.CustomerID, customer)
	}
	if stored.Status != "Active" {
		t.Errorf("persisted status = %q, want Active", stored.Status)
	}
	if stored.Name != "Maple Street" {
		t.Errorf("persisted name = %q, want Maple Street", stored.Name)
	}
	if stored.ID == uuid.Nil {
		t.Error("the project was persisted without an id")
	}
	if stored.CreatedAt.Before(before) || stored.CreatedAt.After(after) {
		t.Errorf("created_at = %s, want roughly now", stored.CreatedAt)
	}

	// The returned project must be the one that was written, not a copy that
	// diverged: the handler echoes it back as the 201 body.
	if p.ID != stored.ID || p.CustomerID != stored.CustomerID || p.Status != stored.Status {
		t.Errorf("returned %+v, want the persisted %+v", *p, stored)
	}
}

// CORRECTNESS: a persistence failure is surfaced, not swallowed into a
// plausible-looking project the client would then fail to fetch.
func TestCreateProject_PersistenceFailureIsReturned(t *testing.T) {
	repo := newFakeProjects()
	repo.createErr = errors.New("failed to create project")

	p, err := NewService(repo).CreateProject(context.Background(), uuid.New(), CreateProjectRequest{Name: "Maple Street"})
	if err == nil {
		t.Fatalf("a persistence failure was swallowed, returning %+v", p)
	}
	if p != nil {
		t.Errorf("returned %+v alongside the error, want nil", p)
	}
}

// --- ListProjects --------------------------------------------------------

// CORRECTNESS: the list is scoped to the session's customer. This is the whole
// tenancy story for the project board.
func TestListProjects_PassesTheSessionCustomerDown(t *testing.T) {
	repo := newFakeProjects()
	mine, theirs := uuid.New(), uuid.New()

	a := Project{ID: uuid.New(), CustomerID: mine, Name: "Mine", Status: "Active"}
	b := Project{ID: uuid.New(), CustomerID: theirs, Name: "Theirs", Status: "Active"}
	repo.projects[a.ID] = a
	repo.projects[b.ID] = b

	got, err := NewService(repo).ListProjects(context.Background(), mine)
	if err != nil {
		t.Fatalf("ListProjects: %v", err)
	}
	if len(repo.listedFor) != 1 || repo.listedFor[0] != mine {
		t.Fatalf("queried %v, want the session customer %s", repo.listedFor, mine)
	}
	if len(got) != 1 || got[0].ID != a.ID {
		t.Errorf("got %+v, want only the session customer's project", got)
	}
}

// --- GetProjectDashboard -------------------------------------------------

// CORRECTNESS: the dashboard is the project plus its three collections, and
// BOTH reads are scoped by (projectID, customerID). Scoping the header read but
// not the entity read would leak another contractor's orders under a project
// header the caller does own.
func TestGetProjectDashboard_ScopesBothReadsAndAssemblesTheDTO(t *testing.T) {
	repo := newFakeProjects()
	customer, projectID := uuid.New(), uuid.New()
	repo.projects[projectID] = Project{ID: projectID, CustomerID: customer, Name: "Maple Street", Status: "Active"}
	repo.orders = []ProjectItem{{ID: uuid.New(), Type: "ORDER", Status: "CONFIRMED", TotalAmount: 1250.75}}
	repo.deliveries = []ProjectItem{{ID: uuid.New(), Type: "DELIVERY", Status: "SCHEDULED"}}
	repo.invoices = []ProjectItem{{ID: uuid.New(), Type: "INVOICE", Status: "OPEN", TotalAmount: 1250.75}}

	dash, err := NewService(repo).GetProjectDashboard(context.Background(), projectID, customer)
	if err != nil {
		t.Fatalf("GetProjectDashboard: %v", err)
	}

	if len(repo.gets) != 1 || repo.gets[0] != (getCall{projectID, customer}) {
		t.Errorf("GetProject called with %v, want {%s %s}", repo.gets, projectID, customer)
	}
	if len(repo.entityLookup) != 1 || repo.entityLookup[0] != (getCall{projectID, customer}) {
		t.Errorf("GetProjectEntities called with %v, want {%s %s}", repo.entityLookup, projectID, customer)
	}

	if dash.Project.ID != projectID || dash.Project.Name != "Maple Street" {
		t.Errorf("header = %+v, want the stored project", dash.Project)
	}
	if len(dash.Orders) != 1 || len(dash.Deliveries) != 1 || len(dash.Invoices) != 1 {
		t.Errorf("collections = %d orders / %d deliveries / %d invoices, want 1 each",
			len(dash.Orders), len(dash.Deliveries), len(dash.Invoices))
	}
	if dash.Orders[0].TotalAmount != 1250.75 {
		t.Errorf("order total = %v, want the repository's 1250.75 dollars", dash.Orders[0].TotalAmount)
	}
}

// CORRECTNESS (security): another customer's project is not found, and the
// entity read is never issued. Issuing it anyway would be a timing/behaviour
// oracle for which project ids exist.
func TestGetProjectDashboard_ForeignProjectStopsBeforeTheEntityRead(t *testing.T) {
	repo := newFakeProjects()
	owner, intruder, projectID := uuid.New(), uuid.New(), uuid.New()
	repo.projects[projectID] = Project{ID: projectID, CustomerID: owner, Name: "Maple Street", Status: "Active"}

	dash, err := NewService(repo).GetProjectDashboard(context.Background(), projectID, intruder)
	if err == nil {
		t.Fatalf("another customer's project was served: %+v", dash)
	}
	if dash != nil {
		t.Errorf("returned %+v alongside the error, want nil", dash)
	}
	if len(repo.entityLookup) != 0 {
		t.Errorf("the entity read ran anyway: %v", repo.entityLookup)
	}
}

// CORRECTNESS: a failure loading the collections fails the dashboard rather
// than serving a project with silently empty orders, deliveries and invoices.
func TestGetProjectDashboard_EntityFailureIsFatal(t *testing.T) {
	repo := newFakeProjects()
	customer, projectID := uuid.New(), uuid.New()
	repo.projects[projectID] = Project{ID: projectID, CustomerID: customer, Name: "Maple Street", Status: "Active"}
	repo.entitiesErr = errors.New("failed to fetch orders")

	dash, err := NewService(repo).GetProjectDashboard(context.Background(), projectID, customer)
	if err == nil {
		t.Fatalf("a collection failure was served as an empty dashboard: %+v", dash)
	}
	if dash != nil {
		t.Errorf("returned %+v alongside the error, want nil", dash)
	}
}

// --- UpdateProject -------------------------------------------------------

// CORRECTNESS: only Active and Completed may be written. Anything else is
// rejected before the write, so the status column stays a closed vocabulary the
// portal's board can group on.
func TestUpdateProject_StatusWhitelist(t *testing.T) {
	customer, projectID := uuid.New(), uuid.New()

	for _, status := range []string{"Active", "Completed"} {
		t.Run("accepts "+status, func(t *testing.T) {
			repo := newFakeProjects()
			repo.projects[projectID] = Project{ID: projectID, CustomerID: customer, Name: "Maple Street", Status: "Active"}

			p, err := NewService(repo).UpdateProject(context.Background(), projectID, customer,
				UpdateProjectRequest{Status: &status})
			if err != nil {
				t.Fatalf("UpdateProject(%q): %v", status, err)
			}
			if p.Status != status {
				t.Errorf("returned status = %q, want %q", p.Status, status)
			}
			if len(repo.updated) != 1 || repo.updated[0].Status != status {
				t.Errorf("persisted %+v, want status %q", repo.updated, status)
			}
		})
	}

	for _, status := range []string{"", "active", "ACTIVE", "Complete", "Cancelled", "On Hold", "Archived", "Active "} {
		t.Run("rejects "+status, func(t *testing.T) {
			repo := newFakeProjects()
			repo.projects[projectID] = Project{ID: projectID, CustomerID: customer, Name: "Maple Street", Status: "Active"}

			s := status
			p, err := NewService(repo).UpdateProject(context.Background(), projectID, customer,
				UpdateProjectRequest{Status: &s})
			if err == nil {
				t.Fatalf("status %q was accepted, returning %+v", status, p)
			}
			if len(repo.updated) != 0 {
				t.Errorf("status %q reached persistence: %+v", status, repo.updated)
			}
		})
	}
}

// CORRECTNESS: the update is a load-modify-save, so a request that names only
// one field must leave the other alone.
func TestUpdateProject_PartialUpdatesLeaveTheOtherFieldAlone(t *testing.T) {
	customer, projectID := uuid.New(), uuid.New()
	original := Project{ID: projectID, CustomerID: customer, Name: "Maple Street", Status: "Active"}

	t.Run("name only", func(t *testing.T) {
		repo := newFakeProjects()
		repo.projects[projectID] = original
		name := "Maple Street Phase 2"

		p, err := NewService(repo).UpdateProject(context.Background(), projectID, customer,
			UpdateProjectRequest{Name: &name})
		if err != nil {
			t.Fatalf("UpdateProject: %v", err)
		}
		if p.Name != name {
			t.Errorf("name = %q, want %q", p.Name, name)
		}
		if p.Status != "Active" {
			t.Errorf("status = %q, want the untouched Active", p.Status)
		}
	})

	t.Run("status only", func(t *testing.T) {
		repo := newFakeProjects()
		repo.projects[projectID] = original
		status := "Completed"

		p, err := NewService(repo).UpdateProject(context.Background(), projectID, customer,
			UpdateProjectRequest{Status: &status})
		if err != nil {
			t.Fatalf("UpdateProject: %v", err)
		}
		if p.Name != "Maple Street" {
			t.Errorf("name = %q, want the untouched Maple Street", p.Name)
		}
		if p.Status != "Completed" {
			t.Errorf("status = %q, want Completed", p.Status)
		}
	})

	t.Run("neither", func(t *testing.T) {
		repo := newFakeProjects()
		repo.projects[projectID] = original

		p, err := NewService(repo).UpdateProject(context.Background(), projectID, customer, UpdateProjectRequest{})
		if err != nil {
			t.Fatalf("UpdateProject: %v", err)
		}
		if p.Name != original.Name || p.Status != original.Status {
			t.Errorf("an empty request changed the project: %+v", *p)
		}
	})
}

// CORRECTNESS (security): the Project handed to the UPDATE carries the session's
// customer id, which is what makes `WHERE id = $3 AND customer_id = $4` a real
// scope. If the service ever wrote back the id it read off the request body, the
// WHERE would be self-satisfying.
func TestUpdateProject_PersistsWithTheSessionCustomerScope(t *testing.T) {
	repo := newFakeProjects()
	customer, projectID := uuid.New(), uuid.New()
	repo.projects[projectID] = Project{ID: projectID, CustomerID: customer, Name: "Maple Street", Status: "Active"}

	status := "Completed"
	if _, err := NewService(repo).UpdateProject(context.Background(), projectID, customer,
		UpdateProjectRequest{Status: &status}); err != nil {
		t.Fatalf("UpdateProject: %v", err)
	}

	if len(repo.updated) != 1 {
		t.Fatalf("UpdateProject reached persistence %d times, want 1", len(repo.updated))
	}
	if repo.updated[0].CustomerID != customer {
		t.Errorf("wrote with customer scope %s, want the session's %s", repo.updated[0].CustomerID, customer)
	}
	if repo.updated[0].ID != projectID {
		t.Errorf("wrote project %s, want %s", repo.updated[0].ID, projectID)
	}
}

// CORRECTNESS (security): another customer's project cannot be updated, and the
// write is never attempted.
func TestUpdateProject_ForeignProjectNeverReachesTheWrite(t *testing.T) {
	repo := newFakeProjects()
	owner, intruder, projectID := uuid.New(), uuid.New(), uuid.New()
	repo.projects[projectID] = Project{ID: projectID, CustomerID: owner, Name: "Maple Street", Status: "Active"}

	status := "Completed"
	p, err := NewService(repo).UpdateProject(context.Background(), projectID, intruder,
		UpdateProjectRequest{Status: &status})
	if err == nil {
		t.Fatalf("another customer's project was updated: %+v", p)
	}
	if len(repo.updated) != 0 {
		t.Errorf("the write was attempted anyway: %+v", repo.updated)
	}
}

// CORRECTNESS: a project cannot be renamed to nothing. CreateProject refuses an
// empty name at service.go:26-28 because the portal's job picker lists projects
// by name and a blank row cannot be selected or told apart from its neighbours;
// UpdateProject has to hold the same invariant, or the rule is only enforced
// for the first second of a project's life.
//
// `{"name":""}` is a non-nil pointer to the empty string, so UpdateProject used
// to assign it straight through and leave the project nameless.
func TestUpdateProject_RejectsAnEmptyName(t *testing.T) {
	repo := newFakeProjects()
	customer, projectID := uuid.New(), uuid.New()
	repo.projects[projectID] = Project{ID: projectID, CustomerID: customer, Name: "Maple Street", Status: "Active"}

	blank := ""
	p, err := NewService(repo).UpdateProject(context.Background(), projectID, customer,
		UpdateProjectRequest{Name: &blank})
	if err == nil {
		t.Fatalf("an empty name was accepted, returning %+v", p)
	}
	if len(repo.updated) != 0 {
		t.Errorf("the blank name reached persistence: %+v", repo.updated)
	}
}

// CORRECTNESS: the inverse of the characterization that used to pin the
// blanking above — a name that is merely OMITTED still leaves the stored one
// alone. The rejection must be of the empty string, not of the update path: a
// PUT that only changes the status must keep working.
func TestUpdateProject_OmittedNameLeavesTheStoredOneAlone(t *testing.T) {
	repo := newFakeProjects()
	customer, projectID := uuid.New(), uuid.New()
	repo.projects[projectID] = Project{ID: projectID, CustomerID: customer, Name: "Maple Street", Status: "Active"}

	status := "Completed"
	p, err := NewService(repo).UpdateProject(context.Background(), projectID, customer,
		UpdateProjectRequest{Status: &status})
	if err != nil {
		t.Fatalf("UpdateProject: %v", err)
	}
	if p.Name != "Maple Street" {
		t.Errorf("name = %q, want the stored %q — a nil Name means 'unchanged', not 'blank'", p.Name, "Maple Street")
	}
	if len(repo.updated) != 1 || repo.updated[0].Name != "Maple Street" {
		t.Errorf("persisted %+v, want the stored name carried through", repo.updated)
	}
}

// CHARACTERIZATION: the create-side name check is `req.Name == ""` with no
// trimming, and the update side is now the same bare comparison, so a
// whitespace name still gets through both. Same rendering problem as the blank
// name, one step removed — and it is deliberately still the SAME rule on both
// sides, so the two cannot drift apart again.
func TestUpdateProject_WhitespaceNameIsNotRejected(t *testing.T) {
	repo := newFakeProjects()
	customer, projectID := uuid.New(), uuid.New()
	repo.projects[projectID] = Project{ID: projectID, CustomerID: customer, Name: "Maple Street", Status: "Active"}

	ws := "   "
	p, err := NewService(repo).UpdateProject(context.Background(), projectID, customer,
		UpdateProjectRequest{Name: &ws})
	if err != nil {
		t.Fatalf("UpdateProject: %v", err)
	}
	if p.Name != ws {
		t.Errorf("name = %q, want the untrimmed %q (this test documents the missing trim)", p.Name, ws)
	}
}

// CORRECTNESS: a write failure is returned rather than reported as a successful
// update the client then renders.
func TestUpdateProject_WriteFailureIsReturned(t *testing.T) {
	repo := newFakeProjects()
	customer, projectID := uuid.New(), uuid.New()
	repo.projects[projectID] = Project{ID: projectID, CustomerID: customer, Name: "Maple Street", Status: "Active"}
	repo.updateErr = errors.New("project not found")

	status := "Completed"
	p, err := NewService(repo).UpdateProject(context.Background(), projectID, customer,
		UpdateProjectRequest{Status: &status})
	if err == nil {
		t.Fatalf("a write failure was swallowed, returning %+v", p)
	}
	if p != nil {
		t.Errorf("returned %+v alongside the error, want nil", p)
	}
}
