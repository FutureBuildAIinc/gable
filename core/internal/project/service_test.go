// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package project

import (
	"context"
	"errors"
	"net/http"
	"testing"

	"github.com/gablelbm/gable/internal/platform/httpx"
	"github.com/gablelbm/gable/pkg/audit"
	"github.com/gablelbm/gable/pkg/outbox"
	"github.com/google/uuid"
)

// The service's behaviour with a fake store (the recipe's service tests): the
// order of calls inside one transaction (row, audit row, event last), the
// name rule, and refusals writing nothing.

type fakeRepo struct {
	stored map[uuid.UUID]*Project
	calls  []string
	order  *[]string
	cust   uuid.UUID
}

func newFakeRepo(customer uuid.UUID) *fakeRepo {
	return &fakeRepo{stored: map[uuid.UUID]*Project{}, cust: customer}
}

func (f *fakeRepo) note(call string) {
	f.calls = append(f.calls, call)
	if f.order != nil {
		*f.order = append(*f.order, call)
	}
}

func (f *fakeRepo) List(_ context.Context, customerID uuid.UUID, _ ListFilter, _ bool) ([]Project, bool, *int64, error) {
	f.note("list")
	return nil, false, nil, nil
}
func (f *fakeRepo) Get(_ context.Context, id, customerID uuid.UUID) (*Project, error) {
	f.note("get")
	p, ok := f.stored[id]
	if !ok || p.CustomerID != customerID {
		return nil, ErrNotFound
	}
	cp := *p
	return &cp, nil
}
func (f *fakeRepo) Create(_ context.Context, p *Project) error {
	f.note("create")
	cp := *p
	cp.Revision = 1
	f.stored[p.ID] = &cp
	return nil
}
func (f *fakeRepo) Lock(_ context.Context, id, customerID uuid.UUID) error {
	f.note("lock")
	p, ok := f.stored[id]
	if !ok || p.CustomerID != customerID {
		return ErrNotFound
	}
	return nil
}
func (f *fakeRepo) Update(_ context.Context, p *Project) error {
	f.note("update")
	cur, ok := f.stored[p.ID]
	if !ok || cur.CustomerID != p.CustomerID {
		return ErrNotFound
	}
	cp := *p
	cp.Revision = cur.Revision + 1
	f.stored[p.ID] = &cp
	return nil
}
func (f *fakeRepo) Entities(_ context.Context, _, _ uuid.UUID) ([]ProjectItem, []ProjectItem, []ProjectItem, error) {
	f.note("entities")
	return []ProjectItem{}, []ProjectItem{}, []ProjectItem{}, nil
}

type recordingEvents struct {
	events []outbox.Event
	err    error
	order  *[]string
}

func (r *recordingEvents) Write(_ context.Context, ev outbox.Event) error {
	if r.err != nil {
		return r.err
	}
	r.events = append(r.events, ev)
	if r.order != nil {
		*r.order = append(*r.order, "event")
	}
	return nil
}

type recordingAudit struct {
	rows  []string
	err   error
	order *[]string
}

func (r *recordingAudit) Log(_ context.Context, e audit.Entry) error {
	if r.err != nil {
		return r.err
	}
	r.rows = append(r.rows, e.Action)
	if r.order != nil {
		*r.order = append(*r.order, "audit:"+e.Action)
	}
	return nil
}

type runTx struct{}

func (runTx) RunInTx(ctx context.Context, fn func(ctx context.Context) error) error { return fn(ctx) }

func nameDraft(name string) *Draft { return &Draft{Name: &name} }

// The create writes the row, re-reads it, then the audit row, then the event
// last.
func TestCreate_OrderRowAuditEvent(t *testing.T) {
	cust := uuid.New()
	repo := newFakeRepo(cust)
	events := &recordingEvents{}
	aud := &recordingAudit{}
	var order []string
	events.order, aud.order, repo.order = &order, &order, &order
	svc := NewService(repo).WithOutbox(events).WithTxRunner(runTx{}).WithAudit(aud)
	if _, err := svc.Create(context.Background(), cust, nameDraft("Phase one")); err != nil {
		t.Fatal(err)
	}
	want := []string{"create", "get", "audit:project.created", "event"}
	for i := range want {
		if order[i] != want[i] {
			t.Fatalf("order = %v, want %v", order, want)
		}
	}
	if len(events.events) != 1 || events.events[0].Type != EventCreated {
		t.Errorf("events = %v, want one %s", events.events, EventCreated)
	}
}

// A create defaults to the active status.
func TestCreate_DefaultsToActive(t *testing.T) {
	cust := uuid.New()
	svc := NewService(newFakeRepo(cust))
	p, err := svc.Create(context.Background(), cust, &Draft{})
	if err != nil {
		t.Fatal(err)
	}
	if p.Status != StatusActive {
		t.Errorf("status = %q, want active", p.Status)
	}
}

// A failed event write fails the create and nothing is kept.
func TestCreate_FailedEventFailsTheCreate(t *testing.T) {
	cust := uuid.New()
	svc := NewService(newFakeRepo(cust)).WithOutbox(&recordingEvents{err: errors.New("no")}).WithTxRunner(runTx{})
	if _, err := svc.Create(context.Background(), cust, nameDraft("x")); err == nil {
		t.Error("create succeeded though its event could not be written")
	}
}

// The update refuses a write with no precondition and a stale one, and moves
// only the fields the request named.
func TestUpdate_PreconditionsAndChangedFields(t *testing.T) {
	cust := uuid.New()
	repo := newFakeRepo(cust)
	existing := &Project{ID: uuid.New(), CustomerID: cust, Name: "Phase one", Status: StatusActive, Revision: 3}
	repo.stored[existing.ID] = existing
	svc := NewService(repo).WithTxRunner(runTx{})

	if _, err := svc.Update(context.Background(), existing.ID, cust, nameDraft("x"), Precondition{}); err == nil {
		t.Fatal("a write with neither If-Match nor body revision must be a 428")
	} else if e, ok := err.(*httpx.Error); !ok || e.Status != http.StatusPreconditionRequired {
		t.Fatalf("no precondition: got %v, want 428", err)
	}
	stale := int64(2)
	if _, err := svc.Update(context.Background(), existing.ID, cust, nameDraft("x"), Precondition{Revision: &stale}); err == nil {
		t.Fatal("a stale revision must be a 409")
	} else if e, ok := err.(*httpx.Error); !ok || e.Status != http.StatusConflict || e.Code != "stale_revision" {
		t.Fatalf("stale revision: got %v, want 409 stale_revision", err)
	}
	rev := int64(3)
	rename := "Phase two"
	done := StatusCompleted
	p, err := svc.Update(context.Background(), existing.ID, cust, &Draft{Name: &rename, Status: done, Revision: &rev}, Precondition{Revision: &rev})
	if err != nil {
		t.Fatal(err)
	}
	if p.Name != "Phase two" || p.Status != StatusCompleted || p.Revision != 4 {
		t.Errorf("update = %+v", p)
	}

	// A re-read of a project of another customer's is a 404, not a leak.
	if _, err := svc.Update(context.Background(), existing.ID, uuid.New(), nameDraft("x"), Precondition{IfMatch: `"4"`}); err == nil {
		t.Error("another customer's project updated")
	} else if e, ok := err.(*httpx.Error); !ok || e.Status != http.StatusNotFound {
		t.Errorf("got %v, want 404", err)
	}
	_ = http.StatusOK
}

// The update's event carries the changed fields.
func TestUpdate_EventCarriesChangedFields(t *testing.T) {
	cust := uuid.New()
	repo := newFakeRepo(cust)
	existing := &Project{ID: uuid.New(), CustomerID: cust, Name: "Phase one", Status: StatusActive, Revision: 1}
	repo.stored[existing.ID] = existing
	events := &recordingEvents{}
	svc := NewService(repo).WithOutbox(events).WithTxRunner(runTx{})
	done := StatusCompleted
	if _, err := svc.Update(context.Background(), existing.ID, cust, &Draft{Status: done}, Precondition{IfMatch: `"1"`}); err != nil {
		t.Fatal(err)
	}
	if len(events.events) != 1 || events.events[0].Type != EventUpdated {
		t.Fatalf("events = %v", events.events)
	}
	// The data is JSON; the changed list names status only.
	data := string(events.events[0].Data)
	if !contains(data, `"changed":["status"]`) {
		t.Errorf("event data = %s, want changed [status]", data)
	}
}

func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}

// The vocabulary: lowercase on the wire, only the writable spellings parse.
func TestStatusVocabulary(t *testing.T) {
	for wire, storage := range map[ProjectStatus]string{
		StatusActive: "Active", StatusCompleted: "Completed", StatusInactive: "Inactive",
	} {
		if got := fromStorage(storage); got != wire {
			t.Errorf("fromStorage(%q) = %q, want %q", storage, got, wire)
		}
	}
	for _, ok := range []string{"active", "completed"} {
		if _, parsed := ParseProjectStatus(ok); !parsed {
			t.Errorf("ParseProjectStatus(%q) refused a writable status", ok)
		}
	}
	for _, refused := range []string{"Active", "ACTIVE", "inactive", "done", ""} {
		if _, parsed := ParseProjectStatus(refused); parsed {
			t.Errorf("ParseProjectStatus(%q) accepted a non writable spelling", refused)
		}
	}
}

// The parse collects every problem into one 400.
func TestRequestParse(t *testing.T) {
	blank := "  "
	req := &Request{Name: &blank, Status: strp("Active")}
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
	for _, want := range []string{"name", "status"} {
		if !fields[want] {
			t.Errorf("details = %v, want a %s entry", e.Details, want)
		}
	}

	// An update may send only the fields it changes; a create requires name.
	if _, err := (&Request{Status: strp("completed")}).Parse(true); err != nil {
		t.Errorf("a status only update: %v", err)
	}
	if _, err := (&Request{}).Parse(false); err == nil {
		t.Error("a create without a name parsed")
	}
}

func strp(s string) *string { return &s }
