// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package pos_test

// The same outcome race on a void claim (r12 P3-1): two operators recording
// the same refund of one stranded void claim concurrently. Exactly one books
// it (the sale voids), and the loser is told the race (409
// intent_not_pending, the claim already settled), never a misleading state
// refusal the winner's own completion caused: the claim row is locked and
// read right after the sale lock, before any guard of the booking.

import (
	"context"
	"net/http"
	"sync"
	"testing"

	"github.com/gablelbm/gable/internal/payment"
	"github.com/gablelbm/gable/internal/testutil"
)

// RULE (r12 P3-1): of two concurrent refund resolutions of one stranded void
// claim recording the same terminal refund, exactly one answers 200 and
// voids the sale; the loser answers 409 intent_not_pending naming the race,
// not invalid_state_transition (the winner's VOIDED sale seen before the
// claim's own settled row). The books move once and the terminal is asked
// for nothing by either resolution.
func TestTwoConcurrentRefundResolutionsOfAVoidClaimNameTheRace(t *testing.T) {
	testutil.LockOutboxTables(t)
	gw := &fakeGateway{charges: []*payment.GatewayResult{approvedCharge()},
		refundErrs: []error{context.DeadlineExceeded}}
	f := newFixture(t, testutil.RequireDBMaxConns(t, 4), func(f *fixture) {
		f.service = f.service.WithGateway(gw)
	})
	f.openTill(0)
	saleID, body := f.saleOf("1", withToken(tender("card", 599)))
	if r := f.do("POST", "/api/v1/pos/transactions/"+saleID+"/void",
		map[string]any{"reason": "stranded void", "revision": rev(t, body)},
		"X-Test-Role", "cashier", "X-Test-Sub", mustUUID(t)); r.status != http.StatusInternalServerError {
		t.Fatalf("the timed out void = %d, want 500: %s", r.status, r.raw)
	}
	intentID, state, refundTxID, _ := pendingIntent(t, f, saleID)
	if state != "PENDING" || refundTxID != "" {
		t.Fatalf("the stranded void claim = (%s, %q), want PENDING carrying no refund id", state, refundTxID)
	}

	same := map[string]any{"outcome": "refund", "refund_tx_id": "ref-op-same", "reason": "both operators read the terminal"}
	res := make(chan resp, 2)
	var wg sync.WaitGroup
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			res <- f.resolveOn(t, intentID, same, "finance")
		}()
	}
	wg.Wait()
	close(res)
	var ok, named int
	for r := range res {
		switch {
		case r.status == http.StatusOK:
			ok++
		case r.status == http.StatusConflict:
			code, blockers, _ := errorOf(t, r)
			if code != "conflict" || len(blockers) == 0 || blockers[0] != "intent_not_pending" {
				t.Errorf("the losing void resolution answers (%s, %v), want conflict intent_not_pending: %s", code, blockers, r.raw)
			} else {
				named++
			}
		default:
			t.Errorf("a contender answered %d: %s", r.status, r.raw)
		}
	}
	if ok != 1 || named != 1 {
		t.Fatalf("of 2 contenders, %d answered 200 and %d named the race: want one of each", ok, named)
	}
	if got := str(t, f.getSale(t, saleID), "status"); got != "voided" {
		t.Errorf("sale status = %q, want voided (the winning resolution completed the void)", got)
	}
	if got := f.accountBalance("1010"); got != 0 {
		t.Errorf("card balance = %d, want 0 (the terminal's refund booked exactly once)", got)
	}
	if got := countOf(t, f.db, `SELECT count(*) FROM payment_refunds`); got != 1 {
		t.Errorf("%d refund rows, want 1", got)
	}
	gw.mu.Lock()
	calls := len(gw.refunds)
	gw.mu.Unlock()
	if calls != 1 {
		t.Errorf("%d gateway refund calls, want 1 (the stranded void's own call)", calls)
	}
	f.assertARInvariants(t)
}
