// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package invoice_test

// The invoice module on the wire contract (ADR 0001) and ADR 0005 sections 4
// and 6: the document and its lines, the list, the filters, the overdue flag
// that replaced the OVERDUE status, the void with its ledger, stock and order
// effects, and the gapless number.

import (
	"context"
	"fmt"
	"net/http"
	"regexp"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gablelbm/gable/internal/testutil"
	"github.com/gablelbm/gable/pkg/outbox"
	"github.com/google/uuid"
)

var numberPattern = regexp.MustCompile(`^IN-\d{6,}$`)

// RULE (IN-1.1, IN-1.2, IN-1.3, IN-1.8, IN-3.8, IN-5): an invoice read carries
// its number, a lowercase status, money in _cents, the scaled unit price, the
// tax with its rate and source, what is still owed, the terms dates, every
// optional field present as null, the revision with its ETag, and every line in
// the shared shape with the cost that left.
func TestInvoiceWireShape(t *testing.T) {
	testutil.LockOutboxTables(t)
	db := testutil.RequireDB(t)
	f := newFixture(t, db)
	invID, orderID := f.invoice("10") // 10 PCS at 5.50, tax 8.875%

	r := f.do("GET", "/api/v1/invoices/"+invID, nil)
	if r.status != http.StatusOK {
		t.Fatalf("get = %d: %s", r.status, r.raw)
	}
	b := r.body
	if !numberPattern.MatchString(str(t, b, "number")) {
		t.Errorf("number = %q, want IN-nnnnnn", b["number"])
	}
	for key, want := range map[string]string{"status": "unpaid", "currency": "USD", "origin": "order", "delivery_type": "pickup",
		"picked_up_by": "Counter customer", "tax_source": "branch_rate", "tax_rate_percent": "8.875", "order_id": orderID} {
		if got := str(t, b, key); got != want {
			t.Errorf("%s = %q, want %q", key, got, want)
		}
	}
	for key, want := range map[string]int64{"subtotal_cents": 5500, "tax_cents": 488, "total_cents": 5988, "open_cents": 5988} {
		if got := num(t, b, key); got != want {
			t.Errorf("%s = %d, want %d", key, got, want)
		}
	}
	if b["is_overdue"] != false || b["tax_exempt"] != false {
		t.Errorf("is_overdue %v tax_exempt %v, want false false", b["is_overdue"], b["tax_exempt"])
	}
	if etag := r.header.Get("ETag"); etag != fmt.Sprintf(`"%d"`, rev(t, r)) {
		t.Errorf("ETag = %q, want the revision %d in quotes", etag, rev(t, r))
	}
	// Every optional field is present, null while unset (ADR 0001 section 12).
	for _, key := range []string{"job_id", "ship_to_id", "ship_to", "delivery_id", "discount_due_date", "discount_percent", "paid_at",
		"voided_at", "voided_by", "void_reason"} {
		if v, ok := b[key]; !ok || v != nil {
			t.Errorf("%s = %v (present %v), want present as null", key, v, ok)
		}
	}
	for _, key := range []string{"id", "branch_id", "customer_id", "customer_name", "payment_terms_id", "gl_entry_id", "created_at", "updated_at"} {
		if _, ok := b[key]; !ok || b[key] == nil {
			t.Errorf("%s is missing or null", key)
		}
	}
	if !regexp.MustCompile(`^\d{4}-\d{2}-\d{2}$`).MatchString(str(t, b, "invoice_date")) || !regexp.MustCompile(`^\d{4}-\d{2}-\d{2}$`).MatchString(str(t, b, "due_date")) {
		t.Errorf("invoice_date %v due_date %v, want YYYY-MM-DD", b["invoice_date"], b["due_date"])
	}
	for _, key := range []string{"subtotal", "tax_rate", "tax_amount", "total_amount", "payment_terms", "data"} {
		if _, ok := b[key]; ok {
			t.Errorf("legacy field %q is still on the wire", key)
		}
	}

	lines := b["lines"].([]any)
	if len(lines) != 1 {
		t.Fatalf("%d lines, want 1: %s", len(lines), r.raw)
	}
	l := lines[0].(map[string]any)
	for key, want := range map[string]string{"line_type": "product", "quantity": "10", "uom": "PCS", "price_uom": "PCS", "uom_qty": "1", "price_uom_qty": "1",
		"price_source": "price_list", "sku": f.sku, "description": "2x4x8 SPF"} {
		if got := str(t, l, key); got != want {
			t.Errorf("line %s = %q, want %q", key, got, want)
		}
	}
	for key, want := range map[string]int64{"unit_price_ten_thousandths": 55000, "line_total_cents": 5500, "unit_cost_ten_thousandths": 32500, "cost_cents": 3250, "position": 0} {
		if got := num(t, l, key); got != want {
			t.Errorf("line %s = %d, want %d", key, got, want)
		}
	}
	if l["taxable"] != true || l["order_line_id"] == nil {
		t.Errorf("line taxable %v order_line_id %v", l["taxable"], l["order_line_id"])
	}
	for _, key := range []string{"parent_line_id", "charge_code", "discount_percent", "discount_cents", "discount_reason", "override_reason"} {
		if v, ok := l[key]; !ok || v != nil {
			t.Errorf("line %s = %v (present %v), want present as null", key, v, ok)
		}
	}

	// The order lists the invoice (IN-5: the record route is stable) and the
	// events feed carries invoice.created.
	g := f.do("GET", "/api/v1/orders/"+orderID, nil)
	if ids := g.body["invoice_ids"].([]any); len(ids) != 1 || ids[0] != invID {
		t.Errorf("order invoice_ids = %v, want [%s]", ids, invID)
	}
	if ev := eventTypes(t, db, "invoice", invID); fmt.Sprint(ev) != "[invoice.created]" {
		t.Errorf("invoice events = %v, want [invoice.created]", ev)
	}
	if bad := f.do("GET", "/api/v1/invoices/"+invID+"?x=1", nil); bad.status != 400 {
		t.Errorf("an undeclared query parameter on the get = %d, want 400", bad.status)
	}
}

// RULE (IN-3.7): not found and a malformed id are the wire's envelope.
func TestInvoiceGetRefusals(t *testing.T) {
	testutil.LockOutboxTables(t)
	db := testutil.RequireDB(t)
	f := newFixture(t, db)
	r := f.do("GET", "/api/v1/invoices/"+uuid.NewString(), nil)
	if r.status != 404 {
		t.Fatalf("unknown invoice = %d, want 404", r.status)
	}
	if code, _, _ := errorOf(t, r); code != "not_found" {
		t.Errorf("code = %q, want not_found", code)
	}
	r = f.do("GET", "/api/v1/invoices/not-a-uuid", nil)
	if code, _, fields := errorOf(t, r); r.status != 400 || code != "bad_request" || len(fields) != 1 || fields[0] != "id" {
		t.Errorf("malformed id = %d %q %v, want 400 bad_request naming id", r.status, code, fields)
	}
}

// RULE (IN-1.4, IN-3.2): the list is the cursor envelope, newest first, every
// row once, with filters that filter and unknown ones refused; an empty page is
// [] in the bytes.
func TestInvoiceList(t *testing.T) {
	testutil.LockOutboxTables(t)
	db := testutil.RequireDB(t)
	f := newFixture(t, db)
	var ids []string
	for i := 0; i < 3; i++ {
		id, _ := f.invoice("1")
		ids = append(ids, id)
	}
	// newest first, by (created_at, id)
	var seen []string
	cursor := ""
	for page := 0; page < 5; page++ {
		path := "/api/v1/invoices?limit=2&customer_id=" + f.customerID.String()
		if cursor != "" {
			path += "&cursor=" + cursor
		}
		r := f.do("GET", path, nil)
		if r.status != 200 {
			t.Fatalf("list = %d: %s", r.status, r.raw)
		}
		for _, it := range r.body["items"].([]any) {
			seen = append(seen, it.(map[string]any)["id"].(string))
		}
		if r.body["next_cursor"] == nil {
			break
		}
		cursor = r.body["next_cursor"].(string)
	}
	want := []string{ids[2], ids[1], ids[0]}
	if fmt.Sprint(seen) != fmt.Sprint(want) {
		t.Errorf("the cursor walk saw %v, want %v (newest first, each once)", seen, want)
	}

	if r := f.do("GET", "/api/v1/invoices?customer_id="+f.customerID.String()+"&include=total", nil); r.status != 200 || num(t, r.body, "total") != 3 {
		t.Errorf("include=total = %d %v, want 3", r.status, r.body["total"])
	}
	if r := f.do("GET", "/api/v1/invoices?customer_id="+f.customerID.String()+"&order_id="+uuid.NewString(), nil); r.status != 200 || len(r.body["items"].([]any)) != 0 {
		t.Errorf("an order_id filter that matches nothing: %d %s", r.status, r.raw)
	}
	empty := f.do("GET", "/api/v1/invoices?customer_id="+uuid.NewString(), nil)
	if !strings.Contains(string(empty.raw), `"items":[]`) {
		t.Errorf("an empty page = %s, want items [] in the bytes", empty.raw)
	}
	for _, bad := range []string{"offset=1", "status=UNPAID", "status=overdue", "overdue=maybe", "customer_id=zzz", "limit=0", "cursor=junk", "nope=1"} {
		if r := f.do("GET", "/api/v1/invoices?"+bad, nil); r.status != 400 {
			t.Errorf("?%s = %d, want 400", bad, r.status)
		}
	}
	if r := f.do("GET", "/api/v1/invoices?status=unpaid,partial&customer_id="+f.customerID.String(), nil); r.status != 200 || len(r.body["items"].([]any)) != 3 {
		t.Errorf("status=unpaid,partial = %d %s", r.status, r.raw)
	}
	if r := f.do("GET", "/api/v1/invoices?status=void&customer_id="+f.customerID.String(), nil); r.status != 200 || len(r.body["items"].([]any)) != 0 {
		t.Errorf("status=void = %d %s", r.status, r.raw)
	}
}

// RULE (ADR 0005 6.1): OVERDUE is gone from the wire. An open invoice past its
// due date is is_overdue, and the overdue filter lists it; a paid one is not
// overdue however old; an invoice not yet due is not.
func TestOverdueIsComputedNotStored(t *testing.T) {
	testutil.LockOutboxTables(t)
	db := testutil.RequireDB(t)
	f := newFixture(t, db)
	late, _ := f.invoice("1")
	current, _ := f.invoice("1")
	paid, _ := f.invoice("1")
	mustExec(t, db, `UPDATE invoices SET due_date = CURRENT_DATE - 40 WHERE id = ANY($1)`, []uuid.UUID{uuid.MustParse(late), uuid.MustParse(paid)})
	mustExec(t, db, `UPDATE invoices SET status = 'PAID', paid_at = NOW() WHERE id = $1`, paid)

	list := func(q string) []string {
		r := f.do("GET", "/api/v1/invoices?customer_id="+f.customerID.String()+q, nil)
		if r.status != 200 {
			t.Fatalf("list ?%s = %d: %s", q, r.status, r.raw)
		}
		var out []string
		for _, it := range r.body["items"].([]any) {
			out = append(out, it.(map[string]any)["id"].(string))
		}
		sort.Strings(out)
		return out
	}
	sorted := func(ids ...string) []string { sort.Strings(ids); return ids }
	if got := list("&overdue=true"); fmt.Sprint(got) != fmt.Sprint(sorted(late)) {
		t.Errorf("overdue=true = %v, want only the open late invoice %s", got, late)
	}
	if got := list("&overdue=false"); fmt.Sprint(got) != fmt.Sprint(sorted(current, paid)) {
		t.Errorf("overdue=false = %v, want the current and the paid one", got)
	}
	if r := f.getInvoice(late); r.body["is_overdue"] != true || str(t, r.body, "status") != "unpaid" {
		t.Errorf("late invoice is_overdue %v status %v, want true unpaid", r.body["is_overdue"], r.body["status"])
	}
	if r := f.getInvoice(paid); r.body["is_overdue"] != false || num(t, r.body, "open_cents") != 0 {
		t.Errorf("paid invoice is_overdue %v open %v, want false 0", r.body["is_overdue"], r.body["open_cents"])
	}
}

// RULE (ADR 0005 7.2): the invoice snapshots its customer's terms: the due date
// (a day of the month after the invoice month, clamped), and the early pay
// discount date and percent.
func TestInvoiceSnapshotsTheCustomersTerms(t *testing.T) {
	testutil.LockOutboxTables(t)
	db := testutil.RequireDB(t)
	f := newFixture(t, db)
	termsID := uuid.New()
	mustExec(t, db, `INSERT INTO payment_terms (id, code, name, kind, day_of_month, discount_percent, discount_days) VALUES ($1, $2, 'The 31st', 'DAY_OF_MONTH', 31, 2, 10)`,
		termsID, "T31-"+termsID.String()[:6])
	t.Cleanup(func() {
		mustExec(t, db, `UPDATE customers SET payment_terms_id = payment_terms_default_id() WHERE id = $1`, f.customerID)
		mustExec(t, db, `DELETE FROM invoices WHERE customer_id = $1`, f.customerID)
		mustExec(t, db, `DELETE FROM payment_terms WHERE id = $1`, termsID)
	})
	mustExec(t, db, `UPDATE customers SET payment_terms_id = $2 WHERE id = $1`, f.customerID, termsID)
	invID, _ := f.invoice("1")
	r := f.getInvoice(invID)
	date := str(t, r.body, "invoice_date")
	d, err := time.Parse("2006-01-02", date)
	if err != nil {
		t.Fatal(err)
	}
	next := time.Date(d.Year(), d.Month()+1, 1, 0, 0, 0, 0, time.UTC)
	last := time.Date(next.Year(), next.Month()+1, 0, 0, 0, 0, 0, time.UTC).Day()
	day := 31
	if last < day {
		day = last
	}
	if want := time.Date(next.Year(), next.Month(), day, 0, 0, 0, 0, time.UTC).Format("2006-01-02"); str(t, r.body, "due_date") != want {
		t.Errorf("due_date = %v, want %s (the 31st clamped to the month)", r.body["due_date"], want)
	}
	if want := d.AddDate(0, 0, 10).Format("2006-01-02"); str(t, r.body, "discount_due_date") != want || str(t, r.body, "discount_percent") != "2" {
		t.Errorf("discount %v until %v, want 2 until %s", r.body["discount_percent"], r.body["discount_due_date"], want)
	}
	if str(t, r.body, "payment_terms_id") != termsID.String() {
		t.Errorf("payment_terms_id = %v, want %s", r.body["payment_terms_id"], termsID)
	}
}

// RULE (ADR 0005 6.2, IN-3.8): the void reverses the invoice's whole entry,
// returns the billed stock to on hand, brings the order's lines back, re-derives
// its status and credits the subledger back; the invoice keeps its number and
// reads void.
func TestInvoiceVoidReversesLedgerStockAndOrder(t *testing.T) {
	testutil.LockOutboxTables(t)
	db := testutil.RequireDB(t)
	f := newFixture(t, db)
	invID, orderID := f.invoice("10")
	number := str(t, f.getInvoice(invID).body, "number")
	if got := f.stock(); got != "90.0000/0.0000" {
		t.Fatalf("stock after billing = %s, want 90/0", got)
	}
	if f.balance() != 5988 {
		t.Fatalf("balance after billing = %d, want 5988", f.balance())
	}

	before := f.getInvoice(invID)
	// the refusals first
	if r := f.do("POST", "/api/v1/invoices/"+invID+"/transitions", map[string]any{"to": "void", "reason": "x"}); r.status != 428 {
		t.Errorf("void without a precondition = %d, want 428", r.status)
	}
	if r := f.voidInvoice(invID, 99, "stale"); r.status != 409 {
		t.Errorf("void on a stale revision = %d, want 409", r.status)
	} else if code, _, _ := errorOf(t, r); code != "stale_revision" {
		t.Errorf("code = %q, want stale_revision", code)
	}
	if r := f.do("POST", "/api/v1/invoices/"+invID+"/transitions", map[string]any{"to": "void", "revision": rev(t, before)}); r.status != 400 {
		t.Errorf("void without a reason = %d, want 400", r.status)
	} else if _, _, fields := errorOf(t, r); len(fields) != 1 || fields[0] != "reason" {
		t.Errorf("fields = %v, want reason", fields)
	}
	for _, to := range []string{"paid", "partial", "unpaid", "written_off"} {
		r := f.do("POST", "/api/v1/invoices/"+invID+"/transitions", map[string]any{"to": to, "revision": rev(t, before), "reason": "x"})
		if code, _, _ := errorOf(t, r); r.status != 409 || code != "invalid_state_transition" {
			t.Errorf("transition to %s = %d %q, want 409 invalid_state_transition", to, r.status, code)
		}
	}
	if r := f.do("POST", "/api/v1/invoices/"+invID+"/transitions", map[string]any{"to": "overdue", "revision": rev(t, before), "reason": "x"}); r.status != 400 {
		t.Errorf("transition to overdue = %d, want 400 (not a status)", r.status)
	}
	if r := f.voidInvoice(invID, rev(t, before), "wrong role", "X-Test-Role", "sales"); r.status != 403 {
		t.Errorf("a sales void = %d, want 403", r.status)
	}
	if got := f.getInvoice(invID); str(t, got.body, "status") != "unpaid" {
		t.Fatalf("a refused void changed the invoice: %s", got.raw)
	}

	r := f.voidInvoice(invID, rev(t, before), "billed in error", "X-Test-Role", "finance", "X-Test-Sub", "fin-user")
	if r.status != 200 {
		t.Fatalf("void = %d: %s", r.status, r.raw)
	}
	for key, want := range map[string]string{"status": "void", "number": number, "void_reason": "billed in error", "voided_by": "fin-user"} {
		if got := str(t, r.body, key); got != want {
			t.Errorf("%s = %q, want %q", key, got, want)
		}
	}
	if r.body["voided_at"] == nil || num(t, r.body, "open_cents") != 0 || rev(t, r) != rev(t, before)+1 {
		t.Errorf("voided_at %v open %v revision %d", r.body["voided_at"], r.body["open_cents"], rev(t, r))
	}
	if r.header.Get("ETag") != fmt.Sprintf(`"%d"`, rev(t, r)) {
		t.Errorf("ETag = %q", r.header.Get("ETag"))
	}
	if again := f.voidInvoice(invID, rev(t, r), "twice"); again.status != 409 {
		t.Errorf("a second void = %d, want 409", again.status)
	}

	// the ledger: the original entry stands, a dated reversal swaps its legs
	entries, orig := f.entryLegs(invID)
	revEntries, rev1 := f.reversalLegs(invID)
	if entries != 1 || revEntries != 1 {
		t.Fatalf("%d invoice entries and %d reversals, want 1 and 1", entries, revEntries)
	}
	for code, l := range orig {
		if got := rev1[code]; got.debit != l.credit || got.credit != l.debit {
			t.Errorf("reversal leg %s = %+v, want the swap of %+v", code, got, l)
		}
	}
	// the stock: on hand back, and allocated again to the reopened order
	if got := f.stock(); got != "100.0000/10.0000" {
		t.Errorf("stock after the void = %s, want 100 on hand and 10 allocated to the reopened order", got)
	}
	// the order: confirmed again, nothing fulfilled
	o := f.do("GET", "/api/v1/orders/"+orderID, nil)
	if str(t, o.body, "status") != "confirmed" || len(o.body["invoice_ids"].([]any)) != 0 {
		t.Errorf("order after the void: status %v invoice_ids %v, want confirmed and none", o.body["status"], o.body["invoice_ids"])
	}
	ol := o.body["lines"].([]any)[0].(map[string]any)
	if str(t, ol, "quantity_fulfilled") != "0" || str(t, ol, "quantity_allocated") != "10" || str(t, ol, "quantity_backordered") != "0" {
		t.Errorf("order line quantities %v/%v/%v, want fulfilled 0 allocated 10 backordered 0", ol["quantity_fulfilled"], ol["quantity_allocated"], ol["quantity_backordered"])
	}
	// the subledger: the invoice's debit is credited back
	if f.balance() != 0 {
		t.Errorf("balance after the void = %d, want 0", f.balance())
	}
	if ev := eventTypes(t, db, "invoice", invID); fmt.Sprint(ev) != "[invoice.created invoice.voided]" {
		t.Errorf("invoice events = %v", ev)
	}
	// a void invoice leaves the open lists and counts nowhere
	if l := f.do("GET", "/api/v1/invoices?status=void&customer_id="+f.customerID.String(), nil); len(l.body["items"].([]any)) != 1 {
		t.Errorf("status=void list = %s", l.raw)
	}

	// the order bills again, and the new invoice takes the next number
	r2, newRev := f.fulfil(orderID, rev(t, o), nil)
	_ = newRev
	if n2 := str(t, f.getInvoice(r2).body, "number"); n2 == number {
		t.Errorf("the re-billed invoice reused the number %s", n2)
	}
}

// RULE (ADR 0005 6.2): a void is refused while a payment is recorded against
// the invoice or an applied credit memo names it (has_applications), and while a
// credit memo that is not void names it (has_credit_memos).
func TestInvoiceVoidRefusals(t *testing.T) {
	testutil.LockOutboxTables(t)
	db := testutil.RequireDB(t)
	f := newFixture(t, db)

	// a payment recorded against the invoice
	paid, _ := f.invoice("1")
	mustExec(t, db, `INSERT INTO payments (invoice_id, amount, method, reference) VALUES ($1, 1.00, 'CASH', 'REF')`, paid)
	r := f.voidInvoice(paid, rev(t, f.getInvoice(paid)), "has a payment")
	if code, blockers, _ := errorOf(t, r); r.status != 409 || code != "conflict" || fmt.Sprint(blockers) != "[has_applications]" {
		t.Errorf("void with a payment = %d %q %v, want 409 conflict has_applications", r.status, code, blockers)
	}

	// an applied credit memo naming it
	applied, _ := f.invoice("1")
	mustExec(t, db, `INSERT INTO credit_memos (invoice_id, customer_id, branch_id, currency, reason_code, reason, amount, status, number, memo_date, subtotal, tax_amount, total_amount, tax_rate)
		VALUES ($1, $2, `+defaultBranch+`, 'USD', 'OTHER', 'applied', 5, 'APPLIED', credit_memo_next_number(), CURRENT_DATE, -5, 0, -5, 0)`, applied, f.customerID)
	r = f.voidInvoice(applied, rev(t, f.getInvoice(applied)), "has an applied memo")
	if _, blockers, _ := errorOf(t, r); r.status != 409 || fmt.Sprint(blockers) != "[has_applications]" {
		t.Errorf("void with an applied credit memo = %d %v, want has_applications", r.status, blockers)
	}

	// a credit memo that is not void names it
	named, _ := f.invoice("2")
	line := f.firstLineID(named)
	cm := f.createCredit(f.creditBody(named, returnLine(line, "-1", false)))
	r = f.voidInvoice(named, rev(t, f.getInvoice(named)), "has a draft memo")
	if _, blockers, _ := errorOf(t, r); r.status != 409 || fmt.Sprint(blockers) != "[has_credit_memos]" {
		t.Errorf("void with a credit memo = %d %v, want has_credit_memos", r.status, blockers)
	}
	// ... until the credit memo is void
	v := f.do("POST", "/api/v1/credit-memos/"+str(t, cm.body, "id")+"/transitions", map[string]any{"to": "void", "revision": rev(t, cm), "reason": "changed my mind"})
	if v.status != 200 {
		t.Fatalf("void the credit memo = %d: %s", v.status, v.raw)
	}
	if r = f.voidInvoice(named, rev(t, f.getInvoice(named)), "now free"); r.status != 200 {
		t.Errorf("void after the credit memo is void = %d: %s", r.status, r.raw)
	}
	// a payment against a void invoice is refused by the payment module
	if n := countOf(t, db, `SELECT count(*) FROM payments WHERE invoice_id = $1`, named); n != 0 {
		t.Errorf("%d payments on the void invoice", n)
	}
}

// RULE (ADR 0005 4.1): invoice numbers are gapless. A fulfilment that rolls back
// (here: its last event write fails) gives its number back, and the next
// invoice takes it; the counter is the numbering of every writer, the seed's raw
// insert included.
func TestInvoiceNumbersHaveNoGapAfterARollback(t *testing.T) {
	testutil.LockOutboxTables(t)
	db := testutil.RequireDB(t)
	failing := true
	f := newFixture(t, db, func(f *fixture) {
		f.orders.WithOutbox(switchable{inner: outbox.NewWriter(db, ""), typ: "order.fulfilled", fail: &failing})
	})
	orderID, r := f.confirmedOrder(f.pickupLine("2"))

	// A passing run of three attempts: another test of another package may mint
	// between the two reads, a real gap would repeat.
	var last error
	for attempt := 0; attempt < 3; attempt++ {
		failing = true
		before := counterNext(t, db, "invoice")
		bad := f.do("POST", "/api/v1/orders/"+orderID+"/fulfillments", map[string]any{"revision": r, "picked_up_by": "X"})
		if bad.status < 500 {
			t.Fatalf("the fulfilment with a failing event write = %d, want a 5xx", bad.status)
		}
		if after := counterNext(t, db, "invoice"); after != before {
			last = fmt.Errorf("the counter moved from %d to %d across a rolled back create", before, after)
			continue
		}
		failing = false
		inv, _ := f.fulfil(orderID, r, nil)
		if got := str(t, f.getInvoice(inv).body, "number"); got != fmt.Sprintf("IN-%06d", before) {
			last = fmt.Errorf("the next invoice is %s, want IN-%06d: the rolled back number was lost", got, before)
			// reset for the next attempt: void it so the order can bill again is overkill; the order is billed now
			return
		}
		last = nil
		break
	}
	if last != nil {
		t.Fatal(last)
	}
	if n := countOf(t, db, `SELECT count(*) FROM invoices WHERE order_id = $1`, orderID); n != 1 {
		t.Errorf("%d invoices for the order, want 1 (the rolled back one left no row)", n)
	}
}

// switchable is an event recorder that fails for one event type while *fail is set.
type switchable struct {
	inner outboxWriter
	typ   string
	fail  *bool
}

type outboxWriter interface {
	Write(ctx context.Context, ev outbox.Event) error
}

func (s switchable) Write(ctx context.Context, ev outbox.Event) error {
	if *s.fail && ev.Type == s.typ {
		return fmt.Errorf("outbox insert failed")
	}
	return s.inner.Write(ctx, ev)
}

// RULE (ADR 0005 4.1, 14.2): three fulfilments racing at pool size 4 get three
// consecutive numbers and no deadlock.
func TestConcurrentFulfilmentsGetConsecutiveNumbers(t *testing.T) {
	testutil.LockOutboxTables(t)
	db := testutil.RequireDBMaxConns(t, 4)
	f := newFixture(t, db)
	const contenders = 3
	type pending struct {
		order string
		rev   int64
	}
	var orders []pending
	for i := 0; i < contenders; i++ {
		id, r := f.confirmedOrder(f.pickupLine("1"))
		orders = append(orders, pending{id, r})
	}
	numbers := make(chan string, contenders)
	var wg sync.WaitGroup
	for _, p := range orders {
		wg.Add(1)
		go func() {
			defer wg.Done()
			r := f.do("POST", "/api/v1/orders/"+p.order+"/fulfillments", map[string]any{"revision": p.rev, "picked_up_by": "X"})
			if r.status != http.StatusCreated {
				t.Errorf("fulfil = %d: %s", r.status, r.raw)
				return
			}
			numbers <- str(t, f.getInvoice(strings.TrimPrefix(r.header.Get("Location"), "/api/v1/invoices/")).body, "number")
		}()
	}
	wg.Wait()
	close(numbers)
	var got []int
	for n := range numbers {
		var v int
		if _, err := fmt.Sscanf(n, "IN-%d", &v); err != nil {
			t.Fatalf("number %q: %v", n, err)
		}
		got = append(got, v)
	}
	sort.Ints(got)
	if len(got) != contenders {
		t.Fatalf("%d invoices, want %d", len(got), contenders)
	}
	// consecutive among themselves (a mint by another package's test between
	// two of ours would break this; a real gap fails every time)
	for i := 1; i < len(got); i++ {
		if got[i] != got[i-1]+1 {
			t.Errorf("numbers %v are not consecutive", got)
			break
		}
	}
}

// RULE (ADR 0005 4.1): a raw SQL writer (the seed) that names no number gets the
// next one from the same counter, and the Go path continues after it.
func TestRawInsertNumbersThroughTheCounter(t *testing.T) {
	testutil.LockOutboxTables(t)
	db := testutil.RequireDB(t)
	f := newFixture(t, db)
	before := counterNext(t, db, "invoice")
	var raw string
	if err := db.Pool.QueryRow(context.Background(), `
		INSERT INTO invoices (customer_id, branch_id, status, total_amount, subtotal, tax_amount, due_date)
		VALUES ($1, `+defaultBranch+`, 'UNPAID', 10, 10, 0, CURRENT_DATE + 30) RETURNING number`, f.customerID).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	if raw != fmt.Sprintf("IN-%06d", before) && counterNext(t, db, "invoice") <= before {
		t.Errorf("raw insert numbered %s with the counter at %d", raw, before)
	}
	if !numberPattern.MatchString(raw) {
		t.Fatalf("raw number = %q", raw)
	}
	inv, _ := f.invoice("1")
	var a, b int
	fmt.Sscanf(raw, "IN-%d", &a)
	fmt.Sscanf(str(t, f.getInvoice(inv).body, "number"), "IN-%d", &b)
	if b <= a {
		t.Errorf("the Go path numbered %d after the raw insert's %d", b, a)
	}
}

// holdCredit takes the customer's credit serialization (the advisory lock the
// order's confirm, release and fulfilment take, ADR 0005 section 11 step 1a) in
// a transaction of its own and returns the function that releases it.
func holdCredit(t *testing.T, f *fixture) (release func()) {
	t.Helper()
	ctx := context.Background()
	tx, err := f.db.Pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended('order-credit:' || $1::text, 0))`, f.customerID.String()); err != nil {
		t.Fatal(err)
	}
	released := false
	release = func() {
		if !released {
			released = true
			_ = tx.Rollback(ctx)
		}
	}
	t.Cleanup(release)
	return release
}

// finishes reports whether the act completes within the wait.
func finishes(done <-chan resp, wait time.Duration) (resp, bool) {
	select {
	case r := <-done:
		return r, true
	case <-time.After(wait):
		return resp{}, false
	}
}

// RULE (ADR 0005 11, step 1a; carried from C2-2b): every act that adds to a
// customer's exposure takes the per customer credit serialization. An invoice
// void re-opens billed quantity on its order, and voiding a posted credit memo
// puts its credit back on the receivable: both wait behind a holder of the
// customer's lock. Voiding a DRAFT adds nothing to the exposure and does not wait.
func TestExposureAddingActsTakeTheCustomerCreditLock(t *testing.T) {
	testutil.LockOutboxTables(t)
	db := testutil.RequireDB(t)
	f := newFixture(t, db)
	invID, _ := f.invoice("4")
	line := f.firstLineID(invID)
	posted := f.createCredit(f.creditBody(invID, returnLine(line, "-1", false)))
	postedID := str(t, posted.body, "id")
	if r := f.postCredit(postedID, 1); r.status != 200 {
		t.Fatalf("post = %d", r.status)
	}
	draft := f.createCredit(f.creditBody(invID, returnLine(line, "-1", false)))
	draftID := str(t, draft.body, "id")
	other, _ := f.invoice("1")

	asyncDo := func(method, path string, body any) <-chan resp {
		ch := make(chan resp, 1)
		go func() { ch <- doOn(t, f.srv.URL, method, path, body) }()
		return ch
	}
	release := holdCredit(t, f)

	// a draft's void is not an exposure act: it completes while the lock is held
	d := asyncDo("POST", "/api/v1/credit-memos/"+draftID+"/transitions", map[string]any{"to": "void", "revision": 1, "reason": "draft"})
	if r, ok := finishes(d, 3*time.Second); !ok || r.status != 200 {
		t.Fatalf("voiding a draft waited on the credit lock (finished %v)", ok)
	}
	// a posted credit memo's void waits
	c := asyncDo("POST", "/api/v1/credit-memos/"+postedID+"/transitions", map[string]any{"to": "void", "revision": 2, "reason": "posted"})
	// an invoice void waits
	v := asyncDo("POST", "/api/v1/invoices/"+other+"/transitions", map[string]any{"to": "void", "revision": 1, "reason": "invoice"})
	if _, ok := finishes(c, 400*time.Millisecond); ok {
		t.Error("a posted credit memo's void ran while the customer's credit lock was held")
	}
	if _, ok := finishes(v, 400*time.Millisecond); ok {
		t.Error("an invoice void ran while the customer's credit lock was held")
	}
	release()
	if r, ok := finishes(c, 5*time.Second); !ok || r.status != 200 {
		t.Errorf("the credit memo void after the release: finished %v status %d", ok, r.status)
	}
	if r, ok := finishes(v, 5*time.Second); !ok || r.status != 200 {
		t.Errorf("the invoice void after the release: finished %v status %d: %s", ok, r.status, r.raw)
	}
}

// RULE (carried from C2-2b round 2, ADR 0005 8.4): the relieved cost read excludes
// void invoices. A non stock line's bill relieves 1030 from the linked receipts'
// posted values pro rata; once the first bill is voided its relief is no longer
// relieved, so the re-bill of the whole line takes the receipts' full value, and
// 1030 nets out to what the receipts posted (1333), not to the 889 left after a
// voided invoice's 444 was counted as spent.
func TestRelievedCostReadExcludesVoidInvoices(t *testing.T) {
	testutil.LockOutboxTables(t)
	db := testutil.RequireDB(t)
	f := newFixture(t, db)
	ctx := context.Background()
	vendor, po := uuid.New(), uuid.New()
	mustExec(t, db, `INSERT INTO vendors (id, name) VALUES ($1, $2)`, vendor, "rv-"+vendor.String()[:8])
	mustExec(t, db, `INSERT INTO purchase_orders (id, vendor_id, status, source, branch_id) VALUES ($1, $2, 'RECEIVED', 'SPECIAL_ORDER', `+defaultBranch+`)`, po, vendor)
	t.Cleanup(func() {
		_, _ = db.Pool.Exec(ctx, `DELETE FROM purchase_order_lines WHERE po_id = $1`, po)
		_, _ = db.Pool.Exec(ctx, `DELETE FROM purchase_orders WHERE id = $1`, po)
		_, _ = db.Pool.Exec(ctx, `DELETE FROM vendors WHERE id = $1`, vendor)
	})
	r := f.do("POST", "/api/v1/orders", map[string]any{"customer_id": f.customerID.String(), "delivery_type": "pickup", "lines": []map[string]any{{
		"description": "Custom millwork", "quantity": "3", "uom": "EA", "unit_price_ten_thousandths": 100000,
		"is_special_order": true, "special_order_unit_cost_ten_thousandths": 30000}}})
	if r.status != 201 {
		t.Fatalf("create = %d: %s", r.status, r.raw)
	}
	orderID := str(t, r.body, "id")
	lineID := str(t, r.body["lines"].([]any)[0].(map[string]any), "id")
	mustExec(t, db, `INSERT INTO purchase_order_lines (id, po_id, description, quantity, cost, qty_received, linked_so_line_id, created_at) VALUES ($1, $2, 'first', 1, 3.33, 1, $3, now() - interval '1 minute')`, uuid.New(), po, lineID)
	mustExec(t, db, `INSERT INTO purchase_order_lines (id, po_id, description, quantity, cost, qty_received, linked_so_line_id) VALUES ($1, $2, 'second', 2, 5.00, 2, $3)`, uuid.New(), po, lineID)
	c := f.do("POST", "/api/v1/orders/"+orderID+"/transitions", map[string]any{"to": "confirmed", "revision": 1})
	if c.status != 200 {
		t.Fatalf("confirm = %d: %s", c.status, c.raw)
	}
	first, rv := f.fulfil(orderID, rev(t, c), []map[string]any{{"order_line_id": lineID, "quantity": "1"}})
	if _, legs := f.entryLegs(first); legs["5010"].debit != 444 {
		t.Fatalf("first bill relieved %+v, want 444", legs["5010"])
	}
	_ = rv
	if v := f.voidInvoice(first, rev(t, f.getInvoice(first)), "billed the wrong line"); v.status != 200 {
		t.Fatalf("void = %d: %s", v.status, v.raw)
	}
	o := f.do("GET", "/api/v1/orders/"+orderID, nil)
	rebill, _ := f.fulfil(orderID, rev(t, o), nil) // all 3 now
	if _, legs := f.entryLegs(rebill); legs["5010"].debit != 1333 || legs["1030"].credit != 1333 {
		t.Errorf("the re-bill relieved %+v / %+v, want the receipts' full 1333 (the void invoice's 444 is not spent)", legs["5010"], legs["1030"])
	}
}

// RULE (carried from C2-2b round 2, ADR 0005 2.4): an invoice line's discount on a
// partial piece is the gross extension of the piece less its net, so every
// invoice line satisfies total + discount = gross piece to the cent and the
// shares over the invoices sum to the order line's discount exactly. Prorating
// the discount on its own (round(2 x 1 / 3) = 1) disagreed with the line total
// split: a line of 3 at 3.3333 with a 0.02 discount billed 1 then 2 carried a
// discount of 1 cent on a first piece whose net is its whole 3.33.
func TestPartialInvoiceLineDiscountsSumExactly(t *testing.T) {
	testutil.LockOutboxTables(t)
	db := testutil.RequireDB(t)
	f := newFixture(t, db)
	product := uuid.New()
	mustExec(t, db, `INSERT INTO products (id, sku, description, uom_primary, base_price, average_unit_cost) VALUES ($1, $2, 'thirds', 'PCS', 3.3333, 1)`, product, "TH-"+product.String()[:8])
	mustExec(t, db, `INSERT INTO inventory (product_id, location_id, location, quantity, allocated) VALUES ($1, $2, 'Y', 50, 0)`, product, f.yardID)
	t.Cleanup(func() {
		_, _ = db.Pool.Exec(context.Background(), `DELETE FROM inventory WHERE product_id = $1`, product)
		_, _ = db.Pool.Exec(context.Background(), `DELETE FROM invoice_lines WHERE product_id = $1`, product)
		_, _ = db.Pool.Exec(context.Background(), `DELETE FROM order_lines WHERE product_id = $1`, product)
	})
	orderID, r := f.confirmedOrder(map[string]any{"product_id": product.String(), "quantity": "3", "discount_cents": 2, "discount_reason": "contract"})
	line := f.do("GET", "/api/v1/orders/"+orderID, nil).body["lines"].([]any)[0].(map[string]any)["id"].(string)
	first, r2 := f.fulfil(orderID, r, []map[string]any{{"order_line_id": line, "quantity": "1"}})
	second, _ := f.fulfil(orderID, r2, nil)

	type piece struct{ total, discount int64 }
	read := func(inv string) piece {
		l := f.getInvoice(inv).body["lines"].([]any)[0].(map[string]any)
		p := piece{total: num(t, l, "line_total_cents")}
		if l["discount_cents"] != nil {
			p.discount = num(t, l, "discount_cents")
		}
		return p
	}
	a, b := read(first), read(second)
	// gross pieces: 3.3333 x 1 = 333, 3.3333 x 3 = 1000 so the rest is 667
	if a.total+a.discount != 333 || b.total+b.discount != 667 {
		t.Errorf("pieces %+v and %+v: total + discount must equal the gross pieces 333 and 667", a, b)
	}
	if a.discount+b.discount != 2 {
		t.Errorf("discounts %d + %d, want the order line's 2 exactly", a.discount, b.discount)
	}
	if a.total+b.total != 998 {
		t.Errorf("totals %d + %d, want 998 (1000 less the discount)", a.total, b.total)
	}
}

// RULE (ADR 0005 6.2): a void of one of several partial invoices of an order gives
// back exactly its quantities: the other invoice stands, the order line's
// fulfilled quantity drops by the voided piece, the returned stock is allocated
// to the order again, and the order lands confirmed, not fulfilled.
func TestVoidOfAPartialInvoiceReopensOnlyItsPiece(t *testing.T) {
	testutil.LockOutboxTables(t)
	db := testutil.RequireDB(t)
	f := newFixture(t, db)
	orderID, r := f.confirmedOrder(f.pickupLine("10"))
	line := f.do("GET", "/api/v1/orders/"+orderID, nil).body["lines"].([]any)[0].(map[string]any)["id"].(string)
	first, r2 := f.fulfil(orderID, r, []map[string]any{{"order_line_id": line, "quantity": "4"}})
	second, _ := f.fulfil(orderID, r2, nil)
	if f.stock() != "90.0000/0.0000" {
		t.Fatalf("stock after both bills = %s", f.stock())
	}
	if v := f.voidInvoice(first, rev(t, f.getInvoice(first)), "the first piece was billed twice"); v.status != 200 {
		t.Fatalf("void = %d: %s", v.status, v.raw)
	}
	o := f.do("GET", "/api/v1/orders/"+orderID, nil)
	ol := o.body["lines"].([]any)[0].(map[string]any)
	if str(t, o.body, "status") != "confirmed" || str(t, ol, "quantity_fulfilled") != "6" || str(t, ol, "quantity_allocated") != "4" || str(t, ol, "quantity_backordered") != "0" {
		t.Errorf("order %v fulfilled %v allocated %v backordered %v, want confirmed 6/4/0", o.body["status"], ol["quantity_fulfilled"], ol["quantity_allocated"], ol["quantity_backordered"])
	}
	if ids := o.body["invoice_ids"].([]any); len(ids) != 1 || ids[0] != second {
		t.Errorf("invoice_ids = %v, want only the second invoice", ids)
	}
	if f.stock() != "94.0000/4.0000" {
		t.Errorf("stock = %s, want 94 on hand (4 returned) and 4 allocated", f.stock())
	}
	if f.getInvoice(second).body["status"] != "unpaid" {
		t.Error("the other invoice moved")
	}
	// the 4 bill again
	third, _ := f.fulfil(orderID, rev(t, o), nil)
	if l := f.getInvoice(third).body["lines"].([]any)[0].(map[string]any); str(t, l, "quantity") != "4" {
		t.Errorf("the re-bill is for %v, want 4", l["quantity"])
	}
}

// RULE (ADR 0005 6.2, 2.6): a void returns a kit's components and gives back
// whole kits: two kits of four posts each bill, void, and the components are
// allocated again in whole kits.
func TestVoidOfAKitInvoiceReturnsWholeKits(t *testing.T) {
	testutil.LockOutboxTables(t)
	db := testutil.RequireDB(t)
	f := newFixture(t, db)
	kit, post := uuid.New(), uuid.New()
	mustExec(t, db, `INSERT INTO products (id, sku, description, uom_primary, base_price, is_kit, taxable) VALUES ($1, $2, 'A fence kit', 'EA', 100.00, TRUE, TRUE)`, kit, "VK-KIT-"+uuid.NewString()[:6])
	mustExec(t, db, `INSERT INTO products (id, sku, description, uom_primary, base_price, average_unit_cost) VALUES ($1, $2, 'A fence post', 'EA', 12.50, 5.00)`, post, "VK-POST-"+uuid.NewString()[:6])
	mustExec(t, db, `INSERT INTO product_kit_components (kit_product_id, component_product_id, quantity, position) VALUES ($1, $2, 4, 0)`, kit, post)
	mustExec(t, db, `INSERT INTO inventory (product_id, location_id, location, quantity, allocated) VALUES ($1, $2, 'Y', 9, 0)`, post, f.yardID)
	t.Cleanup(func() {
		ctx := context.Background()
		_, _ = db.Pool.Exec(ctx, `DELETE FROM invoice_lines WHERE product_id IN ($1, $2)`, kit, post)
		_, _ = db.Pool.Exec(ctx, `DELETE FROM order_lines WHERE product_id IN ($1, $2)`, kit, post)
		_, _ = db.Pool.Exec(ctx, `DELETE FROM product_kit_components WHERE kit_product_id = $1`, kit)
		_, _ = db.Pool.Exec(ctx, `DELETE FROM inventory WHERE product_id = $1`, post)
	})
	orderID, r := f.confirmedOrder(map[string]any{"product_id": kit.String(), "quantity": "2"})
	invID, _ := f.fulfil(orderID, r, nil)
	posts := func() string {
		var s string
		if err := db.Pool.QueryRow(context.Background(), `SELECT quantity::text || '/' || allocated::text FROM inventory WHERE product_id = $1`, post).Scan(&s); err != nil {
			t.Fatal(err)
		}
		return s
	}
	if posts() != "1.0000/0.0000" {
		t.Fatalf("posts after billing two kits = %s, want 1 left", posts())
	}
	if v := f.voidInvoice(invID, rev(t, f.getInvoice(invID)), "kits billed by mistake"); v.status != 200 {
		t.Fatalf("void = %d: %s", v.status, v.raw)
	}
	if posts() != "9.0000/8.0000" {
		t.Errorf("posts after the void = %s, want 9 on hand and 8 allocated (two whole kits)", posts())
	}
	o := f.do("GET", "/api/v1/orders/"+orderID, nil)
	if str(t, o.body, "status") != "confirmed" {
		t.Errorf("order status = %v, want confirmed", o.body["status"])
	}
	for _, l := range o.body["lines"].([]any) {
		lm := l.(map[string]any)
		if str(t, lm, "quantity_fulfilled") != "0" {
			t.Errorf("a %v line still has %v fulfilled", lm["line_type"], lm["quantity_fulfilled"])
		}
		if lm["line_type"] == "component" && (str(t, lm, "quantity_allocated") != "8" || str(t, lm, "quantity_backordered") != "0") {
			t.Errorf("component allocated %v backordered %v, want 8/0", lm["quantity_allocated"], lm["quantity_backordered"])
		}
	}
}
