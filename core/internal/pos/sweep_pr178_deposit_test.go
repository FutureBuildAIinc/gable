// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package pos_test

// Sweep fix for the day close deposit cash floor: a drawer counted short of
// its opening float deposits no cash (the over/short entry already carries the
// shortage), and its checks still reach the bank in full, never netted against
// the negative cash.

import (
	"net/http"
	"testing"

	"github.com/gablelbm/gable/internal/testutil"
)

// shortDrawerBranch makes a branch with a 500 variance threshold, opens a
// 20000 float drawer, sells 599 by check, and counts it at the cash and check
// given, signing the count off when it waits for a manager. It answers the
// session, the sale and the business date.
func (d *dayFixture) shortDrawerBranch(cash, check int64) (b *dayBranch, session, saleID, day string) {
	d.t.Helper()
	b = d.makeBranch()
	d.do("PUT", "/api/v1/pos/till/policy", map[string]any{"branch_id": b.id.String(), "variance_threshold_cents": 500},
		"X-Test-Role", "finance", "X-Test-Sub", mustUUID(d.t))
	reg := b.registers[0]
	session = d.openTillOn(reg, 20000)
	saleID = d.startSaleOn(reg)
	if r := d.addLine(saleID, d.productLine("1")); r.status != http.StatusOK {
		d.t.Fatalf("add line = %d: %s", r.status, r.raw)
	}
	if r := d.completeSaleOn(reg, saleID, tender("check", 599)); r.status != http.StatusOK {
		d.t.Fatalf("complete = %d: %s", r.status, r.raw)
	}
	r := d.count(session, map[string]any{"cash": cash, "check": check})
	if r.status != http.StatusOK {
		d.t.Fatalf("count = %d: %s", r.status, r.raw)
	}
	if sessionOf(d.t, r)["status"] == "pending_signoff" {
		if s := d.signOff(session, "short of the float, banked the checks", "finance", mustUUID(d.t)); s.status != http.StatusOK {
			d.t.Fatalf("sign off = %d: %s", s.status, s.raw)
		}
	}
	return b, session, saleID, d.today(b)
}

// RULE: a drawer short of its opening float deposits no cash; its checks are
// deposited in full and never netted against the negative cash, so the bank
// leg carries every check and the 1010 net is the float shortage alone.
func TestSweepPR178_ShortCashDrawerDoesNotNetAgainstChecksDeposit(t *testing.T) {
	testutil.LockOutboxTables(t)
	d := newDayFixture(t, testutil.RequireDB(t))
	bank, glCode := d.bankWithGL()

	// Case 1: cash 1000 short, checks 599. The deposit is the 599 of checks.
	a, s1, sale1, day1 := d.shortDrawerBranch(19000, 599)
	r := d.closeDay(a, day1, "SWEEP-178-1", bank)
	if r.status != http.StatusCreated {
		t.Fatalf("close 1 = %d: %s", r.status, r.raw)
	}
	c1 := obj(t, r.body, "close")
	close1 := str(t, c1, "id")
	if num(t, c1, "cash_to_deposit_cents") != 0 || num(t, c1, "checks_to_deposit_cents") != 599 {
		t.Fatalf("close 1 figures = %v, want no cash and 599 of checks", c1)
	}
	entries1, legs1 := d.legsFor(close1)
	if entries1 != 1 {
		t.Fatalf("%d entries sourced by close 1, want 1", entries1)
	}
	want1 := map[string]leg{glCode: {debit: 599}, "1010": {credit: 599}}
	for code, w := range want1 {
		if legs1[code] != w {
			t.Errorf("close 1 account %s = %+v, want %+v", code, legs1[code], w)
		}
	}
	if len(legs1) != len(want1) {
		t.Errorf("close 1 legs = %v, want exactly %v", legs1, want1)
	}
	// The 1010 net over the sale's payment, the over/short and the deposit is
	// the 1000 cash shortage; the check sale and its deposit net to 0.
	if got := d.netOn("1010", sale1, s1, close1); got != -1000 {
		t.Errorf("close 1 1010 nets %d over the sale, over/short and deposit, want -1000", got)
	}

	// Case 2: cash 10000 short, checks 10599 (the 599 tendered plus 10000 of
	// extra checks in the drawer). The bank is debited the full 10599.
	b, s2, sale2, day2 := d.shortDrawerBranch(10000, 10599)
	r = d.closeDay(b, day2, "SWEEP-178-2", bank)
	if r.status != http.StatusCreated {
		t.Fatalf("close 2 = %d: %s", r.status, r.raw)
	}
	c2 := obj(t, r.body, "close")
	close2 := str(t, c2, "id")
	if num(t, c2, "cash_to_deposit_cents") != 0 || num(t, c2, "checks_to_deposit_cents") != 10599 {
		t.Fatalf("close 2 figures = %v, want no cash and 10599 of checks", c2)
	}
	entries2, legs2 := d.legsFor(close2)
	if entries2 != 1 {
		t.Fatalf("%d entries sourced by close 2, want 1", entries2)
	}
	want2 := map[string]leg{glCode: {debit: 10599}, "1010": {credit: 10599}}
	for code, w := range want2 {
		if legs2[code] != w {
			t.Errorf("close 2 account %s = %+v, want %+v", code, legs2[code], w)
		}
	}
	if len(legs2) != len(want2) {
		t.Errorf("close 2 legs = %v, want exactly %v", legs2, want2)
	}
	// The statement line of the full 10599 matches with no difference.
	line := d.bankLineOn(bank, 10599, day2)
	m := d.do("POST", "/api/v1/pos/end-of-day/"+close2+"/deposit-match", map[string]any{"bank_transaction_id": line.String()},
		"X-Test-Role", "finance", "X-Test-Sub", mustUUID(t))
	if m.status != http.StatusOK {
		t.Fatalf("deposit match = %d: %s", m.status, m.raw)
	}
	if dm := obj(t, m.body, "deposit_match"); num(t, dm, "difference_cents") != 0 {
		t.Errorf("difference_cents = %v, want 0 against the full 10599 line", dm["difference_cents"])
	}
	// The 1010 net is the 10000 cash shortage, with all 10599 of checks moved
	// out of 1010 into the bank account.
	if got := d.netOn("1010", sale2, s2, close2); got != -10000 {
		t.Errorf("close 2 1010 nets %d over the sale, over/short and deposit, want -10000", got)
	}
}
