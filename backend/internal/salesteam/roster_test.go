// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package salesteam

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/google/uuid"
)

// These are the tests the concrete-*Repository gap used to block. Handler now
// takes the consumer-defined Repository interface declared in repository.go, so
// the roster endpoints can be driven against a fake and the response contract —
// status, envelope, encoding, and which id reaches persistence — is real.
//
// The SQL half (the `WHERE is_active = true` filter and the pgx.ErrNoRows
// mapping) lives in repository_pg_test.go, against a real database.
//
// Tests are CORRECTNESS unless labelled CHARACTERIZATION.

// fakeRoster is an in-memory Repository. It records what it was asked for so a
// test can assert the handler forwarded the right id, not just that it got an
// answer back.
type fakeRoster struct {
	people  []SalesPerson
	person  *SalesPerson
	listErr error
	getErr  error

	listCalls int
	gotIDs    []uuid.UUID
}

var _ Repository = (*fakeRoster)(nil)

func (f *fakeRoster) List(context.Context) ([]SalesPerson, error) {
	f.listCalls++
	return f.people, f.listErr
}

func (f *fakeRoster) Get(_ context.Context, id uuid.UUID) (*SalesPerson, error) {
	f.gotIDs = append(f.gotIDs, id)
	if f.getErr != nil {
		return nil, f.getErr
	}
	return f.person, nil
}

func muxFor(repo Repository) *http.ServeMux {
	mux := http.NewServeMux()
	NewHandler(repo).RegisterRoutes(mux)
	return mux
}

// CORRECTNESS: the roster endpoint serves what the repository returns, as a
// JSON array, with the wire field names the customer-assignment UI reads.
func TestHandleList_ServesTheRepositoryRoster(t *testing.T) {
	a, b := uuid.New(), uuid.New()
	repo := &fakeRoster{people: []SalesPerson{
		{ID: a, Name: "Dana Reyes", Email: "dana@dealer.example", Role: "OUTSIDE", IsActive: true},
		{ID: b, Name: "Sam Okafor", Email: "sam@dealer.example", Role: "INSIDE", IsActive: true},
	}}

	rec := do(t, muxFor(repo), http.MethodGet, "/api/v1/sales-team")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); ct != "application/json" {
		t.Errorf("Content-Type = %q, want application/json", ct)
	}
	if repo.listCalls != 1 {
		t.Errorf("List was called %d times, want exactly 1", repo.listCalls)
	}

	var got []SalesPerson
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode %s: %v", rec.Body, err)
	}
	if len(got) != 2 {
		t.Fatalf("got %d reps, want 2: %s", len(got), rec.Body)
	}
	if got[0].ID != a || got[1].ID != b {
		t.Errorf("roster order changed: got %s,%s want %s,%s", got[0].ID, got[1].ID, a, b)
	}
	if got[0].Name != "Dana Reyes" || got[0].Role != "OUTSIDE" {
		t.Errorf("rep = %+v, want the repository's values", got[0])
	}
}

// CORRECTNESS: an empty roster is an empty JSON array. A `null` body is not the
// same value: app/src/services/SalesTeamService.ts declares this endpoint as
// Promise<SalesPerson[]> and AccountDetailPage.ts:98-99 assigns the result
// straight into a SalesPerson[] field it later reads .length and .map on, so
// null is a TypeError in the browser rather than an empty dropdown.
//
// edi/edi_handler.go:54-56 and edi_handler.go:246-248 do exactly this guard for
// their list endpoints; salesteam now does too.
func TestHandleList_EmptyRosterIsAnEmptyArrayNotNull(t *testing.T) {
	rec := do(t, muxFor(&fakeRoster{people: nil}), http.MethodGet, "/api/v1/sales-team")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if body := strings.TrimSpace(rec.Body.String()); body != "[]" {
		t.Errorf("empty roster serialised as %s, want []", body)
	}
}

// CORRECTNESS: a repository failure is a 500 and must not leak the database
// error to the client.
func TestHandleList_RepositoryFailureIs500AndDoesNotLeak(t *testing.T) {
	const secret = "pq: relation \"sales_team\" does not exist"
	rec := do(t, muxFor(&fakeRoster{listErr: errors.New(secret)}), http.MethodGet, "/api/v1/sales-team")

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", rec.Code)
	}
	if strings.Contains(rec.Body.String(), "sales_team") {
		t.Errorf("the database error reached the client: %s", rec.Body)
	}
	var body struct {
		Error struct{ Code string } `json:"error"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body.Error.Code != "INTERNAL_ERROR" {
		t.Errorf("error code = %q, want INTERNAL_ERROR", body.Error.Code)
	}
}

// CORRECTNESS: the id the handler queries with is the one in the path. A
// handler that parsed the path but then queried with something else — the zero
// UUID, say — would still return 200 with a body, so the id has to be asserted
// at the seam.
func TestHandleGet_QueriesTheIDFromThePath(t *testing.T) {
	want := uuid.New()
	repo := &fakeRoster{person: &SalesPerson{ID: want, Name: "Dana Reyes", IsActive: true}}

	rec := do(t, muxFor(repo), http.MethodGet, "/api/v1/sales-team/"+want.String())
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", rec.Code, rec.Body)
	}
	if len(repo.gotIDs) != 1 {
		t.Fatalf("Get was called %d times, want exactly 1", len(repo.gotIDs))
	}
	if repo.gotIDs[0] != want {
		t.Errorf("the repository was queried with %s, want the path id %s", repo.gotIDs[0], want)
	}

	var got SalesPerson
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got.ID != want {
		t.Errorf("returned rep %s, want %s", got.ID, want)
	}
}

// CORRECTNESS: an unknown salesperson is a 404 with the NOT_FOUND code, and the
// repository's error text — which names the table on a genuine failure — is not
// echoed back.
func TestHandleGet_UnknownRepIs404(t *testing.T) {
	repo := &fakeRoster{getErr: errors.New("salesperson not found")}
	rec := do(t, muxFor(repo), http.MethodGet, "/api/v1/sales-team/"+uuid.NewString())

	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", rec.Code)
	}
	var body struct {
		Error struct{ Code, Message string } `json:"error"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body.Error.Code != "NOT_FOUND" {
		t.Errorf("error code = %q, want NOT_FOUND", body.Error.Code)
	}
	if strings.Contains(body.Error.Message, "salesperson not found") {
		t.Errorf("the repository error was echoed to the client: %q", body.Error.Message)
	}
}

// CORRECTNESS: a malformed id must not reach the repository at all.
func TestHandleGet_MalformedIDNeverReachesTheRepository(t *testing.T) {
	repo := &fakeRoster{person: &SalesPerson{ID: uuid.New()}}
	rec := do(t, muxFor(repo), http.MethodGet, "/api/v1/sales-team/not-a-uuid")

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
	if len(repo.gotIDs) != 0 {
		t.Errorf("the repository was queried with %v despite a malformed id", repo.gotIDs)
	}
}

// CORRECTNESS: the list endpoint never calls Get and the detail endpoint never
// calls List. Crossing them would make the active-only roster filter apply to a
// direct lookup, or vice versa.
func TestRoutes_UseTheirOwnRepositoryMethod(t *testing.T) {
	repo := &fakeRoster{people: []SalesPerson{{ID: uuid.New()}}, person: &SalesPerson{ID: uuid.New()}}
	mux := muxFor(repo)

	do(t, mux, http.MethodGet, "/api/v1/sales-team")
	if len(repo.gotIDs) != 0 {
		t.Errorf("the list route called Get: %v", repo.gotIDs)
	}

	before := repo.listCalls
	do(t, mux, http.MethodGet, "/api/v1/sales-team/"+uuid.NewString())
	if repo.listCalls != before {
		t.Errorf("the detail route called List")
	}
}
