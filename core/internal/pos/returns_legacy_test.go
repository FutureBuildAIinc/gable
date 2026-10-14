// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package pos_test

// The legacy returns of a sale (sixth review P1-1 and P3-2): migration 105
// leaves their quantities and cents positive (it negates only while copying
// into credit memos), so every read takes each source with its own sign; the
// legacy quantity and cents bear on the product's first line by position,
// once, so a sale of several lines of one product loses headroom on one line
// only; and the legacy cents lower that line's remaining money, so the
// rounded shares of later linked returns can never refund past what the sale
// took, and together they sum to it exactly.

import (
	"context"
	"net/http"
	"testing"

	"github.com/gablelbm/gable/internal/payment"
	"github.com/gablelbm/gable/internal/testutil"
	"github.com/google/uuid"
)

// legacyReturnOf writes a legacy (unlinked) return beside a sale in the base
// shape migration 105 leaves: positive quantity, cents and totals.
func legacyReturnOf(t *testing.T, f *fixture, saleID string, qty, unit, line, subtotal, tax, total string, method string) {
	t.Helper()
	legacy := uuid.New()
	mustExec(t, f.db, `INSERT INTO pos_returns (id, register_id, cashier_id, original_transaction_id, subtotal, tax_amount, total,
		refund_method, reason, currency) VALUES ($1, $2, $3, $4, $5::numeric, $6::numeric, $7::numeric, $8, 'legacy', 'USD')`,
		legacy, f.register, mustUUID(t), saleID, subtotal, tax, total, method)
	mustExec(t, f.db, `INSERT INTO pos_return_lines (id, return_id, position, line_type, product_id, description, quantity,
		uom, unit_price, line_total) VALUES ($1, $2, 0, 'PRODUCT', $3, 'legacy', $4::numeric, 'PCS', $5::numeric, $6::numeric)`,
		uuid.New(), legacy, f.productID, qty, unit, line)
	t.Cleanup(func() {
		mustExec(t, f.db, `DELETE FROM pos_return_lines WHERE return_id = $1`, legacy)
		mustExec(t, f.db, `DELETE FROM pos_returns WHERE id = $1`, legacy)
	})
}

// RULE (sixth review P3-2): the legacy quantity bears on the product's first
// line by position, once: a sale of lines 2 and 1 of one product with half a
// unit legacy returned keeps the headroom of the second line whole and loses
// it on the first only.
func TestALegacyReturnBearsOnTheFirstLineOfItsProduct(t *testing.T) {
	testutil.LockOutboxTables(t)
	f := newFixture(t, testutil.RequireDB(t))
	f.openTill(0)
	saleID := f.startSale(&f.customerID)
	if r := f.addLine(saleID, f.productLine("2")); r.status != http.StatusOK {
		t.Fatalf("add line 1 = %d: %s", r.status, r.raw)
	}
	if r := f.addLine(saleID, f.productLine("1")); r.status != http.StatusOK {
		t.Fatalf("add line 2 = %d: %s", r.status, r.raw)
	}
	if r := f.completeSale(saleID, tender("cash", 3593)); r.status != http.StatusOK {
		t.Fatalf("complete = %d: %s", r.status, r.raw)
	}
	lines := f.getSale(t, saleID)["lines"].([]any)
	first := lines[0].(map[string]any)["id"].(string)
	second := lines[1].(map[string]any)["id"].(string)
	legacyReturnOf(t, f, saleID, "0.5", "5.50", "2.75", "2.75", "0.24", "2.99", "CASH")

	// the second line keeps its whole headroom: its one unit comes back
	if r := f.returnOn(t, saleID, second, "1"); r.status != http.StatusCreated {
		t.Fatalf("return of the second line's unit = %d: %s", r.status, r.raw)
	}
	// the first line carries the legacy half: two more of it are refused
	r := f.returnOn(t, saleID, first, "2")
	if r.status != http.StatusConflict {
		t.Fatalf("return of the first line whole = %d, want 409: %s", r.status, r.raw)
	}
	_, blockers, _ := errorOf(t, r)
	if len(blockers) == 0 || blockers[0] != "exceeds_sold" {
		t.Errorf("blockers = %v, want exceeds_sold", blockers)
	}
	// one and a half of it still come back: 2 sold less the legacy half
	if r := f.returnOn(t, saleID, first, "1.5"); r.status != http.StatusCreated {
		t.Fatalf("return of the first line's remainder = %d: %s", r.status, r.raw)
	}
	f.assertARInvariants(t)
}

// RULE (sixth review P3-1): repeated partial returns of fractional
// quantities refund the sale exactly: the return that finishes every
// taxable line takes the rest of the tax (the rounded shares left two
// cents owed), and each restock costs the difference of the cumulative
// positions (the rounded shares restocked three cents past what the sale
// relieved).
func TestSixHalfReturnsOfALineRefundItExactly(t *testing.T) {
	testutil.LockOutboxTables(t)
	f := newFixture(t, testutil.RequireDB(t))
	f.openTill(0)
	// 3 at 5.50: line 1650, tax 146, total 1796
	saleID := f.startSale(&f.customerID)
	if r := f.addLine(saleID, f.productLine("3")); r.status != http.StatusOK {
		t.Fatalf("add line = %d: %s", r.status, r.raw)
	}
	if r := f.completeSale(saleID, tender("cash", 1796)); r.status != http.StatusOK {
		t.Fatalf("complete = %d: %s", r.status, r.raw)
	}
	lineID := f.getSale(t, saleID)["lines"].([]any)[0].(map[string]any)["id"].(string)
	// five shares of 275 line and 24 tax, then the completing half takes the
	// remainder: 275 of line and 26 of tax
	for i, want := range []int64{299, 299, 299, 299, 299, 301} {
		r := f.returnOn(t, saleID, lineID, "0.5")
		if r.status != http.StatusCreated {
			t.Fatalf("half return %d = %d: %s", i+1, r.status, r.raw)
		}
		if got := num(t, r.body, "total_cents"); got != -want {
			t.Errorf("half return %d total = %d, want %d", i+1, got, -want)
		}
		f.assertARInvariants(t)
	}
	if got := f.accountBalance("1010"); got != 0 {
		t.Errorf("cash balance = %d, want 0 (1796 in, exactly 1796 out)", got)
	}
	// the restocks relieved exactly the cost the sale relieved: 3 at 3.25
	var cogs int64
	if err := f.db.Pool.QueryRow(context.Background(), `
		SELECT COALESCE(ROUND(SUM(l.credit) * 100)::bigint, 0) FROM gl_journal_lines l
		JOIN gl_journal_entries e ON e.id = l.journal_entry_id JOIN gl_accounts a ON a.id = l.account_id
		WHERE a.code = '5010' AND e.source_ref_id::text IN
			(SELECT credit_memo_id::text FROM pos_returns WHERE original_transaction_id = $1)`, saleID).Scan(&cogs); err != nil {
		t.Fatal(err)
	}
	if cogs != 975 {
		t.Errorf("the six restocks relieved %d of cost of goods sold, want exactly the 975 the sale relieved", cogs)
	}
	if got := f.stock(); got != "100.0000/0.0000" {
		t.Errorf("stock = %s, want 100 (the three units back once)", got)
	}
}

// RULE (sixth review P2-2, fifth review P2-2): one request naming the same
// sale line several times cannot escape the remainder rule: each duplicate
// would take its own rounded share against the same earlier total, refunding
// a cent more per duplicate than the sale took. The second naming is a 400
// naming the field.
func TestARequestNamingOneSaleLineTwiceIsRefused(t *testing.T) {
	testutil.LockOutboxTables(t)
	f := newFixture(t, testutil.RequireDB(t))
	f.openTill(0)
	// 3 at 5.50 less one cent: line 1649, tax 146, total 1795
	saleID := f.startSale(&f.customerID)
	if r := f.addLine(saleID, map[string]any{"product_id": f.productID.String(), "quantity": "3",
		"discount_cents": 1, "discount_reason": "penny off"}); r.status != http.StatusOK {
		t.Fatalf("add line = %d: %s", r.status, r.raw)
	}
	if r := f.completeSale(saleID, tender("cash", 1795)); r.status != http.StatusOK {
		t.Fatalf("complete = %d: %s", r.status, r.raw)
	}
	lineID := f.getSale(t, saleID)["lines"].([]any)[0].(map[string]any)["id"].(string)
	r := f.do("POST", "/api/v1/pos/returns", map[string]any{
		"register_id": f.register, "original_sale_id": saleID,
		"refund_method": "cash", "reason": "split across lines", "lines": []map[string]any{
			{"line_id": lineID, "quantity": "1", "restock": true},
			{"line_id": lineID, "quantity": "1", "restock": false},
			{"line_id": lineID, "quantity": "1", "restock": false},
		},
	}, "X-Test-Role", "cashier", "X-Test-Sub", mustUUID(t))
	if r.status != http.StatusBadRequest {
		t.Fatalf("a request naming one sale line three times = %d, want 400: %s", r.status, r.raw)
	}
	_, _, fields := errorOf(t, r)
	if len(fields) == 0 || fields[0] != "lines[1].line_id" {
		t.Errorf("fields = %v, want lines[1].line_id (the second naming)", fields)
	}
	// nothing moved: the drawer keeps the sale, the stock keeps its 97
	if got := f.accountBalance("1010"); got != 1795 {
		t.Errorf("cash balance = %d, want 1795 (the refused request paid nothing)", got)
	}
	if got := f.stock(); got != "97.0000/0.0000" {
		t.Errorf("stock = %s, want 97", got)
	}
	f.assertARInvariants(t)
	// the same quantity in one line is the return that was meant
	if r := f.returnOn(t, saleID, lineID, "3"); r.status != http.StatusCreated {
		t.Fatalf("the whole line in one naming = %d: %s", r.status, r.raw)
	}
	if got := f.accountBalance("1010"); got != 0 {
		t.Errorf("cash balance = %d, want 0 (the sale in, the whole line out)", got)
	}
	f.assertARInvariants(t)
}

// RULE (fifth review P3-1, sixth review P2-2): the remainder rule's
// completing branch is provable alone: on a line whose rounded per unit
// shares sum below it (3 units totalling 1648 round to 549 each), the return
// that completes the line takes the remainder (550, not its own 549), so
// repeated partial returns refund exactly what the sale took. With the
// completing branch removed this test goes red on the under refund.
func TestTheReturnThatCompletesALineTakesTheRemainderExactly(t *testing.T) {
	testutil.LockOutboxTables(t)
	f := newFixture(t, testutil.RequireDB(t))
	f.openTill(0)
	// 3 at 5.50 less two cents: line 1648, tax 146, total 1794
	saleID := f.startSale(&f.customerID)
	if r := f.addLine(saleID, map[string]any{"product_id": f.productID.String(), "quantity": "3",
		"discount_cents": 2, "discount_reason": "two off"}); r.status != http.StatusOK {
		t.Fatalf("add line = %d: %s", r.status, r.raw)
	}
	if r := f.completeSale(saleID, tender("cash", 1794)); r.status != http.StatusOK {
		t.Fatalf("complete = %d: %s", r.status, r.raw)
	}
	lineID := f.getSale(t, saleID)["lines"].([]any)[0].(map[string]any)["id"].(string)
	// two returns of one unit: the rounded shares 549 and 549
	for i, want := range []int64{598, 598} {
		r := f.returnOn(t, saleID, lineID, "1")
		if r.status != http.StatusCreated {
			t.Fatalf("partial return %d = %d: %s", i+1, r.status, r.raw)
		}
		if got := num(t, r.body, "total_cents"); got != -want {
			t.Errorf("partial return %d total = %d, want %d", i+1, got, -want)
		}
		f.assertARInvariants(t)
	}
	// the completing return takes the remainder: 1648 less the 1098 the
	// earlier returns took is 550, a cent above its own rounded share
	r := f.returnOn(t, saleID, lineID, "1")
	if r.status != http.StatusCreated {
		t.Fatalf("the completing return = %d: %s", r.status, r.raw)
	}
	if got := num(t, r.body, "subtotal_cents"); got != -550 {
		t.Errorf("the completing return subtotal = %d, want -550 (the line's remainder, not the rounded share)", got)
	}
	if got := f.accountBalance("1010"); got != 0 {
		t.Errorf("cash balance = %d, want 0 (1794 in, exactly 1794 out)", got)
	}
	f.assertARInvariants(t)
}

// RULE (sixth review P1-1): the legacy cents lower the bearer line's
// remaining money, so the return that would round a share past the line
// (0.9 of a 3 unit line whose earlier returns rounded up) is clamped, and
// everything the sale took comes back exactly: the linked shares, the
// clamped last share and the legacy refund sum to the sale. This is also the
// remainder rule's cap branch proved alone: with the clamp removed the last
// return refunds a cent past the sale.
func TestALegacyRefundBoundsTheLinesRemainingMoney(t *testing.T) {
	testutil.LockOutboxTables(t)
	f := newFixture(t, testutil.RequireDB(t))
	f.openTill(0)
	// 3 at 5.50 less one cent: line 1649, tax 146, total 1795
	saleID := f.startSale(&f.customerID)
	if r := f.addLine(saleID, map[string]any{"product_id": f.productID.String(), "quantity": "3",
		"discount_cents": 1, "discount_reason": "penny off"}); r.status != http.StatusOK {
		t.Fatalf("add line = %d: %s", r.status, r.raw)
	}
	if r := f.completeSale(saleID, tender("cash", 1795)); r.status != http.StatusOK {
		t.Fatalf("complete = %d: %s", r.status, r.raw)
	}
	lineID := f.getSale(t, saleID)["lines"].([]any)[0].(map[string]any)["id"].(string)
	// a tenth of a unit came back before the contract, at the list price
	legacyReturnOf(t, f, saleID, "0.1", "5.50", "0.55", "0.55", "0.04", "0.59", "CASH")

	// one unit, then another: each rounds its share up to 550
	for i, want := range []int64{599, 599} {
		r := f.returnOn(t, saleID, lineID, "1")
		if r.status != http.StatusCreated {
			t.Fatalf("partial return %d = %d: %s", i+1, r.status, r.raw)
		}
		if got := num(t, r.body, "total_cents"); got != -want {
			t.Errorf("partial return %d total = %d, want %d", i+1, got, -want)
		}
	}
	// nine tenths: the rounded share is 495, but only 494 remain of the line
	// (1649 less the 1100 the earlier returns took less the 55 the legacy
	// return took)
	r := f.returnOn(t, saleID, lineID, "0.9")
	if r.status != http.StatusCreated {
		t.Fatalf("the clamped return = %d: %s", r.status, r.raw)
	}
	if got := num(t, r.body, "subtotal_cents"); got != -494 {
		t.Errorf("the clamped return subtotal = %d, want -494 (the share clamped to what remains of the line)", got)
	}
	// everything the sale took came back exactly: the linked refunds sum to
	// the sale less the legacy refund (whose cents never left this drawer,
	// the row was written by hand), so the drawer keeps exactly the legacy
	// 59 and never a cent more was paid
	if got := f.accountBalance("1010"); got != 59 {
		t.Errorf("cash balance = %d, want 59 (the sale in, the linked refunds out to the cent of the sale less the legacy refund)", got)
	}
	f.assertARInvariants(t)
}

// RULE (eighth review P3-1): a line the legacy return partly brought back
// completes when the linked returns bring the rest: the return that does it
// takes the line's whole remaining money and the sale's whole remaining tax,
// so no cent is left owed. 3 at 5.50 less one cent (line 1649, tax 146, total
// 1795) with a tenth of a unit legacy returned for 50 cents and 4 of tax: the
// return of the other 2.9 takes 1599 and 142, and with the legacy 54 the sale
// comes back whole.
func TestAPartlyLegacyLineCompletesWithTheLinkedReturn(t *testing.T) {
	testutil.LockOutboxTables(t)
	f := newFixture(t, testutil.RequireDB(t))
	f.openTill(0)
	saleID := f.startSale(&f.customerID)
	if r := f.addLine(saleID, map[string]any{"product_id": f.productID.String(), "quantity": "3",
		"discount_cents": 1, "discount_reason": "penny off"}); r.status != http.StatusOK {
		t.Fatalf("add line = %d: %s", r.status, r.raw)
	}
	if r := f.completeSale(saleID, tender("cash", 1795)); r.status != http.StatusOK {
		t.Fatalf("complete = %d: %s", r.status, r.raw)
	}
	lineID := f.getSale(t, saleID)["lines"].([]any)[0].(map[string]any)["id"].(string)
	legacyReturnOf(t, f, saleID, "0.1", "5.50", "0.50", "0.50", "0.04", "0.54", "CASH")
	f.assertARInvariants(t)
	r := f.returnOn(t, saleID, lineID, "2.9")
	if r.status != http.StatusCreated {
		t.Fatalf("the completing return = %d: %s", r.status, r.raw)
	}
	if got := num(t, r.body, "subtotal_cents"); got != -1599 {
		t.Errorf("the completing return subtotal = %d, want -1599 (the line's remaining money)", got)
	}
	if got := num(t, r.body, "tax_cents"); got != -142 {
		t.Errorf("the completing return tax = %d, want -142 (the sale's tax less the legacy 4)", got)
	}
	if got := f.accountBalance("1010"); got != 54 {
		t.Errorf("cash balance = %d, want 54 (the sale in, the linked refund out; the legacy 54 never left this drawer)", got)
	}
	f.assertARInvariants(t)
}

// RULE (ninth review P3-2, the tax completion): the legacy quantity counts in
// the test that the return finishes every taxable line, so the return that
// brings the last goods back takes the sale's whole remaining tax exactly,
// rounded shares and all. A legacy return of one unit carrying one cent less
// tax than the per unit share leaves the completing return a remainder the
// rounded share alone would strand.
func TestTheLegacyQuantityCompletesTheTaxableLine(t *testing.T) {
	testutil.LockOutboxTables(t)
	gw := &fakeGateway{charges: []*payment.GatewayResult{approvedCharge()}}
	f := newFixture(t, testutil.RequireDB(t), func(f *fixture) {
		f.service = f.service.WithGateway(gw)
	})
	f.openTill(0)
	// 3 at 5.50: line 1650, tax 146, total 1796
	saleID := f.startSale(nil)
	if r := f.addLine(saleID, f.productLine("3")); r.status != http.StatusOK {
		t.Fatalf("add line = %d: %s", r.status, r.raw)
	}
	if r := f.completeSale(saleID, withToken(tender("card", 1796))); r.status != http.StatusOK {
		t.Fatalf("complete = %d: %s", r.status, r.raw)
	}
	lineID := f.getSale(t, saleID)["lines"].([]any)[0].(map[string]any)["id"].(string)
	// a legacy return of one unit whose recorded tax is 48, one cent under
	// the per unit 49: the linked return of the other two completes the line
	legacyReturnOf(t, f, saleID, "1", "5.50", "5.50", "5.50", "0.48", "5.98", "CASH")
	f.assertARInvariants(t)
	r := f.cardReturnOn(t, saleID, lineID, "2")
	if r.status != http.StatusCreated {
		t.Fatalf("the completing card return = %d: %s", r.status, r.raw)
	}
	if got := num(t, r.body, "subtotal_cents"); got != -1100 {
		t.Errorf("the completing return subtotal = %d, want -1100 (the line's remaining money)", got)
	}
	if got := num(t, r.body, "tax_cents"); got != -98 {
		t.Errorf("the completing return tax = %d, want -98 (the sale's 146 less the legacy 48: the rounded share 97 would strand a cent)", got)
	}
	if got := f.scalar(`SELECT ROUND(amount * 100)::bigint FROM pos_refund_intents WHERE sale_id = $1 AND act = 'return'`, saleID).(int64); got != 1198 {
		t.Errorf("the claim holds %d cents, want 1198", got)
	}
	f.assertARInvariants(t)
}
