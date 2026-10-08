// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package outbox

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/gablelbm/gable/internal/testutil"
	"github.com/gablelbm/gable/pkg/database"
	"github.com/gablelbm/gable/pkg/eventbus"
	"github.com/google/uuid"
)

// recorder is a subscriber handler that records every delivered event, the
// same shape the exposure notifier's handler has in the worker.
type recorder struct {
	mu        sync.Mutex
	delivered []delivered
	failIDs   map[string]error // event ids whose delivery fails, for the retry tests
}

type delivered struct {
	eventID string
	subject string
	payload json.RawMessage
}

func (r *recorder) handle(_ context.Context, e eventbus.Event) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.failIDs != nil {
		if err, ok := r.failIDs[e.EventID]; ok {
			return err
		}
	}
	r.delivered = append(r.delivered, delivered{eventID: e.EventID, subject: e.Subject, payload: e.Payload})
	return nil
}

func (r *recorder) snapshot() []delivered {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]delivered, len(r.delivered))
	copy(out, r.delivered)
	return out
}

func (r *recorder) count() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.delivered)
}

func quietLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// The drain delivers committed rows to the subscriber's handler
// synchronously, with the row's outbox event_id as the bus event id and its
// data as the payload; the cursor advances past every row in the window
// (matched or not), and a second pass delivers nothing new. This is the
// mechanism that keeps the exposure emails sending once the mutation wrote
// the outbox row instead of calling the bus.
func TestDrain_DeliversRowsWithTheirEventIDAndAdvancesCursor(t *testing.T) {
	db := testutil.RequireDB(t)
	cleanOutbox(t, db)
	w := NewWriter(db, "")
	ctx := context.Background()

	rec := &recorder{}
	d := NewDrainRunner(db, quietLogger())
	d.Subscribe(eventbus.SubjectExposureAll, "drain-test-notifier", rec.handle)
	d.ensureCursors(ctx, d.snapshotSubs())

	flagged := testEvent(t, eventbus.SubjectExposureFlagged)
	escalated := testEvent(t, eventbus.SubjectExposureEscalated)
	other := testEvent(t, "order.confirmed")
	for _, ev := range []Event{flagged, escalated, other} {
		if err := w.Write(ctx, ev); err != nil {
			t.Fatalf("write %s: %v", ev.Type, err)
		}
	}

	d.pass(ctx)

	got := rec.snapshot()
	if len(got) != 2 {
		t.Fatalf("subscriber received %d events, want the 2 matching ones", len(got))
	}
	bySubject := map[string]delivered{}
	for _, dlv := range got {
		bySubject[dlv.subject] = dlv
	}
	for _, want := range []Event{flagged, escalated} {
		dlv, ok := bySubject[want.Type]
		if !ok {
			t.Errorf("subject %s missing from the deliveries, got %v", want.Type, got)
			continue
		}
		// The outbox row's event_id is the delivery's identity: a replay of
		// the row must present itself as the same event so subscribers dedup
		// on the id the mutation minted, not on a fresh random one.
		if dlv.eventID != want.ID.String() {
			t.Errorf("delivery of %s carried event_id %s, want the row's %s", want.Type, dlv.eventID, want.ID)
		}
		var gotVal, wantVal any
		if err := json.Unmarshal(dlv.payload, &gotVal); err != nil {
			t.Fatalf("delivered payload is not JSON: %v", err)
		}
		if err := json.Unmarshal(want.Data, &wantVal); err != nil {
			t.Fatalf("written payload is not JSON: %v", err)
		}
		if !reflect.DeepEqual(gotVal, wantVal) {
			t.Errorf("payload = %s, want the row's data %s", dlv.payload, want.Data)
		}
	}

	// The cursor advanced past the non-matching row too, so the window is
	// not re-scanned; a second pass delivers nothing.
	if pos := subscriberPosition(t, db, "drain-test-notifier"); pos != maxPosition(t, db) {
		t.Errorf("cursor = %d, want the max position (every row in the window, matched or not)", pos)
	}
	d.pass(ctx)
	if n := rec.count(); n != 2 {
		t.Errorf("second pass has %d deliveries in total, want still 2 (nothing new)", n)
	}

	// A new committed row is delivered on the next pass; nothing older
	// replays.
	fresh := testEvent(t, eventbus.SubjectExposureCleared)
	if err := w.Write(ctx, fresh); err != nil {
		t.Fatal(err)
	}
	d.pass(ctx)
	got = rec.snapshot()
	if len(got) != 3 {
		t.Fatalf("after a new event the subscriber has %d deliveries, want 3", len(got))
	}
	if got[2].subject != fresh.Type || got[2].eventID != fresh.ID.String() {
		t.Errorf("third delivery = %s/%s, want %s/%s", got[2].subject, got[2].eventID, fresh.Type, fresh.ID)
	}
}

// One window per tick: a backlog larger than the batch is drained across
// ticks, never in one pass that would hold the cursor row and deliver an
// unbounded window while later rows keep committing.
func TestDrain_OneWindowPerTick(t *testing.T) {
	db := testutil.RequireDB(t)
	cleanOutbox(t, db)
	w := NewWriter(db, "")
	ctx := context.Background()

	rec := &recorder{}
	d := NewDrainRunner(db, quietLogger())
	d.batch = 2
	d.SubscribeReplay(eventbus.SubjectExposureAll, "drain-window", rec.handle)
	d.ensureCursors(ctx, d.snapshotSubs())

	for i := 0; i < 5; i++ {
		if err := w.Write(ctx, testEvent(t, eventbus.SubjectExposureFlagged)); err != nil {
			t.Fatal(err)
		}
	}

	d.pass(ctx)
	if got := rec.count(); got != 2 {
		t.Fatalf("first pass delivered %d events, want the batch of 2", got)
	}
	d.pass(ctx)
	if got := rec.count(); got != 4 {
		t.Fatalf("second pass has %d deliveries in total, want 4", got)
	}
	d.pass(ctx)
	if got := rec.count(); got != 5 {
		t.Fatalf("third pass has %d deliveries in total, want 5 (the backlog is gone)", got)
	}
}

// A failing handler stops the subscriber's pass with the cursor left before
// the failed row and the attempt counted on the cursor row; after
// maxAttempts consecutive failures of the same row the row is parked, logged
// and the cursor moves on, so one poison event cannot stall the subscriber
// forever.
func TestDrain_HandlerErrorStopsPassRetriesThenParks(t *testing.T) {
	db := testutil.RequireDB(t)
	cleanOutbox(t, db)
	w := NewWriter(db, "")
	ctx := context.Background()

	poison := testEvent(t, eventbus.SubjectExposureFlagged)
	after := testEvent(t, eventbus.SubjectExposureEscalated)
	for _, ev := range []Event{poison, after} {
		if err := w.Write(ctx, ev); err != nil {
			t.Fatal(err)
		}
	}

	rec := &recorder{failIDs: map[string]error{poison.ID.String(): errors.New("smtp on fire")}}
	d := NewDrainRunner(db, quietLogger())
	d.maxAttempts = 3
	d.SubscribeReplay(eventbus.SubjectExposureAll, "drain-poison", rec.handle)
	d.ensureCursors(ctx, d.snapshotSubs())
	base := subscriberPosition(t, db, "drain-poison")

	// Passes one and two: the poison row fails, the pass stops before it,
	// the row after it is not delivered, the attempt is counted.
	for wantAttempts := 1; wantAttempts <= 2; wantAttempts++ {
		d.pass(ctx)
		if got := rec.count(); got != 0 {
			t.Fatalf("pass %d delivered %d events, want 0 (the pass stops at the failing row)", wantAttempts+1, got)
		}
		if pos, att := subscriberPositionAttempts(t, db, "drain-poison"); pos != base || att != wantAttempts {
			t.Fatalf("after pass %d: cursor/attempts = %d/%d, want %d/%d", wantAttempts+1, pos, att, base, wantAttempts)
		}
	}

	// Pass three reaches the attempt limit: the poison row is parked, the
	// cursor moves past it, and the rest of the window delivers.
	d.pass(ctx)
	if got := rec.count(); got != 1 {
		t.Fatalf("after the poison row was parked the subscriber has %d deliveries, want 1 (the row after it)", got)
	}
	if got := rec.snapshot()[0].eventID; got != after.ID.String() {
		t.Errorf("delivered %s after parking, want the row after the poison one", got)
	}
	var parkedID uuid.UUID
	var parkedAttempts int
	if err := db.Pool.QueryRow(ctx,
		`SELECT event_id, attempts FROM event_subscriber_parked WHERE subscriber = $1`,
		"drain-poison").Scan(&parkedID, &parkedAttempts); err != nil {
		t.Fatalf("read parked row: %v", err)
	}
	if parkedID != poison.ID || parkedAttempts != 3 {
		t.Errorf("parked = %s after %d attempts, want %s after 3", parkedID, parkedAttempts, poison.ID)
	}
	if pos, att := subscriberPositionAttempts(t, db, "drain-poison"); pos != maxPosition(t, db) || att != 0 {
		t.Errorf("after parking cursor/attempts = %d/%d, want the head %d and attempts reset", pos, att, maxPosition(t, db))
	}

	// The parked row is never retried: another pass delivers nothing.
	d.pass(ctx)
	if got := rec.count(); got != 1 {
		t.Errorf("pass after parking delivered %d more events, want 0", got-1)
	}
}

// A held cursor row makes drainOne deliver nothing and leave the cursor
// unchanged: FOR UPDATE SKIP LOCKED returning no rows means "another drain
// instance is busy with this subscriber, skip this tick", never "no cursor,
// start from position 0" (which would replay the whole outbox to a subscriber
// that already consumed it - review round 1, P1).
func TestDrain_HeldCursorRowSkipsTheTick(t *testing.T) {
	db := testutil.RequireDB(t)
	cleanOutbox(t, db)
	w := NewWriter(db, "")
	ctx := context.Background()

	rec := &recorder{}
	d := NewDrainRunner(db, quietLogger())
	d.Subscribe(eventbus.SubjectExposureAll, "drain-held-cursor", rec.handle)
	d.ensureCursors(ctx, d.snapshotSubs())

	for i := 0; i < 3; i++ {
		if err := w.Write(ctx, testEvent(t, eventbus.SubjectExposureFlagged)); err != nil {
			t.Fatal(err)
		}
	}
	max := maxPosition(t, db)

	// Another session holds the subscriber's cursor row.
	holdTx, err := db.Pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin hold: %v", err)
	}
	defer holdTx.Rollback(ctx)
	if _, err := holdTx.Exec(ctx,
		`SELECT position FROM event_subscriber_cursors WHERE subscriber = $1 FOR UPDATE`,
		"drain-held-cursor"); err != nil {
		t.Fatalf("hold cursor row: %v", err)
	}

	d.pass(ctx)

	if got := rec.count(); got != 0 {
		t.Fatalf("pass delivered %d events while the cursor row was held, want 0", got)
	}
	var pos int64
	if err := db.Pool.QueryRow(ctx,
		`SELECT position FROM event_subscriber_cursors WHERE subscriber = $1`,
		"drain-held-cursor").Scan(&pos); err != nil {
		t.Fatalf("read cursor: %v", err)
	}
	if pos != 0 {
		t.Errorf("cursor = %d while the row was held, want the unchanged 0", pos)
	}

	// Released, the next pass delivers from the cursor, not from 0.
	if err := holdTx.Rollback(ctx); err != nil {
		t.Fatalf("release hold: %v", err)
	}
	d.pass(ctx)
	if got := rec.count(); got != 3 {
		t.Fatalf("after release the subscriber has %d deliveries, want 3", got)
	}
	if err := db.Pool.QueryRow(ctx,
		`SELECT position FROM event_subscriber_cursors WHERE subscriber = $1`,
		"drain-held-cursor").Scan(&pos); err != nil {
		t.Fatal(err)
	}
	if pos != max {
		t.Errorf("cursor = %d after the pass, want the head %d", pos, max)
	}
}

// Start creates each registered subscriber's cursor row exactly once, at the
// feed's current maximum position: a newly registered subscriber receives
// events committed after it registered, not the whole outbox. SubscribeReplay
// is the explicit opt-out that starts at 0.
func TestDrain_NewSubscriberStartsAtHeadUnlessReplay(t *testing.T) {
	db := testutil.RequireDB(t)
	cleanOutbox(t, db)
	w := NewWriter(db, "")
	ctx := context.Background()

	if err := w.Write(ctx, testEvent(t, eventbus.SubjectExposureEscalated)); err != nil {
		t.Fatal(err)
	}
	head := maxPosition(t, db)

	d := NewDrainRunner(db, quietLogger())
	d.Subscribe(eventbus.SubjectExposureAll, "drain-at-head", (&recorder{}).handle)
	d.SubscribeReplay(eventbus.SubjectExposureAll, "drain-replay", (&recorder{}).handle)
	if err := d.Start(ctx); err != nil {
		t.Fatalf("start: %v", err)
	}
	d.Stop()

	for sub, want := range map[string]int64{"drain-at-head": head, "drain-replay": 0} {
		var pos int64
		if err := db.Pool.QueryRow(ctx,
			`SELECT position FROM event_subscriber_cursors WHERE subscriber = $1`, sub).Scan(&pos); err != nil {
			t.Fatalf("cursor %s: %v", sub, err)
		}
		if pos != want {
			t.Errorf("cursor %s = %d, want %d", sub, pos, want)
		}
	}

	// Starting the same subscribers again changes nothing: the cursor rows
	// are created once (ON CONFLICT DO NOTHING).
	if err := d.ensureCursors(ctx, d.snapshotSubs()); err != nil {
		t.Fatalf("re-ensure cursors: %v", err)
	}
	for sub, want := range map[string]int64{"drain-at-head": head, "drain-replay": 0} {
		var pos int64
		if err := db.Pool.QueryRow(ctx,
			`SELECT position FROM event_subscriber_cursors WHERE subscriber = $1`, sub).Scan(&pos); err != nil {
			t.Fatalf("cursor %s after re-ensure: %v", sub, err)
		}
		if pos != want {
			t.Errorf("cursor %s = %d after re-ensure, want still %d", sub, pos, want)
		}
	}
}

// The drain opens a transaction per subscriber pass; the concurrency rule
// (pool size 4, pool-size-minus-one contenders) applies to it. Three
// concurrent passes on one subscriber - the overlapping drains of a rolling
// deploy - plus a writer and a feed reader on the same pool of 4: SKIP LOCKED
// keeps the passes from queueing behind each other's row lock, every event is
// delivered exactly once, and the cursor lands at the head.
func TestDrain_Pool4ThreeContenders(t *testing.T) {
	db := requireDBMaxConns(t, 4)
	cleanOutbox(t, db)
	w := NewWriter(db, "")
	ctx := context.Background()

	const events = 6
	for i := 0; i < events; i++ {
		if err := w.Write(ctx, testEvent(t, eventbus.SubjectExposureFlagged)); err != nil {
			t.Fatal(err)
		}
	}

	rec := &recorder{}
	d := NewDrainRunner(db, quietLogger())
	d.SubscribeReplay(eventbus.SubjectExposureAll, "drain-pool4", rec.handle)
	d.ensureCursors(ctx, d.snapshotSubs())

	var wg sync.WaitGroup
	passErr := make(chan error, 3)
	for i := 0; i < 3; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			passErr <- d.drainOne(ctx, drainSubscriber{
				pattern: eventbus.SubjectExposureAll, durable: "drain-pool4", handler: rec.handle,
			})
		}()
	}
	// A writer and a reader contend for the same pool while the passes run.
	wg.Add(2)
	go func() {
		defer wg.Done()
		if err := w.Write(ctx, testEvent(t, eventbus.SubjectExposureFlagged)); err != nil {
			t.Errorf("contending writer: %v", err)
		}
	}()
	go func() {
		defer wg.Done()
		if _, err := ListEvents(ctx, db.Pool, 0, nil, 10); err != nil {
			t.Errorf("contending reader: %v", err)
		}
	}()
	wg.Wait()
	close(passErr)
	for err := range passErr {
		if err != nil {
			t.Errorf("contending pass: %v", err)
		}
	}

	// The contending writer's event may have landed inside any of the passes
	// or after them; one final pass flushes whatever is left, and then every
	// committed event must have been delivered exactly once - SKIP LOCKED
	// keeps the passes from double delivering, and the cursor lands at the
	// head.
	d.pass(ctx)
	rows, err := db.Pool.Query(ctx, `SELECT event_id FROM events_outbox`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	committed := map[uuid.UUID]bool{}
	for rows.Next() {
		var id uuid.UUID
		if err := rows.Scan(&id); err != nil {
			t.Fatal(err)
		}
		committed[id] = true
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	seen := map[string]int{}
	for _, dlv := range rec.snapshot() {
		seen[dlv.eventID]++
	}
	if len(seen) != len(committed) {
		t.Errorf("%d distinct events delivered, want the %d committed", len(seen), len(committed))
	}
	for id := range committed {
		if seen[id.String()] != 1 {
			t.Errorf("event %s delivered %d times, want exactly 1", id, seen[id.String()])
		}
	}
	if pos := subscriberPosition(t, db, "drain-pool4"); pos != maxPosition(t, db) {
		t.Errorf("cursor = %d after the final pass, want the head %d", pos, maxPosition(t, db))
	}
}

// Start and Stop bracket the loop: a started runner delivers on its ticker
// without any test forcing a pass, starting twice is an error, Stop waits
// for the in-flight pass, and Stop on a never-started runner is a no-op.
func TestDrain_StartStopLifecycle(t *testing.T) {
	db := testutil.RequireDB(t)
	cleanOutbox(t, db)
	w := NewWriter(db, "")
	ctx := context.Background()

	ev := testEvent(t, eventbus.SubjectExposureEscalated)
	if err := w.Write(ctx, ev); err != nil {
		t.Fatal(err)
	}

	// Stop before Start is a no-op.
	NewDrainRunner(db, nil).Stop()

	rec := &recorder{}
	d := NewDrainRunner(db, quietLogger())
	d.interval = 20 * time.Millisecond
	// The event above was written before the subscriber registered; the
	// lifecycle test wants it delivered, so the subscriber opts into replay.
	d.SubscribeReplay(eventbus.SubjectExposureAll, "drain-lifecycle", rec.handle)

	if err := d.Start(ctx); err != nil {
		t.Fatalf("start: %v", err)
	}
	if err := d.Start(ctx); err == nil {
		t.Fatal("second Start returned nil, want an error")
	}

	deadline := time.Now().Add(3 * time.Second)
	for rec.count() == 0 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if got := rec.count(); got != 1 {
		t.Fatalf("ticker delivered %d events in 3s, want 1", got)
	}

	d.Stop()
	d.Stop() // idempotent

	if pos := subscriberPosition(t, db, "drain-lifecycle"); pos != maxPosition(t, db) {
		t.Errorf("cursor = %d, want the max position after the pass", pos)
	}
}

func subscriberPosition(t *testing.T, db *database.DB, subscriber string) int64 {
	t.Helper()
	pos, _ := subscriberPositionAttempts(t, db, subscriber)
	return pos
}

func subscriberPositionAttempts(t *testing.T, db *database.DB, subscriber string) (int64, int) {
	t.Helper()
	var pos int64
	var attempts int
	if err := db.Pool.QueryRow(context.Background(),
		`SELECT position, attempts FROM event_subscriber_cursors WHERE subscriber = $1`,
		subscriber).Scan(&pos, &attempts); err != nil {
		t.Fatalf("read cursor %s: %v", subscriber, err)
	}
	return pos, attempts
}
