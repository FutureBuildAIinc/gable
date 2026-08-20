// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package main

import (
	"net/http"

	"github.com/gablelbm/gable/internal/reporting"
	"github.com/gablelbm/gable/pkg/middleware"
)

// wireReportSchedules registers the ad-hoc report builder surface, which now
// includes the scheduled-report CRUD routes:
//
//	POST   /api/v1/reporting/schedules
//	GET    /api/v1/reporting/schedules
//	DELETE /api/v1/reporting/schedules/{id}
//	POST   /api/v1/reporting/saved/{id}/run
//
// It is exactly equivalent to the RegisterBuilderRoutes call main.go already
// makes, because the four new routes were added to that method. main.go
// therefore needs NO change for these endpoints to be served; this function
// exists so the schedule wiring, its role guard and the scheduler decision
// below have one obvious home, and so wire_schedules_test.go can pin the
// surface without importing main's initializer.
//
// # THE CRON SCHEDULER IS DELIBERATELY NOT STARTED
//
// reporting.Scheduler is NOT constructed here and NOT started. Schedules are
// created, listed and deleted; nothing executes them. Three things block
// execution, and all three must be fixed together — closing any one alone
// still yields a scheduler that fails silently on a timer:
//
//  1. Scheduler.ExecuteAndSendReport declares `var def ReportDefinition` and
//     never populates it from the saved report's DefinitionJSON, so every run
//     executes an empty definition and dies at "no columns selected".
//     reporting.definitionFromSaved — the decode the working
//     POST /api/v1/reporting/saved/{id}/run endpoint performs — is what it is
//     missing.
//  2. No reporting.EmailSender implementation exists anywhere in this
//     repository. notification.LogEmailService has SendInvoice and
//     SendDeliveryNotification, not SendEmailWithAttachment; the only
//     implementer is a test fake, and there is no SMTP configuration either.
//  3. NewScheduler uses cron.WithSeconds(), so expressions need six fields.
//     The API now rejects five-field crontab strings up front via
//     reporting.ValidateCronExpression rather than storing schedules that
//     could never register, and advertises the dialect on the read path.
//
// Starting a scheduler in that state would schedule guaranteed failures and
// log them where nobody looks, while the UI showed "ACTIVE". So the API tells
// the truth instead: schedules are persisted with status STORED, and every
// schedule response carries an `execution` block with enabled=false and the
// blocker list above (reporting.ScheduleExecution).
//
// TO ENABLE EXECUTION once all three are closed, add to this function:
//
//	sched := reporting.NewScheduler(reportingSvc, emailSender)
//	h = h.WithScheduleExecutor(sched)
//	if err := sched.Start(ctx); err != nil { ... }
//
// WithScheduleExecutor is the single switch that flips execution.enabled to
// true and makes new schedules ACTIVE, so the API's claim about itself and the
// runtime reality cannot drift apart.
func wireReportSchedules(mux *http.ServeMux, h *reporting.Handler) {
	h.RegisterBuilderRoutes(mux, reportScheduleGuard())
}

// reportScheduleGuard is the role guard the builder and schedule routes run
// behind. Creating a schedule arranges for financial data to be emailed to
// arbitrary addresses, so it is gated at least as tightly as reading the
// report. This mirrors the guard main.go already passes to
// RegisterBuilderRoutes; it is named here so the two cannot silently diverge.
func reportScheduleGuard() func(http.Handler) http.Handler {
	return middleware.RequireRole("admin", "owner", "finance")
}
