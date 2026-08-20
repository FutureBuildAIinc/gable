// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package main

import (
	"net/http"

	"github.com/gablelbm/gable/internal/staff"
	"github.com/gablelbm/gable/pkg/audit"
	"github.com/gablelbm/gable/pkg/database"
	"github.com/gablelbm/gable/pkg/middleware"
)

// wireStaffAdmin mounts the staff roster + module-grant administration surface:
//
//	GET    /api/v1/admin/staff
//	POST   /api/v1/admin/staff
//	GET    /api/v1/admin/staff/{id}
//	PUT    /api/v1/admin/staff/{id}
//	POST   /api/v1/admin/staff/{id}/modules
//	DELETE /api/v1/admin/staff/{id}/modules/{module_id}
//	GET    /api/v1/admin/modules
//	PUT    /api/v1/admin/modules/{id}
//
// These are the writes behind POST /api/integration/validate-staff (AI_LM's
// login path, served by internal/integrations): they create the roster rows,
// hand out the per-staff `ai_lm` grants, and flip the global
// `modules.ai_lm.enabled` kill switch that validate-staff reads. Handing out
// module access is a privileged act, so the whole surface is behind
// RequireRole("admin", "owner") — the same guard the other admin surfaces
// (pim, pricing, gl, vision, governance) use.
//
// It is deliberately NOT branch-scoped: the roster is dealer-wide, and a staff
// member's AI_LM entitlement does not depend on which branch the admin has
// selected.
func wireStaffAdmin(mux *http.ServeMux, db *database.DB, auditLog *audit.Logger) {
	staffRepo := staff.NewRepository(db)
	staffSvc := staff.NewService(staffRepo).WithAuditLog(auditLog)
	staffHandler := staff.NewHandler(staffSvc)
	staffHandler.RegisterRoutes(mux, middleware.RequireRole("admin", "owner"))
}
