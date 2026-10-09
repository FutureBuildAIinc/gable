// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package staff

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/gablelbm/gable/internal/platform/httpx"
	"github.com/gablelbm/gable/pkg/audit"
	"github.com/gablelbm/gable/pkg/outbox"
	"github.com/google/uuid"
)

// store is the data dependency of Service. It exists so the service can be
// exercised without Postgres; the production implementation is *Repository.
type store interface {
	List(ctx context.Context, f ListFilter) ([]Staff, error)
	Count(ctx context.Context, f ListFilter) (int64, error)
	Get(ctx context.Context, id uuid.UUID) (*Staff, error)
	LockStaff(ctx context.Context, id uuid.UUID) error
	Create(ctx context.Context, in *ParsedCreate) (*Staff, error)
	Update(ctx context.Context, id uuid.UUID, in *ParsedUpdate) (*Staff, error)
	BumpStaffRevision(ctx context.Context, id uuid.UUID) error
	GrantModule(ctx context.Context, staffID uuid.UUID, moduleID, grantedBy string) (bool, error)
	RevokeModule(ctx context.Context, staffID uuid.UUID, moduleID string) (bool, error)
	EnabledModules(ctx context.Context) (map[string]bool, error)
	SetModuleEnabled(ctx context.Context, moduleID string, enabled bool) (bool, error)
	ReadModuleRevision(ctx context.Context, moduleID string) (int64, error)
	LockModuleRevision(ctx context.Context, moduleID string) (int64, error)
	BumpModuleRevision(ctx context.Context, moduleID string, to int64) error
}

// auditSink is the audit dependency, narrowed to the one method used so tests
// can assert that a privileged grant or revoke was recorded without standing
// up the audit_log table. *audit.Logger satisfies it.
type auditSink interface {
	Log(ctx context.Context, entry audit.Entry) error
}

// EventRecorder writes a domain event into the transactional outbox, as the
// LAST statement of the mutation's transaction (ADR 0003 section 2).
// *outbox.Writer satisfies it.
type EventRecorder interface {
	Write(ctx context.Context, ev outbox.Event) error
}

// TxRunner runs fn inside one transaction, joining the caller's when ctx
// already carries one. *database.DB satisfies it.
type TxRunner interface {
	RunInTx(ctx context.Context, fn func(ctx context.Context) error) error
}

// Precondition is the client's revision for a staff write: the If-Match
// header and/or the body's revision (ADR 0001 section 11).
type Precondition struct {
	IfMatch  string
	Revision *int64
}

func (p Precondition) missing() bool { return p.IfMatch == "" && p.Revision == nil }

func (p Precondition) check(current int64) error {
	return httpx.CheckRevision(current, p.IfMatch, p.Revision)
}

// notFound maps the repository's sentinel to the wire's 404.
func notFound(err error) error {
	if errors.Is(err, ErrNotFound) {
		return httpx.NotFound(ErrNotFound.Error())
	}
	return err
}

// Service holds staff-management business logic. Every mutation runs in one
// transaction that carries its audit row and its outbox event.
type Service struct {
	repo     store
	auditLog auditSink
	events   EventRecorder
	tx       TxRunner
}

func NewService(repo store) *Service {
	return &Service{repo: repo}
}

// WithAuditLog attaches an audit logger so privileged operations are
// recorded. A nil logger is ignored rather than stored: a typed-nil
// *audit.Logger boxed into the interface would be non-nil and panic on first
// use.
func (s *Service) WithAuditLog(l auditSink) *Service {
	if l == nil {
		return s
	}
	s.auditLog = l
	return s
}

// WithOutbox wires the recorder of staff and module events.
func (s *Service) WithOutbox(events EventRecorder) *Service {
	s.events = events
	return s
}

// WithTxRunner wires the transaction wrapper every write uses.
func (s *Service) WithTxRunner(tx TxRunner) *Service {
	s.tx = tx
	return s
}

func (s *Service) inTx(ctx context.Context, fn func(ctx context.Context) error) error {
	if s.tx == nil {
		return fn(ctx)
	}
	return s.tx.RunInTx(ctx, fn)
}

func (s *Service) audit(ctx context.Context, action string, entityID uuid.UUID, entityType string, changes map[string]any) error {
	if s.auditLog == nil {
		return nil
	}
	return s.auditLog.Log(ctx, audit.Entry{
		Action: action, EntityType: entityType, EntityID: entityID, Changes: changes,
	})
}

// record writes one event into the outbox through the transaction's
// executor.
func (s *Service) record(ctx context.Context, eventType, entityType string, entityID uuid.UUID, data map[string]any) error {
	if s.events == nil {
		return nil
	}
	raw, err := json.Marshal(data)
	if err != nil {
		return err
	}
	return s.events.Write(ctx, outbox.Event{
		Type: eventType, EntityType: entityType, EntityID: entityID, Data: raw,
	})
}

func (s *Service) ListStaff(ctx context.Context, f ListFilter, wantTotal bool) (items []Staff, hasMore bool, total *int64, err error) {
	limit := f.Limit
	f.Limit = limit + 1
	rows, err := s.repo.List(ctx, f)
	if err != nil {
		return nil, false, nil, err
	}
	if len(rows) > limit {
		rows, hasMore = rows[:limit], true
	}
	if wantTotal {
		n, err := s.repo.Count(ctx, ListFilter{Active: f.Active})
		if err != nil {
			return nil, false, nil, err
		}
		total = &n
	}
	return rows, hasMore, total, nil
}

func (s *Service) GetStaff(ctx context.Context, id uuid.UUID) (*Staff, error) {
	st, err := s.repo.Get(ctx, id)
	return st, notFound(err)
}

// CreateStaff inserts a roster row at revision 1, with its audit row and
// staff.created as the transaction's last statements.
func (s *Service) CreateStaff(ctx context.Context, in *ParsedCreate) (*Staff, error) {
	var out *Staff
	err := s.inTx(ctx, func(ctx context.Context) error {
		st, err := s.repo.Create(ctx, in)
		if err != nil {
			return err
		}
		if err := s.audit(ctx, "staff.created", st.ID, "staff", map[string]any{
			"email": st.Email, "role": st.Role, "active": st.Active,
		}); err != nil {
			return err
		}
		if err := s.record(ctx, "staff.created", "staff", st.ID, map[string]any{
			"email": st.Email, "revision": st.Revision,
		}); err != nil {
			return err
		}
		out = st
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// UpdateStaff applies the non-nil fields on the client's revision: the row
// is locked, the revision checked, the write and the revision move are one
// database act, and the audit row and staff.updated ride in the same
// transaction. A body with no field set is an idempotent no-op: the current
// document comes back and nothing is written, the grants' rule.
func (s *Service) UpdateStaff(ctx context.Context, id uuid.UUID, in *ParsedUpdate, pre Precondition) (*Staff, error) {
	if pre.missing() {
		return nil, httpx.PreconditionRequired("this write needs If-Match or a body revision")
	}
	var out *Staff
	err := s.inTx(ctx, func(ctx context.Context) error {
		if err := s.repo.LockStaff(ctx, id); err != nil {
			return notFound(err)
		}
		cur, err := s.repo.Get(ctx, id)
		if err != nil {
			return notFound(err)
		}
		if err := pre.check(cur.Revision); err != nil {
			return err
		}
		if in.empty() {
			out = cur
			return nil
		}
		st, err := s.repo.Update(ctx, id, in)
		if err != nil {
			return err
		}
		if err := s.audit(ctx, "staff.updated", id, "staff", map[string]any{
			"revision": st.Revision, "fields": updatedFields(cur, st),
		}); err != nil {
			return err
		}
		out = st
		return s.record(ctx, "staff.updated", "staff", id, map[string]any{
			"email": st.Email, "revision": st.Revision,
		})
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

func updatedFields(cur, next *Staff) []string {
	var fields []string
	if cur.Email != next.Email {
		fields = append(fields, "email")
	}
	if cur.FullName != next.FullName {
		fields = append(fields, "full_name")
	}
	if staffNoOf(cur) != staffNoOf(next) {
		fields = append(fields, "staff_no")
	}
	if cur.Role != next.Role {
		fields = append(fields, "role")
	}
	if cur.Active != next.Active {
		fields = append(fields, "active")
	}
	return fields
}

func staffNoOf(s *Staff) string {
	if s.StaffNo == nil {
		return ""
	}
	return *s.StaffNo
}

// GrantModule grants a module to a staff member on the staff document's
// revision. The modules list is part of the staff document, so a grant that
// adds one moves the revision and takes the same precondition; an
// idempotent re-grant changes nothing and writes nothing.
func (s *Service) GrantModule(ctx context.Context, staffID uuid.UUID, moduleID, grantedBy string, pre Precondition) (*Staff, error) {
	if pre.missing() {
		return nil, httpx.PreconditionRequired("this write needs If-Match or a body revision")
	}
	var out *Staff
	err := s.inTx(ctx, func(ctx context.Context) error {
		if err := s.repo.LockStaff(ctx, staffID); err != nil {
			return notFound(err)
		}
		cur, err := s.repo.Get(ctx, staffID)
		if err != nil {
			return notFound(err)
		}
		if err := pre.check(cur.Revision); err != nil {
			return err
		}
		added, err := s.repo.GrantModule(ctx, staffID, moduleID, grantedBy)
		if err != nil {
			return err
		}
		if !added {
			out = cur
			return nil
		}
		if err := s.repo.BumpStaffRevision(ctx, staffID); err != nil {
			return err
		}
		// Re-read for the response before the sinks, so the event stays the
		// transaction's LAST statement.
		if out, err = s.repo.Get(ctx, staffID); err != nil {
			return err
		}
		if err := s.audit(ctx, "staff.module_granted", staffID, "staff", map[string]any{
			"module_id": moduleID, "granted_by": grantedBy, "revision": cur.Revision + 1,
		}); err != nil {
			return err
		}
		return s.record(ctx, "staff.module_granted", "staff", staffID, map[string]any{
			"module_id": moduleID, "revision": cur.Revision + 1,
		})
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// RevokeModule removes a module grant, the grant route's mirror: it moves
// the revision only when a grant was actually removed.
func (s *Service) RevokeModule(ctx context.Context, staffID uuid.UUID, moduleID string, pre Precondition) (*Staff, error) {
	if pre.missing() {
		return nil, httpx.PreconditionRequired("this write needs If-Match or a body revision")
	}
	var out *Staff
	err := s.inTx(ctx, func(ctx context.Context) error {
		if err := s.repo.LockStaff(ctx, staffID); err != nil {
			return notFound(err)
		}
		cur, err := s.repo.Get(ctx, staffID)
		if err != nil {
			return notFound(err)
		}
		if err := pre.check(cur.Revision); err != nil {
			return err
		}
		removed, err := s.repo.RevokeModule(ctx, staffID, moduleID)
		if err != nil {
			return err
		}
		if !removed {
			out = cur
			return nil
		}
		if err := s.repo.BumpStaffRevision(ctx, staffID); err != nil {
			return err
		}
		// Re-read for the response before the sinks, so the event stays the
		// transaction's LAST statement.
		if out, err = s.repo.Get(ctx, staffID); err != nil {
			return err
		}
		if err := s.audit(ctx, "staff.module_revoked", staffID, "staff", map[string]any{
			"module_id": moduleID, "revision": cur.Revision + 1,
		}); err != nil {
			return err
		}
		return s.record(ctx, "staff.module_revoked", "staff", staffID, map[string]any{
			"module_id": moduleID, "revision": cur.Revision + 1,
		})
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// ListModules returns the catalog with each module's global enabled state
// and its flag's revision.
func (s *Service) ListModules(ctx context.Context) ([]Module, error) {
	enabled, err := s.repo.EnabledModules(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]Module, 0, len(knownModules))
	for _, m := range knownModules {
		rev, err := s.repo.ReadModuleRevision(ctx, m.ID)
		if err != nil {
			return nil, err
		}
		out = append(out, Module{ID: m.ID, Name: m.Name, Enabled: enabled[m.ID], Revision: rev})
	}
	return out, nil
}

// ModuleByID returns one catalog module with its flag state, or ErrNotFound
// for an id the catalog does not declare.
func (s *Service) ModuleByID(ctx context.Context, id string) (*Module, error) {
	for _, m := range knownModules {
		if m.ID != id {
			continue
		}
		enabled, err := s.repo.EnabledModules(ctx)
		if err != nil {
			return nil, err
		}
		rev, err := s.repo.ReadModuleRevision(ctx, id)
		if err != nil {
			return nil, err
		}
		out := Module{ID: m.ID, Name: m.Name, Enabled: enabled[id], Revision: rev}
		return &out, nil
	}
	return nil, ErrNotFound
}

// SetModuleEnabled flips the global modules.<id>.enabled flag on the flag's
// revision: the kill switch that revokes a module for every staff member at
// once WITHOUT deleting any grant, so turning it back on restores the
// previous roster rather than an empty one. A toggle that changes nothing
// writes nothing.
func (s *Service) SetModuleEnabled(ctx context.Context, moduleID string, enabled bool, pre Precondition) (*Module, error) {
	if pre.missing() {
		return nil, httpx.PreconditionRequired("this write needs If-Match or a body revision")
	}
	if !IsKnownModule(moduleID) {
		return nil, notFound(ErrNotFound)
	}
	var out *Module
	err := s.inTx(ctx, func(ctx context.Context) error {
		rev, err := s.repo.LockModuleRevision(ctx, moduleID)
		if err != nil {
			return err
		}
		if err := pre.check(rev); err != nil {
			return err
		}
		changed, err := s.repo.SetModuleEnabled(ctx, moduleID, enabled)
		if err != nil {
			return err
		}
		if !changed {
			out, err = s.ModuleByID(ctx, moduleID)
			return err
		}
		next := rev + 1
		if err := s.repo.BumpModuleRevision(ctx, moduleID, next); err != nil {
			return err
		}
		// Re-read for the response inside the transaction, so the returned
		// revision is the one this write produced and the event stays the
		// transaction's LAST statement.
		if out, err = s.ModuleByID(ctx, moduleID); err != nil {
			return err
		}
		if err := s.audit(ctx, "module.flag_changed", moduleEntityID(moduleID), "module", map[string]any{
			"module_id": moduleID, "enabled": enabled, "revision": next,
		}); err != nil {
			return err
		}
		eventType := "module.disabled"
		if enabled {
			eventType = "module.enabled"
		}
		return s.record(ctx, eventType, "module", moduleEntityID(moduleID), map[string]any{
			"module_id": moduleID, "enabled": enabled, "revision": next,
		})
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}
