// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package pos_test

// The transaction proofs of ADR 0005 section 14.2 C2-5: a failing event
// write rolls the whole sale back (no invoice, payment, stock move or
// entry), and the contention tests at pool size 4 (two sales of the last
// unit, a void racing a return, three registers completing sales with no
// gap in the invoice series against a payment for the walk-in customer).

import (
	"context"
	"fmt"
	"net/http"
	"sync"
	"testing"
	"time"

	"github.com/gablelbm/gable/internal/testutil"
	"github.com/google/uuid"
)

// RULE: a sale whose event write fails leaves no invoice, payment, stock
// move or entry (the recipe's failing event proof, for each kind of write:
// the completion and the void).
func TestFailingEventWriteRollsTheSaleBack(t *testing.T) {
	testutil.LockOutboxTables(t)
	f := newFixture(t, testutil.RequireDBMaxConns(t, 4), func(f *fixture) {
		f.events.fail = "pos_transaction.completed"
	})
	saleID := f.startSale(nil)
	f.addLine(saleID, f.productLine("3"))
	// the table wide counts before the attempt: the seeded database shares
	// these tables, so the proof is that the failed act moves none of them.
	invoicesBefore := countOf(t, f.db, `SELECT count(*) FROM invoices WHERE order_id IS NULL`)
	paymentsBefore := countOf(t, f.db, `SELECT count(*) FROM payments`)
	entriesBefore := countOf(t, f.db, `SELECT count(*) FROM gl_journal_entries`)
	r := f.completeSale(saleID, tender("cash", 3266))
	if r.status == httpOK {
		t.Fatal("the sale completed with a failing event write")
	}
	// nothing stayed: no invoice, no payment, no tender, the stock unmoved,
	// the sale still open.
	for _, c := range []struct {
		sql  string
		want int64
		args []any
	}{
		{`SELECT count(*) FROM invoices WHERE order_id IS NULL`, invoicesBefore, nil},
		{`SELECT count(*) FROM payments`, paymentsBefore, nil},
		{`SELECT count(*) FROM pos_tenders WHERE transaction_id = $1`, 0, []any{saleID}},
		{`SELECT count(*) FROM gl_journal_entries`, entriesBefore, nil},
		{`SELECT count(*) FROM events_outbox WHERE entity_type = 'pos_transaction'`, 0, nil},
	} {
		if got := countOf(t, f.db, c.sql, c.args...); got != c.want {
			t.Errorf("%s = %d, want %d", c.sql, got, c.want)
		}
	}
	if got := f.stock(); got != "100.0000/0.0000" {
		t.Errorf("stock = %s, want the 100 untouched", got)
	}
	if got := str(t, f.getSale(t, saleID), "status"); got != "open" {
		t.Errorf("sale status = %q, want open", got)
	}
	// The gapless invoice series spent nothing: the next invoice takes the
	// number the rolled back sale would have taken.
	f.events.fail = ""
	saleID2, _ := f.saleOf("1", tender("cash", 599))
	if got := countOf(t, f.db, `SELECT count(*) FROM invoices WHERE order_id IS NULL`); got != invoicesBefore+1 {
		t.Errorf("%d invoices after the retry, want %d", got, invoicesBefore+1)
	}
	_ = saleID2

	// The same proof for the void.
	f.events.fail = "pos_transaction.voided"
	f2 := newFixture(t, testutil.RequireDBMaxConns(t, 4), func(f *fixture) {
		f.events.fail = "pos_transaction.voided"
	})
	saleID3, body := f2.saleOf("2", tender("cash", 1198))
	r = f2.do("POST", "/api/v1/pos/transactions/"+saleID3+"/void",
		map[string]any{"reason": "nope", "revision": rev(t, body)},
		"X-Test-Role", "cashier", "X-Test-Sub", mustUUID(t))
	if r.status == httpOK {
		t.Fatal("the void committed with a failing event write")
	}
	if got := str(t, f2.getSale(t, saleID3), "status"); got != "completed" {
		t.Errorf("sale status = %q, want completed (the void rolled back)", got)
	}
	if got := f2.stock(); got != "98.0000/0.0000" {
		t.Errorf("stock = %s, want 98 (the goods still gone)", got)
	}
	if got := f2.accountBalance("1010"); got != 1198 {
		t.Errorf("cash balance = %d, want the 1198 still booked", got)
	}
}

// RULE: two sales of the last unit of stock at one till: one completes,
// the other is refused with insufficient_stock and moves nothing.
func TestTwoSalesOfTheLastUnit(t *testing.T) {
	testutil.LockOutboxTables(t)
	f := newFixture(t, testutil.RequireDBMaxConns(t, 4))
	mustExec(t, f.db, `UPDATE inventory SET quantity = 2 WHERE product_id = $1`, f.productID)
	var wg sync.WaitGroup
	results := make([]int, 2)
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			saleID := f.startSale(nil)
			if r := f.addLine(saleID, f.productLine("2")); r.status != httpOK {
				results[i] = r.status
				return
			}
			results[i] = f.completeSale(saleID, tender("cash", 4390)).status
		}(i)
	}
	wg.Wait()
	wins := 0
	for _, st := range results {
		if st == httpOK {
			wins++
		}
	}
	if wins != 1 {
		t.Errorf("results = %v, want exactly one winner", results)
	}
	if got := f.stock(); got != "0.0000/0.0000" {
		t.Errorf("stock = %s, want 0 (the last unit went out once)", got)
	}
	if got := countOf(t, f.db, `SELECT count(*) FROM invoices WHERE order_id IS NULL`); got != 1 {
		t.Errorf("%d invoices, want 1", got)
	}
}

// RULE: three registers completing sales at pool size 4 get consecutive
// invoice numbers with no gap and no deadlock against a payment for the
// walk-in customer (the counter's hot rows: the gapless series and the
// walk-in customer row, ADR 0005 4.1).
func TestThreeRegistersConsecutiveNumbersAgainstAWalkInPayment(t *testing.T) {
	testutil.LockOutboxTables(t)
	f := newFixture(t, testutil.RequireDBMaxConns(t, 4))
	registers := []string{f.register, "REG-" + uuid.NewString()[:8], "REG-" + uuid.NewString()[:8]}
	for _, reg := range registers[1:] {
		mustExec(t, f.db, `INSERT INTO pos_registers (id, location_id, name, branch_id) VALUES ($1, $2, 'r', $3)`,
			reg, f.yardID, f.branchID)
	}
	var wg sync.WaitGroup
	errs := make([]error, 3)
	for i, reg := range registers {
		wg.Add(1)
		go func(i int, reg string) {
			defer wg.Done()
			saleID := f.startSaleOn(reg)
			if err := f.addLineOn(saleID, f.productLine("1")); err != nil {
				errs[i] = err
				return
			}
			if r := f.completeSaleOn(reg, saleID, tender("cash", 599)); r.status != httpOK {
				errs[i] = fmt.Errorf("register %s: %d %s", reg, r.status, r.raw)
			}
		}(i, reg)
	}
	// three payments for the walk-in customer racing the sales, through the
	// real payment route the fixture mounts: their outcomes are asserted,
	// not discarded
	paymentStatuses := make([]int, 3)
	wg.Add(1)
	go func() {
		defer wg.Done()
		var walkIn string
		if err := f.db.Pool.QueryRow(context.Background(),
			`SELECT id::text FROM customers WHERE account_number = 'WALK-IN'`).Scan(&walkIn); err != nil {
			f.t.Errorf("walk-in customer: %v", err)
			return
		}
		for i := 0; i < 3; i++ {
			r := f.do("POST", "/api/v1/payments", map[string]any{
				"customer_id": walkIn, "method": "cash", "amount_cents": 100,
				"received_on": "2030-01-01",
			}, "X-Test-Role", "finance", "X-Test-Sub", mustUUID(t))
			paymentStatuses[i] = r.status
		}
	}()
	wg.Wait()
	for i, st := range paymentStatuses {
		if st != 201 {
			t.Errorf("racing payment %d = %d, want 201", i, st)
		}
	}
	// the three walk-in payments landed as unapplied cash
	if got := countOf(t, f.db, `SELECT count(*) FROM payments p
		JOIN customers c ON c.id = p.customer_id WHERE c.account_number = 'WALK-IN' AND p.amount_unapplied = 1.00`); got != 3 {
		t.Errorf("%d unapplied walk-in payments of 1.00, want 3", got)
	}
	f.assertARInvariants(t)
	for i, err := range errs {
		if err != nil {
			t.Errorf("register %d: %v", i, err)
		}
	}
	// consecutive numbers, no gap
	var numbers []int
	rows, err := f.db.Pool.Query(context.Background(), `
		SELECT substring(i.number FROM 4)::bigint FROM invoices i WHERE i.order_id IS NULL ORDER BY 1`)
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var n int
		if err := rows.Scan(&n); err != nil {
			t.Fatal(err)
		}
		numbers = append(numbers, n)
	}
	rows.Close()
	if len(numbers) != 3 {
		t.Fatalf("%d invoices, want 3", len(numbers))
	}
	for i := 1; i < len(numbers); i++ {
		if numbers[i] != numbers[i-1]+1 {
			t.Errorf("numbers = %v, want consecutive with no gap", numbers)
			break
		}
	}
}

// RULE: a void racing a return of the same sale: one wins, the other is
// refused, and the books stay consistent.
func TestVoidRacingTwoReturns(t *testing.T) {
	testutil.LockOutboxTables(t)
	f := newFixture(t, testutil.RequireDBMaxConns(t, 4))
	saleID, body := f.saleOf("6", tender("cash", 3593))
	lineID := body.body["lines"].([]any)[0].(map[string]any)["id"].(string)
	// three contenders on one sale at pool size 4: a void and two returns
	// of different quantities of the same line
	var wg sync.WaitGroup
	var voidStatus int
	returnStatuses := make([]int, 2)
	wg.Add(3)
	go func() {
		defer wg.Done()
		voidStatus = f.do("POST", "/api/v1/pos/transactions/"+saleID+"/void",
			map[string]any{"reason": "racing", "revision": rev(t, body)},
			"X-Test-Role", "cashier", "X-Test-Sub", mustUUID(t)).status
	}()
	for i, qty := range []string{"1", "2"} {
		go func(i int, qty string) {
			defer wg.Done()
			returnStatuses[i] = f.do("POST", "/api/v1/pos/returns", map[string]any{
				"register_id": f.register, "customer_id": f.customerID.String(), "original_sale_id": saleID,
				"refund_method": "cash", "reason": "racing",
				"lines": []map[string]any{{"line_id": lineID, "quantity": qty, "restock": true}},
			}, "X-Test-Role", "cashier", "X-Test-Sub", mustUUID(t)).status
		}(i, qty)
	}
	wg.Wait()
	won := func(status int) bool { return status >= 200 && status < 300 }
	t.Logf("void %d, returns %v, stock %s", voidStatus, returnStatuses, f.stock())
	var returnsWon int
	for _, st := range returnStatuses {
		if won(st) {
			returnsWon++
		}
	}
	if won(voidStatus) && returnsWon > 0 {
		t.Fatalf("the void and a return both won (%d, %v)", voidStatus, returnStatuses)
	}
	f.assertARInvariants(t)
	// the stock is whole either way: the sale left 94; a void returns all
	// 6 (100); the returns are capped at what the line sold less each other,
	// so 97 (both won), 95 or 96 (one won) or 100 (the void won) are the
	// coherent outcomes
	switch got := f.stock(); got {
	case "97.0000/0.0000", "95.0000/0.0000", "96.0000/0.0000", "100.0000/0.0000":
	default:
		t.Errorf("stock = %s after the race", got)
	}
}

const httpOK = 200

// startSaleOn opens a cart on a named register.
func (f *fixture) startSaleOn(register string) string {
	f.t.Helper()
	r := f.do("POST", "/api/v1/pos/transactions", map[string]any{"register_id": register},
		"X-Test-Role", "cashier", "X-Test-Sub", mustUUID(f.t))
	if r.status != 201 {
		f.t.Fatalf("start sale on %s = %d: %s", register, r.status, r.raw)
	}
	return str(f.t, r.body, "id")
}

func (f *fixture) addLineOn(saleID string, line map[string]any) error {
	f.t.Helper()
	r := f.addLine(saleID, line)
	if r.status != httpOK {
		return fmt.Errorf("add line = %d: %s", r.status, r.raw)
	}
	return nil
}

func (f *fixture) completeSaleOn(register, saleID string, tenders ...map[string]any) resp {
	f.t.Helper()
	cur := f.getSale(f.t, saleID)
	return f.do("POST", "/api/v1/pos/transactions/"+saleID+"/complete",
		map[string]any{"tenders": tenders, "revision": num(f.t, cur, "revision")},
		"X-Test-Role", "cashier", "X-Test-Sub", mustUUID(f.t))
}

// RULE (third review P2-3): the return cap's in-transaction recheck is the
// guard that holds under a race: three concurrent returns of the one unit a
// sale sold let exactly one through (the pre-transaction check passes for
// every contender; the recheck under the sale row lock counts the winner).
func TestThreeConcurrentReturnsOfOneUnitLetOneThrough(t *testing.T) {
	testutil.LockOutboxTables(t)
	f := newFixture(t, testutil.RequireDBMaxConns(t, 4))
	f.ensureOpenTill(t)
	saleID, body := f.saleOf("1", tender("cash", 599))
	lineID := body.body["lines"].([]any)[0].(map[string]any)["id"].(string)
	// Park every contender past its pre-transaction check: hold the sale row
	// FOR UPDATE, let the three returns run their guards (they read committed
	// rows and pass), then release. All three reach the in-transaction cap
	// recheck under the row lock, where only one may pass.
	ctx := context.Background()
	park, err := f.db.Pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := park.Exec(ctx, `SELECT 1 FROM pos_transactions WHERE id = $1 FOR UPDATE`, saleID); err != nil {
		t.Fatal(err)
	}
	var mu sync.Mutex
	codes := make([]int, 3)
	blockersOf := make([]string, 3)
	var wg sync.WaitGroup
	for i := 0; i < 3; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			r := f.returnOn(t, saleID, lineID, "1")
			mu.Lock()
			codes[i] = r.status
			if r.status != http.StatusCreated {
				_, blockers, _ := errorOf(t, r)
				if len(blockers) > 0 {
					blockersOf[i] = blockers[0]
				}
				if blockersOf[i] == "" {
					t.Errorf("loser %d carried no blocker: %s", i, string(r.raw))
				}
			}
			mu.Unlock()
		}(i)
	}
	time.Sleep(500 * time.Millisecond)
	if err := park.Rollback(ctx); err != nil {
		t.Fatal(err)
	}
	wg.Wait()
	wins := 0
	for _, st := range codes {
		if st == http.StatusCreated {
			wins++
		}
	}
	if wins != 1 || len(codes) != 3 {
		t.Fatalf("return codes = %v, want exactly one 201 and two 409", codes)
	}
	// the refusals are the cap's, named: the recheck under the sale row lock
	// counted the winner before these were allowed
	for i, st := range codes {
		if st == http.StatusCreated {
			continue
		}
		if st != http.StatusConflict || blockersOf[i] != "exceeds_sold" {
			t.Errorf("loser %d = %d %q, want 409 exceeds_sold (the in-transaction cap recheck)", i, st, blockersOf[i])
		}
	}
	// the drawer paid one refund: the sale's 599 in, one 599 out
	if got := f.accountBalance("1010"); got != 0 {
		t.Errorf("cash balance = %d, want 0 (the sale in, exactly one refund out)", got)
	}
	if got := f.stock(); got != "100.0000/0.0000" {
		t.Errorf("stock = %s, want 100 (the one unit back once)", got)
	}
	f.assertARInvariants(t)
}
