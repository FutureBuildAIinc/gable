// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package pos_test

// CP-15: a stranded void or return of a surcharged card sale, resolved with
// the recorded refund, takes the fee back out of the books as the act would.

import (
	"context"
	"fmt"
	"net/http"
	"testing"

	"github.com/gablelbm/gable/internal/payment"
	"github.com/gablelbm/gable/internal/testutil"
	"github.com/google/uuid"
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

// lateFee lands the device's late approval of a lost leg with the fee it
// carried, through the ledger's own answer path.
func (f *fixture) lateFee(t *testing.T, intentID uuid.UUID, ref string, fee int64) {
	t.Helper()
	out, err := payment.NewTerminalLedger(f.db).ApplyDeviceAnswer(context.Background(), payment.TerminalAnswer{
		IntentID: intentID, ProviderRef: ref, Verdict: "authorized", Brand: "VISA", Last4: "4242", AuthCode: "LATE1", FeeCents: fee})
	if err != nil || out != payment.AnswerLateQueued {
		t.Fatalf("the late answer = %s %v, wanted late_queued", out, err)
	}
}

// A surcharged charge whose answer came late, resolved authorized on the open
// sale: the completion books the fee the late answer carried, to 4070, with
// the card clearing holding goods plus fee.
func TestCP15_ALateSurchargedAnswerResolvedOnAnOpenSaleBooksTheFee(t *testing.T) {
	f := terminalFixture(t, payment.SimNoAnswer, nil)
	saleID := f.openSale(t, nil)
	intentID, itemID := f.lostLegOf(t, saleID)
	f.lateFee(t, intentID, "LATE-FEE-1", 36)
	if r := f.resolveItem(itemID, "authorized", f.recordedRefOf(t, intentID, itemID)); r.status != http.StatusOK {
		t.Fatalf("the resolve = %d: %s", r.status, r.raw)
	}
	if r := f.completeSale(saleID, f.cardAll(t, saleID)); r.status != http.StatusOK {
		t.Fatalf("the completion on the authorized leg = %d: %s", r.status, r.raw)
	}
	if got := f.cardTenderSurcharge(saleID); got != 36 {
		t.Fatalf("the card tender stores a fee of %d, wanted the late answer's 36", got)
	}
	if got := f.accountBalance(surchargeIncomeCode); got != -36 {
		t.Fatalf("surcharge income %d, wanted a credit of 36", got)
	}
	if got, want := f.accountBalance("1010"), f.totalOf(t, saleID)+36; got != want {
		t.Fatalf("card clearing %d, wanted goods plus fee %d", got, want)
	}
}

// A held approval kept its figure above the leg; the manager resolves it
// authorized on a sale paid since in cash: the void is refused and the
// refund puts back what the card was charged, goods plus fee.
func TestCP15_AReversedSurchargedChargeGoesBackWithItsFee(t *testing.T) {
	gw := &fakeGateway{voidErrs: []error{fmt.Errorf("cannot void a settled capture")}}
	f := terminalFixture(t, payment.SimNoAnswer, gw)
	saleID := f.openSale(t, nil)
	intentID, itemID := f.lostLegOf(t, saleID)
	if err := payment.NewTerminalLedger(f.db).HoldDeviceAnswer(context.Background(), intentID, 36); err != nil {
		t.Fatalf("hold: %v", err)
	}
	if r := f.completeSale(saleID, f.cashAll(t, saleID)); r.status != http.StatusOK {
		t.Fatalf("the cash completion = %d: %s", r.status, r.raw)
	}
	if r := f.resolveItem(itemID, "authorized", f.recordedRefOf(t, intentID, itemID)); r.status != http.StatusOK {
		t.Fatalf("the resolve = %d: %s", r.status, r.raw)
	}
	if want := f.totalOf(t, saleID) + 36; len(gw.refundCents) != 1 || gw.refundCents[0] != want {
		t.Fatalf("the refund put back %v cents, wanted goods plus fee %d", gw.refundCents, want)
	}
	if got := f.accountBalance(surchargeIncomeCode); got != 0 {
		t.Fatalf("surcharge income %d after a charge put back whole, wanted 0", got)
	}
}
