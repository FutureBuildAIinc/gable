// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package reporting

import (
	"context"
	"encoding/csv"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/gablelbm/gable/internal/testutil"
	"github.com/gablelbm/gable/pkg/database"
	"github.com/google/uuid"
)

// The scheduled-report path, end to end, against a real PostgreSQL.
//
// These tests exist because the bug they replace was a data-shape bug.
// Scheduler.ExecuteAndSendReport used to declare `var def ReportDefinition` and
// never decode the saved report's definition_json into it, so every scheduled
// run executed an empty definition. A fake repository cannot disprove that:
// Service.ExecuteReportDefinition requires a real *PostgresRepository, and the
// definition only takes its true shape after a round trip through a JSONB
// column — map[string]any of []any of map[string]any, which is exactly what the
// missing decode had to cope with. So the proof has to run against Postgres:
// save a report, schedule it, fire it, and read the bytes that reached the
// sender.
//
// They skip cleanly when Postgres is unreachable, like every other DB-gated
// test in this repository (see internal/testutil).

// scheduledReportFixture is a saved report over invoices belonging to one
// throwaway customer, plus a schedule for it. Every row it creates is removed
// again through t.Cleanup, and the customer name is unique per run so the
// report's own filter isolates it from anything else in the database.
type scheduledReportFixture struct {
	customerName string
	reportID     string
	schedule     ReportSchedule
	service      *Service
}

func newScheduledReportFixture(t *testing.T, db *database.DB, cronExpr string) scheduledReportFixture {
	t.Helper()
	ctx := context.Background()

	run := uuid.New().String()
	customerName := "Gable Scheduler E2E " + run
	customerID := uuid.New()

	// customers.primary_branch_id and invoices.branch_id are NOT NULL; the
	// default branch is seeded in system_settings by migration 059.
	if _, err := db.Pool.Exec(ctx, `
		INSERT INTO customers (id, name, account_number, primary_branch_id)
		VALUES ($1, $2, $3, (SELECT value::uuid FROM system_settings WHERE key = 'default_branch_id'))`,
		customerID, customerName, "SCHED-"+run[:8]); err != nil {
		t.Fatalf("seed customer: %v", err)
	}

	// Two invoices, so a SUM is not the same number as either row and a COUNT
	// is not 1 — an aggregation that was quietly dropped would show up.
	for _, amount := range []string{"1234.56", "765.44"} {
		if _, err := db.Pool.Exec(ctx, `
			INSERT INTO invoices (customer_id, status, total_amount, branch_id)
			VALUES ($1, 'UNPAID', $2,
			        (SELECT value::uuid FROM system_settings WHERE key = 'default_branch_id'))`,
			customerID, amount); err != nil {
			t.Fatalf("seed invoice: %v", err)
		}
	}

	svc := NewService(NewRepository(db))

	report := &SavedReport{
		Name:        "AR by customer " + run[:8],
		Description: "scheduler end-to-end fixture",
		EntityType:  "invoices",
		CreatedBy:   uuid.New().String(),
		DefinitionJSON: map[string]interface{}{
			"columns": []interface{}{
				map[string]interface{}{"field": "customer_name", "label": "Customer"},
				map[string]interface{}{"field": "total_amount", "label": "Total", "aggregation": "SUM"},
				map[string]interface{}{"field": "id", "label": "Invoices", "aggregation": "COUNT"},
			},
			"filters":   []interface{}{map[string]interface{}{"field": "customer_name", "operator": "=", "value": customerName}},
			"groupings": []interface{}{map[string]interface{}{"field": "customer_name"}},
		},
	}
	if err := svc.CreateSavedReport(ctx, report); err != nil {
		t.Fatalf("CreateSavedReport: %v", err)
	}

	schedule := &ReportSchedule{
		ReportID:       report.ID,
		CronExpression: cronExpr,
		Recipients:     []string{"controller@example.com", "gm@example.com"},
		Status:         ScheduleStatusActive,
		Format:         "CSV",
	}
	if err := svc.CreateReportSchedule(ctx, schedule); err != nil {
		t.Fatalf("CreateReportSchedule: %v", err)
	}

	t.Cleanup(func() {
		cleanupCtx := context.Background()
		for _, stmt := range []struct {
			sql  string
			args []interface{}
		}{
			{`DELETE FROM report_schedules WHERE report_id = $1`, []interface{}{report.ID}},
			{`DELETE FROM saved_reports WHERE id = $1`, []interface{}{report.ID}},
			{`DELETE FROM invoices WHERE customer_id = $1`, []interface{}{customerID}},
			{`DELETE FROM customers WHERE id = $1`, []interface{}{customerID}},
		} {
			if _, err := db.Pool.Exec(cleanupCtx, stmt.sql, stmt.args...); err != nil {
				t.Errorf("cleanup %q: %v", stmt.sql, err)
			}
		}
	})

	return scheduledReportFixture{
		customerName: customerName,
		reportID:     report.ID,
		schedule:     *schedule,
		service:      svc,
	}
}

// reloadSchedule re-reads the schedule from report_schedules, so what is
// executed is the row as Postgres stored it — recipients decoded back out of
// JSONB included — rather than the in-memory struct that was written.
func reloadSchedule(t *testing.T, svc *Service, id string) ReportSchedule {
	t.Helper()
	schedules, err := svc.ListReportSchedules(context.Background())
	if err != nil {
		t.Fatalf("ListReportSchedules: %v", err)
	}
	for _, s := range schedules {
		if s.ID == id {
			return s
		}
	}
	t.Fatalf("schedule %s is not in report_schedules", id)
	return ReportSchedule{}
}

// CORRECTNESS, and the replacement for the characterization test that used to
// pin the definition-dropping bug: a saved report, scheduled and executed,
// must reach the email sender as a non-empty CSV whose columns and rows are the
// ones the stored definition asked for.
func TestScheduledReport_EndToEndAgainstPostgres(t *testing.T) {
	db := testutil.RequireDB(t)
	fixture := newScheduledReportFixture(t, db, "0 0 9 * * *")

	stored := reloadSchedule(t, fixture.service, fixture.schedule.ID)
	if stored.Status != ScheduleStatusActive {
		t.Errorf("stored status = %q, want %q — Scheduler.Start registers ACTIVE rows only", stored.Status, ScheduleStatusActive)
	}
	if len(stored.Recipients) != 2 {
		t.Fatalf("recipients read back as %v, want the two that were stored", stored.Recipients)
	}

	sender := &fakeSender{}
	sched := NewScheduler(fixture.service, sender)

	if err := sched.ExecuteAndSendReport(context.Background(), stored); err != nil {
		t.Fatalf("ExecuteAndSendReport: %v", err)
	}

	if sender.count() != 1 {
		t.Fatalf("sender received %d emails, want 1", sender.count())
	}
	sent := sender.sent[0]

	// Addressing.
	if strings.Join(sent.to, ",") != strings.Join(stored.Recipients, ",") {
		t.Errorf("sent to %v, want the schedule's recipients %v", sent.to, stored.Recipients)
	}
	if !strings.Contains(sent.subject, "AR by customer") {
		t.Errorf("subject = %q, want it to name the report", sent.subject)
	}
	if !strings.HasPrefix(sent.filename, "AR_by_customer") || !strings.HasSuffix(sent.filename, ".csv") {
		t.Errorf("filename = %q, want a sanitized report name with a .csv extension", sent.filename)
	}

	// The attachment. This is the assertion the old bug could not survive: an
	// undecoded definition has no columns, so BuildAndExecuteQuery rejected it
	// with "no columns selected" and nothing was ever attached.
	if len(sent.content) == 0 {
		t.Fatal("attachment is empty")
	}
	records, err := csv.NewReader(strings.NewReader(string(sent.content))).ReadAll()
	if err != nil {
		t.Fatalf("attachment is not parseable CSV: %v (content %q)", err, sent.content)
	}
	t.Logf("attachment:\n%s", sent.content)

	if len(records) != 2 {
		t.Fatalf("CSV has %d records, want a header and one grouped row: %v", len(records), records)
	}

	// Headers come from the stored column labels, in the stored order.
	if want := []string{"Customer", "Total", "Invoices"}; !equalStrings(records[0], want) {
		t.Errorf("header = %v, want %v — these are the labels stored in definition_json", records[0], want)
	}

	row := records[1]
	if row[0] != fixture.customerName {
		t.Errorf("row customer = %q, want %q", row[0], fixture.customerName)
	}
	// SUM over the two seeded invoices, not either one of them.
	total, err := strconv.ParseFloat(row[1], 64)
	if err != nil {
		t.Fatalf("total %q is not a number: %v", row[1], err)
	}
	if total != 2000.00 {
		t.Errorf("total = %v, want 2000.00 (1234.56 + 765.44) — a dropped aggregation would show one invoice", total)
	}
	// COUNT, proving the GROUP BY collapsed both invoices into this row.
	if row[2] != "2" {
		t.Errorf("invoice count = %q, want 2", row[2])
	}

	// The run is recorded, so an operator can see when it last fired and when
	// it fires next.
	after := reloadSchedule(t, fixture.service, fixture.schedule.ID)
	if after.LastRunAt == nil {
		t.Error("last_run_at is still NULL after a successful run")
	}
	if after.NextRunAt == nil {
		t.Error("next_run_at is still NULL after a successful run")
	}
}

// CORRECTNESS: the cron engine itself must fire a stored schedule. The test
// above proves the report is built and delivered correctly; this proves nobody
// has to call ExecuteAndSendReport by hand for that to happen — Start loads the
// ACTIVE rows out of report_schedules and the engine runs them.
func TestScheduler_StartFiresAStoredScheduleAgainstPostgres(t *testing.T) {
	db := testutil.RequireDB(t)
	// Every second, so the test observes a real tick rather than a simulated
	// one. "@every 1s" is a descriptor, which this package's six-field dialect
	// also accepts.
	fixture := newScheduledReportFixture(t, db, "@every 1s")

	sender := &fakeSender{}
	sched := NewScheduler(fixture.service, sender)

	if err := sched.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer sched.Stop()

	if !sched.isRegistered(fixture.schedule.ID) {
		t.Fatalf("Start did not register the ACTIVE schedule %s", fixture.schedule.ID)
	}

	deadline := time.Now().Add(20 * time.Second)
	var sent sentEmail
	for {
		if e, ok := findEmailFor(sender, fixture.customerName); ok {
			sent = e
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("no scheduled report was delivered within 20s (%d emails sent in total)", sender.count())
		}
		time.Sleep(50 * time.Millisecond)
	}

	if !strings.Contains(string(sent.content), "Customer,Total,Invoices") {
		t.Errorf("cron-fired attachment = %q, want the stored definition's header row", sent.content)
	}
}

// CORRECTNESS: formatValue's pgx cases are only correct if they match what the
// driver really hands back. This pins the three representations that used to
// render as Go internals in every export — and would do so again if a pgx
// upgrade changed them — against a live database rather than against a
// hand-built value.
func TestExport_RendersRealDriverValues(t *testing.T) {
	db := testutil.RequireDB(t)

	rows, err := db.Pool.Query(context.Background(), `
		SELECT '2000.00'::numeric(10,2) AS money,
		       'a75d12e5-28c1-4375-8e62-2895d2c89d96'::uuid AS id,
		       '2026-08-20T16:16:40Z'::timestamptz AS created_at,
		       NULL::numeric AS missing`)
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	defer rows.Close()

	if !rows.Next() {
		t.Fatal("no row")
	}
	values, err := rows.Values()
	if err != nil {
		t.Fatalf("Values: %v", err)
	}

	want := []string{"2000.00", "a75d12e5-28c1-4375-8e62-2895d2c89d96", "", ""}
	// The timestamp is compared after normalizing to UTC, since the session's
	// time zone decides the offset the driver returns.
	for i, value := range values {
		got := formatValue(value)
		if i == 2 {
			parsed, err := time.Parse(time.RFC3339, got)
			if err != nil {
				t.Errorf("timestamp rendered as %q, which is not RFC 3339: %v", got, err)
				continue
			}
			if !parsed.UTC().Equal(time.Date(2026, 8, 20, 16, 16, 40, 0, time.UTC)) {
				t.Errorf("timestamp rendered as %q, want the stored instant", got)
			}
			continue
		}
		if got != want[i] {
			t.Errorf("column %d (%T) rendered as %q, want %q", i, value, got, want[i])
		}
	}
}

// findEmailFor returns the first email whose attachment mentions the fixture's
// customer, so a stray ACTIVE schedule left in the database by something else
// cannot be mistaken for this test's own run.
func findEmailFor(s *fakeSender, customerName string) (sentEmail, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, e := range s.sent {
		if strings.Contains(string(e.content), customerName) {
			return e, true
		}
	}
	return sentEmail{}, false
}

func equalStrings(got, want []string) bool {
	if len(got) != len(want) {
		return false
	}
	for i := range got {
		if got[i] != want[i] {
			return false
		}
	}
	return true
}
