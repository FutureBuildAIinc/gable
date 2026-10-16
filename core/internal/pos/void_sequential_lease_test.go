// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package pos_test

// The void's sequential terminal calls and the lease they run under (the
// sixteenth review's P1-2): every claim of a multi tender void was stamped
// one lease at its insert, but the calls run one after another, each bounded
// at 30 seconds, so a later tender's call could start after the common lease
// had run out. The resolve route then released the live claim on the expired
// lease, the terminal's late answer landed on the FAILED claim, and the
// payback paid the tender a second time: the recorded refund that never
// booked did not hold the tender. The lease is now stamped per call (a
// committed write immediately before each sequential call), and a FAILED
// void claim carrying the terminal's refund id is money that moved: the
// payback refuses it (409 tender_refunded) and it is never a held credit.

import (
	"context"
	"net/http"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gablelbm/gable/internal/payment"
	"github.com/gablelbm/gable/internal/testutil"
)

// fourCardSaleOf completes a walk-in one line sale paid by four card tenders
// of the given cents, answering the sale id and body.
func (f *fixture) fourCardSaleOf(t *testing.T, cents [4]int64) (string, resp) {
	t.Helper()
	saleID := f.startSale(nil)
	if r := f.addLine(saleID, f.productLine("1")); r.status != http.StatusOK {
		t.Fatalf("add line = %d: %s", r.status, r.raw)
	}
	r := f.completeSale(saleID,
		withToken(tender("card", cents[0])), withToken(tender("card", cents[1])),
		withToken(tender("card", cents[2])), withToken(tender("card", cents[3])))
	if r.status != http.StatusOK {
		t.Fatalf("complete = %d: %s", r.status, r.raw)
	}
	return saleID, r
}

// RULE: a multi tender void's terminal calls run one after another, and each
// claim's in flight lease is stamped immediately before its own call, so a
// later tender's call never runs under a lease the earlier calls already
// spent; a resolve on a claim whose call is live is refused inside that
// lease; and once a claim is (wrongly) released and the terminal answers
// late, the recorded refund id holds the tender: the payback and the online
// refund route refuse 409 tender_refunded, the terminal is never asked
// again, and the drawer closes.
func TestAVoidsLaterTenderCallRunsUnderItsOwnLeaseAndALateAnswerNeverPaysTwice(t *testing.T) {
	testutil.LockOutboxTables(t)
	gw := &fakeGateway{charges: []*payment.GatewayResult{approvedCharge(), approvedCharge(), approvedCharge(), approvedCharge()},
		refundStatuses: []payment.GatewayStatus{payment.GatewayStatusRefunded, payment.GatewayStatusRefunded,
			payment.GatewayStatusRefunded, payment.GatewayStatusRefunded}}
	f := newFixture(t, testutil.RequireDBMaxConns(t, 4), func(f *fixture) {
		f.service = f.service.WithGateway(gw)
	})
	till := f.openTill(10000)
	// four card tenders: the first three calls take their turn at the
	// terminal, the fourth parks on it, so the common insert-time lease
	// would be spent by the earlier calls' turns (simulated below by the
	// lapse move). The calls run in the claims' order, which is the tenders'
	// read order, and the tenders share one created_at, so the parked call's
	// claim is whichever tender sorts last: the test reads that claim's own
	// charge and never assumes which one it is.
	saleID, body := f.fourCardSaleOf(t, [4]int64{150, 150, 150, 149})
	callsAfterSale := gatewayRefundCalls(t, gw)

	var refundCalls int32
	parked := make(chan struct{}, 1)
	releaseTerminal := make(chan struct{})
	gw.onRefund = func() {
		if atomic.AddInt32(&refundCalls, 1) <= 3 {
			// the first three calls take their turn at the terminal, so the
			// fourth call starts after them, never with them
			time.Sleep(300 * time.Millisecond)
			return
		}
		select {
		case parked <- struct{}{}:
		default:
		}
		<-releaseTerminal
	}
	var releaseOnce sync.Once
	release := func() { releaseOnce.Do(func() { close(releaseTerminal) }) }

	voidDone := make(chan resp, 1)
	fin := make(chan struct{})
	go func() {
		defer close(fin)
		voidDone <- f.do("POST", "/api/v1/pos/transactions/"+saleID+"/void",
			map[string]any{"reason": "the customer left the card", "revision": rev(t, body)},
			"X-Test-Role", "cashier", "X-Test-Sub", mustUUID(t))
	}()
	t.Cleanup(func() { release(); <-fin })
	select {
	case <-parked:
	case <-time.After(requestDeadline):
		t.Fatalf("the void's fourth terminal call never parked")
	}

	// the four claims stand; the first three carry their refund ids and the
	// fourth is the parked one, unanswered
	intents := f.saleIntents(t, saleID)
	if len(intents) != 4 {
		t.Fatalf("the void opened %d claims, want 4", len(intents))
	}
	var latest map[string]any
	for _, in := range intents {
		if in["state"] == "PENDING" && in["refund_tx_id"] == "" {
			latest = in
		}
	}
	if latest == nil {
		t.Fatalf("no unanswered claim among %v: want the parked fourth call's", f.saleIntents(t, saleID))
	}
	// the lease is stamped per call: the parked claim's lease sits after the
	// three earlier calls' turns, not at the common insert
	var spanSeconds float64
	if err := f.db.Pool.QueryRow(context.Background(),
		`SELECT EXTRACT(EPOCH FROM (max(answer_due_at) - min(answer_due_at))) FROM pos_refund_intents WHERE sale_id = $1 AND act = 'void'`,
		saleID).Scan(&spanSeconds); err != nil {
		t.Fatal(err)
	}
	if spanSeconds < 0.7 {
		t.Errorf("the void claims' leases span %.3f seconds, want at least the three earlier calls' turns end to end (each claim's lease is stamped immediately before its own call, never one common lease at the insert)", spanSeconds)
	}

	// a resolve on the parked claim while its call is live is refused inside
	// its own lease
	if r := f.resolveOn(t, latest["id"].(string), map[string]any{
		"outcome": "no_refund", "reconciliation_reference": "REC-LATE", "reason": "the terminal is still speaking",
	}, "finance"); r.status != http.StatusConflict {
		t.Fatalf("the resolve while the fourth call is live = %d, want 409: %s", r.status, r.raw)
	} else if _, blockers, _ := errorOf(t, r); len(blockers) == 0 || blockers[0] != "refund_in_flight" {
		t.Errorf("blockers = %v, want refund_in_flight (the live claim's own lease runs)", blockers)
	}

	// the lapse the unfixed stamping would let happen: the insert-time lease
	// runs out while the call is still on the wire (moved by hand here, the
	// stand-in for the three earlier calls' turns under one common lease)
	if _, err := f.db.Pool.Exec(context.Background(),
		`UPDATE pos_refund_intents SET answer_due_at = NOW() - INTERVAL '1 second' WHERE id = $1`, latest["id"]); err != nil {
		t.Fatal(err)
	}
	if r := f.resolveOn(t, latest["id"].(string), map[string]any{
		"outcome": "no_refund", "reconciliation_reference": "REC-LATE", "reason": "the lease moved",
	}, "finance"); r.status != http.StatusOK {
		t.Fatalf("the resolve on the lapsed lease = %d, want 200 (the release the restamp prevents): %s", r.status, r.raw)
	}

	// the terminal answers late: the refund id lands on the released claim,
	// and the void's money transaction refuses it (the claim is no longer
	// PENDING)
	release()
	vr := awaitResp(t, voidDone)
	if vr.status != http.StatusConflict {
		t.Fatalf("the void under the late answer = %d, want 409: %s", vr.status, vr.raw)
	}

	// the other three claims resolve as refunds; the last completes the void
	// and the sale stands VOIDED with the fourth tender's charge as unapplied
	// credit on its payment
	for _, in := range f.saleIntents(t, saleID) {
		if in["state"] != "PENDING" || in["refund_tx_id"] == "" {
			continue
		}
		if r := f.resolveOn(t, in["id"].(string), map[string]any{
			"outcome": "refund", "refund_tx_id": in["refund_tx_id"].(string), "reason": "the terminal log shows it",
		}, "finance"); r.status != http.StatusOK {
			t.Fatalf("the refunded claim's resolution = %d: %s", r.status, r.raw)
		}
	}
	if got := str(t, f.getSale(t, saleID), "status"); got != "voided" {
		t.Fatalf("sale status = %q, want voided", got)
	}
	charge := latest["amount"].(int64)
	credit := f.unappliedOf(t, latest["tender_id"].(string))
	if credit != charge {
		t.Fatalf("the parked tender's unapplied credit = %d, want %d (its own charge, reversed onto its payment)", credit, charge)
	}
	f.assertARInvariants(t)

	// the late answer's money moved under the released claim's refund id:
	// the credit is never a held credit and never payable again. The online
	// refund route refuses it with the tender named, and the payback refuses
	// it the same way, never reaching the terminal
	payID, payRev := f.paymentOfTender(t, latest["tender_id"].(string))
	if r := f.do("POST", "/api/v1/payments/"+payID+"/refunds", map[string]any{
		"amount_cents": credit, "reason": "the late answer already refunded this card", "revision": payRev,
	}, "X-Test-Role", "finance", "X-Test-Sub", mustUUID(t)); r.status != http.StatusConflict {
		t.Fatalf("the online refund of the refunded tender = %d, want 409: %s", r.status, r.raw)
	} else if _, blockers, _ := errorOf(t, r); len(blockers) == 0 || blockers[0] != "tender_refunded" {
		t.Errorf("blockers = %v, want tender_refunded (the recorded refund that never booked holds the tender)", blockers)
	}
	if r := f.do("POST", "/api/v1/pos/refund-intents/"+latest["id"].(string)+"/payback",
		map[string]any{"reason": "the customer is at the counter for their card"},
		"X-Test-Role", "finance", "X-Test-Sub", mustUUID(t)); r.status != http.StatusConflict {
		t.Fatalf("the payback of the refunded tender = %d, want 409: %s", r.status, r.raw)
	} else if _, blockers, _ := errorOf(t, r); len(blockers) == 0 || blockers[0] != "tender_refunded" {
		t.Errorf("blockers = %v, want tender_refunded (the terminal already refunded this tender: the card is never paid twice)", blockers)
	}
	if got := gatewayRefundCalls(t, gw); got != callsAfterSale+4 {
		t.Errorf("%d terminal refund calls after the late answer, want %d (the four void calls alone: the payback never reached the terminal)", got, callsAfterSale+4)
	}
	if got := f.unappliedOf(t, latest["tender_id"].(string)); got != credit {
		t.Errorf("the credit after the refused payback = %d, want %d (nothing was booked or spent)", got, credit)
	}
	f.assertARInvariants(t)

	// the drawer closes: the released claim settled and nothing stands
	if r := f.do("POST", "/api/v1/pos/till/"+till+"/close",
		map[string]any{"counted_by_method": map[string]any{"cash": 10000}},
		"X-Test-Role", "cashier", "X-Test-Sub", mustUUID(t)); r.status != http.StatusOK {
		t.Fatalf("the close = %d, want 200: %s", r.status, r.raw)
	}
	f.assertARInvariants(t)
}
