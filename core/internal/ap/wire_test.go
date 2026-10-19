// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package ap_test

// The vendor invoice wire tests (the module recipe's set) and the three live
// failures of ADR 0008 7.4 in their converted form: the approve entry carries
// a debit leg per line, a posting failure fails the act with its cause, and a
// payment applies only to an approved or partial bill. The GL ties after
// every act: each entry balances, and the AP control account (2010) equals
// what the open bills owe.

import (
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"regexp"
	"strings"
	"testing"

	"github.com/gablelbm/gable/internal/testutil"
	"github.com/google/uuid"
)

var numberPattern = regexp.MustCompile(`^AP-\d{6,}$`)

// RULE (ADR 0008 7.4, ADR 0001 sections 7 and 8): a bill read carries
// Gable's own number, the vendor's as vendor_invoice_number, a lowercase
// status, money in cents, the scaled unit price, the revision with its ETag,
// and every line in the shared shape; the optional fields are present as
// null, and no legacy field survives.
func TestWire_InvoiceShape(t *testing.T) {
	testutil.LockOutboxTables(t)
	f := newFixture(t, testutil.RequireDB(t))
	r := f.do("POST", "/api/v1/ap/invoices", map[string]any{
		"vendor_id": f.vendor.String(), "vendor_invoice_number": "V-2026-001",
		"invoice_date": "2026-01-10", "due_date": "2026-02-10", "tax_cents": 488,
		"notes": "fixture bill", "lines": []map[string]any{
			{"description": "2x4x8 SPF", "quantity": "2", "unit_price_ten_thousandths": 550000, "gl_account_id": f.expense.String()},
			{"description": "delivery", "quantity": "1", "unit_price_ten_thousandths": 10000, "gl_account_id": f.expense.String()},
		},
	})
	if r.status != http.StatusCreated {
		t.Fatalf("create = %d: %s", r.status, r.raw)
	}
	b := r.body
	if !numberPattern.MatchString(str(t, b, "number")) {
		t.Errorf("number = %q, want AP-nnnnnn", b["number"])
	}
	for key, want := range map[string]string{"vendor_invoice_number": "V-2026-001", "status": "pending", "currency": "USD",
		"invoice_date": "2026-01-10", "due_date": "2026-02-10", "notes": "fixture bill", "vendor_name": "C41B Vendor " + f.vendor.String()[:8]} {
		if got := str(t, b, key); got != want {
			t.Errorf("%s = %q, want %q", key, got, want)
		}
	}
	for key, want := range map[string]int64{"subtotal_cents": 11100, "tax_cents": 488, "total_cents": 11588,
		"amount_paid_cents": 0, "amount_open_cents": 11588, "revision": 1} {
		if got := num(t, b, key); got != want {
			t.Errorf("%s = %d, want %d", key, got, want)
		}
	}
	if etag := r.header.Get("ETag"); etag != `"1"` {
		t.Errorf("ETag = %q, want \"1\"", etag)
	}
	if loc := r.header.Get("Location"); loc != "/api/v1/ap/invoices/"+str(t, b, "id") {
		t.Errorf("Location = %q", loc)
	}
	if b["branch_id"] != f.branch.String() {
		t.Errorf("branch_id = %v, want the default branch (no branch in the body, no context branch)", b["branch_id"])
	}
	for _, key := range []string{"po_id", "approved_by", "approved_at", "gl_entry_id"} {
		if v, ok := b[key]; !ok || v != nil {
			t.Errorf("%s = %v (present %v), want present as null", key, v, ok)
		}
	}
	for _, key := range []string{"invoice_number", "subtotal", "tax_amount", "total", "amount_paid", "unit_price", "line_total"} {
		if _, ok := b[key]; ok {
			t.Errorf("legacy field %q is still on the wire", key)
		}
	}
	lines := b["lines"].([]any)
	if len(lines) != 2 {
		t.Fatalf("%d lines, want 2: %s", len(lines), r.raw)
	}
	l := lines[0].(map[string]any)
	for key, want := range map[string]string{"quantity": "2", "description": "2x4x8 SPF"} {
		if got := str(t, l, key); got != want {
			t.Errorf("line %s = %q, want %q", key, got, want)
		}
	}
	for key, want := range map[string]int64{"unit_price_ten_thousandths": 550000, "line_total_cents": 11000, "position": 0} {
		if got := num(t, l, key); got != want {
			t.Errorf("line %s = %d, want %d", key, got, want)
		}
	}
	if l["gl_account_id"] != f.expense.String() {
		t.Errorf("line gl_account_id = %v", l["gl_account_id"])
	}
	for _, key := range []string{"purchase_order_line_id", "product_id", "po_freight_charge_id"} {
		if v, ok := l[key]; !ok || v != nil {
			t.Errorf("line %s = %v (present %v), want present as null", key, v, ok)
		}
	}
	if ev := eventTypes(t, f.db, str(t, b, "id")); fmt.Sprint(ev) != "[vendor_invoice.created]" {
		t.Errorf("events = %v, want [vendor_invoice.created]", ev)
	}
	if n := f.count(`SELECT count(*) FROM audit_log WHERE entity_type = 'vendor_invoice' AND entity_id = $1 AND action = 'vendor_invoice.created'`, b["id"]); n != 1 {
		t.Errorf("%d create audit rows, want 1", n)
	}
	// The number is Gable's own and unique: a second bill may carry the same
	// vendor number? No: the vendor's number is unique per vendor.
	if bad := f.do("POST", "/api/v1/ap/invoices", f.createBody("V-2026-001", "1", 1000)); bad.status != http.StatusConflict {
		t.Errorf("duplicate vendor number = %d, want 409", bad.status)
	}
}

// RULE (ADR 0001 section 4): one 400 with every offending field in details,
// the full path included; a body the route cannot consume is bad_request; an
// unknown field is refused; a create has no status.
func TestWire_CreateValidation(t *testing.T) {
	testutil.LockOutboxTables(t)
	f := newFixture(t, testutil.RequireDB(t))
	r := f.do("POST", "/api/v1/ap/invoices", map[string]any{
		"vendor_id": f.vendor.String(),
		"lines": []map[string]any{
			{"description": "", "quantity": "0", "unit_price_ten_thousandths": -5},
			{"description": "ok", "quantity": "1", "unit_price_ten_thousandths": 100},
		},
	})
	if r.status != http.StatusBadRequest {
		t.Fatalf("validation = %d: %s", r.status, r.raw)
	}
	code, _, fields := errorOf(t, r)
	if code != "validation_failed" {
		t.Errorf("code = %q, want validation_failed", code)
	}
	want := []string{"vendor_invoice_number", "invoice_date", "due_date",
		"lines[0].description", "lines[0].quantity", "lines[0].unit_price_ten_thousandths", "lines[0].gl_account_id",
		"lines[1].gl_account_id"}
	if fmt.Sprint(fields) != fmt.Sprint(want) {
		t.Errorf("fields = %v, want %v", fields, want)
	}
	bad := f.do("POST", "/api/v1/ap/invoices", "not-an-object")
	if bad.status != http.StatusBadRequest {
		t.Errorf("bad body = %d, want 400", bad.status)
	}
	if code, _, _ := errorOf(t, bad); code != "bad_request" {
		t.Errorf("bad body code = %q, want bad_request", code)
	}
	if unknown := f.do("POST", "/api/v1/ap/invoices", map[string]any{"vendor_id": f.vendor.String(), "nope": 1}); unknown.status != http.StatusBadRequest {
		t.Errorf("unknown field = %d, want 400", unknown.status)
	}
	nv := f.do("POST", "/api/v1/ap/invoices", map[string]any{
		"vendor_id": uuid.NewString(), "vendor_invoice_number": "V-X", "invoice_date": "2026-01-10", "due_date": "2026-02-10",
		"lines": []map[string]any{{"description": "x", "quantity": "1", "unit_price_ten_thousandths": 1, "gl_account_id": f.expense.String()}}})
	if nv.status != http.StatusBadRequest {
		t.Errorf("unknown vendor = %d, want 400 naming vendor_id", nv.status)
	}
	if n := f.count(`SELECT count(*) FROM vendor_invoices WHERE vendor_id = $1`, f.vendor); n != 0 {
		t.Errorf("%d bills survived a refused create", n)
	}
}

// A bill's money is bounded by its columns: NUMERIC(12,2) for the line
// total, the tax and the total, NUMERIC(12,4) for the unit price. Past a
// bound the create is a 400 naming the field, never a 500 numeric overflow,
// and nothing is written; at the bound it is created (reviews r5 P3-1 and r6
// P3-2).
func TestWire_CreateMoneyCeilings(t *testing.T) {
	testutil.LockOutboxTables(t)
	f := newFixture(t, testutil.RequireDB(t))
	const maxCents, maxPrice = int64(999_999_999_999), int64(999_999_999_999)
	body := func(number string, tax int64, lines ...map[string]any) map[string]any {
		b := map[string]any{"vendor_id": f.vendor.String(), "vendor_invoice_number": number,
			"invoice_date": "2026-01-10", "due_date": "2026-02-10", "tax_cents": tax, "lines": lines}
		return b
	}
	for _, c := range []struct {
		name  string
		body  map[string]any
		field string
	}{
		{"tax past the column", body("V-CAP-1", maxCents+1, f.lineBody("1", 100)), "tax_cents"},
		{"tax at int64 max", body("V-CAP-2", 9223372036854775807, f.lineBody("1", 100)), "tax_cents"},
		{"unit price past the column", body("V-CAP-3", 0, f.lineBody("1", maxPrice+1)), "lines[0].unit_price_ten_thousandths"},
		{"line total past the column", body("V-CAP-4", 0, f.lineBody("100000", 100_000_000_000)), "lines[0].quantity"},
		// 99 units at the largest price is 990000000000 cents: one line fits,
		// two do not, and nor does one with 100000000000 cents of tax.
		{"subtotal past the column", body("V-CAP-5", 0, f.lineBody("99", maxPrice), f.lineBody("99", maxPrice)), "lines"},
		{"total past the column", body("V-CAP-6", 100_000_000_000, f.lineBody("99", maxPrice)), "tax_cents"},
	} {
		r := f.do("POST", "/api/v1/ap/invoices", c.body)
		if r.status != http.StatusBadRequest {
			t.Errorf("%s: create = %d, want 400: %s", c.name, r.status, r.raw)
			continue
		}
		if _, _, fields := errorOf(t, r); fmt.Sprint(fields) != fmt.Sprint([]string{c.field}) {
			t.Errorf("%s: fields = %v, want [%s]", c.name, fields, c.field)
		}
	}
	if n := f.count(`SELECT count(*) FROM vendor_invoices WHERE vendor_id = $1`, f.vendor); n != 0 {
		t.Errorf("%d bills survived a refused create", n)
	}
	// Under the bound: the largest price, and a line, tax and total close to
	// the column's limit, are created.
	r := f.do("POST", "/api/v1/ap/invoices", body("V-CAP-OK", 9_999_999_999, f.lineBody("99", maxPrice)))
	if r.status != http.StatusCreated {
		t.Fatalf("create at the bound = %d, want 201: %s", r.status, r.raw)
	}
	if got, want := f.controlBalance(), f.openSum(); got != want {
		t.Errorf("after the create: control %d, open %d", got, want)
	}
}

// RULE (ADR 0001 sections 1, 2 and 5): the list is the cursor envelope,
// newest first, every row once, with filters that filter and unknown or
// uppercase values refused; an empty page is [] in the bytes.
func TestWire_List(t *testing.T) {
	testutil.LockOutboxTables(t)
	f := newFixture(t, testutil.RequireDB(t))
	var ids []string
	for i := 0; i < 3; i++ {
		id, _ := f.enter(fmt.Sprintf("V-L%d", i), "1", 1000)
		ids = append(ids, id)
	}
	var seen []string
	cursor := ""
	for page := 0; page < 5; page++ {
		path := "/api/v1/ap/invoices?limit=2&vendor_id=" + f.vendor.String()
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
		if num(t, r.body, "limit") != 2 {
			t.Errorf("limit echo = %v, want 2", r.body["limit"])
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
	if r := f.do("GET", "/api/v1/ap/invoices?vendor_id="+f.vendor.String()+"&include=total", nil); r.status != 200 || num(t, r.body, "total") != 3 {
		t.Errorf("include=total = %d %v, want 3", r.status, r.body["total"])
	}
	if r := f.do("GET", "/api/v1/ap/invoices?vendor_id="+f.vendor.String()+"&status=pending", nil); r.status != 200 || len(r.body["items"].([]any)) != 3 {
		t.Errorf("status=pending = %d, want the 3 pending", r.status)
	}
	if r := f.do("GET", "/api/v1/ap/invoices?vendor_id="+f.vendor.String()+"&status=approved,partial", nil); r.status != 200 || len(r.body["items"].([]any)) != 0 {
		t.Errorf("status=approved,partial = %d %s, want none", r.status, r.raw)
	}
	if r := f.do("GET", "/api/v1/ap/invoices?vendor_id="+f.vendor.String()+"&po_id="+uuid.NewString(), nil); r.status != 200 || len(r.body["items"].([]any)) != 0 {
		t.Errorf("a po_id filter that matches nothing: %d %s", r.status, r.raw)
	}
	empty := f.do("GET", "/api/v1/ap/invoices?vendor_id="+uuid.NewString(), nil)
	if !strings.Contains(string(empty.raw), `"items":[]`) {
		t.Errorf("an empty page = %s, want items [] in the bytes", empty.raw)
	}
	for _, bad := range []string{"offset=1", "status=PENDING", "status=unknown", "vendor_id=zzz", "limit=0", "limit=201", "cursor=junk", "nope=1"} {
		if r := f.do("GET", "/api/v1/ap/invoices?"+bad, nil); r.status != 400 {
			t.Errorf("?%s = %d, want 400", bad, r.status)
		}
	}
}

// RULE (ADR 0001 section 3): not found and a malformed id are the wire's
// envelope.
func TestWire_GetRefusals(t *testing.T) {
	testutil.LockOutboxTables(t)
	f := newFixture(t, testutil.RequireDB(t))
	r := f.do("GET", "/api/v1/ap/invoices/"+uuid.NewString(), nil)
	if r.status != 404 {
		t.Fatalf("unknown bill = %d, want 404", r.status)
	}
	if code, _, _ := errorOf(t, r); code != "not_found" {
		t.Errorf("code = %q, want not_found", code)
	}
	r = f.do("GET", "/api/v1/ap/invoices/not-a-uuid", nil)
	if code, _, fields := errorOf(t, r); r.status != 400 || code != "bad_request" || len(fields) != 1 || fields[0] != "id" {
		t.Errorf("malformed id = %d %q %v, want 400 bad_request naming id", r.status, code, fields)
	}
}

// RULE (ADR 0001 section 11): a write needs the revision, through If-Match or
// the body; a mismatch is 409 stale_revision; the new revision and its ETag
// come back on success.
func TestWire_Revision(t *testing.T) {
	testutil.LockOutboxTables(t)
	f := newFixture(t, testutil.RequireDB(t))
	id, _ := f.enter("V-R", "1", 1000)
	body := func() map[string]any {
		return map[string]any{"to": "approved"}
	}
	if r := f.do("POST", "/api/v1/ap/invoices/"+id+"/transitions", body()); r.status != http.StatusPreconditionRequired {
		t.Errorf("no precondition = %d, want 428", r.status)
	}
	r := f.do("POST", "/api/v1/ap/invoices/"+id+"/transitions", map[string]any{"to": "approved", "revision": 5})
	if r.status != http.StatusConflict {
		t.Errorf("stale revision = %d, want 409", r.status)
	}
	if code, _, _ := errorOf(t, r); code != "stale_revision" {
		t.Errorf("stale code = %q", code)
	}
	if r := f.do("POST", "/api/v1/ap/invoices/"+id+"/transitions", map[string]any{"to": "approved", "revision": 1}, "If-Match", `W/"1"`); r.status != http.StatusOK {
		t.Errorf("weak If-Match = %d, want 200", r.status)
	}
	if r := f.do("POST", "/api/v1/ap/invoices/"+id+"/transitions", map[string]any{"to": "voided", "revision": 2, "reason": "wrong bill"}, "If-Match", `"3"`); r.status != http.StatusBadRequest {
		t.Errorf("header and body disagree = %d, want 400", r.status)
	}
}

// RULE (ADR 0008 7.4 and section 10): the lifecycle. Approve posts the entry;
// void reverses it; the events land in order; the edges the lifecycle refuses
// answer 409 invalid_state_transition.
func TestWire_Lifecycle(t *testing.T) {
	testutil.LockOutboxTables(t)
	f := newFixture(t, testutil.RequireDB(t))
	id, r1 := f.enter("V-LC", "2", 500000)

	r := f.approve(id, r1)
	if r.status != http.StatusOK || str(t, r.body, "status") != "approved" {
		t.Fatalf("approve = %d %v: %s", r.status, r.body["status"], r.raw)
	}
	if rev(t, r) != 2 || r.header.Get("ETag") != `"2"` {
		t.Errorf("approve revision = %d %q, want 2 \"2\"", rev(t, r), r.header.Get("ETag"))
	}
	if str(t, r.body, "approved_by") == "" || r.body["approved_at"] == nil {
		t.Errorf("approved_by %v approved_at %v, want set", r.body["approved_by"], r.body["approved_at"])
	}
	// A second approve is refused.
	again := f.approve(id, 2)
	if again.status != http.StatusConflict {
		t.Errorf("second approve = %d, want 409", again.status)
	}
	if code, blockers, _ := errorOf(t, again); code != "invalid_state_transition" || len(blockers) == 0 {
		t.Errorf("second approve = %q %v", code, blockers)
	}
	// A bill with money applied cannot be voided.
	if p := f.pay(10000, id); p.status != http.StatusCreated {
		t.Fatalf("pay = %d: %s", p.status, p.raw)
	}
	v := f.void(id, 3, "wrong bill")
	if v.status != http.StatusConflict {
		t.Fatalf("void with payments = %d, want 409", v.status)
	}
	if code, blockers, _ := errorOf(t, v); code != "invalid_state_transition" || blockers[0] != "has_payments" {
		t.Errorf("void with payments = %q %v, want has_payments", code, blockers)
	}
	// A pending bill voids with no entry to reverse.
	id2, r2 := f.enter("V-LC2", "1", 1000)
	vp := f.void(id2, r2, "entered twice")
	if vp.status != http.StatusOK || str(t, vp.body, "status") != "voided" {
		t.Fatalf("void pending = %d: %s", vp.status, vp.raw)
	}
	if num(t, vp.body, "amount_open_cents") != 0 {
		t.Errorf("a voided bill owes %d, want 0", num(t, vp.body, "amount_open_cents"))
	}
	if n := f.count(`SELECT count(*) FROM gl_journal_entries WHERE source_ref_id = $1`, id2); n != 0 {
		t.Errorf("a pending bill voided with %d entries, want none", n)
	}
	// An approved bill voids by reversing its entry.
	id3, r3 := f.enter("V-LC3", "1", 1000)
	if a := f.approve(id3, r3); a.status != http.StatusOK {
		t.Fatalf("approve = %s", a.raw)
	}
	va := f.void(id3, 2, "wrong vendor")
	if va.status != http.StatusOK || str(t, va.body, "status") != "voided" {
		t.Fatalf("void approved = %d: %s", va.status, va.raw)
	}
	if n := f.count(`SELECT count(*) FROM gl_journal_entries r JOIN gl_journal_entries o ON o.id = r.reverses_entry_id WHERE o.source_ref_id = $1`, id3); n != 1 {
		t.Errorf("the void wrote %d reversals, want 1", n)
	}
	if ev := eventTypes(t, f.db, id3); fmt.Sprint(ev) != "[vendor_invoice.created vendor_invoice.approved vendor_invoice.voided]" {
		t.Errorf("events = %v", ev)
	}
	// The targets a bill never reaches by a transition.
	for _, to := range []string{"pending", "partial", "paid"} {
		if tr := f.do("POST", "/api/v1/ap/invoices/"+id2+"/transitions", map[string]any{"to": to, "revision": 2, "reason": "x"}); tr.status != http.StatusConflict {
			t.Errorf("transition to %s = %d, want 409", to, tr.status)
		}
	}
	if nr := f.do("POST", "/api/v1/ap/invoices/"+id2+"/transitions", map[string]any{"to": "voided", "revision": 2}); nr.status != http.StatusBadRequest {
		t.Errorf("void without a reason = %d, want 400", nr.status)
	}
}

// LIVE FAILURE (ADR 0008 7.4, section 14), converted form: approve loads the
// bill's lines, so the entry carries a debit leg per line beside the Accounts
// Payable credit, and it balances.
func TestWire_ApproveEntryCarriesLineDebits(t *testing.T) {
	testutil.LockOutboxTables(t)
	f := newFixture(t, testutil.RequireDB(t))
	id, r1 := f.enter("V-GL1", "2", 500000) // 2 at 50.00 = 100.00
	if a := f.approve(id, r1); a.status != http.StatusOK {
		t.Fatalf("approve = %s", a.raw)
	}
	entries, legs := f.entryLegs(id)
	if entries != 1 {
		t.Fatalf("%d entries, want 1", entries)
	}
	if len(legs) != 2 {
		t.Fatalf("the entry's accounts = %v, want 5020 (the line) and 2010 (the payable)", legs)
	}
	if legs["5020"].debit != 10000 || legs["5020"].credit != 0 {
		t.Errorf("5020 = %+v, want debit 10000", legs["5020"])
	}
	if legs["2010"].debit != 0 || legs["2010"].credit != 10000 {
		t.Errorf("2010 = %+v, want credit 10000", legs["2010"])
	}
	if !f.balanced() {
		t.Error("the approve entry does not balance")
	}
}

// A bill with tax approves balanced: the tax spreads across the line accounts
// pro rata to their extensions, the last line taking the remainder.
func TestWire_ApproveSpreadsTax(t *testing.T) {
	testutil.LockOutboxTables(t)
	f := newFixture(t, testutil.RequireDB(t))
	r := f.do("POST", "/api/v1/ap/invoices", map[string]any{
		"vendor_id": f.vendor.String(), "vendor_invoice_number": "V-TAX",
		"invoice_date": "2026-01-10", "due_date": "2026-02-10", "tax_cents": 101,
		"lines": []map[string]any{
			{"description": "two thirds", "quantity": "2", "unit_price_ten_thousandths": 500000, "gl_account_id": f.expense.String()},
			{"description": "one third", "quantity": "1", "unit_price_ten_thousandths": 500000, "gl_account_id": f.expense.String()},
		},
	})
	if r.status != http.StatusCreated {
		t.Fatalf("create = %d: %s", r.status, r.raw)
	}
	id := str(t, r.body, "id")
	if num(t, r.body, "subtotal_cents") != 15000 || num(t, r.body, "total_cents") != 15101 {
		t.Fatalf("subtotal %v total %v", r.body["subtotal_cents"], r.body["total_cents"])
	}
	if a := f.approve(id, 1); a.status != http.StatusOK {
		t.Fatalf("approve = %s", a.raw)
	}
	_, legs := f.entryLegs(id)
	if legs["5020"].debit != 15101 {
		t.Errorf("5020 debit = %d, want 15101 (15000 lines plus the 101 tax)", legs["5020"].debit)
	}
	if legs["2010"].credit != 15101 {
		t.Errorf("2010 credit = %d, want 15101", legs["2010"].credit)
	}
	if !f.balanced() {
		t.Error("the taxed approve entry does not balance")
	}
}

// A line with no account cannot be approved (a legacy bill): the approve
// refuses naming the line, and nothing posts.
func TestWire_ApproveRefusesLineWithoutAccount(t *testing.T) {
	testutil.LockOutboxTables(t)
	f := newFixture(t, testutil.RequireDB(t))
	id, _ := f.enter("V-NOACC", "1", 1000)
	mustExec(t, f.db, `UPDATE vendor_invoice_lines SET gl_account_id = NULL WHERE invoice_id = $1`, id)
	r := f.approve(id, 1)
	if r.status != http.StatusConflict {
		t.Fatalf("approve = %d, want 409", r.status)
	}
	if code, blockers, _ := errorOf(t, r); code != "conflict" || blockers[0] != "line_needs_account" {
		t.Errorf("approve = %q %v, want line_needs_account", code, blockers)
	}
	if n := f.count(`SELECT count(*) FROM gl_journal_entries WHERE source_ref_id = $1`, id); n != 0 {
		t.Errorf("%d entries posted for a refused approve", n)
	}
	if s := f.count(`SELECT count(*) FROM vendor_invoices WHERE id = $1 AND status = 'PENDING'`, id); s != 1 {
		t.Error("the bill did not stay pending")
	}
}

// LIVE FAILURE (ADR 0008 7.4, section 14), converted form: a posting failure
// fails the act with its cause. The period holding the bill's date is closed,
// so the journal insert refuses and the approve answers 409 period_closed,
// leaving the bill pending and the ledger untouched.
func TestWire_ApprovePostingFailureFailsTheAct(t *testing.T) {
	testutil.LockOutboxTables(t)
	f := newFixture(t, testutil.RequireDB(t))
	id, r1 := f.enter("V-CLOSED", "1", 1000)
	mustExec(t, f.db, `UPDATE gl_fiscal_periods SET status = 'CLOSED'
		WHERE start_date <= '2026-01-10' AND end_date >= '2026-01-10'`)
	t.Cleanup(func() {
		mustExec(t, f.db, `UPDATE gl_fiscal_periods SET status = 'OPEN'
			WHERE start_date <= '2026-01-10' AND end_date >= '2026-01-10'`)
	})
	closed := f.approve(id, r1)
	if closed.status != http.StatusConflict {
		t.Fatalf("approve into a closed period = %d, want 409: %s", closed.status, closed.raw)
	}
	if code, blockers, _ := errorOf(t, closed); code != "conflict" || blockers[0] != "period_closed" {
		t.Errorf("approve = %q %v, want period_closed", code, blockers)
	}
	if s := f.count(`SELECT count(*) FROM vendor_invoices WHERE id = $1 AND status = 'PENDING' AND revision = 1`, id); s != 1 {
		t.Error("the bill did not stay pending at revision 1")
	}
	if n := f.count(`SELECT count(*) FROM gl_journal_entries WHERE source_ref_id = $1`, id); n != 0 {
		t.Errorf("%d entries posted into a closed period", n)
	}
}

// LIVE FAILURE (ADR 0008 7.4, section 14), converted form: a payment applies
// only to an approved or partial bill. A pending bill is refused with the
// invoice_not_approved blocker and nothing is written.
func TestWire_PaymentRefusesUnapprovedInvoice(t *testing.T) {
	testutil.LockOutboxTables(t)
	f := newFixture(t, testutil.RequireDB(t))
	id, _ := f.enter("V-PENDING", "1", 1000)
	pr := f.pay(1000, id)
	if pr.status != http.StatusConflict {
		t.Fatalf("pay pending = %d, want 409: %s", pr.status, pr.raw)
	}
	if code, blockers, _ := errorOf(t, pr); code != "invalid_state_transition" || blockers[0] != "invoice_not_approved" {
		t.Errorf("pay pending = %q %v, want invoice_not_approved", code, blockers)
	}
	if n := f.count(`SELECT count(*) FROM ap_payments WHERE vendor_id = $1`, f.vendor); n != 0 {
		t.Errorf("%d payments survived the refusal", n)
	}
	if n := f.count(`SELECT count(*) FROM ap_payment_applications a JOIN vendor_invoices i ON i.id = a.invoice_id WHERE i.vendor_id = $1`, f.vendor); n != 0 {
		t.Errorf("%d applications were written against the pending bill", n)
	}
}

// RULE (ADR 0008 7.4): a payment applies to approved and partial bills, in id
// order, and the GL ties after every act: each entry balances and the AP
// control account equals what the open bills owe.
func TestWire_PaymentAppliesAndTies(t *testing.T) {
	testutil.LockOutboxTables(t)
	f := newFixture(t, testutil.RequireDB(t))
	a, ra := f.enter("V-PAY-A", "1", 500000) // 50.00
	b, rb := f.enter("V-PAY-B", "1", 300000) // 30.00
	if aa := f.approve(a, ra); aa.status != http.StatusOK {
		t.Fatal(aa.raw)
	}
	if ab := f.approve(b, rb); ab.status != http.StatusOK {
		t.Fatal(ab.raw)
	}
	if got, want := f.controlBalance(), f.openSum(); got != want {
		t.Fatalf("after the approves: control %d, open %d", got, want)
	}
	// A partial payment of the first bill.
	if pp := f.pay(2000, a); pp.status != http.StatusCreated {
		t.Fatalf("partial pay = %d: %s", pp.status, pp.raw)
	}
	if g := f.do("GET", "/api/v1/ap/invoices/"+a, nil); g.status != 200 || str(t, g.body, "status") != "partial" ||
		num(t, g.body, "amount_paid_cents") != 2000 || num(t, g.body, "amount_open_cents") != 3000 {
		t.Fatalf("after the partial: %s", g.raw)
	}
	if ev := eventTypes(t, f.db, a); fmt.Sprint(ev) != "[vendor_invoice.created vendor_invoice.approved vendor_invoice.partial]" {
		t.Errorf("events = %v", ev)
	}
	if got, want := f.controlBalance(), f.openSum(); got != want {
		t.Fatalf("after the partial payment: control %d, open %d", got, want)
	}
	// A payment naming both bills, more than the first still owes: the rest
	// lands on the second (3000 closes A, 2000 of B's 3000).
	if pb := f.pay(5000, a, b); pb.status != http.StatusCreated {
		t.Fatalf("pay both = %d: %s", pb.status, pb.raw)
	}
	if g := f.do("GET", "/api/v1/ap/invoices/"+a, nil); str(t, g.body, "status") != "paid" {
		t.Errorf("bill A = %s, want paid", g.raw)
	}
	if g := f.do("GET", "/api/v1/ap/invoices/"+b, nil); str(t, g.body, "status") != "partial" || num(t, g.body, "amount_open_cents") != 1000 {
		t.Errorf("bill B = %s, want partial owing 1000", g.raw)
	}
	if ev := eventTypes(t, f.db, a); fmt.Sprint(ev) != "[vendor_invoice.created vendor_invoice.approved vendor_invoice.partial vendor_invoice.paid]" {
		t.Errorf("events = %v", ev)
	}
	if !f.balanced() {
		t.Error("a payment entry does not balance")
	}
	if got, want := f.controlBalance(), f.openSum(); got != want {
		t.Fatalf("after both payments: control %d, open %d", got, want)
	}
	// More than the bills owe is refused, and nothing is written.
	before := f.count(`SELECT count(*) FROM ap_payments WHERE vendor_id = $1`, f.vendor)
	over := f.pay(100000, b) // b is partial, owing 1000
	if over.status != http.StatusConflict {
		t.Fatalf("overpay = %d, want 409: %s", over.status, over.raw)
	}
	if _, blockers, _ := errorOf(t, over); blockers[0] != "exceeds_open" {
		t.Errorf("overpay = %v, want exceeds_open", blockers)
	}
	if after := f.count(`SELECT count(*) FROM ap_payments WHERE vendor_id = $1`, f.vendor); after != before {
		t.Errorf("%d payments were written for a refused overpay", after-before)
	}
	// A bill of another vendor is not this payment's to pay.
	if ui := f.pay(700, uuid.NewString()); ui.status != http.StatusBadRequest {
		t.Errorf("unknown invoice = %d, want 400 naming invoice_ids[0]", ui.status)
	}
	// An old-wire body is a validation failure, not a 500.
	if eb := f.do("POST", "/api/v1/ap/payments", map[string]any{"vendor_id": f.vendor.String()}); eb.status != http.StatusBadRequest {
		t.Errorf("an empty pay body = %d, want 400", eb.status)
	}
	// The payment list stays a bare array until its own conversion.
	if pl := f.do("GET", "/api/v1/ap/payments?vendor_id="+f.vendor.String(), nil); pl.status != 200 || !strings.HasPrefix(string(pl.raw), "[") || strings.Count(string(pl.raw), `"id"`) != 2 {
		t.Errorf("list payments = %d %s, want a bare array of the 2 payments", pl.status, pl.raw)
	}
}

// RULE: an operator's gap in the chart of accounts (no 2010) is a 409 blocker
// naming the account, not a 500: nothing is written either way, but the wire
// answer must tell the operator what to mend (review r2 P3-3).
func TestWire_ApproveAccountMissing(t *testing.T) {
	testutil.LockOutboxTables(t)
	f := newFixture(t, testutil.RequireDB(t))
	mustExec(t, f.db, `UPDATE gl_accounts SET code = 'X2010' WHERE code = '2010'`)
	t.Cleanup(func() {
		mustExec(t, f.db, `UPDATE gl_accounts SET code = '2010' WHERE code = 'X2010'`)
	})
	id, r1 := f.enter("V-NOAP", "1", 1000)
	a := f.approve(id, r1)
	if a.status != http.StatusConflict {
		t.Fatalf("approve with no 2010 = %d, want 409: %s", a.status, a.raw)
	}
	if code, blockers, _ := errorOf(t, a); code != "conflict" || blockers[0] != "account_missing" {
		t.Errorf("approve with no 2010 = %q %v, want account_missing", code, blockers)
	}
	if n := f.count(`SELECT count(*) FROM vendor_invoices WHERE id = $1 AND status = 'PENDING' AND revision = 1`, id); n != 1 {
		t.Error("the bill did not stay pending at revision 1")
	}
	if n := f.count(`SELECT count(*) FROM gl_journal_entries WHERE source_ref_id = $1`, id); n != 0 {
		t.Errorf("%d entries posted without the control account", n)
	}
	// A payment already made on an approved bill leaves the same gap on its
	// entry (DR 2010), so the payment act refuses the same way.
	mustExec(t, f.db, `UPDATE gl_accounts SET code = '2010' WHERE code = 'X2010'`)
	aa := f.approve(id, r1)
	if aa.status != http.StatusOK {
		t.Fatal(aa.raw)
	}
	if got, want := f.controlBalance(), f.openSum(); got != want {
		t.Fatalf("after the approve: control %d, open %d", got, want)
	}
	mustExec(t, f.db, `UPDATE gl_accounts SET code = 'X2010' WHERE code = '2010'`)
	p := f.pay(10, id)
	if p.status != http.StatusConflict {
		t.Fatalf("pay with no 2010 = %d, want 409: %s", p.status, p.raw)
	}
	if _, blockers, _ := errorOf(t, p); blockers[0] != "account_missing" {
		t.Errorf("pay with no 2010 = %v, want account_missing", blockers)
	}
	if n := f.count(`SELECT count(*) FROM ap_payments WHERE vendor_id = $1`, f.vendor); n != 0 {
		t.Errorf("%d payments survived the refusal", n)
	}
	if !f.balanced() {
		t.Error("an entry does not balance")
	}
}

// RULE (review r8 P3-1): a pending bill cannot carry a paid amount. A payment
// applies only to an approved or partial bill, so amount_paid on a pending
// bill is a legacy or hand edited row; migration 106 clamped its amount_open
// below the total (a bill paid past its total sits at zero), and the approve
// posts the whole total while amount_open stands still, so 2010 would hold
// money no bill owes. The approve refuses such a bill; nothing is written.
func TestWire_ApproveRefusesLegacyPaidAmount(t *testing.T) {
	testutil.LockOutboxTables(t)
	f := newFixture(t, testutil.RequireDB(t))

	// The unknown status shape: a bill the migration made PENDING, paid past
	// its total, its open clamped to zero (what 106's LEAST leaves).
	id1, r1 := f.enter("V-PAID-1", "1", 40000) // 4.00
	mustExec(t, f.db, `UPDATE vendor_invoices SET amount_paid = 9.00, amount_open = 0.00 WHERE id = $1`, id1)
	// The drift shape: paid part of its total, its open clamped to the rest.
	id2, r2 := f.enter("V-PAID-2", "1", 100000) // 10.00
	mustExec(t, f.db, `UPDATE vendor_invoices SET amount_paid = 3.00, amount_open = 7.00 WHERE id = $1`, id2)

	for _, bill := range []struct {
		id  string
		rev int64
	}{{id1, r1}, {id2, r2}} {
		a := f.approve(bill.id, bill.rev)
		if a.status != http.StatusConflict {
			t.Fatalf("approve a pending bill with a paid amount = %d, want 409: %s", a.status, a.raw)
		}
		if code, blockers, _ := errorOf(t, a); code != "invalid_state_transition" || blockers[0] != "has_paid_amount" {
			t.Errorf("approve a pending bill with a paid amount = %q %v, want has_paid_amount", code, blockers)
		}
		if n := f.count(`SELECT count(*) FROM vendor_invoices WHERE id = $1 AND status = 'PENDING' AND revision = 1`, bill.id); n != 1 {
			t.Error("a refused approve changed the bill")
		}
		if n := f.count(`SELECT count(*) FROM gl_journal_entries WHERE source = 'VENDOR_INVOICE' AND source_ref_id = $1`, bill.id); n != 0 {
			t.Errorf("%d entries were written for a refused approve", n)
		}
		if !f.balanced() {
			t.Error("an entry does not balance")
		}
		if got, want := f.controlBalance(), f.openSum(); got != want {
			t.Errorf("after the refused approve: control %d, open %d", got, want)
		}
	}
}

// RULE (PR 89 r1 P2-1): an omitted currency comes from the currency.default
// setting, read by the create's resolve (USD where the setting is absent).
// Parse leaves the field empty when the request omits it, so the setting is
// read on the route; a currency the request does name is validated as an ISO
// 4217 code of three capital letters.
func TestWire_CreateCurrencyFromSetting(t *testing.T) {
	testutil.LockOutboxTables(t)
	f := newFixture(t, testutil.RequireDB(t))
	// Migration 091 seeds the row ('USD') and the whole base reads it, so the
	// test borrows it briefly and puts it back; a parallel package's read
	// (the order service's customer facts CROSS JOIN it) must never find it
	// gone.
	restore := `INSERT INTO system_settings (key, value) VALUES ('currency.default', 'USD')
		ON CONFLICT (key) DO UPDATE SET value = 'USD'`
	t.Cleanup(func() { mustExec(t, f.db, restore) })
	mustExec(t, f.db, `UPDATE system_settings SET value = 'EUR' WHERE key = 'currency.default'`)

	// An omitted currency takes the setting's value.
	body := f.createBody("V-CUR-1", "1", 1000)
	r := f.do("POST", "/api/v1/ap/invoices", body)
	if r.status != http.StatusCreated {
		t.Fatalf("create with the currency omitted = %d: %s", r.status, r.raw)
	}
	if got := str(t, r.body, "currency"); got != "EUR" {
		t.Errorf("an omitted currency = %q, want the setting's EUR", got)
	}
	if n := f.count(`SELECT count(*) FROM vendor_invoices WHERE id = $1 AND currency = 'EUR'`, str(t, r.body, "id")); n != 1 {
		t.Error("the bill was not stored in the setting's currency")
	}
	mustExec(t, f.db, restore)

	// A named currency still wins, and a bad one is still refused.
	body2 := f.createBody("V-CUR-2", "1", 1000)
	body2["currency"] = "CAD"
	r2 := f.do("POST", "/api/v1/ap/invoices", body2)
	if r2.status != http.StatusCreated {
		t.Fatalf("create with a named currency = %d: %s", r2.status, r2.raw)
	}
	if got := str(t, r2.body, "currency"); got != "CAD" {
		t.Errorf("a named currency = %q, want CAD", got)
	}
	body3 := f.createBody("V-CUR-3", "1", 1000)
	body3["currency"] = "usd"
	r3 := f.do("POST", "/api/v1/ap/invoices", body3)
	if r3.status != http.StatusBadRequest {
		t.Fatalf("create with a lowercase currency = %d, want 400: %s", r3.status, r3.raw)
	}
	if _, _, fields := errorOf(t, r3); len(fields) == 0 || fields[0] != "currency" {
		t.Errorf("create with a lowercase currency = %v, want currency", fields)
	}

	// A create posts nothing, so the tie stands where it was.
	if !f.balanced() {
		t.Error("an entry does not balance")
	}
	if got, want := f.controlBalance(), f.openSum(); got != want {
		t.Errorf("after the creates: control %d, open %d", got, want)
	}
}

// RULE (ADR 0001 section 4): a client mistake is a 400 naming the field, never
// a 500. A payment naming one bill twice would apply it twice (the second pass
// reuses the stale locked summary) and only the amount_open CHECK stops it, so
// Parse refuses the repeated id up front.
func TestWire_PaymentRefusesDuplicateInvoiceIDs(t *testing.T) {
	testutil.LockOutboxTables(t)
	f := newFixture(t, testutil.RequireDB(t))
	id, r1 := f.enter("V-DUP-ID", "1", 1000)
	if a := f.approve(id, r1); a.status != http.StatusOK {
		t.Fatal(a.raw)
	}
	if got, want := f.controlBalance(), f.openSum(); got != want {
		t.Fatalf("after the approve: control %d, open %d", got, want)
	}
	dup := f.pay(12, id, id)
	if dup.status != http.StatusBadRequest {
		t.Fatalf("pay a repeated invoice id = %d, want 400: %s", dup.status, dup.raw)
	}
	if _, _, fields := errorOf(t, dup); len(fields) == 0 || fields[0] != "invoice_ids[1]" {
		t.Errorf("pay a repeated invoice id names %v, want invoice_ids[1]", fields)
	}
	if n := f.count(`SELECT count(*) FROM ap_payments WHERE vendor_id = $1`, f.vendor); n != 0 {
		t.Errorf("%d payments written for a refused duplicate", n)
	}
	if n := f.count(`SELECT count(*) FROM ap_payment_applications a JOIN vendor_invoices i ON i.id = a.invoice_id WHERE i.vendor_id = $1`, f.vendor); n != 0 {
		t.Errorf("%d applications written for a refused duplicate", n)
	}
	if !f.balanced() {
		t.Error("an entry does not balance")
	}
	if got, want := f.controlBalance(), f.openSum(); got != want {
		t.Errorf("after the refused duplicate: control %d, open %d", got, want)
	}
	// A payment naming the bill once still applies.
	if p := f.pay(10, id); p.status != http.StatusCreated {
		t.Errorf("pay after the refusal = %d, want 201: %s", p.status, p.raw)
	}
	if got, want := f.controlBalance(), f.openSum(); got != want || want != 0 {
		t.Errorf("after the payment: control %d, open %d, want both 0", got, want)
	}
}

// RULE (ADR 0008 section 11, converted here by the lead's promotion of review
// r2 P3-4): the payment request takes integer cents on the wire, like every
// other money field. A decimal amount is a validation failure, never a silent
// rounding, and the old float dollars field is gone.
func TestWire_PayAmountIsCents(t *testing.T) {
	testutil.LockOutboxTables(t)
	f := newFixture(t, testutil.RequireDB(t))
	id, r1 := f.enter("V-CENTS", "1", 1000)
	if a := f.approve(id, r1); a.status != http.StatusOK {
		t.Fatal(a.raw)
	}
	if got, want := f.controlBalance(), f.openSum(); got != want {
		t.Fatalf("after the approve: control %d, open %d", got, want)
	}
	body := func(amount any) map[string]any {
		return map[string]any{"vendor_id": f.vendor.String(), "amount_cents": amount, "method": "CHECK",
			"payment_date": "2026-01-15", "invoice_ids": []any{id}}
	}
	if p := f.do("POST", "/api/v1/ap/payments", body(10)); p.status != http.StatusCreated {
		t.Fatalf("pay integer cents = %d, want 201: %s", p.status, p.raw)
	}
	if got, want := f.controlBalance(), f.openSum(); got != want || want != 0 {
		t.Errorf("after the payment: control %d, open %d, want both 0", got, want)
	}
	if !f.balanced() {
		t.Error("the payment entry does not balance")
	}
	// A decimal is refused, never silently rounded (10.5 is not a cent amount).
	id2, r1b := f.enter("V-CENTS-2", "1", 1000)
	if a := f.approve(id2, r1b); a.status != http.StatusOK {
		t.Fatal(a.raw)
	}
	body2 := func(amount any) map[string]any {
		return map[string]any{"vendor_id": f.vendor.String(), "amount_cents": amount, "method": "CHECK",
			"payment_date": "2026-01-15", "invoice_ids": []any{id2}}
	}
	d := f.do("POST", "/api/v1/ap/payments", body2(10.5))
	if d.status != http.StatusBadRequest {
		t.Fatalf("pay a decimal = %d, want 400: %s", d.status, d.raw)
	}
	if _, _, fields := errorOf(t, d); len(fields) == 0 || fields[0] != "amount_cents" {
		t.Errorf("pay a decimal names %v, want amount_cents", fields)
	}
	// An amount past the payment column's NUMERIC(12,2) is a 400 naming the
	// field, never a numeric overflow answered as a 500.
	for _, huge := range []int64{math.MaxInt64, 1_000_000_000_000} {
		h := f.do("POST", "/api/v1/ap/payments", body2(huge))
		if h.status != http.StatusBadRequest {
			t.Fatalf("pay %d cents = %d, want 400: %s", huge, h.status, h.raw)
		}
		if _, _, fields := errorOf(t, h); len(fields) == 0 || fields[0] != "amount_cents" {
			t.Errorf("pay %d cents names %v, want amount_cents", huge, fields)
		}
	}
	if got, want := f.controlBalance(), f.openSum(); got != want || !f.balanced() {
		t.Errorf("after the refused huge amounts: control %d, open %d, balanced %v", got, want, f.balanced())
	}
	// The old float dollars field is not read.
	old := f.do("POST", "/api/v1/ap/payments", map[string]any{
		"vendor_id": f.vendor.String(), "amount": 0.10, "method": "CHECK",
		"payment_date": "2026-01-15", "invoice_ids": []any{id2}})
	if old.status != http.StatusBadRequest {
		t.Errorf("pay the old float field = %d, want 400", old.status)
	}
	if n := f.count(`SELECT count(*) FROM ap_payments WHERE vendor_id = $1`, f.vendor); n != 1 {
		t.Errorf("%d payments stand, want the one accepted", n)
	}
}

// A bill that prices to zero (a zero price, or a positive price that rounds
// to 0 cents at the line) approves with no entry, because there is nothing to
// post. It owes nothing, so its void has nothing to reverse and goes through:
// it must not be refused as a bill with no linked entry (review r6 P2-1).
func TestWire_VoidZeroBill(t *testing.T) {
	testutil.LockOutboxTables(t)
	f := newFixture(t, testutil.RequireDB(t))

	for _, c := range []struct {
		number string
		price  int64
	}{
		{"V-ZERO-1", 0}, // a zero price
		{"V-ZERO-2", 5}, // 0.0005 a unit, 0 cents at the line
	} {
		id, r1 := f.enter(c.number, "1", c.price)
		a := f.approve(id, r1)
		if a.status != http.StatusOK {
			t.Fatalf("%s: approve = %d: %s", c.number, a.status, a.raw)
		}
		if n := f.count(`SELECT count(*) FROM vendor_invoices WHERE id = $1 AND total = 0 AND gl_entry_id IS NULL`, id); n != 1 {
			t.Fatalf("%s: the approved bill is not a zero total with no entry", c.number)
		}
		if got, want := f.controlBalance(), f.openSum(); got != want {
			t.Fatalf("%s: after the approve: control %d, open %d", c.number, got, want)
		}
		before := f.count(`SELECT count(*) FROM gl_journal_entries`)
		v := f.void(id, 2, "entered in error")
		if v.status != http.StatusOK {
			t.Fatalf("%s: void the zero bill = %d, want 200: %s", c.number, v.status, v.raw)
		}
		if str(t, v.body, "status") != "voided" {
			t.Errorf("%s: the zero bill = %s, want voided", c.number, v.raw)
		}
		if after := f.count(`SELECT count(*) FROM gl_journal_entries`); after != before {
			t.Errorf("%s: %d entries were written for a void with nothing to reverse", c.number, after-before)
		}
		if !f.balanced() {
			t.Errorf("%s: an entry does not balance", c.number)
		}
		if got, want := f.controlBalance(), f.openSum(); got != want {
			t.Errorf("%s: after the void: control %d, open %d", c.number, got, want)
		}
	}
}

// RULE (ADR 0008 7.4): a bill approved before migration 106 carries its entry
// but no pointer to it; the migration links the newest posted, unreversed,
// balanced VENDOR_INVOICE entry, and the void reverses exactly that entry, so
// the tie (every entry balanced, 2010 equal to what the open bills owe) holds
// for a legacy bill as for a new one. A legacy approved bill that holds a
// posted, unreversed entry it is not linked to is refused rather than voided
// silently with the credit stranded in 2010, and so is one whose linked entry
// is the base's one leg shape: a void would mirror it into another unbalanced
// entry. A legacy bill whose entry never posted holds no credit in 2010 and
// voids with nothing to reverse. So does a pending bill that holds a live
// entry it is not linked to (the removed sync could post before the status
// moved): its void is refused the same way, whatever its total says (review
// r1 P3-2).
func TestWire_VoidLegacyApprovedBill(t *testing.T) {
	testutil.LockOutboxTables(t)
	f := newFixture(t, testutil.RequireDB(t))

	// The linked legacy bill: approved with its entry, the pointer absent (the
	// state every pre-106 approved bill holds after the migration).
	id, r1 := f.enter("V-LEG-1", "1", 1000) // 10.00
	if a := f.approve(id, r1); a.status != http.StatusOK {
		t.Fatal(a.raw)
	}
	if got, want := f.controlBalance(), f.openSum(); got != want {
		t.Fatalf("after the approve: control %d, open %d", got, want)
	}
	mustExec(t, f.db, `UPDATE vendor_invoices SET gl_entry_id = NULL WHERE id = $1`, id)
	// The migration's link: the entry the old sync posted is the bill's
	// newest posted, unreversed VENDOR_INVOICE entry.
	mustExec(t, f.db, `UPDATE vendor_invoices vi SET gl_entry_id = e.id
		FROM gl_journal_entries e
		WHERE e.source = 'VENDOR_INVOICE' AND e.source_ref_id = vi.id
		  AND e.status = 'POSTED' AND e.reverses_entry_id IS NULL AND vi.id = $1`, id)
	v := f.void(id, 2, "legacy bill voided")
	if v.status != http.StatusOK {
		t.Fatalf("void the linked legacy bill = %d, want 200: %s", v.status, v.raw)
	}
	if str(t, v.body, "status") != "voided" {
		t.Errorf("the legacy bill = %s, want voided", v.raw)
	}
	reversals := f.count(`SELECT count(*) FROM gl_journal_entries r JOIN gl_journal_entries o ON o.id = r.reverses_entry_id WHERE o.source_ref_id = $1`, id)
	if reversals != 1 {
		t.Errorf("%d reversals of the legacy entry, want exactly 1", reversals)
	}
	_, legs := f.entryLegsWhere(t, f.db, `e.source_ref_id = $1
		OR e.reverses_entry_id IN (SELECT id FROM gl_journal_entries WHERE source_ref_id = $1)`, id)
	if legs["5020"] != (leg{debit: 10, credit: 10}) || legs["2010"] != (leg{debit: 10, credit: 10}) {
		t.Errorf("the reversal is not the exact mirror: %v", legs)
	}
	if !f.balanced() {
		t.Error("an entry of the legacy void does not balance")
	}
	if got, want := f.controlBalance(), f.openSum(); got != want || want != 0 {
		t.Errorf("after the legacy void: control %d, open %d, want both 0", got, want)
	}
	if n := f.count(`SELECT count(*) FROM vendor_invoices WHERE id = $1 AND gl_entry_id IS NOT NULL`, id); n != 1 {
		t.Error("the void lost the legacy entry link")
	}

	// The unlinked legacy bill whose entry never posted (the base swallowed
	// the journal error): nothing of its own stands in 2010, so the void has
	// nothing to reverse and goes through (review r8 P3-2 keys the no
	// reversal arm on the live entry, not the total). The bill dies owing
	// nothing more, and the tie the hand deletion broke is mended.
	id2, r1b := f.enter("V-LEG-2", "1", 2000) // 20.00
	if a := f.approve(id2, r1b); a.status != http.StatusOK {
		t.Fatal(a.raw)
	}
	mustExec(t, f.db, `UPDATE vendor_invoices SET gl_entry_id = NULL WHERE id = $1`, id2)
	mustExec(t, f.db, `DELETE FROM gl_journal_lines WHERE journal_entry_id IN
		(SELECT id FROM gl_journal_entries WHERE source_ref_id = $1)`, id2)
	mustExec(t, f.db, `DELETE FROM gl_journal_entries WHERE source_ref_id = $1`, id2)
	before := f.count(`SELECT count(*) FROM gl_journal_entries`)
	rv := f.void(id2, 2, "legacy bill voided")
	if rv.status != http.StatusOK {
		t.Fatalf("void the unlinked legacy bill = %d, want 200: %s", rv.status, rv.raw)
	}
	if str(t, rv.body, "status") != "voided" {
		t.Errorf("the unlinked legacy bill = %s, want voided", rv.raw)
	}
	if after := f.count(`SELECT count(*) FROM gl_journal_entries`); after != before {
		t.Errorf("%d entries were written for a void with nothing to reverse", after-before)
	}
	if n := f.count(`SELECT count(*) FROM vendor_invoices WHERE id = $1 AND status = 'VOIDED' AND amount_open = 0.00`, id2); n != 1 {
		t.Error("the unlinked legacy bill did not die owing nothing")
	}
	if !f.balanced() {
		t.Error("an entry does not balance")
	}
	if got, want := f.controlBalance(), f.openSum(); got != want || want != 0 {
		t.Errorf("after the unlinked legacy void: control %d, open %d, want both 0", got, want)
	}

	// The base's real legacy shape: the entry the base's approve posted has
	// one leg, the Accounts Payable credit for the whole total with no debit
	// (the base loaded no lines). An operator may link such an entry by hand,
	// or an older link may already point at it; the void must not mirror it
	// into a second unbalanced entry: it is refused with the
	// entry_unbalanced blocker and nothing is written (review r4 P2-1).
	id3, r1c := f.enter("V-LEG-3", "1", 3000) // 30.00
	if a := f.approve(id3, r1c); a.status != http.StatusOK {
		t.Fatal(a.raw)
	}
	// Rewrite the approve entry into the base's one leg shape: only the
	// Accounts Payable credit stays.
	mustExec(t, f.db, `DELETE FROM gl_journal_lines WHERE journal_entry_id IN
		(SELECT id FROM gl_journal_entries WHERE source_ref_id = $1)
		AND account_id <> (SELECT id FROM gl_accounts WHERE code = '2010')`, id3)
	if f.balanced() {
		t.Fatal("the seeded one leg entry balances; the seed did not take")
	}
	controlBefore, openBefore := f.controlBalance(), f.openSum()
	before3 := f.count(`SELECT count(*) FROM gl_journal_entries`)
	bv := f.void(id3, 2, "legacy bill voided")
	if bv.status != http.StatusConflict {
		t.Fatalf("void a bill whose linked entry has one leg = %d, want 409: %s", bv.status, bv.raw)
	}
	if code, blockers, _ := errorOf(t, bv); code != "invalid_state_transition" || blockers[0] != "entry_unbalanced" {
		t.Errorf("void a bill whose linked entry has one leg = %q %v, want entry_unbalanced", code, blockers)
	}
	if n := f.count(`SELECT count(*) FROM vendor_invoices WHERE id = $1 AND status = 'APPROVED' AND revision = 2`, id3); n != 1 {
		t.Error("a refused one leg void changed the bill")
	}
	if after := f.count(`SELECT count(*) FROM gl_journal_entries`); after != before3 {
		t.Errorf("%d entries were written for a refused one leg void", after-before3)
	}
	// The tie after the refused act: the only unbalanced entry in the fixture
	// is the seeded legacy one (no act of the fixture wrote an unbalanced
	// entry), and the refusal moved no money: the control account and the
	// open sum stand where the seed left them.
	if n := f.count(`SELECT count(*) FROM gl_journal_entries e
		JOIN (SELECT journal_entry_id, SUM(debit) d, SUM(credit) c FROM gl_journal_lines GROUP BY journal_entry_id) s
		  ON s.journal_entry_id = e.id
		WHERE e.id IN (SELECT id FROM gl_journal_entries
		               WHERE source_ref_id IN (SELECT id FROM vendor_invoices WHERE vendor_id = $1)
		                  OR source_ref_id IN (SELECT id FROM ap_payments WHERE vendor_id = $1)
		                  OR reverses_entry_id IN (SELECT gl_entry_id FROM vendor_invoices WHERE vendor_id = $1))
		  AND s.d <> s.c`, f.vendor); n != 1 {
		t.Errorf("%d unbalanced entries stand, want only the seeded legacy one", n)
	}
	if got, want := f.controlBalance(), controlBefore; got != want {
		t.Errorf("after the refused one leg void: control %d, want the untouched %d", got, want)
	}
	if got, want := f.openSum(), openBefore; got != want {
		t.Errorf("after the refused one leg void: open %d, want the untouched %d", got, want)
	}

	// The pending shape: a bill whose entry posted while the status never
	// moved off pending (the removed sync could post before the status did),
	// holding a live, balanced entry it is not linked to. A plain void would
	// strand the credit under a VOIDED bill, so it is refused the same way as
	// an approved one (review r1 P3-2).
	id4, r1d := f.enter("V-LEG-4", "1", 120000) // 12.00
	if a := f.approve(id4, r1d); a.status != http.StatusOK {
		t.Fatal(a.raw)
	}
	mustExec(t, f.db, `UPDATE vendor_invoices SET status = 'PENDING', gl_entry_id = NULL,
		approved_by = NULL, approved_at = NULL WHERE id = $1`, id4)
	control4, open4 := f.controlBalance(), f.openSum()
	before4 := f.count(`SELECT count(*) FROM gl_journal_entries`)
	pv := f.void(id4, 2, "pending bill voided")
	if pv.status != http.StatusConflict {
		t.Fatalf("void a pending bill holding a live entry = %d, want 409: %s", pv.status, pv.raw)
	}
	if code, blockers, _ := errorOf(t, pv); code != "invalid_state_transition" || blockers[0] != "entry_not_linked" {
		t.Errorf("void a pending bill holding a live entry = %q %v, want entry_not_linked", code, blockers)
	}
	if n := f.count(`SELECT count(*) FROM vendor_invoices WHERE id = $1 AND status = 'PENDING' AND revision = 2`, id4); n != 1 {
		t.Error("a refused void changed the bill")
	}
	if after := f.count(`SELECT count(*) FROM gl_journal_entries`); after != before4 {
		t.Errorf("%d entries were written for a refused void", after-before4)
	}
	// The only unbalanced entry in the fixture stays the seeded legacy one
	// (the refusal moved no money).
	if n := f.count(`SELECT count(*) FROM gl_journal_entries e
		JOIN (SELECT journal_entry_id, SUM(debit) d, SUM(credit) c FROM gl_journal_lines GROUP BY journal_entry_id) s
		  ON s.journal_entry_id = e.id
		WHERE e.id IN (SELECT id FROM gl_journal_entries
		               WHERE source_ref_id IN (SELECT id FROM vendor_invoices WHERE vendor_id = $1)
		                  OR source_ref_id IN (SELECT id FROM ap_payments WHERE vendor_id = $1)
		                  OR reverses_entry_id IN (SELECT gl_entry_id FROM vendor_invoices WHERE vendor_id = $1))
		  AND s.d <> s.c`, f.vendor); n != 1 {
		t.Errorf("%d unbalanced entries stand, want only the seeded legacy one", n)
	}
	if got, want := f.controlBalance(), control4; got != want {
		t.Errorf("after the refused pending void: control %d, want the untouched %d", got, want)
	}
	if got, want := f.openSum(), open4; got != want {
		t.Errorf("after the refused pending void: open %d, want the untouched %d", got, want)
	}
}

// RULE (review r8 P3-2): the void's no reversal arm is keyed on the bill
// holding no posted, unreversed VENDOR_INVOICE entry, not on its total. A
// legacy bill hand edited to a zero total after its approve still holds the
// credit its entry posted, and a plain void would strand that credit in 2010
// under a voided bill: such a bill is refused when the entry is not linked,
// and voids with the entry reversed when it is. A bill with no live entry at
// all (a zero total bill that posted nothing) holds no credit and voids with
// nothing to reverse (TestWire_VoidZeroBill and the unlinked case above).
func TestWire_VoidHandEditedZeroTotalBill(t *testing.T) {
	testutil.LockOutboxTables(t)
	f := newFixture(t, testutil.RequireDB(t))

	// The hand edited shape: approved over the base's one leg credit (which
	// migration 106 leaves unlinked), its total then edited to zero and its
	// open with it.
	id, r1 := f.enter("V-EDIT-1", "1", 100000) // 10.00
	if a := f.approve(id, r1); a.status != http.StatusOK {
		t.Fatal(a.raw)
	}
	mustExec(t, f.db, `DELETE FROM gl_journal_lines WHERE journal_entry_id IN
		(SELECT id FROM gl_journal_entries WHERE source_ref_id = $1)
		AND account_id <> (SELECT id FROM gl_accounts WHERE code = '2010')`, id)
	mustExec(t, f.db, `UPDATE vendor_invoices SET gl_entry_id = NULL, total = 0.00, amount_open = 0.00 WHERE id = $1`, id)
	if f.balanced() {
		t.Fatal("the seeded one leg entry balances; the seed did not take")
	}
	control, open := f.controlBalance(), f.openSum()
	before := f.count(`SELECT count(*) FROM gl_journal_entries`)
	v := f.void(id, 2, "hand edited bill voided")
	if v.status != http.StatusConflict {
		t.Fatalf("void a hand edited zero total bill over a live entry = %d, want 409: %s", v.status, v.raw)
	}
	if code, blockers, _ := errorOf(t, v); code != "invalid_state_transition" || blockers[0] != "entry_not_linked" {
		t.Errorf("void a hand edited zero total bill = %q %v, want entry_not_linked", code, blockers)
	}
	if n := f.count(`SELECT count(*) FROM vendor_invoices WHERE id = $1 AND status = 'APPROVED' AND revision = 2`, id); n != 1 {
		t.Error("a refused hand edited void changed the bill")
	}
	if after := f.count(`SELECT count(*) FROM gl_journal_entries`); after != before {
		t.Errorf("%d entries were written for a refused void", after-before)
	}
	// The tie after the refused act: the refusal moved no money, and the only
	// unbalanced entry is the seeded legacy one.
	if got, want := f.controlBalance(), control; got != want {
		t.Errorf("after the refused hand edited void: control %d, want the untouched %d", got, want)
	}
	if got, want := f.openSum(), open; got != want {
		t.Errorf("after the refused hand edited void: open %d, want the untouched %d", got, want)
	}

	// The linked shape: the same hand edit over a linked, balanced entry
	// voids with the entry reversed, so 2010 gives the credit back and the
	// bill dies owing nothing.
	id2, r2 := f.enter("V-EDIT-2", "1", 100000) // 10.00
	if a := f.approve(id2, r2); a.status != http.StatusOK {
		t.Fatal(a.raw)
	}
	mustExec(t, f.db, `UPDATE vendor_invoices SET total = 0.00, amount_open = 0.00 WHERE id = $1`, id2)
	control2, open2 := f.controlBalance(), f.openSum()
	v2 := f.void(id2, 2, "hand edited bill voided")
	if v2.status != http.StatusOK {
		t.Fatalf("void a linked zero total bill = %d, want 200: %s", v2.status, v2.raw)
	}
	if str(t, v2.body, "status") != "voided" {
		t.Errorf("the linked zero total bill = %s, want voided", v2.raw)
	}
	if n := f.count(`SELECT count(*) FROM gl_journal_entries r JOIN gl_journal_entries o ON o.id = r.reverses_entry_id
		WHERE o.source_ref_id = $1`, id2); n != 1 {
		t.Errorf("%d reversals of the linked entry, want exactly 1", n)
	}
	if got, want := f.controlBalance(), control2-1000; got != want {
		t.Errorf("after the linked void: control %d, want the reversed %d", got, want)
	}
	if got, want := f.openSum(), open2; got != want {
		t.Errorf("after the linked void: open %d, want the untouched %d", got, want)
	}
	if n := f.count(`SELECT count(*) FROM gl_journal_entries e
		JOIN (SELECT journal_entry_id, SUM(debit) d, SUM(credit) c FROM gl_journal_lines GROUP BY journal_entry_id) s
		  ON s.journal_entry_id = e.id
		WHERE e.id IN (SELECT id FROM gl_journal_entries
		               WHERE source_ref_id IN (SELECT id FROM vendor_invoices WHERE vendor_id = $1)
		                  OR source_ref_id IN (SELECT id FROM ap_payments WHERE vendor_id = $1)
		                  OR reverses_entry_id IN (SELECT gl_entry_id FROM vendor_invoices WHERE vendor_id = $1))
		  AND s.d <> s.c`, f.vendor); n != 1 {
		t.Errorf("%d unbalanced entries stand, want only the seeded legacy one", n)
	}
}

// RULE (review r2 P1-1): a reversal counts against a bill's credit only
// while it is POSTED. The trial balance sums POSTED entries alone, so an
// entry whose reversal was voided keeps its credit live in 2010, and a bill
// that holds such an entry must not void with nothing reversed: the void is
// refused entry_not_linked and nothing is written, whatever the reversal row
// says. The shape is reachable with no hand SQL on the bill: the GL void
// route voids any posted entry, a reversal included, and migration 106
// leaves such a bill unlinked.
func TestWire_VoidRefusesUnlinkedBillWhoseReversalIsVoid(t *testing.T) {
	testutil.LockOutboxTables(t)
	f := newFixture(t, testutil.RequireDB(t))
	id, r1 := f.enter("V-RVOID-1", "1", 500000) // 50.00
	if a := f.approve(id, r1); a.status != http.StatusOK {
		t.Fatal(a.raw)
	}
	entry := f.glEntryOf(id)
	if rv := f.glReverse(entry); rv.status != http.StatusOK {
		t.Fatalf("reverse the entry = %d: %s", rv.status, rv.raw)
	}
	if vv := f.glVoidEntry(f.glReversalOf(entry)); vv.status != http.StatusOK {
		t.Fatalf("void the reversal = %d: %s", vv.status, vv.raw)
	}
	// The bill is the shape migration 106 leaves unlinked (its link predicate
	// reads a reversal row of any status as an undo): the entry's credit
	// stands live in 2010 while the bill still owes it.
	mustExec(t, f.db, `UPDATE vendor_invoices SET gl_entry_id = NULL WHERE id = $1`, id)
	control, open := f.controlBalance(), f.openSum()
	if control != 5000 || open != 5000 {
		t.Fatalf("control %d, open %d, want both 5000", control, open)
	}
	before := f.count(`SELECT count(*) FROM gl_journal_entries`)
	v := f.void(id, 2, "wrong bill")
	if v.status != http.StatusConflict {
		t.Fatalf("void a bill whose entry's reversal is void = %d, want 409: %s", v.status, v.raw)
	}
	if code, blockers, _ := errorOf(t, v); code != "invalid_state_transition" || blockers[0] != "entry_not_linked" {
		t.Errorf("void a bill whose entry's reversal is void = %q %v, want entry_not_linked", code, blockers)
	}
	if n := f.count(`SELECT count(*) FROM vendor_invoices WHERE id = $1 AND status = 'APPROVED' AND revision = 2`, id); n != 1 {
		t.Error("a refused void changed the bill")
	}
	if after := f.count(`SELECT count(*) FROM gl_journal_entries`); after != before {
		t.Errorf("%d entries were written for a refused void", after-before)
	}
	if n := f.count(`SELECT count(*) FROM gl_journal_entries e
		WHERE e.source = 'VENDOR_INVOICE' AND e.source_ref_id = $1
		  AND e.status = 'POSTED' AND e.reverses_entry_id IS NULL
		  AND NOT EXISTS (SELECT 1 FROM gl_journal_entries r WHERE r.reverses_entry_id = e.id AND r.status = 'POSTED')`, id); n != 1 {
		t.Errorf("%d live credits stand for the bill, want 1", n)
	}
	if !f.balanced() {
		t.Error("an entry does not balance")
	}
	if got, want := f.controlBalance(), control; got != want {
		t.Errorf("after the refused void: control %d, want the untouched %d", got, want)
	}
	if got, want := f.openSum(), open; got != want {
		t.Errorf("after the refused void: open %d, want the untouched %d", got, want)
	}
}

// RULE (review r2 P2-1): the void's linked arm is keyed on the ledger too,
// never on the pointer alone. A linked entry the GL routes have already
// reversed or voided holds no live credit in 2010, so the bill voids with
// nothing to reverse (never a 500). A linked entry whose reversal was itself
// voided keeps its credit live while the one reversal row the entry may hold
// is already taken, so no new reversal can post: that void is refused with
// the reversal_not_posted blocker and nothing is written.
func TestWire_VoidLinkedEntryAlreadyUndoneByGL(t *testing.T) {
	testutil.LockOutboxTables(t)
	f := newFixture(t, testutil.RequireDB(t))

	// Reversed by the GL reverse route: the credit is already undone, so the
	// void owes the ledger nothing and the open the bill carried closes.
	id1, r1 := f.enter("V-GLU-1", "1", 300000) // 30.00
	if a := f.approve(id1, r1); a.status != http.StatusOK {
		t.Fatal(a.raw)
	}
	if rv := f.glReverse(f.glEntryOf(id1)); rv.status != http.StatusOK {
		t.Fatalf("reverse the entry = %d: %s", rv.status, rv.raw)
	}
	control1, open1 := f.controlBalance(), f.openSum()
	if control1 != 0 || open1 != 3000 {
		t.Fatalf("control %d, open %d, want 0 and 3000", control1, open1)
	}
	before1 := f.count(`SELECT count(*) FROM gl_journal_entries`)
	v1 := f.void(id1, 2, "wrong bill")
	if v1.status != http.StatusOK {
		t.Fatalf("void a bill whose linked entry the GL route reversed = %d, want 200: %s", v1.status, v1.raw)
	}
	if str(t, v1.body, "status") != "voided" {
		t.Errorf("the bill = %s, want voided", v1.raw)
	}
	if after := f.count(`SELECT count(*) FROM gl_journal_entries`); after != before1 {
		t.Errorf("%d entries were written for a void with nothing to reverse", after-before1)
	}
	if !f.balanced() {
		t.Error("an entry does not balance")
	}
	if got, want := f.controlBalance(), control1; got != want {
		t.Errorf("after the void: control %d, want the untouched %d", got, want)
	}
	if got, want := f.openSum(), open1-3000; got != want {
		t.Errorf("after the void: open %d, want %d", got, want)
	}

	// Voided by the GL void route: the entry is not POSTED, so it counts
	// nowhere in 2010; the bill voids with nothing to reverse.
	id2, r2 := f.enter("V-GLU-2", "1", 400000) // 40.00
	if a := f.approve(id2, r2); a.status != http.StatusOK {
		t.Fatal(a.raw)
	}
	if vv := f.glVoidEntry(f.glEntryOf(id2)); vv.status != http.StatusOK {
		t.Fatalf("void the entry = %d: %s", vv.status, vv.raw)
	}
	control2, open2 := f.controlBalance(), f.openSum()
	if control2 != 0 || open2 != 4000 {
		t.Fatalf("control %d, open %d, want 0 and 4000", control2, open2)
	}
	before2 := f.count(`SELECT count(*) FROM gl_journal_entries`)
	v2 := f.void(id2, 2, "wrong bill")
	if v2.status != http.StatusOK {
		t.Fatalf("void a bill whose linked entry the GL route voided = %d, want 200: %s", v2.status, v2.raw)
	}
	if str(t, v2.body, "status") != "voided" {
		t.Errorf("the bill = %s, want voided", v2.raw)
	}
	if after := f.count(`SELECT count(*) FROM gl_journal_entries`); after != before2 {
		t.Errorf("%d entries were written for a void with nothing to reverse", after-before2)
	}
	if !f.balanced() {
		t.Error("an entry does not balance")
	}
	if got, want := f.controlBalance(), control2; got != want {
		t.Errorf("after the void: control %d, want the untouched %d", got, want)
	}
	if got, want := f.openSum(), open2-4000; got != want {
		t.Errorf("after the void: open %d, want %d", got, want)
	}

	// The reversal voided by the GL void route: the original credit stands
	// live again (a voided reversal undoes nothing), but the entry's one
	// reversal row is taken, so the void cannot reverse the credit either:
	// refused, nothing written, the tie keeps holding.
	id3, r3 := f.enter("V-GLU-3", "1", 500000) // 50.00
	if a := f.approve(id3, r3); a.status != http.StatusOK {
		t.Fatal(a.raw)
	}
	entry3 := f.glEntryOf(id3)
	if rv := f.glReverse(entry3); rv.status != http.StatusOK {
		t.Fatalf("reverse the entry = %d: %s", rv.status, rv.raw)
	}
	if vv := f.glVoidEntry(f.glReversalOf(entry3)); vv.status != http.StatusOK {
		t.Fatalf("void the reversal = %d: %s", vv.status, vv.raw)
	}
	control3, open3 := f.controlBalance(), f.openSum()
	if control3 != 5000 || open3 != 5000 {
		t.Fatalf("control %d, open %d, want both 5000", control3, open3)
	}
	before3 := f.count(`SELECT count(*) FROM gl_journal_entries`)
	v3 := f.void(id3, 2, "wrong bill")
	if v3.status != http.StatusConflict {
		t.Fatalf("void a bill whose linked entry's reversal is void = %d, want 409: %s", v3.status, v3.raw)
	}
	if code, blockers, _ := errorOf(t, v3); code != "invalid_state_transition" || blockers[0] != "reversal_not_posted" {
		t.Errorf("void a bill whose linked entry's reversal is void = %q %v, want reversal_not_posted", code, blockers)
	}
	if n := f.count(`SELECT count(*) FROM vendor_invoices WHERE id = $1 AND status = 'APPROVED' AND revision = 2`, id3); n != 1 {
		t.Error("a refused void changed the bill")
	}
	if after := f.count(`SELECT count(*) FROM gl_journal_entries`); after != before3 {
		t.Errorf("%d entries were written for a refused void", after-before3)
	}
	if !f.balanced() {
		t.Error("an entry does not balance")
	}
	if got, want := f.controlBalance(), control3; got != want {
		t.Errorf("after the refused void: control %d, want the untouched %d", got, want)
	}
	if got, want := f.openSum(), open3; got != want {
		t.Errorf("after the refused void: open %d, want the untouched %d", got, want)
	}

	// The entry voided by the GL void route and its reversal voided with it
	// (review r3 P3-3's untested first branch): neither POSTED entry counts
	// in 2010, so the chain holds no credit and the void has nothing to
	// reverse, whatever the reversal row standing on the entry says.
	id4, r4 := f.enter("V-GLU-4", "1", 600000) // 60.00
	if a := f.approve(id4, r4); a.status != http.StatusOK {
		t.Fatal(a.raw)
	}
	entry4 := f.glEntryOf(id4)
	if rv := f.glReverse(entry4); rv.status != http.StatusOK {
		t.Fatalf("reverse the entry = %d: %s", rv.status, rv.raw)
	}
	if vv := f.glVoidEntry(entry4); vv.status != http.StatusOK {
		t.Fatalf("void the entry = %d: %s", vv.status, vv.raw)
	}
	if vv := f.glVoidEntry(f.glReversalOf(entry4)); vv.status != http.StatusOK {
		t.Fatalf("void the reversal = %d: %s", vv.status, vv.raw)
	}
	control4, open4 := f.controlBalance(), f.openSum()
	if control4 != control3 || open4 != open3+6000 {
		t.Fatalf("control %d, open %d, want the shape's own %d more owed and no more credit (control %d, open %d)", control4, open4, 6000, control3, open3)
	}
	before4 := f.count(`SELECT count(*) FROM gl_journal_entries`)
	v4 := f.void(id4, 2, "wrong bill")
	if v4.status != http.StatusOK {
		t.Fatalf("void a bill whose entry and reversal are both void = %d, want 200: %s", v4.status, v4.raw)
	}
	if str(t, v4.body, "status") != "voided" {
		t.Errorf("the bill = %s, want voided", v4.raw)
	}
	if after := f.count(`SELECT count(*) FROM gl_journal_entries`); after != before4 {
		t.Errorf("%d entries were written for a void with nothing to reverse", after-before4)
	}
	if !f.balanced() {
		t.Error("an entry does not balance")
	}
	if got, want := f.controlBalance(), control4; got != want {
		t.Errorf("after the void: control %d, want the untouched %d", got, want)
	}
	if got, want := f.openSum(), open4-6000; got != want {
		t.Errorf("after the void: open %d, want %d", got, want)
	}
}

// RULE (review r2 P2-2): a payment's entry posts in the bills' currency, not
// the setting's, so 2010 ties per currency; a payment naming bills of more
// than one currency is refused with the currency_mismatch blocker naming the
// bills, and nothing is written.
func TestWire_PaymentCurrencyFollowsTheBills(t *testing.T) {
	testutil.LockOutboxTables(t)
	f := newFixture(t, testutil.RequireDB(t))
	// Migration 091 seeds the row and the whole base reads it, so the test
	// borrows it briefly and puts it back; the shared lock every wire test
	// holds keeps the parallel packages off it for the window.
	restore := `INSERT INTO system_settings (key, value) VALUES ('currency.default', 'USD')
		ON CONFLICT (key) DO UPDATE SET value = 'USD'`
	t.Cleanup(func() { mustExec(t, f.db, restore) })
	mustExec(t, f.db, `UPDATE system_settings SET value = 'EUR' WHERE key = 'currency.default'`)

	// A bill entered while the setting read EUR, paid after the setting went
	// back to USD: the payment entry must post in the bill's EUR.
	eur, r1 := f.enter("V-CURPAY-1", "1", 1000000) // 100.00
	if n := f.count(`SELECT count(*) FROM vendor_invoices WHERE id = $1 AND currency = 'EUR'`, eur); n != 1 {
		t.Fatal("the bill was not entered in the setting's EUR")
	}
	mustExec(t, f.db, restore)
	if a := f.approve(eur, r1); a.status != http.StatusOK {
		t.Fatal(a.raw)
	}
	if got, want := f.controlBalanceIn("EUR"), f.openSumIn("EUR"); got != want || want != 10000 {
		t.Fatalf("after the approve: EUR control %d, open %d, want both 10000", got, want)
	}
	if p := f.pay(10000, eur); p.status != http.StatusCreated {
		t.Fatalf("pay the EUR bill = %d: %s", p.status, p.raw)
	}
	if n := f.count(`SELECT count(*) FROM gl_journal_entries e
		JOIN ap_payments p ON p.id = e.source_ref_id
		WHERE e.source = 'VENDOR_PAYMENT' AND p.vendor_id = $1 AND e.currency = 'EUR'`, f.vendor); n != 1 {
		t.Errorf("%d payment entries post in the bill's EUR, want 1", n)
	}
	if !f.balanced() {
		t.Error("the payment entry does not balance")
	}
	for _, c := range []string{"EUR", "USD", "CAD"} {
		if got, want := f.controlBalanceIn(c), f.openSumIn(c); got != want {
			t.Errorf("after the payment: %s control %d, open %d, want equal", c, got, want)
		}
	}

	// A payment naming bills of two currencies is refused, naming them, and
	// nothing is written.
	usd, r2 := f.enter("V-CURPAY-2", "1", 500000) // 50.00
	cadBody := f.createBody("V-CURPAY-3", "1", 700000)
	cadBody["currency"] = "CAD"
	cr := f.do("POST", "/api/v1/ap/invoices", cadBody)
	if cr.status != http.StatusCreated {
		t.Fatalf("enter the CAD bill = %d: %s", cr.status, cr.raw)
	}
	cad, r3 := str(t, cr.body, "id"), rev(t, cr)
	if a := f.approve(usd, r2); a.status != http.StatusOK {
		t.Fatal(a.raw)
	}
	if got, want := f.controlBalanceIn("USD"), f.openSumIn("USD"); got != want || want != 5000 {
		t.Fatalf("after the USD approve: USD control %d, open %d, want both 5000", got, want)
	}
	if a := f.approve(cad, r3); a.status != http.StatusOK {
		t.Fatal(a.raw)
	}
	if got, want := f.controlBalanceIn("CAD"), f.openSumIn("CAD"); got != want || want != 7000 {
		t.Fatalf("after the CAD approve: CAD control %d, open %d, want both 7000", got, want)
	}
	payments := f.count(`SELECT count(*) FROM ap_payments WHERE vendor_id = $1`, f.vendor)
	applications := f.count(`SELECT count(*) FROM ap_payment_applications a JOIN vendor_invoices i ON i.id = a.invoice_id WHERE i.vendor_id = $1`, f.vendor)
	entries := f.count(`SELECT count(*) FROM gl_journal_entries`)
	mixed := f.pay(12000, usd, cad)
	if mixed.status != http.StatusConflict {
		t.Fatalf("pay bills of two currencies = %d, want 409: %s", mixed.status, mixed.raw)
	}
	if code, blockers, _ := errorOf(t, mixed); code != "invalid_state_transition" || blockers[0] != "currency_mismatch" {
		t.Errorf("pay bills of two currencies = %q %v, want currency_mismatch", code, blockers)
	}
	for _, number := range []string{f.number(usd), f.number(cad)} {
		if !strings.Contains(string(mixed.raw), number) {
			t.Errorf("the refusal does not name %s: %s", number, mixed.raw)
		}
	}
	if after := f.count(`SELECT count(*) FROM ap_payments WHERE vendor_id = $1`, f.vendor); after != payments {
		t.Errorf("%d payments were written for a refused payment", after-payments)
	}
	if after := f.count(`SELECT count(*) FROM ap_payment_applications a JOIN vendor_invoices i ON i.id = a.invoice_id WHERE i.vendor_id = $1`, f.vendor); after != applications {
		t.Errorf("%d applications were written for a refused payment", after-applications)
	}
	if after := f.count(`SELECT count(*) FROM gl_journal_entries`); after != entries {
		t.Errorf("%d entries were written for a refused payment", after-entries)
	}
	if !f.balanced() {
		t.Error("an entry does not balance")
	}
	for _, c := range []string{"EUR", "USD", "CAD"} {
		if got, want := f.controlBalanceIn(c), f.openSumIn(c); got != want {
			t.Errorf("after the refused payment: %s control %d, open %d, want equal", c, got, want)
		}
	}
}

// RULE (review r2 P3-1): a double posted bill does not void with its
// duplicate credit live. A live VENDOR_INVOICE entry beside the linked one
// holds a second credit in 2010 that no route could reach under a VOIDED
// bill, so the void is refused entry_not_linked naming the other entry, and
// nothing is written.
func TestWire_VoidRefusesDoublePostedBill(t *testing.T) {
	testutil.LockOutboxTables(t)
	f := newFixture(t, testutil.RequireDB(t))
	id, r1 := f.enter("V-DUP-ENT", "1", 200000) // 20.00
	if a := f.approve(id, r1); a.status != http.StatusOK {
		t.Fatal(a.raw)
	}
	// The double posted shape: a second POSTED, balanced VENDOR_INVOICE entry
	// for the bill, beside the linked one.
	entry := f.glEntryOf(id)
	dup := uuid.New()
	mustExec(t, f.db, `INSERT INTO gl_journal_entries (id, entry_date, memo, source, source_ref_id, branch_id, status, posted_by, currency)
		SELECT $2, entry_date, memo || ' (duplicate)', source, source_ref_id, branch_id, status, posted_by, currency
		FROM gl_journal_entries WHERE id = $1`, entry, dup)
	mustExec(t, f.db, `INSERT INTO gl_journal_lines (id, journal_entry_id, account_id, description, debit, credit, branch_id)
		SELECT gen_random_uuid(), $2, account_id, description, debit, credit, branch_id
		FROM gl_journal_lines WHERE journal_entry_id = $1`, entry, dup)
	control, open := f.controlBalance(), f.openSum()
	if control != 4000 || open != 2000 {
		t.Fatalf("control %d, open %d, want 4000 and 2000", control, open)
	}
	before := f.count(`SELECT count(*) FROM gl_journal_entries`)
	v := f.void(id, 2, "wrong bill")
	if v.status != http.StatusConflict {
		t.Fatalf("void a double posted bill = %d, want 409: %s", v.status, v.raw)
	}
	if code, blockers, _ := errorOf(t, v); code != "invalid_state_transition" || blockers[0] != "entry_not_linked" {
		t.Errorf("void a double posted bill = %q %v, want entry_not_linked", code, blockers)
	}
	if !strings.Contains(string(v.raw), dup.String()) {
		t.Errorf("the refusal does not name the duplicate entry: %s", v.raw)
	}
	if n := f.count(`SELECT count(*) FROM vendor_invoices WHERE id = $1 AND status = 'APPROVED' AND revision = 2`, id); n != 1 {
		t.Error("a refused void changed the bill")
	}
	if after := f.count(`SELECT count(*) FROM gl_journal_entries`); after != before {
		t.Errorf("%d entries were written for a refused void", after-before)
	}
	if n := f.count(`SELECT count(*) FROM gl_journal_entries r JOIN gl_journal_entries o ON o.id = r.reverses_entry_id WHERE o.source_ref_id = $1`, id); n != 0 {
		t.Errorf("%d reversals were written for a refused void", n)
	}
	if !f.balanced() {
		t.Error("an entry does not balance")
	}
	if got, want := f.controlBalance(), control; got != want {
		t.Errorf("after the refused void: control %d, want the untouched %d", got, want)
	}
	if got, want := f.openSum(), open; got != want {
		t.Errorf("after the refused void: open %d, want the untouched %d", got, want)
	}
}

// RULE (review r4 P1-1): a bill's credit is live exactly when the POSTED
// entries of its VENDOR_INVOICE entry and that entry's whole reversal chain
// leave a net amount on 2010, and every AP act decides on that net, never on
// one step of the chain. A reversal that a later reversal put back (a
// reinstatement) leaves the credit live at any depth, and a chain whose
// POSTED entries cancel holds none, whatever a single reversal row says.
func TestWire_VoidDecidesOnTheWholeReversalChain(t *testing.T) {
	testutil.LockOutboxTables(t)
	f := newFixture(t, testutil.RequireDB(t))

	shapes := []struct {
		name   string
		depth  int
		voided []int
	}{
		{"CHAIN-D0", 0, nil},
		{"CHAIN-D1", 1, nil},
		{"CHAIN-D2", 2, nil}, // the reinstatement, review r4 P1-1
		{"CHAIN-D3", 3, nil},
		{"CHAIN-D1-R1V", 1, []int{1}}, // review r2 P1-1 through the routes
		{"CHAIN-D2-R2V", 2, []int{2}},
		{"CHAIN-D3-R3V", 3, []int{3}},
		{"CHAIN-D2-R1V", 2, []int{1}}, // the reversal voided after its own reversal: twice the credit stands
		{"CHAIN-D3-R1V", 3, []int{1}},
	}

	for i, shape := range shapes {
		cents := int64(1100 + i*100)
		wantNet := netMultiple(shape.depth, shape.voided...) * cents

		// The void of the linked bill. The chain built, the bill keeps its
		// link and its APPROVED status, revision 2.
		id, entry := buildChain(t, f, shape.name+"-LV", cents, shape.depth, shape.voided...)
		if got := chainNet(f, entry); got != wantNet {
			t.Fatalf("%s: the chain nets %d, want %d (the shape itself is wrong)", shape.name, got, wantNet)
		}
		control, open := f.controlBalance(), f.openSum()
		before := f.count(`SELECT count(*) FROM gl_journal_entries`)
		v := f.void(id, 2, "chain test")
		switch {
		case wantNet == 0:
			// Nothing stands in 2010: the void owes the ledger nothing.
			if v.status != http.StatusOK {
				t.Errorf("%s: void a bill whose chain nets zero = %d, want 200: %s", shape.name, v.status, v.raw)
			}
			if after := f.count(`SELECT count(*) FROM gl_journal_entries`); after != before {
				t.Errorf("%s: %d entries were written for a void with nothing to reverse", shape.name, after-before)
			}
			if got, want := f.controlBalance(), control; got != want {
				t.Errorf("%s: after the void: control %d, want the untouched %d", shape.name, got, want)
			}
			if got, want := f.openSum(), open-cents; got != want {
				t.Errorf("%s: after the void: open %d, want %d", shape.name, got, want)
			}
			if got := chainNet(f, entry); got != 0 {
				t.Errorf("%s: after the void: the chain still nets %d on 2010, want 0", shape.name, got)
			}
			if n := f.count(`SELECT count(*) FROM vendor_invoices WHERE id = $1 AND status = 'VOIDED'`, id); n != 1 {
				t.Errorf("%s: the bill is not VOIDED after its void", shape.name)
			}
		case wantNet != 0 && shape.depth == 0:
			// The plain bill: its live entry is reversed whole.
			if v.status != http.StatusOK {
				t.Errorf("%s: void a bill whose entry stands live = %d, want 200: %s", shape.name, v.status, v.raw)
			}
			if after := f.count(`SELECT count(*) FROM gl_journal_entries`); after != before+1 {
				t.Errorf("%s: %d entries were written for a reversing void, want 1", shape.name, after-before)
			}
			if got, want := f.controlBalance(), control-cents; got != want {
				t.Errorf("%s: after the void: control %d, want %d", shape.name, got, want)
			}
			if got, want := f.openSum(), open-cents; got != want {
				t.Errorf("%s: after the void: open %d, want %d", shape.name, got, want)
			}
		default:
			// A live chain whose entry already holds a reversal row: no new
			// reversal can post, so the void is refused naming the shape.
			if v.status != http.StatusConflict {
				t.Errorf("%s: void a bill whose chain nets %d with a reversal row taken = %d, want 409: %s", shape.name, wantNet, v.status, v.raw)
			}
			if v.status == http.StatusConflict {
				if code, blockers, _ := errorOf(t, v); code != "invalid_state_transition" || blockers[0] != "reversal_not_posted" {
					t.Errorf("%s: the void refusal = %q %v, want reversal_not_posted", shape.name, code, blockers)
				}
			}
			if after := f.count(`SELECT count(*) FROM gl_journal_entries`); after != before {
				t.Errorf("%s: %d entries were written for a refused void", shape.name, after-before)
			}
			if n := f.count(`SELECT count(*) FROM vendor_invoices WHERE id = $1 AND status = 'APPROVED' AND revision = 2`, id); n != 1 {
				t.Errorf("%s: a refused void changed the bill", shape.name)
			}
			if got, want := f.controlBalance(), control; got != want {
				t.Errorf("%s: after the refused void: control %d, want the untouched %d", shape.name, got, want)
			}
			if got, want := f.openSum(), open; got != want {
				t.Errorf("%s: after the refused void: open %d, want the untouched %d", shape.name, got, want)
			}
			if got := chainNet(f, entry); got != wantNet {
				t.Errorf("%s: after the refused void: the chain nets %d, want the live %d under the bill that still owes it", shape.name, got, wantNet)
			}
		}
		if !f.balanced() {
			t.Errorf("%s: an entry does not balance after the void", shape.name)
		}

		// The void of the same shape with the link nulled (the legacy
		// unlinked bill migration 106 leaves behind).
		id, entry = buildChain(t, f, shape.name+"-UV", cents, shape.depth, shape.voided...)
		if got := chainNet(f, entry); got != wantNet {
			t.Fatalf("%s unlinked: the chain nets %d, want %d", shape.name, got, wantNet)
		}
		mustExec(t, f.db, `UPDATE vendor_invoices SET gl_entry_id = NULL WHERE id = $1`, id)
		control, open = f.controlBalance(), f.openSum()
		before = f.count(`SELECT count(*) FROM gl_journal_entries`)
		v = f.void(id, 2, "chain test")
		if wantNet == 0 {
			if v.status != http.StatusOK {
				t.Errorf("%s unlinked: void a bill whose chain nets zero = %d, want 200: %s", shape.name, v.status, v.raw)
			}
			if after := f.count(`SELECT count(*) FROM gl_journal_entries`); after != before {
				t.Errorf("%s unlinked: %d entries were written for a void with nothing to reverse", shape.name, after-before)
			}
			if got, want := f.controlBalance(), control; got != want {
				t.Errorf("%s unlinked: after the void: control %d, want the untouched %d", shape.name, got, want)
			}
			if got, want := f.openSum(), open-cents; got != want {
				t.Errorf("%s unlinked: after the void: open %d, want %d", shape.name, got, want)
			}
			if got := chainNet(f, entry); got != 0 {
				t.Errorf("%s unlinked: after the void: the chain still nets %d on 2010, want 0", shape.name, got)
			}
			if n := f.count(`SELECT count(*) FROM vendor_invoices WHERE id = $1 AND status = 'VOIDED'`, id); n != 1 {
				t.Errorf("%s unlinked: the bill is not VOIDED after its void", shape.name)
			}
		} else {
			if v.status != http.StatusConflict {
				t.Errorf("%s unlinked: void a bill whose chain nets %d = %d, want 409: %s", shape.name, wantNet, v.status, v.raw)
			}
			if v.status == http.StatusConflict {
				if code, blockers, _ := errorOf(t, v); code != "invalid_state_transition" || blockers[0] != "entry_not_linked" {
					t.Errorf("%s unlinked: the void refusal = %q %v, want entry_not_linked", shape.name, code, blockers)
				}
			}
			if after := f.count(`SELECT count(*) FROM gl_journal_entries`); after != before {
				t.Errorf("%s unlinked: %d entries were written for a refused void", shape.name, after-before)
			}
			if n := f.count(`SELECT count(*) FROM vendor_invoices WHERE id = $1 AND status = 'APPROVED' AND revision = 2`, id); n != 1 {
				t.Errorf("%s unlinked: a refused void changed the bill", shape.name)
			}
			if got, want := f.controlBalance(), control; got != want {
				t.Errorf("%s unlinked: after the refused void: control %d, want the untouched %d", shape.name, got, want)
			}
			if got, want := f.openSum(), open; got != want {
				t.Errorf("%s unlinked: after the refused void: open %d, want the untouched %d", shape.name, got, want)
			}
			if got := chainNet(f, entry); got != wantNet {
				t.Errorf("%s unlinked: after the refused void: the chain nets %d, want the live %d under the bill that still owes it", shape.name, got, wantNet)
			}
		}
		if !f.balanced() {
			t.Errorf("%s: an entry does not balance after the unlinked void", shape.name)
		}

	}
}

// chainNet is the net 2010 amount of the POSTED entries of the entry's whole
// reversal chain, in cents (the trial balance's own lens).
func chainNet(f *fixture, entry string) int64 {
	f.t.Helper()
	return f.count(`WITH RECURSIVE c(id) AS (SELECT id FROM gl_journal_entries WHERE id = $1
		UNION SELECT e.id FROM gl_journal_entries e JOIN c ON e.reverses_entry_id = c.id)
		SELECT ROUND(COALESCE(SUM(l.credit - l.debit), 0) * 100)::bigint FROM gl_journal_lines l
		JOIN gl_journal_entries e ON e.id = l.journal_entry_id AND e.status = 'POSTED'
		JOIN gl_accounts a ON a.id = l.account_id AND a.code = '2010' WHERE e.id IN (SELECT id FROM c)`, entry)
}

// buildChain approves a bill of cents and grows its linked entry's reversal
// chain to depth (1 is the entry's own reversal, 2 that reversal's, and so
// on), then voids the named chain positions through the GL routes. It
// answers the bill id and the linked entry.
func buildChain(t *testing.T, f *fixture, name string, cents int64, depth int, voided ...int) (string, string) {
	t.Helper()
	id, r1 := f.enter(name, "1", cents*100)
	if a := f.approve(id, r1); a.status != http.StatusOK {
		t.Fatal(a.raw)
	}
	entry := f.glEntryOf(id)
	cur := entry
	for i := 1; i <= depth; i++ {
		if rv := f.glReverse(cur); rv.status != http.StatusOK {
			t.Fatalf("%s: reverse chain depth %d = %d: %s", name, i, rv.status, rv.raw)
		}
		cur = f.glReversalOf(cur)
	}
	for _, p := range voided {
		e := entry
		for i := 0; i < p; i++ {
			e = f.glReversalOf(e)
		}
		if vv := f.glVoidEntry(e); vv.status != http.StatusOK {
			t.Fatalf("%s: void chain position %d = %d: %s", name, p, vv.status, vv.raw)
		}
	}
	return id, entry
}

// netMultiple is the net a chain of the depth leaves on 2010 in whole
// credits: the entry counts +1, its reversal -1, the reversal's reversal +1,
// and a voided position counts 0.
func netMultiple(depth int, voided ...int) int64 {
	gone := map[int]bool{}
	for _, p := range voided {
		gone[p] = true
	}
	var net int64
	for k := 0; k <= depth; k++ {
		if gone[k] {
			continue
		}
		if k%2 == 0 {
			net++
		} else {
			net--
		}
	}
	return net
}

// RULE (review r4 P3-2 on P1-1's chain lens): the approve decides on the
// same liveness as the void, never on the bill's status alone. A bill that
// already holds a live journal entry (a legacy PENDING row whose posting ran
// before the status moved, the link kept or lost by hand, whatever depth its
// reversal chain has reached) must not approve into a second credit: the
// approve is refused with the entry_not_linked blocker naming the live
// entry, and nothing is written. A bill whose chain holds no credit approves
// into the one credit it owes.
func TestWire_ApproveDecidesOnTheWholeReversalChain(t *testing.T) {
	testutil.LockOutboxTables(t)
	f := newFixture(t, testutil.RequireDB(t))
	shapes := []struct {
		name   string
		depth  int
		voided []int
	}{
		{"CHAIN-A-D0", 0, nil},
		{"CHAIN-A-D1", 1, nil},
		{"CHAIN-A-D2", 2, nil},
		{"CHAIN-A-D3", 3, nil},
		{"CHAIN-A-D1-R1V", 1, []int{1}},
		{"CHAIN-A-D2-R2V", 2, []int{2}},
		{"CHAIN-A-D3-R3V", 3, []int{3}},
		{"CHAIN-A-D2-R1V", 2, []int{1}},
		{"CHAIN-A-D3-R1V", 3, []int{1}},
	}
	for i, shape := range shapes {
		cents := int64(2100 + i*100)
		wantNet := netMultiple(shape.depth, shape.voided...) * cents
		for _, linked := range []bool{false, true} {
			arm := "-AL"
			if !linked {
				arm = "-AU"
			}
			id, entry := buildChain(t, f, shape.name+arm, cents, shape.depth, shape.voided...)
			if got, want := chainNet(f, entry), wantNet; got != want {
				t.Fatalf("%s: the chain nets %d, want %d (the shape itself is wrong)", shape.name, got, want)
			}
			// The legacy shape: the posting ran before the status moved, so
			// the bill sits PENDING over the live chain, the link kept or
			// nulled by hand.
			mustExec(t, f.db, `UPDATE vendor_invoices SET status = 'PENDING' WHERE id = $1`, id)
			if !linked {
				mustExec(t, f.db, `UPDATE vendor_invoices SET gl_entry_id = NULL WHERE id = $1`, id)
			}
			control, open := f.controlBalance(), f.openSum()
			before := f.count(`SELECT count(*) FROM gl_journal_entries`)
			a := f.approve(id, 2)
			if wantNet == 0 {
				if a.status != http.StatusOK {
					t.Errorf("%s approve linked=%v: approve a bill whose chain nets zero = %d, want 200: %s", shape.name, linked, a.status, a.raw)
				}
				if after := f.count(`SELECT count(*) FROM gl_journal_entries`); after != before+1 {
					t.Errorf("%s approve linked=%v: %d entries were written for the approve, want 1", shape.name, linked, after-before)
				}
				if got, want := f.controlBalance(), control+cents; got != want {
					t.Errorf("%s approve linked=%v: after the approve: control %d, want %d", shape.name, linked, got, want)
				}
				if got, want := f.openSum(), open+cents; got != want {
					t.Errorf("%s approve linked=%v: after the approve: open %d, want %d", shape.name, linked, got, want)
				}
				if got := chainNet(f, f.glEntryOf(id)); got != cents {
					t.Errorf("%s approve linked=%v: the new entry's chain nets %d, want the %d the bill owes", shape.name, linked, got, cents)
				}
				if n := f.count(`SELECT count(*) FROM vendor_invoices WHERE id = $1 AND status = 'APPROVED' AND amount_open * 100 = $2`, id, cents); n != 1 {
					t.Errorf("%s approve linked=%v: the approved bill does not owe its %d cents", shape.name, linked, cents)
				}
			} else {
				if a.status != http.StatusConflict {
					t.Errorf("%s approve linked=%v: approve a bill whose chain nets %d = %d, want 409: %s", shape.name, linked, wantNet, a.status, a.raw)
				}
				if a.status == http.StatusConflict {
					if code, blockers, _ := errorOf(t, a); code != "invalid_state_transition" || blockers[0] != "entry_not_linked" {
						t.Errorf("%s approve linked=%v: the refusal = %q %v, want entry_not_linked", shape.name, linked, code, blockers)
					}
					if !strings.Contains(string(a.raw), "journal entry #") {
						t.Errorf("%s approve linked=%v: the refusal does not name the live entry: %s", shape.name, linked, a.raw)
					}
				}
				if after := f.count(`SELECT count(*) FROM gl_journal_entries`); after != before {
					t.Errorf("%s approve linked=%v: %d entries were written for a refused approve", shape.name, linked, after-before)
				}
				if n := f.count(`SELECT count(*) FROM vendor_invoices WHERE id = $1 AND status = 'PENDING' AND revision = 2`, id); n != 1 {
					t.Errorf("%s approve linked=%v: a refused approve changed the bill", shape.name, linked)
				}
				if got, want := f.controlBalance(), control; got != want {
					t.Errorf("%s approve linked=%v: after the refused approve: control %d, want the untouched %d", shape.name, linked, got, want)
				}
				if got, want := f.openSum(), open; got != want {
					t.Errorf("%s approve linked=%v: after the refused approve: open %d, want the untouched %d", shape.name, linked, got, want)
				}
				if got := chainNet(f, entry); got != wantNet {
					t.Errorf("%s approve linked=%v: after the refused approve: the chain nets %d, want the live %d it held", shape.name, linked, got, wantNet)
				}
			}
			if !f.balanced() {
				t.Errorf("%s: an entry does not balance after the approve linked=%v", shape.name, linked)
			}
		}
	}
}

// The reproducer of review r4 P1-1, as the review wrote it: reverse the bill's
// entry, then reverse that reversal (a reinstatement), then void the bill.
// The credit is back in 2010, so the void must refuse, linked or not, and the
// bill must never sit VOIDED with its credit live.
func TestWire_VoidRefusesReinstatedEntry(t *testing.T) {
	testutil.LockOutboxTables(t)
	f := newFixture(t, testutil.RequireDB(t))
	for _, unlink := range []bool{false, true} {
		id, r1 := f.enter(fmt.Sprintf("V-REINST-%v", unlink), "1", 160000) // 16.00
		if a := f.approve(id, r1); a.status != http.StatusOK {
			t.Fatal(a.raw)
		}
		entry := f.glEntryOf(id)
		if rv := f.glReverse(entry); rv.status != http.StatusOK {
			t.Fatalf("reverse the entry = %d: %s", rv.status, rv.raw)
		}
		// Reverse the reversal: the original credit is reinstated in 2010.
		if rr := f.glReverse(f.glReversalOf(entry)); rr.status != http.StatusOK {
			t.Fatalf("reverse the reversal = %d: %s", rr.status, rr.raw)
		}
		if unlink {
			mustExec(t, f.db, `UPDATE vendor_invoices SET gl_entry_id = NULL WHERE id = $1`, id)
		}
		live := func() int64 {
			return f.count(`WITH RECURSIVE c(id) AS (SELECT id FROM gl_journal_entries WHERE id = $1
				UNION SELECT e.id FROM gl_journal_entries e JOIN c ON e.reverses_entry_id = c.id)
				SELECT ROUND(COALESCE(SUM(l.credit - l.debit), 0) * 100)::bigint FROM gl_journal_lines l
				JOIN gl_journal_entries e ON e.id = l.journal_entry_id AND e.status = 'POSTED'
				JOIN gl_accounts a ON a.id = l.account_id AND a.code = '2010' WHERE e.id IN (SELECT id FROM c)`, entry)
		}
		if got := live(); got != 1600 {
			t.Fatalf("the reinstated credit = %d, want 1600", got)
		}
		// The ties after the acts that built the shape: every entry balanced,
		// and the control account holding exactly what the open bills owe
		// (both arms share the fixture, so the tie is control against open).
		if !f.balanced() {
			t.Fatalf("unlinked=%v: an entry does not balance before the void", unlink)
		}
		control, open := f.controlBalance(), f.openSum()
		if control != open || open == 0 {
			t.Fatalf("unlinked=%v: the control %d against the open %d before the void, want the tie over a nonzero amount", unlink, control, open)
		}
		v := f.void(id, 2, "wrong bill")
		if v.status != http.StatusConflict {
			t.Errorf("unlinked=%v: void a bill whose credit was reinstated = %d, want 409: %s", unlink, v.status, v.raw)
		}
		if got := live(); got != 0 && f.count(`SELECT count(*) FROM vendor_invoices WHERE id = $1 AND status = 'VOIDED'`, id) == 1 {
			t.Errorf("unlinked=%v: the bill is VOIDED with %d cents of its credit live in 2010", unlink, got)
		}
		// The ties after the refused void: nothing moved (the review's r5
		// P3-2: the test asserted the status and the chain net, never the
		// ledger sums).
		if !f.balanced() {
			t.Errorf("unlinked=%v: an entry does not balance after the refused void", unlink)
		}
		if got := f.controlBalance(); got != control {
			t.Errorf("unlinked=%v: the control = %d after the refused void, want the untouched %d", unlink, got, control)
		}
		if got := f.openSum(); got != open {
			t.Errorf("unlinked=%v: the open = %d after the refused void, want the untouched %d", unlink, got, open)
		}
		if got := f.count(`SELECT count(*) FROM vendor_invoices WHERE id = $1 AND status = 'APPROVED'`, id); got != 1 {
			t.Errorf("unlinked=%v: %d APPROVED rows after the refused void, want the bill kept", unlink, got)
		}
	}
}

// RULE (review r4 P3-3): a void's reversal posts in the original entry's own
// currency, never in the bill's current one. The entry priced the credit
// into its currency when it posted, so undoing it in another currency leaves
// 2010 holding both halves in different currencies (the trial balance never
// sums across two). The shape is legacy only (no route changes a bill's
// currency), built here by hand like the review's probe.
func TestWire_VoidReversalKeepsTheEntryCurrency(t *testing.T) {
	testutil.LockOutboxTables(t)
	f := newFixture(t, testutil.RequireDB(t))
	id, r1 := f.enter("V-CUR-ENTRY", "1", 250000) // 25.00
	if a := f.approve(id, r1); a.status != http.StatusOK {
		t.Fatal(a.raw)
	}
	entry := f.glEntryOf(id)
	if got := f.count(`SELECT count(*) FROM gl_journal_entries WHERE id = $1 AND currency = 'USD'`, entry); got != 1 {
		t.Fatalf("the entry's currency = %d USD rows, want the setting's USD", got)
	}
	// The hand edit no route allows: the bill now says CAD over a USD entry.
	mustExec(t, f.db, `UPDATE vendor_invoices SET currency = 'CAD' WHERE id = $1`, id)
	if got, want := f.controlBalanceIn("USD"), int64(2500); got != want {
		t.Fatalf("the USD control = %d, want %d", got, want)
	}
	if got, want := f.openSumIn("CAD"), int64(2500); got != want {
		t.Fatalf("the CAD open = %d, want %d", got, want)
	}
	v := f.void(id, 2, "wrong currency")
	if v.status != http.StatusOK {
		t.Fatalf("void the hand edited bill = %d: %s", v.status, v.raw)
	}
	if n := f.count(`SELECT count(*) FROM gl_journal_entries WHERE reverses_entry_id = $1 AND currency = 'USD'`, entry); n != 1 {
		t.Errorf("the void's reversal is not in the entry's USD: %d USD reversals, want 1", n)
	}
	if !f.balanced() {
		t.Error("an entry does not balance")
	}
	for _, currency := range []string{"USD", "CAD"} {
		if got, want := f.controlBalanceIn(currency), int64(0); got != want {
			t.Errorf("the %s control = %d, want %d after the void", currency, got, want)
		}
		if got, want := f.openSumIn(currency), int64(0); got != want {
			t.Errorf("the %s open = %d, want %d after the void", currency, got, want)
		}
	}
}

// RULE (review r4 P3-1, the race r3 P3-1 read): an AP void racing a GL
// reverse of the linked entry must never answer 500. The GL reverse route
// refuses subledger-owned VENDOR_INVOICE entries outright (400, never a 500
// whatever the void does beside it), so the AP void wins every round: each
// round leaves exactly one reversal of the entry (the void's), the bill
// voided, and the tie holding.
func TestWire_VoidReverseRaceNever500(t *testing.T) {
	testutil.LockOutboxTables(t)
	f := newFixture(t, testutil.RequireDB(t))
	for round := 1; round <= 20; round++ {
		id, r1 := f.enter(fmt.Sprintf("V-RACE-%02d", round), "1", 1400000) // 140.00
		if a := f.approve(id, r1); a.status != http.StatusOK {
			t.Fatal(a.raw)
		}
		entry := f.glEntryOf(id)
		apRes, glRes := make(chan resp, 1), make(chan resp, 1)
		start := make(chan struct{})
		// Both goroutines go through doSafe, never f.do or f.void: a
		// transport fault must come back in resp.err for this goroutine to
		// fail the test with, never a t.Fatal inside the goroutine.
		go func() {
			<-start
			apRes <- f.doSafe("POST", "/api/v1/ap/invoices/"+id+"/transitions",
				map[string]any{"to": "voided", "revision": 2, "reason": "race round"})
		}()
		// The GL route is called directly here, never through f.glReverse:
		// its SQL fallback calls t.Fatal, which a goroutine must never do.
		go func() {
			<-start
			glRes <- f.doSafe("POST", "/api/v1/gl/journal-entries/"+entry+"/reverse", map[string]any{"reason": "race round"})
		}()
		close(start)
		ap, gl := <-apRes, <-glRes
		if ap.err != nil {
			t.Fatal(ap.err)
		}
		if gl.err != nil {
			t.Fatal(gl.err)
		}
		if gl.status != http.StatusBadRequest {
			t.Errorf("round %d: the GL reverse answered %d in the race, want 400 refusing the subledger-owned entry: %s", round, gl.status, gl.raw)
		}
		if ap.status != http.StatusOK {
			t.Errorf("round %d: the AP void answered %d, want 200 winning every round: %s", round, ap.status, ap.raw)
		}
		if n := f.count(`SELECT count(*) FROM gl_journal_entries WHERE reverses_entry_id = $1`, entry); n != 1 {
			t.Fatalf("round %d: %d reversals of the entry stand, want exactly the void's one", round, n)
		}
		if voided := f.count(`SELECT count(*) FROM vendor_invoices WHERE id = $1 AND status = 'VOIDED'`, id); voided != 1 {
			t.Fatalf("round %d: %d voided bills stand, want 1", round, voided)
		}
		if !f.balanced() {
			t.Errorf("round %d: an entry does not balance after the race", round)
		}
		if got, want := f.controlBalance(), f.openSum(); got != want || got != 0 {
			t.Errorf("round %d: control %d against open %d, want both 0 after the round", round, got, want)
		}
	}
}

// The reproducer of review r6 P2-1, as the review wrote it: a CAD bill whose
// entry the GL reverse route reversed (the reversal posted in the setting's
// USD before the fix, so the chain held CAD 20 and USD -20) must not void
// 200 over a stranded CAD credit: the liveness net never sums across two
// currencies, so the chain reads live in its CAD half and the void refuses.
func TestWire_VoidRefusesCrossCurrencyReversedEntry(t *testing.T) {
	testutil.LockOutboxTables(t)
	f := newFixture(t, testutil.RequireDB(t))
	body := f.createBody("V-XCUR", "1", 200000) // 20.00
	body["currency"] = "CAD"                    // the setting stays USD
	c := f.do("POST", "/api/v1/ap/invoices", body)
	if c.status != http.StatusCreated {
		t.Fatal(c.raw)
	}
	id := str(t, c.body, "id")
	if a := f.approve(id, rev(t, c)); a.status != http.StatusOK {
		t.Fatal(a.raw)
	}
	entry := f.glEntryOf(id)
	if rv := f.glReverse(entry); rv.status != http.StatusOK {
		t.Fatalf("reverse the entry = %d: %s", rv.status, rv.raw)
	}
	v := f.void(id, 2, "wrong bill")
	if v.status == http.StatusOK && f.controlBalanceIn("CAD") != f.openSumIn("CAD") {
		t.Errorf("void = 200 and the bill is VOIDED with CAD control %d against CAD open %d: the CAD credit is stranded",
			f.controlBalanceIn("CAD"), f.openSumIn("CAD"))
	}
}

// RULE (review r6 P2-1, the belt): the chain liveness nets per currency,
// never across two (the trial balance never sums across two currencies), so
// a chain holding two currencies whose nets cancel still reads live in the
// currency that holds the amount, however the chain arose (legacy rows
// included). The shape is built directly in the ledger: the GL route's own
// reversal carries the entry's currency since the route's fix, so no route
// can build it any more. The void, its unlinked arm and the legacy approve
// all decide on the same per currency net.
func TestWire_VoidRefusesCrossCurrencyChainBuiltInTheLedger(t *testing.T) {
	testutil.LockOutboxTables(t)
	f := newFixture(t, testutil.RequireDB(t))
	// handReversal inserts, directly in the ledger, a POSTED reversal of the
	// entry in the named currency: the wrong currency reversal shape.
	handReversal := func(entry uuid.UUID, currency string) {
		t.Helper()
		id := uuid.New()
		mustExec(t, f.db, `INSERT INTO gl_journal_entries (id, entry_date, memo, source, source_ref_id, branch_id, status, posted_by, reverses_entry_id, created_at, updated_at, currency)
			VALUES ($1, now(), 'hand built wrong currency reversal', 'REVERSAL', $2,
				(SELECT value::uuid FROM system_settings WHERE key = 'default_branch_id'), 'POSTED', 'test', $2, now(), now(), $3)`, id, entry, currency)
		mustExec(t, f.db, `INSERT INTO gl_journal_lines (id, journal_entry_id, account_id, description, debit, credit, branch_id)
			SELECT gen_random_uuid(), $1, l.account_id, 'hand built reversal: ' || l.description, l.credit, l.debit,
				(SELECT value::uuid FROM system_settings WHERE key = 'default_branch_id')
			FROM gl_journal_lines l WHERE l.journal_entry_id = $2`, id, entry)
	}
	chainNetIn := func(entry, currency string) int64 {
		return f.count(`WITH RECURSIVE c(id) AS (SELECT id FROM gl_journal_entries WHERE id = $1
			UNION SELECT e.id FROM gl_journal_entries e JOIN c ON e.reverses_entry_id = c.id)
			SELECT ROUND(COALESCE(SUM(l.credit - l.debit), 0) * 100)::bigint FROM gl_journal_lines l
			JOIN gl_journal_entries e ON e.id = l.journal_entry_id AND e.status = 'POSTED'
			JOIN gl_accounts a ON a.id = l.account_id AND a.code = '2010' AND e.currency = $2
			WHERE e.id IN (SELECT id FROM c)`, entry, currency)
	}

	// The linked arm: a CAD bill whose entry's reversal was built in USD.
	body := f.createBody("V-XCUR-LEDGER", "1", 200000) // 20.00
	body["currency"] = "CAD"                           // the setting stays USD
	c := f.do("POST", "/api/v1/ap/invoices", body)
	if c.status != http.StatusCreated {
		t.Fatal(c.raw)
	}
	id := str(t, c.body, "id")
	if a := f.approve(id, rev(t, c)); a.status != http.StatusOK {
		t.Fatal(a.raw)
	}
	entry := f.glEntryOf(id)
	if got, want := f.controlBalanceIn("CAD"), int64(2000); got != want {
		t.Fatalf("the CAD control after the approve = %d, want %d", got, want)
	}
	if got, want := f.openSumIn("CAD"), int64(2000); got != want {
		t.Fatalf("the CAD open after the approve = %d, want %d", got, want)
	}
	if !f.balanced() {
		t.Fatal("an entry does not balance after the approve")
	}
	handReversal(uuid.MustParse(entry), "USD")
	if got, want := chainNetIn(entry, "CAD"), int64(2000); got != want {
		t.Fatalf("the chain nets %d in CAD, want %d (the hand edit)", got, want)
	}
	if got, want := chainNetIn(entry, "USD"), int64(-2000); got != want {
		t.Fatalf("the chain nets %d in USD, want %d (the hand edit)", got, want)
	}
	entries := f.count(`SELECT count(*) FROM gl_journal_entries WHERE id IN (`+fixtureEntries+`)`, f.vendor)
	v := f.void(id, 2, "wrong currency chain")
	if v.status != http.StatusConflict {
		t.Fatalf("void a bill whose chain holds two currencies = %d, want 409: %s", v.status, v.raw)
	}
	if _, blockers, _ := errorOf(t, v); len(blockers) != 1 || blockers[0] != "reversal_not_posted" {
		t.Errorf("the void's blockers = %v, want [reversal_not_posted]", blockers)
	}
	if got := f.count(`SELECT count(*) FROM gl_journal_entries WHERE id IN (`+fixtureEntries+`)`, f.vendor); got != entries {
		t.Errorf("the refused void wrote %d entries, want none", got-entries)
	}
	if got := f.count(`SELECT count(*) FROM vendor_invoices WHERE id = $1 AND status = 'APPROVED'`, id); got != 1 {
		t.Errorf("the bill left APPROVED: %d rows say so", got)
	}
	if got, want := f.controlBalanceIn("CAD"), int64(2000); got != want {
		t.Errorf("the CAD control after the refusal = %d, want %d (nothing written)", got, want)
	}
	if got, want := f.openSumIn("CAD"), int64(2000); got != want {
		t.Errorf("the CAD open after the refusal = %d, want %d (nothing written)", got, want)
	}
	if got, want := f.controlBalanceIn("USD"), int64(-2000); got != want {
		t.Errorf("the USD control after the refusal = %d, want %d (the hand edit's gap, untouched)", got, want)
	}
	if !f.balanced() {
		t.Error("an entry does not balance after the refusal")
	}

	// The unlinked arm of the same shape: the liveness read of a bill with no
	// linked entry nets per currency too, so the unlink cannot make the CAD
	// half of the chain disappear from the decision.
	mustExec(t, f.db, `UPDATE vendor_invoices SET gl_entry_id = NULL WHERE id = $1`, id)
	v2 := f.void(id, 2, "wrong currency chain, unlinked")
	if v2.status != http.StatusConflict {
		t.Fatalf("void the unlinked bill = %d, want 409: %s", v2.status, v2.raw)
	}
	if _, blockers, _ := errorOf(t, v2); len(blockers) != 1 || blockers[0] != "entry_not_linked" {
		t.Errorf("the unlinked void's blockers = %v, want [entry_not_linked]", blockers)
	}
	if got := f.count(`SELECT count(*) FROM gl_journal_entries WHERE id IN (`+fixtureEntries+`)`, f.vendor); got != entries {
		t.Errorf("the refused unlinked void wrote %d entries, want none", got-entries)
	}
	if !f.balanced() {
		t.Error("an entry does not balance after the unlinked refusal")
	}

	// The legacy approve arm: a PENDING bill whose hand built chain holds two
	// currencies is refused, never approved into a second credit.
	body2 := f.createBody("V-XCUR-PENDING", "1", 200000) // 20.00
	body2["currency"] = "CAD"
	c2 := f.do("POST", "/api/v1/ap/invoices", body2)
	if c2.status != http.StatusCreated {
		t.Fatal(c2.raw)
	}
	id2 := str(t, c2.body, "id")
	e2 := uuid.New()
	mustExec(t, f.db, `INSERT INTO gl_journal_entries (id, entry_date, memo, source, source_ref_id, branch_id, status, posted_by, created_at, updated_at, currency)
		VALUES ($1, now(), 'hand built bill entry', 'VENDOR_INVOICE', $2, (SELECT value::uuid FROM system_settings WHERE key = 'default_branch_id'), 'POSTED', 'test', now(), now(), 'CAD')`, e2, id2)
	mustExec(t, f.db, `INSERT INTO gl_journal_lines (id, journal_entry_id, account_id, description, debit, credit, branch_id)
		VALUES (gen_random_uuid(), $1, $2, 'hand built bill line', 20.00, 0, (SELECT value::uuid FROM system_settings WHERE key = 'default_branch_id')),
		       (gen_random_uuid(), $1, (SELECT id FROM gl_accounts WHERE code = '2010'), 'hand built bill line', 0, 20.00, (SELECT value::uuid FROM system_settings WHERE key = 'default_branch_id'))`, e2, f.expense)
	handReversal(e2, "USD")
	a2 := f.approve(id2, 1)
	if a2.status != http.StatusConflict {
		t.Fatalf("approve a pending bill whose chain holds two currencies = %d, want 409: %s", a2.status, a2.raw)
	}
	if _, blockers, _ := errorOf(t, a2); len(blockers) != 1 || blockers[0] != "entry_not_linked" {
		t.Errorf("the approve's blockers = %v, want [entry_not_linked]", blockers)
	}
	if got, want := f.controlBalanceIn("CAD"), int64(4000); got != want {
		t.Errorf("the CAD control after the refused approve = %d, want %d (the two hand shapes, nothing written)", got, want)
	}
	if got, want := f.openSumIn("CAD"), int64(2000); got != want {
		t.Errorf("the CAD open after the refused approve = %d, want %d (the pending bill owes nothing yet)", got, want)
	}
	if !f.balanced() {
		t.Error("an entry does not balance after the refused approve")
	}
}

// RULE (review r6 P3-1): an AP void racing a GL void of the linked entry must
// never answer 500. The void's ledger read can see the entry POSTED while the
// GL void commits beneath it, and the reversal it then books meets an entry
// that is no longer POSTED: that loss maps to the same 409 reversal_not_posted
// as the other lost races, nothing written, and the retry voids with nothing
// to reverse. Fifty rounds, never a 500 on either side.
func TestWire_VoidVoidRaceNever500(t *testing.T) {
	testutil.LockOutboxTables(t)
	f := newFixture(t, testutil.RequireDB(t))
	for round := 1; round <= 50; round++ {
		id, r1 := f.enter(fmt.Sprintf("V-VRACE-%02d", round), "1", 1400000) // 140.00
		if a := f.approve(id, r1); a.status != http.StatusOK {
			t.Fatal(a.raw)
		}
		entry := f.glEntryOf(id)
		controlBefore, openBefore := f.controlBalanceIn("USD"), f.openSumIn("USD")
		apRes, glRes := make(chan resp, 1), make(chan resp, 1)
		start := make(chan struct{})
		go func() { <-start; apRes <- f.voidSafe(id, 2, "race round") }()
		go func() { <-start; glRes <- f.glVoidEntrySafe(entry) }()
		close(start)
		ap, gl := <-apRes, <-glRes
		if ap.err != nil {
			t.Fatalf("round %d: AP void request failed: %v", round, ap.err)
		}
		if gl.err != nil {
			t.Fatalf("round %d: GL void request failed: %v", round, gl.err)
		}
		if ap.status >= 500 {
			t.Errorf("round %d: the AP void answered %d in the race: %s", round, ap.status, ap.raw)
		}
		if gl.status >= 500 {
			t.Errorf("round %d: the GL void answered %d in the race: %s", round, gl.status, gl.raw)
		}
		if ap.status != http.StatusOK && ap.status != http.StatusConflict {
			t.Errorf("round %d: the AP void answered %d, want 200 or 409: %s", round, ap.status, ap.raw)
		}
		if ap.status == http.StatusConflict {
			_, blockers, _ := errorOf(t, ap)
			if len(blockers) != 1 || blockers[0] != "reversal_not_posted" {
				t.Errorf("round %d: the lost race's blockers = %v, want [reversal_not_posted]", round, blockers)
			}
			// Nothing was written by the refused void: the bill is still there
			// to void, and the retry voids with nothing to reverse.
			before := f.count(`SELECT count(*) FROM gl_journal_entries WHERE id IN (`+fixtureEntries+`)`, f.vendor)
			retry := f.void(id, 2, "race round retry")
			if retry.status != http.StatusOK {
				t.Errorf("round %d: the retry void after the GL void = %d, want 200: %s", round, retry.status, retry.raw)
			}
			if after := f.count(`SELECT count(*) FROM gl_journal_entries WHERE id IN (`+fixtureEntries+`)`, f.vendor); after != before {
				t.Errorf("round %d: %d entries were written by the retry void with nothing to reverse", round, after-before)
			}
		}
		if !f.balanced() {
			t.Errorf("round %d: an entry does not balance after the race", round)
		}
		// The tie after the round is asserted on the refused arm alone, as a
		// delta against the round's own snapshot: the GL void that won
		// beneath the refusal is the one ledger move (its entry no longer
		// sums in the control), the retry then voids the bill with nothing to
		// reverse, and the AP act wrote nothing. Where the AP void won, the
		// GL void of the entry beside the void's own reversal is the known
		// out of scope shape (the GL item), so no whole tie holds there.
		if ap.status == http.StatusConflict {
			if got, want := f.controlBalanceIn("USD"), controlBefore-14000; got != want {
				t.Errorf("round %d: USD control %d after the refused void, want %d (the GL void's gap alone)", round, got, want)
			}
			if got, want := f.openSumIn("USD"), openBefore-14000; got != want {
				t.Errorf("round %d: USD open %d after the refused void and its retry, want %d (the retry closed the round's bill)", round, got, want)
			}
		}
	}
}

// RULE (review r5 P3-1): a net negative chain (the original entry was
// reversed and then voided, so the reversal's debit stands alone on 2010) is
// refused by words that are true: the liveness net may stand as a credit or
// as a debit, so every refusal names a net amount on accounts payable and
// none says a credit is live where a debit stands.
func TestWire_VoidRefusalWordsMatchANetNegativeChain(t *testing.T) {
	testutil.LockOutboxTables(t)
	f := newFixture(t, testutil.RequireDB(t))
	id, r1 := f.enter("V-NEGNET", "1", 160000) // 16.00
	if a := f.approve(id, r1); a.status != http.StatusOK {
		t.Fatal(a.raw)
	}
	entry := f.glEntryOf(id)
	if rv := f.glReverse(entry); rv.status != http.StatusOK {
		t.Fatalf("reverse the entry = %d: %s", rv.status, rv.raw)
	}
	if vv := f.glVoidEntry(entry); vv.status != http.StatusOK {
		t.Fatalf("void the original entry = %d: %s", vv.status, vv.raw)
	}
	net := func() int64 {
		return f.count(`WITH RECURSIVE c(id) AS (SELECT id FROM gl_journal_entries WHERE id = $1
			UNION SELECT e.id FROM gl_journal_entries e JOIN c ON e.reverses_entry_id = c.id)
			SELECT ROUND(COALESCE(SUM(l.credit - l.debit), 0) * 100)::bigint FROM gl_journal_lines l
			JOIN gl_journal_entries e ON e.id = l.journal_entry_id AND e.status = 'POSTED'
			JOIN gl_accounts a ON a.id = l.account_id AND a.code = '2010' WHERE e.id IN (SELECT id FROM c)`, entry)
	}
	if got := net(); got != -1600 {
		t.Fatalf("the chain nets %d, want -1600 (the reversal's debit alone stands)", got)
	}
	entries := f.count(`SELECT count(*) FROM gl_journal_entries WHERE id IN (`+fixtureEntries+`)`, f.vendor)
	words := func(act string, r resp) {
		t.Helper()
		if r.status != http.StatusConflict {
			t.Fatalf("%s = %d, want 409: %s", act, r.status, r.raw)
		}
		e, ok := r.body["error"].(map[string]any)
		if !ok {
			t.Fatalf("%s: no error envelope in %s", act, r.raw)
		}
		msg, _ := e["message"].(string)
		if !strings.Contains(msg, "net amount") && !strings.Contains(msg, "nonzero") {
			t.Errorf("%s: the refusal's message does not name the net amount that stands: %s", act, msg)
		}
		if strings.Contains(msg, "credit") {
			t.Errorf("%s: the refusal's message says a credit is live while a debit stands: %s", act, msg)
		}
	}
	// The linked arm: the reversal's debit stands, the entry's one reversal
	// row is taken, so the void is refused.
	words("the linked void", f.void(id, 2, "wrong bill"))
	// The unlinked arm: the same chain read through the unlinked predicate.
	mustExec(t, f.db, `UPDATE vendor_invoices SET gl_entry_id = NULL WHERE id = $1`, id)
	words("the unlinked void", f.void(id, 2, "wrong bill, unlinked"))
	// The approve arm: a hand set PENDING bill over the same chain.
	mustExec(t, f.db, `UPDATE vendor_invoices SET status = 'PENDING' WHERE id = $1`, id)
	words("the approve", f.approve(id, 2))
	// Every refusal wrote nothing and the ties did not move.
	if got := f.count(`SELECT count(*) FROM gl_journal_entries WHERE id IN (`+fixtureEntries+`)`, f.vendor); got != entries {
		t.Errorf("the three refusals wrote %d entries, want none", got-entries)
	}
	if got := net(); got != -1600 {
		t.Errorf("the chain nets %d after the refusals, want the untouched -1600", got)
	}
	if !f.balanced() {
		t.Error("an entry does not balance after the refusals")
	}
	if got, want := f.controlBalance(), int64(-1600); got != want {
		t.Errorf("the control = %d after the refusals, want the GL acts' %d", got, want)
	}
	if got, want := f.openSum(), int64(0); got != want {
		t.Errorf("the open = %d after the refusals, want %d (the bill is PENDING)", got, want)
	}
}

// RULE (ADR 0008 section 11): the branch wall on every read and write. A
// caller held to one branch finds another branch's bill a 404 on the by-id
// routes and never in the list; a create naming a branch it may not target is
// a 403 naming branch_id.
func TestWire_BranchWall(t *testing.T) {
	testutil.LockOutboxTables(t)
	f := newFixture(t, testutil.RequireDB(t))
	other := uuid.New()
	mustExec(t, f.db, `INSERT INTO locations (id, type, code) VALUES ($1, 'BRANCH', $2)`, other, "C41B-"+other.String()[:8])
	t.Cleanup(func() {
		mustExec(t, f.db, `UPDATE vendor_invoices SET branch_id = `+defaultBranch+` WHERE branch_id = $1`, other)
		mustExec(t, f.db, `UPDATE gl_journal_lines SET branch_id = `+defaultBranch+` WHERE branch_id = $1`, other)
		mustExec(t, f.db, `UPDATE gl_journal_entries SET branch_id = `+defaultBranch+` WHERE branch_id = $1`, other)
		mustExec(t, f.db, `DELETE FROM locations WHERE id = $1`, other)
	})
	id, _ := f.enter("V-WALL", "1", 100000)
	mustExec(t, f.db, `UPDATE vendor_invoices SET branch_id = $2 WHERE id = $1`, id, other)

	wall := []string{"X-Test-Branch", f.branch.String()}
	if g := f.do("GET", "/api/v1/ap/invoices/"+id, nil, wall...); g.status != http.StatusNotFound {
		t.Errorf("a held caller reads another branch's bill: %d, want 404", g.status)
	}
	if ta := f.do("POST", "/api/v1/ap/invoices/"+id+"/transitions",
		map[string]any{"to": "approved", "revision": 1}, wall...); ta.status != http.StatusNotFound {
		t.Errorf("a held caller approves another branch's bill: %d, want 404", ta.status)
	}
	if tv := f.do("POST", "/api/v1/ap/invoices/"+id+"/transitions",
		map[string]any{"to": "voided", "revision": 1, "reason": "x"}, wall...); tv.status != http.StatusNotFound {
		t.Errorf("a held caller voids another branch's bill: %d, want 404", tv.status)
	}
	list := f.do("GET", "/api/v1/ap/invoices?limit=50&vendor_id="+f.vendor.String(), nil, wall...)
	if list.status != 200 {
		t.Fatalf("list = %d", list.status)
	}
	for _, it := range list.body["items"].([]any) {
		if it.(map[string]any)["id"].(string) == id {
			t.Error("the walled list shows the other branch's bill")
		}
	}
	foreign := f.do("POST", "/api/v1/ap/invoices", map[string]any{
		"vendor_id": f.vendor.String(), "vendor_invoice_number": "V-FOREIGN", "branch_id": other.String(),
		"invoice_date": "2026-01-10", "due_date": "2026-02-10",
		"lines": []map[string]any{f.lineBody("1", 1000)}}, wall...)
	if foreign.status != http.StatusForbidden {
		t.Errorf("create at a foreign branch = %d, want 403", foreign.status)
	}
	if code, _, fields := errorOf(t, foreign); code != "forbidden" || len(fields) == 0 || fields[0] != "branch_id" {
		t.Errorf("create at a foreign branch = %q %v", code, fields)
	}
	// The same caller creates at its own branch.
	if r := f.do("POST", "/api/v1/ap/invoices", map[string]any{
		"vendor_id": f.vendor.String(), "vendor_invoice_number": "V-OWN", "branch_id": f.branch.String(),
		"invoice_date": "2026-01-10", "due_date": "2026-02-10",
		"lines": []map[string]any{f.lineBody("1", 1000)}}, wall...); r.status != http.StatusCreated {
		t.Errorf("create at the caller's branch = %d, want 201: %s", r.status, r.raw)
	}

	// RULE (ADR 0008 section 11): the wall holds on the aging report, the
	// payments list and the payment act itself, for a caller bound by
	// user_locations and for a key bound to one branch. The foreign bill stays
	// approved and owing (moved above); a payment lands on it through the
	// unwalled administrator so the two reads have something to leak.
	home, rh := f.enter("V-WALL-HOME", "1", 100000)
	if ha := f.approve(home, rh); ha.status != http.StatusOK {
		t.Fatal(ha.raw)
	}
	if fa := f.approve(id, 1); fa.status != http.StatusOK {
		t.Fatalf("the administrator approves the foreign bill = %d: %s", fa.status, fa.raw)
	}
	if pa := f.pay(400, id); pa.status != http.StatusCreated {
		t.Fatalf("the administrator's payment on the foreign bill = %d: %s", pa.status, pa.raw)
	}
	// A legacy payment with no application rows (the base wrote one whenever
	// invoice_ids was empty, and left the rest of an amount unapplied): the
	// wall filters a payment only through the bills it applied to, so a walled
	// caller cannot see it, but an unwalled caller must (an administrator with
	// no header, an unbound key, and every caller while the single branch
	// switch is off).
	mustExec(t, f.db, `INSERT INTO ap_payments (id, vendor_id, amount, method, payment_date, status, created_at)
		VALUES ($1, $2, 1.23, 'CHECK', '2026-01-14', 'COMPLETE', NOW())`, uuid.New(), f.vendor)
	if got, want := f.controlBalance(), f.openSum(); got != want {
		t.Fatalf("after the payment on the foreign bill: control %d, open %d", got, want)
	}
	if !f.balanced() {
		t.Error("the payment entry on the foreign bill does not balance")
	}
	// A user bound to the home branch through user_locations.
	bound := "c41b-bound-" + uuid.NewString()[:8]
	mustExec(t, f.db, `INSERT INTO user_locations (user_sub, branch_id) VALUES ($1, $2)`, bound, f.branch)
	agingTotal := func(headers ...string) int64 {
		t.Helper()
		r := f.do("GET", "/api/v1/ap/aging", nil, headers...)
		if r.status != 200 {
			t.Fatalf("aging = %d: %s", r.status, r.raw)
		}
		var rows []map[string]any
		if err := json.Unmarshal(r.raw, &rows); err != nil {
			t.Fatalf("aging body: %v", err)
		}
		for _, row := range rows {
			if row["vendor_id"] == f.vendor.String() {
				return int64(row["total"].(float64))
			}
		}
		return -1
	}
	for name, headers := range map[string][]string{
		"a user bound by user_locations": {"X-Test-Sub", bound, "X-Test-User-Bound", "1"},
		"a key bound to one branch":      wall,
	} {
		if got := agingTotal(headers...); got != 1010 {
			t.Errorf("the aging total behind the wall for %s = %d, want the caller's branch bills only (1010 cents)", name, got)
		}
		pl := f.do("GET", "/api/v1/ap/payments?vendor_id="+f.vendor.String(), nil, headers...)
		if pl.status != 200 {
			t.Fatalf("payments list = %d: %s", pl.status, pl.raw)
		}
		if n := strings.Count(string(pl.raw), `"id"`); n != 0 {
			t.Errorf("the payments list behind the wall for %s shows %d payment(s) applied to another branch's bill", name, n)
		}
		if pr := f.payHeaders(headers, 400, id); pr.status != http.StatusBadRequest {
			t.Errorf("a payment on another branch's bill for %s = %d, want 400", name, pr.status)
		}
	}
	// Unwalled, both reads carry the other branch's bill and its payment, and
	// the payments list carries the applicationless legacy payment too: an
	// open wall lists every payment.
	if got := agingTotal(); got != 1610 {
		t.Errorf("the unwalled aging total = %d, want every branch's 1610 cents", got)
	}
	if pl := f.do("GET", "/api/v1/ap/payments?vendor_id="+f.vendor.String(), nil); strings.Count(string(pl.raw), `"id"`) != 2 {
		t.Errorf("the unwalled payments list = %s, want the applied payment and the applicationless legacy one", pl.raw)
	}
}

// RULE (ADR 0001 section 9): idempotency through the existing middleware: the
// same create twice with one key returns the first response and makes one row
// and one event; the same key with another body is 422.
func TestWire_Idempotency(t *testing.T) {
	testutil.LockOutboxTables(t)
	f := newFixture(t, testutil.RequireDB(t))
	key := "c41b-" + uuid.NewString()
	body := f.createBody("V-IDEM", "1", 1000)
	first := f.do("POST", "/api/v1/ap/invoices", body, "Idempotency-Key", key)
	if first.status != http.StatusCreated {
		t.Fatalf("first = %d: %s", first.status, first.raw)
	}
	again := f.do("POST", "/api/v1/ap/invoices", body, "Idempotency-Key", key)
	if again.status != http.StatusCreated || again.header.Get("Idempotency-Replayed") != "true" {
		t.Fatalf("replay = %d replayed=%q, want 201 replayed", again.status, again.header.Get("Idempotency-Replayed"))
	}
	if again.body["id"] != first.body["id"] {
		t.Errorf("replay answered %v, want the first bill %v", again.body["id"], first.body["id"])
	}
	if n := f.count(`SELECT count(*) FROM vendor_invoices WHERE vendor_id = $1`, f.vendor); n != 1 {
		t.Errorf("%d bills for one keyed create", n)
	}
	if n := f.count(`SELECT count(*) FROM events_outbox WHERE entity_type = 'vendor_invoice' AND entity_id = $1`, first.body["id"]); n != 1 {
		t.Errorf("%d events for one keyed create", n)
	}
	other := f.createBody("V-IDEM-2", "1", 1000)
	if r := f.do("POST", "/api/v1/ap/invoices", other, "Idempotency-Key", key); r.status != http.StatusUnprocessableEntity {
		t.Errorf("key reuse with another body = %d, want 422", r.status)
	}
}

// The aging read: the buckets come from amount_open, and a voided bill owes
// nothing anywhere.
func TestWire_Aging(t *testing.T) {
	testutil.LockOutboxTables(t)
	f := newFixture(t, testutil.RequireDB(t))
	id, r1 := f.enter("V-AGE", "1", 1000) // 10 cents open
	if a := f.approve(id, r1); a.status != http.StatusOK {
		t.Fatal(a.raw)
	}
	r := f.do("GET", "/api/v1/ap/aging", nil)
	if r.status != 200 {
		t.Fatalf("aging = %d: %s", r.status, r.raw)
	}
	var rows []map[string]any
	if err := json.Unmarshal(r.raw, &rows); err != nil {
		t.Fatalf("aging body: %v", err)
	}
	found := false
	for _, row := range rows {
		if row["vendor_id"] == f.vendor.String() {
			found = true
			if row["total"].(float64) != 10 {
				t.Errorf("aging total = %v, want 10 cents", row["total"])
			}
		}
	}
	if !found {
		t.Errorf("the aging report does not list the fixture's vendor: %s", r.raw)
	}
}

// RULE (review r3 P2-2): the in-process listing matching reads carries each
// bill's own lines in position order, with and without the status filter. The
// lines come from one second query keyed by the page's ids, so a mutant that
// matches no line, or crosses lines between bills, fails here.
func TestListVendorInvoices_EachBillCarriesItsOwnLines(t *testing.T) {
	testutil.LockOutboxTables(t)
	f := newFixture(t, testutil.RequireDB(t))
	enterLines := func(number string, descriptions ...string) (string, int64) {
		t.Helper()
		body := f.createBody(number, "1", 1000)
		var lines []map[string]any
		for _, d := range descriptions {
			l := f.lineBody("1", 1000)
			l["description"] = d
			lines = append(lines, l)
		}
		body["lines"] = lines
		r := f.do("POST", "/api/v1/ap/invoices", body)
		if r.status != http.StatusCreated {
			t.Fatalf("enter %s = %d: %s", number, r.status, r.raw)
		}
		return str(t, r.body, "id"), rev(t, r)
	}
	oneID, oneRev := enterLines("V-LF1", "one-a")
	threeID, _ := enterLines("V-LF3", "three-a", "three-b", "three-c")
	if r := f.approve(oneID, oneRev); r.status != http.StatusOK {
		t.Fatalf("approve = %d: %s", r.status, r.raw)
	}
	if !f.balanced() || f.controlBalance() != f.openSum() {
		t.Fatalf("after the approve: balanced %v, control %d, open %d", f.balanced(), f.controlBalance(), f.openSum())
	}
	want := map[string][]string{oneID: {"one-a"}, threeID: {"three-a", "three-b", "three-c"}}
	check := func(label, status string, wantIDs ...string) {
		t.Helper()
		got, err := f.svc.ListVendorInvoices(t.Context(), &f.vendor, status)
		if err != nil {
			t.Fatalf("%s: %v", label, err)
		}
		if len(got) != len(wantIDs) {
			t.Fatalf("%s: %d bills, want %d", label, len(got), len(wantIDs))
		}
		for _, inv := range got {
			wantLines, ok := want[inv.ID.String()]
			if !ok {
				t.Fatalf("%s: unexpected bill %s", label, inv.ID)
			}
			var descs []string
			for _, l := range inv.Lines {
				descs = append(descs, l.Description)
			}
			if fmt.Sprint(descs) != fmt.Sprint(wantLines) {
				t.Errorf("%s: bill %s carries lines %v, want %v", label, inv.ID, descs, wantLines)
			}
		}
	}
	check("every status", "", oneID, threeID)
	check("approved only", "APPROVED", oneID)
	check("pending only", "PENDING", threeID)
}
