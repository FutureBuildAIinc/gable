// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package outbox

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/gablelbm/gable/pkg/database"
	"github.com/gablelbm/gable/pkg/eventbus"
	"github.com/jackc/pgx/v5"
)

// Drain defaults: the poll interval keeps the near-immediate feel the
// in-process bus had (a mutation's side effects follow within a fraction of
// a second of commit), and the batch bounds one tick's window. Both are
// fields, so tests and future callers can tune them.
const (
	DefaultDrainInterval = 250 * time.Millisecond
	DefaultDrainBatch    = 200
)

// DefaultDrainMaxAttempts is how many consecutive failed deliveries of one
// row a subscriber's drain will retry before it parks the row and moves on.
const DefaultDrainMaxAttempts = 10

// drainSubscriber is one registered consumer of the drain: a durable name
// (its event_subscriber_cursors key), the subject pattern its rows are
// matched by, and the handler rows are delivered to synchronously. replay
// marks a subscriber that opted into the outbox's history rather than
// starting at the head.
type drainSubscriber struct {
	pattern string
	durable string
	handler eventbus.Handler
	replay  bool
}

// DrainRunner delivers committed outbox rows to registered subscribers, one
// cursor per subscriber, so subscribers receive events from rows that
// committed with the mutation instead of from whatever one process kept in
// memory.
//
// Delivery is synchronous, row by row, inside one short transaction per
// subscriber per tick: the handler is called with the row's event_id as the
// bus event id, and the cursor advances past a row only when its handler
// returned nil. A failing handler stops that subscriber's pass with the
// cursor left before the failed row and the attempt counted on the cursor
// row; after DefaultDrainMaxAttempts consecutive failures the row is parked
// (event_subscriber_parked) and the cursor moves on. A crash between a
// handler returning nil and the pass committing replays the window rather
// than losing it, so delivery is at-least-once and subscribers that must not
// act twice dedup on event_id.
type DrainRunner struct {
	db          *database.DB
	logger      *slog.Logger
	interval    time.Duration
	batch       int
	maxAttempts int

	mu      sync.Mutex
	subs    []drainSubscriber
	started bool
	stop    context.CancelFunc
	done    chan struct{}
}

// NewDrainRunner builds a runner over db. Register subscribers with
// Subscribe (or SubscribeReplay) before Start; Start after that is an error.
func NewDrainRunner(db *database.DB, logger *slog.Logger) *DrainRunner {
	if logger == nil {
		logger = slog.Default()
	}
	return &DrainRunner{
		db:          db,
		logger:      logger,
		interval:    DefaultDrainInterval,
		batch:       DefaultDrainBatch,
		maxAttempts: DefaultDrainMaxAttempts,
	}
}

// Subscribe registers one subscriber at the head of the feed with the
// handler its rows are delivered to: Start creates its cursor row at the
// feed's current maximum position, so the subscriber receives events
// committed after it registered. The outbox is a replay window, not a
// ledger; a consumer that wants the history opts in with SubscribeReplay.
// The handler runs inside the drain's pass transaction on each delivery, so
// its database reads and writes go through that transaction (GetExecutor
// resolves it); it must not block for long, and a nil handler disables the
// subscriber. Call before Start.
func (d *DrainRunner) Subscribe(pattern, durable string, h eventbus.Handler) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.subs = append(d.subs, drainSubscriber{pattern: pattern, durable: durable, handler: h})
}

// SubscribeReplay registers one subscriber whose cursor starts at 0: the
// explicit opt-in to the outbox's whole history, not just events committed
// after registration.
func (d *DrainRunner) SubscribeReplay(pattern, durable string, h eventbus.Handler) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.subs = append(d.subs, drainSubscriber{pattern: pattern, durable: durable, handler: h, replay: true})
}

// Start begins the poll loop, in its own goroutine, on a context detached
// from the caller's: Stop owns shutdown. An immediate first pass runs so a
// restart delivers what committed while the process was down without
// waiting a full interval. Starting twice is an error; starting a stopped
// runner is not supported (build a new one).
func (d *DrainRunner) Start(_ context.Context) error {
	d.mu.Lock()
	if d.started {
		d.mu.Unlock()
		return errDrainStarted
	}
	d.started = true
	subs := d.snapshotSubsLocked()
	d.mu.Unlock()

	// Cursor rows are created once, here, before any pass runs. A drain that
	// finds no cursor row treats it as busy-or-missing and skips the tick;
	// without pre-created rows that same state would read as "new
	// subscriber" and replay the whole outbox from position 0.
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := d.ensureCursors(ctx, subs); err != nil {
		d.mu.Lock()
		d.started = false
		d.mu.Unlock()
		return err
	}

	runCtx, stop := context.WithCancel(context.Background())
	d.mu.Lock()
	d.stop = stop
	d.done = make(chan struct{})
	d.mu.Unlock()

	go d.loop(runCtx)
	return nil
}

var errDrainStarted = errors.New("outbox drain: already started")

// ensureCursors creates each subscriber's cursor row exactly once (ON
// CONFLICT DO NOTHING). A new subscriber starts at the feed's head unless it
// opted into replay.
func (d *DrainRunner) ensureCursors(ctx context.Context, subs []drainSubscriber) error {
	for _, sub := range subs {
		start := int64(0)
		if !sub.replay {
			var err error
			start, err = headPosition(ctx, d.db.Pool)
			if err != nil {
				return fmt.Errorf("outbox drain: read the head position: %w", err)
			}
		}
		if _, err := d.db.Pool.Exec(ctx,
			`INSERT INTO event_subscriber_cursors (subscriber, position) VALUES ($1, $2)
			 ON CONFLICT (subscriber) DO NOTHING`, sub.durable, start); err != nil {
			return fmt.Errorf("outbox drain: create cursor for %s: %w", sub.durable, err)
		}
	}
	return nil
}

// headPosition is the feed's current maximum position, 0 on an empty table:
// where a newly registered, non-replay subscriber starts.
func headPosition(ctx context.Context, ex database.Executor) (int64, error) {
	var pos int64
	if err := ex.QueryRow(ctx,
		`SELECT coalesce(max(position), 0) FROM events_outbox`).Scan(&pos); err != nil {
		return 0, err
	}
	return pos, nil
}

// Stop cancels the loop and waits for the in-flight pass to finish, so no
// drain statement is still running when the caller closes the pool. The
// in-flight window is never cancelled mid-transaction (an aborted pass would
// repeat its deliveries after a restart): the stop signal is checked between
// subscriber passes. Safe on a runner that was never started, and safe to
// call twice.
func (d *DrainRunner) Stop() {
	d.mu.Lock()
	cancel := d.stop
	done := d.done
	d.mu.Unlock()
	if cancel == nil {
		return
	}
	cancel()
	if done != nil {
		<-done
	}
}

func (d *DrainRunner) loop(ctx context.Context) {
	defer close(d.done)
	d.pass(ctx)
	t := time.NewTicker(d.interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			d.pass(ctx)
		}
	}
}

// pass runs one drain pass over every registered subscriber. A failing
// subscriber is logged and does not block the others. The stop signal is
// checked between subscriber passes, so an in-flight pass always finishes.
func (d *DrainRunner) pass(ctx context.Context) {
	for _, sub := range d.snapshotSubs() {
		if ctx.Err() != nil {
			return
		}
		if err := d.drainOne(ctx, sub); err != nil {
			if ctx.Err() != nil {
				return
			}
			d.logger.Error("outbox drain: subscriber pass failed",
				"subscriber", sub.durable, "error", err)
		}
	}
}

func (d *DrainRunner) snapshotSubs() []drainSubscriber {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.snapshotSubsLocked()
}

func (d *DrainRunner) snapshotSubsLocked() []drainSubscriber {
	subs := make([]drainSubscriber, len(d.subs))
	copy(subs, d.subs)
	return subs
}

// drainOne drains one subscriber in a single short transaction: lock the
// cursor row (SKIP LOCKED, so a second drain instance holding it makes this
// tick a no-op for this subscriber), read one window of rows past the
// cursor in position order, deliver each matching row to the subscriber's
// handler synchronously, then advance the cursor to the last row whose
// handler returned nil (non-matching rows advance the cursor too, matched or
// not) and commit.
//
// The whole window runs on a context detached from the loop's cancellation:
// Stop (or any other cancellation) never aborts a delivery mid-transaction,
// because an aborted pass repeats its deliveries after a restart; the stop
// signal is checked between passes instead.
//
// The handler is called with the pass's transaction context, so its reads
// and writes go through that transaction (GetExecutor resolves it) and the
// transaction never reaches for a second pool connection. The cursor
// advances past a row only when its handler returned nil; the first error
// stops the subscriber's pass with the cursor left before the failed row
// and the attempt counted on the cursor row. After maxAttempts consecutive
// failures of the same row the row is parked and the cursor moves on.
func (d *DrainRunner) drainOne(ctx context.Context, sub drainSubscriber) (err error) {
	if sub.handler == nil {
		return nil
	}
	passCtx := context.WithoutCancel(ctx)
	return d.db.RunInTx(passCtx, func(txCtx context.Context) error {
		ex := d.db.GetExecutor(txCtx)

		var after int64
		var attempts int
		err := ex.QueryRow(txCtx,
			`SELECT position, attempts FROM event_subscriber_cursors WHERE subscriber = $1 FOR UPDATE SKIP LOCKED`,
			sub.durable,
		).Scan(&after, &attempts)
		if errors.Is(err, pgx.ErrNoRows) {
			// No row under SKIP LOCKED means another drain instance holds
			// this subscriber's cursor (a rolling deploy, a second replica,
			// the worker beside serve), or the row is missing: either way
			// this tick skips the subscriber. It never means "new
			// subscriber, start from position 0" - Start creates the cursor
			// rows, and replaying the outbox to a subscriber that already
			// consumed it would duplicate every delivery in its history.
			return nil
		} else if err != nil {
			return err
		}

		rows, err := ListEvents(txCtx, ex, after, nil, d.batch)
		if err != nil {
			return err
		}

		for _, r := range rows {
			if !eventbus.SubjectMatches(sub.pattern, r.Event.Type) {
				after = r.Position
				continue
			}
			if derr := d.deliver(txCtx, sub, r); derr != nil {
				attempts++
				if attempts < d.maxAttempts {
					// The first error stops this subscriber's pass: the
					// cursor stays before the failed row, the attempt is
					// counted, and the next tick retries it.
					d.logger.Error("outbox drain: handler failed; will retry",
						"subscriber", sub.durable, "event_id", r.Event.ID,
						"type", r.Event.Type, "attempts", attempts, "error", derr)
					break
				}
				// Poison row: park it, log it, move the cursor on so one
				// event cannot stall the subscriber forever.
				d.logger.Error("outbox drain: parking event after failed attempts",
					"subscriber", sub.durable, "event_id", r.Event.ID,
					"type", r.Event.Type, "attempts", attempts, "error", derr)
				if _, err := ex.Exec(txCtx,
					`INSERT INTO event_subscriber_parked (subscriber, position, event_id, type, attempts)
					 VALUES ($1, $2, $3, $4, $5) ON CONFLICT (subscriber, position) DO NOTHING`,
					sub.durable, r.Position, r.Event.ID, r.Event.Type, attempts); err != nil {
					return err
				}
				after = r.Position
				attempts = 0
				continue
			}
			after = r.Position
			attempts = 0
		}

		_, err = ex.Exec(txCtx,
			`UPDATE event_subscriber_cursors
			 SET position = GREATEST(position, $2), attempts = $3, updated_at = NOW()
			 WHERE subscriber = $1`,
			sub.durable, after, attempts)
		return err
	})
}

// deliver calls the subscriber's handler for one row, synchronously, with
// the outbox row's event_id as the bus event id (so replays of the same row
// dedup downstream on the identity the mutation minted) and the row's data
// as the payload. A panicking handler is recovered and reported as a failed
// delivery, which counts as an attempt like any other error.
func (d *DrainRunner) deliver(ctx context.Context, sub drainSubscriber, r Row) (err error) {
	defer func() {
		if p := recover(); p != nil {
			err = fmt.Errorf("outbox drain: handler panicked: %v", p)
		}
	}()
	return sub.handler(ctx, eventbus.NewEventWithID(r.Event.ID.String(), r.Event.Type, r.Event.Data))
}
