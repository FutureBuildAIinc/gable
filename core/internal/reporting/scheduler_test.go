// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package reporting

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"
)

// --- fake email sender ---------------------------------------------------

type sentEmail struct {
	to       []string
	subject  string
	body     string
	filename string
	content  []byte
}

type fakeSender struct {
	mu   sync.Mutex
	sent []sentEmail
	err  error
}

func (f *fakeSender) SendEmailWithAttachment(_ context.Context, to []string, subject, body, filename string, content []byte) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.err != nil {
		return f.err
	}
	f.sent = append(f.sent, sentEmail{to: to, subject: subject, body: body, filename: filename, content: content})
	return nil
}

func (f *fakeSender) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.sent)
}

var _ EmailSender = (*fakeSender)(nil)

// describingSender is a sender that states what it does with a message, the way
// notification.LogEmailService does.
type describingSender struct {
	fakeSender
	desc string
}

func (d *describingSender) DeliveryDescription() string { return d.desc }

var (
	_ EmailSender       = (*describingSender)(nil)
	_ DeliveryDescriber = (*describingSender)(nil)
)

// --- cron expression handling -------------------------------------------

// CORRECTNESS: the scheduler is built with cron.WithSeconds(), so it needs
// SIX-field expressions. A five-field expression — the form every crontab and
// every "0 8 * * *" example on the internet uses — is rejected. That must be
// surfaced as an error at AddSchedule time (it is), because a schedule that
// fails to register would otherwise never fire and never say why.
//
// This is also a usability trap worth pinning: any UI that accepts standard
// cron syntax will hand this function expressions it refuses.
func TestAddSchedule_RequiresSixFieldCronExpressions(t *testing.T) {
	tests := []struct {
		name    string
		expr    string
		wantErr bool
	}{
		{"six fields: every day at 08:00:00", "0 0 8 * * *", false},
		{"six fields: top of every hour", "0 0 * * * *", false},
		{"six fields: every 30 seconds", "*/30 * * * * *", false},
		{"descriptor", "@daily", false},
		{"every-duration descriptor", "@every 1h", false},
		{"five-field crontab syntax is refused", "0 8 * * *", true},
		{"empty", "", true},
		{"garbage", "not a cron expression", true},
		{"out-of-range minute", "0 99 8 * * *", true},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			sched := NewScheduler(NewService(newFakeRepo()), &fakeSender{})
			err := sched.AddSchedule(context.Background(), ReportSchedule{
				ID:             "s1",
				ReportID:       "r1",
				CronExpression: tc.expr,
			})
			if tc.wantErr && err == nil {
				t.Fatalf("AddSchedule(%q) succeeded, want an error", tc.expr)
			}
			if !tc.wantErr && err != nil {
				t.Fatalf("AddSchedule(%q): %v", tc.expr, err)
			}
			if !tc.wantErr {
				if _, ok := sched.jobIDs["s1"]; !ok {
					t.Error("a registered schedule must be tracked in jobIDs so it can be replaced or removed later")
				}
			} else if _, ok := sched.jobIDs["s1"]; ok {
				t.Error("a rejected schedule must not be recorded as registered")
			}
		})
	}
}

// --- Start ---------------------------------------------------------------

// CORRECTNESS: Start only registers ACTIVE schedules. A paused schedule that
// still fired would email report data to recipients who were removed.
func TestScheduler_StartRegistersOnlyActiveSchedules(t *testing.T) {
	repo := newFakeRepo()
	repo.schedules = []ReportSchedule{
		{ID: "active-1", ReportID: "r1", CronExpression: "0 0 8 * * *", Status: "ACTIVE"},
		{ID: "paused", ReportID: "r2", CronExpression: "0 0 8 * * *", Status: "PAUSED"},
		{ID: "active-2", ReportID: "r3", CronExpression: "0 0 9 * * *", Status: "ACTIVE"},
		{ID: "empty-status", ReportID: "r4", CronExpression: "0 0 9 * * *"},
	}

	sched := NewScheduler(NewService(repo), &fakeSender{})
	if err := sched.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer sched.Stop()

	if len(sched.jobIDs) != 2 {
		t.Fatalf("registered %d jobs (%v), want the 2 ACTIVE ones", len(sched.jobIDs), sched.jobIDs)
	}
	for _, id := range []string{"active-1", "active-2"} {
		if _, ok := sched.jobIDs[id]; !ok {
			t.Errorf("ACTIVE schedule %q was not registered", id)
		}
	}
	for _, id := range []string{"paused", "empty-status"} {
		if _, ok := sched.jobIDs[id]; ok {
			t.Errorf("non-ACTIVE schedule %q was registered", id)
		}
	}
}

// CORRECTNESS: an unparseable expression on one schedule must not stop the
// others from being registered — one bad row should not silently disable every
// scheduled report.
func TestScheduler_StartSkipsUnregisterableSchedules(t *testing.T) {
	repo := newFakeRepo()
	repo.schedules = []ReportSchedule{
		{ID: "bad", ReportID: "r1", CronExpression: "0 8 * * *", Status: "ACTIVE"}, // five fields
		{ID: "good", ReportID: "r2", CronExpression: "0 0 8 * * *", Status: "ACTIVE"},
	}

	sched := NewScheduler(NewService(repo), &fakeSender{})
	if err := sched.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer sched.Stop()

	if _, ok := sched.jobIDs["good"]; !ok {
		t.Error("a valid schedule after an invalid one was not registered")
	}
	if _, ok := sched.jobIDs["bad"]; ok {
		t.Error("the invalid schedule was registered")
	}
}

// CORRECTNESS: if the schedule list cannot be read, Start must fail loudly
// rather than come up with no jobs and report success.
func TestScheduler_StartPropagatesLoadFailure(t *testing.T) {
	repo := newFakeRepo()
	repo.savedErr = errors.New("db down")

	sched := NewScheduler(NewService(repo), &fakeSender{})
	if err := sched.Start(context.Background()); err == nil {
		t.Fatal("want an error when the schedule list cannot be loaded")
	}
}

// Stop must be safe even when nothing was ever started.
func TestScheduler_StopWithoutStart(t *testing.T) {
	NewScheduler(NewService(newFakeRepo()), &fakeSender{}).Stop()
}

// --- ExecuteAndSendReport ------------------------------------------------

// CORRECTNESS: a schedule pointing at a report that no longer exists must fail
// and must not send an email with whatever data happened to be lying around.
func TestExecuteAndSendReport_MissingReportSendsNothing(t *testing.T) {
	sender := &fakeSender{}
	sched := NewScheduler(NewService(newFakeRepo()), sender)

	err := sched.ExecuteAndSendReport(context.Background(), ReportSchedule{
		ID: "s1", ReportID: "gone", Recipients: []string{"a@example.com"},
	})
	if err == nil {
		t.Fatal("want an error when the saved report is missing")
	}
	if sender.count() != 0 {
		t.Fatalf("sent %d emails despite the failure", sender.count())
	}
}

// CORRECTNESS, replacing a characterization test of the bug that used to live
// here: ExecuteAndSendReport declared `var def ReportDefinition` and never
// populated it from the saved report's DefinitionJSON, so every scheduled run
// executed an empty definition and died at "no columns selected".
//
// It now decodes through definitionFromSaved — the same helper
// POST /reporting/saved/{id}/run uses. This test pins that the decode step is
// on the path at all: a definition_json that cannot be decoded produces the
// decode error, which is only reachable if the decode happens. It cannot go
// further, because Service.ExecuteReportDefinition requires a real
// *PostgresRepository, and the bug was a data-shape bug that only a real
// database can disprove. The evidence that the decoded definition is genuinely
// honoured — right columns, right rows, in the delivered CSV — is
// TestScheduledReport_EndToEndAgainstPostgres in scheduler_postgres_test.go.
func TestExecuteAndSendReport_DecodesTheSavedDefinition(t *testing.T) {
	repo := newFakeRepo()
	repo.saved["r1"] = &SavedReport{
		ID:         "r1",
		Name:       "AR by customer",
		EntityType: "invoices",
		// A channel cannot be marshalled, so this fails inside
		// definitionFromSaved and nowhere else.
		DefinitionJSON: map[string]any{"columns": make(chan int)},
	}

	sender := &fakeSender{}
	sched := NewScheduler(NewService(repo), sender)

	err := sched.ExecuteAndSendReport(context.Background(), ReportSchedule{
		ID: "s1", ReportID: "r1", Recipients: []string{"finance@example.com"},
	})
	if err == nil {
		t.Fatal("want an error when the stored definition cannot be decoded")
	}
	if !strings.Contains(err.Error(), "report definition") {
		t.Errorf("error = %v, want the definition-decode failure — if the decode were skipped again, "+
			"this would instead fail later with an execution error and every scheduled report would ship no columns", err)
	}
	if sender.count() != 0 {
		t.Errorf("sent %d emails, want 0", sender.count())
	}
}

// CORRECTNESS: a schedule whose saved report has vanished must fail, and must
// not be reported as a successful send.
func TestExecuteAndSendReport_StopsBeforeSendingOnExecutionFailure(t *testing.T) {
	repo := newFakeRepo()
	repo.saved["r1"] = &SavedReport{
		ID:         "r1",
		Name:       "AR by customer",
		EntityType: "invoices",
		DefinitionJSON: map[string]any{
			"columns": []any{map[string]any{"field": "customer_name", "label": "Customer"}},
		},
	}

	sender := &fakeSender{}
	sched := NewScheduler(NewService(repo), sender)

	// The fake repository is not a *PostgresRepository, so execution fails.
	// What matters is the ordering: nothing is emailed and no run is recorded.
	if err := sched.ExecuteAndSendReport(context.Background(), ReportSchedule{
		ID: "s1", ReportID: "r1", Recipients: []string{"finance@example.com"},
	}); err == nil {
		t.Fatal("want an error when the query cannot be executed")
	}
	if sender.count() != 0 {
		t.Errorf("sent %d emails after an execution failure, want 0", sender.count())
	}
	if _, ok := repo.nextRuns["s1"]; ok {
		t.Error("recorded a run for a report that was never delivered")
	}
}

// --- rendering -----------------------------------------------------------

// CORRECTNESS: the attachment's extension must describe its actual bytes. PDF
// is accepted by the API but has no renderer, so a PDF schedule gets CSV — and
// the recipient has to be told, or they get a spreadsheet where they expected a
// document and no way to find out why.
func TestRenderSchedule_ExtensionMatchesTheBytes(t *testing.T) {
	columns := []ReportColumn{{Field: "customer_name", Label: "Customer"}}
	results := []map[string]interface{}{{"customer_name": "Kelbrook Homes"}}

	t.Run("CSV", func(t *testing.T) {
		buf, ext, note, err := renderSchedule("CSV", columns, results)
		if err != nil {
			t.Fatalf("renderSchedule: %v", err)
		}
		if ext != ".csv" {
			t.Errorf("ext = %q, want .csv", ext)
		}
		if note != "" {
			t.Errorf("note = %q, want none when the requested format was produced", note)
		}
		if !strings.Contains(buf.String(), "Customer") {
			t.Errorf("CSV = %q, want the column label as a header", buf.String())
		}
	})

	t.Run("an empty format defaults to CSV", func(t *testing.T) {
		_, ext, _, err := renderSchedule("", columns, results)
		if err != nil {
			t.Fatalf("renderSchedule: %v", err)
		}
		if ext != ".csv" {
			t.Errorf("ext = %q, want .csv", ext)
		}
	})

	t.Run("XLSX", func(t *testing.T) {
		buf, ext, note, err := renderSchedule("XLSX", columns, results)
		if err != nil {
			t.Fatalf("renderSchedule: %v", err)
		}
		if ext != ".xlsx" {
			t.Errorf("ext = %q, want .xlsx", ext)
		}
		if note != "" {
			t.Errorf("note = %q, want none when the requested format was produced", note)
		}
		// A real xlsx is a zip archive; "PK" is its magic number.
		if !strings.HasPrefix(buf.String(), "PK") {
			t.Errorf("XLSX attachment does not start with the zip magic number; got %q", buf.String()[:min(4, buf.Len())])
		}
	})

	t.Run("PDF falls back to CSV and says so", func(t *testing.T) {
		buf, ext, note, err := renderSchedule("PDF", columns, results)
		if err != nil {
			t.Fatalf("renderSchedule: %v", err)
		}
		if ext != ".csv" {
			t.Errorf("ext = %q, want .csv — CSV bytes must not be named .pdf", ext)
		}
		if !strings.Contains(note, "CSV") {
			t.Errorf("note = %q, want it to tell the recipient the format was substituted", note)
		}
		if !strings.Contains(buf.String(), "Customer") {
			t.Errorf("attachment = %q, want the CSV fallback content", buf.String())
		}
	})
}

// Report names are operator free text and end up in a filename — and, the
// moment a real SMTP sender replaces the log-only one, in a MIME header.
func TestSanitizeFilename(t *testing.T) {
	cases := map[string]string{
		"AR by customer":     "AR_by_customer",
		"Q1/Q2 \"summary\"":  "Q1_Q2__summary",
		"../../etc/passwd":   "etc_passwd",
		"report\r\nInjected": "report__Injected",
		"":                   "report",
		"///":                "report",
	}
	for in, want := range cases {
		if got := sanitizeFilename(in); got != want {
			t.Errorf("sanitizeFilename(%q) = %q, want %q", in, got, want)
		}
	}
}

// --- delivery disclosure --------------------------------------------------

// CORRECTNESS: the scheduler's account of how a report reaches its recipients
// must come from the sender that is actually injected. A hard-coded sentence
// would keep claiming "emailed" after someone swapped the sender out, or keep
// claiming "log-only" after someone wired SMTP.
func TestScheduler_DeliveryDescriptionComesFromTheSender(t *testing.T) {
	t.Run("a sender that describes itself is quoted", func(t *testing.T) {
		sched := NewScheduler(NewService(newFakeRepo()), &describingSender{desc: "carrier pigeon"})
		if got := sched.DeliveryDescription(); got != "carrier pigeon" {
			t.Errorf("DeliveryDescription() = %q, want the sender's own words", got)
		}
	})

	t.Run("a sender that does not claims nothing beyond the handoff", func(t *testing.T) {
		got := NewScheduler(NewService(newFakeRepo()), &fakeSender{}).DeliveryDescription()
		if got == "" {
			t.Fatal("DeliveryDescription() is empty")
		}
		if strings.Contains(got, "log") {
			t.Errorf("DeliveryDescription() = %q, want no claim about a sender that did not describe itself", got)
		}
	})
}

// --- registration lifecycle ----------------------------------------------

// CORRECTNESS: re-registering a schedule ID must replace the previous entry.
// Two live entries for one schedule would email the report twice.
func TestAddSchedule_ReregisteringReplacesTheEntry(t *testing.T) {
	sched := NewScheduler(NewService(newFakeRepo()), &fakeSender{})
	s := ReportSchedule{ID: "s1", ReportID: "r1", CronExpression: "0 0 8 * * *"}

	if err := sched.AddSchedule(context.Background(), s); err != nil {
		t.Fatalf("AddSchedule: %v", err)
	}
	s.CronExpression = "0 0 9 * * *"
	if err := sched.AddSchedule(context.Background(), s); err != nil {
		t.Fatalf("AddSchedule (second): %v", err)
	}

	if got := len(sched.cron.Entries()); got != 1 {
		t.Errorf("cron holds %d entries for one schedule, want 1 — the report would be emailed once per stale entry", got)
	}
	if sched.registeredCount() != 1 {
		t.Errorf("jobIDs holds %d entries, want 1", sched.registeredCount())
	}
}

// CORRECTNESS: deleting a schedule must stop it firing. A cron entry outlives
// the row it came from, so a deleted schedule would otherwise keep emailing
// until the next restart.
func TestRemoveSchedule_UnregistersTheJob(t *testing.T) {
	sched := NewScheduler(NewService(newFakeRepo()), &fakeSender{})
	if err := sched.AddSchedule(context.Background(), ReportSchedule{
		ID: "s1", ReportID: "r1", CronExpression: "0 0 8 * * *",
	}); err != nil {
		t.Fatalf("AddSchedule: %v", err)
	}

	sched.RemoveSchedule("s1")

	if sched.isRegistered("s1") {
		t.Error("the schedule is still registered after RemoveSchedule")
	}
	if got := len(sched.cron.Entries()); got != 0 {
		t.Errorf("cron holds %d entries after removal, want 0", got)
	}

	// Removing something that was never registered is not an error.
	sched.RemoveSchedule("never-registered")
}

// The next-run time recorded in the database must come from the same parser the
// engine fires on, or next_run_at would advertise a different schedule from the
// one that actually runs.
func TestNextRunAfter_UsesTheEngineDialect(t *testing.T) {
	base := time.Date(2026, 8, 20, 10, 30, 0, 0, time.UTC)

	next, err := nextRunAfter("0 0 9 * * *", base)
	if err != nil {
		t.Fatalf("nextRunAfter: %v", err)
	}
	if want := time.Date(2026, 8, 21, 9, 0, 0, 0, time.UTC); !next.Equal(want) {
		t.Errorf("next = %v, want %v", next, want)
	}

	if _, err := nextRunAfter("0 9 * * *", base); err == nil {
		t.Error("a five-field expression must not yield a next-run time; the engine would refuse it")
	}
}

// CORRECTNESS: whatever the definition problem, a schedule must never reach the
// email step with an empty attachment. This pins the ordering: execution
// failure short-circuits before SendEmailWithAttachment.
func TestExecuteAndSendReport_NeverEmailsAnEmptyAttachment(t *testing.T) {
	repo := newFakeRepo()
	repo.saved["r1"] = &SavedReport{ID: "r1", Name: "Anything", EntityType: "invoices"}

	sender := &fakeSender{}
	sched := NewScheduler(NewService(repo), sender)

	_ = sched.ExecuteAndSendReport(context.Background(), ReportSchedule{
		ID: "s1", ReportID: "r1", Recipients: []string{"finance@example.com"},
	})

	for _, e := range sender.sent {
		if len(e.content) == 0 {
			t.Errorf("emailed an empty attachment %q to %v", e.filename, e.to)
		}
	}
}
