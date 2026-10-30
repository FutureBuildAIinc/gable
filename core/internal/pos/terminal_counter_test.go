// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package pos_test

// The counter's card legs on the simulated terminal (CP-05, IX-01): the
// approved flow end to end, the decline, the no answer that runs the sweep
// and the manager's resolution, the drawer's refusal while an intent stands,
// the cancel that is honest about the device, and the wire walls of the
// manager routes (only a manager resolves).

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/gablelbm/gable/internal/payment"
	"github.com/gablelbm/gable/internal/testutil"
	"github.com/gablelbm/gable/pkg/audit"
	"github.com/google/uuid"
)

// withTerminal wires the simulated terminal and its ledger into the counter,
// with the wait the test wants.
func withTerminal(outcome payment.SimulatedOutcome, wait time.Duration) func(*fixture) {
	return func(f *fixture) {
		ledger := payment.NewTerminalLedger(f.db).WithAuditLog(audit.NewLogger(f.db))
		f.terminal = payment.NewSimulatedTerminal(outcome, ledger)
		f.service.WithTerminal(f.terminal, ledger)
		if wait > 0 {
			f.service.WithTerminalWait(wait)
		}
	}
}

// cardFor answers a card tender of the last sale's total.
func (f *fixture) cardFor() map[string]any {
	f.t.Helper()
	var total int64
	if err := f.db.Pool.QueryRow(context.Background(),
		`SELECT ROUND(total * 100)::bigint FROM pos_transactions ORDER BY created_at DESC LIMIT 1`).Scan(&total); err != nil {
		f.t.Fatal(err)
	}
	return tender("card", total)
}

// terminalIntentOf reads the one intent of a sale.
func (f *fixture) terminalIntentOf(t *testing.T, saleID string) (id uuid.UUID, state, verdict string) {
	t.Helper()
	var iid uuid.UUID
	var st, vd string
	if err := f.db.Pool.QueryRow(context.Background(),
		`SELECT id, state, answer_verdict FROM payment_terminal_intents WHERE transaction_id = $1`, saleID).
		Scan(&iid, &st, &vd); err != nil {
		t.Fatalf("the sale's terminal intent: %v", err)
	}
	return iid, st, vd
}

// TestCounterCardApproveEndToEnd: the card leg rides the intent, the device
// approves, the completion's one transaction records the tender as a payment
// applied to the invoice, and the intent stands authorized with only safe
// answer fields.
func TestCounterCardApproveEndToEnd(t *testing.T) {
	lockTerminalTests(t)
	f := newFixture(t, testutil.RequireDB(t), withTerminal(payment.SimApprove, 0))
	saleID := f.startSale(nil)
	if r := f.addLine(saleID, f.productLine("1")); r.status != http.StatusOK {
		t.Fatalf("add line = %d: %s", r.status, r.raw)
	}
	r := f.completeSale(saleID, f.cardFor())
	if r.status != http.StatusOK {
		t.Fatalf("complete = %d: %s", r.status, r.raw)
	}
	id, state, verdict := f.terminalIntentOf(t, saleID)
	if state != "authorized" || verdict != "authorized" {
		t.Fatalf("the intent is %s verdict %s, wanted authorized", state, verdict)
	}
	var ref, last4, brand string
	var answered bool
	if err := f.db.Pool.QueryRow(context.Background(),
		`SELECT answer_provider_ref, answer_last4, answer_brand, answered_at IS NOT NULL FROM payment_terminal_intents WHERE id = $1`, id).
		Scan(&ref, &last4, &brand, &answered); err != nil || !answered {
		t.Fatalf("the answered intent: %v answered=%t", err, answered)
	}
	if ref == "" || last4 != "4242" || brand == "" {
		t.Fatalf("the answer's safe fields are ref %q last4 %q brand %q", ref, last4, brand)
	}
	var tenders int
	var gwRef *string
	if err := f.db.Pool.QueryRow(context.Background(),
		`SELECT count(*), max(gateway_tx_id) FROM pos_tenders WHERE transaction_id = $1 AND method = 'CARD'`, saleID).
		Scan(&tenders, &gwRef); err != nil || tenders != 1 {
		t.Fatalf("the card tender: %v count=%d", err, tenders)
	}
	if gwRef == nil || *gwRef != ref {
		t.Fatalf("the tender carries gateway ref %v, wanted the device's %q", gwRef, ref)
	}
}

// TestCounterCardDeclineEndToEnd: the device's own no refuses the completion
// (card_declined), the sale stays open and the intent stands declined.
func TestCounterCardDeclineEndToEnd(t *testing.T) {
	lockTerminalTests(t)
	f := newFixture(t, testutil.RequireDB(t), withTerminal(payment.SimDecline, 0))
	saleID := f.startSale(nil)
	if r := f.addLine(saleID, f.productLine("1")); r.status != http.StatusOK {
		t.Fatalf("add line = %d: %s", r.status, r.raw)
	}
	r := f.completeSale(saleID, f.cardFor())
	requireBlocker(t, r, "card_declined")
	_, state, verdict := f.terminalIntentOf(t, saleID)
	if state != "declined" || verdict != "declined" {
		t.Fatalf("the intent is %s verdict %s, wanted declined", state, verdict)
	}
	var status string
	if err := f.db.Pool.QueryRow(context.Background(),
		`SELECT status FROM pos_transactions WHERE id = $1`, saleID).Scan(&status); err != nil || status != "OPEN" {
		t.Fatalf("the refused sale is %s (%v), wanted OPEN", status, err)
	}
}

// TestCounterCardNoAnswerSweepResolveDeclined: the device never answers; the
// completion refuses with the waiting state's own refusal, the drawer refuses
// while the intent stands, the sweep turns the intent lost and files the
// manager item, and the manager's declined resolution closes it (the
// terminal's own record said no).
func TestCounterCardNoAnswerSweepResolveDeclined(t *testing.T) {
	lockTerminalTests(t)
	f := newFixture(t, testutil.RequireDB(t), withTerminal(payment.SimNoAnswer, 250*time.Millisecond))
	saleID := f.startSale(nil)
	if r := f.addLine(saleID, f.productLine("1")); r.status != http.StatusOK {
		t.Fatalf("add line = %d: %s", r.status, r.raw)
	}
	r := f.completeSale(saleID, f.cardFor())
	requireBlocker(t, r, "terminal_answer_pending")
	id, state, _ := f.terminalIntentOf(t, saleID)
	if state != "pending_at_device" {
		t.Fatalf("the unanswered intent is %s, wanted pending_at_device", state)
	}
	// the desk reads its waiting state
	g := f.do("GET", "/api/v1/pos/terminal-intents/"+id.String(), nil, "X-Test-Role", "cashier", "X-Test-Sub", mustUUID(t))
	if g.status != http.StatusOK || str(t, g.body, "state") != "pending_at_device" {
		t.Fatalf("the waiting state = %d %s", g.status, g.raw)
	}
	// the cancel is honest: the device holds the leg
	c := f.do("POST", "/api/v1/pos/terminal-intents/"+id.String()+"/cancel", nil, "X-Test-Role", "cashier", "X-Test-Sub", mustUUID(t))
	requireBlocker(t, c, "terminal_at_device")
	// the drawer refuses while the intent stands
	sessionID := f.currentTill(t)
	cl := f.do("POST", "/api/v1/pos/till/"+sessionID+"/close", map[string]any{"counted_by_method": map[string]any{"cash": 0}},
		"X-Test-Role", "cashier", "X-Test-Sub", mustUUID(t))
	requireBlocker(t, cl, "terminal_intent_in_flight")
	// only a manager sweeps
	sw := f.do("POST", "/api/v1/pos/terminal-intents/sweep", nil, "X-Test-Role", "cashier", "X-Test-Sub", mustUUID(t))
	if sw.status != http.StatusForbidden {
		t.Fatalf("the cashier sweep = %d, wanted 403", sw.status)
	}
	// the lease runs out (the sweep only decides an intent past its lease)
	mustExec(t, f.db, `UPDATE payment_terminal_intents SET lease_until = NOW() - INTERVAL '1 minute' WHERE id = $1`, id)
	sw = f.do("POST", "/api/v1/pos/terminal-intents/sweep", nil, "X-Test-Role", "finance", "X-Test-Sub", mustUUID(t))
	if sw.status != http.StatusOK {
		t.Fatalf("the manager sweep = %d: %s", sw.status, sw.raw)
	}
	_, state, _ = f.terminalIntentOf(t, saleID)
	if state != "lost" {
		t.Fatalf("the swept intent is %s, wanted lost", state)
	}
	// the manager's queue holds the item; only a manager resolves
	li := f.do("GET", "/api/v1/pos/terminal-lost-items", nil, "X-Test-Role", "finance", "X-Test-Sub", mustUUID(t))
	if li.status != http.StatusOK {
		t.Fatalf("the queue = %d: %s", li.status, li.raw)
	}
	// the queue is every branch's open items, other tests' among them: this
	// test reads only the item of its own intent, and wants exactly one
	items, _ := li.body["items"].([]any)
	var own []map[string]any
	for _, it := range items {
		if m, ok := it.(map[string]any); ok && m["intent_id"] == id.String() {
			own = append(own, m)
		}
	}
	if len(own) != 1 {
		t.Fatalf("the queue holds %d items of this test's intent %s, wanted 1 (%s)", len(own), id, li.raw)
	}
	if own[0]["transaction_id"] != saleID {
		t.Fatalf("the item names sale %v, wanted %s", own[0]["transaction_id"], saleID)
	}
	itemID, _ := own[0]["id"].(string)
	rr := f.do("POST", "/api/v1/pos/terminal-lost-items/"+itemID+"/resolve",
		map[string]any{"outcome": "declined", "reference": "read the terminal"},
		"X-Test-Role", "cashier", "X-Test-Sub", mustUUID(t))
	if rr.status != http.StatusForbidden {
		t.Fatalf("the cashier resolve = %d, wanted 403", rr.status)
	}
	rr = f.do("POST", "/api/v1/pos/terminal-lost-items/"+itemID+"/resolve",
		map[string]any{"outcome": "declined", "reference": "read the terminal"},
		"X-Test-Role", "finance", "X-Test-Sub", mustUUID(t))
	if rr.status != http.StatusOK {
		t.Fatalf("the manager resolve = %d: %s", rr.status, rr.raw)
	}
	if str(t, rr.body, "state") != "resolved_declined" {
		t.Fatalf("the item ended %s", str(t, rr.body, "state"))
	}
	// a second resolution is refused
	rr = f.do("POST", "/api/v1/pos/terminal-lost-items/"+itemID+"/resolve",
		map[string]any{"outcome": "declined", "reference": "again"},
		"X-Test-Role", "finance", "X-Test-Sub", mustUUID(t))
	requireBlocker(t, rr, "lost_item_settled")
	// the drawer closes now that no intent stands
	cl = f.do("POST", "/api/v1/pos/till/"+sessionID+"/close", map[string]any{"counted_by_method": map[string]any{"cash": 0}},
		"X-Test-Role", "cashier", "X-Test-Sub", mustUUID(t))
	if cl.status != http.StatusOK {
		t.Fatalf("the close after the sweep = %d: %s", cl.status, cl.raw)
	}
}

// TestReviewRetryWhileLostItemOpenIsRefused: a card leg that is in flight
// (pending_at_device) refuses a second card charge with terminal_leg_in_flight;
// once the sweep marks the intent lost and opens a manager item, a card retry
// is refused 409 terminal_answer_unresolved without calling the device again;
// once the manager resolves the item as declined, the retry charges as attempt
// #1 and completes the sale.
func TestReviewRetryWhileLostItemOpenIsRefused(t *testing.T) {
	f := terminalFixture(t, payment.SimNoAnswer, nil)
	saleID := f.openSale(t, nil)
	f.terminal.Script("sale-"+saleID+"-card-0#1", payment.SimApprove)

	r := f.completeSale(saleID, f.cardAll(t, saleID))
	requireBlocker(t, r, "terminal_answer_pending")
	if n := len(f.terminal.Dispatched()); n != 1 {
		t.Fatalf("dispatched = %d, wanted 1", n)
	}

	// While the leg is still pending_at_device, retrying is refused with terminal_leg_in_flight.
	inFlight := f.completeSale(saleID, f.cardAll(t, saleID))
	requireBlocker(t, inFlight, "terminal_leg_in_flight")
	if n := len(f.terminal.Dispatched()); n != 1 {
		t.Fatalf("dispatched after in-flight retry = %d, wanted 1", n)
	}

	f.expireTerminalLeases(t, saleID)
	f.sweepAsManager(t)
	itemID := f.itemOfSale(t, saleID)

	// While the manager item is open, retrying is refused with terminal_answer_unresolved.
	unresolved := f.completeSale(saleID, f.cardAll(t, saleID))
	requireBlocker(t, unresolved, "terminal_answer_unresolved")
	if n := len(f.terminal.Dispatched()); n != 1 {
		t.Fatalf("dispatched while lost item open = %d, wanted 1", n)
	}

	// After the manager resolves the item as declined, the retry charges as attempt #1.
	if rr := f.resolveItem(itemID, "declined", "terminal shows no charge"); rr.status != http.StatusOK {
		t.Fatalf("resolve declined = %d: %s", rr.status, rr.raw)
	}
	done := f.completeSale(saleID, f.cardAll(t, saleID))
	if done.status != http.StatusOK {
		t.Fatalf("retry after declined resolution = %d: %s", done.status, done.raw)
	}
	dispatched := f.terminal.Dispatched()
	if len(dispatched) != 2 {
		t.Fatalf("dispatched after retry = %d, wanted 2", len(dispatched))
	}
	if dispatched[1].IdempotencyKey != "sale-"+saleID+"-card-0#1" {
		t.Fatalf("second dispatch key = %q, wanted %q", dispatched[1].IdempotencyKey, "sale-"+saleID+"-card-0#1")
	}
}

// currentTill reads the register's open session id.
func (f *fixture) currentTill(t *testing.T) string {
	t.Helper()
	r := f.do("GET", "/api/v1/pos/till/current?register_id="+f.register, nil, "X-Test-Role", "cashier", "X-Test-Sub", mustUUID(t))
	if r.status != http.StatusOK {
		t.Fatalf("current till = %d: %s", r.status, r.raw)
	}
	return str(t, r.body, "id")
}

// TestCancelClosesUndispatchedClaim: a claim the device never reached
// cancels lost with no manager item.
func TestCancelClosesUndispatchedClaim(t *testing.T) {
	lockTerminalTests(t)
	f := newFixture(t, testutil.RequireDB(t), withTerminal(payment.SimNoAnswer, 0))
	saleID := f.startSale(nil)
	ledger := payment.NewTerminalLedger(f.db)
	var branch uuid.UUID
	var register string
	if err := f.db.Pool.QueryRow(context.Background(),
		`SELECT branch_id, register_id FROM pos_transactions WHERE id = $1`, saleID).Scan(&branch, &register); err != nil {
		t.Fatal(err)
	}
	intent := &payment.TerminalIntent{ID: uuid.New(), TransactionID: mustParseUUID(saleID), LegKey: "card-9",
		RegisterID: register, BranchID: branch, AmountCents: 100, Kind: payment.TerminalKindSale,
		DeviceID: register, IdempotencyKey: "sale-" + saleID + "-card-9",
		LeaseUntil: time.Now().Add(-time.Minute)}
	if err := ledger.Claim(context.Background(), intent); err != nil {
		t.Fatalf("claim: %v", err)
	}
	c := f.do("POST", "/api/v1/pos/terminal-intents/"+intent.ID.String()+"/cancel", nil,
		"X-Test-Role", "cashier", "X-Test-Sub", mustUUID(t))
	if c.status != http.StatusOK || str(t, c.body, "state") != "lost" {
		t.Fatalf("the cancel = %d %s", c.status, c.raw)
	}
	var n int
	if err := f.db.Pool.QueryRow(context.Background(),
		`SELECT count(*) FROM payment_terminal_lost_items WHERE intent_id = $1`, intent.ID).Scan(&n); err != nil || n != 0 {
		t.Fatalf("the cancelled claim filed %d manager items, wanted 0", n)
	}
}

func mustParseUUID(s string) uuid.UUID {
	id, err := uuid.Parse(s)
	if err != nil {
		panic(err)
	}
	return id
}
