// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package middleware

import (
	"context"
	"fmt"
	"log"
	"strconv"

	"github.com/gablelbm/gable/pkg/database"
	"github.com/robfig/cron/v3"
)

// Settings keys consumed by the idempotency retention scheduler. Stored in
// the system_settings table (migration 039_system_settings.sql). Retention is
// ON by default (unlike the reorder and exposure crons, which need an
// operator opt-in): the middleware writes a row on every keyed POST and PUT,
// so a deployment that never flips a setting must not grow the table without
// bound. idempotency.purge_enabled=false is the opt-out.
const (
	settingIdempotencyPurgeEnabled = "idempotency.purge_enabled"
	settingIdempotencyPurgeCron    = "idempotency.purge_cron"

	// Default applied when the cron setting is absent or blank: 04:00 daily
	// (seconds-precision cron, matching cron.WithSeconds()), staggered off
	// the reorder (01:00/02:00) and exposure (03:00) crons.
	defaultIdempotencyPurgeCron = "0 0 4 * * *"

	// Rows deleted per statement in the purge loop (see
	// PurgeExpiredIdempotencyKeys).
	idempotencyPurgeBatchSize = 500
)

// idempotencySettingsReader is the minimal interface for fetching
// configuration from system_settings. Abstracted so unit tests can supply a
// map without a live Postgres.
type idempotencySettingsReader interface {
	Get(ctx context.Context, key string) (string, bool, error)
}

// dbIdempotencySettingsReader reads directly from the system_settings table.
type dbIdempotencySettingsReader struct {
	db *database.DB
}

func (r *dbIdempotencySettingsReader) Get(ctx context.Context, key string) (string, bool, error) {
	const q = `SELECT value FROM system_settings WHERE key = $1`
	var v string
	if err := r.db.GetExecutor(ctx).QueryRow(ctx, q, key).Scan(&v); err != nil {
		// Treat a missing key (or any read error) as "absent" rather than
		// failing the whole scheduler start.
		return "", false, nil //nolint:nilerr
	}
	return v, true, nil
}

// IdempotencyScheduler purges expired idempotency_keys rows on a cron. Every
// completed key is replayable for its retention window and every dead claim
// holds its key until the lease lapses, so the table only shrinks when this
// runs; that is why it is enabled by default rather than opt-in.
type IdempotencyScheduler struct {
	db       *database.DB
	settings idempotencySettingsReader
	cron     *cron.Cron
	enabled  bool
}

// NewIdempotencyScheduler wires the scheduler to its dependencies. Call
// Start to load settings and begin scheduling.
func NewIdempotencyScheduler(db *database.DB) *IdempotencyScheduler {
	return newIdempotencySchedulerWithSettings(db, &dbIdempotencySettingsReader{db: db})
}

// newIdempotencySchedulerWithSettings is the unit-test entry point: accepts a
// fake settings reader so tests need no Postgres.
func newIdempotencySchedulerWithSettings(db *database.DB, sr idempotencySettingsReader) *IdempotencyScheduler {
	return &IdempotencyScheduler{
		db:       db,
		settings: sr,
		cron:     cron.New(cron.WithSeconds()),
	}
}

// Start reads settings from system_settings; unless
// idempotency.purge_enabled is false it registers the purge job and starts
// the cron engine.
func (s *IdempotencyScheduler) Start(ctx context.Context) error {
	enabled := s.settingBool(ctx, settingIdempotencyPurgeEnabled, true)
	s.enabled = enabled
	if !enabled {
		log.Printf("idempotency scheduler: disabled (set %s=false in system_settings)", settingIdempotencyPurgeEnabled)
		return nil
	}

	expr := s.settingString(ctx, settingIdempotencyPurgeCron, defaultIdempotencyPurgeCron)
	if _, err := s.cron.AddFunc(expr, func() { s.runPurge(context.Background()) }); err != nil {
		return fmt.Errorf("register idempotency purge cron %q: %w", expr, err)
	}

	s.cron.Start()
	log.Printf("idempotency scheduler: started (purge=%q batch=%d)", expr, idempotencyPurgeBatchSize)
	return nil
}

// Stop halts the cron engine. In-flight jobs continue running until they
// return; Stop only signals "no new ticks".
func (s *IdempotencyScheduler) Stop() {
	if s.cron != nil {
		s.cron.Stop()
	}
}

// runPurge executes one retention pass.
func (s *IdempotencyScheduler) runPurge(ctx context.Context) {
	defer func() {
		if p := recover(); p != nil {
			log.Printf("idempotency scheduler: purge panic: %v", p)
		}
	}()
	deleted, err := PurgeExpiredIdempotencyKeys(ctx, s.db, idempotencyPurgeBatchSize)
	if err != nil {
		log.Printf("idempotency scheduler: purge failed: %v", err)
		return
	}
	if deleted > 0 {
		log.Printf("idempotency scheduler: purged %d expired rows", deleted)
	}
}

// --- settings helpers --------------------------------------------------

func (s *IdempotencyScheduler) settingString(ctx context.Context, key, def string) string {
	v, ok, err := s.settings.Get(ctx, key)
	if err != nil || !ok || v == "" {
		return def
	}
	return v
}

func (s *IdempotencyScheduler) settingBool(ctx context.Context, key string, def bool) bool {
	v := s.settingString(ctx, key, "")
	if v == "" {
		return def
	}
	b, err := strconv.ParseBool(v)
	if err != nil {
		return def
	}
	return b
}
