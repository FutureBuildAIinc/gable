// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package quote

import (
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"
)

// Quotes are the float64-dollars side of the money boundary documented in
// CLAUDE.md ("money-as-int64-cents is the target, not the current reality").
// The tests below are split into:
//
//   - CORRECTNESS tests: total assembly, state machine, edit rules. These must
//     keep holding after the planned float64 -> int64 cents refactor.
//   - CHARACTERIZATION tests: they pin the exact float64 results today's code
//     produces, including the binary-representation artefacts. A cents refactor
//     WILL change these numbers; that is the point — they must be revisited
//     deliberately, not drift silently.

// --- fake repository ---------------------------------------------------------

type fakeRepo struct {
	stored     *Quote
	getQuote   *Quote
	getErr     error
	createErr  error
	updateErr  error
	updateCall int
	withLines  int
}

func (f *fakeRepo) CreateQuote(_ context.Context, q *Quote) error {
	if f.createErr != nil {
		return f.createErr
	}
	if q.ID == uuid.Nil {
		q.ID = uuid.New()
	}
	cp := *q
	f.stored = &cp
	return nil
}

func (f *fakeRepo) GetQuote(context.Context, uuid.UUID) (*Quote, error) {
	if f.getErr != nil {
		return nil, f.getErr
	}
	return f.getQuote, nil
}

func (f *fakeRepo) UpdateQuote(_ context.Context, q *Quote) error {
	f.updateCall++
	if f.updateErr != nil {
		return f.updateErr
	}
	cp := *q
	f.stored = &cp
	return nil
}

func (f *fakeRepo) UpdateQuoteWithLines(_ context.Context, q *Quote) error {
	f.withLines++
	if f.updateErr != nil {
		return f.updateErr
	}
	cp := *q
	f.stored = &cp
	return nil
}

func (f *fakeRepo) ListQuotes(context.Context) ([]Quote, error) { return nil, nil }
func (f *fakeRepo) ListQuotesPaginated(context.Context, int, int) ([]Quote, int, error) {
	return nil, 0, nil
}
func (f *fakeRepo) ListQuotesByCustomer(context.Context, uuid.UUID) ([]Quote, error) {
	return nil, nil
}
func (f *fakeRepo) GetQuoteAnalytics(context.Context) (*QuoteAnalytics, error) { return nil, nil }
func (f *fakeRepo) GetOriginalFile(context.Context, uuid.UUID) ([]byte, string, string, error) {
	return nil, "", "", nil
}

// fakePO records auto-PO calls made when a quote is accepted.
type fakePO struct {
	calls []poCall
	err   error
}

type poCall struct {
	productID uuid.UUID
	quantity  float64
	unitCost  float64
	lineID    uuid.UUID
}

func (f *fakePO) CreatePOFromSpecialOrderLine(_ context.Context, productID uuid.UUID, _ *uuid.UUID, qty, unitCost float64, lineID uuid.UUID) error {
	f.calls = append(f.calls, poCall{productID, qty, unitCost, lineID})
	return f.err
}

func qline(qty, unitPrice float64) QuoteLine {
	return QuoteLine{ID: uuid.New(), ProductID: uuid.New(), Quantity: qty, UnitPrice: unitPrice}
}

// --- total assembly ----------------------------------------------------------

// CORRECTNESS: TotalAmount is the sum of every line extension plus freight, and
// each line's LineTotal is stamped. These cases use values that are exact in
// binary floating point, so they hold before and after a cents refactor.
func TestCreateQuote_TotalAssembly(t *testing.T) {
	tests := []struct {
		name          string
		lines         []QuoteLine
		freight       float64
		deliveryType  string
		wantLineTotal []float64
		wantTotal     float64
	}{
		{
			name:          "single line",
			lines:         []QuoteLine{qline(4, 12.5)},
			deliveryType:  "DELIVERY",
			wantLineTotal: []float64{50},
			wantTotal:     50,
		},
		{
			name:          "several lines accumulate",
			lines:         []QuoteLine{qline(2, 100), qline(3, 0.25), qline(8, 12.5)},
			deliveryType:  "DELIVERY",
			wantLineTotal: []float64{200, 0.75, 100},
			wantTotal:     300.75,
		},
		{
			name:          "freight is added on a delivery",
			lines:         []QuoteLine{qline(1, 1000)},
			freight:       125.5,
			deliveryType:  "DELIVERY",
			wantLineTotal: []float64{1000},
			wantTotal:     1125.5,
		},
		{
			name:          "no lines and no freight is zero",
			lines:         nil,
			deliveryType:  "DELIVERY",
			wantLineTotal: nil,
			wantTotal:     0,
		},
		{
			name:          "a returned/negative line reduces the total",
			lines:         []QuoteLine{qline(4, 12.5), qline(-1, 12.5)},
			deliveryType:  "DELIVERY",
			wantLineTotal: []float64{50, -12.5},
			wantTotal:     37.5,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			repo := &fakeRepo{}
			svc := NewService(repo)

			q := &Quote{
				CustomerID:    uuid.New(),
				Lines:         tc.lines,
				FreightAmount: tc.freight,
				DeliveryType:  tc.deliveryType,
			}
			if err := svc.CreateQuote(context.Background(), q); err != nil {
				t.Fatalf("CreateQuote: %v", err)
			}
			if q.TotalAmount != tc.wantTotal {
				t.Errorf("TotalAmount = %v, want %v", q.TotalAmount, tc.wantTotal)
			}
			for i, want := range tc.wantLineTotal {
				if q.Lines[i].LineTotal != want {
					t.Errorf("line %d LineTotal = %v, want %v", i, q.Lines[i].LineTotal, want)
				}
			}
			if repo.stored == nil {
				t.Fatal("quote was not persisted")
			}
			if repo.stored.TotalAmount != tc.wantTotal {
				t.Errorf("persisted TotalAmount = %v, want %v", repo.stored.TotalAmount, tc.wantTotal)
			}
		})
	}
}

// CHARACTERIZATION — the float64 dollars boundary.
//
// Quote money is float64 dollars (quote/model.go:31,70-72). Summing line
// extensions in binary floating point produces values that are NOT the exact
// decimal answer. Moving quotes to int64 cents will change every number below,
// so they are pinned here to make that change visible.
//
// Each case records: the float64 the code produces today, and the exact decimal
// amount a cents-based implementation would produce.
func TestCreateQuote_FloatDollarAccumulation_Characterization(t *testing.T) {
	tests := []struct {
		name         string
		lines        []QuoteLine
		freight      float64
		currentF64   float64 // what the float64 implementation yields today
		exactCents   int64   // what an int64-cents implementation would yield
		exactlyEqual bool    // whether currentF64 already equals the exact decimal
	}{
		{
			name:         "0.10 + 0.20 is not 0.30",
			lines:        []QuoteLine{qline(1, 0.10), qline(1, 0.20)},
			currentF64:   0.30000000000000004,
			exactCents:   30,
			exactlyEqual: false,
		},
		{
			// This one happens to land on the same float64 as 6867/100.0 — the
			// accumulated error cancels. Pinned as equal precisely because it is
			// NOT reliable: the next case adds freight to the same lines and the
			// equality disappears. Float64 money is right by luck, not by rule.
			name:         "3 @ $19.99 plus 2 @ $4.35",
			lines:        []QuoteLine{qline(3, 19.99), qline(2, 4.35)},
			currentF64:   68.670000000000002,
			exactCents:   6867,
			exactlyEqual: true,
		},
		{
			name:         "same order with $75.50 freight",
			lines:        []QuoteLine{qline(3, 19.99), qline(2, 4.35)},
			freight:      75.50,
			currentF64:   144.17000000000002,
			exactCents:   14417,
			exactlyEqual: false,
		},
		{
			name: "ten 10-cent lines do not sum to a dollar",
			lines: []QuoteLine{
				qline(1, 0.10), qline(1, 0.10), qline(1, 0.10), qline(1, 0.10), qline(1, 0.10),
				qline(1, 0.10), qline(1, 0.10), qline(1, 0.10), qline(1, 0.10), qline(1, 0.10),
			},
			currentF64:   0.99999999999999989,
			exactCents:   100,
			exactlyEqual: false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			svc := NewService(&fakeRepo{})
			q := &Quote{
				CustomerID:    uuid.New(),
				Lines:         tc.lines,
				FreightAmount: tc.freight,
				DeliveryType:  "DELIVERY",
			}
			if err := svc.CreateQuote(context.Background(), q); err != nil {
				t.Fatalf("CreateQuote: %v", err)
			}
			if q.TotalAmount != tc.currentF64 {
				t.Errorf("TotalAmount = %.17g, current float64 behaviour is %.17g", q.TotalAmount, tc.currentF64)
			}
			// The pinned value is genuinely not the exact decimal amount: this
			// guards the characterization itself from being written as a no-op.
			exact := float64(tc.exactCents) / 100.0
			if (q.TotalAmount == exact) != tc.exactlyEqual {
				t.Errorf("TotalAmount %.17g vs exact %.17g: equality = %v, want %v",
					q.TotalAmount, exact, q.TotalAmount == exact, tc.exactlyEqual)
			}
		})
	}
}

// --- pickup / freight --------------------------------------------------------

// CORRECTNESS: a PICKUP quote carries no vehicle and no freight.
func TestCreateQuote_PickupClearsVehicleAndFreight(t *testing.T) {
	vehicle := uuid.New()
	svc := NewService(&fakeRepo{})

	q := &Quote{
		CustomerID:    uuid.New(),
		Lines:         []QuoteLine{qline(1, 100)},
		DeliveryType:  "PICKUP",
		FreightAmount: 75,
		VehicleID:     &vehicle,
	}
	if err := svc.CreateQuote(context.Background(), q); err != nil {
		t.Fatalf("CreateQuote: %v", err)
	}
	if q.VehicleID != nil {
		t.Errorf("VehicleID = %v, want nil on a pickup", q.VehicleID)
	}
	if q.FreightAmount != 0 {
		t.Errorf("FreightAmount = %v, want 0 on a pickup", q.FreightAmount)
	}
}

// REGRESSION (this was a documented, pinned defect) — a pickup quote must not charge freight.
//
// CreateQuote used to add FreightAmount to TotalAmount and only afterwards let
// the PICKUP branch reset FreightAmount to 0. The freight was already baked
// into TotalAmount by then, so the stored quote showed freight_amount = 0
// while total_amount silently included it — the customer was billed for
// delivery on an order they were collecting themselves, and the total no
// longer reconciled with its own components.
//
// UpdateQuote had the same ordering, and was worse: it never applied the
// DeliveryType default, so only an explicit "PICKUP" cleared freight.
//
// Fixed: quote.normalizeDeliveryAndTotal defaults the delivery type, clears
// vehicle + freight for a pickup, and only then sums the total. Both
// CreateQuote and UpdateQuote go through it.
func TestCreateQuote_PickupExcludesFreightFromTotal(t *testing.T) {
	svc := NewService(&fakeRepo{})
	q := &Quote{
		CustomerID:    uuid.New(),
		Lines:         []QuoteLine{qline(1, 100)},
		DeliveryType:  "PICKUP",
		FreightAmount: 75,
	}
	if err := svc.CreateQuote(context.Background(), q); err != nil {
		t.Fatalf("CreateQuote: %v", err)
	}
	if q.TotalAmount != 100 {
		t.Errorf("TotalAmount = %v, want 100 — a pickup must not be charged freight", q.TotalAmount)
	}
	if q.TotalAmount != sumLines(q)+q.FreightAmount {
		t.Errorf("TotalAmount %v does not reconcile with lines %v + freight %v",
			q.TotalAmount, sumLines(q), q.FreightAmount)
	}
}

// CORRECTNESS: the defaulted delivery type behaves exactly like an explicit
// PICKUP — freight is dropped from the total, not just from the column.
//
// This test previously pinned the buggy total of 175 as a CHARACTERIZATION of
// freight-on-pickup. The fix that unskipped
// TestCreateQuote_PickupExcludesFreightFromTotal invalidates that pin, so the
// expectation moves to the correct 100 (lines only) for both delivery types.
func TestCreateQuote_PickupDropsFreightForExplicitAndDefaultedType(t *testing.T) {
	for _, deliveryType := range []string{"PICKUP", "" /* defaults to PICKUP */} {
		svc := NewService(&fakeRepo{})
		q := &Quote{
			CustomerID:    uuid.New(),
			Lines:         []QuoteLine{qline(1, 100)},
			DeliveryType:  deliveryType,
			FreightAmount: 75,
		}
		if err := svc.CreateQuote(context.Background(), q); err != nil {
			t.Fatalf("CreateQuote: %v", err)
		}
		if q.DeliveryType != "PICKUP" {
			t.Fatalf("DeliveryType = %q, want it to end up PICKUP", q.DeliveryType)
		}
		if q.TotalAmount != 100 {
			t.Errorf("deliveryType=%q: TotalAmount = %v, want 100 — a pickup carries no freight",
				deliveryType, q.TotalAmount)
		}
		if q.FreightAmount != 0 {
			t.Errorf("deliveryType=%q: FreightAmount = %v, want 0", deliveryType, q.FreightAmount)
		}
	}
}

// CORRECTNESS: UpdateQuote applies the same normalization — an edit that
// switches a delivered quote to pickup must take the freight back out of the
// total, and an edit that omits the delivery type gets the PICKUP default.
func TestUpdateQuote_PickupExcludesFreightFromTotal(t *testing.T) {
	for _, deliveryType := range []string{"PICKUP", "" /* defaults to PICKUP */} {
		id := uuid.New()
		repo := &fakeRepo{getQuote: &Quote{ID: id, State: QuoteStateDraft}}
		q := &Quote{
			ID:            id,
			Lines:         []QuoteLine{qline(1, 100)},
			DeliveryType:  deliveryType,
			FreightAmount: 75,
		}
		if err := NewService(repo).UpdateQuote(context.Background(), q); err != nil {
			t.Fatalf("UpdateQuote: %v", err)
		}
		if q.TotalAmount != 100 {
			t.Errorf("deliveryType=%q: TotalAmount = %v, want 100 — a pickup carries no freight",
				deliveryType, q.TotalAmount)
		}
		if q.FreightAmount != 0 {
			t.Errorf("deliveryType=%q: FreightAmount = %v, want 0", deliveryType, q.FreightAmount)
		}
		if q.VehicleID != nil {
			t.Errorf("deliveryType=%q: VehicleID = %v, want nil on a pickup", deliveryType, q.VehicleID)
		}
	}
}

func sumLines(q *Quote) float64 {
	var t float64
	for _, l := range q.Lines {
		t += l.LineTotal
	}
	return t
}

// --- defaults ----------------------------------------------------------------

// CORRECTNESS: a new quote starts as a manual DRAFT for pickup.
func TestCreateQuote_Defaults(t *testing.T) {
	svc := NewService(&fakeRepo{})
	q := &Quote{CustomerID: uuid.New(), Lines: []QuoteLine{qline(1, 10)}}
	if err := svc.CreateQuote(context.Background(), q); err != nil {
		t.Fatalf("CreateQuote: %v", err)
	}
	if q.State != QuoteStateDraft {
		t.Errorf("State = %q, want %q", q.State, QuoteStateDraft)
	}
	if q.Source != "manual" {
		t.Errorf("Source = %q, want manual", q.Source)
	}
	if q.DeliveryType != "PICKUP" {
		t.Errorf("DeliveryType = %q, want PICKUP", q.DeliveryType)
	}
}

// CORRECTNESS: explicit values are not overwritten by the defaults.
func TestCreateQuote_ExplicitValuesWin(t *testing.T) {
	svc := NewService(&fakeRepo{})
	q := &Quote{
		CustomerID:   uuid.New(),
		Lines:        []QuoteLine{qline(1, 10)},
		State:        QuoteStateSent,
		Source:       "ai",
		DeliveryType: "DELIVERY",
	}
	if err := svc.CreateQuote(context.Background(), q); err != nil {
		t.Fatalf("CreateQuote: %v", err)
	}
	if q.State != QuoteStateSent || q.Source != "ai" || q.DeliveryType != "DELIVERY" {
		t.Errorf("defaults overwrote explicit values: state=%q source=%q delivery=%q",
			q.State, q.Source, q.DeliveryType)
	}
}

// CORRECTNESS: a repository failure surfaces.
func TestCreateQuote_RepositoryErrorPropagates(t *testing.T) {
	svc := NewService(&fakeRepo{createErr: errors.New("insert failed")})
	err := svc.CreateQuote(context.Background(), &Quote{CustomerID: uuid.New(), Lines: []QuoteLine{qline(1, 10)}})
	if err == nil {
		t.Fatal("want the repository error to propagate")
	}
}

// --- editing -----------------------------------------------------------------

// CORRECTNESS: only DRAFT quotes may be edited — a sent or accepted quote is a
// commitment and must not be repriced underneath the customer.
func TestUpdateQuote_OnlyDraftsAreEditable(t *testing.T) {
	tests := []struct {
		state   QuoteState
		wantErr bool
	}{
		{QuoteStateDraft, false},
		{QuoteStateSent, true},
		{QuoteStateAccepted, true},
		{QuoteStateRejected, true},
		{QuoteStateExpired, true},
	}

	for _, tc := range tests {
		t.Run(string(tc.state), func(t *testing.T) {
			id := uuid.New()
			repo := &fakeRepo{getQuote: &Quote{ID: id, State: tc.state}}
			svc := NewService(repo)

			err := svc.UpdateQuote(context.Background(), &Quote{
				ID:           id,
				Lines:        []QuoteLine{qline(2, 50)},
				DeliveryType: "DELIVERY",
			})
			if tc.wantErr {
				if err == nil {
					t.Fatalf("want an error editing a %s quote", tc.state)
				}
				if repo.withLines != 0 {
					t.Error("nothing may be written for a rejected edit")
				}
				return
			}
			if err != nil {
				t.Fatalf("UpdateQuote: %v", err)
			}
			if repo.withLines != 1 {
				t.Errorf("UpdateQuoteWithLines called %d times, want 1", repo.withLines)
			}
		})
	}
}

// CORRECTNESS: an edit recomputes the total and forces the quote back to DRAFT.
func TestUpdateQuote_RecalculatesAndForcesDraft(t *testing.T) {
	id := uuid.New()
	repo := &fakeRepo{getQuote: &Quote{ID: id, State: QuoteStateDraft}}
	svc := NewService(repo)

	q := &Quote{
		ID:            id,
		State:         QuoteStateAccepted, // caller tries to smuggle in a state change
		Lines:         []QuoteLine{qline(2, 50), qline(1, 25)},
		FreightAmount: 10,
		DeliveryType:  "DELIVERY",
		TotalAmount:   999999, // stale total from the client
	}
	if err := svc.UpdateQuote(context.Background(), q); err != nil {
		t.Fatalf("UpdateQuote: %v", err)
	}
	if q.TotalAmount != 135 {
		t.Errorf("TotalAmount = %v, want 135 (100 + 25 + 10 freight)", q.TotalAmount)
	}
	if q.State != QuoteStateDraft {
		t.Errorf("State = %q, want the edit to force DRAFT", q.State)
	}
}

// CORRECTNESS: editing a quote that does not exist fails.
func TestUpdateQuote_MissingQuote(t *testing.T) {
	repo := &fakeRepo{getErr: errors.New("no rows")}
	err := NewService(repo).UpdateQuote(context.Background(), &Quote{ID: uuid.New()})
	if err == nil {
		t.Fatal("want an error when the quote does not exist")
	}
}

// --- state machine -----------------------------------------------------------

// CORRECTNESS: the allowed transitions. ACCEPTED is terminal — re-accepting or
// walking an accepted quote back would orphan the order/PO it produced.
func TestValidateStateTransition(t *testing.T) {
	tests := []struct {
		from, to QuoteState
		wantErr  bool
	}{
		{QuoteStateDraft, QuoteStateSent, false},
		{QuoteStateDraft, QuoteStateAccepted, false},
		{QuoteStateDraft, QuoteStateRejected, false},
		{QuoteStateDraft, QuoteStateExpired, false},
		{QuoteStateDraft, QuoteStateDraft, true},
		{QuoteStateSent, QuoteStateAccepted, false},
		{QuoteStateSent, QuoteStateRejected, false},
		{QuoteStateSent, QuoteStateExpired, false},
		{QuoteStateSent, QuoteStateDraft, true},
		{QuoteStateAccepted, QuoteStateSent, true},
		{QuoteStateAccepted, QuoteStateAccepted, true},
		{QuoteStateAccepted, QuoteStateRejected, true},
		{QuoteStateRejected, QuoteStateDraft, false},
		{QuoteStateRejected, QuoteStateAccepted, true},
		{QuoteStateExpired, QuoteStateDraft, false},
		{QuoteStateExpired, QuoteStateAccepted, true},
		{QuoteState("BOGUS"), QuoteStateDraft, true},
	}

	for _, tc := range tests {
		t.Run(string(tc.from)+"->"+string(tc.to), func(t *testing.T) {
			err := validateStateTransition(tc.from, tc.to)
			if tc.wantErr && err == nil {
				t.Errorf("validateStateTransition(%s, %s) = nil, want an error", tc.from, tc.to)
			}
			if !tc.wantErr && err != nil {
				t.Errorf("validateStateTransition(%s, %s) = %v, want nil", tc.from, tc.to, err)
			}
		})
	}
}

// CORRECTNESS: each lifecycle transition stamps its own timestamp and no other.
func TestUpdateState_StampsLifecycleTimestamps(t *testing.T) {
	tests := []struct {
		target                     QuoteState
		wantSent, wantAcc, wantRej bool
	}{
		{QuoteStateSent, true, false, false},
		{QuoteStateAccepted, false, true, false},
		{QuoteStateRejected, false, false, true},
		{QuoteStateExpired, false, false, false},
	}

	for _, tc := range tests {
		t.Run(string(tc.target), func(t *testing.T) {
			id := uuid.New()
			repo := &fakeRepo{getQuote: &Quote{ID: id, State: QuoteStateDraft}}
			svc := NewService(repo)

			if err := svc.UpdateState(context.Background(), id, tc.target); err != nil {
				t.Fatalf("UpdateState: %v", err)
			}
			if repo.stored == nil {
				t.Fatal("quote was not persisted")
			}
			if repo.stored.State != tc.target {
				t.Errorf("State = %q, want %q", repo.stored.State, tc.target)
			}
			if (repo.stored.SentAt != nil) != tc.wantSent {
				t.Errorf("SentAt set = %v, want %v", repo.stored.SentAt != nil, tc.wantSent)
			}
			if (repo.stored.AcceptedAt != nil) != tc.wantAcc {
				t.Errorf("AcceptedAt set = %v, want %v", repo.stored.AcceptedAt != nil, tc.wantAcc)
			}
			if (repo.stored.RejectedAt != nil) != tc.wantRej {
				t.Errorf("RejectedAt set = %v, want %v", repo.stored.RejectedAt != nil, tc.wantRej)
			}
		})
	}
}

// CORRECTNESS: an invalid transition is rejected before anything is written.
func TestUpdateState_RejectsInvalidTransition(t *testing.T) {
	id := uuid.New()
	repo := &fakeRepo{getQuote: &Quote{ID: id, State: QuoteStateAccepted}}
	svc := NewService(repo)

	if err := svc.UpdateState(context.Background(), id, QuoteStateSent); err == nil {
		t.Fatal("want an error moving an ACCEPTED quote back to SENT")
	}
	if repo.updateCall != 0 {
		t.Error("nothing may be written for a rejected transition")
	}
}

// --- auto-PO on accept -------------------------------------------------------

// CORRECTNESS: accepting a quote raises a PO for every costed (special-order)
// line and for no other line, passing the line's own quantity and unit cost.
func TestUpdateState_AutoPOOnlyForCostedLines(t *testing.T) {
	id := uuid.New()
	special := qline(3, 120)
	special.UnitCost = 88.25
	stock := qline(10, 5) // UnitCost stays 0 — a stocked item

	repo := &fakeRepo{getQuote: &Quote{
		ID:    id,
		State: QuoteStateDraft,
		Lines: []QuoteLine{stock, special},
	}}
	po := &fakePO{}
	svc := NewService(repo)
	svc.WithAutoPO(po)

	if err := svc.UpdateState(context.Background(), id, QuoteStateAccepted); err != nil {
		t.Fatalf("UpdateState: %v", err)
	}

	if len(po.calls) != 1 {
		t.Fatalf("raised %d POs, want 1 (only the costed line)", len(po.calls))
	}
	got := po.calls[0]
	if got.productID != special.ProductID {
		t.Errorf("PO raised for product %s, want %s", got.productID, special.ProductID)
	}
	if got.quantity != special.Quantity {
		t.Errorf("PO quantity = %v, want %v", got.quantity, special.Quantity)
	}
	if got.unitCost != special.UnitCost {
		t.Errorf("PO unit cost = %v, want %v", got.unitCost, special.UnitCost)
	}
	if got.lineID != special.ID {
		t.Errorf("PO linked to line %s, want %s", got.lineID, special.ID)
	}
}

// CORRECTNESS: auto-PO is fire-and-forget — a PO failure must not undo an
// accepted quote.
func TestUpdateState_AutoPOFailureDoesNotBlockAcceptance(t *testing.T) {
	id := uuid.New()
	l := qline(1, 100)
	l.UnitCost = 50

	repo := &fakeRepo{getQuote: &Quote{ID: id, State: QuoteStateSent, Lines: []QuoteLine{l}}}
	po := &fakePO{err: errors.New("vendor unavailable")}
	svc := NewService(repo)
	svc.WithAutoPO(po)

	if err := svc.UpdateState(context.Background(), id, QuoteStateAccepted); err != nil {
		t.Fatalf("UpdateState must succeed despite the PO failure, got %v", err)
	}
	if repo.stored == nil || repo.stored.State != QuoteStateAccepted {
		t.Error("quote must still be ACCEPTED after a failed auto-PO")
	}
}

// CORRECTNESS: no PO is raised on any state other than ACCEPTED.
func TestUpdateState_NoAutoPOUnlessAccepted(t *testing.T) {
	id := uuid.New()
	l := qline(1, 100)
	l.UnitCost = 50

	for _, target := range []QuoteState{QuoteStateSent, QuoteStateRejected, QuoteStateExpired} {
		repo := &fakeRepo{getQuote: &Quote{ID: id, State: QuoteStateDraft, Lines: []QuoteLine{l}}}
		po := &fakePO{}
		svc := NewService(repo)
		svc.WithAutoPO(po)

		if err := svc.UpdateState(context.Background(), id, target); err != nil {
			t.Fatalf("UpdateState(%s): %v", target, err)
		}
		if len(po.calls) != 0 {
			t.Errorf("transition to %s raised %d POs, want 0", target, len(po.calls))
		}
	}
}
