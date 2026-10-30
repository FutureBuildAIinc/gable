// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package order_test

// DEBT-202: the debt read's P2 findings on the CP-07 deposits, each closed
// through the payments route.

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/gablelbm/gable/internal/testutil"
	"github.com/google/uuid"
)

// RULE (debt read P2-1): the balance a deposit may take is the order's total
// less what its live invoices were already paid by anything else (a payment
// on account, a memo), and the order answer shows that same figure.
func TestDebt202_DepositCapSubtractsWhatTheOrdersInvoicesWerePaid(t *testing.T) {
	testutil.LockOutboxTables(t)
	db := testutil.RequireDB(t)

	f := newFixture(t, db)
	f.serveOrdersAndPayments()
	f.cleanMoney()
	f.cleanDeposits()
	f.stock(f.productID, "10")
	f.setCost(f.productID, "3.2500")

	r := f.create() // total 5988
	id := str(t, r.body, "id")
	f.transition(id, 1, "confirmed")

	lineID := str(t, f.bodyOfLine(t, id), "id")
	r = f.do("GET", "/api/v1/orders/"+id, nil)
	r = f.fulfil(id, revision(t, r), map[string]any{"picked_up_by": "Counter customer",
		"lines": []map[string]any{{"order_line_id": lineID, "quantity": "5"}}})
	if r.status != http.StatusCreated {
		t.Fatalf("the partial fulfilment = %d: %s", r.status, r.raw)
	}
	inv := invoiceIDOf(t, r) // 2994

	// The invoice is paid on account: a payment that names no order.
	r = f.doUnique("POST", "/api/v1/payments", map[string]any{
		"customer_id": f.customerID.String(), "amount_cents": 2994, "method": "cash",
		"applications": []map[string]any{{"invoice_id": inv, "amount_cents": 2994}},
	}, "req-debt202-pay-on-account")
	if r.status != http.StatusCreated {
		t.Fatalf("the payment on account = %d: %s", r.status, r.raw)
	}

	r = f.do("GET", "/api/v1/orders/"+id, nil)
	if got := num(t, r.body, "balance_due_after_deposits_cents"); got != 2994 {
		t.Errorf("balance_due_after_deposits_cents = %d, want 2994 (the order less the 2994 its invoice was paid)", got)
	}
	r = f.takeDeposit(id, 5988, "cash")
	if r.status != http.StatusConflict {
		t.Errorf("a deposit of the whole total after half was paid = %d: %s, want 409", r.status, r.raw)
	}
	r = f.takeDeposit(id, 2994, "cash")
	if r.status != http.StatusCreated {
		t.Fatalf("a deposit of the true balance = %d: %s, want 201", r.status, r.raw)
	}
	r = f.do("GET", "/api/v1/orders/"+id, nil)
	if got := num(t, r.body, "balance_due_after_deposits_cents"); got != 0 {
		t.Errorf("balance_due_after_deposits_cents after the deposit = %d, want 0", got)
	}
}

// openTill opens a till session on a register of the default branch and
// removes it, with its ledger rows, when the test ends.
func (f *fixture) openTill(reqID string) (tillID, branchID string) {
	f.t.Helper()
	ctx := context.Background()
	regID := "REG-D202-" + uuid.NewString()[:8]
	if err := f.db.Pool.QueryRow(ctx, `SELECT value FROM system_settings WHERE key = 'default_branch_id'`).Scan(&branchID); err != nil {
		f.t.Fatal(err)
	}
	if _, err := f.db.Pool.Exec(ctx, `INSERT INTO pos_registers (id, name, location_id, branch_id) VALUES ($1, $2, $3::uuid, $3::uuid)`, regID, "Counter "+regID, branchID); err != nil {
		f.t.Fatal(err)
	}
	f.t.Cleanup(func() {
		_, _ = f.db.Pool.Exec(ctx, `DELETE FROM gl_journal_lines WHERE journal_entry_id IN (SELECT id FROM gl_journal_entries WHERE source_ref_id IN (SELECT id FROM till_sessions WHERE register_id = $1))`, regID)
		_, _ = f.db.Pool.Exec(ctx, `DELETE FROM gl_journal_entries WHERE source_ref_id IN (SELECT id FROM till_sessions WHERE register_id = $1)`, regID)
		_, _ = f.db.Pool.Exec(ctx, `DELETE FROM events_outbox WHERE entity_type = 'till_session' AND entity_id IN (SELECT id FROM till_sessions WHERE register_id = $1)`, regID)
		_, _ = f.db.Pool.Exec(ctx, `DELETE FROM till_z_reports WHERE register_id = $1`, regID)
		_, _ = f.db.Pool.Exec(ctx, `DELETE FROM till_sessions WHERE register_id = $1`, regID)
		_, _ = f.db.Pool.Exec(ctx, `DELETE FROM pos_registers WHERE id = $1`, regID)
	})
	r := f.doUnique("POST", "/api/v1/pos/till/open", map[string]any{"register_id": regID, "opening_float_cents": 20000}, reqID)
	if r.status != http.StatusCreated {
		f.t.Fatalf("open till = %d: %s", r.status, r.raw)
	}
	return str(f.t, r.body, "id"), branchID
}

func (f *fixture) paymentCount(orderID string) int {
	f.t.Helper()
	var n int
	if err := f.db.Pool.QueryRow(context.Background(), `SELECT count(*) FROM payments WHERE order_id = $1`, orderID).Scan(&n); err != nil {
		f.t.Fatal(err)
	}
	return n
}

func (f *fixture) tillCheck(tillID, orderID, reqID string, cents int64, idChecked bool) resp {
	f.t.Helper()
	body := map[string]any{
		"customer_id": f.customerID.String(), "order_id": orderID, "till_session_id": tillID,
		"amount_cents": cents, "method": "check", "reference": "CHK-D202",
	}
	if idChecked {
		body["id_checked"] = true
	}
	return f.doUnique("POST", "/api/v1/payments", body, reqID)
}

// RULE (debt read P2-2, MONEY-CHECKLIST 7 and 14): a check taken as a counter
// deposit meets the customer's check limit and the dealer's ID rule, as a
// sale's check tender does, and a refusal writes nothing.
func TestDebt202_TillCheckDepositMeetsTheCheckLimitAndIDRule(t *testing.T) {
	testutil.LockOutboxTables(t)
	db := testutil.RequireDB(t)

	f := newFixture(t, db)
	f.serveOrdersAndPayments()
	f.cleanMoney()
	f.cleanDeposits()
	f.stock(f.productID, "1000")
	tillID, _ := f.openTill("req-d202-ck-open")
	if _, err := db.Pool.Exec(context.Background(), `UPDATE customers SET check_limit = 500.00 WHERE id = $1`, f.customerID); err != nil {
		t.Fatal(err)
	}
	setSetting(t, db, "pos.check_id_required_over_cents", "20000")
	t.Cleanup(func() {
		_, _ = db.Pool.Exec(context.Background(), `DELETE FROM system_settings WHERE key = 'pos.check_id_required_over_cents'`)
	})

	r := f.create(f.line("1000"))
	orderID := str(t, r.body, "id")
	f.transition(orderID, 1, "confirmed")

	// over the customer's limit of 500.00: refused, nothing written
	r = f.tillCheck(tillID, orderID, "req-d202-ck-over", 60000, true)
	if r.status != http.StatusConflict || !strings.Contains(string(r.raw), "check_over_limit") {
		t.Errorf("a 600.00 check over a 500.00 limit = %d: %s, want 409 check_over_limit", r.status, r.raw)
	}
	// over the ID figure with no ID checked: refused
	r = f.tillCheck(tillID, orderID, "req-d202-ck-noid", 30000, false)
	if r.status != http.StatusConflict || !strings.Contains(string(r.raw), "id_required") {
		t.Errorf("a 300.00 check with no ID over a 200.00 rule = %d: %s, want 409 id_required", r.status, r.raw)
	}
	if n := f.paymentCount(orderID); n != 0 {
		t.Fatalf("%d payments written by the refused checks, want 0", n)
	}
	// with the ID checked, and under the figure with none, both pass
	r = f.tillCheck(tillID, orderID, "req-d202-ck-id", 30000, true)
	if r.status != http.StatusCreated {
		t.Errorf("a 300.00 check with the ID checked = %d: %s, want 201", r.status, r.raw)
	}
	r = f.tillCheck(tillID, orderID, "req-d202-ck-small", 15000, false)
	if r.status != http.StatusCreated {
		t.Errorf("a 150.00 check under the ID figure = %d: %s, want 201", r.status, r.raw)
	}
	// cash is never held to either rule
	r = f.doUnique("POST", "/api/v1/payments", map[string]any{
		"customer_id": f.customerID.String(), "order_id": orderID, "till_session_id": tillID,
		"amount_cents": 60000, "method": "cash"}, "req-d202-ck-cash")
	if r.status != http.StatusCreated {
		t.Errorf("a 600.00 cash deposit = %d: %s, want 201", r.status, r.raw)
	}
}

// RULE (debt read P2-3, MONEY-CHECKLIST 15): a payment naming an order of
// another branch is refused on every path (cash, card, till), whatever
// branch the payment itself names, and nothing is written or charged. A caller
// granted one branch cannot even read another branch's order.
func TestDebt202_DepositOnAnotherBranchesOrderIsRefusedOnEveryPath(t *testing.T) {
	testutil.LockOutboxTables(t)
	db := testutil.RequireDB(t)
	setSetting(t, db, "multi_branch_enabled", "true")
	setSetting(t, db, "default_branch_required", "false")
	branchB := uuid.New()
	subB := "d202-" + uuid.NewString()[:8]
	if _, err := db.Pool.Exec(context.Background(), `INSERT INTO locations (id, type, code, name) VALUES ($1, 'BRANCH', $2, $2)`, branchB, "D202-B-"+uuid.NewString()[:6]); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Pool.Exec(context.Background(), `INSERT INTO user_locations (user_sub, branch_id, is_home, granted_by) VALUES ($1, $2, TRUE, 'test')`, subB, branchB); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		setSetting(t, db, "multi_branch_enabled", "false")
		setSetting(t, db, "default_branch_required", "true")
		_, _ = db.Pool.Exec(context.Background(), `DELETE FROM user_locations WHERE user_sub = $1`, subB)
		_, _ = db.Pool.Exec(context.Background(), `DELETE FROM locations WHERE id = $1`, branchB)
	})

	f := newFixture(t, db)
	gw := f.serveOrdersAndPayments()
	f.cleanMoney()
	f.cleanDeposits()
	f.stock(f.productID, "10")
	r := f.create() // an order of the default branch
	orderID := str(t, r.body, "id")
	f.transition(orderID, 1, "confirmed")

	// a caller granted branch B only, naming the default branch's order
	scoped := func(reqID string) []string {
		return []string{"X-Request-ID", reqID, "X-Test-Role", "finance", "X-Test-Sub", subB, "X-Branch-Id", branchB.String()}
	}
	r = f.do("POST", "/api/v1/payments", map[string]any{
		"customer_id": f.customerID.String(), "order_id": orderID, "branch_id": branchB.String(),
		"amount_cents": 1000, "method": "cash"}, scoped("req-d202-xb-cash")...)
	if r.status == http.StatusCreated || r.status < 400 {
		t.Errorf("a cash deposit on another branch's order by a caller of branch B = %d: %s, want a refusal", r.status, r.raw)
	}
	r = f.do("POST", "/api/v1/payments/card", map[string]any{
		"customer_id": f.customerID.String(), "order_id": orderID, "branch_id": branchB.String(),
		"amount_cents": 1000, "token_id": "tok_d202"}, scoped("req-d202-xb-card")...)
	if r.status == http.StatusCreated || r.status < 400 {
		t.Errorf("a card deposit on another branch's order by a caller of branch B = %d: %s, want a refusal", r.status, r.raw)
	}
	if n := gw.charges.Load(); n != 0 {
		t.Errorf("%d card charges made for the refused deposit, want 0", n)
	}

	// a caller of every branch naming a branch other than the order's:
	// the branches differ, so every path refuses it
	for _, path := range []string{"cash", "card"} {
		body := map[string]any{"customer_id": f.customerID.String(), "order_id": orderID,
			"branch_id": branchB.String(), "amount_cents": 1000}
		url := "/api/v1/payments"
		if path == "card" {
			url += "/card"
			body["token_id"] = "tok_d202"
		} else {
			body["method"] = "cash"
		}
		r = f.doUnique("POST", url, body, "req-d202-xb-all-"+path)
		if r.status != http.StatusConflict {
			t.Errorf("a %s deposit naming branch B on a default branch order = %d: %s, want 409", path, r.status, r.raw)
		}
	}
	if n := gw.charges.Load(); n != 0 {
		t.Errorf("%d card charges made for the refused deposits, want 0", n)
	}
	if n := f.paymentCount(orderID); n != 0 {
		t.Errorf("%d payments written on the order by the refused deposits, want 0", n)
	}
	// control: the same deposit in the order's own branch posts
	r = f.takeDeposit(orderID, 1000, "cash")
	if r.status != http.StatusCreated {
		t.Errorf("the in branch deposit = %d: %s, want 201", r.status, r.raw)
	}
}
