// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package serve

import (
	"net/http"

	"github.com/gablelbm/gable/internal/account"
	"github.com/gablelbm/gable/internal/crm"
	"github.com/gablelbm/gable/internal/customer"
	"github.com/gablelbm/gable/internal/customer/customerguard"
	"github.com/gablelbm/gable/internal/delivery"
	"github.com/gablelbm/gable/internal/document"
	"github.com/gablelbm/gable/internal/inventory"
	"github.com/gablelbm/gable/internal/invoice"
	"github.com/gablelbm/gable/internal/location"
	"github.com/gablelbm/gable/internal/matching"
	"github.com/gablelbm/gable/internal/order"
	"github.com/gablelbm/gable/internal/payment"
	"github.com/gablelbm/gable/internal/product"
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

// locations mounts the location routes behind the branch middleware: the
// create writes into a branch's tree, the by-id reads and the list are held
// to the caller's branches (the branch switcher reads /me/branches, not this
// list).
func (w *branchWall) locations(mux *http.ServeMux, h *location.Handler) {
	h.WithBranchWall(w.guard, w.mw).RegisterRoutes(mux, middleware.RequireRole("admin", "owner", "warehouse", "sales"))
}

// products mounts the product routes behind the branch middleware: the stock
// totals a product read carries (on_hand, allocated, available) and the
// reorder alerts are sums over inventory, so they are held to the caller's
// branches like every other branch scoped read.
func (w *branchWall) products(mux *http.ServeMux, h *product.Handler) {
	h.RegisterRoutes(mux, w.scoped("admin", "owner", "sales", "warehouse"))
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

// crm mounts the activity routes behind the branch middleware: every route
// addresses a customer (by path) or an activity of one, and the repositories
// hold each read and write to the customer's branches, so a caller held to
// branch A finds branch B's activity a 404.
func (w *branchWall) crm(mux *http.ServeMux, svc *crm.Service) {
	crm.NewHandler(svc).RegisterRoutes(mux, w.scoped("admin", "owner", "sales"))
}

// delivery mounts the delivery routes behind the branch middleware: a stop
// walls through its order's branch and a route through its stops' orders (a
// route with no stops carries no branch fact and is visible), so a caller
// held to branch A finds branch B's stop or route a 404. Vehicles and
// drivers carry no branch column (the fleet is dealer-wide), so their reads
// run on the same mount without a wall — a stated limit of the module's
// contract, listed in CONTRACT-CHANGES.md.
func (w *branchWall) delivery(mux *http.ServeMux, h *delivery.Handler, roles ...string) {
	h.RegisterRoutes(mux, w.scoped(roles...))
}

func (w *branchWall) quotes(mux *http.ServeMux, svc *quote.Service) {
	quote.NewHandler(svc.WithBranchGuard(w.guard)).RegisterRoutes(mux, w.scoped("admin", "owner", "sales"))
}

// orders mounts the order routes: the create takes branch_id from its body,
// so it is held to the caller's branch context (ADR 0007 section 2.3).
func (w *branchWall) orders(mux *http.ServeMux, svc *order.Service) {
	order.NewHandler(svc.WithBranchGuard(w.guard)).RegisterRoutes(mux, w.scoped("admin", "owner", "sales"),
		w.scoped("admin", "owner", "finance", "warehouse"))
}

// invoices mounts the invoice and credit memo routes: the credit memo create
// takes branch_id from its body and the writes address a document by path id,
// so the service holds both to the caller's branch context (ADR 0007 section
// 2.3) beside the wall every read already carries.
func (w *branchWall) invoices(mux *http.ServeMux, svc *invoice.Service) {
	invoice.NewHandler(svc.WithBranchGuard(w.guard)).RegisterRoutes(mux, w.scoped("admin", "owner", "sales", "finance"))
}

// accounts mounts the account and AR routes: the reads (summary, subledger,
// aging, statement) behind the role guard and the branch middleware, the
// reconciliation and the application reversal behind the finance guard.
func (w *branchWall) accounts(mux *http.ServeMux, svc *account.Service) {
	account.NewHandler(svc).RegisterRoutes(mux, w.scoped("admin", "owner", "sales", "finance"), w.scoped("admin", "owner", "finance"))
}

// payments mounts the payment routes: the create takes branch_id from its body
// and the writes address a payment by path id, so the service holds both to the
// caller's branch context (ADR 0007 section 2.3) beside the wall every read
// carries.
func (w *branchWall) payments(mux *http.ServeMux, svc *payment.Service) {
	payment.NewHandler(svc.WithBranchGuard(w.guard)).RegisterRoutes(mux, w.scoped("admin", "owner", "sales", "finance", "cashier"))
}

func (w *branchWall) purchaseOrders(mux *http.ServeMux, h *purchase_order.Handler) {
	h.WithBranchGuard(w.guard).RegisterRoutes(mux, w.scoped("admin", "owner", "purchasing"))
}

// matching mounts the 3-way match routes: both act on a purchase order
// addressed by its path id, so they run behind the branch middleware and the
// purchase order is held to the caller's wall before the matcher runs.
func (w *branchWall) matching(mux *http.ServeMux, svc *matching.Service) {
	matching.NewHandler(svc).WithBranchGuard(w.guard).RegisterRoutes(mux, w.scoped("admin", "owner", "finance"))
}

// documents mounts the document print and email routes behind the branch
// middleware: a print or an email acts on an invoice or an order addressed by
// its path id, and the invoice and order repositories already filter their
// reads on the branch the middleware settles, so a caller held to branch A
// finds branch B's invoice or pick ticket a 404. The handler also holds the
// loaded record's own branch to the wall, which is what scopes a bound caller
// with no context branch (default_branch_required=false, no header) to its
// grants; the repositories' filter never fires for that caller. (The 403
// record checks on the invoice and order modules' own routes arrive with
// their seam.)
func (w *branchWall) documents(mux *http.ServeMux, h *document.Handler) {
	h.WithBranchWall(w.guard).RegisterRoutes(mux, w.scoped("admin", "owner", "sales", "finance"))
}
