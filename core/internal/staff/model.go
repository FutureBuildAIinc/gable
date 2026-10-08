// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

// Package staff is the administration surface for the dealer staff roster and
// the per-module access grants that gate integration modules such as AI_LM.
//
// It owns the WRITE side of two tables created by migration 080: `staff` and
// `module_grants`, plus the `modules.<id>.enabled` rows in `system_settings`.
// The READ side — POST /api/integration/validate-staff, AI_LM's login path —
// lives in internal/integrations and derives entitlement as:
//
//	entitled = staff.active
//	           AND a module_grants row (staff_id, 'ai_lm') exists
//	           AND system_settings['modules.ai_lm.enabled'] == 'true'
//
// This package deliberately does NOT re-implement that rule: a second copy of
// an authorization predicate that nothing calls is a drift hazard. What it does
// instead is expose the three facts the rule reads (active flag, grants, global
// flag) as independently editable admin state.
//
// Note the asymmetry in the two "modules" fields, which is intentional:
// Staff.Modules here is the RAW grant set, because an admin checkbox must show
// what was granted even while the module is globally switched off; the
// integration surface reports granted ∩ globally-enabled, because that is what
// the caller is actually allowed to use right now.
package staff

import (
	"time"

	"github.com/google/uuid"
)

// Staff is a member of the dealer's roster. This is distinct from a JWT-`sub`
// ERP user: it is the identity AI_LM authenticates against via the
// `validate-staff` integration call, and the subject of per-module access
// grants. Module access is governed by module grants (and the global
// modules.<id>.enabled flag), not by Role — Role is a free-text label.
type Staff struct {
	ID        uuid.UUID `json:"id"`
	Email     string    `json:"email"`
	FullName  string    `json:"full_name"`
	StaffNo   *string   `json:"staff_no,omitempty"`
	Role      string    `json:"role"`
	Active    bool      `json:"active"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`

	// Modules is the set of module_ids granted to this staff member. Populated on
	// list/get so the admin UI can render per-module grant checkboxes. Not a DB
	// column, and NOT filtered by the global enable flag — see the package doc.
	Modules []string `json:"modules"`
}

// CreateStaffInput is the payload to create a staff member.
type CreateStaffInput struct {
	Email    string  `json:"email"`
	FullName string  `json:"full_name"`
	StaffNo  *string `json:"staff_no"`
	Role     string  `json:"role"`
	Active   *bool   `json:"active"`
}

// UpdateStaffInput is the payload to update a staff member. All fields optional;
// nil means "leave unchanged". Every field is a pointer for exactly that reason:
// a PUT that only flips `active` must not blank out the email it did not send.
type UpdateStaffInput struct {
	Email    *string `json:"email"`
	FullName *string `json:"full_name"`
	StaffNo  *string `json:"staff_no"`
	Role     *string `json:"role"`
	Active   *bool   `json:"active"`
}

// Module is the global state of an integration module (e.g. AI_LM), toggled via
// the modules.<id>.enabled system setting.
type Module struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	Enabled bool   `json:"enabled"`
}

// knownModules is the catalog of integration modules the admin UI can toggle.
// Today AI_LM is the only one; extend this as modules are added.
var knownModules = []Module{
	{ID: "ai_lm", Name: "AI_LM"},
}
