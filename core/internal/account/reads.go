// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package account

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

// wall is the branch predicate on an aliased table's branch_id; branch and
// grants are the placeholders it uses (the record rule of ADR 0007 2.3, held
// on every read).
func wall(alias string, branch, grants int) string {
	return fmt.Sprintf(`(
	    ($%[2]d::uuid IS NOT NULL AND %[1]s.branch_id = $%[2]d)
	    OR ($%[2]d::uuid IS NULL AND $%[3]d::text IS NOT NULL AND %[1]s.branch_id IN
	        (SELECT branch_id FROM user_locations WHERE user_sub = $%[3]d))
	    OR ($%[2]d::uuid IS NULL AND $%[3]d::text IS NULL)
	  )`, alias, branch, grants)
}

// LocalDate is the branch's calendar date at the instant (the business date of
// ADR 0005 section 8.1).
func (s *Service) LocalDate(ctx context.Context, branchID uuid.UUID, at time.Time) (time.Time, error) {
	var d time.Time
	err := s.ex(ctx).QueryRow(ctx, `
		SELECT ($1::timestamptz AT TIME ZONE COALESCE((SELECT timezone FROM locations WHERE id = $2), 'UTC'))::date`,
		at, branchID).Scan(&d)
	if err != nil {
		return time.Time{}, fmt.Errorf("failed to read the branch's date: %w", err)
	}
	return d, nil
}

// Now is the service clock (a test sets it).
func (s *Service) Now() time.Time { return s.now() }

// GetSummary is GET /api/v1/accounts/{id}: the balance (the subledger), the
// credit limit (null for no limit), the credit left, and the open credit memos
// plus unapplied cash as a negative figure.
func (s *Service) GetSummary(ctx context.Context, customerID uuid.UUID) (*Summary, error) {
	var (
		out   Summary
		bal   int64
		limit *int64
		un    int64
	)
	err := s.ex(ctx).QueryRow(ctx, `
		SELECT c.id,
		       COALESCE(c.currency, (SELECT value FROM system_settings WHERE key = 'currency.default'), 'USD'),
		       ROUND(c.balance_due * 100)::bigint,
		       ROUND(c.credit_limit * 100)::bigint,
		       COALESCE((SELECT SUM(ROUND(m.amount_open * 100)::bigint) FROM credit_memos m
		                 WHERE m.customer_id = c.id AND m.status IN ('OPEN', 'PARTIAL')), 0)
		       - COALESCE((SELECT SUM(ROUND(p.amount_unapplied * 100)::bigint) FROM payments p
		                   WHERE p.customer_id = c.id AND p.status = 'POSTED'), 0)
		FROM customers c WHERE c.id = $1`, customerID).Scan(&out.CustomerID, &out.Currency, &bal, &limit, &un)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, httpx.NotFound("account not found")
	}
	if err != nil {
		return nil, fmt.Errorf("failed to read the account: %w", err)
	}
	out.BalanceCents = httpx.Cents(bal)
	out.UnappliedCents = httpx.Cents(un)
	if limit != nil {
		l, a := httpx.Cents(*limit), httpx.Cents(*limit-bal)
		out.CreditLimitCents, out.AvailableCredit = &l, &a
	}
	return &out, nil
}

// TransactionFilter is the subledger list's keyset position.
type TransactionFilter struct {
	CustomerID uuid.UUID
	AfterTime  *time.Time
	AfterID    uuid.UUID
	Limit      int
}

// ListTransactions answers one page of a customer's subledger, newest first by
// (created_at, id).
func (s *Service) ListTransactions(ctx context.Context, f TransactionFilter) ([]Transaction, error) {
	rows, err := s.ex(ctx).Query(ctx, `
		SELECT t.id, t.customer_id, t.type, t.amount, t.balance_after, t.currency, t.source_kind, t.reference_id,
		       COALESCE(t.description, ''), t.created_at
		FROM customer_transactions t
		WHERE t.customer_id = $1 AND ($2::timestamptz IS NULL OR (t.created_at, t.id) < ($2, $3::uuid))
		ORDER BY t.created_at DESC, t.id DESC
		LIMIT $4`, f.CustomerID, f.AfterTime, f.AfterID, f.Limit)
	if err != nil {
		return nil, fmt.Errorf("failed to list the transactions: %w", err)
	}
	defer rows.Close()
	out := []Transaction{}
	for rows.Next() {
		var t Transaction
		var typ string
		var amount, after int64
		var created time.Time
		if err := rows.Scan(&t.ID, &t.CustomerID, &typ, &amount, &after, &t.Currency, &t.SourceKind, &t.ReferenceID, &t.Description, &created); err != nil {
			return nil, fmt.Errorf("failed to scan a transaction: %w", err)
		}
		t.Type, t.AmountCents, t.BalanceAfterCents, t.CreatedAt = TransactionType(typ), httpx.Cents(amount), httpx.Cents(after), httpx.TimestampOf(created)
		out = append(out, t)
	}
	return out, rows.Err()
}

// CountTransactions is the size of the customer's subledger.
func (s *Service) CountTransactions(ctx context.Context, customerID uuid.UUID) (int64, error) {
	var n int64
	if err := s.ex(ctx).QueryRow(ctx, `SELECT count(*) FROM customer_transactions WHERE customer_id = $1`, customerID).Scan(&n); err != nil {
		return 0, fmt.Errorf("failed to count the transactions: %w", err)
	}
	return n, nil
}

// Application is an ar_applications row on the wire.
type Application struct {
	ID                uuid.UUID        `json:"id"`
	CustomerID        uuid.UUID        `json:"customer_id"`
	Currency          string           `json:"currency"`
	Kind              ApplicationKind  `json:"kind"`
	PaymentID         *uuid.UUID       `json:"payment_id"`
	CreditMemoID      *uuid.UUID       `json:"credit_memo_id"`
	InvoiceID         uuid.UUID        `json:"invoice_id"`
	AmountCents       httpx.Cents      `json:"amount_cents"`
	Reason            *string          `json:"reason"`
	AppliedOn         string           `json:"applied_on"`
	AppliedBy         *string          `json:"applied_by"`
	ActID             uuid.UUID        `json:"act_id"`
	GLEntryID         *uuid.UUID       `json:"gl_entry_id"`
	ReversedAt        *httpx.Timestamp `json:"reversed_at"`
	ReversedBy        *string          `json:"reversed_by"`
	ReversalReason    *string          `json:"reversal_reason"`
	ReversalGLEntryID *uuid.UUID       `json:"reversal_gl_entry_id"`
	ReversedOn        *string          `json:"reversed_on"`
	CreatedAt         httpx.Timestamp  `json:"created_at"`
}

// AppFilter selects applications.
type AppFilter struct {
	PaymentID, CreditMemoID, InvoiceID *uuid.UUID
}

const applicationColumns = `a.id, a.customer_id, a.currency, a.kind, a.payment_id, a.credit_memo_id, a.invoice_id, ROUND(a.amount * 100)::bigint,
	a.reason, to_char(a.applied_on, 'YYYY-MM-DD'), a.applied_by, a.act_id, a.gl_entry_id, a.reversed_at, a.reversed_by, a.reversal_reason,
	a.reversal_gl_entry_id, to_char(a.reversed_on, 'YYYY-MM-DD'), a.created_at`

func scanApplication(row pgx.Row, a *Application) error {
	var kind string
	var amount int64
	var reversed *time.Time
	var created time.Time
	if err := row.Scan(&a.ID, &a.CustomerID, &a.Currency, &kind, &a.PaymentID, &a.CreditMemoID, &a.InvoiceID, &amount, &a.Reason, &a.AppliedOn,
		&a.AppliedBy, &a.ActID, &a.GLEntryID, &reversed, &a.ReversedBy, &a.ReversalReason, &a.ReversalGLEntryID, &a.ReversedOn, &created); err != nil {
		return err
	}
	a.Kind, a.AmountCents = ApplicationKind(kind), httpx.Cents(amount)
	a.ReversedAt, a.CreatedAt = httpx.PtrTimestamp(reversed), httpx.TimestampOf(created)
	return nil
}

// ListApplications answers the applications matching the filter, oldest first.
// It is held to the branch wall through the invoice an application settles.
func (s *Service) ListApplications(ctx context.Context, f AppFilter) ([]Application, error) {
	rows, err := s.ex(ctx).Query(ctx, `
		SELECT `+applicationColumns+`
		FROM ar_applications a JOIN invoices i ON i.id = a.invoice_id
		WHERE `+wall("i", 1, 2)+`
		  AND ($3::uuid IS NULL OR a.payment_id = $3) AND ($4::uuid IS NULL OR a.credit_memo_id = $4) AND ($5::uuid IS NULL OR a.invoice_id = $5)
		ORDER BY a.created_at, a.id`, middleware.BranchIDForQuery(ctx), middleware.GrantsSubForQuery(ctx), f.PaymentID, f.CreditMemoID, f.InvoiceID)
	if err != nil {
		return nil, fmt.Errorf("failed to list the applications: %w", err)
	}
	defer rows.Close()
	out := []Application{}
	for rows.Next() {
		var a Application
		if err := scanApplication(rows, &a); err != nil {
			return nil, fmt.Errorf("failed to scan an application: %w", err)
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

// GetApplication reads one application, held to the branch wall.
func (s *Service) GetApplication(ctx context.Context, id uuid.UUID) (*Application, error) {
	var a Application
	err := scanApplication(s.ex(ctx).QueryRow(ctx, `
		SELECT `+applicationColumns+`
		FROM ar_applications a JOIN invoices i ON i.id = a.invoice_id
		WHERE a.id = $3 AND `+wall("i", 1, 2), middleware.BranchIDForQuery(ctx), middleware.GrantsSubForQuery(ctx), id), &a)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, httpx.NotFound("application not found")
	}
	if err != nil {
		return nil, fmt.Errorf("failed to read the application: %w", err)
	}
	return &a, nil
}

// todayInDefaultBranch is the business date in the default branch's time zone,
// the default of the aging and the statement.
func (s *Service) todayInDefaultBranch(ctx context.Context) (time.Time, error) {
	var d time.Time
	err := s.ex(ctx).QueryRow(ctx, `
		SELECT (NOW() AT TIME ZONE COALESCE((SELECT l.timezone FROM locations l
		        WHERE l.id::text = (SELECT value FROM system_settings WHERE key = 'default_branch_id')), 'UTC'))::date`).Scan(&d)
	if err != nil {
		return time.Time{}, fmt.Errorf("failed to read today's date: %w", err)
	}
	return d, nil
}
