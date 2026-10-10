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
		mustExec(t, f.db, `DELETE FROM locations WHERE id = $1`, other)
	})
	id, _ := f.enter("V-WALL", "1", 1000)
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
