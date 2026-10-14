// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package serve

// SEC-NOHEADER-1 round 4: the findings the r5 Sonnet and r6 Opus reviews
// name on the round 3 head. Each leg is red when the guard it pins is
// reverted alone.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gablelbm/gable/internal/reporting"
	"github.com/gablelbm/gable/pkg/audit"
	"github.com/gablelbm/gable/pkg/middleware"
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

// saveReportAs saves a report as the role given and removes it, with its
// schedules and their audit rows, when the test ends. The saved report counts
// the orders of the fixture's two customers.
func saveReportAs(t *testing.T, f *keyPinFixture, role string, hdr map[string]string) string {
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
	return saved.ID
}

// scheduleBody is the create body for a saved report.
func scheduleBody(reportID string) string {
	return `{"report_id":"` + reportID + `","cron_expression":"0 0 9 * * *","recipients":["probe@example.invalid"]}`
}

// saveAndScheduleAs saves a report and schedules it as the role given, and
// returns the stored schedule the way Scheduler.Start reloads it.
func saveAndScheduleAs(t *testing.T, f *keyPinFixture, role string, hdr map[string]string) (reporting.ReportSchedule, *reporting.Service) {
	t.Helper()
	reportID := saveReportAs(t, f, role, hdr)
	status, buf := f.userAs(t, role, "POST", "/api/v1/reporting/schedules", scheduleBody(reportID), hdr)
	if status != http.StatusCreated {
		t.Fatalf("%s scheduled report: %d %s", role, status, buf)
	}
	svc := reporting.NewService(reporting.NewRepository(f.db))
	schedules, err := svc.ListReportSchedules(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	for i := range schedules {
		if schedules[i].ReportID == reportID {
			return schedules[i], svc
		}
	}
	t.Fatalf("the %s caller's schedule is not stored", role)
	return reporting.ReportSchedule{}, nil
}

// refusalRows counts the report_schedule.refused audit rows of a schedule and
// the reasons they carry.
func refusalRows(t *testing.T, f *keyPinFixture, schedule string) (n int, reasons []string) {
	t.Helper()
	rows, err := f.db.Pool.Query(context.Background(),
		`SELECT COALESCE(changes->>'reason', '') FROM audit_log
		  WHERE entity_type = 'report_schedule' AND entity_id = $1 AND action = 'report_schedule.refused'`, schedule)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	for rows.Next() {
		var reason string
		if err := rows.Scan(&reason); err != nil {
			t.Fatal(err)
		}
		reasons = append(reasons, reason)
	}
	return len(reasons), reasons
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

// TestNoHeader_ReportScheduleGuardsHold pins the scheduler's guards the
// round 3 reviews found untested (r5 P3-1, r6 P3-3), each red when it is
// reverted alone: a creator that holds no branch grant does not run and is
// audited, the refusal audit is wired by the function serve builds the
// scheduler through, a schedule made with no caller identity is refused at
// the create (it could never run), and a body naming created_by or
// creator_is_admin is never stored.
func TestNoHeader_ReportScheduleGuardsHold(t *testing.T) {
	f := newKeyPinFixture(t)
	f.seedWalledTwins(t)

	// A finance creator with a subject and no grant at all: the run is
	// refused, audited with its reason, and sends nothing.
	stored, svc := saveAndScheduleAs(t, f, "finance", nil)
	sender := &r3CaptureSender{}
	sched := newReportScheduler(f.db, svc, sender)
	if err := sched.ExecuteAndSendReport(context.Background(), stored); err == nil {
		t.Error("a finance creator holding no grant ran, want the fail closed refusal")
	}
	if sender.count() != 0 {
		t.Errorf("a creator holding no grant sent %d run(s), want none", sender.count())
	}
	if n, reasons := refusalRows(t, f, stored.ID); n != 1 || !strings.Contains(reasons[0], "no branch grant") {
		t.Errorf("the no grant refusal wrote %d audit row(s) %v, want one naming the missing grant", n, reasons)
	}

	// The refusal audit is wired by the function serve builds the scheduler
	// through: a schedule with no creator recorded writes its row.
	nullID := uuid.NewString()
	if _, err := f.db.Pool.Exec(context.Background(), `
		INSERT INTO report_schedules (id, report_id, cron_expression, recipients, status, format)
		VALUES ($1, $2, '0 0 9 * * *', '["probe@example.invalid"]', 'ACTIVE', 'CSV')`, nullID, stored.ReportID); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = f.db.Pool.Exec(context.Background(), `DELETE FROM audit_log WHERE entity_type = 'report_schedule' AND entity_id = $1`, nullID)
	})
	if err := sched.ExecuteAndSendReport(context.Background(), reporting.ReportSchedule{
		ID: nullID, ReportID: stored.ReportID, CronExpression: "0 0 9 * * *",
		Recipients: []string{"probe@example.invalid"}, Status: reporting.ScheduleStatusActive, Format: "CSV",
	}); err == nil {
		t.Error("a schedule with no creator recorded ran")
	}
	if n, _ := refusalRows(t, f, nullID); n != 1 {
		t.Errorf("the scheduler serve builds wrote %d refusal audit row(s) for a schedule with no creator, want one", n)
	}

	// A caller with no identity (the dev caller, a token with an empty
	// subject) is refused at the create, 409 naming the missing identity,
	// and a body naming created_by or creator_is_admin is never stored.
	reportID := saveReportAs(t, f, "admin", nil)
	forged := `{"report_id":"` + reportID + `","cron_expression":"0 0 9 * * *","recipients":["probe@example.invalid"],` +
		`"created_by":"forged-owner","creator_is_admin":true}`
	for label, send := range map[string]func() (int, []byte){
		"empty subject": func() (int, []byte) {
			return f.userSubAs(t, "", "finance", "POST", "/api/v1/reporting/schedules", forged, nil)
		},
		"no claims": func() (int, []byte) {
			req, err := http.NewRequest("POST", f.srv.URL+"/api/v1/reporting/schedules", strings.NewReader(forged))
			if err != nil {
				t.Fatal(err)
			}
			req.Header.Set("Content-Type", "application/json")
			res, err := http.DefaultClient.Do(req)
			if err != nil {
				t.Fatal(err)
			}
			defer res.Body.Close()
			buf, _ := io.ReadAll(res.Body)
			return res.StatusCode, buf
		},
	} {
		status, buf := send()
		if status != http.StatusConflict || !strings.Contains(string(buf), "caller identity") {
			t.Errorf("%s: create with no caller identity: %d %s, want the 409 naming the missing caller identity", label, status, buf)
		}
		var n int
		if err := f.db.Pool.QueryRow(context.Background(),
			`SELECT COUNT(*) FROM report_schedules WHERE report_id = $1`, reportID).Scan(&n); err != nil {
			t.Fatal(err)
		}
		if n != 0 {
			t.Errorf("%s: the refused create stored %d schedule(s), want none", label, n)
		}
	}
}

// posHiddenLegs is the acts on a branch B record a header A cashier and a key
// bound to A can reach: each answers what a missing record answers.
func posHiddenLegs(f *keyPinFixture) []struct{ label, method, path, body string } {
	tx, till, line := f.txnB.String(), f.tillB.String(), f.lineB.String()
	return []struct{ label, method, path, body string }{
		{"close the till session", "POST", "/api/v1/pos/till/" + till + "/close", `{"counted_by_method":{"cash":5000}}`},
		{"add a line", "POST", "/api/v1/pos/transactions/" + tx + "/items", `{"line":{"product_id":"` + f.posProd.String() + `","quantity":"1","uom":"EA"}}`},
		{"remove a line", "DELETE", "/api/v1/pos/transactions/" + tx + "/items/" + line, ""},
		{"complete the sale", "POST", "/api/v1/pos/transactions/" + tx + "/complete", `{"tenders":[{"method":"cash","amount_cents":5000}]}`},
		{"void the sale", "POST", "/api/v1/pos/transactions/" + tx + "/void", `{"reason":"pin"}`},
	}
}

// posBState is everything the hidden legs could write on B's session and
// live sale: the session's status and Z reports, the sale's status and total
// and its lines.
func posBState(t *testing.T, f *keyPinFixture) string {
	t.Helper()
	var state string
	if err := f.db.Pool.QueryRow(context.Background(), `
		SELECT (SELECT status FROM till_sessions WHERE id = $1) || '/' ||
		       (SELECT COUNT(*) FROM till_z_reports WHERE till_session_id = $1)::text || '/' ||
		       (SELECT status || ':' || total::text FROM pos_transactions WHERE id = $2) || '/' ||
		       (SELECT COUNT(*) FROM pos_line_items WHERE transaction_id = $2)::text`,
		f.tillB, f.txnB).Scan(&state); err != nil {
		t.Fatal(err)
	}
	return state
}

// TestNoHeader_PosHiddenRecordsAnswerMissing is the pos hidden record family
// (r6 P2-4, r5 P3-3): a header A cashier and a key bound to A, acting on B's
// open session and B's live sale, meet the module's 404 that a missing record
// meets (close, add, remove, complete, void), with nothing written.
func TestNoHeader_PosHiddenRecordsAnswerMissing(t *testing.T) {
	f := newKeyPinFixture(t)
	noheaderGrant(t, f, "pin-cashier", f.branchA)
	missing := uuid.NewString()
	before := posBState(t, f)
	for _, l := range posHiddenLegs(f) {
		missingPath := strings.NewReplacer(f.tillB.String(), missing, f.txnB.String(), missing, f.lineB.String(), missing).Replace(l.path)
		for who, send := range map[string]func(path string) (int, []byte){
			"header A cashier": func(path string) (int, []byte) { return f.userAs(t, "cashier", l.method, path, l.body, headerA(f)) },
			"key bound to A":   func(path string) (int, []byte) { return f.key(t, f.bound, l.method, path, l.body, nil) },
		} {
			status, buf := send(l.path)
			if status != http.StatusNotFound {
				t.Errorf("%s: %s of B's record: %d %s, want the missing record's 404", who, l.label, status, buf)
			}
			if mStatus, _ := send(missingPath); mStatus != http.StatusNotFound {
				t.Errorf("%s: %s of a missing record: %d, want 404", who, l.label, mStatus)
			}
		}
	}
	if after := posBState(t, f); after != before {
		t.Errorf("the hidden legs changed B's session or sale: %s, was %s", after, before)
	}
}

// TestNoHeader_PosVoidOfAHiddenSaleIsMissing is the named leg for the void
// mapping 0bc985c2 made (r5 and r6 disagreed whether any test pinned it): a
// header A cashier voids B's live sale and meets the missing sale's 404, with
// nothing written. Red when getTransaction's no rows mapping is reverted
// alone (the answer is the bare 500 then).
func TestNoHeader_PosVoidOfAHiddenSaleIsMissing(t *testing.T) {
	f := newKeyPinFixture(t)
	noheaderGrant(t, f, "pin-cashier", f.branchA)
	before := posBState(t, f)
	status, buf := f.userAs(t, "cashier", "POST", "/api/v1/pos/transactions/"+f.txnB.String()+"/void", `{"reason":"pin"}`, headerA(f))
	if status != http.StatusNotFound {
		t.Errorf("header A cashier void of B's live sale: %d %s, want the missing sale's 404", status, buf)
	}
	if mStatus, _ := f.userAs(t, "cashier", "POST", "/api/v1/pos/transactions/"+uuid.NewString()+"/void", `{"reason":"pin"}`, headerA(f)); mStatus != status {
		t.Errorf("a missing sale answers %d, B's hidden sale %d, want the same", mStatus, status)
	}
	if after := posBState(t, f); after != before {
		t.Errorf("the void changed B's sale: %s, was %s", after, before)
	}
}

// TestExposureUserBranchWall_FailsClosed is the exposure by id wall's lookup
// (r5 P3-4): a lookup error answers 500 and a quote that does not exist 404,
// and neither hands the request on to the handler (the by id exposure writes
// have no wall of their own beneath this one). The control: a quote of the
// context branch passes.
func TestExposureUserBranchWall_FailsClosed(t *testing.T) {
	quote, branch := uuid.New(), uuid.New()
	for _, c := range []struct {
		name   string
		lookup func(ctx context.Context, id uuid.UUID) (*uuid.UUID, error)
		want   int
		called bool
	}{
		{"lookup error", func(context.Context, uuid.UUID) (*uuid.UUID, error) {
			return nil, errors.New("injected lookup failure")
		},
			http.StatusInternalServerError, false},
		{"missing quote", func(context.Context, uuid.UUID) (*uuid.UUID, error) { return nil, nil },
			http.StatusNotFound, false},
		{"quote of the context branch", func(context.Context, uuid.UUID) (*uuid.UUID, error) { return &branch, nil },
			http.StatusOK, true},
	} {
		t.Run(c.name, func(t *testing.T) {
			called := false
			next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { called = true })
			mux := http.NewServeMux()
			mux.Handle("POST /api/v1/quotes/{id}/exposure/acknowledge",
				exposureUserBranchWall(middleware.NewBranchGuard(nil), c.lookup)(next))
			req := httptest.NewRequest("POST", "/api/v1/quotes/"+quote.String()+"/exposure/acknowledge", nil)
			req = req.WithContext(middleware.WithBranchContext(req.Context(), &middleware.BranchContext{BranchID: &branch, UserSub: "pin-sales"}))
			rec := httptest.NewRecorder()
			mux.ServeHTTP(rec, req)
			if rec.Code != c.want || called != c.called {
				t.Errorf("%s: answered %d with the handler called %t, want %d and %t", c.name, rec.Code, called, c.want, c.called)
			}
		})
	}
}
