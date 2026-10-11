// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package pos_test

// The declined void tender's credit (r11 P3-3): a tender whose refund the
// terminal declined keeps its charge as unapplied credit owed to the
// customer, and the way back is the payment's own refund route to the card,
// through the same terminal the charge came from. The walk-in sale is the
// sharpest case (the credit lands on the shared walk-in customer), so the
// proof runs there: the completion of the void leaves the declined card's
// cents unapplied, the payment refund route pays them back to the card, and
// the books close with nothing owed.

import (
	"net/http"
	"testing"

	"github.com/gablelbm/gable/internal/payment"
	"github.com/gablelbm/gable/internal/testutil"
)

// RULE (r11 P3-3): a walk-in sale paid by two cards whose void refunded one
// and declined the other leaves the declined card's charge as unapplied
// credit on the shared walk-in customer once the void completes; the
// payment's own refund route pays it back to the card (the terminal is
// asked once for it), the unapplied cents fall to zero and the customer owes
// nothing.
func TestADeclinedVoidTendersCreditIsPaidBackToItsCard(t *testing.T) {
	testutil.LockOutboxTables(t)
	gw := &fakeGateway{charges: []*payment.GatewayResult{approvedCharge(), approvedCharge()},
		refundStatuses: []payment.GatewayStatus{payment.GatewayStatusRefunded, payment.GatewayStatusDeclined}}
	f := newFixture(t, testutil.RequireDBMaxConns(t, 4), func(f *fixture) {
		f.service = f.service.WithGateway(gw)
		f.paymentGateway = gw
	})
	f.openTill(0)
	saleID, body, _ := f.twoCardSaleOf(t, "1", 300, 299)
	if r := f.do("POST", "/api/v1/pos/transactions/"+saleID+"/void",
		map[string]any{"reason": "declined card", "revision": rev(t, body)},
		"X-Test-Role", "cashier", "X-Test-Sub", mustUUID(t)); r.status != http.StatusConflict {
		t.Fatalf("the void with one tender declined = %d, want 409: %s", r.status, r.raw)
	}
	// the refunded tender's claim stands carrying the refund id, the declined
	// tender's claim was released: resolving the standing claim completes the
	// void and leaves the declined card's charge as unapplied credit
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
	if r := f.resolveOn(t, standing["id"].(string), map[string]any{
		"outcome": "refund", "refund_tx_id": standing["refund_tx_id"].(string), "reason": "the terminal refunded this card",
	}, "finance"); r.status != http.StatusOK {
		t.Fatalf("the resolution of the refunded tender = %d, want 200: %s", r.status, r.raw)
	}
	if got := str(t, f.getSale(t, saleID), "status"); got != "voided" {
		t.Fatalf("sale status = %q, want voided", got)
	}
	// which tender the terminal declined follows the tenders' own order, so
	// the credit is read from the declined claim's own amount
	credit := f.unappliedOf(t, declined["tender_id"].(string))
	owed := declined["amount"].(int64)
	if credit != owed {
		t.Fatalf("the declined tender's payment holds %d unapplied cents, want %d (the credit owed)", credit, owed)
	}

	// the way back: the payment's own refund route pays the credit to the card
	var paymentID string
	var revision int64
	if err := f.db.Pool.QueryRow(t.Context(),
		`SELECT p.id::text, p.revision FROM pos_tenders d JOIN payments p ON p.id = d.payment_id WHERE d.id = $1`,
		declined["tender_id"].(string)).Scan(&paymentID, &revision); err != nil {
		t.Fatalf("read the declined tender's payment: %v", err)
	}
	r := f.do("POST", "/api/v1/payments/"+paymentID+"/refunds", map[string]any{
		"amount_cents": credit, "reason": "the declined tender's credit back to its card", "revision": revision,
	}, "X-Test-Role", "finance", "X-Test-Sub", mustUUID(t))
	if r.status != http.StatusOK && r.status != http.StatusCreated {
		t.Fatalf("the payment refund of the credit = %d, want 200: %s", r.status, r.raw)
	}
	if got := f.unappliedOf(t, declined["tender_id"].(string)); got != 0 {
		t.Errorf("the declined tender's payment holds %d unapplied cents after the payback, want 0", got)
	}
	if got := f.accountBalance("1010"); got != 0 {
		t.Errorf("card balance = %d, want 0 (599 charged, 300 refunded by the void's resolution, 299 paid back to the card)", got)
	}
	if got := f.walkInBalance(t); got != 0 {
		t.Errorf("walk-in balance = %d, want 0 (nothing owed, nothing held on the customer)", got)
	}
	if got := countOf(t, f.db, `SELECT count(*) FROM payment_refunds`); got != 2 {
		t.Errorf("%d refund rows, want 2 (the void's booked refund and the credit's payback)", got)
	}
	gw.mu.Lock()
	calls := len(gw.refunds)
	gw.mu.Unlock()
	if calls != 3 {
		t.Errorf("%d gateway refund calls, want 3 (the void's two, the payback's one)", calls)
	}
	f.assertARInvariants(t)
}
