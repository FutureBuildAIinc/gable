// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package testutil

import (
	"context"
	"testing"

	"github.com/gablelbm/gable/internal/config"
	"github.com/jackc/pgx/v5"
)

// outboxLockKey is the one advisory lock key every test that reads or writes
// the outbox tables (events_outbox, event_subscriber_cursors,
// event_subscriber_parked) takes for its lifetime.
const outboxLockKey int64 = 0x6f7574626f78 // "outbox"

// LockOutboxTables serialises the calling test against every other test, in
// any package, that touches the outbox tables. `go test ./...` runs packages
// in parallel against one database, and these tests share three tables, one
// feed and a TRUNCATE; the lock makes them run one at a time. Call it first,
// before RequireDB, so the lock is released last.
//
// The lock is a session advisory lock on its own dedicated connection, not a
// pool connection, so it does not count against a test's pool size (the pool 4
// contender tests). The connection closes at the end of the test, which
// releases the lock. Without a database it skips or fails like RequireDB.
func LockOutboxTables(t *testing.T) {
	t.Helper()
	lockKey(t, outboxLockKey, "outbox tables")
}

// terminalLockKey is the advisory lock key every test that sweeps or reads
// the card terminal tables as a whole holds for its body.
const terminalLockKey int64 = 0x7465726d696e6c // "terminl"

// LockTerminalTables serialises the calling test against every other test, in
// any package, that sweeps the card terminal intent ledger (a sweep reaches
// every expired intent of its branch, so two suites sweeping together would
// close each other's legs) or empties its tables. Call it first, before
// RequireDB, so the lock is released last.
func LockTerminalTables(t *testing.T) {
	t.Helper()
	lockKey(t, terminalLockKey, "card terminal tables")
}

// productImageLockKey is the advisory lock key every test that writes the
// catalog.product_images setting row holds for its body.
const productImageLockKey int64 = 0x70726f64696d67 // "prodimg"

// LockProductImageSetting serialises the calling test against every other test,
// in any package, that writes the catalog.product_images row of system_settings
// (one row on a database every package shares). Call it first, as
// LockOutboxTables is called, so the lock is released last.
func LockProductImageSetting(t *testing.T) {
	t.Helper()
	lockKey(t, productImageLockKey, "product image setting")
}

// taxZoneLockKey is the advisory lock key every test that empties or seeds
// the tax zone tables holds for its body.
const taxZoneLockKey int64 = 0x7461787a6f6e65 // "taxzone"

// LockTaxZones serialises the calling test against every other test, in any
// package, that empties or seeds the tax zone tables (CP-10, CP-14): a zone
// lookup reads the whole table, so a truncation in one package's test would
// take the zone another package's test just made. Call it before the test
// touches the tables.
func LockTaxZones(t *testing.T) {
	t.Helper()
	lockKey(t, taxZoneLockKey, "tax zone tables")
}

func lockKey(t *testing.T, key int64, what string) {
	t.Helper()
	lockKeyMode(t, key, what, false)
}

// lockKeyMode takes the advisory lock exclusively, or shared when shared is
// true: shared holders run together and wait only for an exclusive one.
func lockKeyMode(t *testing.T, key int64, what string, shared bool) {
	t.Helper()

	cfg, err := config.Load()
	if err != nil {
		unavailable(t, err)
		return
	}
	connCfg, err := pgx.ParseConfig(cfg.DatabaseURL)
	if err != nil {
		unavailable(t, err)
		return
	}
	connCfg.ConnectTimeout = probeTimeout()

	ctx, cancel := context.WithTimeout(context.Background(), probeTimeout())
	conn, err := pgx.ConnectConfig(ctx, connCfg)
	cancel()
	if err != nil {
		unavailable(t, err)
		return
	}
	t.Cleanup(func() { _ = conn.Close(context.Background()) })

	// pg_advisory_lock waits for the holder; the wait is bounded by the
	// test binary's own timeout, which is what turns a stuck holder into a
	// failure rather than a silent hang.
	sql := `SELECT pg_advisory_lock($1)`
	if shared {
		sql = `SELECT pg_advisory_lock_shared($1)`
	}
	if _, err := conn.Exec(context.Background(), sql, key); err != nil {
		t.Fatalf("take the %s lock: %v", what, err)
	}
}
