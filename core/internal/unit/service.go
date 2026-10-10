// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package unit

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"time"

	"github.com/gablelbm/gable/internal/platform/httpx"
	"github.com/gablelbm/gable/internal/units"
	"github.com/gablelbm/gable/pkg/outbox"
	"github.com/google/uuid"
)

// EventRecorder writes a domain event into the transactional outbox; the
// service calls it as the LAST statement of the mutation's transaction.
type EventRecorder interface {
	Write(ctx context.Context, ev outbox.Event) error
}

// TxRunner runs fn inside one transaction, joining the caller's when ctx
// already carries one.
type TxRunner interface {
	RunInTx(ctx context.Context, fn func(ctx context.Context) error) error
}

// Event types the module writes to the outbox (ADR 0006 2.3).
const (
	EventUnitCreated = "unit.created"
	EventUnitUpdated = "unit.updated"
)

// Service is the catalogue's business logic: creates and edits, the
// immutability rules of section 2.1 (a system unit's code, dimension and
// standard size never change; a dealer unit's dimension and standard size
// are immutable once any product unit set row or line names the unit), and
// the events, written last.
type Service struct {
	repo   Repository
	events EventRecorder
	tx     TxRunner
	logger *slog.Logger
	now    func() time.Time
}

func NewService(repo Repository) *Service {
	return &Service{repo: repo, logger: slog.Default(), now: time.Now}
}

func (s *Service) WithOutbox(events EventRecorder) *Service { s.events = events; return s }

func (s *Service) WithTxRunner(tx TxRunner) *Service { s.tx = tx; return s }

func (s *Service) inTx(ctx context.Context, fn func(ctx context.Context) error) error {
	if s.tx == nil {
		return fn(ctx)
	}
	return s.tx.RunInTx(ctx, fn)
}

func (s *Service) record(ctx context.Context, code string, revision int64, eventType string) error {
	if s.events == nil {
		return nil
	}
	raw, err := json.Marshal(map[string]any{"code": code, "revision": revision})
	if err != nil {
		return err
	}
	return s.events.Write(ctx, outbox.Event{
		Type: eventType, EntityType: "unit",
		EntityID: uuid.NewSHA1(unitNamespace, []byte(code)), Data: raw,
	})
}

func notFound(err error) error {
	if errors.Is(err, ErrNotFound) {
		return httpx.NotFound("no such unit")
	}
	return err
}

// Create stores a new dealer unit. Seeded units exist already; a code one
// of them holds is a 409 duplicate.
func (s *Service) Create(ctx context.Context, draft *Draft) (*Unit, error) {
	var out *Row
	err := s.inTx(ctx, func(ctx context.Context) error {
		row := &Row{Code: draft.Code, Name: *draft.Name, Dimension: *draft.Dimension,
			StdUnitQty: draft.StdUnitQty, StdRefQty: draft.StdRefQty, IsActive: true}
		if err := s.repo.CreateUnit(ctx, row); err != nil {
			if errors.Is(err, ErrDuplicate) {
				return &httpx.Error{Status: 409, Code: httpx.CodeDuplicate,
					Message: "a unit with this code already exists",
					Details: []httpx.FieldError{{Field: "code", Message: "already exists"}}}
			}
			return err
		}
		stored, err := s.repo.GetUnit(ctx, draft.Code)
		if err != nil {
			return err
		}
		out = stored
		return s.record(ctx, stored.Code, stored.Revision, EventUnitCreated)
	})
	if err != nil {
		return nil, err
	}
	return ViewOf(out), nil
}

// Update applies a unit's editable fields on the client's revision: name
// and is_active always; the dimension and the standard size only while the
// unit is neither a system row nor named by any product unit set row or
// line. A change the PUT cannot apply is a 400 naming the field.
func (s *Service) Update(ctx context.Context, code string, draft *Draft, ifMatch string, revision *int64) (*Unit, error) {
	var out *Row
	err := s.inTx(ctx, func(ctx context.Context) error {
		if err := s.repo.LockUnit(ctx, code); err != nil {
			return notFound(err)
		}
		cur, err := s.repo.GetUnit(ctx, code)
		if err != nil {
			return notFound(err)
		}
		if err := httpx.CheckRevision(cur.Revision, ifMatch, revision); err != nil {
			return err
		}
		row := &Row{Code: cur.Code, Name: cur.Name, Dimension: cur.Dimension,
			StdUnitQty: cur.StdUnitQty, StdRefQty: cur.StdRefQty,
			IsSystem: cur.IsSystem, IsActive: cur.IsActive}
		if draft.Name != nil {
			row.Name = *draft.Name
		}
		if draft.IsActive != nil {
			row.IsActive = *draft.IsActive
		}
		immutable := func(field, message string) error {
			return httpx.BadRequest("one or more fields failed validation",
				httpx.FieldError{Field: field, Message: message})
		}
		if draft.Dimension != nil && *draft.Dimension != cur.Dimension {
			if cur.IsSystem {
				return immutable("dimension", "a system unit's dimension cannot change")
			}
			referenced, err := s.repo.UnitReferenced(ctx, code)
			if err != nil {
				return err
			}
			if referenced {
				return immutable("dimension", "the dimension is immutable once any product unit set or line names the unit")
			}
			row.Dimension = *draft.Dimension
		}
		if draft.HasStdSize {
			if cur.IsSystem {
				return immutable("std_unit_qty", "a system unit's standard size cannot change")
			}
			if !draft.SameStdSize(cur.StdUnitQty, cur.StdRefQty) {
				referenced, err := s.repo.UnitReferenced(ctx, code)
				if err != nil {
					return err
				}
				if referenced {
					return immutable("std_unit_qty",
						"the standard size is immutable once any product unit set or line names the unit")
				}
				// A standard size is stored canonical (R2).
				pair, err := units.StandardSizePair(*draft.StdUnitQty, *draft.StdRefQty)
				if err != nil {
					return immutable("std_unit_qty", err.Error())
				}
				row.StdUnitQty, row.StdRefQty = &pair.A, &pair.B
			}
		}
		if err := s.repo.UpdateUnit(ctx, row, cur.Revision); err != nil {
			var stale *StaleRevisionError
			switch {
			case errors.As(err, &stale):
				return httpx.StaleRevision("the unit was changed after this revision was read; reload and retry")
			default:
				return notFound(err)
			}
		}
		stored, err := s.repo.GetUnit(ctx, code)
		if err != nil {
			return err
		}
		out = stored
		return s.record(ctx, stored.Code, stored.Revision, EventUnitUpdated)
	})
	if err != nil {
		return nil, err
	}
	return ViewOf(out), nil
}

// GetUnit reads one unit.
func (s *Service) GetUnit(ctx context.Context, code string) (*Unit, error) {
	row, err := s.repo.GetUnit(ctx, code)
	if err != nil {
		return nil, notFound(err)
	}
	return ViewOf(row), nil
}

// ListFilter is the list's filters and keyset position (ADR 0006 2.3:
// ordering units.code, filters dimension and is_active).
type ListFilter struct {
	Dimension *Dimension
	IsActive  *bool
	After     *string
	Limit     int
}

// ListUnits returns one page: up to limit rows, and whether more follow.
func (s *Service) ListUnits(ctx context.Context, f ListFilter) ([]Unit, bool, error) {
	rows, err := s.repo.ListUnits(ctx, f.Dimension, f.IsActive, f.After, f.Limit+1)
	if err != nil {
		return nil, false, err
	}
	more := len(rows) > f.Limit
	if more {
		rows = rows[:f.Limit]
	}
	out := make([]Unit, len(rows))
	for i := range rows {
		out[i] = *ViewOf(&rows[i])
	}
	return out, more, nil
}

// CountUnits is the include=total count.
func (s *Service) CountUnits(ctx context.Context, f ListFilter) (int64, error) {
	return s.repo.CountUnits(ctx, f.Dimension, f.IsActive)
}

// Draft is a validated create or update body. Dimension is nil when the
// update does not name it; a create must.
type Draft struct {
	Code       string
	Name       *string
	Dimension  *Dimension
	StdUnitQty *httpx.Quantity
	StdRefQty  *httpx.Quantity
	HasStdSize bool
	IsActive   *bool
}

// SameStdSize reports whether the draft's standard size equals the stored
// one.
func (d *Draft) SameStdSize(curUnit, curRef *httpx.Quantity) bool {
	if !d.HasStdSize {
		return true
	}
	if curUnit == nil || d.StdUnitQty == nil {
		return false
	}
	return *d.StdUnitQty == *curUnit && *d.StdRefQty == *curRef
}

// unitNamespace is the namespace the catalogue's code key maps into the
// outbox's UUID entity id: every event of one unit carries the same id.
var unitNamespace = uuid.NewSHA1(uuid.NameSpaceURL, []byte("https://gable.dev/units"))
