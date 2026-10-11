// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package pos_test

// The round 4 claims the probes left unproved (ninth review P2-3 and P3-1):
// the figures the claim restates under its lock are the figures the terminal
// is asked for and the books take; a remainder that moves between the claim
// and the money transaction refuses the booking (refund_amount_changed) with
// the claim standing; and the void leg's resolution writes its audit row
// inside the booking, so a rolled back replay leaves none.

import (
	"context"
	"net/http"
	"testing"

	"github.com/gablelbm/gable/internal/payment"
	"github.com/gablelbm/gable/internal/testutil"
	"github.com/google/uuid"
)

// RULE (ninth review P3-1, the restate): after the drawer returns one unit
// (599 of the 2393), the card return of the other three claims, is asked for
// and books exactly the restated remainder 1794 (1648 of line and 146 of
// tax), never the naive rounded share 1795.
func TestTheCardClaimIsAskedForAndBooksTheRestatedRemainder(t *testing.T) {
	testutil.LockOutboxTables(t)
	gw := &fakeGateway{charges: []*payment.GatewayResult{approvedCharge()}}
	f := newFixture(t, testutil.RequireDB(t), func(f *fixture) {
		f.service = f.service.WithGateway(gw)
	})
	f.openTill(0)
	saleID := f.startSale(nil)
	if r := f.addLine(saleID, map[string]any{"product_id": f.productID.String(), "quantity": "4",
		"discount_cents": 2, "discount_reason": "two off"}); r.status != http.StatusOK {
		t.Fatalf("add line = %d: %s", r.status, r.raw)
	}
	if r := f.completeSale(saleID, withToken(tender("card", 2393))); r.status != http.StatusOK {
		t.Fatalf("complete = %d: %s", r.status, r.raw)
	}
	lineID := f.getSale(t, saleID)["lines"].([]any)[0].(map[string]any)["id"].(string)
	if r := f.returnOn(t, saleID, lineID, "1"); r.status != http.StatusCreated {
		t.Fatalf("the cash return of one unit = %d: %s", r.status, r.raw)
	}
	r := f.cardReturnOn(t, saleID, lineID, "3")
	if r.status != http.StatusCreated {
		t.Fatalf("the card return of three units = %d: %s", r.status, r.raw)
	}
	gw.mu.Lock()
	cents := gw.refundCents[0]
	gw.mu.Unlock()
	if cents != 1794 {
		t.Errorf("the terminal was asked for %d cents, want 1794 (the restated remainder, not the naive 1795)", cents)
	}
	if got := num(t, r.body, "total_cents"); got != -1794 {
		t.Errorf("the booked return total = %d, want -1794", got)
	}
	if got := f.scalar(`SELECT ROUND(amount * 100)::bigint FROM pos_refund_intents WHERE sale_id = $1 AND act = 'return'`, saleID).(int64); got != 1794 {
		t.Errorf("the claim holds %d cents, want 1794", got)
	}
	if got := f.accountBalance("1010"); got != 0 {
		t.Errorf("card balance = %d, want 0 (2393 charged, 1794 to the card, 599 from the drawer)", got)
	}
	f.assertARInvariants(t)
}

// RULE (ninth review P3-1, refund_amount_changed reached): a remainder that
// moves between the claim and the money transaction refuses the booking: the
// books never record a cent less than the terminal moved, the claim stands
// PENDING beside the orphan row, and the operator resolution's replay refuses
// the same way. The competing movement is written in the parked window the
// way the suite's crash proofs work: a stand-in row through the real table
// the rule reads (an unlinked return line of the sale), standing for the
// concurrent act the intent table now refuses.
func TestAMovedRemainderRefusesTheBookingAndKeepsTheClaim(t *testing.T) {
	testutil.LockOutboxTables(t)
	gw := &fakeGateway{charges: []*payment.GatewayResult{approvedCharge()}}
	f := newFixture(t, testutil.RequireDBMaxConns(t, 4), func(f *fixture) {
		f.service = f.service.WithGateway(gw)
	})
	f.openTill(0)
	saleID := f.startSale(nil)
	if r := f.addLine(saleID, map[string]any{"product_id": f.productID.String(), "quantity": "4",
		"discount_cents": 2, "discount_reason": "two off"}); r.status != http.StatusOK {
		t.Fatalf("add line = %d: %s", r.status, r.raw)
	}
	if r := f.completeSale(saleID, withToken(tender("card", 2393))); r.status != http.StatusOK {
		t.Fatalf("complete = %d: %s", r.status, r.raw)
	}
	lineID := f.getSale(t, saleID)["lines"].([]any)[0].(map[string]any)["id"].(string)

	parked, release := parkRefund(t, gw)
	returnDone := make(chan resp, 1)
	fin := make(chan struct{})
	go func() { defer close(fin); returnDone <- f.cardReturnOn(t, saleID, lineID, "3") }()
	t.Cleanup(func() { release(); <-fin })
	awaitParked(t, parked)
	// the concurrent act the window allows: an unlinked return line of the
	// sale, the shape a legacy route or a direct write leaves
	insertUnlinkedReturnLine(t, f, saleID, 599)
	release()
	rr := awaitResp(t, returnDone)
	if rr.status != http.StatusConflict {
		t.Fatalf("the card return under a moved remainder = %d, want 409: %s", rr.status, rr.raw)
	}
	_, blockers, _ := errorOf(t, rr)
	if len(blockers) == 0 || blockers[0] != "refund_amount_changed" {
		t.Fatalf("blockers = %v, want refund_amount_changed (the terminal took 1795, the sale now restates 1196)", blockers)
	}
	intentID, state, refundTxID, _ := pendingIntent(t, f, saleID)
	if state != "PENDING" || refundTxID == "" {
		t.Fatalf("the claim = (%s, %q), want PENDING carrying the refund id", state, refundTxID)
	}
	if got := countOf(t, f.db, `SELECT count(*) FROM audit_log WHERE action = 'pos.gateway_refund_orphaned' AND entity_id = $1`, saleID); got != 1 {
		t.Errorf("%d orphaned refund rows, want 1 recording the money that moved", got)
	}
	if got := f.accountBalance("1010"); got != 2393 {
		t.Errorf("card balance = %d, want 2393 (the charge stands: nothing booked under a moved remainder)", got)
	}
	f.assertARInvariants(t)
	// the resolution's replay refuses the same way: the money moved, the
	// books cannot take it under figures that no longer hold
	r := f.resolveOn(t, intentID, map[string]any{
		"outcome": "refund", "refund_tx_id": refundTxID, "reason": "the terminal refunded 1795",
	}, "finance")
	if r.status != http.StatusConflict {
		t.Fatalf("the resolution of the moved remainder = %d, want 409: %s", r.status, r.raw)
	}
	_, blockers, _ = errorOf(t, r)
	if len(blockers) == 0 || blockers[0] != "refund_amount_changed" {
		t.Errorf("blockers = %v, want refund_amount_changed on the replay too", blockers)
	}
	if got := countOf(t, f.db, `SELECT count(*) FROM pos_refund_intents WHERE id = $1 AND state = 'PENDING'`, intentID); got != 1 {
		t.Errorf("the replay settled the claim it could not book")
	}
	f.assertARInvariants(t)
}

// insertUnlinkedReturnLine writes one unlinked (legacy shaped) return row of
// the sale through the real tables the remainder rule reads: the stand-in for
// a concurrent act landing in the parked window, the same standing-in the
// suite's failing event writes do for crashes.
func insertUnlinkedReturnLine(t *testing.T, f *fixture, saleID string, cents int64) {
	t.Helper()
	returnID := uuid.New()
	ctx := context.Background()
	if _, err := f.db.Pool.Exec(ctx, `INSERT INTO pos_returns (id, number, register_id, original_transaction_id, customer_id, branch_id,
		cashier_id, currency, subtotal, tax_amount, total, refund_method, reason, status, created_at, updated_at, revision)
		VALUES ($1, 'RTN-LEGACY', $2, $3, $4, NULL, $5, 'USD', 0, 0, $6::bigint::numeric / 100, 'CASH', 'stand-in', 'COMPLETED', NOW(), NOW(), 1)`,
		returnID, f.register, saleID, f.customerID, mustUUID(t), -cents); err != nil {
		t.Fatalf("insert the stand-in return: %v", err)
	}
	if _, err := f.db.Pool.Exec(ctx, `INSERT INTO pos_return_lines (return_id, position, line_type, product_id, description, quantity, uom,
		unit_price, line_total, restock, created_at)
		VALUES ($1, 0, 'PRODUCT', $2, 'stand-in', -1, 'EA', 0, $3::bigint::numeric / 100, FALSE, NOW())`,
		returnID, f.productID, -cents); err != nil {
		t.Fatalf("insert the stand-in return line: %v", err)
	}
}

// RULE (ninth review P2-3, the void leg's audit row): a void claim's refund
// resolution writes its pos.refund_intent.resolved row inside the booking
// transaction, so a replay that rolls back leaves none and a booking leaves
// exactly one.
func TestTheVoidResolutionsAuditRowRollsBackWithItsBooking(t *testing.T) {
	testutil.LockOutboxTables(t)
	gw := &fakeGateway{charges: []*payment.GatewayResult{approvedCharge()}}
	f := newFixture(t, testutil.RequireDB(t), func(f *fixture) {
		f.service = f.service.WithGateway(gw)
		f.events.fail = "pos_transaction.voided"
	})
	f.openTill(0)
	saleID, body := f.saleOf("1", withToken(tender("card", 599)))
	if r := f.do("POST", "/api/v1/pos/transactions/"+saleID+"/void",
		map[string]any{"reason": "stranded", "revision": rev(t, body)},
		"X-Test-Role", "cashier", "X-Test-Sub", mustUUID(t)); r.status == http.StatusOK {
		t.Fatalf("the void committed under a failing event write: %s", r.raw)
	}
	intentID, state, refundTxID, _ := pendingIntent(t, f, saleID)
	if state != "PENDING" || refundTxID == "" {
		t.Fatalf("the stranded void's claim = (%s, %q)", state, refundTxID)
	}
	// the replay still fails (the event write still fails): no audit row
	if r := f.resolveOn(t, intentID, map[string]any{
		"outcome": "refund", "refund_tx_id": refundTxID, "reason": "rolled back replay",
	}, "finance"); r.status == http.StatusOK {
		t.Fatalf("the replay committed under the failing event write: %s", r.raw)
	}
	if got := countOf(t, f.db, `SELECT count(*) FROM audit_log WHERE action = 'pos.refund_intent.resolved' AND changes->>'intent_id' = $1`, intentID); got != 0 {
		t.Errorf("%d resolution audit rows after a rolled back replay, want 0 (the row rides the booking's transaction)", got)
	}
	// the failure gone, the booking writes exactly one
	f.events.fail = ""
	if r := f.resolveOn(t, intentID, map[string]any{
		"outcome": "refund", "refund_tx_id": refundTxID, "reason": "the terminal refunded",
	}, "finance"); r.status != http.StatusOK {
		t.Fatalf("the resolution = %d, want 200: %s", r.status, r.raw)
	}
	if got := countOf(t, f.db, `SELECT count(*) FROM audit_log WHERE action = 'pos.refund_intent.resolved'
		AND changes->>'intent_id' = $1 AND changes->>'outcome' = 'refund' AND changes->>'refund_tx_id' = $2`, intentID, refundTxID); got != 1 {
		t.Errorf("%d resolution audit rows naming the intent, the outcome and the id, want 1", got)
	}
	if got := str(t, f.getSale(t, saleID), "status"); got != "voided" {
		t.Errorf("sale status = %q, want voided", got)
	}
	f.assertARInvariants(t)
}
