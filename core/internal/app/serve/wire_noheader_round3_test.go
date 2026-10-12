// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package serve

// SEC-NOHEADER-1 round 3: the findings the r4 Opus and r3 Sonnet reviews
// name. A scheduled report runs under its creator's reach, a create that
// fell to the deployment default takes its branch from the grants, the
// reconciliation's branch owned totals follow the reach, the customer
// account surface holds the customer wall, and every guard the reviews
// found untested gets a leg that goes red when it is reverted alone.

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"sync"
	"testing"

	"github.com/gablelbm/gable/internal/reporting"
	"github.com/gablelbm/gable/pkg/audit"
	"github.com/google/uuid"
)

// r3CaptureSender captures what a scheduled run hands the email sender.
type r3CaptureSender struct {
	mu   sync.Mutex
	sent []string
}

func (c *r3CaptureSender) SendEmailWithAttachment(_ context.Context, _ []string, _, _, _ string, content []byte) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.sent = append(c.sent, string(content))
	return nil
}

func (c *r3CaptureSender) count() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.sent)
}

func (c *r3CaptureSender) last() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.sent) == 0 {
		return ""
	}
	return c.sent[len(c.sent)-1]
}

// TestNoHeader_ReportScheduleRunsUnderCreatorReach is the scheduled report
// family (r4 P1-1, r3 P2-1): a finance user granted only A saves a report
// and schedules it, and the scheduled run delivers the granted branches'
// rows only, never every branch's, whatever header the creator sent when
// it made the schedule.
func TestNoHeader_ReportScheduleRunsUnderCreatorReach(t *testing.T) {
	f := newKeyPinFixture(t)
	f.seedWalledTwins(t)
	noheaderGrant(t, f, "pin-finance", f.branchA)

	saveBody := `{"name":"pin noheader schedule r3","description":"pin","entity_type":"orders",` +
		`"definition_json":{"columns":[{"field":"customer_name","label":"Customer"},{"field":"id","label":"Orders","aggregation":"COUNT"}],` +
		`"filters":[{"field":"customer_name","operator":"LIKE","value":"pin cust"}],` +
		`"groupings":[{"field":"customer_name"}]}}`
	status, buf := f.userAs(t, "finance", "POST", "/api/v1/reporting/save", saveBody, nil)
	if status != http.StatusOK {
		t.Fatalf("finance saved report: %d %s", status, buf)
	}
	var saved struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(buf, &saved); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = f.db.Pool.Exec(context.Background(), `DELETE FROM report_schedules WHERE report_id = $1`, saved.ID)
		_, _ = f.db.Pool.Exec(context.Background(), `DELETE FROM saved_reports WHERE id = $1`, saved.ID)
	})

	schedBody := `{"report_id":"` + saved.ID + `","cron_expression":"0 0 9 * * *","recipients":["probe@example.invalid"]}`
	status, buf = f.userAs(t, "finance", "POST", "/api/v1/reporting/schedules", schedBody, nil)
	if status != http.StatusCreated {
		t.Fatalf("finance scheduled report: %d %s", status, buf)
	}

	// Reload the stored row the way Scheduler.Start does, then run the job
	// exactly as the cron closure does. The row must carry the creator the
	// route records: the run's reach is resolved from it.
	svc := reporting.NewService(reporting.NewRepository(f.db))
	schedules, err := svc.ListReportSchedules(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	var stored *reporting.ReportSchedule
	for i := range schedules {
		if schedules[i].ReportID == saved.ID && schedules[i].CreatedBy == "pin-finance" {
			stored = &schedules[i]
		}
	}
	if stored == nil {
		t.Fatalf("the schedule the finance caller created records no creator of its own")
	}

	sender := &r3CaptureSender{}
	sched := reporting.NewScheduler(svc, sender).WithRefusalAudit(audit.NewLogger(f.db))
	if err := sched.ExecuteAndSendReport(context.Background(), *stored); err != nil {
		t.Fatalf("ExecuteAndSendReport: %v", err)
	}
	content := sender.last()
	if strings.Contains(content, "pin cust B") {
		t.Errorf("the scheduled run carries B's rows to the recipients: %s", content)
	}
	if !strings.Contains(content, "pin cust") {
		t.Errorf("the scheduled run misses A's rows: %s", content)
	}

	// The control: an administrator's schedule on the same report keeps
	// every branch.
	status, buf = f.userAs(t, "admin", "POST", "/api/v1/reporting/schedules", schedBody, nil)
	if status != http.StatusCreated {
		t.Fatalf("admin scheduled report: %d %s", status, buf)
	}
	schedules, err = svc.ListReportSchedules(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	for i := range schedules {
		if schedules[i].ReportID == saved.ID && schedules[i].CreatedBy == "pin-admin" {
			adminSched := schedules[i]
			if !adminSched.CreatorIsAdmin {
				t.Fatalf("the admin caller's schedule is not recorded as an administrator's")
			}
			before := sender.count()
			if err := sched.ExecuteAndSendReport(context.Background(), adminSched); err != nil {
				t.Fatalf("admin ExecuteAndSendReport: %v", err)
			}
			adminContent := sender.last()
			if sender.count() != before+1 || !strings.Contains(adminContent, "pin cust B") || !strings.Contains(adminContent, "pin cust") {
				t.Errorf("the administrator's scheduled run does not keep every branch: %s", adminContent)
			}
		}
	}

	// A schedule with no creator recorded sends nothing and audits: the row
	// is what migration 112 left every legacy schedule as.
	nullID := uuid.NewString()
	if _, err := f.db.Pool.Exec(context.Background(), `
		INSERT INTO report_schedules (id, report_id, cron_expression, recipients, status, format)
		VALUES ($1, $2, '0 0 9 * * *', '["probe@example.invalid"]', 'ACTIVE', 'CSV')`, nullID, saved.ID); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = f.db.Pool.Exec(context.Background(), `DELETE FROM audit_log WHERE entity_type = 'report_schedule' AND entity_id = $1`, nullID)
		_, _ = f.db.Pool.Exec(context.Background(), `DELETE FROM report_schedules WHERE id = $1`, nullID)
	})
	before := sender.count()
	if err := sched.ExecuteAndSendReport(context.Background(), reporting.ReportSchedule{
		ID: nullID, ReportID: saved.ID, CronExpression: "0 0 9 * * *",
		Recipients: []string{"probe@example.invalid"}, Status: reporting.ScheduleStatusActive, Format: "CSV",
	}); err == nil {
		t.Error("a schedule with no creator recorded ran, want the fail closed refusal")
	}
	if sender.count() != before {
		t.Errorf("a schedule with no creator recorded sent %d run(s), want none", sender.count()-before)
	}
	var refusals int
	if err := f.db.Pool.QueryRow(context.Background(),
		`SELECT COUNT(*) FROM audit_log WHERE entity_type = 'report_schedule' AND entity_id = $1 AND action = 'report_schedule.refused'`,
		nullID).Scan(&refusals); err != nil {
		t.Fatal(err)
	}
	if refusals != 1 {
		t.Errorf("the refused run wrote %d audit row(s), want one", refusals)
	}
}
