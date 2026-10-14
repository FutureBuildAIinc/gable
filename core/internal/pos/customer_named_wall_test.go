// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package pos_test

// The customer a caller may name at the counter has one rule (round 21): the
// lookup, the attach, the create, the offline sync and the completion all
// hold the named customer to the same customer wall. A user granted only the
// sale's branch, sending no X-Branch-Id header, who names a customer only
// another branch trades with is answered like a missing customer, and
// nothing is written; an account tender never posts receivable against it.

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/gablelbm/gable/internal/testutil"
	"github.com/google/uuid"
)

type namedWall struct {
	f         *fixture
	headers   []string
	hidden    uuid.UUID // trades only at branch B
	reachable uuid.UUID // trades at the sale's branch A
}

func newNamedWall(t *testing.T) *namedWall {
	t.Helper()
	testutil.LockOutboxTables(t)
	db := testutil.RequireDB(t)
	// the kill switch is read when the fixture's middleware is built
	setPosSetting(t, db, "multi_branch_enabled", "true")
	setPosSetting(t, db, "default_branch_required", "false")
	branchB := uuid.New()
	exec := func(sql string, args ...any) {
		t.Helper()
		if _, err := db.Pool.Exec(context.Background(), sql, args...); err != nil {
			t.Fatalf("%s: %v", sql, err)
		}
	}
	exec(`INSERT INTO locations (id, type, code, name) VALUES ($1, 'BRANCH', 'WL-POS-N', 'named wall B')`, branchB)
	w := &namedWall{hidden: uuid.New()}
	f := newFixture(t, db)
	w.f = f
	w.reachable = f.customerID
	exec(`INSERT INTO user_locations (user_sub, branch_id, is_home, granted_by) VALUES ('pos-cw-a', $1, TRUE, 'test')`, f.branchID)
	exec(`INSERT INTO customers (id, name, account_number, primary_branch_id) VALUES ($1, 'Hidden Co', $2, $3)`,
		w.hidden, "HID-"+uuid.NewString()[:8], branchB)
	exec(`INSERT INTO customer_branches (customer_id, branch_id) VALUES ($1, $2), ($3, $4)`, w.hidden, branchB, w.reachable, f.branchID)
	t.Cleanup(func() {
		setPosSetting(t, db, "multi_branch_enabled", "false")
		setPosSetting(t, db, "default_branch_required", "true")
		exec(`DELETE FROM pos_transactions WHERE customer_id = $1`, w.hidden)
		exec(`DELETE FROM invoices WHERE customer_id = $1`, w.hidden)
		exec(`DELETE FROM customer_branches WHERE customer_id IN ($1, $2)`, w.hidden, w.reachable)
		exec(`DELETE FROM customers WHERE id = $1`, w.hidden)
		exec(`DELETE FROM user_locations WHERE user_sub = 'pos-cw-a'`)
		exec(`DELETE FROM locations WHERE id = $1`, branchB)
	})
	w.headers = []string{"X-Test-Role", "cashier", "X-Test-Sub", "pos-cw-a"}
	return w
}

func (w *namedWall) salesNaming(id uuid.UUID) int64 {
	return countOf(w.f.t, w.f.db, `SELECT count(*) FROM pos_transactions WHERE customer_id = $1`, id)
}

// RULE: the create names a customer through the same wall the attach uses.
func TestTheCreateHoldsTheNamedCustomerToTheCustomerWall(t *testing.T) {
	w := newNamedWall(t)
	f := w.f
	body := func(c uuid.UUID) map[string]any {
		return map[string]any{"register_id": f.register, "customer_id": c.String()}
	}
	// the attach and the lookup already hide it; the create must too
	if r := f.do("GET", "/api/v1/pos/customers?q=Hidden", nil, w.headers...); strings.Contains(string(r.raw), w.hidden.String()) {
		t.Fatalf("the lookup carries the hidden customer: %s", r.raw)
	}
	r := f.do("POST", "/api/v1/pos/transactions", body(w.hidden), w.headers...)
	if r.status != http.StatusNotFound {
		t.Fatalf("create naming a customer outside the grants = %d, want 404: %s", r.status, r.raw)
	}
	if n := w.salesNaming(w.hidden); n != 0 {
		t.Errorf("the refused create wrote %d sales naming the hidden customer", n)
	}
	// the reachable customer is named at create
	if r := f.do("POST", "/api/v1/pos/transactions", body(w.reachable), w.headers...); r.status != http.StatusCreated {
		t.Errorf("create naming a customer inside the grants = %d, want 201: %s", r.status, r.raw)
	}
}

// RULE: the offline sync names a customer through the same wall: the item is
// an error, no sale and no invoice is written.
func TestTheSyncHoldsTheNamedCustomerToTheCustomerWall(t *testing.T) {
	w := newNamedWall(t)
	f := w.f
	sync := func(customer uuid.UUID) (string, string) {
		client := uuid.NewString()
		return client, fmt.Sprintf(`{"batch_id":"cw-%s","register_id":%q,"items":[{"client_id":%q,"cashier_id":%q,`+
			`"customer_id":%q,"items":[{"product_id":%q,"quantity":"1"}],`+
			`"tenders":[{"method":"account","amount_cents":599}],"client_created_at":%q}]}`,
			uuid.NewString()[:8], f.register, client, uuid.NewString(), customer.String(),
			f.productID.String(), time.Now().UTC().Format(time.RFC3339))
	}
	client, req := sync(w.hidden)
	r := f.do("POST", "/api/v1/pos/sync", req, w.headers...)
	if r.status != http.StatusOK || !strings.Contains(string(r.raw), `"synced_count":0`) || !strings.Contains(string(r.raw), `"error_count":1`) {
		t.Fatalf("sync naming a customer outside the grants = %d: %s, want the item an error", r.status, r.raw)
	}
	if n := countOf(t, f.db, `SELECT count(*) FROM pos_transactions WHERE id = $1`, client); n != 0 {
		t.Errorf("the refused sync item wrote its sale")
	}
	if n := countOf(t, f.db, `SELECT count(*) FROM invoices WHERE customer_id = $1`, w.hidden); n != 0 {
		t.Errorf("the refused sync item posted %d invoices to the hidden customer", n)
	}
	_, req = sync(w.reachable)
	if r := f.do("POST", "/api/v1/pos/sync", req, w.headers...); !strings.Contains(string(r.raw), `"synced_count":1`) {
		t.Errorf("sync naming a customer inside the grants = %d: %s, want the sale synced", r.status, r.raw)
	}
}

// RULE: a sale named before a grant change is held again at the completion:
// the customer leaves the cashier's reach, the charge answers 404 and the
// sale stays open with nothing posted.
func TestTheCompletionHoldsTheNamedCustomerAgain(t *testing.T) {
	w := newNamedWall(t)
	f := w.f
	r := f.do("POST", "/api/v1/pos/transactions", map[string]any{"register_id": f.register, "customer_id": w.reachable.String()}, w.headers...)
	if r.status != http.StatusCreated {
		t.Fatalf("create = %d: %s", r.status, r.raw)
	}
	saleID := str(t, r.body, "id")
	if r := f.do("POST", "/api/v1/pos/transactions/"+saleID+"/items", map[string]any{"line": f.productLine("1")}, w.headers...); r.status != http.StatusOK {
		t.Fatalf("add line = %d: %s", r.status, r.raw)
	}
	cur := f.getSale(t, saleID)
	total := num(t, cur, "total_cents")
	// the customer's only yard is now another branch
	mustExec(t, f.db, `UPDATE customer_branches SET branch_id = (SELECT branch_id FROM customer_branches WHERE customer_id = $1)
		WHERE customer_id = $2`, w.hidden, w.reachable)
	before := f.counterInvoices(t)
	r = f.do("POST", "/api/v1/pos/transactions/"+saleID+"/complete",
		map[string]any{"tenders": []map[string]any{tender("account", total)}, "revision": cur["revision"]}, w.headers...)
	if r.status != http.StatusNotFound {
		t.Errorf("complete naming a customer that left the grants = %d, want 404: %s", r.status, r.raw)
	}
	if got := f.counterInvoices(t); got != before {
		t.Errorf("the refused completion posted %d invoices", got-before)
	}
	if st := countOf(t, f.db, `SELECT count(*) FROM pos_transactions WHERE id = $1 AND status = 'OPEN'`, saleID); st != 1 {
		t.Errorf("the sale is no longer open after the refused completion")
	}
}

// RULE: a limit of zero means no account charges at all, whatever unapplied
// cash the account holds; the refusal is 409 credit_hold, before any card.
func TestAZeroLimitRefusesEveryAccountTenderEvenWithUnappliedCash(t *testing.T) {
	testutil.LockOutboxTables(t)
	f := newFixture(t, testutil.RequireDB(t))
	f.openTill(0)
	mustExec(t, f.db, `INSERT INTO payments (id, customer_id, branch_id, currency, amount, method, status, received_on, amount_unapplied)
		VALUES ($1, $2, $3, 'USD', 10, 'CHECK', 'POSTED', CURRENT_DATE, 10)`, uuid.New(), f.customerID, f.branchID)
	mustExec(t, f.db, `UPDATE customers SET credit_limit = 0 WHERE id = $1`, f.customerID)
	saleID := f.startSale(&f.customerID)
	if r := f.addLine(saleID, f.productLine("1")); r.status != http.StatusOK {
		t.Fatalf("add line = %d: %s", r.status, r.raw)
	}
	cur := f.getSale(t, saleID)
	before := f.counterInvoices(t)
	r := f.completeSale(saleID, tender("account", num(t, cur, "total_cents")))
	if r.status != http.StatusConflict || !strings.Contains(string(r.raw), "credit_hold") {
		t.Errorf("an account tender on a zero limit with unapplied cash = %d: %s, want 409 credit_hold", r.status, r.raw)
	}
	if got := f.counterInvoices(t); got != before {
		t.Errorf("the refused charge posted %d invoices", got-before)
	}
	// a cash tender on the same account is not held
	if r := f.completeSale(saleID, f.cashFor("1")); r.status != http.StatusOK {
		t.Errorf("cash on a held account = %d, want 200: %s", r.status, r.raw)
	}
}

// RULE: the lookup's query is text to find, never a pattern.
func TestTheLookupTakesItsQueryAsText(t *testing.T) {
	testutil.LockOutboxTables(t)
	f := newFixture(t, testutil.RequireDB(t))
	if r := f.findCustomers("%%"); strings.TrimSpace(string(r.raw)) != "[]" {
		t.Errorf("a query of %%%% answered %s, want []", r.raw)
	}
	if r := f.findCustomers("__"); strings.TrimSpace(string(r.raw)) != "[]" {
		t.Errorf("a query of two underscores answered %s, want []", r.raw)
	}
}

// RULE: a return names a customer through the same wall: a user granted only
// the sale's branch, no header, who returns to a customer only another branch
// trades with, by any refund method, is answered 404 and nothing is written
// (no return, no credit memo, no refund). The same holds when the customer
// comes from the original sale.
func TestTheReturnHoldsTheNamedCustomerToTheCustomerWall(t *testing.T) {
	w := newNamedWall(t)
	f := w.f
	if r := f.do("POST", "/api/v1/pos/till/open", map[string]any{"register_id": f.register, "opening_float_cents": 0}, w.headers...); r.status != http.StatusCreated {
		t.Fatalf("open till = %d: %s", r.status, r.raw)
	}
	r := f.do("POST", "/api/v1/pos/transactions", map[string]any{"register_id": f.register}, w.headers...)
	if r.status != http.StatusCreated {
		t.Fatalf("create = %d: %s", r.status, r.raw)
	}
	saleID := str(t, r.body, "id")
	if r := f.do("POST", "/api/v1/pos/transactions/"+saleID+"/items", map[string]any{"line": f.productLine("1")}, w.headers...); r.status != http.StatusOK {
		t.Fatalf("add line = %d: %s", r.status, r.raw)
	}
	cur := f.getSale(t, saleID)
	r = f.do("POST", "/api/v1/pos/transactions/"+saleID+"/complete",
		map[string]any{"tenders": []map[string]any{tender("cash", num(t, cur, "total_cents"))}, "revision": cur["revision"]}, w.headers...)
	if r.status != http.StatusOK {
		t.Fatalf("complete = %d: %s", r.status, r.raw)
	}
	lineID := r.body["lines"].([]any)[0].(map[string]any)["id"].(string)
	post := func(method string, withSale bool) resp {
		req := map[string]any{
			"register_id": f.register, "customer_id": w.hidden.String(),
			"refund_method": method, "reason": "wall", "lines": []map[string]any{
				{"line_id": lineID, "quantity": "1", "restock": false}},
		}
		if withSale {
			req["original_sale_id"] = saleID
		} else {
			req["lines"] = []map[string]any{{"product_id": f.productID.String(), "quantity": "1",
				"unit_price_ten_thousandths": 5990000, "restock": false}}
		}
		return f.do("POST", "/api/v1/pos/returns", req, w.headers...)
	}
	for _, c := range []struct {
		method string
		sale   bool
	}{{"account", true}, {"account", false}, {"cash", true}, {"cash", false}} {
		// a free return meets the wall (404); against a sale it is the
		// sale's own customer that is credited, so the named one is refused
		// on the field (400): either way nothing is written
		want := http.StatusNotFound
		if c.sale {
			want = http.StatusBadRequest
		}
		if r := post(c.method, c.sale); r.status != want {
			t.Errorf("%s return (sale %v) naming a hidden customer = %d, want %d: %s", c.method, c.sale, r.status, want, r.raw)
		}
	}
	if n := countOf(t, f.db, `SELECT count(*) FROM credit_memos WHERE customer_id = $1`, w.hidden); n != 0 {
		t.Errorf("the refused returns wrote %d credit memos to the hidden customer", n)
	}
	if got := f.counterReturns(t); got != 0 {
		t.Errorf("the refused returns wrote %d returns", got)
	}
	// a sale whose own customer left the grants holds the return the same way
	mustExec(t, f.db, `UPDATE pos_transactions SET customer_id = $1 WHERE id = $2`, w.hidden, saleID)
	req := map[string]any{"register_id": f.register, "original_sale_id": saleID, "refund_method": "account",
		"reason": "wall", "lines": []map[string]any{{"line_id": lineID, "quantity": "1", "restock": false}}}
	if r := f.do("POST", "/api/v1/pos/returns", req, w.headers...); r.status != http.StatusNotFound {
		t.Errorf("return of a sale naming a hidden customer = %d, want 404: %s", r.status, r.raw)
	}
	// the reachable customer still returns
	mustExec(t, f.db, `UPDATE pos_transactions SET customer_id = $1 WHERE id = $2`, w.reachable, saleID)
	req["customer_id"] = w.reachable.String()
	if r := f.do("POST", "/api/v1/pos/returns", req, w.headers...); r.status != http.StatusCreated {
		t.Errorf("return naming a reachable customer = %d, want 201: %s", r.status, r.raw)
	}
}
