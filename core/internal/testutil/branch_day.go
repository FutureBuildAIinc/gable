// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package testutil

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gablelbm/gable/pkg/database"
)

// branchDayLockKey is the advisory lock key behind the default branch's own
// day. A test that dates its fixtures by the branch's day holds it shared, so
// the day cannot move under it; a test that changes the default branch's zone
// holds it exclusively.
const branchDayLockKey int64 = 0x6272616e6368646179 & 0x7fffffffffffffff // "branchday"

// BranchDaySQL is the default branch's business date as the dashboard and the
// aging read it: now in the default branch's own zone (UTC when it has none).
// A fixture that dates a document "today" uses this, never CURRENT_DATE or the
// Go clock, which are UTC on the CI runner.
const BranchDaySQL = `(NOW() AT TIME ZONE COALESCE((SELECT l.timezone FROM locations l
	WHERE l.id::text = (SELECT value FROM system_settings WHERE key = 'default_branch_id')), 'UTC'))::date`

// zoneHeld is true while a test of this process holds the branch day
// exclusively (it changed the zone): its own fixtures must not wait for a
// shared lock behind it. Tests of one package run one at a time.
var zoneHeld atomic.Bool

// LockBranchDay holds the branch day shared for the test's life. Call it
// before the fixture is dated, as LockOutboxTables is called.
func LockBranchDay(t *testing.T) {
	t.Helper()
	if zoneHeld.Load() {
		return
	}
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
	t.Cleanup(func() { set(old) })
	set(zone)
}
