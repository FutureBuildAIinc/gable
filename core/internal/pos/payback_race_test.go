// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package pos_test

// The payback race (the sixteenth review's P1-1): the payback's credit read
// and its claim insert were two unlocked steps, so a second payback that read
// the credit standing could open its claim beside the first's DONE write,
// call the terminal a second time, and leave a PENDING claim carrying the
// terminal's refund id that neither resolve outcome can settle: the card was
// paid twice and the drawer never closed again. The claim now opens in a
// transaction that locks the tender's payment row and rechecks the credit
// under that lock, and the one payback per tender rule is structural (a
// partial unique index on the tender's payback claims, migration 111).

import (
	"context"
	"net/http"
	"sync"
	"testing"
	"time"

	"github.com/gablelbm/gable/internal/payment"
	"github.com/gablelbm/gable/internal/testutil"
)

// holdPaymentRow parks the tender's payment row on its own held connection
// (the row the payback's money transaction books the credit out of), so a
// payback that already wrote its claim DONE parks inside its booking. Its
// waiters(n) waits until n sessions stand blocked behind that hold, directly
// or through any depth of one another's queues (the payback's money
// transaction at the payment row, the contenders behind its sale row, one
// behind another in the lock queue), and release commits and gives the
// connection back. The wait is read on the holding connection itself, so a
// pool sized for the contenders never runs out to the probe, and it counts
// only sessions blocked by that hold or by a session blocked by it, so a
// package running beside this one against the same database never answers it.
func holdPaymentRow(t *testing.T, f *fixture, paymentID string) (waiters func(n int), release func()) {
	t.Helper()
	ctx := context.Background()
	conn, err := f.db.Pool.Acquire(ctx)
	if err != nil {
		t.Fatalf("acquire the holding connection: %v", err)
	}
	if _, err := conn.Exec(ctx, "BEGIN"); err != nil {
		t.Fatalf("begin the hold: %v", err)
	}
	if _, err := conn.Exec(ctx, "SELECT 1 FROM payments WHERE id = $1 FOR NO KEY UPDATE", paymentID); err != nil {
		t.Fatalf("lock the payment row: %v", err)
	}
	waiters = func(n int) {
		t.Helper()
		deadline := time.Now().Add(requestDeadline)
		for {
			// The statistics snapshot a session builds (pg_stat_activity
			// reads it) is cached for the transaction, and a poll every few
			// milliseconds keeps reusing it, so a contender that connects
			// after the first poll would stay invisible to the probe until
			// the transaction ends. Discard it by hand before every read,
			// so each poll sees the sessions as they stand.
			if _, err := conn.Exec(ctx, `SELECT pg_stat_clear_snapshot()`); err != nil {
				t.Fatalf("clear the statistics snapshot: %v", err)
			}
			var blocked int64
			if err := conn.QueryRow(ctx, `
				WITH RECURSIVE behind AS (
					SELECT s.pid AS blocked
					FROM pg_stat_activity s
					WHERE s.pid <> pg_backend_pid() AND s.wait_event_type = 'Lock'
						AND pg_backend_pid() = ANY(pg_blocking_pids(s.pid))
					UNION
					SELECT s.pid
					FROM pg_stat_activity s JOIN behind ON behind.blocked = ANY(pg_blocking_pids(s.pid))
					WHERE s.pid <> pg_backend_pid() AND s.wait_event_type = 'Lock'
				)
				SELECT count(*) FROM behind`).Scan(&blocked); err != nil {
				t.Fatalf("read the sessions blocked behind the hold: %v", err)
			}
			if int(blocked) >= n {
				return
			}
			if time.Now().After(deadline) {
				t.Fatalf("only %d sessions reached the payment row hold within %s, want %d", blocked, requestDeadline, n)
			}
			time.Sleep(2 * time.Millisecond)
		}
	}
	var once sync.Once
	release = func() {
		t.Helper()
		once.Do(func() {
			if _, err := conn.Exec(ctx, "COMMIT"); err != nil {
				t.Fatalf("release the payment row hold: %v", err)
			}
			conn.Release()
		})
	}
	t.Cleanup(release)
	return waiters, release
}

// startPayback fires one payback request in its own goroutine, answering its
// response on the channel and its end on fin (the test joins it in its own
// cleanup, beside the releases that unblock it, the house contention shape).
func startPayback(f *fixture, claimID string) (<-chan resp, <-chan struct{}) {
	done := make(chan resp, 1)
	fin := make(chan struct{})
	go func() {
		defer close(fin)
		done <- f.do("POST", "/api/v1/pos/refund-intents/"+claimID+"/payback",
			map[string]any{"reason": "the customer is at the counter for their card"},
			"X-Test-Role", "finance", "X-Test-Sub", mustUUID(f.t))
	}()
	return done, fin
}

// RULE (the sixteenth review's P1-1, forced interleave): the tender's payment
// row is held while the first payback's money transaction has already written
// its claim DONE and is parked at its booking; a second payback that read the
// credit standing starts behind it. Exactly one terminal call is made, one
// payback answers 200 and the other 409, no payback claim is left PENDING,
// the credit is booked once, and the drawer still closes.
func TestForcedConcurrentPaybacksPayTheCardOnce(t *testing.T) {
	testutil.LockOutboxTables(t)
	gw := &fakeGateway{charges: []*payment.GatewayResult{approvedCharge(), approvedCharge()},
		refundStatuses: []payment.GatewayStatus{payment.GatewayStatusRefunded, payment.GatewayStatusDeclined}}
	f := newFixture(t, testutil.RequireDBMaxConns(t, 4), func(f *fixture) {
		f.service = f.service.WithGateway(gw)
	})
	till := f.openTill(10000)
	_, declined, _ := declinedCreditVoid(t, f, gw, mustUUID(t))
	callsAfterVoid := gatewayRefundCalls(t, gw)
	payID, _ := f.paymentOfTender(t, declined["tender_id"].(string))

	// the first payback parks at the terminal, so its claim is committed
	// PENDING and the credit still stands when the hold is taken
	parked, releaseTerminal := parkRefund(t, gw)
	a, finA := startPayback(f, declined["id"].(string))
	awaitParked(t, parked)

	// the payment row hold: the first payback's money transaction will write
	// its claim DONE and park at its booking under it
	waiters, releaseHold := holdPaymentRow(t, f, payID)
	releaseTerminal()
	// one session waits on the hold: the payback's money transaction, past
	// its DONE write
	waiters(1)

	// the second payback starts on the credit it read standing, while the
	// first's booking is parked: it must not reach the terminal
	b, finB := startPayback(f, declined["id"].(string))
	t.Cleanup(func() { releaseHold(); releaseTerminal(); <-finA; <-finB })
	waiters(2)
	releaseHold()

	ra, rb := awaitResp(t, a), awaitResp(t, b)
	if ra.status != http.StatusOK {
		t.Fatalf("the first payback = %d, want 200: %s", ra.status, ra.raw)
	}
	if rb.status != http.StatusConflict {
		t.Fatalf("the second payback = %d, want 409: %s", rb.status, rb.raw)
	}
	_, blockers, _ := errorOf(t, rb)
	if len(blockers) == 0 || blockers[0] != "no_held_credit" {
		t.Errorf("the losing payback names %v, want no_held_credit (the credit was booked under the lock the loser rechecked)", blockers)
	}
	// the terminal was asked once for the credit, and the books took it once
	if got := gatewayRefundCalls(t, gw); got != callsAfterVoid+1 {
		t.Errorf("%d terminal refund calls after the race, want %d (exactly one payback call: the card is never paid twice)", got, callsAfterVoid+1)
	}
	if got := f.unappliedOf(t, declined["tender_id"].(string)); got != 0 {
		t.Errorf("the credit after the race = %d, want 0 (booked once)", got)
	}
	if got := countOf(t, f.db, `SELECT count(*) FROM pos_refund_intents WHERE act = 'payback' AND tender_id = $1`, declined["tender_id"]); got != 1 {
		t.Errorf("%d payback claims of the tender, want 1 (the winner's DONE claim alone)", got)
	}
	if got := countOf(t, f.db, `SELECT count(*) FROM pos_refund_intents WHERE act = 'payback' AND tender_id = $1 AND state = 'PENDING'`, declined["tender_id"]); got != 0 {
		t.Errorf("%d PENDING payback claims after the race, want 0 (no claim is left standing)", got)
	}
	f.assertARInvariants(t)

	// the drawer still closes: no claim of the session is left PENDING
	if r := f.do("POST", "/api/v1/pos/till/"+till+"/close",
		map[string]any{"counted_by_method": map[string]any{"cash": 10000}},
		"X-Test-Role", "cashier", "X-Test-Sub", mustUUID(t)); r.status != http.StatusOK {
		t.Fatalf("the close after the race = %d, want 200: %s", r.status, r.raw)
	}
	f.assertARInvariants(t)
}

// RULE (BRIEF-COMMON's transaction rule, and the sixteenth review's P2-1): a
// race of three paybacks at pool 4. The first payback's money transaction is
// parked at its booking with its claim DONE; two more paybacks that read the
// credit standing queue behind it. Exactly one terminal call is made, one
// payback answers 200 and the others 409, no payback claim is left PENDING,
// and the drawer closes afterwards.
func TestThreeConcurrentPaybacksAtPoolFourPayTheCardOnce(t *testing.T) {
	testutil.LockOutboxTables(t)
	gw := &fakeGateway{charges: []*payment.GatewayResult{approvedCharge(), approvedCharge()},
		refundStatuses: []payment.GatewayStatus{payment.GatewayStatusRefunded, payment.GatewayStatusDeclined}}
	f := newFixture(t, testutil.RequireDBMaxConns(t, 4), func(f *fixture) {
		f.service = f.service.WithGateway(gw)
	})
	till := f.openTill(10000)
	_, declined, _ := declinedCreditVoid(t, f, gw, mustUUID(t))
	callsAfterVoid := gatewayRefundCalls(t, gw)
	payID, _ := f.paymentOfTender(t, declined["tender_id"].(string))

	parked, releaseTerminal := parkRefund(t, gw)
	a, finA := startPayback(f, declined["id"].(string))
	awaitParked(t, parked)
	waiters, releaseHold := holdPaymentRow(t, f, payID)
	releaseTerminal()
	waiters(1)

	b, finB := startPayback(f, declined["id"].(string))
	c, finC := startPayback(f, declined["id"].(string))
	t.Cleanup(func() { releaseHold(); releaseTerminal(); <-finA; <-finB; <-finC })
	waiters(3)
	releaseHold()
	ra := awaitResp(t, a)
	var ok, refused, other int
	for _, r := range []resp{ra, awaitResp(t, b), awaitResp(t, c)} {
		switch {
		case r.status == http.StatusOK:
			ok++
		case r.status == http.StatusConflict:
			_, blockers, _ := errorOf(t, r)
			if len(blockers) == 0 || blockers[0] != "no_held_credit" {
				other++
				t.Errorf("a losing payback names %v, want no_held_credit: %s", blockers, r.raw)
			} else {
				refused++
			}
		default:
			other++
			t.Errorf("a contender answered %d: %s", r.status, r.raw)
		}
	}
	if ok != 1 || refused != 2 || other != 0 {
		t.Fatalf("of three contenders, %d answered 200, %d answered 409 no_held_credit and %d answered otherwise: want exactly one 200", ok, refused, other)
	}
	if got := gatewayRefundCalls(t, gw); got != callsAfterVoid+1 {
		t.Errorf("%d terminal refund calls after the race of three, want %d (exactly one payback call)", got, callsAfterVoid+1)
	}
	if got := f.unappliedOf(t, declined["tender_id"].(string)); got != 0 {
		t.Errorf("the credit after the race of three = %d, want 0", got)
	}
	if got := countOf(t, f.db, `SELECT count(*) FROM pos_refund_intents WHERE act = 'payback' AND tender_id = $1 AND state = 'PENDING'`, declined["tender_id"]); got != 0 {
		t.Errorf("%d PENDING payback claims after the race of three, want 0 (the drawer cannot close over one)", got)
	}
	f.assertARInvariants(t)

	if r := f.do("POST", "/api/v1/pos/till/"+till+"/close",
		map[string]any{"counted_by_method": map[string]any{"cash": 10000}},
		"X-Test-Role", "cashier", "X-Test-Sub", mustUUID(t)); r.status != http.StatusOK {
		t.Fatalf("the close after the race of three = %d, want 200: %s", r.status, r.raw)
	}
	f.assertARInvariants(t)
}
