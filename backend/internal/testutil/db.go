// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

// Package testutil holds helpers shared by the Go test suite.
//
// Its main job is RequireDB: database-backed integration tests must degrade to
// a clean skip when Postgres is unreachable — the default state of a fresh
// clone and of CI's plain `go test ./...` — instead of failing the build. The
// skip is driven by an actual connection probe, not by `testing.Short()`, so
// the default (no -short) run is the case that gets fixed.
package testutil

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/gablelbm/gable/internal/config"
	"github.com/gablelbm/gable/pkg/database"
	"github.com/jackc/pgx/v5/pgxpool"
)

// SkipReason is the message every DB-gated test skips with. Kept as one string
// so the reason a fresh clone shows skips is greppable and consistent.
const SkipReason = "postgres unavailable: set DATABASE_URL to run integration tests"

// RequireDBEnv, when set to a truthy value, turns the skip into a failure.
// CI jobs that DO stand up Postgres should set it, otherwise a broken database
// service silently degrades the integration suite to a no-op.
const RequireDBEnv = "GABLE_TEST_REQUIRE_DB"

// probeTimeoutEnv overrides how long the reachability probe waits.
const probeTimeoutEnv = "GABLE_TEST_DB_TIMEOUT"

const defaultProbeTimeout = 3 * time.Second

func probeTimeout() time.Duration {
	if v := os.Getenv(probeTimeoutEnv); v != "" {
		if d, err := time.ParseDuration(v); err == nil && d > 0 {
			return d
		}
	}
	return defaultProbeTimeout
}

func dbRequired() bool {
	switch os.Getenv(RequireDBEnv) {
	case "", "0", "false", "FALSE", "no":
		return false
	default:
		return true
	}
}

// unavailable ends the test: a skip normally, a failure when the caller has
// declared (via GABLE_TEST_REQUIRE_DB) that a database must be present.
func unavailable(t *testing.T, err error) {
	t.Helper()
	if dbRequired() {
		t.Fatalf("%s is set but the database is unreachable: %v", RequireDBEnv, err)
	}
	t.Skipf("%s (%v)", SkipReason, err)
}

// RequireDB returns a live database handle for an integration test, or skips
// the test when Postgres cannot be reached.
//
// The pool is built with the same parameters database.Connect uses, so a test
// running against a reachable database behaves exactly as it did before this
// helper existed. The only difference is the connect timeout, which is bounded
// so an unreachable host fails fast instead of stalling the suite.
//
// The returned handle is closed automatically via t.Cleanup.
func RequireDB(t *testing.T) *database.DB {
	t.Helper()

	cfg, err := config.Load()
	if err != nil {
		// Without a config we cannot even address a database, so this is the
		// same "no database here" condition rather than a code failure.
		unavailable(t, err)
		return nil
	}

	poolCfg, err := pgxpool.ParseConfig(cfg.DatabaseURL)
	if err != nil {
		unavailable(t, err)
		return nil
	}

	pc := database.DefaultPoolConfig()
	poolCfg.MaxConns = pc.MaxConns
	poolCfg.MinConns = pc.MinConns
	poolCfg.MaxConnLifetime = pc.MaxConnLifetime
	poolCfg.MaxConnIdleTime = pc.MaxConnIdleTime
	poolCfg.HealthCheckPeriod = pc.HealthCheckPeriod
	poolCfg.ConnConfig.ConnectTimeout = probeTimeout()

	ctx, cancel := context.WithTimeout(context.Background(), probeTimeout())
	defer cancel()

	pool, err := pgxpool.NewWithConfig(ctx, poolCfg)
	if err != nil {
		unavailable(t, err)
		return nil
	}

	// Ping is the real reachability check: it proves a session can be opened
	// and authenticated, not merely that a TCP port accepted a connection.
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		unavailable(t, err)
		return nil
	}

	db := &database.DB{Pool: pool}
	t.Cleanup(db.Close)
	return db
}
