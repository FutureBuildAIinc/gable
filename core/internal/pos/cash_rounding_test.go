// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package pos_test

// Cash rounding without pennies (CP-18): the branch's own rule rounds the
// cash leg only, the drawer keeps the rounded cash, the difference is the
// branch's rounding income or its loss, and the tax never moves for it.
//
// The rule is off until a branch turns it on, so every other sale in the
// suite is untouched by it.
//
// These tests run on a branch of their own (a private locations row of type
// BRANCH, with its own yard, register, stock and customer). The branch's
// rate and its rule are read live, so a fixture of another package that
// wrote the deployment's default branch would move the rate under a sale
// and the rounded cash due would no longer be the cents the test tenders.
// On a branch of their own, no other fixture can move either.

import (
	"net/http"
	"strings"
	"sync"
	"testing"

	"github.com/gablelbm/gable/internal/testutil"
)

// setRounding sets the fixture branch's cash rounding rule.
func (f *fixture) setRounding(mode string) {
	f.t.Helper()
	mustExec(f.t, f.db, `UPDATE locations SET cash_rounding_mode = $2 WHERE id = $1`, f.branchID, mode)
	f.t.Cleanup(func() {
		mustExec(f.t, f.db, `UPDATE locations SET cash_rounding_mode = 'OFF' WHERE id = $1`, f.branchID)
	})
}

// noTax takes the branch's tax off, so a sale's total is its line's own
// figure and the tax account can be shown not to move.
func (f *fixture) noTax() {
	f.t.Helper()
	mustExec(f.t, f.db, `UPDATE locations SET default_tax_rate = 0 WHERE id = $1`, f.branchID)
	f.t.Cleanup(func() {
		mustExec(f.t, f.db, `UPDATE locations SET default_tax_rate = 0.088750 WHERE id = $1`, f.branchID)
	})
}

// saleAtPrice rings a one-line sale of one unit at the given cents and
// answers the sale and its line.
func (f *fixture) saleAtPrice(cents int64) (string, string) {
	f.t.Helper()
	mustExec(f.t, f.db, `UPDATE products SET base_price = $2::numeric / 100 WHERE id = $1`, f.productID, cents)
	saleID := f.startSale(nil)
	if r := f.addLine(saleID, f.productLine("1")); r.status != http.StatusOK {
		f.t.Fatalf("add line = %d: %s", r.status, r.raw)
	}
	lineID := f.getSale(f.t, saleID)["lines"].([]any)[0].(map[string]any)["id"].(string)
	return saleID, lineID
}

// cashPayment is the cash the drawer kept for the sale, in cents: the payment
// the sale booked for its cash tender.
func (f *fixture) cashPayment(saleID string) int64 {
	f.t.Helper()
	return f.paymentCents(saleID, "CASH")
}

// paymentCents is what the drawer holds for one method of a sale, in cents.
func (f *fixture) paymentCents(saleID, method string) int64 {
	f.t.Helper()
	v := f.scalar(`SELECT COALESCE(SUM(ROUND(p.amount * 100)::bigint), 0)::bigint
		FROM payments p JOIN pos_tenders t ON t.payment_id = p.id
		WHERE t.transaction_id = $1 AND p.method = $2 AND p.status = 'POSTED'`, saleID, method)
	n, ok := v.(int64)
	if !ok {
		f.t.Fatalf("the %s payment reads %T, want int64", method, v)
	}
	return n
}

// tillExpectedCash is the till's expected cash for the register, in cents.
func (f *fixture) tillExpectedCash() int64 {
	f.t.Helper()
	r := f.do("GET", "/api/v1/pos/till/"+f.openSessionID()+"/report", nil)
	if r.status != http.StatusOK {
		f.t.Fatalf("till report = %d: %s", r.status, r.raw)
	}
	byMethod, ok := r.body["expected_by_method"].(map[string]any)
	if !ok {
		f.t.Fatalf("the report carries no expected_by_method: %s", r.raw)
	}
	if _, ok := byMethod["CASH"]; !ok {
		f.t.Fatalf("the report carries no CASH figure: %s", r.raw)
	}
	return num(f.t, byMethod, "CASH")
}

// openSessionID is the fixture register's open till session.
func (f *fixture) openSessionID() string {
	f.t.Helper()
	v := f.scalar(`SELECT id::text FROM till_sessions WHERE register_id = $1 AND status = 'OPEN' ORDER BY opened_at DESC LIMIT 1`, f.register)
	s, ok := v.(string)
	if !ok {
		f.t.Fatalf("the open till session reads %T, want a string", v)
	}
	return s
}

// RULE: the rule is off until a branch turns it on, and a cash sale then
// takes the money exactly as it was tendered.
func TestCP18_OffByDefault(t *testing.T) {
	testutil.LockOutboxTables(t)
	f := newOwnBranchFixture(t, testutil.RequireDB(t))
	f.noTax()
	saleID, _ := f.saleAtPrice(103)
	r := f.completeSale(saleID, tender("cash", 103))
	if r.status != http.StatusOK {
		t.Fatalf("complete = %d: %s", r.status, r.raw)
	}
	if got := f.cashPayment(saleID); got != 103 {
		t.Errorf("the cash payment = %d cents, want 103", got)
	}
	if got := f.accountBalance("4095"); got != 0 {
		t.Errorf("the rounding income carries %d cents, want none", got)
	}
	if got := f.accountBalance("5045"); got != 0 {
		t.Errorf("the rounding loss carries %d cents, want none", got)
	}
	f.assertARInvariants(t)
}

// RULE: a cash sale the branch rounds books the rounded cash, the rounding
// income for the difference, the exact revenue and no tax; a card sale and a
// check sale settle to the exact cent.
func TestCP18_CashOnlyRounded(t *testing.T) {
	testutil.LockOutboxTables(t)
	f := newOwnBranchFixture(t, testutil.RequireDB(t))
	f.noTax()
	f.setRounding("NEAREST_5_CENTS")

	// A cash total of $1.03 rounds up to $1.05: the drawer keeps the $1.05
	// and the branch books the two cents as its rounding income.
	saleID, _ := f.saleAtPrice(103)
	r := f.completeSale(saleID, tender("cash", 105))
	if r.status != http.StatusOK {
		t.Fatalf("complete = %d: %s", r.status, r.raw)
	}
	if got := f.cashPayment(saleID); got != 105 {
		t.Errorf("the cash payment = %d cents, want 105", got)
	}
	if got := f.accountBalance("4095"); got != -2 {
		t.Errorf("the rounding income = %d cents, want a credit of two", got)
	}
	if got := f.accountBalance("4010"); got != -103 {
		t.Errorf("the revenue = %d cents, want a credit of 103", got)
	}
	if got := f.accountBalance("2020"); got != 0 {
		t.Errorf("the tax = %d cents, want none", got)
	}
	if got := f.accountBalance("5045"); got != 0 {
		t.Errorf("the rounding loss = %d cents, want none", got)
	}

	// A cash total of $1.07 rounds down to $1.05: the drawer keeps $1.05,
	// the branch books the two cents as its rounding loss, and the invoice
	// closes as PAID.
	downSale, _ := f.saleAtPrice(107)
	r = f.completeSale(downSale, tender("cash", 105))
	if r.status != http.StatusOK {
		t.Fatalf("the rounded-down cash sale = %d: %s", r.status, r.raw)
	}
	if got := f.cashPayment(downSale); got != 105 {
		t.Errorf("the rounded-down cash payment = %d cents, want 105", got)
	}
	if got := f.accountBalance("5045"); got != 2 {
		t.Errorf("the rounding loss = %d cents, want a debit of two", got)
	}
	if got := f.accountBalance("4010"); got != -210 {
		t.Errorf("the revenue = %d cents, want a credit of 210", got)
	}
	invStatus := f.scalar(`SELECT i.status FROM invoices i JOIN pos_transactions t ON t.invoice_id = i.id WHERE t.id = $1`, downSale)
	if invStatus != "PAID" {
		t.Errorf("the rounded-down sale's invoice status = %v, want PAID", invStatus)
	}

	// A card sale settles to the exact cent: the rule moves the cash only.
	cardSale, _ := f.saleAtPrice(107)
	r = f.completeSale(cardSale, tender("card", 107))
	if r.status != http.StatusOK {
		t.Fatalf("the card sale = %d: %s", r.status, r.raw)
	}
	if got := f.paymentCents(cardSale, "CARD"); got != 107 {
		t.Errorf("the card payment = %d cents, want the exact 107", got)
	}
	if got := f.accountBalance("4095"); got != -2 {
		t.Errorf("the rounding income = %d cents after a card sale, want the two cents of the cash sale", got)
	}
	if got := f.accountBalance("5045"); got != 2 {
		t.Errorf("the rounding loss = %d cents after a card sale, want the two cents of the rounded-down cash sale", got)
	}

	// A check sale also settles to the exact cent.
	checkSale, _ := f.saleAtPrice(102)
	r = f.completeSale(checkSale, tender("check", 102))
	if r.status != http.StatusOK {
		t.Fatalf("the check sale = %d: %s", r.status, r.raw)
	}
	if got := f.paymentCents(checkSale, "CHECK"); got != 102 {
		t.Errorf("the check payment = %d cents, want the exact 102", got)
	}
	if got := f.accountBalance("4095"); got != -2 {
		t.Errorf("the rounding income = %d cents after a check sale, want the two cents of the cash sale", got)
	}
	if got := f.accountBalance("5045"); got != 2 {
		t.Errorf("the rounding loss = %d cents after a check sale, want the two cents of the rounded-down cash sale", got)
	}
	f.assertARInvariants(t)
}

// RULE: a split tender rounds the cash leg only; the card leg settles to the
// exact cent and the total is not rounded as a whole.
func TestCP18_SplitTenderRoundsCashLeg(t *testing.T) {
	testutil.LockOutboxTables(t)
	f := newOwnBranchFixture(t, testutil.RequireDB(t))
	f.noTax()
	f.setRounding("NEAREST_5_CENTS")

	// The cash leg of $0.53 rounds up to $0.55; the card takes $0.50.
	saleID, _ := f.saleAtPrice(103)
	r := f.completeSale(saleID, tender("card", 50), tender("cash", 60))
	if r.status != http.StatusOK {
		t.Fatalf("complete = %d: %s", r.status, r.raw)
	}
	if got := f.cashPayment(saleID); got != 55 {
		t.Errorf("the cash payment = %d cents, want the rounded 55", got)
	}
	if got := f.paymentCents(saleID, "CARD"); got != 50 {
		t.Errorf("the card payment = %d cents, want the exact 50", got)
	}
	if got := f.accountBalance("4095"); got != -2 {
		t.Errorf("the rounding income = %d cents, want a credit of two", got)
	}
	if got := f.accountBalance("4010"); got != -103 {
		t.Errorf("the revenue = %d cents, want a credit of 103", got)
	}
	f.assertARInvariants(t)
}

// RULE: the tax is computed before the cash leg moves, so the tax account
// never moves for the rounding.
func TestCP18_TaxBeforeRounding(t *testing.T) {
	testutil.LockOutboxTables(t)
	f := newOwnBranchFixture(t, testutil.RequireDB(t))
	f.setRounding("NEAREST_5_CENTS")

	// A $1.03 line with the branch's tax: the tax is on the exact $1.03, and
	// the cash leg alone rounds.
	saleID, _ := f.saleAtPrice(103)
	r := f.completeSale(saleID, tender("cash", 155))
	if r.status != http.StatusOK {
		t.Fatalf("complete = %d: %s", r.status, r.raw)
	}
	total := num(f.t, r.body, "total_cents")
	tax := num(f.t, r.body, "tax_cents")
	if tax != 9 {
		t.Fatalf("the tax = %d cents, want 9 (the tax on the exact $1.03)", tax)
	}
	if total != 112 {
		t.Fatalf("the total = %d cents, want the exact 112", total)
	}
	if got := f.accountBalance("2020"); got != -9 {
		t.Errorf("the tax payable = %d cents, want a credit of 9", got)
	}
	if got := f.accountBalance("4010"); got != -103 {
		t.Errorf("the revenue = %d cents, want a credit of 103", got)
	}
	f.assertARInvariants(t)
}

// RULE: the rounding posts as a leg on the payment entry, the entry balances,
// and the rounding account carries the difference.
func TestCP18_RoundingPostedBalanced(t *testing.T) {
	testutil.LockOutboxTables(t)
	f := newOwnBranchFixture(t, testutil.RequireDB(t))
	f.noTax()
	f.setRounding("NEAREST_5_CENTS")

	saleID, _ := f.saleAtPrice(103)
	r := f.completeSale(saleID, tender("cash", 105))
	if r.status != http.StatusOK {
		t.Fatalf("complete = %d: %s", r.status, r.raw)
	}
	payID := f.scalar(`SELECT p.id::text FROM payments p JOIN pos_tenders t ON t.payment_id = p.id
		WHERE t.transaction_id = $1 AND p.method = 'CASH' AND p.status = 'POSTED'`, saleID)
	id, ok := payID.(string)
	if !ok {
		f.t.Fatalf("the cash payment id reads %T", payID)
	}
	entries, legs := f.legsFor(id)
	if entries != 1 {
		f.t.Fatalf("the cash payment posted %d entries, want 1", entries)
	}
	cash, ok := legs["1010"]
	if !ok || cash.debit != 105 {
		f.t.Errorf("the cash leg = %+v, want a debit of 105", cash)
	}
	income, ok := legs["4095"]
	if !ok || income.credit != 2 {
		f.t.Errorf("the rounding income leg = %+v, want a credit of two cents", income)
	}
	var debit, credit int64
	for _, l := range legs {
		debit += l.debit
		credit += l.credit
	}
	if debit != credit {
		f.t.Errorf("the payment entry does not balance: %d debits, %d credits", debit, credit)
	}

	// Round-down ($1.06 -> $1.05): the payment entry debits 1010 for 105 and
	// 5045 for 1 against 2200 for 106, and balances.
	downSaleID, _ := f.saleAtPrice(106)
	r = f.completeSale(downSaleID, tender("cash", 105))
	if r.status != http.StatusOK {
		t.Fatalf("complete round-down = %d: %s", r.status, r.raw)
	}
	downPayID := f.scalar(`SELECT p.id::text FROM payments p JOIN pos_tenders t ON t.payment_id = p.id
		WHERE t.transaction_id = $1 AND p.method = 'CASH' AND p.status = 'POSTED'`, downSaleID)
	downID, ok := downPayID.(string)
	if !ok {
		f.t.Fatalf("the round-down cash payment id reads %T", downPayID)
	}
	downEntries, downLegs := f.legsFor(downID)
	if downEntries != 1 {
		f.t.Fatalf("the round-down cash payment posted %d entries, want 1", downEntries)
	}
	downCash, ok := downLegs["1010"]
	if !ok || downCash.debit != 105 {
		f.t.Errorf("the round-down cash leg = %+v, want a debit of 105", downCash)
	}
	loss, ok := downLegs["5045"]
	if !ok || loss.debit != 1 {
		f.t.Errorf("the rounding loss leg = %+v, want a debit of one cent", loss)
	}
	var downDebit, downCredit int64
	for _, l := range downLegs {
		downDebit += l.debit
		downCredit += l.credit
	}
	if downDebit != downCredit {
		f.t.Errorf("the round-down payment entry does not balance: %d debits, %d credits", downDebit, downCredit)
	}
	f.assertARInvariants(t)
}

// RULE: the till's expected cash is the rounded cash, so the drawer variance
// stays correct.
func TestCP18_TillExpectedCashUsesRounded(t *testing.T) {
	testutil.LockOutboxTables(t)
	f := newOwnBranchFixture(t, testutil.RequireDB(t))
	f.noTax()
	f.setRounding("NEAREST_5_CENTS")

	saleID, _ := f.saleAtPrice(103)
	r := f.completeSale(saleID, tender("cash", 105))
	if r.status != http.StatusOK {
		t.Fatalf("complete = %d: %s", r.status, r.raw)
	}
	if got := f.tillExpectedCash(); got != 105 {
		f.t.Errorf("the till expects %d cents of cash, want the rounded 105", got)
	}
}

// RULE: a return refunds the exact amount of the goods against the customer
// and the tax, pays the rounded cash out of the drawer, and takes the
// rounding income back, so the rounding account nets to zero.
func TestCP18_ReturnRefundsExactPaysRounded(t *testing.T) {
	testutil.LockOutboxTables(t)
	f := newOwnBranchFixture(t, testutil.RequireDB(t))
	f.setRounding("NEAREST_5_CENTS")

	// A $1.00 line with the branch's tax: the total of $1.09 rounds up to
	// $1.10, so the sale books a cent of rounding income.
	saleID, lineID := f.saleAtPrice(100)
	r := f.completeSale(saleID, tender("cash", 110))
	if r.status != http.StatusOK {
		t.Fatalf("complete = %d: %s", r.status, r.raw)
	}
	if got := f.cashPayment(saleID); got != 110 {
		f.t.Fatalf("the cash payment = %d cents, want the rounded 110", got)
	}
	if got := f.accountBalance("4095"); got != -1 {
		f.t.Fatalf("the rounding income = %d cents, want a credit of one", got)
	}

	// The full cash return: the customer is credited the exact $1.09, the
	// drawer pays the rounded $1.10, and the rounding income gives the cent
	// back.
	r = f.do("POST", "/api/v1/pos/returns", map[string]any{
		"register_id": f.register, "original_sale_id": saleID, "refund_method": "cash",
		"reason": "the customer changed their mind",
		"lines":  []map[string]any{{"line_id": lineID, "quantity": "1", "restock": true}},
	}, "X-Test-Role", "cashier", "X-Test-Sub", mustUUID(f.t))
	if r.status != http.StatusCreated && r.status != http.StatusOK {
		t.Fatalf("the return = %d: %s", r.status, r.raw)
	}
	memo := f.scalar(`SELECT cm.total_amount::text FROM credit_memos cm
		JOIN pos_returns pr ON pr.credit_memo_id = cm.id
		WHERE pr.original_transaction_id = $1 ORDER BY cm.created_at DESC LIMIT 1`, saleID)
	if s, _ := memo.(string); s != "-1.09" {
		t.Errorf("the credit memo totals %v, want the exact -1.09", memo)
	}
	paid := f.scalar(`SELECT COALESCE(SUM(ROUND(rf.amount * 100)::bigint), 0)::bigint
		FROM payment_refunds rf JOIN pos_returns pr ON pr.credit_memo_id = rf.credit_memo_id
		WHERE pr.original_transaction_id = $1 AND rf.method = 'CASH'`, saleID)
	if n, ok := paid.(int64); !ok || n != 109 {
		t.Errorf("the refund = %v cents, want the exact 109 against the customer", paid)
	}
	cash := f.scalar(`SELECT COALESCE(SUM(ROUND(l.credit * 100)::bigint), 0)::bigint
		FROM gl_journal_lines l
		JOIN gl_journal_entries e ON e.id = l.journal_entry_id
		JOIN gl_accounts a ON a.id = l.account_id AND a.code = '1010'
		JOIN payment_refunds rf ON rf.gl_entry_id = e.id
		JOIN pos_returns pr ON pr.credit_memo_id = rf.credit_memo_id
		WHERE pr.original_transaction_id = $1`, saleID)
	if n, ok := cash.(int64); !ok || n != 110 {
		t.Errorf("the cash out of the drawer = %v cents, want the rounded 110", cash)
	}
	if got := f.accountBalance("4095"); got != 0 {
		t.Errorf("the rounding income = %d cents after the return, want none", got)
	}
	if got := f.accountBalance("4010"); got != 0 {
		t.Errorf("the revenue = %d cents after the return, want none", got)
	}
	if got := f.accountBalance("2020"); got != 0 {
		t.Errorf("the tax = %d cents after the return, want none", got)
	}
	if got := f.tillExpectedCash(); got != 0 {
		t.Errorf("the till expected cash = %d cents after the full return, want 0", got)
	}
	f.assertARInvariants(t)
}

// RULE: a completion the rounded cash due refuses (the tenders carry less
// than the rounded cash) leaves nothing written: no payment, no invoice, no
// ledger line, and the drawer expects nothing (money checklist line 16).
func TestCP18_RoundedRefusalWritesNothing(t *testing.T) {
	testutil.LockOutboxTables(t)
	f := newOwnBranchFixture(t, testutil.RequireDB(t))
	f.noTax()
	f.setRounding("NEAREST_5_CENTS")

	saleID, _ := f.saleAtPrice(103)
	// The rounded cash due is 105; the tenders carry 104.
	r := f.completeSale(saleID, tender("cash", 104))
	if r.status != http.StatusConflict {
		t.Fatalf("the under-rounded completion = %d, want 409: %s", r.status, r.raw)
	}
	if got := str(f.t, f.getSale(f.t, saleID), "status"); got != "open" {
		t.Errorf("the sale's status = %q, want open", got)
	}
	if got := f.cashPayment(saleID); got != 0 {
		t.Errorf("a payment = %d cents, want none", got)
	}
	if inv := f.scalar(`SELECT COUNT(*) FROM pos_transactions t JOIN invoices i ON i.id = t.invoice_id WHERE t.id = $1`, saleID); inv != int64(0) {
		t.Errorf("an invoice exists after the refusal, want none")
	}
	if got := f.accountBalance("4010"); got != 0 {
		t.Errorf("the revenue = %d cents, want none", got)
	}
	if got := f.accountBalance("4095"); got != 0 {
		t.Errorf("the rounding income = %d cents, want none", got)
	}
	if got := f.tillExpectedCash(); got != 0 {
		t.Errorf("the till expects %d cents of cash, want none", got)
	}
	f.assertARInvariants(t)
}

// RULE: the void of a rounded cash sale pays back the rounded cash the drawer
// took and posts the rounding legs in reverse, so the cash, the deposits, the
// rounding accounts and the till's expectation all return to zero. Round up
// (a 103 sale paid 105) and round down (a 106 sale paid 105) both.
func TestCP18_VoidOfRoundedSaleReversesTheRounding(t *testing.T) {
	testutil.LockOutboxTables(t)
	f := newOwnBranchFixture(t, testutil.RequireDB(t))
	f.noTax()
	f.setRounding("NEAREST_5_CENTS")

	voidTheSale := func(saleID string, body resp) {
		t.Helper()
		r := f.do("POST", "/api/v1/pos/transactions/"+saleID+"/void",
			map[string]any{"reason": "rung in error", "revision": rev(t, body)},
			"X-Test-Role", "cashier", "X-Test-Sub", mustUUID(t))
		if r.status != http.StatusOK {
			t.Fatalf("the void = %d: %s", r.status, r.raw)
		}
	}
	assertAllZero := func() {
		t.Helper()
		if got := f.accountBalance("1010"); got != 0 {
			t.Errorf("the cash = %d cents, want none", got)
		}
		if got := f.accountBalance("2200"); got != 0 {
			t.Errorf("the customer deposits = %d cents, want none", got)
		}
		if got := f.accountBalance("4095"); got != 0 {
			t.Errorf("the rounding income = %d cents, want none", got)
		}
		if got := f.accountBalance("5045"); got != 0 {
			t.Errorf("the rounding loss = %d cents, want none", got)
		}
		if got := f.tillExpectedCash(); got != 0 {
			t.Errorf("the till expects %d cents of cash, want none", got)
		}
	}

	// Round up: the drawer took 105 for a 103 sale; the void pays 105 back
	// and takes the two cents of rounding income with it.
	upSale, _ := f.saleAtPrice(103)
	up := f.completeSale(upSale, tender("cash", 105))
	if up.status != http.StatusOK {
		t.Fatalf("complete = %d: %s", up.status, up.raw)
	}
	if got := f.accountBalance("4095"); got != -2 {
		t.Fatalf("the rounding income = %d cents, want a credit of two", got)
	}
	voidTheSale(upSale, up)
	assertAllZero()
	if got := str(t, f.getSale(t, upSale), "status"); got != "voided" {
		t.Errorf("the sale's status = %q, want voided", got)
	}

	// Round down: the drawer took 105 for a 106 sale; the void pays 105 back
	// and gives the cent of rounding loss back.
	downSale, _ := f.saleAtPrice(106)
	down := f.completeSale(downSale, tender("cash", 105))
	if down.status != http.StatusOK {
		t.Fatalf("the round-down complete = %d: %s", down.status, down.raw)
	}
	if got := f.accountBalance("5045"); got != 1 {
		t.Fatalf("the rounding loss = %d cents, want a debit of one", got)
	}
	voidTheSale(downSale, down)
	assertAllZero()
	f.assertARInvariants(t)
}

// RULE: an exchange's receipt counts the returned credit once. The credit is
// a store credit tender row of the new sale, so the exact cash the sale
// needed is the total less that row; an exchange paid in exact cash names no
// rounding, and an exchange whose cash difference the branch rounds names
// the rounding on the cash part alone.
func TestCP18_ExchangeReceiptCountsTheCreditOnce(t *testing.T) {
	testutil.LockOutboxTables(t)
	f := newOwnBranchFixture(t, testutil.RequireDB(t), withRealThermalReceipts())

	// The rule off: one 5.99 line exchanged for two, the 5.99 difference paid
	// in exact cash. Nothing was rounded: the receipt carries no rounding
	// line.
	sale, line, _ := f.cashSaleLine(t, "2")
	r := f.exchange("", map[string]any{
		"original_sale_id": sale, "reason": "wrong size",
		"returned_lines": []map[string]any{lineOf(line, "1", "wrong_item")},
		"sale_lines":     f.replaceWith("2"),
		"tenders":        []map[string]any{tender("cash", 599)},
	})
	if r.status != http.StatusCreated {
		t.Fatalf("the exchange = %d: %s", r.status, r.raw)
	}
	newSale := str(t, r.body["sale"].(map[string]any), "id")
	if got := f.accountBalance("4095"); got != 0 {
		t.Fatalf("the rounding income = %d cents, want none", got)
	}
	if got := f.accountBalance("5045"); got != 0 {
		t.Fatalf("the rounding loss = %d cents, want none", got)
	}
	receipt := string(f.printReceipt(newSale, "layout=thermal80").raw)
	if strings.Contains(receipt, "Cash rounding") {
		t.Errorf("the exact exchange's receipt names a cash rounding: %s", receipt)
	}

	// The rule on: a 500 sale exchanged for the same line re-priced at 503,
	// the 3 cent difference rounded up to 5 in cash. The receipt names the
	// two cents the drawer kept, nothing more.
	f.noTax()
	f.setRounding("NEAREST_5_CENTS")
	plain, plainLine := f.saleAtPrice(500)
	if r := f.completeSale(plain, tender("cash", 500)); r.status != http.StatusOK {
		t.Fatalf("the 500 sale = %d: %s", r.status, r.raw)
	}
	mustExec(t, f.db, `UPDATE products SET base_price = 5.03 WHERE id = $1`, f.productID)
	r = f.exchange("", map[string]any{
		"original_sale_id": plain, "reason": "wrong size",
		"returned_lines": []map[string]any{lineOf(plainLine, "1", "wrong_item")},
		"sale_lines":     f.replaceWith("1"),
		"tenders":        []map[string]any{tender("cash", 5)},
	})
	if r.status != http.StatusCreated {
		t.Fatalf("the rounded exchange = %d: %s", r.status, r.raw)
	}
	roundedSale := str(t, r.body["sale"].(map[string]any), "id")
	if got := f.accountBalance("4095"); got != -2 {
		t.Fatalf("the rounding income = %d cents, want a credit of two", got)
	}
	receipt = string(f.printReceipt(roundedSale, "layout=thermal80").raw)
	if !strings.Contains(receipt, "Cash rounding") || !strings.Contains(receipt, "$0.02") {
		t.Errorf("the rounded exchange's receipt names no two cent rounding: %s", receipt)
	}
}

// RULE: the receipt's rounding line names what the branch actually rounded,
// taken from the drawer's cash, not from the tender rows: a cash tender the
// change swallowed whole never reached the drawer and must not read as
// rounding. A sale the rule did not touch carries no rounding line; a rounded
// sale carries the difference the drawer kept.
func TestCP18_ReceiptRoundingNamesOnlyWhatTheBranchRounded(t *testing.T) {
	testutil.LockOutboxTables(t)
	f := newOwnBranchFixture(t, testutil.RequireDB(t), withRealThermalReceipts())
	f.noTax()

	// The rule off: a 20 cent sale paid with two cash tenders, 50 and 60.
	// The change (90 cents) swallows the first tender whole; the drawer kept
	// 20 cents and nothing was rounded, so the receipt carries no rounding
	// line.
	plainID, _ := f.saleAtPrice(20)
	if r := f.completeSale(plainID, tender("cash", 50), tender("cash", 60)); r.status != http.StatusOK {
		t.Fatalf("complete = %d: %s", r.status, r.raw)
	}
	plain := string(f.printReceipt(plainID, "layout=thermal80").raw)
	if strings.Contains(plain, "Cash rounding") {
		t.Errorf("the unrounded sale's receipt names a cash rounding: %s", plain)
	}

	// The rule on: a 103 sale paid 105 in cash rounds up by two cents, and
	// the receipt says so.
	f.setRounding("NEAREST_5_CENTS")
	upID, _ := f.saleAtPrice(103)
	if r := f.completeSale(upID, tender("cash", 105)); r.status != http.StatusOK {
		t.Fatalf("the rounded complete = %d: %s", r.status, r.raw)
	}
	up := string(f.printReceipt(upID, "layout=thermal80").raw)
	if !strings.Contains(up, "Cash rounding") || !strings.Contains(up, "$0.02") {
		t.Errorf("the rounded sale's receipt names no two cent rounding: %s", up)
	}

	// A 106 sale paid 105 rounds down by one cent, and the receipt says so.
	downID, _ := f.saleAtPrice(106)
	if r := f.completeSale(downID, tender("cash", 105)); r.status != http.StatusOK {
		t.Fatalf("the round-down complete = %d: %s", r.status, r.raw)
	}
	down := string(f.printReceipt(downID, "layout=thermal80").raw)
	if !strings.Contains(down, "Cash rounding") || !strings.Contains(down, "0.01") {
		t.Errorf("the round-down sale's receipt names no cent rounding: %s", down)
	}
}

// RULE: three concurrent rounded cash sales at pool size 4 serialize cleanly,
// book every rounding leg, keep the till's expected cash exact, and preserve
// the AR subledger invariants.
func TestCP18_ConcurrentRoundedCashSales(t *testing.T) {
	testutil.LockOutboxTables(t)
	f := newOwnBranchFixture(t, testutil.RequireDBMaxConns(t, 4))
	f.noTax()
	f.setRounding("NEAREST_5_CENTS")

	prices := []int64{103, 107, 104}
	sales := make([]string, len(prices))
	for i, p := range prices {
		sales[i], _ = f.saleAtPrice(p)
	}
	statuses := make([]int, len(prices))
	var wg sync.WaitGroup
	for i := range sales {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			statuses[i] = f.completeSale(sales[i], tender("cash", 105)).status
		}(i)
	}
	wg.Wait()
	for i, st := range statuses {
		if st != http.StatusOK {
			t.Fatalf("contender %d status = %d, want 200", i, st)
		}
	}
	if got := f.tillExpectedCash(); got != 315 {
		t.Errorf("the till expects %d cents of cash across the three sales, want 315", got)
	}
	if got := f.accountBalance("4095"); got != -3 {
		t.Errorf("the rounding income = %d cents, want a credit of 3", got)
	}
	if got := f.accountBalance("5045"); got != 2 {
		t.Errorf("the rounding loss = %d cents, want a debit of 2", got)
	}
	if got := f.accountBalance("4010"); got != -314 {
		t.Errorf("the revenue = %d cents, want a credit of 314", got)
	}
	f.assertARInvariants(t)
}
