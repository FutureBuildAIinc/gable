// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

// Package staff is the administration surface for the dealer staff roster and
// the per-module access grants that gate integration modules such as AI_LM.
//
// It owns the WRITE side of two tables created by migration 080: `staff` and
// `module_grants`, plus the `modules.<id>.enabled` rows in `system_settings`
// and their revision anchors in `admin_revisions` (migration 095). The READ
// side, POST /api/integration/validate-staff (AI_LM's login path), lives in
// internal/integrations and derives entitlement as:
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
// integration surface reports granted AND globally-enabled, because that is
// what the caller is actually allowed to use right now.
package staff

import (
	"github.com/gablelbm/gable/internal/platform/httpx"
	"github.com/google/uuid"
)

// Staff is a member of the dealer's roster. This is distinct from a JWT-`sub`
// ERP user: it is the identity AI_LM authenticates against via the
// `validate-staff` integration call, and the subject of per-module access
// grants. Module access is governed by module grants (and the global
// modules.<id>.enabled flag), not by Role; Role is a free-text label.
type Staff struct {
	ID        uuid.UUID       `json:"id"`
	Email     string          `json:"email"`
	FullName  string          `json:"full_name"`
	StaffNo   *string         `json:"staff_no"`
	Role      string          `json:"role"`
	Active    bool            `json:"active"`
	Revision  int64           `json:"revision"`
	CreatedAt httpx.Timestamp `json:"created_at"`
	UpdatedAt httpx.Timestamp `json:"updated_at"`

	// Modules is the set of module_ids granted to this staff member. Populated
	// on list/get so the admin UI can render per-module grant checkboxes, and
	// never null. Not a DB column, and NOT filtered by the global enable flag
	// (see the package doc).
	Modules []string `json:"modules"`
}

// Module is the global state of an integration module (e.g. AI_LM), toggled
// via the modules.<id>.enabled system setting. Revision is the flag's
// revision anchor, the precondition every toggle carries.
type Module struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Enabled  bool   `json:"enabled"`
	Revision int64  `json:"revision"`
}

// knownModules is the catalog of integration modules the admin UI can toggle.
// The module grant routes admit exactly these ids. Today AI_LM is the only
// one; extend this as modules are added.
var knownModules = []Module{
	{ID: "ai_lm", Name: "AI_LM"},
}

// KnownModuleIDs lists the grantable module ids.
func KnownModuleIDs() []string {
	ids := make([]string, 0, len(knownModules))
	for _, m := range knownModules {
		ids = append(ids, m.ID)
	}
	return ids
}

// IsKnownModule reports whether id names a module in the catalog.
func IsKnownModule(id string) bool {
	for _, m := range knownModules {
		if m.ID == id {
			return true
		}
	}
	return false
}

// moduleSettingKey is the single place the system_settings key shape is
// spelled out. internal/integrations builds the same key in SQL
// ('modules.' || g.module_id || '.enabled'); the two must stay identical or a
// module toggled here would not be the module read there.
func moduleSettingKey(moduleID string) string {
	return "modules." + moduleID + ".enabled"
}

// moduleResource is the revision anchor name of one module's flag.
func moduleResource(moduleID string) string {
	return "admin.modules." + moduleID
}

// moduleEntityID is the module's stable id on the events feed and the audit
// trail. The outbox names UUID entities and a module id is a text slug, so
// the slug is hashed into the URL namespace: deterministic, stable, derived
// from nothing a caller controls.
func moduleEntityID(moduleID string) uuid.UUID {
	return uuid.NewMD5(uuid.NameSpaceURL, []byte("admin-module/"+moduleID))
}
