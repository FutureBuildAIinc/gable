// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package pos_test

// The return is bounded by the sale it names (ADR 0005 section 14.2 C2-5,
// second review P1-1): a line naming a sale line refunds at that line's own
// extension per unit with its tax share, never the list price; a client
// price on a linked line is refused; the quantity cannot exceed what the
// line sold less every earlier return of it; and a discounted line returns
// at its discounted amount.

import (
	"net/http"
	"testing"

	"github.com/gablelbm/gable/internal/testutil"
)

// returnOn is a linked cash return of qty of the sale's first line.
func (f *fixture) returnOn(t *testing.T, saleID, lineID, qty string, extra ...map[string]any) resp {
	t.Helper()
	line := map[string]any{"line_id": lineID, "quantity": qty, "restock": true}
	for _, e := range extra {
		for k, v := range e {
			line[k] = v
		}
	}
	return f.do("POST", "/api/v1/pos/returns", map[string]any{
		"register_id": f.register, "customer_id": f.customerID.String(), "original_sale_id": saleID,
		"refund_method": "cash", "reason": "bounds", "lines": []map[string]any{line},
	}, "X-Test-Role", "cashier", "X-Test-Sub", mustUUID(t))
}

// RULE: a return line that names a sale line refunds at that line's own
// extension per unit with its tax share, and never more of the line than was
// sold less every earlier return of it.
func TestReturnIsBoundedByTheSale(t *testing.T) {
	testutil.LockOutboxTables(t)
	f := newFixture(t, testutil.RequireDB(t))
	saleID, _ := f.saleOf("4", tender("cash", 2395))
	sale := f.getSale(t, saleID)
	lineID := sale["lines"].([]any)[0].(map[string]any)["id"].(string)

	// One unit back: the line's own extension (550) with its tax share of the
	// sale's tax (49 of 195 on 2200), not the branch rate recomputed.
	r := f.returnOn(t, saleID, lineID, "1")
	if r.status != http.StatusCreated {
		t.Fatalf("return of 1 = %d: %s", r.status, r.raw)
	}
	for key, want := range map[string]int64{"subtotal_cents": -550, "tax_cents": -49, "total_cents": -599} {
		if got := num(t, r.body, key); got != want {
			t.Errorf("return of 1 %s = %d, want %d", key, got, want)
		}
	}
	f.assertARInvariants(t)

	// Ten more against a line that sold 4 and gave 1 back: refused.
	r = f.returnOn(t, saleID, lineID, "10")
	if r.status != http.StatusConflict {
		t.Fatalf("over return = %d, want 409: %s", r.status, r.raw)
	}
	_, blockers, _ := errorOf(t, r)
	if len(blockers) == 0 || blockers[0] != "exceeds_sold" {
		t.Errorf("blockers = %v, want exceeds_sold", blockers)
	}
	// Five of a line that sold 4: refused outright.
	saleID2, _ := f.saleOf("4", tender("cash", 2395))
	line2 := f.getSale(t, saleID2)["lines"].([]any)[0].(map[string]any)["id"].(string)
	r = f.returnOn(t, saleID2, line2, "5")
	if r.status != http.StatusConflict {
		t.Fatalf("return of 5 of 4 = %d, want 409: %s", r.status, r.raw)
	}
	// The drawer paid out only the bounded refunds: two sales of 2395 in, one
	// 599 return out; every refused return moved nothing.
	if got := f.accountBalance("1010"); got != 2395+2395-599 {
		t.Errorf("cash balance = %d, want %d (two sales in, one bounded refund out)", got, 2395+2395-599)
	}
	f.assertARInvariants(t)

	// A client price on a linked line is refused: the refund follows the sale.
	r = f.returnOn(t, saleID2, line2, "1", map[string]any{"unit_price_ten_thousandths": 990000})
	if r.status != http.StatusBadRequest {
		t.Fatalf("client price on a linked line = %d, want 400: %s", r.status, r.raw)
	}
	_, _, fields := errorOf(t, r)
	if len(fields) == 0 || fields[0] != "lines[0].unit_price_ten_thousandths" {
		t.Errorf("fields = %v, want lines[0].unit_price_ten_thousandths", fields)
	}

	// A free line (no sale named) still stands on its own price.
	r = f.do("POST", "/api/v1/pos/returns", map[string]any{
		"register_id": f.register, "customer_id": f.customerID.String(), "refund_method": "cash",
		"reason": "no receipt", "lines": []map[string]any{{
			"product_id": f.productID.String(), "quantity": "1",
			"unit_price_ten_thousandths": 45800, "restock": false,
		}},
	}, "X-Test-Role", "cashier", "X-Test-Sub", mustUUID(t))
	if r.status != http.StatusCreated {
		t.Fatalf("free return = %d: %s", r.status, r.raw)
	}
	if got := num(t, r.body, "subtotal_cents"); got != -458 {
		t.Errorf("free return subtotal = %d, want -458 (its own price)", got)
	}
	f.assertARInvariants(t)
}

// RULE: a discounted line returns at its discounted amount, tax share
// included: the whole money taken comes back, no more.
func TestADiscountedLineReturnsAtItsDiscountedAmount(t *testing.T) {
	testutil.LockOutboxTables(t)
	f := newFixture(t, testutil.RequireDB(t))
	saleID := f.startSale(&f.customerID)
	if r := f.addLine(saleID, map[string]any{"product_id": f.productID.String(), "quantity": "2",
		"discount_cents": 550, "discount_reason": "half off"}); r.status != http.StatusOK {
		t.Fatalf("discount line = %d: %s", r.status, r.raw)
	}
	if r := f.completeSale(saleID, tender("cash", 599)); r.status != http.StatusOK {
		t.Fatalf("complete = %d: %s", r.status, r.raw)
	}
	lineID := f.getSale(t, saleID)["lines"].([]any)[0].(map[string]any)["id"].(string)
	r := f.returnOn(t, saleID, lineID, "2")
	if r.status != http.StatusCreated {
		t.Fatalf("full return = %d: %s", r.status, r.raw)
	}
	for key, want := range map[string]int64{"subtotal_cents": -550, "tax_cents": -49, "total_cents": -599} {
		if got := num(t, r.body, key); got != want {
			t.Errorf("discounted return %s = %d, want %d (the money taken)", key, got, want)
		}
	}
	if got := f.accountBalance("1010"); got != 0 {
		t.Errorf("cash balance = %d, want 0 (599 in, 599 out)", got)
	}
	f.assertARInvariants(t)
}

// RULE (third review P1-1): a return that names a sale names its lines: a
// product line with a client price on a named sale is refused, so every line
// of a named sale is bounded by the line it returns. Only a return that names
// no sale stands free.
func TestAProductLineOnANamedSaleIsRefused(t *testing.T) {
	testutil.LockOutboxTables(t)
	f := newFixture(t, testutil.RequireDB(t))
	f.openTill(0)
	saleID, _ := f.saleOf("1", tender("cash", 599))
	r := f.do("POST", "/api/v1/pos/returns", map[string]any{
		"register_id": f.register, "customer_id": f.customerID.String(), "original_sale_id": saleID,
		"refund_method": "cash", "reason": "no line named", "lines": []map[string]any{{
			"product_id": f.productID.String(), "quantity": "1",
			"unit_price_ten_thousandths": 55000,
		}},
	}, "X-Test-Role", "cashier", "X-Test-Sub", mustUUID(t))
	if r.status != http.StatusBadRequest {
		t.Fatalf("product line on a named sale = %d, want 400: %s", r.status, r.raw)
	}
	_, _, fields := errorOf(t, r)
	if len(fields) == 0 || fields[0] != "lines[0].line_id" {
		t.Errorf("fields = %v, want lines[0].line_id", fields)
	}
	// nothing moved: the drawer still holds the sale, the stock still 99
	if got := f.accountBalance("1010"); got != 599 {
		t.Errorf("cash balance = %d, want 599 (the refused return paid nothing)", got)
	}
	if got := f.stock(); got != "99.0000/0.0000" {
		t.Errorf("stock = %s, want 99", got)
	}
	f.assertARInvariants(t)
}

// RULE (fourth review P2-1): repeated partial returns of one line never
// refund more than the sale took: the return that completes the line takes
// the remainder (the whole line total and the whole tax less what earlier
// returns already refunded), so the rounded shares of the earlier returns
// cannot drift the total past what was paid.
func TestRepeatedPartialReturnsRefundNoMoreThanTheSale(t *testing.T) {
	testutil.LockOutboxTables(t)
	f := newFixture(t, testutil.RequireDB(t))
	f.openTill(0)
	// 3 at 5.50 less one cent: line 1649, tax 146, total 1795 in cash
	saleID := f.startSale(&f.customerID)
	if r := f.addLine(saleID, map[string]any{"product_id": f.productID.String(), "quantity": "3",
		"discount_cents": 1, "discount_reason": "penny off"}); r.status != http.StatusOK {
		t.Fatalf("add line = %d: %s", r.status, r.raw)
	}
	if r := f.completeSale(saleID, tender("cash", 1795)); r.status != http.StatusOK {
		t.Fatalf("complete = %d: %s", r.status, r.raw)
	}
	lineID := f.getSale(t, saleID)["lines"].([]any)[0].(map[string]any)["id"].(string)

	// three returns of one unit each: 599, 599, then the remainder 597
	var refunded int64
	for i, want := range []int64{599, 599, 597} {
		r := f.returnOn(t, saleID, lineID, "1")
		if r.status != http.StatusCreated {
			t.Fatalf("partial return %d = %d: %s", i+1, r.status, r.raw)
		}
		if got := num(t, r.body, "total_cents"); got != -want {
			t.Errorf("partial return %d total = %d, want %d", i+1, got, -want)
		}
		refunded += want
		f.assertARInvariants(t)
	}
	if refunded != 1795 {
		t.Errorf("%d refunded over the three returns, want exactly the 1795 taken", refunded)
	}
	if got := f.accountBalance("1010"); got != 0 {
		t.Errorf("cash balance = %d, want 0 (1795 in, 1795 out, never a cent more)", got)
	}
	// the line is fully returned: nothing more comes back
	if r := f.returnOn(t, saleID, lineID, "1"); r.status != http.StatusConflict {
		t.Fatalf("return past the sold quantity = %d, want 409: %s", r.status, r.raw)
	}
	f.assertARInvariants(t)
}
