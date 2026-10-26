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
	if tint["tint_charge_cents"] != nil {
		t.Errorf("with no charge set the tint carries no charge: %v", tint["tint_charge_cents"])
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

// returnOf posts one return of the line at a reason.
func returnOf(f *fixture, sale, lineID, reason string) resp {
	return f.do("POST", "/api/v1/pos/returns", map[string]any{
		"register_id": f.register, "refund_method": "cash", "reason": reason,
		"original_sale_id": sale,
		"lines":            []map[string]any{{"line_id": lineID, "quantity": "1", "reason_code": reason}},
	}, "X-Test-Role", "cashier", "X-Test-Sub", mustUUID(f.t))
}

// RULE: a return of a tinted line is refused with the policy on, allowed
// with the policy off, and a mis tint return is allowed with the policy on
// and recorded as the mis tint.
func TestTintedReturnRules(t *testing.T) {
	testutil.LockOutboxTables(t)
	f := newFixture(t, testutil.RequireDB(t))
	_, _, _, formulaID := seedTintCatalogue(t, f)
	sale, lineID := completeTintedSale(t, f, formulaID)

	// the policy on: the customer choice return is refused
	setPosSetting(t, f.db, "pos.tinted_no_return", "true")
	r := returnOf(f, sale, lineID, "changed_mind")
	if r.status != http.StatusConflict || !strings.Contains(string(r.raw), "tinted_no_return") {
		t.Errorf("a changed mind return of a tinted line = %d, want the tinted_no_return conflict: %s", r.status, r.raw)
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
	_, _, _, formulaID := seedTintCatalogue(t, f)
	sale, lineID := completeTintedSale(t, f, formulaID)
	setPosSetting(t, f.db, "pos.tinted_no_return", "true")
	r := f.do("POST", "/api/v1/pos/returns", map[string]any{
		"register_id": f.register, "refund_method": "cash", "reason": "mis tint",
		"original_sale_id": sale,
		"lines":            []map[string]any{{"line_id": lineID, "quantity": "5", "reason_code": "mis_tint"}},
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
