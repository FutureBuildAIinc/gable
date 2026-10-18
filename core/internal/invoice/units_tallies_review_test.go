// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package invoice_test

import (
	"fmt"
	"strings"
	"testing"

	"github.com/gablelbm/gable/internal/platform/httpx"
	"github.com/gablelbm/gable/internal/salesdoc"
	"github.com/gablelbm/gable/internal/testutil"
	"github.com/google/uuid"
)

// TestRandomLengthMouldingWithoutCrossSection_TallySurvivesOrderInvoiceAndCreditRead
// covers P1-1: a random length product stocked in LF with no nominal cross
// section (board_thickness_in and board_width_in are NULL) keeps its tally
// rows and linear_feet when read back from orders, invoices, and credit memos.
func TestRandomLengthMouldingWithoutCrossSection_TallySurvivesOrderInvoiceAndCreditRead(t *testing.T) {
	testutil.LockOutboxTables(t)
	db := testutil.RequireDB(t)
	f := newFixture(t, db)

	mouldingID := uuid.New()
	mouldingSKU := "MLD-" + uuid.NewString()[:8]
	mustExec(t, db, `
		INSERT INTO products (id, sku, description, uom_primary, sale_uom, price_uom, purchase_uom,
			base_price, average_unit_cost, random_length, board_thickness_in, board_width_in)
		VALUES ($1, $2, 'WM356 Casing Random Length', 'LF', 'LF', 'LF', 'LF', 1.50, 0.80, TRUE, NULL, NULL)`,
		mouldingID, mouldingSKU)
	mustExec(t, db, `INSERT INTO inventory (product_id, location_id, location, quantity, allocated) VALUES ($1, $2, 'Y', 500, 0)`,
		mouldingID, f.yardID)
	t.Cleanup(func() {
		mustExec(t, db, `DELETE FROM line_tally_rows WHERE order_line_id IN (SELECT id FROM order_lines WHERE product_id = $1)
			OR invoice_line_id IN (SELECT id FROM invoice_lines WHERE product_id = $1)
			OR credit_memo_line_id IN (SELECT id FROM credit_memo_lines WHERE product_id = $1)`, mouldingID)
		mustExec(t, db, `DELETE FROM credit_memo_lines WHERE product_id = $1`, mouldingID)
		mustExec(t, db, `DELETE FROM invoice_lines WHERE product_id = $1`, mouldingID)
		mustExec(t, db, `DELETE FROM order_lines WHERE product_id = $1`, mouldingID)
		mustExec(t, db, `DELETE FROM inventory WHERE product_id = $1`, mouldingID)
		mustExec(t, db, `DELETE FROM products WHERE id = $1`, mouldingID)
	})

	createOrder := f.do("POST", "/api/v1/orders", map[string]any{
		"customer_id":   f.customerID.String(),
		"delivery_type": "pickup",
		"lines": []any{map[string]any{
			"product_id":                 mouldingID.String(),
			"uom":                        "LF",
			"unit_price_ten_thousandths": 15000,
			"override_reason":            "contract moulding rate",
			"tally": map[string]any{
				"rows": []any{
					map[string]any{"pieces": 10, "length_ft": "8"},
					map[string]any{"pieces": 5, "length_ft": "12"},
				},
			},
		}},
	})
	if createOrder.status != 201 {
		t.Fatalf("create order = %d: %s", createOrder.status, createOrder.raw)
	}
	orderID := str(t, createOrder.body, "id")
	getOrd := f.do("GET", "/api/v1/orders/"+orderID, nil)
	if getOrd.status != 200 {
		t.Fatalf("get order = %d: %s", getOrd.status, getOrd.raw)
	}
	ordLines := getOrd.body["lines"].([]any)
	ordTally, ok := ordLines[0].(map[string]any)["tally"].(map[string]any)
	if !ok || ordTally == nil {
		t.Fatalf("order line tally is nil on LF moulding without cross section: %s", getOrd.raw)
	}
	if ordTally["linear_feet"] != "140" || ordTally["thickness_in"] != nil || ordTally["width_in"] != nil || ordTally["board_feet"] != nil {
		t.Fatalf("order line tally = %+v, want linear_feet=140 and null cross section / board_feet", ordTally)
	}
	if len(ordTally["rows"].([]any)) != 2 {
		t.Fatalf("order line tally rows = %+v, want 2 rows", ordTally["rows"])
	}

	conf := f.do("POST", "/api/v1/orders/"+orderID+"/transitions", map[string]any{
		"to": "confirmed", "revision": rev(t, getOrd),
	})
	if conf.status != 200 {
		t.Fatalf("confirm = %d: %s", conf.status, conf.raw)
	}
	invID, _ := f.fulfil(orderID, rev(t, conf), nil)
	getInv := f.getInvoice(invID)
	invLines := getInv.body["lines"].([]any)
	invLine0 := invLines[0].(map[string]any)
	invTally, ok := invLine0["tally"].(map[string]any)
	if !ok || invTally == nil {
		t.Fatalf("invoice line tally is nil on LF moulding without cross section: %s", getInv.raw)
	}
	if invTally["linear_feet"] != "140" || len(invTally["rows"].([]any)) != 2 {
		t.Fatalf("invoice line tally = %+v, want linear_feet=140 with 2 rows", invTally)
	}

	// Full credit memo against the invoice line also preserves the tally on read.
	cmResp := f.createCredit(f.creditBody(invID, returnLine(str(t, invLine0, "id"), "-140", true)))
	cmLines := cmResp.body["lines"].([]any)
	cmTally, ok := cmLines[0].(map[string]any)["tally"].(map[string]any)
	if !ok || cmTally == nil || cmTally["linear_feet"] != "140" || len(cmTally["rows"].([]any)) != 2 {
		t.Fatalf("credit memo line tally = %+v, want linear_feet=140 with 2 rows", cmLines[0])
	}
}

// TestFreeCreditMemoLine_ResolvesUnitSetStockQuantityAndCOGS covers P1-3:
// an unlinked (free) credit memo line on a product stocked in PCS and sold in
// LF (8 LF = 1 PCS) converts quantity into stock_quantity in PCS, computes
// cost_cents on stock_quantity, and restocks on-hand inventory in PCS.
func TestFreeCreditMemoLine_ResolvesUnitSetStockQuantityAndCOGS(t *testing.T) {
	testutil.LockOutboxTables(t)
	db := testutil.RequireDB(t)
	f := newFixture(t, db)

	// f.productID is stocked in PCS (100 on hand), average_unit_cost = 3.25.
	// Add LF to its unit set: 8 LF = 1 PCS, sell = true.
	mustExec(t, db, `
		INSERT INTO product_units (product_id, uom, unit_qty, stock_qty, sell, purchase, price)
		VALUES ($1, 'LF', 8, 1, TRUE, FALSE, FALSE)
		ON CONFLICT (product_id, uom) DO UPDATE SET unit_qty = 8, stock_qty = 1, sell = TRUE`, f.productID)

	cmResp := f.do("POST", "/api/v1/credit-memos", map[string]any{
		"customer_id": f.customerID.String(),
		"reason_code": "return",
		"reason":      "unlinked return in LF",
		"lines": []any{map[string]any{
			"product_id":                 f.productID.String(),
			"uom":                        "LF",
			"quantity":                   "-16",
			"unit_price_ten_thousandths": 10000,
			"restock":                    true,
		}},
	})
	if cmResp.status != 201 {
		t.Fatalf("create free credit memo = %d: %s", cmResp.status, cmResp.raw)
	}
	cmID := str(t, cmResp.body, "id")
	cmLine0 := cmResp.body["lines"].([]any)[0].(map[string]any)
	if cmLine0["stock_uom"] != "PCS" || cmLine0["stock_quantity"] != "-2" {
		t.Fatalf("free credit memo line stock = %v %v, want -2 PCS", cmLine0["stock_quantity"], cmLine0["stock_uom"])
	}
	// 2 PCS * $3.25/PCS = $6.50 (-650 cents), never 16 * $3.25 (-5200 cents).
	if num(t, cmLine0, "cost_cents") != -650 {
		t.Fatalf("free credit memo line cost_cents = %v, want -650", cmLine0["cost_cents"])
	}

	postResp := f.do("POST", "/api/v1/credit-memos/"+cmID+"/transitions", map[string]any{
		"to": "open", "revision": rev(t, cmResp), "reason": "post free return",
	}, "X-Test-Role", "finance", "X-Test-Sub", uuid.NewString())
	if postResp.status != 200 {
		t.Fatalf("post credit memo = %d: %s", postResp.status, postResp.raw)
	}
	// Inventory was 100 PCS; restocking 16 LF (2 PCS) brings it to 102 PCS, not 116 PCS.
	if got := f.stock(); got != "102.0000/0.0000" {
		t.Fatalf("stock after free credit memo restock = %s, want 102.0000/0.0000", got)
	}
}

// TestCrossUnitOrder_SummaryCostAndVoidInvoiceClosedShortCheck covers P1-4 and P1-5:
//   - P1-5: order summary cost_sum multiplies stock_quantity (10 PCS) by
//     average_unit_cost ($3.25/PCS) = $32.50, not quantity (80 LF) * $3.25 = $260.00;
//   - P1-4: voiding the invoice of a fully fulfilled cross-unit order (where
//     QuantityFulfilled = 10 PCS and Quantity = 80 LF) succeeds instead of
//     falsely refusing with 409 order_closed_short.
func TestCrossUnitOrder_SummaryCostAndVoidInvoiceClosedShortCheck(t *testing.T) {
	testutil.LockOutboxTables(t)
	db := testutil.RequireDB(t)
	f := newFixture(t, db)

	mustExec(t, db, `
		INSERT INTO product_units (product_id, uom, unit_qty, stock_qty, sell, purchase, price)
		VALUES ($1, 'LF', 8, 1, TRUE, FALSE, TRUE)
		ON CONFLICT (product_id, uom) DO UPDATE SET unit_qty = 8, stock_qty = 1, sell = TRUE, price = TRUE`, f.productID)

	// f.productID has base_price = $5.50/PCS and average_unit_cost = $3.25/PCS.
	// Selling 80 LF (= 10 PCS) at $8.00/PCS override gives $80.00 revenue (8000 cents).
	createResp := f.do("POST", "/api/v1/orders", map[string]any{
		"customer_id":   f.customerID.String(),
		"delivery_type": "pickup",
		"lines": []any{map[string]any{
			"product_id":                 f.productID.String(),
			"uom":                        "LF",
			"price_uom":                  "PCS",
			"quantity":                   "80",
			"unit_price_ten_thousandths": 80000,
			"override_reason":            "contract PCS price",
		}},
	})
	if createResp.status != 201 {
		t.Fatalf("create cross-unit order = %d: %s", createResp.status, createResp.raw)
	}
	orderID := str(t, createResp.body, "id")

	// P1-5: revenue is 10 PCS * $8.00 = $80.00 (8000 cents).
	// Cost is 10 PCS * $3.25/PCS = $32.50 (3250 cents).
	// Margin is $80.00 - $32.50 = $47.50 (4750 cents).
	listResp := f.do("GET", "/api/v1/orders?customer_id="+f.customerID.String(), nil)
	if listResp.status != 200 {
		t.Fatalf("list orders = %d: %s", listResp.status, listResp.raw)
	}
	items := listResp.body["items"].([]any)
	if len(items) != 1 {
		t.Fatalf("items len = %d, want 1", len(items))
	}
	item0 := items[0].(map[string]any)
	if gotCost, gotMargin := num(t, item0, "total_cost_cents"), num(t, item0, "total_margin_cents"); gotCost != 3250 || gotMargin != 4750 {
		t.Fatalf("order summary total_cost_cents=%d total_margin_cents=%d, want 3250 and 4750", gotCost, gotMargin)
	}

	// Confirm and fulfil the full 80 LF (10 PCS).
	conf := f.do("POST", "/api/v1/orders/"+orderID+"/transitions", map[string]any{
		"to": "confirmed", "revision": rev(t, createResp),
	})
	if conf.status != 200 {
		t.Fatalf("confirm = %d: %s", conf.status, conf.raw)
	}
	invID, _ := f.fulfil(orderID, rev(t, conf), nil)

	// P1-4: void the invoice. Even though QuantityFulfilled (10 PCS) < Quantity (80 LF),
	// QuantityFulfilled == StockQuantity (10 PCS), so the order was NOT closed short.
	invDoc := f.getInvoice(invID)
	voidResp := f.voidInvoice(invID, rev(t, invDoc), "void cross-unit invoice")
	if voidResp.status != 200 {
		t.Fatalf("void cross-unit invoice = %d, want 200: %s", voidResp.status, voidResp.raw)
	}
}

// TestStockUnitChanged_Returns409BlockerAndConvertsWhenExact covers P2-1
// (ADR 0006 section 3.4):
//   - ResolveStockMove converts StockQty through the current unit set when the
//     old stocking unit is still in the set and exact at scale 4, and returns
//     409 stock_unit_changed naming lines[i] otherwise;
//   - Fulfilling an order or restocking a credit memo after the product's
//     stocking unit changed to an incompatible unit refuses with 409 conflict
//     and blocker stock_unit_changed naming lines[0].
func TestStockUnitChanged_Returns409BlockerAndConvertsWhenExact(t *testing.T) {
	t.Run("ResolveStockMove converts when exact and refuses with stock_unit_changed otherwise", func(t *testing.T) {
		pcs := "PCS"
		qty10 := httpx.Quantity(100_000) // 10 PCS
		line := salesdoc.Line{
			Quantity:      &qty10,
			StockUOM:      &pcs,
			StockQuantity: &qty10,
		}
		// Product now stocked in LF with 1 PCS = 8 LF in its current set.
		set := &salesdoc.ProductUnitSet{
			StockUOM: "LF",
			Rows: []salesdoc.UnitSetRow{
				{UOM: "LF", UnitQty: salesdoc.One, StockQty: salesdoc.One, Sell: true, Price: true},
				{UOM: "PCS", UnitQty: salesdoc.One, StockQty: httpx.Quantity(80_000), Sell: true, Price: true},
			},
		}
		newUOM, newQty, err := salesdoc.ResolveStockMove(0, &line, "LF", set)
		if err != nil {
			t.Fatalf("ResolveStockMove exact conversion failed: %v", err)
		}
		if newUOM != "LF" || newQty != httpx.Quantity(800_000) {
			t.Fatalf("ResolveStockMove = (%s, %s), want (LF, 80)", newUOM, newQty.WireString())
		}

		// Product now stocked in BOX with PCS removed from its set -> 409 stock_unit_changed.
		incompatSet := &salesdoc.ProductUnitSet{
			StockUOM: "BOX",
			Rows: []salesdoc.UnitSetRow{
				{UOM: "BOX", UnitQty: salesdoc.One, StockQty: salesdoc.One, Sell: true, Price: true},
			},
		}
		_, _, err = salesdoc.ResolveStockMove(2, &line, "BOX", incompatSet)
		if err == nil {
			t.Fatal("expected 409 stock_unit_changed error, got nil")
		}
		he, ok := err.(*httpx.Error)
		if !ok || he.Status != 409 || he.Code != httpx.CodeConflict {
			t.Fatalf("error = %#v, want 409 conflict", err)
		}
		if len(he.Details) != 1 || he.Details[0].Code != "stock_unit_changed" || he.Details[0].Field != "lines[2]" {
			t.Fatalf("details = %+v, want blocker stock_unit_changed and field lines[2]", he.Details)
		}
	})

	t.Run("fulfilment refuses with 409 stock_unit_changed when product uom_primary changed", func(t *testing.T) {
		testutil.LockOutboxTables(t)
		db := testutil.RequireDB(t)
		f := newFixture(t, db)

		ordID, ordRev := f.confirmedOrder(f.pickupLine("2"))
		// Simulate an incompatible stocking unit change behind the order line.
		mustExec(t, db, `
			INSERT INTO product_units (product_id, uom, unit_qty, stock_qty, sell, purchase, price)
			VALUES ($1, 'BOX', 1, 1, TRUE, TRUE, TRUE)
			ON CONFLICT (product_id, uom) DO NOTHING`, f.productID)
		mustExec(t, db, `SELECT set_config('gable.unit_set_write', 'on', true)`)
		mustExec(t, db, `UPDATE products SET uom_primary = 'BOX' WHERE id = $1`, f.productID)
		t.Cleanup(func() {
			mustExec(t, db, `SELECT set_config('gable.unit_set_write', 'on', true)`)
			mustExec(t, db, `UPDATE products SET uom_primary = 'PCS' WHERE id = $1`, f.productID)
		})

		ful := f.do("POST", "/api/v1/orders/"+ordID+"/fulfillments", map[string]any{
			"revision": ordRev, "picked_up_by": "Buyer",
		})
		if ful.status != 409 {
			t.Fatalf("fulfil after stock unit change = %d, want 409: %s", ful.status, ful.raw)
		}
		_, blockers, fields := errorOf(t, ful)
		if fmt.Sprint(blockers) != "[stock_unit_changed]" || fmt.Sprint(fields) != "[lines[0]]" {
			t.Fatalf("error blockers=%v fields=%v, want [stock_unit_changed] and [lines[0]]", blockers, fields)
		}
	})
}

// TestFulfilmentSubTally_PartialBillsAndExceedsUnbilledValidation covers P2-2
// (ADR 0006 sections 4.3 and 4.4):
//   - POST /orders/{id}/fulfillments accepts lines[].tally (with quantity
//     derived from the sub-tally's linear feet);
//   - a sub-tally row exceeding remaining unbilled pieces at that length is
//     refused 400 validation_failed on lines[i].tally.rows[j].pieces with
//     exceeds_unbilled;
//   - a subsequent bill omitting tally for the full remaining quantity copies
//     the remaining unbilled tally rows onto the invoice line.
func TestFulfilmentSubTally_PartialBillsAndExceedsUnbilledValidation(t *testing.T) {
	testutil.LockOutboxTables(t)
	db := testutil.RequireDB(t)
	f := newFixture(t, db)

	rlID := uuid.New()
	rlSKU := "RL24-" + uuid.NewString()[:8]
	mustExec(t, db, `
		INSERT INTO products (id, sku, description, uom_primary, sale_uom, price_uom, purchase_uom,
			base_price, average_unit_cost, random_length, board_thickness_in, board_width_in)
		VALUES ($1, $2, '2x4 Random Length SPF', 'LF', 'LF', 'LF', 'LF', 1.00, 0.50, TRUE, 2, 4)`,
		rlID, rlSKU)
	mustExec(t, db, `INSERT INTO inventory (product_id, location_id, location, quantity, allocated) VALUES ($1, $2, 'Y', 500, 0)`,
		rlID, f.yardID)
	t.Cleanup(func() {
		mustExec(t, db, `DELETE FROM line_tally_rows WHERE order_line_id IN (SELECT id FROM order_lines WHERE product_id = $1)
			OR invoice_line_id IN (SELECT id FROM invoice_lines WHERE product_id = $1)`, rlID)
		mustExec(t, db, `DELETE FROM invoice_lines WHERE product_id = $1`, rlID)
		mustExec(t, db, `DELETE FROM order_lines WHERE product_id = $1`, rlID)
		mustExec(t, db, `DELETE FROM inventory WHERE product_id = $1`, rlID)
		mustExec(t, db, `DELETE FROM products WHERE id = $1`, rlID)
	})

	// Order 10x8' (80 LF) + 6x12' (72 LF) = 152 LF.
	createOrder := f.do("POST", "/api/v1/orders", map[string]any{
		"customer_id":   f.customerID.String(),
		"delivery_type": "pickup",
		"lines": []any{map[string]any{
			"product_id":                 rlID.String(),
			"uom":                        "LF",
			"unit_price_ten_thousandths": 10000,
			"override_reason":            "contract RL rate",
			"tally": map[string]any{
				"rows": []any{
					map[string]any{"pieces": 10, "length_ft": "8"},
					map[string]any{"pieces": 6, "length_ft": "12"},
				},
			},
		}},
	})
	if createOrder.status != 201 {
		t.Fatalf("create tallied order = %d: %s", createOrder.status, createOrder.raw)
	}
	orderID := str(t, createOrder.body, "id")
	orderLineID := str(t, createOrder.body["lines"].([]any)[0].(map[string]any), "id")

	conf := f.do("POST", "/api/v1/orders/"+orderID+"/transitions", map[string]any{
		"to": "confirmed", "revision": rev(t, createOrder),
	})
	if conf.status != 200 {
		t.Fatalf("confirm = %d: %s", conf.status, conf.raw)
	}
	curRev := rev(t, conf)

	// 1. Asking for 11 pieces at 8' (only 10 ordered) fails 400 on lines[0].tally.rows[0].pieces.
	badFul := f.do("POST", "/api/v1/orders/"+orderID+"/fulfillments", map[string]any{
		"revision":     curRev,
		"picked_up_by": "Driver 1",
		"lines": []any{map[string]any{
			"order_line_id": orderLineID,
			"tally": map[string]any{
				"rows": []any{
					map[string]any{"pieces": 11, "length_ft": "8"},
				},
			},
		}},
	})
	if badFul.status != 400 {
		t.Fatalf("over-tally fulfil = %d, want 400: %s", badFul.status, badFul.raw)
	}
	_, _, fields := errorOf(t, badFul)
	if fmt.Sprint(fields) != "[lines[0].tally.rows[0].pieces]" || !strings.Contains(string(badFul.raw), "exceeds_unbilled") {
		t.Fatalf("expected exceeds_unbilled on lines[0].tally.rows[0].pieces, got fields=%v body=%s", fields, badFul.raw)
	}

	// 2. Partial bill with sub-tally: 4x8' (32 LF) + 2x12' (24 LF) = 56 LF (quantity omitted).
	inv1ID, curRev := f.fulfil(orderID, curRev, []map[string]any{{
		"order_line_id": orderLineID,
		"tally": map[string]any{
			"rows": []any{
				map[string]any{"pieces": 4, "length_ft": "8"},
				map[string]any{"pieces": 2, "length_ft": "12"},
			},
		},
	}})
	inv1 := f.getInvoice(inv1ID)
	inv1Line := inv1.body["lines"].([]any)[0].(map[string]any)
	if inv1Line["quantity"] != "56" || inv1Line["stock_quantity"] != "56" {
		t.Fatalf("invoice 1 line quantities = %v / %v, want 56 / 56", inv1Line["quantity"], inv1Line["stock_quantity"])
	}
	inv1Tally := inv1Line["tally"].(map[string]any)
	if inv1Tally["linear_feet"] != "56" || len(inv1Tally["rows"].([]any)) != 2 {
		t.Fatalf("invoice 1 tally = %+v, want linear_feet=56 and 2 rows", inv1Tally)
	}

	// 3. Asking for 7 pieces at 8' on the second bill (only 6 remain unbilled) fails 400 exceeds_unbilled.
	badFul2 := f.do("POST", "/api/v1/orders/"+orderID+"/fulfillments", map[string]any{
		"revision":     curRev,
		"picked_up_by": "Driver 2",
		"lines": []any{map[string]any{
			"order_line_id": orderLineID,
			"tally": map[string]any{
				"rows": []any{
					map[string]any{"pieces": 7, "length_ft": "8"},
				},
			},
		}},
	})
	if badFul2.status != 400 {
		t.Fatalf("second over-tally fulfil = %d, want 400: %s", badFul2.status, badFul2.raw)
	}
	_, _, fields2 := errorOf(t, badFul2)
	if fmt.Sprint(fields2) != "[lines[0].tally.rows[0].pieces]" || !strings.Contains(string(badFul2.raw), "exceeds_unbilled") {
		t.Fatalf("expected exceeds_unbilled on lines[0].tally.rows[0].pieces, got fields=%v body=%s", fields2, badFul2.raw)
	}

	// 4. Fulfil remaining 96 LF (6x8' + 4x12') with lines omitted -> copies remaining tally rows.
	inv2ID, _ := f.fulfil(orderID, curRev, nil)
	inv2 := f.getInvoice(inv2ID)
	inv2Line := inv2.body["lines"].([]any)[0].(map[string]any)
	inv2Tally, ok := inv2Line["tally"].(map[string]any)
	if !ok || inv2Tally == nil || inv2Tally["linear_feet"] != "96" || len(inv2Tally["rows"].([]any)) != 2 {
		t.Fatalf("invoice 2 remaining tally = %+v, want linear_feet=96 with 2 rows (6x8' and 4x12')", inv2Line["tally"])
	}
}
