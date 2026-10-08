// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package outbox

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/gablelbm/gable/internal/testutil"
	"github.com/gablelbm/gable/pkg/database"
	"github.com/gablelbm/gable/pkg/eventbus"
)

// writeAged writes n events dated age ago and returns their positions in
// order. The writer stamps `at` from the event, so an aged row is a row the
// retention window has already passed.
func writeAged(t *testing.T, db *database.DB, n int, age time.Duration) []int64 {
	t.Helper()
	w := NewWriter(db, "")
	var positions []int64
	for i := 0; i < n; i++ {
		ev := testEvent(t, eventbus.SubjectExposureFlagged)
		ev.At = time.Now().UTC().Add(-age)
		if err := w.Write(context.Background(), ev); err != nil {
			t.Fatalf("write aged event: %v", err)
		}
		positions = append(positions, maxPosition(t, db))
	}
	return positions
}

func setCursor(t *testing.T, db *database.DB, subscriber string, position int64) {
	t.Helper()
	if _, err := db.Pool.Exec(context.Background(),
		`INSERT INTO event_subscriber_cursors (subscriber, position) VALUES ($1, $2)
		 ON CONFLICT (subscriber) DO UPDATE SET position = EXCLUDED.position`,
		subscriber, position); err != nil {
		t.Fatalf("set cursor %s: %v", subscriber, err)
	}
}

func parkPosition(t *testing.T, db *database.DB, subscriber string, position int64) {
	t.Helper()
	if _, err := db.Pool.Exec(context.Background(),
		`INSERT INTO event_subscriber_parked (subscriber, position, event_id, type, attempts)
		 SELECT $1, position, event_id, type, 10 FROM events_outbox WHERE position = $2`,
		subscriber, position); err != nil {
		t.Fatalf("park position %d: %v", position, err)
	}
}

// remaining lists the positions still in the outbox, in order.
func remaining(t *testing.T, db *database.DB) []int64 {
	t.Helper()
	rows, err := db.Pool.Query(context.Background(), `SELECT position FROM events_outbox ORDER BY position`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var out []int64
	for rows.Next() {
		var p int64
		if err := rows.Scan(&p); err != nil {
			t.Fatal(err)
		}
		out = append(out, p)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return out
}

func equalPositions(a, b []int64) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

const testRetention = 24 * time.Hour

// Rows older than the age and behind every subscriber cursor are deleted;
// a row inside the age window is kept even when every cursor is past it.
func TestPurge_DeletesOldRowsBehindEveryCursor(t *testing.T) {
	testutil.LockOutboxTables(t)
	db := testutil.RequireDB(t)
	cleanOutbox(t, db)

	old := writeAged(t, db, 3, 48*time.Hour)
	fresh := writeAged(t, db, 1, time.Minute)
	setCursor(t, db, "purge-a", fresh[0])
	setCursor(t, db, "purge-b", fresh[0])

	n, err := PurgeAll(context.Background(), db.Pool, time.Now().Add(-testRetention), 100)
	if err != nil {
		t.Fatal(err)
	}
	if n != int64(len(old)) {
		t.Errorf("purged %d rows, want %d", n, len(old))
	}
	if got := remaining(t, db); !equalPositions(got, fresh) {
		t.Errorf("remaining = %v, want only the fresh row %v", got, fresh)
	}
}

// A lagging subscriber's cursor is the floor: rows past it are rows the
// subscriber still needs, however old, and are kept. The row AT the cursor
// was delivered and is purgeable.
func TestPurge_KeepsRowsALaggingSubscriberStillNeeds(t *testing.T) {
	testutil.LockOutboxTables(t)
	db := testutil.RequireDB(t)
	cleanOutbox(t, db)

	pos := writeAged(t, db, 5, 48*time.Hour)
	setCursor(t, db, "purge-fast", pos[4])
	setCursor(t, db, "purge-lagging", pos[1]) // delivered through the second row

	if _, err := PurgeAll(context.Background(), db.Pool, time.Now().Add(-testRetention), 100); err != nil {
		t.Fatal(err)
	}
	if got, want := remaining(t, db), pos[2:]; !equalPositions(got, want) {
		t.Errorf("remaining = %v, want %v (everything past the lagging cursor)", got, want)
	}

	// The lagging subscriber catches up: the rest becomes purgeable.
	setCursor(t, db, "purge-lagging", pos[4])
	if _, err := PurgeAll(context.Background(), db.Pool, time.Now().Add(-testRetention), 100); err != nil {
		t.Fatal(err)
	}
	if got := remaining(t, db); len(got) != 0 {
		t.Errorf("remaining = %v after the lagging subscriber caught up, want none", got)
	}
}

// A parked row's cursor has moved past it, so the cursor floor alone would
// let it go. Its event is kept for as long as a parked entry names it, and
// becomes purgeable once the operator resolves (deletes) the entry.
func TestPurge_KeepsAParkedRowsEventUntilResolved(t *testing.T) {
	testutil.LockOutboxTables(t)
	db := testutil.RequireDB(t)
	cleanOutbox(t, db)

	pos := writeAged(t, db, 3, 48*time.Hour)
	setCursor(t, db, "purge-parker", pos[2])
	parkPosition(t, db, "purge-parker", pos[1])

	if _, err := PurgeAll(context.Background(), db.Pool, time.Now().Add(-testRetention), 100); err != nil {
		t.Fatal(err)
	}
	if got, want := remaining(t, db), []int64{pos[1]}; !equalPositions(got, want) {
		t.Fatalf("remaining = %v, want only the parked row's event %v", got, want)
	}

	if _, err := db.Pool.Exec(context.Background(),
		`DELETE FROM event_subscriber_parked WHERE subscriber = 'purge-parker'`); err != nil {
		t.Fatal(err)
	}
	if _, err := PurgeAll(context.Background(), db.Pool, time.Now().Add(-testRetention), 100); err != nil {
		t.Fatal(err)
	}
	if got := remaining(t, db); len(got) != 0 {
		t.Errorf("remaining = %v after the parked entry was resolved, want none", got)
	}
}

// With no registered subscriber nothing is undelivered, so age alone decides.
func TestPurge_NoSubscribersAgeAloneDecides(t *testing.T) {
	testutil.LockOutboxTables(t)
	db := testutil.RequireDB(t)
	cleanOutbox(t, db)

	writeAged(t, db, 2, 48*time.Hour)
	fresh := writeAged(t, db, 1, time.Minute)

	if _, err := PurgeAll(context.Background(), db.Pool, time.Now().Add(-testRetention), 100); err != nil {
		t.Fatal(err)
	}
	if got := remaining(t, db); !equalPositions(got, fresh) {
		t.Errorf("remaining = %v, want only the fresh row %v", got, fresh)
	}
}

// The purge works in batches: PurgeOnce deletes at most batch rows, and
// PurgeAll loops until a batch comes back short.
func TestPurge_BatchLoop(t *testing.T) {
	testutil.LockOutboxTables(t)
	db := testutil.RequireDB(t)
	cleanOutbox(t, db)

	pos := writeAged(t, db, 5, 48*time.Hour)
	setCursor(t, db, "purge-batch", pos[4])
	cutoff := time.Now().Add(-testRetention)

	n, err := PurgeOnce(context.Background(), db.Pool, cutoff, 2)
	if err != nil {
		t.Fatal(err)
	}
	if n != 2 {
		t.Fatalf("one batch deleted %d rows, want 2", n)
	}
	if got, want := remaining(t, db), pos[2:]; !equalPositions(got, want) {
		t.Fatalf("remaining = %v, want the oldest two gone: %v", got, want)
	}

	n, err = PurgeAll(context.Background(), db.Pool, cutoff, 2)
	if err != nil {
		t.Fatal(err)
	}
	if n != 3 {
		t.Errorf("PurgeAll deleted %d more rows, want 3 across two batches", n)
	}
	if got := remaining(t, db); len(got) != 0 {
		t.Errorf("remaining = %v, want none", got)
	}
}

// A cancelled context stops the loop between batches, with the rows deleted
// so far counted and the error reported.
func TestPurge_AllStopsOnCancelledContext(t *testing.T) {
	testutil.LockOutboxTables(t)
	db := testutil.RequireDB(t)
	cleanOutbox(t, db)
	writeAged(t, db, 3, 48*time.Hour)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	n, err := PurgeAll(ctx, db.Pool, time.Now().Add(-testRetention), 1)
	if err == nil {
		t.Error("PurgeAll on a cancelled context returned no error")
	}
	if n != 0 {
		t.Errorf("deleted %d rows on a cancelled context, want 0", n)
	}
	if got := remaining(t, db); len(got) != 3 {
		t.Errorf("remaining = %v, want all three rows untouched", got)
	}
}

// The runner purges on start and on its ticker, Stop waits for the pass and
// ends the loop (no purge runs after it), Stop twice and Stop on a runner
// never started are no-ops, and starting twice is an error.
func TestPurgeRunner_StartStopLifecycle(t *testing.T) {
	testutil.LockOutboxTables(t)
	db := testutil.RequireDB(t)
	cleanOutbox(t, db)

	r := NewPurgeRunner(db, quietLogger(), testRetention)
	r.interval = 20 * time.Millisecond
	r.batch = 2
	NewPurgeRunner(db, quietLogger(), testRetention).Stop() // never started

	writeAged(t, db, 3, 48*time.Hour)
	if err := r.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := r.Start(context.Background()); err == nil {
		t.Error("starting twice returned no error")
	}
	deadline := time.Now().Add(5 * time.Second)
	for len(remaining(t, db)) != 0 {
		if time.Now().After(deadline) {
			t.Fatalf("the runner did not purge the aged rows: %v remain", remaining(t, db))
		}
		time.Sleep(10 * time.Millisecond)
	}

	r.Stop()
	r.Stop()

	// After Stop no pass runs: an aged row written now outlives many ticks.
	writeAged(t, db, 1, 48*time.Hour)
	time.Sleep(150 * time.Millisecond)
	if got := remaining(t, db); len(got) != 1 {
		t.Errorf("remaining = %v after Stop, want the new aged row untouched", got)
	}
}

// A zero retention disables the runner: Start succeeds, nothing is deleted.
func TestPurgeRunner_ZeroRetentionIsDisabled(t *testing.T) {
	testutil.LockOutboxTables(t)
	db := testutil.RequireDB(t)
	cleanOutbox(t, db)
	writeAged(t, db, 1, 48*time.Hour*365)

	r := NewPurgeRunner(db, quietLogger(), 0)
	r.interval = 10 * time.Millisecond
	if err := r.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	time.Sleep(100 * time.Millisecond)
	r.Stop()
	if got := remaining(t, db); len(got) != 1 {
		t.Errorf("remaining = %v with retention 0, want the row kept", got)
	}
}

// The purge is one statement per batch and opens no transaction of its own,
// but it runs beside the writer and the drain on one pool, so the pool 4,
// three contenders rule is proved anyway: three concurrent purges (the
// worker plus two overlapping replicas) beside a writer and a drain pass,
// every aged row deleted exactly once across them (SKIP LOCKED keeps the
// batches disjoint) and no contender stalls or errors.
func TestPurge_Pool4ThreeContenders(t *testing.T) {
	testutil.LockOutboxTables(t)
	db := requireDBMaxConns(t, 4)
	cleanOutbox(t, db)
	ctx := context.Background()

	const aged = 60
	pos := writeAged(t, db, aged, 48*time.Hour)
	setCursor(t, db, "purge-pool4", pos[aged-1])
	cutoff := time.Now().Add(-testRetention)

	var wg sync.WaitGroup
	counts := make(chan int64, 3)
	for i := 0; i < 3; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			n, err := PurgeAll(ctx, db.Pool, cutoff, 7)
			if err != nil {
				t.Errorf("contending purge: %v", err)
			}
			counts <- n
		}()
	}
	wg.Add(2)
	go func() {
		defer wg.Done()
		if err := NewWriter(db, "").Write(ctx, testEvent(t, "order.confirmed")); err != nil {
			t.Errorf("contending writer: %v", err)
		}
	}()
	go func() {
		defer wg.Done()
		d := NewDrainRunner(db, quietLogger())
		d.Subscribe("order.*", "purge-pool4-drain", func(context.Context, eventbus.Event) error { return nil })
		d.ensureCursors(ctx, d.snapshotSubs())
		d.pass(ctx)
	}()
	wg.Wait()
	close(counts)

	var total int64
	for n := range counts {
		total += n
	}
	if total != aged {
		t.Errorf("contenders deleted %d rows in total, want exactly %d", total, aged)
	}
	for _, p := range remaining(t, db) {
		for _, old := range pos {
			if p == old {
				t.Errorf("aged position %d survived the purge", p)
			}
		}
	}
}
