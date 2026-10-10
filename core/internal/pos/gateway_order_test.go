// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package pos_test

// The gateway order (ADR 0005 section 14.2 C2-5, first review P1-2, second
// review P1-5): a refusal never moves money at the terminal (the completion
// guards and reverses before it answers; the void guards under the sale row
// lock, inside its transaction, so concurrent voids make exactly one
// refund), and when a transaction still refuses a gateway success, the
// charge is put back and the refund is recorded in a committed row of its
// own carrying the gateway id.

import (
	"fmt"
	"net/http"
	"sort"
	"sync"
	"testing"

	"github.com/gablelbm/gable/internal/payment"
	"github.com/gablelbm/gable/internal/testutil"
)

// RULE: a stale revision void makes zero gateway refunds and leaves the sale
// completed.
func TestStaleRevisionVoidMakesNoGatewayRefunds(t *testing.T) {
	testutil.LockOutboxTables(t)
	gw := &fakeGateway{charges: []*payment.GatewayResult{approvedCharge()}}
	f := newFixture(t, testutil.RequireDB(t), func(f *fixture) {
		f.service = f.service.WithGateway(gw)
	})
	saleID, body := f.saleOf("4", withToken(tender("card", 2395)))
	r := f.do("POST", "/api/v1/pos/transactions/"+saleID+"/void",
		map[string]any{"reason": "stale", "revision": rev(t, body) + 99},
		"X-Test-Role", "cashier", "X-Test-Sub", mustUUID(t))
	if r.status != http.StatusConflict {
		t.Fatalf("stale void = %d, want 409: %s", r.status, r.raw)
	}
	if code, _, _ := errorOf(t, r); code != "stale_revision" {
		t.Errorf("code = %q, want stale_revision", code)
	}
	if len(gw.refunds) != 0 {
		t.Errorf("%d gateway refunds on a stale revision void, want 0", len(gw.refunds))
	}
	if got := str(t, f.getSale(t, saleID), "status"); got != "completed" {
		t.Errorf("sale status = %q, want completed", got)
	}
	if got := f.accountBalance("1010"); got != 2395 {
		t.Errorf("card balance = %d, want 2395 (the completed sale keeps its charge; no refund was made)", got)
	}
	f.assertARInvariants(t)
}

// RULE: a void whose transaction fails after the gateway refund leaves the
// money accounted for: the refund is recorded in its own committed audit row
// carrying the gateway id, while the sale stands as it was.
func TestAVoidTheTransactionRefusesRecordsItsGatewayRefund(t *testing.T) {
	testutil.LockOutboxTables(t)
	gw := &fakeGateway{charges: []*payment.GatewayResult{approvedCharge()}}
	f := newFixture(t, testutil.RequireDB(t), func(f *fixture) {
		f.service = f.service.WithGateway(gw)
		f.events.fail = "pos_transaction.voided"
	})
	saleID, body := f.saleOf("4", withToken(tender("card", 2395)))
	r := f.do("POST", "/api/v1/pos/transactions/"+saleID+"/void",
		map[string]any{"reason": "event fails", "revision": rev(t, body)},
		"X-Test-Role", "cashier", "X-Test-Sub", mustUUID(t))
	if r.status == http.StatusOK {
		t.Fatalf("the void committed under a failing event write: %s", r.raw)
	}
	if len(gw.refunds) != 1 {
		t.Fatalf("%d gateway refunds, want 1 (the refund was made before the transaction)", len(gw.refunds))
	}
	// The refund is accounted for in a row of its own, committed though the
	// void's transaction rolled back, carrying the gateway's refund id.
	n := countOf(t, f.db, `SELECT count(*) FROM audit_log WHERE action = 'pos.gateway_refund_orphaned'
		AND entity_id = $1 AND changes->>'gateway_refund_id' = 'ref-1'`, saleID)
	if n != 1 {
		t.Errorf("%d orphaned refund audit rows, want 1 carrying the gateway refund id", n)
	}
	if got := str(t, f.getSale(t, saleID), "status"); got != "completed" {
		t.Errorf("sale status = %q, want completed (the void rolled back)", got)
	}
	f.assertARInvariants(t)
}

// RULE: a completion whose transaction fails after the gateway charge gives
// the charge back (the same day void first, the refund when the void is
// refused), leaving no charge behind a sale that never was.
func TestAChargeTheTransactionRefusesIsReversed(t *testing.T) {
	testutil.LockOutboxTables(t)
	gw := &fakeGateway{charges: []*payment.GatewayResult{approvedCharge()}}
	f := newFixture(t, testutil.RequireDB(t), func(f *fixture) {
		f.service = f.service.WithGateway(gw)
		f.events.fail = "pos_transaction.completed"
	})
	saleID := f.startSale(&f.customerID)
	if r := f.addLine(saleID, f.productLine("4")); r.status != http.StatusOK {
		t.Fatalf("add line = %d: %s", r.status, r.raw)
	}
	r := f.completeSale(saleID, withToken(tender("card", 2395)))
	if r.status == http.StatusOK {
		t.Fatalf("the sale completed under a failing event write: %s", r.raw)
	}
	if gw.chargeCalls != 1 {
		t.Fatalf("%d gateway charges, want 1", gw.chargeCalls)
	}
	if len(gw.voids) != 1 {
		t.Errorf("%d gateway voids, want 1 (the charge the refused sale left behind is reversed)", len(gw.voids))
	}
	if got := countOf(t, f.db, `SELECT count(*) FROM audit_log WHERE action = 'pos.card_charge_reversal' AND entity_id = $1`, saleID); got != 1 {
		t.Errorf("%d charge reversal audit rows, want 1", got)
	}
	if got := str(t, f.getSale(t, saleID), "status"); got != "open" {
		t.Errorf("sale status = %q, want open (the completion rolled back)", got)
	}
	if got := f.stock(); got != "100.0000/0.0000" {
		t.Errorf("stock = %s, want the 100 untouched", got)
	}
	if got := countOf(t, f.db, `SELECT count(*) FROM pos_tenders WHERE transaction_id = $1`, saleID); got != 0 {
		t.Errorf("%d tenders after the refused completion, want 0", got)
	}
	f.assertARInvariants(t)
}

// cardReturnOn posts a card return of qty of the sale's first line.
func (f *fixture) cardReturnOn(t *testing.T, saleID, lineID, qty string, extra ...map[string]any) resp {
	t.Helper()
	body := map[string]any{
		"register_id": f.register, "customer_id": f.customerID.String(), "original_sale_id": saleID,
		"refund_method": "card", "reason": "card back", "lines": []map[string]any{
			{"line_id": lineID, "quantity": qty, "restock": false}},
	}
	for _, e := range extra {
		for k, v := range e {
			body[k] = v
		}
	}
	return f.do("POST", "/api/v1/pos/returns", body, "X-Test-Role", "cashier", "X-Test-Sub", mustUUID(t))
}

// RULE (third review P2-1, fourth review P2-4): a card return refunds the
// sale's own card tender's gateway transaction, never a transaction the
// client names, and never more than that tender kept net of earlier card
// refunds. A sale that was not paid by card, a sale paid with several card
// tenders, and a return that names no sale are all refused: a free card
// return has no card of the business behind it.
func TestACardReturnRefundsTheSalesOwnCardTender(t *testing.T) {
	testutil.LockOutboxTables(t)
	gw := &fakeGateway{charges: []*payment.GatewayResult{approvedCharge()}}
	f := newFixture(t, testutil.RequireDB(t), func(f *fixture) {
		f.service = f.service.WithGateway(gw)
	})
	f.openTill(0)
	saleID, body := f.saleOf("4", withToken(tender("card", 2395)))
	lineID := body.body["lines"].([]any)[0].(map[string]any)["id"].(string)

	// a client named gateway transaction is refused outright
	r := f.cardReturnOn(t, saleID, lineID, "1", map[string]any{"gateway_tx_id": "someone-elses-charge"})
	if r.status != http.StatusBadRequest {
		t.Fatalf("client gateway id = %d, want 400: %s", r.status, r.raw)
	}
	_, _, fields := errorOf(t, r)
	if len(fields) == 0 || fields[0] != "gateway_tx_id" {
		t.Errorf("fields = %v, want gateway_tx_id", fields)
	}
	if len(gw.refunds) != 0 {
		t.Fatalf("%d gateway refunds on the refused id, want 0", len(gw.refunds))
	}

	// the return refunds the sale's own charge (gw-1), one unit's money
	r = f.cardReturnOn(t, saleID, lineID, "1")
	if r.status != http.StatusCreated {
		t.Fatalf("card return = %d: %s", r.status, r.raw)
	}
	if len(gw.refunds) != 1 || gw.refunds[0] != "gw-1" {
		t.Fatalf("gateway refunds = %v, want one against the sale's own gw-1", gw.refunds)
	}
	if got := num(t, r.body, "total_cents"); got != -599 {
		t.Errorf("card return total = %d, want -599", got)
	}
	// the refund row carries the tender's own gateway transaction
	memoID := str(t, r.body, "credit_memo_id")
	if got := countOf(t, f.db, `SELECT count(*) FROM payment_refunds rf JOIN credit_memos m ON m.id = rf.credit_memo_id
		WHERE m.id = $1 AND rf.gateway_refund_id = 'gw-1'`, memoID); got != 1 {
		t.Errorf("%d refund rows naming the sale's own gateway transaction, want 1", got)
	}
	f.assertARInvariants(t)

	// a sale that was not paid by card has no card to refund
	cashSale, cashBody := f.saleOf("1", tender("cash", 599))
	cashLine := cashBody.body["lines"].([]any)[0].(map[string]any)["id"].(string)
	r = f.cardReturnOn(t, cashSale, cashLine, "1")
	if r.status != http.StatusConflict {
		t.Fatalf("card return of a cash sale = %d, want 409: %s", r.status, r.raw)
	}
	_, blockers, _ := errorOf(t, r)
	if len(blockers) == 0 || blockers[0] != "card_tender" {
		t.Errorf("blockers = %v, want card_tender", blockers)
	}
	f.assertARInvariants(t)

	// a free return (no sale named) has no card of the business behind it
	r = f.do("POST", "/api/v1/pos/returns", map[string]any{
		"register_id": f.register, "customer_id": f.customerID.String(), "refund_method": "card",
		"reason": "no receipt", "lines": []map[string]any{{
			"product_id": f.productID.String(), "quantity": "1",
			"unit_price_ten_thousandths": 55000,
		}},
	}, "X-Test-Role", "cashier", "X-Test-Sub", mustUUID(t))
	if r.status != http.StatusConflict {
		t.Fatalf("free card return = %d, want 409: %s", r.status, r.raw)
	}
	_, blockers, _ = errorOf(t, r)
	if len(blockers) == 0 || blockers[0] != "card_refund_needs_sale" {
		t.Errorf("blockers = %v, want card_refund_needs_sale", blockers)
	}
	f.assertARInvariants(t)
}

// RULE: a card return never refunds more than the sale's card tender kept
// (net of change and of earlier card refunds): a split cash and card sale
// returns its cash portion to the drawer.
func TestACardReturnIsCappedAtTheTenderItRefunds(t *testing.T) {
	testutil.LockOutboxTables(t)
	gw := &fakeGateway{charges: []*payment.GatewayResult{approvedCharge()}}
	f := newFixture(t, testutil.RequireDB(t), func(f *fixture) {
		f.service = f.service.WithGateway(gw)
	})
	f.openTill(0)
	saleID := f.startSale(&f.customerID)
	if r := f.addLine(saleID, f.productLine("4")); r.status != http.StatusOK {
		t.Fatalf("add line = %d: %s", r.status, r.raw)
	}
	// total 2395: cash 100 walks out as change, the card keeps 2295
	if r := f.completeSale(saleID, tender("cash", 100), withToken(tender("card", 2295))); r.status != http.StatusOK {
		t.Fatalf("complete = %d: %s", r.status, r.raw)
	}
	lineID := f.getSale(t, saleID)["lines"].([]any)[0].(map[string]any)["id"].(string)
	r := f.cardReturnOn(t, saleID, lineID, "4")
	if r.status != http.StatusConflict {
		t.Fatalf("card return past the tender = %d, want 409: %s", r.status, r.raw)
	}
	_, blockers, _ := errorOf(t, r)
	if len(blockers) == 0 || blockers[0] != "exceeds_card_tender" {
		t.Errorf("blockers = %v, want exceeds_card_tender", blockers)
	}
	if len(gw.refunds) != 0 {
		t.Fatalf("%d gateway refunds past the tender, want 0", len(gw.refunds))
	}
	f.assertARInvariants(t)
}

// RULE (third review P2-2, fourth review P2-3): concurrent voids of one card
// sale make exactly one gateway refund: the void takes the sale row lock
// before it calls the gateway, so the losing voids see the sale already
// voided and never move money.
func TestConcurrentVoidsMakeExactlyOneGatewayRefund(t *testing.T) {
	testutil.LockOutboxTables(t)
	gw := &fakeGateway{charges: []*payment.GatewayResult{approvedCharge()}}
	f := newFixture(t, testutil.RequireDBMaxConns(t, 4), func(f *fixture) {
		f.service = f.service.WithGateway(gw)
	})
	saleID, body := f.saleOf("4", withToken(tender("card", 2395)))
	revision := rev(t, body)
	var mu sync.Mutex
	codes := make([]int, 3)
	var wg sync.WaitGroup
	for i := 0; i < 3; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			r := f.do("POST", "/api/v1/pos/transactions/"+saleID+"/void",
				map[string]any{"reason": "double tap", "revision": revision},
				"X-Test-Role", "cashier", "X-Test-Sub", mustUUID(t))
			mu.Lock()
			codes[i] = r.status
			mu.Unlock()
		}(i)
	}
	wg.Wait()
	sort.Ints(codes)
	if codes[0] != http.StatusOK || codes[1] != http.StatusConflict || codes[2] != http.StatusConflict {
		t.Fatalf("void codes = %v, want one 200 and two 409", codes)
	}
	if len(gw.refunds) != 1 {
		t.Fatalf("%d gateway refunds for one voided sale, want exactly 1", len(gw.refunds))
	}
	if got := countOf(t, f.db, `SELECT count(*) FROM audit_log WHERE action = 'pos.gateway_refund_orphaned' AND entity_id = $1`, saleID); got != 0 {
		t.Errorf("%d orphaned refund rows, want 0 (no refund was made without its void)", got)
	}
	if got := str(t, f.getSale(t, saleID), "status"); got != "voided" {
		t.Errorf("sale status = %q, want voided", got)
	}
	if got := f.accountBalance("1010"); got != 0 {
		t.Errorf("card balance = %d, want 0 (the charge refunded whole)", got)
	}
	f.assertARInvariants(t)
}

// RULE (fourth review P1-4): a void whose second card refund fails leaves no
// gateway money unrecorded: every refund already taken is recorded in its own
// committed row, and the sale stands as it was.
func TestAVoidWhoseSecondCardRefundFailsRecordsTheFirst(t *testing.T) {
	testutil.LockOutboxTables(t)
	gw := &fakeGateway{
		charges:    []*payment.GatewayResult{approvedCharge(), approvedCharge()},
		refundErrs: []error{nil, fmt.Errorf("gateway unreachable")},
	}
	f := newFixture(t, testutil.RequireDB(t), func(f *fixture) {
		f.service = f.service.WithGateway(gw)
	})
	saleID := f.startSale(&f.customerID)
	if r := f.addLine(saleID, f.productLine("4")); r.status != http.StatusOK {
		t.Fatalf("add line = %d: %s", r.status, r.raw)
	}
	// two card tenders: 2000 + 395 for the 2395 total
	r := f.completeSale(saleID, withToken(tender("card", 2000)), withToken(tender("card", 395)))
	if r.status != http.StatusOK {
		t.Fatalf("complete = %d: %s", r.status, r.raw)
	}
	r = f.do("POST", "/api/v1/pos/transactions/"+saleID+"/void",
		map[string]any{"reason": "gateway flaky", "revision": rev(t, r)},
		"X-Test-Role", "cashier", "X-Test-Sub", mustUUID(t))
	if r.status == http.StatusOK {
		t.Fatalf("the void committed with a failed refund: %s", r.raw)
	}
	if len(gw.refunds) != 2 {
		t.Fatalf("%d gateway refund calls, want 2 (the first taken, the second failed)", len(gw.refunds))
	}
	// the refund that was taken is accounted for in its own committed row
	if got := countOf(t, f.db, `SELECT count(*) FROM audit_log WHERE action = 'pos.gateway_refund_orphaned' AND entity_id = $1`, saleID); got != 1 {
		t.Errorf("%d orphaned refund rows, want 1 (the refund the failed void left standing)", got)
	}
	if got := str(t, f.getSale(t, saleID), "status"); got != "completed" {
		t.Errorf("sale status = %q, want completed (the void rolled back)", got)
	}
	f.assertARInvariants(t)
}

// RULE (fourth review P1-3): a completion whose second card charge is
// declined reverses the charge the first tender already took: no card is
// left charged behind a sale that never was.
func TestACompletionWhoseSecondChargeIsDeclinedReversesTheFirst(t *testing.T) {
	testutil.LockOutboxTables(t)
	gw := &fakeGateway{charges: []*payment.GatewayResult{approvedCharge(), declinedCharge()}}
	f := newFixture(t, testutil.RequireDB(t), func(f *fixture) {
		f.service = f.service.WithGateway(gw)
	})
	saleID := f.startSale(&f.customerID)
	if r := f.addLine(saleID, f.productLine("4")); r.status != http.StatusOK {
		t.Fatalf("add line = %d: %s", r.status, r.raw)
	}
	r := f.completeSale(saleID, withToken(tender("card", 2000)), withToken(tender("card", 395)))
	if r.status != http.StatusConflict {
		t.Fatalf("second charge declined = %d, want 409: %s", r.status, r.raw)
	}
	_, blockers, _ := errorOf(t, r)
	if len(blockers) == 0 || blockers[0] != "card_declined" {
		t.Errorf("blockers = %v, want card_declined", blockers)
	}
	if gw.chargeCalls != 2 {
		t.Fatalf("%d gateway charges, want 2", gw.chargeCalls)
	}
	if len(gw.voids) != 1 {
		t.Errorf("%d gateway voids, want 1 (the first charge is put back)", len(gw.voids))
	}
	if got := countOf(t, f.db, `SELECT count(*) FROM audit_log WHERE action = 'pos.card_charge_reversal' AND entity_id = $1`, saleID); got != 1 {
		t.Errorf("%d charge reversal audit rows, want 1", got)
	}
	if got := str(t, f.getSale(t, saleID), "status"); got != "open" {
		t.Errorf("sale status = %q, want open (the completion never was)", got)
	}
	if got := countOf(t, f.db, `SELECT count(*) FROM pos_tenders WHERE transaction_id = $1`, saleID); got != 0 {
		t.Errorf("%d tenders after the refused completion, want 0", got)
	}
	if got := f.stock(); got != "100.0000/0.0000" {
		t.Errorf("stock = %s, want the 100 untouched", got)
	}
	f.assertARInvariants(t)
}

// RULE (third review P3-G): when neither the void nor the refund of a
// refused sale's charge works, the completion answers 502
// charge_not_reversed and the reversal row records the outcome failed.
func TestAChargeNeitherVoidNorRefundCanReverseAnswers502(t *testing.T) {
	testutil.LockOutboxTables(t)
	gw := &fakeGateway{
		charges:    []*payment.GatewayResult{approvedCharge()},
		voidErrs:   []error{fmt.Errorf("void refused")},
		refundErrs: []error{fmt.Errorf("refund refused")},
	}
	f := newFixture(t, testutil.RequireDB(t), func(f *fixture) {
		f.service = f.service.WithGateway(gw)
		f.events.fail = "pos_transaction.completed"
	})
	saleID := f.startSale(&f.customerID)
	if r := f.addLine(saleID, f.productLine("4")); r.status != http.StatusOK {
		t.Fatalf("add line = %d: %s", r.status, r.raw)
	}
	r := f.completeSale(saleID, withToken(tender("card", 2395)))
	if r.status != http.StatusBadGateway {
		t.Fatalf("unreversed charge = %d, want 502: %s", r.status, r.raw)
	}
	if code, _, _ := errorOf(t, r); code != "charge_not_reversed" {
		t.Errorf("code = %q, want charge_not_reversed", code)
	}
	if got := countOf(t, f.db, `SELECT count(*) FROM audit_log WHERE action = 'pos.card_charge_reversal'
		AND entity_id = $1 AND changes->>'outcome' = 'failed'`, saleID); got != 1 {
		t.Errorf("%d failed charge reversal rows, want 1", got)
	}
	f.assertARInvariants(t)
}
