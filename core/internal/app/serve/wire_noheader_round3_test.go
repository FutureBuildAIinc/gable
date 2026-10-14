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

// TestNoHeader_ReportBuilderAndSavedRunFollowTheReach is the report
// builder family (r4 P2-1, and the builder legs of r3 P2-2): the preview,
// the export and the on demand saved run carry the caller's reach on every
// entity (orders, invoices, inventory), so a finance user granted only A
// with no header never reads B's rows on any of the three surfaces, and the
// administrator's primed entry and wide view stay wide. Each leg is red
// when the builder's reach wall or the routes' branch middleware mount is
// reverted alone.
func TestNoHeader_ReportBuilderAndSavedRunFollowTheReach(t *testing.T) {
	f := newKeyPinFixture(t)
	f.seedWalledTwins(t)
	noheaderGrant(t, f, "pin-finance", f.branchA)

	ordersDef := `"definition":{"columns":[{"field":"customer_name","label":"Customer"},{"field":"id","label":"Docs","aggregation":"COUNT"}],` +
		`"filters":[{"field":"customer_name","operator":"LIKE","value":"pin cust"}],"groupings":[{"field":"customer_name"}]}`
	invoicesDef := ordersDef
	// The inventory leg groups by product and sums the yards' quantities:
	// the filter grammar cannot bind a uuid, so the wall is what the
	// assertion reads (A's yard alone sums 7; both yards sum 16).
	inventoryDef := `"definition":{"columns":[{"field":"quantity","label":"Qty","aggregation":"SUM"}],"groupings":[{"field":"product_id"}]}`

	// The per entity preview legs. The administrator primes every entity
	// first, so a shared reach or a missing mount would hand the finance
	// caller the wide rows.
	for _, tc := range []struct {
		name string
		body string
	}{
		{"orders", `{"entity_type":"orders",` + ordersDef + `}`},
		{"invoices", `{"entity_type":"invoices",` + invoicesDef + `}`},
		{"inventory", `{"entity_type":"inventory",` + inventoryDef + `}`},
	} {
		status, adminBuf := f.userAs(t, "admin", "POST", "/api/v1/reporting/builder/preview", tc.body, nil)
		if status != http.StatusOK {
			t.Fatalf("admin prime %s preview: %d %s", tc.name, status, adminBuf)
		}
		status, buf := f.userAs(t, "finance", "POST", "/api/v1/reporting/builder/preview", tc.body, nil)
		if status != http.StatusOK {
			t.Fatalf("no header %s preview: %d %s", tc.name, status, buf)
		}
		if tc.name == "inventory" {
			var rows []struct {
				Qty float64 `json:"quantity"`
			}
			if err := json.Unmarshal(buf, &rows); err != nil {
				t.Fatal(err)
			}
			var adminRows []struct {
				Qty float64 `json:"quantity"`
			}
			if err := json.Unmarshal(adminBuf, &adminRows); err != nil {
				t.Fatal(err)
			}
			if len(rows) != 1 || rows[0].Qty != 7 {
				t.Errorf("no header inventory preview sums %v over %d row(s), want A's yard alone (7 on one row)", rows, len(rows))
			}
			wide := false
			for _, r := range adminRows {
				if r.Qty == 16 {
					wide = true
				}
			}
			if !wide {
				t.Errorf("admin inventory preview %v has no row summing both yards' 16", adminRows)
			}
			continue
		}
		if strings.Contains(string(buf), "pin cust B") || !strings.Contains(string(buf), "pin cust") {
			t.Errorf("no header %s preview carries %s, want A's rows only", tc.name, buf)
		}
		if !strings.Contains(string(adminBuf), "pin cust B") {
			t.Errorf("admin %s preview misses B's rows: %s", tc.name, adminBuf)
		}
	}

	// The export leg, same definition, CSV bytes.
	exportBody := `{"entity_type":"orders","format":"csv",` + ordersDef + `}`
	status, adminBuf := f.userAs(t, "admin", "POST", "/api/v1/reporting/builder/export", exportBody, nil)
	if status != http.StatusOK || !strings.Contains(string(adminBuf), "pin cust B") {
		t.Fatalf("admin orders export: %d %s", status, adminBuf)
	}
	status, buf := f.userAs(t, "finance", "POST", "/api/v1/reporting/builder/export", exportBody, nil)
	if status != http.StatusOK || strings.Contains(string(buf), "pin cust B") || !strings.Contains(string(buf), "pin cust") {
		t.Errorf("no header orders export: %d %s, want A's rows only", status, buf)
	}
	status, buf = f.userAs(t, "finance", "POST", "/api/v1/reporting/builder/export", exportBody, headerA(f))
	if status != http.StatusOK || strings.Contains(string(buf), "pin cust B") {
		t.Errorf("header A orders export: %d %s, want A's rows only", status, buf)
	}

	// The on demand saved run leg: a report the administrator saves runs
	// under the calling finance user's reach, not the saver's.
	saveBody := `{"name":"pin builder reach r3","description":"pin","entity_type":"orders","definition_json":{` +
		`"columns":[{"field":"customer_name","label":"Customer"},{"field":"id","label":"Docs","aggregation":"COUNT"}],` +
		`"filters":[{"field":"customer_name","operator":"LIKE","value":"pin cust"}],"groupings":[{"field":"customer_name"}]}}`
	status, buf = f.userAs(t, "admin", "POST", "/api/v1/reporting/save", saveBody, nil)
	if status != http.StatusOK {
		t.Fatalf("admin saved report: %d %s", status, buf)
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
	status, buf = f.userAs(t, "finance", "POST", "/api/v1/reporting/saved/"+saved.ID+"/run", "", nil)
	if status != http.StatusOK || strings.Contains(string(buf), "pin cust B") || !strings.Contains(string(buf), "pin cust") {
		t.Errorf("no header saved run: %d %s, want A's rows only", status, buf)
	}
	status, buf = f.userAs(t, "admin", "POST", "/api/v1/reporting/saved/"+saved.ID+"/run", "", nil)
	if status != http.StatusOK || !strings.Contains(string(buf), "pin cust B") {
		t.Errorf("admin saved run: %d %s, want the wide rows", status, buf)
	}
}

// TestNoHeader_FulfilmentQueueFollowsTheReach is the fulfilment queue leg
// (r3 P2-2): the queue lists only the granted branches' requests for a no
// header caller, and the administrator keeps both branches'.
func TestNoHeader_FulfilmentQueueFollowsTheReach(t *testing.T) {
	f := newKeyPinFixture(t)
	w := f.seedWalledTwins(t)
	noheaderGrant(t, f, "pin-finance", f.branchA)
	ctx := context.Background()
	for _, q := range []struct{ delivery, order uuid.UUID }{
		{w.delA, w.ordA}, {w.delB, w.ordB},
	} {
		if _, err := f.db.Pool.Exec(ctx,
			`INSERT INTO order_fulfillment_requests (delivery_id, order_id) VALUES ($1, $2)`, q.delivery, q.order); err != nil {
			t.Fatal(err)
		}
	}
	t.Cleanup(func() {
		_, _ = f.db.Pool.Exec(ctx, `DELETE FROM order_fulfillment_requests WHERE order_id IN ($1, $2)`, w.ordA, w.ordB)
	})

	status, buf := f.userAs(t, "finance", "GET", "/api/v1/orders/fulfillment-requests", "", nil)
	if status != http.StatusOK {
		t.Fatalf("no header fulfilment queue: %d %s", status, buf)
	}
	if strings.Contains(string(buf), w.ordB.String()) {
		t.Errorf("no header fulfilment queue carries B's request: %s", buf)
	}
	if !strings.Contains(string(buf), w.ordA.String()) {
		t.Errorf("no header fulfilment queue misses A's request: %s", buf)
	}
	status, buf = f.userAs(t, "admin", "GET", "/api/v1/orders/fulfillment-requests", "", nil)
	if status != http.StatusOK || !strings.Contains(string(buf), w.ordB.String()) {
		t.Errorf("admin fulfilment queue misses B's request: %d %s", status, buf)
	}
}

// TestNoHeader_PosMissingRecordIsNotFound is the pos record family (r4
// P3-1 and P3-2): a void or a till close addressing a record that does not
// exist answers the module's 404 for every caller, never a 403 claiming
// the missing record belongs to another branch and never a bare 500.
func TestNoHeader_PosMissingRecordIsNotFound(t *testing.T) {
	f := newKeyPinFixture(t)
	f.seedWalledTwins(t)
	noheaderGrant(t, f, "pin-cashier", f.branchA)
	missing := uuid.NewString()

	for _, hdr := range []map[string]string{nil, headerA(f)} {
		status, buf := f.userAs(t, "cashier", "POST", "/api/v1/pos/transactions/"+missing+"/void", `{"reason":"pin"}`, hdr)
		if status != http.StatusNotFound {
			t.Errorf("void of a missing sale (header %v): %d %s, want 404", hdr != nil, status, buf)
		}
		status, buf = f.userAs(t, "cashier", "POST", "/api/v1/pos/till/"+missing+"/close", `{"counted_by_method":{}}`, hdr)
		if status != http.StatusNotFound {
			t.Errorf("close of a missing till session (header %v): %d %s, want 404", hdr != nil, status, buf)
		}
	}

	// The control: a record of another branch still answers the record
	// refusal by name, not a 404.
	status, buf := f.userAs(t, "cashier", "POST", "/api/v1/pos/transactions/"+f.txnB.String()+"/void", `{"reason":"pin"}`, nil)
	if status != http.StatusForbidden || !strings.Contains(string(buf), "branch this caller may not target") {
		t.Errorf("void of B's sale: %d %s, want the 403 record refusal", status, buf)
	}
}

// TestNoHeader_CustomerAccountSurface is the customer account family (r4
// P2-4, carried from r2 P3-2 and r3 P3-2): the account summary, its
// subledger and the AR statement answer a customer outside the caller's
// reach as missing, the same 404 /customers/{id} serves, with or without
// the header.
func TestNoHeader_CustomerAccountSurface(t *testing.T) {
	f := newKeyPinFixture(t)
	f.seedWalledTwins(t)
	noheaderGrant(t, f, "pin-sales", f.branchA)
	noheaderGrant(t, f, "pin-finance", f.branchA)

	paths := []string{
		"/api/v1/accounts/" + f.custB.String(),
		"/api/v1/accounts/" + f.custB.String() + "/transactions",
		"/api/v1/ar/customers/" + f.custB.String() + "/statement",
	}
	for _, role := range []string{"sales", "finance"} {
		for _, hdr := range []map[string]string{nil, headerA(f)} {
			for _, p := range paths {
				status, buf := f.userAs(t, role, "GET", p, "", hdr)
				if status != http.StatusNotFound {
					t.Errorf("%s no header %t read of B's %s: %d %s, want 404", role, hdr != nil, p, status, buf)
				}
			}
		}
	}

	// The controls: A's customer answers every route, and an owner with no
	// header keeps B.
	for _, p := range []string{
		"/api/v1/accounts/" + f.custID.String(),
		"/api/v1/accounts/" + f.custID.String() + "/transactions",
		"/api/v1/ar/customers/" + f.custID.String() + "/statement",
	} {
		status, buf := f.userAs(t, "finance", "GET", p, "", nil)
		if status != http.StatusOK {
			t.Errorf("no header read of A's %s: %d %s, want 200", p, status, buf)
		}
	}
	status, buf := f.userAs(t, "owner", "GET", "/api/v1/ar/customers/"+f.custB.String()+"/statement", "", nil)
	if status != http.StatusOK {
		t.Errorf("owner no header statement of B's customer: %d %s, want 200", status, buf)
	}
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
