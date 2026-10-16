// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package pos_test

// The customer a caller may name at the counter has one rule (round 21, with
// ruling A on this branch): the lookup, the attach, the create, the offline
// sync and the completion all name a customer through Service.namedCustomer.
// A user reaches every customer, whatever the user's yard or grants, and the
// invoice posts at the selling yard; a branch bound key keeps its pin and is
// answered like a missing customer, with nothing written.

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/gablelbm/gable/internal/platform/httpx"
	"github.com/gablelbm/gable/internal/pos"
	"github.com/gablelbm/gable/internal/testutil"
	"github.com/gablelbm/gable/pkg/branchctx"
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

// A key bound to the sale's branch, as the service sees it: the pin rides the
// context beside the branch.
func (w *namedWall) boundCtx() context.Context {
	branch := w.f.branchID
	ctx := branchctx.With(context.Background(), &branchctx.Context{BranchID: &branch, UserSub: "pos-cw-a"})
	return branchctx.WithKeyBranch(ctx, branch)
}

// RULE (ruling A, the one rule the lookup, the attach, the create, the sync
// and the completion share): a USER names every customer, whatever the user's
// yard or grants; the create is served and the sale names the customer.
func TestTheCreateServesAUserNamingAnotherYardsCustomer(t *testing.T) {
	w := newNamedWall(t)
	f := w.f
	body := func(c uuid.UUID) map[string]any {
		return map[string]any{"register_id": f.register, "customer_id": c.String()}
	}
	if r := f.do("GET", "/api/v1/pos/customers?q=Hidden", nil, w.headers...); !strings.Contains(string(r.raw), w.hidden.String()) {
		t.Fatalf("the lookup does not carry the other yard's customer: %s", r.raw)
	}
	if r := f.do("POST", "/api/v1/pos/transactions", body(w.hidden), w.headers...); r.status != http.StatusCreated {
		t.Fatalf("create naming another yard's customer = %d, want 201: %s", r.status, r.raw)
	}
	if n := w.salesNaming(w.hidden); n != 1 {
		t.Errorf("the served create wrote %d sales naming the customer, want 1", n)
	}
	if r := f.do("POST", "/api/v1/pos/transactions", body(w.reachable), w.headers...); r.status != http.StatusCreated {
		t.Errorf("create naming a customer inside the grants = %d, want 201: %s", r.status, r.raw)
	}
}

// RULE: a branch bound key keeps its pin at the create: another yard's
// customer is answered like a missing customer and nothing is written.
func TestTheCreateHoldsABoundKeyToItsPin(t *testing.T) {
	w := newNamedWall(t)
	f := w.f
	_, err := f.service.StartSale(w.boundCtx(), &pos.StartSale{RegisterID: f.register, CashierID: uuid.New(), CustomerID: &w.hidden}, "key")
	var he *httpx.Error
	if !errors.As(err, &he) || he.Status != http.StatusNotFound {
		t.Fatalf("bound key create naming another yard's customer: %v, want the module's 404", err)
	}
	if n := w.salesNaming(w.hidden); n != 0 {
		t.Errorf("the refused create wrote %d sales naming the customer", n)
	}
	if _, err := f.service.StartSale(w.boundCtx(), &pos.StartSale{RegisterID: f.register, CashierID: uuid.New(), CustomerID: &w.reachable}, "key"); err != nil {
		t.Errorf("bound key create naming its own yard's customer: %v", err)
	}
}

func (w *namedWall) syncBody(customer uuid.UUID) (string, string) {
	client := uuid.NewString()
	return client, fmt.Sprintf(`{"batch_id":"cw-%s","register_id":%q,"items":[{"client_id":%q,"cashier_id":%q,`+
		`"customer_id":%q,"items":[{"product_id":%q,"quantity":"1"}],`+
		`"tenders":[{"method":"account","amount_cents":599}],"client_created_at":%q}]}`,
		uuid.NewString()[:8], w.f.register, client, uuid.NewString(), customer.String(),
		w.f.productID.String(), time.Now().UTC().Format(time.RFC3339))
}

// RULE (money never queues): the offline sync refuses every batch that
// parses, a user naming another yard's customer included, and nothing of it
// is written: no sale, no invoice, no sync log row. The customer walls ahead
// of the handler keep their own refusals (the bound key's is the serve
// package's wire test).
func TestTheSyncRefusesEveryBatchByRule(t *testing.T) {
	w := newNamedWall(t)
	f := w.f
	client, req := w.syncBody(w.hidden)
	r := f.do("POST", "/api/v1/pos/sync", req, w.headers...)
	if r.status != http.StatusConflict {
		t.Fatalf("sync naming another yard's customer = %d: %s, want the rule's 409", r.status, r.raw)
	}
	if code, _, _ := errorOf(t, r); code != "money_never_queues" {
		t.Errorf("code = %q, want money_never_queues (the body: %s)", code, r.raw)
	}
	if n := countOf(t, f.db, `SELECT count(*) FROM pos_transactions WHERE id = $1`, client); n != 0 {
		t.Errorf("the refused sync wrote its sale")
	}
	if n := countOf(t, f.db, `SELECT count(*) FROM invoices WHERE customer_id = $1 AND branch_id = $2`, w.hidden, f.branchID); n != 0 {
		t.Errorf("the refused sync posted %d invoices at the selling yard, want 0", n)
	}
	if n := countOf(t, f.db, `SELECT count(*) FROM pos_sync_log`); n != 0 {
		t.Errorf("the refused sync wrote %d sync log rows, want 0", n)
	}
}

// RULE: a branch bound key keeps its pin in the sync: the item is an error,
// no sale and no invoice is written.
func TestTheSyncHoldsABoundKeyToItsPin(t *testing.T) {
	w := newNamedWall(t)
	f := w.f
	client := uuid.New()
	item := &pos.OfflineSale{ClientID: client, CashierID: uuid.New(), CustomerID: &w.hidden}
	_, err := f.service.StartSaleAt(w.boundCtx(), item, f.register, "key")
	var he *httpx.Error
	if !errors.As(err, &he) || he.Status != http.StatusNotFound {
		t.Fatalf("bound key sync naming another yard's customer: %v, want the module's 404", err)
	}
	if n := countOf(t, f.db, `SELECT count(*) FROM pos_transactions WHERE id = $1`, client); n != 0 {
		t.Errorf("the refused sync item wrote its sale")
	}
	if n := countOf(t, f.db, `SELECT count(*) FROM invoices WHERE customer_id = $1`, w.hidden); n != 0 {
		t.Errorf("the refused sync item posted %d invoices to the customer", n)
	}
}

// the sale is named while its customer is reachable, then the customer's only
// yard becomes another branch before the completion
func (w *namedWall) namedThenMoved(t *testing.T) (saleID string, total int64, revision any) {
	t.Helper()
	f := w.f
	r := f.do("POST", "/api/v1/pos/transactions", map[string]any{"register_id": f.register, "customer_id": w.reachable.String()}, w.headers...)
	if r.status != http.StatusCreated {
		t.Fatalf("create = %d: %s", r.status, r.raw)
	}
	saleID = str(t, r.body, "id")
	if r := f.do("POST", "/api/v1/pos/transactions/"+saleID+"/items", map[string]any{"line": f.productLine("1")}, w.headers...); r.status != http.StatusOK {
		t.Fatalf("add line = %d: %s", r.status, r.raw)
	}
	cur := f.getSale(t, saleID)
	mustExec(t, f.db, `UPDATE customer_branches SET branch_id = (SELECT branch_id FROM customer_branches WHERE customer_id = $1)
		WHERE customer_id = $2`, w.hidden, w.reachable)
	return saleID, num(t, cur, "total_cents"), cur["revision"]
}

// RULE (ruling A): the completion serves a user whose sale names a customer
// that no yard of the user reaches: one invoice, at the selling yard.
func TestTheCompletionServesAUserNamingAnotherYardsCustomer(t *testing.T) {
	w := newNamedWall(t)
	f := w.f
	saleID, total, rev := w.namedThenMoved(t)
	before := f.counterInvoices(t)
	r := f.do("POST", "/api/v1/pos/transactions/"+saleID+"/complete",
		map[string]any{"tenders": []map[string]any{tender("account", total)}, "revision": rev}, w.headers...)
	if r.status != http.StatusOK {
		t.Fatalf("complete naming another yard's customer = %d, want 200: %s", r.status, r.raw)
	}
	if got := f.counterInvoices(t) - before; got != 1 {
		t.Errorf("the completion posted %d invoices, want 1", got)
	}
	if n := countOf(t, f.db, `SELECT count(*) FROM invoices WHERE customer_id = $1 AND branch_id = $2`, w.reachable, f.branchID); n != 1 {
		t.Errorf("the invoice is not at the selling yard (%d found)", n)
	}
}

// RULE: a branch bound key keeps its pin at the completion: 404, the sale
// stays open, nothing is posted.
func TestTheCompletionHoldsABoundKeyToItsPin(t *testing.T) {
	w := newNamedWall(t)
	f := w.f
	saleID, total, _ := w.namedThenMoved(t)
	before := f.counterInvoices(t)
	id := uuid.MustParse(saleID)
	_, err := f.service.CompleteSale(w.boundCtx(), id, "", nil,
		[]pos.TenderIn{{Method: pos.TenderAccount, AmountCents: httpx.Cents(total)}}, "", "key")
	var he *httpx.Error
	if !errors.As(err, &he) || he.Status != http.StatusNotFound {
		t.Fatalf("bound key completion naming another yard's customer: %v, want the module's 404", err)
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

// RULE (ruling A): a user granted only the sale's branch, no header, may
// return to a customer only another branch trades with. A free return is
// served and credits the named customer; against a sale it is the sale's own
// customer that is credited, so a different named one is refused on the field
// (400) and nothing is written; a sale whose customer trades only at another
// yard credits that customer once. A bound key's refusal is walled in the
// serve package's wire test.
func TestAUserReturnNamesAnotherYardsCustomer(t *testing.T) {
	w := newNamedWall(t)
	f := w.f
	// hand what the returns credited to the hidden customer back to the
	// fixture's, whose cleanup knows how to remove it
	t.Cleanup(func() {
		if _, err := f.db.Pool.Exec(context.Background(), `UPDATE credit_memos SET customer_id = $1 WHERE customer_id = $2`, w.reachable, w.hidden); err != nil {
			t.Errorf("cleanup: %v", err)
		}
		if _, err := f.db.Pool.Exec(context.Background(), `UPDATE pos_transactions SET customer_id = $1 WHERE customer_id = $2`, w.reachable, w.hidden); err != nil {
			t.Errorf("cleanup: %v", err)
		}
	})
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
		want   int
	}{{"account", true, http.StatusBadRequest}, {"cash", true, http.StatusBadRequest},
		{"account", false, http.StatusCreated}, {"cash", false, http.StatusCreated}} {
		if r := post(c.method, c.sale); r.status != c.want {
			t.Errorf("%s return (sale %v) naming another yard's customer = %d, want %d: %s", c.method, c.sale, r.status, c.want, r.raw)
		}
	}
	if n := countOf(t, f.db, `SELECT count(*) FROM credit_memos WHERE customer_id = $1`, w.hidden); n != 2 {
		t.Errorf("the free returns wrote %d credit memos to the named customer, want 2", n)
	}
	if got := f.counterReturns(t); got != 2 {
		t.Errorf("the free returns wrote %d returns, want 2", got)
	}
	// a sale whose own customer trades only at another yard: served, one
	// credit to that customer
	mustExec(t, f.db, `UPDATE pos_transactions SET customer_id = $1 WHERE id = $2`, w.hidden, saleID)
	req := map[string]any{"register_id": f.register, "original_sale_id": saleID, "refund_method": "account",
		"reason": "wall", "lines": []map[string]any{{"line_id": lineID, "quantity": "1", "restock": false}}}
	if r := f.do("POST", "/api/v1/pos/returns", req, w.headers...); r.status != http.StatusCreated {
		t.Errorf("return of a sale naming another yard's customer = %d, want 201: %s", r.status, r.raw)
	}
	if n := countOf(t, f.db, `SELECT count(*) FROM credit_memos WHERE customer_id = $1`, w.hidden); n != 3 {
		t.Errorf("the sale's return left %d credit memos on its customer, want 3 in all", n)
	}
}
