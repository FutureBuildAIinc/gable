// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package pos_test

// The verdicts of a refund call (seventh review P2-2): an error from the
// terminal carries no verdict (a timeout, a dropped connection, a context
// cancel after the terminal acted), so the terminal may have moved the money;
// releasing the claim would let the next refund pay the tender twice. Only a
// definite refusal status proves nothing moved.

import (
	"context"
	"net/http"
	"testing"

	"github.com/gablelbm/gable/internal/payment"
	"github.com/gablelbm/gable/internal/testutil"
)

// RULE (seventh review P2-2): a refund call that answers no verdict (a
// timeout) leaves the intent PENDING with one committed audit row naming the
// intent and the unknown outcome; the next refund of the tender is refused
// before the terminal, and the stub gateway counts no second call.
func TestATimedOutCardRefundLeavesItsClaimStanding(t *testing.T) {
	testutil.LockOutboxTables(t)
	gw := &fakeGateway{charges: []*payment.GatewayResult{approvedCharge()},
		refundErrs: []error{context.DeadlineExceeded}}
	f := newFixture(t, testutil.RequireDB(t), func(f *fixture) {
		f.service = f.service.WithGateway(gw)
	})
	saleID, body := f.saleOf("1", withToken(tender("card", 599)))
	lineID := body.body["lines"].([]any)[0].(map[string]any)["id"].(string)
	r := f.cardReturnOn(t, saleID, lineID, "1")
	if r.status != http.StatusInternalServerError {
		t.Fatalf("the card return under a timed out refund = %d, want 500: %s", r.status, r.raw)
	}
	if got := countOf(t, f.db, `SELECT count(*) FROM pos_refund_intents WHERE sale_id = $1 AND state = 'PENDING'`, saleID); got != 1 {
		t.Fatalf("%d PENDING refund intents after the timed out call, want 1 (no verdict came back: the claim must stand)", got)
	}
	if got := countOf(t, f.db, `SELECT count(*) FROM pos_refund_intents WHERE sale_id = $1 AND state = 'FAILED'`, saleID); got != 0 {
		t.Errorf("%d FAILED refund intents, want 0 (the claim is not released without a verdict)", got)
	}
	if got := countOf(t, f.db, `SELECT count(*) FROM audit_log WHERE action = 'pos.refund_outcome_unknown' AND entity_id = $1`, saleID); got != 1 {
		t.Errorf("%d unknown outcome rows, want 1 (the intent is findable for reconciliation)", got)
	}
	var intentID string
	if err := f.db.Pool.QueryRow(context.Background(),
		`SELECT id FROM pos_refund_intents WHERE sale_id = $1 AND state = 'PENDING'`, saleID).Scan(&intentID); err != nil {
		t.Fatal(err)
	}
	if got := countOf(t, f.db, `SELECT count(*) FROM audit_log WHERE action = 'pos.refund_outcome_unknown' AND changes->>'intent_id' = $1`, intentID); got != 1 {
		t.Errorf("%d unknown outcome rows naming the intent, want 1", got)
	}
	// the standing claim refuses the next card refund before the terminal
	r = f.cardReturnOn(t, saleID, lineID, "1")
	if r.status != http.StatusConflict {
		t.Fatalf("the next card return = %d, want 409: %s", r.status, r.raw)
	}
	_, blockers, _ := errorOf(t, r)
	if len(blockers) == 0 || blockers[0] != "refund_in_flight" {
		t.Errorf("blockers = %v, want refund_in_flight", blockers)
	}
	gw.mu.Lock()
	refunds := len(gw.refunds)
	gw.mu.Unlock()
	if refunds != 1 {
		t.Fatalf("%d gateway refund calls, want 1 (the refusal never reached the terminal)", refunds)
	}
	if got := f.accountBalance("1010"); got != 599 {
		t.Errorf("card balance = %d, want 599 (the charge stands, nothing booked)", got)
	}
	f.assertARInvariants(t)
}

// RULE (seventh review P2-2): a definite refusal status (the terminal's own
// no) proves nothing moved: the claim is released and the retry goes through.
func TestADeclinedCardRefundReleasesItsClaim(t *testing.T) {
	testutil.LockOutboxTables(t)
	gw := &fakeGateway{charges: []*payment.GatewayResult{approvedCharge()},
		refundStatuses: []payment.GatewayStatus{payment.GatewayStatusDeclined}}
	f := newFixture(t, testutil.RequireDB(t), func(f *fixture) {
		f.service = f.service.WithGateway(gw)
	})
	saleID, body := f.saleOf("1", withToken(tender("card", 599)))
	lineID := body.body["lines"].([]any)[0].(map[string]any)["id"].(string)
	r := f.cardReturnOn(t, saleID, lineID, "1")
	if r.status != http.StatusConflict {
		t.Fatalf("the declined card return = %d, want 409: %s", r.status, r.raw)
	}
	_, blockers, _ := errorOf(t, r)
	if len(blockers) == 0 || blockers[0] != "card_refund" {
		t.Errorf("blockers = %v, want card_refund (the terminal's own no)", blockers)
	}
	if got := countOf(t, f.db, `SELECT count(*) FROM pos_refund_intents WHERE sale_id = $1 AND state = 'FAILED'`, saleID); got != 1 {
		t.Fatalf("%d FAILED refund intents after the declined call, want 1 (a definite refusal moved nothing)", got)
	}
	// the retry goes through: the released claim no longer blocks the tender
	if r := f.cardReturnOn(t, saleID, lineID, "1"); r.status != http.StatusCreated {
		t.Fatalf("the retried card return = %d: %s", r.status, r.raw)
	}
	if got := countOf(t, f.db, `SELECT count(*) FROM pos_refund_intents WHERE sale_id = $1 AND state = 'DONE'`, saleID); got != 1 {
		t.Errorf("%d DONE refund intents, want 1 (the retry's claim completed with its posting)", got)
	}
	if got := f.accountBalance("1010"); got != 0 {
		t.Errorf("card balance = %d, want 0 (refunded once)", got)
	}
	f.assertARInvariants(t)
}
