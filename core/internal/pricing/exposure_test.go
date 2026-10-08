// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package pricing

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"math"
	"sync"
	"testing"
	"time"

	"github.com/gablelbm/gable/internal/quote"
	"github.com/google/uuid"
)

// The exposure scanner decides, without a human in the loop, whether a lumber
// index move rewrites a customer's already-quoted price. Everything below is a
// CORRECTNESS test of that decision: which event type is emitted, whether the
// line price is mutated, and whether the same index refresh can be applied
// twice.
//
// These run against in-memory fakes — no Postgres.

func quietLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// --- fakes ---------------------------------------------------------------

// fakeExposureRepo is an in-memory ExposureRepository. It models only what the
// scanner and service actually exercise; unused reads return zero values.
type fakeExposureRepo struct {
	mu sync.Mutex

	history    []MarketIndexHistory
	events     []QuoteExposureEvent
	seenKeys   map[string]bool
	escalators map[uuid.UUID]*PriceEscalator // by escalator id
	byIndex    map[uuid.UUID][]EscalatorWithContext
	byQuote    map[uuid.UUID][]uuid.UUID // quote id -> escalator ids

	stateWrites []stateWrite
	deactivated []uuid.UUID
	resolveIdx  map[uuid.UUID]*uuid.UUID
}

type stateWrite struct {
	escalatorID uuid.UUID
	state       string
}

func newFakeExposureRepo() *fakeExposureRepo {
	return &fakeExposureRepo{
		seenKeys:   map[string]bool{},
		escalators: map[uuid.UUID]*PriceEscalator{},
		byIndex:    map[uuid.UUID][]EscalatorWithContext{},
		byQuote:    map[uuid.UUID][]uuid.UUID{},
		resolveIdx: map[uuid.UUID]*uuid.UUID{},
	}
}

func (f *fakeExposureRepo) InsertHistory(_ context.Context, h *MarketIndexHistory) error {
	if h.ID == uuid.Nil {
		h.ID = uuid.New()
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.history = append(f.history, *h)
	return nil
}

func (f *fakeExposureRepo) GetHistoryByID(_ context.Context, id uuid.UUID) (*MarketIndexHistory, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for i := range f.history {
		if f.history[i].ID == id {
			return &f.history[i], nil
		}
	}
	return nil, nil
}

func (f *fakeExposureRepo) ListHistory(_ context.Context, indexID uuid.UUID, from, to time.Time) ([]MarketIndexHistory, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []MarketIndexHistory
	for _, h := range f.history {
		if h.MarketIndexID == indexID && !h.RecordedAt.Before(from) && !h.RecordedAt.After(to) {
			out = append(out, h)
		}
	}
	return out, nil
}

// InsertEvent mirrors the real ON CONFLICT (idempotency_key) DO NOTHING.
func (f *fakeExposureRepo) InsertEvent(_ context.Context, ev *QuoteExposureEvent) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.seenKeys[ev.IdempotencyKey] {
		return false, nil
	}
	if ev.ID == uuid.Nil {
		ev.ID = uuid.New()
	}
	if ev.CreatedAt.IsZero() {
		ev.CreatedAt = time.Now()
	}
	f.seenKeys[ev.IdempotencyKey] = true
	f.events = append(f.events, *ev)
	return true, nil
}

func (f *fakeExposureRepo) EventExists(_ context.Context, key string) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.seenKeys[key], nil
}

func (f *fakeExposureRepo) GetEventsByQuote(_ context.Context, quoteID uuid.UUID) ([]QuoteExposureEvent, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []QuoteExposureEvent
	for _, e := range f.events {
		if e.QuoteID == quoteID {
			out = append(out, e)
		}
	}
	return out, nil
}

func (f *fakeExposureRepo) ListActiveEscalatorsForIndex(_ context.Context, indexID uuid.UUID) ([]EscalatorWithContext, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	src := f.byIndex[indexID]
	out := make([]EscalatorWithContext, len(src))
	copy(out, src)
	// Reflect any state transitions written since the fixture was built, so a
	// second scan sees the state the first one left behind.
	for i := range out {
		if cur, ok := f.escalators[out[i].Escalator.ID]; ok {
			out[i].Escalator.CurrentState = cur.CurrentState
		}
	}
	return out, nil
}

func (f *fakeExposureRepo) ListEscalatorsForQuote(_ context.Context, quoteID uuid.UUID) ([]PriceEscalator, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []PriceEscalator
	for _, id := range f.byQuote[quoteID] {
		if pe, ok := f.escalators[id]; ok && pe.IsActive {
			out = append(out, *pe)
		}
	}
	return out, nil
}

func (f *fakeExposureRepo) UpdateEscalatorState(_ context.Context, id uuid.UUID, state string, at time.Time) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.stateWrites = append(f.stateWrites, stateWrite{escalatorID: id, state: state})
	if pe, ok := f.escalators[id]; ok {
		pe.CurrentState = state
		pe.LastCheckedAt = &at
	}
	return nil
}

func (f *fakeExposureRepo) DeactivateEscalatorsForLine(_ context.Context, lineID uuid.UUID) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.deactivated = append(f.deactivated, lineID)
	for _, pe := range f.escalators {
		if pe.QuoteLineID != nil && *pe.QuoteLineID == lineID {
			pe.IsActive = false
		}
	}
	return nil
}

func (f *fakeExposureRepo) ListCategoryDefaults(context.Context) ([]ProductCategoryIndexDefault, error) {
	return nil, nil
}
func (f *fakeExposureRepo) UpsertCategoryDefault(context.Context, uuid.UUID, uuid.UUID) error {
	return nil
}
func (f *fakeExposureRepo) DeleteCategoryDefault(context.Context, uuid.UUID) error { return nil }

func (f *fakeExposureRepo) ResolveIndexForProduct(_ context.Context, productID uuid.UUID) (*uuid.UUID, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.resolveIdx[productID], nil
}

func (f *fakeExposureRepo) ListExposureForOwner(context.Context, ExposureFilter) ([]ExposureRow, error) {
	return nil, nil
}
func (f *fakeExposureRepo) PortfolioRollup(context.Context, *uuid.UUID) (*PortfolioSummary, error) {
	return &PortfolioSummary{}, nil
}

var _ ExposureRepository = (*fakeExposureRepo)(nil)

// fakeQuoteReader is an in-memory quote.QuoteLineReader.
type fakeQuoteReader struct {
	mu         sync.Mutex
	quotes     map[uuid.UUID]*quote.QuoteForSnapshot
	rollups    map[uuid.UUID]rollup
	priceWrite map[uuid.UUID]float64 // line id -> new unit price
	recomputes []uuid.UUID
}

type rollup struct {
	state   string
	dollars float64
}

func newFakeQuoteReader() *fakeQuoteReader {
	return &fakeQuoteReader{
		quotes:     map[uuid.UUID]*quote.QuoteForSnapshot{},
		rollups:    map[uuid.UUID]rollup{},
		priceWrite: map[uuid.UUID]float64{},
	}
}

func (f *fakeQuoteReader) GetQuoteWithLinesAndCustomer(_ context.Context, id uuid.UUID) (*quote.QuoteForSnapshot, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.quotes[id], nil
}

func (f *fakeQuoteReader) UpdateQuoteExposure(_ context.Context, id uuid.UUID, state string, dollars float64, _ time.Time) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.rollups[id] = rollup{state: state, dollars: dollars}
	return nil
}

func (f *fakeQuoteReader) UpdateLineUnitPrice(_ context.Context, lineID uuid.UUID, price float64) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.priceWrite[lineID] = price
	return nil
}

func (f *fakeQuoteReader) RecomputeQuoteTotal(_ context.Context, quoteID uuid.UUID) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.recomputes = append(f.recomputes, quoteID)
	return nil
}

var _ quote.QuoteLineReader = (*fakeQuoteReader)(nil)

// capturingBus records what the scanner/service published.
type capturingBus struct {
	mu       sync.Mutex
	subjects []string
	payloads []ExposureNotification
}

func (b *capturingBus) Publish(_ context.Context, subject string, payload json.RawMessage) error {
	var n ExposureNotification
	_ = json.Unmarshal(payload, &n)
	b.mu.Lock()
	defer b.mu.Unlock()
	b.subjects = append(b.subjects, subject)
	b.payloads = append(b.payloads, n)
	return nil
}

func (b *capturingBus) snapshot() ([]string, []ExposureNotification) {
	b.mu.Lock()
	defer b.mu.Unlock()
	s := append([]string(nil), b.subjects...)
	p := append([]ExposureNotification(nil), b.payloads...)
	return s, p
}

type recordingAudit struct {
	mu      sync.Mutex
	entries []AuditEntry
}

func (a *recordingAudit) LogEntry(_ context.Context, e AuditEntry) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.entries = append(a.entries, e)
}

func (a *recordingAudit) count() int {
	a.mu.Lock()
	defer a.mu.Unlock()
	return len(a.entries)
}

// --- fixture -------------------------------------------------------------

type scannerFixture struct {
	scanner  *ExposureScanner
	exposure *fakeExposureRepo
	escal    *MockEscalatorRepository
	quotes   *fakeQuoteReader
	bus      *capturingBus
	audit    *recordingAudit

	indexID     uuid.UUID
	quoteID     uuid.UUID
	lineID      uuid.UUID
	escalatorID uuid.UUID
}

type fixtureOpts struct {
	baseIndex     float64
	currentIndex  float64
	basePrice     float64
	quantity      float64
	thresholdPct  float64
	policy        EscalationPolicy
	agreementSign bool
	startingState ExposureState
}

func newScannerFixture(t *testing.T, o fixtureOpts) *scannerFixture {
	t.Helper()

	exposure := newFakeExposureRepo()
	escal := newMockEscalatorRepo()
	quotes := newFakeQuoteReader()
	bus := &capturingBus{}
	audit := &recordingAudit{}

	indexID := uuid.New()
	escal.indices[indexID] = MarketIndex{
		ID:           indexID,
		Name:         "Random Lengths SPF #2&Btr 2x4",
		IndexCode:    "RL_SPF_2X4",
		CurrentValue: o.currentIndex,
		IsActive:     true,
	}

	quoteID, lineID, escalatorID := uuid.New(), uuid.New(), uuid.New()
	policy := string(o.policy)
	threshold := o.thresholdPct
	base := o.baseIndex
	state := o.startingState
	if state == "" {
		state = ExposureStateOK
	}

	pe := PriceEscalator{
		ID:                     escalatorID,
		QuoteLineID:            &lineID,
		MarketIndexID:          &indexID,
		EscalationType:         EscalationIndexDelta,
		BasePrice:              o.basePrice,
		BaseIndexValue:         &base,
		CurrentState:           string(state),
		PolicyAtSnapshot:       &policy,
		ThresholdPctAtSnapshot: &threshold,
		IsActive:               true,
	}
	exposure.escalators[escalatorID] = &pe
	exposure.byQuote[quoteID] = []uuid.UUID{escalatorID}

	var signedAt *time.Time
	if o.agreementSign {
		ts := time.Now().AddDate(0, -3, 0)
		signedAt = &ts
	}
	exposure.byIndex[indexID] = []EscalatorWithContext{{
		Escalator:                 pe,
		QuoteID:                   quoteID,
		QuoteState:                "SENT",
		QuoteShortID:              quoteID.String()[:8],
		CustomerID:                uuid.New(),
		CustomerName:              "Acme Construction",
		LineQuantity:              o.quantity,
		LineUnitPrice:             o.basePrice,
		CustomerAgreementSignedAt: signedAt,
	}}

	scanner := NewExposureScanner(exposure, escal, quotes, audit, nil, quietLogger()).WithEventBus(bus)

	return &scannerFixture{
		scanner: scanner, exposure: exposure, escal: escal, quotes: quotes,
		bus: bus, audit: audit,
		indexID: indexID, quoteID: quoteID, lineID: lineID, escalatorID: escalatorID,
	}
}

func (f *scannerFixture) lastEvent(t *testing.T) QuoteExposureEvent {
	t.Helper()
	f.exposure.mu.Lock()
	defer f.exposure.mu.Unlock()
	if len(f.exposure.events) == 0 {
		t.Fatal("no exposure event was written")
	}
	return f.exposure.events[len(f.exposure.events)-1]
}

func (f *scannerFixture) eventCount() int {
	f.exposure.mu.Lock()
	defer f.exposure.mu.Unlock()
	return len(f.exposure.events)
}

// --- policy routing ------------------------------------------------------

// CORRECTNESS: the snapshotted policy, not the customer's current policy,
// decides what happens; and each policy maps to exactly one event type.
func TestScanner_PolicyRoutingAboveThreshold(t *testing.T) {
	tests := []struct {
		name        string
		policy      EscalationPolicy
		signed      bool
		wantEvent   EventType
		wantState   ExposureState
		wantRewrite bool
	}{
		{"flag for requote", PolicyFlagForRequote, false, EventFlagged, ExposureStateFlagged, false},
		{"require ack", PolicyRequireAck, false, EventAckRequired, ExposureStateAckRequired, false},
		{"auto escalate with signed agreement", PolicyAutoEscalate, true, EventEscalated, ExposureStateEscalated, true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			f := newScannerFixture(t, fixtureOpts{
				baseIndex: 400, currentIndex: 440, // +10%
				basePrice: 100, quantity: 10,
				thresholdPct: 5, policy: tc.policy, agreementSign: tc.signed,
			})

			if err := f.scanner.OnMarketIndexUpdated(context.Background(), f.indexID, uuid.New()); err != nil {
				t.Fatalf("OnMarketIndexUpdated: %v", err)
			}

			ev := f.lastEvent(t)
			if ev.EventType != tc.wantEvent {
				t.Errorf("event_type = %s, want %s", ev.EventType, tc.wantEvent)
			}
			if got := f.exposure.escalators[f.escalatorID].CurrentState; got != string(tc.wantState) {
				t.Errorf("escalator state = %s, want %s", got, tc.wantState)
			}

			_, rewritten := f.quotes.priceWrite[f.lineID]
			if rewritten != tc.wantRewrite {
				t.Errorf("line price rewritten = %v, want %v", rewritten, tc.wantRewrite)
			}
			if tc.wantRewrite {
				// 100 * (440/400) = 110
				if got := f.quotes.priceWrite[f.lineID]; math.Abs(got-110) > 1e-6 {
					t.Errorf("new unit price = %v, want 110", got)
				}
				if len(f.quotes.recomputes) != 1 {
					t.Errorf("quote total recomputed %d times, want 1 — a rewritten line price "+
						"that doesn't reach the header leaves the quote self-inconsistent", len(f.quotes.recomputes))
				}
			}
		})
	}
}

// CORRECTNESS: AUTO_ESCALATE without a signed agreement must NOT rewrite the
// price. Migration 081's CHECK prevents that state on customers, but a quote
// snapshotted before the constraint (or a customer whose agreement was later
// cleared) can still reach the scanner. Falling back to FLAGGED keeps the
// system from silently repricing a contract nobody signed.
func TestScanner_AutoEscalateWithoutAgreementFallsBackToFlagged(t *testing.T) {
	f := newScannerFixture(t, fixtureOpts{
		baseIndex: 400, currentIndex: 480, // +20%
		basePrice: 250, quantity: 40,
		thresholdPct: 5, policy: PolicyAutoEscalate, agreementSign: false,
	})

	if err := f.scanner.OnMarketIndexUpdated(context.Background(), f.indexID, uuid.New()); err != nil {
		t.Fatalf("OnMarketIndexUpdated: %v", err)
	}

	ev := f.lastEvent(t)
	if ev.EventType != EventFlagged {
		t.Errorf("event_type = %s, want FLAGGED", ev.EventType)
	}
	if _, rewritten := f.quotes.priceWrite[f.lineID]; rewritten {
		t.Error("line price was rewritten without a signed escalation agreement")
	}
	if len(f.quotes.recomputes) != 0 {
		t.Error("quote total was recomputed even though no price changed")
	}
}

// --- threshold behaviour -------------------------------------------------

// CORRECTNESS: a move inside the frozen threshold on a quote that was already
// OK writes no event at all — otherwise every index tick would spam the ledger
// and the salesperson's inbox.
func TestScanner_BelowThresholdOnHealthyQuoteWritesNoEvent(t *testing.T) {
	f := newScannerFixture(t, fixtureOpts{
		baseIndex: 400, currentIndex: 408, // +2%
		basePrice: 100, quantity: 10,
		thresholdPct: 5, policy: PolicyFlagForRequote,
	})

	if err := f.scanner.OnMarketIndexUpdated(context.Background(), f.indexID, uuid.New()); err != nil {
		t.Fatalf("OnMarketIndexUpdated: %v", err)
	}

	if n := f.eventCount(); n != 0 {
		t.Errorf("wrote %d events for a sub-threshold move, want 0", n)
	}
	if got := f.exposure.escalators[f.escalatorID].CurrentState; got != string(ExposureStateOK) {
		t.Errorf("escalator state = %s, want OK", got)
	}
	if subjects, _ := f.bus.snapshot(); len(subjects) != 0 {
		t.Errorf("published %v for a sub-threshold move, want nothing", subjects)
	}
}

// CORRECTNESS: when the index falls back inside the threshold, a previously
// raised line must emit CLEARED and return to OK — otherwise a quote stays
// blocked after the market has already recovered.
func TestScanner_RecoveryEmitsCleared(t *testing.T) {
	f := newScannerFixture(t, fixtureOpts{
		baseIndex: 400, currentIndex: 404, // +1%, inside threshold
		basePrice: 100, quantity: 10,
		thresholdPct: 5, policy: PolicyFlagForRequote,
		startingState: ExposureStateFlagged,
	})

	if err := f.scanner.OnMarketIndexUpdated(context.Background(), f.indexID, uuid.New()); err != nil {
		t.Fatalf("OnMarketIndexUpdated: %v", err)
	}

	ev := f.lastEvent(t)
	if ev.EventType != EventCleared {
		t.Fatalf("event_type = %s, want CLEARED", ev.EventType)
	}
	if got := f.exposure.escalators[f.escalatorID].CurrentState; got != string(ExposureStateOK) {
		t.Errorf("escalator state = %s, want OK", got)
	}
	subjects, _ := f.bus.snapshot()
	if len(subjects) != 1 || subjects[0] != "quote.exposure.cleared" {
		t.Errorf("published %v, want [quote.exposure.cleared]", subjects)
	}
}

// CORRECTNESS: exposure dollars are an unsigned magnitude. A falling index is
// still exposure (the dealer is now over market and will lose the job), and
// storing a signed value would let opposite-direction lines cancel out in the
// per-quote rollup and understate the total.
func TestScanner_ExposureDollarsAreUnsignedMagnitude(t *testing.T) {
	f := newScannerFixture(t, fixtureOpts{
		baseIndex: 400, currentIndex: 340, // -15%
		basePrice: 200, quantity: 5,
		thresholdPct: 5, policy: PolicyFlagForRequote,
	})

	if err := f.scanner.OnMarketIndexUpdated(context.Background(), f.indexID, uuid.New()); err != nil {
		t.Fatalf("OnMarketIndexUpdated: %v", err)
	}

	ev := f.lastEvent(t)
	if ev.ExposureDollars == nil {
		t.Fatal("exposure_dollars is nil")
	}
	// |340/400 - 1| * 200 * 5 = 0.15 * 1000 = 150
	if math.Abs(*ev.ExposureDollars-150) > 0.01 {
		t.Errorf("exposure_dollars = %v, want 150 (unsigned)", *ev.ExposureDollars)
	}
	if ev.DeltaPct == nil || *ev.DeltaPct >= 0 {
		t.Errorf("delta_pct = %v, want a negative value — direction belongs on delta, not dollars", ev.DeltaPct)
	}
}

// --- idempotency ---------------------------------------------------------

// CORRECTNESS: replaying the same market_index_history row must be a no-op.
// The refresh endpoint kicks the scanner synchronously and the nightly cron
// re-runs the same indices; without this, one index move would flag a quote
// twice and email the salesperson twice.
func TestScanner_ReplayingTheSameHistoryRowIsANoop(t *testing.T) {
	f := newScannerFixture(t, fixtureOpts{
		baseIndex: 400, currentIndex: 440,
		basePrice: 100, quantity: 10,
		thresholdPct: 5, policy: PolicyFlagForRequote,
	})
	historyID := uuid.New()

	for i := 0; i < 3; i++ {
		if err := f.scanner.OnMarketIndexUpdated(context.Background(), f.indexID, historyID); err != nil {
			t.Fatalf("scan %d: %v", i, err)
		}
	}

	if n := f.eventCount(); n != 1 {
		t.Errorf("wrote %d events across three identical scans, want 1", n)
	}
	if subjects, _ := f.bus.snapshot(); len(subjects) != 1 {
		t.Errorf("published %d notifications, want 1 — a deduped event must not re-notify", len(subjects))
	}
	if got := f.audit.count(); got != 1 {
		t.Errorf("wrote %d audit entries, want 1", got)
	}
}

// CORRECTNESS: the safety-net pass keys on the calendar day rather than a
// history id, so re-running it the same day does not duplicate events.
func TestScanner_SafetyNetIsIdempotentWithinTheDay(t *testing.T) {
	f := newScannerFixture(t, fixtureOpts{
		baseIndex: 400, currentIndex: 440,
		basePrice: 100, quantity: 10,
		thresholdPct: 5, policy: PolicyFlagForRequote,
	})

	for i := 0; i < 3; i++ {
		if err := f.scanner.RunSafetyNet(context.Background()); err != nil {
			t.Fatalf("RunSafetyNet %d: %v", i, err)
		}
	}
	if n := f.eventCount(); n != 1 {
		t.Errorf("wrote %d events across three same-day safety-net passes, want 1", n)
	}
}

// CORRECTNESS: a safety-net event carries no market_index_history_id, because
// no history row triggered it. Writing uuid.Nil there would be a dangling FK.
func TestScanner_SafetyNetEventHasNoHistoryReference(t *testing.T) {
	f := newScannerFixture(t, fixtureOpts{
		baseIndex: 400, currentIndex: 440,
		basePrice: 100, quantity: 10,
		thresholdPct: 5, policy: PolicyFlagForRequote,
	})
	if err := f.scanner.RunSafetyNet(context.Background()); err != nil {
		t.Fatalf("RunSafetyNet: %v", err)
	}
	if ev := f.lastEvent(t); ev.MarketIndexHistoryID != nil {
		t.Errorf("market_index_history_id = %v, want nil for a safety-net event", ev.MarketIndexHistoryID)
	}
}

// --- baselines and guards ------------------------------------------------

// CORRECTNESS: an escalator with no usable baseline cannot produce a delta.
// It must be skipped, not divided by zero into an infinite exposure.
func TestScanner_MissingBaselineIsSkipped(t *testing.T) {
	f := newScannerFixture(t, fixtureOpts{
		baseIndex: 0, currentIndex: 440,
		basePrice: 100, quantity: 10,
		thresholdPct: 5, policy: PolicyFlagForRequote,
	})

	if err := f.scanner.OnMarketIndexUpdated(context.Background(), f.indexID, uuid.New()); err != nil {
		t.Fatalf("OnMarketIndexUpdated: %v", err)
	}
	if n := f.eventCount(); n != 0 {
		t.Errorf("wrote %d events for a zero baseline, want 0", n)
	}
}

// CORRECTNESS: a zero/absent snapshotted threshold falls back to 5%, not to
// "flag everything". A 0% threshold would raise on every index tick.
func TestScanner_ZeroSnapshotThresholdFallsBackToFivePercent(t *testing.T) {
	f := newScannerFixture(t, fixtureOpts{
		baseIndex: 400, currentIndex: 412, // +3%: under the 5% fallback
		basePrice: 100, quantity: 10,
		thresholdPct: 0, policy: PolicyFlagForRequote,
	})
	if err := f.scanner.OnMarketIndexUpdated(context.Background(), f.indexID, uuid.New()); err != nil {
		t.Fatalf("OnMarketIndexUpdated: %v", err)
	}
	if n := f.eventCount(); n != 0 {
		t.Errorf("a +3%% move raised %d events under the 5%% fallback threshold, want 0", n)
	}

	f2 := newScannerFixture(t, fixtureOpts{
		baseIndex: 400, currentIndex: 432, // +8%: over the fallback
		basePrice: 100, quantity: 10,
		thresholdPct: 0, policy: PolicyFlagForRequote,
	})
	if err := f2.scanner.OnMarketIndexUpdated(context.Background(), f2.indexID, uuid.New()); err != nil {
		t.Fatalf("OnMarketIndexUpdated: %v", err)
	}
	if n := f2.eventCount(); n != 1 {
		t.Errorf("a +8%% move raised %d events under the 5%% fallback threshold, want 1", n)
	}
}

// CORRECTNESS: the published notification must carry the fields the notifier
// renders, on the subject mapped from the event type.
func TestScanner_PublishesNotificationOnMappedSubject(t *testing.T) {
	f := newScannerFixture(t, fixtureOpts{
		baseIndex: 400, currentIndex: 440,
		basePrice: 100, quantity: 10,
		thresholdPct: 5, policy: PolicyRequireAck,
	})
	if err := f.scanner.OnMarketIndexUpdated(context.Background(), f.indexID, uuid.New()); err != nil {
		t.Fatalf("OnMarketIndexUpdated: %v", err)
	}

	subjects, payloads := f.bus.snapshot()
	if len(subjects) != 1 || subjects[0] != "quote.exposure.ack_required" {
		t.Fatalf("subjects = %v, want [quote.exposure.ack_required]", subjects)
	}
	p := payloads[0]
	if p.EventType != EventAckRequired {
		t.Errorf("payload event_type = %s, want ACK_REQUIRED", p.EventType)
	}
	if p.QuoteID != f.quoteID {
		t.Errorf("payload quote_id = %s, want %s", p.QuoteID, f.quoteID)
	}
	if p.IndexCode != "RL_SPF_2X4" {
		t.Errorf("payload index_code = %q, want RL_SPF_2X4", p.IndexCode)
	}
	if math.Abs(p.DeltaPct-10) > 0.01 {
		t.Errorf("payload delta_pct = %v, want 10", p.DeltaPct)
	}
}

// CORRECTNESS: a nil bus must not break the scan. Notifications are a
// side-effect; the durable ledger is the product.
func TestScanner_NilBusDoesNotBreakScan(t *testing.T) {
	f := newScannerFixture(t, fixtureOpts{
		baseIndex: 400, currentIndex: 440,
		basePrice: 100, quantity: 10,
		thresholdPct: 5, policy: PolicyFlagForRequote,
	})
	f.scanner.bus = nil

	if err := f.scanner.OnMarketIndexUpdated(context.Background(), f.indexID, uuid.New()); err != nil {
		t.Fatalf("OnMarketIndexUpdated: %v", err)
	}
	if n := f.eventCount(); n != 1 {
		t.Errorf("wrote %d events with no bus wired, want 1", n)
	}
}

// --- rollup --------------------------------------------------------------

// CORRECTNESS: the denormalized quote rollup takes the worst line state.
// quotes.exposure_state is what the pre-ship gate reads, so an ACK_REQUIRED
// line must not be masked by an ESCALATED sibling.
func TestComputeRollup_WorstStateWins(t *testing.T) {
	tests := []struct {
		name   string
		states []ExposureState
		want   ExposureState
	}{
		{"all ok", []ExposureState{ExposureStateOK, ExposureStateOK}, ExposureStateOK},
		{"flagged beats escalated", []ExposureState{ExposureStateEscalated, ExposureStateFlagged}, ExposureStateFlagged},
		{"ack required beats flagged", []ExposureState{ExposureStateFlagged, ExposureStateAckRequired}, ExposureStateAckRequired},
		{"blocked beats everything", []ExposureState{ExposureStateAckRequired, ExposureStateBlocked}, ExposureStateBlocked},
		{"acknowledged is cleared", []ExposureState{ExposureStateAcknowledged, ExposureStateOK}, ExposureStateOK},
		{"overridden is cleared", []ExposureState{ExposureStateOverridden}, ExposureStateOK},
		{"no escalators", nil, ExposureStateOK},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			escalators := make([]PriceEscalator, 0, len(tc.states))
			for _, s := range tc.states {
				escalators = append(escalators, PriceEscalator{CurrentState: string(s)})
			}
			got, _ := computeRollup(escalators, nil)
			if got != tc.want {
				t.Errorf("computeRollup states=%v = %s, want %s", tc.states, got, tc.want)
			}
		})
	}
}

// CORRECTNESS: per-line dollars are summed as magnitudes so a rising and a
// falling index on the same quote do not cancel to zero exposure.
func TestComputeRollup_SumsMagnitudesNotSignedValues(t *testing.T) {
	_, total := computeRollup(nil, []float64{1200, -900})
	if math.Abs(total-2100) > 0.01 {
		t.Errorf("total = %v, want 2100 — mixed-direction line exposures must not net out", total)
	}
}

// REGRESSION (this was a documented, pinned defect): the per-quote dollar rollup double-counted a
// line whose escalator was superseded by a re-send.
//
// rollupQuote derives the *state* from ListEscalatorsForQuote, which filters on
// is_active — so the gate decision was always correct. But it derived the
// *dollars* from sumLatestExposurePerLine, which walked the whole
// quote_exposure_events ledger with no is_active filter.
//
// SnapshotService.SnapshotQuoteLines (the DRAFT→SENT re-send path) deactivates
// the prior escalators and writes fresh ones, but it does NOT write a CLEARED
// event for the superseded lines — it just resets the quote rollup to OK/0. So
// the old FLAGGED event, with its old exposure_dollars, is still the latest
// event for that line id. The next scan that touches any line on the quote
// calls rollupQuote, which resurrects that stale figure and adds it to the
// total.
//
// Impact: quotes.exposure_dollars, the at-risk list and the owner portfolio
// report overstate exposure after a re-quote. Blocking behaviour is unaffected.
//
// Fixed: rollupQuote now passes the active escalators' line ids into
// sumLatestExposurePerLine, which skips events for any other line — so the
// dollar path and the state path are derived from the same escalator set.
func TestScanner_RollupExcludesLinesWhoseEscalatorWasSuperseded(t *testing.T) {
	exposure := newFakeExposureRepo()
	quotes := newFakeQuoteReader()
	quoteID := uuid.New()

	supersededLine, liveLine := uuid.New(), uuid.New()
	supersededEsc, liveEsc := uuid.New(), uuid.New()

	// Line A: escalator deactivated by a re-send. No CLEARED event was written.
	exposure.escalators[supersededEsc] = &PriceEscalator{
		ID: supersededEsc, QuoteLineID: &supersededLine,
		CurrentState: string(ExposureStateFlagged), IsActive: false,
	}
	// Line B: the live escalator from the re-send.
	exposure.escalators[liveEsc] = &PriceEscalator{
		ID: liveEsc, QuoteLineID: &liveLine,
		CurrentState: string(ExposureStateFlagged), IsActive: true,
	}
	exposure.byQuote[quoteID] = []uuid.UUID{supersededEsc, liveEsc}

	stale, live := 500.0, 120.0
	exposure.events = []QuoteExposureEvent{
		{QuoteID: quoteID, QuoteLineID: &supersededLine, EventType: EventFlagged, ExposureDollars: &stale},
		{QuoteID: quoteID, QuoteLineID: &liveLine, EventType: EventFlagged, ExposureDollars: &live},
	}

	scanner := NewExposureScanner(exposure, newMockEscalatorRepo(), quotes, &recordingAudit{}, nil, quietLogger())
	if err := scanner.rollupQuote(context.Background(), quoteID, time.Now()); err != nil {
		t.Fatalf("rollupQuote: %v", err)
	}

	got := quotes.rollups[quoteID]
	if math.Abs(got.dollars-live) > 0.01 {
		t.Errorf("rollup dollars = %v, want %v — only lines with an active escalator count", got.dollars, live)
	}
}

// --- acknowledge / override ---------------------------------------------

// fakeChecker is an ExposureChecker returning a canned status.
type fakeChecker struct {
	status  ExposureStatus
	quoteID *uuid.UUID
}

func (c *fakeChecker) CheckQuoteExposure(_ context.Context, id uuid.UUID) (ExposureStatus, error) {
	s := c.status
	s.QuoteID = id
	return s, nil
}

func (c *fakeChecker) RequireClearForOrder(context.Context, uuid.UUID) error { return nil }

func (c *fakeChecker) QuoteIDForOrder(context.Context, uuid.UUID) (*uuid.UUID, error) {
	return c.quoteID, nil
}

var _ ExposureChecker = (*fakeChecker)(nil)

func newExposureServiceFixture(t *testing.T, state ExposureState) (*ExposureService, *fakeExposureRepo, *fakeQuoteReader, *capturingBus, uuid.UUID, uuid.UUID) {
	t.Helper()
	exposure := newFakeExposureRepo()
	quotes := newFakeQuoteReader()
	bus := &capturingBus{}
	quoteID := uuid.New()
	escalatorID := uuid.New()
	lineID := uuid.New()

	exposure.escalators[escalatorID] = &PriceEscalator{
		ID:           escalatorID,
		QuoteLineID:  &lineID,
		CurrentState: string(state),
		IsActive:     true,
	}
	exposure.byQuote[quoteID] = []uuid.UUID{escalatorID}

	checker := &fakeChecker{status: ExposureStatus{State: state, ExposureDollars: 1234.56}}
	svc := NewExposureService(exposure, newMockEscalatorRepo(), quotes, &recordingAudit{}, checker, quietLogger()).
		WithEventBus(bus)
	return svc, exposure, quotes, bus, quoteID, escalatorID
}

// CORRECTNESS: an acknowledgment is a contractual record. A one-word note or
// an unrecognised method must be refused before anything is persisted.
func TestExposureService_AcknowledgeValidation(t *testing.T) {
	tests := []struct {
		name    string
		req     AcknowledgmentRequest
		wantErr error
	}{
		{"short notes", AcknowledgmentRequest{Method: AckMethodVerbal, Notes: "ok"}, errNotesTooShort},
		{"whitespace notes", AcknowledgmentRequest{Method: AckMethodVerbal, Notes: "          "}, errNotesTooShort},
		{"bad method", AcknowledgmentRequest{Method: "SMOKE_SIGNAL", Notes: "customer confirmed by phone"}, errInvalidAckMethod},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			svc, exposure, _, _, quoteID, _ := newExposureServiceFixture(t, ExposureStateAckRequired)
			_, err := svc.Acknowledge(context.Background(), quoteID, tc.req, "user-1", "sales")
			if err != tc.wantErr {
				t.Fatalf("err = %v, want %v", err, tc.wantErr)
			}
			if len(exposure.events) != 0 {
				t.Error("an event was written despite failed validation")
			}
		})
	}
}

// CORRECTNESS: acknowledging a quote that is already clear is a 409, not a
// silent second acknowledgment that muddies the ledger.
func TestExposureService_AcknowledgeRejectsAlreadyClearedStates(t *testing.T) {
	for _, state := range []ExposureState{ExposureStateOK, ExposureStateAcknowledged, ExposureStateOverridden} {
		t.Run(string(state), func(t *testing.T) {
			svc, _, _, _, quoteID, _ := newExposureServiceFixture(t, state)
			_, err := svc.Acknowledge(context.Background(), quoteID,
				AcknowledgmentRequest{Method: AckMethodEmail, Notes: "customer confirmed by email"}, "u", "sales")
			if err != errAlreadyCleared {
				t.Errorf("err = %v, want errAlreadyCleared", err)
			}
		})
	}
}

// CORRECTNESS: a valid acknowledgment writes the ledger row, zeroes the quote
// rollup, flips the line escalators, and notifies.
func TestExposureService_AcknowledgeClearsTheQuote(t *testing.T) {
	svc, exposure, quotes, bus, quoteID, escalatorID := newExposureServiceFixture(t, ExposureStateAckRequired)

	ev, err := svc.Acknowledge(context.Background(), quoteID,
		AcknowledgmentRequest{Method: AckMethodVerbal, CustomerContact: "Dana", Notes: "confirmed on the phone"},
		"user-7", "sales")
	if err != nil {
		t.Fatalf("Acknowledge: %v", err)
	}
	if ev.EventType != EventAcknowledged {
		t.Errorf("event_type = %s, want ACKNOWLEDGED", ev.EventType)
	}
	if ev.ExposureDollars == nil || *ev.ExposureDollars != 1234.56 {
		t.Errorf("event exposure_dollars = %v, want the 1234.56 that was acknowledged", ev.ExposureDollars)
	}
	// The customer contact must survive into the ledger notes.
	if want := "contact=Dana; confirmed on the phone"; ev.Notes != want {
		t.Errorf("notes = %q, want %q", ev.Notes, want)
	}
	if got := quotes.rollups[quoteID]; got.state != string(ExposureStateAcknowledged) || got.dollars != 0 {
		t.Errorf("quote rollup = %+v, want ACKNOWLEDGED / 0", got)
	}
	if got := exposure.escalators[escalatorID].CurrentState; got != string(ExposureStateAcknowledged) {
		t.Errorf("escalator state = %s, want ACKNOWLEDGED", got)
	}
	if subjects, _ := bus.snapshot(); len(subjects) != 1 || subjects[0] != "quote.exposure.acknowledged" {
		t.Errorf("subjects = %v, want [quote.exposure.acknowledged]", subjects)
	}
}

// CORRECTNESS: an owner override must leave already-OK escalators alone.
// Flipping them to OVERRIDDEN would make the scanner blind to a future move on
// those lines, because OVERRIDDEN is a terminal state for the rollup.
func TestExposureService_OverrideLeavesHealthyEscalatorsScannable(t *testing.T) {
	exposure := newFakeExposureRepo()
	quotes := newFakeQuoteReader()
	quoteID := uuid.New()
	raised, healthy := uuid.New(), uuid.New()
	raisedLine, healthyLine := uuid.New(), uuid.New()

	exposure.escalators[raised] = &PriceEscalator{ID: raised, QuoteLineID: &raisedLine, CurrentState: string(ExposureStateAckRequired), IsActive: true}
	exposure.escalators[healthy] = &PriceEscalator{ID: healthy, QuoteLineID: &healthyLine, CurrentState: string(ExposureStateOK), IsActive: true}
	exposure.byQuote[quoteID] = []uuid.UUID{raised, healthy}

	checker := &fakeChecker{status: ExposureStatus{State: ExposureStateAckRequired, ExposureDollars: 500}}
	svc := NewExposureService(exposure, newMockEscalatorRepo(), quotes, &recordingAudit{}, checker, quietLogger())

	if _, err := svc.Override(context.Background(), quoteID,
		OverrideRequest{Notes: "customer verbally accepted at the counter"}, "owner-1", "owner"); err != nil {
		t.Fatalf("Override: %v", err)
	}

	if got := exposure.escalators[raised].CurrentState; got != string(ExposureStateOverridden) {
		t.Errorf("raised escalator = %s, want OVERRIDDEN", got)
	}
	if got := exposure.escalators[healthy].CurrentState; got != string(ExposureStateOK) {
		t.Errorf("healthy escalator = %s, want OK — overriding it would hide future index moves on that line", got)
	}
}

// CORRECTNESS: an override without a real justification is refused. This is
// the one path that lets a shipment leave the yard against the customer's
// stated policy, so the audit trail has to say why.
func TestExposureService_OverrideRequiresJustification(t *testing.T) {
	svc, exposure, _, _, quoteID, _ := newExposureServiceFixture(t, ExposureStateBlocked)
	if _, err := svc.Override(context.Background(), quoteID, OverrideRequest{Notes: "ok"}, "o", "owner"); err != errNotesTooShort {
		t.Fatalf("err = %v, want errNotesTooShort", err)
	}
	if len(exposure.events) != 0 {
		t.Error("an override event was written without a justification")
	}
}

// CORRECTNESS: OverrideForOrder resolves the order's source quote. An order
// with no source quote (walk-in counter sale) has nothing to override and must
// succeed silently rather than erroring the caller.
func TestExposureService_OverrideForOrderWithNoSourceQuote(t *testing.T) {
	exposure := newFakeExposureRepo()
	checker := &fakeChecker{quoteID: nil}
	svc := NewExposureService(exposure, newMockEscalatorRepo(), newFakeQuoteReader(), &recordingAudit{}, checker, quietLogger())

	ev, err := svc.OverrideForOrder(context.Background(), uuid.New(), "counter sale, no source quote", "o", "owner")
	if err != nil {
		t.Fatalf("OverrideForOrder: %v", err)
	}
	if ev != nil {
		t.Errorf("event = %+v, want nil for an order with no source quote", ev)
	}
}

// CORRECTNESS: an already-cleared quote is not an error for the order gate —
// the order should just proceed.
func TestExposureService_OverrideForOrderOnClearedQuoteIsNotAnError(t *testing.T) {
	quoteID := uuid.New()
	checker := &fakeChecker{status: ExposureStatus{State: ExposureStateOK}, quoteID: &quoteID}
	svc := NewExposureService(newFakeExposureRepo(), newMockEscalatorRepo(), newFakeQuoteReader(), &recordingAudit{}, checker, quietLogger())

	ev, err := svc.OverrideForOrder(context.Background(), uuid.New(), "already acknowledged earlier today", "o", "owner")
	if err != nil {
		t.Fatalf("OverrideForOrder on a cleared quote returned %v, want nil", err)
	}
	if ev != nil {
		t.Errorf("event = %+v, want nil", ev)
	}
}

// --- idempotency key ------------------------------------------------------

// CORRECTNESS: a double-clicked Acknowledge button within the same 5-second
// bucket collapses to one ledger row; two deliberate actions further apart do
// not.
func TestUserEventKey_BucketsWithinFiveSeconds(t *testing.T) {
	q := uuid.New()
	base := time.Date(2026, 3, 4, 10, 0, 0, 0, time.UTC)

	same := userEventKey(q, EventAcknowledged, "u1", base)
	if got := userEventKey(q, EventAcknowledged, "u1", base.Add(1*time.Second)); got != same {
		t.Error("a repeat within the same 5s bucket produced a different key; a double-click would double-write")
	}
	if got := userEventKey(q, EventAcknowledged, "u1", base.Add(30*time.Second)); got == same {
		t.Error("two acknowledgments 30s apart collapsed to one key; a genuine second action would be lost")
	}
	if got := userEventKey(q, EventOverridden, "u1", base); got == same {
		t.Error("different event types produced the same key")
	}
	if got := userEventKey(q, EventAcknowledged, "u2", base); got == same {
		t.Error("different actors produced the same key")
	}
	if got := userEventKey(uuid.New(), EventAcknowledged, "u1", base); got == same {
		t.Error("different quotes produced the same key")
	}
}

// --- subject / action mapping --------------------------------------------

// CORRECTNESS: only notify-worthy event types map to a subject. DETECTED and
// BLOCKED are ledger-only; publishing them would email the salesperson about
// an internal bookkeeping transition.
func TestSubjectForEvent(t *testing.T) {
	cases := map[EventType]string{
		EventFlagged:      "quote.exposure.flagged",
		EventEscalated:    "quote.exposure.escalated",
		EventAckRequired:  "quote.exposure.ack_required",
		EventAcknowledged: "quote.exposure.acknowledged",
		EventCleared:      "quote.exposure.cleared",
		EventDetected:     "",
		EventBlocked:      "",
		EventOverridden:   "",
		EventAckRequested: "",
	}
	for et, want := range cases {
		if got := SubjectForEvent(et); got != want {
			t.Errorf("SubjectForEvent(%s) = %q, want %q", et, got, want)
		}
	}
}

// CORRECTNESS: the state a quote is in determines what the UI offers. A
// BLOCKED quote must offer override; a FLAGGED one must not (it is advisory).
func TestAvailableActionsForState(t *testing.T) {
	cases := map[string][]string{
		"FLAGGED":      {"requote", "acknowledge", "notify_customer"},
		"ACK_REQUIRED": {"acknowledge", "request_ack"},
		"BLOCKED":      {"acknowledge", "override"},
		"ESCALATED":    {"view_audit"},
		"OK":           {"open"},
	}
	for state, want := range cases {
		got := availableActionsForState(state)
		if len(got) != len(want) {
			t.Errorf("availableActionsForState(%s) = %v, want %v", state, got, want)
			continue
		}
		for i := range want {
			if got[i] != want[i] {
				t.Errorf("availableActionsForState(%s) = %v, want %v", state, got, want)
				break
			}
		}
	}
}

// CORRECTNESS: only ACK_REQUIRED and BLOCKED actually stop a shipment.
// FLAGGED is advisory — treating it as a blocker would halt the yard on every
// market wobble.
func TestRequiredActionForState(t *testing.T) {
	cases := map[ExposureState]string{
		ExposureStateAckRequired:  "ACKNOWLEDGE",
		ExposureStateBlocked:      "ACKNOWLEDGE_OR_OVERRIDE",
		ExposureStateFlagged:      "REQUOTE_OR_ACKNOWLEDGE",
		ExposureStateOK:           "",
		ExposureStateEscalated:    "",
		ExposureStateAcknowledged: "",
		ExposureStateOverridden:   "",
	}
	for state, want := range cases {
		if got := requiredActionForState(state); got != want {
			t.Errorf("requiredActionForState(%s) = %q, want %q", state, got, want)
		}
	}
}

// --- unresolved-exposure error contract ----------------------------------

// CORRECTNESS: order and delivery detect the gate error structurally, via the
// UnresolvedExposurePayload method, so they never import pricing. If that
// method is renamed, the 409 body silently degrades to a 500.
func TestErrUnresolvedExposure_PayloadContract(t *testing.T) {
	quoteID := uuid.New()
	err := &ErrUnresolvedExposure{Status: ExposureStatus{
		QuoteID:         quoteID,
		QuoteShortID:    quoteID.String()[:8],
		State:           ExposureStateAckRequired,
		ExposureDollars: 4210.5,
		Indexes:         []string{"RL_SPF_2X4"},
		RequiredAction:  "ACKNOWLEDGE",
	}}

	var duck interface{ UnresolvedExposurePayload() map[string]any }
	if !asDuck(err, &duck) {
		t.Fatal("ErrUnresolvedExposure no longer satisfies the duck-typed interface order/delivery rely on")
	}
	payload := duck.UnresolvedExposurePayload()
	if payload["code"] != "UNRESOLVED_EXPOSURE" {
		t.Errorf("code = %v, want UNRESOLVED_EXPOSURE", payload["code"])
	}
	exposure, ok := payload["exposure"].(map[string]any)
	if !ok {
		t.Fatalf("exposure = %T, want map[string]any", payload["exposure"])
	}
	if exposure["quote_id"] != quoteID {
		t.Errorf("exposure.quote_id = %v, want %v", exposure["quote_id"], quoteID)
	}
	if exposure["exposure_dollars"] != 4210.5 {
		t.Errorf("exposure.exposure_dollars = %v, want 4210.5", exposure["exposure_dollars"])
	}
	// The body must be JSON-serialisable — order writes it straight to the wire.
	if _, err := json.Marshal(payload); err != nil {
		t.Fatalf("payload is not JSON-serialisable: %v", err)
	}
}

// asDuck is a tiny errors.As stand-in for a non-error interface target.
func asDuck(err error, target *interface{ UnresolvedExposurePayload() map[string]any }) bool {
	if v, ok := err.(interface{ UnresolvedExposurePayload() map[string]any }); ok {
		*target = v
		return true
	}
	return false
}
