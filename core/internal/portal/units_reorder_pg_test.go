// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package portal

import (
	"context"
	"log/slog"
	"math"
	"testing"

	"github.com/gablelbm/gable/internal/pricing"
	"github.com/gablelbm/gable/internal/product"
	"github.com/google/uuid"
)

// TestAddToCartAndCreateReorder_CrossUnitProduct covers P1-2 and P2-3:
//   - P1-2: AddToCart on a product sold in PCS and priced in MBF (187.5 PCS =
//     1 MBF at $485.00/MBF) stores unit_price = 485.00 in MBF and computes
//     line_total = $773.41 for 299 PCS (applying the 1/187.5 conversion once,
//     never dividing by 187.5 twice);
//   - P2-3: CreateReorder on a cross-unit order line preserves price_uom,
//     uom_qty, price_uom_qty, stock_uom, stock_quantity, and conversion-aware
//     line_total.
func TestAddToCartAndCreateReorder_CrossUnitProduct(t *testing.T) {
	tt := setupTenants(t)
	ctx := context.Background()

	prodID := uuid.New()
	sku := "P2X4-" + uuid.NewString()[:8]
	if _, err := tt.db.Pool.Exec(ctx, `
		INSERT INTO products (id, sku, description, uom_primary, sale_uom, price_uom, purchase_uom,
			base_price, average_unit_cost, board_thickness_in, board_width_in, board_length_ft)
		VALUES ($1, $2, '2x4x8 SPF Portal', 'PCS', 'PCS', 'PCS', 'PCS', 485.00, 2.00, 2, 4, 8)`,
		prodID, sku); err != nil {
		t.Fatal(err)
	}
	if _, err := tt.db.Pool.Exec(ctx, `
		INSERT INTO product_units (product_id, uom, unit_qty, stock_qty, sell, purchase, price)
		VALUES
			($1, 'LF', 8, 1, TRUE, FALSE, FALSE),
			($1, 'MBF', 1, 187.5, FALSE, TRUE, TRUE)
		ON CONFLICT (product_id, uom) DO NOTHING`, prodID); err != nil {
		t.Fatal(err)
	}
	if _, err := tt.db.Pool.Exec(ctx, `SELECT set_config('gable.unit_set_write', 'on', true)`); err != nil {
		t.Fatal(err)
	}
	if _, err := tt.db.Pool.Exec(ctx, `UPDATE products SET price_uom = 'MBF' WHERE id = $1`, prodID); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = tt.db.Pool.Exec(ctx, `DELETE FROM portal_cart_items WHERE product_id = $1`, prodID)
		_, _ = tt.db.Pool.Exec(ctx, `DELETE FROM order_lines WHERE product_id = $1`, prodID)
		_, _ = tt.db.Pool.Exec(ctx, `DELETE FROM products WHERE id = $1`, prodID)
	})

	repo := NewRepository(tt.db)
	pricingSvc := pricing.NewService(pricing.NewRepository(tt.db))
	productSvc := product.NewService(product.NewRepository(tt.db))
	svc := NewService(repo, "test-secret-32-chars-minimum-1234", slog.Default(), pricingSvc, nil, nil, nil, productSvc)

	// P1-2: Add 299 PCS to cart. 299 PCS at $485.00/MBF (187.5 PCS = 1 MBF) -> $773.41.
	cart, err := svc.AddToCart(ctx, tt.aCustomer, AddToCartRequest{
		ProductID: prodID,
		Quantity:  299,
	})
	if err != nil {
		t.Fatalf("AddToCart: %v", err)
	}
	if len(cart.Items) != 1 {
		t.Fatalf("cart items = %d, want 1", len(cart.Items))
	}
	item := cart.Items[0]
	if item.UOM != "PCS" || item.PriceUOM != "MBF" {
		t.Fatalf("cart item units = %s / %s, want PCS / MBF", item.UOM, item.PriceUOM)
	}
	if math.Abs(item.UnitPrice-485.00) > 1e-6 {
		t.Fatalf("cart item unit_price = %v, want 485.00 (not pre-divided by 187.5)", item.UnitPrice)
	}
	if math.Abs(item.LineTotal-(299.0*485.0/187.5)) > 1e-6 {
		t.Fatalf("cart item line_total = %v, want ~773.4133 (not double-divided ~4.1249)", item.LineTotal)
	}

	// P2-3: Put 8 LF (= 1 PCS) on source order tt.aOrder and call CreateReorder.
	if _, err := tt.db.Pool.Exec(ctx, `DELETE FROM order_lines WHERE order_id = $1`, tt.aOrder); err != nil {
		t.Fatal(err)
	}
	if _, err := tt.db.Pool.Exec(ctx, `
		INSERT INTO order_lines (id, order_id, position, line_type, product_id, sku, description,
			quantity, uom, price_uom, uom_qty, price_uom_qty, stock_uom, stock_quantity,
			unit_price, priced_unit_price, price_source, line_total, taxable)
		VALUES ($1, $2, 0, 'PRODUCT', $3, $4, '2x4x8 SPF Portal',
			299, 'PCS', 'MBF', 187.5, 1, 'PCS', 299,
			485.00, 485.00, 'PRICE_LIST', 773.41, TRUE)`,
		uuid.New(), tt.aOrder, prodID, sku); err != nil {
		t.Fatal(err)
	}

	newOrderID, err := repo.CreateReorder(ctx, tt.aCustomer, tt.aOrder)
	if err != nil {
		t.Fatalf("CreateReorder: %v", err)
	}
	t.Cleanup(func() {
		_, _ = tt.db.Pool.Exec(ctx, `DELETE FROM order_lines WHERE order_id = $1`, newOrderID)
		_, _ = tt.db.Pool.Exec(ctx, `DELETE FROM orders WHERE id = $1`, newOrderID)
	})

	var uom, priceUOM, stockUOM string
	var uomQty, priceUOMQty, stockQty, lineTotal float64
	if err := tt.db.Pool.QueryRow(ctx, `
		SELECT uom, price_uom, uom_qty::float8, price_uom_qty::float8,
		       COALESCE(stock_uom, ''), COALESCE(stock_quantity::float8, 0), line_total::float8
		FROM order_lines WHERE order_id = $1`, newOrderID).
		Scan(&uom, &priceUOM, &uomQty, &priceUOMQty, &stockUOM, &stockQty, &lineTotal); err != nil {
		t.Fatal(err)
	}
	if uom != "PCS" || priceUOM != "MBF" || stockUOM != "PCS" {
		t.Fatalf("reordered line units = uom:%s price_uom:%s stock_uom:%s, want PCS/MBF/PCS", uom, priceUOM, stockUOM)
	}
	if math.Abs(uomQty-187.5) > 1e-6 || math.Abs(priceUOMQty-1.0) > 1e-6 || math.Abs(stockQty-299.0) > 1e-6 {
		t.Fatalf("reordered line quantities = uom_qty:%v price_uom_qty:%v stock_qty:%v, want 187.5/1/299", uomQty, priceUOMQty, stockQty)
	}
	if math.Abs(lineTotal-773.41) > 1e-6 {
		t.Fatalf("reordered line_total = %v, want 773.41", lineTotal)
	}
}
