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
	"strings"
	"testing"

	"github.com/gablelbm/gable/internal/testutil"
	"github.com/google/uuid"
)

func TestAWalkInReturnRefusesAccount(t *testing.T) {
	testutil.LockOutboxTables(t)
	f := newFixture(t, testutil.RequireDB(t))
	saleID, body := f.saleOf("1", tender("cash", 599)) // a walk-in sale
	lineID := body.body["lines"].([]any)[0].(map[string]any)["id"].(string)
	r := f.do("POST", "/api/v1/pos/returns", map[string]any{
		"register_id": f.register, "original_sale_id": saleID, "refund_method": "account",
		"reason": "store credit for a stranger",
		"lines":  []map[string]any{{"line_id": lineID, "quantity": "1", "restock": true}},
	}, "X-Test-Role", "cashier", "X-Test-Sub", mustUUID(t))
	if r.status != http.StatusBadRequest {
		t.Fatalf("walk-in account return = %d, want 400: %s", r.status, r.raw)
	}
	// nothing was written: no memo, no return row, the stock whole
	if got := countOf(t, f.db, `SELECT count(*) FROM credit_memos WHERE customer_id = (SELECT id FROM customers WHERE account_number = 'WALK-IN')`); got != 0 {
		t.Errorf("%d memos on the walk-in customer, want 0", got)
	}
	if got := f.counterReturns(t); got != 0 {
		t.Errorf("%d return rows, want 0", got)
	}
	if got := f.stock(); got != "99.0000/0.0000" {
		t.Errorf("stock = %s, want 99/0 (the sale's unit still out)", got)
	}
	f.assertARInvariants(t)
}

// RULE (third review P3-D): naming the walk-in customer's id explicitly is
// still the walk-in: the account refund is refused whatever way the customer
// was named.
func TestAWalkInReturnNamedAsTheWalkInIsRefusedToo(t *testing.T) {
	testutil.LockOutboxTables(t)
	f := newFixture(t, testutil.RequireDB(t))
	saleID, body := f.saleOf("1", tender("cash", 599))
	lineID := body.body["lines"].([]any)[0].(map[string]any)["id"].(string)
	walkIn := f.scalar(`SELECT id::text FROM customers WHERE account_number = 'WALK-IN'`).(string)
	r := f.do("POST", "/api/v1/pos/returns", map[string]any{
		"register_id": f.register, "customer_id": walkIn, "original_sale_id": saleID, "refund_method": "account",
		"reason": "named walk-in", "lines": []map[string]any{{"line_id": lineID, "quantity": "1"}},
	}, "X-Test-Role", "cashier", "X-Test-Sub", mustUUID(t))
	if r.status != http.StatusBadRequest {
		t.Fatalf("named walk-in account return = %d, want 400: %s", r.status, r.raw)
	}
	if got := f.counterMemos(t); got != 0 {
		t.Errorf("%d memos on the walk-in customer, want 0", got)
	}
	f.assertARInvariants(t)
}

// RULE (round 22 ruling): a return against a sale credits that sale's
// customer. A named customer other than the sale's is refused 400 on the
// field, and a walk-in sale's goods never become another customer's account
// credit; nothing is written.
func TestAReturnCreditsTheSalesOwnCustomerOnly(t *testing.T) {
	testutil.LockOutboxTables(t)
	f := newFixture(t, testutil.RequireDB(t))
	saleID, body := f.saleOf("1", tender("cash", 599)) // a walk-in sale
	lineID := body.body["lines"].([]any)[0].(map[string]any)["id"].(string)
	r := f.do("POST", "/api/v1/pos/returns", map[string]any{
		"register_id": f.register, "customer_id": f.customerID.String(), "original_sale_id": saleID,
		"refund_method": "account", "reason": "someone else's credit",
		"lines": []map[string]any{{"line_id": lineID, "quantity": "1"}},
	}, "X-Test-Role", "cashier", "X-Test-Sub", mustUUID(t))
	if r.status != http.StatusBadRequest || !strings.Contains(string(r.raw), "customer_id") {
		t.Fatalf("a walk-in sale's return naming a customer = %d, want 400 on customer_id: %s", r.status, r.raw)
	}
	// a customer sale refuses a different customer too
	other := uuid.New()
	mustExec(t, f.db, `INSERT INTO customers (id, name, account_number, primary_branch_id) VALUES ($1, 'Other Co', $2, $3)`, other, "OTH-"+uuid.NewString()[:8], f.branchID)
	t.Cleanup(func() { mustExec(t, f.db, `DELETE FROM customers WHERE id = $1`, other) })
	mustExec(t, f.db, `UPDATE pos_transactions SET customer_id = $1 WHERE id = $2`, f.customerID, saleID)
	r = f.do("POST", "/api/v1/pos/returns", map[string]any{
		"register_id": f.register, "customer_id": other.String(), "original_sale_id": saleID,
		"refund_method": "account", "reason": "someone else's credit",
		"lines": []map[string]any{{"line_id": lineID, "quantity": "1"}},
	}, "X-Test-Role", "cashier", "X-Test-Sub", mustUUID(t))
	if r.status != http.StatusBadRequest || !strings.Contains(string(r.raw), "customer_id") {
		t.Fatalf("a return naming another customer = %d, want 400 on customer_id: %s", r.status, r.raw)
	}
	if got := countOf(t, f.db, `SELECT count(*) FROM credit_memos WHERE customer_id = $1`, other); got != 0 {
		t.Errorf("%d memos on the other customer, want 0", got)
	}
	if got := f.counterReturns(t); got != 0 {
		t.Errorf("%d return rows, want 0", got)
	}
}
