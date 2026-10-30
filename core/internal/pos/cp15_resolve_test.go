// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package pos_test

// CP-15: a stranded void or return of a surcharged card sale, resolved with
// the recorded refund, takes the fee back out of the books as the act would.

import (
	"net/http"
	"testing"

	"github.com/gablelbm/gable/internal/testutil"
)

// A stranded void of a surcharged card sale, resolved with the recorded
// refund: the terminal was asked for goods plus fee, so the books must take
// the fee back out of 4070 and the card clearing must net to zero.
func TestCP15_StrandedVoidResolutionTakesTheFeeBack(t *testing.T) {
	testutil.LockOutboxTables(t)
	gw := &fakeGateway{}
	f := newFixture(t, testutil.RequireDB(t), withSurchargingTerminal(300, true), func(f *fixture) {
		f.service = f.service.WithGateway(gw)
	})
	saleID, total := f.cardSale(t)
	fee := f.cardTenderSurcharge(saleID)
	if fee <= 0 {
		t.Fatalf("no fee stored")
	}
	f.events.setFail("pos_transaction.voided")
	if r := f.do("POST", "/api/v1/pos/transactions/"+saleID+"/void",
		map[string]any{"reason": "stranded", "revision": num(t, f.getSale(t, saleID), "revision")},
		"X-Test-Role", "cashier", "X-Test-Sub", mustUUID(t)); r.status == http.StatusOK {
		t.Fatalf("the void committed under a failing event write: %s", r.raw)
	}
	if got := gatewayRefundTotal(gw); got != total+fee {
		t.Fatalf("the terminal was asked for %d, wanted %d", got, total+fee)
	}
	intentID, _, refundTxID, _ := pendingIntent(t, f, saleID)
	f.events.setFail("")
	r := f.resolveOn(t, intentID, map[string]any{
		"outcome": "refund", "refund_tx_id": refundTxID, "reason": "terminal log shows the refund",
	}, "finance")
	if r.status != http.StatusOK {
		t.Fatalf("resolution = %d: %s", r.status, r.raw)
	}
	if got := str(t, f.getSale(t, saleID), "status"); got != "voided" {
		t.Fatalf("status %q", got)
	}
	if got := f.accountBalance(surchargeIncomeCode); got != 0 {
		t.Errorf("surcharge income %d after the resolved void, wanted 0 (the card got %d back)", got, total+fee)
	}
	if got := f.accountBalance("1010"); got != 0 {
		t.Errorf("card clearing %d after the resolved void, wanted 0", got)
	}
}

// The same for a stranded card return: the replay goes through postReturn.
func TestCP15_StrandedReturnResolutionTakesTheFeeBack(t *testing.T) {
	testutil.LockOutboxTables(t)
	gw := &fakeGateway{}
	f := newFixture(t, testutil.RequireDB(t), withSurchargingTerminal(300, true), func(f *fixture) {
		f.service = f.service.WithGateway(gw)
	})
	saleID, total := f.cardSale(t)
	fee := f.cardTenderSurcharge(saleID)
	line := f.firstLineID(t, saleID)
	f.events.setFail("pos_return.completed")
	if r := f.cardReturn(saleID, line, "2"); r.status == http.StatusCreated {
		t.Fatalf("the return committed under a failing event write: %s", r.raw)
	}
	intentID, _, refundTxID, _ := pendingIntent(t, f, saleID)
	f.events.setFail("")
	r := f.resolveOn(t, intentID, map[string]any{
		"outcome": "refund", "refund_tx_id": refundTxID, "reason": "terminal log shows the refund",
	}, "owner")
	if r.status != http.StatusOK {
		t.Fatalf("resolution = %d: %s", r.status, r.raw)
	}
	if got := gatewayRefundTotal(gw); got != total+fee {
		t.Fatalf("the terminal was asked for %d, wanted %d", got, total+fee)
	}
	if got := f.accountBalance(surchargeIncomeCode); got != 0 {
		t.Errorf("surcharge income %d after the resolved return, wanted 0", got)
	}
	if got := f.accountBalance("1010"); got != 0 {
		t.Errorf("card clearing %d after the resolved return, wanted 0", got)
	}
}
