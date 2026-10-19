// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package pos_test

// The first sweep's money rules for the counter's account sales, tenders,
// returns and exchanges: a walk-in exchange never puts the difference on the
// walk-in customer's account, cash leaves the drawer only for the cash or
// check the sale itself kept (a refund_method manager override excepts it),
// the credit check counts the store credit the same sale spends, and a
// sale's cashier is the authenticated caller, not a body field.

import (
	"net/http"
	"testing"

	"github.com/gablelbm/gable/internal/testutil"
	"github.com/google/uuid"
)

// postReturn posts a linked return of the lines with the refund method.
func (f *fixture) postReturn(t *testing.T, saleID, method string, lines ...map[string]any) resp {
	t.Helper()
	f.ensureOpenTill(t)
	return f.do("POST", "/api/v1/pos/returns", map[string]any{
		"register_id": f.register, "original_sale_id": saleID, "refund_method": method,
		"reason": "sweep", "lines": lines,
	}, "X-Test-Role", "cashier", "X-Test-Sub", mustUUID(t))
}

// walkInOpenCents sums the open invoice cents of the walk-in customer: a
// walk-in sale pays, so nothing may stay open on it.
func (f *fixture) walkInOpenCents(t *testing.T) int64 {
	t.Helper()
	return f.scalar(`SELECT ROUND(COALESCE(SUM(i.amount_open), 0) * 100)::bigint FROM invoices i
		JOIN customers c ON c.id = i.customer_id WHERE c.account_number = 'WALK-IN'`).(int64)
}

// cashRefundedCents sums the cash the customer's credit memos paid out of
// the drawer: the money that left for returns of this customer.
func (f *fixture) cashRefundedCents(t *testing.T) int64 {
	t.Helper()
	return f.scalar(`SELECT ROUND(COALESCE(SUM(ABS(rf.amount)), 0) * 100)::bigint FROM payment_refunds rf
		WHERE rf.method = 'CASH' AND rf.credit_memo_id IN
		(SELECT credit_memo_id FROM pos_returns WHERE customer_id = $1)`, f.customerID).(int64)
}

// firstLineOf answers the sale's first line id.
func (f *fixture) firstLineOf(t *testing.T, saleID string) string {
	t.Helper()
	return f.getSale(t, saleID)["lines"].([]any)[0].(map[string]any)["id"].(string)
}

// RULE (P1-1): a walk-in exchange with an ACCOUNT tender is refused: the
// walk-in customer buys on the barrelhead, and no invoice stays open on it.
func TestSweepPR181_P1_1_WalkInExchangeRefusesAccountTender(t *testing.T) {
	testutil.LockOutboxTables(t)
	f := newFixture(t, testutil.RequireDB(t))
	f.openTill(0)
	sale, line, _ := f.cashSaleLine(t, "1") // 599 in the drawer
	r := f.exchange("", map[string]any{
		"original_sale_id": sale, "reason": "upgrade",
		"returned_lines":  []map[string]any{lineOf(line, "1", "wrong_item")},
		"sale_lines":      f.replaceWith("3"), // 1796
		"tenders":         []map[string]any{tender("account", 1197)},
	})
	blockerOf(t, r, "walk_in_account")
	if got := f.walkInOpenCents(t); got != 0 {
		t.Errorf("walk-in open invoice cents = %d, want 0", got)
	}
	f.assertARInvariants(t)
}

// RULE (P1-2a): a store credit sale's goods come back as store credit, not
// drawer cash: the cash return of a wholly store credit sale is refused, the
// account return leaves the credit memo open, and only a refund_method
// manager override lets the cash out, once.
func TestSweepPR181_P1_2a_StoreCreditSaleReturnRefusesCashWithoutOverride(t *testing.T) {
	testutil.LockOutboxTables(t)
	f := newFixture(t, testutil.RequireDB(t))
	f.openTill(0)

	// a refused cash return pays nothing out of the drawer
	f.accountReturnOf() // a 599 open credit memo
	saleA, _ := f.cartOf("1", &f.customerID)
	if r := f.completeSale(saleA, tender("store_credit", 599)); r.status != http.StatusOK {
		t.Fatalf("store credit sale = %d: %s", r.status, r.raw)
	}
	blockerOf(t, f.postReturn(t, saleA, "cash", lineOf(f.firstLineOf(t, saleA), "1", "defective")), "exceeds_cash_tender")
	if got := f.cashRefundedCents(t); got != 0 {
		t.Errorf("cash refunded = %d after the refused return, want 0", got)
	}

	// the account return succeeds and leaves the credit open
	f.accountReturnOf()
	saleB, _ := f.cartOf("1", &f.customerID)
	if r := f.completeSale(saleB, tender("store_credit", 599)); r.status != http.StatusOK {
		t.Fatalf("store credit sale = %d: %s", r.status, r.raw)
	}
	r := f.postReturn(t, saleB, "account", lineOf(f.firstLineOf(t, saleB), "1", "defective"))
	if r.status != http.StatusCreated {
		t.Fatalf("account return = %d: %s", r.status, r.raw)
	}
	if got := f.memoState(str(t, r.body, "credit_memo_id")); got != "OPEN:-599" {
		t.Errorf("memo = %s, want OPEN:-599", got)
	}

	// a refund_method override lets the cash out once
	f.accountReturnOf()
	saleC, _ := f.cartOf("1", &f.customerID)
	if r := f.completeSale(saleC, tender("store_credit", 599)); r.status != http.StatusOK {
		t.Fatalf("store credit sale = %d: %s", r.status, r.raw)
	}
	if r := f.override(saleC, "refund_method", "manager approved cash payout", 599, "admin", uuid.NewString()); r.status != http.StatusCreated {
		t.Fatalf("refund_method override = %d: %s", r.status, r.raw)
	}
	r = f.postReturn(t, saleC, "cash", lineOf(f.firstLineOf(t, saleC), "1", "defective"))
	if r.status != http.StatusCreated {
		t.Fatalf("cash return under the override = %d: %s", r.status, r.raw)
	}
	if got := f.scalar(`SELECT count(*) FROM pos_account_overrides WHERE sale_id = $1 AND consumed_at IS NOT NULL`,
		uuid.MustParse(saleC)).(int64); got != 1 {
		t.Errorf("consumed overrides = %d, want 1", got)
	}
	if got := f.scalar(`SELECT count(*) FROM pos_account_overrides WHERE sale_id = $1 AND consumed_at IS NULL`,
		uuid.MustParse(saleC)).(int64); got != 0 {
		t.Errorf("live overrides = %d after the return, want 0 (it is spent and never reused)", got)
	}
	f.assertARInvariants(t)
}

// RULE (P1-2b): goods bought on account come back to the account, not to the
// drawer: a cash return and a cash exchange surplus of an account sale are
// refused, and the exchange's surplus stays on the account.
func TestSweepPR181_P1_2b_AccountSaleReturnAndExchangeSurplusRefuseCashWithoutOverride(t *testing.T) {
	testutil.LockOutboxTables(t)
	f := newFixture(t, testutil.RequireDB(t))
	f.openTill(0)

	// the sale: 4 units, 2395, wholly on account, its invoice open
	sale, _ := f.cartOf("4", &f.customerID)
	if r := f.completeSale(sale, tender("account", 2395)); r.status != http.StatusOK {
		t.Fatalf("account sale = %d: %s", r.status, r.raw)
	}
	if got := f.scalar(`SELECT ROUND(amount_open * 100)::bigint FROM invoices WHERE id =
		(SELECT invoice_id FROM pos_transactions WHERE id = $1)`, uuid.MustParse(sale)).(int64); got != 2395 {
		t.Fatalf("invoice open = %d, want 2395", got)
	}
	line := f.firstLineOf(t, sale)

	// a cash return of 2 units (1198) is refused and pays no cash
	blockerOf(t, f.postReturn(t, sale, "cash", lineOf(line, "2", "defective")), "exceeds_cash_tender")
	if got := f.cashRefundedCents(t); got != 0 {
		t.Errorf("cash refunded = %d after the refused return, want 0", got)
	}

	// an exchange of the 4 units for 1 (surplus 1796) refuses an explicit
	// cash refund method
	cashExchange := map[string]any{
		"original_sale_id": sale, "reason": "downsized", "refund_method": "cash",
		"returned_lines": []map[string]any{lineOf(line, "4", "overbought")},
		"sale_lines":     f.replaceWith("1"),
	}
	blockerOf(t, f.exchange("", cashExchange), "exceeds_cash_tender")

	// with the surplus left on the account it answers 201, no cash out
	acctExchange := map[string]any{
		"original_sale_id": sale, "reason": "downsized", "refund_method": "account",
		"returned_lines": []map[string]any{lineOf(line, "4", "overbought")},
		"sale_lines":     f.replaceWith("1"),
	}
	r := f.exchange("", acctExchange)
	if r.status != http.StatusCreated {
		t.Fatalf("account surplus exchange = %d: %s", r.status, r.raw)
	}
	if num(t, r.body, "refunded_cents") != 0 {
		t.Errorf("refunded_cents = %v, want 0 (the surplus stays on the account)", r.body["refunded_cents"])
	}
	if got := f.cashRefundedCents(t); got != 0 {
		t.Errorf("cash refunded = %d after the exchange, want 0", got)
	}
	// the 1796 surplus stays with the customer: the memo's remainder
	memoID := str(t, r.body["return"].(map[string]any), "credit_memo_id")
	if got := f.memoState(memoID); got != "PARTIAL:-1796" {
		t.Errorf("memo = %s, want PARTIAL:-1796", got)
	}
	f.assertARInvariants(t)
}

// RULE (P2-1): the credit check counts the store credit the same sale spends:
// the open memo the sale consumes no longer offsets the account portion it
// adds, so a sale over the limit is refused without an override.
func TestSweepPR181_P2_1_StoreCreditPlusAccountHonoursCreditLimit(t *testing.T) {
	testutil.LockOutboxTables(t)
	f := newFixture(t, testutil.RequireDB(t))
	f.openTill(0)
	f.accountReturnOf() // a 599 open credit memo: open receivable -599
	mustExec(t, f.db, `UPDATE customers SET credit_limit = 10.00 WHERE id = $1`, f.customerID)
	sale, _ := f.cartOf("3", &f.customerID) // 1796
	r := f.completeSale(sale, tender("store_credit", 599), tender("account", 1197))
	blockerOf(t, r, "credit_limit")
	f.assertARInvariants(t)
}

// RULE (P2-2): the sale's cashier is the authenticated caller: a body
// cashier_id that differs from the subject is not recorded, so the caller
// who started the sale cannot approve its override alone.
func TestSweepPR181_P2_2_StartTransactionUsesAuthenticatedSubjectAsCashier(t *testing.T) {
	testutil.LockOutboxTables(t)
	f := newFixture(t, testutil.RequireDB(t))
	f.ensureTill()
	managerA, cashierB := uuid.NewString(), uuid.NewString()
	r := f.do("POST", "/api/v1/pos/transactions", map[string]any{
		"register_id": f.register, "cashier_id": cashierB, "customer_id": f.customerID.String(),
	}, "X-Test-Role", "admin", "X-Test-Sub", managerA)
	if r.status != http.StatusCreated {
		t.Fatalf("start sale = %d: %s", r.status, r.raw)
	}
	if got := str(t, f.getSale(t, str(t, r.body, "id")), "cashier_id"); got != managerA {
		t.Errorf("cashier_id = %s, want the authenticated subject %s", got, managerA)
	}
	// the manager who started the sale is its cashier: the override answer is
	// the separation of duties refusal
	ov := f.do("POST", "/api/v1/pos/transactions/"+str(t, r.body, "id")+"/overrides",
		map[string]any{"kind": "account", "reason": "approved", "cap_cents": 500},
		"X-Test-Role", "admin", "X-Test-Sub", managerA)
	if ov.status != http.StatusForbidden {
		t.Errorf("override by the sale's own cashier = %d: %s, want 403", ov.status, ov.raw)
	}
}
