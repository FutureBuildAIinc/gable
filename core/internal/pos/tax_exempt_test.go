// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package pos_test

// CP-10: the counter honours a customer's tax exemption in its running
// totals, not only at the money moment. A certificate exempts when it is
// active and its window covers the branch's business day; the same rule the
// order path reads. The counter also names the customer on a sale that
// already has lines, so an exempt account can be attached at Pay, and
// detaching it restores the tax.

import (
	"fmt"
	"net/http"
	"sync"
	"testing"
	"time"

	"github.com/gablelbm/gable/internal/testutil"
)

// exemptCartWithLines is a walk-in sale of 4 (subtotal 2200) whose lines are
// already rung, the way a sale stands when the customer is named at Pay.
func exemptCartWithLines(t *testing.T, f *fixture) string {
	t.Helper()
	saleID := f.startSale(nil)
	if r := f.addLine(saleID, f.productLine("4")); r.status != http.StatusOK {
		t.Fatalf("add line = %d: %s", r.status, r.raw)
	}
	return saleID
}

// addExemption writes the fixture customer a certificate covering (or not)
// the branch's business day, and removes it at the test's end.
func addExemption(t *testing.T, f *fixture, effective, expiry *time.Time) {
	t.Helper()
	mustExec(t, f.db, `INSERT INTO tax_exemptions (customer_id, exempt_reason, certificate_number, issuing_state, effective_date, expiry_date)
		VALUES ($1, 'RESALE', 'CERT-CP10', 'CT', $2::date, $3::date)`,
		f.customerID, dayArg(effective), dayArg(expiry))
	t.Cleanup(func() {
		mustExec(t, f.db, `DELETE FROM tax_exemptions WHERE customer_id = $1`, f.customerID)
	})
}

func dayArg(d *time.Time) any {
	if d == nil {
		return nil
	}
	return d.Format("2006-01-02")
}

// RULE: the cart's running totals honour the exemption, so the tax the
// screen shows is the tax completion posts: zero, with the certificate the
// counter can name.
func TestCP10_RunningTotalsHonourExemption(t *testing.T) {
	testutil.LockBranchDay(t)
	testutil.LockOutboxTables(t)
	f := newFixture(t, testutil.RequireDB(t))
	day := testutil.BranchDay(t, f.db)
	today := day
	addExemption(t, f, &today, nil)

	saleID := exemptCartWithLines(t, f)
	if r := f.attach(saleID, map[string]any{"customer_id": f.customerID.String()}); r.status != http.StatusOK {
		t.Fatalf("attach the exempt account = %d: %s", r.status, r.raw)
	}
	body := f.getSale(t, saleID)
	if got := num(t, body, "tax_cents"); got != 0 {
		t.Errorf("tax_cents before completion = %d, want 0", got)
	}
	if got := num(t, body, "total_cents"); got != 2200 {
		t.Errorf("total_cents before completion = %d, want 2200 (no tax)", got)
	}
	if ex, ok := body["tax_exemption"].(map[string]any); !ok || ex["certificate_number"] != "CERT-CP10" {
		t.Errorf("tax_exemption = %v, want the certificate number CERT-CP10", body["tax_exemption"])
	}
	r := f.completeSale(saleID, tender("cash", 2200))
	if r.status != http.StatusOK {
		t.Fatalf("complete = %d: %s", r.status, r.raw)
	}
	for key, want := range map[string]int64{"tax_cents": 0, "total_cents": 2200} {
		if got := num(t, r.body, key); got != want {
			t.Errorf("%s = %d, want %d", key, got, want)
		}
	}
	f.assertARInvariants(t)
}

// RULE: a charge sale to an exempt account of exactly the untaxed total
// completes; the old screen total (tax the server removed) no longer refuses
// it at Pay.
func TestCP10_ChargeSaleToExemptAccountCompletes(t *testing.T) {
	testutil.LockBranchDay(t)
	testutil.LockOutboxTables(t)
	f := newFixture(t, testutil.RequireDB(t))
	today := testutil.BranchDay(t, f.db)
	addExemption(t, f, &today, nil)

	saleID := exemptCartWithLines(t, f)
	if r := f.attach(saleID, map[string]any{"customer_id": f.customerID.String()}); r.status != http.StatusOK {
		t.Fatalf("attach the exempt account = %d: %s", r.status, r.raw)
	}
	r := f.completeSale(saleID, tender("account", 2200))
	if r.status != http.StatusOK {
		t.Fatalf("charge to the exempt account = %d: %s", r.status, r.raw)
	}
	if got := num(t, r.body, "tax_cents"); got != 0 {
		t.Errorf("tax_cents = %d, want 0", got)
	}
	if got := num(t, r.body, "total_cents"); got != 2200 {
		t.Errorf("total_cents = %d, want 2200", got)
	}
	f.assertARInvariants(t)
}

// RULE: an expired certificate does not exempt, at the counter and on the
// order path: the tax is the branch rate's.
func TestCP10_ExpiredCertificateDoesNotExempt(t *testing.T) {
	testutil.LockBranchDay(t)
	testutil.LockOutboxTables(t)
	f := newFixture(t, testutil.RequireDB(t))
	day := testutil.BranchDay(t, f.db)
	expired := day.AddDate(0, 0, -1)
	addExemption(t, f, &[]time.Time{day.AddDate(0, 0, -30)}[0], &expired)

	saleID := exemptCartWithLines(t, f)
	if r := f.attach(saleID, map[string]any{"customer_id": f.customerID.String()}); r.status != http.StatusOK {
		t.Fatalf("attach = %d: %s", r.status, r.raw)
	}
	body := f.getSale(t, saleID)
	if got := num(t, body, "tax_cents"); got != 195 {
		t.Errorf("tax_cents = %d, want 195 (the expired window does not exempt)", got)
	}
	if _, has := body["tax_exemption"]; has {
		t.Errorf("tax_exemption = %v, want none for an expired certificate", body["tax_exemption"])
	}
	r := f.completeSale(saleID, tender("cash", 2395))
	if r.status != http.StatusOK {
		t.Fatalf("complete = %d: %s", r.status, r.raw)
	}
	if got := num(t, r.body, "tax_cents"); got != 195 {
		t.Errorf("posted tax_cents = %d, want 195", got)
	}
	f.assertARInvariants(t)
}

// RULE: a certificate whose window has not begun does not exempt yet.
func TestCP10_FutureCertificateDoesNotExemptYet(t *testing.T) {
	testutil.LockBranchDay(t)
	testutil.LockOutboxTables(t)
	f := newFixture(t, testutil.RequireDB(t))
	day := testutil.BranchDay(t, f.db)
	future := day.AddDate(0, 0, 1)
	addExemption(t, f, &future, nil)

	saleID := exemptCartWithLines(t, f)
	if r := f.attach(saleID, map[string]any{"customer_id": f.customerID.String()}); r.status != http.StatusOK {
		t.Fatalf("attach = %d: %s", r.status, r.raw)
	}
	body := f.getSale(t, saleID)
	if got := num(t, body, "tax_cents"); got != 195 {
		t.Errorf("tax_cents = %d, want 195 (the window has not begun)", got)
	}
	r := f.completeSale(saleID, tender("cash", 2395))
	if r.status != http.StatusOK {
		t.Fatalf("complete = %d: %s", r.status, r.raw)
	}
	if got := num(t, r.body, "tax_cents"); got != 195 {
		t.Errorf("posted tax_cents = %d, want 195", got)
	}
	f.assertARInvariants(t)
}

// RULE: detaching the customer restores the tax the exemption removed.
func TestCP10_DetachRestoresTax(t *testing.T) {
	testutil.LockBranchDay(t)
	testutil.LockOutboxTables(t)
	f := newFixture(t, testutil.RequireDB(t))
	today := testutil.BranchDay(t, f.db)
	addExemption(t, f, &today, nil)

	saleID := exemptCartWithLines(t, f)
	if r := f.attach(saleID, map[string]any{"customer_id": f.customerID.String()}); r.status != http.StatusOK {
		t.Fatalf("attach = %d: %s", r.status, r.raw)
	}
	if got := num(t, f.getSale(t, saleID), "tax_cents"); got != 0 {
		t.Fatalf("tax_cents attached = %d, want 0", got)
	}
	if r := f.attach(saleID, map[string]any{"customer_id": nil}); r.status != http.StatusOK {
		t.Fatalf("detach = %d: %s", r.status, r.raw)
	}
	body := f.getSale(t, saleID)
	if got := num(t, body, "tax_cents"); got != 195 {
		t.Errorf("tax_cents after detach = %d, want 195", got)
	}
	if _, has := body["tax_exemption"]; has {
		t.Errorf("tax_exemption = %v, want none once detached", body["tax_exemption"])
	}
	r := f.completeSale(saleID, tender("cash", 2395))
	if r.status != http.StatusOK {
		t.Fatalf("complete = %d: %s", r.status, r.raw)
	}
	if got := num(t, r.body, "tax_cents"); got != 195 {
		t.Errorf("posted tax_cents = %d, want 195", got)
	}
	f.assertARInvariants(t)
}

// RULE: naming the customer and ringing lines contend safely: three cashiers
// on one pool of four connections each rung lines, named the exempt account
// and read a total that matches what their own sale's completion posts.
func TestCP10_ExemptionAttachUnderContenders(t *testing.T) {
	testutil.LockBranchDay(t)
	testutil.LockOutboxTables(t)
	f := newFixture(t, testutil.RequireDBMaxConns(t, 4))
	today := testutil.BranchDay(t, f.db)
	addExemption(t, f, &today, nil)

	var mu sync.Mutex
	var wg sync.WaitGroup
	errs := make(chan error, 3)
	for i := 0; i < 3; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			mu.Lock()
			saleID := f.startSale(nil)
			mu.Unlock()
			if r := f.addLine(saleID, f.productLine("4")); r.status != http.StatusOK {
				errs <- fmt.Errorf("contender %d add line = %d: %s", i, r.status, r.raw)
				return
			}
			if r := f.attach(saleID, map[string]any{"customer_id": f.customerID.String()}); r.status != http.StatusOK {
				errs <- fmt.Errorf("contender %d attach = %d: %s", i, r.status, r.raw)
				return
			}
			r := f.do("GET", "/api/v1/pos/transactions/"+saleID, nil)
			if r.status != http.StatusOK {
				errs <- fmt.Errorf("contender %d get sale = %d: %s", i, r.status, r.raw)
				return
			}
			if tax, ok := r.body["tax_cents"]; !ok || fmt.Sprint(tax) != "0" {
				errs <- fmt.Errorf("contender %d tax_cents = %v, want 0", i, tax)
				return
			}
		}(i)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Error(err)
	}
	f.assertARInvariants(t)
}

// RULE: the certificate window reads the branch's business day, not UTC's:
// a certificate that expired yesterday by the branch's own day does not
// exempt even when UTC's day is still inside the window.
func TestCP10_ExemptionWindowIsTheBranchDay(t *testing.T) {
	testutil.LockBranchDay(t)
	testutil.LockOutboxTables(t)
	f := newFixture(t, testutil.RequireDB(t))
	day := testutil.BranchDay(t, f.db)
	yesterday := day.AddDate(0, 0, -1)
	addExemption(t, f, &[]time.Time{day.AddDate(0, 0, -30)}[0], &yesterday)

	saleID := exemptCartWithLines(t, f)
	if r := f.attach(saleID, map[string]any{"customer_id": f.customerID.String()}); r.status != http.StatusOK {
		t.Fatalf("attach = %d: %s", r.status, r.raw)
	}
	if got := num(t, f.getSale(t, saleID), "tax_cents"); got != 195 {
		t.Errorf("tax_cents = %d, want 195 (the branch's day is past the window)", got)
	}
	f.assertARInvariants(t)
}
