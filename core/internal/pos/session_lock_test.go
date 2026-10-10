// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package pos_test

// The session the sale lives in is checked inside the transaction (ADR 0005
// section 14.2 C2-5, second review P2-2, first review P2-3): a sale cannot
// complete into a closed drawer (its money would miss the count), the void
// holds the session FOR SHARE against the close's FOR UPDATE, and the close
// takes its lock before it aggregates, so a void racing the close ends
// coherent either way.

import (
	"context"
	"net/http"
	"sync"
	"testing"
	"time"

	"github.com/gablelbm/gable/internal/testutil"
)

// RULE: a sale cannot complete into a closed session.
func TestASaleCannotCompleteIntoAClosedSession(t *testing.T) {
	testutil.LockOutboxTables(t)
	f := newFixture(t, testutil.RequireDB(t))
	sessionID := f.openTill(0)
	saleID := f.startSale(nil)
	if r := f.addLine(saleID, f.productLine("1")); r.status != http.StatusOK {
		t.Fatalf("add line = %d: %s", r.status, r.raw)
	}
	// the drawer closes while the sale is open
	r := f.do("POST", "/api/v1/pos/till/"+sessionID+"/close", map[string]any{
		"counted_by_method": map[string]any{"cash": 0}}, "X-Test-Role", "cashier", "X-Test-Sub", mustUUID(t))
	if r.status != http.StatusOK {
		t.Fatalf("close = %d: %s", r.status, r.raw)
	}
	cur := f.getSale(t, saleID)
	r = f.do("POST", "/api/v1/pos/transactions/"+saleID+"/complete",
		map[string]any{"tenders": []map[string]any{tender("cash", 599)}, "revision": num(t, cur, "revision")},
		"X-Test-Role", "cashier", "X-Test-Sub", mustUUID(t))
	if r.status != http.StatusConflict {
		t.Fatalf("complete into a closed session = %d, want 409: %s", r.status, r.raw)
	}
	_, blockers, _ := errorOf(t, r)
	if len(blockers) == 0 || blockers[0] != "session_closed" {
		t.Errorf("blockers = %v, want session_closed", blockers)
	}
	// nothing moved: no invoice, no payment, the stock whole, the sale open
	if got := f.counterInvoices(t); got != 0 {
		t.Errorf("%d invoices, want 0", got)
	}
	if got := f.stock(); got != "100.0000/0.0000" {
		t.Errorf("stock = %s, want the 100 untouched", got)
	}
	if got := str(t, f.getSale(t, saleID), "status"); got != "open" {
		t.Errorf("sale status = %q, want open", got)
	}
	f.assertARInvariants(t)
}

// RULE: a void racing the close ends coherent: either the void commits
// before the close counts (the counted drawer then expects no cash for it)
// or the close commits first and the void is refused session_closed. The
// closed session never expects cash for a sale that is voided.
func TestAVoidRacingTheCloseEndsCoherent(t *testing.T) {
	testutil.LockOutboxTables(t)
	f := newFixture(t, testutil.RequireDBMaxConns(t, 4))
	sessionID := f.openTill(0)
	saleID, body := f.saleOf("1", tender("cash", 599))
	var wg sync.WaitGroup
	var voidStatus, closeStatus int
	wg.Add(2)
	go func() {
		defer wg.Done()
		voidStatus = f.do("POST", "/api/v1/pos/transactions/"+saleID+"/void",
			map[string]any{"reason": "racing the close", "revision": rev(t, body)},
			"X-Test-Role", "cashier", "X-Test-Sub", mustUUID(t)).status
	}()
	go func() {
		defer wg.Done()
		closeStatus = f.do("POST", "/api/v1/pos/till/"+sessionID+"/close", map[string]any{
			"counted_by_method": map[string]any{"cash": 0}}, "X-Test-Role", "cashier", "X-Test-Sub", mustUUID(t)).status
	}()
	wg.Wait()
	t.Logf("void %d, close %d", voidStatus, closeStatus)
	// coherent outcomes: the void won and the close counted after it (or the
	// void's transaction was the last writer before the aggregate), or the
	// close won and the void was refused
	switch {
	case voidStatus == http.StatusOK && closeStatus == http.StatusOK:
		// the void committed and the close's aggregate ran after: the session
		// expects no cash for the voided sale
		if got := f.tillExpected(t, sessionID)["CASH"]; got != 0 {
			t.Errorf("closed session expects CASH %d for a voided sale, want 0", got)
		}
	case voidStatus == http.StatusConflict && closeStatus == http.StatusOK:
		if got := str(t, f.getSale(t, saleID), "status"); got != "completed" {
			t.Errorf("sale status = %q, want completed (the void was refused)", got)
		}
	default:
		t.Errorf("void %d, close %d: an incoherent pair", voidStatus, closeStatus)
	}
	f.assertARInvariants(t)
}

// tillExpected reads a session's stored expected_by_method map.
func (f *fixture) tillExpected(t *testing.T, sessionID string) map[string]int64 {
	t.Helper()
	var expected map[string]int64
	if err := f.db.Pool.QueryRow(context.Background(),
		`SELECT expected_by_method FROM till_sessions WHERE id = $1`, sessionID).Scan(&expected); err != nil {
		t.Fatal(err)
	}
	return expected
}

// RULE (third review P3-E): the completion's FOR SHARE and the close's FOR
// UPDATE on the session row are real locks, proven deterministically: with
// the row held FOR UPDATE by another transaction, both acts block until it
// releases, then run. A completion that skipped the lock would finish beside
// the holder; so would a close that aggregated without the lock.
func TestTheCompletionAndTheCloseBlockOnTheSessionRowLock(t *testing.T) {
	testutil.LockOutboxTables(t)
	f := newFixture(t, testutil.RequireDBMaxConns(t, 4))
	ctx := context.Background()

	// the completion blocks on its FOR SHARE of the session row
	sessionID := f.openTill(0)
	saleID := f.startSale(nil)
	if r := f.addLine(saleID, f.productLine("1")); r.status != http.StatusOK {
		t.Fatalf("add line = %d: %s", r.status, r.raw)
	}
	holder, err := f.db.Pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := holder.Exec(ctx, `SELECT 1 FROM till_sessions WHERE id = $1 FOR UPDATE`, sessionID); err != nil {
		t.Fatal(err)
	}
	done := make(chan int, 1)
	go func() {
		done <- f.completeSale(saleID, tender("cash", 599)).status
	}()
	select {
	case st := <-done:
		t.Fatalf("the completion finished beside the lock holder (status %d): its FOR SHARE of the session row is missing", st)
	case <-time.After(400 * time.Millisecond):
	}
	if err := holder.Rollback(ctx); err != nil {
		t.Fatal(err)
	}
	if st := <-done; st != http.StatusOK {
		t.Fatalf("the completion after the release = %d, want 200", st)
	}
	f.assertARInvariants(t)
	if r := f.do("POST", "/api/v1/pos/till/"+sessionID+"/close", map[string]any{
		"counted_by_method": map[string]any{"cash": 599}}, "X-Test-Role", "cashier", "X-Test-Sub", mustUUID(t)); r.status != http.StatusOK {
		t.Fatalf("close the first session = %d: %s", r.status, r.raw)
	}

	// the close blocks on its FOR UPDATE of the session row
	sessionID2 := f.openTill(0)
	holder2, err := f.db.Pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := holder2.Exec(ctx, `SELECT 1 FROM till_sessions WHERE id = $1 FOR UPDATE`, sessionID2); err != nil {
		t.Fatal(err)
	}
	done2 := make(chan int, 1)
	go func() {
		done2 <- f.do("POST", "/api/v1/pos/till/"+sessionID2+"/close", map[string]any{
			"counted_by_method": map[string]any{"cash": 0}}, "X-Test-Role", "cashier", "X-Test-Sub", mustUUID(t)).status
	}()
	select {
	case st := <-done2:
		t.Fatalf("the close finished beside the lock holder (status %d): its FOR UPDATE of the session row is missing", st)
	case <-time.After(400 * time.Millisecond):
	}
	if err := holder2.Rollback(ctx); err != nil {
		t.Fatal(err)
	}
	if st := <-done2; st != http.StatusOK {
		t.Fatalf("the close after the release = %d, want 200", st)
	}
	f.assertARInvariants(t)
}
