// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package main

import (
	"context"
	"log/slog"
	"net/http"

	"github.com/gablelbm/gable/internal/delivery"
	"github.com/gablelbm/gable/internal/notification"
	"github.com/gablelbm/gable/internal/order"
	"github.com/gablelbm/gable/internal/pricing"
	"github.com/gablelbm/gable/internal/quote"
	"github.com/gablelbm/gable/pkg/audit"
	"github.com/gablelbm/gable/pkg/database"
	"github.com/gablelbm/gable/pkg/eventbus"
	"github.com/gablelbm/gable/pkg/middleware"
	"github.com/google/uuid"
)

// ExposureWiring is the handle main.go keeps after wiring the lumber
// index-aware price-protection subsystem. Call Shutdown during graceful
// shutdown to stop the safety-net cron and drain the event bus.
type ExposureWiring struct {
	// Bus is the in-process event bus carrying quote.exposure.* events.
	// See pkg/eventbus for the (deliberately weak) delivery guarantees.
	Bus eventbus.Bus
	// Scheduler is the nightly safety-net re-evaluation cron. Disabled unless
	// system_settings has exposure.enabled = "true".
	Scheduler *quote.ExposureScheduler
}

// Shutdown stops the safety-net cron and closes the event bus, bounded by ctx.
// Safe to call on a nil receiver so main.go's shutdown path needs no guard.
func (w *ExposureWiring) Shutdown(ctx context.Context) {
	if w == nil {
		return
	}
	if w.Scheduler != nil {
		w.Scheduler.Stop()
	}
	if w.Bus != nil {
		_ = w.Bus.Close(ctx)
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
	EmailSvc      notification.EmailService
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

	// In-process event bus. There is no broker: events are best-effort,
	// at-most-once and lost on restart. Durable state lives in Postgres and
	// the nightly safety-net scan is the recovery path. See pkg/eventbus.
	bus := eventbus.New(eventbus.Config{Logger: logger})
	logger.Info("eventbus: using in-process backend",
		"backend", bus.Backend(),
		"guarantees", "best-effort, at-most-once, single-process, no replay")

	exposureRepo := pricing.NewExposureRepository(deps.DB)
	exposureChecker := pricing.NewExposureChecker(deps.DB)
	exposureAudit := &exposureAuditAdapter{auditLog: deps.AuditLog}

	exposureScanner := pricing.NewExposureScanner(
		exposureRepo, deps.EscalatorRepo, deps.QuoteRepo, exposureAudit, deps.DB, logger,
	).WithEventBus(bus)

	exposureSvc := pricing.NewExposureService(
		exposureRepo, deps.EscalatorRepo, deps.QuoteRepo, exposureAudit, exposureChecker, logger,
	).WithEventBus(bus)

	registerExposureRoutes(deps.Mux, exposureRoutes{
		Scanner:    exposureScanner,
		Checker:    exposureChecker,
		Exposure:   exposureRepo,
		Service:    exposureSvc,
		Escalators: deps.EscalatorRepo,
		DB:         deps.DB,
		Logger:     logger,
	})

	// DRAFT → SENT: freeze the index baseline, customer policy and threshold
	// onto each commodity line. Best-effort inside quote.Service.
	if deps.QuoteSvc != nil {
		deps.QuoteSvc.WithSnapshotService(
			pricing.NewSnapshotService(deps.EscalatorRepo, exposureRepo, deps.QuoteRepo, deps.DB, logger),
		)
	}

	// Pre-ship gate: confirm/fulfill an order, or assign it to a delivery
	// route, is blocked while the source quote sits in ACK_REQUIRED/BLOCKED.
	if deps.OrderSvc != nil {
		deps.OrderSvc.WithExposureGate(exposureChecker, &exposureOverriderAdapter{svc: exposureSvc})
	}
	if deps.DeliverySvc != nil {
		deps.DeliverySvc.WithExposureGate(exposureChecker)
	}

	// Notifications: turn exposure events into salesperson alerts + customer
	// notices. Subscribed on the wildcard subject so every exposure subject
	// routes through one consumer.
	if deps.EmailSvc != nil {
		notifier := notification.NewExposureNotifier(deps.EmailSvc, deps.DB, logger)
		if err := bus.Subscribe(eventbus.SubjectExposureAll, "exposure-notifier", notifier.Handle); err != nil {
			logger.Error("exposure notifier subscription failed; index alerts will not be emailed", "error", err)
		}
	}

	// Nightly safety net. Off by default; an operator enables it by setting
	// exposure.enabled = "true" in system_settings. It re-evaluates every open
	// commodity quote against current index values, which is how the system
	// recovers from an event this bus dropped or a refresh that raced a
	// restart.
	scheduler := quote.NewExposureScheduler(deps.DB, exposureScanner)
	if err := scheduler.Start(context.Background()); err != nil {
		logger.Error("exposure safety-net scheduler failed to start", "error", err)
	}

	return &ExposureWiring{Bus: bus, Scheduler: scheduler}
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
	// portfolio report, admin scan trigger.
	pricing.NewExposureHandler(r.Scanner, r.Checker, r.Exposure, r.Service).
		RegisterRoutes(mux, middleware.RequireRole("admin", "owner", "sales"))

	// Buyer/admin surface: index refresh (+ dry-run preview), metadata edit,
	// history time-series. Deliberately does NOT re-register
	// GET /api/v1/market-indices — that belongs to pricing.EscalatorHandler and
	// a duplicate pattern would panic the ServeMux.
	pricing.NewIndexAdminHandler(r.Escalators, r.Exposure, r.Scanner, r.DB, r.Logger).
		RegisterRoutes(mux, middleware.RequireRole("admin", "owner"))
}

// exposureAuditAdapter bridges the async pkg/audit.Logger to the narrow
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

// exposureOverriderAdapter bridges pricing.ExposureService.OverrideForOrder
// (which returns the created event) to order.ExposureOverrider (error-only).
type exposureOverriderAdapter struct {
	svc *pricing.ExposureService
}

func (a *exposureOverriderAdapter) OverrideForOrder(ctx context.Context, orderID uuid.UUID, notes, actor, role string) error {
	_, err := a.svc.OverrideForOrder(ctx, orderID, notes, actor, role)
	return err
}
