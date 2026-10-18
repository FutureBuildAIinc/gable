// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package pos_test

// The exchange in one act (CP-06): the returned lines and the replacement
// sale lines in one transaction in section 11's lock order, one credit memo
// and one sale linked to each other, tender only for the difference and money
// back when the return is worth more, every figure balanced to the cent.

import (
	"net/http"
	"sync"
	"testing"

	"github.com/gablelbm/gable/internal/testutil"
	"github.com/google/uuid"
)

// exchange posts an exchange of the returned lines for the replacement lines.
func (f *fixture) exchange(path string, body map[string]any) resp {
	f.t.Helper()
	f.ensureOpenTill(f.t)
	body["register_id"] = f.register
	return f.do("POST", "/api/v1/pos/exchanges"+path, body, "X-Test-Role", "cashier", "X-Test-Sub", mustUUID(f.t))
}

func (f *fixture) replaceWith(qty string) []map[string]any {
	return []map[string]any{{"product_id": f.productID.String(), "quantity": qty}}
}

// entriesBalance asserts every ledger entry the documents sourced balances to
// the cent (debits equal credits per entry).
func (f *fixture) entriesBalance(t *testing.T, ids ...string) {
	t.Helper()
	for _, id := range ids {
		_, legs := f.legsFor(id)
		var debit, credit int64
		for _, l := range legs {
			debit += l.debit
			credit += l.credit
		}
		if debit != credit {
			t.Errorf("entries of %s: debits %d and credits %d do not balance", id, debit, credit)
		}
	}
}

func (f *fixture) memoState(memoID string) string {
	f.t.Helper()
	return f.scalar(`SELECT status || ':' || ROUND(amount_open * 100)::bigint FROM credit_memos WHERE id = $1`, uuid.MustParse(memoID)).(string)
}

// RULE: a return worth less than the replacement takes tender for the
// difference only: the memo's credit is a leg of the new sale, one memo and
// one sale point at each other, the stock goes back and out, and the books
// balance.
func TestAnExchangeWorthLessThanTheReplacementTendersTheDifference(t *testing.T) {
	testutil.LockOutboxTables(t)
	f := newFixture(t, testutil.RequireDB(t))
	f.openTill(0)
	sale, line, _ := f.cashSaleLine(t, "2") // 1198, stock 98
	r := f.exchange("", map[string]any{
		"original_sale_id": sale, "reason": "wrong size",
		"returned_lines": []map[string]any{lineOf(line, "1", "wrong_item")},
		"sale_lines":     f.replaceWith("2"),
		"tenders":        []map[string]any{tender("cash", 599)},
	})
	if r.status != http.StatusCreated {
		t.Fatalf("exchange = %d: %s", r.status, r.raw)
	}
	if num(t, r.body, "returned_cents") != 599 || num(t, r.body, "replacement_cents") != 1198 || num(t, r.body, "difference_cents") != 599 {
		t.Errorf("figures = returned %v replacement %v difference %v, want 599, 1198 and 599",
			r.body["returned_cents"], r.body["replacement_cents"], r.body["difference_cents"])
	}
	ret := r.body["return"].(map[string]any)
	newSale := r.body["sale"].(map[string]any)
	if ret["exchange_sale_id"] != newSale["id"] || newSale["exchange_return_id"] != ret["id"] {
		t.Errorf("the return and the sale do not point at each other: %v / %v", ret["exchange_sale_id"], newSale["exchange_return_id"])
	}
	if newSale["status"] != "completed" || num(t, newSale, "total_cents") != 1198 {
		t.Errorf("sale = %v, want completed at 1198", newSale)
	}
	// the sale's legs: the returned credit and the difference in cash
	legs := map[string]int64{}
	for _, x := range newSale["tenders"].([]any) {
		m := x.(map[string]any)
		legs[m["method"].(string)] += num(t, m, "amount_cents")
	}
	if legs["store_credit"] != 599 || legs["cash"] != 599 || len(legs) != 2 {
		t.Errorf("tenders = %v, want 599 returned credit and 599 cash", legs)
	}
	memoID := str(t, ret, "credit_memo_id")
	if got := f.memoState(memoID); got != "APPLIED:0" {
		t.Errorf("memo = %s, want APPLIED:0 (the credit was all used)", got)
	}
	if inv := f.scalar(`SELECT status || ':' || ROUND(amount_open * 100)::bigint FROM invoices WHERE id = $1`, uuid.MustParse(str(t, newSale, "invoice_id"))); inv != "PAID:0" {
		t.Errorf("invoice = %v, want PAID:0", inv)
	}
	// the stock: 100, the sale took 2, the return gave 1 back, the new sale took 2
	if got := f.stock(); got != "97.0000/0.0000" {
		t.Errorf("stock = %s, want 97", got)
	}
	// two returns of the same sale line cannot exceed it
	f.entriesBalance(t, memoID, str(t, newSale, "invoice_id"))
	f.assertARInvariants(t)
	if n := f.scalar(`SELECT count(*) FROM pos_returns WHERE exchange_sale_id IS NOT NULL`); n.(int64) != 1 {
		t.Errorf("exchange returns = %v, want 1", n)
	}
}

// RULE: a tender short of the difference is refused with nothing written: no
// sale, no memo, no stock moved.
func TestAnExchangeShortOfTheDifferenceWritesNothing(t *testing.T) {
	testutil.LockOutboxTables(t)
	f := newFixture(t, testutil.RequireDB(t))
	f.openTill(0)
	sale, line, _ := f.cashSaleLine(t, "2")
	sales := f.scalar(`SELECT count(*) FROM pos_transactions`)
	r := f.exchange("", map[string]any{
		"original_sale_id": sale, "returned_lines": []map[string]any{lineOf(line, "1", "wrong_item")},
		"sale_lines": f.replaceWith("2"), "tenders": []map[string]any{tender("cash", 598)},
	})
	blockerOf(t, r, "insufficient_tender")
	if got := f.scalar(`SELECT count(*) FROM pos_transactions`); got != sales {
		t.Errorf("sales = %v after a refused exchange, want %v", got, sales)
	}
	if n := f.counterReturns(t); n != 0 {
		t.Errorf("returns = %d after a refused exchange, want 0", n)
	}
	if n := f.counterMemos(t); n != 0 {
		t.Errorf("memos = %d after a refused exchange, want 0", n)
	}
	if got := f.stock(); got != "98.0000/0.0000" {
		t.Errorf("stock = %s, want 98 (nothing moved)", got)
	}
}

// RULE: a return worth more pays the surplus back in cash out of the drawer,
// the memo fully used, and the books balance to the cent.
func TestAnExchangeWorthMoreThanTheReplacementPaysTheSurplusBack(t *testing.T) {
	testutil.LockOutboxTables(t)
	f := newFixture(t, testutil.RequireDB(t))
	f.openTill(0)
	sale, line, _ := f.cashSaleLine(t, "2") // 1198 in the drawer
	r := f.exchange("", map[string]any{
		"original_sale_id": sale, "reason": "too much",
		"returned_lines": []map[string]any{lineOf(line, "2", "overbought")},
		"sale_lines":     f.replaceWith("1"),
	})
	if r.status != http.StatusCreated {
		t.Fatalf("exchange = %d: %s", r.status, r.raw)
	}
	if num(t, r.body, "returned_cents") != 1198 || num(t, r.body, "replacement_cents") != 599 ||
		num(t, r.body, "difference_cents") != -599 || num(t, r.body, "refunded_cents") != 599 {
		t.Errorf("figures = %v, want 1198 returned, 599 replacement, -599 difference, 599 refunded", r.body)
	}
	ret := r.body["return"].(map[string]any)
	if ret["refund_method"] != "cash" {
		t.Errorf("refund_method = %v, want cash", ret["refund_method"])
	}
	memoID := str(t, ret, "credit_memo_id")
	if got := f.memoState(memoID); got != "APPLIED:0" {
		t.Errorf("memo = %s, want APPLIED:0", got)
	}
	if got := f.scalar(`SELECT ROUND(COALESCE(SUM(amount), 0) * 100)::bigint FROM payment_refunds WHERE credit_memo_id = $1 AND method = 'CASH'`, uuid.MustParse(memoID)); got.(int64) != 599 {
		t.Errorf("cash refunded = %v, want 599", got)
	}
	// the drawer: 1198 taken, 599 paid back
	if got := num(t, f.tillReport(t)["expected_by_method"].(map[string]any), "CASH"); got != 599 {
		t.Errorf("expected cash = %d, want 599 (1198 taken, 599 paid back)", got)
	}
	f.entriesBalance(t, memoID, str(t, r.body["sale"].(map[string]any), "invoice_id"))
	f.assertARInvariants(t)
}

// RULE: the surplus may stay as store credit on a named account: the memo is
// left open for what was not used.
func TestAnExchangeSurplusMayStayAsStoreCredit(t *testing.T) {
	testutil.LockOutboxTables(t)
	f := newFixture(t, testutil.RequireDB(t))
	f.openTill(0)
	sale, total := f.cartOf("2", &f.customerID)
	if r := f.completeSale(sale, tender("cash", total)); r.status != http.StatusOK {
		t.Fatalf("sale = %d: %s", r.status, r.raw)
	}
	line := f.getSale(t, sale)["lines"].([]any)[0].(map[string]any)["id"].(string)
	r := f.exchange("", map[string]any{
		"original_sale_id": sale, "customer_id": f.customerID.String(), "refund_method": "account",
		"returned_lines": []map[string]any{lineOf(line, "2", "overbought")},
		"sale_lines":     f.replaceWith("1"),
	})
	if r.status != http.StatusCreated {
		t.Fatalf("exchange = %d: %s", r.status, r.raw)
	}
	memoID := str(t, r.body["return"].(map[string]any), "credit_memo_id")
	if got := f.memoState(memoID); got != "PARTIAL:-599" {
		t.Errorf("memo = %s, want PARTIAL:-599 (599 of credit left on the account)", got)
	}
	if num(t, r.body, "refunded_cents") != 0 {
		t.Errorf("refunded_cents = %v, want 0", r.body["refunded_cents"])
	}
	f.assertARInvariants(t)
}

// RULE: equal value takes no tender at all (409 no_tender_due if one is
// offered) and refunds nothing; a walk-in exchange pays any surplus in cash.
func TestAnEvenExchangeTakesNoTender(t *testing.T) {
	testutil.LockOutboxTables(t)
	f := newFixture(t, testutil.RequireDB(t))
	f.openTill(0)
	sale, line, _ := f.cashSaleLine(t, "2")
	body := func(tenders ...map[string]any) map[string]any {
		b := map[string]any{"original_sale_id": sale, "returned_lines": []map[string]any{lineOf(line, "1", "wrong_item")},
			"sale_lines": f.replaceWith("1")}
		if len(tenders) > 0 {
			b["tenders"] = tenders
		}
		return b
	}
	blockerOf(t, f.exchange("", body(tender("cash", 100))), "no_tender_due")
	r := f.exchange("", body())
	if r.status != http.StatusCreated {
		t.Fatalf("even exchange = %d: %s", r.status, r.raw)
	}
	if num(t, r.body, "difference_cents") != 0 || num(t, r.body, "refunded_cents") != 0 {
		t.Errorf("figures = %v, want a zero difference and no refund", r.body)
	}
	f.assertARInvariants(t)
}

// RULE: a restocking fee lowers the credit the exchange gives: the fee is a
// line of the memo, booked to 4030, and the customer pays the difference the
// fee makes.
func TestAnExchangeCarriesTheRestockingFee(t *testing.T) {
	testutil.LockOutboxTables(t)
	f := newFixture(t, testutil.RequireDB(t))
	f.setFee("changed_mind", "1500")
	f.openTill(0)
	sale, line, _ := f.cashSaleLine(t, "2")
	r := f.exchange("", map[string]any{
		"original_sale_id": sale, "returned_lines": []map[string]any{lineOf(line, "2", "changed_mind")},
		"sale_lines": f.replaceWith("2"), "tenders": []map[string]any{tender("cash", 165)},
	})
	if r.status != http.StatusCreated {
		t.Fatalf("exchange = %d: %s", r.status, r.raw)
	}
	if num(t, r.body, "restocking_fee_cents") != 165 || num(t, r.body, "returned_cents") != 1198-165 || num(t, r.body, "difference_cents") != 165 {
		t.Errorf("figures = %v, want a 165 fee, 1033 returned and a 165 difference", r.body)
	}
	memoID := str(t, r.body["return"].(map[string]any), "credit_memo_id")
	if _, legs := f.legsFor(memoID); legs["4030"].credit != 165 {
		t.Errorf("4030 credit = %d, want the 165 fee", legs["4030"].credit)
	}
	f.entriesBalance(t, memoID)
	f.assertARInvariants(t)
}

// RULE: the preview states the figures and writes nothing.
func TestTheExchangePreviewStatesTheFiguresAndWritesNothing(t *testing.T) {
	testutil.LockOutboxTables(t)
	f := newFixture(t, testutil.RequireDB(t))
	f.openTill(0)
	sale, line, _ := f.cashSaleLine(t, "2")
	sales, memos, returns, invoices := f.scalar(`SELECT count(*) FROM pos_transactions`), f.counterMemos(t), f.counterReturns(t), f.counterInvoices(t)
	stock := f.stock()
	r := f.exchange("/preview", map[string]any{
		"original_sale_id": sale, "returned_lines": []map[string]any{lineOf(line, "1", "wrong_item")},
		"sale_lines": f.replaceWith("2"),
	})
	if r.status != http.StatusOK {
		t.Fatalf("preview = %d: %s", r.status, r.raw)
	}
	if num(t, r.body, "returned_cents") != 599 || num(t, r.body, "replacement_cents") != 1198 || num(t, r.body, "difference_cents") != 599 {
		t.Errorf("preview = %v, want 599, 1198 and 599", r.body)
	}
	if got := f.scalar(`SELECT count(*) FROM pos_transactions`); got != sales || f.counterMemos(t) != memos ||
		f.counterReturns(t) != returns || f.counterInvoices(t) != invoices || f.stock() != stock {
		t.Errorf("the preview wrote something: sales %v, memos %d, returns %d, invoices %d, stock %s", got, f.counterMemos(t), f.counterReturns(t), f.counterInvoices(t), f.stock())
	}
	f.assertARInvariants(t)
}

// RULE: a walk-in sale's exchange runs on the walk-in customer: no store
// credit is held for it, the surplus goes back in cash.
func TestAWalkInExchangePaysTheSurplusInCash(t *testing.T) {
	testutil.LockOutboxTables(t)
	f := newFixture(t, testutil.RequireDB(t))
	f.openTill(0)
	sale, line, _ := f.cashSaleLine(t, "2")
	body := map[string]any{"original_sale_id": sale, "returned_lines": []map[string]any{lineOf(line, "2", "overbought")},
		"sale_lines": f.replaceWith("1"), "refund_method": "account"}
	blockerOf(t, f.exchange("", body), "walk_in_account")
	delete(body, "refund_method")
	r := f.exchange("", body)
	if r.status != http.StatusCreated || num(t, r.body, "refunded_cents") != 599 {
		t.Fatalf("walk-in exchange = %d: %s", r.status, r.raw)
	}
	f.assertARInvariants(t)
}

// RULE: what an exchange refuses it refuses whole: a card refund of the
// surplus, a store credit tender, a card token, a return past what the line
// sold, and a replacement the yard cannot supply (the return rolls back).
func TestAnExchangeRefusesWhatItCannotDoAndWritesNothing(t *testing.T) {
	testutil.LockOutboxTables(t)
	f := newFixture(t, testutil.RequireDB(t))
	f.openTill(0)
	sale, line, _ := f.cashSaleLine(t, "2")
	base := func() map[string]any {
		return map[string]any{"original_sale_id": sale, "returned_lines": []map[string]any{lineOf(line, "1", "wrong_item")},
			"sale_lines": f.replaceWith("1")}
	}
	stock := f.stock()
	b := base()
	b["refund_method"] = "card"
	blockerOf(t, f.exchange("", b), "exchange_card_refund")
	b = base()
	b["tenders"] = []map[string]any{tender("store_credit", 100)}
	if r := f.exchange("", b); r.status != http.StatusBadRequest {
		t.Errorf("a store credit tender in an exchange = %d, want 400: %s", r.status, r.raw)
	}
	b = base()
	b["tenders"] = []map[string]any{{"method": "card", "amount_cents": 100, "token_id": "tok"}}
	blockerOf(t, f.exchange("", b), "exchange_card_token")
	b = base()
	b["returned_lines"] = []map[string]any{lineOf(line, "3", "wrong_item")}
	blockerOf(t, f.exchange("", b), "exceeds_sold")
	b = base()
	delete(b, "sale_lines")
	if r := f.exchange("", b); r.status != http.StatusBadRequest {
		t.Errorf("no replacement lines = %d, want 400: %s", r.status, r.raw)
	}
	b = base()
	b["sale_lines"] = f.replaceWith("150") // the yard holds 98
	b["tenders"] = []map[string]any{tender("cash", 89223)}
	blockerOf(t, f.exchange("", b), "insufficient_stock")
	if f.counterReturns(t) != 0 || f.counterMemos(t) != 0 || f.stock() != stock {
		t.Errorf("a refused exchange left a return (%d), a memo (%d) or moved stock (%s from %s)",
			f.counterReturns(t), f.counterMemos(t), f.stock(), stock)
	}
	f.assertARInvariants(t)
}

// An exchange racing two returns of the same line (pool size 4, three
// contenders): the line comes back once, and a losing exchange leaves no sale
// and no memo behind.
func TestAnExchangeRacingReturnsOfTheSameLineReturnsItOnce(t *testing.T) {
	testutil.LockOutboxTables(t)
	f := newFixture(t, testutil.RequireDBMaxConns(t, 4))
	f.openTill(0)
	sale, line, _ := f.cashSaleLine(t, "1")
	f.ensureOpenTill(t)
	statuses := make([]int, 3)
	var wg sync.WaitGroup
	for i := range statuses {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			if i == 2 {
				statuses[i] = f.returnLines(t, sale, "", lineOf(line, "1", "defective")).status
				return
			}
			statuses[i] = f.exchange("", map[string]any{
				"original_sale_id": sale, "returned_lines": []map[string]any{lineOf(line, "1", "wrong_item")},
				"sale_lines": f.replaceWith("1"),
			}).status
		}(i)
	}
	wg.Wait()
	wins := 0
	for _, st := range statuses {
		switch st {
		case http.StatusCreated:
			wins++
		case http.StatusConflict:
		default:
			t.Errorf("statuses = %v, want only 201 and 409", statuses)
		}
	}
	if wins != 1 {
		t.Errorf("statuses = %v, want exactly one return of the line", statuses)
	}
	if n := f.counterReturns(t); n != 1 {
		t.Errorf("returns = %d, want 1", n)
	}
	// the original sale, plus one replacement when the exchange won
	exchanges := f.scalar(`SELECT count(*) FROM pos_returns WHERE exchange_sale_id IS NOT NULL`).(int64)
	if got := f.scalar(`SELECT count(*) FROM pos_transactions WHERE status = 'COMPLETED'`).(int64); got != 1+exchanges {
		t.Errorf("completed sales = %d, want %d (the original and one replacement per exchange won)", got, 1+exchanges)
	}
	f.assertARInvariants(t)
}

// RULE (section 11): no read or write goes through the pool while a
// transaction is open. At pool size 1 a transaction that reached for a second
// connection would wait on itself forever, so each act that opens one here
// (the exchange, the override, the signature, the print count, a store credit
// sale) completes at pool size 1 or the request deadline fails it.
func TestTheCountersNewActsNeverTouchThePoolInsideTheirTransaction(t *testing.T) {
	testutil.LockOutboxTables(t)
	f := newFixture(t, testutil.RequireDBMaxConns(t, 1), withReceipts(thermalStandIn(nil)))
	f.accountReturnOf() // 599 of store credit
	f.openTill(0)
	sale, total := f.cartOf("2", &f.customerID)
	if r := f.completeSale(sale, tender("store_credit", 599), tender("cash", total-599)); r.status != http.StatusOK {
		t.Fatalf("store credit sale at pool size 1 = %d: %s", r.status, r.raw)
	}
	if r := f.sign(sale, "Pat Signer", tinyPNG); r.status != http.StatusCreated {
		t.Fatalf("signature at pool size 1 = %d: %s", r.status, r.raw)
	}
	if r := f.printReceipt(sale, "layout=thermal80"); r.status != http.StatusOK {
		t.Fatalf("print at pool size 1 = %d: %s", r.status, r.raw)
	}
	open, _ := f.cartOf("1", &f.customerID)
	if r := f.override(open, "account", "agreed", 500, "admin", uuid.NewString()); r.status != http.StatusCreated {
		t.Fatalf("override at pool size 1 = %d: %s", r.status, r.raw)
	}
	line := f.getSale(t, sale)["lines"].([]any)[0].(map[string]any)["id"].(string)
	r := f.exchange("", map[string]any{
		"original_sale_id": sale, "customer_id": f.customerID.String(),
		"returned_lines": []map[string]any{lineOf(line, "1", "wrong_item")},
		"sale_lines":     f.replaceWith("1"),
	})
	if r.status != http.StatusCreated {
		t.Fatalf("exchange at pool size 1 = %d: %s", r.status, r.raw)
	}
}
