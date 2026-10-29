// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026, FutureBuild, Inc. and OpenLBM contributors

package largecash

import (
	"context"
	"time"

	"github.com/google/uuid"
)

// worklistRecord is one crossing as the database holds it. The service turns
// it into the row the manager sees: the deadline, the severity and the
// action are all computed, never stored.
type worklistRecord struct {
	EventID              uuid.UUID
	PayerKey             string
	PayerName            string
	CustomerID           *uuid.UUID
	CustomerAccount      string
	BranchID             uuid.UUID
	BranchName           string
	BranchAddress        string
	ContributedBranchIDs []uuid.UUID
	Currency             string
	TotalCents           int64
	RuleFamily           string
	TransactionCount     int
	TenderCount          int
	FirstDate            *time.Time
	LastDate             *time.Time
	ReceivedAt           time.Time
	EventState           string
	FilingRowState       string
	FiledDate            *time.Time
	AcknowledgementNo    string
	PastDeadline         bool
	StatementState       string
	StatementIssuedDate  *time.Time
	PayerDetailsCaptured bool
}

// WorklistRecords reads the crossings the manager's queue is made of. A
// branch scoped caller sees only the crossings its branch received cash for
// or contributed cash to; a nil branch reads every branch.
//
// The query reads the crossing, the branch that took the cash, the named
// customer or the captured payer, the filing row and the statement row, and
// the tenders of the aggregate: the first and the most recent dates and
// every branch that contributed to the total.
func (r *Repository) WorklistRecords(ctx context.Context, branch *uuid.UUID) ([]worklistRecord, error) {
	rows, err := r.exec(ctx).Query(ctx, `
		SELECT e.id, e.payer_key,
		       COALESCE(NULLIF(c.name, ''), NULLIF(p.name, ''), '') AS payer_name,
		       e.customer_id, COALESCE(c.account_number, ''),
		       e.branch_id, COALESCE(l.name, ''), COALESCE(l.address, ''),
		       COALESCE(tenders.branch_ids, ARRAY[]::uuid[]),
		       e.currency, e.total_cents, e.rule_family,
		       COALESCE(tenders.transaction_count, 0), e.tender_count,
		       tenders.first_date, tenders.last_date, e.created_at,
		       e.state, COALESCE(f.state, ''), f.filed_date, COALESCE(f.acknowledgement_no, ''), COALESCE(f.past_deadline, FALSE),
		       COALESCE(st.state, ''), st.issued_date,
		       (p.id IS NOT NULL) AS payer_details_captured
		FROM large_cash_events e
		JOIN locations l ON l.id = e.branch_id
		LEFT JOIN customers c ON c.id = e.customer_id
		LEFT JOIN LATERAL (
		    SELECT lp.id, lp.name FROM large_cash_payers lp
		    WHERE lp.transaction_id = ANY(e.transaction_ids)
		    ORDER BY lp.created_at DESC LIMIT 1
		) p ON TRUE
		LEFT JOIN large_cash_filings f ON f.event_id = e.id
		LEFT JOIN large_cash_statements st
		       ON st.payer_key = e.payer_key
		      AND st.year = EXTRACT(YEAR FROM e.created_at)::int
		LEFT JOIN LATERAL (
		    SELECT min(t.created_at) AS first_date, max(t.created_at) AS last_date,
		           array_agg(DISTINCT t.branch_id) AS branch_ids,
		           count(DISTINCT t.transaction_id) AS transaction_count
		    FROM large_cash_tenders t
		    WHERE t.transaction_id = ANY(e.transaction_ids)
		) tenders ON TRUE
		WHERE $1::uuid IS NULL
		   OR e.branch_id = $1
		   OR EXISTS (
		       SELECT 1 FROM large_cash_tenders t2
		       WHERE t2.transaction_id = ANY(e.transaction_ids) AND t2.branch_id = $1
		   )
		ORDER BY e.created_at DESC`, branch)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []worklistRecord{}
	for rows.Next() {
		var rec worklistRecord
		if err := rows.Scan(&rec.EventID, &rec.PayerKey, &rec.PayerName, &rec.CustomerID,
			&rec.CustomerAccount, &rec.BranchID, &rec.BranchName, &rec.BranchAddress,
			&rec.ContributedBranchIDs, &rec.Currency, &rec.TotalCents, &rec.RuleFamily,
			&rec.TransactionCount, &rec.TenderCount, &rec.FirstDate, &rec.LastDate,
			&rec.ReceivedAt, &rec.EventState, &rec.FilingRowState, &rec.FiledDate,
			&rec.AcknowledgementNo, &rec.PastDeadline, &rec.StatementState,
			&rec.StatementIssuedDate, &rec.PayerDetailsCaptured); err != nil {
			return nil, err
		}
		out = append(out, rec)
	}
	return out, rows.Err()
}
