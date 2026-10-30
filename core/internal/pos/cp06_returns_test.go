// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package pos_test

// CP-06 (audit items 3 to 5): a past sale is found and returned from the
// counter's sale search, a damaged line is returned without a restock while
// the sale's other line goes back to stock, and the refund claims waiting on
// a manager are listed with their pay back and resolve acts.

import (
	"context"
	"net/http"
	"net/url"
	"testing"
	"time"

	"github.com/gablelbm/gable/internal/payment"
	"github.com/gablelbm/gable/internal/testutil"
	"github.com/google/uuid"
)

// listSales queries the counter's sale list with the given query string and
// answers its items.
func (f *fixture) listSales(t *testing.T, query string) []map[string]any {
	t.Helper()
	r := f.do("GET", "/api/v1/pos/transactions?"+query, nil, "X-Test-Role", "cashier", "X-Test-Sub", mustUUID(t))
	if r.status != http.StatusOK {
		t.Fatalf("list sales ?%s = %d: %s", query, r.status, r.raw)
	}
	items, ok := r.body["items"].([]any)
	if !ok {
		t.Fatalf("items is %T (%v), want an array", r.body["items"], r.body["items"])
	}
	out := make([]map[string]any, 0, len(items))
	for _, it := range items {
		out = append(out, it.(map[string]any))
	}
	return out
}

// findSale answers the list item naming the sale, or fails.
func findSale(t *testing.T, items []map[string]any, saleID string) map[string]any {
	t.Helper()
	for _, it := range items {
		if it["id"] == saleID {
			return it
		}
	}
	t.Fatalf("sale %s not in the page: %v", saleID, items)
	return nil
}

// RULE (CP-06 item 3): a past sale is found by its number (the receipt's
// barcode text), its invoice number, or its customer, at the caller's
// branch, bounded by the date when one is named; a voided sale shows as
// voided; and the sale so found returns through the return route, posting
// the credit memo and the cash refund, with the sale's returned quantities
// readable after.
func TestCP06_ReturnPastSaleFromSearch(t *testing.T) {
	testutil.LockOutboxTables(t)
	f := newFixture(t, testutil.RequireDB(t))
	saleID := f.startSale(&f.customerID)
	if r := f.addLine(saleID, f.productLine("2")); r.status != http.StatusOK {
		t.Fatalf("add line = %d: %s", r.status, r.raw)
	}
	body := f.completeSale(saleID, f.cashFor(saleID))
	if body.status != http.StatusOK {
		t.Fatalf("complete = %d: %s", body.status, body.raw)
	}
	number := str(t, body.body, "number")
	lineID := body.body["lines"].([]any)[0].(map[string]any)["id"].(string)
	invoiceNumber := f.scalar(`SELECT i.number FROM invoices i
		JOIN pos_transactions pt ON pt.invoice_id = i.id WHERE pt.id = $1`, saleID).(string)

	// by the sale's own number (what the receipt's barcode scans to)
	item := findSale(t, f.listSales(t, "q="+number), saleID)
	if got := str(t, item, "number"); got != number {
		t.Errorf("number = %q, want %q", got, number)
	}
	if got := str(t, item, "customer_name"); got != f.customerName {
		t.Errorf("customer_name = %q, want %q", got, f.customerName)
	}
	if got := str(t, item, "invoice_number"); got != invoiceNumber {
		t.Errorf("invoice_number = %q, want %q", got, invoiceNumber)
	}
	if got := str(t, item, "status"); got != "completed" {
		t.Errorf("status = %q, want completed", got)
	}
	// by the invoice number and by the customer's name, the whole unique
	// name so only this run's sale can answer
	findSale(t, f.listSales(t, "q="+invoiceNumber), saleID)
	findSale(t, f.listSales(t, "q="+url.QueryEscape(f.customerName)), saleID)
	// the date still bounds a search that names it
	old := time.Now().AddDate(0, 0, -30).Format("2006-01-02")
	if items := f.listSales(t, "q="+number+"&date="+old); len(items) != 0 {
		t.Errorf("search on an old day answered %d items, want none", len(items))
	}
	// a search nothing matches is an empty page
	if items := f.listSales(t, "q=zz-no-such-sale"); len(items) != 0 {
		t.Errorf("search with no match answered %d items, want none", len(items))
	}

	// a voided sale is found and reads as voided, not returnable
	voidedID, voidedBody := f.saleOf("1", tender("cash", 599))
	voidedNumber := str(t, voidedBody.body, "number")
	if r := f.do("POST", "/api/v1/pos/transactions/"+voidedID+"/void",
		map[string]any{"reason": "rung twice", "revision": rev(t, voidedBody)},
		"X-Test-Role", "cashier", "X-Test-Sub", mustUUID(t)); r.status != http.StatusOK {
		t.Fatalf("void = %d: %s", r.status, r.raw)
	}
	if got := str(t, findSale(t, f.listSales(t, "q="+voidedNumber), voidedID), "status"); got != "voided" {
		t.Errorf("voided sale's status = %q, want voided", got)
	}

	// the sale found returns: the credit memo posts and the cash refund pays
	memos, refunds := f.counterMemos(t), f.counterRefunds(t)
	r := f.do("POST", "/api/v1/pos/returns", map[string]any{
		"register_id": f.register, "original_sale_id": saleID,
		"refund_method": "cash", "reason": "found through the counter's sale search",
		"lines": []map[string]any{{"line_id": lineID, "quantity": "1", "restock": true}},
	}, "X-Test-Role", "cashier", "X-Test-Sub", mustUUID(t))
	if r.status != http.StatusCreated {
		t.Fatalf("return on the found sale = %d: %s", r.status, r.raw)
	}
	if str(t, r.body, "credit_memo_id") == "" {
		t.Errorf("the return names no credit memo: %s", r.raw)
	}
	if got := f.counterMemos(t); got != memos+1 {
		t.Errorf("credit memos = %d, want %d (the return's memo)", got, memos+1)
	}
	if got := f.counterRefunds(t); got != refunds+1 {
		t.Errorf("refunds = %d, want %d (the cash refund)", got, refunds+1)
	}
	// the sale reads back what of each line came back
	sale := f.getSale(t, saleID)
	returned, ok := sale["returned_quantities"].(map[string]any)
	if !ok {
		t.Fatalf("returned_quantities is %T (%v), want a map", sale["returned_quantities"], sale["returned_quantities"])
	}
	if got := returned[lineID]; got != "1" {
		t.Errorf("returned_quantities[%s] = %v, want \"1\"", lineID, got)
	}
}

// RULE (CP-06 item 4): the return carries restock per line as chosen: the
// damaged line's goods never re-enter stock while the sale's other line is
// restocked, the kit's components counted one by one.
func TestCP06_DamagedLineNotRestocked(t *testing.T) {
	testutil.LockOutboxTables(t)
	f := newFixture(t, testutil.RequireDB(t))
	saleID := f.startSale(nil)
	if r := f.addLine(saleID, f.productLine("2")); r.status != http.StatusOK {
		t.Fatalf("add product line = %d: %s", r.status, r.raw)
	}
	if r := f.addLine(saleID, map[string]any{"product_id": f.kitID.String(), "quantity": "1"}); r.status != http.StatusOK {
		t.Fatalf("add kit line = %d: %s", r.status, r.raw)
	}
	done := f.completeSale(saleID, f.cashFor(saleID))
	if done.status != http.StatusOK {
		t.Fatalf("complete = %d: %s", done.status, done.raw)
	}
	lines := done.body["lines"].([]any)
	productLineID := lines[0].(map[string]any)["id"].(string)
	kitLineID := lines[1].(map[string]any)["id"].(string)
	// the sale drew 2 of the product and the kit's 2 components: 96 on hand
	if got := f.stock(); got != "96.0000/0.0000" {
		t.Fatalf("stock after the sale = %s, want 96.0000/0.0000", got)
	}

	r := f.do("POST", "/api/v1/pos/returns", map[string]any{
		"register_id": f.register, "original_sale_id": saleID,
		"refund_method": "cash", "reason": "one line damaged, one back to stock",
		"lines": []map[string]any{
			{"line_id": productLineID, "quantity": "2", "restock": false},
			{"line_id": kitLineID, "quantity": "1", "restock": true},
		},
	}, "X-Test-Role", "cashier", "X-Test-Sub", mustUUID(t))
	if r.status != http.StatusCreated {
		t.Fatalf("return = %d: %s", r.status, r.raw)
	}
	// only the kit's 2 components came back: the damaged line never re-entered
	if got := f.stock(); got != "98.0000/0.0000" {
		t.Errorf("stock after the return = %s, want 98.0000/0.0000 (the damaged line not restocked)", got)
	}
	// both lines refund in full, restock choice or not
	if got, want := num(t, r.body, "total_cents"), -num(t, done.body, "total_cents"); got != want {
		t.Errorf("return total_cents = %d, want %d (both lines refunded)", got, want)
	}
}

// RULE (CP-06 item 3, money checklist line 11): a dated search is bounded by
// the sale's branch business day in the branch's own zone, never the server's
// zone and never UTC. The branch here sits 25 hours behind UTC, so a sale
// made in the UTC evening is still yesterday's sale at the branch: the
// branch's date finds it and the UTC date does not, and the list item names
// the branch's business day rather than the UTC slice of the timestamp.
func TestCP06_SearchDateBoundByBranchZone(t *testing.T) {
	testutil.LockOutboxTables(t)
	db := testutil.RequireDB(t)
	// takes the branch day exclusively for the test: no other test reads the
	// day while the zone is not the stored one
	f := newFixture(t, db)
	// the zone goes on the branch the fixture owns: the sale is read by its
	// own branch's day, and the shared default branch's row is not this
	// test's to write
	testutil.SetBranchDayZoneOn(t, db, f.branchID, testutil.AlwaysBehindUTC)
	saleID := f.startSale(&f.customerID)
	if r := f.addLine(saleID, f.productLine("1")); r.status != http.StatusOK {
		t.Fatalf("add line = %d: %s", r.status, r.raw)
	}
	body := f.completeSale(saleID, f.cashFor(saleID))
	if body.status != http.StatusOK {
		t.Fatalf("complete = %d: %s", body.status, body.raw)
	}
	number := str(t, body.body, "number")
	// the sale is dated 20:00 UTC, which the branch's zone reads as 19:00
	// the day before: the branch's business day is 2026-10-27
	mustExec(t, db, `UPDATE pos_transactions SET created_at = '2026-10-28 20:00:00+00' WHERE id = $1`, saleID)

	if items := f.listSales(t, "q="+number+"&date=2026-10-27"); len(items) != 1 {
		t.Fatalf("search on the branch's day = %d items, want the sale", len(items))
	}
	if items := f.listSales(t, "q="+number+"&date=2026-10-28"); len(items) != 0 {
		t.Fatalf("search on the UTC day = %d items, want none", len(items))
	}
	item := findSale(t, f.listSales(t, "q="+number), saleID)
	if got := str(t, item, "business_day"); got != "2026-10-27" {
		t.Errorf("business_day = %q, want 2026-10-27 (the branch's own day, not the UTC slice)", got)
	}
}

// RULE (CP-06 item 3, money checklist line 11): the plain list's own day,
// when the caller names no date and no search, is the branch's business day
// computed in the branch's own zone, never the server's clock. The branch
// here sits 25 hours behind the server, so a sale made now is on the branch's
// day while the server's date has already moved on: the plain list still
// finds it.
func TestCP06_PlainListUsesTheBranchDay(t *testing.T) {
	testutil.LockOutboxTables(t)
	db := testutil.RequireDB(t)
	f := newFixture(t, db)
	// the zone goes on the branch the fixture owns: the sale is read by its
	// own branch's day, and the shared default branch's row is not this
	// test's to write
	testutil.SetBranchDayZoneOn(t, db, f.branchID, testutil.AlwaysBehindUTC)
	saleID := f.startSale(&f.customerID)
	if r := f.addLine(saleID, f.productLine("1")); r.status != http.StatusOK {
		t.Fatalf("add line = %d: %s", r.status, r.raw)
	}
	body := f.completeSale(saleID, f.cashFor(saleID))
	if body.status != http.StatusOK {
		t.Fatalf("complete = %d: %s", body.status, body.raw)
	}
	if got := str(t, f.getSale(t, saleID), "status"); got != "completed" {
		t.Fatalf("sale status = %q, want completed", got)
	}

	if items := f.listSales(t, "register_id="+f.register); len(items) != 1 {
		t.Fatalf("the plain list = %d items, want the branch's own sale of today", len(items))
	}
}

// RULE (CP-06 item 5, money checklist line 15): the claims list carries the
// branch wall on the claim's sale, so a finance caller scoped to one branch
// never sees another branch's open claim, and the caller scoped to that
// branch does. Red the moment the wall is dropped.
func TestCP06_RefundIntentListHoldsTheBranchWall(t *testing.T) {
	testutil.LockOutboxTables(t)
	db := testutil.RequireDB(t)
	setPosSetting(t, db, "multi_branch_enabled", "true")
	setPosSetting(t, db, "default_branch_required", "false")
	branchB := uuid.New()
	exec := func(q string, args ...any) {
		t.Helper()
		if _, err := db.Pool.Exec(context.Background(), q, args...); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	exec(`INSERT INTO locations (id, type, code, name) VALUES ($1, 'BRANCH', 'WL-CP06-Q', 'claims list wall B')`, branchB)
	exec(`INSERT INTO user_locations (user_sub, branch_id, is_home, granted_by) VALUES ('cp06-list-b', $1, TRUE, 'test')`, branchB)
	t.Cleanup(func() {
		setPosSetting(t, db, "multi_branch_enabled", "false")
		setPosSetting(t, db, "default_branch_required", "true")
		exec(`DELETE FROM user_locations WHERE user_sub IN ('cp06-list-b', 'cp06-list-a')`)
		exec(`DELETE FROM locations WHERE id = $1`, branchB)
	})

	// branch A's open claim and branch B's open claim, each a void's declined
	// card credit, the second moved to branch B after its flow
	gw := &fakeGateway{charges: []*payment.GatewayResult{approvedCharge(), approvedCharge(), approvedCharge(), approvedCharge()},
		refundStatuses: []payment.GatewayStatus{payment.GatewayStatusRefunded, payment.GatewayStatusDeclined,
			payment.GatewayStatusRefunded, payment.GatewayStatusDeclined}}
	f := newFixture(t, db, func(f *fixture) {
		mustExec(t, db, `INSERT INTO user_locations (user_sub, branch_id, is_home, granted_by) VALUES ('cp06-list-a', $1, TRUE, 'test')`, f.branchID)
		// every fixture request acts as the A granted person, as the resolve
		// wall test wires it: with multi branch on, a no header random sub
		// has no branch to act in
		f.cashierSub = "cp06-list-a"
		f.service = f.service.WithGateway(gw)
	})
	_, claimA, _ := declinedCreditVoid(t, f, gw, mustUUID(t))
	saleB, claimB, _ := declinedCreditVoid(t, f, gw, mustUUID(t))
	mustExec(t, db, `UPDATE pos_transactions SET branch_id = $2 WHERE id = $1`, saleB, branchB)

	list := func(headers ...string) []map[string]any {
		t.Helper()
		r := f.do("GET", "/api/v1/pos/refund-intents", nil, headers...)
		if r.status != http.StatusOK {
			t.Fatalf("the list = %d: %s", r.status, r.raw)
		}
		items, ok := r.body["items"].([]any)
		if !ok {
			t.Fatalf("items is %T (%v), want an array", r.body["items"], r.body["items"])
		}
		out := make([]map[string]any, 0, len(items))
		for _, it := range items {
			out = append(out, it.(map[string]any))
		}
		return out
	}
	scopedA := []string{"X-Test-Role", "finance", "X-Test-Sub", "cp06-list-a", "X-Branch-Id", f.branchID.String()}
	scopedB := []string{"X-Test-Role", "finance", "X-Test-Sub", "cp06-list-b", "X-Branch-Id", branchB.String()}

	sees := func(items []map[string]any, id string) bool {
		for _, it := range items {
			if it["id"] == id {
				return true
			}
		}
		return false
	}
	if items := list(scopedA...); !sees(items, claimA["id"].(string)) || sees(items, claimB["id"].(string)) {
		t.Errorf("branch A's list = %v, want A's claim only", items)
	}
	if items := list(scopedB...); !sees(items, claimB["id"].(string)) || sees(items, claimA["id"].(string)) {
		t.Errorf("branch B's list = %v, want B's claim only", items)
	}
}

// RULE (CP-06 item 5): the claims waiting on a manager are listed, each with
// its sale, amount, tender and state: a void's declined tender shows failed
// with its held credit until the pay back books it, and the list is the
// resolve routes' own guard, finance and owner only.
func TestCP06_RefundIntentListAndPayback(t *testing.T) {
	testutil.LockOutboxTables(t)
	gw := &fakeGateway{charges: []*payment.GatewayResult{approvedCharge(), approvedCharge()},
		refundStatuses: []payment.GatewayStatus{payment.GatewayStatusRefunded, payment.GatewayStatusDeclined}}
	f := newFixture(t, testutil.RequireDBMaxConns(t, 4), func(f *fixture) {
		f.service = f.service.WithGateway(gw)
	})
	f.openTill(10000)
	saleID, declined, credit := declinedCreditVoid(t, f, gw, mustUUID(t))

	// a cashier never reads the manager's list
	if r := f.do("GET", "/api/v1/pos/refund-intents", nil, "X-Test-Role", "cashier", "X-Test-Sub", mustUUID(t)); r.status != http.StatusForbidden {
		t.Fatalf("the list as a cashier = %d, want 403: %s", r.status, r.raw)
	}

	list := func() []map[string]any {
		t.Helper()
		r := f.do("GET", "/api/v1/pos/refund-intents", nil, "X-Test-Role", "finance", "X-Test-Sub", mustUUID(t))
		if r.status != http.StatusOK {
			t.Fatalf("the list as finance = %d: %s", r.status, r.raw)
		}
		items, ok := r.body["items"].([]any)
		if !ok {
			t.Fatalf("items is %T (%v), want an array", r.body["items"], r.body["items"])
		}
		out := make([]map[string]any, 0, len(items))
		for _, it := range items {
			out = append(out, it.(map[string]any))
		}
		return out
	}

	// the declined tender's claim stands open: failed, its credit held
	item := findSale(t, list(), declined["id"].(string))
	if got := str(t, item, "sale_id"); got != saleID {
		t.Errorf("sale_id = %q, want %q", got, saleID)
	}
	if got := str(t, item, "state"); got != "FAILED" {
		t.Errorf("state = %q, want FAILED", got)
	}
	if got := str(t, item, "act"); got != "void" {
		t.Errorf("act = %q, want void", got)
	}
	if got := num(t, item, "amount_cents"); got != credit {
		t.Errorf("amount_cents = %d, want %d (the held credit)", got, credit)
	}
	if got := str(t, item, "tender_method"); got != "card" {
		t.Errorf("tender_method = %q, want card", got)
	}
	if got := str(t, item, "sale_number"); got == "" {
		t.Errorf("sale_number is empty: %v", item)
	}
	if got := str(t, item, "created_at"); got == "" {
		t.Errorf("created_at is empty: %v", item)
	}

	// the pay back books the credit out and the claim leaves the open list
	if r := f.do("POST", "/api/v1/pos/refund-intents/"+declined["id"].(string)+"/payback",
		map[string]any{"reason": "the customer is at the counter for their card"},
		"X-Test-Role", "finance", "X-Test-Sub", mustUUID(t)); r.status != http.StatusOK {
		t.Fatalf("payback = %d: %s", r.status, r.raw)
	}
	if got := f.unappliedOf(t, declined["tender_id"].(string)); got != 0 {
		t.Errorf("the tender's unapplied credit = %d, want 0 (paid back)", got)
	}
	for _, it := range list() {
		if it["id"] == declined["id"].(string) {
			t.Errorf("the paid back claim still stands in the open list: %v", it)
		}
	}
	f.assertARInvariants(t)
}
