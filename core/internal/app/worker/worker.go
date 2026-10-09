// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

// Package worker is the worker role of the core binary: the background
// jobs that do not belong to the HTTP server. Today that is the
// idempotency retention purge, which R1-4 moved out of serve, the
// outbox drain (R1-12), which delivers committed events to their
// subscribers, and the outbox retention purge (R1-12b); the cron schedulers that serve mounts beside its own HTTP
// surface (auto-reorder and scheduled report delivery) stay in serve.
package worker

import (
	"context"
	"log/slog"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/gablelbm/gable/internal/account"
	"github.com/gablelbm/gable/internal/app/orderwire"
	"github.com/gablelbm/gable/internal/config"
	"github.com/gablelbm/gable/internal/customer"
	"github.com/gablelbm/gable/internal/gl"
	"github.com/gablelbm/gable/internal/inventory"
	"github.com/gablelbm/gable/internal/invoice"
	"github.com/gablelbm/gable/internal/notification"
	"github.com/gablelbm/gable/internal/order"
	"github.com/gablelbm/gable/internal/pricing"
	"github.com/gablelbm/gable/internal/quote"
	"github.com/gablelbm/gable/pkg/audit"
	"github.com/gablelbm/gable/pkg/database"
	"github.com/gablelbm/gable/pkg/eventbus"
	"github.com/gablelbm/gable/pkg/middleware"
	"github.com/gablelbm/gable/pkg/outbox"
)

// Run starts the worker's jobs and blocks until SIGINT or SIGTERM, then
// shuts down in the same graceful order as serve: the jobs stop (an
// in-flight run drains) before the database pool closes, so no statement
// can start against a closing pool.
func Run() {
	// Setup structured logging exactly as serve does, so a deployment's
	// logs are shaped the same whichever role wrote them.
	logLevel := new(slog.LevelVar)
	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: logLevel}))
	slog.SetDefault(logger)

	cfg, err := config.Load()
	if err != nil {
		logger.Error("Configuration error", "error", err)
		os.Exit(1)
	}
	switch strings.ToUpper(cfg.LogLevel) {
	case "DEBUG":
		logLevel.Set(slog.LevelDebug)
	case "WARN":
		logLevel.Set(slog.LevelWarn)
	case "ERROR":
		logLevel.Set(slog.LevelError)
	default:
		logLevel.Set(slog.LevelInfo)
	}

	logger.Info("Starting worker...", "log_level", cfg.LogLevel)

	// Register the signal channel before any job starts, so a stop signal
	// that arrives during startup is buffered rather than taking the
	// process's default disposition.
	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)

	db, err := database.Connect(cfg.DatabaseURL, database.PoolConfig{
		MaxConns:          cfg.DBMaxConns,
		MinConns:          cfg.DBMinConns,
		MaxConnLifetime:   time.Duration(cfg.DBMaxConnLifetime) * time.Minute,
		MaxConnIdleTime:   30 * time.Minute,
		HealthCheckPeriod: 1 * time.Minute,
	})
	if err != nil {
		logger.Error("Failed to connect to database", "error", err)
		os.Exit(1)
	}
	defer db.Close()
	logger.Info("Connected to database")

	// Idempotency retention: purge expired idempotency_keys rows nightly so
	// completed replays and lapsed claims do not accumulate. On by default
	// (the middleware writes a row per keyed POST/PUT); opt out with
	// idempotency.purge_enabled=false in system_settings. This scheduler ran
	// inside serve until R1-4 moved it here; serve keeps no copy. A start
	// failure is logged, not fatal, exactly as serve treated it.
	idempotencyScheduler := middleware.NewIdempotencyScheduler(db)
	if err := idempotencyScheduler.Start(context.Background()); err != nil {
		logger.Error("idempotency purge scheduler failed to start", "error", err)
	}

	// Outbox drain: deliver committed events (pkg/outbox) to the registered
	// subscribers, past each subscriber's cursor. The exposure notifier is the
	// subscriber today; it dedups on the outbox event id. The drain runs only
	// here, so serve replicas never contend for the cursors. A start failure
	// is logged, not fatal, as for the scheduler above.
	orderSvc := newOrderService(db, cfg)
	drain := newOutboxDrain(db, logger, orderSvc)
	drainErr := drain.Start(context.Background())
	if drainErr != nil {
		logger.Error("outbox drain failed to start; event subscribers receive nothing until it runs", "error", drainErr)
	}

	// Outbox retention: delete events_outbox rows past OUTBOX_RETENTION_DAYS
	// that every subscriber cursor has moved beyond and no parked entry
	// names. Runs only here, beside the drain whose cursors it reads. When
	// the drain failed to start no cursor may exist yet, and with no cursor
	// age alone decides, so the purge is skipped rather than risk deleting
	// rows a subscriber has not seen; the outbox grows until the worker
	// restarts with a working drain.
	purge := newOutboxPurge(db, cfg, logger)
	if drainErr != nil {
		logger.Error("outbox purge skipped because the outbox drain did not start; the outbox grows until the worker restarts")
	} else if err := purge.Start(context.Background()); err != nil {
		logger.Error("outbox purge failed to start; the outbox grows until it runs", "error", err)
	}

	// The order queues (ADR 0005 5.4): the back order release, served oldest
	// first, one order per transaction. It runs only here, beside the drain
	// whose subscriber fills it, never in serve.
	allocation := newQueueRunner("order-allocation", orderSvc.ServeAllocationRequest, logger)
	allocation.Start()
	// The fulfilment requests of completed deliveries (ADR 0005 5.5), oldest
	// first; a failure is counted on the request and parks it after ten.
	fulfilment := newQueueRunner("order-fulfilment", orderSvc.ServeFulfilmentRequest, logger)
	fulfilment.Start()

	logger.Info("Worker started", "jobs", "idempotency-purge,outbox-drain,outbox-purge,order-allocation,order-fulfilment")

	sig := <-quit
	logger.Info("Shutdown signal received", "signal", sig.String())

	// Step 1: stop the background jobs. Same reasoning as serve's job stops:
	// no purge statement may start against a draining pool, and an in-flight
	// batch (or drain window) finishes before step 2 closes it.
	logger.Info("Shutdown step 1/2: stopping the order queue jobs...")
	allocation.Stop()
	fulfilment.Stop()
	logger.Info("Shutdown step 1/2: stopping outbox purge...")
	purge.Stop()
	logger.Info("Shutdown step 1/2: outbox purge stopped")
	logger.Info("Shutdown step 1/2: stopping outbox drain...")
	drain.Stop()
	logger.Info("Shutdown step 1/2: outbox drain stopped")
	logger.Info("Shutdown step 1/2: stopping idempotency purge scheduler...")
	idempotencyScheduler.Stop()
	logger.Info("Shutdown step 1/2: idempotency purge scheduler stopped")

	// Step 2: close the database pool, last, as in serve.
	logger.Info("Shutdown step 2/2: closing database pool...")
	db.Close()
	logger.Info("Shutdown step 2/2: database pool closed")

	logger.Info("Worker exiting: clean shutdown complete")
}

// newOutboxDrain builds the drain with its subscribers registered. The
// exposure notifier receives every quote.exposure.* event; its email service
// is the log-only LogEmailService, as in serve, until a real sender exists.
func newOutboxDrain(db *database.DB, logger *slog.Logger, orderSvc *order.Service) *outbox.DrainRunner {
	drain := outbox.NewDrainRunner(db, logger)
	notifier := notification.NewExposureNotifier(notification.NewLogEmailService(logger), db, logger)
	drain.Subscribe(eventbus.SubjectExposureAll, "exposure-notifier", notifier.Handle)
	// The order module's subscriber for purchase_order.received queues the
	// back ordered orders that wait on the received products and does nothing
	// else (ADR 0005 5.4).
	drain.Subscribe(order.SubjectPurchaseOrderReceived, "order-allocation-requests",
		func(ctx context.Context, ev eventbus.Event) error {
			return orderSvc.HandlePurchaseOrderReceived(ctx, ev)
		})
	return drain
}

// newOrderService builds the order service the worker's queue jobs and
// subscriber use, through the same constructor serve uses (orderwire.New):
// the outbox, the transaction runner, the audit log, inventory, the invoice
// writer, the price engine and, with them, the configured tax provider behind
// the rate resolver and the exposure gate, so the delivery completions this
// role bills are priced exactly as the desk's fulfilments are.
func newOrderService(db *database.DB, cfg *config.Config) *order.Service {
	accounts := newAccountService(db)
	return orderwire.New(orderwire.Deps{
		DB:         db,
		Config:     cfg,
		Logger:     slog.Default(),
		Inventory:  inventory.NewService(inventory.NewRepository(db)),
		Invoices:   newInvoiceServiceWith(db, accounts),
		Accounts:   accounts,
		Pricing:    pricing.NewService(pricing.NewRepository(db)),
		Customers:  customer.NewService(customer.NewRepository(db)),
		Escalators: pricing.NewEscalatorRepository(db),
		QuoteLines: quote.NewRepository(db),
	})
}

// newInvoiceService builds the invoice service the fulfilment worker writes
// through: the real GL and account ledger, the audit log.
func newInvoiceService(db *database.DB) *invoice.Service {
	return newInvoiceServiceWith(db, newAccountService(db))
}

// newAccountService builds the AR core on the real ledger.
func newAccountService(db *database.DB) *account.Service {
	logger := slog.Default()
	return account.NewService(db, gl.NewService(gl.NewRepository(db), nil, logger), logger)
}

func newInvoiceServiceWith(db *database.DB, accounts *account.Service) *invoice.Service {
	glSvc := gl.NewService(gl.NewRepository(db), nil, slog.Default())
	return invoice.NewService(invoice.NewRepository(db), glSvc, accounts, db).
		WithAuditLog(audit.NewLogger(db))
}

// newOutboxPurge builds the retention job from the configured age in days.
func newOutboxPurge(db *database.DB, cfg *config.Config, logger *slog.Logger) *outbox.PurgeRunner {
	return outbox.NewPurgeRunner(db, logger, time.Duration(cfg.OutboxRetentionDays)*24*time.Hour)
}
