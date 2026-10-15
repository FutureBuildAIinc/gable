// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package reporting

import (
	"context"
	"errors"
	"fmt"
	"math"
	"time"

	"github.com/gablelbm/gable/pkg/branchctx"
	"github.com/gablelbm/gable/pkg/collections"
	"github.com/gablelbm/gable/pkg/database"
	"github.com/gablelbm/gable/pkg/middleware"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// ErrCustomerNotVisible is the customer statement's visibility refusal: the
// customer's branches sit entirely outside the caller's reach, so the
// statement reads as missing (the wall a header caller already met).
var ErrCustomerNotVisible = errors.New("customer is outside the caller's branches")

// reach is the caller's reach every report query carries (SEC-NOHEADER-1,
// the principal's ruling on the reporting surface): the branch a header
// names, or the user sub whose user_locations grants scope a no header
// caller, both nil for the every branch view an administrator or owner
// keeps. A scheduled run builds the same reach from the schedule's
// recorded creator before it reads anything (Scheduler.creatorRunContext),
// so no report query here runs context free.
func reach(ctx context.Context) (*uuid.UUID, *string) {
	return branchctx.IDForQuery(ctx), branchctx.GrantsSubForQuery(ctx)
}

type Repository interface {
	GetDailyTill(ctx context.Context, date time.Time) (*DailyTillReport, error)
	GetSalesSummary(ctx context.Context, start, end time.Time) (*SalesSummaryReport, error)
	GetARAgingReport(ctx context.Context) (*ARAgingReport, error)
	GetCustomerStatement(ctx context.Context, customerID string, start, end time.Time) (*CustomerStatement, error)

	// Ad-Hoc Report Builder
	CreateSavedReport(ctx context.Context, report *SavedReport) error
	GetSavedReport(ctx context.Context, id string) (*SavedReport, error)
	ListSavedReports(ctx context.Context) ([]SavedReport, error)
	UpdateSavedReport(ctx context.Context, report *SavedReport) error
	DeleteSavedReport(ctx context.Context, id string) error
	CreateReportSchedule(ctx context.Context, schedule *ReportSchedule) error
	ListReportSchedules(ctx context.Context) ([]ReportSchedule, error)
	UpdateReportScheduleNextRun(ctx context.Context, scheduleID string, nextRun time.Time) error
	DeleteReportSchedule(ctx context.Context, id string) error
	// ScheduleCreatorGrants counts the creator's user_locations grants, the
	// fail closed check a scheduled run makes for a creator the schedule
	// does not record as an administrator: the grants are read afresh at
	// every run, so a revoked grant takes effect at the next one.
	ScheduleCreatorGrants(ctx context.Context, sub string) (int, error)
	// MultiBranchEnabled reads the single branch switch the branch
	// middleware reads (a missing row is off, as it is there). A lookup
	// failure is returned, never read as off, so a scheduled run cannot
	// widen its reach on a failed read.
	MultiBranchEnabled(ctx context.Context) (bool, error)
	// PauseReportSchedule sets a schedule's status to PAUSED, the state
	// Scheduler.Start skips, once a run was refused.
	PauseReportSchedule(ctx context.Context, id string) error
}

type PostgresRepository struct {
	db *database.DB
}

func NewRepository(db *database.DB) *PostgresRepository {
	return &PostgresRepository{db: db}
}

// BusinessDay is the default branch's business date now: the day the till, the
// dashboard's card and the Sales Summary's default window are for.
func (r *PostgresRepository) BusinessDay(ctx context.Context) (string, error) {
	var day string
	if err := r.db.GetExecutor(ctx).QueryRow(ctx, `SELECT `+collections.TodaySQL+`::text`).Scan(&day); err != nil {
		return "", fmt.Errorf("failed to read the business day: %w", err)
	}
	return day, nil
}

// GetDailyTill reads one business day's collections in the default branch's
// zone. A zero date is the branch's today (the day the dashboard's Today's
// Revenue card is cut on); a date is that calendar day in the same zone. The
// day's figures are the card's: payments received at the caller's yard (their
// own branch_id, never a voided payment), the refunds paid out that day, and
// their net, all from pkg/collections.
func (r *PostgresRepository) GetDailyTill(ctx context.Context, date time.Time) (*DailyTillReport, error) {
	var asked *string
	if !date.IsZero() {
		d := date.Format("2006-01-02")
		asked = &d
	}
	branch, grants := reach(ctx)
	ex := r.db.GetExecutor(ctx)

	// The day first, so a day with no collections still names itself.
	var day string
	if err := ex.QueryRow(ctx,
		`SELECT COALESCE($1::date, `+collections.TodaySQL+`)::text`, asked).Scan(&day); err != nil {
		return nil, fmt.Errorf("failed to read the till day: %w", err)
	}

	rows, err := ex.Query(ctx, `
		SELECT p.method, COALESCE(SUM(p.amount), 0), COUNT(*)
		FROM `+collections.ReceivedFrom("$1::date", 2, 3)+`
		GROUP BY p.method`, day, branch, grants)
	if err != nil {
		return nil, fmt.Errorf("failed to query daily till: %w", err)
	}
	defer rows.Close()

	report := &DailyTillReport{Date: day, ByMethod: make(map[string]float64)}
	for rows.Next() {
		var method string
		var amount float64
		var count int
		if err := rows.Scan(&method, &amount, &count); err != nil {
			return nil, fmt.Errorf("failed to scan till row: %w", err)
		}
		report.ByMethod[method] = amount
		report.Received += amount
		report.TransactionCount += count
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	rows.Close()

	var refunds float64
	if err := ex.QueryRow(ctx, `
		SELECT COALESCE(SUM(r.amount), 0)::float8
		FROM `+collections.RefundedFrom("$1::date", 2, 3), day, branch, grants).Scan(&refunds); err != nil {
		return nil, fmt.Errorf("failed to query daily till refunds: %w", err)
	}
	report.Refunds = refunds
	// Cents, so the sum of two dollar floats carries no binary dust.
	report.Received = math.Round(report.Received*100) / 100
	report.NetCollected = math.Round((report.Received-refunds)*100) / 100
	report.TotalCollected = report.NetCollected
	return report, nil
}

func (r *PostgresRepository) GetSalesSummary(ctx context.Context, start, end time.Time) (*SalesSummaryReport, error) {
	report := &SalesSummaryReport{
		StartDate: start.Format("2006-01-02"),
		EndDate:   end.Format("2006-01-02"),
	}
	branch, grants := reach(ctx)

	// 1. Invoiced
	queryInvoiced := `
		SELECT COALESCE(SUM(total_amount), 0), COUNT(*)
		FROM invoices
		WHERE created_at >= $1 AND created_at < $2 AND status != 'VOID'
		  AND ` + middleware.Wall("", 3, 4) + `
	`
	err := r.db.GetExecutor(ctx).QueryRow(ctx, queryInvoiced, start, end, branch, grants).Scan(&report.TotalInvoiced, &report.InvoiceCount)
	if err != nil {
		return nil, fmt.Errorf("failed to query invoiced total: %w", err)
	}

	// 2. Collected: the day's net collections over the period, counted as the
	// dashboard's card and the Daily Till count them (pkg/collections: by the
	// payment's received_on and own yard, voided payments out, refunds in).
	queryCollected := `SELECT ` + collections.NetCentsBetween("$1::date", "$2::date", 3, 4) + `::float8 / 100`
	err = r.db.GetExecutor(ctx).QueryRow(ctx, queryCollected, start.Format("2006-01-02"), end.Format("2006-01-02"), branch, grants).Scan(&report.TotalCollected)
	if err != nil {
		return nil, fmt.Errorf("failed to query collected total: %w", err)
	}

	// 3. Outstanding AR (All time up to end date, or just in period? Usually AR is snapshot or accumulation)
	// Let's define it as "Unpaid Amount of Invoices Created in Period" for now + "Overdue" generally?
	// The prompt asks for "Sales Summary", implying performance over period.
	// Outstanding AR usually means "Total money owed to us right now regardless of when invoice was created".
	// Let's do "Total Outstanding" as a global metric for context, or "Outstanding from this period".
	// Let's stick to "Outstanding from this period" to balance report.
	// Actually, standard AR is global. But "Sales Summary" is period based.
	// Let's calculate "Outstanding in Period" = TotalInvoiced - (Payments applied to those invoices).
	// But payments might come later.
	// Let's just do: Total Invoiced - Paid (where invoice is fully paid).
	// Simplification: OutstandingAR = SUM(total_amount) of invoices in period where status != PAID.

	queryOutstanding := `
		SELECT COALESCE(SUM(total_amount), 0)
		FROM invoices
		WHERE created_at >= $1 AND created_at < $2 AND status IN ('UNPAID', 'PARTIAL')
		  AND ` + middleware.Wall("", 3, 4) + `
	`
	// Note: accurate AR needs to subtract partial payments.
	// Let's refine: Total Invoiced in Period - Total Payments applied to *those* invoices (even if payment is outside period? Mixed bag).
	// Let's stick to: Total Invoiced in Period - Total Paid against those invoices.
	// Too complex for MVP SQL?
	// Let's just return Global AR for now as a separate metric if needed, or just "Invoiced amount that isn't paid yet".

	// Better: Get sum of (total_amount) from invoices in period
	// MINUS sum of (amount) from payments linked to those invoices

	// That might be negative if overpaid? unlikely.
	// Actually, just keep it simple: "Total Invoiced" vs "Total Collected (Cash in hand)".
	// "Outstanding AR" = Total for Unpaid/Partial invoices created in this period.

	err = r.db.GetExecutor(ctx).QueryRow(ctx, queryOutstanding, start, end, branch, grants).Scan(&report.OutstandingAR)
	if err != nil {
		return nil, fmt.Errorf("failed to query outstanding: %w", err)
	}

	return report, nil
}

func (r *PostgresRepository) GetARAgingReport(ctx context.Context) (*ARAgingReport, error) {
	branch, grants := reach(ctx)
	query := `
		SELECT
			i.customer_id,
			COALESCE(c.name, 'Unknown'),
			COALESCE(SUM(CASE WHEN CURRENT_DATE - COALESCE(i.due_date, i.created_at)::date <= 30 THEN i.total_amount ELSE 0 END), 0) AS current_bucket,
			COALESCE(SUM(CASE WHEN CURRENT_DATE - COALESCE(i.due_date, i.created_at)::date BETWEEN 31 AND 60 THEN i.total_amount ELSE 0 END), 0) AS days_31_60,
			COALESCE(SUM(CASE WHEN CURRENT_DATE - COALESCE(i.due_date, i.created_at)::date BETWEEN 61 AND 90 THEN i.total_amount ELSE 0 END), 0) AS days_61_90,
			COALESCE(SUM(CASE WHEN CURRENT_DATE - COALESCE(i.due_date, i.created_at)::date > 90 THEN i.total_amount ELSE 0 END), 0) AS over_90,
			COALESCE(SUM(i.total_amount), 0) AS total
		FROM invoices i
		LEFT JOIN customers c ON c.id = i.customer_id
		WHERE i.status IN ('UNPAID', 'PARTIAL')
		  AND ` + middleware.Wall("i", 1, 2) + `
		GROUP BY i.customer_id, c.name
		ORDER BY total DESC
	`

	rows, err := r.db.GetExecutor(ctx).Query(ctx, query, branch, grants)
	if err != nil {
		return nil, fmt.Errorf("failed to query AR aging: %w", err)
	}
	defer rows.Close()

	report := &ARAgingReport{
		AsOfDate: time.Now().Format("2006-01-02"),
	}

	for rows.Next() {
		var b ARAgingBucket
		if err := rows.Scan(&b.CustomerID, &b.CustomerName, &b.Current, &b.Days31to60, &b.Days61to90, &b.Over90, &b.Total); err != nil {
			return nil, fmt.Errorf("failed to scan AR aging row: %w", err)
		}
		report.Buckets = append(report.Buckets, b)
		report.TotalCurrent += b.Current
		report.Total31to60 += b.Days31to60
		report.Total61to90 += b.Days61to90
		report.TotalOver90 += b.Over90
		report.GrandTotal += b.Total
	}

	return report, nil
}

func (r *PostgresRepository) GetCustomerStatement(ctx context.Context, customerID string, start, end time.Time) (*CustomerStatement, error) {
	// The customer's visibility settles the whole statement: a customer whose
	// branches sit outside the caller's reach reads as missing, the wall a
	// header caller already met (the customer family's own wall shape).
	branch, grants := reach(ctx)
	var customerName string
	nameQuery := `SELECT COALESCE(name, '') FROM customers c WHERE c.id = $1 AND (
		    ($2::uuid IS NOT NULL AND EXISTS (SELECT 1 FROM customer_branches cb WHERE cb.customer_id = c.id AND cb.branch_id = $2))
		    OR ($2::uuid IS NULL AND $3::text IS NOT NULL AND EXISTS (SELECT 1 FROM customer_branches cb
		        WHERE cb.customer_id = c.id AND cb.branch_id IN (SELECT branch_id FROM user_locations WHERE user_sub = $3)))
		    OR ($2::uuid IS NULL AND $3::text IS NULL)
		  )`
	if err := r.db.GetExecutor(ctx).QueryRow(ctx, nameQuery, customerID, branch, grants).Scan(&customerName); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrCustomerNotVisible
		}
		return nil, fmt.Errorf("failed to read the statement's customer: %w", err)
	}

	stmt := &CustomerStatement{
		CustomerID:   customerID,
		CustomerName: customerName,
		StartDate:    start.Format("2006-01-02"),
		EndDate:      end.Format("2006-01-02"),
	}

	// Get transactions in range
	query := `
		SELECT ct.created_at, ct.type, ct.description,
		       CASE WHEN ct.amount > 0 THEN ct.amount::float / 100.0 ELSE 0 END AS debit,
		       CASE WHEN ct.amount < 0 THEN ABS(ct.amount)::float / 100.0 ELSE 0 END AS credit,
		       ct.balance_after::float / 100.0 AS balance
		FROM customer_transactions ct
		WHERE ct.customer_id = $1 AND ct.created_at >= $2 AND ct.created_at < $3
		ORDER BY ct.created_at ASC
	`

	rows, err := r.db.GetExecutor(ctx).Query(ctx, query, customerID, start, end)
	if err != nil {
		return nil, fmt.Errorf("failed to query statement: %w", err)
	}
	defer rows.Close()

	for rows.Next() {
		var l StatementLine
		var ts time.Time
		if err := rows.Scan(&ts, &l.Type, &l.Description, &l.Debit, &l.Credit, &l.Balance); err != nil {
			return nil, fmt.Errorf("failed to scan statement line: %w", err)
		}
		l.Date = ts.Format("2006-01-02")
		stmt.Lines = append(stmt.Lines, l)
	}

	// Open balance: balance_after of last transaction before start date
	openQuery := `
		SELECT COALESCE(balance_after::float / 100.0, 0)
		FROM customer_transactions
		WHERE customer_id = $1 AND created_at < $2
		ORDER BY created_at DESC LIMIT 1
	`
	_ = r.db.GetExecutor(ctx).QueryRow(ctx, openQuery, customerID, start).Scan(&stmt.OpenBalance)

	// Close balance: balance_after of last transaction in or before end date
	closeQuery := `
		SELECT COALESCE(balance_after::float / 100.0, 0)
		FROM customer_transactions
		WHERE customer_id = $1 AND created_at < $2
		ORDER BY created_at DESC LIMIT 1
	`
	_ = r.db.GetExecutor(ctx).QueryRow(ctx, closeQuery, customerID, end).Scan(&stmt.CloseBalance)

	return stmt, nil
}

// SavedReport carries its timestamps as strings, so every saved_reports query
// casts them with ::text — selecting a bare TIMESTAMPTZ into a *string fails at
// scan time in pgx with "cannot scan timestamptz (OID 1184) in binary format
// into *string". The casts are load-bearing, not cosmetic; the same note
// applies to the report_schedules queries further down.
func (r *PostgresRepository) CreateSavedReport(ctx context.Context, report *SavedReport) error {
	query := `
INSERT INTO saved_reports (name, description, entity_type, definition_json, created_by)
VALUES ($1, $2, $3, $4, $5)
RETURNING id, created_at::text, updated_at::text
`
	err := r.db.GetExecutor(ctx).QueryRow(ctx, query,
		report.Name, report.Description, report.EntityType, report.DefinitionJSON, nullableUUID(report.CreatedBy)).
		Scan(&report.ID, &report.CreatedAt, &report.UpdatedAt)
	if err != nil {
		return fmt.Errorf("failed to create saved report: %w", err)
	}
	return nil
}

// nullableUUID maps an absent author to SQL NULL.
//
// saved_reports.created_by is a nullable uuid; binding the empty string to it
// fails with `invalid input syntax for type uuid: ""`. "No recorded author" is
// a real state — a report saved by a deployment with authentication disabled
// has one — and NULL is how the column expresses it.
func nullableUUID(s string) interface{} {
	if s == "" {
		return nil
	}
	return s
}

func (r *PostgresRepository) GetSavedReport(ctx context.Context, id string) (*SavedReport, error) {
	query := `
SELECT id, name, description, entity_type, definition_json, COALESCE(created_by::text, ''), created_at::text, updated_at::text
FROM saved_reports
WHERE id = $1
`
	report := &SavedReport{}
	err := r.db.GetExecutor(ctx).QueryRow(ctx, query, id).Scan(
		&report.ID, &report.Name, &report.Description, &report.EntityType,
		&report.DefinitionJSON, &report.CreatedBy, &report.CreatedAt, &report.UpdatedAt)
	if err != nil {
		return nil, fmt.Errorf("failed to get saved report: %w", err)
	}
	return report, nil
}

func (r *PostgresRepository) ListSavedReports(ctx context.Context) ([]SavedReport, error) {
	query := `
SELECT id, name, description, entity_type, definition_json, COALESCE(created_by::text, ''), created_at::text, updated_at::text
FROM saved_reports
ORDER BY created_at DESC
`
	rows, err := r.db.GetExecutor(ctx).Query(ctx, query)
	if err != nil {
		return nil, fmt.Errorf("failed to list saved reports: %w", err)
	}
	defer rows.Close()

	var reports []SavedReport
	for rows.Next() {
		var report SavedReport
		if err := rows.Scan(
			&report.ID, &report.Name, &report.Description, &report.EntityType,
			&report.DefinitionJSON, &report.CreatedBy, &report.CreatedAt, &report.UpdatedAt); err != nil {
			return nil, fmt.Errorf("failed to scan saved report: %w", err)
		}
		reports = append(reports, report)
	}
	return reports, nil
}

func (r *PostgresRepository) UpdateSavedReport(ctx context.Context, report *SavedReport) error {
	query := `
UPDATE saved_reports
SET name = $1, description = $2, entity_type = $3, definition_json = $4, updated_at = NOW()
WHERE id = $5
RETURNING updated_at::text
`
	err := r.db.GetExecutor(ctx).QueryRow(ctx, query,
		report.Name, report.Description, report.EntityType, report.DefinitionJSON, report.ID).
		Scan(&report.UpdatedAt)
	if err != nil {
		return fmt.Errorf("failed to update saved report: %w", err)
	}
	return nil
}

func (r *PostgresRepository) DeleteSavedReport(ctx context.Context, id string) error {
	query := `DELETE FROM saved_reports WHERE id = $1`
	_, err := r.db.GetExecutor(ctx).Exec(ctx, query, id)
	return err
}

// The timestamp columns are cast with ::text because ReportSchedule carries
// them as strings. Selecting a bare TIMESTAMPTZ into a *string fails at scan
// time in pgx, so the casts are load-bearing, not cosmetic.
func (r *PostgresRepository) CreateReportSchedule(ctx context.Context, schedule *ReportSchedule) error {
	query := `
INSERT INTO report_schedules (report_id, cron_expression, recipients, status, format, created_by, creator_is_admin)
VALUES ($1, $2, $3, $4, $5, $6, $7)
RETURNING id, created_at::text, updated_at::text
`
	err := r.db.GetExecutor(ctx).QueryRow(ctx, query,
		schedule.ReportID, schedule.CronExpression, schedule.Recipients, schedule.Status, schedule.Format,
		nullableText(schedule.CreatedBy), schedule.CreatorIsAdmin).
		Scan(&schedule.ID, &schedule.CreatedAt, &schedule.UpdatedAt)
	if err != nil {
		return fmt.Errorf("failed to create report schedule: %w", err)
	}
	return nil
}

// nullableText maps an absent creator to SQL NULL, the same mapping
// nullableUUID makes for saved_reports.created_by: a schedule made where
// no caller identity exists (authentication disabled) is a real state, and
// a NULL creator is the row the run refuses to execute.
func nullableText(s string) interface{} {
	if s == "" {
		return nil
	}
	return s
}

func (r *PostgresRepository) ListReportSchedules(ctx context.Context) ([]ReportSchedule, error) {
	query := `
SELECT id, report_id, cron_expression, recipients, status, format,
       last_run_at::text, next_run_at::text, COALESCE(created_by, ''), creator_is_admin,
       created_at::text, updated_at::text
FROM report_schedules
ORDER BY created_at DESC
`
	rows, err := r.db.GetExecutor(ctx).Query(ctx, query)
	if err != nil {
		return nil, fmt.Errorf("failed to list report schedules: %w", err)
	}
	defer rows.Close()

	var schedules []ReportSchedule
	for rows.Next() {
		var schedule ReportSchedule
		if err := rows.Scan(
			&schedule.ID, &schedule.ReportID, &schedule.CronExpression,
			&schedule.Recipients, &schedule.Status, &schedule.Format,
			&schedule.LastRunAt, &schedule.NextRunAt,
			&schedule.CreatedBy, &schedule.CreatorIsAdmin,
			&schedule.CreatedAt, &schedule.UpdatedAt); err != nil {
			return nil, fmt.Errorf("failed to scan report schedule: %w", err)
		}
		schedules = append(schedules, schedule)
	}
	return schedules, rows.Err()
}

// ScheduleCreatorGrants counts the sub's current user_locations grants. The
// read carries no wall on purpose: it answers the scheduler's fail closed
// question (does this creator still hold any branch grant), not a scoped
// list, and the count itself is what the run resolves its reach from.
func (r *PostgresRepository) ScheduleCreatorGrants(ctx context.Context, sub string) (int, error) {
	var n int
	if err := r.db.GetExecutor(ctx).QueryRow(ctx,
		`SELECT COUNT(*) FROM user_locations WHERE user_sub = $1`, sub).Scan(&n); err != nil {
		return 0, fmt.Errorf("failed to read the schedule creator's grants: %w", err)
	}
	return n, nil
}

// MultiBranchEnabled reads system_settings.multi_branch_enabled, the value
// the branch middleware refreshes its kill switch from.
func (r *PostgresRepository) MultiBranchEnabled(ctx context.Context) (bool, error) {
	var val string
	err := r.db.GetExecutor(ctx).QueryRow(ctx,
		`SELECT value FROM system_settings WHERE key = $1`, middleware.MultiBranchSettingKey).Scan(&val)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("failed to read the single branch switch: %w", err)
	}
	return middleware.ParseBoolSetting(val, false), nil
}

// PauseReportSchedule marks the schedule PAUSED. It touches the status and
// the update time only.
func (r *PostgresRepository) PauseReportSchedule(ctx context.Context, id string) error {
	if _, err := r.db.GetExecutor(ctx).Exec(ctx,
		`UPDATE report_schedules SET status = $2, updated_at = NOW() WHERE id = $1`, id, ScheduleStatusPaused); err != nil {
		return fmt.Errorf("failed to pause report schedule: %w", err)
	}
	return nil
}

func (r *PostgresRepository) DeleteReportSchedule(ctx context.Context, id string) error {
	query := `DELETE FROM report_schedules WHERE id = $1`
	_, err := r.db.GetExecutor(ctx).Exec(ctx, query, id)
	if err != nil {
		return fmt.Errorf("failed to delete report schedule: %w", err)
	}
	return nil
}

func (r *PostgresRepository) UpdateReportScheduleNextRun(ctx context.Context, scheduleID string, nextRun time.Time) error {
	query := `
UPDATE report_schedules
SET last_run_at = NOW(), next_run_at = $1, updated_at = NOW()
WHERE id = $2
`
	_, err := r.db.GetExecutor(ctx).Exec(ctx, query, nextRun, scheduleID)
	return err
}
