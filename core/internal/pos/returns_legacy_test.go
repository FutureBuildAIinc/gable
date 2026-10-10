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
	"net/http"
	"testing"

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
