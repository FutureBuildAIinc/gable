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

// ScheduleStatusStored means "persisted, but nothing executes it".
//
// It is what the API writes when no executor is attached to the handler, and it
// is what every row created before scheduled delivery was implemented carries.
// Scheduler.Start registers ACTIVE rows only, so a STORED row does not begin
// firing when the server is upgraded — an operator who was told a schedule
// would never run does not get a surprise email run. Recreating the schedule
// through the API writes it ACTIVE.
const ScheduleStatusStored = "STORED"

// ScheduleStatusActive is the status a schedule carries when a working executor
// is attached to the handler (Handler.WithScheduleExecutor), which is the state
// cmd/server/main.go wires. Scheduler.Start registers exactly these rows.
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

// ScheduleExecution tells a client whether stored schedules actually run, and
// what "run" means in this deployment.
//
// It exists because an API that accepts a schedule, returns 201 and says
// nothing would let an operator believe a financial report is being emailed to
// their controller every Monday. Every schedule response carries this block,
// and every field in it is derived at request time from what is really wired —
// nothing here is a constant someone has to remember to update.
type ScheduleExecution struct {
	// Enabled reports whether an executor is attached to the handler.
	Enabled bool `json:"enabled"`
	// Summary is a one-line, human-readable statement of the above, suitable
	// for display verbatim in a UI.
	Summary string `json:"summary"`
	// Blockers lists what must be fixed before Enabled can be true. Empty when
	// execution is enabled.
	Blockers []string `json:"blockers,omitempty"`
	// Delivery describes what actually happens to a generated report, in the
	// executor's own words (reporting.DeliveryDescriber). Present only when
	// Enabled.
	//
	// "The schedule runs" and "the report is delivered" are two different
	// claims, and this deployment can honestly make only the first: the server's
	// email service is log-only, so a run produces a real CSV and records the
	// send rather than transmitting it. Collapsing that into Enabled=true would
	// reintroduce, one step later, exactly the false confidence this block
	// exists to prevent.
	Delivery string `json:"delivery,omitempty"`
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

// scheduleExecutionDisabled describes a deployment with no executor attached to
// the handler.
//
// cmd/server/main.go does attach one, so this is not the state of the shipped
// server. It remains reachable — and remains the honest answer — for any
// composition that registers the schedule routes without a running scheduler,
// which is precisely why Handler.execution derives the choice from the executor
// rather than from a build-time constant.
func scheduleExecutionDisabled() ScheduleExecution {
	return ScheduleExecution{
		Enabled: false,
		Summary: "Schedules are saved but never run: no scheduled-report executor is attached in this deployment.",
		Blockers: []string{
			"No reporting.Scheduler is attached to the schedule handler, so nothing loads or fires stored schedules.",
		},
		CronDialect: cronDialectDescription,
	}
}

// scheduleExecutionEnabled describes a deployment with an executor attached.
//
// The summary claims only what the executor can actually do — run the schedule
// and produce the report. What happens to the finished file is the executor's
// own statement, carried separately in Delivery, because a log-only email
// service and a real SMTP one are both "enabled" and an operator needs to know
// which one they have.
func scheduleExecutionEnabled(delivery string) ScheduleExecution {
	return ScheduleExecution{
		Enabled:     true,
		Summary:     "Schedules run on the server's cron engine: each run executes the saved report and renders it to CSV.",
		Delivery:    delivery,
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
