// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package pos_test

// The walk-in customer cannot hold account credit (first review P2-2,
// second review P3-6): a return resolved to the walk-in customer and
// refunded to account is refused walk_in_account, exactly as the sale side
// refuses an ACCOUNT tender for the walk-in; the credit a walk-in returns
// is paid out of the drawer or back to the card, never left open on a
// customer that cannot hold it.

import (
	"net/http"
	"testing"

	"github.com/gablelbm/gable/internal/testutil"
)

func TestAWalkInReturnRefusesAccount(t *testing.T) {
	testutil.LockOutboxTables(t)
	f := newFixture(t, testutil.RequireDB(t))
	saleID, _ := f.saleOf("1", tender("cash", 599)) // a walk-in sale
	r := f.do("POST", "/api/v1/pos/returns", map[string]any{
		"register_id": f.register, "original_sale_id": saleID, "refund_method": "account",
		"reason": "store credit for a stranger",
		"lines": []map[string]any{{"product_id": f.productID.String(), "quantity": "1",
			"unit_price_ten_thousandths": 55000, "restock": true}},
	}, "X-Test-Role", "cashier", "X-Test-Sub", mustUUID(t))
	if r.status != http.StatusConflict {
		t.Fatalf("walk-in account return = %d, want 409: %s", r.status, r.raw)
	}
	_, blockers, _ := errorOf(t, r)
	if len(blockers) == 0 || blockers[0] != "walk_in_account" {
		t.Errorf("blockers = %v, want walk_in_account", blockers)
	}
	// nothing was written: no memo, no return row, the stock whole
	if got := countOf(t, f.db, `SELECT count(*) FROM credit_memos WHERE customer_id = (SELECT id FROM customers WHERE account_number = 'WALK-IN')`); got != 0 {
		t.Errorf("%d memos on the walk-in customer, want 0", got)
	}
	if got := countOf(t, f.db, `SELECT count(*) FROM pos_returns`); got != 0 {
		t.Errorf("%d return rows, want 0", got)
	}
	if got := f.stock(); got != "99.0000/0.0000" {
		t.Errorf("stock = %s, want 99/0 (the sale's unit still out)", got)
	}
	f.assertARInvariants(t)
}
