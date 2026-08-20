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
	"sync"
	"time"

	"github.com/robfig/cron/v3"
)

// EmailSender defines the interface needed to send emails with attachments.
//
// notification.LogEmailService satisfies it structurally, and is what
// cmd/server/main.go passes. See DeliveryDescription for how a caller learns
// what that sender actually does with the message.
type EmailSender interface {
	SendEmailWithAttachment(ctx context.Context, to []string, subject, body string, filename string, content []byte) error
}

// DeliveryDescriber is implemented by an EmailSender that wants to state, in
// one line fit for display, what really happens to a message it is given.
//
// It is optional. A sender that does not implement it gets the generic
// description in Scheduler.DeliveryDescription. It exists so the schedule API's
// self-description (ScheduleExecution.Delivery) is derived from the sender that
// is actually wired rather than from a sentence someone has to remember to
// update.
type DeliveryDescriber interface {
	DeliveryDescription() string
}

// Scheduler is a cron engine for emailing saved reports on a schedule.
//
// It is constructed and started in cmd/server/main.go and stopped in step 3.6
// of graceful shutdown. Start loads every ACTIVE row from report_schedules and
// registers it; the handler registers newly created schedules directly through
// the ScheduleExecutor seam, so a schedule created through the API begins
// firing without a restart.
//
// # What "sent" means here
//
// Everything up to delivery is real: the saved report's definition_json is
// decoded, the query runs against Postgres, and the result set is rendered to
// CSV or XLSX bytes. Delivery then goes to whatever EmailSender was injected. In this
// repository the only implementation is notification.LogEmailService, so the
// message is written to the server log rather than transmitted — the same as
// every other email the application sends (invoices, delivery notifications).
// That is disclosed rather than hidden: ScheduleExecution.Delivery carries the
// sender's own description of itself on every schedule API response, so a UI
// can tell an operator the difference. Swapping in real SMTP means implementing
// notification.EmailService and changing one line of main.go; nothing in this
// package changes.
//
// # Cron dialect
//
// The engine is built from cronDialect, which enables seconds, so expressions
// need SIX fields. A five-field crontab string is rejected at AddSchedule time
// and by ValidateCronExpression at the API — see cronDialect.
type Scheduler struct {
	service     *Service
	emailSender EmailSender
	cron        *cron.Cron

	// mu guards jobIDs. AddSchedule is called both from Start and, per request,
	// from Handler.HandleCreateReportSchedule, so concurrent HTTP requests
	// would otherwise race on the map.
	mu     sync.Mutex
	jobIDs map[string]cron.EntryID
}

// cronDialect is the expression grammar this package accepts: SIX fields,
// seconds first, plus descriptors. It is declared once here and is the ONE
// parser in play — NewScheduler builds the cron engine from it via
// cron.WithParser, ValidateCronExpression validates with it, and AddSchedule
// checks with it. Nothing constructs a second parser, so the API cannot accept
// an expression the engine would later refuse.
//
// This is the same field set cron.WithSeconds() installs; it is spelled out
// rather than using that option so the engine and the validator are provably
// the same object rather than two things that happen to agree.
//
// The dialect is deliberately kept at six fields rather than relaxed to the
// five-field crontab form. Both cannot be supported at once — "0 0 9 * * *" and
// "0 9 * * *" are each valid in one dialect and mean something different in the
// other, and robfig/cron resolves the ambiguity by field count, so accepting
// both would silently reinterpret an operator's expression. Six fields is what
// every stored row, the frontend presets, the API validator and the published
// cronDialectDescription already use. It is documented at every place a user
// types one: ValidateCronExpression's error, ScheduleExecution.CronDialect on
// the read path, and the hint under the cron input in app/src/pages/reports.
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
		cron:        cron.New(cron.WithParser(cronDialect)),
		jobIDs:      make(map[string]cron.EntryID),
	}
}

// DeliveryDescription states how a rendered report actually reaches its
// recipients, by asking the injected sender. A sender that does not describe
// itself gets the neutral description, which claims nothing beyond the handoff
// this package can actually observe.
func (s *Scheduler) DeliveryDescription() string {
	if d, ok := s.emailSender.(DeliveryDescriber); ok {
		return d.DeliveryDescription()
	}
	return "Each run is rendered to CSV and handed to the server's configured email sender."
}

// Start loads all ACTIVE schedules from the database and starts the cron engine.
//
// Rows in any other state are skipped, deliberately: PAUSED must not fire, and
// neither must the legacy STORED rows written while scheduled delivery was
// disabled — an operator who was told a schedule would never run must not have
// it start emailing on an upgrade. Those rows are counted and logged so they
// are discoverable rather than merely absent; recreating a STORED schedule
// through the API writes it ACTIVE.
func (s *Scheduler) Start(ctx context.Context) error {
	schedules, err := s.service.ListReportSchedules(ctx)
	if err != nil {
		return fmt.Errorf("failed to load schedules: %w", err)
	}

	var registered, skipped int
	for _, schedule := range schedules {
		if schedule.Status != ScheduleStatusActive {
			skipped++
			continue
		}
		if err := s.AddSchedule(ctx, schedule); err != nil {
			log.Printf("reporting scheduler: failed to add schedule %s: %v", schedule.ID, err)
			continue
		}
		registered++
	}

	s.cron.Start()
	log.Printf("reporting scheduler: started with %d active schedule(s); %d not in status %s were skipped",
		registered, skipped, ScheduleStatusActive)
	return nil
}

// stopDrainTimeout bounds how long Stop waits for an in-flight run. It is well
// under the server's 15s shutdown deadline: a report query that has not
// finished by then is not worth blocking the rest of shutdown for, and the
// worst case is a partially executed run, not a partially sent email — the send
// is the last step.
const stopDrainTimeout = 5 * time.Second

// Stop halts the cron engine and waits for a run already in flight to finish,
// so a report query cannot still be executing when the database pool closes in
// the next shutdown step. It is safe to call on a scheduler that was never
// started.
func (s *Scheduler) Stop() {
	drained := s.cron.Stop()
	select {
	case <-drained.Done():
	case <-time.After(stopDrainTimeout):
		log.Printf("reporting scheduler: a scheduled report was still running after %s; shutting down anyway", stopDrainTimeout)
	}
}

// AddSchedule registers a single schedule with the cron engine, replacing any
// registration this schedule ID already had.
//
// The expression is validated with ValidateCronExpression first so a rejected
// schedule carries the error that names the six-field dialect, rather than the
// bare parser error an operator cannot act on.
func (s *Scheduler) AddSchedule(ctx context.Context, schedule ReportSchedule) error {
	if err := ValidateCronExpression(schedule.CronExpression); err != nil {
		return err
	}

	job := func() {
		log.Printf("reporting scheduler: executing scheduled report %s (schedule %s)", schedule.ReportID, schedule.ID)
		if err := s.ExecuteAndSendReport(context.Background(), schedule); err != nil {
			log.Printf("reporting scheduler: failed to execute scheduled report %s (schedule %s): %v",
				schedule.ReportID, schedule.ID, err)
		}
	}

	entryID, err := s.cron.AddFunc(schedule.CronExpression, job)
	if err != nil {
		return err
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	// Re-registering an ID must not leave the previous entry firing as well,
	// which would double-send the report.
	if previous, ok := s.jobIDs[schedule.ID]; ok {
		s.cron.Remove(previous)
	}
	s.jobIDs[schedule.ID] = entryID
	return nil
}

// RemoveSchedule unregisters a schedule so it stops firing. It is a no-op for
// an ID that was never registered.
func (s *Scheduler) RemoveSchedule(id string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if entryID, ok := s.jobIDs[id]; ok {
		s.cron.Remove(entryID)
		delete(s.jobIDs, id)
	}
}

// registeredCount reports how many schedules are currently registered.
func (s *Scheduler) registeredCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.jobIDs)
}

// isRegistered reports whether a schedule ID is currently registered.
func (s *Scheduler) isRegistered(id string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, ok := s.jobIDs[id]
	return ok
}

// ExecuteAndSendReport runs the report and emails the rendered file to the
// schedule's recipients.
//
// CSV and XLSX are rendered as requested. PDF is accepted by the API but has no
// renderer in this package, so a PDF schedule is delivered as CSV — with a .csv
// filename and a sentence in the email body saying why. The alternatives were
// both worse: failing the run would be a silent failure on a timer, and
// attaching CSV bytes under a .pdf name would produce a file the recipient
// cannot open and cannot diagnose.
func (s *Scheduler) ExecuteAndSendReport(ctx context.Context, schedule ReportSchedule) error {
	// 1. Fetch the saved report.
	report, err := s.service.GetSavedReport(ctx, schedule.ReportID)
	if err != nil {
		return fmt.Errorf("failed to get report definition: %w", err)
	}
	if report == nil {
		return fmt.Errorf("saved report %s not found", schedule.ReportID)
	}

	// 2. Decode the stored definition and execute it.
	//
	// definitionFromSaved is the same decode POST /reporting/saved/{id}/run
	// performs. It is shared rather than reimplemented so the scheduled run and
	// the on-demand run can never disagree about what a saved report means.
	def, err := definitionFromSaved(report)
	if err != nil {
		return fmt.Errorf("failed to parse report definition for %s: %w", report.Name, err)
	}

	results, err := s.service.ExecuteReportDefinition(ctx, def, report.EntityType)
	if err != nil {
		return fmt.Errorf("failed to execute report query: %w", err)
	}

	// 3. Render the requested format.
	buf, ext, note, err := renderSchedule(schedule.Format, def.Columns, results)
	if err != nil {
		return err
	}

	// 4. Hand it to the email sender.
	subject := fmt.Sprintf("Scheduled Report: %s", report.Name)
	body := fmt.Sprintf("Please find attached the latest run for report '%s'.%s", report.Name, note)
	filename := fmt.Sprintf("%s_%s%s", sanitizeFilename(report.Name), time.Now().Format("2006-01-02"), ext)

	if err := s.emailSender.SendEmailWithAttachment(ctx, schedule.Recipients, subject, body, filename, buf.Bytes()); err != nil {
		return fmt.Errorf("failed to send email: %w", err)
	}

	// 5. Record the run. A failure here does not undo a delivered report, so it
	// is logged rather than returned — returning would make the caller retry a
	// send that already happened.
	if next, err := nextRunAfter(schedule.CronExpression, time.Now()); err == nil {
		if err := s.service.UpdateReportScheduleNextRun(ctx, schedule.ID, next); err != nil {
			log.Printf("reporting scheduler: report for schedule %s was sent but last_run_at/next_run_at could not be recorded: %v",
				schedule.ID, err)
		}
	}

	return nil
}

// renderSchedule renders a result set in the format the schedule asked for,
// returning the bytes, the filename extension that honestly describes them, and
// a sentence for the email body when those two do not match the request.
//
// An empty format means CSV, matching normalizeScheduleFormat's default, so a
// row written before report_schedules.format existed still renders.
func renderSchedule(format string, columns []ReportColumn, results []map[string]interface{}) (buf bytes.Buffer, ext, note string, err error) {
	switch strings.ToUpper(strings.TrimSpace(format)) {
	case "XLSX":
		if err = ExportXLSX(&buf, columns, results); err != nil {
			return buf, "", "", fmt.Errorf("failed to generate XLSX: %w", err)
		}
		return buf, ".xlsx", "", nil
	case "PDF":
		// No PDF renderer exists in this package. Say so in the message rather
		// than shipping CSV bytes under a .pdf name.
		if err = ExportCSV(&buf, columns, results); err != nil {
			return buf, "", "", fmt.Errorf("failed to generate CSV: %w", err)
		}
		return buf, ".csv",
			" (This schedule requests PDF, which this server cannot yet render; the run is attached as CSV.)", nil
	default:
		if err = ExportCSV(&buf, columns, results); err != nil {
			return buf, "", "", fmt.Errorf("failed to generate CSV: %w", err)
		}
		return buf, ".csv", "", nil
	}
}

// nextRunAfter computes the next fire time for an expression using the same
// parser the engine runs on, so next_run_at cannot describe a different
// schedule from the one actually registered.
func nextRunAfter(expr string, after time.Time) (time.Time, error) {
	parsed, err := cronDialect.Parse(expr)
	if err != nil {
		return time.Time{}, err
	}
	return parsed.Next(after), nil
}

// sanitizeFilename keeps a report name usable as an email attachment filename.
// Report names are operator-supplied free text; a "/" or a quote in one would
// otherwise travel into a MIME filename header the moment a real SMTP sender is
// swapped in for the log-only one.
func sanitizeFilename(name string) string {
	cleaned := strings.Map(func(r rune) rune {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
			return r
		case r == '-', r == '_':
			return r
		default:
			return '_'
		}
	}, name)
	cleaned = strings.Trim(cleaned, "_")
	if cleaned == "" {
		return "report"
	}
	return cleaned
}
