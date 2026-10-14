// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package pos_test

// The proof gaps of the gateway loops (seventh review P3-2 to P3-4): the
// reversal loop of a declined later charge, the intents a successful void
// leaves, and the void claim's status recheck that refuses before the
// terminal. Every money act asserts the ledger ties after it.

import (
	"context"
	"fmt"
	"net/http"
	"testing"

	"github.com/gablelbm/gable/internal/payment"
	"github.com/gablelbm/gable/internal/testutil"
	"github.com/jackc/pgx/v5"
)

// RULE (seventh review P3-2): a completion whose third card charge is
// declined reverses the two charges the earlier tenders took, and the loop
// goes on past a reversal that fails: the first charge's void and refund both
// fail (502 charge_not_reversed), and the second charge is still put back.
func TestADeclinedLaterChargeReversesEveryEarlierCharge(t *testing.T) {
	testutil.LockOutboxTables(t)
	gw := &fakeGateway{
		charges:    []*payment.GatewayResult{approvedCharge(), approvedCharge(), declinedCharge()},
		voidErrs:   []error{fmt.Errorf("void refused")},
		refundErrs: []error{fmt.Errorf("refund refused")},
	}
	f := newFixture(t, testutil.RequireDB(t), func(f *fixture) {
		f.service = f.service.WithGateway(gw)
	})
	saleID := f.startSale(&f.customerID)
	if r := f.addLine(saleID, f.productLine("4")); r.status != http.StatusOK {
		t.Fatalf("add line = %d: %s", r.status, r.raw)
	}
	r := f.completeSale(saleID, withToken(tender("card", 1000)), withToken(tender("card", 1000)), withToken(tender("card", 395)))
	if r.status != http.StatusBadGateway {
		t.Fatalf("the completion with a declined third charge = %d, want 502: %s", r.status, r.raw)
	}
	if code, _, _ := errorOf(t, r); code != "charge_not_reversed" {
		t.Errorf("code = %q, want charge_not_reversed (the first reversal failed)", code)
	}
	gw.mu.Lock()
	voids, refunds, charges := len(gw.voids), len(gw.refunds), gw.chargeCalls
	gw.mu.Unlock()
	if charges != 3 {
		t.Errorf("%d charge calls, want 3", charges)
	}
	if voids != 2 {
		t.Errorf("%d gateway voids, want 2 (the loop goes on past the first failed reversal)", voids)
	}
	if refunds != 1 {
		t.Errorf("%d gateway refunds, want 1 (the first charge's fallback; the second charge's void went through)", refunds)
	}
	if got := countOf(t, f.db, `SELECT count(*) FROM audit_log WHERE action = 'pos.card_charge_reversal' AND entity_id = $1`, saleID); got != 2 {
		t.Errorf("%d charge reversal rows, want 2 (one per charge the declined tender found standing)", got)
	}
	if got := str(t, f.getSale(t, saleID), "status"); got != "open" {
		t.Errorf("sale status = %q, want open (the completion never happened)", got)
	}
	f.assertARInvariants(t)
}

// RULE (seventh review P3-3): a void that goes through leaves every one of its
// refund intents DONE carrying the terminal's refund id, and none PENDING.
func TestASuccessfulVoidLeavesItsIntentsDone(t *testing.T) {
	testutil.LockOutboxTables(t)
	gw := &fakeGateway{charges: []*payment.GatewayResult{approvedCharge(), approvedCharge()}}
	f := newFixture(t, testutil.RequireDB(t), func(f *fixture) {
		f.service = f.service.WithGateway(gw)
	})
	saleID := f.startSale(&f.customerID)
	if r := f.addLine(saleID, f.productLine("4")); r.status != http.StatusOK {
		t.Fatalf("add line = %d: %s", r.status, r.raw)
	}
	r := f.completeSale(saleID, withToken(tender("card", 2000)), withToken(tender("card", 395)))
	if r.status != http.StatusOK {
		t.Fatalf("complete = %d: %s", r.status, r.raw)
	}
	f.assertARInvariants(t)
	r = f.do("POST", "/api/v1/pos/transactions/"+saleID+"/void",
		map[string]any{"reason": "changed mind", "revision": rev(t, r)},
		"X-Test-Role", "cashier", "X-Test-Sub", mustUUID(t))
	if r.status != http.StatusOK {
		t.Fatalf("void = %d: %s", r.status, r.raw)
	}
	gw.mu.Lock()
	refunds := len(gw.refunds)
	gw.mu.Unlock()
	if refunds != 2 {
		t.Errorf("%d gateway refunds, want 2 (one per card tender)", refunds)
	}
	if got := countOf(t, f.db, `SELECT count(*) FROM pos_refund_intents WHERE sale_id = $1 AND act = 'void' AND state = 'DONE' AND refund_tx_id IS NOT NULL`, saleID); got != 2 {
		t.Errorf("%d DONE void intents carrying a refund id, want 2", got)
	}
	if got := countOf(t, f.db, `SELECT count(*) FROM pos_refund_intents WHERE sale_id = $1 AND state <> 'DONE'`, saleID); got != 0 {
		t.Errorf("%d intents left not DONE, want 0", got)
	}
	f.assertARInvariants(t)
}

// RULE (seventh review P3-4): the void's claim rechecks the sale's status
// under the sale row lock and refuses before the terminal. A void blocked in
// its claim while a racing void commits answers 409 and the stub gateway is
// never called. The racing act here leaves the revision as it was, so only the
// claim's status recheck can refuse it.
func TestAVoidWhoseSaleIsVoidedUnderItsClaimMakesNoGatewayCall(t *testing.T) {
	testutil.LockOutboxTables(t)
	gw := &fakeGateway{charges: []*payment.GatewayResult{approvedCharge()}}
	f := newFixture(t, testutil.RequireDBMaxConns(t, 4), func(f *fixture) {
		f.service = f.service.WithGateway(gw)
	})
	saleID, body := f.saleOf("1", withToken(tender("card", 599)))
	commit := heldSale(t, f, saleID)
	done := make(chan resp, 1)
	go func() {
		done <- f.do("POST", "/api/v1/pos/transactions/"+saleID+"/void",
			map[string]any{"reason": "second void", "revision": rev(t, body)},
			"X-Test-Role", "cashier", "X-Test-Sub", mustUUID(t))
	}()
	waitForBlockedClaim(t, f)
	commit(func(tx pgx.Tx) error {
		_, err := tx.Exec(context.Background(),
			`UPDATE pos_transactions SET status = 'VOIDED', updated_at = NOW() WHERE id = $1`, saleID)
		return err
	})
	r := awaitResp(t, done)
	if r.status != http.StatusConflict {
		t.Fatalf("the void of a sale voided under its claim = %d, want 409: %s", r.status, r.raw)
	}
	if code, _, _ := errorOf(t, r); code != "invalid_state_transition" {
		t.Errorf("code = %q, want invalid_state_transition (the claim rechecks the status under the lock)", code)
	}
	gw.mu.Lock()
	refunds := len(gw.refunds)
	gw.mu.Unlock()
	if refunds != 0 {
		t.Errorf("%d gateway refunds, want 0 (the claim refuses before the terminal)", refunds)
	}
	if got := countOf(t, f.db, `SELECT count(*) FROM pos_refund_intents WHERE sale_id = $1`, saleID); got != 0 {
		t.Errorf("%d refund intents, want 0 (the refused claim left nothing)", got)
	}
	if got := f.accountBalance("1010"); got != 599 {
		t.Errorf("card balance = %d, want 599 (nothing refunded)", got)
	}
	f.assertARInvariants(t)
}
