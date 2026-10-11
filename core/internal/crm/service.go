// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package crm

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

// Event types the module writes to the outbox. An activity has no lifecycle,
// so the vocabulary is the three facts a logged activity has.
const (
	EventCreated = "activity.created"
	EventUpdated = "activity.updated"
	EventDeleted = "activity.deleted"
)

// Precondition is the client's revision: the If-Match header and the body's
// revision, both optional here; the service refuses a write with neither
// (428) and one whose revision is behind (409).
type Precondition struct {
	IfMatch  string
	Revision *int64
}

// Service is the activity module's behaviour: every mutation is one
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

func (s *Service) List(ctx context.Context, customerID uuid.UUID, f ListFilter, wantTotal bool) ([]Activity, bool, *int64, error) {
	return s.repo.List(ctx, customerID, f, wantTotal)
}

func (s *Service) Get(ctx context.Context, id uuid.UUID) (*Activity, error) {
	a, err := s.repo.Get(ctx, id)
	if err != nil {
		return nil, notFound(err)
	}
	return a, nil
}

// Create logs one activity for the customer the path names. The customer is
// checked behind the caller's branch wall first, so a create on a customer
// the caller cannot see is the same 404 as reading it.
func (s *Service) Create(ctx context.Context, customerID uuid.UUID, d *Draft) (*Activity, error) {
	var out *Activity
	err := s.inTx(ctx, func(ctx context.Context) error {
		visible, err := s.repo.CustomerVisible(ctx, customerID)
		if err != nil {
			return err
		}
		if !visible {
			return httpx.NotFound("customer not found")
		}
		now := httpx.TimestampOf(s.now().UTC())
		a := &Activity{
			ID: uuid.New(), CustomerID: customerID, ContactID: d.ContactID,
			ActivityType: d.ActivityType, Description: d.Description, LoggedBy: d.LoggedBy,
			ActivityDate: now, CreatedAt: now, UpdatedAt: now,
		}
		if d.ActivityDate != nil {
			a.ActivityDate = *d.ActivityDate
		}
		if err := s.repo.Create(ctx, a); err != nil {
			return err
		}
		if out, err = s.repo.Get(ctx, a.ID); err != nil {
			return notFound(err)
		}
		if s.audit != nil {
			if err := s.audit.Log(ctx, audit.Entry{Action: "activity.created", EntityType: "activity",
				EntityID: a.ID, Changes: map[string]any{"customer_id": customerID, "revision": out.Revision}}); err != nil {
				return err
			}
		}
		return s.record(ctx, out, EventCreated)
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// Update replaces an activity's mutable fields on the client's revision.
func (s *Service) Update(ctx context.Context, id uuid.UUID, d *Draft, pre Precondition) (*Activity, error) {
	if pre.IfMatch == "" && pre.Revision == nil {
		return nil, httpx.PreconditionRequired("this write needs If-Match or a body revision")
	}
	var out *Activity
	err := s.inTx(ctx, func(ctx context.Context) error {
		if err := s.repo.Lock(ctx, id); err != nil {
			return notFound(err)
		}
		cur, err := s.repo.Get(ctx, id)
		if err != nil {
			return notFound(err)
		}
		if err := httpx.CheckRevision(cur.Revision, pre.IfMatch, pre.Revision); err != nil {
			return err
		}
		next := *cur
		next.ContactID, next.ActivityType = d.ContactID, d.ActivityType
		next.Description, next.LoggedBy = d.Description, d.LoggedBy
		next.ActivityDate = cur.ActivityDate
		if d.ActivityDate != nil {
			next.ActivityDate = *d.ActivityDate
		}
		if err := s.repo.Update(ctx, &next); err != nil {
			return err
		}
		if out, err = s.repo.Get(ctx, id); err != nil {
			return notFound(err)
		}
		if s.audit != nil {
			if err := s.audit.Log(ctx, audit.Entry{Action: "activity.updated", EntityType: "activity",
				EntityID: id, Changes: map[string]any{"customer_id": cur.CustomerID, "revision": out.Revision}}); err != nil {
				return err
			}
		}
		return s.record(ctx, out, EventUpdated)
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// Delete removes the activity on the client's revision. A DELETE carries no
// body, so the revision travels as If-Match alone.
func (s *Service) Delete(ctx context.Context, id uuid.UUID, pre Precondition) error {
	if pre.IfMatch == "" && pre.Revision == nil {
		return httpx.PreconditionRequired("this write needs If-Match")
	}
	return s.inTx(ctx, func(ctx context.Context) error {
		if err := s.repo.Lock(ctx, id); err != nil {
			return notFound(err)
		}
		cur, err := s.repo.Get(ctx, id)
		if err != nil {
			return notFound(err)
		}
		if err := httpx.CheckRevision(cur.Revision, pre.IfMatch, pre.Revision); err != nil {
			return err
		}
		if err := s.repo.Delete(ctx, id); err != nil {
			return err
		}
		if s.audit != nil {
			if err := s.audit.Log(ctx, audit.Entry{Action: "activity.deleted", EntityType: "activity",
				EntityID: id, Changes: map[string]any{"customer_id": cur.CustomerID, "revision": cur.Revision}}); err != nil {
				return err
			}
		}
		return s.record(ctx, cur, EventDeleted)
	})
}

// record writes the module's event as the transaction's last statement.
func (s *Service) record(ctx context.Context, a *Activity, eventType string) error {
	if s.events == nil {
		return nil
	}
	data, err := json.Marshal(map[string]any{
		"customer_id":   a.CustomerID,
		"activity_type": wireNames[a.ActivityType],
		"revision":      a.Revision,
	})
	if err != nil {
		return err
	}
	return s.events.Write(ctx, outbox.Event{
		Type: eventType, EntityType: "activity", EntityID: a.ID, Data: data,
	})
}

func notFound(err error) error {
	if errors.Is(err, ErrNotFound) {
		return httpx.NotFound(ErrNotFound.Error())
	}
	return err
}
