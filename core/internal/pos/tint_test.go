// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package pos_test

// The counter tint line (row CP-16): a line with a tint records the base,
// the colour and the formula exactly as requested, prices as the base price
// plus the dealer's tint charge (the base price alone when the charge is
// zero), freezes the amounts sold against a later catalogue edit, refuses a
// return of a tinted line while the policy is on (a mis tint returns), and
// stands in the over return cap like any other line. A line without a tint
// is unchanged.

import (
	"fmt"
	"net/http"
	"strings"
	"sync"
	"testing"

	"github.com/gablelbm/gable/internal/testutil"
	"github.com/google/uuid"
)

// seedTintCatalogue seeds one system, base, colorant, colour and gallon
// formula, and answers the ids the requests name.
func seedTintCatalogue(t *testing.T, f *fixture) (baseID, colorantID, colorID, formulaID string) {
	t.Helper()
	sys := uuid.New()
	sysCode := "GEN" + uuid.NewString()[:8]
	baseID, colorantID, colorID, formulaID = uuid.NewString(), uuid.NewString(), uuid.NewString(), uuid.NewString()
	mustExec(t, f.db, `INSERT INTO paint_tint_systems (id, code, name, manufacturer, unit_denominator) VALUES ($1, $2, 'Generic', 'Generic Paints', 384)`, sys, sysCode)
	mustExec(t, f.db, `INSERT INTO paint_bases (id, tint_system_id, code, name, fill_volume_oz) VALUES ($1, $2, 'DEEP', 'Deep base', 122)`, baseID, sys)
	mustExec(t, f.db, `INSERT INTO paint_colorants (id, tint_system_id, code, name) VALUES ($1, $2, 'B1', 'Black')`, colorantID, sys)
	mustExec(t, f.db, `INSERT INTO paint_colorants (id, tint_system_id, code, name) VALUES ($1, $2, 'W1', 'White')`, uuid.NewString(), sys)
	mustExec(t, f.db, `INSERT INTO paint_colors (id, tint_system_id, manufacturer_code, name) VALUES ($1, $2, 'HC-188', 'Essex Green')`, colorID, sys)
	mustExec(t, f.db, `INSERT INTO paint_formulas (id, color_id, base_id, container_size_code, container_volume_oz, source)
		VALUES ($1, $2, $3, 'GAL', 128, 'book')`, formulaID, colorID, baseID)
	mustExec(t, f.db, `INSERT INTO paint_formula_components (formula_id, colorant_id, position, whole_oz, increments_coarse, increments_fine, unit_denominator)
		VALUES ($1, $2, 0, 1, 12, 0, 384)`, formulaID, colorantID)
	return
}

// tintSettingsRows clears the tint settings a test writes, so one test's
// charge never rides into the next one's price.
func tintSettingsRows(t *testing.T, f *fixture) {
	t.Helper()
	t.Cleanup(func() {
		mustExec(t, f.db, `DELETE FROM system_settings WHERE key IN ('pos.tint_charge_cents', 'pos.tint_charge_tiers', 'pos.tinted_no_return')`)
	})
}

// tintLine is one tinted product line of the fixture's product.
func tintLine(qty string, formulaID, colorID string) map[string]any {
	return map[string]any{
		"quantity": qty, "description": "Essex Green gallon",
		"tint": map[string]any{
			"formula_id": formulaID, "color_code": "HC-188", "color_name": "Essex Green",
			"base_code": "DEEP", "container_size_code": "GAL", "rescaled": false,
			"components": []map[string]any{{
				"colorant_code": "B1", "colorant_name": "Black",
				"amount": map[string]any{"whole_oz": 1, "increments_coarse": 12, "increments_fine": 0, "unit_denominator": 384},
			}},
		},
	}
}

// tintOf reads the first line's tint off a sale read.
func tintOf(t *testing.T, r resp) map[string]any {
	t.Helper()
	raw, _ := r.body["lines"].([]any)
	if len(raw) == 0 {
		t.Fatalf("no lines: %s", r.raw)
	}
	m, _ := raw[0].(map[string]any)["tint"].(map[string]any)
	return m
}

// RULE: a line with a tint records the base, the colour and the formula, and
// the child rows match the request exactly.
func TestTintedLineRecordsBaseColourAndFormula(t *testing.T) {
	testutil.LockOutboxTables(t)
	f := newFixture(t, testutil.RequireDB(t))
	tintSettingsRows(t, f)
	_, _, _, formulaID := seedTintCatalogue(t, f)
	sale := f.startSale(nil)
	line := tintLine("1", formulaID, "")
	line["product_id"] = f.productID.String()
	added := f.addLine(sale, line)
	if added.status != http.StatusOK {
		t.Fatalf("add tinted line = %d: %s", added.status, added.raw)
	}
	tint := tintOf(t, added)
	if tint == nil {
		t.Fatalf("the line carries no tint: %s", added.raw)
	}
	if tint["color_name"] != "Essex Green" || tint["color_code"] != "HC-188" ||
		tint["base_code"] != "DEEP" || tint["container_size_code"] != "GAL" {
		t.Errorf("the tint record = %v", tint)
	}
	comps, _ := tint["components"].([]any)
	if len(comps) != 1 {
		t.Fatalf("components = %d, want 1", len(comps))
	}
	comp := comps[0].(map[string]any)
	if comp["colorant_code"] != "B1" {
		t.Errorf("component = %v", comp)
	}
	amount := comp["amount"].(map[string]any)
	if num(t, amount, "whole_oz") != 1 || num(t, amount, "increments_coarse") != 12 ||
		num(t, amount, "unit_denominator") != 384 {
		t.Errorf("the frozen amount = %v, want 1 OZ + 12/32 in a 1/384 grid", amount)
	}
	// with no charge set the frozen record says the charge was zero, which
	// is what the price carries
	if got := num(t, tint, "tint_charge_cents"); got != 0 {
		t.Errorf("the frozen tint charge = %d, want 0", got)
	}
}

// RULE: a line without a tint is what it was before this build: the same
// unit price, the same total, and no tint key on the wire.
func TestLineWithoutTintIsUnchanged(t *testing.T) {
	testutil.LockOutboxTables(t)
	f := newFixture(t, testutil.RequireDB(t))
	seedTintCatalogue(t, f)
	sale := f.startSale(nil)
	plain := f.addLine(sale, f.productLine("1"))
	if plain.status != http.StatusOK {
		t.Fatalf("add = %d: %s", plain.status, plain.raw)
	}
	l := plain.body["lines"].([]any)[0].(map[string]any)
	if _, has := l["tint"]; !has {
		t.Fatal("the wire shape lost its tint key: a client reads one shape per line")
	}
	if l["tint"] != nil {
		t.Errorf("a plain line carries a tint: %v", l["tint"])
	}
	if got := num(t, l, "unit_price_ten_thousandths"); got != 55000 {
		t.Errorf("unit price = %d, want the engine's 55000", got)
	}
}

// RULE: a tinted line prices as the base price plus the dealer's tint
// charge, and a dealer with the charge set to zero gets the base price.
func TestTintChargePricesBasePlusCharge(t *testing.T) {
	testutil.LockOutboxTables(t)
	f := newFixture(t, testutil.RequireDB(t))
	tintSettingsRows(t, f)
	_, _, _, formulaID := seedTintCatalogue(t, f)
	setPosSetting(t, f.db, "pos.tint_charge_cents", "250")
	sale := f.startSale(nil)
	line := tintLine("1", formulaID, "")
	line["product_id"] = f.productID.String()
	added := f.addLine(sale, line)
	if added.status != http.StatusOK {
		t.Fatalf("add tinted line = %d: %s", added.status, added.raw)
	}
	l := added.body["lines"].([]any)[0].(map[string]any)
	if got := num(t, l, "unit_price_ten_thousandths"); got != 80000 {
		t.Errorf("unit price = %d, want the base 55000 plus the 250 cent charge (80000 ten thousandths)", got)
	}
	tint := tintOf(t, added)
	if got := num(t, tint, "tint_charge_cents"); got != 250 {
		t.Errorf("tint charge = %d, want 250", got)
	}
	// the charge off: the base price alone
	setPosSetting(t, f.db, "pos.tint_charge_cents", "0")
	sale2 := f.startSale(nil)
	line2 := tintLine("1", formulaID, "")
	line2["product_id"] = f.productID.String()
	added2 := f.addLine(sale2, line2)
	if added2.status != http.StatusOK {
		t.Fatalf("add = %d: %s", added2.status, added2.raw)
	}
	l2 := added2.body["lines"].([]any)[0].(map[string]any)
	if got := num(t, l2, "unit_price_ten_thousandths"); got != 55000 {
		t.Errorf("unit price with the charge off = %d, want the base 55000", got)
	}
}

// RULE: the dealer's tint charge is the only charge. A request that carries
// its own number is priced with the setting, and the frozen record states the
// charge the price carries (money checklist item 6: the rule is enforced on
// the server, never in the screen).
func TestTheDealersChargeWinsOverTheRequestsNumber(t *testing.T) {
	testutil.LockOutboxTables(t)
	f := newFixture(t, testutil.RequireDB(t))
	tintSettingsRows(t, f)
	_, _, _, formulaID := seedTintCatalogue(t, f)
	setPosSetting(t, f.db, "pos.tint_charge_cents", "250")
	sale := f.startSale(nil)
	line := tintLine("1", formulaID, "")
	line["product_id"] = f.productID.String()
	line["tint"].(map[string]any)["tint_charge_cents"] = 1
	added := f.addLine(sale, line)
	if added.status != http.StatusOK {
		t.Fatalf("add tinted line = %d: %s", added.status, added.raw)
	}
	l := added.body["lines"].([]any)[0].(map[string]any)
	if got := num(t, l, "unit_price_ten_thousandths"); got != 80000 {
		t.Errorf("unit price = %d, want the base 55000 plus the dealer's 250 cent charge (80000), not the request's 1 cent", got)
	}
	if got := num(t, tintOf(t, added), "tint_charge_cents"); got != 250 {
		t.Errorf("the frozen tint charge = %d, want the dealer's 250", got)
	}
	// a dealer with no charge set: a request that carries one is priced at
	// the base alone, and the record says the charge was zero
	setPosSetting(t, f.db, "pos.tint_charge_cents", "0")
	sale2 := f.startSale(nil)
	line2 := tintLine("1", formulaID, "")
	line2["product_id"] = f.productID.String()
	line2["tint"].(map[string]any)["tint_charge_cents"] = 5000
	added2 := f.addLine(sale2, line2)
	if added2.status != http.StatusOK {
		t.Fatalf("add = %d: %s", added2.status, added2.raw)
	}
	l2 := added2.body["lines"].([]any)[0].(map[string]any)
	if got := num(t, l2, "unit_price_ten_thousandths"); got != 55000 {
		t.Errorf("unit price = %d, want the base 55000 with no charge set", got)
	}
	if got := num(t, tintOf(t, added2), "tint_charge_cents"); got != 0 {
		t.Errorf("the frozen tint charge = %d, want 0, not the request's 5000", got)
	}
}

// RULE: a sold formula is frozen. Changing the catalogue formula afterwards
// does not change the amounts on the sale line.
func TestSoldFormulaIsFrozen(t *testing.T) {
	testutil.LockOutboxTables(t)
	f := newFixture(t, testutil.RequireDB(t))
	_, colorantID, _, formulaID := seedTintCatalogue(t, f)
	sale := f.startSale(nil)
	line := tintLine("1", formulaID, "")
	line["product_id"] = f.productID.String()
	if r := f.addLine(sale, line); r.status != http.StatusOK {
		t.Fatalf("add = %d: %s", r.status, r.raw)
	}
	// the catalogue moves under the sale
	mustExec(t, f.db, `UPDATE paint_formula_components SET whole_oz = 9, increments_coarse = 0, increments_fine = 300
		WHERE formula_id = $1 AND colorant_id = $2`, formulaID, colorantID)
	read := f.do("GET", "/api/v1/pos/transactions/"+sale, nil)
	if read.status != http.StatusOK {
		t.Fatalf("read = %d: %s", read.status, read.raw)
	}
	amount := tintOf(t, read)["components"].([]any)[0].(map[string]any)["amount"].(map[string]any)
	if num(t, amount, "whole_oz") != 1 || num(t, amount, "increments_coarse") != 12 {
		t.Errorf("the sold amounts followed the catalogue: %v", amount)
	}
}

// completeTintedSale rings a one line tinted sale through to completion and
// answers the sale id and its line id.
func completeTintedSale(t *testing.T, f *fixture, formulaID string) (string, string) {
	t.Helper()
	sale := f.startSale(nil)
	line := tintLine("1", formulaID, "")
	line["product_id"] = f.productID.String()
	added := f.addLine(sale, line)
	if added.status != http.StatusOK {
		t.Fatalf("add tinted line = %d: %s", added.status, added.raw)
	}
	lineID := lineIDs(t, added)[0]
	r := f.completeSale(sale, tender("cash", 100000))
	if r.status != http.StatusOK {
		t.Fatalf("complete = %d: %s", r.status, r.raw)
	}
	return sale, lineID
}

// returnOf posts one return of the line at a reason. A tinted line never
// goes back into stock, so the request asks for none.
func returnOf(f *fixture, sale, lineID, reason string) resp {
	return f.do("POST", "/api/v1/pos/returns", map[string]any{
		"register_id": f.register, "refund_method": "cash", "reason": reason,
		"original_sale_id": sale,
		"lines":            []map[string]any{{"line_id": lineID, "quantity": "1", "restock": false, "reason_code": reason}},
	}, "X-Test-Role", "cashier", "X-Test-Sub", mustUUID(f.t))
}

// returnOfRestocking posts one return of the line at a reason, asking for the
// goods back into stock.
func returnOfRestocking(f *fixture, sale, lineID, reason string) resp {
	return f.do("POST", "/api/v1/pos/returns", map[string]any{
		"register_id": f.register, "refund_method": "cash", "reason": reason,
		"original_sale_id": sale,
		"lines":            []map[string]any{{"line_id": lineID, "quantity": "1", "restock": true, "reason_code": reason}},
	}, "X-Test-Role", "cashier", "X-Test-Sub", mustUUID(f.t))
}

// RULE: a tinted can the store does take back does not go back into saleable
// stock: it is re-tinted and put on the marked down rack. The server refuses
// the restock, so the rule does not live only in the screen.
func TestTintedReturnNeverGoesBackIntoStock(t *testing.T) {
	testutil.LockOutboxTables(t)
	f := newFixture(t, testutil.RequireDB(t))
	tintSettingsRows(t, f)
	_, _, _, formulaID := seedTintCatalogue(t, f)
	sale, lineID := completeTintedSale(t, f, formulaID)

	r := returnOfRestocking(f, sale, lineID, "mis_tint")
	if r.status != http.StatusBadRequest || !strings.Contains(string(r.raw), "restock") {
		t.Errorf("a restocking mis tint return of a tinted line = %d, want the restock refusal: %s", r.status, r.raw)
	}
	// the stock never moved: the can is not on the shelf
	onHand := f.scalar(`SELECT ROUND(COALESCE(SUM(quantity), 0) * 10000)::bigint FROM inventory WHERE product_id = $1`,
		f.productID).(int64)
	if onHand <= 0 {
		t.Errorf("the tinted can is on the shelf: %v on hand", onHand)
	}
	// nothing was written: a refused return leaves no return row and no
	// credit memo behind (the money self check, item 16)
	if n := f.scalar(`SELECT count(*) FROM pos_returns WHERE original_transaction_id = $1`, sale).(int64); n != 0 {
		t.Errorf("%d return rows were written by a refused return, want 0", n)
	}
	if n := f.scalar(`SELECT count(*) FROM credit_memos WHERE customer_id = (SELECT customer_id FROM pos_transactions WHERE id = $1)`, sale).(int64); n != 0 {
		t.Errorf("%d credit memos were written by a refused return, want 0", n)
	}
}

// RULE: a mis tint return of a tinted line is accepted and the can does not
// go back into stock: the return names it as not restocked.
func TestMisTintReturnOfATintedLineLeavesTheStockAlone(t *testing.T) {
	testutil.LockOutboxTables(t)
	f := newFixture(t, testutil.RequireDB(t))
	tintSettingsRows(t, f)
	_, _, _, formulaID := seedTintCatalogue(t, f)
	sale, lineID := completeTintedSale(t, f, formulaID)
	before := f.scalar(`SELECT ROUND(COALESCE(SUM(quantity), 0) * 10000)::bigint FROM inventory WHERE product_id = $1`,
		f.productID).(int64)
	r := f.do("POST", "/api/v1/pos/returns", map[string]any{
		"register_id": f.register, "refund_method": "cash", "reason": "mis tint",
		"original_sale_id": sale,
		"lines":            []map[string]any{{"line_id": lineID, "quantity": "1", "restock": false, "reason_code": "mis_tint"}},
	}, "X-Test-Role", "cashier", "X-Test-Sub", mustUUID(f.t))
	if r.status != http.StatusCreated {
		t.Fatalf("a mis tint return that is not restocked = %d: %s", r.status, r.raw)
	}
	after := f.scalar(`SELECT ROUND(COALESCE(SUM(quantity), 0) * 10000)::bigint FROM inventory WHERE product_id = $1`,
		f.productID).(int64)
	if after != before {
		t.Errorf("the stock moved on a tinted return: %v before, %v after", before, after)
	}
}

// RULE: a return of a tinted line is refused with the policy on, allowed
// with the policy off, and a mis tint return is allowed with the policy on
// and recorded as the mis tint.
func TestTintedReturnRules(t *testing.T) {
	testutil.LockOutboxTables(t)
	f := newFixture(t, testutil.RequireDB(t))
	tintSettingsRows(t, f)
	_, _, _, formulaID := seedTintCatalogue(t, f)
	sale, lineID := completeTintedSale(t, f, formulaID)

	// the policy on: the customer choice return is refused
	setPosSetting(t, f.db, "pos.tinted_no_return", "true")
	r := returnOf(f, sale, lineID, "changed_mind")
	if r.status != http.StatusConflict || !strings.Contains(string(r.raw), "tinted_no_return") {
		t.Errorf("a changed mind return of a tinted line = %d, want the tinted_no_return conflict: %s", r.status, r.raw)
	}
	// a refused return writes nothing (the money self check, item 16)
	if n := f.scalar(`SELECT count(*) FROM pos_returns WHERE original_transaction_id = $1`, sale).(int64); n != 0 {
		t.Errorf("%d return rows were written by a refused return, want 0", n)
	}
	if n := f.scalar(`SELECT count(*) FROM pos_return_lines WHERE sale_line_id = $1`, lineID).(int64); n != 0 {
		t.Errorf("%d return lines were written by a refused return, want 0", n)
	}
	// the mis tint returns: the store's own error is the accepted exception
	r = returnOf(f, sale, lineID, "mis_tint")
	if r.status != http.StatusCreated {
		t.Fatalf("a mis tint return = %d: %s", r.status, r.raw)
	}
	var misTint, tintedReturn bool
	if err := f.db.Pool.QueryRow(t.Context(),
		`SELECT mis_tint, tinted_return FROM pos_return_lines WHERE sale_line_id = $1`, lineID).
		Scan(&misTint, &tintedReturn); err != nil {
		t.Fatalf("read the return line: %v", err)
	}
	if !misTint || !tintedReturn {
		t.Errorf("the return line records mis_tint=%v tinted_return=%v, want both true", misTint, tintedReturn)
	}

	// the policy off: a customer choice return goes through
	sale2, line2 := completeTintedSale(t, f, formulaID)
	setPosSetting(t, f.db, "pos.tinted_no_return", "false")
	r = returnOf(f, sale2, line2, "changed_mind")
	if r.status != http.StatusCreated {
		t.Fatalf("a changed mind return with the policy off = %d: %s", r.status, r.raw)
	}
}

// RULE: the over return cap still counts a tinted line the same as any other
// line.
func TestTintedOverReturnCapStillBites(t *testing.T) {
	testutil.LockOutboxTables(t)
	f := newFixture(t, testutil.RequireDB(t))
	tintSettingsRows(t, f)
	_, _, _, formulaID := seedTintCatalogue(t, f)
	sale, lineID := completeTintedSale(t, f, formulaID)
	setPosSetting(t, f.db, "pos.tinted_no_return", "true")
	r := f.do("POST", "/api/v1/pos/returns", map[string]any{
		"register_id": f.register, "refund_method": "cash", "reason": "mis tint",
		"original_sale_id": sale,
		"lines":            []map[string]any{{"line_id": lineID, "quantity": "5", "restock": false, "reason_code": "mis_tint"}},
	}, "X-Test-Role", "cashier", "X-Test-Sub", mustUUID(f.t))
	if r.status != http.StatusConflict || !strings.Contains(string(r.raw), "exceeds_sold") {
		t.Errorf("an over return of a tinted line = %d, want the exceeds_sold conflict: %s", r.status, r.raw)
	}
}

// RULE: three tills add tinted lines to the same sale at once at pool size 4:
// every contender lands its line whole, with its own frozen tint rows.
func TestTintedLineAddsUnderContention(t *testing.T) {
	testutil.LockOutboxTables(t)
	f := newFixture(t, testutil.RequireDBMaxConns(t, 4))
	_, _, _, formulaID := seedTintCatalogue(t, f)
	sale := f.startSale(nil)
	statuses := make([]int, 3)
	var wg sync.WaitGroup
	for i := range statuses {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			line := tintLine("1", formulaID, "")
			line["product_id"] = f.productID.String()
			statuses[i] = f.addLine(sale, line).status
		}(i)
	}
	wg.Wait()
	for i, st := range statuses {
		if st != http.StatusOK {
			t.Errorf("contender %d = %d, want 200", i, st)
		}
	}
	var n int
	if err := f.db.Pool.QueryRow(t.Context(), `
		SELECT count(*) FROM paint_line_tints t
		JOIN pos_line_items l ON l.id = t.counter_line_id
		WHERE l.transaction_id = $1`, sale).Scan(&n); err != nil {
		t.Fatalf("count the tint rows: %v", err)
	}
	if n != 3 {
		t.Errorf("tint rows = %d, want 3 (one whole frozen record per line)", n)
	}
}

// RULE: a quantity change on a tinted line keeps the tint and its charge: the
// formula the customer bought and the dealer's tint charge belong to the line,
// not to the request that first made it. The line reprices; the tint does not
// go away.
func TestTintedLineKeepsItsTintAndChargeOnAQuantityChange(t *testing.T) {
	testutil.LockOutboxTables(t)
	f := newFixture(t, testutil.RequireDB(t))
	tintSettingsRows(t, f)
	_, _, _, formulaID := seedTintCatalogue(t, f)
	setPosSetting(t, f.db, "pos.tint_charge_cents", "250")
	sale := f.startSale(nil)
	line := tintLine("1", formulaID, "")
	line["product_id"] = f.productID.String()
	added := f.addLine(sale, line)
	if added.status != http.StatusOK {
		t.Fatalf("add tinted line = %d: %s", added.status, added.raw)
	}
	lineID := lineIDs(t, added)[0]

	patched := f.patchQty(sale, lineID, "2", rev(t, added))
	if patched.status != http.StatusOK {
		t.Fatalf("quantity change = %d: %s", patched.status, patched.raw)
	}
	tint := tintOf(t, patched)
	if tint == nil {
		t.Fatalf("the quantity change dropped the tint: %s", patched.raw)
	}
	if tint["color_name"] != "Essex Green" {
		t.Errorf("the tint record = %v", tint)
	}
	if got := num(t, tint, "tint_charge_cents"); got != 250 {
		t.Errorf("the tint charge = %d, want 250 (the charge rides the line)", got)
	}
	comps, _ := tint["components"].([]any)
	if len(comps) != 1 {
		t.Fatalf("the components = %d, want 1 (the formula rides the line)", len(comps))
	}
}

// RULE: a customer attached to a sale reprices its lines; a tinted line keeps
// its tint and its charge through that reprice, because the formula the
// customer bought is not the customer's to change.
func TestTintedLineKeepsItsTintWhenTheCustomerChanges(t *testing.T) {
	testutil.LockOutboxTables(t)
	f := newFixture(t, testutil.RequireDB(t))
	tintSettingsRows(t, f)
	_, _, _, formulaID := seedTintCatalogue(t, f)
	setPosSetting(t, f.db, "pos.tint_charge_cents", "250")
	sale := f.startSale(nil)
	line := tintLine("1", formulaID, "")
	line["product_id"] = f.productID.String()
	added := f.addLine(sale, line)
	if added.status != http.StatusOK {
		t.Fatalf("add tinted line = %d: %s", added.status, added.raw)
	}

	attached := f.attach(sale, map[string]any{"customer_id": f.customerID.String()})
	if attached.status != http.StatusOK {
		t.Fatalf("attach the customer = %d: %s", attached.status, attached.raw)
	}
	tint := tintOf(t, attached)
	if tint == nil {
		t.Fatalf("the customer change dropped the tint: %s", attached.raw)
	}
	if got := num(t, tint, "tint_charge_cents"); got != 250 {
		t.Errorf("the tint charge = %d, want 250 (the charge rides the line)", got)
	}
}

// RULE: the dealer's tint rule follows the tint, not only the container (row
// CP-16): a tier covers the base tier it names and the colorant total it
// names, and the flat figure covers the rest. The most specific tier wins,
// so a deep base's tier beats a colorant tier.
func TestTheTintChargeFollowsTheColorantAmountAndTheBaseTier(t *testing.T) {
	testutil.LockOutboxTables(t)
	f := newFixture(t, testutil.RequireDB(t))
	tintSettingsRows(t, f)
	baseID, _, _, formulaID := seedTintCatalogue(t, f)
	setPosSetting(t, f.db, "pos.tint_charge_cents", "1000")
	setPosSetting(t, f.db, "pos.tint_charge_tiers",
		`[{"base_tier":"","min_colorant_oz":"2.0000","charge_cents":4000},{"base_tier":"deep","min_colorant_oz":"","charge_cents":9000}]`)

	chargeOf := func(whole, coarse int) int64 {
		t.Helper()
		sale := f.startSale(nil)
		line := tintLine("1", formulaID, "")
		line["product_id"] = f.productID.String()
		comp := line["tint"].(map[string]any)["components"].([]map[string]any)[0]
		comp["amount"] = map[string]any{"whole_oz": whole, "increments_coarse": coarse,
			"increments_fine": 0, "unit_denominator": 384}
		added := f.addLine(sale, line)
		if added.status != http.StatusOK {
			t.Fatalf("add tinted line = %d: %s", added.status, added.raw)
		}
		return num(t, tintOf(t, added), "tint_charge_cents")
	}
	// 1 OZ + 12/32 is 1.375 oz: under the tier's floor, so the flat figure
	if got := chargeOf(1, 12); got != 1000 {
		t.Errorf("the charge on a 1.375 oz tint = %d, want the flat figure 1000", got)
	}
	// 3 oz is over the floor, so the tier
	if got := chargeOf(3, 0); got != 4000 {
		t.Errorf("the charge on a 3 oz tint = %d, want the tier's 4000", got)
	}
	// the base is the deepest of its system: the deep tier wins whatever the mix weighs
	mustExec(t, f.db, `UPDATE paint_bases SET is_deepest = true WHERE id = $1`, baseID)
	if got := chargeOf(1, 12); got != 9000 {
		t.Errorf("the charge on a deep base's 1.375 oz tint = %d, want the deep tier's 9000", got)
	}
}

// RULE: the dealer writes the tint charge and the tinted no return policy on
// the counter settings resource: an owner or admin writes them as one act on
// the revision, audited, and the counter reads them back.
func TestTheTintChargeAndTheNoReturnPolicyAreWritten(t *testing.T) {
	testutil.LockOutboxTables(t)
	f := newFixture(t, testutil.RequireDB(t))
	tintSettingsRows(t, f)
	t.Cleanup(func() {
		mustExec(t, f.db, `DELETE FROM admin_revisions WHERE resource = 'pos.settings'`)
	})
	get := f.do("GET", "/api/v1/pos/settings", nil, "X-Test-Role", "cashier", "X-Test-Sub", mustUUID(t))
	if get.status != http.StatusOK {
		t.Fatalf("the counter reads the settings = %d: %s", get.status, get.raw)
	}
	rev0 := rev(t, get)
	body := map[string]any{"revision": rev0, "rules": map[string]any{
		"check_limit_cents": nil,
		"check_id_required": map[string]any{"mode": "never"},
		"restocking_fees":   allReasonFees(0, 0, 0),
		"tint_charge": map[string]any{"flat_cents": 250, "tiers": []map[string]any{
			{"base_tier": "", "min_colorant_oz": "2.0000", "charge_cents": 4000}}},
		"tinted_no_return": true}}
	// a cashier reads but never writes
	if r := f.do("PUT", "/api/v1/pos/settings", body,
		"X-Test-Role", "cashier", "X-Test-Sub", mustUUID(t)); r.status != http.StatusForbidden {
		t.Errorf("a cashier writing the tint rules = %d, want 403: %s", r.status, r.raw)
	}
	// the owner writes them
	ownerSub := mustUUID(t)
	put := f.do("PUT", "/api/v1/pos/settings", body,
		"X-Test-Role", "owner", "X-Test-Sub", ownerSub, "If-Match", fmt.Sprintf(`"%d"`, rev0))
	if put.status != http.StatusOK {
		t.Fatalf("the owner writes the tint rules = %d: %s", put.status, put.raw)
	}
	rules, _ := put.body["rules"].(map[string]any)
	charge, _ := rules["tint_charge"].(map[string]any)
	if charge == nil {
		t.Fatalf("the settings carry no tint_charge: %s", put.raw)
	}
	if got, _ := numOf(charge["flat_cents"]); got != 250 {
		t.Errorf("the tint charge's flat figure = %v, want 250", charge["flat_cents"])
	}
	tiers, _ := charge["tiers"].([]any)
	if len(tiers) != 1 {
		t.Fatalf("the tint charge's tiers = %v, want the one tier the owner wrote", charge["tiers"])
	}
	if got, _ := numOf(tiers[0].(map[string]any)["charge_cents"]); got != 4000 {
		t.Errorf("the tier's charge = %v, want 4000", tiers[0])
	}
	if policy, _ := rules["tinted_no_return"].(bool); !policy {
		t.Errorf("the tinted no return policy = %v, want true", rules["tinted_no_return"])
	}
	// the rows hold the rule, and the counter reads it back
	if got := f.scalar(`SELECT value FROM system_settings WHERE key = 'pos.tint_charge_cents'`); got != "250" {
		t.Errorf("the tint charge row = %v, want 250", got)
	}
	if got := f.scalar(`SELECT value FROM system_settings WHERE key = 'pos.tinted_no_return'`); got != "true" {
		t.Errorf("the tinted no return row = %v, want true", got)
	}
	back := f.do("GET", "/api/v1/pos/settings", nil, "X-Test-Role", "cashier", "X-Test-Sub", mustUUID(t))
	backRules, _ := back.body["rules"].(map[string]any)
	backCharge, _ := backRules["tint_charge"].(map[string]any)
	if backCharge == nil {
		t.Fatalf("the counter reads no tint charge back: %s", back.raw)
	}
	if got, _ := numOf(backCharge["flat_cents"]); got != 250 {
		t.Errorf("the counter reads the flat figure %v, want 250", backCharge["flat_cents"])
	}
	if policy, _ := backRules["tinted_no_return"].(bool); !policy {
		t.Errorf("the counter reads the policy %v, want true", backRules["tinted_no_return"])
	}
	// the audit row carries what the write made
	if n := countOf(t, f.db, `SELECT count(*) FROM audit_log WHERE action = 'pos.settings.saved'
		AND user_id::text = $1
		AND changes->>'tint_charge_cents' = '250'
		AND changes->>'tinted_no_return' = 'true'`, ownerSub); n != 1 {
		t.Errorf("the tint rules' audit rows = %d, want 1", n)
	}
}

// RULE: a void never puts a tinted can back into saleable stock (row CP-16),
// the same rule the return path holds: a can the store mixed for a customer
// cannot be sold again as the plain base, so the stock the sale took stays
// taken when the sale is voided.
func TestVoidOfATintedLineDoesNotRestockIt(t *testing.T) {
	testutil.LockOutboxTables(t)
	f := newFixture(t, testutil.RequireDB(t))
	tintSettingsRows(t, f)
	_, _, _, formulaID := seedTintCatalogue(t, f)
	onHand := func() int64 {
		t.Helper()
		return f.scalar(`SELECT ROUND(COALESCE(SUM(quantity), 0) * 10000)::bigint FROM inventory WHERE product_id = $1`,
			f.productID).(int64)
	}
	before := onHand()
	sale, _ := completeTintedSale(t, f, formulaID)
	afterSale := onHand()
	if afterSale >= before {
		t.Fatalf("the tinted sale did not move the stock: %d on hand before, %d after", before, afterSale)
	}
	read := f.do("GET", "/api/v1/pos/transactions/"+sale, nil)
	if read.status != http.StatusOK {
		t.Fatalf("read the sale = %d: %s", read.status, read.raw)
	}
	r := f.do("POST", "/api/v1/pos/transactions/"+sale+"/void",
		map[string]any{"reason": "rung twice", "revision": rev(t, read)},
		"X-Test-Role", "cashier", "X-Test-Sub", mustUUID(t))
	if r.status != http.StatusOK {
		t.Fatalf("void = %d: %s", r.status, r.raw)
	}
	if after := onHand(); after != afterSale {
		t.Errorf("the void put the tinted can back into saleable stock: %d on hand, want %d", after, afterSale)
	}
}
