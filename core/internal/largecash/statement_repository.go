// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026, FutureBuild, Inc. and OpenLBM contributors

package largecash

import (
	"context"
	"errors"
	"time"

	"github.com/gablelbm/gable/internal/platform/httpx"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// statementRecord is one payer's statement as the database holds it.
type statementRecord struct {
	PayerKey        string
	PayerName       string
	PayerAddress    string
	BusinessName    string
	BusinessAddress string
	ContactName     string
	Telephone       string
	AggregateCents  int64
	Currency        string
	CrossingCount   int
	FirstDate       *time.Time
	LastDate        *time.Time
	State           string
	IssuedDate      *time.Time
	IssuedBy        string
}

// StatementFacts are the facts the statement for one crossing's payer is
// built from.
type StatementFacts struct {
	PayerKey        string
	Year            int
	PayerName       string
	PayerAddress    string
	BusinessName    string
	BusinessAddress string
	ContactName     string
	Telephone       string
	AggregateCents  int64
	Currency        string
	CrossingCount   int
	FirstDate       *time.Time
	LastDate        *time.Time
	EventState      string
	FilingState     string
	StatementState  string
}

// The statement's year is the calendar year in the receiving branch's own
// zone (money checklist 11), never the session's or the server's. The
// aggregate is the payer's counted cash received in that year, summed from
// the counted tenders, each in its own branch's zone: a crossing's running
// total reaches back across the year boundary and is not the year's cash.
// The business block is the dealer's information contact, the receiving
// branch and the dealer profile; the payer is the addressee.

// StatementRows reads the year's statements, one per payer. A branch scoped
// caller sees the payers whose crossings touched its branch; the aggregate
// each statement carries is the dealer's whole year for that payer, because
// the United States aggregation is deliberately central.
func (r *Repository) StatementRows(ctx context.Context, year int, branch *uuid.UUID) ([]statementRecord, error) {
	rows, err := r.exec(ctx).Query(ctx, `
		WITH crossings AS (
		    SELECT e.id, e.payer_key, e.customer_id, e.branch_id, e.currency, e.created_at, e.transaction_ids
		    FROM large_cash_events e
		    LEFT JOIN locations el ON el.id = e.branch_id
		    WHERE e.state = 'reportable'
		      AND EXTRACT(YEAR FROM (e.created_at AT TIME ZONE COALESCE(el.timezone, 'UTC')))::int = $1
		),
		visible AS (
		    SELECT DISTINCT c.payer_key
		    FROM crossings c
		    WHERE $2::uuid IS NULL
		       OR c.branch_id = $2
		       OR EXISTS (
		           SELECT 1 FROM large_cash_tenders t
		           WHERE t.transaction_id = ANY(c.transaction_ids) AND t.branch_id = $2
		       )
		),
		cash AS (
		    SELECT tn.payer_key,
		           SUM(tn.counted_cents) AS aggregate_cents,
		           MIN(tn.created_at) AS first_date,
		           MAX(tn.created_at) AS last_date
		    FROM large_cash_tenders tn
		    LEFT JOIN locations tl ON tl.id = tn.branch_id
		    WHERE tn.rule_family = 'US_8300'
		      AND tn.payer_key IN (SELECT payer_key FROM crossings)
		      AND EXTRACT(YEAR FROM (tn.created_at AT TIME ZONE COALESCE(tl.timezone, 'UTC')))::int = $1
		    GROUP BY tn.payer_key
		),
		counts AS (
		    SELECT c.payer_key, COUNT(*) AS crossing_count FROM crossings c GROUP BY c.payer_key
		),
		latest AS (
		    SELECT DISTINCT ON (c.payer_key) c.payer_key, c.customer_id, c.branch_id, c.currency, c.transaction_ids
		    FROM crossings c
		    ORDER BY c.payer_key, c.created_at DESC
		)
		SELECT cash.payer_key,
		       COALESCE(NULLIF(cu.name, ''), NULLIF(pay.name, ''), '') AS payer_name,
		       COALESCE(NULLIF(cu.address, ''), pay.address, '') AS payer_address,
		       COALESCE(NULLIF(dp.trading_name, ''), NULLIF(dp.legal_name, ''), NULLIF(bl.name, ''), '') AS business_name,
		       concat_ws(', ', NULLIF(bl.address, ''), NULLIF(bl.city, ''), NULLIF(bl.state, ''), NULLIF(bl.zip, '')) AS business_address,
		       COALESCE(NULLIF(bl.name, ''), NULLIF(dp.trading_name, ''), NULLIF(dp.legal_name, ''), '') AS contact_name,
		       COALESCE(NULLIF(bl.phone, ''), NULLIF(dp.phone, ''), '') AS telephone,
		       cash.aggregate_cents, l.currency, counts.crossing_count, cash.first_date, cash.last_date,
		       COALESCE(st.state, 'not_issued'), st.issued_date, COALESCE(st.issued_by, '')
		FROM cash
		JOIN visible v ON v.payer_key = cash.payer_key
		JOIN counts ON counts.payer_key = cash.payer_key
		JOIN latest l ON l.payer_key = cash.payer_key
		LEFT JOIN locations bl ON bl.id = l.branch_id
		LEFT JOIN dealer_profile dp ON dp.id = 1
		LEFT JOIN customers cu ON cu.id = l.customer_id
		LEFT JOIN LATERAL (
		    SELECT lp.name, lp.address FROM large_cash_payers lp
		    WHERE lp.transaction_id = ANY(l.transaction_ids)
		    ORDER BY lp.created_at DESC LIMIT 1
		) pay ON TRUE
		LEFT JOIN large_cash_statements st ON st.payer_key = cash.payer_key AND st.year = $1
		ORDER BY cash.aggregate_cents DESC, cash.payer_key`, year, branch)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []statementRecord{}
	for rows.Next() {
		var rec statementRecord
		if err := rows.Scan(&rec.PayerKey, &rec.PayerName, &rec.PayerAddress, &rec.BusinessName, &rec.BusinessAddress,
			&rec.ContactName, &rec.Telephone, &rec.AggregateCents, &rec.Currency, &rec.CrossingCount,
			&rec.FirstDate, &rec.LastDate, &rec.State, &rec.IssuedDate, &rec.IssuedBy); err != nil {
			return nil, err
		}
		out = append(out, rec)
	}
	return out, rows.Err()
}

// StatementFacts reads the statement facts of the crossing's payer for the
// year the cash was received. The event and filing states ride with them, so
// the service can refuse a crossing that is not reportable or not filed.
func (r *Repository) StatementFacts(ctx context.Context, eventID uuid.UUID) (*StatementFacts, error) {
	var f StatementFacts
	err := r.exec(ctx).QueryRow(ctx, `
		WITH target AS (
		    SELECT e.id, e.payer_key, e.state, e.branch_id,
		           EXTRACT(YEAR FROM (e.created_at AT TIME ZONE COALESCE(tl.timezone, 'UTC')))::int AS year
		    FROM large_cash_events e
		    LEFT JOIN locations tl ON tl.id = e.branch_id
		    WHERE e.id = $1
		),
		crossings AS (
		    SELECT e.payer_key, e.customer_id, e.branch_id, e.currency, e.created_at, e.transaction_ids
		    FROM large_cash_events e
		    JOIN target t ON t.payer_key = e.payer_key
		    LEFT JOIN locations el ON el.id = e.branch_id
		    WHERE e.state = 'reportable'
		      AND EXTRACT(YEAR FROM (e.created_at AT TIME ZONE COALESCE(el.timezone, 'UTC')))::int = t.year
		),
		cash AS (
		    SELECT SUM(tn.counted_cents) AS aggregate_cents,
		           MIN(tn.created_at) AS first_date,
		           MAX(tn.created_at) AS last_date
		    FROM large_cash_tenders tn
		    JOIN target t ON t.payer_key = tn.payer_key
		    LEFT JOIN locations tl ON tl.id = tn.branch_id
		    WHERE tn.rule_family = 'US_8300'
		      AND EXTRACT(YEAR FROM (tn.created_at AT TIME ZONE COALESCE(tl.timezone, 'UTC')))::int = t.year
		),
		latest AS (
		    SELECT c.customer_id, c.branch_id, c.currency, c.transaction_ids
		    FROM crossings c ORDER BY c.created_at DESC LIMIT 1
		)
		SELECT t.payer_key, t.year,
		       COALESCE(NULLIF(cu.name, ''), NULLIF(pay.name, ''), '') AS payer_name,
		       COALESCE(NULLIF(cu.address, ''), pay.address, '') AS payer_address,
		       COALESCE(NULLIF(dp.trading_name, ''), NULLIF(dp.legal_name, ''), NULLIF(bl.name, ''), '') AS business_name,
		       concat_ws(', ', NULLIF(bl.address, ''), NULLIF(bl.city, ''), NULLIF(bl.state, ''), NULLIF(bl.zip, '')) AS business_address,
		       COALESCE(NULLIF(bl.name, ''), NULLIF(dp.trading_name, ''), NULLIF(dp.legal_name, ''), '') AS contact_name,
		       COALESCE(NULLIF(bl.phone, ''), NULLIF(dp.phone, ''), '') AS telephone,
		       COALESCE(cash.aggregate_cents, 0), COALESCE(l.currency, ''), (SELECT COUNT(*) FROM crossings),
		       cash.first_date, cash.last_date,
		       t.state, COALESCE(f.state, ''), COALESCE(st.state, 'not_issued')
		FROM target t
		LEFT JOIN cash ON TRUE
		LEFT JOIN latest l ON TRUE
		LEFT JOIN locations bl ON bl.id = COALESCE(l.branch_id, t.branch_id)
		LEFT JOIN dealer_profile dp ON dp.id = 1
		LEFT JOIN customers cu ON cu.id = l.customer_id
		LEFT JOIN LATERAL (
		    SELECT lp.name, lp.address FROM large_cash_payers lp
		    WHERE lp.transaction_id = ANY(l.transaction_ids)
		    ORDER BY lp.created_at DESC LIMIT 1
		) pay ON TRUE
		LEFT JOIN large_cash_filings f ON f.event_id = t.id
		LEFT JOIN large_cash_statements st ON st.payer_key = t.payer_key AND st.year = t.year`, eventID).
		Scan(&f.PayerKey, &f.Year, &f.PayerName, &f.PayerAddress, &f.BusinessName, &f.BusinessAddress,
			&f.ContactName, &f.Telephone, &f.AggregateCents, &f.Currency, &f.CrossingCount,
			&f.FirstDate, &f.LastDate, &f.EventState, &f.FilingState, &f.StatementState)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, httpx.NotFound("crossing not found")
	}
	if err != nil {
		return nil, err
	}
	return &f, nil
}

// IssueStatement writes the statement of a payer and a year as issued. The
// upsert is guarded on the row not being issued already and answers whether
// this call issued it: the guard is what lets exactly one of several
// managers issue the one statement, under any interleaving.
func (r *Repository) IssueStatement(ctx context.Context, payerKey string, year int,
	issuedDate time.Time, actor string) (bool, error) {
	var id uuid.UUID
	err := r.exec(ctx).QueryRow(ctx, `
		INSERT INTO large_cash_statements (payer_key, year, state, issued_date, issued_by)
		VALUES ($1, $2, 'issued', $3, $4)
		ON CONFLICT (payer_key, year) DO UPDATE
		SET state = 'issued', issued_date = EXCLUDED.issued_date, issued_by = EXCLUDED.issued_by
		WHERE large_cash_statements.state = 'not_issued'
		RETURNING id`, payerKey, year, issuedDate, actor).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	return err == nil, err
}
