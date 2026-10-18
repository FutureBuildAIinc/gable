// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package gl

// The branch dimension of the ledger (migration 125): statements cut by one
// branch or consolidated over every branch, the GL drill down from a
// statement row, the subledger tie outs, the year end close that zeroes the
// revenue and expense accounts into Retained Earnings per branch, and the
// branch's business day its period resolves on.

import (
	"context"
	"fmt"
	"time"

	"github.com/gablelbm/gable/pkg/collections"
	"github.com/gablelbm/gable/pkg/middleware"
	"github.com/gablelbm/gable/pkg/money"
	"github.com/google/uuid"
)

// AccountCodeRetainedEarnings is the equity account the seeded chart holds
// for cumulative net income; the year end close zeroes into it, per branch.
const AccountCodeRetainedEarnings = "3020"

// GLDetailRow is one posted ledger line behind a statement row: the account,
// its entry, the branch and the entry's business date.
type GLDetailRow struct {
	EntryID     uuid.UUID `json:"entry_id"`
	EntryNumber int       `json:"entry_number"`
	EntryDate   time.Time `json:"entry_date"`
	Memo        string    `json:"memo"`
	Source      string    `json:"source"`
	LineID      uuid.UUID `json:"line_id"`
	AccountID   uuid.UUID `json:"account_id"`
	AccountCode string    `json:"account_code"`
	AccountName string    `json:"account_name"`
	Description string    `json:"description"`
	BranchID    uuid.UUID `json:"branch_id"`
	DebitCents  int64     `json:"debit_cents"`
	CreditCents int64     `json:"credit_cents"`
}

// TieOutLeg is one control account against its subledger, the difference
// answered in cents (zero is a clean tie).
type TieOutLeg struct {
	ControlCents    int64 `json:"control_cents"`
	SubledgerCents  int64 `json:"subledger_cents"`
	DifferenceCents int64 `json:"difference_cents"`
}

// TieOutReport is the subledger tie out of one day: the AR control account
// (1020) against the customers' balances, the AP control account (2010)
// against the open vendor bills, each per branch or consolidated.
type TieOutReport struct {
	AsOf     string     `json:"as_of"`
	Currency string     `json:"currency"`
	BranchID *uuid.UUID `json:"branch_id,omitempty"`
	AR       TieOutLeg  `json:"ar"`
	AP       TieOutLeg  `json:"ap"`
}

// ProfitAndLossComparison carries the previous period's totals beside the
// current ones, each answered in cents.
type ProfitAndLossComparison struct {
	StartDate     string `json:"start_date"`
	EndDate       string `json:"end_date"`
	TotalRevenue  int64  `json:"total_revenue"`
	TotalCOGS     int64  `json:"total_cogs"`
	GrossProfit   int64  `json:"gross_profit"`
	TotalExpenses int64  `json:"total_expenses"`
	NetIncome     int64  `json:"net_income"`
}

// branchFilter is the branch arm every statement read carries: the statement's
// own branch (nil reads consolidated) beside the caller's reach, the same
// predicate shape the reporting surface uses.
type branchFilter struct {
	Branch *uuid.UUID
	Grants *string
}

func (f branchFilter) args() []any { return []any{f.Branch, f.Grants} }

// --- Business day (MY-02) ---

// BranchBusinessDay answers the branch's own today, in its time zone; the
// default branch's zone answers for a consolidated read (the same definition
// the collections reads use, no second helper).
func (r *PostgresRepository) BranchBusinessDay(ctx context.Context, branch *uuid.UUID) (time.Time, error) {
	zone := collections.DefaultBranchZoneSQL
	args := []any{}
	if branch != nil {
		zone = `COALESCE((SELECT l.timezone FROM locations l WHERE l.id = $1), 'UTC')`
		args = append(args, *branch)
	}
	var day time.Time
	q := `SELECT (NOW() AT TIME ZONE ` + zone + `)::date`
	if err := r.db.GetExecutor(ctx).QueryRow(ctx, q, args...).Scan(&day); err != nil {
		return time.Time{}, fmt.Errorf("failed to read the branch business day: %w", err)
	}
	return day, nil
}

// --- Trial balance and statements by branch ---

// GetTrialBalanceFor sums the posted lines per account, currency and branch,
// at the caller's reach, for one branch or consolidated.
func (r *PostgresRepository) GetTrialBalanceFor(ctx context.Context, asOfDate time.Time, currency string, f branchFilter) ([]TrialBalanceRow, error) {
	query := `
		SELECT a.id, a.code, a.name, a.type, e.currency, l.branch_id,
		       COALESCE(SUM(l.debit) * 100, 0)::bigint AS total_debit,
		       COALESCE(SUM(l.credit) * 100, 0)::bigint AS total_credit
		FROM gl_accounts a
		JOIN gl_journal_lines l ON l.account_id = a.id
		JOIN gl_journal_entries e ON e.id = l.journal_entry_id AND e.status = 'POSTED' AND e.entry_date <= $1
		WHERE a.is_active = TRUE AND ($2 = '' OR e.currency = $2)
		  AND ` + middleware.Wall("l", 3, 4) + `
		GROUP BY a.id, a.code, a.name, a.type, e.currency, l.branch_id
		HAVING SUM(l.debit) > 0 OR SUM(l.credit) > 0
		ORDER BY e.currency, a.code, (SELECT b.code FROM locations b WHERE b.id = l.branch_id)
	`
	rows, err := r.db.GetExecutor(ctx).Query(ctx, query, asOfDate, currency, f.Branch, f.Grants)
	if err != nil {
		return nil, fmt.Errorf("failed to get trial balance: %w", err)
	}
	defer rows.Close()

	var result []TrialBalanceRow
	for rows.Next() {
		var row TrialBalanceRow
		if err := rows.Scan(&row.AccountID, &row.AccountCode, &row.AccountName, &row.AccountType, &row.Currency, &row.BranchID,
			&row.Debit, &row.Credit); err != nil {
			return nil, fmt.Errorf("failed to scan trial balance row: %w", err)
		}
		result = append(result, row)
	}
	return result, rows.Err()
}

// GetAccountActivityFor is GetAccountActivity with the branch arm: one
// branch's lines or every branch's summed, at the caller's reach.
func (r *PostgresRepository) GetAccountActivityFor(ctx context.Context, start *time.Time, end time.Time, accountTypes []string, currency string, f branchFilter) ([]AccountActivity, error) {
	query := `
		SELECT a.id, a.code, a.name, a.type, COALESCE(a.subtype, ''),
		       COALESCE(ROUND(SUM(l.debit) * 100), 0)::bigint  AS debit_cents,
		       COALESCE(ROUND(SUM(l.credit) * 100), 0)::bigint AS credit_cents
		FROM gl_accounts a
		JOIN gl_journal_lines l ON l.account_id = a.id
		JOIN gl_journal_entries e ON e.id = l.journal_entry_id
		WHERE e.status = 'POSTED'
		  AND e.entry_date <= $1
		  AND ($2::date IS NULL OR e.entry_date >= $2::date)
		  AND a.type = ANY($3)
		  AND ($2::date IS NULL OR e.source <> 'CLOSING')
		  AND e.currency = $4
		  AND ` + middleware.Wall("l", 5, 6) + `
		GROUP BY a.id, a.code, a.name, a.type, a.subtype
		ORDER BY a.code
	`
	rows, err := r.db.GetExecutor(ctx).Query(ctx, query, end, start, accountTypes, currency, f.Branch, f.Grants)
	if err != nil {
		return nil, fmt.Errorf("failed to get account activity: %w", err)
	}
	defer rows.Close()

	var result []AccountActivity
	for rows.Next() {
		var row AccountActivity
		if err := rows.Scan(&row.AccountID, &row.AccountCode, &row.AccountName, &row.AccountType, &row.AccountSubtype, &row.Debit, &row.Credit); err != nil {
			return nil, fmt.Errorf("failed to scan account activity row: %w", err)
		}
		result = append(result, row)
	}
	return result, rows.Err()
}

// --- Drill down ---

// GetGLDetail answers the posted lines of one account over a date window,
// one branch's or every branch's, at the caller's reach, oldest first.
func (r *PostgresRepository) GetGLDetail(ctx context.Context, accountID uuid.UUID, from, to time.Time, f branchFilter) ([]GLDetailRow, error) {
	query := `
		SELECT e.id, e.entry_number, e.entry_date, e.memo, e.source,
		       l.id, a.id, a.code, a.name, COALESCE(l.description, ''), l.branch_id,
		       ROUND(l.debit * 100)::bigint, ROUND(l.credit * 100)::bigint
		FROM gl_journal_entries e
		JOIN gl_journal_lines l ON l.journal_entry_id = e.id
		JOIN gl_accounts a ON a.id = l.account_id
		WHERE e.status = 'POSTED'
		  AND l.account_id = $1
		  AND e.entry_date >= $2::date AND e.entry_date <= $3::date
		  AND ` + middleware.Wall("l", 4, 5) + `
		ORDER BY e.entry_date, e.entry_number, l.id
	`
	rows, err := r.db.GetExecutor(ctx).Query(ctx, query, accountID, from, to, f.Branch, f.Grants)
	if err != nil {
		return nil, fmt.Errorf("failed to get gl detail: %w", err)
	}
	defer rows.Close()

	var out []GLDetailRow
	for rows.Next() {
		var d GLDetailRow
		if err := rows.Scan(&d.EntryID, &d.EntryNumber, &d.EntryDate, &d.Memo, &d.Source,
			&d.LineID, &d.AccountID, &d.AccountCode, &d.AccountName, &d.Description, &d.BranchID,
			&d.DebitCents, &d.CreditCents); err != nil {
			return nil, fmt.Errorf("failed to scan gl detail row: %w", err)
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

// --- Year end close ---

// YearActivityByBranch answers the year's posted revenue and expense activity
// per account, grouped by branch: the input of the year end close, which
// zeroes each branch's own accounts into its own Retained Earnings.
func (r *PostgresRepository) YearActivityByBranch(ctx context.Context, year int, currency string) (map[uuid.UUID][]AccountActivity, error) {
	// One year end close of a year and currency at a time: a second closer
	// waits here and then reads the first one's closing entries, so it finds
	// nothing left to close. Held until the closing transaction ends.
	if _, err := r.db.GetExecutor(ctx).Exec(ctx,
		`SELECT pg_advisory_xact_lock(hashtextextended('gl-year-close:' || $1::int::text || ':' || $2::text, 0))`, year, currency); err != nil {
		return nil, fmt.Errorf("failed to lock the year end close: %w", err)
	}
	query := `
		SELECT l.branch_id, a.id, a.code, a.name, a.type, COALESCE(a.subtype, ''),
		       COALESCE(ROUND(SUM(l.debit) * 100), 0)::bigint  AS debit_cents,
		       COALESCE(ROUND(SUM(l.credit) * 100), 0)::bigint AS credit_cents
		FROM gl_accounts a
		JOIN gl_journal_lines l ON l.account_id = a.id
		JOIN gl_journal_entries e ON e.id = l.journal_entry_id
		WHERE e.status = 'POSTED'
		  AND e.entry_date >= make_date($1, 1, 1) AND e.entry_date <= make_date($1, 12, 31)
		  AND a.type IN ('REVENUE', 'EXPENSE')
		  AND e.currency = $2
		GROUP BY l.branch_id, a.id, a.code, a.name, a.type, a.subtype
		ORDER BY a.code
	`
	rows, err := r.db.GetExecutor(ctx).Query(ctx, query, year, currency)
	if err != nil {
		return nil, fmt.Errorf("failed to get year activity: %w", err)
	}
	defer rows.Close()

	out := make(map[uuid.UUID][]AccountActivity)
	for rows.Next() {
		var branch uuid.UUID
		var row AccountActivity
		if err := rows.Scan(&branch, &row.AccountID, &row.AccountCode, &row.AccountName, &row.AccountType, &row.AccountSubtype, &row.Debit, &row.Credit); err != nil {
			return nil, fmt.Errorf("failed to scan year activity row: %w", err)
		}
		out[branch] = append(out[branch], row)
	}
	return out, rows.Err()
}

// --- Tie out ---

// controlBalance answers an account's posted balance in cents at a date, in a
// currency, one branch's or every branch's, at the caller's reach. The
// direction is the account's normal one (the control accounts are 1020 debit
// normal and 2010 credit normal).
func (r *PostgresRepository) ControlBalance(ctx context.Context, code string, asOf time.Time, currency string, f branchFilter) (int64, error) {
	query := `
		SELECT COALESCE(ROUND((SUM(l.debit) - SUM(l.credit)) * 100), 0)::bigint
		FROM gl_accounts a
		JOIN gl_journal_lines l ON l.account_id = a.id
		JOIN gl_journal_entries e ON e.id = l.journal_entry_id
		WHERE a.code = $1 AND e.status = 'POSTED' AND e.entry_date <= $2::date AND e.currency = $3
		  AND ` + middleware.Wall("l", 4, 5) + `
	`
	var cents int64
	err := r.db.GetExecutor(ctx).QueryRow(ctx, query, code, asOf, currency, f.Branch, f.Grants).Scan(&cents)
	return cents, err
}

// ARSubledgerCents answers the customer subledger's sum in cents (the rows
// customers.balance_due is moved by), in one currency. Each row's branch is
// resolved from its referenced document (matching the branch 1020 AR journal
// lines carry), falling back to the customer's primary branch only for
// unreferenced adjustments, and live cross-branch credit-memo applications
// shift open receivable between the memo's branch and the invoice's branch.
func (r *PostgresRepository) ARSubledgerCents(ctx context.Context, f branchFilter, currency string) (int64, error) {
	query := `
		SELECT COALESCE(SUM(sub.amount_cents), 0)::bigint
		FROM (
			SELECT t.amount AS amount_cents,
			       t.currency AS currency,
			       COALESCE(inv.branch_id, cm.branch_id, app_inv.branch_id, pr_cm.branch_id, pr_p.branch_id, ret.branch_id, ret_tx.branch_id, c.primary_branch_id) AS branch_id
			FROM customer_transactions t
			JOIN customers c ON c.id = t.customer_id
			LEFT JOIN invoices inv ON inv.id = t.reference_id
			LEFT JOIN credit_memos cm ON cm.id = t.reference_id
			LEFT JOIN ar_applications app ON app.id = t.reference_id
			LEFT JOIN invoices app_inv ON app_inv.id = app.invoice_id
			LEFT JOIN payment_refunds pr ON pr.id = t.reference_id
			LEFT JOIN credit_memos pr_cm ON pr_cm.id = pr.credit_memo_id
			LEFT JOIN payments pr_p ON pr_p.id = pr.payment_id
			LEFT JOIN pos_returns ret ON ret.id = t.reference_id
			LEFT JOIN pos_transactions ret_tx ON ret_tx.id = ret.original_transaction_id
			UNION ALL
			SELECT ROUND(a.amount * 100)::bigint AS amount_cents,
			       a.currency AS currency,
			       cm.branch_id AS branch_id
			FROM ar_applications a
			JOIN credit_memos cm ON cm.id = a.credit_memo_id
			JOIN invoices inv ON inv.id = a.invoice_id
			WHERE a.kind = 'CREDIT_MEMO' AND a.reversed_at IS NULL AND cm.branch_id <> inv.branch_id
			UNION ALL
			SELECT -ROUND(a.amount * 100)::bigint AS amount_cents,
			       a.currency AS currency,
			       inv.branch_id AS branch_id
			FROM ar_applications a
			JOIN credit_memos cm ON cm.id = a.credit_memo_id
			JOIN invoices inv ON inv.id = a.invoice_id
			WHERE a.kind = 'CREDIT_MEMO' AND a.reversed_at IS NULL AND cm.branch_id <> inv.branch_id
		) sub
		WHERE sub.currency = $1
		  AND ` + middleware.Wall("sub", 2, 3) + `
	`
	var cents int64
	err := r.db.GetExecutor(ctx).QueryRow(ctx, query, currency, f.Branch, f.Grants).Scan(&cents)
	return cents, err
}

// apSubledger answers the open vendor bills' amount_open sum in cents, one
// branch's bills or every branch's, at the caller's reach.
func (r *PostgresRepository) APSubledgerCents(ctx context.Context, f branchFilter, currency string) (int64, error) {
	query := `
		SELECT COALESCE(ROUND(SUM(vi.amount_open * 100)), 0)::bigint
		FROM vendor_invoices vi
		WHERE vi.status IN ('APPROVED', 'PARTIAL', 'PAID')
		  AND vi.currency = $1
		  AND ` + middleware.Wall("vi", 2, 3) + `
	`
	var cents int64
	err := r.db.GetExecutor(ctx).QueryRow(ctx, query, currency, f.Branch, f.Grants).Scan(&cents)
	return cents, err
}

// --- Cursor pages of the lists (ADR 0001) ---

// ListAccountsPage answers one page of the chart of accounts, oldest code
// first, keyed on the code; afterCode resumes the page after it. Each
// balance is the caller's reach's.
func (r *PostgresRepository) ListAccountsPage(ctx context.Context, limit int, afterCode string) ([]GLAccount, error) {
	query := `
		SELECT a.id, a.code, a.name, a.type, COALESCE(a.subtype, ''), a.parent_id,
		       a.normal_balance, a.is_active, COALESCE(a.description, ''),
		       COALESCE(
		           (SELECT SUM(l.debit) - SUM(l.credit)
		            FROM gl_journal_lines l
		            JOIN gl_journal_entries e ON e.id = l.journal_entry_id
		            WHERE l.account_id = a.id AND e.status = 'POSTED'
		              AND ` + middleware.Wall("l", 3, 4) + `), 0
		       ) AS balance,
		       a.created_at, a.updated_at, a.revision
		FROM gl_accounts a
		WHERE ($1 = '' OR a.code > $1)
		ORDER BY a.code
		LIMIT $2
	`
	// The balance is the caller's reach's: a caller held to one branch sees
	// that branch's lines, not the company's.
	f := filterFor(ctx, nil)
	rows, err := r.db.GetExecutor(ctx).Query(ctx, query, afterCode, limit, f.Branch, f.Grants)
	if err != nil {
		return nil, fmt.Errorf("failed to list accounts: %w", err)
	}
	defer rows.Close()

	var accounts []GLAccount
	for rows.Next() {
		var a GLAccount
		var balanceFloat float64
		if err := rows.Scan(
			&a.ID, &a.Code, &a.Name, &a.Type, &a.Subtype, &a.ParentID,
			&a.NormalBalance, &a.IsActive, &a.Description,
			&balanceFloat,
			&a.CreatedAt, &a.UpdatedAt, &a.Revision,
		); err != nil {
			return nil, fmt.Errorf("failed to scan account: %w", err)
		}
		a.Balance = money.DollarsToCents(balanceFloat)
		accounts = append(accounts, a)
	}
	return accounts, rows.Err()
}

// ListJournalEntriesPage answers one page of the journal entries, newest
// entry number first, keyed on the entry number; beforeNumber resumes the
// page after it (the numbers below it), at the caller's reach.
func (r *PostgresRepository) ListJournalEntriesPage(ctx context.Context, limit int, beforeNumber int, f branchFilter) ([]JournalEntry, error) {
	query := `
		SELECT e.id, e.entry_number, e.entry_date, e.memo, e.source, e.source_ref_id, e.branch_id,
		       e.status, COALESCE(e.posted_by, ''),
		       COALESCE((SELECT SUM(l.debit) FROM gl_journal_lines l WHERE l.journal_entry_id = e.id), 0) AS total_debit,
		       COALESCE((SELECT SUM(l.credit) FROM gl_journal_lines l WHERE l.journal_entry_id = e.id), 0) AS total_credit,
		       e.created_at, e.updated_at
		FROM gl_journal_entries e
		WHERE ($1 = 0 OR e.entry_number < $1)
		  AND ` + middleware.Wall("e", 3, 4) + `
		ORDER BY e.entry_number DESC
		LIMIT $2
	`
	rows, err := r.db.GetExecutor(ctx).Query(ctx, query, beforeNumber, limit, f.Branch, f.Grants)
	if err != nil {
		return nil, fmt.Errorf("failed to list journal entries: %w", err)
	}
	defer rows.Close()

	var entries []JournalEntry
	for rows.Next() {
		var e JournalEntry
		var totalDebitFloat, totalCreditFloat float64
		if err := rows.Scan(
			&e.ID, &e.EntryNumber, &e.EntryDate, &e.Memo, &e.Source, &e.SourceRefID, &e.BranchID,
			&e.Status, &e.PostedBy,
			&totalDebitFloat, &totalCreditFloat,
			&e.CreatedAt, &e.UpdatedAt,
		); err != nil {
			return nil, fmt.Errorf("failed to scan journal entry: %w", err)
		}
		e.TotalDebit = money.DollarsToCents(totalDebitFloat)
		e.TotalCredit = money.DollarsToCents(totalCreditFloat)
		entries = append(entries, e)
	}
	return entries, rows.Err()
}
