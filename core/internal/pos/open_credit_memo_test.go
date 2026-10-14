// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package pos_test

// An open credit memo's open amount is stored negative, so it lowers the
// counter's open receivable: the lookup's standing and the charge tender's
// credit check both read that one figure.

import (
	"net/http"
	"testing"

	"github.com/gablelbm/gable/internal/testutil"
)

// accountReturnOf leaves a 5.99 open credit memo on the fixture customer: a
// sale, then a return of one unit refunded to the account.
func (f *fixture) accountReturnOf() {
	f.t.Helper()
	f.saleOf("4", tender("cash", 2395))
	r := f.do("POST", "/api/v1/pos/returns", map[string]any{
		"register_id": f.register, "customer_id": f.customerID.String(), "refund_method": "account",
		"reason": "store credit", "lines": []map[string]any{{
			"product_id": f.productID.String(), "quantity": "1",
			"unit_price_ten_thousandths": 55000, "restock": true,
		}}}, "X-Test-Role", "cashier", "X-Test-Sub", mustUUID(f.t))
	if r.status != http.StatusCreated {
		f.t.Fatalf("account return = %d: %s", r.status, r.raw)
	}
}

// RULE: an open credit memo lowers the open receivable the counter's lookup
// states, and the available credit is the limit less that figure.
func TestAnOpenCreditMemoLowersTheCountersOpenReceivable(t *testing.T) {
	testutil.LockOutboxTables(t)
	f := newFixture(t, testutil.RequireDB(t))
	f.accountReturnOf()
	mustExec(t, f.db, `UPDATE customers SET credit_limit = 10.00 WHERE id = $1`, f.customerID)
	acct := f.scalar(`SELECT account_number FROM customers WHERE id = $1`, f.customerID).(string)

	c := standingOf(t, f.findCustomers(acct))
	if got := num(t, c, "open_receivable_cents"); got != -599 {
		t.Errorf("open_receivable_cents = %d, want -599 (the memo lowers it)", got)
	}
	if got := num(t, c, "available_credit_cents"); got != 1000+599 {
		t.Errorf("available_credit_cents = %d, want 1599 (the limit less -599)", got)
	}
	if c["standing"] != "good" {
		t.Errorf("standing = %v, want good", c["standing"])
	}
}

// RULE: the charge tender's credit check counts the open memo: a charge that
// fits only once it counts completes, one over the true available credit is
// refused 409 credit_limit.
func TestTheChargeTenderCountsAnOpenCreditMemo(t *testing.T) {
	testutil.LockOutboxTables(t)
	f := newFixture(t, testutil.RequireDB(t))
	f.accountReturnOf()
	// a limit of 5.00 with an open memo of -5.99: true available credit is 10.99
	mustExec(t, f.db, `UPDATE customers SET credit_limit = 5.00 WHERE id = $1`, f.customerID)

	// 1198 on account is over the true available credit (500 + 599 = 1099)
	over := f.startSale(&f.customerID)
	if r := f.addLine(over, f.productLine("2")); r.status != http.StatusOK {
		t.Fatalf("add line = %d: %s", r.status, r.raw)
	}
	overTotal := num(t, f.getSale(t, over), "total_cents")
	if overTotal != 1198 {
		t.Fatalf("two units total %d, want 1198", overTotal)
	}
	r := f.completeSale(over, tender("account", overTotal))
	if r.status != http.StatusConflict {
		t.Fatalf("over the true available credit = %d, want 409: %s", r.status, r.raw)
	}
	if _, blockers, _ := errorOf(t, r); len(blockers) != 1 || blockers[0] != "credit_limit" {
		t.Errorf("blockers = %v, want [credit_limit]", blockers)
	}

	// 599 on account fits the true available credit; the wrong figure (open
	// +599) would leave only -99 and refuse it
	fits := f.startSale(&f.customerID)
	if r := f.addLine(fits, f.productLine("1")); r.status != http.StatusOK {
		t.Fatalf("add line = %d: %s", r.status, r.raw)
	}
	fitsTotal := num(t, f.getSale(t, fits), "total_cents")
	if r := f.completeSale(fits, tender("account", fitsTotal)); r.status != http.StatusOK {
		t.Fatalf("a charge within the true available credit = %d, want 200: %s", r.status, r.raw)
	}
}
