// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package worker

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"github.com/gablelbm/gable/internal/quote"
	"github.com/gablelbm/gable/pkg/branchctx"
	"github.com/gablelbm/gable/pkg/database"
	"github.com/gablelbm/gable/pkg/outbox"
)

// quoteExpiryInterval is how often the worker sweeps SENT quotes past their
// Valid until. A quote's expiry is minute-scale business and the pass is one
// small indexed query, so the terminal sweep's cadence is plenty.
const quoteExpiryInterval = 15 * time.Second

// quoteExpiryRunner is the quote expiry sweep (QE-01-a): SENT quotes whose
// expires_at has passed move to EXPIRED through the quote service's own
// transition, so the event and the audit ride the act's transaction
// (ADR 0003) and the same lifecycle rules decide it. The pass is batched and
// idempotent: a quote moves at most once, and a second pass finds nothing.
type quoteExpiryRunner struct {
	quotes    *quote.Service
	logger    *slog.Logger
	interval  time.Duration
	mu        sync.Mutex
	stop      context.CancelFunc
	done      chan struct{}
	sweepOnce func(context.Context, time.Time) (int, error)
}

func newQuoteExpiryRunner(db *database.DB, logger *slog.Logger) *quoteExpiryRunner {
	if logger == nil {
		logger = slog.Default()
	}
	quotes := quote.NewService(quote.NewRepository(db)).
		WithOutbox(outbox.NewWriter(db, "")).WithTxRunner(db)
	r := &quoteExpiryRunner{quotes: quotes, logger: logger, interval: quoteExpiryInterval}
	r.sweepOnce = func(ctx context.Context, now time.Time) (int, error) {
		// The sweep is a system caller, as the portal's decisions are: it
		// touches every branch's quotes, and the record branch rule fails
		// closed on a branch-free context.
		return quotes.SweepExpired(branchctx.WithSystem(ctx), now, 0)
	}
	return r
}

// SweepOnce moves one batch and returns how many it moved.
func (r *quoteExpiryRunner) SweepOnce(ctx context.Context, now time.Time) (int, error) {
	moved, err := r.sweepOnce(ctx, now)
	if err != nil {
		return 0, err
	}
	if moved > 0 {
		r.logger.Info("the quote expiry sweep ran", "expired", moved)
	}
	return moved, nil
}

// Start runs the sweep on its interval until Stop.
func (r *quoteExpiryRunner) Start() {
	ctx, cancel := context.WithCancel(context.Background())
	r.mu.Lock()
	r.stop, r.done = cancel, make(chan struct{})
	r.mu.Unlock()
	go r.loop(ctx)
}

func (r *quoteExpiryRunner) loop(ctx context.Context) {
	defer close(r.done)
	t := time.NewTicker(r.interval)
	defer t.Stop()
	for {
		if _, err := r.SweepOnce(context.WithoutCancel(ctx), time.Now().UTC()); err != nil {
			r.logger.Error("quote expiry sweep failed", "error", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

// Stop ends the sweep and waits for the pass in flight.
func (r *quoteExpiryRunner) Stop() {
	r.mu.Lock()
	cancel, done := r.stop, r.done
	r.mu.Unlock()
	if cancel == nil {
		return
	}
	cancel()
	<-done
}
