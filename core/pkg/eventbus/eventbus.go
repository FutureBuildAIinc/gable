// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

// Package eventbus provides a thin publish/subscribe seam for decoupled,
// best-effort domain events (currently lumber quote price-exposure events).
//
// # Why this exists
//
// Producers (the exposure scanner, the exposure service) must not know about
// consumers (the email notifier). The Bus interface is that seam. It is
// deliberately transport-agnostic so no producer or consumer imports a broker
// client directly.
//
// # What backs it
//
// Gable ships a single Go binary and no broker. The only implementation is
// therefore an in-process, in-memory fan-out (see inprocess.go). A self-hoster
// runs `gable` and nothing else.
//
// # What this is NOT
//
// This is not a message broker, and code must never depend on it for
// correctness. Compared with a real broker (NATS JetStream, Kafka, RabbitMQ)
// the in-process bus explicitly does NOT provide:
//
//   - Durability. Events live in a bounded in-memory channel. A crash, a
//     restart, or a deploy loses every event still in flight.
//   - Cross-process delivery. Only subscribers registered inside THIS process
//     ever see an event. Two replicas behind a load balancer each see only
//     their own locally-produced events.
//   - Redelivery / acknowledgement. A Handler that returns an error is logged
//     and the event is dropped. There is no retry, no dead-letter queue, and
//     no at-least-once guarantee. Delivery is at-most-once.
//   - Backpressure. Publish never blocks. When a subscriber's queue is full
//     the event is dropped (and logged) rather than stalling the request
//     goroutine that produced it.
//   - Ordering across subscribers. Each subscriber drains its own queue in
//     order, but two subscribers may observe the same two events at different
//     times relative to each other.
//   - Replay. A subscriber registered after a Publish never sees that event.
//
// Every durable fact in this subsystem lives in PostgreSQL (the
// quote_exposure_events ledger, the price_escalators state column, the quotes
// rollup columns). The bus only drives side-effects — notification emails —
// and the nightly safety-net scan in internal/quote/exposure_scheduler.go
// exists precisely to recover from events this bus dropped.
//
// # Swapping in a real broker
//
// Everything above is an implementation property of inProcessBus, not of the
// interface. A deployment that needs durability or cross-process delivery can
// add a second Bus implementation (e.g. NATS JetStream with a durable
// consumer) and select it in New. No producer or consumer changes: they only
// ever see Publisher, Subscriber, Handler and Event. The `durable` argument on
// Subscribe is carried by the interface for exactly that reason — the
// in-process backend uses it only as a log label, but a JetStream backend
// would use it as the durable consumer name.
package eventbus

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/google/uuid"
)

// Subject constants for the quote price-exposure domain. Subjects are
// dot-delimited tokens and follow NATS naming so a future broker-backed
// implementation can bind the same strings verbatim. The trailing wildcard
// (SubjectExposureAll) uses NATS token semantics: ">" matches one or more
// trailing tokens.
const (
	SubjectExposureFlagged      = "quote.exposure.flagged"
	SubjectExposureEscalated    = "quote.exposure.escalated"
	SubjectExposureAckRequired  = "quote.exposure.ack_required"
	SubjectExposureAcknowledged = "quote.exposure.acknowledged"
	SubjectExposureCleared      = "quote.exposure.cleared"

	// SubjectExposureAll matches every quote.exposure.* subject.
	SubjectExposureAll = "quote.exposure.>"
)

// Backend identifies which transport a Bus is using. Reported for
// logging/observability so an operator can tell from the boot log which
// delivery guarantees are in force.
type Backend string

// BackendInProcess is the only backend shipped with Gable: in-memory,
// best-effort, single-process. See the package doc for what it does not
// guarantee.
const BackendInProcess Backend = "inprocess"

// ErrBusClosed is returned by Publish and Subscribe after Close.
var ErrBusClosed = errors.New("eventbus: bus is closed")

// Event is the envelope carried over the bus. EventID is stable per logical
// event and is used for idempotent consumer handling. Payload is opaque JSON.
type Event struct {
	EventID    string          `json:"event_id"`
	Subject    string          `json:"subject"`
	OccurredAt time.Time       `json:"occurred_at"`
	Payload    json.RawMessage `json:"payload"`
}

// NewEvent builds an Event with a fresh random EventID. Use this when the
// caller has no natural idempotency key; otherwise prefer NewEventWithID so
// retries collapse to a single logical event.
func NewEvent(subject string, payload json.RawMessage) Event {
	return NewEventWithID(uuid.NewString(), subject, payload)
}

// NewEventWithID builds an Event with a caller-supplied stable ID. The ID
// should be deterministic for a given logical occurrence (e.g. derived from
// the exposure event's idempotency key) so that duplicates dedup downstream.
func NewEventWithID(id, subject string, payload json.RawMessage) Event {
	return Event{
		EventID:    id,
		Subject:    subject,
		OccurredAt: time.Now().UTC(),
		Payload:    payload,
	}
}

// Handler processes a delivered Event. Handlers must be idempotent.
//
// A returned error is a signal to the backend that delivery failed. The
// in-process backend logs it and moves on — there is no redelivery. A handler
// that panics is recovered and logged; the panic never reaches the publisher
// and never stops other subscribers.
type Handler func(ctx context.Context, e Event) error

// Publisher publishes events. Publish is best-effort and non-blocking: it must
// never block the caller's request path and must not hard-fail when the
// backend is degraded. A returned error is informational (e.g. marshal
// failure or a closed bus) and callers typically log-and-continue.
type Publisher interface {
	Publish(ctx context.Context, subject string, payload json.RawMessage) error
}

// Subscriber registers a handler for a subject pattern. durable names the
// consumer; the in-process backend uses it only as a log label, but it is part
// of the interface so a durable backend can use it as a cursor name.
//
// Patterns support NATS token wildcards: "*" matches exactly one token, ">"
// matches one or more trailing tokens and must be the final token.
type Subscriber interface {
	Subscribe(pattern, durable string, h Handler) error
}

// Bus is the full event bus surface: publish, subscribe, introspect backend,
// and graceful close.
type Bus interface {
	Publisher
	Subscriber
	// Backend reports the active transport for logging/observability.
	Backend() Backend
	// Close stops delivery and waits for in-flight handlers, honoring ctx for
	// shutdown deadlines. Close is idempotent.
	Close(ctx context.Context) error
}

// Config controls bus construction.
type Config struct {
	// Buffer is the per-subscriber queue depth. Once a subscriber's queue is
	// full, further matching events are dropped for that subscriber (and only
	// that subscriber). Defaults to DefaultBuffer when <= 0.
	Buffer int
	// Logger receives drop/handler-error/panic diagnostics. Defaults to
	// slog.Default() when nil.
	Logger Logger
}

// Logger is the minimal logging surface the bus needs. *slog.Logger satisfies
// it, which keeps the package free of a hard dependency on a logging choice.
type Logger interface {
	Warn(msg string, args ...any)
	Error(msg string, args ...any)
}

// DefaultBuffer is the per-subscriber queue depth used when Config.Buffer is
// unset. Sized so a burst from a full safety-net scan (bounded by the number
// of active escalators) is absorbed without dropping.
const DefaultBuffer = 1024

// New constructs a Bus. It never returns nil and never fails: the only backend
// is in-process, so there is nothing to dial and no reason for boot to block
// on the bus.
func New(cfg Config) Bus {
	return newInProcessBus(cfg)
}
