// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package pos_test

// The till report's card figure through a partly resolved void (r12 P3-2):
// the CARD entry of expected_by_method is the pass-through of what the
// session's sales collected, so a void refund a resolution booked had left it
// naming the full tender. The figure subtracts the booked void refunds of the
// session's sales the same way the CASH figure subtracts the session's cash
// refunds, and it drops with the sale when the void completes.

import (
	"context"
	"net/http"
	"testing"

	"github.com/gablelbm/gable/internal/payment"
	"github.com/gablelbm/gable/internal/testutil"
)

// RULE (r12 P3-2, the M10 shape): a sale of cash 199 with two card tenders
// of 200 each, voided with one card refunded at the terminal and one timed
// out. While the sale still stands completed between the resolutions, the
// report's CARD figure names what the session net collected on cards (400
// tendered less the 200 the resolution booked), and once the last claim is
// resolved and the void completes, both figures name the voided sale's
// absence: the card figure 0 and the cash back to the float.
func TestTheTillReportCardFigureSubtractsBookedVoidRefunds(t *testing.T) {
	testutil.LockOutboxTables(t)
	gw := &fakeGateway{charges: []*payment.GatewayResult{approvedCharge(), approvedCharge()},
		refundErrs: []error{nil, context.DeadlineExceeded}}
	f := newFixture(t, testutil.RequireDBMaxConns(t, 4), func(f *fixture) {
		f.service = f.service.WithGateway(gw)
	})
	sessionID := f.openTill(10000)
	saleID := f.startSale(nil)
	if r := f.addLine(saleID, f.productLine("1")); r.status != http.StatusOK {
		t.Fatalf("add line = %d: %s", r.status, r.raw)
	}
	body := f.completeSale(saleID, tender("cash", 199), withToken(tender("card", 200)), withToken(tender("card", 200)))
	if body.status != http.StatusOK {
		t.Fatalf("complete = %d: %s", body.status, body.raw)
	}
	if r := f.do("POST", "/api/v1/pos/transactions/"+saleID+"/void",
		map[string]any{"reason": "one moved, one unknown", "revision": rev(t, body)},
		"X-Test-Role", "cashier", "X-Test-Sub", mustUUID(t)); r.status != http.StatusInternalServerError {
		t.Fatalf("the void with one card timed out = %d, want 500: %s", r.status, r.raw)
	}
	intents := f.saleIntents(t, saleID)
	var moved, unknown map[string]any
	for _, in := range intents {
		if in["refund_tx_id"] != "" {
			moved = in
		} else if in["state"] == "PENDING" {
			unknown = in
		}
	}
	if moved == nil || unknown == nil {
		t.Fatalf("the claims are %v, want one carrying the refund id and one standing without it", intents)
	}
	if got := f.expectedOf(t, sessionID, "CARD"); got != 400 {
		t.Errorf("the card figure before any resolution = %d, want 400 (both tenders still counted)", got)
	}
	if got := f.expectedOf(t, sessionID, "CASH"); got != 10199 {
		t.Errorf("the cash figure before any resolution = %d, want 10199 (the float and the cash tender)", got)
	}
	// the moved card's refund books: the card figure names the net collection
	if rr := f.resolveOn(t, moved["id"].(string), map[string]any{
		"outcome": "refund", "refund_tx_id": moved["refund_tx_id"].(string), "reason": "the terminal refunded this card",
	}, "finance"); rr.status != http.StatusOK {
		t.Fatalf("the moved tender's resolution = %d: %s", rr.status, rr.raw)
	}
	if got := f.expectedOf(t, sessionID, "CARD"); got != 200 {
		t.Errorf("the card figure with one refund booked = %d, want 200 (the 400 tendered less the 200 refunded)", got)
	}
	if got := f.expectedOf(t, sessionID, "CASH"); got != 10199 {
		t.Errorf("the cash figure with one refund booked = %d, want 10199 (a card refund pays out of no drawer)", got)
	}
	// the last claim resolves and the void completes: both figures drop with
	// the sale
	if rr := f.resolveOn(t, unknown["id"].(string), map[string]any{
		"outcome": "no_refund", "reconciliation_reference": "REC-72-T", "reason": "the terminal log shows no refund",
	}, "finance"); rr.status != http.StatusOK {
		t.Fatalf("the unknown claim's no refund = %d: %s", rr.status, rr.raw)
	}
	if got := str(t, f.getSale(t, saleID), "status"); got != "voided" {
		t.Errorf("sale status = %q, want voided (the last resolution completes the void)", got)
	}
	if got := f.expectedOf(t, sessionID, "CARD"); got != 0 {
		t.Errorf("the card figure after the void completed = %d, want 0 (the voided sale left the session's figures)", got)
	}
	if got := f.expectedOf(t, sessionID, "CASH"); got != 10000 {
		t.Errorf("the cash figure after the void completed = %d, want 10000 (the voided cash payment left the expectation)", got)
	}
	gw.mu.Lock()
	calls := len(gw.refunds)
	gw.mu.Unlock()
	if calls != 2 {
		t.Errorf("%d gateway refund calls, want 2 (the void's own two card calls)", calls)
	}
	f.assertARInvariants(t)
}
