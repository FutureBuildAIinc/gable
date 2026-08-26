// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package crm

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
)

// These are the tests the concrete-*Repository gap used to block. Handler now
// takes the Repository interface declared in activity.go, so every CRUD route
// can be driven end to end against a fake store: which customer id reaches
// persistence, which activity id is written, what the client gets back.
//
// The persistence rules that live inside the Postgres implementation — the
// default-ActivityDate-to-now rule at activity.go:74-76 and the zero-rows
// "not found" mapping — are covered in repository_pg_test.go.
//
// Tests are CORRECTNESS unless labelled CHARACTERIZATION.

// fakeActivities is an in-memory Repository that records every call, so a test
// can assert on the values the handler chose rather than only on the response.
type fakeActivities struct {
	stored map[uuid.UUID]*Activity
	list   []Activity

	createErr error
	getErr    error
	listErr   error
	updateErr error
	deleteErr error

	created     []Activity
	updated     []Activity
	deleted     []uuid.UUID
	listedFor   []uuid.UUID
	fetchedByID []uuid.UUID
}

var _ Repository = (*fakeActivities)(nil)

func newFakeActivities() *fakeActivities {
	return &fakeActivities{stored: map[uuid.UUID]*Activity{}}
}

func (f *fakeActivities) Create(_ context.Context, a *Activity) error {
	if f.createErr != nil {
		return f.createErr
	}
	// Mirror the real repository's id assignment so the handler's response
	// body is the same shape it is in production.
	if a.ID == uuid.Nil {
		a.ID = uuid.New()
	}
	f.created = append(f.created, *a)
	cp := *a
	f.stored[a.ID] = &cp
	return nil
}

func (f *fakeActivities) Get(_ context.Context, id uuid.UUID) (*Activity, error) {
	f.fetchedByID = append(f.fetchedByID, id)
	if f.getErr != nil {
		return nil, f.getErr
	}
	a, ok := f.stored[id]
	if !ok {
		return nil, errors.New("activity not found")
	}
	cp := *a
	return &cp, nil
}

func (f *fakeActivities) ListByCustomer(_ context.Context, customerID uuid.UUID) ([]Activity, error) {
	f.listedFor = append(f.listedFor, customerID)
	if f.listErr != nil {
		return nil, f.listErr
	}
	return f.list, nil
}

func (f *fakeActivities) Update(_ context.Context, a *Activity) error {
	f.updated = append(f.updated, *a)
	return f.updateErr
}

func (f *fakeActivities) Delete(_ context.Context, id uuid.UUID) error {
	f.deleted = append(f.deleted, id)
	return f.deleteErr
}

func muxFor(repo Repository) *http.ServeMux {
	mux := http.NewServeMux()
	NewHandler(repo).RegisterRoutes(mux)
	return mux
}

// --- list ----------------------------------------------------------------

// CORRECTNESS: the activity feed is scoped to the customer in the path. The
// scope has to be asserted at the seam because a handler that queried with the
// wrong id would still return 200 with a plausible body.
func TestHandleListActivities_ScopesToThePathCustomer(t *testing.T) {
	want := uuid.New()
	repo := newFakeActivities()
	repo.list = []Activity{
		{ID: uuid.New(), CustomerID: want, ActivityType: ActivityCall, Description: "Called about the Maple Street order"},
		{ID: uuid.New(), CustomerID: want, ActivityType: ActivityNote, Description: "Left a voicemail"},
	}

	rec := do(t, muxFor(repo), http.MethodGet, "/api/v1/customers/"+want.String()+"/activities", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", rec.Code, rec.Body)
	}
	if len(repo.listedFor) != 1 {
		t.Fatalf("ListByCustomer called %d times, want 1", len(repo.listedFor))
	}
	if repo.listedFor[0] != want {
		t.Errorf("queried customer %s, want the path customer %s", repo.listedFor[0], want)
	}

	var got []Activity
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode %s: %v", rec.Body, err)
	}
	if len(got) != 2 || got[0].Description != "Called about the Maple Street order" {
		t.Errorf("body = %s, want the repository's two activities in order", rec.Body)
	}
}

// CORRECTNESS: a customer with no logged activity gets an empty JSON array.
// null is a different value: crmApi.listActivities declares this endpoint as
// Promise<Activity[]>, and every other list endpoint in this backend — EDI
// partners, EDI catalog entries, portal orders, project dashboards — guarantees
// [].
//
// The Lit consumer happens to defend itself (ActivityFeed.ts:43 does
// `acts || []`), which is why this went unnoticed; the wire contract was still
// wrong and the next consumer would not have known to guard.
func TestHandleListActivities_EmptyFeedIsAnEmptyArrayNotNull(t *testing.T) {
	rec := do(t, muxFor(newFakeActivities()), http.MethodGet, "/api/v1/customers/"+uuid.NewString()+"/activities", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if body := strings.TrimSpace(rec.Body.String()); body != "[]" {
		t.Errorf("empty feed serialised as %s, want []", body)
	}
}

// CORRECTNESS: a repository failure is a 500 and the SQL error does not reach
// the client.
func TestHandleListActivities_RepositoryFailureIs500AndDoesNotLeak(t *testing.T) {
	repo := newFakeActivities()
	repo.listErr = errors.New(`pq: column "activity_date" does not exist`)

	rec := do(t, muxFor(repo), http.MethodGet, "/api/v1/customers/"+uuid.NewString()+"/activities", "")
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", rec.Code)
	}
	if strings.Contains(rec.Body.String(), "activity_date") {
		t.Errorf("the database error reached the client: %s", rec.Body)
	}
}

// --- create --------------------------------------------------------------

// CORRECTNESS (security): the customer id comes from the path, never from the
// body. A caller who posts someone else's customer_id must have it discarded
// before the row is written.
//
// This supersedes the structural stand-in that used to live in
// activity_test.go: the overwrite is now observed at the persistence seam.
func TestHandleCreateActivity_PathCustomerBeatsTheBody(t *testing.T) {
	pathCustomer, attacker := uuid.New(), uuid.New()
	repo := newFakeActivities()

	rec := do(t, muxFor(repo), http.MethodPost, "/api/v1/customers/"+pathCustomer.String()+"/activities",
		`{"customer_id":"`+attacker.String()+`","activity_type":"CALL","description":"x"}`)
	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201: %s", rec.Code, rec.Body)
	}
	if len(repo.created) != 1 {
		t.Fatalf("Create called %d times, want 1", len(repo.created))
	}
	if repo.created[0].CustomerID != pathCustomer {
		t.Errorf("persisted customer_id = %s, want the path customer %s (the body's %s won)",
			repo.created[0].CustomerID, pathCustomer, attacker)
	}
}

// CORRECTNESS: a created activity is echoed back with the id the repository
// assigned, so the client can address it immediately.
func TestHandleCreateActivity_EchoesTheStoredRow(t *testing.T) {
	repo := newFakeActivities()
	customer := uuid.New()
	contact := uuid.New()

	rec := do(t, muxFor(repo), http.MethodPost, "/api/v1/customers/"+customer.String()+"/activities",
		`{"activity_type":"MEETING","description":"Site walk","contact_id":"`+contact.String()+`"}`)
	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201: %s", rec.Code, rec.Body)
	}

	var got Activity
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode %s: %v", rec.Body, err)
	}
	if got.ID == uuid.Nil {
		t.Errorf("the response carries no id: %s", rec.Body)
	}
	if got.ID != repo.created[0].ID {
		t.Errorf("response id %s != persisted id %s", got.ID, repo.created[0].ID)
	}
	if got.ActivityType != ActivityMeeting || got.Description != "Site walk" {
		t.Errorf("response = %+v, want the submitted values", got)
	}
	if got.ContactID == nil || *got.ContactID != contact {
		t.Errorf("contact_id = %v, want %s", got.ContactID, contact)
	}
}

// CORRECTNESS: a persistence failure is a 500, and nothing is echoed as if it
// had been saved.
func TestHandleCreateActivity_PersistenceFailureIs500(t *testing.T) {
	repo := newFakeActivities()
	repo.createErr = errors.New(`pq: insert or update on table "crm_activities" violates foreign key constraint`)

	rec := do(t, muxFor(repo), http.MethodPost, "/api/v1/customers/"+uuid.NewString()+"/activities",
		`{"activity_type":"CALL","description":"x"}`)
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", rec.Code)
	}
	if strings.Contains(rec.Body.String(), "crm_activities") {
		t.Errorf("the database error reached the client: %s", rec.Body)
	}
}

// --- get -----------------------------------------------------------------

// CORRECTNESS: the id queried is the one in the path, and the row is returned
// as-is.
func TestHandleGetActivity_QueriesThePathID(t *testing.T) {
	repo := newFakeActivities()
	id := uuid.New()
	repo.stored[id] = &Activity{
		ID: id, CustomerID: uuid.New(), ActivityType: ActivityEmail,
		Description:  "Sent the revised quote",
		ActivityDate: time.Date(2026, 3, 4, 9, 0, 0, 0, time.UTC),
	}

	rec := do(t, muxFor(repo), http.MethodGet, "/api/v1/activities/"+id.String(), "")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", rec.Code, rec.Body)
	}
	if len(repo.fetchedByID) != 1 || repo.fetchedByID[0] != id {
		t.Fatalf("queried %v, want the path id %s", repo.fetchedByID, id)
	}

	var got Activity
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got.ID != id || got.Description != "Sent the revised quote" {
		t.Errorf("body = %+v, want the stored row", got)
	}
}

// CORRECTNESS: an unknown activity is a 404 with the NOT_FOUND code.
func TestHandleGetActivity_UnknownIs404(t *testing.T) {
	rec := do(t, muxFor(newFakeActivities()), http.MethodGet, "/api/v1/activities/"+uuid.NewString(), "")
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", rec.Code)
	}
	var body struct {
		Error struct{ Code string } `json:"error"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body.Error.Code != "NOT_FOUND" {
		t.Errorf("error code = %q, want NOT_FOUND", body.Error.Code)
	}
}

// --- update / delete -----------------------------------------------------

// CORRECTNESS: the activity being updated is the one named in the path. A body
// that names a different id must not redirect the write.
func TestHandleUpdateActivity_PathIDBeatsTheBody(t *testing.T) {
	pathID, bodyID := uuid.New(), uuid.New()
	repo := newFakeActivities()

	rec := do(t, muxFor(repo), http.MethodPut, "/api/v1/activities/"+pathID.String(),
		`{"id":"`+bodyID.String()+`","activity_type":"NOTE","description":"amended"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", rec.Code, rec.Body)
	}
	if len(repo.updated) != 1 {
		t.Fatalf("Update called %d times, want 1", len(repo.updated))
	}
	if repo.updated[0].ID != pathID {
		t.Errorf("updated %s, want the path id %s (the body's %s won)", repo.updated[0].ID, pathID, bodyID)
	}
	if repo.updated[0].Description != "amended" {
		t.Errorf("description = %q, want the submitted value", repo.updated[0].Description)
	}
}

// CORRECTNESS: updating or deleting an activity that does not exist is a client
// error — the caller named a row that is not there — and must be a 404, the way
// HandleGetActivity already reports the same condition at handler.go:93.
//
// Both used to map the repository's "activity not found" onto a 500, so the
// client was told the server broke and a retry was pointless-but-plausible. The
// repository returned a bare fmt.Errorf rather than a sentinel, so the handler
// had nothing to match on. It now returns crm.ErrNotFound and the handler
// matches it with errors.Is, the way portal/errors.go already does — which is
// why the fake below is primed with the sentinel rather than a look-alike.
func TestHandleUpdateAndDelete_MissingActivityIs404(t *testing.T) {
	for _, tc := range []struct{ name, method, body string }{
		{"update", http.MethodPut, `{"activity_type":"NOTE","description":"x"}`},
		{"delete", http.MethodDelete, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			repo := newFakeActivities()
			repo.updateErr = ErrNotFound
			repo.deleteErr = ErrNotFound

			rec := do(t, muxFor(repo), tc.method, "/api/v1/activities/"+uuid.NewString(), tc.body)
			if rec.Code != http.StatusNotFound {
				t.Errorf("status = %d, want 404", rec.Code)
			}
			var body struct {
				Error struct {
					Code string `json:"code"`
				} `json:"error"`
			}
			if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
				t.Fatalf("decode: %v", err)
			}
			if body.Error.Code != "NOT_FOUND" {
				t.Errorf("error code = %q, want NOT_FOUND", body.Error.Code)
			}
		})
	}
}

// CORRECTNESS: the inverse of the characterization that used to pin the 500 for
// every failure. A repository error that is NOT the not-found sentinel is still
// a 500 — including one whose text happens to read "activity not found", since
// matching is by sentinel identity and not by message. Mapping everything to
// 404 would tell a client that a transient database fault is not worth
// retrying.
func TestHandleUpdateAndDelete_UnclassifiedRepositoryFailureIsStill500(t *testing.T) {
	for _, tc := range []struct {
		name, method, body string
		err                error
	}{
		{"update", http.MethodPut, `{"activity_type":"NOTE","description":"x"}`,
			errors.New(`pq: deadlock detected`)},
		{"delete", http.MethodDelete, "", errors.New(`pq: deadlock detected`)},
		// A look-alike: same message, different error value.
		{"update with a look-alike message", http.MethodPut, `{"activity_type":"NOTE","description":"x"}`,
			errors.New("activity not found")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			repo := newFakeActivities()
			repo.updateErr = tc.err
			repo.deleteErr = tc.err

			rec := do(t, muxFor(repo), tc.method, "/api/v1/activities/"+uuid.NewString(), tc.body)
			if rec.Code != http.StatusInternalServerError {
				t.Errorf("status = %d, want 500", rec.Code)
			}
			if strings.Contains(rec.Body.String(), "deadlock") {
				t.Errorf("the database error reached the client: %s", rec.Body)
			}
		})
	}
}

// CORRECTNESS: a successful delete is 204 with no body, and it deletes the id
// from the path.
func TestHandleDeleteActivity_Is204AndTargetsThePathID(t *testing.T) {
	id := uuid.New()
	repo := newFakeActivities()

	rec := do(t, muxFor(repo), http.MethodDelete, "/api/v1/activities/"+id.String(), "")
	if rec.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want 204", rec.Code)
	}
	if body := strings.TrimSpace(rec.Body.String()); body != "" {
		t.Errorf("204 carried a body: %q", body)
	}
	if len(repo.deleted) != 1 || repo.deleted[0] != id {
		t.Errorf("deleted %v, want the path id %s", repo.deleted, id)
	}
}

// CORRECTNESS: an invalid activity type is rejected on the update path before
// the repository is touched. The existing route test proves the 400; this
// proves nothing was written.
func TestHandleUpdateActivity_InvalidTypeNeverReachesPersistence(t *testing.T) {
	repo := newFakeActivities()

	rec := do(t, muxFor(repo), http.MethodPut, "/api/v1/activities/"+uuid.NewString(),
		`{"activity_type":"VISIT","description":"x"}`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
	if len(repo.updated) != 0 {
		t.Errorf("an invalid activity type was written: %+v", repo.updated)
	}
}

// CORRECTNESS: the seam is an interface and Postgres satisfies it.
func TestCRMSeam_IsAnInterface(t *testing.T) {
	var _ Repository = (*PostgresRepository)(nil)
	if NewHandler(newFakeActivities()) == nil {
		t.Fatal("NewHandler returned nil for a fake repository")
	}
}
