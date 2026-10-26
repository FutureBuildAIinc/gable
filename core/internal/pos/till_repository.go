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
	"github.com/gablelbm/gable/pkg/middleware"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
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
		// two openers racing for one register: the second meets the one open
		// drawer index and answers the same refusal the pre-check does
		var pg *pgconn.PgError
		if errors.As(err, &pg) && pg.Code == "23505" && pg.ConstraintName == "idx_till_sessions_open_register" {
			return conflict("till_open", fmt.Sprintf("register %s already has an open till session", s.RegisterID))
		}
		return fmt.Errorf("failed to open the till session: %w", mapWriteError(err))
	}
	s.OpenedAt = httpx.TimestampOf(opened)
	return nil
}

func (r *PostgresRepository) scanTillSession(row pgx.Row) (*TillSession, error) {
	var s TillSession
	var status string
	var openedAt, closedAt, signedOffAt *time.Time
	var signoffBy, signoffReason *string
	err := row.Scan(&s.ID, &s.RegisterID, &s.BranchID, &s.CashierID, &status, &s.OpeningFloat, &openedAt, &closedAt,
		&s.ExpectedByMethod, &s.CountedByMethod, &s.OverShort, &s.GLEntryID, &s.Notes,
		&s.ClosedBy, &s.VarianceThreshold, &signoffBy, &signoffReason, &signedOffAt)
	if err != nil {
		return nil, err
	}
	if signoffBy != nil && signedOffAt != nil {
		reason := ""
		if signoffReason != nil {
			reason = *signoffReason
		}
		s.SignOff = &TillSignOff{SignedOffBy: *signoffBy, Reason: reason, SignedOffAt: httpx.TimestampOf(*signedOffAt)}
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
	expected_by_method, counted_by_method, over_short_cents, gl_entry_id, notes,
	closed_by, variance_threshold_cents, signoff_by, signoff_reason, signed_off_at`

// tillInner is the session table with its over or short in cents, the
// subquery every walled read selects tillCols from.
const tillInner = `SELECT id, register_id, branch_id, cashier_id, status, opening_float, opened_at, closed_at,
	expected_by_method, counted_by_method,
	CASE WHEN over_short IS NULL THEN NULL ELSE ROUND(over_short * 100)::bigint END AS over_short_cents,
	gl_entry_id, notes, closed_by, variance_threshold_cents, signoff_by, signoff_reason, signed_off_at
	FROM till_sessions`

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
		AND `+middleware.Wall("", 2, 3)+` `+mode, id, middleware.BranchIDForQuery(ctx), middleware.GrantsSubForQuery(ctx)).Scan(&one)
	if errors.Is(err, pgx.ErrNoRows) {
		return httpx.NotFound("till session not found")
	}
	return err
}

// TillSessionBranch reads a till session's own branch with no wall, for the
// act hold that must decide before the walled read hides the record. A
// session that does not exist answers found false, and one that exists with
// no branch answers found true and a nil branch.
func (r *PostgresRepository) TillSessionBranch(ctx context.Context, id uuid.UUID) (*uuid.UUID, bool, error) {
	var branch *uuid.UUID
	err := r.ex(ctx).QueryRow(ctx, `SELECT branch_id FROM till_sessions WHERE id = $1`, id).Scan(&branch)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, fmt.Errorf("failed to read the till session's branch: %w", err)
	}
	return branch, true, nil
}

func (r *PostgresRepository) GetTillSession(ctx context.Context, id uuid.UUID) (*TillSession, error) {
	s, err := r.scanTillSession(r.ex(ctx).QueryRow(ctx, `SELECT `+tillCols+` FROM (`+tillInner+`) t WHERE t.id = $1 AND `+middleware.Wall("t", 2, 3), id, middleware.BranchIDForQuery(ctx), middleware.GrantsSubForQuery(ctx)))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, httpx.NotFound("till session not found")
	}
	if err != nil {
		return nil, fmt.Errorf("failed to read the till session: %w", err)
	}
	return s, nil
}

func (r *PostgresRepository) GetOpenTillSession(ctx context.Context, registerID string) (*TillSession, error) {
	s, err := r.scanTillSession(r.ex(ctx).QueryRow(ctx, `SELECT `+tillCols+` FROM (`+tillInner+`) t WHERE t.register_id = $1 AND t.status = 'OPEN'
			AND `+middleware.Wall("t", 2, 3), registerID, middleware.BranchIDForQuery(ctx), middleware.GrantsSubForQuery(ctx)))
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
	var over, threshold any
	if s.OverShort != nil {
		over = int64(*s.OverShort)
	}
	if s.VarianceThreshold != nil {
		threshold = int64(*s.VarianceThreshold)
	}
	// the count ends the session CLOSED, or PENDING_SIGNOFF when its variance
	// passed the threshold (s.Status says which)
	tag, err := r.ex(ctx).Exec(ctx, `
		UPDATE till_sessions SET status = $7, closed_at = NOW(),
			expected_by_method = $2::jsonb, counted_by_method = $3::jsonb,
			over_short = ($4::bigint::numeric / 100), gl_entry_id = $5, notes = $6,
			closed_by = $8, variance_threshold_cents = $9
		WHERE id = $1 AND status = 'OPEN'`,
		s.ID, string(expected), string(counted), over, s.GLEntryID, s.Notes, string(s.Status), s.ClosedBy, threshold)
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
// out), the cash refunds of the session's returns through their credit
// memos, and the card refunds booked against the still completed sales' card
// tenders (the void refunds resolutions book). The amounts are already net
// of change: a cash payment is the money kept (ADR 0005 section 14.2 C2-5).
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
	// The session's cash refunds, through its returns' credit memos. The cash
	// that left the drawer is the refund entry's cash leg: a cash refund of a
	// rounded sale pays the rounded figure (CP-18), while the refund row
	// carries the exact credit the customer's account was given. A refund with
	// no entry (no ledger wired) falls back to the row's own amount.
	if err := r.ex(ctx).QueryRow(ctx, `
		SELECT COALESCE(SUM(ROUND(COALESCE(cash.cents, rf.amount * 100))::bigint), 0)
		FROM payment_refunds rf
		JOIN credit_memos cm ON cm.id = rf.credit_memo_id
		JOIN pos_returns pr ON pr.credit_memo_id = cm.id
		LEFT JOIN LATERAL (
			SELECT SUM(ROUND(l.credit * 100)::bigint) AS cents
			FROM gl_journal_entries e
			JOIN gl_journal_lines l ON l.journal_entry_id = e.id
			JOIN gl_accounts a ON a.id = l.account_id AND a.code = '1010'
			WHERE e.id = rf.gl_entry_id
		) cash ON true
		WHERE pr.till_session_id = $1 AND rf.method = 'CASH'`, sessionID).Scan(&agg.CashRefundsCents); err != nil {
		return nil, fmt.Errorf("failed to aggregate the session's cash refunds: %w", err)
	}
	// The card refunds booked against the session's still completed sales'
	// card tenders (r12 P3-2): a void refund a resolution booked is written
	// straight against the tender's payment (a return's refund goes through
	// its credit memo and never joins here), and the figure drops with the
	// tender when the void completes, the same completed sale filter the
	// tendered sum carries.
	if err := r.ex(ctx).QueryRow(ctx, `
		SELECT COALESCE(SUM(ROUND(rf.amount * 100)::bigint), 0)
		FROM payment_refunds rf
		JOIN payments p ON p.id = rf.payment_id
		JOIN pos_tenders pt ON pt.payment_id = p.id
		JOIN pos_transactions t ON t.id = pt.transaction_id
		WHERE t.till_session_id = $1 AND t.status = 'COMPLETED' AND p.status = 'POSTED' AND rf.method = 'CARD'`, sessionID).
		Scan(&agg.CardRefundsCents); err != nil {
		return nil, fmt.Errorf("failed to aggregate the session's card refunds: %w", err)
	}
	// The session's card returns, through their credit memos, as the CASH
	// figure counts its cash returns (fourteenth review P3-2): a card return's refund rides
	// its memo, never the tender's payment, so without this the CARD figure
	// disagreed with the books by every card return of the session.
	var cardReturnRefunds int64
	if err := r.ex(ctx).QueryRow(ctx, `
		SELECT COALESCE(SUM(ROUND(rf.amount * 100)::bigint), 0)
		FROM payment_refunds rf
		JOIN credit_memos cm ON cm.id = rf.credit_memo_id
		JOIN pos_returns pr ON pr.credit_memo_id = cm.id
		WHERE pr.till_session_id = $1 AND rf.method = 'CARD'`, sessionID).Scan(&cardReturnRefunds); err != nil {
		return nil, fmt.Errorf("failed to aggregate the session's card return refunds: %w", err)
	}
	agg.CardRefundsCents += cardReturnRefunds
	// The session's movements, by kind: paid in, paid out, drops and pickups
	// move the cash the drawer should hold (CP-08).
	totals, err := r.TillMovementTotals(ctx, sessionID)
	if err != nil {
		return nil, err
	}
	agg.MovementTotals = totals
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
		FROM till_z_reports WHERE till_session_id = $1 AND `+middleware.Wall("", 2, 3), sessionID, middleware.BranchIDForQuery(ctx), middleware.GrantsSubForQuery(ctx)).
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

// zReportZoneSQL is the zone of a Z report row's branch, UTC when it has none.
const zReportZoneSQL = `COALESCE((SELECT l.timezone FROM locations l WHERE l.id = z.branch_id), 'UTC')`

func (r *PostgresRepository) ListZReports(ctx context.Context, registerID string, date string) ([]ZReport, error) {
	// A report's business day is the day it was generated on in its branch's
	// own zone (UTC when it has none), the rule the branch day uses: never a
	// UTC day, and never the server's clock. No date is the branch's today.
	rows, err := r.ex(ctx).Query(ctx, `
		SELECT id, till_session_id, register_id, branch_id, ROUND(over_short * 100)::bigint, payload, generated_at
		FROM till_z_reports z
		WHERE ($1 = '' OR register_id = $1)
			AND (generated_at AT TIME ZONE `+zReportZoneSQL+`)::date
				= COALESCE(NULLIF($2::text, '')::date, (NOW() AT TIME ZONE `+zReportZoneSQL+`)::date)
			AND `+middleware.Wall("", 3, 4)+`
		ORDER BY generated_at DESC`, registerID, date, middleware.BranchIDForQuery(ctx), middleware.GrantsSubForQuery(ctx))
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

// VarianceThreshold is the threshold a close over or short is held to at a
// branch, in cents: the branch's own row, else the database wide default
// (system_settings till.variance_threshold_cents). set is false when neither
// exists: no threshold is in force, and no close waits for sign off.
func (r *PostgresRepository) VarianceThreshold(ctx context.Context, branch *uuid.UUID) (cents int64, set bool, err error) {
	var v *int64
	err = r.ex(ctx).QueryRow(ctx, `SELECT COALESCE(
		(SELECT p.threshold_cents FROM till_variance_policy p WHERE p.branch_id = $1),
		(SELECT CASE WHEN s.value ~ '^[0-9]{1,15}$' THEN s.value::bigint END FROM system_settings s
			WHERE s.key = 'till.variance_threshold_cents'))`, branch).Scan(&v)
	if err != nil {
		return 0, false, fmt.Errorf("failed to read the variance threshold: %w", err)
	}
	if v == nil {
		return 0, false, nil
	}
	return *v, true, nil
}

// SignOffTillSession releases a session waiting for sign off: it ends CLOSED
// with the ledger entry of its variance and who signed off, why and when.
func (r *PostgresRepository) SignOffTillSession(ctx context.Context, s *TillSession) error {
	var signed time.Time
	err := r.ex(ctx).QueryRow(ctx, `
		UPDATE till_sessions SET status = 'CLOSED', gl_entry_id = $2, signoff_by = $3, signoff_reason = $4, signed_off_at = NOW()
		WHERE id = $1 AND status = 'PENDING_SIGNOFF'
		RETURNING signed_off_at`,
		s.ID, s.GLEntryID, s.SignOff.SignedOffBy, s.SignOff.Reason).Scan(&signed)
	if errors.Is(err, pgx.ErrNoRows) {
		return httpx.InvalidStateTransition("the till session is not waiting for sign off")
	}
	if err != nil {
		return fmt.Errorf("failed to sign off the till session: %w", err)
	}
	s.SignOff.SignedOffAt = httpx.TimestampOf(signed)
	return nil
}

// ListPendingSignoffs pages the sessions waiting for a manager, oldest close
// first (closed_at, id), inside the caller's wall.
func (r *PostgresRepository) ListPendingSignoffs(ctx context.Context, branch *uuid.UUID, after *time.Time, afterID *uuid.UUID, limit int) ([]TillSession, bool, error) {
	rows, err := r.ex(ctx).Query(ctx, `SELECT `+tillCols+` FROM (`+tillInner+`) t
		WHERE t.status = 'PENDING_SIGNOFF' AND `+middleware.Wall("t", 1, 2)+`
		  AND ($3::uuid IS NULL OR t.branch_id = $3)
		  AND ($4::timestamptz IS NULL OR (t.closed_at, t.id) > ($4::timestamptz, $5::uuid))
		ORDER BY t.closed_at, t.id LIMIT $6`,
		middleware.BranchIDForQuery(ctx), middleware.GrantsSubForQuery(ctx), branch, after, afterID, limit+1)
	if err != nil {
		return nil, false, fmt.Errorf("failed to list the sessions waiting for sign off: %w", err)
	}
	defer rows.Close()
	out := []TillSession{}
	for rows.Next() {
		s, err := r.scanTillSession(rows)
		if err != nil {
			return nil, false, err
		}
		out = append(out, *s)
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

// VariancePolicy is a branch's threshold answer: its own override, the
// database wide default, and which of them is in force.
type VariancePolicy struct {
	BranchID uuid.UUID `json:"branch_id"`
	// ThresholdCents is the figure in force (null when no threshold exists);
	// BranchThresholdCents the branch's own override (null when it has none);
	// DefaultThresholdCents the database wide default (null when unset).
	ThresholdCents        *httpx.Cents `json:"variance_threshold_cents"`
	BranchThresholdCents  *httpx.Cents `json:"branch_threshold_cents"`
	DefaultThresholdCents *httpx.Cents `json:"default_threshold_cents"`
	Source                string       `json:"source"`
}

// GetVariancePolicy reads the branch's threshold with its two sources.
func (r *PostgresRepository) GetVariancePolicy(ctx context.Context, branch uuid.UUID) (*VariancePolicy, error) {
	p := &VariancePolicy{BranchID: branch}
	err := r.ex(ctx).QueryRow(ctx, `SELECT
		(SELECT threshold_cents FROM till_variance_policy WHERE branch_id = $1),
		(SELECT CASE WHEN value ~ '^[0-9]{1,15}$' THEN value::bigint END FROM system_settings WHERE key = 'till.variance_threshold_cents')`,
		branch).Scan(&p.BranchThresholdCents, &p.DefaultThresholdCents)
	if err != nil {
		return nil, fmt.Errorf("failed to read the variance policy: %w", err)
	}
	switch {
	case p.BranchThresholdCents != nil:
		p.ThresholdCents, p.Source = p.BranchThresholdCents, "branch"
	case p.DefaultThresholdCents != nil:
		p.ThresholdCents, p.Source = p.DefaultThresholdCents, "default"
	default:
		p.Source = "none"
	}
	return p, nil
}

// SetVariancePolicy writes (or, with a nil threshold, clears) the branch's
// own override.
func (r *PostgresRepository) SetVariancePolicy(ctx context.Context, branch uuid.UUID, cents *int64, actor string) error {
	if cents == nil {
		if _, err := r.ex(ctx).Exec(ctx, `DELETE FROM till_variance_policy WHERE branch_id = $1`, branch); err != nil {
			return fmt.Errorf("failed to clear the variance policy: %w", err)
		}
		return nil
	}
	if _, err := r.ex(ctx).Exec(ctx, `INSERT INTO till_variance_policy (branch_id, threshold_cents, updated_by, updated_at)
		VALUES ($1, $2, $3, NOW())
		ON CONFLICT (branch_id) DO UPDATE SET threshold_cents = EXCLUDED.threshold_cents,
			updated_by = EXCLUDED.updated_by, updated_at = NOW()`, branch, *cents, actor); err != nil {
		return fmt.Errorf("failed to write the variance policy: %w", mapWriteError(err))
	}
	return nil
}
