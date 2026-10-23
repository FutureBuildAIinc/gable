// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package pos_test

// CP-02: an account that requires a job on a charge sale. The rule mirrors
// the purchase order rule: any amount on the account with no job picked is
// refused 409 job_required, before a card is charged and again under the
// credit lock; an account that requires a job and has none on file is refused
// with its own words; cash and the other non account tenders never ask.

import (
	"net/http"
	"strings"
	"testing"

	"github.com/gablelbm/gable/internal/payment"
	"github.com/gablelbm/gable/internal/testutil"
	"github.com/google/uuid"
)

// RULE: a charge sale of an account that requires a job is refused without
// one (409 job_required, nothing moved), and completes with one.
func TestCP02_JobRequiredRefusesChargeWithoutJob(t *testing.T) {
	testutil.LockOutboxTables(t)
	f := newFixture(t, testutil.RequireDB(t))
	f.openTill(0)
	job := uuid.New()
	mustExec(t, f.db, `INSERT INTO projects (id, customer_id, name, status) VALUES ($1, $2, 'Ridge Build', 'Active')`, job, f.customerID)
	mustExec(t, f.db, `UPDATE customers SET job_required = TRUE WHERE id = $1`, f.customerID)
	t.Cleanup(func() { mustExec(t, f.db, `UPDATE customers SET job_required = FALSE WHERE id = $1`, f.customerID) })

	saleID := f.startSale(&f.customerID)
	if r := f.addLine(saleID, f.productLine("1")); r.status != http.StatusOK {
		t.Fatalf("add line = %d: %s", r.status, r.raw)
	}
	total := num(t, f.getSale(t, saleID), "total_cents")
	r := f.completeSale(saleID, tender("account", total))
	if r.status != http.StatusConflict || !strings.Contains(string(r.raw), "job_required") {
		t.Fatalf("a charge with no job = %d: %s, want 409 job_required", r.status, r.raw)
	}
	if !strings.Contains(string(r.raw), "requires a job on every charge sale") {
		t.Errorf("the refusal says %s, want the pick-a-job words", r.raw)
	}
	if got := f.scalar(`SELECT count(*) FROM invoices WHERE customer_id = $1`, f.customerID).(int64); got != 0 {
		t.Errorf("%d invoices after the refused charge, want none", got)
	}
	// the lookup carries the flag so the screen can mark the Job select
	c := standingOf(t, f.findCustomers("Counter Co"))
	if c["job_required"] != true {
		t.Errorf("the lookup carries job_required = %v, want true", c["job_required"])
	}
}

// RULE: the charge with a job picked completes and the invoice carries the
// job the sale named.
func TestCP02_JobRequiredChargeWithJobCompletes(t *testing.T) {
	testutil.LockOutboxTables(t)
	f := newFixture(t, testutil.RequireDB(t))
	f.openTill(0)
	job := uuid.New()
	mustExec(t, f.db, `INSERT INTO projects (id, customer_id, name, status) VALUES ($1, $2, 'Ridge Build', 'Active')`, job, f.customerID)
	mustExec(t, f.db, `UPDATE customers SET job_required = TRUE WHERE id = $1`, f.customerID)
	t.Cleanup(func() { mustExec(t, f.db, `UPDATE customers SET job_required = FALSE WHERE id = $1`, f.customerID) })

	saleID := f.startSale(&f.customerID)
	if r := f.attach(saleID, map[string]any{"customer_id": f.customerID.String(), "job_id": job.String()}); r.status != http.StatusOK {
		t.Fatalf("attach the job = %d: %s", r.status, r.raw)
	}
	if r := f.addLine(saleID, f.productLine("1")); r.status != http.StatusOK {
		t.Fatalf("add line = %d: %s", r.status, r.raw)
	}
	total := num(t, f.getSale(t, saleID), "total_cents")
	r := f.completeSale(saleID, tender("account", total))
	if r.status != http.StatusOK {
		t.Fatalf("a charge with the job = %d: %s", r.status, r.raw)
	}
	if str(t, r.body, "job_id") != job.String() {
		t.Errorf("the completed sale carries job_id = %v, want the job", str(t, r.body, "job_id"))
	}
	inv := str(t, r.body, "invoice_id")
	if got := f.scalar(`SELECT project_id::text FROM invoices WHERE id = $1`, uuid.MustParse(inv)); got != job.String() {
		t.Errorf("invoice project_id = %v, want the job %s", got, job)
	}
	f.assertARInvariants(t)
}

// RULE: an account that requires a job and has none on file is refused with
// its own words: the clerk is sent to the account page.
func TestCP02_JobRequiredNoJobsOnFile(t *testing.T) {
	testutil.LockOutboxTables(t)
	f := newFixture(t, testutil.RequireDB(t))
	f.openTill(0)
	mustExec(t, f.db, `UPDATE customers SET job_required = TRUE WHERE id = $1`, f.customerID)
	t.Cleanup(func() { mustExec(t, f.db, `UPDATE customers SET job_required = FALSE WHERE id = $1`, f.customerID) })

	saleID := f.startSale(&f.customerID)
	if r := f.addLine(saleID, f.productLine("1")); r.status != http.StatusOK {
		t.Fatalf("add line = %d: %s", r.status, r.raw)
	}
	total := num(t, f.getSale(t, saleID), "total_cents")
	r := f.completeSale(saleID, tender("account", total))
	if r.status != http.StatusConflict || !strings.Contains(string(r.raw), "job_required") {
		t.Fatalf("a charge with no job on file = %d: %s, want 409 job_required", r.status, r.raw)
	}
	if !strings.Contains(string(r.raw), "none on file") || !strings.Contains(string(r.raw), "account page") {
		t.Errorf("the refusal says %s, want the none-on-file words", r.raw)
	}
	if got := f.scalar(`SELECT count(*) FROM invoices WHERE customer_id = $1`, f.customerID).(int64); got != 0 {
		t.Errorf("%d invoices after the refused charge, want none", got)
	}
}

// RULE: the job rule is the account tender's, as the purchase order rule is:
// a cash sale of the same account needs no job.
func TestCP02_JobRequiredCashSaleUnaffected(t *testing.T) {
	testutil.LockOutboxTables(t)
	f := newFixture(t, testutil.RequireDB(t))
	f.openTill(0)
	mustExec(t, f.db, `UPDATE customers SET job_required = TRUE WHERE id = $1`, f.customerID)
	t.Cleanup(func() { mustExec(t, f.db, `UPDATE customers SET job_required = FALSE WHERE id = $1`, f.customerID) })

	saleID := f.startSale(&f.customerID)
	if r := f.addLine(saleID, f.productLine("1")); r.status != http.StatusOK {
		t.Fatalf("add line = %d: %s", r.status, r.raw)
	}
	total := num(t, f.getSale(t, saleID), "total_cents")
	if r := f.completeSale(saleID, tender("cash", total)); r.status != http.StatusOK {
		t.Fatalf("a cash sale with no job = %d: %s, want 200", r.status, r.raw)
	}
}

// RULE: the job is checked again under the credit lock, as the purchase
// order is. The job leaves the sale while the card is on the wire; the
// transaction refuses the charge 409 job_required and the card is reversed.
func TestCP02_JobRequiredRecheckUnderTheLock(t *testing.T) {
	testutil.LockOutboxTables(t)
	gw := &fakeGateway{charges: []*payment.GatewayResult{approvedCharge()}}
	f := newFixture(t, testutil.RequireDB(t), func(f *fixture) { f.service = f.service.WithGateway(gw) })
	f.openTill(0)
	job := uuid.New()
	mustExec(t, f.db, `INSERT INTO projects (id, customer_id, name, status) VALUES ($1, $2, 'Ridge Build', 'Active')`, job, f.customerID)
	mustExec(t, f.db, `UPDATE customers SET job_required = TRUE WHERE id = $1`, f.customerID)
	t.Cleanup(func() { mustExec(t, f.db, `UPDATE customers SET job_required = FALSE WHERE id = $1`, f.customerID) })
	saleID := f.startSale(&f.customerID)
	if r := f.attach(saleID, map[string]any{"customer_id": f.customerID.String(), "job_id": job.String()}); r.status != http.StatusOK {
		t.Fatalf("attach the job = %d: %s", r.status, r.raw)
	}
	if r := f.addLine(saleID, f.productLine("2")); r.status != http.StatusOK {
		t.Fatalf("add line = %d: %s", r.status, r.raw)
	}
	total := num(t, f.getSale(t, saleID), "total_cents")
	gw.onCharge = func() {
		mustExec(t, f.db, `UPDATE pos_transactions SET job_id = NULL WHERE id = $1`, saleID)
	}
	r := f.completeSale(saleID, withToken(tender("card", total/2)), tender("account", total-total/2))
	if r.status != http.StatusConflict || !strings.Contains(string(r.raw), "job_required") {
		t.Fatalf("the charge with the job gone = %d: %s, want 409 job_required", r.status, r.raw)
	}
	if gw.chargeCalls != 1 || len(gw.voids)+len(gw.refunds) != 1 {
		t.Errorf("charges %d, reversals %d, want the one card charged and reversed", gw.chargeCalls, len(gw.voids)+len(gw.refunds))
	}
	if got := f.scalar(`SELECT count(*) FROM invoices WHERE customer_id = $1`, f.customerID).(int64); got != 0 {
		t.Errorf("%d invoices after the refused charge, want none", got)
	}
}
