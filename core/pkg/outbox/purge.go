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
)

// Purge defaults: the pass runs hourly and deletes in batches of 500, the
// same batch the idempotency purge uses. Both are fields on the runner, so
// tests can tune them.
const (
	DefaultPurgeInterval = time.Hour
	DefaultPurgeBatch    = 500
)

// purgeSQL deletes one batch of rows the retention policy lets go (ADR 0003
// section 6). A row is deletable when ALL of these hold:
//
//   - its event time is older than the cutoff (the retention age);
//   - its position is at or below the lowest subscriber cursor, because a
//     cursor holds the position its subscriber has delivered or parked
//     through, so every row past the lowest cursor is a row some drain still
//     owes a subscriber; with no registered subscriber nothing is owed and
//     age alone decides;
//   - no event_subscriber_parked entry names its position: a parked row's
//     cursor has moved past it, so the cursor floor does not protect it, and
//     its event stays until an operator resolves the entry by deleting it.
//
// The batch is picked FOR UPDATE SKIP LOCKED, so two purges (a rolling
// deploy, a second worker) take disjoint batches instead of queueing. It is
// one statement: no transaction is opened here, and the floor is read in the
// same statement as the delete. Cursors only move forward, so a floor read a
// moment stale is only more conservative.
const purgeSQL = `
WITH floor AS (
    SELECT min(position) AS pos, count(*) AS subscribers FROM event_subscriber_cursors
), victims AS (
    SELECT o.position
    FROM events_outbox o, floor f
    WHERE o.at < $1
      AND (f.subscribers = 0 OR o.position <= f.pos)
      AND NOT EXISTS (SELECT 1 FROM event_subscriber_parked p WHERE p.position = o.position)
    ORDER BY o.position
    LIMIT $2
    FOR UPDATE OF o SKIP LOCKED
)
DELETE FROM events_outbox WHERE position IN (SELECT position FROM victims)`

// PurgeOnce deletes at most batch purgeable rows older than cutoff and
// returns how many it deleted. It is a single statement through ex.
func PurgeOnce(ctx context.Context, ex database.Executor, cutoff time.Time, batch int) (int64, error) {
	if batch <= 0 {
		return 0, errors.New("outbox: purge batch must be positive")
	}
	tag, err := ex.Exec(ctx, purgeSQL, cutoff, batch)
	if err != nil {
		return 0, fmt.Errorf("outbox: purge: %w", err)
	}
	return tag.RowsAffected(), nil
}

// PurgeAll runs PurgeOnce until a batch comes back short, so one pass
// empties everything purgeable without one unbounded statement. The context
// is checked between batches; the rows deleted so far are returned with the
// error when it ends the loop early.
func PurgeAll(ctx context.Context, ex database.Executor, cutoff time.Time, batch int) (int64, error) {
	var total int64
	for {
		if err := ctx.Err(); err != nil {
			return total, err
		}
		n, err := PurgeOnce(ctx, ex, cutoff, batch)
		total += n
		if err != nil {
			return total, err
		}
		if n < int64(batch) {
			return total, nil
		}
	}
}

// PurgeRunner is the outbox retention job of the worker role: on a ticker it
// deletes events_outbox rows older than the retention age that no drain still
// owes a subscriber (see purgeSQL). The retention age of zero or less
// disables it.
type PurgeRunner struct {
	db        *database.DB
	logger    *slog.Logger
	retention time.Duration
	interval  time.Duration
	batch     int

	mu      sync.Mutex
	started bool
	stop    context.CancelFunc
	done    chan struct{}
}

// NewPurgeRunner builds a runner that keeps events for retention.
func NewPurgeRunner(db *database.DB, logger *slog.Logger, retention time.Duration) *PurgeRunner {
	return &PurgeRunner{
		db:        db,
		logger:    logger,
		retention: retention,
		interval:  DefaultPurgeInterval,
		batch:     DefaultPurgeBatch,
	}
}

// Start begins the ticker loop in its own goroutine, with an immediate first
// pass so a restart does not wait a full interval. Starting twice is an
// error; with retention disabled Start logs it and starts nothing.
func (p *PurgeRunner) Start(_ context.Context) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.started {
		return errors.New("outbox purge: already started")
	}
	p.started = true
	if p.retention <= 0 {
		p.logger.Info("outbox purge: disabled (retention is zero)")
		return nil
	}
	ctx, cancel := context.WithCancel(context.Background())
	p.stop = cancel
	p.done = make(chan struct{})
	go p.loop(ctx, p.done)
	p.logger.Info("outbox purge: started", "retention", p.retention.String(),
		"interval", p.interval.String(), "batch", p.batch)
	return nil
}

// Stop ends the loop and waits for a pass in flight, so no purge statement is
// still running when the caller closes the pool. A batch is one statement, so
// cancelling it mid-flight leaves nothing half done. Safe on a runner never
// started and safe to call twice.
func (p *PurgeRunner) Stop() {
	p.mu.Lock()
	cancel, done := p.stop, p.done
	p.mu.Unlock()
	if cancel == nil {
		return
	}
	cancel()
	<-done
}

func (p *PurgeRunner) loop(ctx context.Context, done chan struct{}) {
	defer close(done)
	p.pass(ctx)
	t := time.NewTicker(p.interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			p.pass(ctx)
		}
	}
}

func (p *PurgeRunner) pass(ctx context.Context) {
	defer func() {
		if r := recover(); r != nil {
			p.logger.Error("outbox purge: pass panicked", "panic", r)
		}
	}()
	n, err := PurgeAll(ctx, p.db.Pool, time.Now().Add(-p.retention), p.batch)
	if err != nil && ctx.Err() == nil {
		p.logger.Error("outbox purge: pass failed", "deleted", n, "error", err)
		return
	}
	if n > 0 {
		p.logger.Info("outbox purge: deleted retained events past the age", "deleted", n)
	}
}
