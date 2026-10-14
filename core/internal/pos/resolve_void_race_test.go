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

// RULE (r11 P3-1): three concurrent resolutions over the two claims of a
// stranded two tender void (the refunded tender's claim recorded once, the
// timed out tender's claim fought over by a refund and a no refund): exactly
// one contender answers 200 per claim, the loser names the race (409
// intent_not_pending), the books move once per settled refund, and the void
// completes exactly once whichever leg empties the claims, so the contender
// that wins the last claim never decides the end state twice. The
// resolutions reach the terminal for nothing: the void's own two calls are
// all the gateway sees.
func TestThreeConcurrentResolutionsOfASplitVoidsClaimsSettleEachOnce(t *testing.T) {
	testutil.LockOutboxTables(t)
	gw := &fakeGateway{charges: []*payment.GatewayResult{approvedCharge(), approvedCharge()},
		refundErrs: []error{nil, context.DeadlineExceeded}}
	f := newFixture(t, testutil.RequireDBMaxConns(t, 4), func(f *fixture) {
		f.service = f.service.WithGateway(gw)
	})
	f.openTill(0)
	saleID, body, _ := f.twoCardSaleOf(t, "1", 300, 299)
	if r := f.do("POST", "/api/v1/pos/transactions/"+saleID+"/void",
		map[string]any{"reason": "split void", "revision": rev(t, body)},
		"X-Test-Role", "cashier", "X-Test-Sub", mustUUID(t)); r.status != http.StatusInternalServerError {
		t.Fatalf("the timed out void = %d, want 500: %s", r.status, r.raw)
	}
	// the terminal refunded the first claim and timed out on the second, so
	// one claim carries its refund id and the other stands uncalled
	var withID, withoutID map[string]any
	for _, in := range f.saleIntents(t, saleID) {
		if in["state"] != "PENDING" {
			continue
		}
		if in["refund_tx_id"] != "" {
			withID = in
		} else {
			withoutID = in
		}
	}
	if withID == nil || withoutID == nil {
		t.Fatalf("the standing claims = %v, want one carrying the refund id and one without", f.saleIntents(t, saleID))
	}

	// three contenders: the refunded claim recorded once, the timed out claim
	// fought over by a refund and a no refund
	contenders := []struct {
		intentID string
		body     map[string]any
	}{
		{withID["id"].(string), map[string]any{
			"outcome": "refund", "refund_tx_id": withID["refund_tx_id"].(string), "reason": "the terminal refunded this card"}},
		{withoutID["id"].(string), map[string]any{
			"outcome": "refund", "refund_tx_id": "ref-op-2", "reason": "the terminal log shows this refund"}},
		{withoutID["id"].(string), map[string]any{
			"outcome": "no_refund", "reconciliation_reference": "REC-RACE", "reason": "the terminal log shows no refund"}},
	}
	asked := map[string]int{}
	for _, c := range contenders {
		asked[c.intentID]++
	}
	type answer struct {
		intentID string
		r        resp
	}
	res := make(chan answer, len(contenders))
	var wg sync.WaitGroup
	for _, c := range contenders {
		wg.Add(1)
		go func(c struct {
			intentID string
			body     map[string]any
		}) {
			defer wg.Done()
			res <- answer{c.intentID, f.resolveOn(t, c.intentID, c.body, "finance")}
		}(c)
	}
	wg.Wait()
	close(res)
	counts := map[string]*struct{ ok, refused, other int }{}
	for a := range res {
		c, ok := counts[a.intentID]
		if !ok {
			c = &struct{ ok, refused, other int }{}
			counts[a.intentID] = c
		}
		switch {
		case a.r.status == http.StatusOK:
			c.ok++
		case a.r.status == http.StatusConflict:
			_, blockers, _ := errorOf(t, a.r)
			if len(blockers) == 0 || blockers[0] != "intent_not_pending" {
				t.Errorf("a losing resolution names %v, want intent_not_pending: %s", blockers, a.r.raw)
				c.other++
			} else {
				c.refused++
			}
		default:
			c.other++
			t.Errorf("a contender answered %d: %s", a.r.status, a.r.raw)
		}
	}
	// exactly one 200 per claim, the loser of the fought claim names the race
	for intentID, n := range asked {
		c := counts[intentID]
		if c == nil || c.ok != 1 || c.other != 0 || c.ok+c.refused+c.other != n {
			t.Fatalf("a claim settled %d x 200, %d x 409 intent_not_pending and %d otherwise of %d contenders: want exactly one 200 per claim",
				c.ok, c.refused, c.other, n)
		}
	}
	// the books moved once: every booked refund names a claim settled DONE
	done := countOf(t, f.db, `SELECT count(*) FROM pos_refund_intents WHERE sale_id = $1 AND state = 'DONE'`, saleID)
	refunds := countOf(t, f.db, `SELECT count(*) FROM payment_refunds`)
	if done != refunds {
		t.Errorf("%d booked refunds beside %d claims settled DONE: the money follows the winning outcome alone", refunds, done)
	}
	if done < 1 || done > 2 {
		t.Errorf("%d claims settled DONE, want 1 or 2 (the refunded claim always, the fought claim per its winner)", done)
	}
	// the void completed exactly once, whichever leg emptied the claims
	if got := str(t, f.getSale(t, saleID), "status"); got != "voided" {
		t.Errorf("sale status = %q, want voided (the completion ran from the leg that emptied the claims)", got)
	}
	if got := countOf(t, f.db, `SELECT count(*) FROM audit_log WHERE action = 'pos.transaction.voided' AND entity_id = $1`, saleID); got != 1 {
		t.Errorf("%d voided audit rows, want 1 (the completion is one act whatever resolved last)", got)
	}
	if got := countOf(t, f.db, `SELECT count(*) FROM audit_log WHERE action = 'pos.refund_intent.resolved'
		AND changes->>'intent_id' = ANY($1::text[])`, []string{withID["id"].(string), withoutID["id"].(string)}); got != 2 {
		t.Errorf("%d resolution audit rows, want 2 (the winners' records alone)", got)
	}
	// which tender carries the terminal's refund follows the tenders' own
	// order, so the booked total is read from the claims: the recorded claim
	// always, the fought claim when its refund won
	booked := withID["amount"].(int64)
	if countOf(t, f.db, `SELECT count(*) FROM pos_refund_intents WHERE id = $1 AND state = 'DONE'`, withoutID["id"].(string)) == 1 {
		booked += withoutID["amount"].(int64)
	}
	if got := f.accountBalance("1010"); got != 599-booked {
		t.Errorf("card balance = %d, want %d (599 charged, %d booked back: the recorded claim always, the fought tender per its winner)", got, 599-booked, booked)
	}
	if got := f.stock(); got != "100.0000/0.0000" {
		t.Errorf("stock = %s, want 100 (the void restocked the unit once)", got)
	}
	gw.mu.Lock()
	calls := len(gw.refunds)
	gw.mu.Unlock()
	if calls != 2 {
		t.Errorf("%d gateway refund calls, want 2 (the void's own calls: no resolution reaches the terminal)", calls)
	}
	f.assertARInvariants(t)
}
