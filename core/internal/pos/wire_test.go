// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package pos_test

// The counter on the wire (ADR 0005 section 14.2 C2-5): the sale's shape and
// lifecycle, the completed sale's money (one invoice entry with tax and
// COGS, one receipt and application entry per tender), the void, the return
// as a credit memo, the till, and the offline sync. Inputs proved: IN-1.1,
// IN-1.2, IN-1.3, IN-1.4, IN-3.2, IN-3.7.

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"regexp"
	"testing"

	"github.com/gablelbm/gable/internal/payment"
	"github.com/gablelbm/gable/internal/tax"
	"github.com/gablelbm/gable/internal/testutil"
	"github.com/google/uuid"
)

var posNumberPattern = regexp.MustCompile(`^POS-\d{6,}$`)
var rtnNumberPattern = regexp.MustCompile(`^RTN-\d{6,}$`)

// RULE (IN-1.1, IN-1.2, IN-1.3): the sale carries its POS- number, a
// lowercase status, money in cents, the lines in the shared shape, and the
// tenders in cents.
func TestSaleWireShape(t *testing.T) {
	testutil.LockOutboxTables(t)
	f := newFixture(t, testutil.RequireDB(t))
	saleID := f.startSale(nil)
	r := f.do("GET", "/api/v1/pos/transactions/"+saleID, nil)
	if r.status != http.StatusOK {
		t.Fatalf("get = %d: %s", r.status, r.raw)
	}
	b := r.body
	if !posNumberPattern.MatchString(str(t, b, "number")) {
		t.Errorf("number = %q, want POS-nnnnnn", b["number"])
	}
	for key, want := range map[string]string{"status": "open", "currency": "USD"} {
		if got := str(t, b, key); got != want {
			t.Errorf("%s = %q, want %q", key, got, want)
		}
	}
	for key, want := range map[string]int64{"subtotal_cents": 0, "tax_cents": 0, "total_cents": 0, "change_cents": 0, "revision": 1} {
		if got := num(t, b, key); got != want {
			t.Errorf("%s = %d, want %d", key, got, want)
		}
	}
	for _, key := range []string{"customer_id", "till_session_id", "invoice_id", "completed_at"} {
		if v, ok := b[key]; !ok || v != nil {
			t.Errorf("%s = %v (present %v), want present as null", key, v, ok)
		}
	}
	if etag := r.header.Get("ETag"); etag != fmt.Sprintf(`"%d"`, rev(t, r)) {
		t.Errorf("ETag = %q, want the revision in quotes", etag)
	}

	// A line in the shared shape: priced by the engine, taxable from the
	// product, extended once.
	r = f.addLine(saleID, f.productLine("3"))
	if r.status != http.StatusOK {
		t.Fatalf("add line = %d: %s", r.status, r.raw)
	}
	line := r.body["lines"].([]any)[0].(map[string]any)
	for key, want := range map[string]string{"line_type": "product", "uom": "PCS", "price_uom": "PCS",
		"price_source": "price_list", "quantity": "3"} {
		if got := str(t, line, key); got != want {
			t.Errorf("line.%s = %q, want %q", key, got, want)
		}
	}
	if got := num(t, line, "unit_price_ten_thousandths"); got != 55000 {
		t.Errorf("line.unit_price = %d, want 55000", got)
	}
	if got := num(t, line, "line_total_cents"); got != 1650 {
		t.Errorf("line.line_total_cents = %d, want 1650", got)
	}
	if line["taxable"] != true {
		t.Errorf("line.taxable = %v, want true (from the product)", line["taxable"])
	}
	if got := num(t, r.body, "subtotal_cents"); got != 1650 {
		t.Errorf("subtotal_cents = %d, want 1650", got)
	}
	if got := num(t, r.body, "tax_cents"); got != 146 { // 1650 x 0.08875
		t.Errorf("tax_cents = %d, want 146 (the branch rate estimate)", got)
	}

	// A charge line and a text line at the counter.
	r = f.addLine(saleID, map[string]any{"line_type": "charge", "charge_code": "FREIGHT", "description": "delivery",
		"quantity": "1", "unit_price_ten_thousandths": 250000})
	if r.status != http.StatusOK {
		t.Fatalf("charge line = %d: %s", r.status, r.raw)
	}
	r = f.addLine(saleID, map[string]any{"line_type": "text", "description": "loaded by hand"})
	if r.status != http.StatusOK {
		t.Fatalf("text line = %d: %s", r.status, r.raw)
	}
	lines := r.body["lines"].([]any)
	if len(lines) != 3 {
		t.Fatalf("%d lines, want 3", len(lines))
	}
	if lt := lines[2].(map[string]any)["line_type"]; lt != "text" {
		t.Errorf("third line type = %v, want text", lt)
	}
	// freight is untaxed: subtotal 1650 + 2500, tax still on 1650 only.
	if got := num(t, r.body, "subtotal_cents"); got != 4150 {
		t.Errorf("subtotal_cents = %d, want 4150", got)
	}
	if got := num(t, r.body, "tax_cents"); got != 146 {
		t.Errorf("tax_cents = %d, want 146 (freight is untaxed)", got)
	}

	// A typed override without a reason is a 400 naming the field.
	r = f.addLine(saleID, map[string]any{"product_id": f.productID.String(), "quantity": "1",
		"unit_price_ten_thousandths": 10000})
	if r.status != http.StatusBadRequest {
		t.Fatalf("override without reason = %d, want 400", r.status)
	}
	_, _, fields := errorOf(t, r)
	if len(fields) == 0 || fields[0] != "lines[0].override_reason" {
		t.Errorf("fields = %v, want lines[0].override_reason", fields)
	}
	// An override with a reason is stored with the engine's price beside it
	// and one audit row.
	r = f.addLine(saleID, map[string]any{"product_id": f.productID.String(), "quantity": "1",
		"unit_price_ten_thousandths": 10000, "override_reason": "damaged corner"})
	if r.status != http.StatusOK {
		t.Fatalf("override = %d: %s", r.status, r.raw)
	}
	last := r.body["lines"].([]any)
	ol := last[len(last)-1].(map[string]any)
	if str(t, ol, "price_source") != "override" {
		t.Errorf("price_source = %q, want override", ol["price_source"])
	}
	if got := num(t, ol, "priced_unit_price_ten_thousandths"); got != 55000 {
		t.Errorf("priced_unit_price = %d, want the engine's 55000 beside the override", got)
	}
	if n := countOf(t, f.db, `SELECT count(*) FROM audit_log WHERE action = 'pos_transaction.line_price_overridden' AND entity_id = $1`, saleID); n != 1 {
		t.Errorf("%d override audit rows, want 1", n)
	}
	// A discount rides the line with its reason and its audit row.
	r = f.addLine(saleID, map[string]any{"product_id": f.productID.String(), "quantity": "2",
		"discount_cents": 100, "discount_reason": "goodwill"})
	if r.status != http.StatusOK {
		t.Fatalf("discount line = %d: %s", r.status, r.raw)
	}
	last = r.body["lines"].([]any)
	dl := last[len(last)-1].(map[string]any)
	if got := num(t, dl, "line_total_cents"); got != 1100-100 {
		t.Errorf("discounted line_total_cents = %d, want 1000", got)
	}
	if n := countOf(t, f.db, `SELECT count(*) FROM audit_log WHERE action = 'pos_transaction.line_discounted' AND entity_id = $1`, saleID); n != 1 {
		t.Errorf("%d discount audit rows, want 1", n)
	}
	// A kit explodes: the kit priced whole, one component at zero.
	r = f.addLine(saleID, map[string]any{"product_id": f.kitID.String(), "quantity": "1"})
	if r.status != http.StatusOK {
		t.Fatalf("kit line = %d: %s", r.status, r.raw)
	}
	last = r.body["lines"].([]any)
	kit := last[len(last)-2].(map[string]any)
	comp := last[len(last)-1].(map[string]any)
	if str(t, kit, "line_type") != "kit" || str(t, comp, "line_type") != "component" {
		t.Fatalf("kit lines = %v, %v", kit["line_type"], comp["line_type"])
	}
	if got := str(t, comp, "quantity"); got != "2" {
		t.Errorf("component quantity = %q, want 2 (a decimal string)", got)
	}
	if got := num(t, comp, "line_total_cents"); got != 0 {
		t.Errorf("component line_total_cents = %d, want 0", got)
	}
}

// RULE (ADR 0005 14.2 C2-5, the exit test of the money): a split tender sale
// posts ONE invoice entry with the tax leg and the COGS legs, ONE receipt
// entry per tender (cash and card here) and ONE application entry per
// tender, all in the sale's own transaction; the invoice's lines are the
// sale's own lines (a split tender is never a fake product line).
func TestSplitTenderSalePostsEveryEntryInTheSaleTransaction(t *testing.T) {
	testutil.LockOutboxTables(t)
	f := newFixture(t, testutil.RequireDB(t), func(f *fixture) {
		f.service = f.service.WithGateway(&fakeGateway{charges: []*payment.GatewayResult{approvedCharge()}})
	})
	f.openTill(5000)
	saleID := f.startSale(&f.customerID)
	if r := f.addLine(saleID, f.productLine("10")); r.status != http.StatusOK {
		t.Fatalf("add line = %d: %s", r.status, r.raw)
	}
	// total: 5500 + 488 tax = 5988; split cash 2000 + card 3000 + account 988
	r := f.completeSale(saleID, tender("cash", 2000), withToken(tender("card", 3000)), tender("account", 988))
	if r.status != http.StatusOK {
		t.Fatalf("complete = %d: %s", r.status, r.raw)
	}
	if got := str(t, r.body, "status"); got != "completed" {
		t.Errorf("status = %q, want completed", got)
	}
	invoiceID := str(t, r.body, "invoice_id")

	// The invoice: origin pos, pickup, its own lines, tax and COGS.
	inv := f.do("GET", "/api/v1/invoices/"+invoiceID, nil)
	if inv.status != http.StatusOK {
		t.Fatalf("get invoice = %d: %s", inv.status, inv.raw)
	}
	if str(t, inv.body, "origin") != "pos" || str(t, inv.body, "delivery_type") != "pickup" {
		t.Errorf("origin = %q delivery_type = %q, want pos and pickup", inv.body["origin"], inv.body["delivery_type"])
	}
	for key, want := range map[string]int64{"subtotal_cents": 5500, "tax_cents": 488, "total_cents": 5988} {
		if got := num(t, inv.body, key); got != want {
			t.Errorf("invoice %s = %d, want %d", key, got, want)
		}
	}
	lines := inv.body["lines"].([]any)
	if len(lines) != 1 || str(t, lines[0].(map[string]any), "line_type") != "product" {
		t.Fatalf("invoice lines = %v, want the sale's own product line", lines)
	}
	if got := num(t, lines[0].(map[string]any), "cost_cents"); got != 3250 {
		t.Errorf("invoice line cost_cents = %d, want 3250 (10 x 3.25)", got)
	}

	// ONE invoice entry: DR 1020 5988, DR 5010 3250; CR 4010 5500, CR 2020
	// 488 (the tax leg the base commit never wrote), CR 1030 3250.
	entries, legs := f.legsFor(invoiceID)
	if entries != 1 {
		t.Errorf("%d invoice entries, want 1", entries)
	}
	for code, want := range map[string]leg{
		"1020": {debit: 5988}, "5010": {debit: 3250},
		"4010": {credit: 5500}, "2020": {credit: 488}, "1030": {credit: 3250},
	} {
		if legs[code] != want {
			t.Errorf("account %s = %+v, want %+v", code, legs[code], want)
		}
	}

	// The tenders became payments: cash 2000, card 3000, and the account
	// portion left open on the invoice.
	pays := f.paymentsOf(saleID)
	if len(pays) != 2 {
		t.Fatalf("%d payments, want 2 (the account tender is not one)", len(pays))
	}
	// One receipt entry per tender, one application entry per tender: the
	// payments' own entries, then their applications'.
	for _, p := range pays {
		n, l := f.legsFor(p.id)
		if n != 1 || l["1010"].debit != p.cents || l["2200"].credit != p.cents {
			t.Errorf("payment %s: %d entries %+v, want one DR 1010 / CR 2200 of %d", p.id, n, l, p.cents)
		}
	}
	var appEntries int
	for _, p := range pays {
		var n int64
		if err := f.db.Pool.QueryRow(context.Background(), `SELECT count(*) FROM gl_journal_entries e
			JOIN ar_applications a ON a.gl_entry_id = e.id WHERE a.payment_id = $1`, p.id).Scan(&n); err != nil {
			t.Fatal(err)
		}
		appEntries += int(n)
	}
	if appEntries != 2 {
		t.Errorf("%d application entries, want one per tender", appEntries)
	}
	// The invoice: partially paid (the account portion open), the payments
	// fully applied.
	if got := str(t, inv.body, "status"); got != "partial" {
		t.Errorf("invoice status = %q, want partial (the account portion is open)", got)
	}
	if got := num(t, inv.body, "open_cents"); got != 988 {
		t.Errorf("open_cents = %d, want 988", got)
	}
	// The stock left: 100 - 10 = 90.
	if got := f.stock(); got != "90.0000/0.0000" {
		t.Errorf("stock = %s, want 90/0", got)
	}
	// The AR invariants (9.3): the subledger equals balance_due equals the
	// 1020 balance; the unapplied cash equals the 2200 balance.
	f.assertARInvariants(t)
	// The events, in order: payment.recorded per payment, the AR core's
	// (payment.applied, invoice.partial, customer.updated), invoice.created,
	// then the sale's own.
	types := eventTypes(t, f.db, "pos_transaction", saleID)
	if len(types) != 1 || types[0] != "pos_transaction.completed" {
		t.Errorf("sale events = %v, want one completed", types)
	}
	invEvents := eventTypes(t, f.db, "invoice", invoiceID)
	if len(invEvents) == 0 || invEvents[0] != "invoice.created" {
		t.Errorf("invoice events = %v, want invoice.created first", invEvents)
	}
}

// RULE (the walk-in customer, ADR 0005 14.2 C2-5): a sale without a customer
// completes against the walk-in customer, and an ACCOUNT tender is refused
// for it.
func TestWalkInSaleRefusesAccountTender(t *testing.T) {
	testutil.LockOutboxTables(t)
	f := newFixture(t, testutil.RequireDB(t))
	saleID, _ := f.saleOf("2", tender("cash", 1198))
	r := f.do("GET", "/api/v1/pos/transactions/"+saleID, nil)
	if v := r.body["customer_id"]; v != nil {
		t.Errorf("walk-in sale customer_id = %v, want null on the sale", v)
	}
	// its invoice exists against the walk-in customer
	invoiceID := str(t, r.body, "invoice_id")
	walkIn := f.scalar(`SELECT id::text FROM customers WHERE account_number = 'WALK-IN'`).(string)
	n := countOf(t, f.db, `SELECT count(*) FROM invoices WHERE id = $1 AND customer_id = $2`, invoiceID, walkIn)
	if n != 1 {
		t.Errorf("the walk-in invoice is not against the walk-in customer")
	}

	saleID = f.startSale(nil)
	f.addLine(saleID, f.productLine("1"))
	r = f.completeSale(saleID, tender("account", 599))
	if r.status != http.StatusConflict {
		t.Fatalf("account tender for the walk-in = %d, want 409", r.status)
	}
	_, blockers, _ := errorOf(t, r)
	if len(blockers) == 0 || blockers[0] != "walk_in_account" {
		t.Errorf("blockers = %v, want walk_in_account", blockers)
	}
}

// RULE (the change rule): change comes only from cash, and the payment is
// the money kept: tendered less change.
func TestChangeIsNeverSubtractedTwice(t *testing.T) {
	testutil.LockOutboxTables(t)
	f := newFixture(t, testutil.RequireDB(t))
	f.openTill(1000)
	saleID, _ := f.saleOf("2", tender("cash", 2000)) // total 1198, change 802
	pays := f.paymentsOf(saleID)
	if len(pays) != 1 || pays[0].cents != 1198 {
		t.Fatalf("cash payment = %+v, want one of 1198 (net of the 802 change)", pays)
	}
	if got := num(t, f.getSale(t, saleID), "change_cents"); got != 802 {
		t.Errorf("change_cents = %d, want 802", got)
	}
	// The drawer expectation: float + the payment kept, the change never
	// subtracted a second time.
	report := f.tillReport(t)
	if got := report["expected_by_method"].(map[string]any)["CASH"]; got != nil {
		if v := int64Of(got); v != 1000+1198 {
			t.Errorf("expected CASH = %d, want 2198 (float + the net payment)", v)
		}
	}
	// Over-tender on a non-cash method is refused.
	saleID = f.startSale(nil)
	f.addLine(saleID, f.productLine("1"))
	r := f.completeSale(saleID, tender("card", 100000))
	if r.status != http.StatusConflict {
		t.Fatalf("card over tender = %d, want 409", r.status)
	}
	_, blockers, _ := errorOf(t, r)
	if len(blockers) == 0 || blockers[0] != "change_from_cash" {
		t.Errorf("blockers = %v, want change_from_cash", blockers)
	}
}

// RULE (the void, ADR 0005 14.2 C2-5): a void while the session is open is
// one transaction over the sale, its payments and its invoice. A card
// tender's refund goes through the gateway before it; the ledger ends at
// zero for the sale.
func TestVoidOfACardSaleLeavesTheLedgerAtZero(t *testing.T) {
	testutil.LockOutboxTables(t)
	gw := &fakeGateway{charges: []*payment.GatewayResult{approvedCharge()}, refunds: []string{}}
	f := newFixture(t, testutil.RequireDB(t), func(f *fixture) {
		f.service = f.service.WithGateway(gw)
	})
	f.openTill(0)
	saleID, body := f.saleOf("4", withToken(tender("card", 2395))) // 2200 + 195 tax
	invoiceID := str(t, body.body, "invoice_id")
	r := f.do("POST", "/api/v1/pos/transactions/"+saleID+"/void",
		map[string]any{"reason": "wrong basket", "revision": rev(t, body)},
		"X-Test-Role", "cashier", "X-Test-Sub", mustUUID(t))
	if r.status != http.StatusOK {
		t.Fatalf("void = %d: %s", r.status, r.raw)
	}
	if got := str(t, r.body, "status"); got != "voided" {
		t.Errorf("status = %q, want voided", got)
	}
	if len(gw.refunds) != 1 {
		t.Fatalf("%d gateway refunds, want 1", len(gw.refunds))
	}
	// The ledger at zero for the sale: every account the sale touched nets
	// to zero (the invoice entry and its reversal, the receipt and its
	// application refund entries).
	for _, code := range []string{"1010", "1020", "2020", "2200", "4010", "5010", "1030"} {
		if got := f.accountBalance(code); got != 0 {
			t.Errorf("account %s balance = %d, want 0", code, got)
		}
	}
	// The stock came back.
	if got := f.stock(); got != "100.0000/0.0000" {
		t.Errorf("stock = %s, want 100/0", got)
	}
	// The invoice is void.
	inv := f.do("GET", "/api/v1/invoices/"+invoiceID, nil)
	if str(t, inv.body, "status") != "void" {
		t.Errorf("invoice status = %q, want void", inv.body["status"])
	}
	f.assertARInvariants(t)
	types := eventTypes(t, f.db, "pos_transaction", saleID)
	if len(types) != 2 || types[1] != "pos_transaction.voided" {
		t.Errorf("sale events = %v, want completed then voided", types)
	}
}

// RULE (the void after the session closes is a return): once the drawer is
// counted, the sale is no longer voidable.
func TestVoidAfterSessionCloseIsRefused(t *testing.T) {
	testutil.LockOutboxTables(t)
	f := newFixture(t, testutil.RequireDB(t))
	sessionID := f.openTill(0)
	saleID, _ := f.saleOf("1", tender("cash", 599))
	r := f.do("POST", "/api/v1/pos/till/"+sessionID+"/close", map[string]any{
		"counted_by_method": map[string]any{"cash": 599}}, "X-Test-Role", "cashier", "X-Test-Sub", mustUUID(t))
	if r.status != http.StatusOK {
		t.Fatalf("close till = %d: %s", r.status, r.raw)
	}
	r = f.do("POST", "/api/v1/pos/transactions/"+saleID+"/void", map[string]any{"reason": "late"},
		"X-Test-Role", "cashier", "X-Test-Sub", mustUUID(t))
	if r.status != http.StatusConflict {
		t.Fatalf("late void = %d, want 409", r.status)
	}
	_, blockers, _ := errorOf(t, r)
	if len(blockers) == 0 || blockers[0] != "session_closed" {
		t.Errorf("blockers = %v, want session_closed", blockers)
	}
}

// RULE (the return, ADR 0005 14.2 C2-5): a return is a credit memo created
// and posted in one act with its restock lines, refunded in cash or card or
// left as account credit; it restocks and reverses COGS.
func TestReturnRestocksAndReversesCOGS(t *testing.T) {
	testutil.LockOutboxTables(t)
	f := newFixture(t, testutil.RequireDB(t))
	saleID, body := f.saleOf("10", tender("cash", 5988))
	saleLines := body.body["lines"].([]any)
	firstLine := saleLines[0].(map[string]any)["id"].(string)
	r := f.do("POST", "/api/v1/pos/returns", map[string]any{
		"register_id": f.register, "customer_id": f.customerID.String(), "original_sale_id": saleID,
		"refund_method": "cash",
		"reason":        "wrong length", "lines": []map[string]any{{
			"product_id": f.productID.String(), "line_id": firstLine, "quantity": "4", "restock": true,
		}}}, "X-Test-Role", "cashier", "X-Test-Sub", mustUUID(t))
	if r.status != http.StatusCreated {
		t.Fatalf("return = %d: %s", r.status, r.raw)
	}
	if !rtnNumberPattern.MatchString(str(t, r.body, "number")) {
		t.Errorf("return number = %q, want RTN-nnnnnn", r.body["number"])
	}
	// negative totals, a linked credit memo
	for key, want := range map[string]int64{"subtotal_cents": -2200, "tax_cents": -195, "total_cents": -2395} {
		if got := num(t, r.body, key); got != want {
			t.Errorf("return %s = %d, want %d", key, got, want)
		}
	}
	memoID := str(t, r.body, "credit_memo_id")
	memo := f.do("GET", "/api/v1/credit-memos/"+memoID, nil)
	if memo.status != http.StatusOK {
		t.Fatalf("get memo = %d: %s", memo.status, memo.raw)
	}
	if str(t, memo.body, "status") != "applied" {
		t.Errorf("memo status = %q, want applied (the cash refund used it)", memo.body["status"])
	}
	// The stock came back: 90 + 4.
	if got := f.stock(); got != "94.0000/0.0000" {
		t.Errorf("stock = %s, want 94/0", got)
	}
	// The memo's entry: DR 4010 2200, DR 2020 195, DR 1030 1300 (4 x 3.25
	// restocked cost); CR 1020 2395, CR 5010 1300.
	entries, legs := f.legsFor(memoID)
	if entries != 1 {
		t.Errorf("%d memo entries, want 1", entries)
	}
	for code, want := range map[string]leg{
		"4010": {debit: 2200}, "2020": {debit: 195}, "1030": {debit: 1300},
		"1020": {credit: 2395}, "5010": {credit: 1300},
	} {
		if legs[code] != want {
			t.Errorf("account %s = %+v, want %+v", code, legs[code], want)
		}
	}
	// The cash refund pays the credit out: DR 1020 / CR 1010 of 2395.
	if got := f.accountBalance("1010"); got != 5988-2395 {
		t.Errorf("cash balance = %d, want 3593 (the sale in, the refund out)", got)
	}
	f.assertARInvariants(t)
	types := eventTypes(t, f.db, "pos_return", str(t, r.body, "id"))
	if len(types) != 1 || types[0] != "pos_return.completed" {
		t.Errorf("return events = %v", types)
	}
	_ = saleID
}

// RULE (the account credit return): refunded to account, the credit stays
// open on the customer's account.
func TestAccountReturnLeavesOpenCredit(t *testing.T) {
	testutil.LockOutboxTables(t)
	f := newFixture(t, testutil.RequireDB(t))
	f.saleOf("4", tender("cash", 2395))
	r := f.do("POST", "/api/v1/pos/returns", map[string]any{
		"register_id": f.register, "customer_id": f.customerID.String(), "refund_method": "account",
		"reason": "store credit", "lines": []map[string]any{{
			"product_id": f.productID.String(), "quantity": "1",
			"unit_price_ten_thousandths": 55000, "restock": true,
		}}}, "X-Test-Role", "cashier", "X-Test-Sub", mustUUID(t))
	if r.status != http.StatusCreated {
		t.Fatalf("account return = %d: %s", r.status, r.raw)
	}
	memoID := str(t, r.body, "credit_memo_id")
	memo := f.do("GET", "/api/v1/credit-memos/"+memoID, nil)
	if str(t, memo.body, "status") != "open" {
		t.Errorf("memo status = %q, want open", memo.body["status"])
	}
	if got := num(t, memo.body, "open_cents"); got != -599 {
		t.Errorf("memo open_cents = %d, want -599", got)
	}
	f.assertARInvariants(t)
}

// RULE (the till): the drawer's expected cash is the session's cash
// payments (already net of change) plus the opening float less cash refunds.
func TestTillExpectedCashIncludesFloatAndRefunds(t *testing.T) {
	testutil.LockOutboxTables(t)
	f := newFixture(t, testutil.RequireDB(t))
	f.openTill(5000)
	f.saleOf("2", tender("cash", 2000)) // keeps 1198, change 802
	f.saleOf("1", tender("card", 599))  // card; no cash, terminal settles it
	// a cash return of 500 out of the drawer
	r := f.do("POST", "/api/v1/pos/returns", map[string]any{
		"register_id": f.register, "customer_id": f.customerID.String(), "refund_method": "cash",
		"reason": "scratched", "lines": []map[string]any{{
			"product_id": f.productID.String(), "quantity": "1",
			"unit_price_ten_thousandths": 45800, "restock": false,
		}}}, "X-Test-Role", "cashier", "X-Test-Sub", mustUUID(t))
	if r.status != http.StatusCreated {
		t.Fatalf("cash return = %d: %s", r.status, r.raw)
	}
	// 458 + tax(8.875%) = 458 + 41 = 499 -> 500 with rounding handled by cents
	report := f.tillReport(t)
	expected := report["expected_by_method"].(map[string]any)
	if v := int64Of(expected["CASH"]); v != 5000+1198-499 {
		t.Errorf("expected CASH = %d, want %d (float + net cash - cash refund)", v, 5000+1198-499)
	}
	if v := int64Of(expected["CARD"]); v != 599 {
		t.Errorf("expected CARD = %d, want 599", v)
	}
	// closing counts the drawer and books the variance inside the close's
	// transaction
	r = f.do("POST", "/api/v1/pos/till/"+f.currentTillID(t)+"/close", map[string]any{
		"counted_by_method": map[string]any{"cash": 5699}}, "X-Test-Role", "cashier", "X-Test-Sub", mustUUID(t))
	if r.status != http.StatusOK {
		t.Fatalf("close = %d: %s", r.status, r.raw)
	}
	sess := r.body["session"].(map[string]any)
	if got := num(t, sess, "over_short_cents"); got != 0 {
		t.Errorf("over_short = %d, want 0", got)
	}
	// A short drawer books the variance to 5030.
	sessionID := f.openTill(0)
	f.saleOf("1", tender("cash", 599))
	r = f.do("POST", "/api/v1/pos/till/"+sessionID+"/close", map[string]any{
		"counted_by_method": map[string]any{"cash": 500}}, "X-Test-Role", "cashier", "X-Test-Sub", mustUUID(t))
	if r.status != http.StatusOK {
		t.Fatalf("close 2 = %d: %s", r.status, r.raw)
	}
	sess2 := r.body["session"].(map[string]any)
	if got := num(t, sess2, "over_short_cents"); got != -99 {
		t.Errorf("over_short = %d, want -99", got)
	}
	if got := f.accountBalance("5030"); got != 99 {
		t.Errorf("5030 balance = %d, want 99 (a short drawer is a debit)", got)
	}
}

// RULE (IN-1.4, IN-3.2): the sale list is the list envelope, filters filter
// and unknown parameters are refused.
func TestSaleListEnvelopeAndFilters(t *testing.T) {
	testutil.LockOutboxTables(t)
	f := newFixture(t, testutil.RequireDB(t))
	f.saleOf("1", tender("cash", 599))
	// an empty register's page is [] in the bytes
	r := f.do("GET", "/api/v1/pos/transactions?register_id=NOPE", nil)
	if r.status != http.StatusOK {
		t.Fatalf("list = %d: %s", r.status, r.raw)
	}
	if items, ok := r.body["items"].([]any); !ok || len(items) != 0 {
		t.Errorf("items = %v, want an empty array", r.body["items"])
	}
	// the status filter filters; an unknown value is refused
	r = f.do("GET", "/api/v1/pos/transactions?status=completed", nil)
	if r.status != http.StatusOK {
		t.Fatalf("list = %d: %s", r.status, r.raw)
	}
	if items := r.body["items"].([]any); len(items) != 1 {
		t.Errorf("%d completed sales, want 1", len(items))
	}
	r = f.do("GET", "/api/v1/pos/transactions?status=OPEN", nil)
	if r.status != http.StatusBadRequest {
		t.Fatalf("uppercase status = %d, want 400", r.status)
	}
	r = f.do("GET", "/api/v1/pos/transactions?bogus=1", nil)
	if r.status != http.StatusBadRequest {
		t.Fatalf("unknown parameter = %d, want 400", r.status)
	}
	// the returns list is an array, never null
	r = f.do("GET", "/api/v1/pos/returns", nil)
	if r.status != http.StatusOK || r.raw == nil || string(r.raw) == "null" {
		t.Errorf("returns list = %d %s", r.status, r.raw)
	}
}

// RULE (IN-3.7): every refusal is the error envelope with its status, code
// and blocker; the revision precondition refuses a stale completion.
func TestErrorEnvelopeAndRevision(t *testing.T) {
	testutil.LockOutboxTables(t)
	f := newFixture(t, testutil.RequireDB(t))
	saleID := f.startSale(nil)
	f.addLine(saleID, f.productLine("1"))
	// a stale revision
	r := f.completeSaleWithRevision(saleID, 99, tender("cash", 599))
	if r.status != http.StatusConflict {
		t.Fatalf("stale complete = %d, want 409", r.status)
	}
	if code, _, _ := errorOf(t, r); code != "stale_revision" {
		t.Errorf("code = %q, want stale_revision", code)
	}
	// an unknown sale
	r = f.do("GET", "/api/v1/pos/transactions/"+uuid.NewString(), nil)
	if r.status != http.StatusNotFound {
		t.Errorf("unknown sale = %d, want 404", r.status)
	}
	// a bad tender method names its field
	r = f.completeSale(saleID, tender("clamshells", 599))
	if r.status != http.StatusBadRequest {
		t.Fatalf("bad method = %d, want 400", r.status)
	}
	_, _, fields := errorOf(t, r)
	if len(fields) == 0 || fields[0] != "tenders[0].method" {
		t.Errorf("fields = %v, want tenders[0].method", fields)
	}
	// a tender in cents: a decimal point is a decode error, never a rounding
	r = f.do("POST", "/api/v1/pos/transactions/"+saleID+"/complete",
		`{"tenders":[{"method":"cash","amount_cents":5.99}]}`,
		"X-Test-Role", "cashier", "X-Test-Sub", mustUUID(t))
	if r.status != http.StatusBadRequest {
		t.Errorf("decimal tender = %d, want 400 (IN-1.2: integer cents)", r.status)
	}
}

// ---------------------------------------------------------------------------
// Helpers over the database.
// ---------------------------------------------------------------------------

type paymentOf struct {
	id     string
	cents  int64
	method string
}

func (f *fixture) paymentsOf(saleID string) []paymentOf {
	f.t.Helper()
	rows, err := f.db.Pool.Query(context.Background(), `
		SELECT p.id::text, ROUND(p.amount * 100)::bigint, p.method
		FROM payments p JOIN pos_tenders pt ON pt.payment_id = p.id
		WHERE pt.transaction_id = $1 ORDER BY p.created_at`, saleID)
	if err != nil {
		f.t.Fatal(err)
	}
	defer rows.Close()
	var out []paymentOf
	for rows.Next() {
		var p paymentOf
		if err := rows.Scan(&p.id, &p.cents, &p.method); err != nil {
			f.t.Fatal(err)
		}
		out = append(out, p)
	}
	return out
}

// assertARInvariants proves the subledger rule of ADR 0005 9.3 for every
// customer the fixture touched (its own and the walk-in that carries most
// of the sales) with all three legs: balance_due = the sum of its
// customer_transactions = the sum of amount_open over its live invoices and
// posted credit memos. The control accounts tie globally, each in one
// statement so the snapshot is consistent: GL 1020 = the sum of every
// customer's balance_due, GL 2200 = the sum of unapplied cash.
func (f *fixture) assertARInvariants(t *testing.T) {
	t.Helper()
	walkIn := f.scalar(`SELECT id::text FROM customers WHERE account_number = 'WALK-IN'`).(string)
	rows, err := f.db.Pool.Query(context.Background(), `
		SELECT c.id, COALESCE(ROUND(c.balance_due * 100)::bigint, 0),
			COALESCE((SELECT SUM(ROUND(ct.amount)::bigint) FROM customer_transactions ct WHERE ct.customer_id = c.id), 0),
			COALESCE((SELECT SUM(ROUND(i.amount_open * 100)::bigint) FROM invoices i
				WHERE i.customer_id = c.id AND i.status IN ('UNPAID', 'PARTIAL', 'OVERDUE')), 0)
			+ COALESCE((SELECT SUM(ROUND(m.amount_open * 100)::bigint) FROM credit_memos m
				WHERE m.customer_id = c.id AND m.status IN ('OPEN', 'PARTIAL')), 0)
		FROM customers c WHERE c.id = ANY($1::uuid[])`, []string{f.customerID.String(), walkIn})
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	for rows.Next() {
		var id string
		var balance, subledger, documents int64
		if err := rows.Scan(&id, &balance, &subledger, &documents); err != nil {
			t.Fatal(err)
		}
		if balance != subledger {
			t.Errorf("customer %s: balance_due is %d but the subledger sums %d", id, balance, subledger)
		}
		if balance != documents {
			t.Errorf("customer %s: balance_due is %d but the open documents sum %d", id, balance, documents)
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	// GL 1020 ties to the sum of every customer's balance, GL 2200 to the
	// unapplied cash; each in one statement, so the two sides of a tie are
	// read from one consistent snapshot even while other tests run.
	var ties bool
	if err := f.db.Pool.QueryRow(context.Background(), `
		SELECT (SELECT COALESCE(ROUND(SUM(c.balance_due) * 100)::bigint, 0) FROM customers c)
			= (SELECT COALESCE(ROUND((SUM(l.debit) - SUM(l.credit)) * 100)::bigint, 0)
				FROM gl_journal_lines l JOIN gl_accounts a ON a.id = l.account_id WHERE a.code = '1020')`).Scan(&ties); err != nil {
		t.Fatal(err)
	}
	if !ties {
		t.Error("GL 1020 does not tie to the sum of the customers' balances")
	}
	if err := f.db.Pool.QueryRow(context.Background(), `
		SELECT (SELECT COALESCE(SUM(ROUND(p.amount_unapplied * 100)::bigint), 0) FROM payments p WHERE p.status = 'POSTED')
			= (SELECT COALESCE(ROUND((SUM(l.credit) - SUM(l.debit)) * 100)::bigint, 0)
				FROM gl_journal_lines l JOIN gl_accounts a ON a.id = l.account_id WHERE a.code = '2200')`).Scan(&ties); err != nil {
		t.Fatal(err)
	}
	if !ties {
		t.Error("GL 2200 does not tie to the sum of the unapplied cash (2200 carries it as a credit)")
	}
}

func (f *fixture) getSale(t *testing.T, id string) map[string]any {
	t.Helper()
	r := f.do("GET", "/api/v1/pos/transactions/"+id, nil)
	if r.status != http.StatusOK {
		t.Fatalf("get sale = %d: %s", r.status, r.raw)
	}
	return r.body
}

func (f *fixture) completeSaleWithRevision(saleID string, revision int64, tenders ...map[string]any) resp {
	f.t.Helper()
	return f.do("POST", "/api/v1/pos/transactions/"+saleID+"/complete",
		map[string]any{"tenders": tenders, "revision": revision},
		"X-Test-Role", "cashier", "X-Test-Sub", mustUUID(f.t))
}

func (f *fixture) tillReport(t *testing.T) map[string]any {
	t.Helper()
	r := f.do("GET", "/api/v1/pos/till/"+f.currentTillID(t)+"/report", nil)
	if r.status != http.StatusOK {
		t.Fatalf("till report = %d: %s", r.status, r.raw)
	}
	return r.body
}

func (f *fixture) currentTillID(t *testing.T) string {
	t.Helper()
	var id string
	if err := f.db.Pool.QueryRow(context.Background(), `SELECT id::text FROM till_sessions WHERE register_id = $1 AND status = 'OPEN' ORDER BY opened_at DESC LIMIT 1`, f.register).Scan(&id); err != nil {
		t.Fatal(err)
	}
	return id
}

func int64Of(v any) int64 {
	switch n := v.(type) {
	case int64:
		return n
	case float64:
		return int64(n)
	case json.Number:
		i, _ := n.Int64()
		return i
	}
	return -1
}

// fakeGateway is the card terminal in tests.
type fakeGateway struct {
	charges     []*payment.GatewayResult
	refunds     []string
	voids       []string
	chargeCalls int
	currency    []string
}

func approvedCharge() *payment.GatewayResult {
	return &payment.GatewayResult{Status: payment.GatewayStatusApproved, TransactionID: "gw-1", AuthCode: "A1",
		CardLast4: "4242", CardBrand: "VISA"}
}

func (g *fakeGateway) Charge(ctx context.Context, req payment.ChargeRequest) (*payment.GatewayResult, error) {
	g.chargeCalls++
	g.currency = append(g.currency, req.Currency)
	if len(g.charges) == 0 {
		return nil, fmt.Errorf("no charge scripted")
	}
	res := g.charges[0]
	g.charges = g.charges[1:]
	return res, nil
}

func (g *fakeGateway) Capture(ctx context.Context, id string, cents int64) (*payment.GatewayResult, error) {
	return approvedCharge(), nil
}

func (g *fakeGateway) Void(ctx context.Context, id string) (*payment.GatewayResult, error) {
	g.voids = append(g.voids, id)
	return &payment.GatewayResult{Status: payment.GatewayStatusVoided}, nil
}

func (g *fakeGateway) Refund(ctx context.Context, id string, cents int64) (*payment.GatewayResult, error) {
	g.refunds = append(g.refunds, id)
	return &payment.GatewayResult{Status: payment.GatewayStatusRefunded, TransactionID: "ref-1"}, nil
}

func withToken(t map[string]any) map[string]any {
	t["token_id"] = "tok-1"
	return t
}

// flakyTaxProvider answers PreviewTax with an error while its fail flag is
// set, the configured provider otherwise (the offline sync's pending path).
type flakyTaxProvider struct {
	fail bool
}

func (p *flakyTaxProvider) ProviderConfigured() bool { return true }

func (p *flakyTaxProvider) PreviewTax(ctx context.Context, req *tax.TaxPreviewRequest) (*tax.TaxResult, error) {
	if p.fail {
		return nil, fmt.Errorf("provider outage")
	}
	return &tax.TaxResult{TotalTax: 0}, nil
}

// RULE (ADR 0005 section 14.2 C2-5, and section 3's one exception): an
// offline sale synced while the tax provider fails is never rejected; it
// stays pending in the sync log, and the same batch retried once the
// provider answers completes it through the same path a live sale takes.
func TestOfflineSyncPendingOnTaxFailureCompletesOnRetry(t *testing.T) {
	testutil.LockOutboxTables(t)
	provider := &flakyTaxProvider{fail: true}
	f := newFixture(t, testutil.RequireDBMaxConns(t, 4), func(f *fixture) {
		f.service.WithTaxProvider(provider)
	})
	batch := func(clientID string) map[string]any {
		return map[string]any{
			"batch_id": "sync-pending-1", "register_id": f.register,
			"items": []map[string]any{{
				"client_id": clientID, "cashier_id": mustUUID(t),
				"items":   []map[string]any{{"product_id": f.productID.String(), "quantity": "1"}},
				"tenders": []map[string]any{{"method": "cash", "amount_cents": 599}},
			}},
		}
	}
	clientID := "c1c1c1c1-0000-4000-8000-00000000beef"
	// The provider is down: the sale is pending, not an error, and nothing of
	// it exists yet (no sale row, no invoice, no payment).
	r := f.do("POST", "/api/v1/pos/sync", batch(clientID), "X-Test-Role", "cashier", "X-Test-Sub", mustUUID(t))
	if r.status != http.StatusOK {
		t.Fatalf("sync under a provider outage = %d: %s", r.status, r.raw)
	}
	if got := num(t, r.body, "pending_count"); got != 1 {
		t.Fatalf("pending_count = %d, want 1 (the body: %s)", got, r.raw)
	}
	if got := num(t, r.body, "error_count"); got != 0 {
		t.Errorf("error_count = %d, want 0: a provider failure is never a rejection", got)
	}
	if got := countOf(t, f.db, `SELECT count(*) FROM invoices WHERE order_id IS NULL`); got != 0 {
		t.Errorf("%d invoices after the pending sync, want 0", got)
	}
	if got := countOf(t, f.db, `SELECT count(*) FROM payments`); got != 0 {
		t.Errorf("%d payments after the pending sync, want 0", got)
	}
	// The provider answers: the same batch completes the sale through the
	// same path a live sale takes, and the replay is a duplicate.
	provider.fail = false
	r = f.do("POST", "/api/v1/pos/sync", batch(clientID), "X-Test-Role", "cashier", "X-Test-Sub", mustUUID(t))
	if r.status != http.StatusOK {
		t.Fatalf("sync retry = %d: %s", r.status, r.raw)
	}
	if got := num(t, r.body, "synced_count"); got != 1 {
		t.Errorf("synced_count = %d, want 1 (the body: %s)", got, r.raw)
	}
	if got := countOf(t, f.db, `SELECT count(*) FROM invoices WHERE order_id IS NULL`); got != 1 {
		t.Errorf("%d invoices after the retry, want 1", got)
	}
	r = f.do("POST", "/api/v1/pos/sync", batch(clientID), "X-Test-Role", "cashier", "X-Test-Sub", mustUUID(t))
	if got := num(t, r.body, "duplicate_count"); got != 1 {
		t.Errorf("duplicate_count = %d, want 1", got)
	}
	f.assertARInvariants(t)
}
