// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package pos_test

// The refund intent (ADR 0005 section 11: "The card gateway is called
// before the transaction and its result written inside it", and section
// 14.2 C2-5: "each card payment, whose gateway void or refund is made
// before the transaction (as every gateway call is)"). Because the gateway
// sits outside the money transaction, no row lock can span the call, so a
// committed intent row, unique per sale and tender while pending, is what
// serializes the card refunds of a tender: a second void or card return is
// refused 409 refund_in_flight before it reaches the terminal; the money
// transaction marks the intent done as it posts; a gateway failure releases
// it having moved no money; a refund whose transaction refused leaves it
// standing beside the orphaned refund row.

import (
	"context"
	"fmt"
	"net/http"
	"sync"
	"testing"

	"github.com/gablelbm/gable/internal/payment"
	"github.com/gablelbm/gable/internal/testutil"
)

// RULE (sixth review P2-1, the lead's decision): three concurrent card
// returns of the one unit a card sale sold make exactly one gateway refund:
// the winner's committed intent refuses the losers before the terminal, so
// the card is never refunded twice for one unit.
func TestThreeConcurrentCardReturnsMakeOneGatewayRefund(t *testing.T) {
	testutil.LockOutboxTables(t)
	gw := &fakeGateway{charges: []*payment.GatewayResult{approvedCharge()}}
	f := newFixture(t, testutil.RequireDBMaxConns(t, 4), func(f *fixture) {
		f.service = f.service.WithGateway(gw)
	})
	saleID, body := f.saleOf("1", withToken(tender("card", 599)))
	lineID := body.body["lines"].([]any)[0].(map[string]any)["id"].(string)
	var mu sync.Mutex
	codes := make([]int, 3)
	blockersOf := make([]string, 3)
	raws := make([]string, 3)
	var wg sync.WaitGroup
	for i := 0; i < 3; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			r := f.cardReturnOn(t, saleID, lineID, "1")
			mu.Lock()
			defer mu.Unlock()
			codes[i] = r.status
			raws[i] = string(r.raw)
			if r.status != http.StatusCreated {
				_, blockers, _ := errorOf(t, r)
				if len(blockers) > 0 {
					blockersOf[i] = blockers[0]
				}
			}
		}(i)
	}
	wg.Wait()
	wins, refused := 0, 0
	for _, st := range codes {
		switch st {
		case http.StatusCreated:
			wins++
		case http.StatusConflict:
			refused++
		}
	}
	if wins != 1 || refused != 2 {
		t.Fatalf("card return codes = %v, want one 201 and two 409", codes)
	}
	gw.mu.Lock()
	refunds := len(gw.refunds)
	gw.mu.Unlock()
	if refunds != 1 {
		t.Fatalf("%d gateway refunds for one returned unit, want exactly 1", refunds)
	}
	for i, st := range codes {
		if st == http.StatusCreated {
			continue
		}
		// both refusals happen before the terminal: the loser either met the
		// winner's standing claim (refund_in_flight) or rechecked the caps
		// under the claim's sale lock after the winner committed
		// (exceeds_sold)
		if blockersOf[i] != "refund_in_flight" && blockersOf[i] != "exceeds_sold" {
			t.Errorf("loser %d blocker = %q, want refund_in_flight or exceeds_sold (refused before the terminal): %s", i, blockersOf[i], raws[i])
		}
	}
	if got := countOf(t, f.db, `SELECT count(*) FROM audit_log WHERE action = 'pos.gateway_refund_orphaned' AND entity_id = $1`, saleID); got != 0 {
		t.Errorf("%d orphaned refund rows, want 0 (no refund was made without its return)", got)
	}
	if got := f.accountBalance("1010"); got != 0 {
		t.Errorf("card balance = %d, want 0 (the charge refunded once)", got)
	}
	if got := f.stock(); got != "99.0000/0.0000" {
		t.Errorf("stock = %s, want 99 (the card return restocks nothing)", got)
	}
	f.assertARInvariants(t)
}

// RULE (sixth review P2-1): a void racing a card return of the same tender
// makes exactly one gateway refund: whichever act commits the intent first
// owns the terminal, and the other is refused refund_in_flight before it
// reaches it. Here the card return is parked at the gateway with its intent
// committed, and the void runs against it.
func TestAVoidRacingACardReturnMakesOneGatewayRefund(t *testing.T) {
	testutil.LockOutboxTables(t)
	gw := &fakeGateway{charges: []*payment.GatewayResult{approvedCharge()}}
	parked := make(chan struct{})
	release := make(chan struct{})
	gw.onRefund = func() {
		parked <- struct{}{}
		<-release
	}
	f := newFixture(t, testutil.RequireDBMaxConns(t, 4), func(f *fixture) {
		f.service = f.service.WithGateway(gw)
	})
	saleID, body := f.saleOf("1", withToken(tender("card", 599)))
	lineID := body.body["lines"].([]any)[0].(map[string]any)["id"].(string)
	returnDone := make(chan resp, 1)
	go func() {
		returnDone <- f.cardReturnOn(t, saleID, lineID, "1")
	}()
	<-parked
	// the return's refund is in flight with its intent committed: the void
	// of the same sale is refused before the terminal
	r := f.do("POST", "/api/v1/pos/transactions/"+saleID+"/void",
		map[string]any{"reason": "racing a card return", "revision": rev(t, body)},
		"X-Test-Role", "cashier", "X-Test-Sub", mustUUID(t))
	if r.status != http.StatusConflict {
		t.Fatalf("the void racing a card return = %d, want 409: %s", r.status, r.raw)
	}
	_, blockers, _ := errorOf(t, r)
	if len(blockers) == 0 || blockers[0] != "refund_in_flight" {
		t.Errorf("blockers = %v, want refund_in_flight (the return's intent refused the void)", blockers)
	}
	close(release)
	if rr := <-returnDone; rr.status != http.StatusCreated {
		t.Fatalf("the parked card return = %d: %s", rr.status, rr.raw)
	}
	gw.mu.Lock()
	refunds := len(gw.refunds)
	gw.mu.Unlock()
	if refunds != 1 {
		t.Fatalf("%d gateway refunds for one unit refunded and a void racing it, want 1", refunds)
	}
	if got := str(t, f.getSale(t, saleID), "status"); got != "completed" {
		t.Errorf("sale status = %q, want completed (the void was refused)", got)
	}
	if got := f.accountBalance("1010"); got != 0 {
		t.Errorf("card balance = %d, want 0 (599 charged, 599 refunded once)", got)
	}
	f.assertARInvariants(t)
}

// RULE (sixth review P2-3, the lead's decision): no lock of the money
// transaction is held across a gateway call: while the terminal is on the
// wire, the sale row and its session row are free (a NOWAIT lock on either
// succeeds), for the void's refund and the return's alike.
func TestTheGatewayCallRunsOutsideEveryTransaction(t *testing.T) {
	testutil.LockOutboxTables(t)
	gw := &fakeGateway{charges: []*payment.GatewayResult{approvedCharge(), approvedCharge()}}
	f := newFixture(t, testutil.RequireDBMaxConns(t, 4), func(f *fixture) {
		f.service = f.service.WithGateway(gw)
	})
	f.openTill(0)
	ctx := context.Background()
	probe := func(saleID, sessionID string) func() {
		return func() {
			for _, row := range []struct{ what, sql, id string }{
				{"the sale row", `SELECT 1 FROM pos_transactions WHERE id = $1 FOR UPDATE NOWAIT`, saleID},
				{"the session row", `SELECT 1 FROM till_sessions WHERE id = $1 FOR UPDATE NOWAIT`, sessionID},
			} {
				conn, err := f.db.Pool.Acquire(ctx)
				if err != nil {
					t.Errorf("probe connection: %v", err)
					return
				}
				if _, err := conn.Exec(ctx, row.sql, row.id); err != nil {
					t.Errorf("while the gateway was called, %s was locked: %v (ADR 0005 section 11 keeps the call outside the transaction)", row.what, err)
				}
				conn.Release()
			}
		}
	}
	// the void's refund
	saleID, body := f.saleOf("1", withToken(tender("card", 599)))
	sessionID := f.currentTillID(t)
	gw.onRefund = probe(saleID, sessionID)
	if r := f.do("POST", "/api/v1/pos/transactions/"+saleID+"/void",
		map[string]any{"reason": "probe", "revision": rev(t, body)},
		"X-Test-Role", "cashier", "X-Test-Sub", mustUUID(t)); r.status != http.StatusOK {
		t.Fatalf("the probed void = %d: %s", r.status, r.raw)
	}
	// the card return's refund
	saleID2, body2 := f.saleOf("1", withToken(tender("card", 599)))
	lineID := body2.body["lines"].([]any)[0].(map[string]any)["id"].(string)
	gw.onRefund = probe(saleID2, sessionID)
	if r := f.cardReturnOn(t, saleID2, lineID, "1"); r.status != http.StatusCreated {
		t.Fatalf("the probed card return = %d: %s", r.status, r.raw)
	}
	f.assertARInvariants(t)
}

// RULE (the lead's decision): a gateway failure releases the intent having
// moved no money: the tender may be refunded again, and the retry's own
// intent is the one that completes.
func TestAFailedCardRefundReleasesTheIntentAndMovesNoMoney(t *testing.T) {
	testutil.LockOutboxTables(t)
	gw := &fakeGateway{charges: []*payment.GatewayResult{approvedCharge()},
		refundErrs: []error{fmt.Errorf("terminal offline")}}
	f := newFixture(t, testutil.RequireDB(t), func(f *fixture) {
		f.service = f.service.WithGateway(gw)
	})
	saleID, body := f.saleOf("1", withToken(tender("card", 599)))
	lineID := body.body["lines"].([]any)[0].(map[string]any)["id"].(string)
	r := f.cardReturnOn(t, saleID, lineID, "1")
	if r.status != http.StatusInternalServerError {
		t.Fatalf("the card return under a failed refund = %d, want 500: %s", r.status, r.raw)
	}
	if got := countOf(t, f.db, `SELECT count(*) FROM pos_refund_intents WHERE sale_id = $1 AND state = 'FAILED'`, saleID); got != 1 {
		t.Errorf("%d FAILED refund intents, want 1 (the failed call released its claim)", got)
	}
	if got := countOf(t, f.db, `SELECT count(*) FROM pos_refund_intents WHERE sale_id = $1 AND state = 'PENDING'`, saleID); got != 0 {
		t.Errorf("%d PENDING refund intents after the failure, want 0", got)
	}
	if got := countOf(t, f.db, `SELECT count(*) FROM audit_log WHERE action = 'pos.gateway_refund_orphaned' AND entity_id = $1`, saleID); got != 0 {
		t.Errorf("%d orphaned refund rows, want 0 (no money moved)", got)
	}
	if got := f.accountBalance("1010"); got != 599 {
		t.Errorf("card balance = %d, want 599 (the charge stands, nothing refunded)", got)
	}
	f.assertARInvariants(t)
	// the retry goes through: the failed intent no longer blocks the tender
	if r := f.cardReturnOn(t, saleID, lineID, "1"); r.status != http.StatusCreated {
		t.Fatalf("the retried card return = %d: %s", r.status, r.raw)
	}
	if got := countOf(t, f.db, `SELECT count(*) FROM pos_refund_intents WHERE sale_id = $1 AND state = 'DONE'`, saleID); got != 1 {
		t.Errorf("%d DONE refund intents, want 1 (the retry's claim completed with its posting)", got)
	}
	if got := f.accountBalance("1010"); got != 0 {
		t.Errorf("card balance = %d, want 0 after the retry (refunded once)", got)
	}
	f.assertARInvariants(t)
}

// RULE (the lead's decision): a gateway success whose money transaction
// refuses leaves the intent standing and the refund recorded, and the
// standing intent refuses the next card refund of the tender until the
// money is reconciled.
func TestARefusedCardReturnLeavesItsIntentRefusingTheNext(t *testing.T) {
	testutil.LockOutboxTables(t)
	gw := &fakeGateway{charges: []*payment.GatewayResult{approvedCharge()}}
	f := newFixture(t, testutil.RequireDB(t), func(f *fixture) {
		f.service = f.service.WithGateway(gw)
		f.events.fail = "pos_return.completed"
	})
	f.openTill(0)
	saleID, body := f.saleOf("4", withToken(tender("card", 2395)))
	lineID := body.body["lines"].([]any)[0].(map[string]any)["id"].(string)
	r := f.cardReturnOn(t, saleID, lineID, "1")
	if r.status == http.StatusCreated {
		t.Fatalf("the return committed under a failing event write: %s", r.raw)
	}
	if got := countOf(t, f.db, `SELECT count(*) FROM pos_refund_intents WHERE sale_id = $1 AND state = 'PENDING'`, saleID); got != 1 {
		t.Fatalf("%d PENDING refund intents after the refused transaction, want 1 (the claim records the refund)", got)
	}
	if got := countOf(t, f.db, `SELECT count(*) FROM audit_log WHERE action = 'pos.gateway_refund_orphaned' AND entity_id = $1`, saleID); got != 1 {
		t.Errorf("%d orphaned refund rows, want 1", got)
	}
	// the standing intent refuses the next card return of the tender before
	// the terminal
	r = f.cardReturnOn(t, saleID, lineID, "1")
	if r.status != http.StatusConflict {
		t.Fatalf("the next card return = %d, want 409 (the standing intent refuses it): %s", r.status, r.raw)
	}
	_, blockers, _ := errorOf(t, r)
	if len(blockers) == 0 || blockers[0] != "refund_in_flight" {
		t.Errorf("blockers = %v, want refund_in_flight", blockers)
	}
	gw.mu.Lock()
	refunds := len(gw.refunds)
	gw.mu.Unlock()
	if refunds != 1 {
		t.Fatalf("%d gateway refunds, want 1 (the refusal never reached the terminal)", refunds)
	}
	f.assertARInvariants(t)
}
