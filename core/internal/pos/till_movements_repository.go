// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package pos

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/gablelbm/gable/internal/platform/httpx"
	"github.com/gablelbm/gable/pkg/middleware"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

const movementCols = `id, till_session_id, register_id, branch_id, kind, amount_cents, reason_code, note, actor, taken_by,
	gl_entry_id, created_at`

func scanMovement(row pgx.Row) (*TillMovement, error) {
	var m TillMovement
	var kind string
	var created time.Time
	if err := row.Scan(&m.ID, &m.TillSessionID, &m.RegisterID, &m.BranchID, &kind, &m.AmountCents, &m.ReasonCode, &m.Note,
		&m.Actor, &m.TakenBy, &m.GLEntryID, &created); err != nil {
		return nil, err
	}
	m.Kind = MovementKind(kind)
	m.CreatedAt = httpx.TimestampOf(created)
	return &m, nil
}

// BranchToday is the branch's business date now, in the branch's own zone
// (UTC when it has none), as the date a journal entry or a day close carries.
func (r *PostgresRepository) BranchToday(ctx context.Context, branch uuid.UUID) (time.Time, error) {
	var day time.Time
	if err := r.ex(ctx).QueryRow(ctx, `SELECT (NOW() AT TIME ZONE COALESCE(
		(SELECT l.timezone FROM locations l WHERE l.id = $1), 'UTC'))::date`, branch).Scan(&day); err != nil {
		return time.Time{}, fmt.Errorf("failed to read the branch's business date: %w", err)
	}
	return day, nil
}

// CreateTillMovement writes a movement row inside the act's transaction.
func (r *PostgresRepository) CreateTillMovement(ctx context.Context, m *TillMovement) error {
	var created time.Time
	err := r.ex(ctx).QueryRow(ctx, `
		INSERT INTO till_movements (id, till_session_id, register_id, branch_id, kind, amount_cents, reason_code, note, actor,
			taken_by, gl_entry_id)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11)
		RETURNING created_at`,
		m.ID, m.TillSessionID, m.RegisterID, m.BranchID, string(m.Kind), int64(m.AmountCents), m.ReasonCode, m.Note, m.Actor,
		m.TakenBy, m.GLEntryID).Scan(&created)
	if err != nil {
		return fmt.Errorf("failed to record the movement: %w", mapWriteError(err))
	}
	m.CreatedAt = httpx.TimestampOf(created)
	return nil
}

// RecordPickupApproval writes the signed-in taker's own confirmation of a
// pickup: an unconsumed row the cashier's later record consumes. Nothing
// moves and nothing posts; the cash is still in the drawer.
func (r *PostgresRepository) RecordPickupApproval(ctx context.Context, sessionID uuid.UUID, amountCents int64, takenBy string) error {
	if _, err := r.ex(ctx).Exec(ctx, `
		INSERT INTO till_pickup_approvals (till_session_id, amount_cents, taken_by) VALUES ($1, $2, $3)`,
		sessionID, amountCents, takenBy); err != nil {
		return fmt.Errorf("failed to record the pickup approval: %w", mapWriteError(err))
	}
	return nil
}

// ConsumePickupApproval takes the unconsumed confirmation of this session,
// amount and taker, and answers the taker's authenticated subject. Nothing
// waits when no such row exists (found false): the cashier cannot record the
// pickup alone. The subquery locks the row it picks FOR UPDATE SKIP LOCKED
// and the outer WHERE tests consumed_at again, so concurrent records of one
// confirmation cannot all consume it: under READ COMMITTED a contender either
// skips the locked row or finds it already consumed.
func (r *PostgresRepository) ConsumePickupApproval(ctx context.Context, sessionID uuid.UUID, amountCents int64, taker string) (string, bool, error) {
	var approved string
	err := r.ex(ctx).QueryRow(ctx, `
		UPDATE till_pickup_approvals SET consumed_at = NOW()
		WHERE id = (
			SELECT id FROM till_pickup_approvals
			WHERE till_session_id = $1 AND amount_cents = $2 AND LOWER(taken_by) = LOWER($3) AND consumed_at IS NULL
			ORDER BY created_at
			LIMIT 1
			FOR UPDATE SKIP LOCKED)
			AND consumed_at IS NULL
		RETURNING taken_by`, sessionID, amountCents, taker).Scan(&approved)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", false, nil
	}
	if err != nil {
		return "", false, fmt.Errorf("failed to consume the pickup approval: %w", err)
	}
	return approved, true, nil
}

// ListTillMovements reads every movement of a session, oldest first, through
// the caller's wall (the report embeds them).
func (r *PostgresRepository) ListTillMovements(ctx context.Context, sessionID uuid.UUID) ([]TillMovement, error) {
	rows, err := r.ex(ctx).Query(ctx, `SELECT `+movementCols+` FROM till_movements
		WHERE till_session_id = $1 AND `+middleware.Wall("", 2, 3)+`
		ORDER BY created_at, id`, sessionID, middleware.BranchIDForQuery(ctx), middleware.GrantsSubForQuery(ctx))
	if err != nil {
		return nil, fmt.Errorf("failed to list the session's movements: %w", err)
	}
	defer rows.Close()
	out := []TillMovement{}
	for rows.Next() {
		m, err := scanMovement(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *m)
	}
	return out, rows.Err()
}

// ListTillMovementsPage is the movements list's keyset page, oldest first:
// created_at, id ascending after the cursor's row. more is true when a row
// follows the page.
func (r *PostgresRepository) ListTillMovementsPage(ctx context.Context, sessionID uuid.UUID, after *time.Time, afterID *uuid.UUID, limit int) ([]TillMovement, bool, error) {
	rows, err := r.ex(ctx).Query(ctx, `SELECT `+movementCols+` FROM till_movements
		WHERE till_session_id = $1 AND `+middleware.Wall("", 2, 3)+`
		  AND ($4::timestamptz IS NULL OR (created_at, id) > ($4::timestamptz, $5::uuid))
		ORDER BY created_at, id LIMIT $6`,
		sessionID, middleware.BranchIDForQuery(ctx), middleware.GrantsSubForQuery(ctx), after, afterID, limit+1)
	if err != nil {
		return nil, false, fmt.Errorf("failed to list the session's movements: %w", err)
	}
	defer rows.Close()
	out := []TillMovement{}
	for rows.Next() {
		m, err := scanMovement(rows)
		if err != nil {
			return nil, false, err
		}
		out = append(out, *m)
	}
	if err := rows.Err(); err != nil {
		return nil, false, err
	}
	more := len(out) > limit
	if more {
		out = out[:limit]
	}
	return out, more, nil
}

// TillMovementTotals sums a session's movements by kind, in cents, inside the
// caller's transaction (the close counts them under the session lock).
func (r *PostgresRepository) TillMovementTotals(ctx context.Context, sessionID uuid.UUID) (map[string]int64, error) {
	rows, err := r.ex(ctx).Query(ctx, `SELECT kind, COALESCE(SUM(amount_cents), 0)::bigint FROM till_movements
		WHERE till_session_id = $1 GROUP BY kind`, sessionID)
	if err != nil {
		return nil, fmt.Errorf("failed to total the session's movements: %w", err)
	}
	defer rows.Close()
	out := map[string]int64{}
	for rows.Next() {
		var kind string
		var cents int64
		if err := rows.Scan(&kind, &cents); err != nil {
			return nil, err
		}
		out[kind] = cents
	}
	return out, rows.Err()
}
