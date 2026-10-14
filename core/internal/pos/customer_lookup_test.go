// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package pos_test

// The counter's customer: the lookup with the account's standing, naming the
// customer on an open sale, and the purchase order and job an account that
// asks for them is charged with.

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"testing"

	"github.com/gablelbm/gable/internal/payment"
	"github.com/gablelbm/gable/internal/testutil"
	"github.com/google/uuid"
)

const cashierHeaders = "X-Test-Role"

// decodeArray reads a bare array body, numbers kept exact like the fixture's.
func decodeArray(t *testing.T, raw []byte, out *[]map[string]any) bool {
	t.Helper()
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	return dec.Decode(out) == nil
}

func (f *fixture) findCustomers(q string) resp {
	f.t.Helper()
	return f.do("GET", "/api/v1/pos/customers?q="+url.QueryEscape(q), nil,
		cashierHeaders, "cashier", "X-Test-Sub", mustUUID(f.t))
}

func (f *fixture) attach(saleID string, body map[string]any) resp {
	f.t.Helper()
	return f.do("PUT", "/api/v1/pos/transactions/"+saleID+"/customer", body,
		cashierHeaders, "cashier", "X-Test-Sub", mustUUID(f.t))
}

func standingOf(t *testing.T, r resp) map[string]any {
	t.Helper()
	if r.status != http.StatusOK {
		t.Fatalf("lookup = %d: %s", r.status, r.raw)
	}
	var out []map[string]any
	if !decodeArray(t, r.raw, &out) || len(out) != 1 {
		t.Fatalf("lookup = %s, want exactly the one customer", r.raw)
	}
	return out[0]
}

// RULE: the lookup finds a customer by name or by account number and states
// the standing the charge tender will read: no limit, good with the available
// credit, no credit (a limit of zero: on hold), and over the limit.
func TestTheCounterLookupStatesTheAccountsStanding(t *testing.T) {
	testutil.LockOutboxTables(t)
	f := newFixture(t, testutil.RequireDB(t))
	acct := f.scalar(`SELECT account_number FROM customers WHERE id = $1`, f.customerID).(string)

	for _, q := range []string{"Counter Co", "counter", acct} {
		c := standingOf(t, f.findCustomers(q))
		if c["id"] != f.customerID.String() || c["standing"] != "no_limit" || c["available_credit_cents"] != nil {
			t.Errorf("lookup %q = %v, want the customer with no limit", q, c)
		}
	}
	if r := f.findCustomers("c"); strings.TrimSpace(string(r.raw)) != "[]" {
		t.Errorf("a one character query answered %s, want []", r.raw)
	}

	mustExec(t, f.db, `UPDATE customers SET credit_limit = 100.00 WHERE id = $1`, f.customerID)
	c := standingOf(t, f.findCustomers(acct))
	if c["standing"] != "good" || num(t, c, "credit_limit_cents") != 10000 || num(t, c, "available_credit_cents") != 10000 {
		t.Errorf("a 100.00 limit with nothing open = %v, want good with 10000 available", c)
	}

	mustExec(t, f.db, `UPDATE customers SET credit_limit = 0 WHERE id = $1`, f.customerID)
	if c := standingOf(t, f.findCustomers(acct)); c["standing"] != "no_credit" {
		t.Errorf("a limit of zero = %v, want no_credit (on hold)", c)
	}

	// an account sale takes 599 plus tax on account; then a limit under it
	mustExec(t, f.db, `UPDATE customers SET credit_limit = NULL WHERE id = $1`, f.customerID)
	f.openTill(0)
	saleID := f.startSale(&f.customerID)
	if r := f.addLine(saleID, f.productLine("1")); r.status != http.StatusOK {
		t.Fatalf("add line = %d: %s", r.status, r.raw)
	}
	cur := f.getSale(t, saleID)
	total := num(t, cur, "total_cents")
	if r := f.completeSale(saleID, tender("account", total)); r.status != http.StatusOK {
		t.Fatalf("charge sale = %d: %s", r.status, r.raw)
	}
	mustExec(t, f.db, `UPDATE customers SET credit_limit = 1.00 WHERE id = $1`, f.customerID)
	c = standingOf(t, f.findCustomers(acct))
	if c["standing"] != "over_limit" || num(t, c, "open_receivable_cents") != total || num(t, c, "available_credit_cents") != 100-total {
		t.Errorf("a 1.00 limit under %d open = %v, want over_limit", total, c)
	}
	mustExec(t, f.db, `UPDATE customers SET credit_limit = NULL WHERE id = $1`, f.customerID)
}

// RULE: the customer is named before the first line, the purchase order and
// the job may follow; a job is the customer's own; a sale answers the
// customer it was named for.
func TestTheSaleTakesItsCustomerBeforeTheFirstLine(t *testing.T) {
	testutil.LockOutboxTables(t)
	f := newFixture(t, testutil.RequireDB(t))
	job, other := uuid.New(), uuid.New()
	mustExec(t, f.db, `INSERT INTO projects (id, customer_id, name, status) VALUES ($1, $2, 'Smith Deck', 'Active')`, job, f.customerID)
	mustExec(t, f.db, `INSERT INTO customers (id, name, account_number, primary_branch_id) VALUES ($1, 'Other Co', $2, $3)`, other, "OTH-"+uuid.NewString()[:8], f.branchID)
	otherJob := uuid.New()
	mustExec(t, f.db, `INSERT INTO projects (id, customer_id, name, status) VALUES ($1, $2, 'Elsewhere', 'Active')`, otherJob, other)
	t.Cleanup(func() {
		mustExec(t, f.db, `DELETE FROM projects WHERE customer_id = $1`, other)
		mustExec(t, f.db, `DELETE FROM customers WHERE id = $1`, other)
	})

	if c := standingOf(t, f.findCustomers("Counter Co")); len(c["jobs"].([]any)) != 1 {
		t.Errorf("the lookup carries %v jobs, want the customer's one open job", c["jobs"])
	}

	saleID := f.startSale(nil)
	r := f.attach(saleID, map[string]any{"customer_id": f.customerID.String(), "customer_po": " PO-7731 ", "job_id": job.String()})
	if r.status != http.StatusOK || str(t, r.body, "customer_id") != f.customerID.String() ||
		str(t, r.body, "customer_po") != "PO-7731" || str(t, r.body, "job_id") != job.String() {
		t.Fatalf("attach = %d: %s", r.status, r.raw)
	}
	// another customer's job is refused, and so is a job with no customer
	if r := f.attach(saleID, map[string]any{"customer_id": f.customerID.String(), "job_id": otherJob.String()}); r.status != http.StatusBadRequest {
		t.Errorf("another customer's job = %d, want 400: %s", r.status, r.raw)
	}
	if r := f.attach(saleID, map[string]any{"customer_id": nil, "job_id": job.String()}); r.status != http.StatusBadRequest {
		t.Errorf("a job with no customer = %d, want 400: %s", r.status, r.raw)
	}
	// a missing customer is a 404
	if r := f.attach(saleID, map[string]any{"customer_id": uuid.NewString()}); r.status != http.StatusNotFound {
		t.Errorf("a missing customer = %d, want 404: %s", r.status, r.raw)
	}
	// the first line, then the customer is fixed; the PO may still change
	if r := f.addLine(saleID, f.productLine("1")); r.status != http.StatusOK {
		t.Fatalf("add line = %d: %s", r.status, r.raw)
	}
	if r := f.attach(saleID, map[string]any{"customer_id": other.String()}); r.status != http.StatusConflict {
		t.Errorf("another customer after a line = %d, want 409: %s", r.status, r.raw)
	}
	r = f.attach(saleID, map[string]any{"customer_id": f.customerID.String(), "customer_po": "PO-8000", "job_id": job.String()})
	if r.status != http.StatusOK || str(t, r.body, "customer_po") != "PO-8000" {
		t.Errorf("a new PO after a line = %d: %s", r.status, r.raw)
	}
	if got := f.scalar(`SELECT count(*) FROM audit_log WHERE action = 'pos.sale.customer_attached' AND entity_id = $1`, uuid.MustParse(saleID)); got.(int64) < 2 {
		t.Errorf("%v customer audit rows, want one per naming", got)
	}
}

// RULE: an account that asks for a purchase order is not charged without one
// (409 po_required, nothing moved, before a card could be charged), and the
// invoice carries the PO and the job the sale named.
func TestAChargeSaleCarriesThePurchaseOrderAndJob(t *testing.T) {
	testutil.LockOutboxTables(t)
	f := newFixture(t, testutil.RequireDB(t))
	f.openTill(0)
	job := uuid.New()
	mustExec(t, f.db, `INSERT INTO projects (id, customer_id, name, status) VALUES ($1, $2, 'Smith Deck', 'Active')`, job, f.customerID)
	mustExec(t, f.db, `UPDATE customers SET po_required = TRUE WHERE id = $1`, f.customerID)
	t.Cleanup(func() { mustExec(t, f.db, `UPDATE customers SET po_required = FALSE WHERE id = $1`, f.customerID) })

	saleID := f.startSale(nil)
	if r := f.attach(saleID, map[string]any{"customer_id": f.customerID.String()}); r.status != http.StatusOK {
		t.Fatalf("attach = %d: %s", r.status, r.raw)
	}
	if r := f.addLine(saleID, f.productLine("1")); r.status != http.StatusOK {
		t.Fatalf("add line = %d: %s", r.status, r.raw)
	}
	total := num(t, f.getSale(t, saleID), "total_cents")
	r := f.completeSale(saleID, tender("account", total))
	if r.status != http.StatusConflict || !strings.Contains(string(r.raw), "po_required") {
		t.Fatalf("a charge with no PO = %d: %s, want 409 po_required", r.status, r.raw)
	}
	if got := f.scalar(`SELECT count(*) FROM invoices WHERE customer_id = $1`, f.customerID).(int64); got != 0 {
		t.Errorf("%d invoices after the refused charge, want none", got)
	}
	// cash needs no PO: the check is the account tender's
	if r := f.attach(saleID, map[string]any{"customer_id": f.customerID.String(), "customer_po": "PO-5521", "job_id": job.String()}); r.status != http.StatusOK {
		t.Fatalf("attach PO = %d: %s", r.status, r.raw)
	}
	r = f.completeSale(saleID, tender("account", total))
	if r.status != http.StatusOK {
		t.Fatalf("a charge with the PO = %d: %s", r.status, r.raw)
	}
	inv := str(t, r.body, "invoice_id")
	if got := f.scalar(`SELECT customer_po FROM invoices WHERE id = $1`, uuid.MustParse(inv)); got != "PO-5521" {
		t.Errorf("invoice customer_po = %v, want PO-5521", got)
	}
	if got := f.scalar(`SELECT project_id::text FROM invoices WHERE id = $1`, uuid.MustParse(inv)); got != job.String() {
		t.Errorf("invoice project_id = %v, want the job %s", got, job)
	}
	f.assertARInvariants(t)
}

// RULE: naming the customer takes the sale row's lock, so three contenders at
// pool size 4 each land whole: every answer is 200, every one moves the
// revision, and the sale ends holding one of the purchase orders.
func TestThreeNamingsOfOneSaleAreSerialized(t *testing.T) {
	testutil.LockOutboxTables(t)
	f := newFixture(t, testutil.RequireDBMaxConns(t, 4))
	saleID := f.startSale(nil)
	start := num(t, f.getSale(t, saleID), "revision")
	var wg sync.WaitGroup
	statuses := make([]int, 3)
	pos := []string{"PO-A", "PO-B", "PO-C"}
	for i := range pos {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			statuses[i] = f.attach(saleID, map[string]any{"customer_id": f.customerID.String(), "customer_po": pos[i]}).status
		}(i)
	}
	wg.Wait()
	for i, st := range statuses {
		if st != http.StatusOK {
			t.Errorf("contender %d = %d, want 200", i, st)
		}
	}
	end := f.getSale(t, saleID)
	if got := num(t, end, "revision"); got != start+3 {
		t.Errorf("revision = %d, want %d (three namings)", got, start+3)
	}
	po := str(t, end, "customer_po")
	if po != "PO-A" && po != "PO-B" && po != "PO-C" {
		t.Errorf("customer_po = %q, want one of the three", po)
	}
}

// RULE: the card side checks stand alone. A split of card and account that
// the account side would refuse (a purchase order the account asks for and
// the sale does not carry, or a zero limit) charges no card at all.
func TestASplitWhoseAccountLegIsRefusedChargesNoCard(t *testing.T) {
	testutil.LockOutboxTables(t)
	gw := &fakeGateway{charges: []*payment.GatewayResult{approvedCharge()}}
	f := newFixture(t, testutil.RequireDB(t), func(f *fixture) { f.service = f.service.WithGateway(gw) })
	f.openTill(0)
	mustExec(t, f.db, `UPDATE customers SET po_required = TRUE WHERE id = $1`, f.customerID)
	t.Cleanup(func() {
		mustExec(t, f.db, `UPDATE customers SET po_required = FALSE, credit_limit = NULL WHERE id = $1`, f.customerID)
	})
	saleID := f.startSale(&f.customerID)
	if r := f.addLine(saleID, f.productLine("2")); r.status != http.StatusOK {
		t.Fatalf("add line = %d: %s", r.status, r.raw)
	}
	total := num(t, f.getSale(t, saleID), "total_cents")
	r := f.completeSale(saleID, withToken(tender("card", total/2)), tender("account", total-total/2))
	if r.status != http.StatusConflict || !strings.Contains(string(r.raw), "po_required") {
		t.Fatalf("the split with no PO = %d: %s, want 409 po_required", r.status, r.raw)
	}
	mustExec(t, f.db, `UPDATE customers SET po_required = FALSE, credit_limit = 0 WHERE id = $1`, f.customerID)
	r = f.completeSale(saleID, withToken(tender("card", total/2)), tender("account", total-total/2))
	if r.status != http.StatusConflict || !strings.Contains(string(r.raw), "credit_hold") {
		t.Fatalf("the split on a zero limit = %d: %s, want 409 credit_hold", r.status, r.raw)
	}
	if gw.chargeCalls != 0 {
		t.Errorf("the refused splits reached the terminal %d times, want 0", gw.chargeCalls)
	}
}

// RULE: the purchase order is checked again under the credit lock. The PO
// leaves the sale while the card is on the wire; the transaction refuses the
// charge 409 po_required, the card is reversed and no invoice is posted.
func TestThePORecheckUnderTheLockRefusesAPOGoneWhileTheCardWasCharged(t *testing.T) {
	testutil.LockOutboxTables(t)
	gw := &fakeGateway{charges: []*payment.GatewayResult{approvedCharge()}}
	f := newFixture(t, testutil.RequireDB(t), func(f *fixture) { f.service = f.service.WithGateway(gw) })
	f.openTill(0)
	mustExec(t, f.db, `UPDATE customers SET po_required = TRUE WHERE id = $1`, f.customerID)
	t.Cleanup(func() { mustExec(t, f.db, `UPDATE customers SET po_required = FALSE WHERE id = $1`, f.customerID) })
	saleID := f.startSale(nil)
	if r := f.attach(saleID, map[string]any{"customer_id": f.customerID.String(), "customer_po": "PO-1"}); r.status != http.StatusOK {
		t.Fatalf("attach = %d: %s", r.status, r.raw)
	}
	if r := f.addLine(saleID, f.productLine("2")); r.status != http.StatusOK {
		t.Fatalf("add line = %d: %s", r.status, r.raw)
	}
	total := num(t, f.getSale(t, saleID), "total_cents")
	gw.onCharge = func() {
		mustExec(t, f.db, `UPDATE pos_transactions SET customer_po = NULL WHERE id = $1`, saleID)
	}
	r := f.completeSale(saleID, withToken(tender("card", total/2)), tender("account", total-total/2))
	if r.status != http.StatusConflict || !strings.Contains(string(r.raw), "po_required") {
		t.Fatalf("the charge with the PO gone = %d: %s, want 409 po_required", r.status, r.raw)
	}
	if gw.chargeCalls != 1 || len(gw.voids)+len(gw.refunds) != 1 {
		t.Errorf("charges %d, reversals %d, want the one card charged and reversed", gw.chargeCalls, len(gw.voids)+len(gw.refunds))
	}
	if got := f.scalar(`SELECT count(*) FROM invoices WHERE customer_id = $1`, f.customerID).(int64); got != 0 {
		t.Errorf("%d invoices after the refused charge, want none", got)
	}
}
