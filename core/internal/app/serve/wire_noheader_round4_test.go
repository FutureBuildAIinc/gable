// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package serve

// SEC-NOHEADER-1 round 4: the findings the r5 Sonnet and r6 Opus reviews
// name on the round 3 head. Each leg is red when the guard it pins is
// reverted alone.

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/gablelbm/gable/internal/reporting"
	"github.com/gablelbm/gable/pkg/audit"
	"github.com/google/uuid"
)

// TestNoHeader_TaxExemptionsHoldTheCustomerWall is the tax exemption family
// (r6 P1-1, r5 P2-2): a finance user granted only A, with no header or with
// header A, and a finance user with no grants at all, read, create and
// delete nothing of B's customer and preview no tax for it or its ship to,
// while A's customer answers as it did and an owner with no header keeps
// every branch.
func TestNoHeader_TaxExemptionsHoldTheCustomerWall(t *testing.T) {
	f := newKeyPinFixture(t)
	ctx := context.Background()
	noheaderGrant(t, f, "pin-finance", f.branchA)
	A, B := f.custID.String(), f.custB.String()

	shipB := uuid.New()
	if _, err := f.db.Pool.Exec(ctx,
		`INSERT INTO customer_ship_tos (id, customer_id, code, name, line1) VALUES ($1, $2, 'pin', 'pin ship to', 'pin')`,
		shipB, f.custB); err != nil {
		t.Fatalf("seed ship to: %v", err)
	}
	create := func(customer string) string {
		return fmt.Sprintf(`{"customer_id":%q,"exempt_reason":"pin"}`, customer)
	}
	preview := func(customer string) string {
		return fmt.Sprintf(`{"customer_id":%q,"document_type":"SalesInvoice","lines":[{"amount":100}]}`, customer)
	}
	previewShip := fmt.Sprintf(`{"ship_to_id":%q,"document_type":"SalesInvoice","lines":[{"amount":100}]}`, shipB)

	type caller struct {
		label string
		send  func(method, path, body string) (int, []byte)
	}
	callers := []caller{
		{"finance granted A, no header", func(m, p, b string) (int, []byte) {
			return f.userAs(t, "finance", m, p, b, nil)
		}},
		{"finance granted A, header A", func(m, p, b string) (int, []byte) {
			return f.userAs(t, "finance", m, p, b, headerA(f))
		}},
		{"finance with no grants", func(m, p, b string) (int, []byte) {
			return f.userSubAs(t, "pin-nogrants-tax", "finance", m, p, b, nil)
		}},
	}
	for _, c := range callers {
		before := f.exemptionsOf(t, f.custB)
		if status, buf := c.send("GET", "/api/v1/tax/exemptions/"+B, ""); status != http.StatusNotFound {
			t.Errorf("%s: read of B's exemptions: %d %s, want 404", c.label, status, buf)
		}
		status, buf := c.send("POST", "/api/v1/tax/exemptions", create(B))
		if status != http.StatusForbidden || !strings.Contains(string(buf), "customer_id") {
			t.Errorf("%s: create on B's customer: %d %s, want the 403 naming customer_id", c.label, status, buf)
		}
		if status, buf := c.send("DELETE", "/api/v1/tax/exemptions/"+f.taxExempt.String(), ""); status != http.StatusNotFound {
			t.Errorf("%s: delete of B's exemption: %d %s, want 404", c.label, status, buf)
		}
		if n := f.exemptionsOf(t, f.custB); n != before {
			t.Errorf("%s: B's customer holds %d exemptions after the refused verbs, want %d", c.label, n, before)
		}
		for name, body := range map[string]string{"customer": preview(B), "ship to": previewShip} {
			status, buf := c.send("POST", "/api/v1/tax/preview", body)
			if status != http.StatusForbidden || !strings.Contains(string(buf), "customer_id") {
				t.Errorf("%s: preview naming B's %s: %d %s, want the 403 naming customer_id", c.label, name, status, buf)
			}
		}
	}

	// The control: A's customer answers every verb for the granted caller.
	for _, c := range callers[:2] {
		if status, buf := c.send("GET", "/api/v1/tax/exemptions/"+A, ""); status != http.StatusOK {
			t.Errorf("%s: read of A's exemptions: %d %s, want 200", c.label, status, buf)
		}
		if status, buf := c.send("POST", "/api/v1/tax/exemptions", create(A)); status != http.StatusCreated {
			t.Errorf("%s: create on A's customer: %d %s, want 201", c.label, status, buf)
		}
		own := uuid.New()
		f.seedTaxExempt(t, own, f.custID)
		if status, buf := c.send("DELETE", "/api/v1/tax/exemptions/"+own.String(), ""); status != http.StatusNoContent {
			t.Errorf("%s: delete of A's exemption: %d %s, want 204", c.label, status, buf)
		}
		if status, buf := c.send("POST", "/api/v1/tax/preview", preview(A)); status != http.StatusOK {
			t.Errorf("%s: preview naming A's customer: %d %s, want 200", c.label, status, buf)
		}
	}

	// The owner with no header keeps every branch.
	if status, buf := f.userAs(t, "owner", "GET", "/api/v1/tax/exemptions/"+B, "", nil); status != http.StatusOK {
		t.Errorf("owner no header read of B's exemptions: %d %s, want 200", status, buf)
	}
	if status, buf := f.userAs(t, "owner", "POST", "/api/v1/tax/exemptions", create(B), nil); status != http.StatusCreated {
		t.Errorf("owner no header create on B's customer: %d %s, want 201", status, buf)
	}
	row := uuid.New()
	f.seedTaxExempt(t, row, f.custB)
	if status, buf := f.userAs(t, "owner", "DELETE", "/api/v1/tax/exemptions/"+row.String(), "", nil); status != http.StatusNoContent {
		t.Errorf("owner no header delete of B's exemption: %d %s, want 204", status, buf)
	}
	if status, buf := f.userAs(t, "owner", "POST", "/api/v1/tax/preview", preview(B), nil); status != http.StatusOK {
		t.Errorf("owner no header preview naming B's customer: %d %s, want 200", status, buf)
	}
}

// rowlessCustomer inserts a customer with a primary branch and no
// customer_branches row, the shape the seed wrote before it mirrored the
// row (r5 P2-1), carrying a balance so the reconciliation can see it.
func rowlessCustomer(t *testing.T, f *keyPinFixture) uuid.UUID {
	t.Helper()
	id := uuid.New()
	if _, err := f.db.Pool.Exec(context.Background(),
		`INSERT INTO customers (id, name, account_number, primary_branch_id, balance_due)
		VALUES ($1, 'pin rowless', $2, $3, 0)`, id, "PINR-"+id.String()[:8], f.branchA); err != nil {
		t.Fatalf("seed rowless customer: %v", err)
	}
	t.Cleanup(func() {
		_, _ = f.db.Pool.Exec(context.Background(), `DELETE FROM customers WHERE id = $1`, id)
	})
	return id
}

// devCaller sends one request with no claims, the AUTH_MODE=dev caller.
func devCaller(t *testing.T, f *keyPinFixture, path string) int {
	t.Helper()
	res, err := http.Get(f.srv.URL + path)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	return res.StatusCode
}

// accountSurface is the three routes the account wall holds.
func accountSurface(customer uuid.UUID) []string {
	return []string{
		"/api/v1/accounts/" + customer.String(),
		"/api/v1/accounts/" + customer.String() + "/transactions",
		"/api/v1/ar/customers/" + customer.String() + "/statement",
	}
}

// TestNoHeader_AccountWallKeepsRowlessCustomersForTheWideCallers is the
// account wall's both nil arm (r5 P2-1): a customer with a primary branch and
// no customer_branches row is read on the account, transactions and AR
// statement routes by an owner or an administrator with no header and by the
// dev caller, and its balance is in the owner's reconciliation, while a
// finance user held to its grants still meets the fail closed 404.
func TestNoHeader_AccountWallKeepsRowlessCustomersForTheWideCallers(t *testing.T) {
	f := newKeyPinFixture(t)
	noheaderGrant(t, f, "pin-finance", f.branchA)
	rowless := rowlessCustomer(t, f)

	for _, p := range accountSurface(rowless) {
		for _, role := range []string{"owner", "admin"} {
			if status, buf := f.userAs(t, role, "GET", p, "", nil); status != http.StatusOK {
				t.Errorf("%s no header read of the rowless customer's %s: %d %s, want 200", role, p, status, buf)
			}
		}
		if status := devCaller(t, f, p); status != http.StatusOK {
			t.Errorf("dev caller read of the rowless customer's %s: %d, want 200", p, status)
		}
		for _, hdr := range []map[string]string{nil, headerA(f)} {
			if status, buf := f.userAs(t, "finance", "GET", p, "", hdr); status != http.StatusNotFound {
				t.Errorf("finance granted A (header %t) read of the rowless customer's %s: %d %s, want the fail closed 404", hdr != nil, p, status, buf)
			}
		}
	}

	balanceSum := func() int64 {
		status, buf := f.userAs(t, "owner", "GET", "/api/v1/ar/reconciliation", "", nil)
		if status != http.StatusOK {
			t.Fatalf("owner reconciliation: %d %s", status, buf)
		}
		var recon struct {
			Currencies []struct {
				Currency string `json:"currency"`
				Balance  int64  `json:"balance_sum_cents"`
			} `json:"currencies"`
		}
		if err := json.Unmarshal(buf, &recon); err != nil {
			t.Fatal(err)
		}
		for _, c := range recon.Currencies {
			if c.Currency == "USD" {
				return c.Balance
			}
		}
		return 0
	}
	before := balanceSum()
	if _, err := f.db.Pool.Exec(context.Background(), `UPDATE customers SET balance_due = 43.21 WHERE id = $1`, rowless); err != nil {
		t.Fatal(err)
	}
	if after := balanceSum(); after-before != 4321 {
		t.Errorf("raising the rowless customer's balance by 4321 moved the owner's balance_sum_cents by %d, want 4321", after-before)
	}

}

// TestNoHeader_AccountWallSwitchOffKeepsRowlessCustomers is the same arm
// under the single branch switch: every caller is treated as wide, a finance
// user with no grants included (r5 P2-1).
func TestNoHeader_AccountWallSwitchOffKeepsRowlessCustomers(t *testing.T) {
	f := newKeyPinFixtureSwitched(t, "false")
	rowless := rowlessCustomer(t, f)
	for _, p := range accountSurface(rowless) {
		if status, buf := f.userSubAs(t, "pin-nogrants-acct", "finance", "GET", p, "", nil); status != http.StatusOK {
			t.Errorf("switch off: finance with no grants read of the rowless customer's %s: %d %s, want 200", p, status, buf)
		}
	}
}

// saveAndScheduleAs saves a report as the role given and schedules it, and
// returns the stored schedule the way Scheduler.Start reloads it. The saved
// report counts the orders of the fixture's two customers.
func saveAndScheduleAs(t *testing.T, f *keyPinFixture, role string, hdr map[string]string) (reporting.ReportSchedule, *reporting.Service) {
	t.Helper()
	saveBody := `{"name":"pin noheader schedule r4","description":"pin","entity_type":"orders",` +
		`"definition_json":{"columns":[{"field":"customer_name","label":"Customer"},{"field":"id","label":"Orders","aggregation":"COUNT"}],` +
		`"filters":[{"field":"customer_name","operator":"LIKE","value":"pin cust"}],` +
		`"groupings":[{"field":"customer_name"}]}}`
	status, buf := f.userAs(t, role, "POST", "/api/v1/reporting/save", saveBody, hdr)
	if status != http.StatusOK {
		t.Fatalf("%s saved report: %d %s", role, status, buf)
	}
	var saved struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(buf, &saved); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = f.db.Pool.Exec(context.Background(), `DELETE FROM audit_log WHERE entity_type = 'report_schedule'
			AND entity_id IN (SELECT id FROM report_schedules WHERE report_id = $1)`, saved.ID)
		_, _ = f.db.Pool.Exec(context.Background(), `DELETE FROM report_schedules WHERE report_id = $1`, saved.ID)
		_, _ = f.db.Pool.Exec(context.Background(), `DELETE FROM saved_reports WHERE id = $1`, saved.ID)
	})
	schedBody := `{"report_id":"` + saved.ID + `","cron_expression":"0 0 9 * * *","recipients":["probe@example.invalid"]}`
	status, buf = f.userAs(t, role, "POST", "/api/v1/reporting/schedules", schedBody, hdr)
	if status != http.StatusCreated {
		t.Fatalf("%s scheduled report: %d %s", role, status, buf)
	}
	svc := reporting.NewService(reporting.NewRepository(f.db))
	schedules, err := svc.ListReportSchedules(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	for i := range schedules {
		if schedules[i].ReportID == saved.ID {
			return schedules[i], svc
		}
	}
	t.Fatalf("the %s caller's schedule is not stored", role)
	return reporting.ReportSchedule{}, nil
}

// TestNoHeader_ReportScheduleUnderTheSingleBranchSwitch is the scheduler
// under the switch the branch middleware reads (r6 P2-1): while
// multi_branch_enabled is off every caller is treated as wide, so a finance
// creator with no grants at all gets the run and the rows of every branch,
// as the creator's own on demand run does. The scheduler reads the setting
// at each run: with the switch on, the same schedule meets item 65's rule
// (a creator with no grants does not run, audited) at the very next run.
func TestNoHeader_ReportScheduleUnderTheSingleBranchSwitch(t *testing.T) {
	f := newKeyPinFixtureSwitched(t, "false")
	f.seedWalledTwins(t)
	stored, svc := saveAndScheduleAs(t, f, "finance", nil)
	if stored.CreatedBy != "pin-finance" || stored.CreatorIsAdmin {
		t.Fatalf("the finance caller's schedule records creator %q admin %t", stored.CreatedBy, stored.CreatorIsAdmin)
	}
	sender := &r3CaptureSender{}
	sched := reporting.NewScheduler(svc, sender).WithRefusalAudit(audit.NewLogger(f.db))

	if err := sched.ExecuteAndSendReport(context.Background(), stored); err != nil {
		t.Fatalf("switch off: a finance creator with no grants did not run: %v", err)
	}
	content := sender.last()
	if !strings.Contains(content, "pin cust B") || !strings.Contains(content, "pin cust") {
		t.Errorf("switch off: the run misses a branch's rows, want every branch: %s", content)
	}

	// The switch on: the next run of the same schedule is held to the
	// creator's grants, and a creator with none does not run.
	setSetting(t, f.db, "multi_branch_enabled", "true")
	before := sender.count()
	if err := sched.ExecuteAndSendReport(context.Background(), stored); err == nil {
		t.Error("switch on: a finance creator with no grants ran, want the fail closed refusal")
	}
	if sender.count() != before {
		t.Errorf("switch on: the refused run sent %d run(s), want none", sender.count()-before)
	}
}
