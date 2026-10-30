// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026, FutureBuild, Inc. and OpenLBM contributors

package worker

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"github.com/gablelbm/gable/internal/largecash"
	"github.com/gablelbm/gable/pkg/audit"
	"github.com/gablelbm/gable/pkg/database"
	"github.com/google/uuid"
)

// largeCashDeadlineInterval is how often the worker marks the large cash
// filings that are past their deadline. The deadline is fifteen days, so an
// hourly pass is plenty, and the pass is idempotent: a second pass marks
// nothing.
const largeCashDeadlineInterval = time.Hour

// largeCashDeadlineRunner is the large cash deadline sweep (CP-17b): a
// nightly pass that marks the filings that are past their deadline and still
// not filed, so the worklist carries the mark and a report can find them
// without recomputing the clock. It is the card present sweep's shape, the
// one the counter's lost answers run on.
type largeCashDeadlineRunner struct {
	largeCash *largecash.Service
	auditLog  *audit.Logger
	logger    *slog.Logger
	interval  time.Duration

	mu   sync.Mutex
	stop context.CancelFunc
	done chan struct{}
}

// newLargeCashDeadlineRunner builds the deadline sweep.
func newLargeCashDeadlineRunner(db *database.DB, logger *slog.Logger) *largeCashDeadlineRunner {
	if logger == nil {
		logger = slog.Default()
	}
	return &largeCashDeadlineRunner{
		largeCash: largecash.NewService(db, nil, ""),
		auditLog:  audit.NewLogger(db),
		logger:    logger,
		interval:  largeCashDeadlineInterval,
	}
}

// SweepOnce marks the filings that are past their deadline and returns how
// many it marked. The pass is idempotent: a second pass marks nothing.
func (r *largeCashDeadlineRunner) SweepOnce(ctx context.Context, now time.Time) (int64, error) {
	marked, err := r.largeCash.MarkPastDeadlines(ctx, now)
	if err != nil {
		return 0, err
	}
	if marked > 0 && r.auditLog != nil {
		if err := r.auditLog.Log(ctx, audit.Entry{
			Action: "large_cash.deadline.swept", EntityType: "large_cash_filing", EntityID: uuid.Nil,
			Changes: map[string]any{"marked": marked},
		}); err != nil {
			r.logger.Error("large cash deadline sweep audit row failed", "error", err)
		}
	}
	if marked > 0 {
		r.logger.Info("the large cash deadline sweep ran", "marked", marked)
	}
	return marked, nil
}

// Start runs the sweep on its interval until Stop.
func (r *largeCashDeadlineRunner) Start() {
	ctx, cancel := context.WithCancel(context.Background())
	r.mu.Lock()
	r.stop, r.done = cancel, make(chan struct{})
	r.mu.Unlock()
	go r.loop(ctx)
}

func (r *largeCashDeadlineRunner) loop(ctx context.Context) {
	defer close(r.done)
	t := time.NewTicker(r.interval)
	defer t.Stop()
	for {
		if _, err := r.SweepOnce(context.WithoutCancel(ctx), time.Now().UTC()); err != nil {
			r.logger.Error("large cash deadline sweep failed", "error", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

// Stop ends the sweep and waits for the pass in flight.
func (r *largeCashDeadlineRunner) Stop() {
	r.mu.Lock()
	cancel, done := r.stop, r.done
	r.mu.Unlock()
	if cancel == nil {
		return
	}
	cancel()
	<-done
}
