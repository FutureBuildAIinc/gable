// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package pos_test

// The resolution's branch hold for a caller with no context branch (r12
// P1-2): with multi_branch_enabled on and default_branch_required off, a
// finance user held to branch B who sends no X-Branch-Id header gets a
// context with no branch, so the sale read's repository filter never fired
// and the user settled branch A's claim, freeing a tender whose money may
// already have moved. The route holds the claim's sale to the caller's
// grants through the branch guard serve passes the document and purchase
// order handlers: a sale outside the grants is a missing row (404) on both
// legs, nothing moves, and the goods' money leaves through the terminal
// exactly once in the end.

import (
	"context"
	"net/http"
	"testing"

	"github.com/gablelbm/gable/internal/payment"
	"github.com/gablelbm/gable/internal/testutil"
	"github.com/google/uuid"
)

func TestTheResolutionHoldsTheSaleToTheCallersGrantsWithoutAHeader(t *testing.T) {
	testutil.LockOutboxTables(t)
	db := testutil.RequireDB(t)
	// the kill switch is read when the fixture's middleware is built
	setPosSetting(t, db, "multi_branch_enabled", "true")
	setPosSetting(t, db, "default_branch_required", "false")
	branchB := uuid.New()
	mustExecB := func(sql string, args ...any) {
		t.Helper()
		if _, err := db.Pool.Exec(context.Background(), sql, args...); err != nil {
			t.Fatalf("%s: %v", sql, err)
		}
	}
	mustExecB(`INSERT INTO locations (id, type, code, name) VALUES ($1, 'BRANCH', 'WL-POS-G', 'grants wall B')`, branchB)
	mustExecB(`INSERT INTO user_locations (user_sub, branch_id, is_home, granted_by) VALUES ('pos-gr-b', $1, TRUE, 'test')`, branchB)
	t.Cleanup(func() {
		setPosSetting(t, db, "multi_branch_enabled", "false")
		setPosSetting(t, db, "default_branch_required", "true")
		mustExecB(`DELETE FROM user_locations WHERE user_sub IN ('pos-gr-b', 'pos-gr-a')`)
		mustExecB(`DELETE FROM locations WHERE id = $1`, branchB)
	})

	gw := &fakeGateway{charges: []*payment.GatewayResult{approvedCharge(), approvedCharge()},
		refundErrs: []error{context.DeadlineExceeded, nil, context.DeadlineExceeded, nil}}
	f := newFixture(t, db, func(f *fixture) {
		f.service = f.service.WithGateway(gw)
		// the in branch finance user is granted the sale's own branch, so the
		// same no header path passes for a caller the grants cover
		mustExecB(`INSERT INTO user_locations (user_sub, branch_id, is_home, granted_by) VALUES ('pos-gr-a', $1, TRUE, 'test')`, f.branchID)
		f.cashierSub = "pos-gr-a"
	})
	f.openTill(0)
	// the B finance user sends no X-Branch-Id header at all: the middleware
	// answers a context with no branch (the setting is off)
	noHeader := []string{"X-Test-Role", "finance", "X-Test-Sub", "pos-gr-b"}

	// the return claim: a card return of a branch A sale timed out at the
	// terminal (nothing moved)
	saleID, body := f.saleOf("1", withToken(tender("card", 599)))
	lineID := body.body["lines"].([]any)[0].(map[string]any)["id"].(string)
	if r := f.cardReturnOn(t, saleID, lineID, "1"); r.status != http.StatusInternalServerError {
		t.Fatalf("the timed out card return = %d, want 500: %s", r.status, r.raw)
	}
	intentID, state, refundTxID, _ := pendingIntent(t, f, saleID)
	if state != "PENDING" || refundTxID != "" {
		t.Fatalf("the stranded return claim = (%s, %q), want PENDING carrying no refund id", state, refundTxID)
	}
	// both legs through the door: 404, the claim stands, nothing moves
	if r := f.do("POST", "/api/v1/pos/refund-intents/"+intentID+"/resolve", map[string]any{
		"outcome": "no_refund", "reconciliation_reference": "REC-GB", "reason": "out of my grants",
	}, noHeader...); r.status != http.StatusNotFound {
		t.Errorf("another branch's no refund without a header = %d, want 404: %s", r.status, r.raw)
	}
	if r := f.do("POST", "/api/v1/pos/refund-intents/"+intentID+"/resolve", map[string]any{
		"outcome": "refund", "refund_tx_id": "ref-op-gb", "reason": "out of my grants",
	}, noHeader...); r.status != http.StatusNotFound {
		t.Errorf("another branch's refund without a header = %d, want 404: %s", r.status, r.raw)
	}
	if got := countOf(t, f.db, `SELECT count(*) FROM pos_refund_intents WHERE id = $1 AND state = 'PENDING'`, intentID); got != 1 {
		t.Errorf("the claim was settled through the grants wall: %d PENDING rows, want 1", got)
	}
	if got := countOf(t, f.db, `SELECT count(*) FROM audit_log WHERE action = 'pos.refund_intent.resolved' AND changes->>'intent_id' = $1`, intentID); got != 0 {
		t.Errorf("%d resolution audit rows through the grants wall, want 0", got)
	}
	// a finance user the sale's branch is granted resolves with no header the
	// same way: the grants cover the sale, so the hold passes
	if r := f.do("POST", "/api/v1/pos/refund-intents/"+intentID+"/resolve", map[string]any{
		"outcome": "no_refund", "reconciliation_reference": "REC-GA", "reason": "the terminal log shows no refund",
	}, "X-Test-Role", "finance", "X-Test-Sub", "pos-gr-a"); r.status != http.StatusOK {
		t.Fatalf("the granted finance user's no refund without a header = %d, want 200: %s", r.status, r.raw)
	}
	// the goods' money then leaves through the terminal exactly once: the
	// later card return of the same tender makes its own single call
	if r := f.cardReturnOn(t, saleID, lineID, "1"); r.status != http.StatusCreated {
		t.Fatalf("the later card return = %d, want 201: %s", r.status, r.raw)
	}
	if got := f.accountBalance("1010"); got != 0 {
		t.Errorf("card balance = %d, want 0 (charged 599, refunded 599 exactly once)", got)
	}
	if got := f.counterRefunds(t); got != 1 {
		t.Errorf("%d refund rows, want 1 (one booked refund of the one tender)", got)
	}

	// the void claim: the same wall on the void act
	saleID2, body2 := f.saleOf("1", withToken(tender("card", 599)))
	if r := f.do("POST", "/api/v1/pos/transactions/"+saleID2+"/void",
		map[string]any{"reason": "stranded", "revision": rev(t, body2)},
		"X-Test-Role", "cashier", "X-Test-Sub", mustUUID(t)); r.status != http.StatusInternalServerError {
		t.Fatalf("the timed out void = %d, want 500: %s", r.status, r.raw)
	}
	intentID2, state2, refundTxID2, _ := pendingIntent(t, f, saleID2)
	if state2 != "PENDING" || refundTxID2 != "" {
		t.Fatalf("the stranded void claim = (%s, %q), want PENDING carrying no refund id", state2, refundTxID2)
	}
	if r := f.do("POST", "/api/v1/pos/refund-intents/"+intentID2+"/resolve", map[string]any{
		"outcome": "no_refund", "reconciliation_reference": "REC-GB2", "reason": "out of my grants",
	}, noHeader...); r.status != http.StatusNotFound {
		t.Errorf("another branch's void no refund without a header = %d, want 404: %s", r.status, r.raw)
	}
	if r := f.do("POST", "/api/v1/pos/refund-intents/"+intentID2+"/resolve", map[string]any{
		"outcome": "refund", "refund_tx_id": "ref-op-gb2", "reason": "out of my grants",
	}, noHeader...); r.status != http.StatusNotFound {
		t.Errorf("another branch's void refund without a header = %d, want 404: %s", r.status, r.raw)
	}
	if got := countOf(t, f.db, `SELECT count(*) FROM pos_refund_intents WHERE id = $1 AND state = 'PENDING'`, intentID2); got != 1 {
		t.Errorf("the void claim was settled through the grants wall: %d PENDING rows, want 1", got)
	}
	// the granted user releases it and a fresh void of the same sale reaches
	// the terminal once more, exactly as its own act
	if r := f.do("POST", "/api/v1/pos/refund-intents/"+intentID2+"/resolve", map[string]any{
		"outcome": "no_refund", "reconciliation_reference": "REC-GA2", "reason": "the terminal log shows no refund",
	}, "X-Test-Role", "finance", "X-Test-Sub", "pos-gr-a"); r.status != http.StatusOK {
		t.Fatalf("the granted finance user's void no refund = %d, want 200: %s", r.status, r.raw)
	}
	cur := f.getSale(t, saleID2)
	if r := f.do("POST", "/api/v1/pos/transactions/"+saleID2+"/void",
		map[string]any{"reason": "fresh void", "revision": num(t, cur, "revision")},
		"X-Test-Role", "cashier", "X-Test-Sub", mustUUID(t)); r.status != http.StatusOK {
		t.Fatalf("the fresh void = %d, want 200: %s", r.status, r.raw)
	}
	// two sales, each refunded at the terminal exactly once
	gw.mu.Lock()
	calls := len(gw.refunds)
	gw.mu.Unlock()
	if calls != 4 {
		t.Errorf("%d gateway refund calls, want 4 (each sale's stranded call and its one later act)", calls)
	}
	if got := f.counterRefunds(t); got != 2 {
		t.Errorf("%d refund rows, want 2 (one booked refund per sale)", got)
	}
	f.assertARInvariants(t)
}
