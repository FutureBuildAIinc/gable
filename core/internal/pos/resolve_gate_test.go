// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package pos_test

// The gates the round 13 review found unproven: the void that completes only
// beside a booked refund (a void whose every claim settles with no refund
// anywhere keeps the sale completable), the card refund SQL's completed sale
// filter (one sale's booked refund must not net another sale's figure), and
// the no refund leg's session hold against the close.

import (
	"context"
	"net/http"
	"testing"

	"github.com/gablelbm/gable/internal/payment"
	"github.com/gablelbm/gable/internal/testutil"
)

// RULE (the completion gate): a void whose every claim settles without a
// booked refund keeps the sale COMPLETED (its invoice paid, its stock out),
// and a fresh void may be made: no card charge turns into credit with no
// refund anywhere.
func TestAVoidWhoseEveryClaimSettlesWithoutARefundKeepsTheSaleCompletable(t *testing.T) {
	testutil.LockOutboxTables(t)
	gw := &fakeGateway{charges: []*payment.GatewayResult{approvedCharge(), approvedCharge()},
		refundErrs: []error{context.DeadlineExceeded, nil, nil}}
	f := newFixture(t, testutil.RequireDBMaxConns(t, 4), func(f *fixture) {
		f.service = f.service.WithGateway(gw)
	})
	f.openTill(0)
	saleID, body, _ := f.twoCardSaleOf(t, "1", 300, 299)
	if r := f.do("POST", "/api/v1/pos/transactions/"+saleID+"/void",
		map[string]any{"reason": "both lost", "revision": rev(t, body)},
		"X-Test-Role", "cashier", "X-Test-Sub", mustUUID(t)); r.status != http.StatusInternalServerError {
		t.Fatalf("the void with both cards timed out = %d, want 500: %s", r.status, r.raw)
	}
	// both claims settle as no refund: no refund was ever booked
	for _, in := range f.saleIntents(t, saleID) {
		if r := f.resolveOn(t, in["id"].(string), map[string]any{
			"outcome": "no_refund", "reconciliation_reference": "REC-74", "reason": "terminal log empty",
		}, "finance"); r.status != http.StatusOK {
			t.Fatalf("the no refund of claim %v = %d: %s", in["id"], r.status, r.raw)
		}
	}
	if got := str(t, f.getSale(t, saleID), "status"); got != "completed" {
		t.Fatalf("sale status = %q, want completed (no claim booked a refund: the void did not complete)", got)
	}
	if got := f.scalar(`SELECT amount_open::text FROM invoices WHERE id = (SELECT invoice_id FROM pos_transactions WHERE id = $1)`, saleID); got != "0.00" {
		t.Errorf("the invoice's open amount = %v, want 0.00 (paid: no application was reversed)", got)
	}
	if got := f.stock(); got != "99.0000/0.0000" {
		t.Errorf("stock = %s, want 99 on hand (the sale's unit is still out)", got)
	}
	f.assertARInvariants(t)
	// a fresh void goes through: the terminal refunds both cards this time
	fresh := resp{body: f.getSale(t, saleID)}
	if r := f.do("POST", "/api/v1/pos/transactions/"+saleID+"/void",
		map[string]any{"reason": "fresh", "revision": rev(t, fresh)},
		"X-Test-Role", "cashier", "X-Test-Sub", mustUUID(t)); r.status != http.StatusOK {
		t.Fatalf("the fresh void = %d, want 200: %s", r.status, r.raw)
	}
	if got := str(t, f.getSale(t, saleID), "status"); got != "voided" {
		t.Errorf("sale status after the fresh void = %q, want voided", got)
	}
	if got := f.stock(); got != "100.0000/0.0000" {
		t.Errorf("stock after the fresh void = %s, want 100 (the unit came back)", got)
	}
	f.assertARInvariants(t)
}

// RULE (the completed sale filter): the card refunds booked against one
// sale's tenders net only that sale's figure. A second, untouched card sale
// of the same session keeps its whole CARD expectation while the first
// sale's void refund is booked, and after the first sale's void completes.
func TestOneSalesBookedRefundDoesNotNetAnotherSalesCardFigure(t *testing.T) {
	testutil.LockOutboxTables(t)
	gw := &fakeGateway{charges: []*payment.GatewayResult{approvedCharge(), approvedCharge(), approvedCharge()},
		refundErrs: []error{nil, context.DeadlineExceeded}}
	f := newFixture(t, testutil.RequireDBMaxConns(t, 4), func(f *fixture) {
		f.service = f.service.WithGateway(gw)
	})
	sessionID := f.openTill(10000)
	// the untouched sale: 599 on one card
	f.saleOf("1", withToken(tender("card", 599)))
	// the M10 shape: cash 199 with two card tenders of 200, one refunded at
	// the terminal, one timed out
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
	var moved, unknown map[string]any
	for _, in := range f.saleIntents(t, saleID) {
		if in["refund_tx_id"] != "" {
			moved = in
		} else if in["state"] == "PENDING" {
			unknown = in
		}
	}
	if got := f.expectedOf(t, sessionID, "CARD"); got != 999 {
		t.Errorf("the card figure before any resolution = %d, want 999 (599 and both 200 tenders)", got)
	}
	if r := f.resolveOn(t, moved["id"].(string), map[string]any{
		"outcome": "refund", "refund_tx_id": moved["refund_tx_id"].(string), "reason": "the terminal refunded this card",
	}, "finance"); r.status != http.StatusOK {
		t.Fatalf("the moved tender's resolution = %d: %s", r.status, r.raw)
	}
	if got := f.expectedOf(t, sessionID, "CARD"); got != 799 {
		t.Errorf("the card figure with one refund booked = %d, want 799 (999 less the 200 booked refund of the other sale's tender)", got)
	}
	// the void completes: the voided sale's refund must not follow its
	// tender out of the session's figure
	if r := f.resolveOn(t, unknown["id"].(string), map[string]any{
		"outcome": "no_refund", "reconciliation_reference": "REC-75", "reason": "the terminal log shows no refund",
	}, "finance"); r.status != http.StatusOK {
		t.Fatalf("the unknown claim's no refund = %d: %s", r.status, r.raw)
	}
	if got := str(t, f.getSale(t, saleID), "status"); got != "voided" {
		t.Fatalf("sale status = %q, want voided", got)
	}
	if got := f.expectedOf(t, sessionID, "CARD"); got != 599 {
		t.Errorf("the card figure after the void completed = %d, want 599 (the untouched sale's whole card; the voided sale's booked refund dropped with its tender)", got)
	}
	if got := f.expectedOf(t, sessionID, "CASH"); got != 10000 {
		t.Errorf("the cash figure after the void completed = %d, want 10000", got)
	}
	f.assertARInvariants(t)
}

// RULE (the no refund leg's session hold): the leg that completes a void
// from a no refund resolution holds the drawer's session row, so the close
// and the completion serialize: either the leg commits first and the close
// then counts a settled session, or the close commits first, refuses on the
// standing claim and the leg answers session_closed. No other pair, and no
// deadlock.
func TestTheNoRefundLegAndTheCloseSerializeOnTheSession(t *testing.T) {
	testutil.LockOutboxTables(t)
	gw := &fakeGateway{charges: []*payment.GatewayResult{approvedCharge(), approvedCharge()},
		refundErrs: []error{nil, context.DeadlineExceeded}}
	f := newFixture(t, testutil.RequireDBMaxConns(t, 4), func(f *fixture) {
		f.service = f.service.WithGateway(gw)
	})
	till := f.openTill(10000)
	saleID, body, _ := f.twoCardSaleOf(t, "1", 300, 299)
	if r := f.do("POST", "/api/v1/pos/transactions/"+saleID+"/void",
		map[string]any{"reason": "one moved, one unknown", "revision": rev(t, body)},
		"X-Test-Role", "cashier", "X-Test-Sub", mustUUID(t)); r.status != http.StatusInternalServerError {
		t.Fatalf("the void with one card timed out = %d, want 500: %s", r.status, r.raw)
	}
	var moved, standing map[string]any
	for _, in := range f.saleIntents(t, saleID) {
		if in["refund_tx_id"] != "" {
			moved = in
		} else if in["state"] == "PENDING" {
			standing = in
		}
	}
	if moved == nil || standing == nil {
		t.Fatal("no moved and standing claim pair to resolve")
	}
	// the moved tender's refund books first, so the no refund leg that follows
	// is the one that completes the void
	if r := f.resolveOn(t, moved["id"].(string), map[string]any{
		"outcome": "refund", "refund_tx_id": moved["refund_tx_id"].(string), "reason": "the terminal refunded this card",
	}, "finance"); r.status != http.StatusOK {
		t.Fatalf("the moved tender's resolution = %d: %s", r.status, r.raw)
	}
	legDone := make(chan resp, 1)
	closeDone := make(chan resp, 1)
	fin := make(chan struct{})
	go func() {
		defer close(fin)
		legDone <- f.resolveOn(t, standing["id"].(string), map[string]any{
			"outcome": "no_refund", "reconciliation_reference": "REC-76", "reason": "terminal log empty",
		}, "finance")
	}()
	go func() {
		closeDone <- f.do("POST", "/api/v1/pos/till/"+till+"/close",
			map[string]any{"counted_by_method": map[string]any{"cash": 10000}},
			"X-Test-Role", "cashier", "X-Test-Sub", mustUUID(t))
	}()
	leg := awaitResp(t, legDone)
	closed := awaitResp(t, closeDone)
	<-fin
	// The leg always completes (the close's refusal rolls back, so the
	// session row it read stays open under the leg's share): the pair is
	// either both 200 (the close waited for the leg and counted the settled
	// session) or the leg 200 with the close 409 refund_in_flight (the close
	// read the standing claim before the leg committed and rolled back).
	if leg.status != http.StatusOK {
		t.Fatalf("the leg = %d, want 200 (the close's refusal rolls back, the session stays open): %s", leg.status, leg.raw)
	}
	if got := str(t, f.getSale(t, saleID), "status"); got != "voided" {
		t.Errorf("sale status = %q, want voided (the leg completed the void)", got)
	}
	switch closed.status {
	case http.StatusOK:
	case http.StatusConflict:
		if _, blockers, _ := errorOf(t, closed); len(blockers) == 0 || blockers[0] != "refund_in_flight" {
			t.Errorf("the close's blockers = %v, want refund_in_flight", blockers)
		}
		// the settled session closes on the retry
		if r := f.do("POST", "/api/v1/pos/till/"+till+"/close",
			map[string]any{"counted_by_method": map[string]any{"cash": 10000}},
			"X-Test-Role", "cashier", "X-Test-Sub", mustUUID(t)); r.status != http.StatusOK {
			t.Fatalf("the close retry after the completed void = %d, want 200: %s", r.status, r.raw)
		}
	default:
		t.Fatalf("the close = %d, want 200 or 409: %s", closed.status, closed.raw)
	}
	f.assertARInvariants(t)
}
