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

// FindStepUpManager reads one roster row by staff id or staff number, the
// two ways a screen names a manager. The counter PIN hash is read here and
// never leaves the server.
func (r *PostgresRepository) FindStepUpManager(ctx context.Context, manager string) (*StepUpManager, bool, error) {
	var m StepUpManager
	err := r.ex(ctx).QueryRow(ctx, `
		SELECT id, full_name, COALESCE(staff_no, ''), email, role, active,
		       (counter_pin_hash IS NOT NULL), counter_pin_locked, COALESCE(counter_pin_hash, '')
		FROM staff
		WHERE id::text = LOWER($1) OR LOWER(COALESCE(staff_no, '')) = LOWER($1) LIMIT 1`, manager).
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
// row, one update so two wrong tries at once cannot both pass the threshold.
func (r *PostgresRepository) RecordStepUpFailure(ctx context.Context, id uuid.UUID, max int) error {
	tag, err := r.ex(ctx).Exec(ctx, `
		UPDATE staff SET counter_pin_failures = counter_pin_failures + 1,
			counter_pin_locked = (counter_pin_failures + 1 >= $2), updated_at = NOW()
		WHERE id = $1`, id, max)
	if err != nil {
		return fmt.Errorf("failed to count the wrong PIN: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return httpx.NotFound("staff not found")
	}
	return nil
}

// ResetStepUpFailures clears the count on a verified PIN.
func (r *PostgresRepository) ResetStepUpFailures(ctx context.Context, id uuid.UUID) error {
	_, err := r.ex(ctx).Exec(ctx,
		`UPDATE staff SET counter_pin_failures = 0 WHERE id = $1 AND counter_pin_failures <> 0`, id)
	if err != nil {
		return fmt.Errorf("failed to clear the PIN failures: %w", err)
	}
	return nil
}

// InsertStepUpApproval mints one approval: the token only as its SHA-256
// hash, bound to the act, the target, the manager and the expiry.
func (r *PostgresRepository) InsertStepUpApproval(ctx context.Context, act string, target uuid.UUID,
	amountCents *int64, tokenHash string, managerID uuid.UUID, managerName string, expires time.Time) error {
	_, err := r.ex(ctx).Exec(ctx, `
		INSERT INTO pos_step_up_approvals (act, target_id, amount_cents, token_hash, manager_staff_id, manager_name, expires_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7)`, act, target, amountCents, tokenHash, managerID, managerName, expires)
	if err != nil {
		return fmt.Errorf("failed to mint the approval: %w", mapWriteError(err))
	}
	return nil
}

// ConsumeStepUpApproval spends one approval: the token's hash, the act, the
// target and (for a pickup) the amount must all match, the row must be live
// and unexpired. The subquery locks the row it picks FOR UPDATE SKIP LOCKED
// and the outer WHERE tests consumed_at again, so of any number of racing
// acts exactly one spends it; the others find nothing.
func (r *PostgresRepository) ConsumeStepUpApproval(ctx context.Context, act string, target uuid.UUID,
	amountCents *int64, tokenHash string) (string, bool, error) {
	var name string
	err := r.ex(ctx).QueryRow(ctx, `
		UPDATE pos_step_up_approvals SET consumed_at = NOW()
		WHERE id = (
			SELECT id FROM pos_step_up_approvals
			WHERE token_hash = $1 AND consumed_at IS NULL
			FOR UPDATE SKIP LOCKED)
			AND consumed_at IS NULL
			AND act = $2 AND target_id = $3
			AND amount_cents IS NOT DISTINCT FROM $4::bigint
			AND expires_at > NOW()
		RETURNING manager_name`, tokenHash, act, target, amountCents).Scan(&name)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", false, nil
	}
	if err != nil {
		return "", false, fmt.Errorf("failed to spend the approval: %w", err)
	}
	return name, true, nil
}
