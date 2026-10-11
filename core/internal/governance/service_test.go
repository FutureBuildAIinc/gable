// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package governance

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/gablelbm/gable/internal/platform/httpx"
	"github.com/gablelbm/gable/pkg/outbox"
	"github.com/google/uuid"
)

// fakeRepo is an in-memory Repository for unit tests (no DB required). It
// records the order of calls, so a test can prove a refusal writes nothing.
type fakeRepo struct {
	rfcs  map[uuid.UUID]*RFC
	calls []string
}

func newFakeRepo() *fakeRepo {
	return &fakeRepo{rfcs: map[uuid.UUID]*RFC{}}
}

func (f *fakeRepo) note(call string) { f.calls = append(f.calls, call) }

func (f *fakeRepo) CreateRFC(_ context.Context, rfc *RFC) error {
	f.note("create")
	if rfc.ID == uuid.Nil {
		rfc.ID = uuid.New()
	}
	rfc.Revision = 1
	rfc.CreatedAt = fakeNow()
	rfc.UpdatedAt = rfc.CreatedAt
	cp := *rfc
	f.rfcs[rfc.ID] = &cp
	return nil
}

func (f *fakeRepo) LockRFC(_ context.Context, id uuid.UUID) error {
	f.note("lock")
	if _, ok := f.rfcs[id]; !ok {
		return ErrNotFound
	}
	return nil
}

func (f *fakeRepo) GetRFC(_ context.Context, id uuid.UUID) (*RFC, error) {
	f.note("get")
	cp, ok := f.rfcs[id]
	if !ok {
		return nil, ErrNotFound
	}
	return cp, nil
}

func (f *fakeRepo) ListRFCs(_ context.Context, _ ListFilter) ([]RFCSummary, error) {
	out := make([]RFCSummary, 0, len(f.rfcs))
	for _, r := range f.rfcs {
		out = append(out, r.RFCSummary)
	}
	return out, nil
}

func (f *fakeRepo) CountRFCs(_ context.Context, _ ListFilter) (int64, error) {
	return int64(len(f.rfcs)), nil
}

func (f *fakeRepo) UpdateRFC(_ context.Context, rfc *RFC) error {
	f.note("update")
	stored := f.rfcs[rfc.ID]
	stored.Title = rfc.Title
	stored.ProblemStatement = rfc.ProblemStatement
	stored.ProposedSolution = rfc.ProposedSolution
	stored.Content = rfc.Content
	stored.Revision++
	return nil
}

func (f *fakeRepo) SetStatus(_ context.Context, id uuid.UUID, status RFCStatus) error {
	f.note("set_status")
	f.rfcs[id].Status = status
	f.rfcs[id].Revision++
	return nil
}

func (f *fakeRepo) NextNumber(_ context.Context) (string, error) {
	f.note("number")
	return "RFC-000042", nil
}

func fakeNow() httpx.Timestamp {
	return httpx.TimestampOf(time.Date(2030, 1, 2, 3, 4, 5, 0, time.UTC))
}

// recorderEvents collects the events a service writes.
type recorderEvents struct {
	events []outbox.Event
	err    error
}

func (r *recorderEvents) Write(_ context.Context, ev outbox.Event) error {
	if r.err != nil {
		return r.err
	}
	r.events = append(r.events, ev)
	return nil
}

func parsedCreate() *ParsedCreate {
	return &ParsedCreate{
		Title:            "Add will-call pickup workflow",
		ProblemStatement: "Orders have no pickup path distinct from delivery.",
		ProposedSolution: "Introduce will_call_tickets and a READY_FOR_PICKUP status.",
	}
}

func TestDraftRFC_GeneratesContentMintsNumberWritesEvent(t *testing.T) {
	repo := newFakeRepo()
	events := &recorderEvents{}
	svc := NewService(repo, NewTemplateAIProvider()).WithOutbox(events)

	rfc, err := svc.DraftRFC(context.Background(), parsedCreate())
	if err != nil {
		t.Fatalf("DraftRFC: %v", err)
	}
	if rfc.Status != RFCStatusDraft {
		t.Fatalf("status = %s, want draft", rfc.Status)
	}
	if rfc.Content == nil || *rfc.Content == "" {
		t.Fatal("the provider's content is missing")
	}
	if rfc.Number != "RFC-000042" {
		t.Errorf("number = %s", rfc.Number)
	}
	if len(events.events) != 1 || events.events[0].Type != EventCreated {
		t.Fatalf("events = %v, want one rfc.created", events.events)
	}
}

func TestDraftRFC_ProviderFailureCreatesNothing(t *testing.T) {
	repo := newFakeRepo()
	svc := NewService(repo, failingProvider{})
	if _, err := svc.DraftRFC(context.Background(), parsedCreate()); err == nil {
		t.Fatal("DraftRFC succeeded though the provider failed")
	}
	if len(repo.calls) != 0 {
		t.Errorf("a refused create reached the repository: %v", repo.calls)
	}
}

type failingProvider struct{}

func (failingProvider) GenerateRFC(context.Context, string, string, string) (string, error) {
	return "", errors.New("generator down")
}

func TestUpdateRFC_Rules(t *testing.T) {
	repo := newFakeRepo()
	events := &recorderEvents{}
	svc := NewService(repo, NewTemplateAIProvider()).WithOutbox(events)
	created, err := svc.DraftRFC(context.Background(), parsedCreate())
	if err != nil {
		t.Fatal(err)
	}
	events.events = nil

	// No precondition: refused, nothing written.
	if _, err := svc.UpdateRFC(context.Background(), created.ID, &ParsedUpdate{}, Precondition{}); err == nil {
		t.Fatal("an update without a precondition succeeded")
	}
	if len(events.events) != 0 {
		t.Errorf("the refused update wrote %v", events.events)
	}

	rev := int64(1)
	title := "Renamed"
	if _, err := svc.UpdateRFC(context.Background(), created.ID, &ParsedUpdate{Title: &title}, Precondition{Revision: &rev}); err != nil {
		t.Fatalf("update: %v", err)
	}
	if got := repo.rfcs[created.ID].Title; got != "Renamed" {
		t.Errorf("title = %s", got)
	}
	if repo.rfcs[created.ID].Revision != 2 {
		t.Errorf("revision = %d, want 2", repo.rfcs[created.ID].Revision)
	}
	if len(events.events) != 1 || events.events[0].Type != EventUpdated {
		t.Errorf("events = %v, want rfc.updated alone", events.events)
	}

	// Approved RFCs cannot be edited.
	repo.rfcs[created.ID].Status = RFCStatusApproved
	rev = repo.rfcs[created.ID].Revision
	if _, err := svc.UpdateRFC(context.Background(), created.ID, &ParsedUpdate{Title: &title}, Precondition{Revision: &rev}); err == nil {
		t.Fatal("an approved RFC was edited")
	}
}

func TestTransition_EdgesAndEvents(t *testing.T) {
	repo := newFakeRepo()
	events := &recorderEvents{}
	svc := NewService(repo, NewTemplateAIProvider()).WithOutbox(events)
	created, err := svc.DraftRFC(context.Background(), parsedCreate())
	if err != nil {
		t.Fatal(err)
	}
	events.events = nil

	rev := int64(1)
	// draft -> approved is not an edge.
	if _, err := svc.Transition(context.Background(), created.ID, RFCStatusApproved, Precondition{Revision: &rev}); err == nil {
		t.Fatal("draft -> approved was allowed")
	}
	if repo.rfcs[created.ID].Status != RFCStatusDraft {
		t.Errorf("the refused transition moved the status to %s", repo.rfcs[created.ID].Status)
	}
	// draft -> review -> approved, each writing its event.
	if _, err := svc.Transition(context.Background(), created.ID, RFCStatusReview, Precondition{Revision: &rev}); err != nil {
		t.Fatalf("draft -> review: %v", err)
	}
	rev = repo.rfcs[created.ID].Revision
	if _, err := svc.Transition(context.Background(), created.ID, RFCStatusApproved, Precondition{Revision: &rev}); err != nil {
		t.Fatalf("review -> approved: %v", err)
	}
	// approved is terminal.
	rev = repo.rfcs[created.ID].Revision
	if _, err := svc.Transition(context.Background(), created.ID, RFCStatusReview, Precondition{Revision: &rev}); err == nil {
		t.Fatal("approved -> review was allowed")
	}

	var types []string
	for _, ev := range events.events {
		types = append(types, ev.Type)
	}
	want := []string{EventReview, EventApproved}
	if len(types) != len(want) || types[0] != want[0] || types[1] != want[1] {
		t.Errorf("events = %v, want %v", types, want)
	}
}
