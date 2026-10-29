// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package pos_test

// CP-06's restocking fee settings (slice 7b): the dealer sets and sees the
// fee each return reason carries, on the counter settings resource an owner
// or admin writes; the counter reads the figures a return will carry before
// it is taken, from a preview that prices nothing on the screen and writes
// nothing on the server; the fee itself never takes more than the line's own
// refund, whatever the setting allows.

import (
	"encoding/json"
	"fmt"
	"net/http"
	"sync"
	"testing"

	"github.com/gablelbm/gable/internal/testutil"
)

// numOf reads a decoded JSON number (the fixture decodes with UseNumber).
func numOf(v any) (int64, bool) {
	switch n := v.(type) {
	case json.Number:
		i, err := n.Int64()
		return i, err == nil
	case float64:
		return int64(n), true
	}
	return 0, false
}

// feeSettingsRows cleans this test file's settings rows and their anchor.
func feeSettingsRows(t *testing.T, f *fixture) {
	t.Helper()
	t.Cleanup(func() {
		mustExec(t, f.db, `DELETE FROM system_settings WHERE key LIKE 'pos.restocking_fee_bps.%'`)
		mustExec(t, f.db, `DELETE FROM system_settings WHERE key IN ('pos.check_limit_cents', 'pos.check_id_required_over_cents')`)
		mustExec(t, f.db, `DELETE FROM admin_revisions WHERE resource = 'pos.settings'`)
	})
}

// settingsBody is the PUT body of the whole rules object.
func settingsBody(fees map[string]int64, revision int64) map[string]any {
	return map[string]any{"revision": revision,
		"rules": map[string]any{"check_limit_cents": nil,
			"check_id_required": map[string]any{"mode": "never"},
			"restocking_fees":   fees}}
}

// allReasonFees is the closed list's fees as the wire spells them.
func allReasonFees(changedMind, orderedWrong, overbought int64) map[string]int64 {
	return map[string]int64{"defective": 0, "damaged": 0, "wrong_item": 0,
		"ordered_wrong": orderedWrong, "changed_mind": changedMind, "overbought": overbought, "other": 0}
}

// RULE: the settings carry the restocking fee of every reason code: the GET
// states the fee in force (zero everywhere before anything is set) beside the
// suggested figures the screen offers, and an owner or admin writes the whole
// map as one act on the revision, audited with the fees it wrote. A cashier
// reads but never writes; a code outside the closed list, a figure over 10000
// basis points or a missing reason is a 400 naming the field.
func TestCP06_RestockingFeeSettingsRoundTripAndGuard(t *testing.T) {
	testutil.LockOutboxTables(t)
	f := newFixture(t, testutil.RequireDB(t))
	feeSettingsRows(t, f)

	get := f.do("GET", "/api/v1/pos/settings", nil, "X-Test-Role", "cashier", "X-Test-Sub", mustUUID(t))
	if get.status != http.StatusOK {
		t.Fatalf("the counter reads the settings = %d: %s", get.status, get.raw)
	}
	feesOf := func(m map[string]any) map[string]any {
		rules, _ := m["rules"].(map[string]any)
		fees, _ := rules["restocking_fees"].(map[string]any)
		return fees
	}
	defaultsOf := func(m map[string]any) map[string]any {
		rules, _ := m["rules"].(map[string]any)
		fees, _ := rules["restocking_fee_defaults"].(map[string]any)
		return fees
	}
	if feesOf(get.body) == nil {
		t.Fatalf("the settings carry no restocking_fees map: %s", get.raw)
	}
	if got := len(feesOf(get.body)); got != 7 {
		t.Errorf("the fee map names %d codes, want the closed list of 7: %v", got, feesOf(get.body))
	}
	for code, v := range feesOf(get.body) {
		if n, ok := numOf(v); !ok || n != 0 {
			t.Errorf("code %s = %v before anything is set, want 0", code, v)
		}
	}
	// the suggested figures: the customer's own faults carry one, the dealer's carry none
	cm, _ := numOf(defaultsOf(get.body)["changed_mind"])
	df, _ := numOf(defaultsOf(get.body)["defective"])
	if cm <= 0 || df != 0 {
		t.Errorf("the suggested fees = %v, want a figure on changed_mind and none on defective", defaultsOf(get.body))
	}
	rev0 := rev(t, get)

	// a cashier may read but not write
	if r := f.do("PUT", "/api/v1/pos/settings", settingsBody(allReasonFees(1500, 1500, 1500), rev0),
		"X-Test-Role", "cashier", "X-Test-Sub", mustUUID(t)); r.status != http.StatusForbidden {
		t.Errorf("a cashier writing the fees = %d, want 403: %s", r.status, r.raw)
	}
	// a code outside the closed list
	bad := allReasonFees(1500, 1500, 1500)
	bad["no_such_reason"] = 100
	if r := f.do("PUT", "/api/v1/pos/settings", settingsBody(bad, rev0),
		"X-Test-Role", "admin", "X-Test-Sub", mustUUID(t)); r.status != http.StatusBadRequest {
		t.Errorf("an unknown reason code = %d, want 400: %s", r.status, r.raw)
	}
	// a figure over the whole of the line
	over := allReasonFees(10001, 0, 0)
	if r := f.do("PUT", "/api/v1/pos/settings", settingsBody(over, rev0),
		"X-Test-Role", "admin", "X-Test-Sub", mustUUID(t)); r.status != http.StatusBadRequest {
		t.Errorf("10001 basis points = %d, want 400 (the fee may never pass the line): %s", r.status, r.raw)
	}
	// a reason left out
	partial := map[string]int64{"changed_mind": 1500}
	if r := f.do("PUT", "/api/v1/pos/settings", settingsBody(partial, rev0),
		"X-Test-Role", "admin", "X-Test-Sub", mustUUID(t)); r.status != http.StatusBadRequest {
		t.Errorf("a fee map naming one code of seven = %d, want 400: %s", r.status, r.raw)
	}

	// the write: owner and admin both, one revision each
	want := allReasonFees(1500, 1500, 1000)
	ownerSub := mustUUID(t)
	put := f.do("PUT", "/api/v1/pos/settings", settingsBody(want, rev0),
		"X-Test-Role", "owner", "X-Test-Sub", ownerSub, "If-Match", fmt.Sprintf(`"%d"`, rev0))
	if put.status != http.StatusOK {
		t.Fatalf("the owner writes the fees = %d: %s", put.status, put.raw)
	}
	if rev(t, put) != rev0+1 {
		t.Errorf("revision after the write = %d, want %d", rev(t, put), rev0+1)
	}
	for code, v := range feesOf(put.body) {
		if n, _ := numOf(v); n != want[code] {
			t.Errorf("code %s = %v, want %d", code, v, want[code])
		}
	}
	// the reasons the counter reads state the same fees
	reasons := f.do("GET", "/api/v1/pos/return-reasons", nil, "X-Test-Role", "cashier", "X-Test-Sub", mustUUID(t))
	if reasons.status != http.StatusOK {
		t.Fatalf("return-reasons = %d: %s", reasons.status, reasons.raw)
	}
	reasonFees := map[string]int64{}
	for _, x := range reasons.body["items"].([]any) {
		rr := x.(map[string]any)
		reasonFees[rr["code"].(string)], _ = numOf(rr["restocking_fee_bps"])
	}
	for code, v := range want {
		if int64(reasonFees[code]) != v {
			t.Errorf("return-reasons code %s = %v, want %d", code, reasonFees[code], v)
		}
	}
	// only the figures over zero hold rows; a zero deletes its row
	if n := countOf(t, f.db, `SELECT count(*) FROM system_settings WHERE key LIKE 'pos.restocking_fee_bps.%'`); n != 3 {
		t.Errorf("fee setting rows = %d, want 3 (only the figures over zero)", n)
	}
	// the audit row carries the fees the write made (scoped to this test's
	// actor; the table keeps earlier runs' rows)
	if n := countOf(t, f.db, `SELECT count(*) FROM audit_log WHERE action = 'pos.settings.saved'
		AND user_id::text = $1
		AND changes->'restocking_fees'->>'changed_mind' = '1500'
		AND changes->'restocking_fees'->>'defective' = '0'`, ownerSub); n != 1 {
		t.Errorf("the fee write's audit rows = %d, want 1", n)
	}
	// clearing a figure deletes its row
	cleared := allReasonFees(0, 1500, 1000)
	put = f.do("PUT", "/api/v1/pos/settings", settingsBody(cleared, rev0+1),
		"X-Test-Role", "admin", "X-Test-Sub", mustUUID(t))
	if put.status != http.StatusOK {
		t.Fatalf("clearing a fee = %d: %s", put.status, put.raw)
	}
	if n := countOf(t, f.db, `SELECT count(*) FROM system_settings WHERE key LIKE 'pos.restocking_fee_bps.%'`); n != 2 {
		t.Errorf("fee setting rows after the clear = %d, want 2", n)
	}
	if cleared, _ := numOf(feesOf(put.body)["changed_mind"]); cleared != 0 {
		t.Errorf("changed_mind after the clear = %v, want 0", feesOf(put.body)["changed_mind"])
	}
}

// RULE: the fee never takes more than the line's own refund: 10000 basis
// points is the most a reason may carry, and at the most the fee is the line's
// whole refund, leaving the tax whole.
func TestCP06_FeeNeverMoreThanTheLine(t *testing.T) {
	testutil.LockOutboxTables(t)
	f := newFixture(t, testutil.RequireDB(t))
	feeSettingsRows(t, f)
	f.setFee("changed_mind", "10000")
	f.openTill(0)
	sale, line, body := f.cashSaleLine(t, "2")
	saleTax := num(t, body.body, "tax_cents")

	r := f.returnLines(t, sale, "", lineOf(line, "1", "changed_mind"))
	if r.status != http.StatusCreated {
		t.Fatalf("a whole of the line fee return = %d: %s", r.status, r.raw)
	}
	l0 := r.body["lines"].([]any)[0].(map[string]any)
	if num(t, l0, "restocking_fee_cents") != 550 || num(t, l0, "line_total_cents") != -550 {
		t.Errorf("line = %v, want the fee to take the line's whole 550 refund", l0)
	}
	if got := num(t, r.body, "restocking_fee_cents"); got != 550 {
		t.Errorf("restocking_fee_cents = %d, want 550 (never more than the line)", got)
	}
	// the tax stays whole: only the goods' credit was taken
	if got := -num(t, r.body, "tax_cents"); got != saleTax/2 {
		t.Errorf("tax_cents = %d, want the line's whole share %d", got, saleTax/2)
	}
	f.assertARInvariants(t)
}

// RULE: the preview states the figures a return will carry, fee included,
// before anything is taken; it writes nothing, and the return taken on the
// same lines carries the figures it stated.
func TestCP06_ReturnPreviewStatesTheFeeAndWritesNothing(t *testing.T) {
	testutil.LockOutboxTables(t)
	f := newFixture(t, testutil.RequireDB(t))
	feeSettingsRows(t, f)
	put := f.do("PUT", "/api/v1/pos/settings", settingsBody(allReasonFees(1500, 1500, 1000), 1),
		"X-Test-Role", "admin", "X-Test-Sub", mustUUID(t))
	if put.status != http.StatusOK {
		t.Fatalf("the fees = %d: %s", put.status, put.raw)
	}
	f.openTill(0)
	sale, line, _ := f.cashSaleLine(t, "4")

	returns, memos := f.counterReturns(t), f.counterMemos(t)
	body := map[string]any{"register_id": f.register, "original_sale_id": sale, "refund_method": "cash",
		"lines": []map[string]any{lineOf(line, "1", "changed_mind")}}
	r := f.do("POST", "/api/v1/pos/returns/preview", body, "X-Test-Role", "cashier", "X-Test-Sub", mustUUID(t))
	if r.status != http.StatusOK {
		t.Fatalf("the preview = %d: %s", r.status, r.raw)
	}
	if got := num(t, r.body, "restocking_fee_cents"); got != 82 {
		t.Errorf("preview restocking_fee_cents = %d, want 82 (82.5 half even)", got)
	}
	// the figures the return itself will state: subtotal and total negative,
	// the money back positive, the fee per line beside its refund
	if num(t, r.body, "subtotal_cents") != -468 || num(t, r.body, "total_cents") >= 0 || num(t, r.body, "refund_cents") <= 0 {
		t.Errorf("preview figures = %v, want a 468 net credit and a positive refund", r.body)
	}
	previewRefund := num(t, r.body, "refund_cents")
	previewFee := num(t, r.body, "restocking_fee_cents")
	lines := r.body["lines"].([]any)
	if len(lines) != 1 {
		t.Fatalf("preview lines = %d, want 1", len(lines))
	}
	pl := lines[0].(map[string]any)
	if pl["line_id"] != line || pl["reason_code"] != "changed_mind" ||
		num(t, pl, "line_total_cents") != -550 || num(t, pl, "restocking_fee_cents") != 82 {
		t.Errorf("preview line = %v, want the sale line, changed_mind, -550 and 82", pl)
	}
	// nothing written
	if f.counterReturns(t) != returns || f.counterMemos(t) != memos {
		t.Errorf("the preview wrote something: returns %d memos %d", f.counterReturns(t), f.counterMemos(t))
	}
	// the return taken on the same lines carries what the preview stated
	taken := f.returnLines(t, sale, "", lineOf(line, "1", "changed_mind"))
	if taken.status != http.StatusCreated {
		t.Fatalf("the return = %d: %s", taken.status, taken.raw)
	}
	if num(t, taken.body, "restocking_fee_cents") != previewFee {
		t.Errorf("the taken return's fee = %d, want the preview's %d", num(t, taken.body, "restocking_fee_cents"), previewFee)
	}
	if got := -num(t, taken.body, "total_cents"); got != previewRefund {
		t.Errorf("the taken return's money back = %d, want the preview's %d", got, previewRefund)
	}
	f.assertARInvariants(t)
}

// RULE: three owners write different fee maps at once from one revision (pool
// size 4): exactly one wins, the others are refused the stale revision, and
// the stored rows are the winner's map, not a blend of the three.
func TestCP06_RestockingFeeSettingsConcurrentWrites(t *testing.T) {
	testutil.LockOutboxTables(t)
	db := testutil.RequireDBMaxConns(t, 4)
	f := newFixture(t, db)
	feeSettingsRows(t, f)
	get := f.do("GET", "/api/v1/pos/settings", nil, "X-Test-Role", "admin", "X-Test-Sub", mustUUID(t))
	cur := rev(t, get)

	var wg sync.WaitGroup
	var mu sync.Mutex
	ok, stale := 0, 0
	maps := []map[string]int64{allReasonFees(1000, 0, 0), allReasonFees(2000, 0, 0), allReasonFees(3000, 0, 0)}
	for i := 0; i < 3; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			r := f.do("PUT", "/api/v1/pos/settings", settingsBody(maps[i], cur),
				"X-Test-Role", "owner", "X-Test-Sub", mustUUID(t), "If-Match", fmt.Sprintf(`"%d"`, cur))
			mu.Lock()
			defer mu.Unlock()
			switch {
			case r.status == http.StatusOK:
				ok++
			case r.status == http.StatusConflict && staleCode(r):
				stale++
			default:
				t.Errorf("a contender = %d: %s", r.status, r.raw)
			}
		}(i)
	}
	wg.Wait()
	if ok != 1 || stale != 2 {
		t.Errorf("winners %d, stale %d, want 1 and 2", ok, stale)
	}
	// the stored figure the act wrote, not a recomputed read
	stored := f.scalar(`SELECT value FROM system_settings WHERE key = 'pos.restocking_fee_bps.changed_mind'`)
	if stored == nil {
		t.Fatalf("no changed_mind row was written")
	}
	wantOne := false
	for _, m := range maps {
		if fmt.Sprintf("%d", m["changed_mind"]) == stored.(string) {
			wantOne = true
		}
	}
	if !wantOne {
		t.Errorf("the stored changed_mind fee = %v, a figure no contender sent", stored)
	}
	if n := countOf(t, f.db, `SELECT count(*) FROM system_settings WHERE key LIKE 'pos.restocking_fee_bps.%'`); n != 1 {
		t.Errorf("fee setting rows = %d, want the winner's single figure over zero", n)
	}
}
