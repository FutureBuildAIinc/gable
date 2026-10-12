// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package pos_test

// The declined void tender's credit and its one way back
// (FROM-PRINCIPAL 60b): a tender whose refund the terminal declined keeps
// its charge as unapplied credit on the customer (held to its tender,
// round 9's apply refusal), and it is paid back only to that card through
// the counter terminal rail, by the small payback route beside the resolve
// route. The payment refund route through the online gateway is never the
// way back for a counter tender: that rail is not the terminal the charge
// came from, and the route is refused for such a credit. The walk-in sale
// is the sharpest case (the credit lands on the shared walk-in customer),
// so the proof runs there.

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/gablelbm/gable/internal/payment"
	"github.com/gablelbm/gable/internal/testutil"
	"github.com/google/uuid"
)

// declinedCreditVoid completes a walk-in two card sale whose void refunded
// one card and declined the other: the standing claim is resolved, the void
// completes, and the declined tender's payment holds its charge as the
// credit. It answers the sale id, the declined claim and the credit cents.
func declinedCreditVoid(t *testing.T, f *fixture, gw *fakeGateway, resolveSub string) (string, map[string]any, int64) {
	t.Helper()
	saleID, body, _ := f.twoCardSaleOf(t, "1", 300, 299)
	if r := f.do("POST", "/api/v1/pos/transactions/"+saleID+"/void",
		map[string]any{"reason": "declined card", "revision": rev(t, body)},
		"X-Test-Role", "cashier", "X-Test-Sub", mustUUID(t)); r.status != http.StatusConflict {
		t.Fatalf("the void with one tender declined = %d, want 409: %s", r.status, r.raw)
	}
	var standing, declined map[string]any
	for _, in := range f.saleIntents(t, saleID) {
		if in["state"] == "PENDING" {
			standing = in
		}
		if in["state"] == "FAILED" {
			declined = in
		}
	}
	if standing == nil || declined == nil {
		t.Fatalf("the claims = %v, want one PENDING and one FAILED", f.saleIntents(t, saleID))
	}
	if r := f.resolveOnSub(t, standing["id"].(string), map[string]any{
		"outcome": "refund", "refund_tx_id": standing["refund_tx_id"].(string), "reason": "the terminal refunded this card",
	}, "finance", resolveSub); r.status != http.StatusOK {
		t.Fatalf("the resolution of the refunded tender = %d, want 200: %s", r.status, r.raw)
	}
	if got := str(t, f.getSale(t, saleID), "status"); got != "voided" {
		t.Fatalf("sale status = %q, want voided", got)
	}
	credit := f.unappliedOf(t, declined["tender_id"].(string))
	if credit != declined["amount"].(int64) {
		t.Fatalf("the declined tender's payment holds %d unapplied cents, want %d (the credit owed)", credit, declined["amount"].(int64))
	}
	f.assertARInvariants(t)
	return saleID, declined, credit
}

// gatewayRefundCalls reads the stub terminal's refund call count.
func gatewayRefundCalls(t *testing.T, gw *fakeGateway) int {
	t.Helper()
	gw.mu.Lock()
	defer gw.mu.Unlock()
	return len(gw.refunds)
}

// RULE (FROM-PRINCIPAL 60b): the declined tender's credit is paid back to
// its card through the counter terminal rail alone, by the payback route
// beside the resolve route (finance and owner only): the terminal is asked
// once for the credit, the credit's unapplied cash falls to zero, the cash
// account moves by the refund, and a second payback answers 409 without
// reaching the terminal. The payment refund route through the online
// gateway is refused for such a credit (the wrong rail), red first.
func TestADeclinedVoidTendersCreditIsPaidBackThroughTheCounterTerminal(t *testing.T) {
	testutil.LockOutboxTables(t)
	gw := &fakeGateway{charges: []*payment.GatewayResult{approvedCharge(), approvedCharge()},
		refundStatuses: []payment.GatewayStatus{payment.GatewayStatusRefunded, payment.GatewayStatusDeclined}}
	f := newFixture(t, testutil.RequireDBMaxConns(t, 4), func(f *fixture) {
		f.service = f.service.WithGateway(gw)
		f.paymentGateway = gw
	})
	f.openTill(10000)
	_, declined, credit := declinedCreditVoid(t, f, gw, mustUUID(t))
	callsAfterVoid := gatewayRefundCalls(t, gw)
	if callsAfterVoid != 2 {
		t.Fatalf("%d terminal refund calls after the void, want 2 (one accepted, one declined)", callsAfterVoid)
	}
	payID, payRev := f.paymentOfTender(t, declined["tender_id"].(string))
	card1010 := f.accountBalance("1010")

	// the online gateway's payment refund route is never the way back for a
	// counter tender: refused for the credit, and the online rail is never
	// asked (the stub counts every refund call of both rails here, so the
	// count proves no third call happened)
	r := f.do("POST", "/api/v1/payments/"+payID+"/refunds", map[string]any{
		"amount_cents": credit, "reason": "the declined tender's credit back to its card", "revision": payRev,
	}, "X-Test-Role", "finance", "X-Test-Sub", mustUUID(t))
	if r.status != http.StatusConflict {
		t.Fatalf("the payment refund route on the held credit = %d, want 409: %s", r.status, r.raw)
	}
	_, blockers, _ := errorOf(t, r)
	if len(blockers) == 0 || blockers[0] != "tender_credit_held" {
		t.Errorf("blockers = %v, want tender_credit_held (a counter tender's credit rides the terminal rail, never the online gateway)", blockers)
	}
	if got := gatewayRefundCalls(t, gw); got != callsAfterVoid {
		t.Errorf("%d refund calls after the refused online refund, want %d (the online rail was never asked)", got, callsAfterVoid)
	}
	if got := f.unappliedOf(t, declined["tender_id"].(string)); got != credit {
		t.Errorf("the credit after the refused online refund = %d, want %d", got, credit)
	}
	f.assertARInvariants(t)

	// the way back: the counter terminal rail, through the payback route,
	// finance only (a cashier is refused)
	if r := f.do("POST", "/api/v1/pos/refund-intents/"+declined["id"].(string)+"/payback",
		map[string]any{"reason": "the customer is at the counter for their card"},
		"X-Test-Role", "cashier", "X-Test-Sub", mustUUID(t)); r.status != http.StatusForbidden {
		t.Fatalf("a cashier's payback = %d, want 403: %s", r.status, r.raw)
	}
	r = f.do("POST", "/api/v1/pos/refund-intents/"+declined["id"].(string)+"/payback",
		map[string]any{"reason": "the customer is at the counter for their card"},
		"X-Test-Role", "finance", "X-Test-Sub", mustUUID(t))
	if r.status != http.StatusOK {
		t.Fatalf("the payback = %d, want 200: %s", r.status, r.raw)
	}
	// the terminal was asked exactly once for the credit, and the books took it
	if got := gatewayRefundCalls(t, gw); got != callsAfterVoid+1 {
		t.Errorf("%d terminal refund calls after the payback, want %d (one payback call)", got, callsAfterVoid+1)
	}
	if got := f.unappliedOf(t, declined["tender_id"].(string)); got != 0 {
		t.Errorf("the credit after the payback = %d, want 0 (paid back to the card)", got)
	}
	if got := f.accountBalance("1010"); got != card1010-credit {
		t.Errorf("card balance = %d, want %d (the cash account moved by the refund)", got, card1010-credit)
	}
	if got := f.walkInBalance(t); got != 0 {
		t.Errorf("walk-in balance = %d, want 0 (nothing owed, nothing held on the customer)", got)
	}
	if got := countOf(t, f.db, `SELECT count(*) FROM payment_refunds`); got != 2 {
		t.Errorf("%d refund rows, want 2 (the void's booked refund and the credit's payback)", got)
	}
	// the terminal's outcome is recorded the way the resolve route records
	// it: one audit row naming the person, the intent, the outcome and the
	// reason
	if got := countOf(t, f.db, `SELECT count(*) FROM audit_log WHERE action = 'pos.refund_intent.paid_back'
		AND changes->>'intent_id' = $1 AND changes->>'tender_id' = $2 AND changes->>'outcome' = 'refund'
		AND changes->>'reason' = 'the customer is at the counter for their card'`, declined["id"], declined["tender_id"]); got != 1 {
		t.Errorf("%d payback audit rows naming the intent, the tender, the outcome and the reason, want 1", got)
	}
	// the payback's own claim completes DONE carrying the terminal's refund
	// id, beside the void's FAILED claim (the void's own record)
	if got := countOf(t, f.db, `SELECT count(*) FROM pos_refund_intents WHERE act = 'payback' AND state = 'DONE'
		AND tender_id = $1 AND refund_tx_id IS NOT NULL`, declined["tender_id"]); got != 1 {
		t.Errorf("%d DONE payback claims of the tender, want 1", got)
	}
	f.assertARInvariants(t)

	// exactly one resolution per tender: a second payback answers 409 and
	// never reaches the terminal
	r = f.do("POST", "/api/v1/pos/refund-intents/"+declined["id"].(string)+"/payback",
		map[string]any{"reason": "again"},
		"X-Test-Role", "finance", "X-Test-Sub", mustUUID(t))
	if r.status != http.StatusConflict {
		t.Fatalf("the second payback = %d, want 409: %s", r.status, r.raw)
	}
	_, blockers, _ = errorOf(t, r)
	if len(blockers) == 0 || blockers[0] != "no_held_credit" {
		t.Errorf("blockers = %v, want no_held_credit (the credit was paid back)", blockers)
	}
	if got := gatewayRefundCalls(t, gw); got != callsAfterVoid+1 {
		t.Errorf("%d terminal refund calls after the second payback, want %d (the terminal was not asked again)", got, callsAfterVoid+1)
	}
	f.assertARInvariants(t)
}

// RULE: a payback whose terminal call answers no verdict stands as a claim
// like any act (the money may have moved), and settles through the resolve
// route: a recorded refund books the credit out to the card; a no refund
// releases the claim and the credit stays held, payable again at the
// counter.
func TestAPaybackWhoseCallAnswersNoVerdictSettlesThroughTheResolveRoute(t *testing.T) {
	testutil.LockOutboxTables(t)
	gw := &fakeGateway{charges: []*payment.GatewayResult{approvedCharge(), approvedCharge()},
		refundStatuses: []payment.GatewayStatus{payment.GatewayStatusRefunded, payment.GatewayStatusDeclined},
		refundErrs:     []error{nil, nil, errDeadline()}}
	f := newFixture(t, testutil.RequireDBMaxConns(t, 4), func(f *fixture) {
		f.service = f.service.WithGateway(gw)
	})
	f.openTill(10000)
	_, declined, credit := declinedCreditVoid(t, f, gw, mustUUID(t))

	// the payback's own terminal call loses its verdict: the route answers
	// the error and the payback claim stands PENDING, stamped answered
	r := f.do("POST", "/api/v1/pos/refund-intents/"+declined["id"].(string)+"/payback",
		map[string]any{"reason": "the customer is at the counter for their card"},
		"X-Test-Role", "finance", "X-Test-Sub", mustUUID(t))
	if r.status != http.StatusInternalServerError {
		t.Fatalf("the payback whose call lost its verdict = %d, want 500: %s", r.status, r.raw)
	}
	var paybackID string
	var paybackState string
	if err := f.db.Pool.QueryRow(context.Background(),
		`SELECT id::text, state FROM pos_refund_intents WHERE act = 'payback' AND tender_id = $1`, declined["tender_id"]).
		Scan(&paybackID, &paybackState); err != nil {
		t.Fatal(err)
	}
	if paybackState != "PENDING" {
		t.Fatalf("the payback claim = %s, want PENDING (the terminal may have moved the money)", paybackState)
	}
	if got := f.unappliedOf(t, declined["tender_id"].(string)); got != credit {
		t.Errorf("the credit while the payback claim stands = %d, want %d (nothing books until the claim settles)", got, credit)
	}
	f.assertARInvariants(t)

	// a recorded refund books the credit out to the card and completes the
	// claim DONE with the terminal's own id
	if r := f.resolveOn(t, paybackID, map[string]any{
		"outcome": "refund", "refund_tx_id": "rf-pay-1", "reason": "the terminal log shows the payback refund",
	}, "finance"); r.status != http.StatusOK {
		t.Fatalf("the payback claim's refund resolution = %d, want 200: %s", r.status, r.raw)
	}
	if got := f.unappliedOf(t, declined["tender_id"].(string)); got != 0 {
		t.Errorf("the credit after the resolution = %d, want 0", got)
	}
	if got := countOf(t, f.db, `SELECT count(*) FROM pos_refund_intents WHERE id = $1 AND state = 'DONE' AND refund_tx_id = 'rf-pay-1'`, paybackID); got != 1 {
		t.Errorf("the payback claim did not complete DONE with the terminal's own id")
	}
	if got := countOf(t, f.db, `SELECT count(*) FROM payment_refunds WHERE gateway_refund_id = 'rf-pay-1'`); got != 1 {
		t.Errorf("%d refund rows under the payback's terminal id, want 1", got)
	}
	f.assertARInvariants(t)
}

// RULE: a payback the terminal declined releases its claim and moves
// nothing: the credit stays held on its tender, and the counter may ask
// again.
func TestAPaybackTheTerminalDeclinedReleasesItsClaim(t *testing.T) {
	testutil.LockOutboxTables(t)
	gw := &fakeGateway{charges: []*payment.GatewayResult{approvedCharge(), approvedCharge()},
		refundStatuses: []payment.GatewayStatus{payment.GatewayStatusRefunded, payment.GatewayStatusDeclined,
			payment.GatewayStatusDeclined, payment.GatewayStatusRefunded}}
	f := newFixture(t, testutil.RequireDBMaxConns(t, 4), func(f *fixture) {
		f.service = f.service.WithGateway(gw)
	})
	f.openTill(10000)
	_, declined, credit := declinedCreditVoid(t, f, gw, mustUUID(t))

	// the terminal declines the payback: nothing moved, the claim is
	// released, the credit stays held
	r := f.do("POST", "/api/v1/pos/refund-intents/"+declined["id"].(string)+"/payback",
		map[string]any{"reason": "the customer is at the counter for their card"},
		"X-Test-Role", "finance", "X-Test-Sub", mustUUID(t))
	if r.status != http.StatusConflict {
		t.Fatalf("the declined payback = %d, want 409: %s", r.status, r.raw)
	}
	_, blockers, _ := errorOf(t, r)
	if len(blockers) == 0 || blockers[0] != "card_refund" {
		t.Errorf("blockers = %v, want card_refund (the terminal's own no)", blockers)
	}
	if got := countOf(t, f.db, `SELECT count(*) FROM pos_refund_intents WHERE act = 'payback' AND state = 'PENDING'`); got != 0 {
		t.Errorf("%d PENDING payback claims after the decline, want 0 (released)", got)
	}
	if got := f.unappliedOf(t, declined["tender_id"].(string)); got != credit {
		t.Errorf("the credit after the declined payback = %d, want %d (nothing moved)", got, credit)
	}
	f.assertARInvariants(t)

	// the counter asks again and the card is paid
	r = f.do("POST", "/api/v1/pos/refund-intents/"+declined["id"].(string)+"/payback",
		map[string]any{"reason": "the terminal accepted it this time"},
		"X-Test-Role", "finance", "X-Test-Sub", mustUUID(t))
	if r.status != http.StatusOK {
		t.Fatalf("the retried payback = %d, want 200: %s", r.status, r.raw)
	}
	if got := f.unappliedOf(t, declined["tender_id"].(string)); got != 0 {
		t.Errorf("the credit after the retried payback = %d, want 0", got)
	}
	f.assertARInvariants(t)
}

// RULE: the payback route holds the credit's sale to the caller's branch
// wall (and the no context hold), the same wall the resolve route runs:
// another branch's caller finds a missing row (404, nothing moved, the
// terminal never asked), and a caller whose grants do not carry the sale's
// branch is refused (403).
func TestThePaybackStaysInsideTheCallersBranch(t *testing.T) {
	testutil.LockOutboxTables(t)
	db := testutil.RequireDB(t)
	setPosSetting(t, db, "multi_branch_enabled", "true")
	setPosSetting(t, db, "default_branch_required", "false")
	branchB := uuid.New()
	mustExecB := func(sql string, args ...any) {
		t.Helper()
		if _, err := db.Pool.Exec(context.Background(), sql, args...); err != nil {
			t.Fatalf("%s: %v", sql, err)
		}
	}
	mustExecB(`INSERT INTO locations (id, type, code, name) VALUES ($1, 'BRANCH', 'WL-POS-P', 'payback wall B')`, branchB)
	mustExecB(`INSERT INTO user_locations (user_sub, branch_id, is_home, granted_by) VALUES ('pos-pay-b', $1, TRUE, 'test')`, branchB)
	t.Cleanup(func() {
		setPosSetting(t, db, "multi_branch_enabled", "false")
		setPosSetting(t, db, "default_branch_required", "true")
		mustExecB(`DELETE FROM user_locations WHERE user_sub IN ('pos-pay-b', 'pos-pay-a')`)
		mustExecB(`DELETE FROM locations WHERE id = $1`, branchB)
	})
	gw := &fakeGateway{charges: []*payment.GatewayResult{approvedCharge(), approvedCharge()},
		refundStatuses: []payment.GatewayStatus{payment.GatewayStatusRefunded, payment.GatewayStatusDeclined}}
	f := newFixture(t, db, func(f *fixture) {
		f.service = f.service.WithGateway(gw)
		mustExecB(`INSERT INTO user_locations (user_sub, branch_id, is_home, granted_by) VALUES ('pos-pay-a', $1, TRUE, 'test')`, f.branchID)
	})
	f.openTill(10000)
	_, declined, _ := declinedCreditVoid(t, f, gw, "pos-pay-a")
	callsAfterVoid := gatewayRefundCalls(t, gw)

	scoped := []string{"X-Test-Role", "finance", "X-Test-Sub", "pos-pay-b", "X-Branch-Id", branchB.String()}
	// the B caller, header or not, finds a missing row: nothing moved and
	// the terminal was never asked
	for _, headers := range [][]string{scoped,
		{"X-Test-Role", "finance", "X-Test-Sub", "pos-pay-b"}} {
		if r := f.do("POST", "/api/v1/pos/refund-intents/"+declined["id"].(string)+"/payback",
			map[string]any{"reason": "out of my branch"}, headers...); r.status != http.StatusNotFound {
			t.Errorf("another branch's payback (%v) = %d, want 404: %s", headers, r.status, r.raw)
		}
	}
	if got := countOf(t, f.db, `SELECT count(*) FROM pos_refund_intents WHERE act = 'payback'`); got != 0 {
		t.Errorf("%d payback claims through the wall, want 0", got)
	}
	if got := gatewayRefundCalls(t, gw); got != callsAfterVoid {
		t.Errorf("%d terminal refund calls through the wall, want %d (the terminal was never asked)", got, callsAfterVoid)
	}
	// a caller granted the sale's branch only, heading another branch's
	// header, is refused by the wall
	if r := f.do("POST", "/api/v1/pos/refund-intents/"+declined["id"].(string)+"/payback",
		map[string]any{"reason": "wrong header"},
		"X-Test-Role", "finance", "X-Test-Sub", "pos-pay-a", "X-Branch-Id", branchB.String()); r.status != http.StatusForbidden {
		t.Errorf("the granted caller under another branch's header = %d, want 403: %s", r.status, r.raw)
	}
	f.assertARInvariants(t)

	// the in branch caller pays the credit back
	if r := f.do("POST", "/api/v1/pos/refund-intents/"+declined["id"].(string)+"/payback",
		map[string]any{"reason": "the customer is at the counter for their card"},
		"X-Test-Role", "finance", "X-Test-Sub", "pos-pay-a"); r.status != http.StatusOK {
		t.Fatalf("the in branch payback = %d, want 200: %s", r.status, r.raw)
	}
	if got := f.unappliedOf(t, declined["tender_id"].(string)); got != 0 {
		t.Errorf("the credit after the in branch payback = %d, want 0", got)
	}
	f.assertARInvariants(t)
}

// RULE: a machine key never pays a credit back: the payback is a person's
// act, refused in the route itself, bound or not.
func TestAMachineKeyNeverPaysBackAHeldCredit(t *testing.T) {
	testutil.LockOutboxTables(t)
	gw := &fakeGateway{charges: []*payment.GatewayResult{approvedCharge(), approvedCharge()},
		refundStatuses: []payment.GatewayStatus{payment.GatewayStatusRefunded, payment.GatewayStatusDeclined}}
	f := newFixture(t, testutil.RequireDBMaxConns(t, 4), func(f *fixture) {
		f.service = f.service.WithGateway(gw)
	})
	f.openTill(10000)
	_, declined, _ := declinedCreditVoid(t, f, gw, mustUUID(t))
	keyed := keyedServer(f, uuid.NewString())
	req, _ := http.NewRequest(http.MethodPost, keyed.URL+"/api/v1/pos/refund-intents/"+declined["id"].(string)+"/payback",
		strings.NewReader(`{"reason":"keyed"}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Request-ID", "req-pos-key-test")
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if res.StatusCode != http.StatusForbidden {
		t.Fatalf("a machine key's payback = %d, want 403", res.StatusCode)
	}
	if got := countOf(t, f.db, `SELECT count(*) FROM pos_refund_intents WHERE act = 'payback'`); got != 0 {
		t.Errorf("%d payback claims by a machine key, want 0", got)
	}
	f.assertARInvariants(t)
}
