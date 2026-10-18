// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package account

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/gablelbm/gable/internal/gl"
	"github.com/gablelbm/gable/internal/platform/httpx"
	"github.com/gablelbm/gable/pkg/audit"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// Periodic finance charges (AR-05): one run as of a date charges every
// account whose invoices sit past the grace days, in exact basis-point
// arithmetic over integer cents, and posts the charge as a real invoice with
// its balanced entry (Debit AR 1020, Credit the income account the rules
// name). The default does not compound: a charge's own invoice is not a basis
// for the next charge, unless the tenant turned compounding on.
//
// A dry run is a run: it records the same run row and the same per customer
// items, marked DRY_RUN with no invoice and no entry, so the preview an
// operator read is the record of what was previewed. Executed, the act is
// idempotent per day: a customer a completed run already charged as of a
// date is not its basis again, so running twice for one as of charges once.
// Across days the overdue invoices are the basis each time, which is what a
// periodic charge is.

// FinanceChargeRules is the tenant's finance charge rules on the wire.
type FinanceChargeRules struct {
	MonthlyRateBps    int64           `json:"monthly_rate_bps"`
	GraceDays         int64           `json:"grace_days"`
	MinChargeCents    httpx.Cents     `json:"min_charge_cents"`
	MinBalanceCents   httpx.Cents     `json:"min_balance_cents"`
	Compound          bool            `json:"compound"`
	IncomeAccountCode string          `json:"income_account_code"`
	UpdatedBy         *string         `json:"updated_by"`
	UpdatedAt         httpx.Timestamp `json:"updated_at"`
}

// maxMonthlyRateBps caps a fat finger: ten times the whole balance per month
// is not a rate anyone meant. 10000 basis points is 100 percent.
const maxMonthlyRateBps int64 = 100_000

// GetFinanceChargeRules reads the tenant's rules (migration 124 seeds one
// row, charges off).
func (s *Service) GetFinanceChargeRules(ctx context.Context) (*FinanceChargeRules, error) {
	out := &FinanceChargeRules{}
	var updatedAt time.Time
	err := s.ex(ctx).QueryRow(ctx, `
		SELECT monthly_rate_bps, grace_days, min_charge_cents, min_balance_cents, compound, income_account_code,
		       updated_by, updated_at
		FROM ar_finance_charge_rules ORDER BY tenant_id NULLS FIRST LIMIT 1`).Scan(
		&out.MonthlyRateBps, &out.GraceDays, &out.MinChargeCents, &out.MinBalanceCents, &out.Compound,
		&out.IncomeAccountCode, &out.UpdatedBy, &updatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, httpx.NotFound("finance charge rules not found")
	}
	if err != nil {
		return nil, fmt.Errorf("failed to read the finance charge rules: %w", err)
	}
	out.UpdatedAt = httpx.TimestampOf(updatedAt)
	return out, nil
}

// UpdateFinanceChargeRules writes the tenant's rules. The whole row changes
// together: a rate and its grace days are one policy, not two switches.
func (s *Service) UpdateFinanceChargeRules(ctx context.Context, in *FinanceChargeRules, actor, role string) (*FinanceChargeRules, error) {
	if role != "" && !FinanceRole(role) {
		return nil, httpx.Forbidden("changing the finance charge rules needs the admin, owner or finance role")
	}
	if in.MonthlyRateBps < 0 {
		return nil, invalidField("monthly_rate_bps", "must not be negative")
	}
	if in.MonthlyRateBps > maxMonthlyRateBps {
		return nil, invalidField("monthly_rate_bps", fmt.Sprintf("must be at most %d (10000 basis points is 100 percent a month)", maxMonthlyRateBps))
	}
	if in.GraceDays < 0 || in.GraceDays > 365 {
		return nil, invalidField("grace_days", "must be between 0 and 365")
	}
	if in.MinChargeCents < 0 {
		return nil, invalidField("min_charge_cents", "must not be negative")
	}
	if in.MinBalanceCents < 0 {
		return nil, invalidField("min_balance_cents", "must not be negative")
	}
	if in.IncomeAccountCode == "" {
		return nil, invalidField("income_account_code", "is required")
	}
	var out *FinanceChargeRules
	err := s.inTx(ctx, func(ctx context.Context) error {
		var one int
		if err := s.ex(ctx).QueryRow(ctx, `SELECT 1 FROM gl_accounts WHERE code = $1`, in.IncomeAccountCode).Scan(&one); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return invalidField("income_account_code", "names no account in the chart of accounts")
			}
			return fmt.Errorf("failed to read the income account: %w", err)
		}
		if _, err := s.ex(ctx).Exec(ctx, `
			UPDATE ar_finance_charge_rules SET monthly_rate_bps = $1, grace_days = $2, min_charge_cents = $3,
				min_balance_cents = $4, compound = $5, income_account_code = $6, updated_by = NULLIF($7, ''), updated_at = NOW()
			WHERE id = (SELECT id FROM ar_finance_charge_rules ORDER BY tenant_id NULLS FIRST LIMIT 1)`,
			in.MonthlyRateBps, in.GraceDays, in.MinChargeCents, in.MinBalanceCents, in.Compound,
			in.IncomeAccountCode, actor); err != nil {
			return fmt.Errorf("failed to write the finance charge rules: %w", err)
		}
		if s.auditLog != nil {
			if err := s.auditLog.Log(ctx, audit.Entry{Action: "finance_charge.rules_updated", EntityType: "ar_finance_charge_rules",
				EntityID: uuid.Nil, UserID: actor, Changes: map[string]interface{}{"monthly_rate_bps": in.MonthlyRateBps,
					"grace_days": in.GraceDays, "min_charge_cents": int64(in.MinChargeCents),
					"min_balance_cents": int64(in.MinBalanceCents), "compound": in.Compound,
					"income_account_code": in.IncomeAccountCode}}); err != nil {
				return fmt.Errorf("failed to write the audit row: %w", err)
			}
		}
		var err error
		out, err = s.GetFinanceChargeRules(ctx)
		return err
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// roundHalfEven is banker's rounding of num over den, both positive: a tie
// goes to the even cent, so a charge of exactly half a cent does not drift
// upward run after run.
func roundHalfEven(num, den int64) int64 {
	q, r := num/den, num%den
	twice := r * 2
	switch {
	case twice < den:
		return q
	case twice > den:
		return q + 1
	default:
		if q%2 == 0 {
			return q
		}
		return q + 1
	}
}

// FinanceChargeRunIn is the body of POST /api/v1/ar/finance-charges/runs.
type FinanceChargeRunIn struct {
	AsOf        time.Time
	DryRun      bool
	CustomerIDs []uuid.UUID
	Actor       string
	Role        string
}

// uuidsOrNil is an empty list as a null array, so a filter that names nobody
// names everybody.
func uuidsOrNil(ids []uuid.UUID) any {
	if len(ids) == 0 {
		return nil
	}
	return ids
}

// FinanceChargeRun is an ar_finance_charge_runs row on the wire.
type FinanceChargeRun struct {
	ID                uuid.UUID           `json:"id"`
	AsOfDate          string              `json:"as_of_date"`
	RateBps           int64               `json:"rate_bps"`
	GraceDays         int64               `json:"grace_days"`
	MinChargeCents    httpx.Cents         `json:"min_charge_cents"`
	Status            string              `json:"status"`
	TotalChargedCents httpx.Cents         `json:"total_charged_cents"`
	CustomerCount     int                 `json:"customer_count"`
	TriggeredBy       *string             `json:"triggered_by"`
	CreatedAt         httpx.Timestamp     `json:"created_at"`
	CompletedAt       *httpx.Timestamp    `json:"completed_at"`
	Items             []FinanceChargeItem `json:"items,omitempty"`
}

// FinanceChargeItem is one customer's charge, linked to the invoice and the
// entry it generated (both null on a dry run's preview item).
type FinanceChargeItem struct {
	ID             uuid.UUID       `json:"id"`
	RunID          uuid.UUID       `json:"run_id"`
	CustomerID     uuid.UUID       `json:"customer_id"`
	CustomerName   string          `json:"customer_name"`
	Currency       string          `json:"currency"`
	OverdueCents   httpx.Cents     `json:"overdue_cents"`
	ChargeCents    httpx.Cents     `json:"charge_cents"`
	InvoiceID      *uuid.UUID      `json:"invoice_id"`
	InvoiceNumber  *string         `json:"invoice_number"`
	JournalEntryID *uuid.UUID      `json:"journal_entry_id"`
	CreatedAt      httpx.Timestamp `json:"created_at"`
}

const financeChargeRunColumns = `r.id, r.as_of_date, r.rate_bps, r.grace_days, r.min_charge_cents, r.status,
	r.total_charged_cents, r.customer_count, r.triggered_by, r.created_at, r.completed_at`

func scanFinanceChargeRun(row pgx.Row, r *FinanceChargeRun) error {
	var asOf, created time.Time
	var completed *time.Time
	if err := row.Scan(&r.ID, &asOf, &r.RateBps, &r.GraceDays, &r.MinChargeCents, &r.Status, &r.TotalChargedCents,
		&r.CustomerCount, &r.TriggeredBy, &created, &completed); err != nil {
		return err
	}
	r.AsOfDate, r.Status = date(asOf), lower(r.Status)
	r.CreatedAt, r.CompletedAt = httpx.TimestampOf(created), httpx.PtrTimestamp(completed)
	return nil
}

const financeChargeItemColumns = `i.id, i.run_id, i.customer_id, c.name, i.currency, i.overdue_cents, i.charge_cents,
	i.invoice_id, inv.number, i.journal_entry_id, i.created_at`

func scanFinanceChargeItem(row pgx.Row, it *FinanceChargeItem) error {
	var created time.Time
	if err := row.Scan(&it.ID, &it.RunID, &it.CustomerID, &it.CustomerName, &it.Currency, &it.OverdueCents,
		&it.ChargeCents, &it.InvoiceID, &it.InvoiceNumber, &it.JournalEntryID, &created); err != nil {
		return err
	}
	it.CreatedAt = httpx.TimestampOf(created)
	return nil
}

// financeChargeBasisSQL answers one row per customer: the sum of the open
// cents of the invoices that are chargeable as of the run's date. An invoice
// is chargeable when it sits more than the grace days past its due date (the
// open amount taken as of that date, the aging's arithmetic) and, without
// compounding, when it is not itself a finance charge invoice. Parameters:
// $1 the as of date, $2 the grace days, $3 the compound flag, $4 the named
// customers (null for every eligible one). A customer a completed run
// already charged as of $1 is not charged twice by the same day's runs.
const financeChargeBasisSQL = `
SELECT i.customer_id, c.name,
       COALESCE(c.currency, (SELECT value FROM system_settings WHERE key = 'currency.default'), 'USD') AS currency,
       COALESCE(c.primary_branch_id, (SELECT value::uuid FROM system_settings WHERE key = 'default_branch_id')) AS branch,
       SUM(i.open_cents) AS overdue
FROM (
	SELECT k.id, k.customer_id, k.due_date,
	       ROUND(k.total_amount * 100)::bigint - COALESCE((SELECT SUM(ROUND(a.amount * 100)::bigint) FROM ar_applications a
	            WHERE a.invoice_id = k.id AND a.applied_on <= $1::date AND (a.reversed_on IS NULL OR a.reversed_on > $1::date)), 0) AS open_cents,
	       k.origin
	FROM invoices k
	WHERE k.status IN ('UNPAID', 'PARTIAL') AND k.invoice_date <= $1::date AND k.due_date IS NOT NULL
) i
JOIN customers c ON c.id = i.customer_id
WHERE i.open_cents > 0 AND ($1::date - i.due_date) > $2::int AND ($3::boolean OR i.origin <> 'FINANCE_CHARGE')
  AND ($4::uuid[] IS NULL OR i.customer_id = ANY($4))
  AND NOT EXISTS (SELECT 1 FROM ar_finance_charge_items fci
        JOIN ar_finance_charge_runs fcr ON fcr.id = fci.run_id
        WHERE fcr.status = 'COMPLETED' AND fcr.as_of_date = $1::date AND fci.customer_id = i.customer_id)
GROUP BY 1, 2, 3, 4
ORDER BY 1`

// RunFinanceCharges previews (dry run) or executes one finance charge run as
// of a date. The named customers, when given, narrow the run to them;
// absent, every eligible customer is charged. Executed, each charged
// customer gets a finance charge invoice, the charge moves the customer's
// balance through the subledger, and the balanced entry posts when a ledger
// is wired.
func (s *Service) RunFinanceCharges(ctx context.Context, in FinanceChargeRunIn) (*FinanceChargeRun, error) {
	if in.Role != "" && !FinanceRole(in.Role) {
		return nil, httpx.Forbidden("running a finance charge run needs the admin, owner or finance role")
	}
	today, err := s.todayInDefaultBranch(ctx)
	if err != nil {
		return nil, err
	}
	if in.AsOf.IsZero() {
		in.AsOf = today
	}
	if date(in.AsOf) > date(today) {
		return nil, invalidField("as_of_date", fmt.Sprintf("has not come yet: a charge is taken no later than today, %s", date(today)))
	}
	rules, err := s.GetFinanceChargeRules(ctx)
	if err != nil {
		return nil, err
	}

	type basisRow struct {
		customerID uuid.UUID
		name       string
		currency   string
		branch     uuid.NullUUID
		overdue    int64
	}
	rows, err := s.ex(ctx).Query(ctx, financeChargeBasisSQL, date(in.AsOf), rules.GraceDays, rules.Compound,
		uuidsOrNil(in.CustomerIDs))
	if err != nil {
		return nil, fmt.Errorf("failed to read the chargeable balances: %w", err)
	}
	defer rows.Close()
	var basis []basisRow
	for rows.Next() {
		var r basisRow
		if err := rows.Scan(&r.customerID, &r.name, &r.currency, &r.branch, &r.overdue); err != nil {
			return nil, fmt.Errorf("failed to scan a chargeable balance: %w", err)
		}
		basis = append(basis, r)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	type planned struct {
		basisRow
		charge int64
	}
	var plans []planned
	for _, b := range basis {
		if b.overdue < int64(rules.MinBalanceCents) {
			continue
		}
		charge := roundHalfEven(b.overdue*rules.MonthlyRateBps, 10_000)
		if charge < int64(rules.MinChargeCents) {
			continue
		}
		plans = append(plans, planned{basisRow: b, charge: charge})
	}

	run := &FinanceChargeRun{RateBps: rules.MonthlyRateBps, GraceDays: rules.GraceDays, MinChargeCents: rules.MinChargeCents}
	err = s.inTx(ctx, func(ctx context.Context) error {
		id := uuid.New()
		status := "RUNNING"
		if in.DryRun {
			status = "DRY_RUN"
		}
		if _, err := s.ex(ctx).Exec(ctx, `
			INSERT INTO ar_finance_charge_runs (id, as_of_date, rate_bps, grace_days, min_charge_cents, status, triggered_by)
			VALUES ($1, $2::date, $3, $4, $5, $6, NULLIF($7, ''))`,
			id, date(in.AsOf), rules.MonthlyRateBps, rules.GraceDays, rules.MinChargeCents, status, in.Actor); err != nil {
			return fmt.Errorf("failed to open the finance charge run: %w", err)
		}
		run.ID = id
		if !in.DryRun {
			fx := &Effects{}
			var total int64
			for _, p := range plans {
				if !p.branch.Valid {
					return conflict("no_branch", fmt.Sprintf("customer %s names no branch: a finance charge invoice needs one", p.customerID))
				}
				invID := uuid.New()
				var number string
				if err := s.ex(ctx).QueryRow(ctx, `
					INSERT INTO invoices (id, order_id, customer_id, branch_id, status, origin, currency, subtotal, tax_rate,
						tax_amount, total_amount, due_date, invoice_date)
					VALUES ($1, NULL, $2, $3, 'UNPAID', 'FINANCE_CHARGE', $4, $5::bigint::numeric / 100, 0, 0,
						$5::bigint::numeric / 100, $6::date + 30, $6::date)
					RETURNING number`,
					invID, p.customerID, p.branch.UUID, p.currency, p.charge, date(in.AsOf)).Scan(&number); err != nil {
					return fmt.Errorf("failed to write the finance charge invoice: %w", err)
				}
				entry, err := s.post(ctx, gl.PostingInput{EntryDate: in.AsOf, Memo: "Finance charge " + number,
					Source: gl.SourceInvoice, SourceRefID: &invID, BranchID: p.branch.UUID, Currency: p.currency, PostedBy: in.Actor, Legs: []gl.Leg{
						{AccountCode: gl.AccountCodeAR, Description: "Accounts Receivable", Debit: p.charge},
						{AccountCode: rules.IncomeAccountCode, Description: "Finance Charge Income", Credit: p.charge},
					}})
				if err != nil {
					return err
				}
				fx.entry(entry)
				if entry != nil {
					if _, err := s.ex(ctx).Exec(ctx, `UPDATE invoices SET gl_entry_id = $2 WHERE id = $1`,
						invID, *entry); err != nil {
						return fmt.Errorf("failed to record the charge entry: %w", err)
					}
				}
				if err := s.subledger(ctx, fx, p.customerID, p.currency, TransactionTypeInvoice, p.charge, invID,
					sourceInvoice, "Finance charge as of "+date(in.AsOf)); err != nil {
					return err
				}
				var entryID *uuid.UUID
				if entry != nil {
					e := *entry
					entryID = &e
				}
				if _, err := s.ex(ctx).Exec(ctx, `
					INSERT INTO ar_finance_charge_items (id, run_id, customer_id, currency, overdue_cents, charge_cents,
						invoice_id, journal_entry_id)
					VALUES ($1, $2, $3, $4, $5, $6, $7, $8)`,
					uuid.New(), id, p.customerID, p.currency, p.overdue, p.charge, invID, entryID); err != nil {
					return fmt.Errorf("failed to record the finance charge item: %w", err)
				}
				total += p.charge
			}
			if err := s.RecordEvents(ctx, fx.Events()); err != nil {
				return err
			}
			if _, err := s.ex(ctx).Exec(ctx, `
				UPDATE ar_finance_charge_runs SET status = 'COMPLETED', total_charged_cents = $2, customer_count = $3,
					completed_at = NOW()
				WHERE id = $1`, id, total, len(plans)); err != nil {
				return fmt.Errorf("failed to close the finance charge run: %w", err)
			}
		} else {
			var total int64
			for _, p := range plans {
				if _, err := s.ex(ctx).Exec(ctx, `
					INSERT INTO ar_finance_charge_items (id, run_id, customer_id, currency, overdue_cents, charge_cents)
					VALUES ($1, $2, $3, $4, $5, $6)`,
					uuid.New(), id, p.customerID, p.currency, p.overdue, p.charge); err != nil {
					return fmt.Errorf("failed to record the finance charge preview: %w", err)
				}
				total += p.charge
			}
			// The preview is a record too: the totals it showed are the ones
			// it keeps, so the run an operator read back is what they read.
			if _, err := s.ex(ctx).Exec(ctx, `
				UPDATE ar_finance_charge_runs SET total_charged_cents = $2, customer_count = $3 WHERE id = $1`,
				id, total, len(plans)); err != nil {
				return fmt.Errorf("failed to record the preview totals: %w", err)
			}
		}
		if s.auditLog != nil {
			if err := s.auditLog.Log(ctx, audit.Entry{Action: "finance_charge.run", EntityType: "ar_finance_charge_run",
				EntityID: id, UserID: in.Actor, Changes: map[string]interface{}{"as_of": date(in.AsOf),
					"dry_run": in.DryRun, "customer_count": len(plans)}}); err != nil {
				return fmt.Errorf("failed to write the audit row: %w", err)
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return s.GetFinanceChargeRun(ctx, run.ID)
}

// FinanceChargeRunFilter is the run list's keyset position.
type FinanceChargeRunFilter struct {
	AfterTime *time.Time
	AfterID   uuid.UUID
	Limit     int
}

// ListFinanceChargeRuns answers one page of runs, newest first.
func (s *Service) ListFinanceChargeRuns(ctx context.Context, f FinanceChargeRunFilter) ([]FinanceChargeRun, error) {
	if f.Limit <= 0 {
		f.Limit = 50
	}
	rows, err := s.ex(ctx).Query(ctx, `
		SELECT `+financeChargeRunColumns+`
		FROM ar_finance_charge_runs r
		WHERE ($2::timestamptz IS NULL OR (r.created_at, r.id) < ($2, $3::uuid))
		ORDER BY r.created_at DESC, r.id DESC
		LIMIT $1`, f.Limit+1, f.AfterTime, f.AfterID)
	if err != nil {
		return nil, fmt.Errorf("failed to list the finance charge runs: %w", err)
	}
	defer rows.Close()
	out := []FinanceChargeRun{}
	for rows.Next() {
		var r FinanceChargeRun
		if err := scanFinanceChargeRun(rows, &r); err != nil {
			return nil, fmt.Errorf("failed to scan a finance charge run: %w", err)
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// CountFinanceChargeRuns is the size of the run list.
func (s *Service) CountFinanceChargeRuns(ctx context.Context) (int64, error) {
	var n int64
	if err := s.ex(ctx).QueryRow(ctx, `SELECT count(*) FROM ar_finance_charge_runs`).Scan(&n); err != nil {
		return 0, fmt.Errorf("failed to count the finance charge runs: %w", err)
	}
	return n, nil
}

// GetFinanceChargeRun reads one run with its items.
func (s *Service) GetFinanceChargeRun(ctx context.Context, id uuid.UUID) (*FinanceChargeRun, error) {
	run := &FinanceChargeRun{}
	err := scanFinanceChargeRun(s.ex(ctx).QueryRow(ctx,
		`SELECT `+financeChargeRunColumns+` FROM ar_finance_charge_runs r WHERE r.id = $1`, id), run)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, httpx.NotFound("finance charge run not found")
	}
	if err != nil {
		return nil, fmt.Errorf("failed to read the finance charge run: %w", err)
	}
	rows, err := s.ex(ctx).Query(ctx, `
		SELECT `+financeChargeItemColumns+`
		FROM ar_finance_charge_items i
		JOIN customers c ON c.id = i.customer_id
		LEFT JOIN invoices inv ON inv.id = i.invoice_id
		WHERE i.run_id = $1
		ORDER BY c.name, i.customer_id, i.id`, id)
	if err != nil {
		return nil, fmt.Errorf("failed to read the finance charge items: %w", err)
	}
	defer rows.Close()
	run.Items = []FinanceChargeItem{}
	for rows.Next() {
		var it FinanceChargeItem
		if err := scanFinanceChargeItem(rows, &it); err != nil {
			return nil, fmt.Errorf("failed to scan a finance charge item: %w", err)
		}
		run.Items = append(run.Items, it)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return run, nil
}
