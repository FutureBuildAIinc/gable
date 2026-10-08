// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package location

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/gablelbm/gable/internal/platform/httpx"
	"github.com/gablelbm/gable/pkg/audit"
	"github.com/gablelbm/gable/pkg/outbox"
	"github.com/google/uuid"
)

// EventRecorder writes a domain event into the transactional outbox; the
// service calls it as the LAST statement of the mutation's transaction
// (ADR 0003 section 2). *outbox.Writer satisfies it.
type EventRecorder interface {
	Write(ctx context.Context, ev outbox.Event) error
}

// TxRunner runs fn inside one transaction, joining the caller's when ctx
// already carries one. *database.DB satisfies it.
type TxRunner interface {
	RunInTx(ctx context.Context, fn func(ctx context.Context) error) error
}

// AuditLogger writes an audit row for a location write; *audit.Logger
// satisfies it.
type AuditLogger interface {
	Log(ctx context.Context, entry audit.Entry) error
}

// Event types the module writes to the outbox.
const (
	EventLocationCreated  = "location.created"
	EventLocationUpdated  = "location.updated"
	EventLocationArchived = "location.archived"
)

type Service struct {
	repo   Repository
	events EventRecorder // optional; nil records nothing (unit tests)
	tx     TxRunner      // optional; nil runs each write unwrapped (unit tests)
	audits AuditLogger   // optional; nil writes no audit rows (unit tests)
}

func NewService(repo Repository) *Service {
	return &Service{repo: repo}
}

// WithOutbox wires the recorder of the location events.
func (s *Service) WithOutbox(events EventRecorder) *Service {
	s.events = events
	return s
}

// WithTxRunner wires the transaction wrapper every write uses, so the write,
// its revision move, its audit row and its event are one transactional fact.
func (s *Service) WithTxRunner(tx TxRunner) *Service {
	s.tx = tx
	return s
}

// WithAudit wires the audit rows of the location writes.
func (s *Service) WithAudit(a AuditLogger) *Service {
	s.audits = a
	return s
}

// inTx runs fn in one transaction when a runner is wired.
func (s *Service) inTx(ctx context.Context, fn func(ctx context.Context) error) error {
	if s.tx == nil {
		return fn(ctx)
	}
	return s.tx.RunInTx(ctx, fn)
}

// record writes the location write's audit row and its outbox event, inside
// the caller's transaction; the event is the transaction's last statement
// (ADR 0003 section 2). A failed audit write fails the mutation (the
// recipe's rule).
func (s *Service) record(ctx context.Context, loc *Location, action, eventType string) error {
	typeWire := strings.ToLower(string(loc.Type))
	if s.audits != nil {
		changes := map[string]any{"code": loc.Code, "type": typeWire, "revision": loc.Revision, "active": loc.Active}
		if err := s.audits.Log(ctx, audit.Entry{
			Action: action, EntityType: "location", EntityID: loc.ID, Changes: changes,
		}); err != nil {
			return err
		}
	}
	if s.events == nil {
		return nil
	}
	raw, err := json.Marshal(map[string]any{"code": loc.Code, "type": typeWire, "revision": loc.Revision})
	if err != nil {
		return err
	}
	return s.events.Write(ctx, outbox.Event{
		Type: eventType, EntityType: "location", EntityID: loc.ID, Data: raw,
	})
}

// CreateLocation validates and persists a new location. Branch rows must be
// root-level (no parent) and carry a Name; physical sub-locations must have a
// parent so the trigger can derive their branch_id.
func (s *Service) CreateLocation(ctx context.Context, loc *Location) error {
	v := &httpx.Validator{}
	v.Required("code", loc.Code)
	if loc.Type == "" {
		v.Check(false, "type", "is required")
	} else if _, ok := ParseLocationType(lower(string(loc.Type))); !ok {
		v.Check(false, "type", "must be one of: branch, zone, aisle, rack, shelf, bin, yard")
	}
	if err := v.Err(); err != nil {
		return err
	}

	if loc.Type == LocTypeBranch {
		if loc.ParentID != nil {
			return httpx.BadRequest("one or more fields failed validation",
				httpx.FieldError{Field: "parent_id", Message: "a branch is root-level; send no parent"})
		}
		if loc.Name == nil || *loc.Name == "" {
			return httpx.BadRequest("one or more fields failed validation",
				httpx.FieldError{Field: "name", Message: "is required on a branch"})
		}
		if loc.Path == "" {
			loc.Path = *loc.Name
		}
	} else if loc.ParentID == nil {
		return httpx.BadRequest("one or more fields failed validation",
			httpx.FieldError{Field: "parent_id", Message: "is required on a non-branch location"})
	}

	// loc.Active is taken as given. The "default to active unless explicitly
	// false" rule cannot be expressed here: Location.Active is a plain bool
	// whose zero value is indistinguishable from an explicit false, so forcing
	// it true here would make an inactive location impossible to create (an
	// importer bringing in a decommissioned yard would silently reactivate it).
	// The default is applied at the HTTP boundary instead, where
	// createLocationRequest.Active is a *bool — see handler.go.

	// The create, its audit row and its event are one transactional fact.
	return s.inTx(ctx, func(ctx context.Context) error {
		if err := s.repo.CreateLocation(ctx, loc); err != nil {
			return err
		}
		return s.record(ctx, loc, EventLocationCreated, EventLocationCreated)
	})
}

func lower(s string) string {
	if s == "" {
		return s
	}
	b := []byte(s)
	for i := range b {
		if b[i] >= 'A' && b[i] <= 'Z' {
			b[i] += 'a' - 'A'
		}
	}
	return string(b)
}

// UpdateLocation persists edits to a location. Type and parent_id are not
// mutable here; create a new row instead. The revision precondition is part
// of the write (ADR 0001 section 11).
func (s *Service) UpdateLocation(ctx context.Context, loc *Location, revision int64) error {
	if loc.ID == uuid.Nil {
		return httpx.BadRequest("one or more fields failed validation",
			httpx.FieldError{Field: "id", Message: "is required"})
	}
	v := &httpx.Validator{}
	v.Required("code", loc.Code)
	if err := v.Err(); err != nil {
		return err
	}
	return s.inTx(ctx, func(ctx context.Context) error {
		if err := resolveWrite(s.repo.UpdateLocation(ctx, loc, revision)); err != nil {
			return err
		}
		return s.record(ctx, loc, EventLocationUpdated, EventLocationUpdated)
	})
}

// DeleteLocation archives the location (active=false) with its audit row and
// its event in the same transaction.
func (s *Service) DeleteLocation(ctx context.Context, id uuid.UUID, revision int64) error {
	return s.inTx(ctx, func(ctx context.Context) error {
		old, err := s.repo.GetLocation(ctx, id)
		if err != nil {
			return resolveWrite(err)
		}
		if err := resolveWrite(s.repo.DeleteLocation(ctx, id, revision)); err != nil {
			return err
		}
		old.Active = false
		old.Revision++
		return s.record(ctx, old, EventLocationArchived, EventLocationArchived)
	})
}

// resolveWrite maps the repository's two write refusals to the boundary
// errors: a missing row is 404, a stale revision is 409.
func resolveWrite(err error) error {
	switch {
	case err == nil:
		return nil
	case errors.Is(err, ErrNotFound):
		return httpx.NotFound("no such location")
	case errors.Is(err, ErrStaleRevision):
		return httpx.StaleRevision("the location was changed after this revision was read; reload and retry")
	default:
		return err
	}
}

func (s *Service) GetLocation(ctx context.Context, id uuid.UUID) (*Location, error) {
	return s.repo.GetLocation(ctx, id)
}

// ListLocationsPage is the locations list's keyset page under the caller's
// branch scope; limit+1 rows are read so the handler knows whether another
// page exists.
func (s *Service) ListLocationsPage(ctx context.Context, scope ListScope, after *time.Time, afterID *uuid.UUID, limit int) ([]Location, bool, error) {
	rows, err := s.repo.ListLocationsPage(ctx, scope, after, afterID, limit+1)
	if err != nil {
		return nil, false, err
	}
	more := len(rows) > limit
	if more {
		rows = rows[:limit]
	}
	return rows, more, nil
}

// ListBranchesPage is the branches list's keyset page.
func (s *Service) ListBranchesPage(ctx context.Context, includeInactive bool, after *time.Time, afterID *uuid.UUID, limit int) ([]Location, bool, error) {
	rows, err := s.repo.ListBranchesPage(ctx, includeInactive, after, afterID, limit+1)
	if err != nil {
		return nil, false, err
	}
	more := len(rows) > limit
	if more {
		rows = rows[:limit]
	}
	return rows, more, nil
}

func (s *Service) CountBranches(ctx context.Context, includeInactive bool) (int64, error) {
	return s.repo.CountBranches(ctx, includeInactive)
}

func (s *Service) CountLocations(ctx context.Context, scope ListScope) (int64, error) {
	return s.repo.CountLocations(ctx, scope)
}

func (s *Service) GetBranchTree(ctx context.Context, branchID uuid.UUID) ([]Location, error) {
	return s.repo.GetBranchTree(ctx, branchID)
}

func (s *Service) IsBranch(ctx context.Context, id uuid.UUID) (bool, error) {
	return s.repo.IsBranch(ctx, id)
}
