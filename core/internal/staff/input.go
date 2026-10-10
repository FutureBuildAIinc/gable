// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package staff

import (
	"encoding/json"
	"strings"

	"github.com/gablelbm/gable/internal/platform/httpx"
)

// CreateStaffInput is the body of POST /api/v1/admin/staff. Active defaults
// to true and role to "staff"; a create has no revision to precondition on
// (a request carrying one is a 400 naming it).
type CreateStaffInput struct {
	Email    *string          `json:"email"`
	FullName *string          `json:"full_name"`
	StaffNo  *string          `json:"staff_no"`
	Role     *string          `json:"role"`
	Active   *bool            `json:"active"`
	Revision json.RawMessage `json:"revision"`
}

// Parsed is the validated create.
type ParsedCreate struct {
	Email    string
	FullName string
	StaffNo  *string
	Role     string
	Active   bool
}

// Parse validates the create, collecting every problem into one 400.
func (r *CreateStaffInput) Parse() (*ParsedCreate, error) {
	v := &httpx.Validator{}
	out := &ParsedCreate{Active: true, Role: "staff"}
	if r.Email == nil || strings.TrimSpace(*r.Email) == "" {
		v.Check(false, "email", "is required")
	} else {
		e := strings.TrimSpace(*r.Email)
		v.Check(len(e) <= 255, "email", "must be 255 characters or fewer")
		v.Check(strings.Contains(e, "@") && !strings.Contains(e, " "), "email", "must be an email address")
		out.Email = e
	}
	if r.FullName == nil || strings.TrimSpace(*r.FullName) == "" {
		v.Check(false, "full_name", "is required")
	} else {
		fn := strings.TrimSpace(*r.FullName)
		v.Check(len(fn) <= 255, "full_name", "must be 255 characters or fewer")
		out.FullName = fn
	}
	if r.StaffNo != nil {
		s := strings.TrimSpace(*r.StaffNo)
		v.Check(len(s) <= 64, "staff_no", "must be 64 characters or fewer")
		out.StaffNo = &s
	}
	if r.Role != nil {
		role := strings.TrimSpace(*r.Role)
		v.Check(len(role) <= 64, "role", "must be 64 characters or fewer")
		if role != "" {
			out.Role = role
		}
	}
	if r.Active != nil {
		out.Active = *r.Active
	}
	if len(r.Revision) > 0 && string(r.Revision) != "null" {
		v.Check(false, "revision", "a create has no revision to precondition on")
	}
	if err := v.Err(); err != nil {
		return nil, err
	}
	return out, nil
}

// UpdateStaffInput is the body of PUT /api/v1/admin/staff/{id}. Every field
// is a pointer: nil means "leave unchanged", so a PUT that only flips
// `active` must not blank out the email it did not send. Revision is the
// body's precondition.
type UpdateStaffInput struct {
	Email    *string          `json:"email"`
	FullName *string          `json:"full_name"`
	StaffNo  *string          `json:"staff_no"`
	Role     *string          `json:"role"`
	Active   *bool            `json:"active"`
	Revision json.RawMessage `json:"revision"`
}

// ParsedUpdate is the validated update.
type ParsedUpdate struct {
	Email    *string
	FullName *string
	StaffNo  *string
	Role     *string
	Active   *bool
}

// empty reports whether the update carries no field to apply: the grants'
// idempotent no-op rule, where a write that changes nothing writes nothing.
func (u *ParsedUpdate) empty() bool {
	return u == nil || (u.Email == nil && u.FullName == nil && u.StaffNo == nil && u.Role == nil && u.Active == nil)
}

// Parse validates the update. A staff_no of null clears the number (it is
// optional on the wire); an email or full_name, when sent, must be valid.
func (r *UpdateStaffInput) Parse() (*ParsedUpdate, *int64, error) {
	v := &httpx.Validator{}
	out := &ParsedUpdate{}
	if r.Email != nil {
		e := strings.TrimSpace(*r.Email)
		if e == "" {
			v.Check(false, "email", "cannot be emptied")
		} else {
			v.Check(len(e) <= 255 && strings.Contains(e, "@") && !strings.Contains(e, " "), "email", "must be an email address")
			out.Email = &e
		}
	}
	if r.FullName != nil {
		fn := strings.TrimSpace(*r.FullName)
		if fn == "" {
			v.Check(false, "full_name", "cannot be emptied")
		} else {
			v.Check(len(fn) <= 255, "full_name", "must be 255 characters or fewer")
			out.FullName = &fn
		}
	}
	if r.StaffNo != nil {
		s := strings.TrimSpace(*r.StaffNo)
		v.Check(len(s) <= 64, "staff_no", "must be 64 characters or fewer")
		out.StaffNo = &s // nil clears, a non-empty string sets
	}
	if r.Role != nil {
		role := strings.TrimSpace(*r.Role)
		v.Check(len(role) <= 64, "role", "must be 64 characters or fewer")
		out.Role = &role
	}
	if r.Active != nil {
		out.Active = r.Active
	}
	var revision *int64
	if n, ok := v.Int("revision", r.Revision, false); ok {
		v.Check(n >= 1, "revision", "must be a revision number, 1 or more")
		revision = &n
	}
	if err := v.Err(); err != nil {
		return nil, nil, err
	}
	return out, revision, nil
}

// GrantModuleInput is the body of POST /api/v1/admin/staff/{id}/modules.
type GrantModuleInput struct {
	ModuleID *string          `json:"module_id"`
	Revision json.RawMessage `json:"revision"`
}

// Parse validates the grant: the module must be one the catalog knows.
func (r *GrantModuleInput) Parse() (moduleID string, revision *int64, err error) {
	v := &httpx.Validator{}
	if r.ModuleID == nil || strings.TrimSpace(*r.ModuleID) == "" {
		v.Check(false, "module_id", "is required")
	} else {
		id := strings.TrimSpace(*r.ModuleID)
		v.Check(IsKnownModule(id), "module_id", "must be one of: ai_lm")
		moduleID = id
	}
	if n, ok := v.Int("revision", r.Revision, false); ok {
		v.Check(n >= 1, "revision", "must be a revision number, 1 or more")
		revision = &n
	}
	if err := v.Err(); err != nil {
		return "", nil, err
	}
	return moduleID, revision, nil
}

// SetModuleEnabledInput is the body of PUT /api/v1/admin/modules/{id}.
type SetModuleEnabledInput struct {
	Enabled  *bool            `json:"enabled"`
	Revision json.RawMessage `json:"revision"`
}

// Parse validates the toggle.
func (r *SetModuleEnabledInput) Parse() (enabled bool, revision *int64, err error) {
	v := &httpx.Validator{}
	if r.Enabled == nil {
		v.Check(false, "enabled", "is required")
	} else {
		enabled = *r.Enabled
	}
	if n, ok := v.Int("revision", r.Revision, false); ok {
		v.Check(n >= 1, "revision", "must be a revision number, 1 or more")
		revision = &n
	}
	if err := v.Err(); err != nil {
		return false, nil, err
	}
	return enabled, revision, nil
}
