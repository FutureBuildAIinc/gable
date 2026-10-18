// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package pos_test

// Tenders: check limits (per customer, else a database wide
// default; over the limit refused or approved by a manager override), "ID
// checked" with the ID type and never an ID number, store credit as a tender
// through the AR core's single writer, one account leg per sale, legs that
// sum exactly to the total and change only from cash.

import (
	"net/http"
	"strings"
	"sync"
	"testing"

	"github.com/gablelbm/gable/internal/testutil"
	"github.com/google/uuid"
)

// setCheckDefault writes the database wide check limit for one test.
func (f *fixture) setCheckDefault(cents string) {
	f.t.Helper()
	mustExec(f.t, f.db, `INSERT INTO system_settings (key, value) VALUES ('pos.check_limit_cents', $1)
		ON CONFLICT (key) DO UPDATE SET value = EXCLUDED.value`, cents)
	f.t.Cleanup(func() { mustExec(f.t, f.db, `DELETE FROM system_settings WHERE key = 'pos.check_limit_cents'`) })
}

// restockAll puts the fixture product back to 100 on hand between large sales.
func (f *fixture) restockAll() {
	f.t.Helper()
	mustExec(f.t, f.db, `UPDATE inventory SET quantity = 100, allocated = 0 WHERE location_id = $1`, f.yardID)
}

func (f *fixture) cartOf(qty string, customer *uuid.UUID) (string, int64) {
	f.t.Helper()
	id := f.startSale(customer)
	if r := f.addLine(id, f.productLine(qty)); r.status != http.StatusOK {
		f.t.Fatalf("add line = %d: %s", r.status, r.raw)
	}
	return id, num(f.t, f.getSale(f.t, id), "total_cents")
}

func blockerOf(t *testing.T, r resp, want string) {
	t.Helper()
	_, blockers, _ := errorOf(t, r)
	if r.status != http.StatusConflict || len(blockers) != 1 || blockers[0] != want {
		t.Fatalf("answer = %d: %s, want 409 %s", r.status, r.raw, want)
	}
}

// RULE: a customer's own check limit holds each check; the database wide
// default holds a customer with none; no limit anywhere means no limit.
func TestACheckIsHeldToTheCustomerLimitElseTheDefault(t *testing.T) {
	testutil.LockOutboxTables(t)
	f := newFixture(t, testutil.RequireDB(t))

	// no limit anywhere: a large check passes
	big, bigTotal := f.cartOf("100", &f.customerID)
	if r := f.completeSale(big, tender("check", bigTotal)); r.status != http.StatusOK {
		t.Fatalf("a check with no limit anywhere = %d: %s", r.status, r.raw)
	}

	// the default holds a customer with no limit of its own
	f.setCheckDefault("50000")
	over, overTotal := f.cartOf("100", &f.customerID)
	stock := f.stock()
	r := f.completeSale(over, tender("check", overTotal))
	blockerOf(t, r, "check_over_limit")
	if !strings.Contains(string(r.raw), "500.00") {
		t.Errorf("the refusal %s does not state the limit in words", r.raw)
	}
	if f.stock() != stock {
		t.Errorf("a refused check moved stock")
	}
	// a check at the limit with the rest in cash completes
	f.restockAll()
	if r := f.completeSale(over, tender("check", 50000), tender("cash", overTotal-50000)); r.status != http.StatusOK {
		t.Errorf("a check at the default limit = %d: %s", r.status, r.raw)
	}

	// the customer's own limit wins over the default, either way
	mustExec(t, f.db, `UPDATE customers SET check_limit = 5.00 WHERE id = $1`, f.customerID)
	small, smallTotal := f.cartOf("1", &f.customerID)
	blockerOf(t, f.completeSale(small, tender("check", smallTotal)), "check_over_limit")
	mustExec(t, f.db, `UPDATE customers SET check_limit = 900.00 WHERE id = $1`, f.customerID)
	f.restockAll()
	ok, okTotal := f.cartOf("100", &f.customerID)
	if r := f.completeSale(ok, tender("check", okTotal)); r.status != http.StatusOK {
		t.Errorf("a check under the customer's own higher limit = %d: %s", r.status, r.raw)
	}
	// a limit of zero takes no check at all
	mustExec(t, f.db, `UPDATE customers SET check_limit = 0 WHERE id = $1`, f.customerID)
	none, noneTotal := f.cartOf("1", &f.customerID)
	blockerOf(t, f.completeSale(none, tender("check", noneTotal)), "check_over_limit")
	if v := f.standing(f.customerID, ""); num(t, v.body, "check_limit_cents") != 0 {
		t.Errorf("the standing states check_limit_cents = %v, want 0", v.body["check_limit_cents"])
	} else {
		for _, o := range v.body["payment_options"].([]any) {
			if o == "check" {
				t.Errorf("payment_options offers check to a customer who may write none")
			}
		}
	}
}

// RULE: a check over the limit is approved by a manager override of kind
// check: one act for the sale, its cap in cents holds the check, consumed by
// the completion once.
func TestAManagerOverrideApprovesACheckOverTheLimitOnce(t *testing.T) {
	testutil.LockOutboxTables(t)
	f := newFixture(t, testutil.RequireDB(t))
	f.setCheckDefault("1000")
	cashier, manager := uuid.NewString(), uuid.NewString()
	sale := f.startSaleBy(cashier, nil)
	if r := f.addLine(sale, f.productLine("2")); r.status != http.StatusOK {
		t.Fatalf("add line = %d: %s", r.status, r.raw)
	}
	total := num(t, f.getSale(t, sale), "total_cents") // 1198
	blockerOf(t, f.completeSale(sale, tender("check", total)), "check_over_limit")
	// a cap under the check does not cover it
	if r := f.override(sale, "check", "regular customer", total-1, "admin", manager); r.status != http.StatusCreated {
		t.Fatalf("override = %d: %s", r.status, r.raw)
	}
	blockerOf(t, f.completeSale(sale, tender("check", total)), "check_over_limit")
	mustExec(t, f.db, `DELETE FROM pos_account_overrides WHERE sale_id = $1`, sale)
	if r := f.override(sale, "check", "regular customer", total, "admin", manager); r.status != http.StatusCreated {
		t.Fatalf("override = %d: %s", r.status, r.raw)
	}
	if r := f.completeSale(sale, tender("check", total)); r.status != http.StatusOK {
		t.Fatalf("a check with its override = %d: %s", r.status, r.raw)
	}
	if n := f.scalar(`SELECT count(*) FROM pos_account_overrides WHERE sale_id = $1 AND consumed_at IS NOT NULL AND kind = 'check'`, uuid.MustParse(sale)); n.(int64) != 1 {
		t.Errorf("consumed check overrides = %v, want 1", n)
	}
	// an account override does not approve a check
	other := f.startSaleBy(cashier, nil)
	f.addLine(other, f.productLine("2"))
	if r := f.override(other, "account", "x", total, "admin", manager); r.status != http.StatusConflict {
		t.Errorf("an account override on a walk-in sale = %d, want 409", r.status)
	}
}

// RULE: "ID checked" and the ID type ride on a check or an account tender and
// are stored with it; a number is never taken (the body has no field for it),
// a type needs "checked", the type is a closed list, and other tenders carry
// none.
func TestIDCheckedIsRecordedWithTheTypeNeverANumber(t *testing.T) {
	testutil.LockOutboxTables(t)
	f := newFixture(t, testutil.RequireDB(t))
	sale, total := f.cartOf("1", &f.customerID)

	for name, bad := range map[string]map[string]any{
		"a type with no check":  {"method": "check", "amount_cents": total, "id_type": "drivers_license"},
		"an unknown type":       {"method": "check", "amount_cents": total, "id_checked": true, "id_type": "library_card"},
		"ID on a cash tender":   {"method": "cash", "amount_cents": total, "id_checked": true, "id_type": "state_id"},
		"an ID number":          {"method": "check", "amount_cents": total, "id_checked": true, "id_number": "D1234567"},
		"a checked false typed": {"method": "check", "amount_cents": total, "id_checked": false, "id_type": "passport"},
	} {
		r := f.do("POST", "/api/v1/pos/transactions/"+sale+"/complete",
			map[string]any{"tenders": []map[string]any{bad}, "revision": num(t, f.getSale(t, sale), "revision")},
			"X-Test-Role", "cashier", "X-Test-Sub", mustUUID(t))
		if r.status != http.StatusBadRequest {
			t.Errorf("%s = %d, want 400: %s", name, r.status, r.raw)
		}
	}
	good := map[string]any{"method": "check", "amount_cents": total, "id_checked": true, "id_type": "drivers_license", "reference": "1042"}
	r := f.do("POST", "/api/v1/pos/transactions/"+sale+"/complete",
		map[string]any{"tenders": []map[string]any{good}, "revision": num(t, f.getSale(t, sale), "revision")},
		"X-Test-Role", "cashier", "X-Test-Sub", mustUUID(t))
	if r.status != http.StatusOK {
		t.Fatalf("a check with its ID = %d: %s", r.status, r.raw)
	}
	tenders := r.body["tenders"].([]any)
	got := tenders[0].(map[string]any)
	if got["id_checked"] != true || got["id_type"] != "drivers_license" {
		t.Errorf("tender = %v, want id_checked true and the type stored", got)
	}
	if n := f.scalar(`SELECT count(*) FROM information_schema.columns WHERE table_name = 'pos_tenders' AND column_name ILIKE '%number%'`); n.(int64) != 0 {
		t.Errorf("pos_tenders has a number column: an ID number is never stored")
	}
}

// RULE: store credit is the customer's open credit memos applied to the sale
// through the AR core: the memo is used, the sale's tenders show it, the
// drawer expects none of it, and the subledger stays balanced.
func TestStoreCreditPaysASaleThroughTheARCore(t *testing.T) {
	testutil.LockOutboxTables(t)
	f := newFixture(t, testutil.RequireDB(t))
	f.accountReturnOf() // 599 of store credit
	f.ensureTill()
	sale, total := f.cartOf("2", &f.customerID) // 1198
	r := f.completeSale(sale, tender("store_credit", 599), tender("cash", total-599))
	if r.status != http.StatusOK {
		t.Fatalf("a sale paid half in store credit = %d: %s", r.status, r.raw)
	}
	memoStatus := f.scalar(`SELECT status || ':' || ROUND(amount_open * 100)::bigint FROM credit_memos WHERE customer_id = $1 AND reason_code = 'RETURN' ORDER BY created_at DESC LIMIT 1`, f.customerID)
	if memoStatus != "APPLIED:0" {
		t.Errorf("memo = %v, want APPLIED:0", memoStatus)
	}
	var sc map[string]any
	for _, x := range r.body["tenders"].([]any) {
		if m := x.(map[string]any); m["method"] == "store_credit" {
			sc = m
		}
	}
	if sc == nil || num(t, sc, "amount_cents") != 599 || sc["payment_id"] != nil {
		t.Fatalf("store credit tender = %v, want 599 and no payment", sc)
	}
	if ref, _ := sc["reference"].(string); !strings.HasPrefix(ref, "CM-") {
		t.Errorf("store credit reference = %v, want the memo number", sc["reference"])
	}
	inv := f.scalar(`SELECT ROUND(amount_open * 100)::bigint || ':' || status FROM invoices WHERE id = $1`, uuid.MustParse(str(t, r.body, "invoice_id")))
	if inv != "0:PAID" {
		t.Errorf("invoice = %v, want paid with nothing open", inv)
	}
	if n := f.scalar(`SELECT count(*) FROM ar_applications WHERE kind = 'CREDIT_MEMO' AND invoice_id = $1`, uuid.MustParse(str(t, r.body, "invoice_id"))); n.(int64) != 1 {
		t.Errorf("credit memo applications = %v, want 1", n)
	}
	// the drawer expects the cash only
	rep := f.tillReport(t)
	exp := rep["expected_by_method"].(map[string]any)
	if _, has := exp["STORE_CREDIT"]; has {
		t.Errorf("expected_by_method = %v, must not hold store credit", exp)
	}
	f.assertARInvariants(t)
}

// RULE: store credit is refused beyond what the account holds
// (store_credit_short), for a walk-in (walk_in_store_credit) and twice in a
// sale; a partly used memo keeps the rest; a void gives the credit back.
func TestStoreCreditLimitsAndTheVoidReturnsIt(t *testing.T) {
	testutil.LockOutboxTables(t)
	f := newFixture(t, testutil.RequireDB(t))
	f.accountReturnOf() // 599
	f.ensureTill()

	sale, total := f.cartOf("2", &f.customerID)
	blockerOf(t, f.completeSale(sale, tender("store_credit", 600), tender("cash", total-600)), "store_credit_short")
	blockerOf(t, f.completeSale(sale, tender("store_credit", 100), tender("store_credit", 100), tender("cash", total-200)), "one_store_credit_leg")
	walk, walkTotal := f.cartOf("1", nil)
	blockerOf(t, f.completeSale(walk, tender("store_credit", 100), tender("cash", walkTotal-100)), "walk_in_store_credit")

	r := f.completeSale(sale, tender("store_credit", 300), tender("cash", total-300))
	if r.status != http.StatusOK {
		t.Fatalf("part store credit = %d: %s", r.status, r.raw)
	}
	if got := f.scalar(`SELECT status || ':' || ROUND(amount_open * 100)::bigint FROM credit_memos WHERE customer_id = $1 AND reason_code = 'RETURN' ORDER BY created_at DESC LIMIT 1`, f.customerID); got != "PARTIAL:-299" {
		t.Errorf("memo = %v, want PARTIAL:-299 (299 of credit still open)", got)
	}
	if v := f.standing(f.customerID, ""); num(t, v.body, "open_credit_memo_cents") != 299 {
		t.Errorf("open_credit_memo_cents = %v, want 299", v.body["open_credit_memo_cents"])
	}

	// the void puts the 300 back
	v := f.do("POST", "/api/v1/pos/transactions/"+str(t, r.body, "id")+"/void",
		map[string]any{"reason": "customer changed their mind", "revision": num(t, r.body, "revision")},
		"X-Test-Role", "cashier", "X-Test-Sub", mustUUID(t))
	if v.status != http.StatusOK {
		t.Fatalf("void of a store credit sale = %d: %s", v.status, v.raw)
	}
	if got := f.scalar(`SELECT status || ':' || ROUND(amount_open * 100)::bigint FROM credit_memos WHERE customer_id = $1 AND reason_code = 'RETURN' ORDER BY created_at DESC LIMIT 1`, f.customerID); got != "OPEN:-599" {
		t.Errorf("memo after the void = %v, want OPEN:-599", got)
	}
	f.assertARInvariants(t)
}

// RULE: a sale carries at most one account leg; the legs that stay sum
// exactly to the total and change comes out of cash only: an over tender on
// the account or a check is never change.
func TestLegsSumToTheTotalAndChangeIsCashOnly(t *testing.T) {
	testutil.LockOutboxTables(t)
	f := newFixture(t, testutil.RequireDB(t))
	sale, total := f.cartOf("2", &f.customerID)
	blockerOf(t, f.completeSale(sale, tender("account", 500), tender("account", total-500)), "one_account_leg")
	// an account leg over the total with cash for the "change" is no change
	blockerOf(t, f.completeSale(sale, tender("account", total+300), tender("cash", 100)), "change_from_cash")
	blockerOf(t, f.completeSale(sale, tender("check", total+300), tender("cash", 100)), "change_from_cash")

	// cash over the total gives change; the legs kept sum exactly
	r := f.completeSale(sale, tender("account", 500), tender("cash", total-500+250))
	if r.status != http.StatusOK {
		t.Fatalf("cash over the total = %d: %s", r.status, r.raw)
	}
	if got := num(t, r.body, "change_cents"); got != 250 {
		t.Errorf("change_cents = %d, want 250", got)
	}
	var kept int64
	for _, x := range r.body["tenders"].([]any) {
		kept += num(t, x.(map[string]any), "amount_cents")
	}
	if kept != total {
		t.Errorf("the tenders are stored net of change and sum %d, want exactly the total %d", kept, total)
	}
	f.assertARInvariants(t)
}

// Three sales race for the same open credit memo (pool size 4, three
// contenders): the memo locks serialize them, exactly one is paid with the
// credit and the memo is never used past what it held.
func TestRacingSalesSpendAStoreCreditMemoOnce(t *testing.T) {
	testutil.LockOutboxTables(t)
	f := newFixture(t, testutil.RequireDBMaxConns(t, 4))
	f.accountReturnOf() // 599
	carts := make([]string, 3)
	totals := make([]int64, 3)
	for i := range carts {
		carts[i], totals[i] = f.cartOf("2", &f.customerID)
	}
	statuses := make([]int, 3)
	var wg sync.WaitGroup
	for i := range carts {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			statuses[i] = f.completeSale(carts[i], tender("store_credit", 599), tender("cash", totals[i]-599)).status
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
		t.Errorf("statuses = %v, want exactly one sale paid with the 599 of credit", statuses)
	}
	if got := f.scalar(`SELECT status || ':' || ROUND(amount_open * 100)::bigint FROM credit_memos WHERE customer_id = $1 AND reason_code = 'RETURN' ORDER BY created_at DESC LIMIT 1`, f.customerID); got != "APPLIED:0" {
		t.Errorf("memo = %v, want APPLIED:0", got)
	}
	f.assertARInvariants(t)
}
