// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package matching

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"math"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gablelbm/gable/internal/ap"
	"github.com/gablelbm/gable/internal/purchase_order"
	"github.com/google/uuid"
)

// Three-way matching is the control that stops a vendor being paid for goods
// nobody received. Assertions below are CORRECTNESS tests unless labelled
// CHARACTERIZATION.
//
// RunMatch is exercised directly: the service depends on the consumer-defined
// POSource and APSource interfaces (see TestRunMatch_SeamIsConsumerDefined at
// the bottom of this file), so the match runs against fakes with no Postgres.
// The rest covers the tolerance arithmetic, the config surface that decides
// what "within tolerance" means, and the HTTP layer.

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

	created       []MatchLineDetail // every detail handed to CreateMatchLineDetail
	updatedResult []MatchResult     // every result handed to UpdateMatchResult
}

func (f *fakeRepo) CreateMatchResult(context.Context, *MatchResult) error { return nil }

func (f *fakeRepo) GetMatchResult(context.Context, uuid.UUID) (*MatchResult, error) {
	if f.resultErr != nil {
		return nil, f.resultErr
	}
	return f.result, nil
}

func (f *fakeRepo) UpdateMatchResult(_ context.Context, r *MatchResult) error {
	f.updatedResult = append(f.updatedResult, *r)
	return nil
}

func (f *fakeRepo) ListExceptions(context.Context) ([]MatchException, error) {
	return f.exceptions, f.excErr
}

func (f *fakeRepo) CreateMatchLineDetail(_ context.Context, d *MatchLineDetail) error {
	f.created = append(f.created, *d)
	return nil
}

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

// --- fake PO / AP collaborators ------------------------------------------
//
// matching.Service takes the consumer-defined POSource and APSource
// interfaces, so the three-way match itself runs here with no Postgres. The
// real *purchase_order.Service and *ap.Service satisfy the same interfaces.

type fakePO struct {
	po   *purchase_order.PurchaseOrder
	err  error
	seen []uuid.UUID
}

func (f *fakePO) GetPO(_ context.Context, id uuid.UUID) (*purchase_order.PurchaseOrder, error) {
	f.seen = append(f.seen, id)
	if f.err != nil {
		return nil, f.err
	}
	return f.po, nil
}

var _ POSource = (*fakePO)(nil)

type approval struct {
	invoiceID  uuid.UUID
	approverID uuid.UUID
}

type fakeAP struct {
	headers    []ap.VendorInvoice             // what ListVendorInvoices returns
	full       map[uuid.UUID]ap.VendorInvoice // what GetVendorInvoice returns (with lines)
	listErr    error
	getErr     error
	approveErr error
	approvals  []approval
}

func (f *fakeAP) ListVendorInvoices(context.Context, *uuid.UUID, string) ([]ap.VendorInvoice, error) {
	return f.headers, f.listErr
}

func (f *fakeAP) GetVendorInvoice(_ context.Context, id uuid.UUID) (*ap.VendorInvoice, error) {
	if f.getErr != nil {
		return nil, f.getErr
	}
	inv, ok := f.full[id]
	if !ok {
		return nil, errors.New("vendor invoice not found")
	}
	return &inv, nil
}

func (f *fakeAP) ApproveInvoice(_ context.Context, invoiceID, approverID uuid.UUID) (*ap.VendorInvoice, error) {
	f.approvals = append(f.approvals, approval{invoiceID: invoiceID, approverID: approverID})
	if f.approveErr != nil {
		return nil, f.approveErr
	}
	inv := f.full[invoiceID]
	return &inv, nil
}

var _ APSource = (*fakeAP)(nil)

func quietLogger() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

// newMatchService wires the full three-way matcher against fakes.
func newMatchService(repo Repository, po POSource, apSvc APSource) *Service {
	return NewService(nil, repo, po, apSvc, quietLogger())
}

// poWithLines builds a PO whose lines are given as (ordered, received, unit
// cost in dollars) triples.
type poLineSpec struct {
	ordered  float64
	received float64
	cost     float64 // dollars, as purchase_order stores them
}

func poWithLines(specs ...poLineSpec) *purchase_order.PurchaseOrder {
	po := &purchase_order.PurchaseOrder{ID: uuid.New()}
	for _, s := range specs {
		po.Lines = append(po.Lines, purchase_order.PurchaseOrderLine{
			ID:          uuid.New(),
			POID:        po.ID,
			Description: "2x4x8 SPF",
			Quantity:    s.ordered,
			QtyReceived: s.received,
			Cost:        s.cost,
		})
	}
	return po
}

// invoiceFor builds a vendor invoice linked to poID, with (qty, unit price in
// cents) line pairs.
type invLineSpec struct {
	qty       float64
	unitPrice int64 // cents, as ap stores them
}

func invoiceFor(poID uuid.UUID, specs ...invLineSpec) *fakeAP {
	inv := ap.VendorInvoice{ID: uuid.New(), POID: &poID, InvoiceNumber: "V-1001"}
	for _, s := range specs {
		inv.Lines = append(inv.Lines, ap.VendorInvoiceLine{
			ID:        uuid.New(),
			InvoiceID: inv.ID,
			Quantity:  s.qty,
			UnitPrice: s.unitPrice,
			LineTotal: int64(s.qty * float64(s.unitPrice)),
		})
	}
	header := inv
	header.Lines = nil // ListVendorInvoices returns headers without lines
	return &fakeAP{
		headers: []ap.VendorInvoice{header},
		full:    map[uuid.UUID]ap.VendorInvoice{inv.ID: inv},
	}
}

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

// --- the three-way match itself ------------------------------------------

// REGRESSION (this was a documented, pinned defect). The dollar tolerance was applied to the
// per-UNIT price difference and, when satisfied, unconditionally overrode the
// percentage check. With the shipped default of $50 per unit, a $10 stud
// invoiced at $55 — a 450% overcharge — was price-matched no matter how many
// thousands of studs were on the line.
//
// It is now a floor on the EXTENDED line amount beside the percentage check:
// "do not raise an exception for less than this many dollars". The overcharge
// below is $4,500 on the line and must be an exception.
func TestPriceTolerance_DollarOverrideMustNotBeatThePercentageCheck(t *testing.T) {
	po := poWithLines(poLineSpec{ordered: 100, received: 100, cost: 10.00})
	apSvc := invoiceFor(po.ID, invLineSpec{qty: 100, unitPrice: 5500}) // $55.00 each
	repo := &fakeRepo{cfg: &MatchConfig{QtyTolerancePct: 0, PriceTolerancePct: 2.0, DollarTolerance: 5000}}

	result, err := newMatchService(repo, &fakePO{po: po}, apSvc).RunMatch(context.Background(), po.ID)
	if err != nil {
		t.Fatalf("RunMatch: %v", err)
	}
	if len(repo.created) != 1 {
		t.Fatalf("wrote %d line details, want 1", len(repo.created))
	}
	line := repo.created[0]
	if line.LineStatus != MatchStatusException {
		t.Errorf("a 450%% overcharge ($10.00 -> $55.00 x 100) was accepted as price-matched: %#v", line)
	}
	if result.Status != MatchStatusException {
		t.Errorf("result status = %s, want EXCEPTION", result.Status)
	}
	if len(apSvc.approvals) != 0 {
		t.Errorf("the vendor invoice was auto-approved despite the overcharge: %v", apSvc.approvals)
	}
}

// CORRECTNESS: the dollar tolerance still does what it is for — swallowing a
// difference too small to be worth an operator's time — and it is measured on
// the whole line, so it cannot be multiplied up by the quantity.
func TestPriceTolerance_DollarFloorAppliesToTheLineTotal(t *testing.T) {
	cfg := &MatchConfig{QtyTolerancePct: 0, PriceTolerancePct: 2.0, DollarTolerance: 5000} // $50

	tests := []struct {
		name       string
		qty        float64
		poCents    int64
		invCents   int64
		wantStatus MatchStatus
	}{
		{
			// 10c over on a $10 item is 1% — inside the percentage tolerance.
			name: "small percentage variance matches on percentage alone",
			qty:  1000, poCents: 1000, invCents: 1010, wantStatus: MatchStatusMatched,
		},
		{
			// 30% over, but only $3.00 on the whole line: below the floor the
			// operator configured, so not worth an exception.
			name: "large percentage but a trivial line amount is forgiven",
			qty:  1, poCents: 1000, invCents: 1300, wantStatus: MatchStatusMatched,
		},
		{
			// The same 30% over, on a line that is actually worth money.
			name: "the same percentage on a real line is an exception",
			qty:  1000, poCents: 1000, invCents: 1300, wantStatus: MatchStatusException,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			detail := MatchLineDetail{
				POQty: tc.qty, ReceivedQty: tc.qty, InvoicedQty: tc.qty,
				POUnitCost: tc.poCents, InvoiceUnitPrice: tc.invCents,
			}
			if got := evaluateLine(&detail, cfg, true); got != tc.wantStatus {
				t.Errorf("status = %s, want %s (line diff %d cents vs tolerance %d)",
					got, tc.wantStatus, lineAmountDiffCents(&detail), cfg.DollarTolerance)
			}
		})
	}
}

// REGRESSION (this was a documented, pinned defect). A three-way match compares purchase order,
// RECEIPT and invoice. The implementation computed InvoicedQty and stored it
// on the line detail, but never compared it to anything: the only quantity
// variance computed was PO qty vs received qty. A vendor who shipped 10 units
// and invoiced 1,000 passed the quantity check, which is precisely the fraud a
// three-way match exists to stop.
func TestThreeWayMatch_MustCompareInvoicedQtyToReceivedQty(t *testing.T) {
	po := poWithLines(poLineSpec{ordered: 10, received: 10, cost: 10.00})
	// Right price, right receipt — 100x the quantity billed.
	apSvc := invoiceFor(po.ID, invLineSpec{qty: 1000, unitPrice: 1000})
	repo := &fakeRepo{cfg: &MatchConfig{QtyTolerancePct: 0, PriceTolerancePct: 2.0, DollarTolerance: 5000}}

	result, err := newMatchService(repo, &fakePO{po: po}, apSvc).RunMatch(context.Background(), po.ID)
	if err != nil {
		t.Fatalf("RunMatch: %v", err)
	}
	if len(repo.created) != 1 {
		t.Fatalf("wrote %d line details, want 1", len(repo.created))
	}
	line := repo.created[0]
	if line.LineStatus != MatchStatusException {
		t.Errorf("10 received, 1000 invoiced was accepted as quantity-matched: %#v", line)
	}
	if line.InvoicedQty != 1000 {
		t.Errorf("InvoicedQty = %v, want the 1000 the vendor billed", line.InvoicedQty)
	}
	if math.Abs(line.QtyVariancePct) < 1 {
		t.Errorf("QtyVariancePct = %v, want the over-invoicing to be reported, not hidden behind a clean receipt",
			line.QtyVariancePct)
	}
	if result.Status != MatchStatusException {
		t.Errorf("result status = %s, want EXCEPTION", result.Status)
	}
	if len(apSvc.approvals) != 0 {
		t.Errorf("the over-invoiced vendor invoice was auto-approved: %v", apSvc.approvals)
	}
}

// CORRECTNESS: the ordered -> received hop is still checked. A short shipment
// is an exception even when the invoice agrees with what arrived.
func TestThreeWayMatch_ShortShipmentIsStillAnException(t *testing.T) {
	po := poWithLines(poLineSpec{ordered: 100, received: 60, cost: 10.00})
	apSvc := invoiceFor(po.ID, invLineSpec{qty: 60, unitPrice: 1000})
	repo := &fakeRepo{cfg: &MatchConfig{QtyTolerancePct: 0, PriceTolerancePct: 2.0, DollarTolerance: 5000}}

	if _, err := newMatchService(repo, &fakePO{po: po}, apSvc).RunMatch(context.Background(), po.ID); err != nil {
		t.Fatalf("RunMatch: %v", err)
	}
	if repo.created[0].LineStatus != MatchStatusException {
		t.Errorf("60 of 100 received was accepted as quantity-matched: %#v", repo.created[0])
	}
}

// REGRESSION (this was a documented, pinned defect). PO line costs are dollars in a float64 and
// were converted with a bare truncating cast, so ordinary prices lost a cent:
// $10.99 is 1098.9999999999998 in binary floating point and truncated to 1098.
// Every downstream price comparison then started one cent wrong.
func TestPOUnitCost_DollarsToCentsMustRoundNotTruncate(t *testing.T) {
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
		po := poWithLines(poLineSpec{ordered: 1, received: 1, cost: tc.dollars})
		apSvc := invoiceFor(po.ID, invLineSpec{qty: 1, unitPrice: tc.want})
		repo := &fakeRepo{cfg: &MatchConfig{QtyTolerancePct: 0, PriceTolerancePct: 0, DollarTolerance: 0}}

		if _, err := newMatchService(repo, &fakePO{po: po}, apSvc).RunMatch(context.Background(), po.ID); err != nil {
			t.Fatalf("RunMatch: %v", err)
		}
		got := repo.created[0]
		if got.POUnitCost != tc.want {
			t.Errorf("PO cost $%v stored as %d cents, want %d", tc.dollars, got.POUnitCost, tc.want)
		}
		// With zero tolerances, an exactly-correct invoice must still match:
		// a one-cent conversion error would show up here as an exception.
		if got.LineStatus != MatchStatusMatched {
			t.Errorf("PO cost $%v vs an invoice at the same price was an exception: %#v", tc.dollars, got)
		}
	}
}

// CORRECTNESS: a clean match auto-approves the vendor invoice under the
// well-known system actor, and records the result as MATCHED.
func TestRunMatch_CleanMatchAutoApproves(t *testing.T) {
	po := poWithLines(
		poLineSpec{ordered: 100, received: 100, cost: 10.99},
		poLineSpec{ordered: 4, received: 4, cost: 250.00},
	)
	apSvc := invoiceFor(po.ID,
		invLineSpec{qty: 100, unitPrice: 1099},
		invLineSpec{qty: 4, unitPrice: 25000},
	)
	repo := &fakeRepo{cfg: &MatchConfig{QtyTolerancePct: 0, PriceTolerancePct: 2.0, DollarTolerance: 0, AutoApproveOnMatch: true}}

	result, err := newMatchService(repo, &fakePO{po: po}, apSvc).RunMatch(context.Background(), po.ID)
	if err != nil {
		t.Fatalf("RunMatch: %v", err)
	}
	if result.Status != MatchStatusMatched {
		t.Fatalf("status = %s, want MATCHED (%s)", result.Status, result.Notes)
	}
	if len(apSvc.approvals) != 1 {
		t.Fatalf("approvals = %v, want exactly one", apSvc.approvals)
	}
	if apSvc.approvals[0].approverID != SystemApproverID {
		t.Errorf("approver = %s, want the system actor %s", apSvc.approvals[0].approverID, SystemApproverID)
	}
	if result.VendorInvoiceID == nil || *result.VendorInvoiceID != apSvc.headers[0].ID {
		t.Errorf("VendorInvoiceID = %v, want the linked invoice %s", result.VendorInvoiceID, apSvc.headers[0].ID)
	}
}

// CORRECTNESS: auto-approval is a configurable control. With it off, a clean
// match must still not pay the vendor.
func TestRunMatch_AutoApproveCanBeDisabled(t *testing.T) {
	po := poWithLines(poLineSpec{ordered: 10, received: 10, cost: 10.00})
	apSvc := invoiceFor(po.ID, invLineSpec{qty: 10, unitPrice: 1000})
	repo := &fakeRepo{cfg: &MatchConfig{PriceTolerancePct: 2.0, AutoApproveOnMatch: false}}

	result, err := newMatchService(repo, &fakePO{po: po}, apSvc).RunMatch(context.Background(), po.ID)
	if err != nil {
		t.Fatalf("RunMatch: %v", err)
	}
	if result.Status != MatchStatusMatched {
		t.Fatalf("status = %s, want MATCHED", result.Status)
	}
	if len(apSvc.approvals) != 0 {
		t.Errorf("auto-approval was off but the invoice was approved: %v", apSvc.approvals)
	}
}

// CORRECTNESS: a PO with no linked vendor invoice cannot be matched — there is
// no third document — and nothing may be approved.
func TestRunMatch_NoVendorInvoiceIsAnException(t *testing.T) {
	po := poWithLines(poLineSpec{ordered: 10, received: 10, cost: 10.00})
	apSvc := &fakeAP{full: map[uuid.UUID]ap.VendorInvoice{}}
	repo := &fakeRepo{cfg: &MatchConfig{PriceTolerancePct: 2.0, AutoApproveOnMatch: true}}

	result, err := newMatchService(repo, &fakePO{po: po}, apSvc).RunMatch(context.Background(), po.ID)
	if err != nil {
		t.Fatalf("RunMatch: %v", err)
	}
	if result.Status != MatchStatusException {
		t.Errorf("status = %s, want EXCEPTION with no invoice to match", result.Status)
	}
	if result.VendorInvoiceID != nil {
		t.Errorf("VendorInvoiceID = %v, want nil", result.VendorInvoiceID)
	}
	if len(apSvc.approvals) != 0 {
		t.Errorf("approved something with no invoice: %v", apSvc.approvals)
	}
}

// CORRECTNESS: a PO that cannot be loaded, or has no lines, is an error rather
// than a vacuous "all lines matched".
func TestRunMatch_RejectsUnusablePOs(t *testing.T) {
	repo := &fakeRepo{cfg: &MatchConfig{}}

	t.Run("load failure", func(t *testing.T) {
		po := &fakePO{err: errors.New("db down")}
		if _, err := newMatchService(repo, po, &fakeAP{}).RunMatch(context.Background(), uuid.New()); err == nil {
			t.Fatal("want an error when the PO cannot be loaded")
		}
	})

	t.Run("no lines", func(t *testing.T) {
		po := &fakePO{po: &purchase_order.PurchaseOrder{ID: uuid.New()}}
		if _, err := newMatchService(repo, po, &fakeAP{}).RunMatch(context.Background(), po.po.ID); err == nil {
			t.Fatal("want an error for a PO with no lines")
		}
		if len(repo.created) != 0 {
			t.Error("wrote line details for a PO with no lines")
		}
	})
}

// CORRECTNESS: a mixed PO reports PARTIAL, and the counts in the notes are the
// numbers an operator reconciles against.
func TestRunMatch_PartialReportsBothCounts(t *testing.T) {
	po := poWithLines(
		poLineSpec{ordered: 10, received: 10, cost: 10.00}, // clean
		poLineSpec{ordered: 10, received: 10, cost: 10.00}, // over-invoiced
	)
	apSvc := invoiceFor(po.ID,
		invLineSpec{qty: 10, unitPrice: 1000},
		invLineSpec{qty: 100, unitPrice: 1000},
	)
	repo := &fakeRepo{cfg: &MatchConfig{QtyTolerancePct: 0, PriceTolerancePct: 2.0, AutoApproveOnMatch: true}}

	result, err := newMatchService(repo, &fakePO{po: po}, apSvc).RunMatch(context.Background(), po.ID)
	if err != nil {
		t.Fatalf("RunMatch: %v", err)
	}
	if result.Status != MatchStatusPartial {
		t.Fatalf("status = %s, want PARTIAL", result.Status)
	}
	if !strings.Contains(result.Notes, "1/2 lines matched, 1 exceptions") {
		t.Errorf("notes = %q, want the matched/exception counts", result.Notes)
	}
	if len(apSvc.approvals) != 0 {
		t.Errorf("a PARTIAL match auto-approved the invoice: %v", apSvc.approvals)
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

// TestRunMatch_SeamIsConsumerDefined pins the seam that makes every RunMatch
// test above possible.
//
// RunMatch used to reach for *purchase_order.Service and *ap.Service, concrete
// struct pointers whose repositories hold a *database.DB, so there was no
// point at which a test could supply a PO without Postgres running. The
// dependencies are now the consumer-defined POSource and APSource interfaces.
// The real services must keep satisfying them, or main.go stops compiling.
func TestRunMatch_SeamIsConsumerDefined(t *testing.T) {
	var _ POSource = (*purchase_order.Service)(nil)
	var _ APSource = (*ap.Service)(nil)
}
