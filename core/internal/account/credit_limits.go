// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package account

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/gablelbm/gable/internal/platform/httpx"
	"github.com/gablelbm/gable/pkg/audit"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// The credit limit change workflow (AR-08): every change to a limit is a row
// that names what it was, what it becomes, who asked and who approved. The
// act that approves applies the new limit to the customer in the same
// transaction, so the audit and the books cannot disagree about when the
// limit moved.

// CreditLimitChange is a credit_limit_changes row on the wire.
type CreditLimitChange struct {
	ID            uuid.UUID        `json:"id"`
	CustomerID    uuid.UUID        `json:"customer_id"`
	OldLimitCents httpx.Cents      `json:"old_limit_cents"`
	NewLimitCents httpx.Cents      `json:"new_limit_cents"`
	RequestedBy   *string          `json:"requested_by"`
	ApprovedBy    *string          `json:"approved_by"`
	Status        string           `json:"status"`
	Reason        *string          `json:"reason"`
	CreatedAt     httpx.Timestamp  `json:"created_at"`
	DecidedAt     *httpx.Timestamp `json:"decided_at"`
}

const creditLimitChangeColumns = `k.id, k.customer_id, k.old_limit_cents, k.new_limit_cents, k.requested_by, k.approved_by,
	k.status, k.reason, k.created_at, k.decided_at`

func scanCreditLimitChange(row pgx.Row, k *CreditLimitChange) error {
	var status string
	var created time.Time
	var decided *time.Time
	if err := row.Scan(&k.ID, &k.CustomerID, &k.OldLimitCents, &k.NewLimitCents, &k.RequestedBy, &k.ApprovedBy,
		&status, &k.Reason, &created, &decided); err != nil {
		return err
	}
	k.Status, k.CreatedAt, k.DecidedAt = lower(status), httpx.TimestampOf(created), httpx.PtrTimestamp(decided)
	return nil
}

// CreditLimitChangeIn is the body of POST /api/v1/ar/customers/{id}/credit-limit-changes.
type CreditLimitChangeIn struct {
	NewLimitCents int64
	Reason        string
	// Approve applies the change in the same act; otherwise the row waits as
	// pending until an approved request (this route with approve true again)
	// decides it.
	Approve bool
	Actor   string
	Role    string
}

// RequestCreditLimitChange records one limit change and, when Approve is set,
// approves and applies it in the same transaction. The customer row is locked
// from before the old limit is read to after the new one lands, so two
// approvals of one account serialize instead of interleaving.
func (s *Service) RequestCreditLimitChange(ctx context.Context, customerID uuid.UUID, in CreditLimitChangeIn) (*CreditLimitChange, error) {
	if in.Role != "" && !FinanceRole(in.Role) {
		return nil, httpx.Forbidden("a credit limit change needs the admin, owner or finance role")
	}
	if in.NewLimitCents < 0 {
		return nil, invalidField("new_limit_cents", "must not be negative")
	}
	reason := strings.TrimSpace(in.Reason)
	if reason == "" {
		return nil, invalidField("reason", "is required")
	}
	if len(reason) > 500 {
		return nil, invalidField("reason", "must be at most 500 characters")
	}
	if err := s.CustomerInWall(ctx, customerID); err != nil {
		return nil, err
	}
	var out *CreditLimitChange
	err := s.inTx(ctx, func(ctx context.Context) error {
		var old int64
		err := s.ex(ctx).QueryRow(ctx, `SELECT ROUND(credit_limit * 100)::bigint FROM customers WHERE id = $1 FOR UPDATE`,
			customerID).Scan(&old)
		if errors.Is(err, pgx.ErrNoRows) {
			return httpx.NotFound("account not found")
		}
		if err != nil {
			return fmt.Errorf("failed to lock the customer: %w", err)
		}
		id := uuid.New()
		if _, err := s.ex(ctx).Exec(ctx, `
			INSERT INTO credit_limit_changes (id, customer_id, old_limit_cents, new_limit_cents, requested_by, status, reason)
			VALUES ($1, $2, $3, $4, NULLIF($5, ''), 'PENDING', $6)`,
			id, customerID, old, in.NewLimitCents, in.Actor, reason); err != nil {
			return fmt.Errorf("failed to record the credit limit change: %w", err)
		}
		if in.Approve {
			if err := s.approveCreditLimitChange(ctx, id, customerID, old, in.NewLimitCents, in.Actor); err != nil {
				return err
			}
		} else if s.auditLog != nil {
			if err := s.auditLog.Log(ctx, audit.Entry{Action: "credit_limit.requested", EntityType: "credit_limit_change", EntityID: id,
				UserID: in.Actor, Changes: map[string]interface{}{"customer_id": customerID, "old_limit_cents": old,
					"new_limit_cents": in.NewLimitCents, "reason": reason}}); err != nil {
				return fmt.Errorf("failed to write the audit row: %w", err)
			}
		}
		out = &CreditLimitChange{}
		if err := scanCreditLimitChange(s.ex(ctx).QueryRow(ctx,
			`SELECT `+creditLimitChangeColumns+` FROM credit_limit_changes k WHERE k.id = $1`, id), out); err != nil {
			return fmt.Errorf("failed to read the credit limit change: %w", err)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// approveCreditLimitChange applies a pending change: the row, the limit and
// the audit, all in the caller's transaction. The guard on the pending status
// makes a second approval a skip rather than a double write.
func (s *Service) approveCreditLimitChange(ctx context.Context, id, customerID uuid.UUID, oldLimit, newLimit int64, actor string) error {
	tag, err := s.ex(ctx).Exec(ctx, `
		UPDATE credit_limit_changes SET status = 'APPROVED', approved_by = NULLIF($2, ''), decided_at = NOW()
		WHERE id = $1 AND status = 'PENDING'`, id, actor)
	if err != nil {
		return fmt.Errorf("failed to approve the credit limit change: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return conflict("already_decided", "the credit limit change is already decided")
	}
	if _, err := s.ex(ctx).Exec(ctx, `UPDATE customers SET credit_limit = $2::bigint::numeric / 100, updated_at = NOW() WHERE id = $1`,
		customerID, newLimit); err != nil {
		return fmt.Errorf("failed to apply the credit limit: %w", err)
	}
	if s.auditLog != nil {
		if err := s.auditLog.Log(ctx, audit.Entry{Action: "credit_limit.approved", EntityType: "credit_limit_change", EntityID: id,
			UserID: actor, Changes: map[string]interface{}{"customer_id": customerID, "old_limit_cents": oldLimit,
				"new_limit_cents": newLimit}}); err != nil {
			return fmt.Errorf("failed to write the audit row: %w", err)
		}
	}
	return nil
}

// CountCreditLimitChanges is the size of a customer's limit history.
func (s *Service) CountCreditLimitChanges(ctx context.Context, customerID uuid.UUID) (int64, error) {
	var n int64
	if err := s.ex(ctx).QueryRow(ctx,
		`SELECT count(*) FROM credit_limit_changes WHERE customer_id = $1`, customerID).Scan(&n); err != nil {
		return 0, fmt.Errorf("failed to count the credit limit changes: %w", err)
	}
	return n, nil
}

// CreditLimitChangeFilter is the history list's keyset position.
type CreditLimitChangeFilter struct {
	CustomerID uuid.UUID
	AfterTime  *time.Time
	AfterID    uuid.UUID
	Limit      int
}

// ListCreditLimitChanges answers one page of a customer's limit history,
// newest first. A customer outside the caller's reach reads as missing.
func (s *Service) ListCreditLimitChanges(ctx context.Context, f CreditLimitChangeFilter) ([]CreditLimitChange, error) {
	if f.Limit <= 0 {
		f.Limit = 50
	}
	if err := s.CustomerInWall(ctx, f.CustomerID); err != nil {
		return nil, err
	}
	rows, err := s.ex(ctx).Query(ctx, `
		SELECT `+creditLimitChangeColumns+`
		FROM credit_limit_changes k
		WHERE k.customer_id = $1 AND ($2::timestamptz IS NULL OR (k.created_at, k.id) < ($2, $3::uuid))
		ORDER BY k.created_at DESC, k.id DESC
		LIMIT $4`, f.CustomerID, f.AfterTime, f.AfterID, f.Limit+1)
	if err != nil {
		return nil, fmt.Errorf("failed to list the credit limit changes: %w", err)
	}
	defer rows.Close()
	out := []CreditLimitChange{}
	for rows.Next() {
		var k CreditLimitChange
		if err := scanCreditLimitChange(rows, &k); err != nil {
			return nil, fmt.Errorf("failed to scan a credit limit change: %w", err)
		}
		out = append(out, k)
	}
	return out, rows.Err()
}
