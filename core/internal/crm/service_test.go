// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package crm

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/gablelbm/gable/internal/platform/httpx"
	"github.com/gablelbm/gable/pkg/audit"
	"github.com/gablelbm/gable/pkg/outbox"
	"github.com/google/uuid"
)

// The service's behaviour with a fake store (the recipe's service tests): the
// order of calls inside one transaction (row, audit row, event last), the
// defaults, and refusals writing nothing.

// fakeRepo records every call in order.
type fakeRepo struct {
	visible   bool
	stored    map[uuid.UUID]*Activity
	calls     []string
	orderRef  *[]string
	createErr error
}

func newFakeRepo() *fakeRepo { return &fakeRepo{visible: true, stored: map[uuid.UUID]*Activity{}} }

func (f *fakeRepo) note(call string) {
	f.calls = append(f.calls, call)
	if f.orderRef != nil {
		*f.orderRef = append(*f.orderRef, call)
	}
}

func (f *fakeRepo) List(_ context.Context, _ uuid.UUID, fl ListFilter, _ bool) ([]Activity, bool, *int64, error) {
	f.note("list")
	return nil, false, nil, nil
}
func (f *fakeRepo) Get(_ context.Context, id uuid.UUID) (*Activity, error) {
	f.note("get")
	a, ok := f.stored[id]
	if !ok {
		return nil, ErrNotFound
	}
	cp := *a
	return &cp, nil
}
func (f *fakeRepo) Create(_ context.Context, a *Activity) error {
	f.note("create")
	if f.createErr != nil {
		return f.createErr
	}
	cp := *a
	cp.Revision = 1
	f.stored[a.ID] = &cp
	return nil
}
func (f *fakeRepo) Lock(_ context.Context, id uuid.UUID) error {
	f.note("lock")
	if _, ok := f.stored[id]; !ok {
		return ErrNotFound
	}
	return nil
}
func (f *fakeRepo) Update(_ context.Context, a *Activity) error {
	f.note("update")
	cur, ok := f.stored[a.ID]
	if !ok {
		return ErrNotFound
	}
	cp := *a
	cp.Revision = cur.Revision + 1
	f.stored[a.ID] = &cp
	return nil
}
func (f *fakeRepo) Delete(_ context.Context, id uuid.UUID) error {
	f.note("delete")
	if _, ok := f.stored[id]; !ok {
		return ErrNotFound
	}
	delete(f.stored, id)
	return nil
}
func (f *fakeRepo) CustomerVisible(_ context.Context, _ uuid.UUID) (bool, error) {
	return f.visible, nil
}

// recordingEvents records the events and, when told to, the call order
// against the other calls.
type recordingEvents struct {
	events   []outbox.Event
	err      error
	orderRef *[]string
}

func (r *recordingEvents) Write(_ context.Context, ev outbox.Event) error {
	if r.err != nil {
		return r.err
	}
	r.events = append(r.events, ev)
	if r.orderRef != nil {
		*r.orderRef = append(*r.orderRef, "event")
	}
	return nil
}

// recordingAudit records the audit rows and the call order.
type recordingAudit struct {
	rows     []string
	err      error
	orderRef *[]string
}

func (r *recordingAudit) Log(_ context.Context, e audit.Entry) error {
	if r.err != nil {
		return r.err
	}
	r.rows = append(r.rows, e.Action)
	if r.orderRef != nil {
		*r.orderRef = append(*r.orderRef, "audit:"+e.Action)
	}
	return nil
}

// runTx is a TxRunner that just runs fn (the order under a real transaction
// is the tx proofs' subject).
type runTx struct{}

func (runTx) RunInTx(ctx context.Context, fn func(ctx context.Context) error) error { return fn(ctx) }

func draft(now time.Time) *Draft {
	call := ActivityCall
	ts := httpx.TimestampOf(now)
	return &Draft{ActivityType: call, Description: "first call", ActivityDate: &ts}
}

// The create writes the row, then the audit row, then the event last.
func TestCreate_OrderRowAuditEvent(t *testing.T) {
	repo := newFakeRepo()
	events := &recordingEvents{}
	aud := &recordingAudit{}
	var order []string
	events.orderRef, aud.orderRef, repo.orderRef = &order, &order, &order
	svc := NewService(repo).WithOutbox(events).WithTxRunner(runTx{}).WithAudit(aud)
	if _, err := svc.Create(context.Background(), uuid.New(), draft(time.Now())); err != nil {
		t.Fatal(err)
	}
	want := []string{"create", "get", "audit:activity.created", "event"}
	if strings.Join(order, ",") != strings.Join(want, ",") {
		t.Errorf("order = %v, want %v", order, want)
	}
	if len(events.events) != 1 || events.events[0].Type != EventCreated {
		t.Errorf("events = %v, want one %s", events.events, EventCreated)
	}
}

// A missing activity_date defaults to now, so an undated call sorts with the
// day's log, not at the epoch.
func TestCreate_DefaultsActivityDateToNow(t *testing.T) {
	repo := newFakeRepo()
	cust := uuid.New()
	svc := NewService(repo)
	before := time.Now().Add(-time.Second)
	a, err := svc.Create(context.Background(), cust, &Draft{ActivityType: ActivityNote, Description: "note"})
	if err != nil {
		t.Fatal(err)
	}
	if a.ActivityDate.Before(before) {
		t.Errorf("activity_date = %v, want now", a.ActivityDate)
	}
}

// A failed event write fails the create and nothing is kept.
func TestCreate_FailedEventFailsTheCreate(t *testing.T) {
	repo := newFakeRepo()
	svc := NewService(repo).WithOutbox(&recordingEvents{err: errors.New("no")}).WithTxRunner(runTx{})
	if _, err := svc.Create(context.Background(), uuid.New(), draft(time.Now())); err == nil {
		t.Error("create succeeded though its event could not be written")
	}
}

// The update refuses a write with no precondition, whatever the repository.
func TestUpdate_Preconditions(t *testing.T) {
	repo := newFakeRepo()
	existing := &Activity{ID: uuid.New(), ActivityType: ActivityCall, Description: "x", Revision: 4}
	repo.stored[existing.ID] = existing
	svc := NewService(repo).WithTxRunner(runTx{})

	if _, err := svc.Update(context.Background(), existing.ID, draft(time.Now()), Precondition{}); err == nil {
		t.Error("a write with neither If-Match nor body revision must be a 428")
	} else if e, ok := err.(*httpx.Error); !ok || e.Status != 428 {
		t.Errorf("no precondition: got %v, want 428", err)
	}
	stale := int64(3)
	if _, err := svc.Update(context.Background(), existing.ID, draft(time.Now()), Precondition{Revision: &stale}); err == nil {
		t.Error("a stale revision must be a 409")
	} else if e, ok := err.(*httpx.Error); !ok || e.Status != 409 || e.Code != "stale_revision" {
		t.Errorf("stale revision: got %v, want 409 stale_revision", err)
	}
	good := int64(4)
	if _, err := svc.Update(context.Background(), existing.ID, draft(time.Now()), Precondition{Revision: &good}); err != nil {
		t.Fatalf("current revision: %v", err)
	}
	if a := repo.stored[existing.ID]; a.Revision != 5 {
		t.Errorf("revision after update = %d, want 5", a.Revision)
	}
}

// The update keeps the stored activity_date when the body sends none.
func TestUpdate_KeepsActivityDateWhenAbsent(t *testing.T) {
	repo := newFakeRepo()
	when := httpx.TimestampOf(time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC))
	existing := &Activity{ID: uuid.New(), ActivityType: ActivityCall, Description: "x", Revision: 1, ActivityDate: when}
	repo.stored[existing.ID] = existing
	svc := NewService(repo).WithTxRunner(runTx{})
	rev := int64(1)
	a, err := svc.Update(context.Background(), existing.ID, &Draft{ActivityType: ActivityNote, Description: "y", Revision: &rev}, Precondition{Revision: &rev})
	if err != nil {
		t.Fatal(err)
	}
	if !a.ActivityDate.Time.Equal(when.Time) {
		t.Errorf("activity_date = %v, want the stored %v", a.ActivityDate, when)
	}
}

// The delete takes the same precondition, and writes its audit row and event.
func TestDelete_PreconditionAndEvent(t *testing.T) {
	repo := newFakeRepo()
	existing := &Activity{ID: uuid.New(), ActivityType: ActivityCall, Description: "x", Revision: 2}
	repo.stored[existing.ID] = existing
	events := &recordingEvents{}
	svc := NewService(repo).WithOutbox(events).WithTxRunner(runTx{}).WithAudit(&recordingAudit{})
	if err := svc.Delete(context.Background(), existing.ID, Precondition{}); err == nil {
		t.Fatal("delete without a precondition must be a 428")
	}
	if err := svc.Delete(context.Background(), existing.ID, Precondition{IfMatch: `"2"`}); err != nil {
		t.Fatal(err)
	}
	if len(events.events) != 1 || events.events[0].Type != EventDeleted {
		t.Errorf("events = %v, want one %s", events.events, EventDeleted)
	}
}

// A create on a customer the caller cannot see is the same 404 as reading it.
func TestCreate_InvisibleCustomerIs404(t *testing.T) {
	repo := newFakeRepo()
	repo.visible = false
	svc := NewService(repo).WithTxRunner(runTx{})
	_, err := svc.Create(context.Background(), uuid.New(), draft(time.Now()))
	if err == nil {
		t.Fatal("create on an invisible customer succeeded")
	}
	if e, ok := err.(*httpx.Error); !ok || e.Status != 404 {
		t.Errorf("got %v, want 404", err)
	}
	if len(repo.calls) != 0 {
		t.Errorf("calls = %v, want none", repo.calls)
	}
}

// The event data is a small summary with the wire vocabulary.
func TestEvent_DataIsASmallSummary(t *testing.T) {
	repo := newFakeRepo()
	events := &recordingEvents{}
	svc := NewService(repo).WithOutbox(events).WithTxRunner(runTx{})
	if _, err := svc.Create(context.Background(), uuid.New(), draft(time.Now())); err != nil {
		t.Fatal(err)
	}
	var data map[string]any
	if err := json.Unmarshal(events.events[0].Data, &data); err != nil {
		t.Fatal(err)
	}
	if data["activity_type"] != "call" {
		t.Errorf("activity_type = %v, want the wire spelling call", data["activity_type"])
	}
	if _, ok := data["description"]; ok {
		t.Error("the event carries the document, not a summary")
	}
}

// The vocabulary: lowercase on the wire, and only the lowercase spelling
// parses (ADR 0001 section 6).
func TestActivityTypeVocabulary(t *testing.T) {
	for storage, wire := range map[ActivityType]string{
		ActivityCall: "call", ActivityMeeting: "meeting", ActivityEmail: "email", ActivityNote: "note",
	} {
		got, err := storage.MarshalText()
		if err != nil || string(got) != wire {
			t.Errorf("MarshalText(%q) = %q, %v; want %q", storage, got, err, wire)
		}
		back, ok := ParseActivityType(wire)
		if !ok || back != storage {
			t.Errorf("ParseActivityType(%q) = %q, %v", wire, back, ok)
		}
	}
	for _, refused := range []string{"CALL", "Call", "calls", "task", ""} {
		if _, ok := ParseActivityType(refused); ok {
			t.Errorf("ParseActivityType(%q) accepted a non lowercase spelling", refused)
		}
	}
}

// The parse collects every problem into one 400.
func TestRequestParse_CollectsEveryProblem(t *testing.T) {
	req := &Request{ActivityType: strp("CALL"), Description: strp("  ")}
	_, err := req.Parse(false)
	if err == nil {
		t.Fatal("parse succeeded")
	}
	e, ok := err.(*httpx.Error)
	if !ok || e.Code != "validation_failed" {
		t.Fatalf("got %v, want a validation_failed", err)
	}
	fields := map[string]bool{}
	for _, d := range e.Details {
		fields[d.Field] = true
	}
	for _, want := range []string{"activity_type", "description"} {
		if !fields[want] {
			t.Errorf("details = %v, want a %s entry", e.Details, want)
		}
	}
}

// A body customer_id is refused on both routes: the path names the customer.
func TestRequestParse_RefusesBodyCustomerID(t *testing.T) {
	req := &Request{ActivityType: strp("call"), Description: strp("d"), CustomerID: json.RawMessage(`"00000000-0000-0000-0000-000000000001"`)}
	if _, err := req.Parse(false); err == nil {
		t.Error("create accepted a body customer_id")
	}
	if _, err := req.Parse(true); err == nil {
		t.Error("update accepted a body customer_id")
	}
}

func strp(s string) *string { return &s }

// MarshalText degrades, never errors: the storage column held free text for
// the base's whole life and one stray row must not fail a response midway
// through encoding (a 200 with a truncated body). A value outside the four
// reads as the wire's catch-all, note, so the closed wire vocabulary holds.
func TestActivityTypeMarshalTextDegrades(t *testing.T) {
	for _, storage := range []ActivityType{ActivityCall, ActivityMeeting, ActivityEmail, ActivityNote} {
		text, err := storage.MarshalText()
		if err != nil {
			t.Fatalf("%s MarshalText: %v", storage, err)
		}
		if want := wireNames[storage]; string(text) != want {
			t.Errorf("%s MarshalText = %q, want %q", storage, text, want)
		}
	}
	for _, stray := range []ActivityType{"call", "WeIrD", "", "SOMEDAY"} {
		text, err := stray.MarshalText()
		if err != nil {
			t.Fatalf("a stray storage value %q failed the read: %v", stray, err)
		}
		if string(text) != "note" {
			t.Errorf("a stray storage value %q read as %q, want the catch-all note", stray, text)
		}
	}
}
