// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package reporting

type DailyTillReport struct {
	Date             string             `json:"date"`
	TotalCollected   float64            `json:"total_collected"`
	ByMethod         map[string]float64 `json:"by_method"`
	TransactionCount int                `json:"transaction_count"`
}

type SalesSummaryReport struct {
	StartDate      string  `json:"start_date"`
	EndDate        string  `json:"end_date"`
	TotalInvoiced  float64 `json:"total_invoiced"`
	TotalCollected float64 `json:"total_collected"`
	OutstandingAR  float64 `json:"outstanding_ar"`
	InvoiceCount   int     `json:"invoice_count"`
}

// AR Aging Report
type ARAgingBucket struct {
	CustomerID   string  `json:"customer_id"`
	CustomerName string  `json:"customer_name"`
	Current      float64 `json:"current"` // 0-30 days
	Days31to60   float64 `json:"days_31_60"`
	Days61to90   float64 `json:"days_61_90"`
	Over90       float64 `json:"over_90"`
	Total        float64 `json:"total"`
}

type ARAgingReport struct {
	AsOfDate     string          `json:"as_of_date"`
	Buckets      []ARAgingBucket `json:"buckets"`
	TotalCurrent float64         `json:"total_current"`
	Total31to60  float64         `json:"total_31_60"`
	Total61to90  float64         `json:"total_61_90"`
	TotalOver90  float64         `json:"total_over_90"`
	GrandTotal   float64         `json:"grand_total"`
}

// Customer Statement
type StatementLine struct {
	Date        string  `json:"date"`
	Type        string  `json:"type"`
	Description string  `json:"description"`
	Debit       float64 `json:"debit"`
	Credit      float64 `json:"credit"`
	Balance     float64 `json:"balance"`
}

type CustomerStatement struct {
	CustomerID   string          `json:"customer_id"`
	CustomerName string          `json:"customer_name"`
	StartDate    string          `json:"start_date"`
	EndDate      string          `json:"end_date"`
	OpenBalance  float64         `json:"open_balance"`
	CloseBalance float64         `json:"close_balance"`
	Lines        []StatementLine `json:"lines"`
}

// Ad-Hoc Report Builder Models
type SavedReport struct {
	ID             string                 `json:"id"`
	Name           string                 `json:"name"`
	Description    string                 `json:"description"`
	EntityType     string                 `json:"entity_type"`
	DefinitionJSON map[string]interface{} `json:"definition_json"`
	CreatedBy      string                 `json:"created_by"`
	CreatedAt      string                 `json:"created_at"`
	UpdatedAt      string                 `json:"updated_at"`
}

// ScheduleStatusStored is the status every schedule created through the API
// receives. It means "persisted, but nothing executes it".
//
// It is deliberately NOT "ACTIVE": nothing in this repository runs scheduled
// reports (see ScheduleExecution), and Scheduler.Start only registers rows
// whose status is "ACTIVE". Writing "STORED" therefore both tells the truth to
// anyone reading the table and guarantees these rows cannot start firing by
// accident if the scheduler is wired up before it is fixed.
const ScheduleStatusStored = "STORED"

// ScheduleStatusActive is the status a schedule would carry once a working
// executor is attached to the handler (Handler.WithScheduleExecutor).
const ScheduleStatusActive = "ACTIVE"

type ReportSchedule struct {
	ID             string   `json:"id"`
	ReportID       string   `json:"report_id"`
	CronExpression string   `json:"cron_expression"`
	Recipients     []string `json:"recipients"`
	Status         string   `json:"status"`
	Format         string   `json:"format"`
	LastRunAt      *string  `json:"last_run_at,omitempty"`
	NextRunAt      *string  `json:"next_run_at,omitempty"`
	CreatedAt      string   `json:"created_at"`
	UpdatedAt      string   `json:"updated_at"`
}

// ScheduleExecution tells a client whether stored schedules actually run.
//
// This exists because the honest answer today is "no", and an API that
// accepted a schedule, returned 201 and said nothing would let an operator
// believe a financial report is being emailed to their controller every
// Monday when in fact nothing will ever fire. Every schedule response carries
// this block.
type ScheduleExecution struct {
	// Enabled reports whether an executor is attached to the handler.
	Enabled bool `json:"enabled"`
	// Summary is a one-line, human-readable statement of the above, suitable
	// for display verbatim in a UI.
	Summary string `json:"summary"`
	// Blockers lists what must be fixed before Enabled can be true. Empty when
	// execution is enabled.
	Blockers []string `json:"blockers,omitempty"`
	// CronDialect states the expression grammar POST accepts.
	//
	// It is advertised here because the grammar is surprising and the shared
	// error envelope cannot explain it: pkg/httputil.RespondError deliberately
	// replaces every 4xx message with a generic "Bad Request" so internal
	// details cannot leak, so a rejected expression comes back with no reason
	// attached. Publishing the rule on the read path lets a client get it
	// right the first time instead of guessing after a 400.
	CronDialect string `json:"cron_dialect"`
}

// cronDialectDescription is the six-field grammar cron.WithSeconds() imposes.
// Every crontab and every "0 8 * * *" example online is five fields and is
// rejected.
const cronDialectDescription = `Six fields, seconds first: "second minute hour day-of-month month day-of-week" (e.g. "0 0 9 * * *" for 09:00 daily). Five-field crontab expressions are rejected. Descriptors such as @daily and @every 1h are accepted.`

// scheduleExecutionDisabled is the truthful state of scheduled reporting in
// this repository. The three blockers are the full list from the Scheduler doc
// comment in scheduler.go; all three must be closed before schedules run.
func scheduleExecutionDisabled() ScheduleExecution {
	return ScheduleExecution{
		Enabled: false,
		Summary: "Schedules are saved but never run: scheduled report delivery is not enabled in this deployment.",
		Blockers: []string{
			"reporting.Scheduler is not wired into the server; nothing loads or fires schedules.",
			"Scheduler.ExecuteAndSendReport never decodes the saved report's definition_json, so it would execute an empty report definition.",
			"No EmailSender implementation exists, so a generated report has nowhere to be delivered.",
		},
		CronDialect: cronDialectDescription,
	}
}

func scheduleExecutionEnabled() ScheduleExecution {
	return ScheduleExecution{
		Enabled:     true,
		Summary:     "Schedules run on the server's cron engine and are emailed to their recipients.",
		CronDialect: cronDialectDescription,
	}
}

// ReportScheduleResponse is the body returned by POST
// /api/v1/reporting/schedules. The Execution block is not decoration: it is the
// difference between an API that stores a schedule and one that runs it.
type ReportScheduleResponse struct {
	Schedule  ReportSchedule    `json:"schedule"`
	Execution ScheduleExecution `json:"execution"`
}

// ReportScheduleListResponse is the body returned by GET
// /api/v1/reporting/schedules.
type ReportScheduleListResponse struct {
	Schedules []ReportSchedule  `json:"schedules"`
	Execution ScheduleExecution `json:"execution"`
}

type ReportDefinition struct {
	Columns   []ReportColumn   `json:"columns"`
	Filters   []ReportFilter   `json:"filters"`
	Groupings []ReportGrouping `json:"groupings"`
}

type ReportColumn struct {
	Field       string `json:"field"`
	Label       string `json:"label"`
	Aggregation string `json:"aggregation,omitempty"` // SUM, COUNT, AVG, etc.
}

type ReportFilter struct {
	Field    string      `json:"field"`
	Operator string      `json:"operator"` // =, !=, >, <, IN, LIKE, etc.
	Value    interface{} `json:"value"`
}

type ReportGrouping struct {
	Field string `json:"field"`
}
