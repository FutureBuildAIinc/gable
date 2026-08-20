// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package vendor

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/google/uuid"
)

// Vendors carry the payment terms that drive AP due dates and the performance
// metrics that drive reorder decisions. Tests are CORRECTNESS unless labelled
// CHARACTERIZATION.

// --- fake repository -----------------------------------------------------

type fakeRepo struct {
	byID   map[uuid.UUID]*Vendor
	byName map[string]*Vendor

	created    []Vendor
	statsCalls []struct {
		id                        uuid.UUID
		leadTime, fillRate, spend float64
	}

	listErr   error
	getErr    error
	nameErr   error
	createErr error
	statsErr  error

	nameLookups []string
}

func newFakeRepo() *fakeRepo {
	return &fakeRepo{byID: map[uuid.UUID]*Vendor{}, byName: map[string]*Vendor{}}
}

func (f *fakeRepo) ListVendors(context.Context) ([]Vendor, error) {
	if f.listErr != nil {
		return nil, f.listErr
	}
	// Deliberately mirrors PostgresRepository.ListVendors, which declares
	// `var vendors []Vendor` and returns it — so "no rows" is a NIL slice, not
	// an empty one. The distinction is invisible in Go and decisive in JSON.
	var out []Vendor
	for _, v := range f.byID {
		out = append(out, *v)
	}
	return out, nil
}

func (f *fakeRepo) GetVendor(_ context.Context, id uuid.UUID) (*Vendor, error) {
	if f.getErr != nil {
		return nil, f.getErr
	}
	v, ok := f.byID[id]
	if !ok {
		return nil, nil // the Postgres repository returns (nil, nil) for "not found"
	}
	return v, nil
}

func (f *fakeRepo) GetVendorByName(_ context.Context, name string) (*Vendor, error) {
	f.nameLookups = append(f.nameLookups, name)
	if f.nameErr != nil {
		return nil, f.nameErr
	}
	v, ok := f.byName[name]
	if !ok {
		return nil, nil
	}
	return v, nil
}

func (f *fakeRepo) CreateVendor(_ context.Context, v *Vendor) error {
	if f.createErr != nil {
		return f.createErr
	}
	if v.ID == uuid.Nil {
		v.ID = uuid.New()
	}
	f.created = append(f.created, *v)
	f.byID[v.ID] = v
	f.byName[v.Name] = v
	return nil
}

func (f *fakeRepo) UpdateStats(_ context.Context, id uuid.UUID, leadTime, fillRate, spend float64) error {
	if f.statsErr != nil {
		return f.statsErr
	}
	f.statsCalls = append(f.statsCalls, struct {
		id                        uuid.UUID
		leadTime, fillRate, spend float64
	}{id, leadTime, fillRate, spend})
	if v, ok := f.byID[id]; ok {
		v.AverageLeadTimeDays = leadTime
		v.FillRate = fillRate
		v.TotalSpendYTD = spend
	}
	return nil
}

var _ Repository = (*fakeRepo)(nil)

func strPtr(s string) *string { return &s }

// --- CreateVendor --------------------------------------------------------

// CORRECTNESS: payment terms decide when a vendor invoice becomes due, so an
// unspecified term must fall back to the documented house default rather than
// to an empty string that no AP aging bucket can interpret.
func TestCreateVendor_PaymentTermsDefault(t *testing.T) {
	repo := newFakeRepo()
	svc := NewService(repo)

	v, err := svc.CreateVendor(context.Background(), CreateVendorRequest{Name: "Weyerhaeuser"})
	if err != nil {
		t.Fatalf("CreateVendor: %v", err)
	}
	if v.PaymentTerms != "Net 30" {
		t.Errorf("PaymentTerms = %q, want the Net 30 default", v.PaymentTerms)
	}
	if len(repo.created) != 1 || repo.created[0].PaymentTerms != "Net 30" {
		t.Errorf("persisted %v, want Net 30", repo.created)
	}
}

// CORRECTNESS: an explicit term overrides the default.
func TestCreateVendor_ExplicitPaymentTerms(t *testing.T) {
	repo := newFakeRepo()
	v, err := NewService(repo).CreateVendor(context.Background(), CreateVendorRequest{
		Name:         "Boise Cascade",
		PaymentTerms: strPtr("2/10 Net 45"),
	})
	if err != nil {
		t.Fatalf("CreateVendor: %v", err)
	}
	if v.PaymentTerms != "2/10 Net 45" {
		t.Errorf("PaymentTerms = %q, want 2/10 Net 45", v.PaymentTerms)
	}
}

// CORRECTNESS: the default must survive an explicit but empty payment_terms.
// A request that sends `"payment_terms": ""` — which is what an HTML form with
// an untouched text input produces — decodes to a non-nil pointer to the empty
// string. Treating that as an override creates a vendor with no terms at all,
// and AP then has nothing to compute a due date from.
func TestCreateVendor_EmptyPaymentTermsMustFallBackToTheDefault(t *testing.T) {
	repo := newFakeRepo()
	v, err := NewService(repo).CreateVendor(context.Background(), CreateVendorRequest{
		Name:         "Empty Terms Co",
		PaymentTerms: strPtr(""),
	})
	if err != nil {
		t.Fatalf("CreateVendor: %v", err)
	}
	if v.PaymentTerms != "Net 30" {
		t.Errorf("PaymentTerms = %q, want the Net 30 default when an empty string is supplied", v.PaymentTerms)
	}
}

// CHARACTERIZATION: CreateVendor performs no validation. A vendor with an
// empty name is accepted, which makes EnsureVendorByName("") match it later
// and quietly attach purchase orders to a nameless vendor.
func TestCreateVendor_AcceptsAnEmptyName(t *testing.T) {
	repo := newFakeRepo()
	v, err := NewService(repo).CreateVendor(context.Background(), CreateVendorRequest{Name: ""})
	if err != nil {
		t.Fatalf("CreateVendor: %v", err)
	}
	if v.Name != "" {
		t.Fatalf("Name = %q; if validation has been added this characterization test should become a rejection test", v.Name)
	}
	if len(repo.created) != 1 {
		t.Errorf("persisted %d vendors, want 1", len(repo.created))
	}
}

// CORRECTNESS: a failed insert must surface as an error and must not return a
// vendor value the caller could go on to reference.
func TestCreateVendor_InsertFailure(t *testing.T) {
	repo := newFakeRepo()
	repo.createErr = errors.New("insert failed")
	got, err := NewService(repo).CreateVendor(context.Background(), CreateVendorRequest{Name: "X"})
	if err == nil {
		t.Fatal("want an error when the insert fails")
	}
	if got != nil {
		t.Errorf("returned %+v alongside the error, want nil", got)
	}
	if !strings.Contains(err.Error(), "failed to create vendor") {
		t.Errorf("error = %q, want it wrapped with context", err)
	}
}

// CORRECTNESS: optional contact fields must be carried through as-is,
// including the difference between absent (nil) and empty.
func TestCreateVendor_OptionalContactFields(t *testing.T) {
	repo := newFakeRepo()
	email := "ap@example.com"
	v, err := NewService(repo).CreateVendor(context.Background(), CreateVendorRequest{
		Name:         "Contact Co",
		ContactEmail: &email,
	})
	if err != nil {
		t.Fatalf("CreateVendor: %v", err)
	}
	if v.ContactEmail == nil || *v.ContactEmail != email {
		t.Errorf("ContactEmail = %v, want %q", v.ContactEmail, email)
	}
	if v.Phone != nil {
		t.Errorf("Phone = %v, want nil when it was not supplied", v.Phone)
	}
}

// --- EnsureVendorByName --------------------------------------------------

// CORRECTNESS: this is the idempotent upsert used by PO and EDI import. It
// must return the existing vendor without creating a duplicate — a duplicate
// splits a vendor's spend and AP balance across two rows.
func TestEnsureVendorByName_ExistingVendorIsReused(t *testing.T) {
	repo := newFakeRepo()
	existing := &Vendor{ID: uuid.New(), Name: "Weyerhaeuser", PaymentTerms: "2/10 Net 45"}
	repo.byName["Weyerhaeuser"] = existing

	got, err := NewService(repo).EnsureVendorByName(context.Background(), "Weyerhaeuser")
	if err != nil {
		t.Fatalf("EnsureVendorByName: %v", err)
	}
	if got.ID != existing.ID {
		t.Errorf("returned a different vendor (%s), want the existing %s", got.ID, existing.ID)
	}
	if got.PaymentTerms != "2/10 Net 45" {
		t.Errorf("PaymentTerms = %q, want the existing vendor's terms preserved", got.PaymentTerms)
	}
	if len(repo.created) != 0 {
		t.Errorf("created %d vendors, want 0 — an existing vendor was duplicated", len(repo.created))
	}
}

// CORRECTNESS: an unknown name creates exactly one vendor, with the house
// default terms, and a second call then reuses it.
func TestEnsureVendorByName_CreatesOnceThenReuses(t *testing.T) {
	repo := newFakeRepo()
	svc := NewService(repo)

	first, err := svc.EnsureVendorByName(context.Background(), "New Mill Co")
	if err != nil {
		t.Fatalf("EnsureVendorByName: %v", err)
	}
	if first.PaymentTerms != "Net 30" {
		t.Errorf("PaymentTerms = %q, want Net 30", first.PaymentTerms)
	}

	second, err := svc.EnsureVendorByName(context.Background(), "New Mill Co")
	if err != nil {
		t.Fatalf("EnsureVendorByName (second call): %v", err)
	}
	if second.ID != first.ID {
		t.Errorf("second call created a duplicate: %s vs %s", second.ID, first.ID)
	}
	if len(repo.created) != 1 {
		t.Errorf("created %d vendors across two calls, want 1", len(repo.created))
	}
}

// CORRECTNESS: name matching is exact. Silently folding case or whitespace
// would merge two genuinely distinct trading partners; failing to fold them is
// the safer default, and this pins which one the code does.
func TestEnsureVendorByName_MatchIsExact(t *testing.T) {
	repo := newFakeRepo()
	repo.byName["Weyerhaeuser"] = &Vendor{ID: uuid.New(), Name: "Weyerhaeuser"}
	svc := NewService(repo)

	for _, variant := range []string{"weyerhaeuser", "WEYERHAEUSER", " Weyerhaeuser", "Weyerhaeuser "} {
		before := len(repo.created)
		got, err := svc.EnsureVendorByName(context.Background(), variant)
		if err != nil {
			t.Fatalf("EnsureVendorByName(%q): %v", variant, err)
		}
		if got.Name != variant {
			t.Errorf("EnsureVendorByName(%q) returned name %q", variant, got.Name)
		}
		if len(repo.created) != before+1 {
			t.Errorf("EnsureVendorByName(%q) did not create a new vendor; the match is no longer exact", variant)
		}
	}
}

// CORRECTNESS: a lookup failure must abort rather than fall through to a
// create — falling through would duplicate a vendor every time the database
// hiccuped.
func TestEnsureVendorByName_LookupFailureDoesNotCreate(t *testing.T) {
	repo := newFakeRepo()
	repo.nameErr = errors.New("db down")

	if _, err := NewService(repo).EnsureVendorByName(context.Background(), "Anything"); err == nil {
		t.Fatal("want an error when the lookup fails")
	}
	if len(repo.created) != 0 {
		t.Fatalf("created %d vendors after a failed lookup — a transient error would duplicate the vendor", len(repo.created))
	}
}

// CORRECTNESS: a failed create is reported, not returned as a phantom vendor.
func TestEnsureVendorByName_CreateFailure(t *testing.T) {
	repo := newFakeRepo()
	repo.createErr = errors.New("insert failed")
	got, err := NewService(repo).EnsureVendorByName(context.Background(), "New Co")
	if err == nil {
		t.Fatal("want an error when the create fails")
	}
	if got != nil {
		t.Errorf("returned %+v alongside the error, want nil", got)
	}
}

// --- UpdatePerformance ---------------------------------------------------

// CORRECTNESS: the three metrics must reach the repository in the right order.
// Transposing lead time and fill rate would make the reorder engine treat a
// 95% fill rate as a 95-day lead time.
func TestUpdatePerformance_ArgumentOrder(t *testing.T) {
	repo := newFakeRepo()
	id := uuid.New()
	repo.byID[id] = &Vendor{ID: id, Name: "V"}

	if err := NewService(repo).UpdatePerformance(context.Background(), id, 12.5, 0.95, 148250.75); err != nil {
		t.Fatalf("UpdatePerformance: %v", err)
	}
	if len(repo.statsCalls) != 1 {
		t.Fatalf("repository called %d times, want 1", len(repo.statsCalls))
	}
	c := repo.statsCalls[0]
	if c.id != id {
		t.Errorf("id = %s, want %s", c.id, id)
	}
	if c.leadTime != 12.5 {
		t.Errorf("leadTime = %v, want 12.5", c.leadTime)
	}
	if c.fillRate != 0.95 {
		t.Errorf("fillRate = %v, want 0.95", c.fillRate)
	}
	if c.spend != 148250.75 {
		t.Errorf("spend = %v, want 148250.75 (exact dollars)", c.spend)
	}

	repo.statsErr = errors.New("update failed")
	if err := NewService(repo).UpdatePerformance(context.Background(), id, 1, 1, 1); err == nil {
		t.Fatal("want an error when the update fails")
	}
}

// --- HTTP layer ----------------------------------------------------------

func newTestMux(repo Repository) *http.ServeMux {
	mux := http.NewServeMux()
	NewHandler(NewService(repo)).RegisterRoutes(mux)
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

// CORRECTNESS: the Postgres repository signals "not found" as (nil, nil), so
// the handler must turn a nil vendor into a 404 rather than encoding "null"
// with a 200.
func TestHandleGet_NilVendorIs404(t *testing.T) {
	rec := do(t, newTestMux(newFakeRepo()), http.MethodGet, "/api/v1/vendors/"+uuid.NewString(), "")
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404 (body %q)", rec.Code, rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), "null") {
		t.Errorf("body = %q, want an error envelope rather than a null vendor", rec.Body.String())
	}
}

func TestHandleGet_MalformedIDIs400(t *testing.T) {
	rec := do(t, newTestMux(newFakeRepo()), http.MethodGet, "/api/v1/vendors/not-a-uuid", "")
	if rec.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", rec.Code)
	}
}

func TestHandleGet_RepositoryFailureIs500(t *testing.T) {
	repo := newFakeRepo()
	repo.getErr = errors.New("db down")
	rec := do(t, newTestMux(repo), http.MethodGet, "/api/v1/vendors/"+uuid.NewString(), "")
	if rec.Code != http.StatusInternalServerError {
		t.Errorf("status = %d, want 500", rec.Code)
	}
	if strings.Contains(rec.Body.String(), "db down") {
		t.Errorf("the internal error leaked: %s", rec.Body.String())
	}
}

// CORRECTNESS: a created vendor is returned under 201 with its generated id
// and the terms the service resolved.
func TestHandleCreate(t *testing.T) {
	repo := newFakeRepo()
	rec := do(t, newTestMux(repo), http.MethodPost, "/api/v1/vendors", `{"name":"Boise Cascade"}`)

	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201 (body %s)", rec.Code, rec.Body.String())
	}
	var got Vendor
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got.ID == uuid.Nil {
		t.Error("the response did not carry the generated id")
	}
	if got.PaymentTerms != "Net 30" {
		t.Errorf("PaymentTerms = %q, want Net 30", got.PaymentTerms)
	}
}

func TestHandleCreate_MalformedBodyIs400(t *testing.T) {
	repo := newFakeRepo()
	rec := do(t, newTestMux(repo), http.MethodPost, "/api/v1/vendors", "{not json")
	if rec.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", rec.Code)
	}
	if len(repo.created) != 0 {
		t.Error("a malformed body reached the repository")
	}
}

// CORRECTNESS: an empty vendor list must serialise as [] rather than null,
// matching every other list endpoint in the codebase. HandleList must not
// encode a nil repository slice straight through.
func TestHandleList_EmptyIsArrayNotNull(t *testing.T) {
	rec := do(t, newTestMux(newFakeRepo()), http.MethodGet, "/api/v1/vendors", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if got := strings.TrimSpace(rec.Body.String()); got != "[]" {
		t.Errorf("body = %s, want []", got)
	}
}

func TestHandleList_ReturnsVendorsAndPropagatesFailure(t *testing.T) {
	repo := newFakeRepo()
	id := uuid.New()
	repo.byID[id] = &Vendor{ID: id, Name: "Acme", PaymentTerms: "Net 30", FillRate: 0.97}

	rec := do(t, newTestMux(repo), http.MethodGet, "/api/v1/vendors", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	var got []Vendor
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(got) != 1 || got[0].FillRate != 0.97 {
		t.Fatalf("got %+v, want the vendor with its fill rate", got)
	}

	repo.listErr = errors.New("db down")
	rec = do(t, newTestMux(repo), http.MethodGet, "/api/v1/vendors", "")
	if rec.Code != http.StatusInternalServerError {
		t.Errorf("status = %d, want 500", rec.Code)
	}
}

// CORRECTNESS: the role guard must wrap every vendor route — vendor records
// carry banking-adjacent contact details and drive AP.
func TestRegisterRoutes_RoleGuardWrapsEveryEndpoint(t *testing.T) {
	guard := func(http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusForbidden)
		})
	}
	mux := http.NewServeMux()
	NewHandler(NewService(newFakeRepo())).RegisterRoutes(mux, guard)

	routes := []struct{ method, path string }{
		{http.MethodGet, "/api/v1/vendors"},
		{http.MethodPost, "/api/v1/vendors"},
		{http.MethodGet, "/api/v1/vendors/" + uuid.NewString()},
	}
	for _, r := range routes {
		rec := do(t, mux, r.method, r.path, "{}")
		if rec.Code != http.StatusForbidden {
			t.Errorf("%s %s = %d, want 403", r.method, r.path, rec.Code)
		}
	}
}
