// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package testutil

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gablelbm/gable/pkg/database"
	"github.com/google/uuid"
)

// branchDayLockKey is the advisory lock key behind the default branch's own
// day. A test that dates its fixtures by the branch's day holds it shared, so
// the day cannot move under it; a test that changes the default branch's zone
// holds it exclusively. The key is the ASCII of "branchday" masked to 63 bits
// (so it reads as "ranchday" in hex), which clashes with no other key here.
//
// Other packages' tests do not take this lock. That is safe today because no
// test outside the ones that call LockBranchDay compares a day it stamped
// through the default branch's zone with a day read at another moment (the
// migration, characterization and seed tests use scratch databases). A new leg
// that reads the default branch's day must call LockBranchDay, as a test that
// touches the outbox calls LockOutboxTables.
//
// Lock order: take the branch day before the outbox lock where a test needs
// both, so two tests cannot hold them the other way round. Each lock sits on
// its own connection, which Postgres's deadlock detector does not see.
const branchDayLockKey int64 = 0x6272616e6368646179 & 0x7fffffffffffffff

// AlwaysBehindUTC is a zone on an earlier day than UTC at every hour: UTC
// minus 25 hours (a POSIX offset, which Postgres accepts). A fixture dated by
// UTC's day falls AFTER such a branch's day, which is the failure the dashboard
// legs had; a branch ahead of UTC never shows it.
const AlwaysBehindUTC = "<-25>+25"

// foreignZones are the zones the tables put the default branch in; a database
// found in one of them was left there by a test that died before its cleanup.
var foreignZones = map[string]bool{
	"Pacific/Kiritimati": true, "Pacific/Pago_Pago": true, AlwaysBehindUTC: true,
}

// BranchDaySQL is the default branch's business date as the dashboard and the
// aging read it: now in the default branch's own zone (UTC when it has none).
// A fixture that dates a document "today" uses this, never CURRENT_DATE or the
// Go clock, which are UTC on the CI runner.
const BranchDaySQL = `(NOW() AT TIME ZONE COALESCE((SELECT l.timezone FROM locations l
	WHERE l.id::text = (SELECT value FROM system_settings WHERE key = 'default_branch_id')), 'UTC'))::date`

// zoneHeld is true while a test of this process holds the branch day
// exclusively (it changed the zone): its own fixtures must not wait for a
// shared lock behind it. It is process wide, so it is correct only while the
// package's tests run one at a time (no t.Parallel test reads the branch day).
var zoneHeld atomic.Bool

// LockBranchDay holds the branch day shared for the test's life. Call it
// before the fixture is dated, as LockOutboxTables is called.
func LockBranchDay(t *testing.T) {
	t.Helper()
	if !zoneHeld.CompareAndSwap(false, true) {
		return
	}
	t.Cleanup(func() { zoneHeld.Store(false) })
	lockKeyMode(t, branchDayLockKey, "branch day", true)
}

// BranchDay is the default branch's business date now.
func BranchDay(t *testing.T, db *database.DB) time.Time {
	t.Helper()
	var day time.Time
	if err := db.Pool.QueryRow(context.Background(), `SELECT `+BranchDaySQL).Scan(&day); err != nil {
		t.Fatalf("the branch's day: %v", err)
	}
	return day
}

// SetBranchDayZone gives the default branch the IANA zone for the test and
// puts the old one back at its end. It holds the branch day exclusively, so no
// day-dated test in any package runs while the zone is not the stored one.
func SetBranchDayZone(t *testing.T, db *database.DB, zone string) {
	t.Helper()
	lockKeyMode(t, branchDayLockKey, "branch day", false)
	zoneHeld.Store(true)
	t.Cleanup(func() { zoneHeld.Store(false) })
	ctx := context.Background()
	var id, old string
	if err := db.Pool.QueryRow(ctx, `SELECT l.id::text, l.timezone FROM locations l
		WHERE l.id::text = (SELECT value FROM system_settings WHERE key = 'default_branch_id')`).Scan(&id, &old); err != nil {
		t.Fatalf("the default branch: %v", err)
	}
	set := func(z string) {
		if _, err := db.Pool.Exec(context.Background(), `UPDATE locations SET timezone = $2 WHERE id::text = $1`, id, z); err != nil {
			t.Errorf("set the branch zone to %s: %v", z, err)
		}
	}
	// a test binary killed on its timeout runs no cleanup and can leave a
	// persistent test database in one of these zones: put the usual one back
	if foreignZones[old] {
		old = "America/New_York"
		set(old)
	}
	t.Cleanup(func() { set(old) })
	set(zone)
}

// SetBranchDayZoneOn gives the branch the test owns the IANA zone for the
// test and puts the old one back at its end.
//
// A test whose fixture stands on a branch of its own dates its documents by
// that branch's zone: the shared default branch's zone is not the branch the
// fixture's sales are read by, and writing it would move a row every other
// package's fixture reads.
func SetBranchDayZoneOn(t *testing.T, db *database.DB, branchID uuid.UUID, zone string) {
	t.Helper()
	ctx := context.Background()
	var old string
	if err := db.Pool.QueryRow(ctx, `SELECT timezone FROM locations WHERE id = $1`, branchID).Scan(&old); err != nil {
		t.Fatalf("the branch %s: %v", branchID, err)
	}
	set := func(z string) {
		if _, err := db.Pool.Exec(context.Background(), `UPDATE locations SET timezone = $2 WHERE id = $1`, branchID, z); err != nil {
			t.Errorf("set the branch zone to %s: %v", z, err)
		}
	}
	t.Cleanup(func() { set(old) })
	set(zone)
}
