// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package pos_test

// The refund method wall (ninth review P1-2, tenth review P1-1): every refund
// method but the card's own consults the intent table under the sale row lock
// before any money moves. A card claim PENDING at the terminal means the
// goods' money may already have left on the card, so the account must not
// take the same goods onto credit, exactly as the drawer must not pay them
// out a second time (the rule the cash method already carries).

import (
	"net/http"
	"testing"

	"github.com/gablelbm/gable/internal/payment"
	"github.com/gablelbm/gable/internal/testutil"
)

// accountReturnOn posts an account credit return of qty of the sale's first
// line.
func (f *fixture) accountReturnOn(t *testing.T, saleID, lineID, qty string) resp {
	t.Helper()
	return f.do("POST", "/api/v1/pos/returns", map[string]any{
		"register_id": f.register, "customer_id": f.customerID.String(), "original_sale_id": saleID,
		"refund_method": "account", "reason": "account credit", "lines": []map[string]any{
			{"line_id": lineID, "quantity": qty, "restock": false}},
	}, "X-Test-Role", "cashier", "X-Test-Sub", mustUUID(t))
}

// RULE: an ACCOUNT return of a sale whose card claim is PENDING at the
// terminal is refused 409 refund_in_flight: the same goods cannot sit on the
// account as credit while the card may already have paid them back. The
// parked card return then books, and the account holds no credit.
func TestAnAccountReturnWhileACardReturnIsParkedRefuses(t *testing.T) {
	testutil.LockOutboxTables(t)
	gw := &fakeGateway{charges: []*payment.GatewayResult{approvedCharge()}}
	f := newFixture(t, testutil.RequireDBMaxConns(t, 4), func(f *fixture) {
		f.service = f.service.WithGateway(gw)
	})
	parked, release := parkRefund(t, gw)
	f.openTill(0)
	saleID, body := f.saleOf("1", withToken(tender("card", 599)))
	lineID := body.body["lines"].([]any)[0].(map[string]any)["id"].(string)
	returnDone := make(chan resp, 1)
	fin := make(chan struct{})
	go func() { defer close(fin); returnDone <- f.cardReturnOn(t, saleID, lineID, "1") }()
	t.Cleanup(func() { release(); <-fin })
	awaitParked(t, parked)
	r := f.accountReturnOn(t, saleID, lineID, "1")
	if r.status != http.StatusConflict {
		t.Fatalf("the account return under a parked card claim = %d, want 409: %s", r.status, r.raw)
	}
	_, blockers, _ := errorOf(t, r)
	if len(blockers) == 0 || blockers[0] != "refund_in_flight" {
		t.Errorf("blockers = %v, want refund_in_flight (the card claim holds the sale)", blockers)
	}
	if got := f.counterMemos(t); got != 0 {
		t.Errorf("%d credit memos, want 0 (no account credit for goods the card may have refunded)", got)
	}
	if got := f.balance(); got != 0 {
		t.Errorf("customer balance = %d, want 0 (nothing left on the account)", got)
	}
	f.assertARInvariants(t)
	release()
	if rr := awaitResp(t, returnDone); rr.status != http.StatusCreated {
		t.Fatalf("the parked card return = %d: %s", rr.status, rr.raw)
	}
	gw.mu.Lock()
	refunds := len(gw.refunds)
	gw.mu.Unlock()
	if refunds != 1 {
		t.Fatalf("%d gateway refunds, want 1 (the terminal refunded the card once)", refunds)
	}
	if got := f.accountBalance("1010"); got != 0 {
		t.Errorf("card balance = %d, want 0 (599 charged, 599 refunded once)", got)
	}
	if got := countOf(t, f.db, `SELECT count(*) FROM pos_returns WHERE original_transaction_id = $1`, saleID); got != 1 {
		t.Errorf("%d returns of the sale, want 1 (the account return was refused)", got)
	}
	f.assertARInvariants(t)
}

// RULE: the refusal holds while a void's claim stands too, and the void then
// completes.
func TestAnAccountReturnWhileAVoidIsParkedRefuses(t *testing.T) {
	testutil.LockOutboxTables(t)
	gw := &fakeGateway{charges: []*payment.GatewayResult{approvedCharge()}}
	f := newFixture(t, testutil.RequireDBMaxConns(t, 4), func(f *fixture) {
		f.service = f.service.WithGateway(gw)
	})
	parked, release := parkRefund(t, gw)
	f.openTill(0)
	saleID, body := f.saleOf("1", withToken(tender("card", 599)))
	lineID := body.body["lines"].([]any)[0].(map[string]any)["id"].(string)
	voidDone := make(chan resp, 1)
	fin := make(chan struct{})
	go func() {
		defer close(fin)
		voidDone <- f.do("POST", "/api/v1/pos/transactions/"+saleID+"/void",
			map[string]any{"reason": "parked", "revision": rev(t, body)},
			"X-Test-Role", "cashier", "X-Test-Sub", mustUUID(t))
	}()
	t.Cleanup(func() { release(); <-fin })
	awaitParked(t, parked)
	r := f.accountReturnOn(t, saleID, lineID, "1")
	if r.status != http.StatusConflict {
		t.Fatalf("the account return under a parked void = %d, want 409: %s", r.status, r.raw)
	}
	_, blockers, _ := errorOf(t, r)
	if len(blockers) == 0 || blockers[0] != "refund_in_flight" {
		t.Errorf("blockers = %v, want refund_in_flight (the void's claim holds the sale)", blockers)
	}
	if got := f.counterMemos(t); got != 0 {
		t.Errorf("%d credit memos, want 0", got)
	}
	f.assertARInvariants(t)
	release()
	if rr := <-voidDone; rr.status != http.StatusOK {
		t.Fatalf("the parked void = %d: %s", rr.status, rr.raw)
	}
	if got := str(t, f.getSale(t, saleID), "status"); got != "voided" {
		t.Errorf("sale status = %q, want voided", got)
	}
	gw.mu.Lock()
	refunds := len(gw.refunds)
	gw.mu.Unlock()
	if refunds != 1 {
		t.Fatalf("%d gateway refunds, want 1", refunds)
	}
	f.assertARInvariants(t)
}
