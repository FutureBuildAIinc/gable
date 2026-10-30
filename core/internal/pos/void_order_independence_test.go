// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package pos_test

// The order independence of a split void's resolutions (r12 P1-1): a void
// with several card claims ends in the same state whatever order its claims
// are resolved in. Each shape below strands a void of one walk-in unit (599)
// with some claims refunded at the terminal and the others unknown or
// declined, resolves every standing claim in EVERY order, and asserts one
// identical end state per shape: the sale status, the 1010, 1020 and 2200
// balances, the walk-in customer's balance, the stock and the drawer's
// expected cash. The resolutions themselves never reach the terminal: the
// stub gateway's refund call count stays what the void itself made.

import (
	"context"
	"net/http"
	"testing"

	"github.com/gablelbm/gable/internal/payment"
	"github.com/gablelbm/gable/internal/testutil"
)

// orderTender is one tender of a shape: its method and cents.
type orderTender struct {
	method string
	cents  int64
}

// orderShape is one stranded void shape: the tenders of the sale, the
// terminal's answer per card call the void makes ("ok", "timeout" or
// "decline"), and how many calls the void makes before it is refused. Which
// tender takes which answer follows the tenders' own order, so the moved
// total is read back from the claims each subtest strands.
type orderShape struct {
	name    string
	tenders []orderTender
	answers []string
	gwCalls int
}

// orderShapes are r12's Area 2 shapes: M3a against M3b, M5a against M5b, M6a
// to M6c, and M10 with cash. A declined answer releases its own claim and
// stops the void, so the cards after it are never called and their claims
// stand uncalled; every other standing claim is resolved per its own record.
var orderShapes = []orderShape{
	{name: "two cards, one refunded, one timed out (M3)",
		tenders: []orderTender{{"card", 300}, {"card", 299}}, answers: []string{"ok", "timeout"},
		gwCalls: 2},
	{name: "three cards, one refunded, one declined, one never called (M5)",
		tenders: []orderTender{{"card", 200}, {"card", 200}, {"card", 199}}, answers: []string{"ok", "decline"},
		gwCalls: 2},
	{name: "three cards, two refunded, one timed out (M6)",
		tenders: []orderTender{{"card", 200}, {"card", 200}, {"card", 199}}, answers: []string{"ok", "ok", "timeout"},
		gwCalls: 3},
	{name: "cash with two cards, one refunded, one timed out (M10)",
		tenders: []orderTender{{"cash", 199}, {"card", 200}, {"card", 200}}, answers: []string{"ok", "timeout"},
		gwCalls: 2},
}

// standingOf answers how many claims of the shape stand for the operator to
// resolve: a declined call releases its claim, an ok or a timeout leaves it
// standing, and a card never called (after a decline) stands uncalled.
func (s orderShape) standingOf() int {
	cards := 0
	for _, td := range s.tenders {
		if td.method == "card" {
			cards++
		}
	}
	declined := 0
	for _, a := range s.answers {
		if a == "decline" {
			declined++
		}
	}
	return cards - declined
}

// permutations lists every order of the integers 0 to n-1.
func permutations(n int) [][]int {
	perms := [][]int{{}}
	for i := 0; i < n; i++ {
		var next [][]int
		for _, p := range perms {
			for j := 0; j <= len(p); j++ {
				q := append([]int(nil), p[:j]...)
				q = append(q, i)
				q = append(q, p[j:]...)
				next = append(next, q)
			}
		}
		perms = next
	}
	return perms
}

// orderGateway builds the stub gateway for one shape: one approved charge per
// card tender, and the terminal's answers scripted per refund call in call
// order (the scripts are positional, so every call before a scripted answer
// carries an explicit nil or the default status).
func orderGateway(t *testing.T, shape orderShape) *fakeGateway {
	t.Helper()
	cards := 0
	for _, td := range shape.tenders {
		if td.method == "card" {
			cards++
		}
	}
	gw := &fakeGateway{charges: make([]*payment.GatewayResult, 0, cards)}
	for i := 0; i < cards; i++ {
		gw.charges = append(gw.charges, approvedCharge())
	}
	for i, answer := range shape.answers {
		for len(gw.refundErrs) < i {
			gw.refundErrs = append(gw.refundErrs, nil)
		}
		for len(gw.refundStatuses) < i {
			gw.refundStatuses = append(gw.refundStatuses, payment.GatewayStatusRefunded)
		}
		switch answer {
		case "timeout":
			gw.refundErrs = append(gw.refundErrs, context.DeadlineExceeded)
			gw.refundStatuses = append(gw.refundStatuses, payment.GatewayStatusRefunded)
		case "decline":
			gw.refundErrs = append(gw.refundErrs, nil)
			gw.refundStatuses = append(gw.refundStatuses, payment.GatewayStatusDeclined)
		default:
			gw.refundErrs = append(gw.refundErrs, nil)
			gw.refundStatuses = append(gw.refundStatuses, payment.GatewayStatusRefunded)
		}
	}
	return gw
}

// walkInBalance reads the walk-in customer's subledger balance in cents,
// over the rows this fixture's own documents made. The walk-in row is shared
// by every counter in the deployment, so its balance is the sum of the rows
// the documents of this fixture named, never the customer's whole balance:
// another package's walk-in sale would move that.
func (f *fixture) walkInBalance(t *testing.T) int64 {
	t.Helper()
	var n int64
	if err := f.db.Pool.QueryRow(context.Background(), `
		SELECT COALESCE(SUM(amount)::bigint, 0) FROM customer_transactions
		WHERE customer_id = (SELECT id FROM customers WHERE account_number = 'WALK-IN')
			AND reference_id IN (
				(SELECT id FROM invoices WHERE branch_id = $1)
				UNION (SELECT id FROM credit_memos WHERE branch_id = $1)
				UNION (SELECT id FROM payments WHERE branch_id = $1)
				UNION (SELECT id FROM pos_transactions WHERE branch_id = $1)
				UNION (SELECT id FROM pos_returns WHERE branch_id = $1)
				UNION (SELECT id FROM ar_applications
					WHERE invoice_id IN (SELECT id FROM invoices WHERE branch_id = $1)
						OR payment_id IN (SELECT id FROM payments WHERE branch_id = $1)
						OR credit_memo_id IN (SELECT id FROM credit_memos WHERE branch_id = $1))
				UNION (SELECT id FROM payment_refunds
					WHERE payment_id IN (SELECT id FROM payments WHERE branch_id = $1)
						OR credit_memo_id IN (SELECT id FROM credit_memos WHERE branch_id = $1)))`,
		f.branchID).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

// expectedOf reads the open session's expected_by_method figure for one
// method through the report route.
func (f *fixture) expectedOf(t *testing.T, sessionID, method string) int64 {
	t.Helper()
	r := f.do("GET", "/api/v1/pos/till/"+sessionID+"/report", nil, "X-Test-Role", "finance", "X-Test-Sub", mustUUID(t))
	if r.status != http.StatusOK {
		t.Fatalf("the till report = %d: %s", r.status, r.raw)
	}
	byMethod, ok := r.body["expected_by_method"].(map[string]any)
	if !ok {
		t.Fatalf("the report carries no expected_by_method: %s", r.raw)
	}
	v, ok := byMethod[method]
	if !ok {
		return 0
	}
	return int64Of(v)
}

// orderSale completes one sale of the shape's tenders on a fresh fixture with
// the drawer open at a 10000 float, answering the fixture, the sale id, the
// completion body and the stub gateway.
func orderSale(t *testing.T, shape orderShape) (*fixture, string, resp, *fakeGateway, string) {
	t.Helper()
	gw := orderGateway(t, shape)
	f := newFixture(t, testutil.RequireDBMaxConns(t, 4), func(f *fixture) {
		f.service = f.service.WithGateway(gw)
	})
	sessionID := f.openTill(10000)
	tenders := make([]map[string]any, 0, len(shape.tenders))
	for _, td := range shape.tenders {
		tk := tender(td.method, td.cents)
		if td.method == "card" {
			tk = withToken(tk)
		}
		tenders = append(tenders, tk)
	}
	saleID := f.startSale(nil)
	if r := f.addLine(saleID, f.productLine("1")); r.status != http.StatusOK {
		t.Fatalf("add line = %d: %s", r.status, r.raw)
	}
	body := f.completeSale(saleID, tenders...)
	if body.status != http.StatusOK {
		t.Fatalf("complete = %d: %s", body.status, body.raw)
	}
	return f, saleID, body, gw, sessionID
}

// RULE (r12 P1-1): a void with several card claims ends in the same state
// whatever order its claims are resolved in. The natural order (book the
// refund that moved first, the no refund last) and every other order void
// the sale, restock the unit, leave the unrefunded cards' charge as unapplied
// credit and the drawer expecting its float back, and no resolution reaches
// the terminal.
func TestASplitVoidEndsTheSameStateWhateverOrderItsClaimsAreResolved(t *testing.T) {
	testutil.LockOutboxTables(t)
	for _, shape := range orderShapes {
		for _, order := range permutations(shape.standingOf()) {
			t.Run(shape.name+" / resolves "+orderName(order), func(t *testing.T) {
				f, saleID, body, gw, sessionID := orderSale(t, shape)
				if r := f.do("POST", "/api/v1/pos/transactions/"+saleID+"/void",
					map[string]any{"reason": "split void", "revision": rev(t, body)},
					"X-Test-Role", "cashier", "X-Test-Sub", mustUUID(t)); r.status == http.StatusOK {
					t.Fatalf("the stranded void committed: %s", r.raw)
				}
				// the standing claims, sorted so the refund carrying claims
				// come first: the order names stay deterministic (0-1 is
				// always refund then no refund) whatever order the claims were
				// created in. The resolutions run in the order this case
				// names. The moved total is read from the claims themselves:
				// which tender took the terminal's ok follows the tenders'
				// own order, not the shape's list.
				var withID, withoutID []map[string]any
				var refunded int64
				for _, in := range f.saleIntents(t, saleID) {
					if in["state"] != "PENDING" {
						continue
					}
					if in["refund_tx_id"] != "" {
						withID = append(withID, in)
						refunded += in["amount"].(int64)
					} else {
						withoutID = append(withoutID, in)
					}
				}
				standing := append(withID, withoutID...)
				if len(standing) != len(order) {
					t.Fatalf("%d standing claims, want %d", len(standing), len(order))
				}
				for _, i := range order {
					in := standing[i]
					if in["refund_tx_id"] != "" {
						if rr := f.resolveOn(t, in["id"].(string), map[string]any{
							"outcome": "refund", "refund_tx_id": in["refund_tx_id"].(string), "reason": "the terminal refunded this card",
						}, "finance"); rr.status != http.StatusOK {
							t.Fatalf("the refund resolution = %d: %s", rr.status, rr.raw)
						}
					} else if rr := f.resolveOn(t, in["id"].(string), map[string]any{
						"outcome": "no_refund", "reconciliation_reference": "REC-ORDER", "reason": "the terminal log shows no refund",
					}, "finance"); rr.status != http.StatusOK {
						t.Fatalf("the no refund resolution = %d: %s", rr.status, rr.raw)
					}
				}
				// one identical end state per shape, whatever the order was
				if got := str(t, f.getSale(t, saleID), "status"); got != "voided" {
					t.Errorf("sale status = %q, want voided", got)
				}
				// the drawer methods' charge leaves the books with the void's
				// completion (the cash payment voids whole), so the unrefunded
				// figure the shape asserts is the card charge the terminal
				// never paid back
				var drawerCents int64
				for _, td := range shape.tenders {
					if td.method == "cash" || td.method == "check" {
						drawerCents += td.cents
					}
				}
				unrefunded := 599 - drawerCents - refunded
				if got := f.accountBalance("1010"); got != unrefunded {
					t.Errorf("card balance = %d, want %d (exactly the %d the terminal moved paid back)", got, unrefunded, refunded)
				}
				if got := f.accountBalance("1020"); got != 0 {
					t.Errorf("receivable balance = %d, want 0 (the invoice voided)", got)
				}
				if got := f.accountBalance("2200"); got != -unrefunded {
					t.Errorf("deposit balance = %d, want %d (the unrefunded charge as unapplied credit)", got, -unrefunded)
				}
				if got := f.walkInBalance(t); got != 0 {
					t.Errorf("walk-in balance = %d, want 0 (the credit sits unapplied, never owed)", got)
				}
				if got := f.stock(); got != "100.0000/0.0000" {
					t.Errorf("stock = %s, want 100 (the void restocked the unit)", got)
				}
				if got := f.expectedOf(t, sessionID, "CASH"); got != 10000 {
					t.Errorf("the drawer's expected cash = %d, want 10000 (the float: the voided sale's cash payment left the expectation)", got)
				}
				if got := f.expectedOf(t, sessionID, "CARD"); got != 0 {
					t.Errorf("the drawer's expected card = %d, want 0 (the voided sale's card tenders left the figure)", got)
				}
				gw.mu.Lock()
				calls := len(gw.refunds)
				gw.mu.Unlock()
				if calls != shape.gwCalls {
					t.Errorf("%d gateway refund calls, want %d (the void's own calls: a resolution never reaches the terminal)", calls, shape.gwCalls)
				}
				f.assertARInvariants(t)
			})
		}
	}
}

// orderName names one order for the subtest's title.
func orderName(order []int) string {
	out := make([]byte, 0, len(order)*2)
	for _, i := range order {
		if len(out) > 0 {
			out = append(out, '-')
		}
		out = append(out, byte('0'+i))
	}
	return string(out)
}
