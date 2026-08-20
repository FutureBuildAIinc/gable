// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package matching

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/google/uuid"
)

// Three-way matching is the control that stops a vendor being paid for goods
// nobody received. Assertions below are CORRECTNESS tests unless labelled
// CHARACTERIZATION.
//
// RunMatch itself cannot be exercised here — see
// TestRunMatch_IsNotUnitTestable at the bottom of this file for why — so these
// tests cover the tolerance arithmetic, the config surface that decides what
// "within tolerance" means, and the HTTP layer.

// --- fake repository -----------------------------------------------------

type fakeRepo struct {
	cfg       *MatchConfig
	cfgErr    error
	updateErr error
	updated   []MatchConfig

	result     *MatchResult
	resultErr  error
	lines      []MatchLineDetail
	exceptions []MatchException
	excErr     error
}

func (f *fakeRepo) CreateMatchResult(context.Context, *MatchResult) error { return nil }

func (f *fakeRepo) GetMatchResult(context.Context, uuid.UUID) (*MatchResult, error) {
	if f.resultErr != nil {
		return nil, f.resultErr
	}
	return f.result, nil
}

func (f *fakeRepo) UpdateMatchResult(context.Context, *MatchResult) error { return nil }

func (f *fakeRepo) ListExceptions(context.Context) ([]MatchException, error) {
	return f.exceptions, f.excErr
}

func (f *fakeRepo) CreateMatchLineDetail(context.Context, *MatchLineDetail) error { return nil }

func (f *fakeRepo) GetMatchLineDetails(context.Context, uuid.UUID) ([]MatchLineDetail, error) {
	return f.lines, nil
}

func (f *fakeRepo) DeleteMatchLineDetails(context.Context, uuid.UUID) error { return nil }

func (f *fakeRepo) GetConfig(context.Context) (*MatchConfig, error) {
	if f.cfgErr != nil {
		return nil, f.cfgErr
	}
	if f.cfg == nil {
		return &MatchConfig{}, nil
	}
	cp := *f.cfg
	return &cp, nil
}

func (f *fakeRepo) UpdateConfig(_ context.Context, cfg *MatchConfig) error {
	if f.updateErr != nil {
		return f.updateErr
	}
	f.updated = append(f.updated, *cfg)
	return nil
}

var _ Repository = (*fakeRepo)(nil)

// newCfgService builds a service whose only live dependency is the repository.
// The PO and AP services are nil, which is safe for every method below because
// none of them touch those collaborators.
func newCfgService(repo Repository) *Service {
	return NewService(nil, repo, nil, nil, nil)
}

func ptrF(v float64) *float64 { return &v }
func ptrB(v bool) *bool       { return &v }

// --- variance arithmetic -------------------------------------------------

// CORRECTNESS: variance is (actual - expected) / expected, signed. The sign is
// what distinguishes an over-shipment from a short-shipment, and the magnitude
// is what the tolerance is compared against.
func TestCalcVariancePct(t *testing.T) {
	tests := []struct {
		name     string
		expected float64
		actual   float64
		want     float64
	}{
		{"exact match is zero variance", 100, 100, 0},
		{"10 percent over is positive", 100, 110, 10},
		{"10 percent short is negative", 100, 90, -10},
		{"nothing received against 100 ordered is -100 percent", 100, 0, -100},
		{"fractional quantities", 2.5, 3, 20},
		{"both zero is zero, not a division by zero", 0, 0, 0},
		{"received something against a zero-quantity line", 0, 5, 100},
		{"double the order", 100, 200, 100},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := calcVariancePct(tc.expected, tc.actual)
			if math.Abs(got-tc.want) > 1e-9 {
				t.Errorf("calcVariancePct(%v, %v) = %v, want %v", tc.expected, tc.actual, got, tc.want)
			}
			if math.IsNaN(got) || math.IsInf(got, 0) {
				t.Errorf("calcVariancePct(%v, %v) produced %v — a non-finite variance defeats every tolerance comparison", tc.expected, tc.actual, got)
			}
		})
	}
}

// CHARACTERIZATION: a zero-cost PO line that arrives with a non-zero invoice
// price reports exactly 100% variance regardless of how large that price is.
// $0 ordered, $5,000 invoiced and $0 ordered, $0.05 invoiced are
// indistinguishable to the tolerance check.
func TestCalcVariancePct_ZeroExpectedFlattensMagnitude(t *testing.T) {
	small := calcVariancePctInt(0, 5)
	huge := calcVariancePctInt(0, 500000)
	if small != huge {
		t.Fatalf("expected the zero-expected branch to flatten magnitude: got %v and %v", small, huge)
	}
	if small != 100 {
		t.Fatalf("calcVariancePctInt(0, n) = %v, want 100", small)
	}
}

// CORRECTNESS: the cents variant must agree with the float variant on the same
// economic facts, and must stay exact on cent-sized differences.
func TestCalcVariancePctInt(t *testing.T) {
	tests := []struct {
		name     string
		expected int64
		actual   int64
		want     float64
	}{
		{"same price", 10000, 10000, 0},
		{"one cent over on $100 is 0.01 percent", 10000, 10001, 0.01},
		{"one cent under on $100", 10000, 9999, -0.01},
		{"doubled price", 500, 1000, 100},
		{"halved price", 1000, 500, -50},
		{"both zero", 0, 0, 0},
		{"free line invoiced", 0, 100, 100},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := calcVariancePctInt(tc.expected, tc.actual)
			if math.Abs(got-tc.want) > 1e-9 {
				t.Errorf("calcVariancePctInt(%d, %d) = %v, want %v", tc.expected, tc.actual, got, tc.want)
			}
		})
	}
}

func TestAbs64(t *testing.T) {
	tests := []struct{ in, want int64 }{
		{0, 0}, {5, 5}, {-5, 5}, {math.MaxInt64, math.MaxInt64},
	}
	for _, tc := range tests {
		if got := abs64(tc.in); got != tc.want {
			t.Errorf("abs64(%d) = %d, want %d", tc.in, got, tc.want)
		}
	}
	// CHARACTERIZATION: two's complement has no positive counterpart for
	// MinInt64, so abs64 returns it unchanged (still negative). Any tolerance
	// comparison against it would pass trivially. Reaching this value requires
	// a ~$92 quadrillion price difference, so it is documented, not fixed.
	if got := abs64(math.MinInt64); got >= 0 {
		t.Errorf("abs64(MinInt64) = %d, want the (negative) MinInt64 overflow value", got)
	}
}

// --- tolerance config ----------------------------------------------------

// CORRECTNESS: the dollar tolerance is entered in dollars and stored in cents.
// Getting this conversion wrong by 100x is the difference between a $50 and a
// $5,000 rubber stamp on vendor invoices.
func TestUpdateConfig_DollarToleranceDollarsToCents(t *testing.T) {
	tests := []struct {
		name    string
		dollars float64
		want    int64
	}{
		{"fifty dollars", 50, 5000},
		{"with cents", 12.34, 1234},
		{"float-repr trap", 8.20, 820},
		{"zero disables the dollar override", 0, 0},
		{"one cent", 0.01, 1},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			repo := &fakeRepo{cfg: &MatchConfig{DollarTolerance: 999999}}
			svc := newCfgService(repo)

			got, err := svc.UpdateConfig(context.Background(), UpdateMatchConfigRequest{
				DollarTolerance: ptrF(tc.dollars),
			})
			if err != nil {
				t.Fatalf("UpdateConfig: %v", err)
			}
			if got.DollarTolerance != tc.want {
				t.Errorf("DollarTolerance = %d cents, want %d cents (from $%.2f)", got.DollarTolerance, tc.want, tc.dollars)
			}
			if len(repo.updated) != 1 || repo.updated[0].DollarTolerance != tc.want {
				t.Errorf("persisted %v, want DollarTolerance=%d", repo.updated, tc.want)
			}
		})
	}
}

// CORRECTNESS: the request is a patch. A field that is absent must not be
// silently reset to its zero value — zeroing PriceTolerancePct would flip
// every previously-matching line into an exception, and zeroing
// AutoApproveOnMatch would stop payments dead.
func TestUpdateConfig_OmittedFieldsAreUntouched(t *testing.T) {
	original := MatchConfig{
		QtyTolerancePct:    1.5,
		PriceTolerancePct:  2.0,
		DollarTolerance:    5000,
		AutoApproveOnMatch: true,
	}

	repo := &fakeRepo{cfg: &original}
	svc := newCfgService(repo)

	got, err := svc.UpdateConfig(context.Background(), UpdateMatchConfigRequest{
		QtyTolerancePct: ptrF(3.0), // the only field supplied
	})
	if err != nil {
		t.Fatalf("UpdateConfig: %v", err)
	}
	if got.QtyTolerancePct != 3.0 {
		t.Errorf("QtyTolerancePct = %v, want 3.0", got.QtyTolerancePct)
	}
	if got.PriceTolerancePct != original.PriceTolerancePct {
		t.Errorf("PriceTolerancePct = %v, want %v (unchanged)", got.PriceTolerancePct, original.PriceTolerancePct)
	}
	if got.DollarTolerance != original.DollarTolerance {
		t.Errorf("DollarTolerance = %v, want %v (unchanged)", got.DollarTolerance, original.DollarTolerance)
	}
	if got.AutoApproveOnMatch != original.AutoApproveOnMatch {
		t.Errorf("AutoApproveOnMatch = %v, want %v (unchanged)", got.AutoApproveOnMatch, original.AutoApproveOnMatch)
	}
}

// CORRECTNESS: turning auto-approval OFF must actually persist. A patch that
// sends false for a bool is the case a naive "if value != zero" implementation
// drops.
func TestUpdateConfig_CanDisableAutoApprove(t *testing.T) {
	repo := &fakeRepo{cfg: &MatchConfig{AutoApproveOnMatch: true}}
	svc := newCfgService(repo)

	got, err := svc.UpdateConfig(context.Background(), UpdateMatchConfigRequest{
		AutoApproveOnMatch: ptrB(false),
	})
	if err != nil {
		t.Fatalf("UpdateConfig: %v", err)
	}
	if got.AutoApproveOnMatch {
		t.Fatal("AutoApproveOnMatch stayed true: auto-approval could not be turned off")
	}
	if len(repo.updated) != 1 || repo.updated[0].AutoApproveOnMatch {
		t.Fatalf("persisted config still auto-approves: %v", repo.updated)
	}
}

// CORRECTNESS: if the current config cannot be read, the update must fail
// rather than write a config built from an empty struct — that would silently
// zero every tolerance.
func TestUpdateConfig_ReadFailureDoesNotWrite(t *testing.T) {
	repo := &fakeRepo{cfgErr: errors.New("db down")}
	svc := newCfgService(repo)

	if _, err := svc.UpdateConfig(context.Background(), UpdateMatchConfigRequest{QtyTolerancePct: ptrF(5)}); err == nil {
		t.Fatal("want an error when the existing config cannot be loaded")
	}
	if len(repo.updated) != 0 {
		t.Fatalf("wrote %d configs despite failing to read the current one", len(repo.updated))
	}
}

// CORRECTNESS: a failed write must be reported, not reported as success with
// the in-memory value.
func TestUpdateConfig_WriteFailurePropagates(t *testing.T) {
	repo := &fakeRepo{cfg: &MatchConfig{}, updateErr: errors.New("write failed")}
	if _, err := newCfgService(repo).UpdateConfig(context.Background(), UpdateMatchConfigRequest{QtyTolerancePct: ptrF(5)}); err == nil {
		t.Fatal("want an error when the config write fails")
	}
}

// KNOWN BUG. The dollar tolerance is applied to the per-UNIT price difference
// and, when satisfied, unconditionally overrides the percentage check. With
// the shipped default of $50 per unit, a $10 stud invoiced at $55 — a 450%
// overcharge — is treated as price-matched. A dollar tolerance is meant to
// stop trivial rounding exceptions, not to authorise arbitrary overcharges on
// low-value items, and it should apply to the extended line amount, not the
// unit price.
//
// backend/internal/matching/service.go:146-149:
//
//	priceDiffCents := abs64(detail.POUnitCost - detail.InvoiceUnitPrice)
//	if priceDiffCents <= cfg.DollarTolerance {
//	    priceOK = true
//	}
//
// The default DollarTolerance of 5000 cents is set at service.go:81.
func TestPriceTolerance_DollarOverrideMustNotBeatThePercentageCheck(t *testing.T) {
	t.Skip("KNOWN BUG: matching/service.go:146 applies the $50 dollar tolerance to the per-unit price, so a $10 item invoiced at $55 passes the price match")

	const (
		poUnitCost       int64 = 1000 // $10.00 ordered
		invoiceUnitPrice int64 = 5500 // $55.00 invoiced
	)
	cfg := MatchConfig{PriceTolerancePct: 2.0, DollarTolerance: 5000}

	pricePct := calcVariancePctInt(poUnitCost, invoiceUnitPrice)
	priceOK := math.Abs(pricePct) <= cfg.PriceTolerancePct
	if priceDiff := abs64(poUnitCost - invoiceUnitPrice); priceDiff <= cfg.DollarTolerance {
		priceOK = true // this is the line under test
	}

	if priceOK {
		t.Fatalf("a %.0f%% overcharge ($%.2f -> $%.2f) was accepted as price-matched",
			pricePct, float64(poUnitCost)/100, float64(invoiceUnitPrice)/100)
	}
}

// KNOWN BUG, recorded as a documented finding because the code path that
// contains it (RunMatch) has no test seam — see TestRunMatch_IsNotUnitTestable.
//
// A three-way match compares purchase order, RECEIPT and invoice. This
// implementation computes InvoicedQty and stores it on the line detail, but
// never compares it to anything: the only quantity variance computed is PO qty
// vs received qty. A vendor who ships 10 units and invoices 1,000 passes the
// quantity check, which is precisely the fraud a three-way match exists to
// stop.
//
// backend/internal/matching/service.go:136 —
//
//	detail.QtyVariancePct = calcVariancePct(detail.POQty, detail.ReceivedQty)
//
// InvoicedQty is assigned at service.go:131 and then read by nothing in the
// module: `grep -rn InvoicedQty internal/matching` shows only the model field,
// the assignment, and the repository's column mapping.
//
// This test body is intentionally empty: asserting the correct comparison here
// would only be testing arithmetic this test file wrote, not the service. It
// becomes a real test once RunMatch is reachable.
func TestThreeWayMatch_MustCompareInvoicedQtyToReceivedQty(t *testing.T) {
	t.Skip("KNOWN BUG: matching/service.go:136 compares PO qty to received qty only; invoiced qty is stored and never checked, so over-invoicing passes the quantity match")
}

// KNOWN BUG. PO line costs are dollars in a float64 and are converted with a
// bare truncating cast, so ordinary prices lose a cent: 10.99 dollars is
// 1098.9999999999998 in binary floating point and truncates to 1098 cents.
// Every downstream price comparison then starts one cent wrong.
//
// backend/internal/matching/service.go:126 —
//
//	POUnitCost: int64(poLine.Cost * 100.0),
//
// The rest of the codebase uses int64(x*100.0 + 0.5) for this conversion.
func TestPOUnitCost_DollarsToCentsMustRoundNotTruncate(t *testing.T) {
	t.Skip("KNOWN BUG: matching/service.go:126 truncates dollars to cents, so a $10.99 PO line is stored as 1098 cents")

	tests := []struct {
		dollars float64
		want    int64
	}{
		{10.99, 1099},
		{8.20, 820},
		{0.29, 29},
		{1.15, 115},
	}
	for _, tc := range tests {
		if got := int64(tc.dollars * 100.0); got != tc.want {
			t.Errorf("int64(%v*100) = %d cents, want %d cents", tc.dollars, got, tc.want)
		}
	}
}

// --- read-through service methods ---------------------------------------

// CORRECTNESS: GetMatchResult must attach the line details, because the
// exception detail is the whole point of the endpoint.
func TestGetMatchResult_AttachesLines(t *testing.T) {
	resultID := uuid.New()
	repo := &fakeRepo{
		result: &MatchResult{ID: resultID, Status: MatchStatusException},
		lines: []MatchLineDetail{
			{MatchResultID: resultID, LineStatus: MatchStatusException, Description: "2x4x8 SPF"},
		},
	}
	got, err := newCfgService(repo).GetMatchResult(context.Background(), uuid.New())
	if err != nil {
		t.Fatalf("GetMatchResult: %v", err)
	}
	if len(got.Lines) != 1 || got.Lines[0].Description != "2x4x8 SPF" {
		t.Fatalf("Lines = %#v, want the fetched line detail", got.Lines)
	}
}

func TestGetMatchResult_PropagatesNotFound(t *testing.T) {
	repo := &fakeRepo{resultErr: errors.New("no such match")}
	if _, err := newCfgService(repo).GetMatchResult(context.Background(), uuid.New()); err == nil {
		t.Fatal("want an error when the match result does not exist")
	}
}

// --- HTTP layer ----------------------------------------------------------

func newTestMux(repo Repository) *http.ServeMux {
	mux := http.NewServeMux()
	NewHandler(newCfgService(repo)).RegisterRoutes(mux)
	return mux
}

// CORRECTNESS: a malformed PO id must be a client error, and must never reach
// the service.
func TestHandler_RejectsMalformedPOID(t *testing.T) {
	mux := newTestMux(&fakeRepo{cfg: &MatchConfig{}})

	for _, path := range []string{"/api/v1/matching/results/not-a-uuid"} {
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
		if rec.Code != http.StatusBadRequest {
			t.Errorf("GET %s = %d, want 400", path, rec.Code)
		}
	}

	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/v1/matching/run/not-a-uuid", nil))
	if rec.Code != http.StatusBadRequest {
		t.Errorf("POST /api/v1/matching/run/not-a-uuid = %d, want 400", rec.Code)
	}
}

// CORRECTNESS: an empty exception list must serialise as [] and not null, or
// every client has to special-case a null before iterating.
func TestHandler_ListExceptionsEmptyIsArrayNotNull(t *testing.T) {
	mux := newTestMux(&fakeRepo{})
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/matching/exceptions", nil))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if got := strings.TrimSpace(rec.Body.String()); got != "[]" {
		t.Errorf("body = %s, want []", got)
	}
}

func TestHandler_ListExceptionsPropagatesFailure(t *testing.T) {
	mux := newTestMux(&fakeRepo{excErr: errors.New("db down")})
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/matching/exceptions", nil))
	if rec.Code != http.StatusInternalServerError {
		t.Errorf("status = %d, want 500", rec.Code)
	}
}

// CORRECTNESS: the config endpoint is the operator-facing control over how
// much money can slip through unreviewed. The round trip must preserve the
// units: dollars in, dollars-converted-to-cents out.
func TestHandler_UpdateConfigRoundTrip(t *testing.T) {
	repo := &fakeRepo{cfg: &MatchConfig{QtyTolerancePct: 1, PriceTolerancePct: 1, DollarTolerance: 100, AutoApproveOnMatch: true}}
	mux := newTestMux(repo)

	body := `{"price_tolerance_pct":2.5,"dollar_tolerance":12.34,"auto_approve_on_match":false}`
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodPut, "/api/v1/matching/config", strings.NewReader(body)))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body %s)", rec.Code, rec.Body.String())
	}
	var got MatchConfig
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if got.PriceTolerancePct != 2.5 {
		t.Errorf("PriceTolerancePct = %v, want 2.5", got.PriceTolerancePct)
	}
	if got.DollarTolerance != 1234 {
		t.Errorf("DollarTolerance = %d, want 1234 cents from $12.34", got.DollarTolerance)
	}
	if got.AutoApproveOnMatch {
		t.Error("AutoApproveOnMatch = true, want false")
	}
	if got.QtyTolerancePct != 1 {
		t.Errorf("QtyTolerancePct = %v, want 1 (not supplied, must be preserved)", got.QtyTolerancePct)
	}
}

func TestHandler_UpdateConfigRejectsMalformedJSON(t *testing.T) {
	mux := newTestMux(&fakeRepo{cfg: &MatchConfig{}})
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodPut, "/api/v1/matching/config", strings.NewReader("{not json")))
	if rec.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", rec.Code)
	}
}

// CORRECTNESS: the role guard passed at registration must actually wrap every
// route. A matching endpoint reachable without the finance role would let
// anyone widen the tolerances that gate vendor payment.
func TestRegisterRoutes_RoleGuardWrapsEveryEndpoint(t *testing.T) {
	var guarded int
	guard := func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			guarded++
			w.WriteHeader(http.StatusForbidden)
		})
	}

	mux := http.NewServeMux()
	NewHandler(newCfgService(&fakeRepo{cfg: &MatchConfig{}})).RegisterRoutes(mux, guard)

	routes := []struct{ method, path string }{
		{http.MethodPost, "/api/v1/matching/run/" + uuid.NewString()},
		{http.MethodGet, "/api/v1/matching/results/" + uuid.NewString()},
		{http.MethodGet, "/api/v1/matching/exceptions"},
		{http.MethodGet, "/api/v1/matching/config"},
		{http.MethodPut, "/api/v1/matching/config"},
	}

	for _, r := range routes {
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, httptest.NewRequest(r.method, r.path, strings.NewReader("{}")))
		if rec.Code != http.StatusForbidden {
			t.Errorf("%s %s = %d, want 403: the role guard did not wrap this route", r.method, r.path, rec.Code)
		}
	}
	if guarded != len(routes) {
		t.Errorf("guard ran %d times, want %d", guarded, len(routes))
	}
}

// TestRunMatch_IsNotUnitTestable documents a testability gap rather than
// behaviour.
//
// Service.RunMatch is the actual three-way match, but it reaches for
// *purchase_order.Service and *ap.Service, which are concrete struct pointers.
// purchase_order.NewService takes a *purchase_order.Repository — also a
// concrete struct, holding a *database.DB — so there is no seam at which a
// test can supply a PO without Postgres running. The fix is a one-line
// interface extraction in the matching service (accept a
// `interface{ GetPO(ctx, id) (*purchase_order.PO, error) }`), which is a
// production-code change and therefore out of scope for this test pass.
func TestRunMatch_IsNotUnitTestable(t *testing.T) {
	t.Skip("TESTABILITY GAP: matching/service.go:24-30 depends on concrete *purchase_order.Service and *ap.Service, which require Postgres; RunMatch cannot be unit-tested without extracting an interface")
}
