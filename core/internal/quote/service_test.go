// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package quote

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"testing"
	"time"

	"github.com/gablelbm/gable/internal/order"
	"github.com/gablelbm/gable/internal/platform/httpx"
	"github.com/gablelbm/gable/internal/product"
	"github.com/gablelbm/gable/pkg/outbox"
	"github.com/google/uuid"
)

// The service against an in-memory repository: pricing, defaults, the
// lifecycle, the revision rules and the order of the event write. The wire
// facts (status codes, envelopes) are wire_test.go's; the atomicity and
// concurrency proofs against Postgres are tx_test.go's.

type fakeRepo struct {
	quotes   map[uuid.UUID]*Quote
	products map[uuid.UUID]ProductRef
	seq      int
	log      *[]string // shared call log with the recorder, to assert ordering
	insErr   error
}

func newFakeRepo() *fakeRepo {
	log := []string{}
	return &fakeRepo{quotes: map[uuid.UUID]*Quote{}, products: map[uuid.UUID]ProductRef{}, log: &log}
}

func (f *fakeRepo) note(s string) { *f.log = append(*f.log, s) }

func (f *fakeRepo) NextNumber(context.Context) (string, error) {
	f.seq++
	return fmt.Sprintf("Q-%06d", f.seq), nil
}

func (f *fakeRepo) LookupProducts(_ context.Context, ids []uuid.UUID) (map[uuid.UUID]ProductRef, error) {
	out := map[uuid.UUID]ProductRef{}
	for _, id := range ids {
		if p, ok := f.products[id]; ok {
			out[id] = p
		}
	}
	return out, nil
}

func (f *fakeRepo) InsertQuote(_ context.Context, q *Quote) error {
	f.note("insert")
	if f.insErr != nil {
		return f.insErr
	}
	cp := *q
	f.quotes[q.ID] = &cp
	return nil
}

func (f *fakeRepo) GetQuote(_ context.Context, id uuid.UUID) (*Quote, error) {
	q, ok := f.quotes[id]
	if !ok {
		return nil, ErrNotFound
	}
	cp := *q
	return &cp, nil
}

func (f *fakeRepo) LockQuote(_ context.Context, id uuid.UUID) error {
	if _, ok := f.quotes[id]; !ok {
		return ErrNotFound
	}
	f.note("lock")
	return nil
}

func (f *fakeRepo) ReplaceDraft(_ context.Context, q *Quote) error {
	f.note("replace")
	cur := f.quotes[q.ID]
	cp := *q
	cp.Revision = cur.Revision + 1
	cp.Status = cur.Status
	f.quotes[q.ID] = &cp
	return nil
}

func (f *fakeRepo) SetStatus(_ context.Context, q *Quote) error {
	f.note("set-status")
	cur := f.quotes[q.ID]
	cp := *cur
	cp.Status, cp.SentAt, cp.AcceptedAt, cp.RejectedAt = q.Status, q.SentAt, q.AcceptedAt, q.RejectedAt
	cp.Revision = cur.Revision + 1
	f.quotes[q.ID] = &cp
	return nil
}

func (f *fakeRepo) ListQuotes(context.Context, ListFilter) ([]QuoteSummary, error) { return nil, nil }
func (f *fakeRepo) CountQuotes(context.Context, ListFilter) (int64, error)         { return 0, nil }
func (f *fakeRepo) ListQuotesByCustomer(context.Context, uuid.UUID) ([]QuoteSummary, error) {
	return nil, nil
}
func (f *fakeRepo) GetQuoteAnalytics(context.Context) (*QuoteAnalytics, error) { return nil, nil }
func (f *fakeRepo) GetOriginalFile(context.Context, uuid.UUID) ([]byte, string, string, error) {
	return nil, "", "", ErrNotFound
}

// seed stores a quote directly, bypassing Create.
func (f *fakeRepo) seed(status QuoteState, lines ...QuoteLine) *Quote {
	q := &Quote{Lines: lines}
	q.ID, q.Status, q.Revision, q.Number = uuid.New(), status, 1, fmt.Sprintf("Q-%06d", len(f.quotes)+900)
	q.CustomerID, q.BranchID = uuid.New(), uuid.New()
	f.quotes[q.ID] = q
	return q
}

// recorder is the fake outbox: it records events and the repo calls around them.
type recorder struct {
	log    *[]string
	events []outbox.Event
	err    error
}

func (r *recorder) Write(_ context.Context, ev outbox.Event) error {
	*r.log = append(*r.log, "event:"+ev.Type)
	if r.err != nil {
		return r.err
	}
	r.events = append(r.events, ev)
	return nil
}

func newTestService() (*Service, *fakeRepo, *recorder) {
	repo := newFakeRepo()
	rec := &recorder{log: repo.log}
	return NewService(repo).WithOutbox(rec), repo, rec
}

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

// dl builds a draft line of PCS at the given quantity and unit price.
func dl(qty, price int64) DraftLine {
	return DraftLine{
		SKU: "SKU", Description: "line", Quantity: httpx.Quantity(qty * 10000), UOM: product.UOM_PCS, PriceUOM: "PCS",
		UOMQty: one, PriceUOMQty: one, UnitPrice: httpx.Price(price),
	}
}

func draft(lines ...DraftLine) *Draft {
	return &Draft{CustomerID: uuid.New(), DeliveryType: DeliveryPickup, Source: "manual", Lines: lines}
}

func asHTTPError(t *testing.T, err error, status int, code string) *httpx.Error {
	t.Helper()
	var e *httpx.Error
	if !errors.As(err, &e) {
		t.Fatalf("error = %v, want a *httpx.Error", err)
	}
	if e.Status != status || e.Code != code {
		t.Fatalf("error = %d %s (%s), want %d %s", e.Status, e.Code, e.Message, status, code)
	}
	return e
}

// RULE (ADR 0001 section 7a): the total is the sum of the lines' extensions,
// each rounded once to cents, plus freight; nothing passes through a float.
func TestCreate_TotalAssembly(t *testing.T) {
	cases := []struct {
		name      string
		lines     []DraftLine
		freight   httpx.Cents
		delivery  DeliveryType
		wantTotal httpx.Cents
		wantLines []httpx.Cents
	}{
		{"one line", []DraftLine{dl(10, 55000)}, 0, DeliveryPickup, 5500, []httpx.Cents{5500}},
		{"two lines plus freight on delivery", []DraftLine{dl(2, 125000), dl(3, 5000)}, 4500, DeliveryDelivery, 2500 + 150 + 4500, []httpx.Cents{2500, 150}},
		{"freight is cleared on a pickup", []DraftLine{dl(1, 100000)}, 9900, DeliveryPickup, 1000, []httpx.Cents{1000}},
		{"no lines", nil, 0, DeliveryPickup, 0, nil},
		// 3 at 0.3333 is 0.9999, which rounds to 100 cents... per line, once.
		{"sub cent prices round once per line", []DraftLine{dl(3, 3333), dl(3, 3333)}, 0, DeliveryPickup, 200, []httpx.Cents{100, 100}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			svc, _, _ := newTestService()
			d := draft(c.lines...)
			d.DeliveryType, d.FreightCents = c.delivery, c.freight
			q, err := svc.Create(context.Background(), d)
			if err != nil {
				t.Fatal(err)
			}
			if q.TotalCents != c.wantTotal {
				t.Errorf("total = %d, want %d", q.TotalCents, c.wantTotal)
			}
			for i, want := range c.wantLines {
				if q.Lines[i].LineTotal != want {
					t.Errorf("line %d total = %d, want %d", i, q.Lines[i].LineTotal, want)
				}
			}
		})
	}
}

func TestCreate_ConversionPairExtendsExactly(t *testing.T) {
	svc, _, _ := newTestService()
	l := dl(0, 5000000)
	l.Quantity, l.PriceUOM, l.UOMQty, l.PriceUOMQty = 1875000, "MBF", 1875000, one // 187.5 PCS at 500.00 per MBF
	q, err := svc.Create(context.Background(), draft(l))
	if err != nil {
		t.Fatal(err)
	}
	if q.TotalCents != 50000 {
		t.Errorf("187.5 PCS at 500.00/MBF = %d cents, want 50000", q.TotalCents)
	}
}

func TestCreate_PickupClearsVehicleAndFreight(t *testing.T) {
	svc, _, _ := newTestService()
	veh := uuid.New()
	d := draft(dl(1, 10000))
	d.FreightCents, d.VehicleID = 5000, &veh
	q, err := svc.Create(context.Background(), d)
	if err != nil {
		t.Fatal(err)
	}
	if q.VehicleID != nil || q.FreightCents != 0 {
		t.Errorf("pickup kept vehicle=%v freight=%d", q.VehicleID, q.FreightCents)
	}
}

func TestCreate_DefaultsAndIdentity(t *testing.T) {
	svc, repo, _ := newTestService()
	p := ProductRef{ID: uuid.New(), SKU: "LUM-248", Description: "2x4x8", UOMPrimary: "PCS"}
	repo.products[p.ID] = p

	l := dl(1, 10000)
	l.SKU, l.Description, l.ProductID = "", "", &p.ID
	q, err := svc.Create(context.Background(), draft(l))
	if err != nil {
		t.Fatal(err)
	}
	if q.Status != QuoteStateDraft || q.Revision != 1 || q.Number != "Q-000001" {
		t.Errorf("status/revision/number = %s/%d/%s", q.Status, q.Revision, q.Number)
	}
	if q.Lines[0].SKU != "LUM-248" || q.Lines[0].Description != "2x4x8" {
		t.Errorf("line defaults = %q %q, want the product's", q.Lines[0].SKU, q.Lines[0].Description)
	}
	if q.Lines[0].QuoteID != q.ID || q.Lines[0].ID == uuid.Nil {
		t.Errorf("line identity not set: %+v", q.Lines[0])
	}
}

func TestCreate_UnknownProductIsAFieldError(t *testing.T) {
	svc, repo, rec := newTestService()
	missing := uuid.New()
	l := dl(1, 10000)
	l.ProductID = &missing
	_, err := svc.Create(context.Background(), draft(dl(1, 100), l))
	e := asHTTPError(t, err, http.StatusBadRequest, httpx.CodeValidationFailed)
	if len(e.Details) != 1 || e.Details[0].Field != "lines[1].product_id" {
		t.Errorf("details = %+v, want one naming lines[1].product_id", e.Details)
	}
	if len(repo.quotes) != 0 || len(rec.events) != 0 {
		t.Errorf("a refused create stored %d quotes and %d events", len(repo.quotes), len(rec.events))
	}
}

// RULE (ADR 0003 section 2): the event is the LAST statement of the mutation.
func TestCreate_EventIsTheLastStatement(t *testing.T) {
	svc, repo, rec := newTestService()
	if _, err := svc.Create(context.Background(), draft(dl(1, 10000))); err != nil {
		t.Fatal(err)
	}
	log := *repo.log
	if len(log) < 2 || log[len(log)-1] != "event:quote.created" {
		t.Fatalf("call order = %v, want the event last", log)
	}
	ev := rec.events[0]
	if ev.EntityType != "quote" || ev.BranchID == nil {
		t.Errorf("event = %+v", ev)
	}
}

func TestCreate_EventFailureFailsTheCreate(t *testing.T) {
	svc, _, rec := newTestService()
	rec.err = errors.New("outbox down")
	if _, err := svc.Create(context.Background(), draft(dl(1, 10000))); err == nil {
		t.Fatal("a create whose event cannot be recorded must fail, not pretend")
	}
}

// RULE (ADR 0001 section 11): a revision is required, matched, and moved.
func TestUpdate_RevisionRules(t *testing.T) {
	svc, repo, _ := newTestService()
	q := repo.seed(QuoteStateDraft)
	ctx := context.Background()

	_, err := svc.Update(ctx, q.ID, draft(dl(1, 100)), Precondition{})
	asHTTPError(t, err, http.StatusPreconditionRequired, httpx.CodePreconditionRequired)

	rev := int64(7)
	_, err = svc.Update(ctx, q.ID, draft(dl(1, 100)), Precondition{Revision: &rev})
	asHTTPError(t, err, http.StatusConflict, httpx.CodeStaleRevision)

	rev = 1
	got, err := svc.Update(ctx, q.ID, draft(dl(2, 100)), Precondition{Revision: &rev})
	if err != nil {
		t.Fatal(err)
	}
	if got.Revision != 2 {
		t.Errorf("revision after an update = %d, want 2", got.Revision)
	}

	_, err = svc.Update(ctx, uuid.New(), draft(), Precondition{Revision: &rev})
	asHTTPError(t, err, http.StatusNotFound, httpx.CodeNotFound)
}

func TestUpdate_OnlyDraftsAreEditable(t *testing.T) {
	for _, st := range []QuoteState{QuoteStateSent, QuoteStateAccepted, QuoteStateRejected, QuoteStateExpired} {
		t.Run(st.Status(), func(t *testing.T) {
			svc, repo, _ := newTestService()
			q := repo.seed(st)
			rev := int64(1)
			_, err := svc.Update(context.Background(), q.ID, draft(dl(1, 100)), Precondition{Revision: &rev})
			asHTTPError(t, err, http.StatusConflict, httpx.CodeConflict)
			if repo.quotes[q.ID].Revision != 1 {
				t.Error("a refused edit moved the revision")
			}
		})
	}
}

func TestValidateStateTransition(t *testing.T) {
	allowed := map[QuoteState][]QuoteState{
		QuoteStateDraft:    {QuoteStateSent, QuoteStateAccepted, QuoteStateRejected, QuoteStateExpired},
		QuoteStateSent:     {QuoteStateAccepted, QuoteStateRejected, QuoteStateExpired},
		QuoteStateAccepted: {},
		QuoteStateRejected: {QuoteStateDraft},
		QuoteStateExpired:  {QuoteStateDraft},
	}
	all := []QuoteState{QuoteStateDraft, QuoteStateSent, QuoteStateAccepted, QuoteStateRejected, QuoteStateExpired}
	for _, from := range all {
		for _, to := range all {
			ok := false
			for _, a := range allowed[from] {
				if a == to {
					ok = true
				}
			}
			err := validateStateTransition(from, to)
			if ok && err != nil {
				t.Errorf("%s -> %s refused: %v", from, to, err)
			}
			if !ok {
				asHTTPError(t, err, http.StatusConflict, httpx.CodeInvalidStateTransition)
			}
		}
	}
	err := validateStateTransition(QuoteStateAccepted, QuoteStateSent)
	if err.Error() != "cannot transition from accepted to sent" {
		t.Errorf("message = %q, want the lowercase wire names", err)
	}
}

func TestTransition_StampsLifecycleTimestampsAndMovesRevision(t *testing.T) {
	cases := []struct {
		target                     QuoteState
		wantSent, wantAcc, wantRej bool
	}{
		{QuoteStateSent, true, false, false},
		{QuoteStateAccepted, false, true, false},
		{QuoteStateRejected, false, false, true},
		{QuoteStateExpired, false, false, false},
	}
	for _, c := range cases {
		t.Run(c.target.Status(), func(t *testing.T) {
			svc, repo, rec := newTestService()
			q := repo.seed(QuoteStateDraft)
			rev := int64(1)
			got, err := svc.Transition(context.Background(), q.ID, c.target, Precondition{Revision: &rev})
			if err != nil {
				t.Fatal(err)
			}
			if got.Status != c.target || got.Revision != 2 {
				t.Errorf("status/revision = %s/%d", got.Status, got.Revision)
			}
			if (got.SentAt != nil) != c.wantSent || (got.AcceptedAt != nil) != c.wantAcc || (got.RejectedAt != nil) != c.wantRej {
				t.Errorf("timestamps sent=%v accepted=%v rejected=%v", got.SentAt != nil, got.AcceptedAt != nil, got.RejectedAt != nil)
			}
			if len(rec.events) != 1 || rec.events[0].Type != transitionEvents[c.target] {
				t.Errorf("events = %+v, want one %s", rec.events, transitionEvents[c.target])
			}
			log := *repo.log
			if log[len(log)-1] != "event:"+transitionEvents[c.target] {
				t.Errorf("call order = %v, want the event last", log)
			}
		})
	}
}

func TestTransition_RefusalsWriteNothing(t *testing.T) {
	svc, repo, rec := newTestService()
	q := repo.seed(QuoteStateAccepted)
	ctx := context.Background()

	rev := int64(1)
	_, err := svc.Transition(ctx, q.ID, QuoteStateSent, Precondition{Revision: &rev})
	asHTTPError(t, err, http.StatusConflict, httpx.CodeInvalidStateTransition)

	stale := int64(9)
	_, err = svc.Transition(ctx, q.ID, QuoteStateSent, Precondition{Revision: &stale})
	asHTTPError(t, err, http.StatusConflict, httpx.CodeStaleRevision)

	_, err = svc.Transition(ctx, q.ID, QuoteStateSent, Precondition{})
	asHTTPError(t, err, http.StatusPreconditionRequired, httpx.CodePreconditionRequired)

	if repo.quotes[q.ID].Revision != 1 || len(rec.events) != 0 {
		t.Errorf("a refused transition moved the revision to %d and wrote %d events", repo.quotes[q.ID].Revision, len(rec.events))
	}
}

// UpdateState is the in process transition (the portal's accept, the
// integration seam): the same rules and event, no client revision.
func TestUpdateState_SameRulesNoPrecondition(t *testing.T) {
	svc, repo, rec := newTestService()
	q := repo.seed(QuoteStateSent)
	if err := svc.UpdateState(context.Background(), q.ID, QuoteStateAccepted); err != nil {
		t.Fatal(err)
	}
	if repo.quotes[q.ID].Status != QuoteStateAccepted || len(rec.events) != 1 {
		t.Errorf("status %s, %d events", repo.quotes[q.ID].Status, len(rec.events))
	}
	if err := svc.UpdateState(context.Background(), q.ID, QuoteStateSent); err == nil {
		t.Error("accepted is terminal for in process callers too")
	}
}

func costedLine(qty, cost int64) QuoteLine {
	id := uuid.New()
	return QuoteLine{ID: uuid.New(), ProductID: &id, Quantity: httpx.Quantity(qty * 10000), UnitCost: httpx.Price(cost)}
}

// Accepting raises a PO for every costed (special order) line and no other,
// with the line's own quantity and unit cost, after the commit.
func TestTransition_AutoPOOnlyForCostedLines(t *testing.T) {
	svc, repo, _ := newTestService()
	special := costedLine(3, 882500)
	stock := costedLine(10, 0)
	q := repo.seed(QuoteStateDraft, stock, special)
	po := &fakePO{}
	svc.WithAutoPO(po)

	rev := int64(1)
	if _, err := svc.Transition(context.Background(), q.ID, QuoteStateAccepted, Precondition{Revision: &rev}); err != nil {
		t.Fatal(err)
	}
	if len(po.calls) != 1 {
		t.Fatalf("raised %d POs, want 1", len(po.calls))
	}
	got := po.calls[0]
	if got.productID != *special.ProductID || got.quantity != 3 || got.unitCost != 88.25 || got.lineID != special.ID {
		t.Errorf("PO call = %+v", got)
	}
}

func TestTransition_AutoPOFailureDoesNotBlockAcceptance(t *testing.T) {
	svc, repo, _ := newTestService()
	q := repo.seed(QuoteStateSent, costedLine(1, 500000))
	svc.WithAutoPO(&fakePO{err: errors.New("vendor unavailable")})
	if err := svc.UpdateState(context.Background(), q.ID, QuoteStateAccepted); err != nil {
		t.Fatalf("a failed auto-PO must not undo the acceptance: %v", err)
	}
	if repo.quotes[q.ID].Status != QuoteStateAccepted {
		t.Error("quote must still be accepted")
	}
}

func TestTransition_NoAutoPOUnlessAccepted(t *testing.T) {
	for _, target := range []QuoteState{QuoteStateSent, QuoteStateRejected, QuoteStateExpired} {
		svc, repo, _ := newTestService()
		q := repo.seed(QuoteStateDraft, costedLine(1, 500000))
		po := &fakePO{}
		svc.WithAutoPO(po)
		if err := svc.UpdateState(context.Background(), q.ID, target); err != nil {
			t.Fatal(err)
		}
		if len(po.calls) != 0 {
			t.Errorf("transition to %s raised %d POs", target, len(po.calls))
		}
	}
}

type fakeSnapshot struct{ calls []uuid.UUID }

func (f *fakeSnapshot) SnapshotQuoteLines(_ context.Context, id uuid.UUID) error {
	f.calls = append(f.calls, id)
	return errors.New("pricing down")
}

// The exposure snapshot fires on send and is best effort.
func TestTransition_SnapshotOnSendIsBestEffort(t *testing.T) {
	svc, repo, _ := newTestService()
	q := repo.seed(QuoteStateDraft)
	snap := &fakeSnapshot{}
	svc.WithSnapshotService(snap)
	if err := svc.UpdateState(context.Background(), q.ID, QuoteStateSent); err != nil {
		t.Fatalf("a failing snapshot must not block the send: %v", err)
	}
	if len(snap.calls) != 1 || snap.calls[0] != q.ID {
		t.Errorf("snapshot calls = %v", snap.calls)
	}
}

// fakeOrderCreator records what the convert handed the order module.
type fakeOrderCreator struct {
	sources  []*order.QuoteSource
	created  []*order.Order
	existing bool
	failWith error
}

func (f *fakeOrderCreator) PrepareQuoteTax(context.Context, *order.QuoteSource) (*order.ProviderTax, error) {
	return nil, nil
}
func (f *fakeOrderCreator) QuoteHasOrder(context.Context, uuid.UUID) (bool, error) {
	return f.existing, nil
}
func (f *fakeOrderCreator) CreateFromQuote(_ context.Context, src *order.QuoteSource, _ *order.ProviderTax) (*order.Order, error) {
	if f.failWith != nil {
		return nil, f.failWith
	}
	f.sources = append(f.sources, src)
	o := &order.Order{}
	o.ID = uuid.New()
	o.QuoteID = &src.QuoteID
	o.Number = "SO-000001"
	o.Status = order.StatusDraft
	f.created = append(f.created, o)
	return o, nil
}

// The convert accepts the quote and hands the order module the lines without
// loss: the pair and the scale 4 price cross exactly (ADR 0005 5.8 lifts
// R1-15's refusal).
func TestConvert_HandsTheLinesToTheOrderCreator(t *testing.T) {
	svc, repo, _ := newTestService()
	pid := uuid.New()
	mbfLine := QuoteLine{
		ID: uuid.New(), ProductID: &pid, Quantity: 1875000, UOM: product.UOM_PCS, PriceUOM: "MBF",
		UOMQty: 1875000, PriceUOMQty: one, UnitPrice: 5000000,
	}
	plain := QuoteLine{
		ID: uuid.New(), Quantity: 100000, UOM: product.UOM_PCS, PriceUOM: "PCS",
		UOMQty: one, PriceUOMQty: one, UnitPrice: 26667,
	}
	q := repo.seed(QuoteStateSent, mbfLine, plain)
	creator := &fakeOrderCreator{}
	svc.WithOrderCreator(creator)

	rev := int64(1)
	o, err := svc.Convert(context.Background(), q.ID, Precondition{Revision: &rev})
	if err != nil {
		t.Fatal(err)
	}
	if o == nil || o.QuoteID == nil || *o.QuoteID != q.ID {
		t.Fatalf("convert returned %+v, want the created order of the quote", o)
	}
	if len(creator.sources) != 1 || len(creator.sources[0].Lines) != 2 {
		t.Fatalf("the order module received %d sources", len(creator.sources))
	}
	got := creator.sources[0].Lines[0]
	if got.UOMQty != 1875000 || got.PriceUOMQty != one || got.UnitPrice != 5000000 || got.PriceUOM != "MBF" {
		t.Errorf("the MBF line crossed as %+v, want the pair and the price exactly", got)
	}
	if after, err := svc.GetQuote(context.Background(), q.ID); err != nil || after.Status != QuoteStateAccepted {
		t.Errorf("quote after convert = %v (%v), want accepted", after.Status, err)
	}
}

// A quote that already has an order not cancelled is refused with
// already_converted, and the quote stays as it was.
func TestConvert_RefusesAnAlreadyConvertedQuote(t *testing.T) {
	svc, repo, _ := newTestService()
	q := repo.seed(QuoteStateSent)
	svc.WithOrderCreator(&fakeOrderCreator{existing: true})
	rev := int64(1)
	_, err := svc.Convert(context.Background(), q.ID, Precondition{Revision: &rev})
	var herr *httpx.Error
	if !errors.As(err, &herr) || herr.Code != httpx.CodeConflict || len(herr.Details) != 1 || herr.Details[0].Code != "already_converted" {
		t.Fatalf("err = %v, want conflict with an already_converted blocker", err)
	}
	if got, gerr := svc.GetQuote(context.Background(), q.ID); gerr != nil || got.Status != QuoteStateSent {
		t.Errorf("status after a refused convert = %v (%v), want sent", got.Status, gerr)
	}
}

// A failing order create surfaces from the convert: the quote service does
// not swallow it. The atomicity (the acceptance rolling back with the failed
// order in one transaction) is proven over the real database by the wire
// tests; this unit rig has no transaction runner to roll back.
func TestConvert_AFailedOrderCreateSurfaces(t *testing.T) {
	svc, repo, _ := newTestService()
	q := repo.seed(QuoteStateSent)
	svc.WithOrderCreator(&fakeOrderCreator{failWith: errors.New("order insert failed")})
	rev := int64(1)
	if _, err := svc.Convert(context.Background(), q.ID, Precondition{Revision: &rev}); err == nil {
		t.Fatal("convert succeeded though the order could not be created")
	}
}

func TestListQuotes_FetchesOneExtraRowToKnowThereIsAnotherPage(t *testing.T) {
	repo := &pagedRepo{fakeRepo: newFakeRepo(), rows: 5}
	svc := NewService(repo)
	items, more, total, err := svc.ListQuotes(context.Background(), ListFilter{Limit: 2}, true)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 2 || !more || total == nil || *total != 5 {
		t.Errorf("items=%d more=%v total=%v", len(items), more, total)
	}
	items, more, total, _ = svc.ListQuotes(context.Background(), ListFilter{Limit: 5}, false)
	if len(items) != 5 || more || total != nil {
		t.Errorf("last page: items=%d more=%v total=%v", len(items), more, total)
	}
}

type pagedRepo struct {
	*fakeRepo
	rows int
}

func (p *pagedRepo) ListQuotes(_ context.Context, f ListFilter) ([]QuoteSummary, error) {
	n := f.Limit
	if n > p.rows {
		n = p.rows
	}
	out := make([]QuoteSummary, n)
	for i := range out {
		out[i] = QuoteSummary{ID: uuid.New(), CreatedAt: httpx.TimestampOf(time.Now())}
	}
	return out, nil
}

func (p *pagedRepo) CountQuotes(context.Context, ListFilter) (int64, error) {
	return int64(p.rows), nil
}
