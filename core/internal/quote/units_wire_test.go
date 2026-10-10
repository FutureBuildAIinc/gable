// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package quote_test

// The quote line units and tallies of ADR 0006 (sections 3.3, 3.4, 4 and
// 7.4), tested end to end like the rest of the module's wire tests: the pair
// resolved from the product's unit set and stored on the line, the stocking
// quantity exact or refused with the nearest quantities, the tally of a
// random length line priced by board foot. Each test states a wire fact.

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/gablelbm/gable/internal/testutil"
	"github.com/google/uuid"
)

// unitsFixture is the wire fixture with the products the unit rules need:
// the fixture's own 2x4x8, a 2x4x14 and a 2x4 random length product stocked
// in LF.
type unitsFixture = fixture

func newUnitsFixture(t *testing.T) *unitsFixture {
	t.Helper()
	testutil.LockOutboxTables(t)
	f := newFixture(t, testutil.RequireDB(t))
	ctx := context.Background()

	// A 2x4x14: 1 BF is 3/28 PCS, the worked refusal of section 3.4.
	f.product14 = uuid.New()
	if _, err := f.db.Pool.Exec(ctx, `INSERT INTO products (id, sku, description, uom_primary,
			board_thickness_in, board_width_in, board_length_ft)
		VALUES ($1, $2, '2x4x14 SPF', 'PCS', 2, 4, 14)`, f.product14, "W14-"+uuid.NewString()[:8]); err != nil {
		t.Fatalf("seed 2x4x14: %v", err)
	}
	if _, err := f.db.Pool.Exec(ctx, `INSERT INTO product_units (product_id, uom, unit_qty, stock_qty, sell, purchase, price) VALUES
		($1, 'BF', 28, 3, TRUE, FALSE, TRUE)`, f.product14); err != nil {
		t.Fatalf("seed 2x4x14 set: %v", err)
	}

	// A 2x4x8 stocked in BF (the rule 3 piece row case): the piece row is
	// PCS (0.1875, 1) and LF (1.5, 1), so a line sold by the foot stocks
	// board feet at 1.5 LF per BF.
	f.bfProduct = uuid.New()
	if _, err := f.db.Pool.Exec(ctx, `INSERT INTO products (id, sku, description, uom_primary,
			board_thickness_in, board_width_in, board_length_ft)
		VALUES ($1, $2, '2x4x8 SYP by board foot', 'BF', 2, 4, 8)`, f.bfProduct, "BFB-"+uuid.NewString()[:8]); err != nil {
		t.Fatalf("seed BF stocked product: %v", err)
	}
	if _, err := f.db.Pool.Exec(ctx, `INSERT INTO product_units (product_id, uom, unit_qty, stock_qty, sell, purchase, price) VALUES
		($1, 'PCS', 0.1875, 1, TRUE, FALSE, FALSE),
		($1, 'LF', 1.5, 1, TRUE, FALSE, FALSE),
		($1, 'MBF', 1, 1000, FALSE, TRUE, TRUE)`, f.bfProduct); err != nil {
		t.Fatalf("seed BF stocked set: %v", err)
	}

	// A 2x4 random length product stocked in LF (section 4's fixture): BF
	// through the cross section, MBF through the standard size, the pair
	// (1500, 1) against LF.
	f.randomProduct = uuid.New()
	if _, err := f.db.Pool.Exec(ctx, `INSERT INTO products (id, sku, description, uom_primary,
			board_thickness_in, board_width_in, random_length)
		VALUES ($1, $2, '2x4 SYP random length', 'LF', 2, 4, TRUE)`, f.randomProduct, "RLX-"+uuid.NewString()[:8]); err != nil {
		t.Fatalf("seed random length: %v", err)
	}
	if _, err := f.db.Pool.Exec(ctx, `INSERT INTO product_units (product_id, uom, unit_qty, stock_qty, sell, purchase, price) VALUES
		($1, 'BF', 1, 1.5, FALSE, FALSE, TRUE),
		($1, 'MBF', 1, 1500, FALSE, TRUE, TRUE)`, f.randomProduct); err != nil {
		t.Fatalf("seed random length set: %v", err)
	}
	return f
}

// quoteLine reads one line of a created quote from the COLUMNS (the exit
// test's rule: read from the columns, not the response).
func (f *unitsFixture) storedLine(t *testing.T, quoteID string) map[string]string {
	t.Helper()
	var uom, priceUOM, uomQty, priceUOMQty, stockUOM, stockQty string
	var thickness, width *string
	err := f.db.Pool.QueryRow(context.Background(), `
		SELECT uom, price_uom, uom_qty::text, price_uom_qty::text, stock_uom, stock_quantity::text,
		       board_thickness_in::text, board_width_in::text
		FROM quote_lines WHERE quote_id = $1 ORDER BY position`, quoteID).
		Scan(&uom, &priceUOM, &uomQty, &priceUOMQty, &stockUOM, &stockQty, &thickness, &width)
	if err != nil {
		t.Fatal(err)
	}
	deref := func(s *string) string {
		if s == nil {
			return ""
		}
		return *s
	}
	return map[string]string{
		"uom": uom, "price_uom": priceUOM, "uom_qty": uomQty, "price_uom_qty": priceUOMQty,
		"stock_uom": stockUOM, "stock_quantity": stockQty,
		"thickness": deref(thickness), "width": deref(width),
	}
}

// TestLinePairResolvedAndStored is the exit test's line (ADR 0006 section
// 9.3): a line sent with uom and price_uom only stores the resolved pair,
// its stocking unit and quantity, read from the columns; a later unit set
// change leaves them as they were.
func TestLinePairResolvedAndStored(t *testing.T) {
	f := newUnitsFixture(t)

	line := f.line("10")
	line["price_uom"] = "MBF"
	created := f.create(line)
	id := str(t, created.body, "id")

	stored := f.storedLine(t, id)
	for field, want := range map[string]string{
		"uom": "PCS", "price_uom": "MBF", "uom_qty": "187.5000", "price_uom_qty": "1.0000",
		"stock_uom": "PCS", "stock_quantity": "10.0000",
	} {
		if stored[field] != want {
			t.Errorf("stored %s = %s, want %s", field, stored[field], want)
		}
	}

	// A later unit set change leaves the written line exactly as it was:
	// every line stores its own pair (section 3.3, the kit explosion
	// posture). Changing an existing row's pair is the allowed change.
	if _, err := f.db.Pool.Exec(context.Background(),
		`UPDATE product_units SET unit_qty = 1, stock_qty = 2000 WHERE product_id = $1 AND uom = 'MBF'`, f.productID); err != nil {
		t.Fatal(err)
	}
	after := f.storedLine(t, id)
	for k, v := range stored {
		if after[k] != v {
			t.Errorf("a unit set change moved the line's %s from %s to %s", k, v, after[k])
		}
	}
}

// TestLineOnABoardFootStockedProduct proves the stocking quantity on a
// product stocked in BF whose LF row came from the piece row derivation
// (1.5 LF per BF, never board_length_ft LF per BF): a line of 12 LF stocks
// exactly 8 BF and prices per MBF through the pair (1500, 1).
func TestLineOnABoardFootStockedProduct(t *testing.T) {
	f := newUnitsFixture(t)

	line := map[string]any{
		"product_id": f.bfProduct.String(), "quantity": "12", "uom": "LF",
		"price_uom": "MBF", "unit_price_ten_thousandths": 5000000,
	}
	r := f.do("POST", "/api/v1/quotes", f.createBody(line))
	if r.status != http.StatusCreated {
		t.Fatalf("12 LF of the BF stocked 2x4x8 = %d: %s", r.status, r.raw)
	}
	stored := f.storedLine(t, str(t, r.body, "id"))
	for field, want := range map[string]string{
		"uom": "LF", "price_uom": "MBF", "uom_qty": "1500.0000", "price_uom_qty": "1.0000",
		"stock_uom": "BF", "stock_quantity": "8.0000",
	} {
		if stored[field] != want {
			t.Errorf("stored %s = %s, want %s", field, stored[field], want)
		}
	}
	if got := num(t, r.body["lines"].([]any)[0].(map[string]any), "line_total_cents"); got != 400 {
		t.Errorf("line_total_cents = %d, want 400 (12 LF is 0.008 MBF at 500.00)", got)
	}

	// A quantity that is not an exact multiple of the smallest exact LF
	// step is refused with the neighbouring multiples of it: 1.5 LF per BF
	// makes the step 0.0003 LF, so 10 LF names 9.9999 and 10.0002.
	line["quantity"] = "10"
	r = f.do("POST", "/api/v1/quotes", f.createBody(line))
	if r.status != http.StatusBadRequest || !strings.Contains(string(r.raw), "lines[0].quantity") ||
		!strings.Contains(string(r.raw), "9.9999") || !strings.Contains(string(r.raw), "10.0002") {
		t.Errorf("10 LF does not convert exactly into BF, got %d: %s", r.status, r.raw)
	}
}

// TestSentPairStoredCanonically proves a product line's sent pair that
// equals the resolved pair as a ratio is stored as the canonical resolved
// pair itself (ADR 0006 section 3.3, R2): sending 375 and 2 on the 2x4x8
// by the piece per MBF stores 187.5 and 1, on the wire and in the columns,
// so one conversion holds one byte form.
func TestSentPairStoredCanonically(t *testing.T) {
	f := newUnitsFixture(t)

	sent := f.line("10")
	sent["price_uom"] = "MBF"
	sent["uom_qty"], sent["price_uom_qty"] = "375", "2"
	r := f.do("POST", "/api/v1/quotes", f.createBody(sent))
	if r.status != http.StatusCreated {
		t.Fatalf("an equivalent sent pair = %d, want 201: %s", r.status, r.raw)
	}
	line := r.body["lines"].([]any)[0].(map[string]any)
	if line["uom_qty"] != "187.5" || line["price_uom_qty"] != "1" {
		t.Errorf("the answer carries the canonical pair, got %v and %v; want 187.5 and 1",
			line["uom_qty"], line["price_uom_qty"])
	}
	stored := f.storedLine(t, str(t, r.body, "id"))
	if stored["uom_qty"] != "187.5000" || stored["price_uom_qty"] != "1.0000" {
		t.Errorf("the columns store the canonical pair, got %s and %s; want 187.5 and 1",
			stored["uom_qty"], stored["price_uom_qty"])
	}

	// A non product line's standard size derivation stores the derived pair
	// too when the sent pair agrees: EA priced per M sends (2000, 2), the
	// same ratio as (1000, 1), and stores (1000, 1).
	free := f.nonStockLine("2000", "EA")
	free["price_uom"] = "M"
	free["unit_price_ten_thousandths"] = 37500
	free["uom_qty"], free["price_uom_qty"] = "2000", "2"
	r = f.do("POST", "/api/v1/quotes", f.createBody(free))
	if r.status != http.StatusCreated {
		t.Fatalf("an equivalent standard size pair = %d, want 201: %s", r.status, r.raw)
	}
	line = r.body["lines"].([]any)[0].(map[string]any)
	if line["uom_qty"] != "1000" || line["price_uom_qty"] != "1" {
		t.Errorf("the non product line stores the derived canonical pair, got %v and %v; want 1000 and 1",
			line["uom_qty"], line["price_uom_qty"])
	}
}

// TestQuoteLineUnitRefusals proves the unit rules of sections 3.3 and 3.4 on
// the wire: a disagreeing pair refused naming lines[i].uom_qty with the pair
// to send, a non sale unit refused, a quantity that does not convert exactly
// into the stocking unit refused with the nearest quantities that do, and an
// inactive unit refused on a new line.
func TestQuoteLineUnitRefusals(t *testing.T) {
	f := newUnitsFixture(t)

	// A sent pair must equal the resolved one as a ratio.
	disagree := f.line("10")
	disagree["price_uom"] = "MBF"
	disagree["uom_qty"], disagree["price_uom_qty"] = "1000", "1"
	r := f.do("POST", "/api/v1/quotes", f.createBody(disagree))
	if r.status != http.StatusBadRequest {
		t.Fatalf("a disagreeing pair = %d, want 400: %s", r.status, r.raw)
	}
	body := string(r.raw)
	if !strings.Contains(body, "lines[0].uom_qty") || !strings.Contains(body, "187.5 and 1") {
		t.Errorf("the refusal names lines[0].uom_qty and the pair to send, got %s", body)
	}

	// A non sale unit of the product: the 2x4x8 sells in PCS and LF, not BF.
	nonSale := f.line("10")
	nonSale["uom"] = "BF"
	r = f.do("POST", "/api/v1/quotes", f.createBody(nonSale))
	if r.status != http.StatusBadRequest || !strings.Contains(string(r.raw), "lines[0].uom") {
		t.Fatalf("a non sale unit = %d, want 400 naming lines[0].uom: %s", r.status, r.raw)
	}
	if !strings.Contains(string(r.raw), "not a sale unit") {
		t.Errorf("the refusal says the unit is not a sale unit of the product, got %s", r.raw)
	}

	// 10 BF of a 2x4x14, where 1 BF is 3/28 PCS: not exact at scale 4, and
	// the refusal names the neighbouring multiples of 0.0028 BF (section
	// 3.4's worked case).
	inexact := map[string]any{
		"product_id": f.product14.String(), "quantity": "10", "uom": "BF",
		"price_uom": "BF", "unit_price_ten_thousandths": 7000,
	}
	r = f.do("POST", "/api/v1/quotes", f.createBody(inexact))
	if r.status != http.StatusBadRequest {
		t.Fatalf("10 BF of a 2x4x14 = %d, want 400: %s", r.status, r.raw)
	}
	body = string(r.raw)
	if !strings.Contains(body, "lines[0].quantity") ||
		!strings.Contains(body, "9.9988") || !strings.Contains(body, "10.0016") {
		t.Errorf("the refusal names lines[0].quantity and the nearest exact quantities, got %s", body)
	}
	// 28 BF converts exactly (3 PCS).
	exact := map[string]any{
		"product_id": f.product14.String(), "quantity": "28", "uom": "BF",
		"price_uom": "BF", "unit_price_ten_thousandths": 7000,
	}
	r = f.do("POST", "/api/v1/quotes", f.createBody(exact))
	if r.status != http.StatusCreated {
		t.Fatalf("28 BF of a 2x4x14 = %d, want 201: %s", r.status, r.raw)
	}
	stored := f.storedLine(t, str(t, r.body, "id"))
	if stored["stock_uom"] != "PCS" || stored["stock_quantity"] != "3.0000" {
		t.Errorf("28 BF stocks 3 PCS, got %v", stored)
	}

	// An inactive unit cannot enter a new line.
	if _, err := f.db.Pool.Exec(context.Background(), `UPDATE units SET is_active = FALSE WHERE code = 'CY'`); err != nil {
		t.Fatal(err)
	}
	defer func() {
		_, _ = f.db.Pool.Exec(context.Background(), `UPDATE units SET is_active = TRUE WHERE code = 'CY'`)
	}()
	inactive := f.nonStockLine("2", "CY")
	r = f.do("POST", "/api/v1/quotes", f.createBody(inactive))
	if r.status != http.StatusBadRequest || !strings.Contains(string(r.raw), "lines[0].uom") ||
		!strings.Contains(string(r.raw), "inactive") {
		t.Fatalf("an inactive unit on a new line = %d, want 400 naming lines[0].uom: %s", r.status, r.raw)
	}
}

// TestRandomLengthTally is the exit test's line (ADR 0006 section 9.3): the
// 2x4 tally of 10 at 8 and 5 at 14 at 500.00 per MBF is 150 linear feet, 100
// board feet and 5000 cents; the 10 at 10 tally is 3333 cents with
// board_feet 66.6667; a quantity that disagrees with the rows is refused, a
// tally on a fixed length product is refused, and a read only field in the
// request is a 400 naming it.
func TestRandomLengthTally(t *testing.T) {
	f := newUnitsFixture(t)

	newQuote := func(line map[string]any) resp {
		line["product_id"] = f.randomProduct.String()
		return f.do("POST", "/api/v1/quotes", f.createBody(line))
	}

	// 10 at 8 and 5 at 14: 150 linear feet, exactly 100 board feet, 50.00.
	r := newQuote(map[string]any{
		"uom": "LF", "price_uom": "MBF", "unit_price_ten_thousandths": 5000000,
		"tally": map[string]any{"rows": []any{
			map[string]any{"pieces": 10, "length_ft": "8"},
			map[string]any{"pieces": 5, "length_ft": "14"},
		}},
	})
	if r.status != http.StatusCreated {
		t.Fatalf("the tallied line = %d: %s", r.status, r.raw)
	}
	line := r.body["lines"].([]any)[0].(map[string]any)
	if line["quantity"] != "150" || line["uom"] != "LF" || line["price_uom"] != "MBF" ||
		line["uom_qty"] != "1500" || line["price_uom_qty"] != "1" {
		t.Errorf("the tallied line = %v, want 150 LF priced per MBF at 1500 to 1", line)
	}
	if got := num(t, line, "line_total_cents"); got != 5000 {
		t.Errorf("line_total_cents = %d, want 5000", got)
	}
	tally := line["tally"].(map[string]any)
	if tally["thickness_in"] != "2" || tally["width_in"] != "4" || tally["linear_feet"] != "150" ||
		tally["board_feet"] != "100" || len(tally["rows"].([]any)) != 2 {
		t.Errorf("the tally on the wire = %v, want the cross section, 150 feet and 100 board feet", tally)
	}
	if line["stock_uom"] != "LF" || line["stock_quantity"] != "150" {
		t.Errorf("a tallied line stocks linear feet, got %v", line)
	}

	// The rows are stored, read back through the columns.
	var rows int
	if err := f.db.Pool.QueryRow(context.Background(), `
		SELECT count(*) FROM line_tally_rows tr
		JOIN quote_lines ql ON ql.id = tr.quote_line_id
		JOIN quotes q ON q.id = ql.quote_id
		WHERE q.customer_id = $1 AND ql.quantity = 150`, f.customerID).Scan(&rows); err != nil || rows != 2 {
		t.Errorf("the tally rows are stored, got %d (%v)", rows, err)
	}
	stored := f.storedLine(t, str(t, r.body, "id"))
	if stored["thickness"] != "2.0000" || stored["width"] != "4.0000" {
		t.Errorf("the cross section is snapshotted on the line, got %v", stored)
	}

	// 10 at 10: 100 linear feet, 200/3 board feet displayed as 66.6667, and
	// 33.3333... cents' worth rounded once to 3333.
	r = newQuote(map[string]any{
		"uom": "LF", "price_uom": "MBF", "unit_price_ten_thousandths": 5000000,
		"tally": map[string]any{"rows": []any{map[string]any{"pieces": 10, "length_ft": "10"}}},
	})
	if r.status != http.StatusCreated {
		t.Fatalf("the 10 at 10 tally = %d: %s", r.status, r.raw)
	}
	line = r.body["lines"].([]any)[0].(map[string]any)
	tally = line["tally"].(map[string]any)
	if tally["board_feet"] != "66.6667" || line["quantity"] != "100" {
		t.Errorf("the 10 at 10 tally = %v, want 100 feet and 66.6667 board feet", tally)
	}
	if got := num(t, line, "line_total_cents"); got != 3333 {
		t.Errorf("line_total_cents = %d, want 3333", got)
	}

	// A quantity that disagrees with the rows is refused naming it.
	r = newQuote(map[string]any{
		"quantity": "149", "uom": "LF", "price_uom": "MBF", "unit_price_ten_thousandths": 5000000,
		"tally": map[string]any{"rows": []any{
			map[string]any{"pieces": 10, "length_ft": "8"},
			map[string]any{"pieces": 5, "length_ft": "14"},
		}},
	})
	if r.status != http.StatusBadRequest || !strings.Contains(string(r.raw), "lines[0].quantity") {
		t.Fatalf("a disagreeing quantity = %d, want 400 naming lines[0].quantity: %s", r.status, r.raw)
	}

	// A tally on a fixed length product is refused naming it.
	fixed := f.line("80")
	fixed["uom"] = "LF"
	fixed["tally"] = map[string]any{"rows": []any{map[string]any{"pieces": 10, "length_ft": "8"}}}
	r = f.do("POST", "/api/v1/quotes", f.createBody(fixed))
	if r.status != http.StatusBadRequest || !strings.Contains(string(r.raw), "lines[0].tally") {
		t.Fatalf("a tally on a fixed length product = %d, want 400 naming lines[0].tally: %s", r.status, r.raw)
	}

	// A read only field in the request is a 400 naming it.
	r = newQuote(map[string]any{
		"uom": "LF", "price_uom": "MBF", "unit_price_ten_thousandths": 5000000,
		"tally": map[string]any{"rows": []any{map[string]any{"pieces": 10, "length_ft": "8"}}, "board_feet": "100"},
	})
	if r.status != http.StatusBadRequest || !strings.Contains(string(r.raw), "board_feet") {
		t.Fatalf("a read only tally field = %d, want 400 naming it: %s", r.status, r.raw)
	}
}

// TestTallySumBeyondTheBoundRefused proves the tally's sum cannot overflow
// its int64 accumulator and be accepted as a small quantity: 19 rows of
// 1,000,000 pieces, each within the row limits, whose true sum is 2^64 +
// 448384 scaled units, is a 400 naming lines[0].tally that states the
// quantity bound, never a 201 for the wrapped 44.8384.
func TestTallySumBeyondTheBoundRefused(t *testing.T) {
	f := newUnitsFixture(t)

	lengths := []string{
		"99999999.9983", "99999999.9982", "99999999.9981", "99999999.998", "99999999.9979",
		"99999999.9978", "99999999.9977", "99999999.9976", "99999999.9975", "99999999.9974",
		"99999999.9973", "99999999.9972", "99999999.9971", "99999999.997", "99999999.9969",
		"99999999.9968", "99999999.9967", "99999999.9966", "44674407.4169",
	}
	rows := make([]any, len(lengths))
	for i, l := range lengths {
		rows[i] = map[string]any{"pieces": 1000000, "length_ft": l}
	}
	r := f.do("POST", "/api/v1/quotes", f.createBody(map[string]any{
		"product_id": f.randomProduct.String(),
		"uom":        "LF", "price_uom": "MBF", "unit_price_ten_thousandths": 5000000,
		"tally": map[string]any{"rows": rows},
	}))
	if r.status != http.StatusBadRequest {
		t.Fatalf("a tally whose sum wraps past 2^64 = %d, want 400: %s", r.status, r.raw)
	}
	if !strings.Contains(string(r.raw), "lines[0].tally") || !strings.Contains(string(r.raw), "99999999.9999") {
		t.Errorf("the refusal names lines[0].tally and states the quantity bound, got %s", r.raw)
	}
	// The quote was never written.
	var n int
	if err := f.db.Pool.QueryRow(context.Background(),
		`SELECT count(*) FROM line_tally_rows`).Scan(&n); err != nil || n != 0 {
		t.Errorf("the refused tally left no row behind, got %d (%v)", n, err)
	}
}

// TestNonProductLineStandardPair proves the kept R1-15 rule with section
// 3.3's refinement: a non stock line's pair is the client's, except that two
// units with standard sizes in one dimension derive it (EA per M is
// (1000, 1)) and a sent pair that disagrees is refused.
func TestNonProductLineStandardPair(t *testing.T) {
	f := newUnitsFixture(t)

	derived := f.nonStockLine("2000", "EA")
	derived["price_uom"] = "M"
	derived["unit_price_ten_thousandths"] = 37500 // 3.75 per M
	r := f.do("POST", "/api/v1/quotes", f.createBody(derived))
	if r.status != http.StatusCreated {
		t.Fatalf("EA priced per M with no pair = %d, want 201: %s", r.status, r.raw)
	}
	line := r.body["lines"].([]any)[0].(map[string]any)
	if line["uom_qty"] != "1000" || line["price_uom_qty"] != "1" {
		t.Errorf("the derived pair = %v %v, want 1000 and 1", line["uom_qty"], line["price_uom_qty"])
	}
	if got := num(t, line, "line_total_cents"); got != 750 { // 2000 EA x 0.375 = 750 cents
		t.Errorf("line_total_cents = %d, want 750", got)
	}

	disagree := f.nonStockLine("2000", "EA")
	disagree["price_uom"] = "M"
	disagree["unit_price_ten_thousandths"] = 37500
	disagree["uom_qty"], disagree["price_uom_qty"] = "100", "1"
	r = f.do("POST", "/api/v1/quotes", f.createBody(disagree))
	if r.status != http.StatusBadRequest || !strings.Contains(string(r.raw), "lines[0].uom_qty") {
		t.Fatalf("a pair that disagrees with the standard sizes = %d, want 400: %s", r.status, r.raw)
	}
}
