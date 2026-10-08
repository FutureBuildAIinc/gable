// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package outbox

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/gablelbm/gable/internal/testutil"
	"github.com/gablelbm/gable/pkg/eventbus"
)

// captureBus is a real in-process bus with one recording subscriber, the
// same shape the exposure notifier subscription has in the server.
func captureBus(t *testing.T, pattern, durable string) (eventbus.Bus, *recorder) {
	t.Helper()
	bus := eventbus.New(eventbus.Config{Logger: slog.New(slog.NewTextHandler(io.Discard, nil))})
	rec := &recorder{}
	if err := bus.Subscribe(pattern, durable, rec.handle); err != nil {
		t.Fatalf("subscribe: %v", err)
	}
	return bus, rec
}

type recorder struct {
	mu        sync.Mutex
	delivered []delivered
}

type delivered struct {
	subject string
	payload json.RawMessage
}

func (r *recorder) handle(_ context.Context, e eventbus.Event) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.delivered = append(r.delivered, delivered{subject: e.Subject, payload: e.Payload})
	return nil
}

func (r *recorder) snapshot() []delivered {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]delivered, len(r.delivered))
	copy(out, r.delivered)
	return out
}

// The drain feeds the in-process bus subscribers from committed rows: the
// rows a subscriber's pattern matches are published with the row's type as
// the subject and its data as the payload, the cursor advances past every
// row in the window (matched or not), and a second pass delivers nothing
// new. This is the mechanism that keeps the exposure emails sending once
// publishers write the outbox instead of calling the bus.
func TestDrain_FeedsBusSubscribersAndAdvancesCursor(t *testing.T) {
	db := testutil.RequireDB(t)
	cleanOutbox(t, db)
	w := NewWriter(db, "")
	ctx := context.Background()

	bus, rec := captureBus(t, eventbus.SubjectExposureAll, "drain-test-notifier")
	d := NewDrainRunner(db, bus, slog.New(slog.NewTextHandler(io.Discard, nil)))
	d.Subscribe(eventbus.SubjectExposureAll, "drain-test-notifier")
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
	bySubject := map[string]json.RawMessage{}
	for _, dlv := range got {
		bySubject[dlv.subject] = dlv.payload
	}
	if _, ok := bySubject[flagged.Type]; !ok {
		t.Errorf("subject %s missing from the deliveries, got %v", flagged.Type, got)
	}
	if _, ok := bySubject[escalated.Type]; !ok {
		t.Errorf("subject %s missing from the deliveries, got %v", escalated.Type, got)
	}
	if payload, ok := bySubject[flagged.Type]; ok {
		// The row's data travels as JSONB, which normalizes whitespace and
		// key order; the JSON value must survive the trip unchanged.
		var gotVal, wantVal any
		if err := json.Unmarshal(payload, &gotVal); err != nil {
			t.Fatalf("delivered payload is not JSON: %v", err)
		}
		if err := json.Unmarshal(flagged.Data, &wantVal); err != nil {
			t.Fatalf("written payload is not JSON: %v", err)
		}
		if !reflect.DeepEqual(gotVal, wantVal) {
			t.Errorf("payload = %s, want the row's data %s", payload, flagged.Data)
		}
	}

	// The cursor advanced past the non-matching row too, so the window is
	// not re-scanned; a second pass delivers nothing.
	var pos int64
	if err := db.Pool.QueryRow(ctx,
		`SELECT position FROM event_subscriber_cursors WHERE subscriber = $1`,
		"drain-test-notifier").Scan(&pos); err != nil {
		t.Fatalf("read cursor: %v", err)
	}
	max := maxPosition(t, db)
	if pos != max {
		t.Errorf("cursor = %d, want %d (every row in the window, matched or not)", pos, max)
	}
	d.pass(ctx)
	if n := len(rec.snapshot()); n != 2 {
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
	if got[2].subject != fresh.Type {
		t.Errorf("third delivery subject = %s, want %s", got[2].subject, fresh.Type)
	}
}

// A held cursor row makes drainOne publish nothing and leave the cursor
// unchanged: FOR UPDATE SKIP LOCKED returning no rows means "another drain
// instance is busy with this subscriber, skip this tick", never "no cursor,
// start from position 0" (which would replay the whole outbox to a subscriber
// that already consumed it - review round 1, P1).
func TestDrain_HeldCursorRowSkipsTheTick(t *testing.T) {
	db := testutil.RequireDB(t)
	cleanOutbox(t, db)
	w := NewWriter(db, "")
	ctx := context.Background()

	bus, rec := captureBus(t, eventbus.SubjectExposureAll, "drain-held-cursor")
	d := NewDrainRunner(db, bus, slog.New(slog.NewTextHandler(io.Discard, nil)))
	d.Subscribe(eventbus.SubjectExposureAll, "drain-held-cursor")
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

	if got := len(rec.snapshot()); got != 0 {
		t.Fatalf("pass published %d events while the cursor row was held, want 0", got)
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
	if got := len(rec.snapshot()); got != 3 {
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

	d := NewDrainRunner(db, nil, slog.New(slog.NewTextHandler(io.Discard, nil)))
	d.Subscribe(eventbus.SubjectExposureAll, "drain-at-head")
	d.SubscribeReplay(eventbus.SubjectExposureAll, "drain-replay")
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

	bus, rec := captureBus(t, eventbus.SubjectExposureAll, "drain-pool4")
	d := NewDrainRunner(db, bus, slog.New(slog.NewTextHandler(io.Discard, nil)))
	d.SubscribeReplay(eventbus.SubjectExposureAll, "drain-pool4")
	d.ensureCursors(ctx, d.snapshotSubs())

	var wg sync.WaitGroup
	passErr := make(chan error, 3)
	for i := 0; i < 3; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			passErr <- d.drainOne(ctx, drainSubscriber{
				pattern: eventbus.SubjectExposureAll, durable: "drain-pool4",
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

	// Whichever pass won the cursor row delivered the window exactly once
	// (SKIP LOCKED keeps the others out until it commits); the passes after
	// it find the cursor advanced and deliver nothing new.
	if got := len(rec.snapshot()); got != events {
		t.Errorf("deliveries = %d, want exactly the %d events (no double delivery)", got, events)
	}
	var pos int64
	if err := db.Pool.QueryRow(ctx,
		`SELECT position FROM event_subscriber_cursors WHERE subscriber = $1`,
		"drain-pool4").Scan(&pos); err != nil {
		t.Fatal(err)
	}
	d.pass(ctx)
	if got := len(rec.snapshot()); got != events+1 {
		t.Errorf("after the late commit deliveries = %d, want %d", got, events+1)
	}
	if err := db.Pool.QueryRow(ctx,
		`SELECT position FROM event_subscriber_cursors WHERE subscriber = $1`,
		"drain-pool4").Scan(&pos); err != nil {
		t.Fatal(err)
	}
	if final := maxPosition(t, db); pos != final {
		t.Errorf("cursor = %d after the final pass, want the head %d", pos, final)
	}
}

// Start and Stop bracket the loop: a started runner delivers on its ticker
// without any test forcing a pass, starting twice is an error, Stop waits
// for the in-flight tick, and Stop on a never-started runner is a no-op.
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
	NewDrainRunner(db, nil, nil).Stop()

	bus, rec := captureBus(t, eventbus.SubjectExposureAll, "drain-lifecycle")
	d := NewDrainRunner(db, bus, slog.New(slog.NewTextHandler(io.Discard, nil)))
	d.interval = 20 * time.Millisecond
	// The event above was written before the subscriber registered; the
	// lifecycle test wants it delivered, so the subscriber opts into replay.
	d.SubscribeReplay(eventbus.SubjectExposureAll, "drain-lifecycle")

	if err := d.Start(ctx); err != nil {
		t.Fatalf("start: %v", err)
	}
	if err := d.Start(ctx); err == nil {
		t.Fatal("second Start returned nil, want an error")
	}

	deadline := time.Now().Add(3 * time.Second)
	for len(rec.snapshot()) == 0 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if got := rec.snapshot(); len(got) != 1 {
		t.Fatalf("ticker delivered %d events in 3s, want 1", len(got))
	}

	d.Stop()
	d.Stop() // idempotent

	var pos int64
	if err := db.Pool.QueryRow(ctx,
		`SELECT position FROM event_subscriber_cursors WHERE subscriber = $1`,
		"drain-lifecycle").Scan(&pos); err != nil {
		t.Fatalf("read cursor: %v", err)
	}
	if pos != maxPosition(t, db) {
		t.Errorf("cursor = %d, want the max position after the pass", pos)
	}
}
