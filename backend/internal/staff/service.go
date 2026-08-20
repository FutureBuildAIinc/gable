// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package staff

import (
	"context"

	"github.com/gablelbm/gable/pkg/audit"
	"github.com/google/uuid"
)

// store is the data dependency of Service. It exists so the handlers can be
// exercised over httptest without Postgres; the production implementation is
// *Repository.
type store interface {
	List(ctx context.Context) ([]Staff, error)
	Get(ctx context.Context, id uuid.UUID) (*Staff, error)
	Create(ctx context.Context, in CreateStaffInput) (*Staff, error)
	Update(ctx context.Context, id uuid.UUID, in UpdateStaffInput) (*Staff, error)
	GrantModule(ctx context.Context, staffID uuid.UUID, moduleID, grantedBy string) error
	RevokeModule(ctx context.Context, staffID uuid.UUID, moduleID string) error
	EnabledModules(ctx context.Context) (map[string]bool, error)
	SetModuleEnabled(ctx context.Context, moduleID string, enabled bool) error
}

// auditSink is the audit dependency, narrowed to the one method used so tests
// can assert that a privileged grant/revoke was recorded without standing up
// the audit_log table. *audit.Logger satisfies it.
type auditSink interface {
	Log(ctx context.Context, entry audit.Entry)
}

// Service holds staff-management business logic. Module grant/revoke operations
// are audit-logged via pkg/audit.Logger.
type Service struct {
	repo     store
	auditLog auditSink
}

func NewService(repo store) *Service {
	return &Service{repo: repo}
}

// WithAuditLog attaches an audit logger so grant/revoke operations are recorded.
// A nil logger is ignored rather than stored: a typed-nil *audit.Logger boxed
// into the auditSink interface would be non-nil and panic on first use.
func (s *Service) WithAuditLog(l *audit.Logger) *Service {
	if l == nil {
		return s
	}
	s.auditLog = l
	return s
}

func (s *Service) ListStaff(ctx context.Context) ([]Staff, error) {
	return s.repo.List(ctx)
}

func (s *Service) GetStaff(ctx context.Context, id uuid.UUID) (*Staff, error) {
	return s.repo.Get(ctx, id)
}

func (s *Service) CreateStaff(ctx context.Context, in CreateStaffInput) (*Staff, error) {
	return s.repo.Create(ctx, in)
}

func (s *Service) UpdateStaff(ctx context.Context, id uuid.UUID, in UpdateStaffInput) (*Staff, error) {
	return s.repo.Update(ctx, id, in)
}

// GrantModule grants a module to a staff member and audit-logs the action.
func (s *Service) GrantModule(ctx context.Context, staffID uuid.UUID, moduleID, grantedBy string) error {
	if err := s.repo.GrantModule(ctx, staffID, moduleID, grantedBy); err != nil {
		return err
	}
	if s.auditLog != nil {
		s.auditLog.Log(ctx, audit.Entry{
			Action:     "module.grant",
			EntityType: "staff",
			EntityID:   staffID,
			Changes:    map[string]interface{}{"module_id": moduleID, "granted_by": grantedBy},
		})
	}
	return nil
}

// RevokeModule revokes a module from a staff member and audit-logs the action.
func (s *Service) RevokeModule(ctx context.Context, staffID uuid.UUID, moduleID string) error {
	if err := s.repo.RevokeModule(ctx, staffID, moduleID); err != nil {
		return err
	}
	if s.auditLog != nil {
		s.auditLog.Log(ctx, audit.Entry{
			Action:     "module.revoke",
			EntityType: "staff",
			EntityID:   staffID,
			Changes:    map[string]interface{}{"module_id": moduleID},
		})
	}
	return nil
}

// ListModules returns the known integration modules with their global
// enabled state.
func (s *Service) ListModules(ctx context.Context) ([]Module, error) {
	enabled, err := s.repo.EnabledModules(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]Module, 0, len(knownModules))
	for _, m := range knownModules {
		m.Enabled = enabled[m.ID]
		out = append(out, m)
	}
	return out, nil
}

// SetModuleEnabled flips the global modules.<id>.enabled flag. This is the kill
// switch: turning a module off revokes it for every staff member at once
// WITHOUT deleting any grant, so turning it back on restores the previous
// roster rather than an empty one.
func (s *Service) SetModuleEnabled(ctx context.Context, moduleID string, enabled bool) error {
	return s.repo.SetModuleEnabled(ctx, moduleID, enabled)
}
