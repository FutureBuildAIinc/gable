// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package pos_test

// The close against the terminal (eighth review P2-2): a card void parked at
// the gateway let the close commit, and the void was then refused
// session_closed after the card was refunded: the money left after the drawer
// was counted, and the closed session's expected card still counted what the
// terminal gave back. The close refuses while any intent of the session's
// sales is PENDING.

import (
	"net/http"
	"strings"
	"testing"

	"github.com/gablelbm/gable/internal/payment"
	"github.com/gablelbm/gable/internal/testutil"
)

// RULE (eighth review P2-2): the close is refused 409 refund_in_flight while
// a card refund intent of one of the session's sales is PENDING, under the
// session lock: the drawer is not counted and closed while a terminal refund
// of one of its sales may still move money. The parked void then completes,
// and the close goes through.
func TestTheCloseRefusesWhileAVoidIsParkedAtTheGateway(t *testing.T) {
	testutil.LockOutboxTables(t)
	gw := &fakeGateway{charges: []*payment.GatewayResult{approvedCharge()}}
	f := newFixture(t, testutil.RequireDBMaxConns(t, 4), func(f *fixture) {
		f.service = f.service.WithGateway(gw)
	})
	// the final count below is off by design: the variance is not the subject
	f.allowAnyVariance()
	f.openTill(0)
	saleID, body := f.saleOf("1", withToken(tender("card", 599)))
	sessionID := f.currentTillID(t)
	parked, release := parkRefund(t, gw)
	voidDone := make(chan resp, 1)
	fin := make(chan struct{})
	go func() {
		defer close(fin)
		voidDone <- f.do("POST", "/api/v1/pos/transactions/"+saleID+"/void",
			map[string]any{"reason": "parked at the terminal", "revision": rev(t, body)},
			"X-Test-Role", "cashier", "X-Test-Sub", mustUUID(t))
	}()
	t.Cleanup(func() { release(); <-fin })
	awaitParked(t, parked)
	// the close fires while the void's refund is on the wire
	r := f.do("POST", "/api/v1/pos/till/"+sessionID+"/close",
		map[string]any{"counted_by_method": map[string]any{"cash": 0, "card": 599}},
		"X-Test-Role", "cashier", "X-Test-Sub", mustUUID(t))
	if r.status != http.StatusConflict {
		t.Fatalf("the close under a parked card void = %d, want 409: %s", r.status, r.raw)
	}
	_, blockers, _ := errorOf(t, r)
	if len(blockers) == 0 || blockers[0] != "refund_in_flight" {
		t.Errorf("blockers = %v, want refund_in_flight (the void's claim holds the session)", blockers)
	}
	// the refusal names the standing claim and who unblocks the drawer (tenth
	// review P3-5): the cashier knows the claim id and the resolve route
	intentID, _, _, _ := pendingIntent(t, f, saleID)
	if !strings.Contains(string(r.raw), intentID) {
		t.Errorf("the close's refusal does not name the standing claim %s: %s", intentID, r.raw)
	}
	if !strings.Contains(string(r.raw), "/api/v1/pos/refund-intents/") {
		t.Errorf("the close's refusal does not name the resolve route: %s", r.raw)
	}
	if got := f.scalar(`SELECT status FROM till_sessions WHERE id = $1`, sessionID); got != "OPEN" {
		t.Errorf("the session = %v, want OPEN (the close refused, nothing was counted)", got)
	}
	f.assertARInvariants(t)
	release()
	if rr := <-voidDone; rr.status != http.StatusOK {
		t.Fatalf("the parked void = %d: %s", rr.status, rr.raw)
	}
	gw.mu.Lock()
	refunds := len(gw.refunds)
	gw.mu.Unlock()
	if refunds != 1 {
		t.Fatalf("%d gateway refunds, want 1 (the terminal refunded the card once)", refunds)
	}
	// the void completed: the drawer closes over it
	r = f.do("POST", "/api/v1/pos/till/"+sessionID+"/close",
		map[string]any{"counted_by_method": map[string]any{"cash": 0, "card": 599}},
		"X-Test-Role", "cashier", "X-Test-Sub", mustUUID(t))
	if r.status != http.StatusOK {
		t.Fatalf("the close after the void completed = %d: %s", r.status, r.raw)
	}
	if got := f.scalar(`SELECT status FROM till_sessions WHERE id = $1`, sessionID); got != "CLOSED" {
		t.Errorf("the session = %v, want CLOSED", got)
	}
	f.assertARInvariants(t)
}
