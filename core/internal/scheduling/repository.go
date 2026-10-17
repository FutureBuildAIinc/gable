// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package scheduling

import (
	"context"
	"fmt"
	"strconv"

	"github.com/gablelbm/gable/pkg/database"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// Repository reads and writes the window definitions against Postgres.
type Repository struct {
	db *database.DB
}

// NewRepository creates the repository.
func NewRepository(db *database.DB) *Repository {
	return &Repository{db: db}
}

const windowSelect = `
	SELECT w.id, w.yard_id, COALESCE(l.code, ''), COALESCE(l.timezone, 'UTC'),
	       w.kind, w.weekday,
	       to_char(w.start_time, 'HH24:MI'), to_char(w.end_time, 'HH24:MI'),
	       w.capacity, w.cutoff_minutes, w.is_active, w.created_at, w.updated_at
	FROM yard_schedule_windows w
	JOIN locations l ON l.id = w.yard_id`

func scanWindow(scanner interface{ Scan(dest ...any) error }) (*Window, error) {
	var w Window
	err := scanner.Scan(&w.ID, &w.YardID, &w.YardCode, &w.YardTimezone, &w.Kind, &w.Weekday,
		&w.StartTime, &w.EndTime, &w.Capacity, &w.CutoffMinutes, &w.IsActive,
		&w.CreatedAt, &w.UpdatedAt)
	if err != nil {
		return nil, err
	}
	return &w, nil
}

// List lists windows, optionally narrowed to a yard and a kind. Inactive
// windows are included: the yard manager edits what was switched off.
func (r *Repository) List(ctx context.Context, yardID *uuid.UUID, kind string) ([]Window, error) {
	q := windowSelect + ` WHERE 1=1`
	args := []any{}
	if yardID != nil {
		args = append(args, *yardID)
		q += ` AND w.yard_id = $` + strconv.Itoa(len(args))
	}
	if kind != "" {
		args = append(args, kind)
		q += ` AND w.kind = $` + strconv.Itoa(len(args))
	}
	q += ` ORDER BY l.code, w.kind, w.weekday, w.start_time`
	rows, err := r.db.GetExecutor(ctx).Query(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("failed to list windows: %w", err)
	}
	defer rows.Close()
	out := make([]Window, 0)
	for rows.Next() {
		w, err := scanWindow(rows)
		if err != nil {
			return nil, fmt.Errorf("failed to scan window: %w", err)
		}
		out = append(out, *w)
	}
	return out, rows.Err()
}

// Get reads one window.
func (r *Repository) Get(ctx context.Context, id uuid.UUID) (*Window, error) {
	w, err := scanWindow(r.db.GetExecutor(ctx).QueryRow(ctx, windowSelect+` WHERE w.id = $1`, id))
	if err != nil {
		if err == pgx.ErrNoRows {
			return nil, ErrWindowNotFound
		}
		return nil, fmt.Errorf("failed to get window: %w", err)
	}
	return w, nil
}

// YardExists reports whether the yard (a location) exists, so a create can
// refuse a bad yard with a 400 rather than a foreign key 500.
func (r *Repository) YardExists(ctx context.Context, yardID uuid.UUID) (bool, error) {
	var ok bool
	err := r.db.GetExecutor(ctx).QueryRow(ctx,
		`SELECT EXISTS(SELECT 1 FROM locations WHERE id = $1)`, yardID).Scan(&ok)
	return ok, err
}

// Create writes a new window definition.
func (r *Repository) Create(ctx context.Context, w *Window) error {
	return r.db.GetExecutor(ctx).QueryRow(ctx, `
		INSERT INTO yard_schedule_windows
			(yard_id, kind, weekday, start_time, end_time, capacity, cutoff_minutes)
		VALUES ($1, $2, $3, $4, $5, $6, $7)
		RETURNING id, created_at, updated_at
	`, w.YardID, w.Kind, w.Weekday, w.StartTime, w.EndTime, w.Capacity, w.CutoffMinutes,
	).Scan(&w.ID, &w.CreatedAt, &w.UpdatedAt)
}

// Update writes the mutable fields of one window.
func (r *Repository) Update(ctx context.Context, w *Window) error {
	return r.db.GetExecutor(ctx).QueryRow(ctx, `
		UPDATE yard_schedule_windows SET
			kind = $2, weekday = $3, start_time = $4, end_time = $5,
			capacity = $6, cutoff_minutes = $7, is_active = $8, updated_at = NOW()
		WHERE id = $1
		RETURNING updated_at
	`, w.ID, w.Kind, w.Weekday, w.StartTime, w.EndTime, w.Capacity, w.CutoffMinutes, w.IsActive,
	).Scan(&w.UpdatedAt)
}

// Deactivate switches a window off without deleting it, so its holds keep
// their reference and its history stays auditable.
func (r *Repository) Deactivate(ctx context.Context, id uuid.UUID) error {
	tag, err := r.db.GetExecutor(ctx).Exec(ctx,
		`UPDATE yard_schedule_windows SET is_active = FALSE, updated_at = NOW() WHERE id = $1`, id)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrWindowNotFound
	}
	return nil
}
