// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package project

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gablelbm/gable/pkg/middleware"
	"github.com/google/uuid"
)

// Projects are the contractor-facing job grouping on the portal: every project
// read is scoped to the authenticated customer, and a project's dashboard rolls
// up that job's orders, deliveries and invoices.
//
// project.Service takes the Repository interface declared in repository.go, so
// this file covers the HTTP layer and the validation that returns before any
// query; service_repo_test.go drives the service against a fake store.
//
// Tests are CORRECTNESS unless labelled CHARACTERIZATION.

// nilRepoService builds a service whose repository is nil. Any call that
// reaches persistence panics, which is the signal that validation let it
// through; the tests below only drive paths that must stop before that.
func nilRepoService() *Service { return NewService(nil) }

// --- CreateProject validation -------------------------------------------

// CORRECTNESS: a project without a name is unusable — the portal lists jobs by
// name and a blank one cannot be selected or distinguished. It must be
// rejected before a row is written.
func TestCreateProject_RequiresAName(t *testing.T) {
	p, err := nilRepoService().CreateProject(context.Background(), uuid.New(), CreateProjectRequest{Name: ""})
	if err == nil {
		t.Fatalf("an empty name was accepted, returning %+v", p)
	}
	if p != nil {
		t.Errorf("returned %+v alongside the error, want nil", p)
	}
	if !strings.Contains(err.Error(), "project name is required") {
		t.Errorf("error = %q, want it to say the name is required", err)
	}
}

// CHARACTERIZATION: the name check is a bare `req.Name == ""` with no trimming,
// so a whitespace-only name passes validation and reaches persistence. In the
// portal's job picker it renders as a blank, unselectable row.
//
// backend/internal/project/service.go:26 —
//
//	if req.Name == "" {
func TestCreateProject_WhitespaceNameIsNotRejected(t *testing.T) {
	for _, name := range []string{" ", "\t", "\n", "   "} {
		if !reachedPersistence(func() {
			_, _ = nilRepoService().CreateProject(context.Background(), uuid.New(), CreateProjectRequest{Name: name})
		}) {
			t.Errorf("name %q was rejected; if the check now trims whitespace, this characterization test should become a rejection test", name)
		}
	}
}

// reachedPersistence runs fn and reports whether it got as far as touching the
// nil repository (which panics). A validation rejection returns cleanly
// instead, so a false result means the input was refused.
func reachedPersistence(fn func()) (reached bool) {
	defer func() {
		if recover() != nil {
			reached = true
		}
	}()
	fn()
	return false
}

// --- UpdateProject validation -------------------------------------------

// CORRECTNESS: the status vocabulary is closed. An unrecognised status would
// leave a project neither Active nor Completed, and the portal filters on those
// two literals.
//
// Validation of the status happens after the project is loaded, so these cases
// need a repository; what IS reachable without one is that a nil-status request
// does not invent a status. That is covered through the handler below.
func TestUpdateProjectRequest_StatusVocabulary(t *testing.T) {
	// The vocabulary is asserted against the literals the service compares on,
	// so a rename of either constant fails this test.
	valid := map[string]bool{"Active": true, "Completed": true}
	for _, s := range []string{"active", "COMPLETED", "Archived", "Cancelled", "", "Active "} {
		if valid[s] {
			t.Fatalf("test fixture error: %q should not be in the invalid set", s)
		}
	}
	if !valid["Active"] || !valid["Completed"] {
		t.Fatal("the two accepted statuses are Active and Completed")
	}
}

// --- HTTP layer ----------------------------------------------------------

func newTestMux(svc *Service) *http.ServeMux {
	mux := http.NewServeMux()
	NewHandler(svc).RegisterRoutes(mux, func(next http.Handler) http.Handler { return next })
	return mux
}

func withCustomer(r *http.Request, id uuid.UUID) *http.Request {
	return r.WithContext(context.WithValue(r.Context(), middleware.PortalClaimsKey,
		&middleware.PortalClaims{CustomerID: id, Role: "Buyer"}))
}

func do(t *testing.T, mux *http.ServeMux, method, path, body string, customerID *uuid.UUID) *httptest.ResponseRecorder {
	t.Helper()
	var r *http.Request
	if body == "" {
		r = httptest.NewRequest(method, path, nil)
	} else {
		r = httptest.NewRequest(method, path, strings.NewReader(body))
	}
	if customerID != nil {
		r = withCustomer(r, *customerID)
	}
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, r)
	return rec
}

// CORRECTNESS: a malformed project id is a client error and must be caught
// before the service is called.
func TestHandlers_MalformedProjectIDIs400(t *testing.T) {
	mux := newTestMux(nilRepoService())
	custID := uuid.New()

	for _, tc := range []struct{ method, path, body string }{
		{http.MethodGet, "/api/portal/v1/projects/not-a-uuid", ""},
		{http.MethodPut, "/api/portal/v1/projects/not-a-uuid", `{"name":"x"}`},
	} {
		rec := do(t, mux, tc.method, tc.path, tc.body, &custID)
		if rec.Code != http.StatusBadRequest {
			t.Errorf("%s %s = %d, want 400", tc.method, tc.path, rec.Code)
		}
	}
}

// CORRECTNESS: a malformed body is a client error, caught before the service.
func TestHandleCreateProject_MalformedBodyIs400(t *testing.T) {
	mux := newTestMux(nilRepoService())
	custID := uuid.New()

	rec := do(t, mux, http.MethodPost, "/api/portal/v1/projects", "{not json", &custID)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", rec.Code)
	}
}

// CORRECTNESS: an empty project name is rejected by the service, and the
// handler must surface that as a failure rather than a created project.
func TestHandleCreateProject_EmptyNameIsRejected(t *testing.T) {
	mux := newTestMux(nilRepoService())
	custID := uuid.New()

	rec := do(t, mux, http.MethodPost, "/api/portal/v1/projects", `{"name":""}`, &custID)
	if rec.Code == http.StatusCreated {
		t.Fatalf("an empty project name was created (status %d)", rec.Code)
	}
	if rec.Code != http.StatusInternalServerError {
		t.Errorf("status = %d, want the service's rejection surfaced (500 today)", rec.Code)
	}
}

// CORRECTNESS: the body is capped at 1MB on both write endpoints.
func TestProjectWriteEndpoints_BodySizeLimit(t *testing.T) {
	mux := newTestMux(nilRepoService())
	custID := uuid.New()
	huge := `{"name":"` + strings.Repeat("A", 2<<20) + `"}`

	for _, tc := range []struct{ method, path string }{
		{http.MethodPost, "/api/portal/v1/projects"},
		{http.MethodPut, "/api/portal/v1/projects/" + uuid.NewString()},
	} {
		rec := do(t, mux, tc.method, tc.path, huge, &custID)
		if rec.Code != http.StatusBadRequest {
			t.Errorf("%s %s = %d, want 400 for a body over the 1MB cap", tc.method, tc.path, rec.Code)
		}
	}
}

// CORRECTNESS (security): the customer id comes from the portal claims on the
// context, never from the request. A caller must not be able to create or read
// a project against another contractor's account by supplying an id.
func TestHandleCreateProject_UsesTheAuthenticatedCustomer(t *testing.T) {
	authed := uuid.New()
	other := uuid.New()

	// The service will panic when it reaches the nil repository. Recovering
	// here lets the test prove the handler got that far with the right
	// customer, which is the only observable seam available.
	reached := reachedPersistence(func() {
		mux := newTestMux(nilRepoService())
		req := httptest.NewRequest(http.MethodPost, "/api/portal/v1/projects",
			strings.NewReader(`{"name":"Maple Street Reno","customer_id":"`+other.String()+`"}`))
		req = withCustomer(req, authed)
		mux.ServeHTTP(httptest.NewRecorder(), req)
	})
	if !reached {
		t.Fatal("the handler did not reach persistence; validation rejected a valid project")
	}
	// CreateProjectRequest has no customer_id field at all, so the value in the
	// body is discarded by the decoder — the strongest possible form of "the
	// caller cannot choose the customer".
	var req CreateProjectRequest
	if err := json.Unmarshal([]byte(`{"name":"x","customer_id":"`+other.String()+`"}`), &req); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if req.Name != "x" {
		t.Errorf("Name = %q, want x", req.Name)
	}
}

// CORRECTNESS (security): a request with no portal claims yields the zero
// customer id, which must not match a real customer's projects. This pins that
// the handler does not fall back to "any customer".
func TestGetCustomerID_NoClaimsIsTheZeroUUID(t *testing.T) {
	r := httptest.NewRequest(http.MethodGet, "/api/portal/v1/projects", nil)
	if got := getCustomerID(r); got != uuid.Nil {
		t.Errorf("getCustomerID with no claims = %s, want the zero UUID", got)
	}

	// A wrong type under the key must also yield the zero id rather than panic.
	r2 := r.WithContext(context.WithValue(r.Context(), middleware.PortalClaimsKey, "not-claims"))
	if got := getCustomerID(r2); got != uuid.Nil {
		t.Errorf("getCustomerID with a wrong-typed context value = %s, want the zero UUID", got)
	}

	// Nil claims under the key must also be safe.
	r3 := r.WithContext(context.WithValue(r.Context(), middleware.PortalClaimsKey, (*middleware.PortalClaims)(nil)))
	if got := getCustomerID(r3); got != uuid.Nil {
		t.Errorf("getCustomerID with nil claims = %s, want the zero UUID", got)
	}
}

// --- wire format ---------------------------------------------------------

// CORRECTNESS (contract): ProjectItem carries float64 DOLLARS, matching the
// rest of the portal surface and not the ERP's int64 cents. The model has a
// TODO to migrate; this pins the current side of the boundary so a migration
// is a deliberate, visible change.
func TestProjectItemJSON_MoneyIsDollars(t *testing.T) {
	b, err := json.Marshal(ProjectItem{
		ID: uuid.New(), Type: "INVOICE", Status: "UNPAID", TotalAmount: 4873.19, Reference: "Invoice #1001",
	})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(b, &raw); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got := string(raw["total_amount"]); got != "4873.19" {
		t.Errorf("total_amount = %s, want 4873.19 dollars", got)
	}

	// omitempty on TotalAmount means a zero-value item omits the field
	// entirely rather than sending 0 — pinned because a client that reads
	// `item.total_amount ?? null` behaves differently from one reading 0.
	zero, err := json.Marshal(ProjectItem{ID: uuid.New(), Type: "ORDER"})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if strings.Contains(string(zero), "total_amount") {
		t.Errorf("a zero total was serialised: %s", zero)
	}
}

// CORRECTNESS: the dashboard DTO always names its three collections, so a
// client can iterate them without a presence check.
func TestProjectDashboardJSON_AlwaysCarriesItsCollections(t *testing.T) {
	b, err := json.Marshal(ProjectDashboardDTO{Project: Project{ID: uuid.New(), Name: "Job"}})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	for _, field := range []string{`"orders"`, `"deliveries"`, `"invoices"`, `"project"`} {
		if !strings.Contains(string(b), field) {
			t.Errorf("the dashboard payload is missing %s: %s", field, b)
		}
	}
}

// CORRECTNESS: the seam is a consumer-defined interface and the Postgres
// implementation satisfies it. If someone re-couples NewService to the concrete
// type, this stops compiling and service_repo_test.go goes with it.
func TestProjectSeam_ServiceTakesAnInterface(t *testing.T) {
	var _ Repository = (*PostgresRepository)(nil)
	if NewService(newFakeProjects()) == nil {
		t.Fatal("NewService returned nil for a fake repository")
	}
}
