// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

// Package worker is the worker role of the core binary: the background
// jobs that do not belong to the HTTP server. Today that is the
// idempotency retention purge, which R1-4 moved out of serve, and the
// outbox drain (R1-12), which delivers committed events to their
// subscribers; the cron schedulers that serve mounts beside its own HTTP
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

	"github.com/gablelbm/gable/internal/config"
	"github.com/gablelbm/gable/internal/notification"
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
	drain := newOutboxDrain(db, logger)
	if err := drain.Start(context.Background()); err != nil {
		logger.Error("outbox drain failed to start; event subscribers receive nothing until it runs", "error", err)
	}

	logger.Info("Worker started", "jobs", "idempotency-purge,outbox-drain")

	sig := <-quit
	logger.Info("Shutdown signal received", "signal", sig.String())

	// Step 1: stop the background jobs. Same reasoning as serve's job stops:
	// no purge statement may start against a draining pool, and an in-flight
	// batch (or drain window) finishes before step 2 closes it.
	logger.Info("Shutdown step 1/2: stopping outbox drain and idempotency purge scheduler...")
	drain.Stop()
	idempotencyScheduler.Stop()
	logger.Info("Shutdown step 1/2: outbox drain and idempotency purge scheduler stopped")

	// Step 2: close the database pool, last, as in serve.
	logger.Info("Shutdown step 2/2: closing database pool...")
	db.Close()
	logger.Info("Shutdown step 2/2: database pool closed")

	logger.Info("Worker exiting: clean shutdown complete")
}

// newOutboxDrain builds the drain with its subscribers registered. The
// exposure notifier receives every quote.exposure.* event; its email service
// is the log-only LogEmailService, as in serve, until a real sender exists.
func newOutboxDrain(db *database.DB, logger *slog.Logger) *outbox.DrainRunner {
	drain := outbox.NewDrainRunner(db, logger)
	notifier := notification.NewExposureNotifier(notification.NewLogEmailService(logger), db, logger)
	drain.Subscribe(eventbus.SubjectExposureAll, "exposure-notifier", notifier.Handle)
	return drain
}
