// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package invoice_test

// The credit memo on the wire contract (ADR 0001) and ADR 0005 section 6.3: a
// draft with no number, lines that return invoice lines or give a price back,
// the number minted at post, the tax never above what the invoice charged, the
// restock with its entry at the original cost, and the void.

import (
	"fmt"
	"net/http"
	"regexp"
	"strings"
	"testing"

	"github.com/gablelbm/gable/internal/testutil"
	"github.com/google/uuid"
)

var creditNumberPattern = regexp.MustCompile(`^CM-\d{6,}$`)

// RULE (IN-1.1, IN-1.2, IN-1.3, IN-1.4): the create answers 201 with Location, a
// draft with number null and lowercase status, negative totals, the lines in the
// shared shape with the invoice line they return, the revision and ETag.
func TestCreditMemoCreateShape(t *testing.T) {
	testutil.LockOutboxTables(t)
	db := testutil.RequireDB(t)
	f := newFixture(t, db)
	invID, _ := f.invoice("10")
	line := f.firstLineID(invID)
	r := f.do("POST", "/api/v1/credit-memos", f.creditBody(invID, returnLine(line, "-4", true)))
	if r.status != http.StatusCreated {
		t.Fatalf("create = %d: %s", r.status, r.raw)
	}
	b := r.body
	id := str(t, b, "id")
	if r.header.Get("Location") != "/api/v1/credit-memos/"+id {
		t.Errorf("Location = %q", r.header.Get("Location"))
	}
	if r.header.Get("ETag") != `"1"` || rev(t, r) != 1 {
		t.Errorf("ETag %q revision %d, want \"1\"", r.header.Get("ETag"), rev(t, r))
	}
	for key, want := range map[string]string{"status": "draft", "reason_code": "return", "currency": "USD", "invoice_id": invID} {
		if got := str(t, b, key); got != want {
			t.Errorf("%s = %q, want %q", key, got, want)
		}
	}
	// 4 of 10 on a 55.00 line: 22.00, tax 8.875% of 2200 = 195.25 -> 195 (not the last piece)
	for key, want := range map[string]int64{"subtotal_cents": -2200, "tax_cents": -195, "total_cents": -2395, "open_cents": 0} {
		if got := num(t, b, key); got != want {
			t.Errorf("%s = %d, want %d", key, got, want)
		}
	}
	for _, key := range []string{"number", "pos_return_id", "job_id", "ship_to_id", "gl_entry_id", "voided_at", "voided_by", "void_reason"} {
		if v, ok := b[key]; !ok || v != nil {
			t.Errorf("%s = %v (present %v), want present as null", key, v, ok)
		}
	}
	if str(t, b, "tax_rate_percent") != "8.875" {
		t.Errorf("tax_rate_percent = %v, want the invoice's 8.875", b["tax_rate_percent"])
	}
	l := b["lines"].([]any)[0].(map[string]any)
	if str(t, l, "quantity") != "-4" || num(t, l, "line_total_cents") != -2200 || l["restock"] != true || str(t, l, "invoice_line_id") != line {
		t.Errorf("line = %v", l)
	}
	if num(t, l, "unit_price_ten_thousandths") != 55000 || str(t, l, "uom") != "PCS" || num(t, l, "unit_cost_ten_thousandths") != 32500 || num(t, l, "cost_cents") != -1300 {
		t.Errorf("line price/cost = %v", l)
	}
	if ev := eventTypes(t, db, "credit_memo", id); fmt.Sprint(ev) != "[credit_memo.created]" {
		t.Errorf("events = %v", ev)
	}
	// the get reads the same
	g := f.do("GET", "/api/v1/credit-memos/"+id, nil)
	if g.status != 200 || g.header.Get("ETag") != `"1"` || num(t, g.body, "total_cents") != -2395 {
		t.Errorf("get = %d %s", g.status, g.raw)
	}
	// nothing moved: a draft is not a posting
	if f.stock() != "90.0000/0.0000" || f.balance() != 5988 {
		t.Errorf("a draft moved stock %s or the balance %d", f.stock(), f.balance())
	}
	if e, _ := f.entryLegs(id); e != 0 {
		t.Errorf("%d journal entries for a draft", e)
	}
}

// RULE (ADR 0001 section 4): one 400 with every offending field and its full
// path; an unknown body field refused; a currency sent is refused.
func TestCreditMemoValidation(t *testing.T) {
	testutil.LockOutboxTables(t)
	db := testutil.RequireDB(t)
	f := newFixture(t, db)
	invID, _ := f.invoice("2")
	line := f.firstLineID(invID)
	body := map[string]any{
		"invoice_id": invID, "reason_code": "refund", "reason": " ",
		"lines": []map[string]any{
			{"invoice_line_id": line, "quantity": "2"},                                      // positive
			{"invoice_line_id": line, "quantity": "-1", "product_id": f.productID.String()}, // price fields with an invoice line
			{"line_type": "product", "quantity": "-1"},                                      // no description, no price
			{"line_type": "kit", "quantity": "-1", "description": "k"},                      // a server's type
			{"line_type": "charge", "quantity": "-1"},                                       // no code
		},
	}
	r := f.do("POST", "/api/v1/credit-memos", body)
	if r.status != 400 {
		t.Fatalf("create = %d: %s", r.status, r.raw)
	}
	code, _, fields := errorOf(t, r)
	if code != "validation_failed" {
		t.Errorf("code = %q, want validation_failed", code)
	}
	want := []string{"reason_code", "reason", "lines[0].quantity", "lines[1].product_id", "lines[2].description", "lines[2].unit_price_ten_thousandths", "lines[3].line_type", "lines[4].charge_code"}
	for _, w := range want {
		found := false
		for _, got := range fields {
			if got == w {
				found = true
			}
		}
		if !found {
			t.Errorf("no detail for %s in %v", w, fields)
		}
	}
	if r := f.do("POST", "/api/v1/credit-memos", map[string]any{"invoice_id": invID, "reason_code": "return", "reason": "x", "currency": "CAD", "lines": []map[string]any{returnLine(line, "-1", false)}}); r.status != 400 {
		t.Errorf("a currency in the body = %d, want 400 (never sent)", r.status)
	}
	if r := f.do("POST", "/api/v1/credit-memos", map[string]any{"invoice_id": invID, "reason_code": "return", "reason": "x", "revision": 1, "lines": []map[string]any{returnLine(line, "-1", false)}}); r.status != 400 {
		t.Errorf("a revision on create = %d, want 400", r.status)
	}
	if r := f.do("POST", "/api/v1/credit-memos", map[string]any{"reason_code": "return", "reason": "x", "lines": []map[string]any{{"line_type": "text", "description": "n"}}}); r.status != 400 {
		t.Errorf("no customer or invoice = %d, want 400", r.status)
	}
	// lines that are only notes credit nothing
	if r := f.do("POST", "/api/v1/credit-memos", map[string]any{"invoice_id": invID, "reason_code": "return", "reason": "x", "lines": []map[string]any{{"line_type": "text", "description": "n"}}}); r.status != 400 {
		t.Errorf("a credit memo of notes = %d, want 400", r.status)
	}
	// a line that is not on this invoice, a bad reference
	other := uuid.NewString()
	if r := f.do("POST", "/api/v1/credit-memos", f.creditBody(invID, returnLine(other, "-1", false))); r.status != 400 {
		t.Errorf("a line of another invoice = %d, want 400", r.status)
	}
	if r := f.do("POST", "/api/v1/credit-memos", map[string]any{"invoice_id": uuid.NewString(), "reason_code": "return", "reason": "x", "lines": []map[string]any{returnLine(line, "-1", false)}}); r.status != 400 {
		t.Errorf("an unknown invoice = %d, want 400", r.status)
	}
	if r := f.do("POST", "/api/v1/credit-memos", map[string]any{"customer_id": uuid.NewString(), "reason_code": "price_adjustment", "reason": "x",
		"lines": []map[string]any{{"line_type": "charge", "charge_code": "ADJUST", "quantity": "-1", "unit_price_ten_thousandths": 100}}}); r.status != 400 {
		t.Errorf("an unknown customer = %d, want 400", r.status)
	}
	if n := countOf(t, db, `SELECT count(*) FROM credit_memos WHERE customer_id = $1`, f.customerID); n != 0 {
		t.Errorf("%d credit memos from refused creates", n)
	}
}

// RULE (ADR 0005 6.3): a line cannot credit more than the invoice line billed
// less what earlier credit memos returned (409 exceeds_billed), the drafts
// counted at draft time.
func TestCreditMemoCannotReturnMoreThanBilled(t *testing.T) {
	testutil.LockOutboxTables(t)
	db := testutil.RequireDB(t)
	f := newFixture(t, db)
	invID, _ := f.invoice("10")
	line := f.firstLineID(invID)

	if r := f.do("POST", "/api/v1/credit-memos", f.creditBody(invID, returnLine(line, "-10.0001", false))); r.status != 409 {
		t.Fatalf("returning 10.0001 of 10 = %d, want 409: %s", r.status, r.raw)
	} else if code, blockers, _ := errorOf(t, r); code != "conflict" || fmt.Sprint(blockers) != "[exceeds_billed]" {
		t.Errorf("code %q blockers %v, want conflict exceeds_billed", code, blockers)
	}
	first := f.createCredit(f.creditBody(invID, returnLine(line, "-6", false)))
	if r := f.do("POST", "/api/v1/credit-memos", f.creditBody(invID, returnLine(line, "-5", false))); r.status != 409 {
		t.Errorf("6 drafted then 5 more of 10 = %d, want 409 exceeds_billed", r.status)
	}
	second := f.createCredit(f.creditBody(invID, returnLine(line, "-4", false))) // exactly the rest
	// post the first; the second still fits what is left
	if r := f.postCredit(str(t, first.body, "id"), 1); r.status != 200 {
		t.Fatalf("post = %d: %s", r.status, r.raw)
	}
	if r := f.postCredit(str(t, second.body, "id"), 1); r.status != 200 {
		t.Fatalf("post the second = %d: %s", r.status, r.raw)
	}
	// everything billed is credited: nothing more
	if r := f.do("POST", "/api/v1/credit-memos", f.creditBody(invID, returnLine(line, "-0.0001", false))); r.status != 409 {
		t.Errorf("returning more after the whole line is credited = %d, want 409", r.status)
	}
}

// RULE (ADR 0005 3 and 6.3): partial credit memos never credit more tax than the
// invoice charged: seven single piece returns each round their tax up (48.8 to
// 49 cents, seven times 343) against an invoice that charged 342, so the last
// takes exactly the remainder (48), and the credited tax sums to the charge.
func TestPartialCreditMemosNeverCreditMoreTaxThanCharged(t *testing.T) {
	testutil.LockOutboxTables(t)
	db := testutil.RequireDB(t)
	f := newFixture(t, db)
	invID, _ := f.invoice("7") // 38.50, tax 8.875% = 341.69 -> 342
	if inv := f.getInvoice(invID); num(t, inv.body, "tax_cents") != 342 || num(t, inv.body, "subtotal_cents") != 3850 {
		t.Fatalf("invoice tax %v subtotal %v, want 342 and 3850", inv.body["tax_cents"], inv.body["subtotal_cents"])
	}
	line := f.firstLineID(invID)
	var taxes []int64
	var subtotals int64
	for i := 0; i < 7; i++ {
		cm := f.createCredit(f.creditBody(invID, returnLine(line, "-1", false)))
		r := f.postCredit(str(t, cm.body, "id"), 1)
		if r.status != 200 {
			t.Fatalf("post %d = %d: %s", i, r.status, r.raw)
		}
		taxes = append(taxes, -num(t, r.body, "tax_cents"))
		subtotals += -num(t, r.body, "subtotal_cents")
	}
	var sum int64
	for _, tx := range taxes {
		sum += tx
	}
	if sum != 342 {
		t.Errorf("tax credited %v sums to %d, want exactly the 342 charged", taxes, sum)
	}
	if taxes[6] != 48 {
		t.Errorf("the last memo credited %d, want the remainder 48", taxes[6])
	}
	if subtotals != 3850 {
		t.Errorf("subtotals credited %d, want the whole 3850 to the cent", subtotals)
	}
	for i := 0; i < 6; i++ {
		if taxes[i] != 49 {
			t.Errorf("memo %d credited %d tax, want 49", i, taxes[i])
		}
	}
	// the receivable is whole again: invoice 4192 - 7 credits
	if f.balance() != 0 {
		t.Errorf("balance after crediting the whole invoice = %d, want 0", f.balance())
	}
}

// RULE (ADR 0005 6.3, 4.1): a draft carries no number and takes the next gapless
// CM- number at post; a voided draft consumes none; the entry, the subledger and
// the restock all land in the post.
func TestCreditMemoNumberIsMintedAtPost(t *testing.T) {
	testutil.LockOutboxTables(t)
	db := testutil.RequireDB(t)
	f := newFixture(t, db)
	invID, _ := f.invoice("10")
	line := f.firstLineID(invID)

	var failure error
	for attempt := 0; attempt < 3; attempt++ {
		failure = nil
		start := counterNext(t, db, "credit_memo")
		a := f.createCredit(f.creditBody(invID, returnLine(line, "-1", false)))
		b := f.createCredit(f.creditBody(invID, returnLine(line, "-1", false)))
		c := f.createCredit(f.creditBody(invID, returnLine(line, "-1", false)))
		for _, d := range []resp{a, b, c} {
			if d.body["number"] != nil {
				t.Fatalf("a draft carries a number: %v", d.body["number"])
			}
		}
		if got := counterNext(t, db, "credit_memo"); got != start {
			failure = fmt.Errorf("drafts moved the counter from %d to %d", start, got)
			continue
		}
		// void the middle draft: it never held a number
		v := f.do("POST", "/api/v1/credit-memos/"+str(t, b.body, "id")+"/transitions", map[string]any{"to": "void", "revision": 1, "reason": "oops"})
		if v.status != 200 || v.body["number"] != nil || str(t, v.body, "status") != "void" {
			t.Fatalf("void draft = %d: %s", v.status, v.raw)
		}
		pa := f.postCredit(str(t, a.body, "id"), 1)
		pc := f.postCredit(str(t, c.body, "id"), 1)
		if pa.status != 200 || pc.status != 200 {
			t.Fatalf("post = %d / %d: %s %s", pa.status, pc.status, pa.raw, pc.raw)
		}
		na, nc := str(t, pa.body, "number"), str(t, pc.body, "number")
		if !creditNumberPattern.MatchString(na) || na != fmt.Sprintf("CM-%06d", start) || nc != fmt.Sprintf("CM-%06d", start+1) {
			failure = fmt.Errorf("posted numbers %s and %s, want CM-%06d and CM-%06d (the voided draft consumed none)", na, nc, start, start+1)
			continue
		}
		if str(t, pa.body, "status") != "open" || pa.body["gl_entry_id"] == nil || rev(t, pa) != 2 || num(t, pa.body, "open_cents") != num(t, pa.body, "total_cents") {
			t.Errorf("posted memo = %s", pa.raw)
		}
		if ev := eventTypes(t, db, "credit_memo", str(t, a.body, "id")); fmt.Sprint(ev) != "[credit_memo.created credit_memo.posted]" {
			t.Errorf("events = %v", ev)
		}
		break
	}
	if failure != nil {
		t.Fatal(failure)
	}
}

// RULE (ADR 0005 8.2 and 8.4): a restocking credit memo's post returns the
// quantity to on hand and writes one balanced entry: revenue and tax back,
// receivable down, inventory up and cost of goods sold down at the cost that
// LEFT (the invoice line's), not today's average. A return without restock
// writes no inventory or cost leg. The void reverses both.
func TestRestockingCreditMemoReversesCOGSAtTheOriginalCost(t *testing.T) {
	testutil.LockOutboxTables(t)
	db := testutil.RequireDB(t)
	f := newFixture(t, db)
	invID, _ := f.invoice("10") // cost 3.25 each
	line := f.firstLineID(invID)
	mustExec(t, db, `UPDATE products SET average_unit_cost = 9.99 WHERE id = $1`, f.productID) // the cost moved since

	cm := f.createCredit(f.creditBody(invID, returnLine(line, "-4", true)))
	id := str(t, cm.body, "id")
	r := f.postCredit(id, 1)
	if r.status != 200 {
		t.Fatalf("post = %d: %s", r.status, r.raw)
	}
	if f.stock() != "94.0000/0.0000" {
		t.Errorf("stock after the post = %s, want 94 on hand", f.stock())
	}
	entries, legs := f.entryLegs(id)
	if entries != 1 {
		t.Fatalf("%d entries for the credit memo, want 1", entries)
	}
	want := map[string]leg{
		"1020": {0, 2395}, "4010": {2200, 0}, "2020": {195, 0}, "1030": {1300, 0}, "5010": {0, 1300},
	}
	if len(legs) != len(want) {
		t.Errorf("legs = %v, want exactly %v", legs, want)
	}
	for code, w := range want {
		if legs[code] != w {
			t.Errorf("leg %s = %+v, want %+v (1300 is 4 x the ORIGINAL 3.25, not 9.99)", code, legs[code], w)
		}
	}
	if f.balance() != 5988-2395 {
		t.Errorf("balance = %d, want %d", f.balance(), 5988-2395)
	}

	// a return that does not restock moves no stock and writes no cost legs
	cm2 := f.createCredit(f.creditBody(invID, returnLine(line, "-1", false)))
	id2 := str(t, cm2.body, "id")
	if r := f.postCredit(id2, 1); r.status != 200 {
		t.Fatalf("post 2 = %d: %s", r.status, r.raw)
	}
	_, legs2 := f.entryLegs(id2)
	if _, ok := legs2["1030"]; ok {
		t.Errorf("a return without restock wrote an inventory leg: %v", legs2)
	}
	if _, ok := legs2["5010"]; ok {
		t.Errorf("a return without restock wrote a cost leg: %v", legs2)
	}
	if f.stock() != "94.0000/0.0000" {
		t.Errorf("stock after a no restock return = %s, want 94", f.stock())
	}

	// the void reverses the entry and the restock
	v := f.do("POST", "/api/v1/credit-memos/"+id+"/transitions", map[string]any{"to": "void", "revision": 2, "reason": "returned in error"})
	if v.status != 200 || str(t, v.body, "status") != "void" || str(t, v.body, "number") != str(t, r.body, "number") {
		t.Fatalf("void = %d: %s", v.status, v.raw)
	}
	if f.stock() != "90.0000/0.0000" {
		t.Errorf("stock after the void = %s, want 90 (the restock reversed)", f.stock())
	}
	if n, rl := f.reversalLegs(id); n != 1 || rl["1030"] != (leg{0, 1300}) || rl["5010"] != (leg{1300, 0}) || rl["1020"] != (leg{2395, 0}) {
		t.Errorf("reversal %d %v", n, rl)
	}
	if f.balance() != 5988-599 {
		t.Errorf("balance after voiding the first memo = %d, want 5389 (the invoice less the second memo's 599)", f.balance())
	}
	if ev := eventTypes(t, db, "credit_memo", id); fmt.Sprint(ev) != "[credit_memo.created credit_memo.posted credit_memo.voided]" {
		t.Errorf("events = %v", ev)
	}
	// a void when the restocked goods are gone is refused
	cm3 := f.createCredit(f.creditBody(invID, returnLine(line, "-2", true)))
	if r := f.postCredit(str(t, cm3.body, "id"), 1); r.status != 200 {
		t.Fatalf("post 3 = %d: %s", r.status, r.raw)
	}
	mustExec(t, db, `UPDATE inventory SET quantity = 0 WHERE product_id = $1`, f.productID) // sold elsewhere
	r3 := f.do("POST", "/api/v1/credit-memos/"+str(t, cm3.body, "id")+"/transitions", map[string]any{"to": "void", "revision": 2, "reason": "too late"})
	if _, blockers, _ := errorOf(t, r3); r3.status != 409 || fmt.Sprint(blockers) != "[restock_consumed]" {
		t.Errorf("void of a restock whose goods are gone = %d %v, want 409 restock_consumed", r3.status, blockers)
	}
}

// RULE (ADR 0001 section 11): the draft edit takes the revision (428, 409 stale,
// If-Match strong and weak, the body revision), recomputes the lines, and works
// on drafts only (409 credit_memo_not_draft). The transitions refuse everything
// outside the table.
func TestCreditMemoEditAndTransitions(t *testing.T) {
	testutil.LockOutboxTables(t)
	db := testutil.RequireDB(t)
	f := newFixture(t, db)
	invID, _ := f.invoice("10")
	line := f.firstLineID(invID)
	cm := f.createCredit(f.creditBody(invID, returnLine(line, "-2", false)))
	id := str(t, cm.body, "id")
	edit := f.creditBody(invID, returnLine(line, "-3", false))
	edit["reason"] = "edited"

	if r := f.do("PUT", "/api/v1/credit-memos/"+id, edit); r.status != 428 {
		t.Errorf("PUT without a precondition = %d, want 428", r.status)
	}
	if r := f.do("PUT", "/api/v1/credit-memos/"+id, edit, "If-Match", `"7"`); r.status != 409 {
		t.Errorf("PUT on a stale revision = %d, want 409", r.status)
	}
	r := f.do("PUT", "/api/v1/credit-memos/"+id, edit, "If-Match", `W/"1"`)
	if r.status != 200 || rev(t, r) != 2 || str(t, r.body, "reason") != "edited" || num(t, r.body, "subtotal_cents") != -1650 {
		t.Fatalf("PUT = %d: %s", r.status, r.raw)
	}
	// the customer and the invoice are fixed once created
	other := f.creditBody(invID, returnLine(line, "-1", false))
	other["customer_id"] = uuid.NewString()
	if r := f.do("PUT", "/api/v1/credit-memos/"+id, other, "If-Match", `"2"`); r.status != 400 {
		t.Errorf("PUT naming another customer = %d, want 400", r.status)
	}
	// a line keeps its id when the edit sends it
	lineID := r.body["lines"].([]any)[0].(map[string]any)["id"].(string)
	keep := f.creditBody(invID, returnLine(line, "-3", false))
	keep["lines"].([]map[string]any)[0]["id"] = lineID
	if r2 := f.do("PUT", "/api/v1/credit-memos/"+id, keep, "If-Match", `"2"`); r2.status != 200 || r2.body["lines"].([]any)[0].(map[string]any)["id"] != lineID {
		t.Errorf("PUT keeping the line id = %d: %s", r2.status, r2.raw)
	}

	for _, to := range []string{"partial", "applied", "draft"} {
		x := f.do("POST", "/api/v1/credit-memos/"+id+"/transitions", map[string]any{"to": to, "revision": 3})
		if code, _, _ := errorOf(t, x); x.status != 409 || code != "invalid_state_transition" {
			t.Errorf("transition to %s = %d %q, want 409 invalid_state_transition", to, x.status, code)
		}
	}
	if x := f.do("POST", "/api/v1/credit-memos/"+id+"/transitions", map[string]any{"to": "void", "revision": 3}); x.status != 400 {
		t.Errorf("void without a reason = %d, want 400", x.status)
	}
	if x := f.do("POST", "/api/v1/credit-memos/"+id+"/transitions", map[string]any{"to": "open", "revision": 3}, "X-Test-Role", "sales"); x.status != 403 {
		t.Errorf("a sales post = %d, want 403", x.status)
	}
	post := f.postCredit(id, 3)
	if post.status != 200 {
		t.Fatalf("post = %d: %s", post.status, post.raw)
	}
	if x := f.postCredit(id, 4); x.status != 409 {
		t.Errorf("posting twice = %d, want 409", x.status)
	}
	put := f.do("PUT", "/api/v1/credit-memos/"+id, edit, "If-Match", `"4"`)
	if _, blockers, _ := errorOf(t, put); put.status != 409 || fmt.Sprint(blockers) != "[credit_memo_not_draft]" {
		t.Errorf("PUT on a posted memo = %d %v, want 409 credit_memo_not_draft", put.status, blockers)
	}
	// a void invoice cannot be credited
	voidInv, _ := f.invoice("2")
	vl := f.firstLineID(voidInv)
	if v := f.voidInvoice(voidInv, rev(t, f.getInvoice(voidInv)), "billed twice"); v.status != 200 {
		t.Fatalf("void the invoice = %d: %s", v.status, v.raw)
	}
	x := f.do("POST", "/api/v1/credit-memos", f.creditBody(voidInv, returnLine(vl, "-1", false)))
	if _, blockers, _ := errorOf(t, x); x.status != 409 || fmt.Sprint(blockers) != "[invoice_void]" {
		t.Errorf("a credit memo against a void invoice = %d %v, want 409 invoice_void", x.status, blockers)
	}
}

// RULE (ADR 0005 6.3): the credit memo list is the cursor envelope with working
// filters, and an empty page is [] in the bytes.
func TestCreditMemoList(t *testing.T) {
	testutil.LockOutboxTables(t)
	db := testutil.RequireDB(t)
	f := newFixture(t, db)
	invID, _ := f.invoice("10")
	line := f.firstLineID(invID)
	var ids []string
	for i := 0; i < 3; i++ {
		ids = append(ids, str(t, f.createCredit(f.creditBody(invID, returnLine(line, "-1", false))).body, "id"))
	}
	f.postCredit(ids[0], 1)
	var seen []string
	cursor := ""
	for page := 0; page < 5; page++ {
		path := "/api/v1/credit-memos?limit=2&invoice_id=" + invID
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
	if fmt.Sprint(seen) != fmt.Sprint([]string{ids[2], ids[1], ids[0]}) {
		t.Errorf("walk = %v, want newest first each once", seen)
	}
	if r := f.do("GET", "/api/v1/credit-memos?customer_id="+f.customerID.String()+"&status=open", nil); len(r.body["items"].([]any)) != 1 {
		t.Errorf("status=open = %s", r.raw)
	}
	if r := f.do("GET", "/api/v1/credit-memos?customer_id="+f.customerID.String()+"&status=draft,open&include=total", nil); num(t, r.body, "total") != 3 {
		t.Errorf("include=total = %s", r.raw)
	}
	empty := f.do("GET", "/api/v1/credit-memos?customer_id="+uuid.NewString(), nil)
	if !strings.Contains(string(empty.raw), `"items":[]`) {
		t.Errorf("empty page = %s", empty.raw)
	}
	for _, bad := range []string{"status=PENDING", "offset=1", "customer=x", "limit=0", "job_id=zzz"} {
		if r := f.do("GET", "/api/v1/credit-memos?"+bad, nil); r.status != 400 {
			t.Errorf("?%s = %d, want 400", bad, r.status)
		}
	}
	if r := f.do("GET", "/api/v1/credit-memos/"+uuid.NewString(), nil); r.status != 404 {
		t.Errorf("unknown credit memo = %d, want 404", r.status)
	}
	// the old routes are gone
	if r := f.do("GET", "/api/v1/credit-memos/"+f.customerID.String()+"/x", nil); r.status != 404 && r.status != 405 {
		t.Errorf("a stray credit memo path = %d", r.status)
	}
	if r := f.do("POST", "/api/v1/invoices/"+invID+"/credit-memo", map[string]any{"amount_cents": 500, "reason": "old route"}); r.status != 404 && r.status != 405 {
		t.Errorf("the removed invoice credit memo route = %d, want 404 or 405", r.status)
	}
}

// RULE (ADR 0005 3, 6.3): a credit memo with no invoice (a price given back) takes
// the customer's currency and the branch rate, and a free charge line posts to
// its code's account; the idempotent replay makes one memo and one event.
func TestCreditMemoWithNoInvoice(t *testing.T) {
	testutil.LockOutboxTables(t)
	db := testutil.RequireDB(t)
	f := newFixture(t, db)
	body := map[string]any{"customer_id": f.customerID.String(), "reason_code": "price_adjustment", "reason": "goodwill",
		"lines": []map[string]any{
			{"line_type": "product", "description": "Freight allowance", "quantity": "-1", "uom": "EA", "unit_price_ten_thousandths": 100000},
			{"line_type": "text", "description": "per the account manager"},
		}}
	key := "cm-once-" + uuid.NewString()
	r := f.do("POST", "/api/v1/credit-memos", body, "Idempotency-Key", key)
	if r.status != 201 {
		t.Fatalf("create = %d: %s", r.status, r.raw)
	}
	if again := f.do("POST", "/api/v1/credit-memos", body, "Idempotency-Key", key); again.header.Get("Idempotency-Replayed") != "true" || str(t, again.body, "id") != str(t, r.body, "id") {
		t.Errorf("the replay = %d replayed %q", again.status, again.header.Get("Idempotency-Replayed"))
	}
	if n := countOf(t, db, `SELECT count(*) FROM credit_memos WHERE customer_id = $1`, f.customerID); n != 1 {
		t.Errorf("%d credit memos after a replay, want 1", n)
	}
	// 10.00 credited, taxable by default, at the branch rate 8.875% = 88.75 -> 89
	for key, want := range map[string]int64{"subtotal_cents": -1000, "tax_cents": -89, "total_cents": -1089} {
		if got := num(t, r.body, key); got != want {
			t.Errorf("%s = %d, want %d", key, got, want)
		}
	}
	if r.body["invoice_id"] != nil || str(t, r.body, "currency") != "USD" {
		t.Errorf("invoice_id %v currency %v", r.body["invoice_id"], r.body["currency"])
	}
	id := str(t, r.body, "id")
	if p := f.postCredit(id, 1); p.status != 200 {
		t.Fatalf("post = %d: %s", p.status, p.raw)
	}
	_, legs := f.entryLegs(id)
	if legs["4010"] != (leg{1000, 0}) || legs["2020"] != (leg{89, 0}) || legs["1020"] != (leg{0, 1089}) {
		t.Errorf("legs = %v", legs)
	}
	if f.balance() != -1089 {
		t.Errorf("balance = %d, want -1089 (the customer is owed)", f.balance())
	}
}
