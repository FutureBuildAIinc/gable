// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package serve

import (
	"net/http"

	"github.com/gablelbm/gable/internal/reporting"
	"github.com/gablelbm/gable/pkg/audit"
	"github.com/gablelbm/gable/pkg/database"
	"github.com/gablelbm/gable/pkg/middleware"
)

// newReportScheduler builds the scheduled report runner serve starts. A run
// the fail closed rule refuses (no creator recorded, a creator holding no
// grant) records an audit row beside its log line, so a schedule that stops
// emailing is discoverable; wiring the logger here is what a test drives.
func newReportScheduler(db *database.DB, svc *reporting.Service, sender reporting.EmailSender) *reporting.Scheduler {
	return reporting.NewScheduler(svc, sender).WithRefusalAudit(audit.NewLogger(db))
}

// wireReportSchedules registers the ad-hoc report builder surface, which
// includes the scheduled-report CRUD routes:
//
//	POST   /api/v1/reporting/schedules
//	GET    /api/v1/reporting/schedules
//	DELETE /api/v1/reporting/schedules/{id}
//	POST   /api/v1/reporting/saved/{id}/run
//
// and attaches the executor that runs stored schedules.
//
// # The executor is the single switch
//
// reporting.Handler.WithScheduleExecutor is the ONLY thing that makes the
// schedule endpoints report execution.enabled = true and write new schedules
// with status ACTIVE, and the handler derives both from whether an executor is
// present rather than from a constant. So passing a live *reporting.Scheduler
// here is what flips the API's account of itself, and passing nil is what
// restores the honest "saved but never run" disclosure. The claim and the
// reality are the same fact read twice; they cannot drift.
//
// main.go passes a scheduler. A composition that wants the routes without a
// runner — a read-only replica, a test — passes nil and gets an API that says
// so on every response.
//
// # What execution actually means
//
// A run decodes the saved report's definition_json, queries Postgres and
// renders the result to CSV (or XLSX). Delivery then goes to whatever
// notification.EmailService main.go wired, which today is the log-only
// LogEmailService — so the report is really generated and the send is really
// recorded, but nothing leaves the process. That distinction is published, not
// buried: the executor describes itself through reporting.DeliveryDescriber and
// the description travels on every schedule response as execution.delivery.
//
// Note also the cron dialect: expressions need SIX fields, seconds first. The
// API rejects five-field crontab strings up front via
// reporting.ValidateCronExpression rather than storing schedules the engine
// would refuse, and advertises the dialect on the read path.
func wireReportSchedules(mux *http.ServeMux, h *reporting.Handler, executor reporting.ScheduleExecutor, extra ...func(http.Handler) http.Handler) {
	if executor != nil {
		h = h.WithScheduleExecutor(executor)
	}
	h.RegisterBuilderRoutes(mux, middleware.Compose(reportScheduleGuard(), middleware.Compose(extra...)))
}

// reportScheduleGuard is the role guard the builder and schedule routes run
// behind. Creating a schedule arranges for financial data to be emailed to
// arbitrary addresses, so it is gated at least as tightly as reading the
// report. This mirrors the guard main.go already passes to
// RegisterBuilderRoutes; it is named here so the two cannot silently diverge.
func reportScheduleGuard() func(http.Handler) http.Handler {
	return middleware.RequireRole("admin", "owner", "finance")
}
