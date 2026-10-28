// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package pos

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/gablelbm/gable/internal/platform/httpx"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// The step up's store (migration 137): the roster rows the picker lists, the
// failure count and lock on the staff row, and the single use approvals
// spent by the acts. Every statement runs through the caller's executor, so
// a consumption inside an act's transaction is that transaction's own.

// FindStepUpManager reads one roster row by staff id, staff number or email.
// The counter PIN hash is read here and never leaves the server.
func (r *PostgresRepository) FindStepUpManager(ctx context.Context, manager string) (*StepUpManager, bool, error) {
	return r.findStepUpManager(ctx, manager, false)
}

// FindStepUpManagerForUpdate reads and locks one roster row FOR UPDATE inside
// the step up transaction so concurrent PIN tries against the same manager
// serialize on the row lock and cannot race past the five try lock.
func (r *PostgresRepository) FindStepUpManagerForUpdate(ctx context.Context, manager string) (*StepUpManager, bool, error) {
	return r.findStepUpManager(ctx, manager, true)
}

func (r *PostgresRepository) findStepUpManager(ctx context.Context, manager string, forUpdate bool) (*StepUpManager, bool, error) {
	q := `
		SELECT id, full_name, COALESCE(staff_no, ''), email, role, active,
		       (counter_pin_hash IS NOT NULL), counter_pin_locked, COALESCE(counter_pin_hash, '')
		FROM staff
		WHERE id::text = LOWER($1) OR LOWER(COALESCE(staff_no, '')) = LOWER($1) OR LOWER(email) = LOWER($1)
		LIMIT 1`
	if forUpdate {
		q += ` FOR UPDATE`
	}
	var m StepUpManager
	err := r.ex(ctx).QueryRow(ctx, q, manager).
		Scan(&m.ID, &m.FullName, &m.StaffNo, &m.Email, &m.Role, &m.Active, &m.HasPIN, &m.Locked, &m.Hash)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, fmt.Errorf("failed to read the manager: %w", err)
	}
	return &m, true, nil
}

// ListStepUpManagers reads the picker's list: active staff with a PIN whose
// role the act admits, by name.
func (r *PostgresRepository) ListStepUpManagers(ctx context.Context, roles []string) ([]StepUpManagerSummary, error) {
	rows, err := r.ex(ctx).Query(ctx, `
		SELECT id, full_name, COALESCE(staff_no, ''), role
		FROM staff
		WHERE active AND counter_pin_hash IS NOT NULL AND NOT counter_pin_locked
		  AND LOWER(role) = ANY($1::text[])
		ORDER BY full_name`, lowerAll(roles))
	if err != nil {
		return nil, fmt.Errorf("failed to list the managers: %w", err)
	}
	defer rows.Close()
	out := []StepUpManagerSummary{}
	for rows.Next() {
		var m StepUpManagerSummary
		if err := rows.Scan(&m.ID, &m.FullName, &m.StaffNo, &m.Role); err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

func lowerAll(in []string) []string {
	out := make([]string, len(in))
	for i, s := range in {
		out[i] = strings.ToLower(s)
	}
	return out
}

// RecordStepUpFailure counts one wrong PIN and locks the PIN at max in a
// row. One update holds both the count and the lock, and the count is capped
// at max, so any number of concurrent wrong tries together count no more
// than the lock's own five.
func (r *PostgresRepository) RecordStepUpFailure(ctx context.Context, id uuid.UUID, max int) error {
	tag, err := r.ex(ctx).Exec(ctx, `
		UPDATE staff SET counter_pin_failures = LEAST(counter_pin_failures + 1, $2),
			counter_pin_locked = (LEAST(counter_pin_failures + 1, $2) >= $2), updated_at = NOW()
		WHERE id = $1`, id, max)
	if err != nil {
		return fmt.Errorf("failed to count the wrong PIN: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return httpx.NotFound("staff not found")
	}
	return nil
}

// ResetStepUpFailuresUnlocked clears the count on a verified PIN, and
// answers whether the row was still unlocked: five wrong tries raced in
// between leave the PIN locked and no approval may be minted on it.
func (r *PostgresRepository) ResetStepUpFailuresUnlocked(ctx context.Context, id uuid.UUID) (bool, error) {
	var reset bool
	err := r.ex(ctx).QueryRow(ctx,
		`UPDATE staff SET counter_pin_failures = 0 WHERE id = $1 AND NOT counter_pin_locked RETURNING TRUE`, id).Scan(&reset)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("failed to clear the PIN failures: %w", err)
	}
	return reset, nil
}

// InsertStepUpApproval mints one approval: the token only as its SHA-256
// hash, bound to the act, its terms (an override's kind, the amount the
// manager approved), the target, the manager and the expiry.
func (r *PostgresRepository) InsertStepUpApproval(ctx context.Context, act, kind string, target uuid.UUID,
	amountCents *int64, tokenHash string, managerID uuid.UUID, managerName string, expires time.Time) error {
	_, err := r.ex(ctx).Exec(ctx, `
		INSERT INTO pos_step_up_approvals (act, kind, target_id, amount_cents, token_hash, manager_staff_id, manager_name, expires_at)
		VALUES ($1, NULLIF($2, ''), $3, $4, $5, $6, $7, $8)`, act, kind, target, amountCents, tokenHash, managerID, managerName, expires)
	if err != nil {
		return fmt.Errorf("failed to mint the approval: %w", mapWriteError(err))
	}
	return nil
}

// ConsumeStepUpApproval spends one approval: the token's hash, the act, the
// kind and the amount the manager approved, and the target must all match,
// and the row must be live and unexpired. The subquery locks the row it
// picks FOR UPDATE SKIP LOCKED and the outer WHERE tests consumed_at again,
// so of any number of racing acts exactly one spends it; the others find
// nothing. The answer carries the manager's name (the act's own columns) and
// staff id (its audit row and person rules).
func (r *PostgresRepository) ConsumeStepUpApproval(ctx context.Context, act, kind string, target uuid.UUID,
	amountCents *int64, tokenHash string) (StepUpManagerRef, bool, error) {
	var ref StepUpManagerRef
	err := r.ex(ctx).QueryRow(ctx, `
		UPDATE pos_step_up_approvals SET consumed_at = NOW()
		WHERE id = (
			SELECT id FROM pos_step_up_approvals
			WHERE token_hash = $1 AND consumed_at IS NULL
			FOR UPDATE SKIP LOCKED)
			AND consumed_at IS NULL
			AND act = $2 AND target_id = $3
			AND kind IS NOT DISTINCT FROM NULLIF($4, '')
			AND amount_cents IS NOT DISTINCT FROM $5::bigint
			AND expires_at > NOW()
		RETURNING manager_name, manager_staff_id::text`, tokenHash, act, target, kind, amountCents).Scan(&ref.Name, &ref.StaffID)
	if errors.Is(err, pgx.ErrNoRows) {
		return StepUpManagerRef{}, false, nil
	}
	if err != nil {
		return StepUpManagerRef{}, false, fmt.Errorf("failed to spend the approval: %w", err)
	}
	return ref, true, nil
}
