// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package pos_test

// The exchange in one act: the returned lines and the replacement
// sale lines in one transaction in section 11's lock order, one credit memo
// and one sale linked to each other, tender only for the difference and money
// back when the return is worth more, every figure balanced to the cent.

import (
	"context"
	"net/http"
	"sync"
	"testing"
	"time"

	"github.com/gablelbm/gable/internal/tax"
	"github.com/gablelbm/gable/internal/testutil"
	"github.com/gablelbm/gable/pkg/database"
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

// RULE (ADR 0005 section 11): return and exchange acquire the customer credit
// advisory lock (step 1a) before stock locks (step 6), and the customer row
// lock (step 7) before the gapless document counter lock (step 8). Racing an
// exchange on sale A against a concurrent return and a concurrent
// store-credit/on-account sale on sale B for the same customer and overlapping
// products at pool size 4 produces zero deadlocks and conserves balance_due,
// qty_on_hand, and gapless CM-*/IN-* numbers.
func TestExchangeAndReturnRespectSection11LockOrderAndCrossTableConcurrency(t *testing.T) {
	testutil.LockOutboxTables(t)
	db := testutil.RequireDBMaxConns(t, 4)
	f := newFixture(t, db)
	f.openTill(0)
	ctx := t.Context()

	sale1, total1 := f.cartOf("2", &f.customerID)
	if r := f.completeSale(sale1, tender("cash", total1)); r.status != http.StatusOK {
		t.Fatalf("sale1 = %d: %s", r.status, r.raw)
	}
	line1 := f.getSale(t, sale1)["lines"].([]any)[0].(map[string]any)["id"].(string)

	waitForWaiter := func(waitQuery string, args ...any) {
		t.Helper()
		for range 400 {
			var waiting int64
			if err := db.Pool.QueryRow(ctx, waitQuery, args...).Scan(&waiting); err == nil && waiting > 0 {
				return
			}
			time.Sleep(5 * time.Millisecond)
		}
		t.Fatalf("timed out waiting for contender to block on expected lock")
	}

	// 1. Return locks customers (step 7) BEFORE document_counters (step 8).
	txCust, err := db.Pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin txCust: %v", err)
	}
	defer func() { _ = txCust.Rollback(ctx) }()
	var lockedID uuid.UUID
	if err := txCust.QueryRow(ctx, `SELECT id FROM customers WHERE id = $1 FOR NO KEY UPDATE`, f.customerID).Scan(&lockedID); err != nil {
		t.Fatalf("lock customer: %v", err)
	}

	retDone := make(chan resp, 1)
	go func() {
		retDone <- f.returnLines(t, sale1, "", lineOf(line1, "1", "defective"))
	}()

	waitForWaiter(`SELECT count(*) FROM pg_locks WHERE NOT granted AND locktype IN ('tuple', 'transactionid')`)

	// While the return is blocked waiting on the customer row (step 7),
	// document_counters for credit_memo (step 8) must NOT be locked yet.
	txProbe, err := db.Pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin txProbe: %v", err)
	}
	var lastCM int64
	err = txProbe.QueryRow(ctx, `SELECT next_value FROM document_counters WHERE series = 'credit_memo' FOR UPDATE NOWAIT`).Scan(&lastCM)
	_ = txProbe.Rollback(ctx)
	if err != nil {
		_ = txCust.Rollback(ctx)
		<-retDone
		t.Fatalf("return locked document_counters (step 8) before customers (step 7): %v", err)
	}
	if err := txCust.Commit(ctx); err != nil {
		t.Fatalf("commit txCust: %v", err)
	}
	if r := <-retDone; r.status != http.StatusCreated {
		t.Fatalf("return after customer unlock = %d: %s", r.status, r.raw)
	}

	// 2. Exchange unconditionally acquires customer credit advisory lock (step 1a)
	// before locking inventory (step 6), even when no account tender is sent.
	txAdv, err := db.Pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin txAdv: %v", err)
	}
	defer func() { _ = txAdv.Rollback(ctx) }()
	if _, err := txAdv.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended('order-credit:' || $1::text, 0))`, f.customerID.String()); err != nil {
		t.Fatalf("lock advisory: %v", err)
	}

	exDone := make(chan resp, 1)
	go func() {
		exDone <- f.exchange("", map[string]any{
			"original_sale_id": sale1, "customer_id": f.customerID.String(),
			"returned_lines": []map[string]any{lineOf(line1, "1", "wrong_item")},
			"sale_lines":     f.replaceWith("1"),
		})
	}()

	waitForWaiter(`SELECT count(*) FROM pg_locks WHERE NOT granted AND locktype = 'advisory'`)

	txInvProbe, err := db.Pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin txInvProbe: %v", err)
	}
	var invRowID uuid.UUID
	err = txInvProbe.QueryRow(ctx, `SELECT id FROM inventory WHERE product_id = $1 FOR UPDATE NOWAIT`, f.productID).Scan(&invRowID)
	_ = txInvProbe.Rollback(ctx)
	if err != nil {
		_ = txAdv.Rollback(ctx)
		<-exDone
		t.Fatalf("exchange locked inventory (step 6) before customer credit advisory lock (step 1a): %v", err)
	}
	if err := txAdv.Commit(ctx); err != nil {
		t.Fatalf("commit txAdv: %v", err)
	}
	if r := <-exDone; r.status != http.StatusCreated {
		t.Fatalf("exchange after advisory unlock = %d: %s", r.status, r.raw)
	}

	// 2b. Exchange also locks customers (step 7) BEFORE document_counters (step 8).
	sale2, total2 := f.cartOf("1", &f.customerID)
	if r := f.completeSale(sale2, tender("cash", total2)); r.status != http.StatusOK {
		t.Fatalf("sale2 = %d: %s", r.status, r.raw)
	}
	line2 := f.getSale(t, sale2)["lines"].([]any)[0].(map[string]any)["id"].(string)

	txCustEx, err := db.Pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin txCustEx: %v", err)
	}
	defer func() { _ = txCustEx.Rollback(ctx) }()
	if err := txCustEx.QueryRow(ctx, `SELECT id FROM customers WHERE id = $1 FOR NO KEY UPDATE`, f.customerID).Scan(&lockedID); err != nil {
		t.Fatalf("lock customer for exchange: %v", err)
	}

	exCustDone := make(chan resp, 1)
	go func() {
		exCustDone <- f.exchange("", map[string]any{
			"original_sale_id": sale2, "customer_id": f.customerID.String(),
			"returned_lines": []map[string]any{lineOf(line2, "1", "wrong_item")},
			"sale_lines":     f.replaceWith("1"),
		})
	}()

	waitForWaiter(`SELECT count(*) FROM pg_locks WHERE NOT granted AND locktype IN ('tuple', 'transactionid')`)

	txProbeEx, err := db.Pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin txProbeEx: %v", err)
	}
	err = txProbeEx.QueryRow(ctx, `SELECT next_value FROM document_counters WHERE series = 'credit_memo' FOR UPDATE NOWAIT`).Scan(&lastCM)
	_ = txProbeEx.Rollback(ctx)
	if err != nil {
		_ = txCustEx.Rollback(ctx)
		<-exCustDone
		t.Fatalf("exchange locked document_counters (step 8) before customers (step 7): %v", err)
	}
	if err := txCustEx.Commit(ctx); err != nil {
		t.Fatalf("commit txCustEx: %v", err)
	}
	if r := <-exCustDone; r.status != http.StatusCreated {
		t.Fatalf("exchange after customer unlock = %d: %s", r.status, r.raw)
	}

	// 3. Three-contender cross-table concurrency at pool size 4:
	// Contender A: exchange on saleA (customer, overlapping product, cash/account)
	// Contender B: return on saleC (same customer, overlapping product, account credit)
	// Contender C: store-credit + on-account sale completion on saleB (same customer, overlapping product)
	f.accountReturnOf() // leaves 599 of open store credit on f.customerID
	saleA, totalA := f.cartOf("2", &f.customerID)
	if r := f.completeSale(saleA, tender("cash", totalA)); r.status != http.StatusOK {
		t.Fatalf("saleA = %d: %s", r.status, r.raw)
	}
	lineA := f.getSale(t, saleA)["lines"].([]any)[0].(map[string]any)["id"].(string)

	saleC, totalC := f.cartOf("1", &f.customerID)
	if r := f.completeSale(saleC, tender("cash", totalC)); r.status != http.StatusOK {
		t.Fatalf("saleC = %d: %s", r.status, r.raw)
	}
	lineC := f.getSale(t, saleC)["lines"].([]any)[0].(map[string]any)["id"].(string)

	saleB, totalB := f.cartOf("2", &f.customerID)
	if r := f.attach(saleB, map[string]any{"customer_id": f.customerID.String(), "customer_po": "PO-CONC-1"}); r.status != http.StatusOK {
		t.Fatalf("attach saleB = %d: %s", r.status, r.raw)
	}

	balBefore := f.scalar(`SELECT ROUND(balance_due * 100)::bigint FROM customers WHERE id = $1`, f.customerID).(int64)
	qtyBefore := f.scalar(`SELECT ROUND(SUM(quantity) * 10000)::bigint FROM inventory WHERE product_id = $1`, f.productID).(int64)
	cmBefore := f.scalar(`SELECT next_value FROM document_counters WHERE series = 'credit_memo'`).(int64)
	inBefore := f.scalar(`SELECT next_value FROM document_counters WHERE series = 'invoice'`).(int64)

	start := make(chan struct{})
	var wg sync.WaitGroup
	var resEx, resRet, resSale resp
	wg.Add(3)
	go func() {
		defer wg.Done()
		<-start
		resEx = f.exchange("", map[string]any{
			"original_sale_id": saleA, "customer_id": f.customerID.String(),
			"returned_lines": []map[string]any{lineOf(lineA, "1", "wrong_item")},
			"sale_lines":     f.replaceWith("2"),
			"tenders":        []map[string]any{tender("cash", 599)},
		})
	}()
	go func() {
		defer wg.Done()
		<-start
		resRet = f.do("POST", "/api/v1/pos/returns", map[string]any{
			"register_id": f.register, "original_sale_id": saleC, "customer_id": f.customerID.String(),
			"refund_method": "account", "reason": "unused",
			"lines": []map[string]any{lineOf(lineC, "1", "overbought")},
		}, "X-Test-Role", "cashier", "X-Test-Sub", mustUUID(t))
	}()
	go func() {
		defer wg.Done()
		<-start
		resSale = f.completeSale(saleB, tender("store_credit", 599), tender("account", totalB-599))
	}()
	close(start)
	wg.Wait()

	if resEx.status != http.StatusCreated {
		t.Fatalf("concurrent exchange = %d: %s", resEx.status, resEx.raw)
	}
	if resRet.status != http.StatusCreated {
		t.Fatalf("concurrent return = %d: %s", resRet.status, resRet.raw)
	}
	if resSale.status != http.StatusOK {
		t.Fatalf("concurrent store-credit/account sale = %d: %s", resSale.status, resSale.raw)
	}

	// Balance conservation:
	// - Exchange returned 599 (applied to replacement 1198) + 599 cash paid -> net 0 on balance_due
	// - Return to account -> -599 on balance_due
	// - SaleB (1198) paid 599 by existing store_credit + 599 on account -> +1198 invoice, 0 subledger for credit memo app -> net +1198 on balance_due
	wantBal := balBefore - 599 + totalB
	gotBal := f.scalar(`SELECT ROUND(balance_due * 100)::bigint FROM customers WHERE id = $1`, f.customerID).(int64)
	if gotBal != wantBal {
		t.Errorf("balance_due = %d, want %d", gotBal, wantBal)
	}
	// Stock conservation:
	// - Exchange: +1 returned, -2 replacement = -1
	// - Return: +1 returned = +1
	// - SaleB: -2 issued = -2
	// Net = -2 units (-20000 scale-4)
	wantQty := qtyBefore - 20000
	gotQty := f.scalar(`SELECT ROUND(SUM(quantity) * 10000)::bigint FROM inventory WHERE product_id = $1`, f.productID).(int64)
	if gotQty != wantQty {
		t.Errorf("qty_on_hand = %d, want %d", gotQty, wantQty)
	}
	// Gapless counters: 2 credit memos (exchange + return) and 2 invoices (exchange + saleB).
	if gotCM := f.scalar(`SELECT next_value FROM document_counters WHERE series = 'credit_memo'`).(int64); gotCM != cmBefore+2 {
		t.Errorf("credit_memo counter = %d, want %d", gotCM, cmBefore+2)
	}
	if gotIN := f.scalar(`SELECT next_value FROM document_counters WHERE series = 'invoice'`).(int64); gotIN != inBefore+2 {
		t.Errorf("invoice counter = %d, want %d", gotIN, inBefore+2)
	}
	f.assertARInvariants(t)
}

type recordingTaxProvider struct {
	taxCents int64
	calls    int
	sawInTx  bool
}

func (p *recordingTaxProvider) ProviderConfigured() bool { return true }

func (p *recordingTaxProvider) PreviewTax(ctx context.Context, req *tax.TaxPreviewRequest) (*tax.TaxResult, error) {
	p.calls++
	if database.InTx(ctx) {
		p.sawInTx = true
	}
	return &tax.TaxResult{TotalTax: p.taxCents}, nil
}

// RULE: an exchange with an external tax provider prices the replacement lines
// and quotes the tax provider before opening the database transaction, on both
// preview and commit.
func TestAnExchangeQuotesTheTaxProviderBeforeOpeningTheTransaction(t *testing.T) {
	testutil.LockOutboxTables(t)
	prov := &recordingTaxProvider{taxCents: 90} // subtotal 1100 + 90 provider tax = 1190
	f := newFixture(t, testutil.RequireDB(t))
	f.openTill(0)
	sale, line, _ := f.cashSaleLine(t, "2") // 1198 (599 per unit at branch rate)
	f.service.WithTaxProvider(prov)

	body := map[string]any{
		"original_sale_id": sale, "reason": "wrong size",
		"returned_lines": []map[string]any{lineOf(line, "1", "wrong_item")},
		"sale_lines":     f.replaceWith("2"),
	}
	prev := f.exchange("/preview", body)
	if prev.status != http.StatusOK {
		t.Fatalf("preview with tax provider = %d: %s", prev.status, prev.raw)
	}
	if num(t, prev.body, "returned_cents") != 599 || num(t, prev.body, "replacement_cents") != 1190 || num(t, prev.body, "difference_cents") != 591 {
		t.Errorf("preview figures = %v, want returned 599, replacement 1190, difference 591", prev.body)
	}
	if prov.calls != 1 || prov.sawInTx {
		t.Errorf("provider after preview: calls = %d, sawInTx = %v, want 1 call outside tx", prov.calls, prov.sawInTx)
	}

	body["tenders"] = []map[string]any{tender("cash", 591)}
	res := f.exchange("", body)
	if res.status != http.StatusCreated {
		t.Fatalf("exchange with tax provider = %d: %s", res.status, res.raw)
	}
	if num(t, res.body, "returned_cents") != 599 || num(t, res.body, "replacement_cents") != 1190 || num(t, res.body, "difference_cents") != 591 {
		t.Errorf("exchange figures = %v, want returned 599, replacement 1190, difference 591", res.body)
	}
	if prov.calls != 2 || prov.sawInTx {
		t.Errorf("provider after exchange: calls = %d, sawInTx = %v, want 2 calls outside tx", prov.calls, prov.sawInTx)
	}
	newSale := res.body["sale"].(map[string]any)
	invID := uuid.MustParse(str(t, newSale, "invoice_id"))
	if src := f.scalar(`SELECT tax_source || ':' || ROUND(tax_amount * 100)::bigint FROM invoices WHERE id = $1`, invID); src != "PROVIDER:90" {
		t.Errorf("invoice tax = %v, want PROVIDER:90", src)
	}
	f.assertARInvariants(t)
}

// RULE: POST /api/v1/pos/exchanges/preview is read-only: it succeeds while
// another transaction holds FOR UPDATE locks on document_counters, customers,
// pos_transactions, and inventory, and it never advances
// pos_transaction_number_seq or pos_return_number_seq.
func TestExchangePreviewIsReadOnlyAndNeverLocksCountersCustomersOrAdvancesSequences(t *testing.T) {
	testutil.LockOutboxTables(t)
	db := testutil.RequireDBMaxConns(t, 4)
	f := newFixture(t, db)
	f.openTill(0)
	sale, line, _ := f.cashSaleLine(t, "3")
	// Seed one return so pos_return_number_seq is called at least once.
	if r := f.returnLines(t, sale, "", lineOf(line, "1", "defective")); r.status != http.StatusCreated {
		t.Fatalf("seed return = %d: %s", r.status, r.raw)
	}

	txSeqBefore := f.scalar(`SELECT last_value FROM pos_transaction_number_seq`).(int64)
	retSeqBefore := f.scalar(`SELECT last_value FROM pos_return_number_seq`).(int64)
	cmBefore := f.scalar(`SELECT next_value FROM document_counters WHERE series = 'credit_memo'`).(int64)
	inBefore := f.scalar(`SELECT next_value FROM document_counters WHERE series = 'invoice'`).(int64)

	ctx := t.Context()
	txHold, err := db.Pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin txHold: %v", err)
	}
	defer func() { _ = txHold.Rollback(ctx) }()

	if _, err := txHold.Exec(ctx, `SELECT next_value FROM document_counters WHERE series IN ('credit_memo', 'invoice') FOR UPDATE`); err != nil {
		t.Fatalf("lock document_counters: %v", err)
	}
	if _, err := txHold.Exec(ctx, `SELECT id FROM customers FOR UPDATE`); err != nil {
		t.Fatalf("lock customers: %v", err)
	}
	if _, err := txHold.Exec(ctx, `SELECT id FROM pos_transactions WHERE id = $1 FOR UPDATE`, uuid.MustParse(sale)); err != nil {
		t.Fatalf("lock sale: %v", err)
	}
	if _, err := txHold.Exec(ctx, `SELECT id FROM inventory WHERE product_id = $1 FOR UPDATE`, f.productID); err != nil {
		t.Fatalf("lock inventory: %v", err)
	}

	done := make(chan resp, 1)
	go func() {
		done <- f.exchange("/preview", map[string]any{
			"original_sale_id": sale,
			"returned_lines":   []map[string]any{lineOf(line, "1", "wrong_item")},
			"sale_lines":       f.replaceWith("2"),
		})
	}()

	select {
	case prev := <-done:
		if prev.status != http.StatusOK {
			t.Fatalf("preview = %d: %s", prev.status, prev.raw)
		}
		if num(t, prev.body, "returned_cents") != 599 || num(t, prev.body, "replacement_cents") != 1198 || num(t, prev.body, "difference_cents") != 599 {
			t.Errorf("preview figures = %v, want 599, 1198, 599", prev.body)
		}
	case <-time.After(500 * time.Millisecond):
		_ = txHold.Rollback(ctx)
		<-done
		t.Fatalf("preview blocked on FOR UPDATE locks held on document_counters, customers, pos_transactions, or inventory")
	}

	_ = txHold.Rollback(ctx)

	if got := f.scalar(`SELECT last_value FROM pos_transaction_number_seq`).(int64); got != txSeqBefore {
		t.Errorf("pos_transaction_number_seq advanced on preview: got %d, want %d", got, txSeqBefore)
	}
	if got := f.scalar(`SELECT last_value FROM pos_return_number_seq`).(int64); got != retSeqBefore {
		t.Errorf("pos_return_number_seq advanced on preview: got %d, want %d", got, retSeqBefore)
	}
	if got := f.scalar(`SELECT next_value FROM document_counters WHERE series = 'credit_memo'`).(int64); got != cmBefore {
		t.Errorf("credit_memo counter changed on preview: got %d, want %d", got, cmBefore)
	}
	if got := f.scalar(`SELECT next_value FROM document_counters WHERE series = 'invoice'`).(int64); got != inBefore {
		t.Errorf("invoice counter changed on preview: got %d, want %d", got, inBefore)
	}
}



