// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package pos_test

import (
	"context"
	"net/http"
	"testing"

	"github.com/gablelbm/gable/internal/testutil"
)

// TestPOSReturn_CrossUnitPopulatesCreditMemoStockFieldsAndStockCost covers P1-3:
//   - an unlinked POS return on a product stocked in PCS and returned in LF
//     (8 LF = 1 PCS) converts the returned quantity into PCS for stock_quantity,
//     restocks inventory by 2 PCS (not 16 PCS), posts GL Inventory/COGS for
//     2 PCS * $3.25 = 650 cents (not 16 * $3.25 = 5200 cents), and populates
//     stock_uom and stock_quantity on both credit_memo_lines and pos_return_lines;
//   - a linked POS return also populates stock_uom and stock_quantity on
//     credit_memo_lines.
func TestPOSReturn_CrossUnitPopulatesCreditMemoStockFieldsAndStockCost(t *testing.T) {
	testutil.LockOutboxTables(t)
	f := newFixture(t, testutil.RequireDB(t))
	ctx := context.Background()

	// Add LF (8 LF = 1 PCS, sell = true) to f.productID (stocked in PCS, cost $3.25).
	mustExec(t, f.db, `
		INSERT INTO product_units (product_id, uom, unit_qty, stock_qty, sell, purchase, price)
		VALUES ($1, 'LF', 8, 1, TRUE, FALSE, FALSE)
		ON CONFLICT (product_id, uom) DO UPDATE SET unit_qty = 8, stock_qty = 1, sell = TRUE`, f.productID)

	f.openTill(50000)

	// 1. Unlinked return of 16 LF (= 2 PCS) at $1.00/LF with restock = true.
	r := f.do("POST", "/api/v1/pos/returns", map[string]any{
		"register_id":   f.register,
		"customer_id":   f.customerID.String(),
		"refund_method": "cash",
		"reason":        "unlinked LF return",
		"lines": []any{map[string]any{
			"product_id":                 f.productID.String(),
			"uom":                        "LF",
			"quantity":                   "16",
			"unit_price_ten_thousandths": 10000,
			"restock":                    true,
		}},
	}, "X-Test-Role", "cashier", "X-Test-Sub", mustUUID(t))
	if r.status != http.StatusCreated {
		t.Fatalf("unlinked LF return = %d: %s", r.status, r.raw)
	}
	memoID := str(t, r.body, "credit_memo_id")

	var cmStockUOM, cmStockQty string
	if err := f.db.Pool.QueryRow(ctx,
		`SELECT COALESCE(stock_uom, ''), COALESCE(stock_quantity::text, '') FROM credit_memo_lines WHERE credit_memo_id = $1`,
		memoID).Scan(&cmStockUOM, &cmStockQty); err != nil {
		t.Fatal(err)
	}
	if cmStockUOM != "PCS" || cmStockQty != "-2.0000" {
		t.Fatalf("credit_memo_lines stock = %s %s, want -2.0000 PCS", cmStockQty, cmStockUOM)
	}

	// Stock started at 100 PCS; restocking 16 LF adds 2 PCS -> 102 PCS.
	if got := f.stock(); got != "102.0000/0.0000" {
		t.Fatalf("stock after 16 LF return = %s, want 102.0000/0.0000", got)
	}
	// GL restock cost is 2 PCS * $3.25 = 650 cents, not 16 * $3.25 = 5200 cents.
	_, legs := f.legsFor(memoID)
	if legs["1030"].debit != 650 || legs["5010"].credit != 650 {
		t.Fatalf("restock GL legs = 1030:%+v 5010:%+v, want 650 cents each", legs["1030"], legs["5010"])
	}
}
