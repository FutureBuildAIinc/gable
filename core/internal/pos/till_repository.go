// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package pos

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/gablelbm/gable/internal/platform/httpx"
	"github.com/gablelbm/gable/pkg/branchctx"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

func (r *PostgresRepository) CreateTillSession(ctx context.Context, s *TillSession) error {
	if s.ID == uuid.Nil {
		s.ID = uuid.New()
	}
	var opened time.Time
	err := r.ex(ctx).QueryRow(ctx, `
		INSERT INTO till_sessions (id, register_id, branch_id, cashier_id, status, opening_float, opened_at)
		VALUES ($1, $2, $3, $4, $5, $6::numeric / 100, NOW())
		RETURNING opened_at`,
		s.ID, s.RegisterID, s.BranchID, s.CashierID, string(s.Status), int64(s.OpeningFloat)).Scan(&opened)
	if err != nil {
		return fmt.Errorf("failed to open the till session: %w", mapWriteError(err))
	}
	s.OpenedAt = httpx.TimestampOf(opened)
	return nil
}

func (r *PostgresRepository) scanTillSession(row pgx.Row) (*TillSession, error) {
	var s TillSession
	var status string
	var openedAt, closedAt *time.Time
	err := row.Scan(&s.ID, &s.RegisterID, &s.BranchID, &s.CashierID, &status, &s.OpeningFloat, &openedAt, &closedAt,
		&s.ExpectedByMethod, &s.CountedByMethod, &s.OverShort, &s.GLEntryID, &s.Notes)
	if err != nil {
		return nil, err
	}
	if openedAt != nil {
		s.OpenedAt = httpx.TimestampOf(*openedAt)
	}
	s.ClosedAt = httpx.PtrTimestamp(closedAt)
	s.Status = TillSessionStatus(status)
	if s.ExpectedByMethod == nil {
		s.ExpectedByMethod = map[string]int64{}
	}
	if s.CountedByMethod == nil {
		s.CountedByMethod = map[string]int64{}
	}
	return &s, nil
}

const tillCols = `id, register_id, branch_id, cashier_id, status, ROUND(opening_float * 100)::bigint, opened_at, closed_at,
	expected_by_method, counted_by_method, over_short_cents, gl_entry_id, notes`

// LockTillSession locks a session row inside the caller's transaction: the
// completion and the void take it FOR SHARE (the close's FOR UPDATE then
// waits for them, so a sale never lands in a drawer being counted), the
// close takes it FOR UPDATE before it aggregates. The branch wall applies.
func (r *PostgresRepository) LockTillSession(ctx context.Context, id uuid.UUID, forUpdate bool) error {
	mode := "FOR SHARE"
	if forUpdate {
		mode = "FOR UPDATE"
	}
	var one int
	err := r.ex(ctx).QueryRow(ctx, `SELECT 1 FROM till_sessions WHERE id = $1
		AND ($2::uuid IS NULL OR branch_id = $2) `+mode, id, branchctx.IDForQuery(ctx)).Scan(&one)
	if errors.Is(err, pgx.ErrNoRows) {
		return httpx.NotFound("till session not found")
	}
	return err
}

func (r *PostgresRepository) GetTillSession(ctx context.Context, id uuid.UUID) (*TillSession, error) {
	s, err := r.scanTillSession(r.ex(ctx).QueryRow(ctx, `SELECT `+tillCols+` FROM (
		SELECT id, register_id, branch_id, cashier_id, status, opening_float, opened_at, closed_at,
			expected_by_method, counted_by_method,
			CASE WHEN over_short IS NULL THEN NULL ELSE ROUND(over_short * 100)::bigint END AS over_short_cents,
			gl_entry_id, notes
		FROM till_sessions) t WHERE t.id = $1 AND ($2::uuid IS NULL OR t.branch_id = $2)`, id, branchctx.IDForQuery(ctx)))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, httpx.NotFound("till session not found")
	}
	if err != nil {
		return nil, fmt.Errorf("failed to read the till session: %w", err)
	}
	return s, nil
}

func (r *PostgresRepository) GetOpenTillSession(ctx context.Context, registerID string) (*TillSession, error) {
	s, err := r.scanTillSession(r.ex(ctx).QueryRow(ctx, `SELECT `+tillCols+` FROM (
		SELECT id, register_id, branch_id, cashier_id, status, opening_float, opened_at, closed_at,
			expected_by_method, counted_by_method,
			CASE WHEN over_short IS NULL THEN NULL ELSE ROUND(over_short * 100)::bigint END AS over_short_cents,
			gl_entry_id, notes
		FROM till_sessions) t WHERE t.register_id = $1 AND t.status = 'OPEN'
			AND ($2::uuid IS NULL OR t.branch_id = $2)`, registerID, branchctx.IDForQuery(ctx)))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("failed to read the open till session: %w", err)
	}
	return s, nil
}

func (r *PostgresRepository) CloseTillSession(ctx context.Context, s *TillSession) error {
	expected, err := json.Marshal(s.ExpectedByMethod)
	if err != nil {
		return err
	}
	counted, err := json.Marshal(s.CountedByMethod)
	if err != nil {
		return err
	}
	var over any
	if s.OverShort != nil {
		over = int64(*s.OverShort)
	}
	tag, err := r.ex(ctx).Exec(ctx, `
		UPDATE till_sessions SET status = 'CLOSED', closed_at = NOW(),
			expected_by_method = $2::jsonb, counted_by_method = $3::jsonb,
			over_short = ($4::bigint::numeric / 100), gl_entry_id = $5, notes = $6
		WHERE id = $1 AND status = 'OPEN'`,
		s.ID, string(expected), string(counted), over, s.GLEntryID, s.Notes)
	if err != nil {
		return fmt.Errorf("failed to close the till session: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return httpx.InvalidStateTransition("the till session is not open")
	}
	return nil
}

// AggregateTillSession sums a session's completed sales from the payments
// their tenders became (a voided sale's payments are VOIDED, so they drop
// out), and the cash refunds of the session's returns through their credit
// memos. The amounts are already net of change: a cash payment is the money
// kept (ADR 0005 section 14.2 C2-5).
func (r *PostgresRepository) AggregateTillSession(ctx context.Context, sessionID uuid.UUID) (*TillAggregate, error) {
	agg := &TillAggregate{TenderedByMethod: map[string]int64{}}
	err := r.ex(ctx).QueryRow(ctx, `
		SELECT count(DISTINCT t.id),
			COALESCE(SUM(ROUND(t.total * 100)::bigint), 0),
			COALESCE(SUM(ROUND(t.tax_amount * 100)::bigint), 0),
			COALESCE(SUM(ROUND(t.change_due * 100)::bigint), 0)
		FROM pos_transactions t
		WHERE t.till_session_id = $1 AND t.status = 'COMPLETED'`, sessionID).
		Scan(&agg.SaleCount, &agg.SalesTotalCents, &agg.TaxTotalCents, &agg.ChangeCents)
	if err != nil {
		return nil, fmt.Errorf("failed to aggregate the session's sales: %w", err)
	}
	rows, err := r.ex(ctx).Query(ctx, `
		SELECT p.method, COALESCE(SUM(ROUND(p.amount * 100)::bigint), 0)
		FROM payments p
		JOIN pos_tenders pt ON pt.payment_id = p.id
		JOIN pos_transactions t ON t.id = pt.transaction_id
		WHERE t.till_session_id = $1 AND t.status = 'COMPLETED' AND p.status = 'POSTED'
		GROUP BY p.method`, sessionID)
	if err != nil {
		return nil, fmt.Errorf("failed to aggregate the session's payments: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var method string
		var cents int64
		if err := rows.Scan(&method, &cents); err != nil {
			return nil, err
		}
		agg.TenderedByMethod[method] = cents
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	// The session's cash refunds, through its returns' credit memos.
	if err := r.ex(ctx).QueryRow(ctx, `
		SELECT COALESCE(SUM(ROUND(rf.amount * 100)::bigint), 0)
		FROM payment_refunds rf
		JOIN credit_memos cm ON cm.id = rf.credit_memo_id
		JOIN pos_returns pr ON pr.credit_memo_id = cm.id
		WHERE pr.till_session_id = $1 AND rf.method = 'CASH'`, sessionID).Scan(&agg.CashRefundsCents); err != nil {
		return nil, fmt.Errorf("failed to aggregate the session's cash refunds: %w", err)
	}
	return agg, nil
}

func (r *PostgresRepository) CreateZReport(ctx context.Context, z *ZReport) error {
	if z.ID == uuid.Nil {
		z.ID = uuid.New()
	}
	var generated time.Time
	err := r.ex(ctx).QueryRow(ctx, `
		INSERT INTO till_z_reports (id, till_session_id, register_id, branch_id, over_short, payload, generated_at)
		VALUES ($1, $2, $3, $4, $5::numeric / 100, $6, NOW())
		ON CONFLICT (till_session_id) DO NOTHING
		RETURNING generated_at`,
		z.ID, z.TillSessionID, z.RegisterID, z.BranchID, int64(z.OverShort), z.Payload).Scan(&generated)
	z.GeneratedAt = httpx.TimestampOf(generated)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil // the frozen snapshot already exists
		}
		return fmt.Errorf("failed to persist the Z-report: %w", err)
	}
	return nil
}

func (r *PostgresRepository) GetZReportBySession(ctx context.Context, sessionID uuid.UUID) (*ZReport, error) {
	z := &ZReport{}
	var generatedAt time.Time
	err := r.ex(ctx).QueryRow(ctx, `
		SELECT id, till_session_id, register_id, branch_id, ROUND(over_short * 100)::bigint, payload, generated_at
		FROM till_z_reports WHERE till_session_id = $1`, sessionID).
		Scan(&z.ID, &z.TillSessionID, &z.RegisterID, &z.BranchID, &z.OverShort, &z.Payload, &generatedAt)
	z.GeneratedAt = httpx.TimestampOf(generatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, httpx.NotFound("no Z-report for this session")
	}
	if err != nil {
		return nil, fmt.Errorf("failed to read the Z-report: %w", err)
	}
	return z, nil
}

func (r *PostgresRepository) ListZReports(ctx context.Context, registerID string, date time.Time) ([]ZReport, error) {
	rows, err := r.ex(ctx).Query(ctx, `
		SELECT id, till_session_id, register_id, branch_id, ROUND(over_short * 100)::bigint, payload, generated_at
		FROM till_z_reports
		WHERE ($1 = '' OR register_id = $1)
			AND (generated_at >= $2::date AND generated_at < ($2::date + 1))
			AND ($3::uuid IS NULL OR branch_id = $3)
		ORDER BY generated_at DESC`, registerID, date, branchctx.IDForQuery(ctx))
	if err != nil {
		return nil, fmt.Errorf("failed to list Z-reports: %w", err)
	}
	defer rows.Close()
	out := []ZReport{}
	for rows.Next() {
		var z ZReport
		var generatedAt time.Time
		if err := rows.Scan(&z.ID, &z.TillSessionID, &z.RegisterID, &z.BranchID, &z.OverShort, &z.Payload, &generatedAt); err != nil {
			return nil, err
		}
		z.GeneratedAt = httpx.TimestampOf(generatedAt)
		out = append(out, z)
	}
	return out, rows.Err()
}
