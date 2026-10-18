// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package pos_test

// The crash windows of a card leg (CP-05, F2 and F5), each driven end to end
// through the counter act (complete sale, then sweep, then resolve) with a
// seam that stops the act between two statements, asserting where the
// intent, the sale, the payment, the AR subledger and the general ledger end.
// Every scripted outcome of the simulated terminal is used here: approve and
// decline in terminal_counter_test.go, no answer in the resolve scenarios,
// and crash after dispatch, duplicate answer and late answer below.

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gablelbm/gable/internal/payment"
	"github.com/gablelbm/gable/internal/pos"
	"github.com/gablelbm/gable/internal/testutil"
	"github.com/google/uuid"
)

// crashSeam is a test seam that stops the counter's card leg at one point and
// can be switched off for the retry.
type crashSeam struct{ point atomic.Value }

func (c *crashSeam) set(point string) { c.point.Store(point) }

func (c *crashSeam) hook() func(string) error {
	return func(p string) error {
		if cur, _ := c.point.Load().(string); cur != "" && cur == p {
			return errors.New("the act stopped at " + p)
		}
		return nil
	}
}

// crashFixture is a fixture whose counter act can be stopped at a point.
func crashFixture(t *testing.T, outcome payment.SimulatedOutcome, gw *fakeGateway) (*fixture, *crashSeam) {
	t.Helper()
	seam := &crashSeam{}
	lockTerminalTests(t)
	f := newFixture(t, testutil.RequireDB(t), withTerminal(outcome, 150*time.Millisecond), func(f *fixture) {
		f.service.WithTerminalCrash(seam.hook())
		if gw != nil {
			f.service.WithGateway(gw)
		}
	})
	f.openTill(0)
	return f, seam
}

// waitFor polls until the condition holds.
func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

// moneyMark is the count of documents the counter has written so far.
type moneyMark struct{ payments, invoices, entries, tenders int64 }

func (f *fixture) mark(t *testing.T, saleID string) moneyMark {
	t.Helper()
	return moneyMark{payments: f.counterPayments(t), invoices: f.counterInvoices(t), entries: f.counterEntries(t),
		tenders: countOf(t, f.db, `SELECT count(*) FROM pos_tenders WHERE transaction_id = $1`, saleID)}
}

// TestCrashAfterClaimNeverReachesTheDevice: the act dies between the
// committed claim and the dispatch move. No money moved; the drawer refuses
// while the claim stands; the sweep closes it lost with no manager item; the
// retry makes a new attempt and the sale completes.
func TestCrashAfterClaimNeverReachesTheDevice(t *testing.T) {
	f, seam := crashFixture(t, payment.SimApprove, nil)
	saleID := f.openSale(t, nil)
	seam.set(pos.TerminalPointAfterClaim)
	before := f.mark(t, saleID)
	if r := f.completeSale(saleID, f.cardAll(t, saleID)); r.status != http.StatusInternalServerError {
		t.Fatalf("the stopped act = %d, wanted the crash's 500: %s", r.status, r.raw)
	}
	id, state, _ := f.terminalIntentOf(t, saleID)
	if state != "claimed" {
		t.Fatalf("the intent is %s, wanted claimed", state)
	}
	if n := len(f.terminal.Dispatched()); n != 0 {
		t.Fatalf("the device was called %d times, wanted 0", n)
	}
	if after := f.mark(t, saleID); after != before {
		t.Fatalf("money moved before the device was reached: %+v became %+v", before, after)
	}
	if got := f.saleStatus(t, saleID); got != "OPEN" {
		t.Fatalf("the sale is %s, wanted OPEN", got)
	}
	// the drawer refuses while the claim stands
	sessionID := f.currentTill(t)
	cl := f.do("POST", "/api/v1/pos/till/"+sessionID+"/close", map[string]any{"counted_by_method": map[string]any{"cash": 0}},
		"X-Test-Role", "cashier", "X-Test-Sub", mustUUID(t))
	requireBlocker(t, cl, "terminal_intent_in_flight")
	// the sweep closes the claim lost, with no manager item
	f.expireTerminalLeases(t, saleID)
	f.sweepAsManager(t)
	if state, _, _ := f.intentFacts(t, id); state != "lost" {
		t.Fatalf("the swept claim is %s, wanted lost", state)
	}
	if n := countOf(t, f.db, `SELECT count(*) FROM payment_terminal_lost_items WHERE intent_id = $1`, id); n != 0 {
		t.Fatalf("a claim that never reached the device filed %d manager items", n)
	}
	// the retry is a new attempt and completes the sale
	seam.set("")
	if r := f.completeSale(saleID, f.cardAll(t, saleID)); r.status != http.StatusOK {
		t.Fatalf("the retry = %d: %s", r.status, r.raw)
	}
	if n := len(f.terminal.Dispatched()); n != 1 {
		t.Fatalf("the device was dispatched %d times, wanted 1", n)
	}
	if got := f.accountBalance("1010"); got != f.totalOf(t, saleID) {
		t.Fatalf("the cash and card account holds %d, wanted one charge of %d", got, f.totalOf(t, saleID))
	}
	f.assertARInvariants(t)
}

// TestCrashAfterDispatchMoveIsAManagerItem: the act dies after the dispatch
// move and before the device call. The device may have been reached as far as
// anyone can tell, so the sweep files a manager item; the manager reads the
// terminal's record (no sale), declines it, and the retry completes the sale.
func TestCrashAfterDispatchMoveIsAManagerItem(t *testing.T) {
	f, seam := crashFixture(t, payment.SimApprove, nil)
	saleID := f.openSale(t, nil)
	seam.set(pos.TerminalPointAfterDispatchMove)
	before := f.mark(t, saleID)
	if r := f.completeSale(saleID, f.cardAll(t, saleID)); r.status != http.StatusInternalServerError {
		t.Fatalf("the stopped act = %d: %s", r.status, r.raw)
	}
	id, state, _ := f.terminalIntentOf(t, saleID)
	if state != "pending_at_device" {
		t.Fatalf("the intent is %s, wanted pending_at_device", state)
	}
	if n := len(f.terminal.Dispatched()); n != 0 {
		t.Fatalf("the device was called %d times, wanted 0", n)
	}
	if after := f.mark(t, saleID); after != before {
		t.Fatalf("money moved: %+v became %+v", before, after)
	}
	f.expireTerminalLeases(t, saleID)
	f.sweepAsManager(t)
	itemID := f.itemOfSale(t, saleID)
	if r := f.resolveItem(itemID, "declined", "the terminal shows no sale"); r.status != http.StatusOK {
		t.Fatalf("the declined resolve = %d: %s", r.status, r.raw)
	}
	if state, _, _ := f.intentFacts(t, id); state != "lost" {
		t.Fatalf("the intent is %s after a declined resolve, wanted lost", state)
	}
	seam.set("")
	if r := f.completeSale(saleID, f.cardAll(t, saleID)); r.status != http.StatusOK {
		t.Fatalf("the retry after the declined resolve = %d: %s", r.status, r.raw)
	}
	if got := f.accountBalance("1010"); got != f.totalOf(t, saleID) {
		t.Fatalf("the cash and card account holds %d, wanted one charge", got)
	}
	f.assertARInvariants(t)
}

// TestCrashAfterDeviceCallKeepsTheAuthorizedLeg: the device took the leg and
// approved it; the act dies before it reads the answer. The leg is authorized
// and unspent on the open sale, nothing is booked, and the retry completes
// the sale on that leg without charging the card again.
func TestCrashAfterDeviceCallKeepsTheAuthorizedLeg(t *testing.T) {
	f, seam := crashFixture(t, payment.SimApprove, nil)
	saleID := f.openSale(t, nil)
	seam.set(pos.TerminalPointAfterDeviceCall)
	before := f.mark(t, saleID)
	if r := f.completeSale(saleID, f.cardAll(t, saleID)); r.status != http.StatusInternalServerError {
		t.Fatalf("the stopped act = %d: %s", r.status, r.raw)
	}
	id, _, _ := f.terminalIntentOf(t, saleID)
	waitFor(t, "the device's answer", func() bool {
		state, _, _ := f.intentFacts(t, id)
		return state == "authorized"
	})
	if state, spent, _ := f.intentFacts(t, id); state != "authorized" || spent {
		t.Fatalf("the leg is %s spent=%t, wanted authorized and unspent", state, spent)
	}
	if after := f.mark(t, saleID); after != before {
		t.Fatalf("money moved before the completion: %+v became %+v", before, after)
	}
	if got := f.saleStatus(t, saleID); got != "OPEN" {
		t.Fatalf("the sale is %s, wanted OPEN", got)
	}
	seam.set("")
	if r := f.completeSale(saleID, f.cardAll(t, saleID)); r.status != http.StatusOK {
		t.Fatalf("the retry = %d: %s", r.status, r.raw)
	}
	if n := len(f.terminal.Dispatched()); n != 1 {
		t.Fatalf("the device was dispatched %d times, wanted 1 (the authorized leg is reused)", n)
	}
	if state, spent, _ := f.intentFacts(t, id); state != "authorized" || !spent {
		t.Fatalf("the leg is %s spent=%t after the completion, wanted authorized and spent", state, spent)
	}
	if got := f.accountBalance("1010"); got != f.totalOf(t, saleID) {
		t.Fatalf("the cash and card account holds %d, wanted one charge", got)
	}
	f.assertARInvariants(t)
}

// TestCrashAfterAnswerBeforeTheBookingKeepsTheAuthorizedLeg: the act reads the
// authorized answer and dies before its booking transaction opens.
func TestCrashAfterAnswerBeforeTheBookingKeepsTheAuthorizedLeg(t *testing.T) {
	f, seam := crashFixture(t, payment.SimApprove, nil)
	saleID := f.openSale(t, nil)
	seam.set(pos.TerminalPointAfterAnswer)
	before := f.mark(t, saleID)
	if r := f.completeSale(saleID, f.cardAll(t, saleID)); r.status != http.StatusInternalServerError {
		t.Fatalf("the stopped act = %d: %s", r.status, r.raw)
	}
	id, state, _ := f.terminalIntentOf(t, saleID)
	if state != "authorized" {
		t.Fatalf("the intent is %s, wanted authorized", state)
	}
	if after := f.mark(t, saleID); after != before {
		t.Fatalf("money moved before the booking: %+v became %+v", before, after)
	}
	// a completion that leaves the authorized leg behind is refused
	requireBlocker(t, f.completeSale(saleID, f.cashAll(t, saleID)), "authorized_leg_unused")
	seam.set("")
	if r := f.completeSale(saleID, f.cardAll(t, saleID)); r.status != http.StatusOK {
		t.Fatalf("the retry = %d: %s", r.status, r.raw)
	}
	if n := len(f.terminal.Dispatched()); n != 1 {
		t.Fatalf("the device was dispatched %d times, wanted 1", n)
	}
	if _, spent, _ := f.intentFacts(t, id); !spent {
		t.Fatal("the leg is not spent after the completion")
	}
	f.assertARInvariants(t)
}

// TestSweepClosingTheClaimStopsTheDispatch: the sweep closes a claim whose
// lease ran out while the act is still alive; the act then finds the dispatch
// move refused and never calls the device.
func TestSweepClosingTheClaimStopsTheDispatch(t *testing.T) {
	lockTerminalTests(t)
	var f *fixture
	var saleID string
	f = newFixture(t, testutil.RequireDB(t), withTerminal(payment.SimApprove, 150*time.Millisecond), func(fx *fixture) {
		fx.service.WithTerminalCrash(func(point string) error {
			if point == pos.TerminalPointAfterClaim {
				// the lease ran out between the claim and the dispatch move
				if _, err := fx.db.Pool.Exec(context.Background(),
					`UPDATE payment_terminal_intents SET lease_until = NOW() - INTERVAL '1 minute' WHERE transaction_id = $1`, saleID); err != nil {
					return err
				}
				if _, err := payment.NewTerminalLedger(fx.db).Sweep(context.Background(), time.Now()); err != nil {
					return err
				}
			}
			return nil
		})
	})
	f.openTill(0)
	saleID = f.openSale(t, nil)
	r := f.completeSale(saleID, f.cardAll(t, saleID))
	requireBlocker(t, r, "terminal_leg_closed")
	if n := len(f.terminal.Dispatched()); n != 0 {
		t.Fatalf("the device was called %d times after the sweep closed the claim", n)
	}
	id, state, _ := f.terminalIntentOf(t, saleID)
	if state != "lost" {
		t.Fatalf("the intent is %s, wanted lost", state)
	}
	if n := countOf(t, f.db, `SELECT count(*) FROM payment_terminal_lost_items WHERE intent_id = $1`, id); n != 0 {
		t.Fatalf("the swept claim filed %d manager items", n)
	}
}

// TestSimulatedCrashAfterDispatchEndToEnd: the device recorded the leg and
// the dispatch call failed. The act waits its bound and refuses with the
// waiting state; the intent is pending_at_device, so the sweep files an item;
// the manager reads the terminal's record, authorizes it with the reference
// the terminal gives, and the completion uses the leg without a second charge.
func TestSimulatedCrashAfterDispatchEndToEnd(t *testing.T) {
	f, _ := crashFixture(t, payment.SimCrashAfterDispatch, nil)
	saleID := f.openSale(t, nil)
	r := f.completeSale(saleID, f.cardAll(t, saleID))
	requireBlocker(t, r, "terminal_answer_pending")
	id, state, _ := f.terminalIntentOf(t, saleID)
	if state != "pending_at_device" {
		t.Fatalf("the intent is %s, wanted pending_at_device (a dispatch that failed may have reached the device)", state)
	}
	if n := len(f.terminal.Dispatched()); n != 1 {
		t.Fatalf("the device was dispatched %d times, wanted 1", n)
	}
	f.expireTerminalLeases(t, saleID)
	f.sweepAsManager(t)
	itemID := f.itemOfSale(t, saleID)
	if rr := f.resolveItem(itemID, "authorized", "devref-crashed-1"); rr.status != http.StatusOK {
		t.Fatalf("the authorized resolve = %d: %s", rr.status, rr.raw)
	}
	if state, spent, ref := f.intentFacts(t, id); state != "authorized" || spent || ref != "devref-crashed-1" {
		t.Fatalf("the leg is %s spent=%t ref %q", state, spent, ref)
	}
	if done := f.completeSale(saleID, f.cardAll(t, saleID)); done.status != http.StatusOK {
		t.Fatalf("the completion on the resolved leg = %d: %s", done.status, done.raw)
	}
	if n := len(f.terminal.Dispatched()); n != 1 {
		t.Fatalf("the device was dispatched %d times, wanted 1", n)
	}
	if got := f.accountBalance("1010"); got != f.totalOf(t, saleID) {
		t.Fatalf("the cash and card account holds %d, wanted one charge", got)
	}
	f.assertARInvariants(t)
}

// TestSimulatedDuplicateAnswerEndToEnd: the device answers authorized twice;
// the second is a no-op, the intent moved once, and one payment is booked.
func TestSimulatedDuplicateAnswerEndToEnd(t *testing.T) {
	f, _ := crashFixture(t, payment.SimDuplicateAnswer, nil)
	saleID := f.openSale(t, nil)
	if r := f.completeSale(saleID, f.cardAll(t, saleID)); r.status != http.StatusOK {
		t.Fatalf("the completion = %d: %s", r.status, r.raw)
	}
	waitFor(t, "both answers", func() bool { return len(f.terminal.Outcomes()) == 2 })
	outcomes := f.terminal.Outcomes()
	applied, duplicate := 0, 0
	for _, o := range outcomes {
		switch o {
		case payment.AnswerApplied:
			applied++
		case payment.AnswerDuplicate:
			duplicate++
		}
	}
	if applied != 1 || duplicate != 1 {
		t.Fatalf("the two answers landed as %v, wanted one applied and one duplicate", outcomes)
	}
	if got := f.counterPayments(t); got != 1 {
		t.Fatalf("%d payments, wanted 1", got)
	}
	if n := countOf(t, f.db, `SELECT count(*) FROM audit_log WHERE action LIKE 'payment.terminal_answer_%' AND entity_id = $1`, saleID); n != 0 {
		t.Fatalf("a duplicate answer wrote %d answer audit rows, wanted none", n)
	}
	f.assertARInvariants(t)
}

// TestSimulatedLateAnswerEndToEnd: the device holds its answer until after
// the sweep. The sweep filed the item; the late answer lands on the lost
// intent, keeps its verdict and reference on the item, the manager resolves
// authorized without typing a reference (the device's own is used), and the
// completion uses the leg.
func TestSimulatedLateAnswerEndToEnd(t *testing.T) {
	f, _ := crashFixture(t, payment.SimLateAnswer, nil)
	saleID := f.openSale(t, nil)
	r := f.completeSale(saleID, f.cardAll(t, saleID))
	requireBlocker(t, r, "terminal_answer_pending")
	id, _, _ := f.terminalIntentOf(t, saleID)
	f.expireTerminalLeases(t, saleID)
	f.sweepAsManager(t)
	itemID := f.itemOfSale(t, saleID)
	var key string
	if err := f.db.Pool.QueryRow(context.Background(), `SELECT idempotency_key FROM payment_terminal_intents WHERE id = $1`, id).Scan(&key); err != nil {
		t.Fatal(err)
	}
	f.terminal.DeliverLate(context.Background(), key)
	waitFor(t, "the late answer", func() bool { return len(f.terminal.Outcomes()) == 1 })
	if got := f.terminal.Outcomes()[0]; got != payment.AnswerLateQueued {
		t.Fatalf("the late answer landed as %s, wanted late_queued", got)
	}
	list := f.do("GET", "/api/v1/pos/terminal-lost-items", nil, "X-Test-Role", "finance", "X-Test-Sub", mustUUID(t))
	var seen bool
	for _, raw := range list.body["items"].([]any) {
		m := raw.(map[string]any)
		if m["id"] == itemID.String() {
			seen = true
			if m["late_verdict"] != "authorized" || m["late_provider_ref"] != "simterm_"+key {
				t.Fatalf("the queue shows the late word %v / %v", m["late_verdict"], m["late_provider_ref"])
			}
		}
	}
	if !seen {
		t.Fatal("the item left the queue after the late answer")
	}
	if state, _, _ := f.intentFacts(t, id); state != "lost" {
		t.Fatalf("the late answer moved the lost intent to %s", state)
	}
	if rr := f.resolveItem(itemID, "authorized", ""); rr.status != http.StatusOK {
		t.Fatalf("the resolve without a typed reference = %d: %s", rr.status, rr.raw)
	}
	if state, spent, ref := f.intentFacts(t, id); state != "authorized" || spent || ref != "simterm_"+key {
		t.Fatalf("the leg is %s spent=%t ref %q, wanted authorized, unspent, the device's own reference", state, spent, ref)
	}
	if done := f.completeSale(saleID, f.cardAll(t, saleID)); done.status != http.StatusOK {
		t.Fatalf("the completion on the resolved leg = %d: %s", done.status, done.raw)
	}
	if n := len(f.terminal.Dispatched()); n != 1 {
		t.Fatalf("the device was dispatched %d times, wanted 1", n)
	}
	f.assertARInvariants(t)
}

// providerSpy records what state an intent is in when the device is called.
type providerSpy struct {
	inner *payment.SimulatedTerminal
	f     *fixture
	state string
}

func (p *providerSpy) Dispatch(ctx context.Context, d payment.TerminalDispatch) (payment.TerminalReceipt, error) {
	_ = p.f.db.Pool.QueryRow(context.Background(), `SELECT state FROM payment_terminal_intents WHERE id = $1`, d.IntentID).Scan(&p.state)
	return p.inner.Dispatch(ctx, d)
}

func (p *providerSpy) SettlementBatch(ctx context.Context, deviceID string, from, to time.Time) ([]payment.TerminalBatchLine, error) {
	return p.inner.SettlementBatch(ctx, deviceID, from, to)
}

// TestTheDispatchMoveIsCommittedBeforeTheDeviceIsCalled: when the device is
// called the intent already stands pending_at_device, so claimed means the
// device was never reached and the sweep may close it.
func TestTheDispatchMoveIsCommittedBeforeTheDeviceIsCalled(t *testing.T) {
	lockTerminalTests(t)
	var spy *providerSpy
	f := newFixture(t, testutil.RequireDB(t), withTerminal(payment.SimApprove, 150*time.Millisecond), func(f *fixture) {
		spy = &providerSpy{inner: f.terminal, f: f}
		ledger := payment.NewTerminalLedger(f.db)
		f.service.WithTerminal(spy, ledger)
	})
	f.openTill(0)
	saleID := f.openSale(t, nil)
	if r := f.completeSale(saleID, f.cardAll(t, saleID)); r.status != http.StatusOK {
		t.Fatalf("the completion = %d: %s", r.status, r.raw)
	}
	if spy.state != "pending_at_device" {
		t.Fatalf("the intent was %q when the device was called, wanted pending_at_device", spy.state)
	}
}

// TestAnAnswerForAClaimedIntentIsRefusedAtTheLedger: kept here beside the
// act's windows; the defect surfaces as an error, never as a queued item.
func TestAnAnswerForAClaimedIntentIsRefusedAtTheLedger(t *testing.T) {
	lockTerminalTests(t)
	f := newFixture(t, testutil.RequireDB(t), withTerminal(payment.SimNoAnswer, 150*time.Millisecond))
	saleID := f.openSale(t, nil)
	ledger := payment.NewTerminalLedger(f.db)
	var branch uuid.UUID
	var register string
	if err := f.db.Pool.QueryRow(context.Background(), `SELECT branch_id, register_id FROM pos_transactions WHERE id = $1`, saleID).Scan(&branch, &register); err != nil {
		t.Fatal(err)
	}
	in := &payment.TerminalIntent{ID: uuid.New(), TransactionID: mustParseUUID(saleID), LegKey: "card-0", RegisterID: register,
		BranchID: branch, AmountCents: 100, Kind: payment.TerminalKindSale, DeviceID: register,
		IdempotencyKey: "sale-" + saleID + "-card-0-claimed", LeaseUntil: time.Now().Add(time.Minute)}
	if err := ledger.Claim(context.Background(), in); err != nil {
		t.Fatal(err)
	}
	if _, err := ledger.ApplyDeviceAnswer(context.Background(), payment.TerminalAnswer{IntentID: in.ID, ProviderRef: "x", Verdict: "authorized"}); !errors.Is(err, payment.ErrAnswerForClaimedIntent) {
		t.Fatalf("the answer for a claimed intent = %v, wanted ErrAnswerForClaimedIntent", err)
	}
}

// TestReviewTillClosesOverAnAuthorizedUnbookedLeg: when a card sale act stops
// at TerminalPointAfterAnswer (the device authorized the charge, the sale's
// booking transaction has not opened), the intent is authorized and unspent on
// the open sale. CloseTill refuses with 409 naming the intent until the open
// sale completes on that leg.
func TestReviewTillClosesOverAnAuthorizedUnbookedLeg(t *testing.T) {
	f, seam := crashFixture(t, payment.SimApprove, nil)
	saleID := f.openSale(t, nil)
	seam.set(pos.TerminalPointAfterAnswer)
	if r := f.completeSale(saleID, f.cardAll(t, saleID)); r.status != http.StatusInternalServerError {
		t.Fatalf("stopped act = %d, wanted 500: %s", r.status, r.raw)
	}
	id, state, _ := f.terminalIntentOf(t, saleID)
	if state != "authorized" {
		t.Fatalf("intent = %s, wanted authorized", state)
	}
	if _, spent, _ := f.intentFacts(t, id); spent {
		t.Fatal("intent is already marked spent before booking")
	}
	sessionID := f.currentTill(t)
	cl := f.do("POST", "/api/v1/pos/till/"+sessionID+"/close", map[string]any{"counted_by_method": map[string]any{"cash": 0}},
		"X-Test-Role", "cashier", "X-Test-Sub", mustUUID(t))
	requireBlocker(t, cl, "terminal_intent_in_flight")
	if !strings.Contains(string(cl.raw), id.String()) {
		t.Fatalf("till close refusal does not name intent %s: %s", id, cl.raw)
	}
	seam.set("")
	if done := f.completeSale(saleID, f.cardAll(t, saleID)); done.status != http.StatusOK {
		t.Fatalf("completion on authorized leg = %d: %s", done.status, done.raw)
	}
	cl2 := f.do("POST", "/api/v1/pos/till/"+sessionID+"/close", map[string]any{"counted_by_method": map[string]any{"cash": 0, "card": f.totalOf(t, saleID)}},
		"X-Test-Role", "cashier", "X-Test-Sub", mustUUID(t))
	if cl2.status != http.StatusOK {
		t.Fatalf("till close after booking = %d: %s", cl2.status, cl2.raw)
	}
}

