// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package pos_test

// The manager's resolve acts for a lost terminal answer (CP-05, F4): every
// act claims the item first, and the money follows today's paths. Each
// scenario runs the real counter act (complete sale, sweep, resolve) and
// asserts where the intent, the sale, the payment, the AR subledger and the
// general ledger end.

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gablelbm/gable/internal/payment"
	"github.com/gablelbm/gable/internal/pos"
	"github.com/gablelbm/gable/internal/testutil"
	"github.com/google/uuid"
)

// lockTerminalTests serialises the calling test against every suite that
// writes the outbox or sweeps the terminal tables.
func lockTerminalTests(t *testing.T) {
	t.Helper()
	testutil.LockOutboxTables(t)
	testutil.LockTerminalTables(t)
}

// totalOf reads a sale's total in cents.
func (f *fixture) totalOf(t *testing.T, saleID string) int64 {
	t.Helper()
	return countOf(t, f.db, `SELECT ROUND(total * 100)::bigint FROM pos_transactions WHERE id = $1`, saleID)
}

// cardAll is a card tender of the sale's whole total.
func (f *fixture) cardAll(t *testing.T, saleID string) map[string]any {
	t.Helper()
	return tender("card", f.totalOf(t, saleID))
}

// cashAll is a cash tender of the sale's whole total.
func (f *fixture) cashAll(t *testing.T, saleID string) map[string]any {
	t.Helper()
	return tender("cash", f.totalOf(t, saleID))
}

// expireTerminalLeases lapses every lease of the sale's intents, so the sweep
// decides them.
func (f *fixture) expireTerminalLeases(t *testing.T, saleID string) {
	t.Helper()
	mustExec(t, f.db, `UPDATE payment_terminal_intents SET lease_until = NOW() - INTERVAL '1 minute' WHERE transaction_id = $1`, saleID)
	mustExec(t, f.db, `UPDATE pos_refund_intents SET answer_due_at = NOW() - INTERVAL '1 minute' WHERE sale_id = $1`, saleID)
}

// sweepAsManager runs the sweep route as a manager.
func (f *fixture) sweepAsManager(t *testing.T) {
	t.Helper()
	if r := f.do("POST", "/api/v1/pos/terminal-intents/sweep", nil, "X-Test-Role", "finance", "X-Test-Sub", mustUUID(t)); r.status != http.StatusOK {
		t.Fatalf("the sweep = %d: %s", r.status, r.raw)
	}
}

// itemOfSale reads the open manager item of a sale's lost leg.
func (f *fixture) itemOfSale(t *testing.T, saleID string) (itemID uuid.UUID) {
	t.Helper()
	r := f.do("GET", "/api/v1/pos/terminal-lost-items", nil, "X-Test-Role", "finance", "X-Test-Sub", mustUUID(t))
	if r.status != http.StatusOK {
		t.Fatalf("the queue = %d: %s", r.status, r.raw)
	}
	for _, raw := range r.body["items"].([]any) {
		m := raw.(map[string]any)
		if m["transaction_id"] == saleID {
			return mustParseUUID(m["id"].(string))
		}
	}
	t.Fatalf("the queue holds no item of sale %s: %s", saleID, r.raw)
	return uuid.Nil
}

// lostLegOf runs the counter act for a card leg the device never answers:
// the completion refuses with the waiting state, the lease lapses, the
// manager's sweep files the item. It answers the intent and the item.
func (f *fixture) lostLegOf(t *testing.T, saleID string) (intentID, itemID uuid.UUID) {
	t.Helper()
	r := f.completeSale(saleID, f.cardAll(t, saleID))
	requireBlocker(t, r, "terminal_answer_pending")
	intentID, state, _ := f.terminalIntentOf(t, saleID)
	if state != "pending_at_device" {
		t.Fatalf("the unanswered intent is %s, wanted pending_at_device", state)
	}
	f.expireTerminalLeases(t, saleID)
	f.sweepAsManager(t)
	return intentID, f.itemOfSale(t, saleID)
}

func (f *fixture) resolveItem(itemID uuid.UUID, outcome, reference string) resp {
	f.t.Helper()
	return f.do("POST", "/api/v1/pos/terminal-lost-items/"+itemID.String()+"/resolve",
		map[string]any{"outcome": outcome, "reference": reference, "reason": "read the terminal's own record"},
		"X-Test-Role", "finance", "X-Test-Sub", mustUUID(f.t))
}

// intentFacts reads where an intent stands.
func (f *fixture) intentFacts(t *testing.T, id uuid.UUID) (state string, spent bool, answerRef string) {
	t.Helper()
	if err := f.db.Pool.QueryRow(context.Background(),
		`SELECT state, spent_at IS NOT NULL, answer_provider_ref FROM payment_terminal_intents WHERE id = $1`, id).
		Scan(&state, &spent, &answerRef); err != nil {
		t.Fatal(err)
	}
	return state, spent, answerRef
}

func (f *fixture) saleStatus(t *testing.T, saleID string) string {
	t.Helper()
	var s string
	if err := f.db.Pool.QueryRow(context.Background(), `SELECT status FROM pos_transactions WHERE id = $1`, saleID).Scan(&s); err != nil {
		t.Fatal(err)
	}
	return s
}

func (f *fixture) cardTendersOf(t *testing.T, saleID string) int64 {
	t.Helper()
	return countOf(t, f.db, `SELECT count(*) FROM pos_tenders WHERE transaction_id = $1 AND method = 'CARD'`, saleID)
}

func (f *fixture) saleInvoiceOpen(t *testing.T, saleID string) int64 {
	t.Helper()
	return countOf(t, f.db, `SELECT ROUND(i.amount_open * 100)::bigint FROM invoices i JOIN pos_transactions p ON p.invoice_id = i.id WHERE p.id = $1`, saleID)
}

func itemStateOf(t *testing.T, f *fixture, id uuid.UUID) string {
	t.Helper()
	var s string
	if err := f.db.Pool.QueryRow(context.Background(), `SELECT state FROM payment_terminal_lost_items WHERE id = $1`, id).Scan(&s); err != nil {
		t.Fatal(err)
	}
	return s
}

// terminalFixture is a fixture with the simulated terminal that never
// answers, a gateway to put charges back through, and an open drawer.
func terminalFixture(t *testing.T, outcome payment.SimulatedOutcome, gw *fakeGateway) *fixture {
	t.Helper()
	lockTerminalTests(t)
	f := newFixture(t, testutil.RequireDB(t), withTerminal(outcome, 150*time.Millisecond), func(f *fixture) {
		if gw != nil {
			f.service.WithGateway(gw)
		}
	})
	f.openTill(0)
	return f
}

func (f *fixture) openSale(t *testing.T, customer *uuid.UUID) string {
	t.Helper()
	saleID := f.startSale(customer)
	if r := f.addLine(saleID, f.productLine("1")); r.status != http.StatusOK {
		t.Fatalf("add line = %d: %s", r.status, r.raw)
	}
	return saleID
}

// TestResolveAuthorizedOnOpenSaleKeepsTheLegForTheCompletion: the device took
// the money and the sale is still open: the authorized leg stays on the sale,
// and its completion uses that tender and never charges the card again.
func TestResolveAuthorizedOnOpenSaleKeepsTheLegForTheCompletion(t *testing.T) {
	f := terminalFixture(t, payment.SimNoAnswer, nil)
	saleID := f.openSale(t, nil)
	intentID, itemID := f.lostLegOf(t, saleID)

	r := f.resolveItem(itemID, "authorized", "devref-open-1")
	if r.status != http.StatusOK || str(t, r.body, "state") != "resolved_authorized" {
		t.Fatalf("the resolve = %d: %s", r.status, r.raw)
	}
	state, spent, ref := f.intentFacts(t, intentID)
	if state != "authorized" || spent || ref != "devref-open-1" {
		t.Fatalf("the leg is %s spent=%t ref %q, wanted authorized, unspent, the manager's reference", state, spent, ref)
	}
	if got := f.saleStatus(t, saleID); got != "OPEN" {
		t.Fatalf("the sale is %s, wanted it still OPEN", got)
	}
	if got := f.counterPayments(t); got != 0 {
		t.Fatalf("%d payments booked before the completion, wanted 0", got)
	}
	// a second resolve is refused and moves nothing
	requireBlocker(t, f.resolveItem(itemID, "authorized", "devref-open-1"), "lost_item_settled")
	// completing with cash would leave the authorized card leg behind: refused
	requireBlocker(t, f.completeSale(saleID, f.cashAll(t, saleID)), "authorized_leg_unused")
	// the completion with the card tender uses the leg
	done := f.completeSale(saleID, f.cardAll(t, saleID))
	if done.status != http.StatusOK {
		t.Fatalf("the completion on the authorized leg = %d: %s", done.status, done.raw)
	}
	if n := len(f.terminal.Dispatched()); n != 1 {
		t.Fatalf("the device was dispatched %d times, wanted 1 (never charged again)", n)
	}
	if got := f.saleStatus(t, saleID); got != "COMPLETED" {
		t.Fatalf("the sale is %s, wanted COMPLETED", got)
	}
	var gwRef string
	if err := f.db.Pool.QueryRow(context.Background(),
		`SELECT gateway_tx_id FROM pos_tenders WHERE transaction_id = $1 AND method = 'CARD'`, saleID).Scan(&gwRef); err != nil || gwRef != "devref-open-1" {
		t.Fatalf("the card tender carries gateway ref %q (%v), wanted the manager's reference", gwRef, err)
	}
	if state, spent, _ := f.intentFacts(t, intentID); state != "authorized" || !spent {
		t.Fatalf("the leg after the completion is %s spent=%t, wanted authorized and spent", state, spent)
	}
	if got := f.saleInvoiceOpen(t, saleID); got != 0 {
		t.Fatalf("the invoice still owes %d cents", got)
	}
	if got := f.counterPayments(t); got != 1 {
		t.Fatalf("%d payments, wanted the one card payment", got)
	}
	if got := f.accountBalance("1010"); got != f.totalOf(t, saleID) {
		t.Fatalf("the cash and card account holds %d, wanted the sale's total %d", got, f.totalOf(t, saleID))
	}
	f.assertARInvariants(t)
}

// TestResolveAuthorizedOnSalePaidInFullReversesTheWholeCharge: the sale was
// completed with cash while the card answer was lost; the device's charge
// goes back whole (void first), once, and nothing is booked as a card
// payment.
func TestResolveAuthorizedOnSalePaidInFullReversesTheWholeCharge(t *testing.T) {
	gw := &fakeGateway{}
	f := terminalFixture(t, payment.SimNoAnswer, gw)
	saleID := f.openSale(t, nil)
	intentID, itemID := f.lostLegOf(t, saleID)
	if r := f.completeSale(saleID, f.cashAll(t, saleID)); r.status != http.StatusOK {
		t.Fatalf("the cash completion = %d: %s", r.status, r.raw)
	}
	r := f.resolveItem(itemID, "authorized", "devref-full-1")
	if r.status != http.StatusOK || str(t, r.body, "state") != "resolved_authorized" {
		t.Fatalf("the resolve = %d: %s", r.status, r.raw)
	}
	if len(gw.voids) != 1 || gw.voids[0] != "devref-full-1" || len(gw.refunds) != 0 {
		t.Fatalf("the charge went back through voids %v refunds %v, wanted one void of the manager's reference", gw.voids, gw.refunds)
	}
	if got := f.cardTendersOf(t, saleID); got != 0 {
		t.Fatalf("%d card tenders booked on a sale paid in cash", got)
	}
	if got := f.counterPayments(t); got != 1 {
		t.Fatalf("%d payments, wanted only the cash payment", got)
	}
	if state, spent, _ := f.intentFacts(t, intentID); state != "authorized" || !spent {
		t.Fatalf("the leg is %s spent=%t, wanted authorized and spent (put back)", state, spent)
	}
	// a second resolve is a conflict and the gateway is not called again
	requireBlocker(t, f.resolveItem(itemID, "authorized", "devref-full-1"), "lost_item_settled")
	if len(gw.voids) != 1 {
		t.Fatalf("the second resolve called the gateway: voids %v", gw.voids)
	}
	if got := f.saleInvoiceOpen(t, saleID); got != 0 {
		t.Fatalf("the invoice owes %d cents after the reversal", got)
	}
	f.assertARInvariants(t)
}

// TestResolveAuthorizedFallsBackToRefundWhenTheVoidIsRefused: the settled
// capture cannot be voided, so the whole charge is refunded under the same
// key.
func TestResolveAuthorizedFallsBackToRefundWhenTheVoidIsRefused(t *testing.T) {
	gw := &fakeGateway{voidErrs: []error{fmt.Errorf("cannot void a settled capture")}}
	f := terminalFixture(t, payment.SimNoAnswer, gw)
	saleID := f.openSale(t, nil)
	_, itemID := f.lostLegOf(t, saleID)
	if r := f.completeSale(saleID, f.cashAll(t, saleID)); r.status != http.StatusOK {
		t.Fatalf("the cash completion = %d: %s", r.status, r.raw)
	}
	if r := f.resolveItem(itemID, "authorized", "devref-fb-1"); r.status != http.StatusOK {
		t.Fatalf("the resolve = %d: %s", r.status, r.raw)
	}
	if len(gw.refunds) != 1 || gw.refundCents[0] != f.totalOf(t, saleID) {
		t.Fatalf("the fallback refunded %v for %v cents, wanted the whole charge", gw.refunds, gw.refundCents)
	}
}

// TestResolveAuthorizedWhenTheReversalFailsReleasesTheItem: neither the void
// nor the refund goes through: 502, the item is open again for a retry, and
// nothing is booked. A retry after the gateway recovers settles it once.
func TestResolveAuthorizedWhenTheReversalFailsReleasesTheItem(t *testing.T) {
	gw := &fakeGateway{voidErrs: []error{fmt.Errorf("void refused")}, refundErrs: []error{fmt.Errorf("refund refused")}}
	f := terminalFixture(t, payment.SimNoAnswer, gw)
	saleID := f.openSale(t, nil)
	intentID, itemID := f.lostLegOf(t, saleID)
	if r := f.completeSale(saleID, f.cashAll(t, saleID)); r.status != http.StatusOK {
		t.Fatalf("the cash completion = %d: %s", r.status, r.raw)
	}
	r := f.resolveItem(itemID, "authorized", "devref-502")
	if r.status != http.StatusBadGateway {
		t.Fatalf("the failed reversal = %d, wanted 502: %s", r.status, r.raw)
	}
	if got := itemStateOf(t, f, itemID); got != "open" {
		t.Fatalf("the item is %s after the failed reversal, wanted open", got)
	}
	if state, spent, _ := f.intentFacts(t, intentID); state != "authorized" || spent {
		t.Fatalf("the intent is %s spent=%t after the failed reversal, wanted authorized and unspent", state, spent)
	}
	if r2 := f.resolveItem(itemID, "authorized", "devref-502"); r2.status != http.StatusOK {
		t.Fatalf("the retry after the gateway recovered = %d: %s", r2.status, r2.raw)
	}
	if state, spent, _ := f.intentFacts(t, intentID); state != "authorized" || !spent {
		t.Fatalf("the intent is %s spent=%t after the retry, wanted authorized and spent", state, spent)
	}
}

// TestResolveAuthorizedAppliesUpToTheOpenAmountAndReversesTheRest: the sale
// completed with cash and an account tender, so its invoice still owes part
// of the lost card leg: the money applies up to that and the rest goes back.
func TestResolveAuthorizedAppliesUpToTheOpenAmountAndReversesTheRest(t *testing.T) {
	gw := &fakeGateway{}
	f := terminalFixture(t, payment.SimNoAnswer, gw)
	customer := f.customerID
	saleID := f.openSale(t, &customer)
	_, itemID := f.lostLegOf(t, saleID)
	total := f.totalOf(t, saleID)
	if r := f.completeSale(saleID, tender("cash", 100), tender("account", total-100)); r.status != http.StatusOK {
		t.Fatalf("the cash and account completion = %d: %s", r.status, r.raw)
	}
	if got := f.saleInvoiceOpen(t, saleID); got != total-100 {
		t.Fatalf("the invoice owes %d, wanted %d", got, total-100)
	}
	r := f.resolveItem(itemID, "authorized", "devref-part-1")
	if r.status != http.StatusOK {
		t.Fatalf("the resolve = %d: %s", r.status, r.raw)
	}
	if len(gw.refunds) != 1 || gw.refundCents[0] != 100 || len(gw.voids) != 0 {
		t.Fatalf("the rest went back through refunds %v cents %v voids %v, wanted one refund of 100", gw.refunds, gw.refundCents, gw.voids)
	}
	if got := f.saleInvoiceOpen(t, saleID); got != 0 {
		t.Fatalf("the invoice owes %d after the application, wanted 0", got)
	}
	var cents int64
	if err := f.db.Pool.QueryRow(context.Background(),
		`SELECT COALESCE(SUM(ROUND(amount * 100)), 0)::bigint FROM pos_tenders WHERE transaction_id = $1 AND method = 'CARD'`, saleID).Scan(&cents); err != nil || cents != total-100 {
		t.Fatalf("the card tender holds %d cents (%v), wanted %d", cents, err, total-100)
	}
	f.assertARInvariants(t)
}

// TestResolveAuthorizedOnVoidedSaleReversesTheWholeCharge: the sale was
// completed and voided while the card answer was lost.
func TestResolveAuthorizedOnVoidedSaleReversesTheWholeCharge(t *testing.T) {
	gw := &fakeGateway{}
	f := terminalFixture(t, payment.SimNoAnswer, gw)
	saleID := f.openSale(t, nil)
	_, itemID := f.lostLegOf(t, saleID)
	done := f.completeSale(saleID, f.cashAll(t, saleID))
	if done.status != http.StatusOK {
		t.Fatalf("the cash completion = %d: %s", done.status, done.raw)
	}
	v := f.do("POST", "/api/v1/pos/transactions/"+saleID+"/void", map[string]any{"reason": "customer left", "revision": rev(t, done)},
		"X-Test-Role", "cashier", "X-Test-Sub", mustUUID(t))
	if v.status != http.StatusOK {
		t.Fatalf("the void = %d: %s", v.status, v.raw)
	}
	if r := f.resolveItem(itemID, "authorized", "devref-void-1"); r.status != http.StatusOK {
		t.Fatalf("the resolve = %d: %s", r.status, r.raw)
	}
	if len(gw.voids) != 1 || f.cardTendersOf(t, saleID) != 0 {
		t.Fatalf("voids %v card tenders %d, wanted one void and no card tender", gw.voids, f.cardTendersOf(t, saleID))
	}
}

// TestResolveDeclinedMovesNothing: the terminal's own record said no: the
// item closes and no payment, tender, gateway call or intent move follows.
func TestResolveDeclinedMovesNothing(t *testing.T) {
	gw := &fakeGateway{}
	f := terminalFixture(t, payment.SimNoAnswer, gw)
	saleID := f.openSale(t, nil)
	intentID, itemID := f.lostLegOf(t, saleID)
	r := f.resolveItem(itemID, "declined", "the terminal shows no sale")
	if r.status != http.StatusOK || str(t, r.body, "state") != "resolved_declined" {
		t.Fatalf("the resolve = %d: %s", r.status, r.raw)
	}
	if len(gw.voids)+len(gw.refunds) != 0 || f.counterPayments(t) != 0 || f.cardTendersOf(t, saleID) != 0 {
		t.Fatalf("a declined resolution moved money: voids %v refunds %v payments %d", gw.voids, gw.refunds, f.counterPayments(t))
	}
	if state, _, _ := f.intentFacts(t, intentID); state != "lost" {
		t.Fatalf("the intent is %s, wanted it left lost", state)
	}
	if got := f.saleStatus(t, saleID); got != "OPEN" {
		t.Fatalf("the sale is %s, wanted OPEN", got)
	}
	// the card can be charged again on the open sale: the declined resolution
	// frees the leg for a new attempt
	requireBlocker(t, f.resolveItem(itemID, "declined", "again"), "lost_item_settled")
}

// TestRacingResolvesCallTheGatewayOnce: three managers resolve one lost item
// at once on the reversal path at pool size 4; exactly one resolution wins,
// the others get a conflict, and the gateway is called once.
func TestRacingResolvesCallTheGatewayOnce(t *testing.T) {
	lockTerminalTests(t)
	gw := &fakeGateway{}
	f := newFixture(t, testutil.RequireDBMaxConns(t, 4), withTerminal(payment.SimNoAnswer, 150*time.Millisecond), func(f *fixture) {
		f.service.WithGateway(gw)
	})
	f.openTill(0)
	saleID := f.openSale(t, nil)
	_, itemID := f.lostLegOf(t, saleID)
	if r := f.completeSale(saleID, f.cashAll(t, saleID)); r.status != http.StatusOK {
		t.Fatalf("the cash completion = %d: %s", r.status, r.raw)
	}
	var wg sync.WaitGroup
	statuses := make([]int, 3)
	for i := 0; i < 3; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			statuses[i] = f.resolveItem(itemID, "authorized", "devref-race-1").status
		}(i)
	}
	wg.Wait()
	ok, conflicts := 0, 0
	for _, s := range statuses {
		switch s {
		case http.StatusOK:
			ok++
		case http.StatusConflict:
			conflicts++
		}
	}
	if ok != 1 || conflicts != 2 {
		t.Fatalf("the racing resolves answered %v, wanted one 200 and two 409", statuses)
	}
	if len(gw.voids) != 1 {
		t.Fatalf("the racing resolves called the gateway %d times, wanted 1", len(gw.voids))
	}
}

// settledCardSale completes a card sale on the simulated terminal that
// approves, with the gateway scripted for the refund that follows.
func settledCardSale(t *testing.T, gw *fakeGateway) (*fixture, string) {
	t.Helper()
	f := terminalFixture(t, payment.SimApprove, gw)
	saleID := f.openSale(t, nil)
	if r := f.completeSale(saleID, f.cardAll(t, saleID)); r.status != http.StatusOK {
		t.Fatalf("the card completion = %d: %s", r.status, r.raw)
	}
	return f, saleID
}

// refundLegOf reads the terminal intent of a refund claim's leg.
func (f *fixture) refundLegOf(t *testing.T, saleID, kind string) (intentID uuid.UUID, legKey string) {
	t.Helper()
	if err := f.db.Pool.QueryRow(context.Background(),
		`SELECT id, leg_key FROM payment_terminal_intents WHERE transaction_id = $1 AND kind = $2`, saleID, kind).Scan(&intentID, &legKey); err != nil {
		t.Fatalf("the %s leg of the sale: %v", kind, err)
	}
	return intentID, legKey
}

// TestResolveAuthorizedSettlesAStrandedVoidClaim: a card void's refund timed
// out at the terminal, the sweep filed the item, and the manager's authorized
// resolution settles the void's refund claim through the void resolution:
// the sale is voided, the claim is DONE under the terminal's reference, and a
// second resolve finds it settled.
func TestResolveAuthorizedSettlesAStrandedVoidClaim(t *testing.T) {
	gw := &fakeGateway{refundErrs: []error{context.DeadlineExceeded}}
	f, saleID := settledCardSale(t, gw)
	done := f.getSale(t, saleID)
	v := f.do("POST", "/api/v1/pos/transactions/"+saleID+"/void", map[string]any{"reason": "customer left", "revision": rev2(t, done)},
		"X-Test-Role", "cashier", "X-Test-Sub", mustUUID(t))
	if v.status != http.StatusInternalServerError {
		t.Fatalf("the timed out void = %d, wanted 500: %s", v.status, v.raw)
	}
	legID, legKey := f.refundLegOf(t, saleID, "void")
	claimID, claimState, _, _ := pendingIntent(t, f, saleID)
	if legKey != claimID || claimState != "PENDING" {
		t.Fatalf("the void leg key %q names claim %q (%s), wanted the claim's own id", legKey, claimID, claimState)
	}
	f.expireTerminalLeases(t, saleID)
	f.sweepAsManager(t)
	itemID := f.itemOfSale(t, saleID)
	if r := f.resolveItem(itemID, "authorized", "refund-ref-void-1"); r.status != http.StatusOK {
		t.Fatalf("the resolve = %d: %s", r.status, r.raw)
	}
	var claimNow, refundTx string
	if err := f.db.Pool.QueryRow(context.Background(), `SELECT state, COALESCE(refund_tx_id, '') FROM pos_refund_intents WHERE id = $1`, claimID).Scan(&claimNow, &refundTx); err != nil {
		t.Fatal(err)
	}
	if claimNow != "DONE" || refundTx != "refund-ref-void-1" {
		t.Fatalf("the claim is %s under %q, wanted DONE under the manager's reference", claimNow, refundTx)
	}
	if got := f.saleStatus(t, saleID); got != "VOIDED" {
		t.Fatalf("the sale is %s, wanted VOIDED", got)
	}
	if state, spent, _ := f.intentFacts(t, legID); state != "authorized" || !spent {
		t.Fatalf("the void leg is %s spent=%t, wanted authorized and spent", state, spent)
	}
	if got := f.accountBalance("1010"); got != 0 {
		t.Fatalf("the cash and card account holds %d after the void, wanted 0", got)
	}
	if got := f.counterRefunds(t); got != 1 {
		t.Fatalf("%d refunds booked, wanted exactly 1", got)
	}
	requireBlocker(t, f.resolveItem(itemID, "authorized", "refund-ref-void-1"), "lost_item_settled")
	if got := f.counterRefunds(t); got != 1 {
		t.Fatalf("the second resolve booked another refund: %d", got)
	}
	// the drawer closes: no claim or intent stands
	sessionID := f.currentTill(t)
	if cl := f.do("POST", "/api/v1/pos/till/"+sessionID+"/close", map[string]any{"counted_by_method": map[string]any{"cash": 0, "card": 0}},
		"X-Test-Role", "cashier", "X-Test-Sub", mustUUID(t)); cl.status == http.StatusConflict {
		t.Fatalf("the drawer still refuses after the settlement: %s", cl.raw)
	}
	f.assertARInvariants(t)
}

// rev2 reads a sale's revision from its body map.
func rev2(t *testing.T, body map[string]any) int64 { return num(t, body, "revision") }

// TestResolveAuthorizedSettlesAStrandedReturnClaim: a card return's refund
// timed out; the manager's authorized resolution posts the return through the
// return's own posting, never a payment refund on a fully applied payment.
func TestResolveAuthorizedSettlesAStrandedReturnClaim(t *testing.T) {
	gw := &fakeGateway{refundErrs: []error{context.DeadlineExceeded}}
	f, saleID := settledCardSale(t, gw)
	lineID := f.getSale(t, saleID)["lines"].([]any)[0].(map[string]any)["id"].(string)
	if r := f.cardReturnOn(t, saleID, lineID, "1"); r.status != http.StatusInternalServerError {
		t.Fatalf("the timed out card return = %d, wanted 500: %s", r.status, r.raw)
	}
	legID, legKey := f.refundLegOf(t, saleID, "refund")
	claimID, claimState, _, _ := pendingIntent(t, f, saleID)
	if legKey != claimID || claimState != "PENDING" {
		t.Fatalf("the refund leg key %q names claim %q (%s)", legKey, claimID, claimState)
	}
	f.expireTerminalLeases(t, saleID)
	f.sweepAsManager(t)
	itemID := f.itemOfSale(t, saleID)
	if r := f.resolveItem(itemID, "authorized", "refund-ref-return-1"); r.status != http.StatusOK {
		t.Fatalf("the resolve = %d: %s", r.status, r.raw)
	}
	var claimNow string
	if err := f.db.Pool.QueryRow(context.Background(), `SELECT state FROM pos_refund_intents WHERE id = $1`, claimID).Scan(&claimNow); err != nil || claimNow != "DONE" {
		t.Fatalf("the claim is %s (%v), wanted DONE", claimNow, err)
	}
	if got := f.counterReturns(t); got != 1 {
		t.Fatalf("%d returns posted, wanted 1", got)
	}
	if state, spent, _ := f.intentFacts(t, legID); state != "authorized" || !spent {
		t.Fatalf("the refund leg is %s spent=%t, wanted authorized and spent", state, spent)
	}
	if got := f.accountBalance("1010"); got != 0 {
		t.Fatalf("the cash and card account holds %d after the return, wanted 0", got)
	}
	requireBlocker(t, f.resolveItem(itemID, "authorized", "refund-ref-return-1"), "lost_item_settled")
	if got := f.counterReturns(t); got != 1 {
		t.Fatalf("the second resolve posted another return: %d", got)
	}
	f.assertARInvariants(t)
}

// TestRefusedSaleAfterAnAuthorizedLegIsReversedAndTheRetryCharges: the
// completion refuses after the device authorized the leg (the stock is gone):
// the charge goes back under a compensation void intent of its own, the sale
// leg is spent, and the retried completion makes a new attempt at the device.
func TestRefusedSaleAfterAnAuthorizedLegIsReversedAndTheRetryCharges(t *testing.T) {
	gw := &fakeGateway{}
	f := terminalFixture(t, payment.SimApprove, gw)
	saleID := f.openSale(t, nil)
	mustExec(t, f.db, `UPDATE inventory SET quantity = 0 WHERE product_id = $1`, f.productID)
	r := f.completeSale(saleID, f.cardAll(t, saleID))
	if r.status != http.StatusConflict {
		t.Fatalf("the refused completion = %d, wanted 409: %s", r.status, r.raw)
	}
	if len(gw.voids) != 1 {
		t.Fatalf("the refusal reversed the charge %d times, wanted 1 void", len(gw.voids))
	}
	saleLegID, _, _ := f.terminalIntentOfKind(t, saleID, "sale")
	if state, spent, _ := f.intentFacts(t, saleLegID); state != "authorized" || !spent {
		t.Fatalf("the reversed sale leg is %s spent=%t, wanted authorized and spent", state, spent)
	}
	compID, compKey := f.refundLegOf(t, saleID, "void")
	if compKey != "comp-card-0" {
		t.Fatalf("the compensation leg key is %q, wanted comp-card-0", compKey)
	}
	if state, _, _ := f.intentFacts(t, compID); state != "authorized" {
		t.Fatalf("the compensation void is %s, wanted authorized", state)
	}
	// the stock returns and the retry charges again, as a new attempt
	mustExec(t, f.db, `UPDATE inventory SET quantity = 100 WHERE product_id = $1`, f.productID)
	done := f.completeSale(saleID, f.cardAll(t, saleID))
	if done.status != http.StatusOK {
		t.Fatalf("the retried completion = %d: %s", done.status, done.raw)
	}
	if n := len(f.terminal.Dispatched()); n != 2 {
		t.Fatalf("the device was dispatched %d times, wanted 2 (the refused charge was put back)", n)
	}
	if got := f.accountBalance("1010"); got != f.totalOf(t, saleID) {
		t.Fatalf("the cash and card account holds %d, wanted one charge of %d", got, f.totalOf(t, saleID))
	}
}

// terminalIntentOfKind reads the one intent of a sale by kind.
func (f *fixture) terminalIntentOfKind(t *testing.T, saleID, kind string) (id uuid.UUID, state, legKey string) {
	t.Helper()
	if err := f.db.Pool.QueryRow(context.Background(),
		`SELECT id, state, leg_key FROM payment_terminal_intents WHERE transaction_id = $1 AND kind = $2 ORDER BY claimed_at LIMIT 1`, saleID, kind).
		Scan(&id, &state, &legKey); err != nil {
		t.Fatalf("the %s intent of the sale: %v", kind, err)
	}
	return id, state, legKey
}

// TestResolveAuthorizedSettlesALostCompensationVoid: the compensation void of
// a refused sale's charge lost its answer too: its item names the sale (no
// refund claim row exists), resolves on its own path, books nothing, and the
// sale leg it reversed is spent.
func TestResolveAuthorizedSettlesALostCompensationVoid(t *testing.T) {
	gw := &fakeGateway{voidErrs: []error{fmt.Errorf("void refused")}, refundErrs: []error{fmt.Errorf("refund refused")}}
	f := terminalFixture(t, payment.SimApprove, gw)
	saleID := f.openSale(t, nil)
	mustExec(t, f.db, `UPDATE inventory SET quantity = 0 WHERE product_id = $1`, f.productID)
	r := f.completeSale(saleID, f.cardAll(t, saleID))
	if r.status != http.StatusBadGateway {
		t.Fatalf("the refused sale whose reversal failed = %d, wanted 502: %s", r.status, r.raw)
	}
	compID, compKey := f.refundLegOf(t, saleID, "void")
	if compKey != "comp-card-0" {
		t.Fatalf("the compensation leg key is %q", compKey)
	}
	if state, _, _ := f.intentFacts(t, compID); state != "pending_at_device" {
		t.Fatalf("the compensation void is %s, wanted pending_at_device", state)
	}
	f.expireTerminalLeases(t, saleID)
	f.sweepAsManager(t)
	itemID := f.itemOfSale(t, saleID)
	before := f.counterPayments(t)
	if rr := f.resolveItem(itemID, "authorized", "devref-comp-1"); rr.status != http.StatusOK {
		t.Fatalf("the resolve = %d: %s", rr.status, rr.raw)
	}
	saleLegID, _, _ := f.terminalIntentOfKind(t, saleID, "sale")
	if state, spent, _ := f.intentFacts(t, saleLegID); state != "authorized" || !spent {
		t.Fatalf("the reversed sale leg is %s spent=%t, wanted authorized and spent", state, spent)
	}
	if got := f.counterPayments(t); got != before {
		t.Fatalf("the compensation resolution booked %d payments", got-before)
	}
	if got := itemStateOf(t, f, itemID); got != "resolved_authorized" {
		t.Fatalf("the item is %s", got)
	}
}

// requireBlocker asserts that a 409 response carries the expected blocker code.
func requireBlocker(t *testing.T, r resp, want string) {
	t.Helper()
	if r.status != http.StatusConflict {
		t.Fatalf("status = %d, wanted 409: %s", r.status, r.raw)
	}
	_, blockers, _ := errorOf(t, r)
	for _, b := range blockers {
		if b == want {
			return
		}
	}
	t.Fatalf("blockers = %v, wanted %q in %s", blockers, want, r.raw)
}

// TestReviewConflictItemResolvedAuthorized: a card sale completed on an
// authorized leg, then the device delivered a conflicting declined answer that
// filed an open manager item. Resolving the item as authorized agrees with the
// books: 200 OK, zero gateway voids or refunds, the item closes as
// resolved_authorized, and the sale stays COMPLETED with its one card payment.
func TestReviewConflictItemResolvedAuthorized(t *testing.T) {
	gw := &fakeGateway{}
	f, saleID := settledCardSale(t, gw)
	intentID, state, _ := f.terminalIntentOf(t, saleID)
	if state != "authorized" {
		t.Fatalf("the completed sale's leg is %s, wanted authorized", state)
	}
	ledger := payment.NewTerminalLedger(f.db)
	out, err := ledger.ApplyDeviceAnswer(context.Background(), payment.TerminalAnswer{
		IntentID: intentID, ProviderRef: "devref-conflict", Verdict: "declined",
	})
	if err != nil || out != payment.AnswerConflictQueued {
		t.Fatalf("conflicting answer = %s %v, wanted conflict_queued", out, err)
	}
	itemID := f.itemOfSale(t, saleID)
	r := f.resolveItem(itemID, "authorized", "devref-conflict")
	if r.status != http.StatusOK || str(t, r.body, "state") != "resolved_authorized" {
		t.Fatalf("resolve authorized = %d: %s; gateway voids %v refunds %v; item %s",
			r.status, r.raw, gw.voids, gw.refunds, itemStateOf(t, f, itemID))
	}
	if len(gw.voids) != 0 || len(gw.refunds) != 0 {
		t.Fatalf("gateway voids %v refunds %v, wanted none", gw.voids, gw.refunds)
	}
	if got := itemStateOf(t, f, itemID); got != "resolved_authorized" {
		t.Fatalf("item state = %s, wanted resolved_authorized", got)
	}
	if st, spent, _ := f.intentFacts(t, intentID); st != "authorized" || !spent {
		t.Fatalf("intent = %s spent=%t, wanted authorized and spent", st, spent)
	}
	if got := f.saleStatus(t, saleID); got != "COMPLETED" {
		t.Fatalf("sale status = %s, wanted COMPLETED", got)
	}
	if got := f.cardTendersOf(t, saleID); got != 1 {
		t.Fatalf("card tenders = %d, wanted 1", got)
	}
	if got := f.counterPayments(t); got != 1 {
		t.Fatalf("payments = %d, wanted 1", got)
	}
	if got := f.accountBalance("1010"); got != f.totalOf(t, saleID) {
		t.Fatalf("1010 = %d, wanted %d", got, f.totalOf(t, saleID))
	}
	f.assertARInvariants(t)
}

// TestReviewConflictItemOnDeclinedLeg: a card leg was declined at the counter,
// then a conflicting authorized answer arrived and filed an open manager item.
// Resolving the item as authorized on an open sale moves the leg to authorized
// (unspent) so the completion reuses it; on a sale subsequently paid in cash,
// resolving as authorized reverses the charge cleanly at the gateway.
func TestReviewConflictItemOnDeclinedLeg(t *testing.T) {
	gw := &fakeGateway{}
	f := terminalFixture(t, payment.SimDecline, gw)
	ledger := payment.NewTerminalLedger(f.db)

	// Case A: open sale keeps the authorized leg unspent for completion.
	saleOpen := f.openSale(t, nil)
	if r := f.completeSale(saleOpen, f.cardAll(t, saleOpen)); r.status != http.StatusConflict {
		t.Fatalf("declined completion = %d, wanted 409: %s", r.status, r.raw)
	}
	intentOpen, state, _ := f.terminalIntentOf(t, saleOpen)
	if state != "declined" {
		t.Fatalf("intent is %s, wanted declined", state)
	}
	if out, err := ledger.ApplyDeviceAnswer(context.Background(), payment.TerminalAnswer{
		IntentID: intentOpen, ProviderRef: "devref-decl-open", Verdict: "authorized",
		Brand: "VISA", Last4: "4242", AuthCode: "AUTH1",
	}); err != nil || out != payment.AnswerConflictQueued {
		t.Fatalf("conflicting authorized answer = %s %v, wanted conflict_queued", out, err)
	}
	itemOpen := f.itemOfSale(t, saleOpen)
	if r := f.resolveItem(itemOpen, "authorized", "devref-decl-open"); r.status != http.StatusOK {
		t.Fatalf("resolve authorized on open sale = %d: %s; item %s", r.status, r.raw, itemStateOf(t, f, itemOpen))
	}
	if st, spent, ref := f.intentFacts(t, intentOpen); st != "authorized" || spent || ref != "devref-decl-open" {
		t.Fatalf("intent after resolve = %s spent=%t ref=%q, wanted authorized unspent", st, spent, ref)
	}
	dispatchesBefore := len(f.terminal.Dispatched())
	if done := f.completeSale(saleOpen, f.cardAll(t, saleOpen)); done.status != http.StatusOK {
		t.Fatalf("completion on resolved leg = %d: %s", done.status, done.raw)
	}
	if got := len(f.terminal.Dispatched()); got != dispatchesBefore {
		t.Fatalf("completion dispatched again: %d vs %d", got, dispatchesBefore)
	}
	if st, spent, _ := f.intentFacts(t, intentOpen); st != "authorized" || !spent {
		t.Fatalf("intent after completion = %s spent=%t, wanted authorized spent", st, spent)
	}

	// Case B: sale completed in cash after the decline, then manager resolves
	// the conflicting authorized answer -> reverses cleanly at the gateway.
	salePaid := f.openSale(t, nil)
	if r := f.completeSale(salePaid, f.cardAll(t, salePaid)); r.status != http.StatusConflict {
		t.Fatalf("declined completion = %d, wanted 409: %s", r.status, r.raw)
	}
	intentPaid, _, _ := f.terminalIntentOf(t, salePaid)
	if out, err := ledger.ApplyDeviceAnswer(context.Background(), payment.TerminalAnswer{
		IntentID: intentPaid, ProviderRef: "devref-decl-paid", Verdict: "authorized",
	}); err != nil || out != payment.AnswerConflictQueued {
		t.Fatalf("conflicting authorized answer = %s %v", out, err)
	}
	if r := f.completeSale(salePaid, f.cashAll(t, salePaid)); r.status != http.StatusOK {
		t.Fatalf("cash completion = %d: %s", r.status, r.raw)
	}
	itemPaid := f.itemOfSale(t, salePaid)
	if r := f.resolveItem(itemPaid, "authorized", "devref-decl-paid"); r.status != http.StatusOK {
		t.Fatalf("resolve authorized on paid sale = %d: %s", r.status, r.raw)
	}
	if len(gw.voids) != 1 || gw.voids[0] != "devref-decl-paid" {
		t.Fatalf("gateway voids = %v, wanted [devref-decl-paid]", gw.voids)
	}
	if st, spent, _ := f.intentFacts(t, intentPaid); st != "authorized" || !spent {
		t.Fatalf("intent after reversal = %s spent=%t, wanted authorized spent", st, spent)
	}
	f.assertARInvariants(t)
}

// TestReviewConflictItemResolvedDeclinedOnBookedLegIsRefused: a completed card
// sale receives a conflicting declined answer; resolving the item as declined
// is refused with 409 terminal_leg_already_booked naming the booked tender, and
// the item remains open so the books never silently keep a payment the manager
// marked declined.
func TestReviewConflictItemResolvedDeclinedOnBookedLegIsRefused(t *testing.T) {
	gw := &fakeGateway{}
	f, saleID := settledCardSale(t, gw)
	intentID, _, _ := f.terminalIntentOf(t, saleID)
	var tenderID string
	if err := f.db.Pool.QueryRow(context.Background(),
		`SELECT id::text FROM pos_tenders WHERE transaction_id = $1 AND method = 'CARD'`, saleID).Scan(&tenderID); err != nil {
		t.Fatal(err)
	}
	ledger := payment.NewTerminalLedger(f.db)
	if out, err := ledger.ApplyDeviceAnswer(context.Background(), payment.TerminalAnswer{
		IntentID: intentID, ProviderRef: "devref-booked-decl", Verdict: "declined",
	}); err != nil || out != payment.AnswerConflictQueued {
		t.Fatalf("conflicting declined answer = %s %v", out, err)
	}
	itemID := f.itemOfSale(t, saleID)
	r := f.resolveItem(itemID, "declined", "terminal tape shows declined")
	requireBlocker(t, r, "terminal_leg_already_booked")
	if !strings.Contains(string(r.raw), tenderID) {
		t.Fatalf("refusal does not name booked tender %s: %s", tenderID, r.raw)
	}
	if got := itemStateOf(t, f, itemID); got != "open" {
		t.Fatalf("item state = %s, wanted open", got)
	}
}

// TestReviewConflictItemResolvedDeclinedOnUnspentAuthorizedLeg: an open sale
// has an authorized unspent card leg (stopped after answer before booking),
// then a conflicting declined answer files an open manager item. Resolving the
// item as declined moves the intent to declined and closes the item as
// resolved_declined so the open sale can charge again.
func TestReviewConflictItemResolvedDeclinedOnUnspentAuthorizedLeg(t *testing.T) {
	f, seam := crashFixture(t, payment.SimApprove, nil)
	saleID := f.openSale(t, nil)
	seam.set(pos.TerminalPointAfterAnswer)
	if r := f.completeSale(saleID, f.cardAll(t, saleID)); r.status != http.StatusInternalServerError {
		t.Fatalf("stopped act = %d, wanted 500: %s", r.status, r.raw)
	}
	intentID, state, _ := f.terminalIntentOf(t, saleID)
	if state != "authorized" {
		t.Fatalf("intent state = %s, wanted authorized", state)
	}
	ledger := payment.NewTerminalLedger(f.db)
	if out, err := ledger.ApplyDeviceAnswer(context.Background(), payment.TerminalAnswer{
		IntentID: intentID, ProviderRef: "devref-unspent-decl", Verdict: "declined",
	}); err != nil || out != payment.AnswerConflictQueued {
		t.Fatalf("conflicting declined answer = %s %v", out, err)
	}
	itemID := f.itemOfSale(t, saleID)
	r := f.resolveItem(itemID, "declined", "terminal tape shows declined")
	if r.status != http.StatusOK || str(t, r.body, "state") != "resolved_declined" {
		t.Fatalf("resolve declined = %d: %s", r.status, r.raw)
	}
	if st, spent, _ := f.intentFacts(t, intentID); st != "declined" || spent {
		t.Fatalf("intent after resolve = %s spent=%t, wanted declined unspent", st, spent)
	}
	if got := itemStateOf(t, f, itemID); got != "resolved_declined" {
		t.Fatalf("item state = %s, wanted resolved_declined", got)
	}
	seam.set("")
	if done := f.completeSale(saleID, f.cardAll(t, saleID)); done.status != http.StatusOK {
		t.Fatalf("retry after declined resolution = %d: %s", done.status, done.raw)
	}
	f.assertARInvariants(t)
}

