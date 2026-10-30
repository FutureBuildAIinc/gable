// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package pricing

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/gablelbm/gable/pkg/database"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
)

// IndexSourceRepository is the market index source reader (QE-07-b1): the
// deployment's staleness threshold, the freshness of each active index's
// values, and the history write that stores a value for one index, day and
// source once.
//
// It is its own reader, separate from ExposureRepository, because the import
// and the staleness banner need exactly three things and nothing else: the
// row migration 159 seeds, a freshness query, and a history write that can
// say it did not write because the value was already there.
type IndexSourceRepository interface {
	// StalenessDays reads the deployment's staleness threshold in days. The
	// row is seeded by the migration, so a missing row is not an error: the
	// default threshold answers.
	StalenessDays(ctx context.Context) (int, error)

	// ListFreshness returns every active index with the day of its newest
	// stored value. An index with no stored value has no day.
	ListFreshness(ctx context.Context) ([]IndexFreshness, error)

	// HistoryByAsOf finds the value stored for one index, day and source. It
	// returns nil when there is none.
	HistoryByAsOf(ctx context.Context, indexID uuid.UUID, asOf time.Time, source string) (*MarketIndexHistory, error)

	// InsertHistoryIfAbsent writes one history row and reports whether it
	// was written. A value for one index, day and source is stored once, so
	// a second write of the same value writes nothing and says so: that is
	// what makes an import of the same file twice a no-op.
	InsertHistoryIfAbsent(ctx context.Context, hist *MarketIndexHistory) (bool, error)
}

// IndexFreshness is one active index and the day of its newest value.
type IndexFreshness struct {
	IndexID      uuid.UUID  `json:"index_id"`
	IndexCode    string     `json:"index_code"`
	Name         string     `json:"name"`
	CurrentValue float64    `json:"current_value"`
	NewestDate   *time.Time `json:"newest_date"`
	DaysOld      int        `json:"days_old"`
	Stale        bool       `json:"stale"`
}

// PostgresIndexSourceRepository is the PostgreSQL reader.
type PostgresIndexSourceRepository struct {
	db *database.DB
}

// NewIndexSourceRepository returns the reader over db.
func NewIndexSourceRepository(db *database.DB) *PostgresIndexSourceRepository {
	return &PostgresIndexSourceRepository{db: db}
}

// defaultStalenessDays is the threshold the deployment starts with and the
// one a missing settings row answers with.
const defaultStalenessDays = 7

// StalenessDays reads the threshold from the market_index_source row.
func (r *PostgresIndexSourceRepository) StalenessDays(ctx context.Context) (int, error) {
	var days int
	err := r.db.GetExecutor(ctx).QueryRow(ctx,
		`SELECT staleness_days FROM market_index_source WHERE singleton`).Scan(&days)
	if err != nil {
		// A deployment whose row is missing answers the default rather than
		// failing: the banner is a notice, not a gate.
		return defaultStalenessDays, nil
	}
	if days <= 0 {
		return defaultStalenessDays, nil
	}
	return days, nil
}

// ListFreshness returns every active index with the day of its newest stored
// value. The newest day is the value's own as-of date when it has one and
// the day it was recorded otherwise, so a hand typed value counts too.
func (r *PostgresIndexSourceRepository) ListFreshness(ctx context.Context) ([]IndexFreshness, error) {
	const q = `
		SELECT mi.id, mi.index_code, mi.name, mi.current_value,
		       MAX(COALESCE(mh.as_of_date, mh.recorded_at::date)) AS newest
		FROM market_indices mi
		LEFT JOIN market_index_history mh ON mh.market_index_id = mi.id
		WHERE mi.is_active
		GROUP BY mi.id, mi.index_code, mi.name, mi.current_value
		ORDER BY mi.index_code COLLATE "C" ASC`
	rows, err := r.db.GetExecutor(ctx).Query(ctx, q)
	if err != nil {
		return nil, fmt.Errorf("list index freshness: %w", err)
	}
	defer rows.Close()
	out := []IndexFreshness{}
	for rows.Next() {
		var f IndexFreshness
		var newest pgtype.Date
		if err := rows.Scan(&f.IndexID, &f.IndexCode, &f.Name, &f.CurrentValue, &newest); err != nil {
			return nil, fmt.Errorf("scan index freshness: %w", err)
		}
		if newest.Valid {
			t := newest.Time
			f.NewestDate = &t
		}
		out = append(out, f)
	}
	return out, rows.Err()
}

// HistoryByAsOf finds the value stored for one index, day and source.
func (r *PostgresIndexSourceRepository) HistoryByAsOf(ctx context.Context, indexID uuid.UUID, asOf time.Time, source string) (*MarketIndexHistory, error) {
	const q = `
		SELECT id, market_index_id, value, recorded_at, COALESCE(recorded_by, ''), source,
		       as_of_date, COALESCE(source_url, ''), fetched_at
		FROM market_index_history
		WHERE market_index_id = $1 AND as_of_date = $2 AND source = $3`
	asOfArg := time.Date(asOf.Year(), asOf.Month(), asOf.Day(), 0, 0, 0, 0, time.UTC)
	var h MarketIndexHistory
	var asOfOut pgtype.Date
	var fetched pgtype.Timestamptz
	err := r.db.GetExecutor(ctx).QueryRow(ctx, q, indexID, asOfArg, source).Scan(
		&h.ID, &h.MarketIndexID, &h.Value, &h.RecordedAt, &h.RecordedBy, &h.Source,
		&asOfOut, &h.SourceURL, &fetched)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, nil
		}
		return nil, fmt.Errorf("read market index history by as of: %w", err)
	}
	if asOfOut.Valid {
		t := asOfOut.Time
		h.AsOfDate = &t
	}
	if fetched.Valid {
		t := fetched.Time
		h.FetchedAt = &t
	}
	return &h, nil
}
// InsertHistoryIfAbsent writes one history row unless one is already stored
// for the same index, day and source. It reports whether it wrote.
func (r *PostgresIndexSourceRepository) InsertHistoryIfAbsent(ctx context.Context, hist *MarketIndexHistory) (bool, error) {
	if hist.ID == uuid.Nil {
		hist.ID = uuid.New()
	}
	if hist.RecordedAt.IsZero() {
		hist.RecordedAt = time.Now()
	}
	if hist.Source == "" {
		hist.Source = "MANUAL"
	}
	if hist.AsOfDate == nil {
		return false, errors.New("a history row for an import must carry its as of date")
	}
	const q = `
		INSERT INTO market_index_history
			(id, market_index_id, value, recorded_at, recorded_by, source, as_of_date, source_url, fetched_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, NULLIF($8, ''), $9)
		ON CONFLICT (market_index_id, as_of_date, source) WHERE as_of_date IS NOT NULL
		DO NOTHING`
	tag, err := r.db.GetExecutor(ctx).Exec(ctx, q,
		hist.ID, hist.MarketIndexID, hist.Value, hist.RecordedAt, hist.RecordedBy, hist.Source,
		hist.AsOfDate, hist.SourceURL, hist.FetchedAt)
	if err != nil {
		return false, fmt.Errorf("insert market index history: %w", err)
	}
	return tag.RowsAffected() > 0, nil
}
