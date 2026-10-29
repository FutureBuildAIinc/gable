// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package staff

import "net/http"

// RegisterRoutes wires the staff-management + module-flag admin endpoints under
// /api/v1/admin/staff* and /api/v1/admin/modules*. An optional roleGuard
// (middleware.RequireRole("admin","owner")) is applied to every route — the
// variadic shape matches every other RegisterRoutes in this repo, but callers
// must pass one: these endpoints hand out module access, so an unguarded mount
// is a privilege-escalation path. cmd/server/wire_staff.go is the only caller
// and always supplies the guard.
func (h *Handler) RegisterRoutes(mux *http.ServeMux, roleGuard ...func(http.Handler) http.Handler) {
	guard := func(handler http.HandlerFunc) http.HandlerFunc {
		if len(roleGuard) > 0 && roleGuard[0] != nil {
			return func(w http.ResponseWriter, r *http.Request) {
				roleGuard[0](handler).ServeHTTP(w, r)
			}
		}
		return handler
	}

	// Staff CRUD
	mux.HandleFunc("GET /api/v1/admin/staff", guard(h.ListStaff))
	mux.HandleFunc("POST /api/v1/admin/staff", guard(h.CreateStaff))
	mux.HandleFunc("GET /api/v1/admin/staff/{id}", guard(h.GetStaff))
	mux.HandleFunc("PUT /api/v1/admin/staff/{id}", guard(h.UpdateStaff))

	// The manager's counter PIN (CP-04): set, clear, unlock. The same
	// admin and owner guard as the staff writes; the PIN is hashed on the
	// server and never logged.
	mux.HandleFunc("PUT /api/v1/admin/staff/{id}/counter-pin", guard(h.SetStaffCounterPIN))
	mux.HandleFunc("DELETE /api/v1/admin/staff/{id}/counter-pin", guard(h.ClearStaffCounterPIN))
	mux.HandleFunc("POST /api/v1/admin/staff/{id}/counter-pin/unlock", guard(h.UnlockStaffCounterPIN))

	// Per-staff module grants
	mux.HandleFunc("POST /api/v1/admin/staff/{id}/modules", guard(h.GrantModule))
	mux.HandleFunc("DELETE /api/v1/admin/staff/{id}/modules/{module_id}", guard(h.RevokeModule))

	// Global module enable flags
	mux.HandleFunc("GET /api/v1/admin/modules", guard(h.ListModules))
	mux.HandleFunc("PUT /api/v1/admin/modules/{id}", guard(h.SetModuleEnabled))
}
