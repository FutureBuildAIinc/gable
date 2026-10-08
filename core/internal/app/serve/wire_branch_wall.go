// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package serve

import (
	"net/http"

	"github.com/gablelbm/gable/internal/customer"
	"github.com/gablelbm/gable/internal/customer/customerguard"
	"github.com/gablelbm/gable/internal/inventory"
	"github.com/gablelbm/gable/internal/location"
	"github.com/gablelbm/gable/internal/purchase_order"
	"github.com/gablelbm/gable/internal/quote"
	"github.com/gablelbm/gable/pkg/database"
	"github.com/gablelbm/gable/pkg/middleware"
)

// branchWall is the branch wall as serve wires it: the branch middleware that
// settles a request's branch context, the role-plus-branch composition every
// scoped route uses, and the guard that holds a branch or location a request
// body names to that context (ADR 0007 section 2.3). The mount methods below
// are the only places a route that takes a branch from its body gets its guard,
// and wire_branch_wall_test.go drives them with the real middleware, so
// dropping a guard here fails a test.
type branchWall struct {
	mw     func(http.Handler) http.Handler
	guard  *middleware.BranchGuard
	scoped func(roles ...string) func(http.Handler) http.Handler
}

func newBranchWall(db *database.DB) *branchWall {
	w := &branchWall{mw: middleware.NewBranchMiddleware(db).Handler, guard: middleware.NewBranchGuard(db)}
	w.scoped = func(roles ...string) func(http.Handler) http.Handler {
		return middleware.Compose(middleware.RequireRole(roles...), w.mw)
	}
	return w
}

// locations mounts the location routes. They are not branch scoped as a group
// (the branch switcher reads /me/branches before a branch is chosen), but the
// create writes into a branch's tree, so it runs behind the branch middleware.
func (w *branchWall) locations(mux *http.ServeMux, h *location.Handler) {
	h.WithBranchWall(w.guard, w.mw).RegisterRoutes(mux, middleware.RequireRole("admin", "owner", "warehouse", "sales"))
}

func (w *branchWall) inventory(mux *http.ServeMux, svc *inventory.Service) {
	inventory.NewHandler(svc).WithBranchGuard(w.guard).RegisterRoutes(mux, w.scoped("admin", "owner", "warehouse"))
}

// customers mounts the customer routes: the create takes primary_branch_id from
// its body, so it is held to the caller's branch context; payment terms writes
// take the narrower finance guard.
func (w *branchWall) customers(mux *http.ServeMux, svc *customer.Service) {
	customer.NewHandler(svc.WithBranchGuard(customerguard.New(w.guard))).
		RegisterRoutes(mux, w.scoped("admin", "owner", "sales"), w.scoped("admin", "owner", "finance"))
}

func (w *branchWall) quotes(mux *http.ServeMux, svc *quote.Service) {
	quote.NewHandler(svc.WithBranchGuard(w.guard)).RegisterRoutes(mux, w.scoped("admin", "owner", "sales"))
}

func (w *branchWall) purchaseOrders(mux *http.ServeMux, h *purchase_order.Handler) {
	h.WithBranchGuard(w.guard).RegisterRoutes(mux, w.scoped("admin", "owner", "purchasing"))
}
