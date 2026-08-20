// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package eventbus

import (
	"context"
	"encoding/json"
	"log/slog"
	"runtime/debug"
	"strings"
	"sync"
	"sync/atomic"
)

// inProcessBus is the in-memory fan-out backing Bus. See the package doc for
// the full list of guarantees it does NOT make.
//
// Shape: every subscription owns a bounded queue and a dedicated worker
// goroutine. Publish does a non-blocking send into each *matching* queue and
// returns immediately. That gives three properties the exposure subsystem
// depends on:
//
//   - The publisher (an HTTP request goroutine, or the scanner loop) is never
//     blocked by handler latency.
//   - A slow subscriber cannot stall delivery to a fast one, because queues
//     and workers are per-subscription rather than shared.
//   - A panicking handler is recovered inside its own worker, so it takes down
//     neither the publisher nor any sibling subscriber.
type inProcessBus struct {
	buffer int
	logger Logger

	mu     sync.RWMutex
	subs   []*subscription
	closed bool

	// dropped counts events discarded because a subscriber queue was full.
	// Exported through Dropped for tests and operational visibility.
	dropped atomic.Uint64
}

// subscription is one registered handler plus the queue and worker that feed
// it.
type subscription struct {
	pattern string
	durable string
	handler Handler

	queue chan Event
	quit  chan struct{}
	done  chan struct{}
}

func newInProcessBus(cfg Config) *inProcessBus {
	buffer := cfg.Buffer
	if buffer <= 0 {
		buffer = DefaultBuffer
	}
	var logger Logger = cfg.Logger
	if logger == nil {
		logger = slog.Default()
	}
	return &inProcessBus{buffer: buffer, logger: logger}
}

func (b *inProcessBus) Backend() Backend { return BackendInProcess }

// Publish fans an event out to every subscriber whose pattern matches. It
// never blocks: a full subscriber queue causes the event to be dropped for
// that subscriber, counted, and logged.
//
// The returned error is informational. It is non-nil only when the bus is
// closed; a drop is NOT an error, because callers must not treat delivery as
// part of their success path.
func (b *inProcessBus) Publish(_ context.Context, subject string, payload json.RawMessage) error {
	b.mu.RLock()
	if b.closed {
		b.mu.RUnlock()
		return ErrBusClosed
	}
	// Copy the slice header's contents so dispatch happens outside the lock;
	// a handler that calls Subscribe must not deadlock against the publisher.
	matching := make([]*subscription, 0, len(b.subs))
	for _, s := range b.subs {
		if subjectMatch(s.pattern, subject) {
			matching = append(matching, s)
		}
	}
	b.mu.RUnlock()

	if len(matching) == 0 {
		return nil
	}

	e := NewEvent(subject, payload)
	for _, s := range matching {
		select {
		case s.queue <- e:
		default:
			// Send-or-drop: never block a request path on a saturated queue.
			b.dropped.Add(1)
			b.logger.Warn("eventbus(inprocess): subscriber queue full, dropping event",
				"durable", s.durable, "subject", subject, "event_id", e.EventID)
		}
	}
	return nil
}

// Subscribe registers h for pattern and starts its worker. Registering after
// Close returns ErrBusClosed. Subscriptions are not replayed: a handler only
// sees events published after it returns.
func (b *inProcessBus) Subscribe(pattern, durable string, h Handler) error {
	if h == nil {
		return nil
	}
	b.mu.Lock()
	if b.closed {
		b.mu.Unlock()
		return ErrBusClosed
	}
	s := &subscription{
		pattern: pattern,
		durable: durable,
		handler: h,
		queue:   make(chan Event, b.buffer),
		quit:    make(chan struct{}),
		done:    make(chan struct{}),
	}
	b.subs = append(b.subs, s)
	b.mu.Unlock()

	go b.worker(s)
	return nil
}

// Dropped reports how many (subscriber, event) deliveries were discarded
// because the subscriber's queue was full since process start.
func (b *inProcessBus) Dropped() uint64 { return b.dropped.Load() }

// worker drains one subscription's queue until Close. On shutdown it drains
// whatever is already queued before exiting, so a graceful stop delivers
// in-flight events rather than silently discarding them.
func (b *inProcessBus) worker(s *subscription) {
	defer close(s.done)
	for {
		select {
		case e := <-s.queue:
			b.deliver(s, e)
		case <-s.quit:
			// Drain the backlog, then stop. Non-blocking so a quiet queue
			// exits immediately.
			for {
				select {
				case e := <-s.queue:
					b.deliver(s, e)
				default:
					return
				}
			}
		}
	}
}

// deliver invokes one handler with panic recovery. Each handler gets a fresh
// background context: the originating request may already have returned (and
// had its context cancelled) by the time the worker runs.
func (b *inProcessBus) deliver(s *subscription, e Event) {
	defer func() {
		if p := recover(); p != nil {
			b.logger.Error("eventbus(inprocess): handler panicked",
				"durable", s.durable, "subject", e.Subject, "event_id", e.EventID,
				"panic", p, "stack", string(debug.Stack()))
		}
	}()
	if err := s.handler(context.Background(), e); err != nil {
		// No redelivery: this backend is at-most-once. The nightly safety-net
		// scan is the recovery path for anything lost here.
		b.logger.Error("eventbus(inprocess): handler error, event dropped",
			"durable", s.durable, "subject", e.Subject,
			"event_id", e.EventID, "error", err)
	}
}

// Close stops every worker and waits for them to finish draining, bounded by
// ctx. It is idempotent; subsequent calls return nil immediately.
func (b *inProcessBus) Close(ctx context.Context) error {
	b.mu.Lock()
	if b.closed {
		b.mu.Unlock()
		return nil
	}
	b.closed = true
	subs := make([]*subscription, len(b.subs))
	copy(subs, b.subs)
	b.mu.Unlock()

	for _, s := range subs {
		close(s.quit)
	}

	allDone := make(chan struct{})
	go func() {
		for _, s := range subs {
			<-s.done
		}
		close(allDone)
	}()

	select {
	case <-allDone:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// subjectMatch implements NATS token-wildcard matching for the subset we use:
//
//   - "*" matches exactly one token
//   - ">" matches one or more trailing tokens (must be the final token)
//
// Tokens are dot-delimited. Kept here rather than in the interface file
// because it is a property of this backend's matching, not of the seam.
func subjectMatch(pattern, subject string) bool {
	if pattern == subject {
		return true
	}
	pt := strings.Split(pattern, ".")
	st := strings.Split(subject, ".")
	for i, p := range pt {
		if p == ">" {
			// ">" must be the last pattern token and matches >=1 remaining.
			return i == len(pt)-1 && i < len(st)
		}
		if i >= len(st) {
			return false
		}
		if p == "*" {
			continue
		}
		if p != st[i] {
			return false
		}
	}
	return len(pt) == len(st)
}
