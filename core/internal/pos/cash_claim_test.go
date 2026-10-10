// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package pos_test

// The card claim against the drawer and the books (eighth review P2-1,
// seventh review P2-1): the refund intent serializes card refunds against
// card refunds only, so a cash return could commit while a card claim stood
// (the same goods paid twice, once at the terminal and once from the drawer),
// and the money transaction restated its total under the lock to a figure the
// terminal was never asked for, booking a cent less than the gateway took.

import (
	"context"
	"net/http"
	"sync"
	"testing"
	"time"

	"github.com/gablelbm/gable/internal/payment"
	"github.com/gablelbm/gable/internal/testutil"
	"github.com/google/uuid"
)

// parkRefund parks the stub gateway's refunds and answers a release function
// that also runs at cleanup, so a failing assertion never leaves the parked
// handler holding the test server's close.
func parkRefund(t *testing.T, gw *fakeGateway) (parked <-chan struct{}, release func()) {
	t.Helper()
	// buffered: a second refund that reaches the terminal (the very defect a
	// claim test hunts) must not block on the announce and hang the package
	p := make(chan struct{}, 16)
	r := make(chan struct{})
	gw.onRefund = func() {
		p <- struct{}{}
		<-r
	}
	var once sync.Once
	release = func() {
		once.Do(func() { close(r) })
	}
	t.Cleanup(release)
	return p, release
}

// awaitParked waits for a refund to reach the terminal, failing at the
// deadline instead of hanging the package (seventh review P3-5).
func awaitParked(t *testing.T, parked <-chan struct{}) {
	t.Helper()
	select {
	case <-parked:
	case <-time.After(requestDeadline):
		t.Fatalf("no refund reached the terminal within %s", requestDeadline)
	}
}

// awaitResp waits for a contender's answer, failing at the deadline.
func awaitResp(t *testing.T, ch <-chan resp) resp {
	t.Helper()
	select {
	case r := <-ch:
		return r
	case <-time.After(requestDeadline):
		t.Fatalf("a contender did not answer within %s", requestDeadline)
		return resp{}
	}
}

// RULE (eighth review P2-1): a cash return of a sale is refused with 409
// refund_in_flight while any card refund intent of that sale is PENDING,
// under the sale row lock, before the drawer pays: the same goods cannot be
// paid back twice. The parked card return then books exactly what its claim
// held.
func TestACashReturnWhileACardReturnIsParkedRefuses(t *testing.T) {
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
	// the card claim stands: the drawer must not pay the same unit again
	r := f.returnOn(t, saleID, lineID, "1")
	if r.status != http.StatusConflict {
		t.Fatalf("the cash return under a parked card claim = %d, want 409: %s", r.status, r.raw)
	}
	_, blockers, _ := errorOf(t, r)
	if len(blockers) == 0 || blockers[0] != "refund_in_flight" {
		t.Errorf("blockers = %v, want refund_in_flight (the card claim holds the sale)", blockers)
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
		t.Errorf("%d returns of the sale, want 1 (the cash return was refused: the goods were paid back once)", got)
	}
	f.assertARInvariants(t)
}

// RULE (eighth review P2-1): the refusal holds while a void's claim stands
// too, and the void then completes.
func TestACashReturnWhileAVoidIsParkedRefuses(t *testing.T) {
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
	r := f.returnOn(t, saleID, lineID, "1")
	if r.status != http.StatusConflict {
		t.Fatalf("the cash return under a parked void = %d, want 409: %s", r.status, r.raw)
	}
	_, blockers, _ := errorOf(t, r)
	if len(blockers) == 0 || blockers[0] != "refund_in_flight" {
		t.Errorf("blockers = %v, want refund_in_flight (the void's claim holds the sale)", blockers)
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

// RULE (eighth review P2-1, the crashed claim): a claim whose act died
// between the terminal call and its money transaction stands PENDING; the
// drawer refuses the same goods until it is reconciled.
func TestACashReturnAfterACrashedClaimRefuses(t *testing.T) {
	testutil.LockOutboxTables(t)
	gw := &fakeGateway{charges: []*payment.GatewayResult{approvedCharge()}}
	f := newFixture(t, testutil.RequireDB(t), func(f *fixture) {
		f.service = f.service.WithGateway(gw)
	})
	f.openTill(0)
	saleID, body := f.saleOf("1", withToken(tender("card", 599)))
	lineID := body.body["lines"].([]any)[0].(map[string]any)["id"].(string)
	// what a crashed card return leaves: the PENDING claim, nothing booked
	mustExec(t, f.db, `
		INSERT INTO pos_refund_intents (id, sale_id, tender_id, gateway_tx_id, amount, act, state)
		SELECT $1, t.id, d.id, d.gateway_tx_id, d.amount, 'return', 'PENDING'
		FROM pos_transactions t JOIN pos_tenders d ON d.transaction_id = t.id
		WHERE t.id = $2 AND d.method = 'CARD'`, uuid.New(), saleID)
	r := f.returnOn(t, saleID, lineID, "1")
	if r.status != http.StatusConflict {
		t.Fatalf("the cash return after a crashed card claim = %d, want 409: %s", r.status, r.raw)
	}
	_, blockers, _ := errorOf(t, r)
	if len(blockers) == 0 || blockers[0] != "refund_in_flight" {
		t.Errorf("blockers = %v, want refund_in_flight (the crashed claim holds the sale)", blockers)
	}
	if got := f.accountBalance("1010"); got != 599 {
		t.Errorf("card balance = %d, want 599 (the charge stands; the drawer paid nothing)", got)
	}
	f.assertARInvariants(t)
}

// RULE (seventh review P2-1, the lead's decision): the money transaction
// restates its total under the sale row lock, and the terminal is asked for
// the figure the lock sees: the claim restates the same way inside its own
// lock, and the money transaction refuses when its restated total differs
// from the amount the gateway took. The books never record a cent less than
// the terminal moved: the books and the terminal hold the same figure, or
// the intent stands beside the orphan row for finance.
func TestTheCardReturnBooksWhatTheGatewayTook(t *testing.T) {
	testutil.LockOutboxTables(t)
	gw := &fakeGateway{charges: []*payment.GatewayResult{approvedCharge()}}
	var refundTook int64
	f := newFixture(t, testutil.RequireDBMaxConns(t, 4), func(f *fixture) {
		f.service = f.service.WithGateway(gw)
	})
	parked, release := parkRefund(t, gw)
	f.openTill(0)
	// 4 at 5.50 less 2 cents: line 2198, tax 195, total 2393
	saleID := f.startSale(nil)
	if r := f.addLine(saleID, map[string]any{"product_id": f.productID.String(), "quantity": "4",
		"discount_cents": 2, "discount_reason": "two off"}); r.status != http.StatusOK {
		t.Fatalf("add line = %d: %s", r.status, r.raw)
	}
	if r := f.completeSale(saleID, withToken(tender("card", 2393))); r.status != http.StatusOK {
		t.Fatalf("complete = %d: %s", r.status, r.raw)
	}
	lineID := f.getSale(t, saleID)["lines"].([]any)[0].(map[string]any)["id"].(string)
	returnDone := make(chan resp, 1)
	fin := make(chan struct{})
	go func() { defer close(fin); returnDone <- f.cardReturnOn(t, saleID, lineID, "3") }()
	t.Cleanup(func() { release(); <-fin })
	awaitParked(t, parked)
	if err := f.db.Pool.QueryRow(context.Background(),
		`SELECT ROUND(amount * 100)::bigint FROM pos_refund_intents WHERE sale_id = $1 AND state = 'PENDING'`, saleID).
		Scan(&refundTook); err != nil {
		t.Fatalf("read the standing claim: %v", err)
	}
	if refundTook != 1795 {
		t.Fatalf("the claim holds %d cents, want 1795 (the figure the lock sees: 1649 of line and 146 of tax)", refundTook)
	}
	// the cash return of the last unit is refused while the claim stands
	if r := f.returnOn(t, saleID, lineID, "1"); r.status != http.StatusConflict {
		t.Fatalf("the cash return under the parked claim = %d, want 409: %s", r.status, r.raw)
	}
	release()
	rr := awaitResp(t, returnDone)
	if rr.status != http.StatusCreated {
		t.Fatalf("the parked card return = %d: %s", rr.status, rr.raw)
	}
	if got := num(t, rr.body, "total_cents"); got != -refundTook {
		t.Errorf("the card return booked %d, want %d (exactly what the terminal took)", got, -refundTook)
	}
	if got := countOf(t, f.db, `SELECT count(*) FROM audit_log WHERE action = 'pos.gateway_refund_orphaned' AND entity_id = $1`, saleID); got != 0 {
		t.Errorf("%d orphaned refund rows, want 0 (the books match the terminal)", got)
	}
	if got := f.accountBalance("1010"); got != 598 {
		t.Errorf("card balance = %d, want 598 (2393 charged, 1795 refunded once to the card)", got)
	}
	f.assertARInvariants(t)
	// the last unit still returns: the drawer pays it
	r := f.returnOn(t, saleID, lineID, "1")
	if r.status != http.StatusCreated {
		t.Fatalf("the cash return after the card return = %d: %s", r.status, r.raw)
	}
	if got := num(t, r.body, "total_cents"); got != -598 {
		t.Errorf("the completing cash return total = %d, want -598 (the line's remainder 549 and the tax's remainder 49)", got)
	}
	if got := f.accountBalance("1010"); got != 0 {
		t.Errorf("card balance = %d, want 0 (2393 in, 1795 to the card and 598 from the drawer)", got)
	}
	f.assertARInvariants(t)
}
