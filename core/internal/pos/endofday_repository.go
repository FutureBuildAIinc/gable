// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package pos

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/gablelbm/gable/internal/platform/httpx"
	"github.com/gablelbm/gable/pkg/collections"
	"github.com/gablelbm/gable/pkg/middleware"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// branchZoneSQL is the branch's IANA zone, UTC when it has none: the zone a
// business date of the branch is cut in.
const branchZoneSQL = `COALESCE((SELECT l.timezone FROM locations l WHERE l.id = $1), 'UTC')`

// BranchZone answers the branch's zone, and false when no branch has the id.
func (r *PostgresRepository) BranchZone(ctx context.Context, branch uuid.UUID) (string, bool, error) {
	var zone *string
	err := r.ex(ctx).QueryRow(ctx, `SELECT COALESCE(timezone, 'UTC') FROM locations WHERE id = $1 AND type = 'BRANCH'`, branch).Scan(&zone)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", false, nil
	}
	if err != nil {
		return "", false, fmt.Errorf("failed to read the branch: %w", err)
	}
	return *zone, true, nil
}

// LockBranchDay takes the branch's day lock for the transaction: a till open
// and a day close of one branch serialize on it.
func (r *PostgresRepository) LockBranchDay(ctx context.Context, branch uuid.UUID) error {
	if _, err := r.ex(ctx).Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended('till-day:' || $1::text, 0))`, branch); err != nil {
		return fmt.Errorf("failed to take the branch day lock: %w", err)
	}
	return nil
}

// BranchDayClosed reports whether the branch's business date now is closed.
func (r *PostgresRepository) BranchDayClosed(ctx context.Context, branch uuid.UUID) (bool, error) {
	var closed bool
	err := r.ex(ctx).QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM till_day_closes
		WHERE branch_id = $1 AND business_date = (NOW() AT TIME ZONE `+branchZoneSQL+`)::date)`, branch).Scan(&closed)
	if err != nil {
		return false, fmt.Errorf("failed to read the branch day: %w", err)
	}
	return closed, nil
}

// sessionBusinessDate is a session's business date: the day it opened, in the
// branch's zone.
const sessionBusinessDate = `(t.opened_at AT TIME ZONE ` + branchZoneSQL + `)::date`

// ListBranchDaySessions reads a branch's sessions that opened on a business
// date, by register then time, inside the caller's wall.
func (r *PostgresRepository) ListBranchDaySessions(ctx context.Context, branch uuid.UUID, date string) ([]TillSession, error) {
	rows, err := r.ex(ctx).Query(ctx, `SELECT `+tillCols+` FROM (`+tillInner+`) t
		WHERE t.branch_id = $1 AND `+sessionBusinessDate+` = $2::date AND `+middleware.Wall("t", 3, 4)+`
		ORDER BY t.register_id, t.opened_at, t.id`,
		branch, date, middleware.BranchIDForQuery(ctx), middleware.GrantsSubForQuery(ctx))
	if err != nil {
		return nil, fmt.Errorf("failed to list the branch day's sessions: %w", err)
	}
	defer rows.Close()
	return r.scanTillSessions(rows)
}

// ListUnsettledSessions reads the branch's sessions still open or waiting for
// sign off that opened on the business date or before.
func (r *PostgresRepository) ListUnsettledSessions(ctx context.Context, branch uuid.UUID, date string) ([]TillSession, error) {
	rows, err := r.ex(ctx).Query(ctx, `SELECT `+tillCols+` FROM (`+tillInner+`) t
		WHERE t.branch_id = $1 AND t.status IN ('OPEN', 'PENDING_SIGNOFF') AND `+sessionBusinessDate+` <= $2::date
		ORDER BY t.register_id, t.opened_at, t.id`, branch, date)
	if err != nil {
		return nil, fmt.Errorf("failed to list the branch's unsettled sessions: %w", err)
	}
	defer rows.Close()
	return r.scanTillSessions(rows)
}

func (r *PostgresRepository) scanTillSessions(rows pgx.Rows) ([]TillSession, error) {
	out := []TillSession{}
	for rows.Next() {
		s, err := r.scanTillSession(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *s)
	}
	return out, rows.Err()
}

// BranchCollectionsNet is the branch's net collections of a business date, the
// figure the Daily Till carries (pkg/collections).
func (r *PostgresRepository) BranchCollectionsNet(ctx context.Context, branch uuid.UUID, date string) (httpx.Cents, error) {
	var cents int64
	if err := r.ex(ctx).QueryRow(ctx, `SELECT `+collections.NetCentsForBranchAt("$2", 1), branch, date).Scan(&cents); err != nil {
		return 0, fmt.Errorf("failed to read the branch day's collections: %w", err)
	}
	return httpx.Cents(cents), nil
}

const dayCloseCols = `c.id, c.branch_id, c.business_date::text, c.deposit_slip_ref, c.closed_by, c.closed_at,
	c.cash_to_deposit_cents, c.checks_to_deposit_cents, c.card_total_cents, c.totals,
	c.bank_account_id, c.gl_entry_id, c.bank_transaction_id, b.amount, b.transaction_date::text, COALESCE(b.description, ''),
	c.deposit_difference_cents, c.deposit_matched_by, c.deposit_matched_at`

func scanDayClose(row pgx.Row) (*DayClose, error) {
	var c DayClose
	var closedAt time.Time
	var bankID *uuid.UUID
	var bankAmount, diff *int64
	var bankDate, bankDesc, matchedBy *string
	var matchedAt *time.Time
	if err := row.Scan(&c.ID, &c.BranchID, &c.BusinessDate, &c.DepositSlipRef, &c.ClosedBy, &closedAt,
		&c.CashToDepositCents, &c.ChecksToDepositCents, &c.CardTotalCents, &c.Totals,
		&c.BankAccountID, &c.GLEntryID, &bankID, &bankAmount, &bankDate, &bankDesc, &diff, &matchedBy, &matchedAt); err != nil {
		return nil, err
	}
	c.ClosedAt = httpx.TimestampOf(closedAt)
	if bankID != nil && bankAmount != nil && diff != nil && matchedAt != nil {
		m := &DepositMatch{BankTransactionID: *bankID, BankAmountCents: httpx.Cents(*bankAmount), DifferenceCents: httpx.Cents(*diff),
			MatchedAt: httpx.TimestampOf(*matchedAt)}
		if bankDate != nil {
			m.BankTransactionDate = *bankDate
		}
		if bankDesc != nil {
			m.BankDescription = *bankDesc
		}
		if matchedBy != nil {
			m.MatchedBy = *matchedBy
		}
		c.DepositMatch = m
	}
	return &c, nil
}

const dayCloseFrom = ` FROM till_day_closes c LEFT JOIN bank_transactions b ON b.id = c.bank_transaction_id`

// GetDayClose reads the close of a branch's business date, nil when the day is
// open, inside the caller's wall.
func (r *PostgresRepository) GetDayClose(ctx context.Context, branch uuid.UUID, date string) (*DayClose, error) {
	c, err := scanDayClose(r.ex(ctx).QueryRow(ctx, `SELECT `+dayCloseCols+dayCloseFrom+`
		WHERE c.branch_id = $1 AND c.business_date = $2::date AND `+middleware.Wall("c", 3, 4),
		branch, date, middleware.BranchIDForQuery(ctx), middleware.GrantsSubForQuery(ctx)))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("failed to read the branch day close: %w", err)
	}
	return c, nil
}

// GetDayCloseByID reads a close by id inside the caller's wall; a close the
// wall hides is a missing one. forUpdate locks the row for the match.
func (r *PostgresRepository) GetDayCloseByID(ctx context.Context, id uuid.UUID, forUpdate bool) (*DayClose, error) {
	lock := ""
	if forUpdate {
		lock = ` FOR UPDATE OF c`
	}
	c, err := scanDayClose(r.ex(ctx).QueryRow(ctx, `SELECT `+dayCloseCols+dayCloseFrom+`
		WHERE c.id = $1 AND `+middleware.Wall("c", 2, 3)+lock, id, middleware.BranchIDForQuery(ctx), middleware.GrantsSubForQuery(ctx)))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, httpx.NotFound("branch day close not found")
	}
	if err != nil {
		return nil, fmt.Errorf("failed to read the branch day close: %w", err)
	}
	return c, nil
}

// CreateDayClose writes the close; the one per branch per date index turns a
// second close of a date into the day_already_closed conflict.
func (r *PostgresRepository) CreateDayClose(ctx context.Context, c *DayClose) error {
	var closed time.Time
	err := r.ex(ctx).QueryRow(ctx, `INSERT INTO till_day_closes (id, branch_id, business_date, deposit_slip_ref, closed_by, cash_to_deposit_cents,
		checks_to_deposit_cents, card_total_cents, totals, bank_account_id, gl_entry_id)
		VALUES ($1, $2, $3::date, $4, $5, $6, $7, $8, $9, $10, $11) RETURNING closed_at`,
		c.ID, c.BranchID, c.BusinessDate, c.DepositSlipRef, c.ClosedBy, int64(c.CashToDepositCents), int64(c.ChecksToDepositCents),
		int64(c.CardTotalCents), []byte(c.Totals), c.BankAccountID, c.GLEntryID).Scan(&closed)
	if err != nil {
		var pg *pgconn.PgError
		if errors.As(err, &pg) && pg.Code == "23505" && pg.ConstraintName == "till_day_closes_one_per_day" {
			return conflict("day_already_closed", fmt.Sprintf("the branch's %s is already closed", c.BusinessDate))
		}
		return fmt.Errorf("failed to close the branch day: %w", mapWriteError(err))
	}
	c.ClosedAt = httpx.TimestampOf(closed)
	return nil
}

// MatchDayClose records the bank line a day's deposit was matched to, once: a
// day already matched, or a line another day holds, is a conflict.
func (r *PostgresRepository) MatchDayClose(ctx context.Context, id uuid.UUID, m *DepositMatch) error {
	var at time.Time
	err := r.ex(ctx).QueryRow(ctx, `UPDATE till_day_closes SET bank_transaction_id = $2, deposit_difference_cents = $3,
		deposit_matched_by = $4, deposit_matched_at = NOW()
		WHERE id = $1 AND bank_transaction_id IS NULL RETURNING deposit_matched_at`,
		id, m.BankTransactionID, int64(m.DifferenceCents), m.MatchedBy).Scan(&at)
	if errors.Is(err, pgx.ErrNoRows) {
		return conflict("deposit_already_matched", "this day's deposit is already matched to a bank statement line")
	}
	if err != nil {
		var pg *pgconn.PgError
		if errors.As(err, &pg) && pg.Code == "23505" {
			return conflict("bank_line_unavailable", "another branch day holds the bank statement line")
		}
		return fmt.Errorf("failed to match the deposit: %w", err)
	}
	m.MatchedAt = httpx.TimestampOf(at)
	return nil
}

// DayCloseBranch reads a close's own branch with no wall, for the act hold
// that must decide before the walled read hides the record (found false: the
// close does not exist).
func (r *PostgresRepository) DayCloseBranch(ctx context.Context, id uuid.UUID) (uuid.UUID, bool, error) {
	var branch uuid.UUID
	err := r.ex(ctx).QueryRow(ctx, `SELECT branch_id FROM till_day_closes WHERE id = $1`, id).Scan(&branch)
	if errors.Is(err, pgx.ErrNoRows) {
		return uuid.Nil, false, nil
	}
	if err != nil {
		return uuid.Nil, false, fmt.Errorf("failed to read the branch day close's branch: %w", err)
	}
	return branch, true, nil
}
