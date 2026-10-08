// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package customer

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

// The customer module owns the credit limit and the AR balance that the order
// credit gate and the B2B portal both read. Its Service is a thin pass-through,
// so the behaviour worth pinning is the HTTP contract: what units money is in
// on the wire, and what a client is allowed to do.
//
// Tests are CORRECTNESS unless labelled CHARACTERIZATION.

// --- fake repository -----------------------------------------------------

type fakeRepo struct {
	customers  map[uuid.UUID]*Customer
	priceLevel []PriceLevel
	contacts   map[uuid.UUID]*Contact

	created      []Customer
	balanceCalls []struct {
		id    uuid.UUID
		delta float64
	}
	salespersonCalls []struct {
		customerID    uuid.UUID
		salespersonID *uuid.UUID
	}

	// policies backs the lumber-index escalation-policy endpoints. A customer
	// absent from this map is treated as "not found" by GetEscalationPolicy.
	policies map[uuid.UUID]*EscalationPolicy

	err      error
	total    int
	listArgs [][2]int
}

func newFakeRepo() *fakeRepo {
	return &fakeRepo{
		customers: map[uuid.UUID]*Customer{},
		contacts:  map[uuid.UUID]*Contact{},
		policies:  map[uuid.UUID]*EscalationPolicy{},
	}
}

func (f *fakeRepo) CreateCustomer(_ context.Context, c *Customer) error {
	if f.err != nil {
		return f.err
	}
	if c.ID == uuid.Nil {
		c.ID = uuid.New()
	}
	f.created = append(f.created, *c)
	f.customers[c.ID] = c
	return nil
}

func (f *fakeRepo) GetCustomer(_ context.Context, id uuid.UUID) (*Customer, error) {
	if f.err != nil {
		return nil, f.err
	}
	c, ok := f.customers[id]
	if !ok {
		return nil, errors.New("customer not found")
	}
	return c, nil
}

func (f *fakeRepo) GetCustomerByEmail(_ context.Context, email string) (*Customer, error) {
	if f.err != nil {
		return nil, f.err
	}
	for _, c := range f.customers {
		if c.Email == email {
			return c, nil
		}
	}
	return nil, errors.New("customer not found")
}

func (f *fakeRepo) ListCustomers(context.Context) ([]Customer, error) {
	if f.err != nil {
		return nil, f.err
	}
	out := make([]Customer, 0, len(f.customers))
	for _, c := range f.customers {
		out = append(out, *c)
	}
	return out, nil
}

func (f *fakeRepo) ListCustomersPaginated(_ context.Context, limit, offset int) ([]Customer, int, error) {
	f.listArgs = append(f.listArgs, [2]int{limit, offset})
	if f.err != nil {
		return nil, 0, f.err
	}
	out := make([]Customer, 0, len(f.customers))
	for _, c := range f.customers {
		out = append(out, *c)
	}
	return out, f.total, nil
}

func (f *fakeRepo) ListPriceLevels(context.Context) ([]PriceLevel, error) {
	if f.err != nil {
		return nil, f.err
	}
	return f.priceLevel, nil
}

func (f *fakeRepo) GetPriceLevel(_ context.Context, id uuid.UUID) (*PriceLevel, error) {
	for i := range f.priceLevel {
		if f.priceLevel[i].ID == id {
			return &f.priceLevel[i], nil
		}
	}
	return nil, errors.New("price level not found")
}

func (f *fakeRepo) UpdateBalance(_ context.Context, id uuid.UUID, delta float64) error {
	if f.err != nil {
		return f.err
	}
	f.balanceCalls = append(f.balanceCalls, struct {
		id    uuid.UUID
		delta float64
	}{id, delta})
	if c, ok := f.customers[id]; ok {
		c.BalanceDue += delta
	}
	return nil
}

func (f *fakeRepo) UpdateSalesperson(_ context.Context, customerID uuid.UUID, salespersonID *uuid.UUID) error {
	if f.err != nil {
		return f.err
	}
	f.salespersonCalls = append(f.salespersonCalls, struct {
		customerID    uuid.UUID
		salespersonID *uuid.UUID
	}{customerID, salespersonID})
	if c, ok := f.customers[customerID]; ok {
		c.SalespersonID = salespersonID
	}
	return nil
}

func (f *fakeRepo) CreateContact(_ context.Context, c *Contact) error {
	if f.err != nil {
		return f.err
	}
	if c.ID == uuid.Nil {
		c.ID = uuid.New()
	}
	f.contacts[c.ID] = c
	return nil
}

func (f *fakeRepo) GetContact(_ context.Context, id uuid.UUID) (*Contact, error) {
	if f.err != nil {
		return nil, f.err
	}
	c, ok := f.contacts[id]
	if !ok {
		return nil, errors.New("contact not found")
	}
	return c, nil
}

func (f *fakeRepo) ListContactsByCustomer(_ context.Context, customerID uuid.UUID) ([]Contact, error) {
	if f.err != nil {
		return nil, f.err
	}
	var out []Contact
	for _, c := range f.contacts {
		if c.CustomerID == customerID {
			out = append(out, *c)
		}
	}
	return out, nil
}

func (f *fakeRepo) UpdateContact(_ context.Context, c *Contact) error {
	if f.err != nil {
		return f.err
	}
	if _, ok := f.contacts[c.ID]; !ok {
		return errors.New("contact not found")
	}
	f.contacts[c.ID] = c
	return nil
}

func (f *fakeRepo) DeleteContact(_ context.Context, id uuid.UUID) error {
	if f.err != nil {
		return f.err
	}
	if _, ok := f.contacts[id]; !ok {
		return errors.New("contact not found")
	}
	delete(f.contacts, id)
	return nil
}

func (f *fakeRepo) GetEscalationPolicy(_ context.Context, customerID uuid.UUID) (*EscalationPolicy, error) {
	if f.err != nil {
		return nil, f.err
	}
	p, ok := f.policies[customerID]
	if !ok {
		return nil, nil
	}
	clone := *p
	return &clone, nil
}

func (f *fakeRepo) SetEscalationPolicy(_ context.Context, p *EscalationPolicy) error {
	if f.err != nil {
		return f.err
	}
	clone := *p
	f.policies[p.CustomerID] = &clone
	return nil
}

var _ Repository = (*fakeRepo)(nil)

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

// --- money units on the wire --------------------------------------------

// CORRECTNESS (contract): the customer API speaks float64 DOLLARS for
// credit_limit and balance_due, while the account module reads and writes the
// same customers.balance_due column as int64 CENTS (see CLAUDE.md, "Money
// convention is not uniform across modules"). This test pins the customer
// side of that boundary: a $73.88 balance must serialise as 73.88, not 7388.
//
// If the module is migrated to cents, this test fails — deliberately. Anyone
// making that change must also update the portal and partner dashboards, which
// pass these values straight through.
func TestHandleGetCustomer_MoneyIsDollarsOnTheWire(t *testing.T) {
	repo := newFakeRepo()
	id := uuid.New()
	repo.customers[id] = &Customer{
		ID:          id,
		Name:        "Acme Construction",
		CreditLimit: 25000.00,
		BalanceDue:  73.88,
	}

	rec := do(t, newTestMux(repo), http.MethodGet, "/api/v1/customers/"+id.String(), "")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}

	// Decode into a raw map so the assertion is about the JSON text, not the
	// Go struct that produced it.
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(rec.Body.Bytes(), &raw); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got := string(raw["balance_due"]); got != "73.88" {
		t.Errorf("balance_due = %s, want 73.88 — this endpoint emits dollars, not cents", got)
	}
	if got := string(raw["credit_limit"]); got != "25000" {
		t.Errorf("credit_limit = %s, want 25000 dollars", got)
	}
}

// CORRECTNESS: sub-cent noise must not appear on the wire. A dollars-as-float
// API is already fragile; emitting 73.88000000000001 would break every client
// that formats to two places by string manipulation.
func TestCustomerJSON_DoesNotLeakFloatArtifacts(t *testing.T) {
	c := Customer{BalanceDue: 0.1 + 0.2, CreditLimit: 1000}
	b, err := json.Marshal(c)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if strings.Contains(string(b), "0.30000000000000004") {
		t.Errorf("balance_due serialised with float noise: %s", b)
	}
}

// --- list / pagination ---------------------------------------------------

// CORRECTNESS: an empty page must serialise as [] rather than null. A null
// here crashes clients that iterate the result without a guard.
func TestHandleListCustomers_EmptyDataIsArrayNotNull(t *testing.T) {
	rec := do(t, newTestMux(newFakeRepo()), http.MethodGet, "/api/v1/customers", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	var resp struct {
		Data   json.RawMessage `json:"data"`
		Total  int             `json:"total"`
		Limit  int             `json:"limit"`
		Offset int             `json:"offset"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if string(resp.Data) != "[]" {
		t.Errorf("data = %s, want []", resp.Data)
	}
}

// CORRECTNESS: pagination parameters must reach the repository, and the
// response must echo the window it actually used so a client can page.
func TestHandleListCustomers_PaginationParameters(t *testing.T) {
	tests := []struct {
		name       string
		query      string
		wantLimit  int
		wantOffset int
	}{
		{"defaults", "", 50, 0},
		{"explicit", "?limit=10&offset=20", 10, 20},
		{"limit above the cap falls back to the default", "?limit=100000", 50, 0},
		{"zero limit falls back to the default", "?limit=0", 50, 0},
		{"negative limit falls back to the default", "?limit=-5", 50, 0},
		{"negative offset falls back to zero", "?offset=-5", 50, 0},
		{"non-numeric values fall back", "?limit=abc&offset=xyz", 50, 0},
		{"the maximum is accepted", "?limit=200", 200, 0},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			repo := newFakeRepo()
			repo.total = 7
			rec := do(t, newTestMux(repo), http.MethodGet, "/api/v1/customers"+tc.query, "")
			if rec.Code != http.StatusOK {
				t.Fatalf("status = %d, want 200", rec.Code)
			}
			if len(repo.listArgs) != 1 {
				t.Fatalf("repository called %d times, want 1", len(repo.listArgs))
			}
			if repo.listArgs[0] != [2]int{tc.wantLimit, tc.wantOffset} {
				t.Errorf("repository got limit/offset %v, want [%d %d]", repo.listArgs[0], tc.wantLimit, tc.wantOffset)
			}

			var resp struct {
				Total  int `json:"total"`
				Limit  int `json:"limit"`
				Offset int `json:"offset"`
			}
			if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
				t.Fatalf("decode: %v", err)
			}
			if resp.Limit != tc.wantLimit || resp.Offset != tc.wantOffset {
				t.Errorf("response echoed limit/offset %d/%d, want %d/%d", resp.Limit, resp.Offset, tc.wantLimit, tc.wantOffset)
			}
			if resp.Total != 7 {
				t.Errorf("total = %d, want the repository's 7", resp.Total)
			}
		})
	}
}

func TestHandleListCustomers_RepositoryFailureIs500(t *testing.T) {
	repo := newFakeRepo()
	repo.err = errors.New("db down")
	rec := do(t, newTestMux(repo), http.MethodGet, "/api/v1/customers", "")
	if rec.Code != http.StatusInternalServerError {
		t.Errorf("status = %d, want 500", rec.Code)
	}
	if strings.Contains(rec.Body.String(), "db down") {
		t.Errorf("the internal error leaked to the client: %s", rec.Body.String())
	}
}

// --- id parsing ----------------------------------------------------------

// CORRECTNESS: a malformed id is a client error and must never reach the
// repository.
func TestHandlers_MalformedIDs(t *testing.T) {
	repo := newFakeRepo()
	mux := newTestMux(repo)

	cases := []struct{ method, path, body string }{
		{http.MethodGet, "/api/v1/customers/not-a-uuid", ""},
		{http.MethodPatch, "/api/v1/customers/not-a-uuid/salesperson", `{"salesperson_id":null}`},
		{http.MethodGet, "/api/v1/customers/not-a-uuid/contacts", ""},
		{http.MethodPost, "/api/v1/customers/not-a-uuid/contacts", `{"first_name":"A"}`},
		{http.MethodGet, "/api/v1/contacts/not-a-uuid", ""},
		{http.MethodPut, "/api/v1/contacts/not-a-uuid", `{"first_name":"A"}`},
		{http.MethodDelete, "/api/v1/contacts/not-a-uuid", ""},
	}

	for _, tc := range cases {
		rec := do(t, mux, tc.method, tc.path, tc.body)
		if rec.Code != http.StatusBadRequest {
			t.Errorf("%s %s = %d, want 400", tc.method, tc.path, rec.Code)
		}
	}
	if len(repo.salespersonCalls) != 0 || len(repo.created) != 0 || len(repo.contacts) != 0 {
		t.Error("a malformed id reached the repository")
	}
}

// CORRECTNESS: a customer that does not exist is a 404, not a 500, and must
// not leak the underlying error text.
func TestHandleGetCustomer_NotFound(t *testing.T) {
	rec := do(t, newTestMux(newFakeRepo()), http.MethodGet, "/api/v1/customers/"+uuid.NewString(), "")
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", rec.Code)
	}
	var body struct {
		Error struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body.Error.Code != "NOT_FOUND" {
		t.Errorf("error code = %q, want NOT_FOUND", body.Error.Code)
	}
	if strings.Contains(body.Error.Message, "customer not found") {
		t.Errorf("the repository's message leaked to the client: %q", body.Error.Message)
	}
}

// --- create --------------------------------------------------------------

// CORRECTNESS: a created customer comes back with the generated id so the
// client can act on it, under 201.
func TestHandleCreateCustomer(t *testing.T) {
	repo := newFakeRepo()
	rec := do(t, newTestMux(repo), http.MethodPost, "/api/v1/customers",
		`{"name":"Acme Construction","credit_limit":25000,"tier":"GOLD"}`)

	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201 (body %s)", rec.Code, rec.Body.String())
	}
	var got Customer
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got.ID == uuid.Nil {
		t.Error("the response did not carry the generated id")
	}
	if got.Name != "Acme Construction" || got.CreditLimit != 25000 || got.Tier != TierGold {
		t.Errorf("round-tripped %+v", got)
	}
	if len(repo.created) != 1 {
		t.Fatalf("persisted %d customers, want 1", len(repo.created))
	}
}

func TestHandleCreateCustomer_MalformedBodyIs400(t *testing.T) {
	repo := newFakeRepo()
	rec := do(t, newTestMux(repo), http.MethodPost, "/api/v1/customers", "{not json")
	if rec.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", rec.Code)
	}
	if len(repo.created) != 0 {
		t.Error("a malformed body reached the repository")
	}
}

// CHARACTERIZATION: the create endpoint performs no validation at all. A
// customer with no name, a negative credit limit and an unknown tier is
// accepted and persisted. Recorded here because these are the fields the order
// credit gate and the pricing waterfall read.
func TestHandleCreateCustomer_AcceptsUnvalidatedInput(t *testing.T) {
	repo := newFakeRepo()
	rec := do(t, newTestMux(repo), http.MethodPost, "/api/v1/customers",
		`{"name":"","credit_limit":-5000,"balance_due":-1,"tier":"NOT_A_TIER"}`)

	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201 (current behaviour)", rec.Code)
	}
	if len(repo.created) != 1 {
		t.Fatalf("persisted %d customers, want 1", len(repo.created))
	}
	got := repo.created[0]
	if got.Name != "" || got.CreditLimit != -5000 || string(got.Tier) != "NOT_A_TIER" {
		t.Fatalf("persisted %+v; if validation has been added, this characterization test should become a rejection test", got)
	}
}

// --- salesperson assignment ---------------------------------------------

// CORRECTNESS: assigning a salesperson persists the assignment and returns the
// updated customer, so the UI does not have to refetch.
func TestHandleUpdateSalesperson(t *testing.T) {
	repo := newFakeRepo()
	custID := uuid.New()
	repo.customers[custID] = &Customer{ID: custID, Name: "Acme"}
	repID := uuid.New()

	rec := do(t, newTestMux(repo), http.MethodPatch,
		"/api/v1/customers/"+custID.String()+"/salesperson",
		`{"salesperson_id":"`+repID.String()+`"}`)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body %s)", rec.Code, rec.Body.String())
	}
	if len(repo.salespersonCalls) != 1 {
		t.Fatalf("repository called %d times, want 1", len(repo.salespersonCalls))
	}
	call := repo.salespersonCalls[0]
	if call.customerID != custID {
		t.Errorf("customerID = %s, want %s", call.customerID, custID)
	}
	if call.salespersonID == nil || *call.salespersonID != repID {
		t.Errorf("salespersonID = %v, want %s", call.salespersonID, repID)
	}

	var got Customer
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got.SalespersonID == nil || *got.SalespersonID != repID {
		t.Errorf("response did not reflect the new assignment: %+v", got)
	}
}

// CORRECTNESS: a null salesperson_id means "unassign" and must be passed
// through as nil rather than silently ignored or turned into the zero UUID.
func TestHandleUpdateSalesperson_NullUnassigns(t *testing.T) {
	repo := newFakeRepo()
	custID := uuid.New()
	existing := uuid.New()
	repo.customers[custID] = &Customer{ID: custID, SalespersonID: &existing}

	rec := do(t, newTestMux(repo), http.MethodPatch,
		"/api/v1/customers/"+custID.String()+"/salesperson", `{"salesperson_id":null}`)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if len(repo.salespersonCalls) != 1 || repo.salespersonCalls[0].salespersonID != nil {
		t.Fatalf("repository got %v, want a nil salesperson id", repo.salespersonCalls)
	}
	if repo.customers[custID].SalespersonID != nil {
		t.Error("the customer is still assigned after an unassign")
	}
}

// --- contacts ------------------------------------------------------------

// CORRECTNESS: a contact is created under the customer in the path, and the
// customer_id in the body must not be able to override it — otherwise a caller
// with access to customer A could attach contacts to customer B.
func TestHandleCreateContact_PathCustomerWins(t *testing.T) {
	repo := newFakeRepo()
	pathCustomer := uuid.New()
	otherCustomer := uuid.New()

	rec := do(t, newTestMux(repo), http.MethodPost,
		"/api/v1/customers/"+pathCustomer.String()+"/contacts",
		`{"first_name":"Jo","last_name":"Smith","role":"Buyer","customer_id":"`+otherCustomer.String()+`"}`)

	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201 (body %s)", rec.Code, rec.Body.String())
	}
	if len(repo.contacts) != 1 {
		t.Fatalf("persisted %d contacts, want 1", len(repo.contacts))
	}
	for _, c := range repo.contacts {
		if c.CustomerID != pathCustomer {
			t.Errorf("contact filed under %s, want the customer from the path (%s)", c.CustomerID, pathCustomer)
		}
	}
}

// CORRECTNESS: updating a contact uses the id from the path, not the body.
func TestHandleUpdateContact_PathIDWins(t *testing.T) {
	repo := newFakeRepo()
	custID := uuid.New()
	contactID := uuid.New()
	otherID := uuid.New()
	repo.contacts[contactID] = &Contact{ID: contactID, CustomerID: custID, FirstName: "Old"}
	repo.contacts[otherID] = &Contact{ID: otherID, CustomerID: custID, FirstName: "Untouched"}

	rec := do(t, newTestMux(repo), http.MethodPut, "/api/v1/contacts/"+contactID.String(),
		`{"id":"`+otherID.String()+`","first_name":"New","role":"AP"}`)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body %s)", rec.Code, rec.Body.String())
	}
	if repo.contacts[contactID].FirstName != "New" {
		t.Errorf("path contact was not updated: %+v", repo.contacts[contactID])
	}
	if repo.contacts[otherID].FirstName != "Untouched" {
		t.Errorf("the contact named in the body was modified: %+v", repo.contacts[otherID])
	}
}

// CORRECTNESS: a successful delete returns 204 with no body; deleting a
// contact that is not there is a server-reported failure, not a silent 204.
func TestHandleDeleteContact(t *testing.T) {
	repo := newFakeRepo()
	id := uuid.New()
	repo.contacts[id] = &Contact{ID: id}

	rec := do(t, newTestMux(repo), http.MethodDelete, "/api/v1/contacts/"+id.String(), "")
	if rec.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want 204", rec.Code)
	}
	if rec.Body.Len() != 0 {
		t.Errorf("204 response carried a body: %q", rec.Body.String())
	}
	if len(repo.contacts) != 0 {
		t.Error("the contact was not deleted")
	}

	rec = do(t, newTestMux(repo), http.MethodDelete, "/api/v1/contacts/"+uuid.NewString(), "")
	if rec.Code == http.StatusNoContent {
		t.Error("deleting a nonexistent contact reported success")
	}
}

// CORRECTNESS: contacts are listed for the customer in the path only.
func TestHandleListContacts_ScopedToTheCustomer(t *testing.T) {
	repo := newFakeRepo()
	mine, theirs := uuid.New(), uuid.New()
	a, b := uuid.New(), uuid.New()
	repo.contacts[a] = &Contact{ID: a, CustomerID: mine, FirstName: "Mine"}
	repo.contacts[b] = &Contact{ID: b, CustomerID: theirs, FirstName: "Theirs"}

	rec := do(t, newTestMux(repo), http.MethodGet, "/api/v1/customers/"+mine.String()+"/contacts", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	var got []Contact
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(got) != 1 || got[0].FirstName != "Mine" {
		t.Fatalf("got %+v, want only the requested customer's contact", got)
	}
}

// --- role guard ----------------------------------------------------------

// CORRECTNESS: the guard passed at registration must wrap every route,
// including the contact routes, or customer PII is reachable unauthenticated.
func TestRegisterRoutes_RoleGuardWrapsEveryEndpoint(t *testing.T) {
	guard := func(http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusForbidden)
		})
	}
	mux := http.NewServeMux()
	NewHandler(NewService(newFakeRepo())).RegisterRoutes(mux, guard)

	id := uuid.NewString()
	routes := []struct{ method, path string }{
		{http.MethodGet, "/api/v1/customers"},
		{http.MethodGet, "/api/v1/customers/" + id},
		{http.MethodPost, "/api/v1/customers"},
		{http.MethodPatch, "/api/v1/customers/" + id + "/salesperson"},
		{http.MethodGet, "/api/v1/price_levels"},
		{http.MethodGet, "/api/v1/customers/" + id + "/contacts"},
		{http.MethodPost, "/api/v1/customers/" + id + "/contacts"},
		{http.MethodGet, "/api/v1/contacts/" + id},
		{http.MethodPut, "/api/v1/contacts/" + id},
		{http.MethodDelete, "/api/v1/contacts/" + id},
	}

	for _, r := range routes {
		rec := do(t, mux, r.method, r.path, "{}")
		if rec.Code != http.StatusForbidden {
			t.Errorf("%s %s = %d, want 403: the role guard did not wrap this route", r.method, r.path, rec.Code)
		}
	}
}

// --- price levels --------------------------------------------------------

// CORRECTNESS: the price-level multiplier is the factor the pricing waterfall
// applies to a base price, so it must survive the round trip exactly.
func TestHandleListPriceLevels(t *testing.T) {
	repo := newFakeRepo()
	repo.priceLevel = []PriceLevel{
		{ID: uuid.New(), Name: "Contractor", Multiplier: 0.85},
		{ID: uuid.New(), Name: "Retail", Multiplier: 1.0},
	}

	rec := do(t, newTestMux(repo), http.MethodGet, "/api/v1/price_levels", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	var got []PriceLevel
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(got) != 2 || got[0].Multiplier != 0.85 {
		t.Fatalf("got %+v, want the multipliers preserved", got)
	}

	repo.err = errors.New("db down")
	rec = do(t, newTestMux(repo), http.MethodGet, "/api/v1/price_levels", "")
	if rec.Code != http.StatusInternalServerError {
		t.Errorf("status = %d, want 500", rec.Code)
	}
}

// --- balance -------------------------------------------------------------

// CORRECTNESS: UpdateBalance is a signed DELTA in dollars, not an absolute
// balance. Passing the delta through unchanged (including its sign and any
// fractional cents) is what keeps the denormalised column in step with the
// subledger.
//
// Note that account.PostTransaction writes this same column in int64 CENTS,
// so the two writers disagree about units — see CLAUDE.md. This test pins
// what the customer module itself promises.
func TestUpdateBalance_PassesTheSignedDeltaThrough(t *testing.T) {
	tests := []struct {
		name    string
		opening float64
		delta   float64
		want    float64
	}{
		{"an invoice increases what is owed", 0, 108.25, 108.25},
		{"a payment decreases it", 108.25, -108.25, 0},
		{"a credit can drive it negative", 25, -40, -15},
		{"a zero delta is a no-op", 77.77, 0, 77.77},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			repo := newFakeRepo()
			id := uuid.New()
			repo.customers[id] = &Customer{ID: id, BalanceDue: tc.opening}
			svc := NewService(repo)

			if err := svc.UpdateBalance(context.Background(), id, tc.delta); err != nil {
				t.Fatalf("UpdateBalance: %v", err)
			}
			if len(repo.balanceCalls) != 1 || repo.balanceCalls[0].delta != tc.delta {
				t.Fatalf("repository got %v, want a single delta of %v", repo.balanceCalls, tc.delta)
			}
			if got := repo.customers[id].BalanceDue; got != tc.want {
				t.Errorf("balance = %v, want %v", got, tc.want)
			}
		})
	}
}

// --- lumber-index escalation policy -------------------------------------

// CORRECTNESS: AUTO_ESCALATE lets the scanner rewrite a customer's locked
// quote price without asking anyone. Migration 081 enforces a signed agreement
// with a CHECK constraint; the service must reject it first so the caller gets
// a 400 with a readable reason instead of a 500 from Postgres.
func TestHandleSetEscalationPolicy_AutoEscalateRequiresSignedAgreement(t *testing.T) {
	repo := newFakeRepo()
	id := uuid.New()
	repo.customers[id] = &Customer{ID: id, Name: "Acme"}

	body := `{"policy":"AUTO_ESCALATE","threshold_pct":5}`
	rec := do(t, newTestMux(repo), http.MethodPut, "/api/v1/customers/"+id.String()+"/escalation-policy", body)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 — AUTO_ESCALATE without a signed agreement must be refused", rec.Code)
	}
	if _, ok := repo.policies[id]; ok {
		t.Error("policy was persisted despite validation failure")
	}
}

// CORRECTNESS: with a signed agreement on file, AUTO_ESCALATE is accepted and
// the response echoes the persisted row.
func TestHandleSetEscalationPolicy_AutoEscalateWithAgreementSucceeds(t *testing.T) {
	repo := newFakeRepo()
	id := uuid.New()
	repo.customers[id] = &Customer{ID: id, Name: "Acme"}

	body := `{"policy":"AUTO_ESCALATE","threshold_pct":3.5,"agreement_signed_at":"2026-01-15T00:00:00Z","agreement_ref":"MSA-2026-11"}`
	rec := do(t, newTestMux(repo), http.MethodPut, "/api/v1/customers/"+id.String()+"/escalation-policy", body)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body = %s", rec.Code, rec.Body.String())
	}
	stored, ok := repo.policies[id]
	if !ok {
		t.Fatal("policy was not persisted")
	}
	if stored.Policy != PolicyAutoEscalate || stored.ThresholdPct != 3.5 {
		t.Errorf("stored = %+v, want AUTO_ESCALATE at 3.5%%", stored)
	}
	if stored.AgreementSignedAt == nil {
		t.Error("agreement_signed_at was dropped")
	}

	var resp EscalationPolicy
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.Policy != PolicyAutoEscalate || resp.ThresholdPct != 3.5 {
		t.Errorf("response = %+v, want the persisted row echoed back", resp)
	}
}

// CORRECTNESS: the threshold bounds mirror the migration-081 CHECK
// (0 < threshold <= 50). A zero threshold would flag every quote on any index
// tick; a >50% threshold means the customer has effectively opted out and
// should say so explicitly rather than through a silent number.
func TestHandleSetEscalationPolicy_ThresholdBounds(t *testing.T) {
	tests := []struct {
		name     string
		body     string
		wantCode int
	}{
		{"zero threshold rejected", `{"policy":"REQUIRE_ACK","threshold_pct":0}`, http.StatusBadRequest},
		{"negative threshold rejected", `{"policy":"REQUIRE_ACK","threshold_pct":-1}`, http.StatusBadRequest},
		{"above ceiling rejected", `{"policy":"REQUIRE_ACK","threshold_pct":50.1}`, http.StatusBadRequest},
		{"at ceiling accepted", `{"policy":"REQUIRE_ACK","threshold_pct":50}`, http.StatusOK},
		{"typical value accepted", `{"policy":"FLAG_FOR_REQUOTE","threshold_pct":5}`, http.StatusOK},
		{"unknown policy rejected", `{"policy":"YOLO","threshold_pct":5}`, http.StatusBadRequest},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			repo := newFakeRepo()
			id := uuid.New()
			repo.customers[id] = &Customer{ID: id}
			rec := do(t, newTestMux(repo), http.MethodPut, "/api/v1/customers/"+id.String()+"/escalation-policy", tc.body)
			if rec.Code != tc.wantCode {
				t.Errorf("status = %d, want %d; body = %s", rec.Code, tc.wantCode, rec.Body.String())
			}
		})
	}
}

// CORRECTNESS: an agreement reference without a date would leave an
// unauditable "we have a contract somewhere" marker. The service stamps
// signed-at so the reference always carries a date.
func TestSetEscalationPolicy_AgreementRefWithoutDateIsStamped(t *testing.T) {
	repo := newFakeRepo()
	id := uuid.New()
	svc := NewService(repo)

	p := &EscalationPolicy{
		CustomerID:   id,
		Policy:       PolicyRequireAck,
		ThresholdPct: 4,
		AgreementRef: "MSA-2026-42",
	}
	if err := svc.SetEscalationPolicy(context.Background(), p); err != nil {
		t.Fatalf("SetEscalationPolicy: %v", err)
	}
	stored := repo.policies[id]
	if stored.AgreementSignedAt == nil {
		t.Fatal("agreement_signed_at is nil; a reference without a date is unauditable")
	}
}

// CORRECTNESS: reading the policy of a customer that does not exist is a 404,
// not an empty 200 that a caller would misread as "no protection configured".
func TestHandleGetEscalationPolicy_UnknownCustomerIs404(t *testing.T) {
	rec := do(t, newTestMux(newFakeRepo()), http.MethodGet,
		"/api/v1/customers/"+uuid.New().String()+"/escalation-policy", "")
	if rec.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404", rec.Code)
	}
}

// CORRECTNESS: the path id wins over any customer_id in the body, so a caller
// cannot rewrite another account's policy by forging the payload.
func TestHandleSetEscalationPolicy_PathIDOverridesBodyID(t *testing.T) {
	repo := newFakeRepo()
	target := uuid.New()
	victim := uuid.New()
	repo.customers[target] = &Customer{ID: target}
	repo.customers[victim] = &Customer{ID: victim}

	body := `{"customer_id":"` + victim.String() + `","policy":"REQUIRE_ACK","threshold_pct":5}`
	rec := do(t, newTestMux(repo), http.MethodPut, "/api/v1/customers/"+target.String()+"/escalation-policy", body)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body = %s", rec.Code, rec.Body.String())
	}
	if _, ok := repo.policies[victim]; ok {
		t.Error("body customer_id was honoured — a caller could rewrite another account's policy")
	}
	if _, ok := repo.policies[target]; !ok {
		t.Error("path customer_id was not used")
	}
}
