// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package project

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/gablelbm/gable/internal/platform/httpx"
	"github.com/gablelbm/gable/pkg/audit"
	"github.com/gablelbm/gable/pkg/outbox"
	"github.com/google/uuid"
)

// EventRecorder writes a domain event into the transactional outbox. The
// write joins the caller's transaction when there is one, so the event and
// the mutation are one fact (ADR 0003 section 3). *outbox.Writer satisfies
// it; nil records nothing (unit tests).
type EventRecorder interface {
	Write(ctx context.Context, ev outbox.Event) error
}

// TxRunner runs fn inside one transaction, joining the caller's when ctx
// carries one. *database.DB satisfies it.
type TxRunner interface {
	RunInTx(ctx context.Context, fn func(ctx context.Context) error) error
}

// AuditLogger writes an audit row; *audit.Logger satisfies it.
type AuditLogger interface {
	Log(ctx context.Context, entry audit.Entry) error
}

// Event types the module writes to the outbox: a project has no lifecycle
// machine, so the vocabulary is the two facts a project write has.
const (
	EventCreated = "project.created"
	EventUpdated = "project.updated"
)

// Precondition is the client's revision: the If-Match header and the body's
// revision, both optional here; the service refuses a write with neither
// (428) and one whose revision is behind (409).
type Precondition struct {
	IfMatch  string
	Revision *int64
}

// Service is the project module's behaviour: every mutation is one
// transaction that writes the row, its audit row and its event, in that
// order, with the event last (ADR 0003 section 2).
type Service struct {
	repo   Repository
	now    func() time.Time
	events EventRecorder
	tx     TxRunner
	audit  AuditLogger
}

func NewService(repo Repository) *Service {
	return &Service{repo: repo, now: time.Now}
}

// WithOutbox wires the event writes.
func (s *Service) WithOutbox(events EventRecorder) *Service { s.events = events; return s }

// WithTxRunner wires the transaction wrapper every write uses.
func (s *Service) WithTxRunner(tx TxRunner) *Service { s.tx = tx; return s }

// WithAudit wires the audit rows.
func (s *Service) WithAudit(a AuditLogger) *Service { s.audit = a; return s }

func (s *Service) inTx(ctx context.Context, fn func(ctx context.Context) error) error {
	if s.tx == nil {
		return fn(ctx)
	}
	return s.tx.RunInTx(ctx, fn)
}

func (s *Service) List(ctx context.Context, customerID uuid.UUID, f ListFilter, wantTotal bool) ([]Project, bool, *int64, error) {
	return s.repo.List(ctx, customerID, f, wantTotal)
}

func (s *Service) Dashboard(ctx context.Context, projectID, customerID uuid.UUID) (*ProjectDashboard, error) {
	proj, err := s.repo.Get(ctx, projectID, customerID)
	if err != nil {
		return nil, notFound(err)
	}
	orders, deliveries, invoices, err := s.repo.Entities(ctx, projectID, customerID)
	if err != nil {
		return nil, err
	}
	return &ProjectDashboard{Project: *proj, Orders: orders, Deliveries: deliveries, Invoices: invoices}, nil
}

// Create makes a project for the customer the portal chain identified.
func (s *Service) Create(ctx context.Context, customerID uuid.UUID, d *Draft) (*Project, error) {
	name := "Untitled project"
	if d.Name != nil {
		name = *d.Name
	}
	status := StatusActive
	p := &Project{ID: uuid.New(), CustomerID: customerID, Name: name, Status: status,
		CreatedAt: httpx.TimestampOf(s.now().UTC()), UpdatedAt: httpx.TimestampOf(s.now().UTC())}
	var out *Project
	err := s.inTx(ctx, func(ctx context.Context) error {
		if err := s.repo.Create(ctx, p); err != nil {
			return err
		}
		got, err := s.repo.Get(ctx, p.ID, customerID)
		if err != nil {
			return notFound(err)
		}
		out = got
		if s.audit != nil {
			if err := s.audit.Log(ctx, audit.Entry{Action: "project.created", EntityType: "project",
				EntityID: p.ID, Changes: map[string]any{"customer_id": customerID, "revision": out.Revision}}); err != nil {
				return err
			}
		}
		return s.record(ctx, out, EventCreated, nil)
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// Update replaces the project's name and status on the client's revision.
func (s *Service) Update(ctx context.Context, projectID, customerID uuid.UUID, d *Draft, pre Precondition) (*Project, error) {
	if pre.IfMatch == "" && pre.Revision == nil {
		return nil, httpx.PreconditionRequired("this write needs If-Match or a body revision")
	}
	var out *Project
	err := s.inTx(ctx, func(ctx context.Context) error {
		if err := s.repo.Lock(ctx, projectID, customerID); err != nil {
			return notFound(err)
		}
		cur, err := s.repo.Get(ctx, projectID, customerID)
		if err != nil {
			return notFound(err)
		}
		if err := httpx.CheckRevision(cur.Revision, pre.IfMatch, pre.Revision); err != nil {
			return err
		}
		next := *cur
		changed := []string{}
		if d.Name != nil && *d.Name != cur.Name {
			next.Name = *d.Name
			changed = append(changed, "name")
		}
		if d.Status != "" && d.Status != cur.Status {
			next.Status = d.Status
			changed = append(changed, "status")
		}
		// A body that names nothing to change is not a write: the row answers
		// with its current revision, and no audit row or event is recorded.
		if len(changed) == 0 {
			out = cur
			return nil
		}
		if err := s.repo.Update(ctx, &next); err != nil {
			return err
		}
		if out, err = s.repo.Get(ctx, projectID, customerID); err != nil {
			return notFound(err)
		}
		if s.audit != nil {
			if err := s.audit.Log(ctx, audit.Entry{Action: "project.updated", EntityType: "project",
				EntityID: projectID, Changes: map[string]any{"customer_id": customerID,
					"revision": out.Revision, "changed": changed}}); err != nil {
				return err
			}
		}
		return s.record(ctx, out, EventUpdated, map[string]any{"changed": changed})
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// record writes the module's event as the transaction's last statement.
func (s *Service) record(ctx context.Context, p *Project, eventType string, extra map[string]any) error {
	if s.events == nil {
		return nil
	}
	data := map[string]any{
		"project_id":  p.ID,
		"customer_id": p.CustomerID,
		"status":      string(p.Status),
		"revision":    p.Revision,
	}
	for k, v := range extra {
		data[k] = v
	}
	raw, err := json.Marshal(data)
	if err != nil {
		return err
	}
	return s.events.Write(ctx, outbox.Event{
		Type: eventType, EntityType: "project", EntityID: p.ID, Data: raw,
	})
}

func notFound(err error) error {
	if errors.Is(err, ErrNotFound) {
		return httpx.NotFound(ErrNotFound.Error())
	}
	return err
}
