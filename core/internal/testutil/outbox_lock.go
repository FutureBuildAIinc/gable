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
	if _, err := conn.Exec(context.Background(), `SELECT pg_advisory_lock($1)`, outboxLockKey); err != nil {
		t.Fatalf("take the outbox tables lock: %v", err)
	}
}
