// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package pos_test

// CP-05 card truth (walker A P2 1 and 2, plan reviews/cp walka money A1 and
// B): a card the counter cannot run is refused before anything is booked,
// the register read says which card path the counter has, and a card sale
// goes back to the card through the gateway wired beside the terminal.

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/gablelbm/gable/internal/payment"
	"github.com/gablelbm/gable/internal/testutil"
)

const noTerminalWords = "This register has no card terminal: take cash, check or the account"

// withSimulatedWayBack wires the simulated refund gateway beside the
// simulated terminal, as serve does on a demo stack.
func withSimulatedWayBack(gw *payment.SimulatedTerminalGateway) func(*fixture) {
	return func(f *fixture) { f.service = f.service.WithGateway(gw) }
}

// wantCardBlocker fails unless the answer is a 409 carrying the blocker code,
// and (when words is set) a message containing them.
func wantCardBlocker(t *testing.T, what string, r resp, code, words string) {
	t.Helper()
	if r.status != http.StatusConflict {
		t.Fatalf("%s = %d, wanted 409 %s: %s", what, r.status, code, r.raw)
	}
	_, blockers, _ := errorOf(t, r)
	if len(blockers) != 1 || blockers[0] != code {
		t.Fatalf("%s blockers = %v, wanted [%s]: %s", what, blockers, code, r.raw)
	}
	if words != "" && !strings.Contains(string(r.raw), words) {
		t.Fatalf("%s says %s, wanted %q", what, r.raw, words)
	}
}

// RULE (CP-05 A1): with no card terminal wired, a card tender is refused
// 409 card_terminal_not_wired in the counter's words, and nothing is booked:
// the sale stays open with no tender, no invoice, no payment and no ledger
// entry. A token on the tender changes nothing when no gateway is wired.
func TestCP05A1_ACardWithNoTerminalBooksNothing(t *testing.T) {
	testutil.LockOutboxTables(t)
	f := newFixture(t, testutil.RequireDB(t))
	saleID := f.openSale(t, nil)
	total := f.totalOf(t, saleID)
	payments := countOf(t, f.db, `SELECT count(*) FROM payments`)
	entries := countOf(t, f.db, `SELECT count(*) FROM gl_journal_entries`)

	wantCardBlocker(t, "a card with no terminal", f.completeSale(saleID, tender("card", total)),
		"card_terminal_not_wired", noTerminalWords)
	wantCardBlocker(t, "a token bearing card with no terminal and no gateway",
		f.completeSale(saleID, withToken(tender("card", total))), "card_terminal_not_wired", noTerminalWords)
	// a split with cash is refused whole: the cash leg books nothing either
	wantCardBlocker(t, "a card beside cash with no terminal",
		f.completeSale(saleID, tender("cash", 100), tender("card", total-100)), "card_terminal_not_wired", noTerminalWords)

	var status string
	var invoice *string
	if err := f.db.Pool.QueryRow(context.Background(),
		`SELECT status, invoice_id::text FROM pos_transactions WHERE id = $1`, saleID).Scan(&status, &invoice); err != nil {
		t.Fatal(err)
	}
	if status != "OPEN" || invoice != nil {
		t.Fatalf("the refused sale is %s with invoice %v, wanted OPEN with none", status, invoice)
	}
	if n := countOf(t, f.db, `SELECT count(*) FROM pos_tenders WHERE transaction_id = $1`, saleID); n != 0 {
		t.Fatalf("the refused sale has %d tenders, wanted none", n)
	}
	if n := countOf(t, f.db, `SELECT count(*) FROM payments`); n != payments {
		t.Fatalf("payments went from %d to %d, wanted no new one", payments, n)
	}
	if n := countOf(t, f.db, `SELECT count(*) FROM gl_journal_entries`); n != entries {
		t.Fatalf("ledger entries went from %d to %d, wanted no new one", entries, n)
	}
	// cash still sells at the same register
	if r := f.completeSale(saleID, tender("cash", total)); r.status != http.StatusOK {
		t.Fatalf("the cash sale after the refusal = %d: %s", r.status, r.raw)
	}
}

// RULE (CP-05 A1): the register read names the counter's card path, so the
// Pay screen can disable Card: none with no terminal, simulator on a demo
// stack's simulated terminal.
func TestCP05A1_TheRegisterReadNamesTheCardTerminal(t *testing.T) {
	db := testutil.RequireDB(t)
	for _, c := range []struct {
		name string
		opts []func(*fixture)
		want string
	}{
		{"no terminal", nil, "none"},
		{"the simulated terminal", []func(*fixture){withTerminal(payment.SimApprove, 0)}, "simulator"},
	} {
		t.Run(c.name, func(t *testing.T) {
			f := newFixture(t, db, c.opts...)
			r := f.do("GET", "/api/v1/pos/registers", nil, "X-Test-Role", "admin", "X-Test-Sub", mustUUID(t))
			if r.status != http.StatusOK {
				t.Fatalf("registers = %d: %s", r.status, r.raw)
			}
			items, _ := r.body["items"].([]any)
			found := false
			for _, it := range items {
				reg := it.(map[string]any)
				if got := reg["card_terminal"]; got != c.want {
					t.Fatalf("register %v card_terminal = %v, wanted %s", reg["id"], got, c.want)
				}
				found = found || reg["id"] == f.register
			}
			if !found {
				t.Fatalf("the fixture's register %s is not listed: %s", f.register, r.raw)
			}
		})
	}
}

// RULE (CP-05 B): with the gateway wired beside the terminal, a simulated
// card sale returned to the card is refunded through the gateway on the
// leg's own reference, and a void of a card sale goes back to the card too.
func TestCP05B_ASimulatorCardSaleGoesBackToTheCard(t *testing.T) {
	lockTerminalTests(t)
	gw := payment.NewSimulatedTerminalGateway()
	f := newFixture(t, testutil.RequireDB(t), withTerminal(payment.SimApprove, 0), withSimulatedWayBack(gw))

	saleID, total := f.cardSale(t)
	ref := f.scalar(`SELECT gateway_tx_id FROM pos_tenders WHERE transaction_id = $1 AND method = 'CARD'`, saleID)
	lineID := f.getSale(t, saleID)["lines"].([]any)[0].(map[string]any)["id"].(string)
	r := f.do("POST", "/api/v1/pos/returns", map[string]any{
		"register_id": f.register, "original_sale_id": saleID, "refund_method": "card", "reason": "cp05",
		"lines": []map[string]any{{"line_id": lineID, "quantity": "2", "restock": true}},
	}, "X-Test-Role", "cashier", "X-Test-Sub", mustUUID(t))
	if r.status != http.StatusCreated {
		t.Fatalf("card return = %d: %s", r.status, r.raw)
	}
	refunds := gw.Refunds()
	if len(refunds) != 1 || refunds[0].GatewayTxID != ref || refunds[0].AmountCents != total {
		t.Fatalf("refunds = %+v, wanted one of %d on %v", refunds, total, ref)
	}

	voidID, voidTotal := f.cardSale(t)
	r = f.do("POST", "/api/v1/pos/transactions/"+voidID+"/void",
		map[string]any{"reason": "rung in error", "revision": num(t, f.getSale(t, voidID), "revision")},
		"X-Test-Role", "cashier", "X-Test-Sub", mustUUID(t))
	if r.status != http.StatusOK {
		t.Fatalf("void = %d: %s", r.status, r.raw)
	}
	refunds = gw.Refunds()
	if len(refunds) != 2 || refunds[1].AmountCents != voidTotal {
		t.Fatalf("refunds after the void = %+v, wanted a second of %d", refunds, voidTotal)
	}
}

// RULE (CP-05 B, the words): a sale actually paid by card is never told "not
// paid by card". A card booked with no gateway transaction (the legacy
// externally captured card) is told it was not run through Gable; a sale
// with no card is told it was not paid by card; and with no gateway wired a
// card return and a card void answer card_terminal_not_wired.
func TestCP05B_TheCardRefundWords(t *testing.T) {
	lockTerminalTests(t)
	f := newFixture(t, testutil.RequireDB(t), withTerminal(payment.SimApprove, 0))
	returnToCard := func(saleID string) resp {
		t.Helper()
		lineID := f.getSale(t, saleID)["lines"].([]any)[0].(map[string]any)["id"].(string)
		return f.do("POST", "/api/v1/pos/returns", map[string]any{
			"register_id": f.register, "original_sale_id": saleID, "refund_method": "card", "reason": "cp05",
			"lines": []map[string]any{{"line_id": lineID, "quantity": "1", "restock": true}},
		}, "X-Test-Role", "cashier", "X-Test-Sub", mustUUID(t))
	}

	// a card sale with no gateway wired to take it back
	cardID, _ := f.cardSale(t)
	wantCardBlocker(t, "a card return with no gateway", returnToCard(cardID), "card_terminal_not_wired",
		"This register has no card terminal to refund the card")
	r := f.do("POST", "/api/v1/pos/transactions/"+cardID+"/void",
		map[string]any{"reason": "rung in error", "revision": num(t, f.getSale(t, cardID), "revision")},
		"X-Test-Role", "cashier", "X-Test-Sub", mustUUID(t))
	wantCardBlocker(t, "a card void with no gateway", r, "card_terminal_not_wired",
		"This register has no card terminal to put the card back")

	// the same card as the legacy path booked it: no gateway transaction
	mustExec(t, f.db, `UPDATE pos_tenders SET gateway_tx_id = NULL WHERE transaction_id = $1 AND method = 'CARD'`, cardID)
	r = returnToCard(cardID)
	wantCardBlocker(t, "a card return of a card not run through Gable", r, "card_tender",
		"the card on this sale was not run through Gable: refund it on the machine that took it, or from the drawer")
	if strings.Contains(string(r.raw), "not paid by card") {
		t.Fatalf("a sale paid by card was told it was not paid by card: %s", r.raw)
	}

	// a cash sale is still told plainly
	cashID := f.openSale(t, nil)
	if r := f.completeSale(cashID, f.cashAll(t, cashID)); r.status != http.StatusOK {
		t.Fatalf("cash sale = %d: %s", r.status, r.raw)
	}
	wantCardBlocker(t, "a card return of a cash sale", returnToCard(cashID), "card_tender",
		"the sale was not paid by card")
}
