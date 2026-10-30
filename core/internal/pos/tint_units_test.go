// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package pos_test

// The tint charge is a charge per container (per sale unit), and the line
// extension scales a price per PRICE unit by the unit ratio. A tinted line
// whose sale unit is not its price unit must still carry exactly the frozen
// charge times the quantity on top of the base extension (debt read of CP-16,
// P2-219-1). Where the charge cannot be carried exactly in the price unit the
// tint is refused with a plain message, never rounded.

import (
	"net/http"
	"strings"
	"testing"

	"github.com/gablelbm/gable/internal/testutil"
	"github.com/google/uuid"
)

// gallonProduct is a paint base priced per GAL, sold by the quart and by a
// five gallon pail (BUNDLE stands for the pail in the unit table).
func gallonProduct(t *testing.T, f *fixture) uuid.UUID {
	t.Helper()
	id := uuid.New()
	mustExec(t, f.db, `INSERT INTO products (id, sku, description, uom_primary, base_price, average_unit_cost)
		VALUES ($1, $2, 'Deep base paint', 'GAL', 40, 20)`, id, "POS-GAL-"+uuid.NewString()[:8])
	mustExec(t, f.db, `INSERT INTO product_units (product_id, uom, unit_qty, stock_qty, sell, purchase, price)
		VALUES ($1, 'QT', 1, 0.25, TRUE, FALSE, FALSE), ($1, 'BUNDLE', 1, 5, TRUE, FALSE, FALSE), ($1, 'BAG', 1, 3, TRUE, FALSE, FALSE)`, id)
	mustExec(t, f.db, `INSERT INTO inventory (product_id, location_id, location, quantity, allocated) VALUES ($1, $2, 'Y', 500, 0)`, id, f.yardID)
	t.Cleanup(func() {
		_, _ = f.db.Pool.Exec(t.Context(), `DELETE FROM pos_line_items WHERE product_id = $1`, id)
		_, _ = f.db.Pool.Exec(t.Context(), `DELETE FROM inventory WHERE product_id = $1`, id)
		_, _ = f.db.Pool.Exec(t.Context(), `DELETE FROM products WHERE id = $1`, id)
	})
	return id
}

// unitLine is a gallon product line sold in uom with the stated pair.
func unitLine(prod uuid.UUID, qty, uom, uomQty, priceUOMQty string) map[string]any {
	return map[string]any{"product_id": prod.String(), "quantity": qty, "uom": uom, "price_uom": "GAL",
		"uom_qty": uomQty, "price_uom_qty": priceUOMQty}
}

// lineTotal adds a line and answers its total, or fails the test.
func addedTotal(t *testing.T, r resp) int64 {
	t.Helper()
	if r.status != http.StatusOK {
		t.Fatalf("add = %d: %s", r.status, r.raw)
	}
	return num(t, r.body["lines"].([]any)[0].(map[string]any), "line_total_cents")
}

// RULE: whatever the sale unit, the tinted line's total less the same line
// untinted is the frozen tint charge times the quantity, to the cent.
func TestTintChargeIsPerContainerWhateverTheSaleUnit(t *testing.T) {
	testutil.LockOutboxTables(t)
	f := newFixture(t, testutil.RequireDB(t))
	tintSettingsRows(t, f)
	_, _, _, formulaID := seedTintCatalogue(t, f)
	setPosSetting(t, f.db, "pos.tint_charge_cents", "500")
	prod := gallonProduct(t, f)
	cases := []struct {
		name, qty, uom, uomQty, priceUOMQty string
		extra                               int64
	}{
		{"quart priced per gallon", "3", "QT", "4", "1", 1500},
		{"five gallon pail priced per gallon", "2", "BUNDLE", "1", "5", 1000},
		{"gallon priced per gallon", "2", "GAL", "1", "1", 1000},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			plain := addedTotal(t, f.addLine(f.startSale(nil), unitLine(prod, c.qty, c.uom, c.uomQty, c.priceUOMQty)))
			line := unitLine(prod, c.qty, c.uom, c.uomQty, c.priceUOMQty)
			line["tint"] = tintLine("1", formulaID, "")["tint"]
			r := f.addLine(f.startSale(nil), line)
			tinted := addedTotal(t, r)
			if got := tinted - plain; got != c.extra {
				t.Errorf("tinted %d less plain %d = %d, want the frozen charge times the quantity %d", tinted, plain, got, c.extra)
			}
			if got := num(t, tintOf(t, r), "tint_charge_cents"); got != 500 {
				t.Errorf("frozen tint charge = %d, want 500", got)
			}
		})
	}
}

// RULE: a quantity change keeps the same per container charge, one charge.
func TestTintChargePerContainerSurvivesAQuantityChange(t *testing.T) {
	testutil.LockOutboxTables(t)
	f := newFixture(t, testutil.RequireDB(t))
	tintSettingsRows(t, f)
	_, _, _, formulaID := seedTintCatalogue(t, f)
	setPosSetting(t, f.db, "pos.tint_charge_cents", "500")
	prod := gallonProduct(t, f)
	plain := addedTotal(t, f.addLine(f.startSale(nil), unitLine(prod, "7", "QT", "4", "1")))
	sale := f.startSale(nil)
	line := unitLine(prod, "3", "QT", "4", "1")
	line["tint"] = tintLine("1", formulaID, "")["tint"]
	added := f.addLine(sale, line)
	addedTotal(t, added)
	changed := f.patchQty(sale, lineIDs(t, added)[0], "7", rev(t, added))
	if changed.status != http.StatusOK {
		t.Fatalf("patch = %d: %s", changed.status, changed.raw)
	}
	got := num(t, changed.body["lines"].([]any)[0].(map[string]any), "line_total_cents")
	if got-plain != 3500 {
		t.Errorf("after the quantity change tinted %d less plain %d = %d, want 3500", got, plain, got-plain)
	}
}

// RULE: where the charge cannot be carried exactly in the price unit, the
// tint is refused and says why; no line is written.
func TestTintRefusedWhereTheChargeCannotBeCarriedExactly(t *testing.T) {
	testutil.LockOutboxTables(t)
	f := newFixture(t, testutil.RequireDB(t))
	tintSettingsRows(t, f)
	_, _, _, formulaID := seedTintCatalogue(t, f)
	setPosSetting(t, f.db, "pos.tint_charge_cents", "500")
	prod := gallonProduct(t, f)
	sale := f.startSale(nil)
	// 3 gallons to the container: 50000 / 3 is not a whole price figure
	line := unitLine(prod, "1", "BAG", "1", "3")
	line["tint"] = tintLine("1", formulaID, "")["tint"]
	r := f.addLine(sale, line)
	if r.status != http.StatusBadRequest {
		t.Fatalf("add = %d, want 400: %s", r.status, r.raw)
	}
	if !strings.Contains(string(r.raw), "tint") {
		t.Errorf("the refusal does not say it is the tint: %s", r.raw)
	}
	read := f.do("GET", "/api/v1/pos/transactions/"+sale, nil)
	if raw, _ := read.body["lines"].([]any); len(raw) != 0 {
		t.Errorf("a refused tint left %d lines on the sale", len(raw))
	}
}
