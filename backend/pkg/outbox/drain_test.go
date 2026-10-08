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
	d.Subscribe(eventbus.SubjectExposureAll, "drain-lifecycle")

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
