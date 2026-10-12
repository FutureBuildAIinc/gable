// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package serve

import (
	"net/http"

	"github.com/gablelbm/gable/internal/account"
	"github.com/gablelbm/gable/internal/chargecode"
	"github.com/gablelbm/gable/internal/crm"
	"github.com/gablelbm/gable/internal/customer"
	"github.com/gablelbm/gable/internal/customer/customerguard"
	"github.com/gablelbm/gable/internal/delivery"
	"github.com/gablelbm/gable/internal/document"
	"github.com/gablelbm/gable/internal/events"
	"github.com/gablelbm/gable/internal/inventory"
	"github.com/gablelbm/gable/internal/invoice"
	"github.com/gablelbm/gable/internal/location"
	"github.com/gablelbm/gable/internal/matching"
	"github.com/gablelbm/gable/internal/order"
	"github.com/gablelbm/gable/internal/payment"
	"github.com/gablelbm/gable/internal/pos"
	"github.com/gablelbm/gable/internal/product"
	"github.com/gablelbm/gable/internal/purchase_order"
	"github.com/gablelbm/gable/internal/quote"
	"github.com/gablelbm/gable/internal/reporting"
	"github.com/gablelbm/gable/pkg/audit"
	"github.com/gablelbm/gable/pkg/database"
	"github.com/gablelbm/gable/pkg/middleware"
)

// branchWall is the branch wall as serve wires it: the branch middleware that
// settles a request's branch context, the role-plus-branch composition every
// scoped route uses, the guard that holds a branch or location a request
// body names to that context (ADR 0007 section 2.3), and the key branch wall
// that holds a branch bound machine key to its pin on the routes that mount
// no branch middleware (ADR 0007 section 5.5). The mount methods below
// are the only places a route that takes a branch from its body gets its guard,
// and wire_branch_wall_test.go drives them with the real middleware, so
// dropping a guard here fails a test.
type branchWall struct {
	mw      func(http.Handler) http.Handler
	guard   *middleware.BranchGuard
	keyWall *middleware.KeyBranchWall
	scoped  func(roles ...string) func(http.Handler) http.Handler
	db      *database.DB
}

func newBranchWall(db *database.DB) *branchWall {
	w := &branchWall{mw: middleware.NewBranchMiddleware(db).Handler, guard: middleware.NewBranchGuard(db),
		keyWall: middleware.NewKeyBranchWall(db, audit.NewLogger(db)), db: db}
	w.scoped = func(roles ...string) func(http.Handler) http.Handler {
		return middleware.Compose(middleware.RequireRole(roles...), w.mw)
	}
	return w
}

// reporting mounts the reporting surface in its three registrations. Every
// report spans every branch's rows, so a branch bound key is refused the
// surface outright by the route allowlist gate (ADR 0007 section 5.5) and the
// role guard alone serves everyone else. Three methods because serve builds
// the scheduler between the registrations.
func (w *branchWall) reporting(mux *http.ServeMux, h *reporting.Handler) {
	h.RegisterRoutes(mux, middleware.RequireRole("admin", "owner", "finance"))
}

// reportingSchedules is reporting over the builder and schedule routes.
func (w *branchWall) reportingSchedules(mux *http.ServeMux, h *reporting.Handler, executor reporting.ScheduleExecutor) {
	wireReportSchedules(mux, h, executor)
}

// reportingBIIntegration is reporting over the BI export routes.
func (w *branchWall) reportingBIIntegration(mux *http.ServeMux, h *reporting.Handler) {
	h.RegisterBIIntegrationRoutes(mux, middleware.RequireRole("admin", "owner"))
}

// eventsFeed mounts the events feed: the outbox carries every branch's events
// with their payloads, so a branch bound key is refused it outright by the
// route allowlist gate and the role guard alone serves everyone else.
func (w *branchWall) eventsFeed(mux *http.ServeMux, h *events.Handler) {
	h.RegisterRoutes(mux, middleware.RequireRole("admin", "owner"))
}

// chargeCodes mounts the charge code master: every role that prices a line
// reads it, and only admin, owner and finance write it (a code carries its
// revenue account).
func (w *branchWall) chargeCodes(mux *http.ServeMux, svc *chargecode.Service) {
	chargecode.NewHandler(svc).RegisterRoutes(mux, w.scoped("admin", "owner", "sales", "finance"), w.scoped("admin", "owner", "finance"))
}

// locations mounts the location routes behind the branch middleware: the
// create writes into a branch's tree, the by-id reads and the list are held
// to the caller's branches (the branch switcher reads /me/branches, not this
// list). The key branch wall covers the routes that mount no branch
// middleware: the by-id writes, the branch directory verbs and the grant
// routes (ADR 0007 section 5.5).
func (w *branchWall) locations(mux *http.ServeMux, h *location.Handler) {
	h.WithBranchWall(w.guard, w.mw).WithKeyBranchWall(w.keyWall).
		RegisterRoutes(mux, middleware.RequireRole("admin", "owner", "warehouse", "sales"))
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
// take the narrower finance guard. The service also carries the shared
// customer guard (principal rulings 43 to 45 and item 48): a key bound to one
// branch changes only the branch local fields of a customer another branch
// holds, and adds but never changes or deletes its ship-tos and contacts; a
// shared customer's restricted fields change only for the finance or owner
// role (a user) or an unbound key carrying customers:restricted, which is why
// the third guard admits finance on exactly the three restricted field routes
// (the header write, the salesperson and the escalation policy) and the two
// reads those writes need (the record and its escalation policy, so finance
// can hold the revision it must send as If-Match, item 57) and nowhere else:
// every other customer route keeps the roles it had. The routes note their method and path so a
// refusal the service serves can name them in its key.branch_refused row.
func (w *branchWall) customers(mux *http.ServeMux, svc *customer.Service) {
	svc.WithBranchGuard(customerguard.New(w.guard)).WithSharedGuard(customerguard.NewShared(w.keyWall))
	customer.NewHandler(svc).RegisterRoutes(mux,
		middleware.Compose(w.scoped("admin", "owner", "sales"), w.keyWall.RecordRequest()),
		w.scoped("admin", "owner", "finance"),
		middleware.Compose(w.scoped("admin", "owner", "sales", "finance"), w.keyWall.RecordRequest()))
}

// crm mounts the activity routes behind the branch middleware: every route
// addresses a customer (by path) or an activity of one, and the repositories
// hold each read and write to the customer's branches, so a caller held to
// branch A finds branch B's activity a 404. An activity carries no branch tag,
// so on a customer another branch also holds a branch bound key adds but never
// changes or deletes one (the contact rule, principal item 54); the routes note
// their method and path for the refusal's key.branch_refused row.
func (w *branchWall) crm(mux *http.ServeMux, svc *crm.Service) {
	svc.WithSharedGuard(customerguard.NewShared(w.keyWall))
	crm.NewHandler(svc).RegisterRoutes(mux,
		middleware.Compose(w.scoped("admin", "owner", "sales"), w.keyWall.RecordRequest()))
}

// delivery mounts the delivery routes behind the branch middleware: a stop
// walls through its order's branch and a route through its stops' orders (a
// route with no stops carries no branch fact and is visible), so a caller
// held to branch A finds branch B's stop or route a 404. Vehicles and
// drivers carry no branch column (the fleet is dealer-wide), so their reads
// run on the same mount without a wall, a stated limit of the module's
// contract, listed in CONTRACT-CHANGES.md.
func (w *branchWall) delivery(mux *http.ServeMux, h *delivery.Handler, roles ...string) {
	h.RegisterRoutes(mux, w.scoped(roles...))
}

// quotes mounts the quote routes: the create and the update take customer_id
// from their body, so a branch bound key reaches them only while the pin holds
// that customer (round 9 P1-1), and their job_id is held the same way through
// the job's customer (round 11 P1-1). The conversion rechecks the quote's own
// customer under the same wall (round 11 P2-1): a quote moved onto another
// branch's customer by a caller who may must not become an order at the pin's
// branch.
func (w *branchWall) quotes(mux *http.ServeMux, svc *quote.Service) {
	quote.NewHandler(svc.WithBranchGuard(w.guard)).RegisterRoutes(mux,
		w.patternWall(
			w.bodyDocumentCustomer(w.scoped("admin", "owner", "sales"), "POST /api/v1/quotes", "PUT /api/v1/quotes/{id}"),
			w.keyWall.CustomerBranch(quoteRowCustomerOf(w.db)),
			"POST /api/v1/quotes/{id}/convert"))
}

// orders mounts the order routes: the create takes branch_id from its body,
// so it is held to the caller's branch context (ADR 0007 section 2.3), and the
// create and the update take customer_id from it, so a branch bound key
// reaches them only while the pin holds that customer (round 9 P1-1: the
// order would otherwise take another branch's tax exemption). Their job_id is
// held the same way through the job's customer (round 11 P1-1: the order
// would otherwise be priced by the other customer's job rule).
func (w *branchWall) orders(mux *http.ServeMux, svc *order.Service) {
	order.NewHandler(svc.WithBranchGuard(w.guard)).RegisterRoutes(mux,
		w.bodyDocumentCustomer(w.scoped("admin", "owner", "sales"), "POST /api/v1/orders", "PUT /api/v1/orders/{id}"),
		w.scoped("admin", "owner", "finance", "warehouse"))
}

// invoices mounts the invoice and credit memo routes: the credit memo create
// takes branch_id from its body and the writes address a document by path id,
// so the service holds both to the caller's branch context (ADR 0007 section
// 2.3) beside the wall every read already carries. The credit memo create and
// update take customer_id from their body, so a branch bound key reaches them
// only while the pin holds that customer (round 9 P1-1).
func (w *branchWall) invoices(mux *http.ServeMux, svc *invoice.Service) {
	invoice.NewHandler(svc.WithBranchGuard(w.guard)).RegisterRoutes(mux,
		w.bodyCustomer(w.scoped("admin", "owner", "sales", "finance"), "POST /api/v1/credit-memos", "PUT /api/v1/credit-memos/{id}"))
}

// accounts mounts the account and AR routes: the reads (summary, subledger,
// aging, statement) behind the role guard and the branch middleware, the
// reconciliation and the application reversal behind the finance guard. The
// summary, the subledger and the statement name a customer in the path, so
// the read guard carries the customer branch wall beside the branch
// middleware: a bound key reaches them only while that customer holds its
// pin (round 6 P1-2). The aging reads carry the repository's branch wall and
// name no customer, so the wall passes them; the application reversal walls
// through the invoice its application settles (the repository's wall), and
// the reconciliation reads every branch's books (its ledger half carries no
// branch fact), so a bound key is refused it by the route allowlist gate.
func (w *branchWall) accounts(mux *http.ServeMux, svc *account.Service) {
	customer := w.keyWall.CustomerBranch(accountCustomerPathOf())
	account.NewHandler(svc).RegisterRoutes(mux,
		middleware.Compose(w.scoped("admin", "owner", "sales", "finance"), customer),
		w.scoped("admin", "owner", "finance"))
}

// payments mounts the payment routes: the create takes branch_id from its body
// and the writes address a payment by path id, so the service holds both to the
// caller's branch context (ADR 0007 section 2.3) beside the wall every read
// carries. The cash and card creates take customer_id from their body, so a
// branch bound key reaches them only while the pin holds that customer
// (round 9 P1-1: a posted payment and its GL entry land on that customer). The
// intent create takes no customer at all, so it keeps the guard alone and its
// own 400 answers a body that names one, for every caller alike (round 11
// P3-2).
func (w *branchWall) payments(mux *http.ServeMux, svc *payment.Service) {
	payment.NewHandler(svc.WithBranchGuard(w.guard)).RegisterRoutes(mux,
		w.bodyCustomer(w.scoped("admin", "owner", "sales", "finance", "cashier"),
			"POST /api/v1/payments", "POST /api/v1/payments/card"))
}

// bodyCustomer composes a route group's guard with the key branch wall's hold
// on the customer_id the request body names, for the listed patterns only: the
// other routes of the group keep the guard alone.
func (w *branchWall) bodyCustomer(base func(http.Handler) http.Handler, patterns ...string) func(http.Handler) http.Handler {
	return w.patternWall(base, w.keyWall.CustomerBodyBranch(), patterns...)
}

// bodyDocumentCustomer is bodyCustomer over a document body, whose job_id
// names a customer through its project as well (round 11 P1-1): the hold is
// on the body's customer_id and the customer of the job its job_id names.
func (w *branchWall) bodyDocumentCustomer(base func(http.Handler) http.Handler, patterns ...string) func(http.Handler) http.Handler {
	return w.patternWall(base, w.keyWall.CustomerBranch(documentCustomersOf(w.db)), patterns...)
}

// patternWall runs hold after base on the listed route patterns and base alone
// on every other route of the group.
func (w *branchWall) patternWall(base, hold func(http.Handler) http.Handler, patterns ...string) func(http.Handler) http.Handler {
	held := make(map[string]bool, len(patterns))
	for _, p := range patterns {
		held[p] = true
	}
	return func(next http.Handler) http.Handler {
		plain, walled := base(next), middleware.Compose(base, hold)(next)
		return http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
			if held[r.Pattern] {
				walled.ServeHTTP(rw, r)
				return
			}
			plain.ServeHTTP(rw, r)
		})
	}
}

// draftGuard is the guard of a draft kind's routes: the role and branch guard,
// and on the promotion the hold on the customer the draft's stored payload
// names (round 9 P1-1: a draft written by a user, or by the key before the
// customer was checked, is checked again where it becomes a document).
func (w *branchWall) draftGuard(db *database.DB, kind string, roles ...string) func(http.Handler) http.Handler {
	return w.patternWall(w.scoped(roles...), w.keyWall.CustomerBranch(draftPayloadCustomerOf(db)),
		"POST /api/v1/drafts/"+kind+"/{id}/promote")
}

// pos mounts the POS routes: the repositories hold the by id reads and writes
// to their branch, and the offline sync replays whole sales whose bodies name
// a customer (an ACCOUNT tender invoices that customer at the register's
// branch), so the sync is held to the pin through the customer each replayed
// sale names (round 10 P1-1). The cashier creates refuse a machine key
// outright in the handler (a cashier must be a user).
func (w *branchWall) pos(mux *http.ServeMux, h *pos.Handler, roles ...string) {
	// The counter's operator routes (the resolve and the payback, the finance
	// money movers) run behind the finance wall: the claim's sale is read
	// through the caller's branch context, so a finance user held to one
	// branch finds another branch's claim a missing row (ninth review P1-3,
	// ADR 0007 section 2.3). The handler also holds the claim's sale to the
	// wall, which is what scopes a caller with no context branch
	// (default_branch_required=false, no header) to its grants (r12 P1-2),
	// the same record hold the document and purchase order routes carry.
	h.WithBranchGuard(w.guard).RegisterRoutes(mux,
		w.patternWall(w.scoped(roles...), w.keyWall.CustomerBranch(posSyncCustomersOf()), "POST /api/v1/pos/sync"),
		w.scoped("finance", "owner"))
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
