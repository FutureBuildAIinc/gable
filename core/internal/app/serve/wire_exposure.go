// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package serve

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"

	"github.com/gablelbm/gable/internal/delivery"
	"github.com/gablelbm/gable/internal/order"
	"github.com/gablelbm/gable/internal/platform/httpx"
	"github.com/gablelbm/gable/internal/pricing"
	"github.com/gablelbm/gable/internal/quote"
	"github.com/gablelbm/gable/pkg/audit"
	"github.com/gablelbm/gable/pkg/branchctx"
	"github.com/gablelbm/gable/pkg/database"
	"github.com/gablelbm/gable/pkg/middleware"
	"github.com/gablelbm/gable/pkg/outbox"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// ExposureWiring is the handle main.go keeps after wiring the lumber
// index-aware price-protection subsystem. Call Shutdown during graceful
// shutdown to stop the safety-net cron. The outbox drain that delivers the
// exposure notifications is not here: it is a background job and runs in the
// worker role (internal/app/worker).
type ExposureWiring struct {
	// Scheduler is the nightly safety-net re-evaluation cron. Disabled unless
	// system_settings has exposure.enabled = "true".
	Scheduler *quote.ExposureScheduler
}

// Shutdown stops the safety-net cron, bounded by ctx. Safe to
// call on a nil receiver so main.go's shutdown path needs no guard.
func (w *ExposureWiring) Shutdown(ctx context.Context) {
	if w == nil {
		return
	}
	if w.Scheduler != nil {
		w.Scheduler.Stop()
	}
}

// exposureDeps is everything the price-protection subsystem needs from the
// rest of main.go's initializer. Grouped into a struct so adding a dependency
// later does not churn the call site in main.go (which four subsystems share).
type exposureDeps struct {
	Mux           *http.ServeMux
	DB            *database.DB
	Logger        *slog.Logger
	AuditLog      *audit.Logger
	EscalatorRepo pricing.EscalatorRepository
	QuoteRepo     quote.QuoteLineReader
	QuoteSvc      *quote.Service
	OrderSvc      *order.Service
	DeliverySvc   *delivery.Service
	// EventsOrg is the org slug the outbox writer stamps on every event
	// (EVENTS_ORG, "default" when unset): one database per dealer today, so
	// the org is a property of the deployment.
	EventsOrg string
	// MW is the branch middleware (SEC-NOHEADER-1): the exposure routes
	// mount it so a user request carries a branch context, grants and the
	// default_branch_required check like every scoped surface.
	MW func(http.Handler) http.Handler
	// Guard is the branch guard the by id routes hold their quote to.
	Guard *middleware.BranchGuard
}

// wireExposure builds and registers the entire lumber index-aware quote
// price-protection subsystem:
//
//   - the in-process event bus and the notification subscriber on
//     quote.exposure.>
//   - the exposure repository, checker, scanner and service
//   - the salesperson/owner HTTP surface and the market-index admin surface
//   - the DRAFT→SENT snapshot hook on quote.Service
//   - the pre-ship exposure gate on order.Service and delivery.Service
//   - the nightly safety-net cron (off unless exposure.enabled = "true")
//
// Every seam is optional-by-nil on the consuming side, so a failure here
// degrades the feature rather than the process. The returned handle is never
// nil.
func wireExposure(deps exposureDeps) *ExposureWiring {
	logger := deps.Logger
	if logger == nil {
		logger = slog.Default()
	}

	// Delivery: the worker role's outbox drain delivers committed events_outbox
	// rows (pkg/outbox) synchronously to each registered subscriber handler,
	// so delivery follows the commit, not the process lifetime. The eventbus
	// package's Event envelope and subject wildcard rules remain the shared
	// vocabulary between the drain and its subscribers.
	logger.Info("exposure events: delivered by the outbox drain, at least once, per subscriber cursor")

	exposureRepo := pricing.NewExposureRepository(deps.DB)
	exposureChecker := pricing.NewExposureChecker(deps.DB)
	exposureAudit := &exposureAuditAdapter{auditLog: deps.AuditLog}

	// The outbox writer stamps the deployment's org (EVENTS_ORG, "default"
	// until a deployment level org identity exists) on every event; the
	// scanner and service record their notification events through it inside
	// their mutations' transactions.
	outboxWriter := outbox.NewWriter(deps.DB, deps.EventsOrg)

	exposureScanner := pricing.NewExposureScanner(
		exposureRepo, deps.EscalatorRepo, deps.QuoteRepo, exposureAudit, deps.DB, logger,
	).WithOutbox(outboxWriter)

	exposureSvc := pricing.NewExposureService(
		exposureRepo, deps.EscalatorRepo, deps.QuoteRepo, exposureAudit, exposureChecker, logger,
	).WithOutbox(outboxWriter).WithTxRunner(deps.DB)

	registerExposureRoutes(deps.Mux, exposureRoutes{
		Scanner:    exposureScanner,
		Checker:    exposureChecker,
		Exposure:   exposureRepo,
		Service:    exposureSvc,
		Escalators: deps.EscalatorRepo,
		DB:         deps.DB,
		Logger:     logger,
		AuditLog:   deps.AuditLog,
		MW:         deps.MW,
		Guard:      deps.Guard,
	})

	// DRAFT → SENT: freeze the index baseline, customer policy and threshold
	// onto each commodity line. Best-effort inside quote.Service.
	if deps.QuoteSvc != nil {
		deps.QuoteSvc.WithSnapshotService(
			pricing.NewSnapshotService(deps.EscalatorRepo, exposureRepo, deps.QuoteRepo, deps.DB, logger),
		)
	}

	// Pre-ship gate: the order service's gate is wired by its own constructor
	// (internal/app/orderwire), which both roles share; only the delivery
	// service's gate is wired here.
	if deps.DeliverySvc != nil {
		deps.DeliverySvc.WithExposureGate(exposureChecker)
	}

	// Notifications: the exposure notifier is registered on the outbox drain
	// in the worker role (internal/app/worker), which delivers every exposure
	// event committed to the outbox to it, at least once, per cursor.

	// Nightly safety net. Off by default; an operator enables it by setting
	// exposure.enabled = "true" in system_settings. It re-evaluates every open
	// commodity quote against current index values, which is how the system
	// recovers from an event the drain parked or dropped and a refresh that
	// raced a restart.
	scheduler := quote.NewExposureScheduler(deps.DB, exposureScanner)
	if err := scheduler.Start(context.Background()); err != nil {
		logger.Error("exposure safety-net scheduler failed to start", "error", err)
	}

	return &ExposureWiring{Scheduler: scheduler}
}

// exposureRoutes carries the collaborators the exposure HTTP surface needs.
type exposureRoutes struct {
	Scanner    *pricing.ExposureScanner
	Checker    pricing.ExposureChecker
	Exposure   pricing.ExposureRepository
	Service    *pricing.ExposureService
	Escalators pricing.EscalatorRepository
	DB         *database.DB
	Logger     *slog.Logger
	// AuditLog writes the key.branch_refused rows the key branch wall on
	// these routes records. Optional: nil leaves the refusals un-audited.
	AuditLog *audit.Logger
	// MW is the branch middleware that settles a request's branch context
	// (SEC-NOHEADER-1): without it these routes carried no context branch,
	// no grants and no default_branch_required check for a user.
	MW func(http.Handler) http.Handler
	// Guard holds a by id route's quote branch to the caller the middleware
	// settled (the context branch, or a no header user's grants).
	Guard *middleware.BranchGuard
}

// registerExposureRoutes attaches the twelve price-protection endpoints this
// subsystem owns. The other four routes in the feature's surface live with the
// modules that own their resource: the two /orders/{id}/exposure-* routes in
// internal/order, and the two /customers/{id}/escalation-policy routes in
// internal/customer.
//
// Split out of wireExposure so the route surface can be asserted without
// standing up a database (see wire_exposure_test.go).
func registerExposureRoutes(mux *http.ServeMux, r exposureRoutes) {
	// Salesperson + owner surface: at-risk list, per-quote detail and actions,
	// portfolio report, admin scan trigger. The branch middleware settles the
	// request's branch context, the user branch wall holds a by id route's
	// quote to it (a read of a quote outside the caller's reach reads as
	// missing, an act on one is refused, nothing written, SEC-NOHEADER-1),
	// and the key branch wall holds a branch bound key to its pin on the by
	// id routes (CONFINED in the route allowlist); the two lists (the at-risk
	// and portfolio reports) carry the grants wall in their queries and are
	// refused a bound key outright by the route allowlist gate.
	var keyAuditor middleware.BranchRefusalAuditor // a nil logger must not ride a non nil interface
	if r.AuditLog != nil {
		keyAuditor = r.AuditLog
	}
	keyWall := middleware.NewKeyBranchWall(r.DB, keyAuditor)
	guard := middleware.Compose(middleware.RequireRole("admin", "owner", "sales"), r.MW)
	if r.Guard != nil {
		guard = middleware.Compose(guard, exposureUserBranchWall(r.Guard, r.quoteBranchOf))
	}
	exposureGuard := middleware.Compose(guard, exposureKeyBranchWall(keyWall, r.quoteBranchOf))
	pricing.NewExposureHandler(r.Scanner, r.Checker, r.Exposure, r.Service).
		RegisterRoutes(mux, exposureGuard)

	// Buyer/admin surface: index refresh (+ dry-run preview), metadata edit,
	// history time-series. Deliberately does NOT re-register
	// GET /api/v1/market-indices: that belongs to pricing.EscalatorHandler and
	// a duplicate pattern would panic the ServeMux. The refresh and its
	// preview re-check every branch's quotes, so the route allowlist gate
	// refuses a branch bound key both; the index metadata and history are
	// dealer wide reference data, stated limits of the allowlist.
	pricing.NewIndexAdminHandler(r.Escalators, r.Exposure, r.Scanner, r.DB, r.Logger).
		RegisterRoutes(mux, indexAdminGuard())
}

// indexAdminGuard is the market index admin surface's role guard. The refresh
// and preview refusals live in the route allowlist gate, not here.
func indexAdminGuard() func(http.Handler) http.Handler {
	return middleware.RequireRole("admin", "owner")
}

// quoteBranchOf resolves the branch a quote belongs to, the row branch the
// exposure by id routes are held to. A quote that does not exist names no
// branch (the handler's own 404 answers).
func (r exposureRoutes) quoteBranchOf(ctx context.Context, id uuid.UUID) (*uuid.UUID, error) {
	if r.DB == nil {
		return nil, errors.New("exposure key branch wall: no database for the quote lookup")
	}
	var branch *uuid.UUID
	err := r.DB.GetExecutor(ctx).QueryRow(ctx,
		`SELECT branch_id FROM quotes WHERE id = $1`, id).Scan(&branch)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return branch, nil
}

// exposureKeyBranchWall routes the exposure surface's five by id quote routes
// through the key branch wall: each holds its quote's branch to a bound key's
// pin (CONFINED in the route allowlist, proven by
// TestKeyBranchPin_ExposureRoutes). The two lists and the admin scan are
// REFUSED by the route allowlist gate before the handler runs, so they keep
// the role guard alone here.
func exposureKeyBranchWall(keyWall *middleware.KeyBranchWall, quoteBranchOf func(ctx context.Context, id uuid.UUID) (*uuid.UUID, error)) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		byID := keyWall.RowBranch(quoteBranchOf)(next)
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.PathValue("id") != "" {
				byID.ServeHTTP(w, r)
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

// exposureUserBranchWall holds a by id exposure route's quote to the branch
// context the branch middleware settled for a user (SEC-NOHEADER-1): the
// quote's own branch must be one the caller may target, so a user granted
// only A neither reads nor writes B's quote, with or without the header. A
// read outside the reach answers missing (404, the wall a header caller
// already met); an act is refused (403 naming id) before the handler runs,
// so nothing is written. The wall fails closed: a lookup error answers 500
// and a quote that does not exist (or of no branch, which names none the
// caller could hold) answers 404, and neither hands the request on to the
// handler, whose by id writes have no wall of their own beneath this one.
func exposureUserBranchWall(guard *middleware.BranchGuard, quoteBranchOf func(ctx context.Context, id uuid.UUID) (*uuid.UUID, error)) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			// A branch bound key is the key branch wall's caller, one wall
			// down: its refusal names the pin rule and writes the
			// key.branch_refused row, so this wall does not answer first.
			if _, ok := branchctx.KeyBranchFromContext(r.Context()); ok {
				next.ServeHTTP(w, r)
				return
			}
			raw := r.PathValue("id")
			if raw == "" {
				next.ServeHTTP(w, r)
				return
			}
			id, err := uuid.Parse(raw)
			if err != nil {
				next.ServeHTTP(w, r) // the handler answers the malformed id
				return
			}
			branch, err := quoteBranchOf(r.Context(), id)
			if err != nil {
				httpx.WriteError(w, r, fmt.Errorf("quote branch lookup: %w", err))
				return
			}
			if branch == nil {
				httpx.WriteError(w, r, httpx.NotFound("quote not found"))
				return
			}
			if err := guard.CheckPayloadBranch(r.Context(), *branch); errors.Is(err, middleware.ErrPayloadBranchRefused) {
				if r.Method == http.MethodGet || r.Method == http.MethodHead {
					http.NotFound(w, r)
					return
				}
				httpx.WriteError(w, r, &httpx.Error{Status: http.StatusForbidden, Code: httpx.CodeForbidden,
					Message: "quote is outside the branches this caller may target",
					Details: []httpx.FieldError{{Field: "id", Code: httpx.CodeForbidden, Message: "not a branch this caller may target"}}})
				return
			} else if err != nil {
				httpx.WriteError(w, r, fmt.Errorf("quote branch lookup: %w", err))
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

// exposureAuditAdapter bridges the synchronous pkg/audit.Logger to the narrow
// pricing.AuditWriter interface (which re-declares the audit entry to avoid a
// pricing→pkg/audit import). EntityID arrives as a string; non-UUID values map
// to uuid.Nil rather than dropping the audit entry.
type exposureAuditAdapter struct {
	auditLog *audit.Logger
}

func (a *exposureAuditAdapter) LogEntry(ctx context.Context, e pricing.AuditEntry) {
	if a.auditLog == nil {
		return
	}
	entityID, err := uuid.Parse(e.EntityID)
	if err != nil {
		entityID = uuid.Nil
	}
	a.auditLog.Log(ctx, audit.Entry{
		Action:     e.Action,
		EntityType: e.EntityType,
		EntityID:   entityID,
		UserID:     e.UserID,
		Changes:    e.Changes,
	})
}
