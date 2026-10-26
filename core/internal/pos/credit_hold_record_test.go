// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package pos_test

// A credit hold record the AR core placed (audit CP-11, item 1): an ACTIVE
// row in credit_holds stops a charge sale at the counter, the standing read
// states it with its own reason and the completion refuses the same code
// inside the sale's transaction, an override window on the hold allows up to
// the hold's own cap, and every other tender still completes.

import (
	"context"
	"log/slog"
	"net/http"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gablelbm/gable/internal/account"
	"github.com/gablelbm/gable/internal/gl"
	"github.com/gablelbm/gable/internal/testutil"
	"github.com/gablelbm/gable/pkg/database"
	"github.com/google/uuid"
)

// placeActiveHold writes the customer's standing hold the way the AR core's
// place act does: an ACTIVE row and the flag it keeps in sync.
func (f *fixture) placeActiveHold(reason string) {
	f.t.Helper()
	mustExec(f.t, f.db, `INSERT INTO credit_holds (customer_id, reason, hold_type, status, placed_by)
		VALUES ($1, $2, 'MANUAL', 'ACTIVE', 'ar-test')`, f.customerID, reason)
	mustExec(f.t, f.db, `UPDATE customers SET credit_hold = TRUE WHERE id = $1`, f.customerID)
	f.t.Cleanup(func() {
		mustExec(f.t, f.db, `DELETE FROM credit_holds WHERE customer_id = $1`, f.customerID)
		mustExec(f.t, f.db, `UPDATE customers SET credit_hold = FALSE WHERE id = $1`, f.customerID)
	})
}

// placeOverriddenHold writes the hold with a manager's window on it: the
// same row, OVERRIDDEN, with the window's end and the cap the window allows.
func (f *fixture) placeOverriddenHold(reason string, until time.Time, maxOrder any) {
	f.t.Helper()
	mustExec(f.t, f.db, `INSERT INTO credit_holds (customer_id, reason, hold_type, status, placed_by,
			override_until, override_by, override_reason, max_order_cents)
		VALUES ($1, $2, 'MANUAL', 'OVERRIDDEN', 'ar-test', $3, 'manager', 'agreed on the phone', $4)`,
		f.customerID, reason, until, maxOrder)
	mustExec(f.t, f.db, `UPDATE customers SET credit_hold = FALSE WHERE id = $1`, f.customerID)
	f.t.Cleanup(func() {
		mustExec(f.t, f.db, `DELETE FROM credit_holds WHERE customer_id = $1`, f.customerID)
		mustExec(f.t, f.db, `UPDATE customers SET credit_hold = FALSE WHERE id = $1`, f.customerID)
	})
}

// releaseHold ends the standing hold the way the AR core's release act does.
func (f *fixture) releaseHold() {
	f.t.Helper()
	mustExec(f.t, f.db, `UPDATE credit_holds SET status = 'RELEASED', released_at = NOW(),
		released_by = 'ar-test', release_reason = 'paid up' WHERE customer_id = $1`, f.customerID)
	mustExec(f.t, f.db, `UPDATE customers SET credit_hold = FALSE WHERE id = $1`, f.customerID)
}

// newLedgerEntries counts the journal entries this fixture's acts wrote.
func (f *fixture) newLedgerEntries() int64 {
	f.t.Helper()
	rows, err := f.db.Pool.Query(context.Background(), `SELECT id::text FROM gl_journal_entries`)
	if err != nil {
		f.t.Fatal(err)
	}
	defer rows.Close()
	var n int64
	for rows.Next() {
		var id string
		if rows.Scan(&id) == nil && !f.priorEntries[id] {
			n++
		}
	}
	return n
}

// RULE: an ACTIVE hold record blocks the charge sale: the standing read says
// blocked account_hold with the hold's reason in the words and pay down
// unavailable, the completion answers 409 account_hold and writes nothing:
// no invoice, no AR subledger row, no ledger entry.
func TestCP11_ActiveHoldBlocksChargeSale(t *testing.T) {
	testutil.LockOutboxTables(t)
	f := newFixture(t, testutil.RequireDB(t))
	f.placeActiveHold("past due 60 days")
	sale, total := f.accountCart("2", nil)
	stockBefore := f.stock()

	v := verdictOf(t, f.standing(f.customerID, "amount_cents="+itoa(total)))
	if v["verdict"] != "blocked" || v["reason"] != "account_hold" {
		t.Fatalf("verdict = %v reason %v, want blocked account_hold", v["verdict"], v["reason"])
	}
	if msg := str(t, v, "message"); !strings.Contains(msg, "past due 60 days") {
		t.Errorf("message = %q, want the hold's reason in it", msg)
	}
	if wayOf(t, v, "pay_down")["available"] != false {
		t.Errorf("pay_down must not be offered: paying the balance does not lift a hold")
	}
	if wayOf(t, v, "manager_override")["available"] != true {
		t.Errorf("a manager override must be offered")
	}
	for _, o := range f.standing(f.customerID, "").body["payment_options"].([]any) {
		if o == "account" {
			t.Errorf("payment_options must not offer account to a held account")
		}
	}

	r := f.completeSale(sale, tender("account", total))
	if _, blockers, _ := errorOf(t, r); r.status != http.StatusConflict || len(blockers) != 1 || blockers[0] != "account_hold" {
		t.Fatalf("an account tender on a held account = %d: %s, want 409 account_hold", r.status, r.raw)
	}
	if f.counterInvoices(t) != 0 || f.balance() != 0 || f.newLedgerEntries() != 0 {
		t.Errorf("a refused charge wrote something: invoices %d, balance %d, ledger entries %d",
			f.counterInvoices(t), f.balance(), f.newLedgerEntries())
	}
	if n := f.scalar(`SELECT count(*) FROM customer_transactions WHERE customer_id = $1`, f.customerID).(int64); n != 0 {
		t.Errorf("the refused charge wrote %d AR subledger rows, want 0", n)
	}
	if f.stock() != stockBefore {
		t.Errorf("a refused charge moved the stock")
	}
}

// RULE: a hold placed after the customer was attached still stops the charge:
// the completion re checks the verdict inside the sale's transaction.
func TestCP11_HoldPlacedAfterAttachStillBlocks(t *testing.T) {
	testutil.LockOutboxTables(t)
	f := newFixture(t, testutil.RequireDB(t))
	sale, total := f.accountCart("2", nil)
	f.placeActiveHold("credit review")
	r := f.completeSale(sale, tender("account", total))
	if _, blockers, _ := errorOf(t, r); r.status != http.StatusConflict || len(blockers) != 1 || blockers[0] != "account_hold" {
		t.Fatalf("a hold placed after the attach = %d: %s, want 409 account_hold", r.status, r.raw)
	}
	if f.counterInvoices(t) != 0 {
		t.Errorf("a refused charge wrote an invoice")
	}
	f.assertARInvariants(t)
}

// RULE: a released hold no longer blocks: the charge completes with its
// invoice and the account's balance.
func TestCP11_ReleasedHoldAllowsCharge(t *testing.T) {
	testutil.LockOutboxTables(t)
	f := newFixture(t, testutil.RequireDB(t))
	f.placeActiveHold("past due")
	f.releaseHold()
	if v := verdictOf(t, f.standing(f.customerID, "")); v["verdict"] != "allowed" {
		t.Fatalf("verdict after the release = %v, want allowed", v["verdict"])
	}
	sale, total := f.accountCart("1", nil)
	if r := f.completeSale(sale, tender("account", total)); r.status != http.StatusOK {
		t.Fatalf("charge after the release = %d: %s", r.status, r.raw)
	}
	if got := f.balance(); got != total {
		t.Errorf("balance_due = %d, want %d", got, total)
	}
	f.assertARInvariants(t)
}

// RULE: an OVERRIDDEN hold whose window is still ahead allows the sale up to
// its max order cents and blocks above them with words that name the cap; a
// window that has passed counts as standing.
func TestCP11_OverrideWindowAllowsUpToCap(t *testing.T) {
	testutil.LockOutboxTables(t)
	f := newFixture(t, testutil.RequireDB(t))
	ahead := time.Now().Add(2 * time.Hour)
	f.placeOverriddenHold("past due", ahead, 599)

	// inside the window, at the cap and under it: allowed
	sale, total := f.accountCart("1", nil)
	if total != 599 {
		t.Fatalf("the fixture sale is %d cents, the test is written for 599", total)
	}
	if r := f.completeSale(sale, tender("account", total)); r.status != http.StatusOK {
		t.Fatalf("a sale at the cap inside the window = %d: %s", r.status, r.raw)
	}

	// inside the window, over the cap: blocked with the cap in the words
	over, total2 := f.accountCart("2", nil)
	v := verdictOf(t, f.standing(f.customerID, "amount_cents="+itoa(total2)))
	if v["verdict"] != "blocked" || v["reason"] != "account_hold" {
		t.Fatalf("verdict over the cap = %v reason %v, want blocked account_hold", v["verdict"], v["reason"])
	}
	if msg := str(t, v, "message"); !strings.Contains(msg, "5.99") {
		t.Errorf("message = %q, want the cap (5.99) in words in it", msg)
	}
	if r := f.completeSale(over, tender("account", total2)); r.status != http.StatusConflict {
		t.Errorf("a sale over the cap inside the window = %d, want 409: %s", r.status, r.raw)
	}

	// after the window: the hold counts as standing again
	f.releaseHoldWindowPast()
	if v := verdictOf(t, f.standing(f.customerID, "amount_cents=100")); v["verdict"] != "blocked" || v["reason"] != "account_hold" {
		t.Errorf("verdict after the window = %v reason %v, want blocked account_hold", v["verdict"], v["reason"])
	}
	f.assertARInvariants(t)
}

// releaseHoldWindowPast expires the override window without releasing the
// hold: the row still stands, its window behind it.
func (f *fixture) releaseHoldWindowPast() {
	f.t.Helper()
	mustExec(f.t, f.db, `UPDATE credit_holds SET override_until = NOW() - INTERVAL '1 hour' WHERE customer_id = $1`, f.customerID)
	mustExec(f.t, f.db, `UPDATE customers SET credit_hold = TRUE WHERE id = $1`, f.customerID)
}

// RULE: a held account still buys: cash, check, card and store credit
// tenders complete on a held account.
func TestCP11_HeldAccountStillPaysCash(t *testing.T) {
	testutil.LockOutboxTables(t)
	f := newFixture(t, testutil.RequireDB(t))
	f.placeActiveHold("past due")
	sale, total := f.accountCart("1", nil)
	if r := f.completeSale(sale, tender("cash", total)); r.status != http.StatusOK {
		t.Fatalf("a cash sale on a held account = %d: %s", r.status, r.raw)
	}
	if got := f.balance(); got != 0 {
		t.Errorf("balance_due = %d, want 0 (nothing went on the account)", got)
	}
	f.assertARInvariants(t)
}

// RULE: a manager's per sale override covers a hold as it covers the zero
// limit: one act for one sale, consumed by its completion.
func TestCP11_ManagerOverrideCoversHold(t *testing.T) {
	testutil.LockOutboxTables(t)
	f := newFixture(t, testutil.RequireDB(t))
	f.placeActiveHold("past due")
	cashier, manager := uuid.NewString(), uuid.NewString()
	sale := f.startSaleBy(cashier, &f.customerID)
	if r := f.addLine(sale, f.productLine("1")); r.status != http.StatusOK {
		t.Fatalf("add line = %d: %s", r.status, r.raw)
	}
	total := num(t, f.getSale(t, sale), "total_cents")
	if r := f.completeSale(sale, tender("account", total)); r.status != http.StatusConflict {
		t.Fatalf("before the override = %d, want 409: %s", r.status, r.raw)
	}
	if r := f.override(sale, "account", "agreed on the phone", total, "admin", manager); r.status != http.StatusCreated {
		t.Fatalf("override = %d: %s", r.status, r.raw)
	}
	if r := f.completeSale(sale, tender("account", total)); r.status != http.StatusOK {
		t.Fatalf("complete with the override = %d: %s", r.status, r.raw)
	}
	if got := f.balance(); got != total {
		t.Errorf("balance_due = %d, want %d", got, total)
	}
	f.assertARInvariants(t)
}

// RULE: a hold placed while sales complete (pool size 4, three contenders,
// the hold placed through the AR core's own act, the completions the real
// completions): the hold act serializes with the completion on the
// customer's credit lock, so it cannot commit inside a completion's window
// (between its fact read and its commit) and let a charge land on an
// account that is held from that moment on.
func TestCP11_HoldPlacedWhileSalesComplete(t *testing.T) {
	testutil.LockOutboxTables(t)
	f := newFixture(t, testutil.RequireDBMaxConns(t, 4))
	arSvc := account.NewService(f.db, gl.NewService(gl.NewRepository(f.db), nil, slog.Default()), slog.Default())
	sales := make([]string, 3)
	for i := range sales {
		sales[i] = f.startSale(&f.customerID)
		if r := f.addLine(sales[i], f.productLine("1")); r.status != http.StatusOK {
			t.Fatalf("add line = %d: %s", r.status, r.raw)
		}
	}
	t.Cleanup(func() {
		mustExec(t, f.db, `DELETE FROM credit_holds WHERE customer_id = $1`, f.customerID)
		mustExec(t, f.db, `UPDATE customers SET credit_hold = FALSE WHERE id = $1`, f.customerID)
	})

	// A connection of the test's own (never one of the pool's four) holds
	// the product's inventory row, so every completion stops at its stock
	// out: after it has read the account facts and passed the verdict,
	// before it posts. That is the window the hold act must not land in.
	stall, err := database.Connect(os.Getenv("DATABASE_URL"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { stall.Close() })
	stallTx, err := stall.Pool.Begin(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer stallTx.Rollback(context.Background())
	var stallXID string
	if err := stallTx.QueryRow(context.Background(), `
		SELECT pg_current_xact_id()::text FROM inventory WHERE product_id = $1 AND location_id = $2
		FOR UPDATE OF inventory`, f.productID, f.yardID).Scan(&stallXID); err != nil {
		t.Fatalf("hold the inventory row: %v", err)
	}

	// Three completions race (pool size 4): the first holds the customer's
	// credit lock and stops at the stock out, the other two queue on the
	// lock.
	ctx := context.Background()
	statuses := make([]int, 3)
	var wg sync.WaitGroup
	for i := range sales {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			statuses[i] = f.completeSale(sales[i], tender("account", 599)).status
		}(i)
	}
	// wait until a completion is blocked on the stalled row: a lock waiting
	// on the stall's transaction id
	deadline := time.Now().Add(10 * time.Second)
	for {
		var waiting int
		if err := f.db.Pool.QueryRow(ctx, `
			SELECT count(*) FROM pg_locks WHERE NOT granted AND transactionid = $1`, stallXID).Scan(&waiting); err != nil {
			t.Fatal(err)
		}
		if waiting > 0 || time.Now().After(deadline) {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if n := f.scalar(`SELECT count(*) FROM pg_locks WHERE NOT granted AND transactionid = $1`, stallXID).(int64); n == 0 {
		t.Fatal("no completion ever reached its stock out: the stall proved nothing")
	}

	// The AR core places the hold, the act the route drives. It must not
	// commit while a completion's transaction is open on this customer.
	placed := make(chan error, 1)
	go func() {
		_, _, err := arSvc.PlaceHold(ctx, account.PlaceHoldIn{CustomerID: f.customerID,
			Reason: "credit review", HoldType: "manual", Actor: "ar-test"})
		placed <- err
	}()
	select {
	case err := <-placed:
		if err == nil {
			t.Fatal("the hold act committed while a charge sale's transaction was open: money can go on an account the hold already holds")
		}
		t.Fatalf("the hold act failed: %v", err)
	case <-time.After(2 * time.Second):
		// still running: the act is waiting for the credit lock the
		// completions hold, which is the rule this test proves
	}

	// let the sales through and take the hold's answer
	if err := stallTx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	wg.Wait()
	if err := <-placed; err != nil {
		t.Fatalf("the hold act failed: %v", err)
	}
	// Every sale either completed or was refused by the hold itself: the
	// credit lock orders the four acts, so a sale refused after the hold
	// landed (409 account_hold) is the rule working, and a sale that
	// completed decided before the hold could land.
	wins := 0
	for i, st := range statuses {
		if st == http.StatusOK {
			wins++
			continue
		}
		if st != http.StatusConflict {
			t.Errorf("sale %d = %d, want 200 or 409", i, st)
		}
	}
	if got := f.balance(); got != int64(wins)*599 {
		t.Errorf("balance_due = %d with %d completions, want %d", got, wins, int64(wins)*599)
	}

	// the hold stands: the next charge on the account is refused
	next := f.startSale(&f.customerID)
	if r := f.addLine(next, f.productLine("1")); r.status != http.StatusOK {
		t.Fatalf("add line = %d: %s", r.status, r.raw)
	}
	if r := f.completeSale(next, tender("account", 599)); r.status != http.StatusConflict {
		t.Errorf("a sale after the hold = %d, want 409: %s", r.status, r.raw)
	}
	f.assertARInvariants(t)
}
