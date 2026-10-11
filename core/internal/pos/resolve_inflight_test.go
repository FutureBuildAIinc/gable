// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package pos_test

// The claim's own terminal call (the r14 review's P1-1): a resolution that
// lands while the act's refund is still on the wire releases a tender whose
// money has not answered yet, the terminal then refunds it, and a later act
// refunds the same tender again. A live claim holds the resolve and the
// close until its call answers or its in flight lease expires.

import (
	"net/http"
	"testing"

	"github.com/gablelbm/gable/internal/payment"
	"github.com/gablelbm/gable/internal/testutil"
	"github.com/google/uuid"
)

// RULE: a no refund resolution of a claim whose own terminal call is parked
// on the wire is refused with 409 refund_in_flight; the act's own answer then
// books the money once, and a fresh card return of the same tender moves
// nothing at the terminal (one refund of one tender, ever).
func TestAResolutionCannotReleaseAClaimWhoseActIsStillAtTheTerminal(t *testing.T) {
	testutil.LockOutboxTables(t)
	gw := &fakeGateway{charges: []*payment.GatewayResult{approvedCharge()}}
	f := newFixture(t, testutil.RequireDBMaxConns(t, 4), func(f *fixture) {
		f.service = f.service.WithGateway(gw)
	})
	f.openTill(0)
	saleID, body := f.saleOf("1", withToken(tender("card", 599)))
	lineID := body.body["lines"].([]any)[0].(map[string]any)["id"].(string)
	parked, release := parkRefund(t, gw)
	done := make(chan resp, 1)
	fin := make(chan struct{})
	go func() { defer close(fin); done <- f.cardReturnOn(t, saleID, lineID, "1") }()
	t.Cleanup(func() { release(); <-fin })
	awaitParked(t, parked)
	id, _, _, _ := pendingIntent(t, f, saleID)
	r := f.resolveOn(t, id, map[string]any{
		"outcome": "no_refund", "reconciliation_reference": "R-1", "reason": "log empty"}, "finance")
	if r.status != http.StatusConflict {
		t.Fatalf("the resolve of a live claim = %d, want 409: %s", r.status, r.raw)
	}
	_, blockers, _ := errorOf(t, r)
	if len(blockers) == 0 || blockers[0] != "refund_in_flight" {
		t.Errorf("blockers = %v, want refund_in_flight (the claim's own call is on the wire)", blockers)
	}
	f.assertARInvariants(t)
	release()
	if rr := awaitResp(t, done); rr.status != http.StatusCreated {
		t.Fatalf("the parked card return = %d: %s", rr.status, rr.raw)
	}
	f.assertARInvariants(t)
	// the tender was refunded once at the terminal: a fresh card return of the
	// same unit never reaches the gateway again
	if rr := f.cardReturnOn(t, saleID, lineID, "1"); rr.status != http.StatusConflict {
		t.Fatalf("the second card return of the refunded tender = %d, want 409: %s", rr.status, rr.raw)
	}
	gw.mu.Lock()
	refunds := len(gw.refunds)
	cents := int64(0)
	for _, c := range gw.refundCents {
		cents += c
	}
	gw.mu.Unlock()
	if refunds != 1 || cents != 599 {
		t.Fatalf("the terminal refunded %d times for %d cents, want 1 for 599 (one refund of one tender)", refunds, cents)
	}
	if got := f.accountBalance("1010"); got != 0 {
		t.Errorf("card balance = %d, want 0 (599 charged, 599 refunded once)", got)
	}
	f.assertARInvariants(t)
}

// RULE: the same lease holds the drawer: while the void's own terminal call
// is on the wire the claim is not resolvable and the close refuses 409
// refund_in_flight; once the call answers the void completes and the close
// goes through.
func TestTheCloseHoldsWhileAVoidsTerminalCallIsOnTheWire(t *testing.T) {
	testutil.LockOutboxTables(t)
	gw := &fakeGateway{charges: []*payment.GatewayResult{approvedCharge()}}
	f := newFixture(t, testutil.RequireDBMaxConns(t, 4), func(f *fixture) {
		f.service = f.service.WithGateway(gw)
	})
	till := f.openTill(10000)
	saleID, body := f.saleOf("1", withToken(tender("card", 599)))
	parked, release := parkRefund(t, gw)
	done := make(chan resp, 1)
	fin := make(chan struct{})
	go func() {
		defer close(fin)
		done <- f.do("POST", "/api/v1/pos/transactions/"+saleID+"/void",
			map[string]any{"reason": "parked", "revision": rev(t, body)},
			"X-Test-Role", "cashier", "X-Test-Sub", mustUUID(t))
	}()
	t.Cleanup(func() { release(); <-fin })
	awaitParked(t, parked)
	id, _, _, _ := pendingIntent(t, f, saleID)
	if r := f.resolveOn(t, id, map[string]any{
		"outcome": "no_refund", "reconciliation_reference": "R-2", "reason": "log empty"}, "finance"); r.status != http.StatusConflict {
		t.Fatalf("the resolve of a live void claim = %d, want 409: %s", r.status, r.raw)
	}
	// the drawer cannot close over a claim whose terminal call may still move
	// money after the counting
	if r := f.do("POST", "/api/v1/pos/till/"+till+"/close",
		map[string]any{"counted_by_method": map[string]any{"cash": 10000}},
		"X-Test-Role", "cashier", "X-Test-Sub", mustUUID(t)); r.status != http.StatusConflict {
		t.Fatalf("the close under a live void claim = %d, want 409: %s", r.status, r.raw)
	}
	_, blockers, _ := errorOf(t, f.do("POST", "/api/v1/pos/till/"+till+"/close",
		map[string]any{"counted_by_method": map[string]any{"cash": 10000}},
		"X-Test-Role", "cashier", "X-Test-Sub", mustUUID(t)))
	if len(blockers) == 0 || blockers[0] != "refund_in_flight" {
		t.Errorf("blockers = %v, want refund_in_flight", blockers)
	}
	f.assertARInvariants(t)
	release()
	if rr := awaitResp(t, done); rr.status != http.StatusOK {
		t.Fatalf("the parked void = %d: %s", rr.status, rr.raw)
	}
	if got := str(t, f.getSale(t, saleID), "status"); got != "voided" {
		t.Errorf("sale status = %q, want voided (the void's own flow completed)", got)
	}
	if r := f.do("POST", "/api/v1/pos/till/"+till+"/close",
		map[string]any{"counted_by_method": map[string]any{"cash": 10000}},
		"X-Test-Role", "cashier", "X-Test-Sub", mustUUID(t)); r.status != http.StatusOK {
		t.Fatalf("the close after the void completed = %d: %s", r.status, r.raw)
	}
	gw.mu.Lock()
	refunds := len(gw.refunds)
	gw.mu.Unlock()
	if refunds != 1 {
		t.Fatalf("%d gateway refunds, want 1", refunds)
	}
	f.assertARInvariants(t)
}

// RULE: once the in flight lease expires with no answer (a crashed act whose
// terminal call never returned), the claim is resolvable again and records
// that the terminal never answered: the claim is marked UNKNOWN and settles
// as the operator recorded.
func TestAnUnansweredClaimBecomesResolvableWhenItsLeaseExpires(t *testing.T) {
	testutil.LockOutboxTables(t)
	gw := &fakeGateway{charges: []*payment.GatewayResult{approvedCharge()}}
	f := newFixture(t, testutil.RequireDBMaxConns(t, 4), func(f *fixture) {
		f.service = f.service.WithGateway(gw)
	})
	f.openTill(0)
	saleID, _ := f.saleOf("1", withToken(tender("card", 599)))
	// what a crashed act leaves: the PENDING claim, its call never returned,
	// nothing booked and no answer stamped
	mustExec(t, f.db, `
		INSERT INTO pos_refund_intents (id, sale_id, tender_id, gateway_tx_id, amount, act, state, request)
		SELECT $1, t.id, d.id, d.gateway_tx_id, d.amount, 'return', 'PENDING', '{}'::jsonb
		FROM pos_transactions t JOIN pos_tenders d ON d.transaction_id = t.id
		WHERE t.id = $2 AND d.method = 'CARD'`, uuid.New(), saleID)
	id, state, refundTxID, _ := pendingIntent(t, f, saleID)
	if state != "PENDING" || refundTxID != "" {
		t.Fatalf("the crashed claim = (%s, %q), want PENDING with no refund id", state, refundTxID)
	}
	// the lease has not expired: the resolve is refused
	if r := f.resolveOn(t, id, map[string]any{
		"outcome": "no_refund", "reconciliation_reference": "R-3", "reason": "log empty"}, "finance"); r.status != http.StatusConflict {
		t.Fatalf("the resolve inside the lease = %d, want 409: %s", r.status, r.raw)
	}
	// the lease expires with the terminal never answering
	mustExec(t, f.db, `UPDATE pos_refund_intents SET answer_due_at = NOW() - INTERVAL '1 second' WHERE id = $1`, id)
	if r := f.resolveOn(t, id, map[string]any{
		"outcome": "no_refund", "reconciliation_reference": "R-3", "reason": "log empty"}, "finance"); r.status != http.StatusOK {
		t.Fatalf("the resolve after the lease = %d, want 200: %s", r.status, r.raw)
	}
	if got := countOf(t, f.db, `SELECT count(*) FROM pos_refund_intents WHERE id = $1 AND state = 'FAILED' AND answer_state = 'UNKNOWN'`, id); got != 1 {
		t.Errorf("the expired claim is not marked UNKNOWN: %d rows", got)
	}
	f.assertARInvariants(t)
}
