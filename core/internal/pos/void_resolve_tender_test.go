// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package pos_test

// The per tender void resolution (ninth review P1-1, tenth review P1-2): the
// resolution's void leg replayed the void's whole money transaction, which
// reverses and refunds every card tender, so on a sale paid by two cards,
// where the terminal refunded one and declined the other, the books recorded
// a refund of both. The resolution accounts for each tender separately: it
// books only the resolved tender's refund, and the sale's void completes only
// when no claim of the sale is PENDING anymore. A tender whose refund was
// declined, or recorded as no refund, keeps its charge: its application is
// reversed and the money stays unapplied on the customer's account, the
// credit the dealer owes, never a refund the terminal did not make.

import (
	"context"
	"net/http"
	"testing"

	"github.com/gablelbm/gable/internal/payment"
	"github.com/gablelbm/gable/internal/testutil"
)

// twoCardSaleOf completes a sale of qty paid by two card tenders, cents1 and
// cents2, answering the sale id, its completion body and the first line's id.
func (f *fixture) twoCardSaleOf(t *testing.T, qty string, cents1, cents2 int64) (string, resp, string) {
	t.Helper()
	saleID := f.startSale(nil)
	if r := f.addLine(saleID, f.productLine(qty)); r.status != http.StatusOK {
		t.Fatalf("add line = %d: %s", r.status, r.raw)
	}
	r := f.completeSale(saleID, withToken(tender("card", cents1)), withToken(tender("card", cents2)))
	if r.status != http.StatusOK {
		t.Fatalf("complete = %d: %s", r.status, r.raw)
	}
	lineID := r.body["lines"].([]any)[0].(map[string]any)["id"].(string)
	return saleID, r, lineID
}

// saleIntents reads the sale's claim rows whole: id, state, refund id, tender
// id and amount, in creation order.
func (f *fixture) saleIntents(t *testing.T, saleID string) []map[string]any {
	t.Helper()
	rows, err := f.db.Pool.Query(context.Background(),
		`SELECT id::text, state, COALESCE(refund_tx_id, ''), tender_id::text, ROUND(amount * 100)::bigint
		 FROM pos_refund_intents WHERE sale_id = $1 ORDER BY created_at, id`, saleID)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var out []map[string]any
	for rows.Next() {
		var id, state, refundTxID, tenderID string
		var amount int64
		if err := rows.Scan(&id, &state, &refundTxID, &tenderID, &amount); err != nil {
			t.Fatal(err)
		}
		out = append(out, map[string]any{"id": id, "state": state, "refund_tx_id": refundTxID,
			"tender_id": tenderID, "amount": amount})
	}
	return out
}

// unappliedOf reads the tender's payment's unapplied cents.
func (f *fixture) unappliedOf(t *testing.T, tenderID string) int64 {
	t.Helper()
	var n int64
	if err := f.db.Pool.QueryRow(context.Background(),
		`SELECT ROUND(p.amount_unapplied * 100)::bigint FROM pos_tenders d JOIN payments p ON p.id = d.payment_id
		 WHERE d.id = $1`, tenderID).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

// RULE (tenth review P1-2, the M1 shape): a sale of 1 paid by two cards; the
// terminal refunded the first tender and declined the second. Resolving the
// refunded tender's claim books exactly that tender's refund: the declined
// card is never refunded in the books, its charge is reversed to unapplied
// credit the dealer owes the customer, and the sale's void completes.
func TestAVoidResolutionOfOneTenderOfTwoBooksOnlyThatTender(t *testing.T) {
	testutil.LockOutboxTables(t)
	gw := &fakeGateway{charges: []*payment.GatewayResult{approvedCharge(), approvedCharge()},
		refundStatuses: []payment.GatewayStatus{payment.GatewayStatusRefunded, payment.GatewayStatusDeclined}}
	f := newFixture(t, testutil.RequireDBMaxConns(t, 4), func(f *fixture) {
		f.service = f.service.WithGateway(gw)
	})
	f.openTill(0)
	saleID, body, _ := f.twoCardSaleOf(t, "1", 300, 299)
	r := f.do("POST", "/api/v1/pos/transactions/"+saleID+"/void",
		map[string]any{"reason": "two cards", "revision": rev(t, body)},
		"X-Test-Role", "cashier", "X-Test-Sub", mustUUID(t))
	if r.status != http.StatusConflict {
		t.Fatalf("the void with one tender declined = %d, want 409: %s", r.status, r.raw)
	}
	intents := f.saleIntents(t, saleID)
	if len(intents) != 2 {
		t.Fatalf("%d claims after the refused void, want 2", len(intents))
	}
	var refunded, declined map[string]any
	for _, in := range intents {
		if in["state"] == "PENDING" && in["refund_tx_id"] != "" {
			refunded = in
		}
		if in["state"] == "FAILED" {
			declined = in
		}
	}
	if refunded == nil || declined == nil {
		t.Fatalf("the claims are %v, want one PENDING carrying the refund id and one FAILED", intents)
	}
	paid := refunded["amount"].(int64)
	if paid != 300 && paid != 299 {
		t.Fatalf("the refunded tender's claim holds %d cents, want one of the two cards (300 or 299)", paid)
	}
	// the books move exactly the refunded card
	rr := f.resolveOn(t, refunded["id"].(string), map[string]any{
		"outcome": "refund", "refund_tx_id": refunded["refund_tx_id"].(string), "reason": "the terminal refunded this card",
	}, "finance")
	if rr.status != http.StatusOK {
		t.Fatalf("the resolution of the refunded tender = %d, want 200: %s", rr.status, rr.raw)
	}
	if got := str(t, f.getSale(t, saleID), "status"); got != "voided" {
		t.Errorf("sale status = %q, want voided (the last claim resolved: the void completes)", got)
	}
	if got := f.accountBalance("1010"); got != 599-paid {
		t.Errorf("card balance = %d, want %d (599 charged, exactly the %d the terminal refunded paid back)", got, 599-paid, paid)
	}
	if got := countOf(t, f.db, `SELECT count(*) FROM payment_refunds WHERE gateway_refund_id = $1`, refunded["refund_tx_id"]); got != 1 {
		t.Errorf("%d refund rows naming the terminal's refund, want 1", got)
	}
	if got := countOf(t, f.db, `SELECT count(*) FROM payment_refunds`); got != 1 {
		t.Errorf("%d refund rows in all, want 1 (the declined card was never refunded)", got)
	}
	// the declined tender's charge is the credit the dealer owes the customer
	if got := f.unappliedOf(t, declined["tender_id"].(string)); got != 599-paid {
		t.Errorf("the declined tender's payment holds %d unapplied cents, want %d (the credit owed, never a refund)", got, 599-paid)
	}
	if got := f.balance(); got != 0 {
		t.Errorf("customer balance = %d, want 0 (the credit sits as unapplied cash)", got)
	}
	if got := f.stock(); got != "100.0000/0.0000" {
		t.Errorf("stock = %s, want 100 (the void restocked the unit)", got)
	}
	gw.mu.Lock()
	refunds := len(gw.refunds)
	gw.mu.Unlock()
	if refunds != 2 {
		t.Fatalf("%d gateway refund calls, want 2 (the refunded one and the declined one)", refunds)
	}
	f.assertARInvariants(t)
}

// RULE (ninth review P1-1, both tenders refunded): a void whose transaction
// refused after the terminal refunded both cards leaves both claims PENDING
// with their refund ids. Each resolution books only its own tender's leg; the
// sale's void stays incomplete between them and completes with the last.
func TestTheStrandedVoidOfTwoTendersResolvesOneTenderAtATime(t *testing.T) {
	testutil.LockOutboxTables(t)
	gw := &fakeGateway{charges: []*payment.GatewayResult{approvedCharge(), approvedCharge()}}
	f := newFixture(t, testutil.RequireDBMaxConns(t, 4), func(f *fixture) {
		f.service = f.service.WithGateway(gw)
		f.events.fail = "pos_transaction.voided"
	})
	f.openTill(0)
	saleID, body, _ := f.twoCardSaleOf(t, "1", 300, 299)
	r := f.do("POST", "/api/v1/pos/transactions/"+saleID+"/void",
		map[string]any{"reason": "stranded", "revision": rev(t, body)},
		"X-Test-Role", "cashier", "X-Test-Sub", mustUUID(t))
	if r.status == http.StatusOK {
		t.Fatalf("the void committed under a failing event write: %s", r.raw)
	}
	intents := f.saleIntents(t, saleID)
	if len(intents) != 2 {
		t.Fatalf("%d claims after the stranded void, want 2", len(intents))
	}
	f.events.fail = ""
	// the first resolution books its own tender only: the sale's void waits
	rr := f.resolveOn(t, intents[0]["id"].(string), map[string]any{
		"outcome": "refund", "refund_tx_id": intents[0]["refund_tx_id"].(string), "reason": "the terminal refunded this card",
	}, "finance")
	if rr.status != http.StatusOK {
		t.Fatalf("the first tender's resolution = %d, want 200: %s", rr.status, rr.raw)
	}
	if got := str(t, f.getSale(t, saleID), "status"); got != "completed" {
		t.Errorf("sale status = %q after one tender resolved, want completed (the void waits for the other claim)", got)
	}
	if got := f.accountBalance("1010"); got != 599-intents[0]["amount"].(int64) {
		t.Errorf("card balance = %d after one tender resolved, want %d (only the first card's %d paid back)",
			got, 599-intents[0]["amount"].(int64), intents[0]["amount"].(int64))
	}
	// the cash and account routes cannot pay the goods while the claim stands
	if got := f.balance(); got != 0 {
		t.Errorf("customer balance = %d, want 0", got)
	}
	f.assertARInvariants(t)
	// the second resolution completes the void
	rr = f.resolveOn(t, intents[1]["id"].(string), map[string]any{
		"outcome": "refund", "refund_tx_id": intents[1]["refund_tx_id"].(string), "reason": "the terminal refunded this card",
	}, "finance")
	if rr.status != http.StatusOK {
		t.Fatalf("the second tender's resolution = %d, want 200: %s", rr.status, rr.raw)
	}
	if got := str(t, f.getSale(t, saleID), "status"); got != "voided" {
		t.Errorf("sale status = %q, want voided (the last claim resolved: the void completes)", got)
	}
	if got := f.accountBalance("1010"); got != 0 {
		t.Errorf("card balance = %d, want 0 (both cards refunded exactly once)", got)
	}
	if got := countOf(t, f.db, `SELECT count(*) FROM payment_refunds`); got != 2 {
		t.Errorf("%d refund rows, want 2 (one per card)", got)
	}
	gw.mu.Lock()
	refunds := len(gw.refunds)
	gw.mu.Unlock()
	if refunds != 2 {
		t.Fatalf("%d gateway refund calls, want 2 (the resolutions make none: the terminal was asked once per card)", refunds)
	}
	f.assertARInvariants(t)
}

// RULE (the half resolved void): a sale whose first tender's refund moved and
// whose second tender's call answered no verdict leaves one claim PENDING
// without an id. After the first is resolved as a refund and the second as a
// no refund, the sale stands completed with one tender's refund booked: the
// goods cannot be paid back again by any method, because the sale's void
// already refunded a card tender at the terminal.
func TestAGoodCannotBeReturnedAgainAfterAPartlyResolvedVoid(t *testing.T) {
	testutil.LockOutboxTables(t)
	gw := &fakeGateway{charges: []*payment.GatewayResult{approvedCharge(), approvedCharge()},
		refundErrs: []error{nil, context.DeadlineExceeded}}
	f := newFixture(t, testutil.RequireDBMaxConns(t, 4), func(f *fixture) {
		f.service = f.service.WithGateway(gw)
	})
	f.openTill(0)
	saleID, body, lineID := f.twoCardSaleOf(t, "1", 300, 299)
	r := f.do("POST", "/api/v1/pos/transactions/"+saleID+"/void",
		map[string]any{"reason": "one moved, one unknown", "revision": rev(t, body)},
		"X-Test-Role", "cashier", "X-Test-Sub", mustUUID(t))
	if r.status != http.StatusInternalServerError {
		t.Fatalf("the void with one tender timed out = %d, want 500: %s", r.status, r.raw)
	}
	intents := f.saleIntents(t, saleID)
	var moved, unknown map[string]any
	for _, in := range intents {
		if in["refund_tx_id"] != "" {
			moved = in
		} else {
			unknown = in
		}
	}
	if moved == nil || unknown == nil {
		t.Fatalf("the claims are %v, want one carrying the refund id and one without it", intents)
	}
	// the moved tender books its leg; the unknown claim holds the sale
	if rr := f.resolveOn(t, moved["id"].(string), map[string]any{
		"outcome": "refund", "refund_tx_id": moved["refund_tx_id"].(string), "reason": "the terminal refunded this card",
	}, "finance"); rr.status != http.StatusOK {
		t.Fatalf("the moved tender's resolution = %d: %s", rr.status, rr.raw)
	}
	if got := f.accountBalance("1010"); got != 599-moved["amount"].(int64) {
		t.Errorf("card balance = %d, want %d (exactly the moved card refunded)", got, 599-moved["amount"].(int64))
	}
	if rr := f.returnOn(t, saleID, lineID, "1"); rr.status != http.StatusConflict {
		t.Fatalf("the cash return while the unknown claim stands = %d, want 409: %s", rr.status, rr.raw)
	}
	// the terminal never moved the second card: finance records the no refund
	if rr := f.resolveOn(t, unknown["id"].(string), map[string]any{
		"outcome": "no_refund", "reconciliation_reference": "REC-72", "reason": "the terminal log shows no refund",
	}, "finance"); rr.status != http.StatusOK {
		t.Fatalf("the unknown claim's no refund = %d: %s", rr.status, rr.raw)
	}
	if got := str(t, f.getSale(t, saleID), "status"); got != "completed" {
		t.Errorf("sale status = %q, want completed (a no refund moves nothing: the void stays incomplete)", got)
	}
	// the goods were already refunded on one card: no method pays them again
	r = f.returnOn(t, saleID, lineID, "1")
	if r.status != http.StatusConflict {
		t.Fatalf("the cash return after the partly resolved void = %d, want 409: %s", r.status, r.raw)
	}
	_, blockers, _ := errorOf(t, r)
	if len(blockers) == 0 || blockers[0] != "void_refund_recorded" {
		t.Errorf("blockers = %v, want void_refund_recorded (the void already refunded a card tender)", blockers)
	}
	if rr := f.accountReturnOn(t, saleID, lineID, "1"); rr.status != http.StatusConflict {
		t.Fatalf("the account return after the partly resolved void = %d, want 409: %s", rr.status, rr.raw)
	}
	// a card return cannot pick a tender of a two card sale either
	if rr := f.cardReturnOn(t, saleID, lineID, "1"); rr.status != http.StatusConflict {
		t.Fatalf("the card return after the partly resolved void = %d, want 409: %s", rr.status, rr.raw)
	}
	if got := f.accountBalance("1010"); got != 599-moved["amount"].(int64) {
		t.Errorf("card balance = %d, want %d (nothing further moved)", got, 599-moved["amount"].(int64))
	}
	if got := f.counterReturns(t); got != 0 {
		t.Errorf("%d returns booked, want 0", got)
	}
	f.assertARInvariants(t)
}
