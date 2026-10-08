// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package outbox

import (
	"context"
	"errors"
	"fmt"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/gablelbm/gable/internal/config"
	"github.com/gablelbm/gable/internal/testutil"
	"github.com/gablelbm/gable/pkg/database"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

// These tests are the item's exit test: a rolled back mutation writes no
// event; a committed one is read exactly once by cursor and again only from
// an earlier cursor; the late commit case is never skipped; and three
// contending writers inside transactions run at pool size 4. They need a
// migrated Postgres (migration 089) and skip, rather than fail, without one
// unless GABLE_TEST_REQUIRE_DB says otherwise.

func skipUnlessDB(t *testing.T, err error) {
	t.Helper()
	if dbRequired() {
		t.Fatalf("%s is set but the database is unreachable: %v", testutil.RequireDBEnv, err)
	}
	t.Skipf("%s (%v)", testutil.SkipReason, err)
}

func dbRequired() bool {
	switch os.Getenv(testutil.RequireDBEnv) {
	case "", "0", "false", "FALSE", "no":
		return false
	default:
		return true
	}
}

// requireDBMaxConns is testutil.RequireDB with a caller-chosen MaxConns, so
// the concurrency proof can run against a pool capped at 4 exactly as the
// item requires. Every test constructs its own pool; none share one.
func requireDBMaxConns(t *testing.T, maxConns int32) *database.DB {
	t.Helper()

	cfg, err := config.Load()
	if err != nil {
		skipUnlessDB(t, err)
		return nil
	}
	poolCfg, err := pgxpool.ParseConfig(cfg.DatabaseURL)
	if err != nil {
		t.Fatalf("parse DATABASE_URL: %v", err)
	}
	pc := database.DefaultPoolConfig()
	poolCfg.MaxConns = maxConns
	poolCfg.MinConns = pc.MinConns
	poolCfg.MaxConnLifetime = pc.MaxConnLifetime
	poolCfg.MaxConnIdleTime = pc.MaxConnIdleTime
	poolCfg.HealthCheckPeriod = pc.HealthCheckPeriod
	poolCfg.ConnConfig.ConnectTimeout = 3 * time.Second

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	pool, err := pgxpool.NewWithConfig(ctx, poolCfg)
	if err != nil {
		skipUnlessDB(t, err)
		return nil
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		skipUnlessDB(t, err)
		return nil
	}
	db := &database.DB{Pool: pool}
	t.Cleanup(db.Close)
	return db
}

// testEvent builds a valid event with a distinct id, type and entity per
// call, so tests can identify their own rows among whatever else the table
// holds.
func testEvent(t *testing.T, typ string) Event {
	t.Helper()
	return Event{
		ID:         uuid.New(),
		Type:       typ,
		Org:        "test-org",
		EntityType: "quote",
		EntityID:   uuid.New(),
		Data:       []byte(`{"n":` + fmt.Sprint(time.Now().UnixNano()) + `}`),
		At:         time.Now().UTC(),
	}
}

// cleanOutbox empties all three tables so every test starts from a known
// feed: the drain reads from position 0 for a fresh subscriber, and a test's
// assertions about counts and deliveries must not see a prior run's rows.
func cleanOutbox(t *testing.T, db *database.DB) {
	t.Helper()
	if _, err := db.Pool.Exec(context.Background(),
		`TRUNCATE events_outbox, event_subscriber_cursors, event_subscriber_parked`); err != nil {
		t.Fatalf("truncate outbox: %v", err)
	}
}

// maxPosition is the baseline every read in these tests pages from, so they
// hold on a database that already holds other events (earlier tests, seeds).
func maxPosition(t *testing.T, db *database.DB) int64 {
	t.Helper()
	var after int64
	if err := db.Pool.QueryRow(context.Background(),
		`SELECT coalesce(max(position), 0) FROM events_outbox`).Scan(&after); err != nil {
		t.Fatalf("read max position: %v", err)
	}
	return after
}

// A rolled back mutation writes no event, and a committed one writes
// exactly one: the event is a fact about the mutation and shares its fate.
func TestWrite_RollbackWritesNothingCommitWritesOne(t *testing.T) {
	db := testutil.RequireDB(t)
	cleanOutbox(t, db)
	w := NewWriter(db, "")

	rolledBack := testEvent(t, "test.rolled_back")
	err := db.RunInTx(context.Background(), func(ctx context.Context) error {
		if err := w.Write(ctx, rolledBack); err != nil {
			return err
		}
		return errors.New("mutation failed, rollback")
	})
	if err == nil || err.Error() != "mutation failed, rollback" {
		t.Fatalf("RunInTx returned %v, want the mutation error", err)
	}

	committed := testEvent(t, "test.committed")
	if err := db.RunInTx(context.Background(), func(ctx context.Context) error {
		return w.Write(ctx, committed)
	}); err != nil {
		t.Fatalf("committed write: %v", err)
	}

	var n int
	if err := db.Pool.QueryRow(context.Background(),
		`SELECT count(*) FROM events_outbox WHERE event_id = $1`, rolledBack.ID).Scan(&n); err != nil {
		t.Fatalf("count rolled back: %v", err)
	}
	if n != 0 {
		t.Errorf("rolled back mutation left %d events, want 0", n)
	}
	if err := db.Pool.QueryRow(context.Background(),
		`SELECT count(*) FROM events_outbox WHERE event_id = $1`, committed.ID).Scan(&n); err != nil {
		t.Fatalf("count committed: %v", err)
	}
	if n != 1 {
		t.Errorf("committed mutation left %d events, want 1", n)
	}
}

// Outside a transaction the write wraps itself in its own transaction (so
// the advisory lock stays transaction scoped) and the row lands. The
// event's own org wins over the writer's stamp; the stamp fills events
// that carry none, and a writer with no configured org stamps DefaultOrg.
func TestWrite_OutsideTransactionWrapsItself(t *testing.T) {
	db := testutil.RequireDB(t)
	cleanOutbox(t, db)
	stamped := NewWriter(db, "stamp-org")

	ev := testEvent(t, "test.no_tx")
	if err := stamped.Write(context.Background(), ev); err != nil {
		t.Fatalf("Write outside a transaction: %v", err)
	}

	var org string
	var branch any
	if err := db.Pool.QueryRow(context.Background(),
		`SELECT org, branch_id FROM events_outbox WHERE event_id = $1`, ev.ID).Scan(&org, &branch); err != nil {
		t.Fatalf("read back: %v", err)
	}
	if org != "test-org" {
		t.Errorf("org = %q, want the event's own test-org (the event wins over the stamp)", org)
	}
	if branch != nil {
		t.Errorf("branch_id = %v, want NULL with no branch context", branch)
	}

	unstamped := testEvent(t, "test.no_tx")
	unstamped.Org = ""
	if err := stamped.Write(context.Background(), unstamped); err != nil {
		t.Fatal(err)
	}
	if err := db.Pool.QueryRow(context.Background(),
		`SELECT org FROM events_outbox WHERE event_id = $1`, unstamped.ID).Scan(&org); err != nil {
		t.Fatal(err)
	}
	if org != "stamp-org" {
		t.Errorf("org = %q, want the writer's stamp-org on an event that carries none", org)
	}

	defaults := NewWriter(db, "")
	defEv := testEvent(t, "test.no_tx")
	defEv.Org = ""
	if err := defaults.Write(context.Background(), defEv); err != nil {
		t.Fatal(err)
	}
	if err := db.Pool.QueryRow(context.Background(),
		`SELECT org FROM events_outbox WHERE event_id = $1`, defEv.ID).Scan(&org); err != nil {
		t.Fatal(err)
	}
	if org != DefaultOrg {
		t.Errorf("org = %q, want %q from the unconfigured writer", org, DefaultOrg)
	}
}

// Invalid events are refused before any statement runs, so a caller inside
// a transaction fails its mutation instead of committing a broken event.
func TestWrite_ValidatesTheEvent(t *testing.T) {
	db := testutil.RequireDB(t)
	cleanOutbox(t, db)
	w := NewWriter(db, "")

	cases := []struct {
		name string
		ev   Event
	}{
		{"no type", Event{EntityType: "quote", EntityID: uuid.New()}},
		{"uppercase type", Event{Type: "Quote.Created", EntityType: "quote", EntityID: uuid.New()}},
		{"empty token", Event{Type: "quote..created", EntityType: "quote", EntityID: uuid.New()}},
		{"no entity type", Event{Type: "quote.created", EntityID: uuid.New()}},
		{"no entity id", Event{Type: "quote.created", EntityType: "quote"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if err := w.Write(context.Background(), tc.ev); err == nil {
				t.Fatal("Write accepted an invalid event")
			}
		})
	}
}

// A committed event is read exactly once by a walk of position cursors, and
// again only from an earlier cursor: the keyset rule the events API serves.
func TestListEvents_ReadExactlyOncePerCursorWalk(t *testing.T) {
	db := testutil.RequireDB(t)
	cleanOutbox(t, db)
	w := NewWriter(db, "")
	after := maxPosition(t, db)

	var ids []uuid.UUID
	for i := 0; i < 5; i++ {
		ev := testEvent(t, "test.order")
		ids = append(ids, ev.ID)
		if err := w.Write(context.Background(), ev); err != nil {
			t.Fatalf("write %d: %v", i, err)
		}
	}

	seen := map[uuid.UUID]int{}
	cursor := after
	pages := 0
	for {
		rows, err := ListEvents(context.Background(), db.Pool, cursor, nil, 2)
		if err != nil {
			t.Fatalf("list: %v", err)
		}
		if len(rows) == 0 {
			break
		}
		pages++
		var last int64
		for _, r := range rows {
			if r.Position <= cursor {
				t.Fatalf("row %d at or below cursor %d", r.Position, cursor)
			}
			if r.Position <= last {
				t.Fatalf("rows out of order: %d after %d", r.Position, last)
			}
			last = r.Position
			seen[r.Event.ID]++
		}
		cursor = last
		if len(rows) < 2 {
			break
		}
	}
	if pages != 3 {
		t.Errorf("walked %d pages, want 3 (2+2+1)", pages)
	}
	for _, id := range ids {
		if seen[id] != 1 {
			t.Errorf("event %s seen %d times in one walk, want exactly 1", id, seen[id])
		}
	}

	// From an earlier cursor the events are readable again: a cursor is a
	// position, not a consumption claim.
	rows, err := ListEvents(context.Background(), db.Pool, after, nil, 100)
	if err != nil {
		t.Fatalf("re-read: %v", err)
	}
	again := map[uuid.UUID]bool{}
	for _, r := range rows {
		again[r.Event.ID] = true
	}
	for _, id := range ids {
		if !again[id] {
			t.Errorf("event %s missing from the re-read starting at %d", id, after)
		}
	}
}

// The type filter admits only the listed types, matching exactly (no
// wildcard syntax on the read API).
func TestListEvents_TypeFilter(t *testing.T) {
	db := testutil.RequireDB(t)
	cleanOutbox(t, db)
	w := NewWriter(db, "")
	base := maxPosition(t, db)

	keep := testEvent(t, "test.keep.this")
	drop := testEvent(t, "test.drop")
	if err := w.Write(context.Background(), keep); err != nil {
		t.Fatal(err)
	}
	if err := w.Write(context.Background(), drop); err != nil {
		t.Fatal(err)
	}

	rows, err := ListEvents(context.Background(), db.Pool, base-1, []string{"test.keep.this", "test.other"}, 100)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	found := false
	for _, r := range rows {
		if r.Event.ID == drop.ID {
			t.Errorf("unlisted type %s passed the filter", drop.Type)
		}
		if r.Event.ID == keep.ID {
			found = true
		}
	}
	if !found {
		t.Error("listed type missing from the filtered read")
	}
}

// The late commit case, the heart of the ordering rule. While one writer's
// transaction is open, a second event writer cannot even draw a position
// (the advisory lock serializes event writers to commit order), and a
// reader that pages in the meantime advances only to the positions it
// actually read, so the late commit is served next rather than skipped. A
// reader that advanced to the sequence's last value, or an implementation
// without the lock (letting a higher position commit while a lower one is
// in flight), fails this test.
func TestWrite_LateCommitIsNeverSkipped(t *testing.T) {
	db := testutil.RequireDB(t)
	cleanOutbox(t, db)
	w := NewWriter(db, "")
	ctx := context.Background()

	// A committed row the reader has already paged past.
	seed := testEvent(t, "test.late.seed")
	if err := w.Write(ctx, seed); err != nil {
		t.Fatal(err)
	}
	cursor := maxPosition(t, db)
	seedPos := cursor

	// Writer one writes its event and then holds its transaction open; the
	// test performs its checks in the middle of the transaction, through the
	// fn RunInTx is holding open.
	holding := make(chan struct{})
	release := make(chan struct{})
	late := testEvent(t, "test.late.commit")
	lateID := late.ID
	var latePos int64
	txErr := make(chan error, 1)
	go func() {
		txErr <- db.RunInTx(ctx, func(txCtx context.Context) error {
			if err := w.Write(txCtx, late); err != nil {
				return err
			}
			if err := db.GetExecutor(txCtx).QueryRow(txCtx,
				`SELECT position FROM events_outbox WHERE event_id = $1`, lateID).Scan(&latePos); err != nil {
				return err
			}
			close(holding)
			<-release
			return nil
		})
	}()
	<-holding
	defer func() {
		select {
		case <-release:
		default:
			close(release)
		}
	}()
	if latePos <= seedPos {
		t.Fatalf("late event position %d not past the seed %d", latePos, seedPos)
	}

	// A second event writer is blocked on the advisory lock while the first
	// transaction is open: this serialization is what makes a lower
	// position committing last impossible among outbox writers.
	blocked := make(chan error, 1)
	go func() {
		blockCtx, cancel := context.WithTimeout(context.Background(), 400*time.Millisecond)
		defer cancel()
		blocked <- db.RunInTx(blockCtx, func(txCtx context.Context) error {
			return w.Write(txCtx, testEvent(t, "test.late.blocked"))
		})
	}()
	select {
	case err := <-blocked:
		t.Fatalf("a second event writer was not serialized behind the open transaction: %v", err)
	case <-time.After(150 * time.Millisecond):
		// Still running: the writer is waiting on the lock, as designed.
	}
	if err := <-blocked; err == nil {
		t.Fatal("the blocked write should have been cancelled by its timeout")
	}

	// The reader pages while the first transaction is uncommitted: it sees
	// nothing new and its cursor stays at the seed position.
	rows, err := ListEvents(ctx, db.Pool, cursor, nil, 100)
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range rows {
		if r.Event.ID == lateID {
			t.Fatal("uncommitted event is visible to the reader")
		}
		cursor = r.Position
	}
	if cursor != seedPos {
		t.Fatalf("reader advanced to %d while the next event was uncommitted, want %d", cursor, seedPos)
	}

	// The late commit lands, and the reader's next page serves it.
	close(release)
	if err := <-txErr; err != nil {
		t.Fatalf("late writer's transaction: %v", err)
	}
	rows, err = ListEvents(ctx, db.Pool, cursor, nil, 100)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, r := range rows {
		if r.Position <= cursor {
			t.Fatalf("row %d at or below cursor %d", r.Position, cursor)
		}
		if r.Event.ID == lateID {
			found = true
		}
	}
	if !found {
		t.Fatal("the late-committing event was skipped by the cursor reader")
	}

	// The blocked writer proceeds once the lock frees, at a higher
	// position, so a reader resuming from the late event is served next.
	after := testEvent(t, "test.late.after")
	if err := w.Write(ctx, after); err != nil {
		t.Fatal(err)
	}
	final := maxPosition(t, db)
	if final <= latePos {
		t.Fatalf("positions after the late commit: max %d, late %d", final, latePos)
	}
}

// Three contending writers inside transactions at pool size 4: every event
// lands, positions are unique, and a concurrent reader paging the feed never
// walks backwards. The writers each hold exactly one connection for their
// whole transaction and never reach for a second, so four connections serve
// three writers and a reader without deadlock.
func TestConcurrentWriters_Pool4ThreeContenders(t *testing.T) {
	db := requireDBMaxConns(t, 4)
	cleanOutbox(t, db)
	w := NewWriter(db, "")
	ctx := context.Background()
	base := maxPosition(t, db)

	const writers, each = 3, 6
	stop := make(chan struct{})
	var mu sync.Mutex
	written := make(map[uuid.UUID]bool)

	var wg sync.WaitGroup
	errs := make(chan error, writers)
	for i := 0; i < writers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			err := db.RunInTx(ctx, func(txCtx context.Context) error {
				for j := 0; j < each; j++ {
					ev := testEvent(t, "test.concurrent")
					if err := w.Write(txCtx, ev); err != nil {
						return err
					}
					mu.Lock()
					written[ev.ID] = true
					mu.Unlock()
				}
				return nil
			})
			if err != nil {
				errs <- err
			}
		}()
	}

	// A reader pages the feed continuously while the writers contend: every
	// page it accepts is strictly increasing past its cursor.
	readerErr := make(chan error, 1)
	go func() {
		cursor := base
		for {
			rows, err := ListEvents(ctx, db.Pool, cursor, nil, 5)
			if err != nil {
				readerErr <- err
				return
			}
			for _, r := range rows {
				if r.Position <= cursor {
					readerErr <- fmt.Errorf("concurrent reader went backwards: %d after cursor %d", r.Position, cursor)
					return
				}
				cursor = r.Position
			}
			if len(rows) == 0 {
				select {
				case <-stop:
					readerErr <- nil
					return
				default:
				}
			}
		}
	}()

	wg.Wait()
	close(stop)
	close(errs)
	for err := range errs {
		t.Errorf("contending writer: %v", err)
	}
	if err := <-readerErr; err != nil {
		t.Fatalf("concurrent reader: %v", err)
	}

	// Every committed event is readable in one final walk, positions unique.
	rows, err := ListEvents(ctx, db.Pool, base, nil, writers*each+10)
	if err != nil {
		t.Fatalf("final walk: %v", err)
	}
	seen := make(map[uuid.UUID]bool)
	var last int64
	for _, r := range rows {
		if seen[r.Event.ID] {
			t.Fatalf("event %s served twice in one walk", r.Event.ID)
		}
		seen[r.Event.ID] = true
		if r.Position <= last {
			t.Fatalf("positions out of order: %d after %d", r.Position, last)
		}
		last = r.Position
	}
	for id := range written {
		if !seen[id] {
			t.Errorf("event %s committed but never read", id)
		}
	}
	if got := len(seen); got < writers*each {
		t.Errorf("final walk served %d events, want at least %d", got, writers*each)
	}
}
