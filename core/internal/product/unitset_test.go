// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package product_test

// The product unit set on the wire (ADR 0006 sections 3.1 to 3.3), tested
// end to end like the rest of the module: the C3-2A-units list of section
// 9.2, each line a wire fact. The derivations, the agreement refusals, the
// warnings, the stocking unit hold and the invariants.

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gablelbm/gable/internal/platform/httpx"
	"github.com/gablelbm/gable/internal/product"
	"github.com/gablelbm/gable/internal/testutil"
	"github.com/gablelbm/gable/pkg/database"
	"github.com/google/uuid"
)

// setFixture is kitFixture with a name of its own for the unit set routes.
type setFixture = kitFixture

func newSetFixture(t *testing.T) *setFixture {
	t.Helper()
	return newKitFixture(t, nil, testutil.RequireDB(t))
}

// createBoard posts a product carrying the board measure columns and hands
// back its id and revision.
func (f *setFixture) createBoard(overrides map[string]any) (string, int64) {
	f.t.Helper()
	body := map[string]any{
		"sku":         "SET-" + uuid.NewString()[:12],
		"description": "board product",
		"stock_uom":   "PCS",
	}
	for k, v := range overrides {
		body[k] = v
	}
	res := f.do("POST", "/api/v1/products", body)
	if res.status != http.StatusCreated {
		f.t.Fatalf("create product = %d: %s", res.status, res.raw)
	}
	rev, _ := res.body["revision"].(json.Number).Int64()
	return res.body["id"].(string), rev
}

// putUnits replaces a product's unit set.
func (f *setFixture) putUnits(id string, body map[string]any) resp {
	f.t.Helper()
	return f.do("PUT", "/api/v1/products/"+id+"/units", body)
}

// unitsBody builds a PUT body from the parts.
func unitsBody(revision int64, stock, sale, price, purchase string, units []any) map[string]any {
	return map[string]any{
		"revision": revision, "stock_uom": stock, "sale_uom": sale,
		"price_uom": price, "purchase_uom": purchase, "units": units,
	}
}

func unitRow(uom string, sell, purchase, price bool, pair ...string) map[string]any {
	row := map[string]any{"uom": uom, "sell": sell, "purchase": purchase, "price": price}
	if len(pair) == 2 {
		row["unit_qty"], row["stock_qty"] = pair[0], pair[1]
	}
	return row
}

// rowOf reads one row of the answered set.
func rowOf(t *testing.T, body map[string]any, uom string) map[string]any {
	t.Helper()
	for _, it := range body["units"].([]any) {
		row := it.(map[string]any)
		if row["uom"] == uom {
			return row
		}
	}
	t.Fatalf("no %s row in the answer: %v", uom, body["units"])
	return nil
}

// TestUnitSetDerivations proves each derivation rule of ADR 0006 section
// 3.2 reads back exactly: the 2x4x8 stores PCS (1, 1), LF (8, 1), BF
// (1, 0.1875) and MBF (1, 187.5); the 2x4 random length product stocked in
// LF stores BF (1, 1.5) and MBF (1, 1500); a client pair is stored
// canonically; and the answer carries the four defaults.
func TestUnitSetDerivations(t *testing.T) {
	f := newSetFixture(t)

	// The 2x4x8: rules 1 to 4, LF as (8, 1).
	fixed, rev := f.createBoard(map[string]any{
		"board_thickness_in": "2", "board_width_in": "4", "board_length_ft": "8",
	})
	body := unitsBody(rev, "PCS", "PCS", "PCS", "PCS", []any{
		unitRow("PCS", true, true, true),
		unitRow("LF", true, false, false),
		unitRow("BF", false, false, true),
		unitRow("MBF", false, true, true),
	})
	res := f.putUnits(fixed, body)
	if res.status != http.StatusOK {
		t.Fatalf("the 2x4x8 PUT = %d: %s", res.status, res.raw)
	}
	for uom, want := range map[string][2]string{
		"PCS": {"1", "1"}, "LF": {"8", "1"}, "BF": {"1", "0.1875"}, "MBF": {"1", "187.5"},
	} {
		row := rowOf(t, res.body, uom)
		if row["unit_qty"] != want[0] || row["stock_qty"] != want[1] {
			t.Errorf("%s stored (%v, %v); want (%s, %s)", uom, row["unit_qty"], row["stock_qty"], want[0], want[1])
		}
	}
	for field, want := range map[string]string{
		"stock_uom": "PCS", "sale_uom": "PCS", "price_uom": "PCS", "purchase_uom": "PCS",
	} {
		if res.body[field] != want {
			t.Errorf("%s = %v; want %s", field, res.body[field], want)
		}
	}
	if rev, _ := res.body["revision"].(json.Number).Int64(); rev != 2 {
		t.Errorf("the PUT moves the revision to 2, got %v", res.body["revision"])
	}

	// The 2x4 random length: stocked in LF, BF through the cross section,
	// MBF through the standard size.
	random, rrev := f.createBoard(map[string]any{
		"stock_uom": "LF", "board_thickness_in": "2", "board_width_in": "4", "random_length": true,
	})
	res = f.putUnits(random, unitsBody(rrev, "LF", "LF", "LF", "LF", []any{
		unitRow("LF", true, true, true),
		unitRow("BF", false, false, true),
		unitRow("MBF", false, true, true),
	}))
	if res.status != http.StatusOK {
		t.Fatalf("the random length PUT = %d: %s", res.status, res.raw)
	}
	for uom, want := range map[string][2]string{
		"LF": {"1", "1"}, "BF": {"1", "1.5"}, "MBF": {"1", "1500"},
	} {
		row := rowOf(t, res.body, uom)
		if row["unit_qty"] != want[0] || row["stock_qty"] != want[1] {
			t.Errorf("%s stored (%v, %v); want (%s, %s)", uom, row["unit_qty"], row["stock_qty"], want[0], want[1])
		}
	}

	// The 2x4x8 stocked in each of BF, LF and MBF: the piece row (PCS) is
	// sent and the other rows derive through it (rule 3 anchors a COUNT
	// piece row, never the stocking row). One piece is 16/3 BF: against BF
	// stock the PCS row is (0.1875, 1), LF (1.5, 1) and MBF (1, 1000);
	// against LF stock the PCS row is (1, 8), BF (1, 1.5) through the cross
	// section and MBF (1, 1500); against MBF stock the PCS row is
	// (187.5, 1), LF (1500, 1) and BF (1000, 1).
	for _, tc := range []struct {
		stock, pcsUnit, pcsStock string
		want                     map[string][2]string
	}{
		{"BF", "0.1875", "1", map[string][2]string{
			"BF": {"1", "1"}, "PCS": {"0.1875", "1"}, "LF": {"1.5", "1"}, "MBF": {"1", "1000"}}},
		{"LF", "1", "8", map[string][2]string{
			"LF": {"1", "1"}, "PCS": {"1", "8"}, "BF": {"1", "1.5"}, "MBF": {"1", "1500"}}},
		{"MBF", "187.5", "1", map[string][2]string{
			"MBF": {"1", "1"}, "PCS": {"187.5", "1"}, "LF": {"1500", "1"}, "BF": {"1000", "1"}}},
	} {
		id, rev := f.createBoard(map[string]any{
			"stock_uom": tc.stock, "board_thickness_in": "2", "board_width_in": "4", "board_length_ft": "8",
		})
		units := []any{unitRow(tc.stock, true, true, true)}
		for _, uom := range []string{"PCS", "LF", "BF", "MBF"} {
			if uom == tc.stock {
				continue
			}
			if uom == "PCS" {
				units = append(units, unitRow("PCS", true, false, false, tc.pcsUnit, tc.pcsStock))
				continue
			}
			units = append(units, unitRow(uom, uom == "LF", uom == "MBF", uom != "LF"))
		}
		res := f.putUnits(id, unitsBody(rev, tc.stock, tc.stock, tc.stock, tc.stock, units))
		if res.status != http.StatusOK {
			t.Fatalf("the 2x4x8 stocked in %s = %d: %s", tc.stock, res.status, res.raw)
		}
		for uom, want := range tc.want {
			row := rowOf(t, res.body, uom)
			if row["unit_qty"] != want[0] || row["stock_qty"] != want[1] {
				t.Errorf("stocked in %s: %s stored (%v, %v); want (%s, %s)", tc.stock, uom,
					row["unit_qty"], row["stock_qty"], want[0], want[1])
			}
		}
	}

	// A client pair is stored canonically: 200 BOX = 2 PCS reads (100, 1),
	// and a free pair (no rule applies) is accepted as sent, canonically.
	boxed, brev := f.createBoard(nil)
	res = f.putUnits(boxed, unitsBody(brev, "PCS", "PCS", "PCS", "PCS", []any{
		unitRow("PCS", true, true, true),
		unitRow("BOX", true, true, false, "200", "2"),
	}))
	if res.status != http.StatusOK {
		t.Fatalf("the boxed PUT = %d: %s", res.status, res.raw)
	}
	row := rowOf(t, res.body, "BOX")
	if row["unit_qty"] != "100" || row["stock_qty"] != "1" {
		t.Errorf("a sent pair is stored canonically, got (%v, %v); want (100, 1)", row["unit_qty"], row["stock_qty"])
	}

	// The GET serves the same set with the product's revision.
	got := f.do("GET", "/api/v1/products/"+fixed+"/units", nil)
	if got.status != http.StatusOK {
		t.Fatalf("GET = %d: %s", got.status, got.raw)
	}
	if lrow := rowOf(t, got.body, "LF"); lrow["unit_qty"] != "8" {
		t.Errorf("the GET serves the stored set, LF = %v", lrow)
	}
	if got.body["stock_uom"] != "PCS" || got.body["random_length"] != false {
		t.Errorf("the GET carries the facts, got %s", got.raw)
	}

	// The product read carries the set beside the new columns.
	detail := f.do("GET", "/api/v1/products/"+fixed, nil)
	if detail.status != http.StatusOK {
		t.Fatalf("product read = %d: %s", detail.status, detail.raw)
	}
	if _, has := detail.body["units"]; !has {
		t.Fatal("the product read carries units")
	}
	if detail.body["sale_uom"] != "PCS" || detail.body["board_length_ft"] != "8" ||
		detail.body["board_thickness_in"] != "2" || detail.body["random_length"] != false {
		t.Errorf("the product read carries the new columns, got %s", detail.raw)
	}
}

// TestUnitSetAgreementRefusals proves a sent pair that disagrees with a
// derivation rule is a 400 naming units[k].unit_qty with the derived pair
// in the message, and an ordered pair that cannot resolve within the bound
// is a 400 naming the row.
func TestUnitSetAgreementRefusals(t *testing.T) {
	f := newSetFixture(t)

	// BF (1, 1.5) beside MBF (1, 1400): the standard 1000 to 1 disagrees.
	random, rev := f.createBoard(map[string]any{
		"stock_uom": "LF", "board_thickness_in": "2", "board_width_in": "4", "random_length": true,
	})
	res := f.putUnits(random, unitsBody(rev, "LF", "LF", "LF", "LF", []any{
		unitRow("LF", true, true, true),
		unitRow("BF", false, false, true, "1", "1.5"),
		unitRow("MBF", false, true, true, "1", "1400"),
	}))
	if res.status != http.StatusBadRequest {
		t.Fatalf("a disagreeing MBF pair is a 400, got %d: %s", res.status, res.raw)
	}
	details := res.body["error"].(map[string]any)["details"].([]any)
	d := details[0].(map[string]any)
	if d["field"] != "units[2].unit_qty" || !strings.Contains(d["message"].(string), "(1, 1500)") {
		t.Errorf("the refusal names units[2].unit_qty and the derived pair, got %s: %s", res.raw, d)
	}

	// An MBF pair off the cross section on the fixed length product.
	fixed, frev := f.createBoard(map[string]any{
		"board_thickness_in": "2", "board_width_in": "4", "board_length_ft": "8",
	})
	res = f.putUnits(fixed, unitsBody(frev, "PCS", "PCS", "PCS", "PCS", []any{
		unitRow("PCS", true, true, true),
		unitRow("MBF", false, true, true, "1", "1000"),
	}))
	if res.status != http.StatusBadRequest {
		t.Fatalf("an MBF pair off the cross section is a 400, got %d: %s", res.status, res.raw)
	}
	if !strings.Contains(string(res.raw), "units[1].unit_qty") || !strings.Contains(string(res.raw), "(1, 187.5)") {
		t.Errorf("the refusal names the row and the derived pair, got %s", res.raw)
	}

	// Two rows whose own sides fit the columns but whose conversion does
	// not resolve within the pair's bound are refused naming the row: a
	// line between them could never carry its pair.
	big, brev := f.createBoard(nil)
	res = f.putUnits(big, unitsBody(brev, "PCS", "PCS", "PCS", "PCS", []any{
		unitRow("PCS", true, true, true),
		unitRow("BOX", true, false, false, "99999999.9999", "0.9999"),
		unitRow("CTN", true, false, false, "0.9999", "99999999.9999"),
	}))
	if res.status != http.StatusBadRequest || !strings.Contains(string(res.raw), "units[") ||
		!strings.Contains(string(res.raw), "does not fit the pair's bound") {
		t.Errorf("rows whose conversion does not fit the bound are a 400 naming the row, got %d: %s", res.status, res.raw)
	}
}

// TestUnitSetWarning proves the paver's warnings entry: a sell row whose
// one unit does not convert exactly into the stocking unit is stored and
// named in warnings, with the finer stocking unit that would make every
// sell row exact. The warning does not refuse.
func TestUnitSetWarning(t *testing.T) {
	f := newSetFixture(t)
	paver, rev := f.createBoard(map[string]any{"description": "6x9 paver", "stock_uom": "PCS"})
	res := f.putUnits(paver, unitsBody(rev, "PCS", "SF", "PCS", "PCS", []any{
		unitRow("PCS", true, true, true),
		unitRow("SF", true, false, false, "3", "8"),
	}))
	if res.status != http.StatusOK {
		t.Fatalf("the paver PUT = %d: %s", res.status, res.raw)
	}
	warnings, ok := res.body["warnings"].([]any)
	if !ok || len(warnings) != 1 {
		t.Fatalf("warnings is always an array; the paver's set carries one entry, got %s", res.raw)
	}
	w := warnings[0].(map[string]any)
	if w["field"] != "units[1]" || !strings.Contains(w["message"].(string), "SF") ||
		!strings.Contains(w["message"].(string), "0.375") {
		t.Errorf("the warning names units[1] and the finer stocking unit, got %v", w)
	}
	// A set with nothing to say answers an empty array.
	plain, prev := f.createBoard(nil)
	res = f.putUnits(plain, unitsBody(prev, "PCS", "PCS", "PCS", "PCS", []any{
		unitRow("PCS", true, true, true),
	}))
	if res.status != http.StatusOK || len(res.body["warnings"].([]any)) != 0 {
		t.Errorf("warnings is empty when there is nothing to say, got %s", res.raw)
	}
}

// TestUnitSetPriceUnitHold proves A2's CHECK through the PUT: a price unit
// other than the stocking unit is a 400 naming price_uom until C3-2B.
func TestUnitSetPriceUnitHold(t *testing.T) {
	f := newSetFixture(t)
	id, rev := f.createBoard(map[string]any{
		"board_thickness_in": "2", "board_width_in": "4", "board_length_ft": "8",
	})
	res := f.putUnits(id, unitsBody(rev, "PCS", "PCS", "MBF", "MBF", []any{
		unitRow("PCS", true, true, true),
		unitRow("MBF", false, true, true, "1", "187.5"),
	}))
	if res.status != http.StatusBadRequest || !strings.Contains(string(res.raw), "price_uom") {
		t.Errorf("a price unit other than the stocking unit is a 400 naming price_uom, got %d: %s", res.status, res.raw)
	}
	if !strings.Contains(string(res.raw), "C3-2B") {
		t.Errorf("the message names what arrives with C3-2B, got %s", res.raw)
	}
}

// TestUnitSetStockingUnitRefusals proves the three stocking unit refusals
// of section 3.2: stock_unit_in_use under stock or an open order line,
// price_unit_held under a nonzero base or a contract, both 409 conflict
// with the blocker; and the invariant refusals (a random length product is
// stocked in LF; the stocking row carries price).
func TestUnitSetStockingUnitRefusals(t *testing.T) {
	f := newSetFixture(t)
	ctx := context.Background()

	// stock_unit_in_use: an inventory row holds a nonzero quantity.
	held, rev := f.createBoard(map[string]any{"description": "held by stock", "stock_uom": "PCS"})
	var locID string
	if err := f.db.Pool.QueryRow(ctx,
		`SELECT id FROM locations WHERE type = 'BRANCH' LIMIT 1`).Scan(&locID); err != nil {
		t.Fatal(err)
	}
	if _, err := f.db.Pool.Exec(ctx,
		`INSERT INTO inventory (product_id, location, location_id, quantity) VALUES ($1, 'YARD', $2, 10)`,
		held, locID); err != nil {
		t.Fatal(err)
	}
	defer func() {
		_, _ = f.db.Pool.Exec(context.Background(), `DELETE FROM inventory WHERE product_id = $1`, held)
	}()
	res := f.putUnits(held, unitsBody(rev, "EA", "EA", "EA", "EA", []any{unitRow("EA", true, true, true)}))
	if res.status != http.StatusConflict {
		t.Fatalf("a stocking unit change under stock is a 409, got %d: %s", res.status, res.raw)
	}
	details := res.body["error"].(map[string]any)["details"].([]any)
	if details[0].(map[string]any)["code"] != "stock_unit_in_use" {
		t.Errorf("the blocker is stock_unit_in_use, got %s", res.raw)
	}

	// price_unit_held: a nonzero base price. Zero the stock first.
	priced, prev := f.createBoard(map[string]any{
		"description": "priced product", "stock_uom": "PCS", "base_price_ten_thousandths": 5250000,
	})
	res = f.putUnits(priced, unitsBody(prev, "EA", "EA", "EA", "EA", []any{unitRow("EA", true, true, true)}))
	if res.status != http.StatusConflict {
		t.Fatalf("a stocking unit change under a base price is a 409, got %d: %s", res.status, res.raw)
	}
	if res.body["error"].(map[string]any)["details"].([]any)[0].(map[string]any)["code"] != "price_unit_held" {
		t.Errorf("the blocker is price_unit_held, got %s", res.raw)
	}

	// price_unit_held: a contract names the product (no base price).
	contracted, crev := f.createBoard(map[string]any{"description": "contracted product", "stock_uom": "PCS"})
	var customerID uuid.UUID
	if err := f.db.Pool.QueryRow(ctx, `INSERT INTO customers (id, name, account_number, primary_branch_id)
		VALUES ($1, 'Unit Set Test Co', $2,
			(SELECT value::uuid FROM system_settings WHERE key = 'default_branch_id'))
		RETURNING id`, uuid.New(), "UST-"+uuid.NewString()[:8]).Scan(&customerID); err != nil {
		t.Fatal(err)
	}
	if _, err := f.db.Pool.Exec(ctx,
		`INSERT INTO customer_contracts (customer_id, product_id, contract_price)
		 VALUES ($1, $2, 5.00)`, customerID, contracted); err != nil {
		t.Fatal(err)
	}
	defer func() {
		_, _ = f.db.Pool.Exec(context.Background(), `DELETE FROM customer_contracts WHERE product_id = $1`, contracted)
	}()
	res = f.putUnits(contracted, unitsBody(crev, "EA", "EA", "EA", "EA", []any{unitRow("EA", true, true, true)}))
	if res.status != http.StatusConflict ||
		res.body["error"].(map[string]any)["details"].([]any)[0].(map[string]any)["code"] != "price_unit_held" {
		t.Errorf("a stocking unit change under a contract is a 409 price_unit_held, got %d: %s", res.status, res.raw)
	}

	// A product with no price and no stock changes its stocking unit freely.
	free, frev := f.createBoard(map[string]any{"description": "free product", "stock_uom": "PCS"})
	res = f.putUnits(free, unitsBody(frev, "EA", "EA", "EA", "EA", []any{unitRow("EA", true, true, true)}))
	if res.status != http.StatusOK {
		t.Errorf("a product with no price changes its stocking unit freely, got %d: %s", res.status, res.raw)
	}
	// The old stocking unit's row is gone: the set is exactly what was sent.
	if got := f.do("GET", "/api/v1/products/"+free+"/units", nil); got.status == http.StatusOK {
		for _, it := range got.body["units"].([]any) {
			if it.(map[string]any)["uom"] == "PCS" {
				t.Errorf("the replaced set no longer holds the old stocking row, got %s", got.raw)
			}
		}
	}

	// A random length product is stocked in LF: the PUT changing it to PCS
	// is a 400 naming stock_uom (the deferred trigger holds the raw write).
	random, rrev := f.createBoard(map[string]any{
		"stock_uom": "LF", "board_thickness_in": "2", "board_width_in": "4", "random_length": true,
	})
	res = f.putUnits(random, unitsBody(rrev, "PCS", "PCS", "PCS", "PCS", []any{
		unitRow("PCS", true, true, true), unitRow("LF", true, true, true, "8", "1"),
	}))
	if res.status != http.StatusBadRequest || !strings.Contains(string(res.raw), "stock_uom") {
		t.Errorf("a random length product stocked in PCS is refused naming stock_uom, got %d: %s", res.status, res.raw)
	}

	// The stocking row must carry price: a PUT clearing it on the stocking
	// row is a 400 naming the row.
	plain, pprev := f.createBoard(nil)
	res = f.putUnits(plain, unitsBody(pprev, "PCS", "PCS", "PCS", "PCS", []any{
		unitRow("PCS", true, true, false),
	}))
	if res.status != http.StatusBadRequest || !strings.Contains(string(res.raw), "price") {
		t.Errorf("a stocking row without price is refused naming it, got %d: %s", res.status, res.raw)
	}

	// The revision preconditions: 428 without, 409 stale.
	res = f.putUnits(plain, map[string]any{
		"stock_uom": "PCS", "sale_uom": "PCS", "price_uom": "PCS", "purchase_uom": "PCS",
		"units": []any{unitRow("PCS", true, true, true)},
	})
	if res.status != http.StatusPreconditionRequired {
		t.Errorf("a PUT without a revision is a 428, got %d", res.status)
	}
	res = f.putUnits(plain, unitsBody(99, "PCS", "PCS", "PCS", "PCS", []any{unitRow("PCS", true, true, true)}))
	if res.status != http.StatusConflict {
		t.Errorf("a stale revision is a 409, got %d", res.status)
	}
}

// TestUnitSetStockingUnitChangeKeepsTheNamedDefaults proves the PUT names
// all four unit columns itself: a stocking unit change that keeps selling in
// the old stocking unit (stock PCS to LF with sale_uom PCS) stores the sale
// and purchase defaults the body sent, never the dragged new stocking unit,
// and the GET right after agrees with the PUT's answer. The defaults trigger
// of migration 099 serves raw writers only; the write marks itself with the
// transaction local gable.unit_set_write flag.
func TestUnitSetStockingUnitChangeKeepsTheNamedDefaults(t *testing.T) {
	f := newSetFixture(t)
	id, rev := f.createBoard(map[string]any{
		"board_thickness_in": "2", "board_width_in": "4", "board_length_ft": "8",
	})
	res := f.putUnits(id, unitsBody(rev, "LF", "PCS", "LF", "LF", []any{
		unitRow("LF", true, true, true),
		unitRow("PCS", true, false, false, "1", "8"),
	}))
	if res.status != http.StatusOK {
		t.Fatalf("the stocking unit change with a kept sale default = %d: %s", res.status, res.raw)
	}
	for field, want := range map[string]string{
		"stock_uom": "LF", "sale_uom": "PCS", "price_uom": "LF", "purchase_uom": "LF",
	} {
		if res.body[field] != want {
			t.Errorf("the PUT answers %s = %v; want %s", field, res.body[field], want)
		}
	}
	got := f.do("GET", "/api/v1/products/"+id+"/units", nil)
	if got.status != http.StatusOK {
		t.Fatalf("GET = %d: %s", got.status, got.raw)
	}
	for field, want := range map[string]string{
		"stock_uom": "LF", "sale_uom": "PCS", "price_uom": "LF", "purchase_uom": "LF",
	} {
		if got.body[field] != want {
			t.Errorf("the stored %s is %v; the PUT answered %s (the GET must agree with the PUT)", field, got.body[field], want)
		}
	}
	// The columns agree with the product read too: a quote line that omits
	// its uom defaults to the stored sale_uom.
	detail := f.do("GET", "/api/v1/products/"+id, nil)
	if detail.status != http.StatusOK || detail.body["sale_uom"] != "PCS" {
		t.Errorf("the product read carries the stored sale default, got %d %s", detail.status, detail.raw)
	}
}

// TestUnitSetPutAnswerCarriesTheBoardMeasureFacts proves the PUT's answer is
// the same document the GET serves: the board measure facts (thickness,
// width, length, random_length) read from the product row the write locked,
// not the zero values the answer literal left them at.
func TestUnitSetPutAnswerCarriesTheBoardMeasureFacts(t *testing.T) {
	f := newSetFixture(t)
	fixed, rev := f.createBoard(map[string]any{
		"board_thickness_in": "2", "board_width_in": "4", "board_length_ft": "8",
	})
	res := f.putUnits(fixed, unitsBody(rev, "PCS", "PCS", "PCS", "PCS", []any{
		unitRow("PCS", true, true, true),
		unitRow("LF", true, false, false),
	}))
	if res.status != http.StatusOK {
		t.Fatalf("the 2x4x8 PUT = %d: %s", res.status, res.raw)
	}
	if res.body["board_thickness_in"] != "2" || res.body["board_width_in"] != "4" ||
		res.body["board_length_ft"] != "8" || res.body["random_length"] != false {
		t.Errorf("the PUT answer carries the board measure facts, got %s", res.raw)
	}
	// A random length product's answer carries its facts too.
	random, rrev := f.createBoard(map[string]any{
		"stock_uom": "LF", "board_thickness_in": "2", "board_width_in": "4", "random_length": true,
	})
	res = f.putUnits(random, unitsBody(rrev, "LF", "LF", "LF", "LF", []any{
		unitRow("LF", true, true, true),
	}))
	if res.status != http.StatusOK {
		t.Fatalf("the random length PUT = %d: %s", res.status, res.raw)
	}
	if res.body["board_thickness_in"] != "2" || res.body["board_width_in"] != "4" ||
		res.body["board_length_ft"] != nil || res.body["random_length"] != true {
		t.Errorf("the random length PUT answer carries its facts, got %s", res.raw)
	}
}

// TestUnitSetStockingUnitComesFromStockUOM proves the stocking unit the PUT
// stores is the request's stock_uom, never the first (1, 1) row of the set:
// a set carrying two (1, 1) rows (EA and PCS, the natural fastener set)
// stores stock_uom's unit whatever the row order, the answer agrees with
// what is stored, and the stock_unit_in_use and price_unit_held holds fire
// as 409s, never a 500 from a deferred trigger.
func TestUnitSetStockingUnitComesFromStockUOM(t *testing.T) {
	f := newSetFixture(t)
	ctx := context.Background()
	var locID string
	if err := f.db.Pool.QueryRow(ctx,
		`SELECT id FROM locations WHERE type = 'BRANCH' LIMIT 1`).Scan(&locID); err != nil {
		t.Fatal(err)
	}

	// A PCS product holding stock: a PUT whose stock_uom stays PCS with the
	// EA row first (both rows are (1, 1)) keeps uom_primary PCS and the
	// answer says so.
	held, rev := f.createBoard(map[string]any{"description": "held by stock", "stock_uom": "PCS"})
	if _, err := f.db.Pool.Exec(ctx,
		`INSERT INTO inventory (product_id, location, location_id, quantity) VALUES ($1, 'YARD', $2, 10)`,
		held, locID); err != nil {
		t.Fatal(err)
	}
	defer func() {
		_, _ = f.db.Pool.Exec(context.Background(), `DELETE FROM inventory WHERE product_id = $1`, held)
	}()
	res := f.putUnits(held, unitsBody(rev, "PCS", "PCS", "PCS", "PCS", []any{
		unitRow("EA", true, true, true),
		unitRow("PCS", true, true, true),
	}))
	if res.status != http.StatusOK {
		t.Fatalf("a PUT whose stock_uom stays PCS under stock = %d: %s", res.status, res.raw)
	}
	if res.body["stock_uom"] != "PCS" {
		t.Errorf("the answer's stock_uom is PCS, got %v", res.body["stock_uom"])
	}
	heldRev, _ := res.body["revision"].(json.Number).Int64()
	if got := f.do("GET", "/api/v1/products/"+held, nil); got.status != http.StatusOK || got.body["stock_uom"] != "PCS" {
		t.Errorf("the stored stocking unit is PCS whatever the row order, got %d %s", got.status, got.raw)
	}
	if got := f.do("GET", "/api/v1/products/"+held+"/units", nil); got.status != http.StatusOK || got.body["stock_uom"] != "PCS" {
		t.Errorf("the stored set's stocking unit is PCS, got %d %s", got.status, got.raw)
	}

	// The same PUT on a priced product is served, not a 500 from a deferred
	// trigger: the stocking unit does not change, so no hold fires.
	priced, prev := f.createBoard(map[string]any{
		"description": "priced product", "stock_uom": "PCS", "base_price_ten_thousandths": 5250000,
	})
	res = f.putUnits(priced, unitsBody(prev, "PCS", "PCS", "PCS", "PCS", []any{
		unitRow("EA", true, true, true),
		unitRow("PCS", true, true, true),
	}))
	if res.status != http.StatusOK || res.body["stock_uom"] != "PCS" {
		t.Fatalf("a PUT whose stock_uom stays PCS on a priced product = %d: %s", res.status, res.raw)
	}
	pricedRev, _ := res.body["revision"].(json.Number).Int64()

	// A stocking unit change under stock is the 409 stock_unit_in_use,
	// whatever the row order.
	res = f.putUnits(held, unitsBody(heldRev, "EA", "EA", "EA", "EA", []any{
		unitRow("PCS", true, true, true),
		unitRow("EA", true, true, true),
	}))
	if res.status != http.StatusConflict {
		t.Fatalf("a stocking unit change under stock is a 409, got %d: %s", res.status, res.raw)
	}
	if res.body["error"].(map[string]any)["details"].([]any)[0].(map[string]any)["code"] != "stock_unit_in_use" {
		t.Errorf("the blocker is stock_unit_in_use, got %s", res.raw)
	}

	// A stocking unit change on a priced product is the 409 price_unit_held,
	// never a 500.
	res = f.putUnits(priced, unitsBody(pricedRev, "EA", "EA", "EA", "EA", []any{
		unitRow("EA", true, true, true),
	}))
	if res.status != http.StatusConflict {
		t.Fatalf("a stocking unit change under a base price is a 409, got %d: %s", res.status, res.raw)
	}
	if res.body["error"].(map[string]any)["details"].([]any)[0].(map[string]any)["code"] != "price_unit_held" {
		t.Errorf("the blocker is price_unit_held, got %s", res.raw)
	}

	// A stock_uom with no row of the set, or whose row is sent as another
	// pair than (1, 1), is a 400 naming stock_uom: the stocking unit comes
	// from stock_uom, never from row order.
	res = f.putUnits(priced, unitsBody(pricedRev, "EA", "EA", "EA", "EA", []any{
		unitRow("PCS", true, true, true),
	}))
	if res.status != http.StatusBadRequest || !strings.Contains(string(res.raw), `"stock_uom"`) {
		t.Errorf("a stock_uom with no row is a 400 naming stock_uom, got %d: %s", res.status, res.raw)
	}
	res = f.putUnits(priced, unitsBody(pricedRev, "EA", "EA", "EA", "EA", []any{
		unitRow("EA", true, true, true, "1", "2"),
	}))
	if res.status != http.StatusBadRequest || !strings.Contains(string(res.raw), `"stock_uom"`) {
		t.Errorf("a stock_uom row sent as another pair is a 400 naming stock_uom, got %d: %s", res.status, res.raw)
	}
}

// TestUnitSetStockHoldLocksTheRowsItReads proves the stock unit hold's
// check takes the row locks it reads, inside the caller's transaction: a
// receive on the product's bin, run while the unit set write's transaction
// sits between the check and its commit, waits for that transaction; once
// it commits, the same receive is served. A row that first appears after
// the check (a new bin, a new order line) is cycle 4's stock identity work
// and is not what this test pins.
func TestUnitSetStockHoldLocksTheRowsItReads(t *testing.T) {
	f := newSetFixture(t)
	ctx := context.Background()
	held, _ := f.createBoard(map[string]any{"description": "hold locks", "stock_uom": "PCS"})
	uid := uuid.MustParse(held)
	var locID string
	if err := f.db.Pool.QueryRow(ctx,
		`SELECT id FROM locations WHERE type = 'BRANCH' LIMIT 1`).Scan(&locID); err != nil {
		t.Fatal(err)
	}
	if _, err := f.db.Pool.Exec(ctx,
		`INSERT INTO inventory (product_id, location, location_id, quantity) VALUES ($1, 'YARD', $2, 10)`,
		held, locID); err != nil {
		t.Fatal(err)
	}
	defer func() {
		_, _ = f.db.Pool.Exec(context.Background(), `DELETE FROM inventory WHERE product_id = $1`, held)
	}()

	repo := product.NewRepository(f.db)
	checked := make(chan error, 1)
	release := make(chan struct{})
	done := make(chan error, 1)
	go func() {
		done <- f.db.RunInTx(ctx, func(txCtx context.Context) error {
			inUse, err := repo.ProductStockUnitInUse(txCtx, uid)
			if err != nil {
				checked <- err
				return err
			}
			if !inUse {
				checked <- errors.New("the hold check did not see the product's stock")
				return nil
			}
			checked <- nil
			<-release
			return nil
		})
	}()
	if err := <-checked; err != nil {
		t.Fatal(err)
	}
	// Release the gated transaction on every path, so a failure below never
	// leaves it holding a pool connection through the fixture's cleanup.
	var releaseOnce sync.Once
	releaseNow := func() { releaseOnce.Do(func() { close(release) }) }
	defer releaseNow()

	// The receive waits: the row lock is held to the transaction's end.
	updCtx, cancel := context.WithTimeout(ctx, 750*time.Millisecond)
	defer cancel()
	if _, err := f.db.Pool.Exec(updCtx,
		`UPDATE inventory SET quantity = 12 WHERE product_id = $1`, held); err == nil {
		t.Fatal("a receive on the product's bin waits for the unit set write's transaction")
	}
	releaseNow()
	if err := <-done; err != nil {
		t.Fatalf("the gated transaction: %v", err)
	}

	// The same receive is served once the transaction has committed.
	if _, err := f.db.Pool.Exec(ctx,
		`UPDATE inventory SET quantity = 12 WHERE product_id = $1`, held); err != nil {
		t.Fatalf("the receive is served after the commit: %v", err)
	}
}

// TestUnitSetFieldRefusals proves the parse rules: an inactive unit refused
// on a new set row, a default that is not a row of the set with its flag
// refused naming it, an unknown unit refused, and a body field the PUT does
// not apply (board_thickness_in) refused naming it.
func TestUnitSetFieldRefusals(t *testing.T) {
	f := newSetFixture(t)
	id, rev := f.createBoard(nil)

	// An inactive unit cannot enter a new set row.
	if _, err := f.db.Pool.Exec(context.Background(),
		`UPDATE units SET is_active = FALSE WHERE code = 'CY'`); err != nil {
		t.Fatal(err)
	}
	defer func() {
		_, _ = f.db.Pool.Exec(context.Background(), `UPDATE units SET is_active = TRUE, revision = 1 WHERE code = 'CY'`)
	}()
	res := f.putUnits(id, unitsBody(rev, "PCS", "PCS", "PCS", "PCS", []any{
		unitRow("PCS", true, true, true),
		unitRow("CY", true, false, false, "1", "27"),
	}))
	if res.status != http.StatusBadRequest || !strings.Contains(string(res.raw), "units[1].uom") {
		t.Errorf("an inactive unit is refused on a new set row naming it, got %d: %s", res.status, res.raw)
	}

	// A sale default that is not a sell row of the set.
	res = f.putUnits(id, unitsBody(rev, "PCS", "BOX", "PCS", "PCS", []any{
		unitRow("PCS", true, true, true),
		unitRow("BOX", false, false, true, "100", "1"),
	}))
	if res.status != http.StatusBadRequest || !strings.Contains(string(res.raw), "sale_uom") {
		t.Errorf("a default that is not a row of the set with its flag is a 400 naming it, got %d: %s", res.status, res.raw)
	}

	// A unit outside the catalogue.
	res = f.putUnits(id, unitsBody(rev, "PCS", "PCS", "PCS", "PCS", []any{
		unitRow("PCS", true, true, true),
		unitRow("NOPE", true, false, false, "1", "1"),
	}))
	if res.status != http.StatusBadRequest || !strings.Contains(string(res.raw), "units[1].uom") {
		t.Errorf("a unit outside the catalogue is a 400 naming the row, got %d: %s", res.status, res.raw)
	}

	// A field the PUT cannot change.
	res = f.putUnits(id, map[string]any{
		"revision": rev, "stock_uom": "PCS", "sale_uom": "PCS", "price_uom": "PCS",
		"purchase_uom": "PCS", "board_thickness_in": "2",
		"units": []any{unitRow("PCS", true, true, true)},
	})
	if res.status != http.StatusBadRequest || !strings.Contains(string(res.raw), "board_thickness_in") {
		t.Errorf("a field the PUT cannot change is a 400 naming it, got %d: %s", res.status, res.raw)
	}
}

// TestUnitSetAuditRevisionAndConcurrency proves the audit row, the revision
// proof and the concurrency proofs of the recipe for the unit set write:
// three contenders on one revision leave exactly one winner, and a failing
// event write rolls the whole replace back.
func TestUnitSetAuditRevisionAndConcurrency(t *testing.T) {
	f := newSetFixture(t)
	ctx := context.Background()
	id, rev := f.createBoard(nil)

	res := f.putUnits(id, unitsBody(rev, "PCS", "PCS", "PCS", "PCS", []any{
		unitRow("PCS", true, true, true),
		unitRow("MBF", false, true, true, "1", "187.5"),
	}))
	if res.status != http.StatusOK {
		t.Fatalf("the first PUT = %d: %s", res.status, res.raw)
	}
	// The audit row, with before and after.
	var n int
	if err := f.db.Pool.QueryRow(ctx,
		`SELECT count(*) FROM audit_log WHERE entity_type = 'product' AND entity_id = $1 AND action = 'product.updated'`,
		id).Scan(&n); err != nil || n < 1 {
		t.Fatalf("the unit set write carries its audit row, got %d (%v)", n, err)
	}

	// Three contenders on one revision: exactly one winner.
	var wg sync.WaitGroup
	winners := make(chan int, 3)
	for i := 0; i < 3; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			r := f.putUnits(id, unitsBody(rev+1, "PCS", "PCS", "PCS", "PCS", []any{
				unitRow("PCS", true, true, true),
			}))
			if r.status == http.StatusOK {
				winners <- i
			}
		}(i)
	}
	wg.Wait()
	close(winners)
	if len(winners) != 1 {
		t.Fatalf("exactly one of three contenders on one revision wins, got %d", len(winners))
	}

	// A failing event write rolls the whole replace back: the write that
	// re-adds the MBF row reaches its event, the event fails, and the set
	// keeps the winner's rows exactly.
	svc := product.NewService(product.NewRepository(f.db)).
		WithOutbox(failingEvents{}).WithTxRunner(f.db).WithAudit(kitAuditLogger(t, f.db))
	uid, err := uuid.Parse(id)
	if err != nil {
		t.Fatal(err)
	}
	req := &product.PutUnitSetRequest{
		StockUOM: "PCS", SaleUOM: "PCS", PriceUOM: "PCS", PurchaseUOM: "PCS",
		Units: []product.UnitRowRequest{
			{UOM: "PCS", Sell: true, Purchase: true, Price: true},
			{UOM: "MBF", UnitQty: "1", StockQty: "187.5", Purchase: true, Price: true},
		},
	}
	revNow := int64(3)
	if _, err := svc.ReplaceUnitSet(ctx, uid, req, product.RevisionPrecondition{Revision: &revNow}); err == nil {
		t.Fatal("the replace fails when its event write fails")
	}
	var mbf int
	if err := f.db.Pool.QueryRow(ctx,
		`SELECT count(*) FROM product_units WHERE product_id = $1 AND uom = 'MBF'`, id).Scan(&mbf); err != nil || mbf != 0 {
		t.Fatalf("the rolled back replace leaves no MBF row, got %d (%v)", mbf, err)
	}
	var revision int64
	if err := f.db.Pool.QueryRow(ctx, `SELECT revision FROM products WHERE id = $1`, id).Scan(&revision); err != nil || revision != 3 {
		t.Fatalf("the rolled back replace leaves the revision at 3, got %d (%v)", revision, err)
	}
}

// TestUnitSetGatedSaturation is the gated saturation proof for the unit
// set write: four writers hold their transactions at a gate until the pool
// is empty, then replace the set. A statement that reached for a second
// connection would never finish.
func TestUnitSetGatedSaturation(t *testing.T) {
	testutil.LockOutboxTables(t)
	db := testutil.RequireDBMaxConns(t, 4)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	repo := product.NewRepository(db)
	svc := product.NewService(repo).
		WithOutbox(kitOutboxWriter(t, db)).WithTxRunner(db).WithAudit(kitAuditLogger(t, db))
	draft := &product.Product{SKU: "SATSET-" + uuid.NewString()[:10], Description: "saturation", UOMPrimary: product.UOM("PCS")}
	if err := svc.CreateProduct(ctx, draft); err != nil {
		t.Fatal(err)
	}
	defer func() {
		_, _ = db.Pool.Exec(context.Background(), `DELETE FROM products WHERE id = $1`, draft.ID)
	}()

	const contenders = 4
	gate := newSetGate(db, contenders)
	var wg sync.WaitGroup
	results := make(chan error, contenders)
	for i := 0; i < contenders; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			s := product.NewService(repo).WithTxRunner(gate)
			rev := int64(1)
			_, err := s.ReplaceUnitSet(ctx, draft.ID, &product.PutUnitSetRequest{
				StockUOM: "PCS", SaleUOM: "PCS", PriceUOM: "PCS", PurchaseUOM: "PCS",
				Units: []product.UnitRowRequest{{UOM: "PCS", Sell: true, Purchase: true, Price: true},
					{UOM: "BOX", UnitQty: "100", StockQty: "1", Sell: true}},
			}, product.RevisionPrecondition{Revision: &rev})
			results <- err
		}(i)
	}
	wg.Wait()
	close(results)
	winners, losers := 0, 0
	for err := range results {
		var he *httpx.Error
		switch {
		case err == nil:
			winners++
		case errors.As(err, &he) && he.Code == httpx.CodeStaleRevision:
			losers++
		default:
			t.Errorf("a contender failed with something other than a stale revision: %v", err)
		}
	}
	if winners != 1 || losers != contenders-1 {
		t.Fatalf("exactly one of %d contenders at one revision wins and the rest answer stale, got %d winners and %d stale losers", contenders, winners, losers)
	}
	if ctx.Err() != nil {
		t.Fatalf("four contenders at pool size 4 did not finish: a transaction waited on a second pool connection")
	}
}

// TestUnitSetTwoPutsAndAReceiveAtPool4 proves the write's product row lock
// serializes a concurrent receive's new bin row (the inventory foreign key
// takes a FOR KEY SHARE the write's FOR UPDATE holds back): two unit set
// PUTs changing the stocking unit at one revision and a receive that INSERTs
// a new bin run together at pool size 4 over several rounds. Every round
// ends consistently: at most one PUT wins (the receive landing first turns
// both into stock_unit_in_use), every loser answers a business refusal (a
// stale revision, or stock_unit_in_use), and the receive always completes.
func TestUnitSetTwoPutsAndAReceiveAtPool4(t *testing.T) {
	testutil.LockOutboxTables(t)
	db := testutil.RequireDBMaxConns(t, 4)
	f := newKitFixture(t, nil, db)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	var locID string
	if err := db.Pool.QueryRow(ctx,
		`SELECT id FROM locations WHERE type = 'BRANCH' LIMIT 1`).Scan(&locID); err != nil {
		t.Fatal(err)
	}

	for round := 0; round < 5; round++ {
		id, rev := f.createBoard(map[string]any{"description": "two puts and a receive", "stock_uom": "PCS"})
		put := func(stock string) resp {
			return f.putUnits(id, unitsBody(rev, stock, stock, stock, stock, []any{
				unitRow(stock, true, true, true),
			}))
		}
		results := make(chan resp, 2)
		received := make(chan error, 1)
		go func() { results <- put("LF") }()
		go func() { results <- put("EA") }()
		go func() {
			_, err := db.Pool.Exec(ctx,
				`INSERT INTO inventory (product_id, location, location_id, quantity) VALUES ($1, 'YARD', $2, 10)`,
				id, locID)
			received <- err
		}()

		winners, stockInUse, stale := 0, 0, 0
		for i := 0; i < 2; i++ {
			r := <-results
			switch {
			case r.status == http.StatusOK:
				winners++
			case r.status == http.StatusConflict && strings.Contains(string(r.raw), "stock_unit_in_use"):
				stockInUse++
			case r.status == http.StatusConflict && strings.Contains(string(r.raw), "stale_revision"):
				stale++
			default:
				t.Fatalf("round %d: a PUT answered %d, want 200, stale or stock_unit_in_use: %s", round, r.status, r.raw)
			}
		}
		if err := <-received; err != nil {
			t.Fatalf("round %d: the receive completed, got %v", round, err)
		}
		if winners > 1 {
			t.Fatalf("round %d: at most one PUT wins, got %d", round, winners)
		}
		if winners+stockInUse+stale != 2 {
			t.Fatalf("round %d: both PUTs answered, got %d+%d+%d", round, winners, stockInUse, stale)
		}
		// The stored state agrees with the winner, and the receive's row is
		// there whatever the interleaving was.
		got := f.do("GET", "/api/v1/products/"+id, nil)
		if got.status != http.StatusOK {
			t.Fatalf("round %d: the product read = %d: %s", round, got.status, got.raw)
		}
		switch {
		case winners == 1:
			if got.body["stock_uom"] == "PCS" {
				t.Fatalf("round %d: the winning PUT's stocking unit is stored, got %v", round, got.body["stock_uom"])
			}
		case got.body["stock_uom"] != "PCS":
			t.Fatalf("round %d: no PUT won, so the stocking unit is still PCS, got %v", round, got.body["stock_uom"])
		}
		var bins int
		if err := db.Pool.QueryRow(ctx,
			`SELECT count(*) FROM inventory WHERE product_id = $1 AND quantity = 10`, id).Scan(&bins); err != nil || bins != 1 {
			t.Fatalf("round %d: the receive's row is stored, got %d (%v)", round, bins, err)
		}
	}
}

// setGate is the gated transaction runner of the recipe's saturation proof:
// the first `want` transactions meet inside their transactions before any
// of them runs a statement, so with want equal to the pool size the pool is
// empty when the gate opens.
type setGate struct {
	db      *database.DB
	want    int32
	entered int32
	gate    sync.WaitGroup
}

func newSetGate(db *database.DB, want int) *setGate {
	g := &setGate{db: db, want: int32(want)}
	g.gate.Add(want)
	return g
}

func (g *setGate) RunInTx(ctx context.Context, fn func(ctx context.Context) error) error {
	return g.db.RunInTx(ctx, func(txCtx context.Context) error {
		if atomic.AddInt32(&g.entered, 1) <= g.want {
			g.gate.Done()
			g.gate.Wait()
		}
		return fn(txCtx)
	})
}
