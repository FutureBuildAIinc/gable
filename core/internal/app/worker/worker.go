// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

// Package worker is the worker role of the core binary: the background
// jobs that do not belong to the HTTP server. Today that is the
// idempotency retention purge, which R1-4 moved out of serve; the cron
// schedulers that serve mounts beside its own HTTP surface (auto-reorder
// and scheduled report delivery) stay in serve until the outbox item
// (R1-12) gives the worker its drain.
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
	"github.com/gablelbm/gable/pkg/database"
	"github.com/gablelbm/gable/pkg/middleware"
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

	logger.Info("Worker started", "jobs", "idempotency-purge")

	// Wait for interrupt signal using a buffered channel
	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
	sig := <-quit
	logger.Info("Shutdown signal received", "signal", sig.String())

	// Step 1: stop the background jobs. Same reasoning as serve's job stops:
	// no purge statement may start against a draining pool, and an in-flight
	// batch finishes before step 2 closes it.
	logger.Info("Shutdown step 1/2: stopping idempotency purge scheduler...")
	idempotencyScheduler.Stop()
	logger.Info("Shutdown step 1/2: idempotency purge scheduler stopped")

	// Step 2: close the database pool, last, as in serve.
	logger.Info("Shutdown step 2/2: closing database pool...")
	db.Close()
	logger.Info("Shutdown step 2/2: database pool closed")

	logger.Info("Worker exiting: clean shutdown complete")
}
