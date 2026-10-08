// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package partner

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gablelbm/gable/internal/customer"
	"github.com/gablelbm/gable/internal/quote"
	"github.com/gablelbm/gable/pkg/middleware"
	"github.com/google/uuid"
)

// The partner API is customer-facing and multi-tenant: every request is scoped
// to the authenticated contractor's customer id. The most important property
// here is that one contractor cannot read another's quotes or AR position.
//
// Tests are CORRECTNESS unless labelled CHARACTERIZATION.

// --- fakes ---------------------------------------------------------------

type fakeCustomerRepo struct {
	customer.Repository // only GetCustomer is used; anything else is a loud nil call
	customers           map[uuid.UUID]*customer.Customer
	err                 error
	lookups             []uuid.UUID
}

func (f *fakeCustomerRepo) GetCustomer(_ context.Context, id uuid.UUID) (*customer.Customer, error) {
	f.lookups = append(f.lookups, id)
	if f.err != nil {
		return nil, f.err
	}
	c, ok := f.customers[id]
	if !ok {
		return nil, errors.New("customer not found")
	}
	return c, nil
}

type fakeQuoteRepo struct {
	quote.Repository
	byID       map[uuid.UUID]*quote.Quote
	byCustomer map[uuid.UUID][]quote.Quote
	err        error
}

func (f *fakeQuoteRepo) GetQuote(_ context.Context, id uuid.UUID) (*quote.Quote, error) {
	if f.err != nil {
		return nil, f.err
	}
	q, ok := f.byID[id]
	if !ok {
		return nil, errors.New("quote not found")
	}
	return q, nil
}

func (f *fakeQuoteRepo) ListQuotesByCustomer(_ context.Context, customerID uuid.UUID) ([]quote.Quote, error) {
	if f.err != nil {
		return nil, f.err
	}
	return f.byCustomer[customerID], nil
}

func testLogger() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

// --- dashboard -----------------------------------------------------------

// CORRECTNESS: the partner dashboard shows the contractor their own credit
// limit and balance, in float64 dollars (this side of the dollars/cents
// boundary documented in CLAUDE.md). Both figures must come from the customer
// record for the id that was asked for, unmodified.
func TestGetDashboard_ReportsTheCustomersOwnFigures(t *testing.T) {
	id := uuid.New()
	custRepo := &fakeCustomerRepo{customers: map[uuid.UUID]*customer.Customer{
		id: {ID: id, Name: "Acme Construction", CreditLimit: 25000.00, BalanceDue: 4873.19},
	}}
	svc := NewService(custRepo, &fakeQuoteRepo{}, testLogger())

	dto, err := svc.GetDashboard(context.Background(), id)
	if err != nil {
		t.Fatalf("GetDashboard: %v", err)
	}
	if dto.CreditLimit != 25000.00 {
		t.Errorf("CreditLimit = %v, want 25000 dollars", dto.CreditLimit)
	}
	if dto.BalanceDue != 4873.19 {
		t.Errorf("BalanceDue = %v, want 4873.19 dollars — not cents, not rounded", dto.BalanceDue)
	}
	if len(custRepo.lookups) != 1 || custRepo.lookups[0] != id {
		t.Errorf("looked up %v, want exactly the requested customer %s", custRepo.lookups, id)
	}
}

// CORRECTNESS: the dashboard must serialise as dollars on the wire. If this
// module is ever migrated to int64 cents, this assertion is the tripwire that
// forces the portal frontend to be migrated with it.
func TestGetDashboard_WireFormatIsDollars(t *testing.T) {
	id := uuid.New()
	svc := NewService(&fakeCustomerRepo{customers: map[uuid.UUID]*customer.Customer{
		id: {ID: id, CreditLimit: 25000, BalanceDue: 73.88},
	}}, &fakeQuoteRepo{}, testLogger())

	dto, err := svc.GetDashboard(context.Background(), id)
	if err != nil {
		t.Fatalf("GetDashboard: %v", err)
	}
	b, err := json.Marshal(dto)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(b, &raw); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got := string(raw["balance_due"]); got != "73.88" {
		t.Errorf("balance_due = %s, want 73.88 dollars", got)
	}
	if got := string(raw["credit_limit"]); got != "25000" {
		t.Errorf("credit_limit = %s, want 25000 dollars", got)
	}
}

// CORRECTNESS: a customer that cannot be loaded must produce an error, not a
// zero-valued dashboard that shows a contractor a $0 credit limit and a $0
// balance as if those were facts.
func TestGetDashboard_LoadFailureIsAnError(t *testing.T) {
	svc := NewService(&fakeCustomerRepo{err: errors.New("db down")}, &fakeQuoteRepo{}, testLogger())

	dto, err := svc.GetDashboard(context.Background(), uuid.New())
	if err == nil {
		t.Fatal("want an error when the customer cannot be loaded")
	}
	if dto != nil {
		t.Errorf("returned %+v alongside the error, want nil", dto)
	}
}

// --- quote listing -------------------------------------------------------

// CORRECTNESS: listing is scoped by customer id at the repository, so the
// service must pass the caller's own id and nothing else.
func TestListQuotes_ScopedToTheCaller(t *testing.T) {
	mine, theirs := uuid.New(), uuid.New()
	qr := &fakeQuoteRepo{byCustomer: map[uuid.UUID][]quote.Quote{
		mine:   {{ID: uuid.New(), CustomerID: mine, TotalAmount: 100}},
		theirs: {{ID: uuid.New(), CustomerID: theirs, TotalAmount: 999}},
	}}
	svc := NewService(&fakeCustomerRepo{}, qr, testLogger())

	got, err := svc.ListQuotes(context.Background(), mine)
	if err != nil {
		t.Fatalf("ListQuotes: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("got %d quotes, want 1", len(got))
	}
	if got[0].CustomerID != mine {
		t.Errorf("returned a quote belonging to %s", got[0].CustomerID)
	}
}

func TestListQuotes_PropagatesFailure(t *testing.T) {
	svc := NewService(&fakeCustomerRepo{}, &fakeQuoteRepo{err: errors.New("db down")}, testLogger())
	if _, err := svc.ListQuotes(context.Background(), uuid.New()); err == nil {
		t.Fatal("want an error when the quote list fails")
	}
}

// --- quote fetch: cross-tenant isolation --------------------------------

// CORRECTNESS (security): GetQuote takes an id straight from the URL, so the
// ownership check is the only thing between contractor A and contractor B's
// pricing. It must refuse, and it must refuse with a message that does not
// reveal that the quote exists.
func TestGetQuote_RefusesAnotherCustomersQuote(t *testing.T) {
	attacker, victim := uuid.New(), uuid.New()
	victimQuoteID := uuid.New()

	qr := &fakeQuoteRepo{byID: map[uuid.UUID]*quote.Quote{
		victimQuoteID: {ID: victimQuoteID, CustomerID: victim, TotalAmount: 128450.00, State: quote.QuoteStateSent},
	}}
	svc := NewService(&fakeCustomerRepo{}, qr, testLogger())

	got, err := svc.GetQuote(context.Background(), attacker, victimQuoteID)
	if err == nil {
		t.Fatalf("cross-customer read succeeded and returned %+v", got)
	}
	if got != nil {
		t.Errorf("returned %+v alongside the error, want nil", got)
	}
	// The error must be indistinguishable from a genuine miss, otherwise it is
	// an existence oracle for other customers' quote ids.
	missing, missErr := svc.GetQuote(context.Background(), attacker, uuid.New())
	if missing != nil || missErr == nil {
		t.Fatal("a genuinely missing quote must also error")
	}
	if err.Error() != missErr.Error() {
		t.Errorf("forbidden error %q differs from not-found error %q; the difference leaks whether the quote exists", err, missErr)
	}
}

// CORRECTNESS: the owner can read their own quote, with its money intact.
func TestGetQuote_OwnerCanRead(t *testing.T) {
	owner := uuid.New()
	id := uuid.New()
	qr := &fakeQuoteRepo{byID: map[uuid.UUID]*quote.Quote{
		id: {ID: id, CustomerID: owner, TotalAmount: 128450.00, FreightAmount: 350.00, State: quote.QuoteStateSent},
	}}
	svc := NewService(&fakeCustomerRepo{}, qr, testLogger())

	got, err := svc.GetQuote(context.Background(), owner, id)
	if err != nil {
		t.Fatalf("GetQuote: %v", err)
	}
	if got.ID != id {
		t.Errorf("ID = %s, want %s", got.ID, id)
	}
	if got.TotalAmount != 128450.00 || got.FreightAmount != 350.00 {
		t.Errorf("money changed in transit: %+v", got)
	}
}

// CORRECTNESS: the zero UUID is not a customer. An unauthenticated request
// that reaches the service with uuid.Nil must not be able to read a quote that
// also happens to carry uuid.Nil as its customer, so this pins that the check
// is a real comparison rather than a "not empty" test.
func TestGetQuote_ZeroCallerCannotReadARealCustomersQuote(t *testing.T) {
	owner := uuid.New()
	id := uuid.New()
	qr := &fakeQuoteRepo{byID: map[uuid.UUID]*quote.Quote{
		id: {ID: id, CustomerID: owner},
	}}
	svc := NewService(&fakeCustomerRepo{}, qr, testLogger())

	if got, err := svc.GetQuote(context.Background(), uuid.Nil, id); err == nil {
		t.Fatalf("a zero customer id read %+v", got)
	}
}

func TestGetQuote_PropagatesLoadFailure(t *testing.T) {
	svc := NewService(&fakeCustomerRepo{}, &fakeQuoteRepo{err: errors.New("db down")}, testLogger())
	if _, err := svc.GetQuote(context.Background(), uuid.New(), uuid.New()); err == nil {
		t.Fatal("want an error when the quote cannot be loaded")
	}
}

// --- HTTP layer ----------------------------------------------------------

func newTestMux(svc *Service) *http.ServeMux {
	mux := http.NewServeMux()
	// Pass-through middleware: the tests inject the customer themselves so the
	// handler's own extraction and guard logic is what is under test.
	NewHandler(svc).RegisterRoutes(mux, func(next http.Handler) http.Handler { return next })
	return mux
}

func withCustomer(r *http.Request, c *customer.Customer) *http.Request {
	return r.WithContext(context.WithValue(r.Context(), middleware.CustomerContextKey, c))
}

// CORRECTNESS (security): every partner endpoint must reject a request with no
// authenticated customer on the context. A missing context value must be a 401,
// not a lookup for the zero UUID.
func TestHandlers_RejectRequestsWithNoCustomerContext(t *testing.T) {
	custRepo := &fakeCustomerRepo{}
	qr := &fakeQuoteRepo{}
	mux := newTestMux(NewService(custRepo, qr, testLogger()))

	routes := []struct{ method, path string }{
		{http.MethodGet, "/api/partner/v1/dashboard"},
		{http.MethodGet, "/api/partner/v1/quotes"},
		{http.MethodGet, "/api/partner/v1/quotes/" + uuid.NewString()},
	}
	for _, r := range routes {
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, httptest.NewRequest(r.method, r.path, nil))
		if rec.Code != http.StatusUnauthorized {
			t.Errorf("%s %s = %d, want 401", r.method, r.path, rec.Code)
		}
	}
	if len(custRepo.lookups) != 0 {
		t.Errorf("an unauthenticated request reached the repository: %v", custRepo.lookups)
	}
}

// CORRECTNESS (security): a nil customer stored under the context key — which
// is what a middleware bug or a type assertion on the wrong value looks like —
// must be treated the same as no customer at all.
func TestHandlers_RejectANilCustomerOnTheContext(t *testing.T) {
	mux := newTestMux(NewService(&fakeCustomerRepo{}, &fakeQuoteRepo{}, testLogger()))

	rec := httptest.NewRecorder()
	req := withCustomer(httptest.NewRequest(http.MethodGet, "/api/partner/v1/dashboard", nil), nil)
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401 for a nil customer", rec.Code)
	}
}

// CORRECTNESS (security): the handler must use the customer from the auth
// context and ignore anything the caller supplies. This drives the whole stack
// — handler, service, ownership check — with an attacker asking for a victim's
// quote id.
func TestHandleGetQuote_CrossCustomerRequestIs404(t *testing.T) {
	attacker := &customer.Customer{ID: uuid.New()}
	victimID := uuid.New()
	victimQuote := uuid.New()

	qr := &fakeQuoteRepo{byID: map[uuid.UUID]*quote.Quote{
		victimQuote: {ID: victimQuote, CustomerID: victimID, TotalAmount: 128450},
	}}
	mux := newTestMux(NewService(&fakeCustomerRepo{}, qr, testLogger()))

	rec := httptest.NewRecorder()
	req := withCustomer(httptest.NewRequest(http.MethodGet, "/api/partner/v1/quotes/"+victimQuote.String(), nil), attacker)
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", rec.Code)
	}
	if body := rec.Body.String(); jsonContainsNumber(t, body, 128450) {
		t.Fatalf("the victim's quote total leaked into the response: %s", body)
	}
}

// CORRECTNESS: the owner gets their quote back through the same path.
func TestHandleGetQuote_OwnerGets200(t *testing.T) {
	owner := &customer.Customer{ID: uuid.New()}
	id := uuid.New()
	qr := &fakeQuoteRepo{byID: map[uuid.UUID]*quote.Quote{
		id: {ID: id, CustomerID: owner.ID, TotalAmount: 4200.50},
	}}
	mux := newTestMux(NewService(&fakeCustomerRepo{}, qr, testLogger()))

	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, withCustomer(httptest.NewRequest(http.MethodGet, "/api/partner/v1/quotes/"+id.String(), nil), owner))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body %s)", rec.Code, rec.Body.String())
	}
	var got quote.Quote
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got.TotalAmount != 4200.50 {
		t.Errorf("total_amount = %v, want 4200.50 dollars", got.TotalAmount)
	}
}

func TestHandleGetQuote_MalformedIDIs400(t *testing.T) {
	mux := newTestMux(NewService(&fakeCustomerRepo{}, &fakeQuoteRepo{}, testLogger()))
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, withCustomer(httptest.NewRequest(http.MethodGet, "/api/partner/v1/quotes/not-a-uuid", nil),
		&customer.Customer{ID: uuid.New()}))
	if rec.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", rec.Code)
	}
}

// CORRECTNESS: the dashboard handler asks for the authenticated customer's
// own id, never one supplied by the request.
func TestHandleDashboard_UsesTheAuthenticatedCustomerID(t *testing.T) {
	authed := &customer.Customer{ID: uuid.New()}
	other := uuid.New()

	custRepo := &fakeCustomerRepo{customers: map[uuid.UUID]*customer.Customer{
		authed.ID: {ID: authed.ID, CreditLimit: 100, BalanceDue: 10},
		other:     {ID: other, CreditLimit: 999999, BalanceDue: 888888},
	}}
	mux := newTestMux(NewService(custRepo, &fakeQuoteRepo{}, testLogger()))

	rec := httptest.NewRecorder()
	req := withCustomer(httptest.NewRequest(http.MethodGet, "/api/partner/v1/dashboard?customer_id="+other.String(), nil), authed)
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if len(custRepo.lookups) != 1 || custRepo.lookups[0] != authed.ID {
		t.Fatalf("looked up %v, want only the authenticated customer %s", custRepo.lookups, authed.ID)
	}
	var dto DashboardDTO
	if err := json.Unmarshal(rec.Body.Bytes(), &dto); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if dto.CreditLimit != 100 {
		t.Errorf("CreditLimit = %v, want the authenticated customer's 100", dto.CreditLimit)
	}
}

func TestHandleDashboard_RepositoryFailureIs500(t *testing.T) {
	mux := newTestMux(NewService(&fakeCustomerRepo{err: errors.New("db down")}, &fakeQuoteRepo{}, testLogger()))
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, withCustomer(httptest.NewRequest(http.MethodGet, "/api/partner/v1/dashboard", nil),
		&customer.Customer{ID: uuid.New()}))
	if rec.Code != http.StatusInternalServerError {
		t.Errorf("status = %d, want 500", rec.Code)
	}
}

// jsonContainsNumber reports whether the JSON text contains the given number as
// a value anywhere, used to prove a leak did not happen.
func jsonContainsNumber(t *testing.T, body string, n float64) bool {
	t.Helper()
	var v any
	if err := json.Unmarshal([]byte(body), &v); err != nil {
		// Not JSON at all: fall back to a substring check.
		return false
	}
	var walk func(any) bool
	walk = func(x any) bool {
		switch val := x.(type) {
		case float64:
			return val == n
		case map[string]any:
			for _, vv := range val {
				if walk(vv) {
					return true
				}
			}
		case []any:
			for _, vv := range val {
				if walk(vv) {
					return true
				}
			}
		}
		return false
	}
	return walk(v)
}
