// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package pos_test

// Can this sale go on account? (CP-11, CP-02): the standing read states the
// credit, the open credit memos, the hold, the terms and a verdict with its
// reason and the ways forward; the completion enforces the same verdict and
// the manager override is one act for one sale, consumed by its completion.

import (
	"context"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/gablelbm/gable/internal/testutil"
	"github.com/google/uuid"
)

func (f *fixture) standing(customer uuid.UUID, query string) resp {
	f.t.Helper()
	path := "/api/v1/pos/customers/" + customer.String() + "/standing"
	if query != "" {
		path += "?" + query
	}
	return f.do("GET", path, nil, "X-Test-Role", "cashier", "X-Test-Sub", mustUUID(f.t))
}

func verdictOf(t *testing.T, r resp) map[string]any {
	t.Helper()
	if r.status != http.StatusOK {
		t.Fatalf("standing = %d: %s", r.status, r.raw)
	}
	v, ok := r.body["on_account"].(map[string]any)
	if !ok {
		t.Fatalf("no on_account verdict in %s", r.raw)
	}
	return v
}

func wayOf(t *testing.T, verdict map[string]any, kind string) map[string]any {
	t.Helper()
	ways, _ := verdict["ways_forward"].([]any)
	for _, w := range ways {
		m := w.(map[string]any)
		if m["kind"] == kind {
			return m
		}
	}
	t.Fatalf("no %q way forward in %v", kind, verdict["ways_forward"])
	return nil
}

// accountSaleOf rings a named sale of qty on the fixture customer's account.
func (f *fixture) accountSaleOf(qty string, cents int64) string {
	f.t.Helper()
	id := f.startSale(&f.customerID)
	if r := f.addLine(id, f.productLine(qty)); r.status != http.StatusOK {
		f.t.Fatalf("add line = %d: %s", r.status, r.raw)
	}
	if r := f.completeSale(id, tender("account", cents)); r.status != http.StatusOK {
		f.t.Fatalf("account sale = %d: %s", r.status, r.raw)
	}
	return id
}

// override asks a manager (the admin role, another subject than the sale's
// cashier) to approve the sale on account up to cap cents.
func (f *fixture) override(saleID, kind, reason string, capCents int64, role, sub string) resp {
	f.t.Helper()
	return f.do("POST", "/api/v1/pos/transactions/"+saleID+"/overrides",
		map[string]any{"kind": kind, "reason": reason, "cap_cents": capCents},
		"X-Test-Role", role, "X-Test-Sub", sub)
}

// startSaleBy opens a cart as the cashier subject given.
func (f *fixture) startSaleBy(sub string, customer *uuid.UUID) string {
	f.t.Helper()
	body := map[string]any{"register_id": f.register, "cashier_id": sub}
	if customer != nil {
		body["customer_id"] = customer.String()
	}
	r := f.do("POST", "/api/v1/pos/transactions", body, "X-Test-Role", "cashier", "X-Test-Sub", sub)
	if r.status != http.StatusCreated {
		f.t.Fatalf("start sale = %d: %s", r.status, r.raw)
	}
	return str(f.t, r.body, "id")
}

// RULE: an account with no limit is allowed, and states what it is: the
// terms kind, the hold flag, no limit and no available figure.
func TestStandingAllowsAnAccountWithNoLimit(t *testing.T) {
	testutil.LockOutboxTables(t)
	f := newFixture(t, testutil.RequireDB(t))
	r := f.standing(f.customerID, "")
	v := verdictOf(t, r)
	if v["verdict"] != "allowed" {
		t.Errorf("verdict = %v, want allowed", v["verdict"])
	}
	if r.body["credit_limit_cents"] != nil || r.body["available_credit_cents"] != nil {
		t.Errorf("limit and available = %v and %v, want both null (no limit is unlimited)",
			r.body["credit_limit_cents"], r.body["available_credit_cents"])
	}
	if r.body["on_hold"] != false {
		t.Errorf("on_hold = %v, want false", r.body["on_hold"])
	}
	if r.body["terms_kind"] != "net_days" {
		t.Errorf("terms_kind = %v, want net_days", r.body["terms_kind"])
	}
	if r.body["named_buyer_required"] != false {
		t.Errorf("named_buyer_required = %v, want false", r.body["named_buyer_required"])
	}
	opts, _ := r.body["payment_options"].([]any)
	got := []string{}
	for _, o := range opts {
		got = append(got, o.(string))
	}
	if strings.Join(got, ",") != "cash,check,card,account" {
		t.Errorf("payment_options = %v, want cash, check, card and account", got)
	}
}

// RULE: over the limit the verdict is blocked with the reason credit_limit,
// the amount in words, and the three ways forward: pay down (with the amount
// that clears it), a manager override and a switch of tender.
func TestStandingBlocksAnAccountOverItsLimitWithThreeWaysForward(t *testing.T) {
	testutil.LockOutboxTables(t)
	f := newFixture(t, testutil.RequireDB(t))
	mustExec(t, f.db, `UPDATE customers SET credit_limit = 30.00 WHERE id = $1`, f.customerID)
	f.accountSaleOf("4", 2395)

	r := f.standing(f.customerID, "amount_cents=1000")
	v := verdictOf(t, r)
	if v["verdict"] != "blocked" || v["reason"] != "credit_limit" {
		t.Fatalf("verdict = %v reason %v, want blocked credit_limit", v["verdict"], v["reason"])
	}
	if got := num(t, r.body, "open_receivable_cents"); got != 2395 {
		t.Errorf("open_receivable_cents = %d, want 2395", got)
	}
	if got := num(t, r.body, "available_credit_cents"); got != 605 {
		t.Errorf("available_credit_cents = %d, want 605", got)
	}
	if got := num(t, v, "over_cents"); got != 395 {
		t.Errorf("over_cents = %d, want 395", got)
	}
	if msg := str(t, v, "message"); !strings.Contains(msg, "3.95") || !strings.Contains(msg, "30.00") {
		t.Errorf("message = %q, want the amount over (3.95) and the limit (30.00) in words", msg)
	}
	pay := wayOf(t, v, "pay_down")
	if pay["available"] != true || num(t, pay, "amount_cents") != 395 {
		t.Errorf("pay_down = %v, want available with the 395 that clears it", pay)
	}
	if wayOf(t, v, "manager_override")["available"] != true {
		t.Errorf("manager_override must be offered over the limit")
	}
	sw := wayOf(t, v, "switch_tender")
	if sw["available"] != true {
		t.Errorf("switch_tender = %v, want available", sw)
	}

	// a sale that fits is allowed, and the answer without an amount reads
	// the account as it stands (605 available, so allowed)
	if v := verdictOf(t, f.standing(f.customerID, "amount_cents=605")); v["verdict"] != "allowed" {
		t.Errorf("an amount that fits = %v, want allowed", v["verdict"])
	}
	if v := verdictOf(t, f.standing(f.customerID, "")); v["verdict"] != "allowed" {
		t.Errorf("no amount = %v, want allowed", v["verdict"])
	}
}

// RULE: a limit of zero is the hold the counter has today: blocked with the
// reason credit_hold, where paying down cannot help but a manager can
// approve.
func TestStandingStatesTheHoldTheCounterHasToday(t *testing.T) {
	testutil.LockOutboxTables(t)
	f := newFixture(t, testutil.RequireDB(t))
	mustExec(t, f.db, `UPDATE customers SET credit_limit = 0 WHERE id = $1`, f.customerID)
	r := f.standing(f.customerID, "amount_cents=100")
	v := verdictOf(t, r)
	if v["verdict"] != "blocked" || v["reason"] != "credit_hold" {
		t.Fatalf("verdict = %v reason %v, want blocked credit_hold", v["verdict"], v["reason"])
	}
	if r.body["on_hold"] != true {
		t.Errorf("on_hold = %v, want true", r.body["on_hold"])
	}
	if wayOf(t, v, "pay_down")["available"] != false {
		t.Errorf("pay_down on a hold must be unavailable")
	}
	if wayOf(t, v, "manager_override")["available"] != true {
		t.Errorf("manager_override on a hold must be available")
	}
}

// RULE: an account whose terms are due on receipt (the terms master's kind,
// whatever its code or name) is blocked from on account tender, with no
// override and the payment shapes the server enumerates for it.
func TestStandingBlocksACashOnlyAccountByTheTermsKind(t *testing.T) {
	testutil.LockOutboxTables(t)
	db := testutil.RequireDB(t)
	// a terms row whose code and name say nothing of cash: only its kind does
	addTerms(t, db, "ZZ-PAYNOW", "Pay now", "DUE_ON_RECEIPT", nil)
	f := newFixture(t, db)
	f.setTerms("ZZ-PAYNOW")

	r := f.standing(f.customerID, "")
	v := verdictOf(t, r)
	if v["verdict"] != "blocked" || v["reason"] != "cash_only_terms" {
		t.Fatalf("verdict = %v reason %v, want blocked cash_only_terms", v["verdict"], v["reason"])
	}
	if r.body["terms_kind"] != "due_on_receipt" {
		t.Errorf("terms_kind = %v, want due_on_receipt", r.body["terms_kind"])
	}
	if wayOf(t, v, "manager_override")["available"] != false || wayOf(t, v, "pay_down")["available"] != false {
		t.Errorf("a cash only account takes neither a pay down nor an override: %v", v["ways_forward"])
	}
	opts, _ := r.body["payment_options"].([]any)
	for _, o := range opts {
		if o == "account" {
			t.Errorf("payment_options = %v, must not offer account to a cash only account", opts)
		}
	}
}

// RULE: the read refuses what it should: the walk-in customer is no account
// (404), an unknown or malformed id too (404, 400), and a bad amount is 400.
func TestStandingRefusesWhatIsNoAccount(t *testing.T) {
	testutil.LockOutboxTables(t)
	f := newFixture(t, testutil.RequireDB(t))
	var walkIn uuid.UUID
	if err := f.db.Pool.QueryRow(context.Background(), `SELECT id FROM customers WHERE account_number = 'WALK-IN'`).Scan(&walkIn); err != nil {
		t.Fatal(err)
	}
	if r := f.standing(walkIn, ""); r.status != http.StatusNotFound {
		t.Errorf("walk-in standing = %d, want 404: %s", r.status, r.raw)
	}
	if r := f.standing(uuid.New(), ""); r.status != http.StatusNotFound {
		t.Errorf("unknown customer = %d, want 404: %s", r.status, r.raw)
	}
	if r := f.do("GET", "/api/v1/pos/customers/not-a-uuid/standing", nil, "X-Test-Role", "cashier", "X-Test-Sub", mustUUID(t)); r.status != http.StatusBadRequest {
		t.Errorf("malformed id = %d, want 400: %s", r.status, r.raw)
	}
	for _, q := range []string{"amount_cents=-5", "amount_cents=abc", "amount_cents=1.5", "sale_id=nope", "nothing=1"} {
		if r := f.standing(f.customerID, q); r.status != http.StatusBadRequest {
			t.Errorf("query %q = %d, want 400: %s", q, r.status, r.raw)
		}
	}
}

// RULE: the open credit memos show beside the balance, and the store credit
// payment option is offered only while there is any.
func TestStandingShowsTheOpenCreditMemos(t *testing.T) {
	testutil.LockOutboxTables(t)
	f := newFixture(t, testutil.RequireDB(t))
	r := f.standing(f.customerID, "")
	if got := num(t, r.body, "open_credit_memo_cents"); got != 0 {
		t.Errorf("open_credit_memo_cents = %d, want 0 before any return", got)
	}
	for _, o := range r.body["payment_options"].([]any) {
		if o == "store_credit" {
			t.Errorf("store_credit offered with no open credit memo")
		}
	}
	f.accountReturnOf()
	r = f.standing(f.customerID, "")
	if got := num(t, r.body, "open_credit_memo_cents"); got != 599 {
		t.Errorf("open_credit_memo_cents = %d, want 599", got)
	}
	found := false
	for _, o := range r.body["payment_options"].([]any) {
		if o == "store_credit" {
			found = true
		}
	}
	if !found {
		t.Errorf("payment_options = %v, want store_credit while a memo is open", r.body["payment_options"])
	}
}

// RULE: the completion enforces the verdict the read states, and the manager
// override is one act for one sale: another subject than the cashier with a
// manager role, a reason, a cap in cents, audited, consumed by that sale's
// completion and never again.
func TestAManagerOverrideIsOneActForOneSaleConsumedOnce(t *testing.T) {
	testutil.LockOutboxTables(t)
	f := newFixture(t, testutil.RequireDB(t))
	mustExec(t, f.db, `UPDATE customers SET credit_limit = 30.00 WHERE id = $1`, f.customerID)
	f.accountSaleOf("4", 2395)
	cashier := uuid.NewString()
	manager := uuid.NewString()

	sale := f.startSaleBy(cashier, &f.customerID)
	if r := f.addLine(sale, f.productLine("2")); r.status != http.StatusOK {
		t.Fatalf("add line = %d: %s", r.status, r.raw)
	}
	total := num(t, f.getSale(t, sale), "total_cents")
	// refused without an override: the read states the same verdict
	r := f.completeSale(sale, tender("account", total))
	if _, blockers, _ := errorOf(t, r); r.status != http.StatusConflict || len(blockers) != 1 || blockers[0] != "credit_limit" {
		t.Fatalf("over the limit with no override = %d: %s", r.status, r.raw)
	}
	if v := verdictOf(t, f.standing(f.customerID, "amount_cents="+itoa(total))); v["reason"] != "credit_limit" {
		t.Errorf("the read says %v while the completion refused credit_limit", v["reason"])
	}

	// who may approve: a cashier may not, the sale's own cashier may not
	if r := f.override(sale, "account", "agreed on the phone", total, "cashier", manager); r.status != http.StatusForbidden {
		t.Errorf("a cashier's override = %d, want 403: %s", r.status, r.raw)
	}
	if r := f.override(sale, "account", "agreed on the phone", total, "admin", cashier); r.status != http.StatusForbidden {
		t.Errorf("the sale's own cashier as manager = %d, want 403: %s", r.status, r.raw)
	}
	if r := f.override(sale, "account", "agreed on the phone", total, "admin", ""); r.status != http.StatusForbidden {
		t.Errorf("a manager with no subject = %d, want 403: %s", r.status, r.raw)
	}
	for _, bad := range []map[string]any{
		{"kind": "account", "reason": "", "cap_cents": total},
		{"kind": "account", "reason": "ok", "cap_cents": 0},
		{"kind": "account", "reason": "ok", "cap_cents": -5},
		{"kind": "nonsense", "reason": "ok", "cap_cents": total},
	} {
		r := f.do("POST", "/api/v1/pos/transactions/"+sale+"/overrides", bad, "X-Test-Role", "admin", "X-Test-Sub", manager)
		if r.status != http.StatusBadRequest {
			t.Errorf("override %v = %d, want 400: %s", bad, r.status, r.raw)
		}
	}
	if r := f.override(uuid.NewString(), "account", "x", 100, "admin", manager); r.status != http.StatusNotFound {
		t.Errorf("an override of a missing sale = %d, want 404: %s", r.status, r.raw)
	}

	// a cap under the account portion does not cover it
	if r := f.override(sale, "account", "agreed on the phone", total-1, "admin", manager); r.status != http.StatusCreated {
		t.Fatalf("override = %d: %s", r.status, r.raw)
	}
	if r := f.completeSale(sale, tender("account", total)); r.status != http.StatusConflict {
		t.Errorf("a cap under the account portion = %d, want 409: %s", r.status, r.raw)
	}
	// one live override per sale and kind
	if r := f.override(sale, "account", "again", total, "admin", manager); r.status != http.StatusConflict {
		t.Errorf("a second live override = %d, want 409: %s", r.status, r.raw)
	}
	// the live one is replaced by superseding: void it by consuming? no: the
	// manager asks again after the first is spent, so drop it directly here
	mustExec(t, f.db, `DELETE FROM pos_account_overrides WHERE sale_id = $1`, sale)
	if r := f.override(sale, "account", "agreed on the phone", total, "admin", manager); r.status != http.StatusCreated {
		t.Fatalf("override = %d: %s", r.status, r.raw)
	}
	// the read shows the override covering the sale
	if v := verdictOf(t, f.standing(f.customerID, "amount_cents="+itoa(total)+"&sale_id="+sale)); v["verdict"] != "allowed" || v["reason"] != "manager_override" {
		t.Errorf("the read with the sale = %v reason %v, want allowed by manager_override", v["verdict"], v["reason"])
	}
	if r := f.completeSale(sale, tender("account", total)); r.status != http.StatusOK {
		t.Fatalf("complete with the override = %d: %s", r.status, r.raw)
	}
	if n := f.scalar(`SELECT count(*) FROM pos_account_overrides WHERE sale_id = $1 AND consumed_at IS NOT NULL`, uuid.MustParse(sale)); n.(int64) != 1 {
		t.Errorf("consumed overrides of the sale = %v, want 1", n)
	}
	if n := f.scalar(`SELECT count(*) FROM audit_log WHERE entity_id::text = $1 AND action = 'pos.override.approved'`, sale); n.(int64) != 2 {
		t.Errorf("approved audit rows = %v, want 2 (the short one and the one spent)", n)
	}
	if n := f.scalar(`SELECT count(*) FROM audit_log WHERE entity_id::text = $1 AND action = 'pos.override.consumed'`, sale); n.(int64) != 1 {
		t.Errorf("consumed audit rows = %v, want 1", n)
	}

	// a later sale for the same customer is blocked again: the override went
	// with its sale
	next := f.startSaleBy(cashier, &f.customerID)
	if r := f.addLine(next, f.productLine("2")); r.status != http.StatusOK {
		t.Fatalf("add line = %d: %s", r.status, r.raw)
	}
	if r := f.completeSale(next, tender("account", total)); r.status != http.StatusConflict {
		t.Errorf("the next sale after a consumed override = %d, want 409: %s", r.status, r.raw)
	}
	f.assertARInvariants(t)
}

func itoa(n int64) string { return strconv.FormatInt(n, 10) }

// RULE: an override cannot cover a cash only account (the terms are not the
// manager's to waive) and a walk-in sale has no account to override.
func TestAnOverrideDoesNotWaiveCashOnlyTermsOrAWalkIn(t *testing.T) {
	testutil.LockOutboxTables(t)
	db := testutil.RequireDB(t)
	addTerms(t, db, "ZZ-PAYNOW2", "Pay now", "DUE_ON_RECEIPT", nil)
	f := newFixture(t, db)
	f.setTerms("ZZ-PAYNOW2")
	manager := uuid.NewString()
	sale := f.startSaleBy(uuid.NewString(), &f.customerID)
	if r := f.addLine(sale, f.productLine("1")); r.status != http.StatusOK {
		t.Fatalf("add line = %d: %s", r.status, r.raw)
	}
	total := num(t, f.getSale(t, sale), "total_cents")
	if r := f.override(sale, "account", "please", total, "admin", manager); r.status != http.StatusCreated {
		t.Fatalf("override = %d: %s", r.status, r.raw)
	}
	r := f.completeSale(sale, tender("account", total))
	if _, blockers, _ := errorOf(t, r); r.status != http.StatusConflict || len(blockers) != 1 || blockers[0] != "cash_only_terms" {
		t.Errorf("an account tender on cash only terms with an override = %d: %s, want 409 cash_only_terms", r.status, r.raw)
	}
	walk := f.startSale(nil)
	if r := f.override(walk, "account", "please", 100, "admin", manager); r.status != http.StatusConflict {
		t.Errorf("an account override of a walk-in sale = %d, want 409: %s", r.status, r.raw)
	}
}

// Two completions of one sale race on its override (pool size 4, three
// contenders): the sale completes once, the override is consumed once and
// the account is charged once.
func TestRacingCompletionsConsumeAnOverrideOnce(t *testing.T) {
	testutil.LockOutboxTables(t)
	f := newFixture(t, testutil.RequireDBMaxConns(t, 4))
	mustExec(t, f.db, `UPDATE customers SET credit_limit = 30.00 WHERE id = $1`, f.customerID)
	f.accountSaleOf("4", 2395)
	cashier, manager := uuid.NewString(), uuid.NewString()
	sale := f.startSaleBy(cashier, &f.customerID)
	if r := f.addLine(sale, f.productLine("2")); r.status != http.StatusOK {
		t.Fatalf("add line = %d: %s", r.status, r.raw)
	}
	total := num(t, f.getSale(t, sale), "total_cents")
	if r := f.override(sale, "account", "agreed", total, "admin", manager); r.status != http.StatusCreated {
		t.Fatalf("override = %d: %s", r.status, r.raw)
	}
	rev := num(t, f.getSale(t, sale), "revision")
	var wg sync.WaitGroup
	statuses := make([]int, 3)
	for i := range statuses {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			statuses[i] = f.completeSaleWithRevision(sale, rev, tender("account", total)).status
		}(i)
	}
	wg.Wait()
	wins := 0
	for _, st := range statuses {
		if st == http.StatusOK {
			wins++
		}
	}
	if wins != 1 {
		t.Errorf("statuses = %v, want exactly one 200", statuses)
	}
	if n := f.scalar(`SELECT count(*) FROM pos_account_overrides WHERE sale_id = $1 AND consumed_at IS NOT NULL`, uuid.MustParse(sale)); n.(int64) != 1 {
		t.Errorf("consumed overrides = %v, want 1", n)
	}
	if got := f.balance(); got != 2395+total {
		t.Errorf("balance_due = %d, want %d (charged once)", got, 2395+total)
	}
	f.assertARInvariants(t)
}
