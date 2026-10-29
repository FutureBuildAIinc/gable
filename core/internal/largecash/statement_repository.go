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
	BusinessAddress string
	ContactName     string
	Telephone       string
	AggregateCents  int64
	Currency        string
	CrossingCount   int
	FirstDate       *time.Time
	LastDate        *time.Time
	FilingState     string
	StatementState  string
}

// StatementRows reads the year's statements, one per payer. A branch scoped
// caller sees the payers whose crossings touched its branch; the aggregate
// each statement carries is the dealer's whole year for that payer, because
// the United States aggregation is deliberately central.
func (r *Repository) StatementRows(ctx context.Context, year int, branch *uuid.UUID) ([]statementRecord, error) {
	rows, err := r.exec(ctx).Query(ctx, `
		WITH crossings AS (
		    SELECT e.payer_key, e.customer_id, e.currency, e.total_cents, e.created_at, e.transaction_ids
		    FROM large_cash_events e
		    WHERE e.state = 'reportable'
		      AND EXTRACT(YEAR FROM e.created_at) = $1
		),
		visible AS (
		    SELECT DISTINCT c.payer_key
		    FROM crossings c
		    WHERE $2::uuid IS NULL
		       OR EXISTS (
		           SELECT 1 FROM large_cash_events e
		           WHERE e.payer_key = c.payer_key
		             AND e.state = 'reportable'
		             AND EXTRACT(YEAR FROM e.created_at) = $1
		             AND (e.branch_id = $2 OR EXISTS (
		                 SELECT 1 FROM large_cash_tenders t
		                 WHERE t.transaction_id = ANY(e.transaction_ids) AND t.branch_id = $2
		             ))
		       )
		),
		totals AS (
		    SELECT c.payer_key,
		           SUM(c.total_cents) AS aggregate_cents,
		           COUNT(*) AS crossing_count,
		           MIN(c.created_at) AS first_date,
		           MAX(c.created_at) AS last_date
		    FROM crossings c
		    GROUP BY c.payer_key
		),
		latest AS (
		    SELECT DISTINCT ON (e.payer_key) e.payer_key, e.customer_id, e.currency, e.transaction_ids
		    FROM large_cash_events e
		    WHERE e.state = 'reportable' AND EXTRACT(YEAR FROM e.created_at) = $1
		    ORDER BY e.payer_key, e.created_at DESC
		)
		SELECT t.payer_key,
		       COALESCE(NULLIF(cu.name, ''), NULLIF(pay.name, ''), '') AS payer_name,
		       COALESCE(NULLIF(cu.address, ''), pay.address, '') AS business_address,
		       COALESCE(cc.contact_name, '') AS contact_name,
		       COALESCE(cc.phone, cu.phone, '') AS telephone,
		       t.aggregate_cents, l.currency, t.crossing_count, t.first_date, t.last_date,
		       COALESCE(st.state, 'not_issued'), st.issued_date, COALESCE(st.issued_by, '')
		FROM totals t
		JOIN visible v ON v.payer_key = t.payer_key
		JOIN latest l ON l.payer_key = t.payer_key
		LEFT JOIN customers cu ON cu.id = l.customer_id
		LEFT JOIN LATERAL (
		    SELECT lp.name, lp.address FROM large_cash_payers lp
		    WHERE lp.transaction_id = ANY(l.transaction_ids)
		    ORDER BY lp.created_at DESC LIMIT 1
		) pay ON TRUE
		LEFT JOIN LATERAL (
		    SELECT trim(COALESCE(ct.first_name, '') || ' ' || COALESCE(ct.last_name, '')) AS contact_name, ct.phone
		    FROM customer_contacts ct
		    WHERE ct.customer_id = l.customer_id
		    ORDER BY ct.is_primary DESC, ct.created_at LIMIT 1
		) cc ON TRUE
		LEFT JOIN large_cash_statements st ON st.payer_key = t.payer_key AND st.year = $1
		ORDER BY t.aggregate_cents DESC, t.payer_key`, year, branch)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []statementRecord{}
	for rows.Next() {
		var rec statementRecord
		if err := rows.Scan(&rec.PayerKey, &rec.PayerName, &rec.BusinessAddress, &rec.ContactName,
			&rec.Telephone, &rec.AggregateCents, &rec.Currency, &rec.CrossingCount,
			&rec.FirstDate, &rec.LastDate, &rec.State, &rec.IssuedDate, &rec.IssuedBy); err != nil {
			return nil, err
		}
		out = append(out, rec)
	}
	return out, rows.Err()
}

// StatementFacts reads the statement facts of the crossing's payer for the
// year the cash was received.
func (r *Repository) StatementFacts(ctx context.Context, eventID uuid.UUID) (*StatementFacts, error) {
	var f StatementFacts
	err := r.exec(ctx).QueryRow(ctx, `
		WITH target AS (
		    SELECT e.payer_key, EXTRACT(YEAR FROM e.created_at)::int AS year
		    FROM large_cash_events e WHERE e.id = $1
		),
		crossings AS (
		    SELECT e.payer_key, e.customer_id, e.currency, e.total_cents, e.created_at, e.transaction_ids
		    FROM large_cash_events e, target t
		    WHERE e.payer_key = t.payer_key
		      AND e.state = 'reportable'
		      AND EXTRACT(YEAR FROM e.created_at) = t.year
		),
		totals AS (
		    SELECT c.payer_key,
		           SUM(c.total_cents) AS aggregate_cents,
		           COUNT(*) AS crossing_count,
		           MIN(c.created_at) AS first_date,
		           MAX(c.created_at) AS last_date
		    FROM crossings c
		    GROUP BY c.payer_key
		),
		latest AS (
		    SELECT DISTINCT ON (e.payer_key) e.payer_key, e.customer_id, e.currency, e.transaction_ids
		    FROM large_cash_events e, target t
		    WHERE e.payer_key = t.payer_key
		      AND e.state = 'reportable'
		      AND EXTRACT(YEAR FROM e.created_at) = t.year
		    ORDER BY e.payer_key, e.created_at DESC
		)
		SELECT t.payer_key, t.year,
		       COALESCE(NULLIF(cu.name, ''), NULLIF(pay.name, ''), '') AS payer_name,
		       COALESCE(NULLIF(cu.address, ''), pay.address, '') AS business_address,
		       COALESCE(cc.contact_name, '') AS contact_name,
		       COALESCE(cc.phone, cu.phone, '') AS telephone,
		       tot.aggregate_cents, l.currency, tot.crossing_count, tot.first_date, tot.last_date,
		       COALESCE(f.state, ''), COALESCE(st.state, 'not_issued')
		FROM target t
		JOIN totals tot ON tot.payer_key = t.payer_key
		JOIN latest l ON l.payer_key = t.payer_key
		LEFT JOIN customers cu ON cu.id = l.customer_id
		LEFT JOIN LATERAL (
		    SELECT lp.name, lp.address FROM large_cash_payers lp
		    WHERE lp.transaction_id = ANY(l.transaction_ids)
		    ORDER BY lp.created_at DESC LIMIT 1
		) pay ON TRUE
		LEFT JOIN LATERAL (
		    SELECT trim(COALESCE(ct.first_name, '') || ' ' || COALESCE(ct.last_name, '')) AS contact_name, ct.phone
		    FROM customer_contacts ct
		    WHERE ct.customer_id = l.customer_id
		    ORDER BY ct.is_primary DESC, ct.created_at LIMIT 1
		) cc ON TRUE
		LEFT JOIN large_cash_filings f ON f.event_id = $1
		LEFT JOIN large_cash_statements st ON st.payer_key = t.payer_key AND st.year = t.year`, eventID).
		Scan(&f.PayerKey, &f.Year, &f.PayerName, &f.BusinessAddress, &f.ContactName, &f.Telephone,
			&f.AggregateCents, &f.Currency, &f.CrossingCount, &f.FirstDate, &f.LastDate,
			&f.FilingState, &f.StatementState)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, httpx.NotFound("crossing not found")
	}
	if err != nil {
		return nil, err
	}
	return &f, nil
}

// IssueStatement writes the statement of a payer and a year as issued.
func (r *Repository) IssueStatement(ctx context.Context, payerKey string, year int,
	issuedDate time.Time, actor string) error {
	_, err := r.exec(ctx).Exec(ctx, `
		INSERT INTO large_cash_statements (payer_key, year, state, issued_date, issued_by)
		VALUES ($1, $2, 'issued', $3, $4)
		ON CONFLICT (payer_key, year) DO UPDATE
		SET state = 'issued', issued_date = EXCLUDED.issued_date, issued_by = EXCLUDED.issued_by`,
		payerKey, year, issuedDate, actor)
	return err
}
