// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package outbox

import (
	"context"
	"errors"
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

// drainSubscriber is one registered consumer of the drain: a durable name
// (its event_subscriber_cursors key) and the subject pattern its events are
// routed by, mirroring its pkg/eventbus subscription.
type drainSubscriber struct {
	pattern string
	durable string
}

// DrainRunner republishes committed outbox rows to the in-process event
// bus, one cursor per registered subscriber, so subscribers keep receiving
// events exactly as they did when publishers called the bus directly, but
// from rows that committed with the mutation instead of from memory.
//
// Delivery is at least once across replays: the cursor advances only after
// the window has been handed to the bus, so a crash in between republishes
// the window. Subscribers that must not act twice dedup on event_id.
type DrainRunner struct {
	db       *database.DB
	bus      eventbus.Publisher
	logger   *slog.Logger
	interval time.Duration
	batch    int

	mu      sync.Mutex
	subs    []drainSubscriber
	started bool
	stop    context.CancelFunc
	done    chan struct{}
}

// NewDrainRunner builds a runner publishing to bus. Register subscribers
// with Subscribe before Start; Start after that is an error.
func NewDrainRunner(db *database.DB, bus eventbus.Publisher, logger *slog.Logger) *DrainRunner {
	if logger == nil {
		logger = slog.Default()
	}
	return &DrainRunner{
		db:       db,
		bus:      bus,
		logger:   logger,
		interval: DefaultDrainInterval,
		batch:    DefaultDrainBatch,
	}
}

// Subscribe registers one subscriber: its cursor key and the subject
// pattern its rows are matched by (the bus's own wildcard rules, since a
// row's type is its subject). Call before Start; a runner drains only what
// was registered when it started.
func (d *DrainRunner) Subscribe(pattern, durable string) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.subs = append(d.subs, drainSubscriber{pattern: pattern, durable: durable})
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
	ctx, cancel := context.WithCancel(context.Background())
	d.stop = cancel
	d.done = make(chan struct{})
	d.mu.Unlock()

	go d.loop(ctx)
	return nil
}

var errDrainStarted = errors.New("outbox drain: already started")

// Stop cancels the loop and waits for the in-flight tick to finish, so no
// drain statement is still running when the caller closes the pool. Safe on
// a runner that was never started, and safe to call twice.
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
// subscriber is logged and does not block the others.
func (d *DrainRunner) pass(ctx context.Context) {
	d.mu.Lock()
	subs := make([]drainSubscriber, len(d.subs))
	copy(subs, d.subs)
	d.mu.Unlock()

	for _, sub := range subs {
		if err := d.drainOne(ctx, sub); err != nil {
			if ctx.Err() != nil {
				return
			}
			d.logger.Error("outbox drain: subscriber pass failed",
				"subscriber", sub.durable, "error", err)
		}
	}
}

// drainOne drains one subscriber in a single short transaction: lock the
// cursor row (SKIP LOCKED, so a second drain instance skips a busy
// subscriber instead of queueing behind it), read windows of rows past the
// cursor in position order, publish the ones whose type matches the
// pattern, then advance the cursor to the highest position in the last
// window, matched or not, and commit.
//
// Publishing happens before the cursor advances: a crash in between
// republishes the window rather than losing it. The bus Publish is an
// in-memory hand-off, so no database work happens between the reads and
// the commit besides the cursor's own UPDATE, and the transaction never
// reaches for a second pool connection.
func (d *DrainRunner) drainOne(ctx context.Context, sub drainSubscriber) (err error) {
	return d.db.RunInTx(ctx, func(txCtx context.Context) error {
		ex := d.db.GetExecutor(txCtx)

		var after int64
		err := ex.QueryRow(txCtx,
			`SELECT position FROM event_subscriber_cursors WHERE subscriber = $1 FOR UPDATE SKIP LOCKED`,
			sub.durable,
		).Scan(&after)
		if errors.Is(err, pgx.ErrNoRows) {
			after = 0
		} else if err != nil {
			return err
		}

		for {
			rows, err := ListEvents(txCtx, ex, after, nil, d.batch)
			if err != nil {
				return err
			}
			if len(rows) == 0 {
				break
			}
			for _, r := range rows {
				if eventbus.SubjectMatches(sub.pattern, r.Event.Type) {
					if err := d.bus.Publish(txCtx, r.Event.Type, r.Event.Data); err != nil {
						d.logger.Warn("outbox drain: publish to bus failed",
							"subscriber", sub.durable, "type", r.Event.Type, "error", err)
					}
				}
				after = r.Position
			}
			if len(rows) < d.batch {
				break
			}
		}

		if _, err := ex.Exec(txCtx,
			`INSERT INTO event_subscriber_cursors (subscriber, position) VALUES ($1, $2)
			 ON CONFLICT (subscriber) DO UPDATE
			 SET position = GREATEST(event_subscriber_cursors.position, EXCLUDED.position),
			     updated_at = NOW()`,
			sub.durable, after,
		); err != nil {
			return err
		}
		return nil
	})
}
