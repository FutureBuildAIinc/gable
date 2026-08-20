// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package reporting

import (
	"context"
	"errors"
	"sync"
	"testing"
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

// CHARACTERIZATION of a KNOWN BUG. ExecuteAndSendReport declares
// `var def ReportDefinition` and never populates it from the saved report's
// DefinitionJSON — the source comments say the mapping is "omitted for
// brevity". Every scheduled report therefore executes an empty definition,
// which BuildAndExecuteQuery rejects with "no columns selected", so no
// scheduled report has ever produced output.
//
// backend/internal/reporting/scheduler.go:94-96 —
//
//	var def ReportDefinition
//	// ... populate def from report.DefinitionJSON ...
//	results, err := s.service.ExecuteReportDefinition(ctx, &def, report.EntityType)
//
// The test asserts the currently-observable consequence: the call fails and no
// email is sent, even for a saved report with a perfectly good definition. When
// the mapping is implemented this test will fail, which is the intended signal.
func TestExecuteAndSendReport_DropsTheSavedDefinition(t *testing.T) {
	repo := newFakeRepo()
	repo.saved["r1"] = &SavedReport{
		ID:         "r1",
		Name:       "AR by customer",
		EntityType: "invoices",
		DefinitionJSON: map[string]any{
			"columns": []any{
				map[string]any{"field": "customer_name", "label": "Customer"},
				map[string]any{"field": "total_amount", "label": "Total", "aggregation": "SUM"},
			},
			"groupings": []any{map[string]any{"field": "customer_name"}},
		},
	}

	sender := &fakeSender{}
	sched := NewScheduler(NewService(repo), sender)

	err := sched.ExecuteAndSendReport(context.Background(), ReportSchedule{
		ID: "s1", ReportID: "r1", Recipients: []string{"finance@example.com"},
	})
	if err == nil {
		t.Fatal("expected the empty-definition failure; if this now succeeds the DefinitionJSON mapping has been implemented and this characterization test should become a correctness test")
	}
	if sender.count() != 0 {
		t.Errorf("sent %d emails, want 0", sender.count())
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
