// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package reporting

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/robfig/cron/v3"
)

// EmailSender defines the interface needed to send emails with attachments.
type EmailSender interface {
	SendEmailWithAttachment(ctx context.Context, to []string, subject, body string, filename string, content []byte) error
}

// Scheduler is a cron engine for emailing saved reports on a schedule.
//
// NOT WIRED, AND KNOWN-BROKEN. Nothing in this repository constructs a
// Scheduler outside scheduler_test.go. Do not treat scheduled reports as a
// working feature, and do not wire this into cmd/server/main.go as-is —
// starting it would only schedule guaranteed failures. Three things are
// missing, in the order they must be fixed:
//
//  1. ExecuteAndSendReport never populates ReportDefinition from the saved
//     report's DefinitionJSON (see the BUG note in that function). It
//     executes an empty definition, which BuildAndExecuteQuery rejects with
//     "no columns selected", so no scheduled report has ever produced
//     output. TestExecuteAndSendReport_DropsTheSavedDefinition in
//     scheduler_test.go characterizes this: it passes *because* the call
//     fails, and will start failing — deliberately — once the mapping lands.
//  2. No EmailSender implementation exists. notification.LogEmailService has
//     SendInvoice/SendDeliveryNotification, not SendEmailWithAttachment; the
//     only implementer today is the test fake.
//  3. No HTTP route creates, lists or deletes a schedule. Migration
//     032b_report_builder_and_bi.sql creates report_schedules and
//     Repository/Service expose CreateReportSchedule, ListReportSchedules and
//     UpdateReportScheduleNextRun, but reporting.Handler registers only the
//     report, builder and BI-export routes — so the table is always empty and
//     Start would load nothing even if it were called.
//
// Also note the cron dialect: New is built with cron.WithSeconds(), so cron
// expressions here need SIX fields. A standard five-field crontab string is
// rejected at AddSchedule time.
//
// Tracking: CLAUDE.md § "Tier 1 Backlog → #10 candidates → A. Finish
// reporting scheduler" carries the full work list.
type Scheduler struct {
	service     *Service
	emailSender EmailSender
	cron        *cron.Cron
	jobIDs      map[string]cron.EntryID
}

// cronDialect is the expression grammar this package accepts. It is
// cron.WithSeconds(): SIX fields, seconds first. It is declared once here and
// used both by NewScheduler and by ValidateCronExpression so the API can never
// accept an expression the engine would later refuse.
var cronDialect = cron.NewParser(
	cron.Second | cron.Minute | cron.Hour | cron.Dom | cron.Month | cron.Dow | cron.Descriptor,
)

// ValidateCronExpression reports whether expr can be registered with this
// package's cron engine.
//
// The error deliberately spells out the six-field requirement. Every crontab,
// and every "0 8 * * *" example on the internet, is FIVE fields, and this
// engine rejects those — an operator who pastes one otherwise gets a bare
// parse error with no hint that a leading seconds field is missing.
func ValidateCronExpression(expr string) error {
	if strings.TrimSpace(expr) == "" {
		return errors.New("cron_expression is required")
	}
	if _, err := cronDialect.Parse(expr); err != nil {
		return fmt.Errorf(
			"invalid cron_expression %q: this scheduler uses six fields with a leading seconds field "+
				"(e.g. \"0 0 9 * * *\" for 09:00 daily), not the five-field crontab form, or a descriptor like @daily: %w",
			expr, err)
	}
	return nil
}

func NewScheduler(service *Service, emailSender EmailSender) *Scheduler {
	return &Scheduler{
		service:     service,
		emailSender: emailSender,
		cron:        cron.New(cron.WithSeconds()), // Standard cron + seconds
		jobIDs:      make(map[string]cron.EntryID),
	}
}

// Start loads all active schedules from the database and starts the cron engine.
func (s *Scheduler) Start(ctx context.Context) error {
	schedules, err := s.service.ListReportSchedules(ctx)
	if err != nil {
		return fmt.Errorf("failed to load schedules: %w", err)
	}

	for _, schedule := range schedules {
		if schedule.Status == "ACTIVE" {
			if err := s.AddSchedule(ctx, schedule); err != nil {
				log.Printf("failed to add schedule %s: %v", schedule.ID, err)
			}
		}
	}

	s.cron.Start()
	return nil
}

// Stop gracefully shuts down the cron engine.
func (s *Scheduler) Stop() {
	s.cron.Stop()
}

// AddSchedule registers a single schedule with the cron engine.
func (s *Scheduler) AddSchedule(ctx context.Context, schedule ReportSchedule) error {
	job := func() {
		log.Printf("Executing scheduled report: %s", schedule.ReportID)
		if err := s.ExecuteAndSendReport(context.Background(), schedule); err != nil {
			log.Printf("Failed to execute scheduled report %s: %v", schedule.ReportID, err)
		}
	}

	entryID, err := s.cron.AddFunc(schedule.CronExpression, job)
	if err != nil {
		return err
	}

	s.jobIDs[schedule.ID] = entryID
	return nil
}

// ExecuteAndSendReport runs the report and emails the PDF/CSV to recipients.
func (s *Scheduler) ExecuteAndSendReport(ctx context.Context, schedule ReportSchedule) error {
	// 1. Fetch Report Definition
	report, err := s.service.GetSavedReport(ctx, schedule.ReportID)
	if err != nil {
		return fmt.Errorf("failed to get report definition: %w", err)
	}

	// 2. Execute Query
	//
	// BUG (see the Scheduler doc comment): report.DefinitionJSON is a
	// map[string]interface{} and is never decoded into def. The zero
	// ReportDefinition has no columns, so ExecuteReportDefinition below always
	// fails with "no columns selected" and this function can never send a report.
	// Fix this first; TestExecuteAndSendReport_DropsTheSavedDefinition will flip
	// from passing to failing when you do, which is the intended signal.
	var def ReportDefinition
	// ... populate def from report.DefinitionJSON ...

	results, err := s.service.ExecuteReportDefinition(ctx, &def, report.EntityType)
	if err != nil {
		return fmt.Errorf("failed to execute report query: %w", err)
	}

	// 3. Generate CSV (Defaulting to CSV for scheduled reports for simplicity)
	var buf bytes.Buffer
	if err := ExportCSV(&buf, def.Columns, results); err != nil {
		return fmt.Errorf("failed to generate CSV: %w", err)
	}

	// 4. Send Email
	subject := fmt.Sprintf("Scheduled Report: %s", report.Name)
	body := fmt.Sprintf("Please find attached the latest run for report '%s'.", report.Name)
	filename := fmt.Sprintf("%s_%s.csv", report.Name, time.Now().Format("2006-01-02"))

	if err := s.emailSender.SendEmailWithAttachment(ctx, schedule.Recipients, subject, body, filename, buf.Bytes()); err != nil {
		return fmt.Errorf("failed to send email: %w", err)
	}

	// 5. Update Schedule Last/Next Run Status — NOT IMPLEMENTED.
	// Service.UpdateReportScheduleNextRun exists and is never called, so
	// report_schedules.last_run_at / next_run_at would stay NULL forever.

	return nil
}
