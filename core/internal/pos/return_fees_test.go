// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package pos_test

// Returns, what M0b leaves: a closed lowercase list of reasons on
// each returned line (other asks for the note), and a restocking fee in
// basis points per reason (a database wide setting; the exempt reasons carry
// none), half even on each line's refund, shown on the credit memo as its own
// line and booked to the income account inside the return's transaction.

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/gablelbm/gable/internal/testutil"
	"github.com/google/uuid"
)

// setFee writes one reason's restocking fee in basis points for one test.
func (f *fixture) setFee(reason, bps string) {
	f.t.Helper()
	key := "pos.restocking_fee_bps." + reason
	mustExec(f.t, f.db, `INSERT INTO system_settings (key, value) VALUES ($1, $2)
		ON CONFLICT (key) DO UPDATE SET value = EXCLUDED.value`, key, bps)
	f.t.Cleanup(func() { mustExec(f.t, f.db, `DELETE FROM system_settings WHERE key = $1`, key) })
}

// returnLines posts a linked cash return of the lines (each a map with
// line_id, quantity and optionally reason_code and restock).
func (f *fixture) returnLines(t *testing.T, saleID, note string, lines ...map[string]any) resp {
	t.Helper()
	f.ensureOpenTill(t)
	body := map[string]any{"register_id": f.register, "original_sale_id": saleID, "refund_method": "cash", "lines": lines}
	if note != "" {
		body["reason"] = note
	}
	return f.do("POST", "/api/v1/pos/returns", body, "X-Test-Role", "cashier", "X-Test-Sub", mustUUID(t))
}

func lineOf(lineID, qty, reason string) map[string]any {
	l := map[string]any{"line_id": lineID, "quantity": qty, "restock": true}
	if reason != "" {
		l["reason_code"] = reason
	}
	return l
}

// a cash sale of qty (the product is 5.50 a unit, taxed at 8.875 percent),
// answering the sale and its first line
func (f *fixture) cashSaleLine(t *testing.T, qty string) (saleID, lineID string, body resp) {
	t.Helper()
	sale, total := f.cartOf(qty, nil)
	r := f.completeSale(sale, tender("cash", total))
	if r.status != http.StatusOK {
		t.Fatalf("sale = %d: %s", r.status, r.raw)
	}
	return sale, r.body["lines"].([]any)[0].(map[string]any)["id"].(string), r
}

// RULE: the reason list is closed, lowercase and states each reason's fee;
// anything else on a line is refused naming the field.
func TestTheReturnReasonListIsClosedAndStatesTheFees(t *testing.T) {
	testutil.LockOutboxTables(t)
	f := newFixture(t, testutil.RequireDB(t))
	f.setFee("changed_mind", "1500")
	r := f.do("GET", "/api/v1/pos/return-reasons", nil, "X-Test-Role", "cashier", "X-Test-Sub", mustUUID(t))
	if r.status != http.StatusOK {
		t.Fatalf("return reasons = %d: %s", r.status, r.raw)
	}
	items, _ := r.body["items"].([]any)
	codes := map[string]map[string]any{}
	for _, x := range items {
		m := x.(map[string]any)
		codes[m["code"].(string)] = m
	}
	for _, want := range []string{"defective", "damaged", "wrong_item", "ordered_wrong", "changed_mind", "overbought", "other"} {
		if codes[want] == nil {
			t.Errorf("reason %q missing from %s", want, r.raw)
		}
	}
	if num(t, codes["changed_mind"], "restocking_fee_bps") != 1500 || num(t, codes["defective"], "restocking_fee_bps") != 0 {
		t.Errorf("fees = %v / %v, want 1500 for changed_mind and 0 for defective", codes["changed_mind"], codes["defective"])
	}
	if codes["other"]["needs_note"] != true || codes["defective"]["needs_note"] != false {
		t.Errorf("only other needs a note: %v / %v", codes["other"], codes["defective"])
	}

	sale, line, _ := f.cashSaleLine(t, "2")
	for name, code := range map[string]string{"an unknown reason": "buyers_remorse", "an upper case reason": "Defective", "spaces": " defective"} {
		r := f.returnLines(t, sale, "note", lineOf(line, "1", code))
		if r.status != http.StatusBadRequest {
			t.Errorf("%s = %d, want 400: %s", name, r.status, r.raw)
			continue
		}
		if _, _, fields := errorOf(t, r); len(fields) == 0 || fields[0] != "lines[0].reason_code" {
			t.Errorf("%s: fields = %v, want lines[0].reason_code", name, fields)
		}
	}
}

// RULE: other needs the note: a line with no reason is other, and a return
// whose line is other with no note is refused naming the field; a return
// whose every line names a reason of the list needs no note and the memo
// states the reasons in words.
func TestOtherNeedsTheNoteAndACodedReturnNeedsNone(t *testing.T) {
	testutil.LockOutboxTables(t)
	f := newFixture(t, testutil.RequireDB(t))
	sale, line, _ := f.cashSaleLine(t, "4")

	for name, code := range map[string]string{"no reason": "", "other": "other"} {
		r := f.returnLines(t, sale, "", lineOf(line, "1", code))
		if r.status != http.StatusBadRequest {
			t.Errorf("%s with no note = %d, want 400: %s", name, r.status, r.raw)
			continue
		}
		if _, _, fields := errorOf(t, r); len(fields) == 0 || fields[0] != "reason" {
			t.Errorf("%s: fields = %v, want reason", name, fields)
		}
	}
	r := f.returnLines(t, sale, "kept the wrong colour", lineOf(line, "1", "other"))
	if r.status != http.StatusCreated {
		t.Fatalf("other with a note = %d: %s", r.status, r.raw)
	}
	if r.body["reason"] != "kept the wrong colour" || r.body["lines"].([]any)[0].(map[string]any)["reason_code"] != "other" {
		t.Errorf("return = %v, want the note and reason_code other", r.body)
	}
	r = f.returnLines(t, sale, "", lineOf(line, "1", "defective"))
	if r.status != http.StatusCreated {
		t.Fatalf("a coded return with no note = %d: %s", r.status, r.raw)
	}
	if got := str(t, r.body, "reason"); got != "Defective" {
		t.Errorf("reason = %q, want the reason in words", got)
	}
	var memoReason string
	if err := f.db.Pool.QueryRow(context.Background(), `SELECT reason FROM credit_memos WHERE id = $1`, uuid.MustParse(str(t, r.body, "credit_memo_id"))).Scan(&memoReason); err != nil || memoReason != "Defective" {
		t.Errorf("memo reason = %q (%v), want Defective", memoReason, err)
	}
}

// RULE: each reason's fee is its basis points of the line's refund, half
// even to the cent: 15 percent of 5.50 is 82.5, which rounds to the even 82,
// and 15 percent of 16.50 is 247.5, which rounds to the even 248. An exempt
// reason carries none. The fee is a line of the credit memo, books to the
// RESTOCK income account in the return's own entry, which balances to the
// cent, and the cash goes back net of it.
func TestARestockingFeeIsHalfEvenOnTheLineAndBooksBalanced(t *testing.T) {
	testutil.LockOutboxTables(t)
	f := newFixture(t, testutil.RequireDB(t))
	f.setFee("changed_mind", "1500")
	f.setFee("defective", "0")
	f.openTill(0)
	sale, line, body := f.cashSaleLine(t, "4")
	saleTax := num(t, body.body, "tax_cents")

	// one unit of 5.50: 550 * 1500 / 10000 = 82.5 -> 82 (even)
	r := f.returnLines(t, sale, "", lineOf(line, "1", "changed_mind"))
	if r.status != http.StatusCreated {
		t.Fatalf("return = %d: %s", r.status, r.raw)
	}
	if got := num(t, r.body, "restocking_fee_cents"); got != 82 {
		t.Errorf("restocking_fee_cents = %d, want 82 (82.5 half even)", got)
	}
	l0 := r.body["lines"].([]any)[0].(map[string]any)
	if num(t, l0, "restocking_fee_cents") != 82 || num(t, l0, "line_total_cents") != -550 || l0["reason_code"] != "changed_mind" {
		t.Errorf("line = %v, want -550 refunded (the line total is negative) with an 82 fee for changed_mind", l0)
	}
	// the tax refunded is the whole share (the fee is not taxed): one of four
	// units of a 2200 line taxed 195 refunds 49
	tax := -num(t, r.body, "tax_cents")
	if tax <= 0 || tax > saleTax {
		t.Fatalf("tax_cents = %d", num(t, r.body, "tax_cents"))
	}
	gross := 550 + tax
	if got := -num(t, r.body, "total_cents"); got != gross-82 {
		t.Errorf("total_cents = %d, want the goods and tax less the fee (%d)", -num(t, r.body, "total_cents"), gross-82)
	}
	if got := -num(t, r.body, "subtotal_cents"); got != 550-82 {
		t.Errorf("subtotal_cents = %d, want 468", -num(t, r.body, "subtotal_cents"))
	}
	memoID := str(t, r.body, "credit_memo_id")
	// the memo shows the fee as its own line
	m := f.do("GET", "/api/v1/credit-memos/"+memoID, nil, "X-Test-Role", "admin", "X-Test-Sub", mustUUID(t))
	if m.status != http.StatusOK {
		t.Fatalf("credit memo = %d: %s", m.status, m.raw)
	}
	var feeLines int
	for _, x := range m.body["lines"].([]any) {
		ml := x.(map[string]any)
		if ml["line_type"] == "charge" {
			feeLines++
			if num(t, ml, "line_total_cents") != 82 || !strings.Contains(strings.ToLower(ml["description"].(string)), "restocking") {
				t.Errorf("fee line = %v, want 82 and a restocking description", ml)
			}
		}
	}
	if feeLines != 1 {
		t.Errorf("fee lines on the memo = %d, want 1", feeLines)
	}
	// the posting: the fee is a credit to 4030, AR is credited the net, and
	// the entry balances to the cent (the ledger refuses one that does not)
	entries, legs := f.legsFor(memoID)
	if entries != 1 {
		t.Fatalf("entries of the memo = %d, want 1", entries)
	}
	if legs["4030"].credit != 82 {
		t.Errorf("4030 credit = %d, want the 82 fee", legs["4030"].credit)
	}
	if legs["1020"].credit != gross-82 {
		t.Errorf("1020 credit = %d, want the net %d", legs["1020"].credit, gross-82)
	}
	var debit, credit int64
	for _, l := range legs {
		debit += l.debit
		credit += l.credit
	}
	if debit != credit {
		t.Errorf("the entry's debits %d and credits %d do not balance", debit, credit)
	}

	// three units of 16.50: 1650 * 1500 / 10000 = 247.5 -> 248 (even)
	r = f.returnLines(t, sale, "", lineOf(line, "3", "changed_mind"))
	if r.status != http.StatusCreated {
		t.Fatalf("return of three = %d: %s", r.status, r.raw)
	}
	if got := num(t, r.body, "restocking_fee_cents"); got != 248 {
		t.Errorf("restocking_fee_cents = %d, want 248 (247.5 half even)", got)
	}
	f.assertARInvariants(t)
}

// RULE: the exempt reasons carry no fee whatever the others carry, and a
// return of lines with different reasons withholds the sum of their fees.
func TestExemptReasonsCarryNoFeeAndLinesSumTheirFees(t *testing.T) {
	testutil.LockOutboxTables(t)
	f := newFixture(t, testutil.RequireDB(t))
	f.setFee("changed_mind", "2000")
	f.setFee("overbought", "1000")
	f.openTill(0)
	sale, line, _ := f.cashSaleLine(t, "4")
	// a defective unit: no fee at all
	r := f.returnLines(t, sale, "", lineOf(line, "1", "defective"))
	if r.status != http.StatusCreated || num(t, r.body, "restocking_fee_cents") != 0 {
		t.Fatalf("a defective return = %d fee %v: %s, want 201 and no fee", r.status, r.body["restocking_fee_cents"], r.raw)
	}
	if n := f.scalar(`SELECT count(*) FROM credit_memo_lines WHERE credit_memo_id = $1 AND line_type = 'CHARGE'`, uuid.MustParse(str(t, r.body, "credit_memo_id"))); n.(int64) != 0 {
		t.Errorf("a fee free memo carries %v charge lines, want none", n)
	}
	if entries, legs := f.legsFor(str(t, r.body, "credit_memo_id")); entries != 1 || legs["4030"].credit != 0 {
		t.Errorf("a fee free return booked 4030 credit %d", legs["4030"].credit)
	}
	// two lines of the sale, two reasons
	sale2, total := f.cartOf("1", nil)
	f.addLine(sale2, f.productLine("1"))
	total = num(t, f.getSale(t, sale2), "total_cents")
	if r := f.completeSale(sale2, tender("cash", total)); r.status != http.StatusOK {
		t.Fatalf("two line sale = %d: %s", r.status, r.raw)
	}
	lines := f.getSale(t, sale2)["lines"].([]any)
	if len(lines) != 2 {
		t.Fatalf("lines = %d, want 2", len(lines))
	}
	a, b := lines[0].(map[string]any)["id"].(string), lines[1].(map[string]any)["id"].(string)
	r = f.returnLines(t, sale2, "", lineOf(a, "1", "changed_mind"), lineOf(b, "1", "overbought"))
	if r.status != http.StatusCreated {
		t.Fatalf("two reasons = %d: %s", r.status, r.raw)
	}
	// 550 * 20% = 110, 550 * 10% = 55
	if got := num(t, r.body, "restocking_fee_cents"); got != 165 {
		t.Errorf("restocking_fee_cents = %d, want 165 (110 and 55)", got)
	}
	f.assertARInvariants(t)
}

// RULE: a fee setting that is not a whole number of basis points is an error,
// never a silent zero, and a fee needs the RESTOCK charge code to book to.
func TestABadFeeSettingIsAnErrorNotASilentZero(t *testing.T) {
	testutil.LockOutboxTables(t)
	f := newFixture(t, testutil.RequireDB(t))
	f.openTill(0)
	sale, line, _ := f.cashSaleLine(t, "2")
	f.setFee("changed_mind", "fifteen")
	if r := f.returnLines(t, sale, "", lineOf(line, "1", "changed_mind")); r.status < 500 {
		t.Errorf("a malformed fee setting = %d, want a server error: %s", r.status, r.raw)
	}
	f.setFee("changed_mind", "10001")
	if r := f.returnLines(t, sale, "", lineOf(line, "1", "changed_mind")); r.status < 500 {
		t.Errorf("a fee over 100 percent = %d, want a server error: %s", r.status, r.raw)
	}
	if n := f.counterMemos(t); n != 0 {
		t.Errorf("a refused return left %d memos", n)
	}
}
