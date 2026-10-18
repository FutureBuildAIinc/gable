// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package bankrecon

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/gablelbm/gable/pkg/database"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// Errors of the deposit lines: a missing line, and a line that is not a
// deposit that is still free to claim.
var (
	ErrDepositLineNotFound = errors.New("bankrecon: no such bank statement line")
	ErrDepositLineNotFree  = errors.New("bankrecon: the bank statement line is not an unmatched deposit")
	ErrBankAccountNotFound = errors.New("bankrecon: no such bank account")
	ErrBankAccountInactive = errors.New("bankrecon: the bank account is not active")
	depositLineColumns     = `id, bank_account_id, reconciliation_id, transaction_date, amount, COALESCE(description, ''), COALESCE(reference, ''), matched_journal_entry_id, status, created_at`
)

// DepositLines is the branch day close's view of the bank statement: it reads
// the imported lines, offers the unmatched deposits near a day, and claims
// one for a day close so neither the auto match nor a second day can take it.
// Every call runs on the context's executor, inside the caller's transaction.
type DepositLines struct {
	db *database.DB
}

// NewDepositLines builds the deposit line reader.
func NewDepositLines(db *database.DB) *DepositLines { return &DepositLines{db: db} }

func scanLine(row pgx.Row) (*BankTransaction, error) {
	var txn BankTransaction
	if err := row.Scan(&txn.ID, &txn.BankAccountID, &txn.ReconciliationID, &txn.TransactionDate, &txn.Amount, &txn.Description,
		&txn.Reference, &txn.MatchedJournalEntryID, &txn.Status, &txn.CreatedAt); err != nil {
		return nil, err
	}
	return &txn, nil
}

// Line reads one statement line; ErrDepositLineNotFound when it does not exist.
func (d *DepositLines) Line(ctx context.Context, id uuid.UUID) (*BankTransaction, error) {
	txn, err := scanLine(d.db.GetExecutor(ctx).QueryRow(ctx, `SELECT `+depositLineColumns+` FROM bank_transactions WHERE id = $1`, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrDepositLineNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("failed to read the bank statement line: %w", err)
	}
	return txn, nil
}

// AccountGL answers the code of the GL account a bank account is kept in, the
// account a deposit into it debits. ErrBankAccountNotFound for an id with no
// bank account, ErrBankAccountInactive for a retired one.
func (d *DepositLines) AccountGL(ctx context.Context, id uuid.UUID) (string, error) {
	var code string
	var active bool
	err := d.db.GetExecutor(ctx).QueryRow(ctx, `SELECT a.code, b.is_active FROM bank_accounts b JOIN gl_accounts a ON a.id = b.gl_account_id
		WHERE b.id = $1`, id).Scan(&code, &active)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", ErrBankAccountNotFound
	}
	if err != nil {
		return "", fmt.Errorf("failed to read the bank account: %w", err)
	}
	if !active {
		return "", ErrBankAccountInactive
	}
	return code, nil
}

// Claim marks an unmatched deposit line MATCHED so the auto match and a
// second claimant leave it alone, links it to the journal entry that posted
// the deposit (nil: no entry to link), and reports the line it claimed. A line that
// is not a deposit (amount above zero), is already matched or excluded, or
// does not exist answers ErrDepositLineNotFree or ErrDepositLineNotFound.
func (d *DepositLines) Claim(ctx context.Context, id uuid.UUID, entry *uuid.UUID) (*BankTransaction, error) {
	txn, err := scanLine(d.db.GetExecutor(ctx).QueryRow(ctx, `UPDATE bank_transactions SET status = $2, matched_journal_entry_id = $4
		WHERE id = $1 AND status = $3 AND amount > 0
		RETURNING `+depositLineColumns, id, string(TransactionStatusMatched), string(TransactionStatusUnmatched), entry))
	if errors.Is(err, pgx.ErrNoRows) {
		if _, lerr := d.Line(ctx, id); lerr != nil {
			return nil, lerr
		}
		return nil, ErrDepositLineNotFree
	}
	if err != nil {
		return nil, fmt.Errorf("failed to claim the bank statement line: %w", err)
	}
	return txn, nil
}

// Candidates lists the unmatched deposit lines dated from..to inclusive,
// nearest to the amount first (then the earliest), at most limit.
func (d *DepositLines) Candidates(ctx context.Context, from, to time.Time, amount int64, limit int) ([]BankTransaction, error) {
	rows, err := d.db.GetExecutor(ctx).Query(ctx, `SELECT `+depositLineColumns+` FROM bank_transactions
		WHERE status = $1 AND amount > 0 AND transaction_date BETWEEN $2::date AND $3::date
		ORDER BY abs(amount - $4::bigint), transaction_date, id LIMIT $5`,
		string(TransactionStatusUnmatched), from, to, amount, limit)
	if err != nil {
		return nil, fmt.Errorf("failed to list the deposit lines: %w", err)
	}
	defer rows.Close()
	out := []BankTransaction{}
	for rows.Next() {
		txn, err := scanLine(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *txn)
	}
	return out, rows.Err()
}
