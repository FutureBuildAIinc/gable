// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package seed

import (
	"context"
	"database/sql"
	"os"
	"os/exec"
	"strings"
	"testing"
)

// The seed once guard.
//
// The demo seed TRUNCATEs the transactional tables before it writes (see
// AGENTS.md, "Seed resets transactional data each run"). That is right for a
// developer's local stack, and wrong for a demo tenant that lives behind a
// deployment platform: every redeploy of the same commit would wipe what the
// prospect was looking at.
//
// So the seed can be asked to run once. It writes a marker when it finishes,
// and a later run finds the marker and skips, leaving the data alone. The
// operator can always ask for a reseed on purpose.
//
// The three facts this test states:
//  1. the first run seeds (the seed's own rows are there and the marker is
//     written);
//  2. the second run skips (a row written after the first seed survives);
//  3. a forced run reseeds (that row is gone again).
func TestSeedRunsOnceAndForceReseeds(t *testing.T) {
	if testing.Short() {
		t.Skip("seeds a scratch database of its own; skipped under -short")
	}
	db, dbURL := scratchSeedDB(t)
	runSeedCommand(t, dbURL, "./cmd/migrate")

	// 1. The first run seeds and writes the marker.
	first := runSeedWithEnv(t, dbURL, "DEMO_SEED_ONCE=1")
	if !strings.Contains(first, "DATABASE SEEDING COMPLETE") {
		t.Fatalf("the first run did not seed:\n%s", first)
	}
	marker, ok := readMarker(t, db)
	if !ok {
		t.Fatalf("the first run wrote no %s marker", seedMarkerKey)
	}
	if marker == "" {
		t.Fatalf("the %s marker is empty", seedMarkerKey)
	}
	if got := countRows(t, db, "pos_sync_log"); got == 0 {
		t.Fatal("the first run wrote no demo rows")
	}

	// A row written after the seed: the seed truncates pos_sync_log on every
	// run it makes, so this row's survival is what tells a skip from a reseed.
	insertProbeRow(t, db)

	// 2. The second run skips: the row survives and the marker is unchanged.
	second := runSeedWithEnv(t, dbURL, "DEMO_SEED_ONCE=1")
	if !strings.Contains(second, seedSkipMessage) {
		t.Fatalf("the second run did not say it skipped:\n%s", second)
	}
	if got := countRows(t, db, "pos_sync_log"); got != 3 {
		t.Fatalf("pos_sync_log holds %d rows after the skipped run, want the seed's 2 plus the probe row", got)
	}
	if again, _ := readMarker(t, db); again != marker {
		t.Fatalf("the marker changed on the skipped run: %q became %q", marker, again)
	}

	// 3. A forced run reseeds: the row is gone.
	forced := runSeedWithEnv(t, dbURL, "DEMO_SEED_ONCE=1", "DEMO_SEED_FORCE=1")
	if !strings.Contains(forced, "DATABASE SEEDING COMPLETE") {
		t.Fatalf("the forced run did not seed:\n%s", forced)
	}
	if got := countRows(t, db, "pos_sync_log"); got != 2 {
		t.Fatalf("pos_sync_log holds %d rows after the forced run, want the seed's 2 and no probe row", got)
	}
}

// runSeedWithEnv runs the demo seed against the scratch database with the
// demo gate on and the given environment values added, and answers what the
// command printed.
func runSeedWithEnv(t *testing.T, dbURL string, env ...string) string {
	t.Helper()
	cmd := exec.Command("go", "run", "./cmd/seed")
	cmd.Dir = "../../.."
	cmd.Env = append(os.Environ(), "DATABASE_URL="+dbURL, demoSeedEnv+"=1")
	cmd.Env = append(cmd.Env, env...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("go run ./cmd/seed: %v\n%s", err, out)
	}
	return string(out)
}

// readMarker reads the seed marker from the scratch database and answers its
// value and whether it is there at all. A read that fails is a failed test,
// not an absent marker.
func readMarker(t *testing.T, db *sql.DB) (string, bool) {
	t.Helper()
	var value string
	err := db.QueryRowContext(context.Background(),
		`SELECT value FROM system_settings WHERE key = $1`, seedMarkerKey).Scan(&value)
	if err != nil {
		if err == sql.ErrNoRows {
			return "", false
		}
		t.Fatalf("reading the %s marker: %v", seedMarkerKey, err)
	}
	return value, true
}

// insertProbeRow writes one row into a table the seed truncates on every run.
func insertProbeRow(t *testing.T, db *sql.DB) {
	t.Helper()
	if _, err := db.ExecContext(context.Background(),
		`INSERT INTO pos_sync_log (batch_id, register_id, synced_count) VALUES ('probe-batch', 'PROBE', 1)`); err != nil {
		t.Fatalf("writing the probe row: %v", err)
	}
}

// countRows counts the rows of one table.
func countRows(t *testing.T, db *sql.DB, table string) int {
	t.Helper()
	var n int
	if err := db.QueryRowContext(context.Background(), `SELECT COUNT(*) FROM `+table).Scan(&n); err != nil {
		t.Fatalf("counting %s: %v", table, err)
	}
	return n
}
