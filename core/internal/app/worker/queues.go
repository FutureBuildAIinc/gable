// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package worker

import (
	"context"
	"log/slog"
	"sync"
	"time"
)

// queueInterval is how often a queue job looks for work. A request left
// waiting is picked up within one interval; a job that served one drains the
// queue before sleeping again.
const queueInterval = time.Second

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
