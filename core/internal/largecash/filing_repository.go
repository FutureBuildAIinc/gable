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

// EventFilingFacts are the crossing's own facts as the filing needs them:
// what state the crossing is in, whether a filing is already recorded, and
// the figures the audit row names.
type EventFilingFacts struct {
	EventID       uuid.UUID
	State         string
	RuleFamily    string
	BranchID      uuid.UUID
	TotalCents    int64
	Currency      string
	FilingState   string
	PayerCaptured bool
}

// Form8300Facts are the facts the Form 8300 data set is built from: the
// crossing's money and dates, the branch that received the cash, and the
// payer's captured identity with the taxpayer identification number still
// sealed.
type Form8300Facts struct {
	EventID          uuid.UUID
	State            string
	RuleFamily       string
	Currency         string
	TotalCents       int64
	TransactionCount int
	TenderCount      int
	FirstDate        time.Time
	LastDate         time.Time
	ReceivedAt       time.Time
	BranchID         uuid.UUID
	BranchName       string
	BranchAddress    string
	PayerName        string
	PayerAddress     string
	PayerOccupation  string
	PayerDOB         string
	TINSealed        string
	ThirdPartyPayer  bool
	ThirdPartyName   string
	PayerCaptured    bool
}

// EventFilingFacts reads one crossing and the filing row it carries.
func (r *Repository) EventFilingFacts(ctx context.Context, eventID uuid.UUID) (*EventFilingFacts, error) {
	var f EventFilingFacts
	err := r.exec(ctx).QueryRow(ctx, `
		SELECT e.id, e.state, e.rule_family, e.branch_id, e.total_cents, e.currency,
		       COALESCE(f.state, ''),
		       EXISTS (SELECT 1 FROM large_cash_payers p WHERE p.transaction_id = ANY(e.transaction_ids))
		FROM large_cash_events e
		LEFT JOIN large_cash_filings f ON f.event_id = e.id
		WHERE e.id = $1`, eventID).Scan(&f.EventID, &f.State, &f.RuleFamily, &f.BranchID,
		&f.TotalCents, &f.Currency, &f.FilingState, &f.PayerCaptured)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, httpx.NotFound("crossing not found")
	}
	if err != nil {
		return nil, err
	}
	return &f, nil
}

// EnsureFilingRow creates the standing filing row of a crossing when it is
// not there yet. The standing row is the worklist's record of the filing
// obligation: it is what the deadline sweep marks and what a manager's
// filing takes.
func (r *Repository) EnsureFilingRow(ctx context.Context, eventID uuid.UUID) error {
	_, err := r.exec(ctx).Exec(ctx, `
		INSERT INTO large_cash_filings (event_id, state)
		VALUES ($1, 'not_filed')
		ON CONFLICT DO NOTHING`, eventID)
	return err
}

// ClaimFiling takes the standing row of a crossing for a filing. The update
// names the state it may take from, so under concurrency the second manager
// finds nothing to take and the caller answers the conflict.
func (r *Repository) ClaimFiling(ctx context.Context, eventID uuid.UUID, state string,
	filedDate time.Time, acknowledgementNo, actor string) (*Filing, error) {
	var f Filing
	err := r.exec(ctx).QueryRow(ctx, `
		UPDATE large_cash_filings
		SET state = $2, filed_date = $3, acknowledgement_no = $4, recorded_by = $5,
		    recorded_at = NOW(), past_deadline = FALSE
		WHERE event_id = $1 AND state = 'not_filed'
		RETURNING event_id, state, filed_date, acknowledgement_no, recorded_by, recorded_at, past_deadline`,
		eventID, state, filedDate, acknowledgementNo, actor).
		Scan(&f.EventID, &f.State, &f.FiledDate, &f.AcknowledgementNo, &f.RecordedBy, &f.RecordedAt, &f.PastDeadline)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &f, nil
}

// Form8300Facts reads everything the Form 8300 data set is built from. The
// taxpayer identification number comes back sealed: it is opened only by the
// export itself.
func (r *Repository) Form8300Facts(ctx context.Context, eventID uuid.UUID) (*Form8300Facts, error) {
	var f Form8300Facts
	err := r.exec(ctx).QueryRow(ctx, `
		SELECT e.id, e.state, e.rule_family, e.currency, e.total_cents, e.tender_count, e.created_at,
		       l.id, COALESCE(l.name, ''), COALESCE(l.address, ''),
		       COALESCE(p.name, ''), COALESCE(p.address, ''), COALESCE(p.occupation, ''),
		       COALESCE(to_char(p.date_of_birth, 'YYYY-MM-DD'), ''),
		       COALESCE(p.tin_sealed, ''), COALESCE(p.third_party_payer, FALSE),
		       COALESCE(tenders.first_date, e.created_at), COALESCE(tenders.last_date, e.created_at),
		       COALESCE(tenders.transaction_count, 0),
		       (p.id IS NOT NULL)
		FROM large_cash_events e
		JOIN locations l ON l.id = e.branch_id
		LEFT JOIN LATERAL (
		    SELECT lp.id, lp.name, lp.address, lp.occupation, lp.date_of_birth, lp.tin_sealed, lp.third_party_payer
		    FROM large_cash_payers lp
		    WHERE lp.transaction_id = ANY(e.transaction_ids)
		    ORDER BY lp.created_at DESC LIMIT 1
		) p ON TRUE
		LEFT JOIN LATERAL (
		    SELECT min(t.created_at) AS first_date, max(t.created_at) AS last_date,
		           count(DISTINCT t.transaction_id) AS transaction_count
		    FROM large_cash_tenders t
		    WHERE t.transaction_id = ANY(e.transaction_ids)
		) tenders ON TRUE
		WHERE e.id = $1`, eventID).
		Scan(&f.EventID, &f.State, &f.RuleFamily, &f.Currency, &f.TotalCents, &f.TenderCount, &f.ReceivedAt,
			&f.BranchID, &f.BranchName, &f.BranchAddress,
			&f.PayerName, &f.PayerAddress, &f.PayerOccupation, &f.PayerDOB,
			&f.TINSealed, &f.ThirdPartyPayer,
			&f.FirstDate, &f.LastDate, &f.TransactionCount, &f.PayerCaptured)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, httpx.NotFound("crossing not found")
	}
	if err != nil {
		return nil, err
	}
	return &f, nil
}

// EventWallFacts reads the branches a crossing belongs to: the branch that
// received the cash and every branch that contributed to the aggregate.
func (r *Repository) EventWallFacts(ctx context.Context, eventID uuid.UUID) (*EventWallFacts, error) {
	var f EventWallFacts
	err := r.exec(ctx).QueryRow(ctx, `
		SELECT e.id, e.branch_id,
		       COALESCE(tenders.branch_ids, ARRAY[]::uuid[])
		FROM large_cash_events e
		LEFT JOIN LATERAL (
		    SELECT array_agg(DISTINCT t.branch_id) AS branch_ids
		    FROM large_cash_tenders t
		    WHERE t.transaction_id = ANY(e.transaction_ids)
		) tenders ON TRUE
		WHERE e.id = $1`, eventID).Scan(&f.EventID, &f.BranchID, &f.ContributedBranchIDs)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, httpx.NotFound("crossing not found")
	}
	if err != nil {
		return nil, err
	}
	return &f, nil
}

// MarkPastDeadlines is the deadline sweep. It materializes the standing
// filing row of every reportable crossing, then marks the ones that are
// past their deadline and still not filed. It is idempotent: a second pass
// marks nothing and changes nothing.
func (r *Repository) MarkPastDeadlines(ctx context.Context, now time.Time) (int64, error) {
	var marked int64
	err := r.db.RunInTx(ctx, func(ctx context.Context) error {
		if _, err := r.exec(ctx).Exec(ctx, `
			INSERT INTO large_cash_filings (event_id, state)
			SELECT e.id, 'not_filed' FROM large_cash_events e
			WHERE e.state = 'reportable'
			ON CONFLICT DO NOTHING`); err != nil {
			return err
		}
		tag, err := r.exec(ctx).Exec(ctx, `
			UPDATE large_cash_filings f
			SET past_deadline = TRUE
			FROM large_cash_events e
			WHERE f.event_id = e.id
			  AND f.state = 'not_filed'
			  AND f.past_deadline = FALSE
			  AND e.state = 'reportable'
			  AND e.created_at + make_interval(days => $2) < $1`, now, FilingDeadlineDays)
		if err != nil {
			return err
		}
		marked = tag.RowsAffected()
		return nil
	})
	if err != nil {
		return 0, err
	}
	return marked, nil
}
