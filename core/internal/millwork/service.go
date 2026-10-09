// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package millwork

import (
	"context"
	"encoding/json"
	"errors"

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

// EventCreated is the module's one event: the catalog's only mutation is the
// create.
const EventCreated = "millwork_option.created"

// Service is the millwork catalog's behaviour: the create is one transaction
// that writes the row, its audit row and its event, in that order, with the
// event last (ADR 0003 section 2).
type Service struct {
	repo   Repository
	events EventRecorder
	tx     TxRunner
	audit  AuditLogger
}

func NewService(repo Repository) *Service { return &Service{repo: repo} }

// WithOutbox wires the event writes.
func (s *Service) WithOutbox(events EventRecorder) *Service { s.events = events; return s }

// WithTxRunner wires the transaction wrapper the create uses.
func (s *Service) WithTxRunner(tx TxRunner) *Service { s.tx = tx; return s }

// WithAudit wires the audit rows.
func (s *Service) WithAudit(a AuditLogger) *Service { s.audit = a; return s }

func (s *Service) inTx(ctx context.Context, fn func(ctx context.Context) error) error {
	if s.tx == nil {
		return fn(ctx)
	}
	return s.tx.RunInTx(ctx, fn)
}

func (s *Service) List(ctx context.Context, f ListFilter, wantTotal bool) ([]Option, bool, *int64, error) {
	return s.repo.List(ctx, f, wantTotal)
}

func (s *Service) Get(ctx context.Context, id uuid.UUID) (*Option, error) {
	o, err := s.repo.Get(ctx, id)
	if err != nil {
		return nil, notFound(err)
	}
	return o, nil
}

// Create adds one option to the catalog.
func (s *Service) Create(ctx context.Context, d *Draft) (*Option, error) {
	o := &Option{ID: uuid.New(), Category: d.Category, Name: d.Name,
		PriceAdjustmentCents: d.PriceAdjustmentCents, Attributes: d.Attributes}
	var out *Option
	err := s.inTx(ctx, func(ctx context.Context) error {
		if err := s.repo.Create(ctx, o); err != nil {
			return err
		}
		got, err := s.repo.Get(ctx, o.ID)
		if err != nil {
			return notFound(err)
		}
		out = got
		if s.audit != nil {
			if err := s.audit.Log(ctx, audit.Entry{Action: "millwork_option.created", EntityType: "millwork_option",
				EntityID: o.ID, Changes: map[string]any{"category": d.Category, "revision": out.Revision}}); err != nil {
				return err
			}
		}
		return s.record(ctx, out)
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// record writes the module's event as the transaction's last statement.
func (s *Service) record(ctx context.Context, o *Option) error {
	if s.events == nil {
		return nil
	}
	data, err := json.Marshal(map[string]any{
		"category":               o.Category,
		"price_adjustment_cents": o.PriceAdjustmentCents,
		"revision":               o.Revision,
	})
	if err != nil {
		return err
	}
	return s.events.Write(ctx, outbox.Event{
		Type: EventCreated, EntityType: "millwork_option", EntityID: o.ID, Data: data,
	})
}

func notFound(err error) error {
	if errors.Is(err, ErrNotFound) {
		return httpx.NotFound(ErrNotFound.Error())
	}
	return err
}
