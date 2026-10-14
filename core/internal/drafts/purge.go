// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package drafts

import (
	"context"
	"log/slog"
	"time"

	"github.com/gablelbm/gable/pkg/database"
)

// Purge defaults: the pass runs hourly and deletes in batches, the outbox
// purge's shape (ADR 0007 section 3.2 points at ADR 0003 section 6).
const (
	DefaultPurgeInterval = time.Hour
	DefaultPurgeBatch    = 500
)

// PurgeRunner deletes draft_events rows older than the retention in
// batches, recording the highest purged position in draft_events_purged so
// a client resuming from a cursor at or below it can be told to re-read
// (event: reset). It runs in the worker role. The draft itself is never
// purged; only its change rows are. A zero or negative retention turns the
// purge off (the outbox's rule).
type PurgeRunner struct {
	repo      *PostgresRepository
	logger    *slog.Logger
	retention time.Duration
	interval  time.Duration
	batch     int

	stop chan struct{}
	done chan struct{}
}

// NewPurgeRunner builds the runner.
func NewPurgeRunner(db *database.DB, logger *slog.Logger, retention time.Duration) *PurgeRunner {
	if logger == nil {
		logger = slog.Default()
	}
	return &PurgeRunner{
		repo: NewRepository(db), logger: logger, retention: retention,
		interval: DefaultPurgeInterval, batch: DefaultPurgeBatch,
		stop: make(chan struct{}), done: make(chan struct{}),
	}
}

// Start runs the purge loop until Stop. A zero or negative retention keeps
// the runner parked (the purge is off) rather than stopped, so Stop stays
// uniform for the caller.
func (p *PurgeRunner) Start(ctx context.Context) error {
	go func() {
		defer close(p.done)
		if p.retention <= 0 {
			p.logger.Info("drafts: draft_events purge is off (DRAFT_EVENTS_RETENTION is zero or negative)")
			<-p.stop
			return
		}
		ticker := time.NewTicker(p.interval)
		defer ticker.Stop()
		for {
			select {
			case <-p.stop:
				return
			case <-ctx.Done():
				return
			case <-ticker.C:
				deleted, _, err := p.repo.PurgeOnce(ctx, time.Now().UTC().Add(-p.retention), p.batch)
				if err != nil {
					p.logger.Warn("drafts: draft_events purge failed", "error", err)
					continue
				}
				if deleted > 0 {
					p.logger.Info("drafts: purged draft_events rows", "deleted", deleted)
				}
			}
		}
	}()
	return nil
}

// Stop ends the loop and waits for an in-flight batch.
func (p *PurgeRunner) Stop() {
	close(p.stop)
	<-p.done
}
