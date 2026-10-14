// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package pos_test

// The resolution race (ninth review P2-2, tenth review P2-1): two operators
// resolving one claim concurrently with opposite outcomes both answered 200,
// because the claim was read PENDING outside the transaction and every state
// change ignored its rows affected. The claim row is locked inside the
// resolution's transaction and every state change refuses 409
// intent_not_pending when it settles no row, so of any number of contenders
// exactly one wins, the books move at most once, and the losers are told the
// claim left PENDING.

import (
	"context"
	"net/http"
	"sync"
	"testing"

	"github.com/gablelbm/gable/internal/payment"
	"github.com/gablelbm/gable/internal/testutil"
)

// RULE: three concurrent resolutions of one stranded return claim (two
// refunds with different recorded ids and a no refund): exactly one answers
// 200, the others 409 intent_not_pending naming the race, the claim's final
// state matches the winner, the books moved at most once, and the terminal
// was asked for nothing (the resolutions make no gateway call).
func TestThreeConcurrentResolutionsOfOneClaimSettleItOnce(t *testing.T) {
	testutil.LockOutboxTables(t)
	gw := &fakeGateway{charges: []*payment.GatewayResult{approvedCharge()},
		refundErrs: []error{context.DeadlineExceeded}}
	f := newFixture(t, testutil.RequireDBMaxConns(t, 4), func(f *fixture) {
		f.service = f.service.WithGateway(gw)
	})
	f.openTill(0)
	saleID, body := f.saleOf("1", withToken(tender("card", 599)))
	lineID := body.body["lines"].([]any)[0].(map[string]any)["id"].(string)
	if r := f.cardReturnOn(t, saleID, lineID, "1"); r.status != http.StatusInternalServerError {
		t.Fatalf("the timed out card return = %d, want 500: %s", r.status, r.raw)
	}
	intentID, state, refundTxID, _ := pendingIntent(t, f, saleID)
	if state != "PENDING" || refundTxID != "" {
		t.Fatalf("the stranded claim = (%s, %q)", state, refundTxID)
	}

	bodies := []map[string]any{
		{"outcome": "refund", "refund_tx_id": "ref-op-1", "reason": "first operator read the terminal"},
		{"outcome": "refund", "refund_tx_id": "ref-op-2", "reason": "second operator read the terminal"},
		{"outcome": "no_refund", "reconciliation_reference": "REC-RACE", "reason": "third operator read the terminal"},
	}
	res := make(chan resp, len(bodies))
	var wg sync.WaitGroup
	for _, b := range bodies {
		wg.Add(1)
		go func(b map[string]any) {
			defer wg.Done()
			res <- f.resolveOn(t, intentID, b, "finance")
		}(b)
	}
	wg.Wait()
	close(res)
	var ok, refused, other int
	for r := range res {
		switch {
		case r.status == http.StatusOK:
			ok++
		case r.status == http.StatusConflict:
			_, blockers, _ := errorOf(t, r)
			if len(blockers) == 0 || blockers[0] != "intent_not_pending" {
				t.Errorf("a losing resolution names %v, want intent_not_pending: %s", blockers, r.raw)
				other++
			} else {
				refused++
			}
		default:
			other++
			t.Errorf("a contender answered %d: %s", r.status, r.raw)
		}
	}
	if ok != 1 || refused != len(bodies)-1 || other != 0 {
		t.Fatalf("of %d contenders, %d answered 200, %d answered 409 intent_not_pending and %d answered otherwise: want exactly one 200", len(bodies), ok, refused, other)
	}
	// the claim's final state is one settled state, whichever outcome won
	if got := countOf(t, f.db, `SELECT count(*) FROM pos_refund_intents WHERE id = $1 AND state IN ('DONE', 'FAILED')`, intentID); got != 1 {
		t.Errorf("the claim is not settled once: %d rows", got)
	}
	if got := countOf(t, f.db, `SELECT count(*) FROM audit_log WHERE action = 'pos.refund_intent.resolved' AND changes->>'intent_id' = $1`, intentID); got != 1 {
		t.Errorf("%d resolution audit rows, want 1 (the winners' record alone)", got)
	}
	// the books moved at most once and the ties hold whichever way it went
	done := countOf(t, f.db, `SELECT count(*) FROM pos_refund_intents WHERE id = $1 AND state = 'DONE'`, intentID)
	refunds := countOf(t, f.db, `SELECT count(*) FROM payment_refunds`)
	if done != refunds {
		t.Errorf("%d booked refunds beside a claim settled DONE %d times: the money follows the winning outcome alone", refunds, done)
	}
	gw.mu.Lock()
	calls := len(gw.refunds)
	gw.mu.Unlock()
	if calls != 1 {
		t.Errorf("%d gateway refund calls, want 1 (the stranded act's own call: the resolutions ask the terminal for nothing)", calls)
	}
	f.assertARInvariants(t)
}
