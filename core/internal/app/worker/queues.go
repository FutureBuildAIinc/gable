// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package worker

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"github.com/gablelbm/gable/internal/payment"
	"github.com/gablelbm/gable/pkg/audit"
	"github.com/gablelbm/gable/pkg/database"
	"github.com/google/uuid"
)

// queueInterval is how often a queue job looks for work. A request left
// waiting is picked up within one interval; a job that served one drains the
// queue before sleeping again.
const queueInterval = time.Second

// terminalSweepInterval is how often the worker sweeps expired counter
// terminal intents across every branch.
const terminalSweepInterval = 15 * time.Second

// queueRunner is one background job that serves a request queue: it calls
// serve until it answers false (the queue is empty), then waits an interval
// (ADR 0005 5.4 and 5.5). An error is logged and the job waits the interval,
// so a poison request never spins; the queue's own attempt counting decides
// when a request is parked. Stop waits for the serve in flight.
type queueRunner struct {
	name   string
	serve  func(ctx context.Context) (bool, error)
	logger *slog.Logger

	mu   sync.Mutex
	stop context.CancelFunc
	done chan struct{}
}

func newQueueRunner(name string, serve func(ctx context.Context) (bool, error), logger *slog.Logger) *queueRunner {
	return &queueRunner{name: name, serve: serve, logger: logger}
}

func (q *queueRunner) Start() {
	ctx, cancel := context.WithCancel(context.Background())
	q.mu.Lock()
	q.stop, q.done = cancel, make(chan struct{})
	q.mu.Unlock()
	go q.loop(ctx)
}

func (q *queueRunner) loop(ctx context.Context) {
	defer close(q.done)
	t := time.NewTicker(queueInterval)
	defer t.Stop()
	for {
		q.drain(ctx)
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

// drain serves requests until the queue is empty, the context ends or a serve
// fails.
func (q *queueRunner) drain(ctx context.Context) {
	for ctx.Err() == nil {
		// the serve runs on a context detached from the loop's cancellation:
		// Stop never aborts a transaction mid-flight
		served, err := q.serve(context.WithoutCancel(ctx))
		if err != nil {
			q.logger.Error("queue job: serve failed", "job", q.name, "error", err)
			return
		}
		if !served {
			return
		}
	}
}

// Stop cancels the loop and waits for the in-flight serve to finish. Safe on a
// runner that was never started, and safe to call twice.
func (q *queueRunner) Stop() {
	q.mu.Lock()
	cancel, done := q.stop, q.done
	q.mu.Unlock()
	if cancel == nil {
		return
	}
	cancel()
	<-done
}

// terminalSweepRunner runs the counter terminal intent sweep across every
// branch on an interval, turning expired claimed intents into lost and filing
// one manager item per expired pending_at_device leg.
type terminalSweepRunner struct {
	ledger   *payment.TerminalLedger
	auditLog *audit.Logger
	logger   *slog.Logger
	interval time.Duration

	mu   sync.Mutex
	stop context.CancelFunc
	done chan struct{}
}

func newTerminalSweepRunner(db *database.DB, logger *slog.Logger) *terminalSweepRunner {
	if logger == nil {
		logger = slog.Default()
	}
	return &terminalSweepRunner{
		ledger:   payment.NewTerminalLedger(db),
		auditLog: audit.NewLogger(db),
		logger:   logger,
		interval: terminalSweepInterval,
	}
}

func (r *terminalSweepRunner) SweepOnce(ctx context.Context, now time.Time) (*payment.SweepResult, error) {
	res, err := r.ledger.Sweep(ctx, now)
	if err != nil {
		return nil, err
	}
	if res.ClaimedClosed > 0 || res.DispatchedLost > 0 {
		if r.auditLog != nil {
			if err := r.auditLog.Log(ctx, audit.Entry{
				Action: "pos.terminal_intents.swept", EntityType: "pos_transaction", EntityID: uuid.Nil,
				Changes: map[string]any{"claimed_closed": res.ClaimedClosed, "dispatched_lost": res.DispatchedLost},
			}); err != nil {
				r.logger.Error("terminal intent sweep audit row failed", "error", err)
			}
		}
		r.logger.Info("the terminal intent sweep ran",
			"claimed_closed", res.ClaimedClosed, "dispatched_lost", res.DispatchedLost)
	}
	return res, nil
}

func (r *terminalSweepRunner) Start() {
	ctx, cancel := context.WithCancel(context.Background())
	r.mu.Lock()
	r.stop, r.done = cancel, make(chan struct{})
	r.mu.Unlock()
	go r.loop(ctx)
}

func (r *terminalSweepRunner) loop(ctx context.Context) {
	defer close(r.done)
	t := time.NewTicker(r.interval)
	defer t.Stop()
	for {
		if _, err := r.SweepOnce(context.WithoutCancel(ctx), time.Now().UTC()); err != nil {
			r.logger.Error("terminal intent sweep failed", "error", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

func (r *terminalSweepRunner) Stop() {
	r.mu.Lock()
	cancel, done := r.stop, r.done
	r.mu.Unlock()
	if cancel == nil {
		return
	}
	cancel()
	<-done
}
