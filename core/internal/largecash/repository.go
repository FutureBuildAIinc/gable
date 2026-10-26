// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026, FutureBuild, Inc. and OpenLBM contributors

package largecash

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/gablelbm/gable/internal/platform/httpx"
	"github.com/gablelbm/gable/pkg/database"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// Repository is the large cash SQL, all through database.GetExecutor(ctx)
// so a call made inside the completion's transaction joins it: the counted
// tenders, the payer row and the crossing's event row ride the completion
// or none of them do.
type Repository struct {
	db *database.DB
}

// NewRepository builds the repository.
func NewRepository(db *database.DB) *Repository { return &Repository{db: db} }

func (r *Repository) exec(ctx context.Context) database.Executor { return r.db.GetExecutor(ctx) }

// Setting reads one system_settings value, "" when unset.
func (r *Repository) Setting(ctx context.Context, key string) (string, error) {
	var v *string
	if err := r.exec(ctx).QueryRow(ctx, `SELECT value FROM system_settings WHERE key = $1`, key).Scan(&v); err != nil {
		if errors.Is(err, errNoRows) {
			return "", nil
		}
		return "", err
	}
	if v == nil {
		return "", nil
	}
	return *v, nil
}

// PutSetting upserts one system_settings value.
func (r *Repository) PutSetting(ctx context.Context, key, value string) error {
	_, err := r.exec(ctx).Exec(ctx,
		`INSERT INTO system_settings (key, value) VALUES ($1, $2)
		 ON CONFLICT (key) DO UPDATE SET value = EXCLUDED.value`, key, value)
	return err
}

// BranchCountry answers the country whose rule a branch runs: the branch's
// own country_code, else the dealer's country.default, else "".
func (r *Repository) BranchCountry(ctx context.Context, branchID uuid.UUID) (string, error) {
	var country string
	err := r.exec(ctx).QueryRow(ctx, `
		SELECT COALESCE(NULLIF(l.country_code, ''), COALESCE((SELECT value FROM system_settings WHERE key = $2), ''))
		FROM locations l WHERE l.id = $1`, branchID, KeyCountryDefault).Scan(&country)
	if errors.Is(err, errNoRows) {
		return "", errors.New("the sale's branch does not exist")
	}
	return strings.ToUpper(strings.TrimSpace(country)), err
}

// BranchTimezone answers the branch's timezone, UTC when unset.
func (r *Repository) BranchTimezone(ctx context.Context, branchID uuid.UUID) *time.Location {
	var tz *string
	if err := r.exec(ctx).QueryRow(ctx, `SELECT timezone FROM locations WHERE id = $1`, branchID).Scan(&tz); err != nil || tz == nil || *tz == "" {
		return time.UTC
	}
	loc, err := time.LoadLocation(*tz)
	if err != nil {
		return time.UTC
	}
	return loc
}

// BranchDayWindow is the static 24 hour window the Canadian rule runs in:
// the branch's local calendar day of the moment, never longer than 24
// hours and never overlapping the next day's.
func (r *Repository) BranchDayWindow(ctx context.Context, branchID uuid.UUID, now time.Time) (time.Time, time.Time, error) {
	loc := r.BranchTimezone(ctx, branchID)
	local := now.In(loc)
	start := time.Date(local.Year(), local.Month(), local.Day(), 0, 0, 0, 0, loc)
	return start, start.Add(24 * time.Hour), nil
}

// AnyConsumerDurableLine answers whether any line is a product flagged
// consumer durable priced over the threshold (the test is at the sale
// line: the IRS's own rule, and no grouping of consumer durables).
func (r *Repository) AnyConsumerDurableLine(ctx context.Context, lines []LineFact, thresholdCents int64) (bool, error) {
	ids := make([]uuid.UUID, 0, len(lines))
	for i := range lines {
		if lines[i].LineTotalCents > thresholdCents {
			ids = append(ids, lines[i].ProductID)
		}
	}
	if len(ids) == 0 {
		return false, nil
	}
	var n int64
	if err := r.exec(ctx).QueryRow(ctx,
		`SELECT count(*) FROM products WHERE id = ANY($1) AND consumer_durable`, ids).Scan(&n); err != nil {
		return false, err
	}
	return n > 0, nil
}

// AdvisoryLock serializes the aggregations of one payer, so two registers
// crossing the threshold for the same payer at once both see the other's
// tenders and exactly one event row comes out of it with the right total.
func (r *Repository) AdvisoryLock(ctx context.Context, payerKey string) error {
	_, err := r.exec(ctx).Exec(ctx, `SELECT pg_advisory_xact_lock(hashtext($1))`, payerKey)
	return err
}

// USSinceLastEvent answers the payer's United States running total since
// their most recent crossing inside the 12 month window: the counter is
// cash received since the last report, not cash received in the year. It
// also answers the transactions and tender count of that running period,
// for the crossing row. The caller holds the payer's advisory lock.
func (r *Repository) USSinceLastEvent(ctx context.Context, payerKey string, windowStart time.Time) (int64, []uuid.UUID, int, error) {
	var base int64
	var since *time.Time
	if err := r.exec(ctx).QueryRow(ctx, `
		SELECT MAX(created_at) FROM large_cash_events
		WHERE payer_key = $1 AND rule_family = $2 AND created_at >= $3`, payerKey, RuleUS, windowStart).Scan(&since); err != nil {
		return 0, nil, 0, err
	}
	lower := windowStart
	if since != nil {
		lower = *since
	}
	var count int
	if err := r.exec(ctx).QueryRow(ctx, `
		SELECT COALESCE(SUM(counted_cents), 0), count(*) FROM large_cash_tenders
		WHERE payer_key = $1 AND rule_family = $2 AND created_at > $3`, payerKey, RuleUS, lower).Scan(&base, &count); err != nil {
		return 0, nil, 0, err
	}
	ids, err := r.transactionsSince(ctx, payerKey, RuleUS, lower)
	if err != nil {
		return 0, nil, 0, err
	}
	return base, ids, count, nil
}

// CAInWindow answers the payer's Canadian dealer threshold total inside
// the static 24 hour window, with the transactions and tender count of the
// window. The caller holds the payer's advisory lock.
func (r *Repository) CAInWindow(ctx context.Context, payerKey string, start, end time.Time) (int64, []uuid.UUID, int, error) {
	var base int64
	var count int
	if err := r.exec(ctx).QueryRow(ctx, `
		SELECT COALESCE(SUM(counted_cents), 0), count(*) FROM large_cash_tenders
		WHERE payer_key = $1 AND rule_family = $2 AND created_at >= $3 AND created_at < $4`,
		payerKey, RuleCA, start, end).Scan(&base, &count); err != nil {
		return 0, nil, 0, err
	}
	ids, err := r.transactionsSince(ctx, payerKey, RuleCA, start)
	if err != nil {
		return 0, nil, 0, err
	}
	return base, ids, count, nil
}

func (r *Repository) transactionsSince(ctx context.Context, payerKey, rule string, since time.Time) ([]uuid.UUID, error) {
	rows, err := r.exec(ctx).Query(ctx,
		`SELECT DISTINCT transaction_id FROM large_cash_tenders WHERE payer_key = $1 AND rule_family = $2 AND created_at > $3`,
		payerKey, rule, since)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var ids []uuid.UUID
	for rows.Next() {
		var id uuid.UUID
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

// InsertTender records one tender that counted, with the classifier's
// reason and instrument, the audit a later dispute reads.
func (r *Repository) InsertTender(ctx context.Context, transactionID, branchID uuid.UUID, ruleFamily, payerKey string,
	countedCents int64, instrument, reason string, tenderIndex int) error {
	_, err := r.exec(ctx).Exec(ctx, `
		INSERT INTO large_cash_tenders (transaction_id, branch_id, rule_family, payer_key, counted_cents, instrument, reason, tender_index)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8)`,
		transactionID, branchID, ruleFamily, payerKey, countedCents, instrument, reason, tenderIndex)
	return err
}

// InsertPayer records the captured payer identity. The taxpayer
// identification number and the ID number arrive sealed.
func (r *Repository) InsertPayer(ctx context.Context, transactionID, branchID uuid.UUID, customerID *uuid.UUID,
	country, name, address, occupation, dateOfBirth, idType, idNumberSealed, tinSealed, tinHash string, thirdParty bool) error {
	dob := &dateOfBirth
	if dateOfBirth == "" {
		dob = nil
	}
	_, err := r.exec(ctx).Exec(ctx, `
		INSERT INTO large_cash_payers (transaction_id, branch_id, customer_id, country, name, address, occupation,
			date_of_birth, id_type, id_number_sealed, tin_sealed, tin_hash, third_party_payer)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13)`,
		transactionID, branchID, customerID, country, name, address, occupation, dob,
		idType, idNumberSealed, tinSealed, tinHash, thirdParty)
	return err
}

// InsertEvent records one threshold crossing: reportable is the United
// States duty, recorded is the Canadian dealer policy.
func (r *Repository) InsertEvent(ctx context.Context, payerKey string, customerID *uuid.UUID, branchID uuid.UUID, ruleFamily, currency string,
	amountCents, totalCents int64, transactionIDs []uuid.UUID, tenderCount int, state string) error {
	_, err := r.exec(ctx).Exec(ctx, `
		INSERT INTO large_cash_events (payer_key, customer_id, branch_id, rule_family, currency, amount_cents, total_cents,
			transaction_ids, tender_count, state)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)`,
		payerKey, customerID, branchID, ruleFamily, currency, amountCents, totalCents, transactionIDs, tenderCount, state)
	return err
}

// SaleFacts loads one open or held sale's large cash facts: branch, named
// customer, currency, and each billed line's product and total in cents.
func (r *Repository) SaleFacts(ctx context.Context, transactionID uuid.UUID) (*CompletionFacts, error) {
	f := &CompletionFacts{TransactionID: transactionID}
	err := r.exec(ctx).QueryRow(ctx, `
		SELECT branch_id, customer_id, COALESCE(currency, 'USD') FROM pos_transactions WHERE id = $1`,
		transactionID).Scan(&f.BranchID, &f.CustomerID, &f.Currency)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, &httpx.Error{Status: http.StatusNotFound, Code: httpx.CodeNotFound,
			Message: "sale not found"}
	}
	if err != nil {
		return nil, err
	}
	rows, err := r.exec(ctx).Query(ctx, `
		SELECT product_id, ROUND(line_total * 100)::bigint FROM pos_line_items
		WHERE transaction_id = $1 AND line_type = 'PRODUCT'`, transactionID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var l LineFact
		if err := rows.Scan(&l.ProductID, &l.LineTotalCents); err != nil {
			return nil, err
		}
		f.Lines = append(f.Lines, l)
	}
	return f, rows.Err()
}

var errNoRows = pgx.ErrNoRows
