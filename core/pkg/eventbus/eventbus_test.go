// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package eventbus

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// waitFor polls cond until it holds or the deadline expires. Used instead of a
// fixed sleep so the tests stay fast and are not flaky under -race.
func waitFor(t *testing.T, timeout time.Duration, cond func() bool) bool {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if cond() {
			return true
		}
		time.Sleep(time.Millisecond)
	}
	return cond()
}

// quietConfig silences the bus logger so the expected drop/panic/error logs in
// these tests don't spam `go test` output.
func quietConfig() Config {
	return Config{Logger: slog.New(slog.NewTextHandler(io.Discard, nil))}
}

func newTestBus(t *testing.T) Bus {
	t.Helper()
	b := New(quietConfig())
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		if err := b.Close(ctx); err != nil {
			t.Errorf("Close: %v", err)
		}
	})
	return b
}

func TestSubjectMatch(t *testing.T) {
	cases := []struct {
		pattern, subject string
		want             bool
	}{
		{"quote.exposure.flagged", "quote.exposure.flagged", true},
		{"quote.exposure.flagged", "quote.exposure.escalated", false},
		{"quote.exposure.>", "quote.exposure.flagged", true},
		{"quote.exposure.>", "quote.exposure.escalated", true},
		{"quote.exposure.>", "quote.exposure", false}, // ">" needs >=1 trailing token
		{"quote.exposure.>", "quote.exposure.a.b", true},
		{"quote.*.flagged", "quote.exposure.flagged", true},
		{"quote.*.flagged", "quote.exposure.escalated", false},
		{"quote.*", "quote.exposure.flagged", false}, // "*" is exactly one token
		{"quote.exposure", "quote.exposure.flagged", false},
		{">", "quote.exposure.flagged", true},
		{"quote.exposure.flagged", "quote.exposure.flagged.extra", false},
	}
	for _, c := range cases {
		if got := subjectMatch(c.pattern, c.subject); got != c.want {
			t.Errorf("subjectMatch(%q, %q) = %v, want %v", c.pattern, c.subject, got, c.want)
		}
	}
}

func TestNewReportsInProcessBackend(t *testing.T) {
	b := newTestBus(t)
	if b == nil {
		t.Fatal("New returned nil")
	}
	if got := b.Backend(); got != BackendInProcess {
		t.Fatalf("Backend() = %q, want %q", got, BackendInProcess)
	}
}

func TestPublishSubscribeDeliversPayload(t *testing.T) {
	bus := newTestBus(t)

	got := make(chan Event, 1)
	if err := bus.Subscribe(SubjectExposureFlagged, "d1", func(_ context.Context, e Event) error {
		got <- e
		return nil
	}); err != nil {
		t.Fatalf("Subscribe: %v", err)
	}

	payload := json.RawMessage(`{"quote_id":"abc","delta_pct":7.5}`)
	if err := bus.Publish(context.Background(), SubjectExposureFlagged, payload); err != nil {
		t.Fatalf("Publish: %v", err)
	}

	select {
	case e := <-got:
		if e.Subject != SubjectExposureFlagged {
			t.Errorf("Subject = %q, want %q", e.Subject, SubjectExposureFlagged)
		}
		if e.EventID == "" {
			t.Error("EventID is empty; want a generated id")
		}
		if e.OccurredAt.IsZero() {
			t.Error("OccurredAt is zero")
		}
		var decoded struct {
			QuoteID  string  `json:"quote_id"`
			DeltaPct float64 `json:"delta_pct"`
		}
		if err := json.Unmarshal(e.Payload, &decoded); err != nil {
			t.Fatalf("payload not round-tripped: %v", err)
		}
		if decoded.QuoteID != "abc" || decoded.DeltaPct != 7.5 {
			t.Errorf("payload = %+v, want {abc 7.5}", decoded)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for delivery")
	}
}

// TestWildcardSubscriptionMatchesEverySubject covers the exact pattern
// internal/notification/exposure_events.go registers with.
func TestWildcardSubscriptionMatchesEverySubject(t *testing.T) {
	bus := newTestBus(t)

	var mu sync.Mutex
	seen := map[string]int{}
	if err := bus.Subscribe(SubjectExposureAll, "exposure-notifier", func(_ context.Context, e Event) error {
		mu.Lock()
		seen[e.Subject]++
		mu.Unlock()
		return nil
	}); err != nil {
		t.Fatalf("Subscribe: %v", err)
	}

	subjects := []string{
		SubjectExposureFlagged,
		SubjectExposureEscalated,
		SubjectExposureAckRequired,
		SubjectExposureAcknowledged,
		SubjectExposureCleared,
	}
	for _, s := range subjects {
		if err := bus.Publish(context.Background(), s, json.RawMessage(`{}`)); err != nil {
			t.Fatalf("Publish %s: %v", s, err)
		}
	}
	// A subject outside the pattern must NOT reach this subscriber.
	if err := bus.Publish(context.Background(), "order.confirmed", json.RawMessage(`{}`)); err != nil {
		t.Fatalf("Publish order.confirmed: %v", err)
	}

	ok := waitFor(t, 2*time.Second, func() bool {
		mu.Lock()
		defer mu.Unlock()
		return len(seen) == len(subjects)
	})
	mu.Lock()
	defer mu.Unlock()
	if !ok {
		t.Fatalf("timed out; saw %v", seen)
	}
	for _, s := range subjects {
		if seen[s] != 1 {
			t.Errorf("subject %s delivered %d times, want 1", s, seen[s])
		}
	}
	if seen["order.confirmed"] != 0 {
		t.Errorf("non-matching subject was delivered %d times, want 0", seen["order.confirmed"])
	}
}

func TestSubjectFilteringIsPerSubscriber(t *testing.T) {
	bus := newTestBus(t)

	got := make(chan string, 4)
	if err := bus.Subscribe(SubjectExposureFlagged, "only-flagged", func(_ context.Context, e Event) error {
		got <- e.Subject
		return nil
	}); err != nil {
		t.Fatalf("Subscribe: %v", err)
	}

	_ = bus.Publish(context.Background(), SubjectExposureEscalated, json.RawMessage(`{}`))
	_ = bus.Publish(context.Background(), SubjectExposureFlagged, json.RawMessage(`{}`))

	select {
	case s := <-got:
		if s != SubjectExposureFlagged {
			t.Fatalf("got subject %q, want %q", s, SubjectExposureFlagged)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("expected flagged delivery")
	}

	select {
	case s := <-got:
		t.Fatalf("unexpected extra delivery: %q", s)
	case <-time.After(100 * time.Millisecond):
	}
}

func TestMultipleSubscribersEachReceiveEveryMatchingEvent(t *testing.T) {
	bus := newTestBus(t)

	const nSubs = 4
	var counts [nSubs]atomic.Int64
	for i := 0; i < nSubs; i++ {
		i := i
		if err := bus.Subscribe(SubjectExposureAll, "sub", func(_ context.Context, _ Event) error {
			counts[i].Add(1)
			return nil
		}); err != nil {
			t.Fatalf("Subscribe %d: %v", i, err)
		}
	}

	const nEvents = 10
	for i := 0; i < nEvents; i++ {
		if err := bus.Publish(context.Background(), SubjectExposureFlagged, json.RawMessage(`{}`)); err != nil {
			t.Fatalf("Publish %d: %v", i, err)
		}
	}

	ok := waitFor(t, 2*time.Second, func() bool {
		for i := 0; i < nSubs; i++ {
			if counts[i].Load() != nEvents {
				return false
			}
		}
		return true
	})
	if !ok {
		for i := 0; i < nSubs; i++ {
			t.Errorf("subscriber %d got %d events, want %d", i, counts[i].Load(), nEvents)
		}
		t.Fatal("not all subscribers received every event")
	}
}

// TestPanickingSubscriberDoesNotAffectOthers is the safety property the
// package doc promises: a handler that panics is contained inside its own
// worker. It must not kill the process, must not stop sibling subscribers, and
// must not stop the panicking subscription from receiving later events.
func TestPanickingSubscriberDoesNotAffectOthers(t *testing.T) {
	bus := newTestBus(t)

	var panics, healthy atomic.Int64
	if err := bus.Subscribe(SubjectExposureAll, "panicker", func(_ context.Context, _ Event) error {
		panics.Add(1)
		panic("handler exploded")
	}); err != nil {
		t.Fatalf("Subscribe panicker: %v", err)
	}
	if err := bus.Subscribe(SubjectExposureAll, "healthy", func(_ context.Context, _ Event) error {
		healthy.Add(1)
		return nil
	}); err != nil {
		t.Fatalf("Subscribe healthy: %v", err)
	}

	const nEvents = 5
	for i := 0; i < nEvents; i++ {
		if err := bus.Publish(context.Background(), SubjectExposureFlagged, json.RawMessage(`{}`)); err != nil {
			t.Fatalf("Publish %d: %v", i, err)
		}
	}

	if !waitFor(t, 2*time.Second, func() bool { return healthy.Load() == nEvents }) {
		t.Fatalf("healthy subscriber got %d events, want %d (panicking sibling starved it)", healthy.Load(), nEvents)
	}
	// The panicking worker must have survived its own panic and kept consuming.
	if !waitFor(t, 2*time.Second, func() bool { return panics.Load() == nEvents }) {
		t.Fatalf("panicking subscriber invoked %d times, want %d (worker died on first panic)", panics.Load(), nEvents)
	}
}

// TestErroringSubscriberDoesNotAffectOthers: an error return is logged and
// dropped (at-most-once), and does not disturb other subscribers.
func TestErroringSubscriberDoesNotAffectOthers(t *testing.T) {
	bus := newTestBus(t)

	var errs, healthy atomic.Int64
	_ = bus.Subscribe(SubjectExposureAll, "failer", func(_ context.Context, _ Event) error {
		errs.Add(1)
		return context.DeadlineExceeded
	})
	_ = bus.Subscribe(SubjectExposureAll, "healthy", func(_ context.Context, _ Event) error {
		healthy.Add(1)
		return nil
	})

	const nEvents = 3
	for i := 0; i < nEvents; i++ {
		_ = bus.Publish(context.Background(), SubjectExposureCleared, json.RawMessage(`{}`))
	}

	if !waitFor(t, 2*time.Second, func() bool { return healthy.Load() == nEvents && errs.Load() == nEvents }) {
		t.Fatalf("healthy=%d errs=%d, want %d each", healthy.Load(), errs.Load(), nEvents)
	}
	// No redelivery: exactly nEvents invocations, not more.
	time.Sleep(50 * time.Millisecond)
	if got := errs.Load(); got != nEvents {
		t.Errorf("failing handler invoked %d times, want %d (this backend must not redeliver)", got, nEvents)
	}
}

// TestSlowSubscriberDoesNotBlockPublisher pins the non-blocking contract: a
// handler that never returns, on a queue that has fully saturated, must not
// stall Publish. Publish drops instead of applying backpressure.
func TestSlowSubscriberDoesNotBlockPublisher(t *testing.T) {
	// Buffer 1 so the stalled subscriber's queue saturates immediately and
	// Publish is forced down the drop path.
	bus := newInProcessBus(Config{Buffer: 1, Logger: quietConfig().Logger})
	release := make(chan struct{})
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = bus.Close(ctx)
	})

	_ = bus.Subscribe(SubjectExposureAll, "slow", func(_ context.Context, _ Event) error {
		<-release // blocks until the test releases it
		return nil
	})

	const nEvents = 200
	done := make(chan struct{})
	go func() {
		defer close(done)
		for i := 0; i < nEvents; i++ {
			_ = bus.Publish(context.Background(), SubjectExposureFlagged, json.RawMessage(`{}`))
		}
	}()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		close(release)
		t.Fatal("Publish blocked on a stalled subscriber")
	}
	if got := bus.Dropped(); got == 0 {
		t.Error("Dropped() = 0; a depth-1 queue behind a stalled handler must have dropped events")
	}
	close(release)
}

// TestSlowSubscriberDoesNotStarveOthers: a handler that never returns must not
// stop a sibling subscriber from draining, because queues and workers are
// per-subscription.
func TestSlowSubscriberDoesNotStarveOthers(t *testing.T) {
	bus := newTestBus(t)

	release := make(chan struct{})
	t.Cleanup(func() { close(release) })

	var fast atomic.Int64
	_ = bus.Subscribe(SubjectExposureAll, "slow", func(_ context.Context, _ Event) error {
		<-release
		return nil
	})
	_ = bus.Subscribe(SubjectExposureAll, "fast", func(_ context.Context, _ Event) error {
		fast.Add(1)
		return nil
	})

	// Well under DefaultBuffer, so nothing is dropped and the fast subscriber
	// must observe every event even though its sibling is wedged.
	const nEvents = 50
	for i := 0; i < nEvents; i++ {
		_ = bus.Publish(context.Background(), SubjectExposureFlagged, json.RawMessage(`{}`))
	}

	if !waitFor(t, 2*time.Second, func() bool { return fast.Load() == nEvents }) {
		t.Fatalf("fast subscriber got %d events, want %d (starved by its stalled sibling)", fast.Load(), nEvents)
	}
}

func TestPublishWithNoSubscribersIsANoop(t *testing.T) {
	bus := newTestBus(t)
	if err := bus.Publish(context.Background(), SubjectExposureFlagged, json.RawMessage(`{}`)); err != nil {
		t.Fatalf("Publish with no subscribers: %v", err)
	}
}

// TestSubscriberRegisteredAfterPublishSeesNothing documents the no-replay
// property from the package doc.
func TestSubscriberRegisteredAfterPublishSeesNothing(t *testing.T) {
	bus := newTestBus(t)

	_ = bus.Publish(context.Background(), SubjectExposureFlagged, json.RawMessage(`{}`))

	got := make(chan Event, 1)
	_ = bus.Subscribe(SubjectExposureAll, "late", func(_ context.Context, e Event) error {
		got <- e
		return nil
	})

	select {
	case e := <-got:
		t.Fatalf("late subscriber received a pre-registration event: %+v", e)
	case <-time.After(150 * time.Millisecond):
	}
}

func TestCloseIsIdempotentAndRejectsFurtherUse(t *testing.T) {
	bus := New(quietConfig())
	ctx := context.Background()

	var delivered atomic.Int64
	_ = bus.Subscribe(SubjectExposureAll, "d", func(_ context.Context, _ Event) error {
		delivered.Add(1)
		return nil
	})
	_ = bus.Publish(ctx, SubjectExposureFlagged, json.RawMessage(`{}`))

	if err := bus.Close(ctx); err != nil {
		t.Fatalf("first Close: %v", err)
	}
	if err := bus.Close(ctx); err != nil {
		t.Fatalf("second Close: %v", err)
	}
	// Close drains the backlog before returning.
	if got := delivered.Load(); got != 1 {
		t.Errorf("delivered = %d, want 1 (Close must drain queued events)", got)
	}
	if err := bus.Publish(ctx, SubjectExposureFlagged, json.RawMessage(`{}`)); err != ErrBusClosed {
		t.Errorf("Publish after Close = %v, want ErrBusClosed", err)
	}
	if err := bus.Subscribe(SubjectExposureAll, "x", func(context.Context, Event) error { return nil }); err != ErrBusClosed {
		t.Errorf("Subscribe after Close = %v, want ErrBusClosed", err)
	}
}

// TestConcurrentPublishAndSubscribe is the -race workhorse: many publishers,
// subscribers registering mid-flight, one handler that publishes back onto the
// bus (re-entrancy must not deadlock).
func TestConcurrentPublishAndSubscribe(t *testing.T) {
	// Buffer generously so this test measures concurrency safety, not the
	// drop policy (which TestDroppedCounterIncrementsOnSaturation covers).
	bus := New(Config{Buffer: 8192, Logger: quietConfig().Logger})
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := bus.Close(ctx); err != nil {
			t.Errorf("Close: %v", err)
		}
	})

	var received atomic.Int64
	_ = bus.Subscribe(SubjectExposureAll, "counter", func(_ context.Context, _ Event) error {
		received.Add(1)
		return nil
	})
	// Re-entrant handler: publishing from inside a handler must not deadlock
	// against the publisher's read lock.
	_ = bus.Subscribe(SubjectExposureFlagged, "reentrant", func(ctx context.Context, _ Event) error {
		return bus.Publish(ctx, SubjectExposureCleared, json.RawMessage(`{}`))
	})

	const (
		publishers = 8
		perGoro    = 100
	)
	var wg sync.WaitGroup
	for i := 0; i < publishers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < perGoro; j++ {
				_ = bus.Publish(context.Background(), SubjectExposureFlagged, json.RawMessage(`{"n":1}`))
			}
		}()
	}
	// Register more subscribers while publishing is in flight.
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; i < 20; i++ {
			_ = bus.Subscribe(SubjectExposureEscalated, "churn", func(context.Context, Event) error { return nil })
		}
	}()
	wg.Wait()

	// The "counter" subscriber was registered before any publish and its queue
	// (DefaultBuffer=1024) comfortably holds publishers*perGoro=800 events, so
	// every direct publish must land. The re-entrant CLEARED republishes are
	// additional, hence >=.
	want := int64(publishers * perGoro)
	if !waitFor(t, 5*time.Second, func() bool { return received.Load() >= want }) {
		t.Fatalf("received %d events, want at least %d", received.Load(), want)
	}
}

func TestNewEventWithIDIsStable(t *testing.T) {
	a := NewEventWithID("fixed-key", SubjectExposureFlagged, json.RawMessage(`{}`))
	b := NewEventWithID("fixed-key", SubjectExposureFlagged, json.RawMessage(`{}`))
	if a.EventID != b.EventID {
		t.Errorf("NewEventWithID produced %q and %q; caller-supplied ids must be preserved verbatim", a.EventID, b.EventID)
	}
	c := NewEvent(SubjectExposureFlagged, json.RawMessage(`{}`))
	d := NewEvent(SubjectExposureFlagged, json.RawMessage(`{}`))
	if c.EventID == d.EventID {
		t.Error("NewEvent produced duplicate ids; want a fresh id per call")
	}
}

func TestDroppedCounterIncrementsOnSaturation(t *testing.T) {
	bus := newInProcessBus(Config{Buffer: 1, Logger: quietConfig().Logger})
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = bus.Close(ctx)
	})

	block := make(chan struct{})
	started := make(chan struct{}, 1)
	_ = bus.Subscribe(SubjectExposureAll, "stuck", func(_ context.Context, _ Event) error {
		select {
		case started <- struct{}{}:
		default:
		}
		<-block
		return nil
	})

	// First event is consumed by the worker; wait for it to be in the handler.
	_ = bus.Publish(context.Background(), SubjectExposureFlagged, json.RawMessage(`{}`))
	select {
	case <-started:
	case <-time.After(2 * time.Second):
		close(block)
		t.Fatal("handler never ran")
	}
	// Second fills the depth-1 queue; third onward must be dropped.
	for i := 0; i < 5; i++ {
		_ = bus.Publish(context.Background(), SubjectExposureFlagged, json.RawMessage(`{}`))
	}
	if got := bus.Dropped(); got == 0 {
		t.Error("Dropped() = 0, want > 0 after saturating a depth-1 queue")
	}
	close(block)
}
