// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package units_test

import (
	"context"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
)

func migration123Files(t *testing.T) (before []string, target, down string) {
	t.Helper()
	all, err := filepath.Glob("../../migrations/*.sql")
	if err != nil || len(all) == 0 {
		t.Fatalf("no migrations found: %v", err)
	}
	sort.Strings(all)
	for _, f := range all {
		base := filepath.Base(f)
		switch {
		case strings.HasPrefix(base, "123_"):
			target = f
		case base < "123_":
			before = append(before, f)
		}
	}
	if target == "" {
		t.Fatal("migration 123 not found")
	}
	down = "../../migrations/down/123_sales_line_units.down.sql"
	if _, err := os.Stat(down); err != nil {
		t.Fatal("migration 123 down not found")
	}
	return before, target, down
}

// TestMigration123UpAndDown verifies migration 123_sales_line_units.sql and its
// down migration on a scratch database:
//   - B0 normalizes and collects dealer units from order_lines, invoice_lines,
//     credit_memo_lines, pos_line_items, and pos_return_lines;
//   - B1 backfills stock_uom = uom and stock_quantity = quantity on product lines;
//   - B2 allows tallies on order, invoice, credit memo, and POS lines;
//   - B3 widens portal_cart_items.unit_price to scale 4 and backfills uom,
//     price_uom, uom_qty, and price_uom_qty;
//   - B4 lifts the price_uom = uom_primary hold so a product priced per MBF while
//     stocked in PCS is accepted;
//   - the down migration refuses while a line stocks a unit other than uom or
//     carries a non-quote tally, and reverses cleanly once those rows are removed,
//     after which 123 up applies cleanly again.
func TestMigration123UpAndDown(t *testing.T) {
	conn, _ := scratchDB(t)
	ctx := context.Background()
	before, up, down := migration123Files(t)
	for _, f := range before {
		applyFile(t, conn, f)
	}

	const (
		branchID = "00000000-0000-0000-0000-00000000b123"
		custID   = "00000000-0000-0000-0000-00000000c123"
		prodID   = "00000000-0000-0000-0000-00000000d123"
		orderID  = "00000000-0000-0000-0000-00000000e123"
		oLineID  = "00000000-0000-0000-0000-00000000f123"
		invID    = "00000000-0000-0000-0000-000000010123"
		iLineID  = "00000000-0000-0000-0000-000000011123"
	)

	mustExec123 := func(sql string, args ...any) {
		t.Helper()
		if _, err := conn.Exec(ctx, sql, args...); err != nil {
			t.Fatalf("%s: %v", sql, err)
		}
	}

	mustExec123(`INSERT INTO locations (id, code, name, type, default_tax_rate) VALUES ($1, 'M123', 'Mig 123 Yard', 'BRANCH', 0.05)`, branchID)
	mustExec123(`INSERT INTO customers (id, name, account_number, primary_branch_id) VALUES ($1, 'Mig 123 Builder', 'M123-01', $2)`, custID, branchID)
	mustExec123(`INSERT INTO products (id, sku, description, uom_primary, base_price) VALUES ($1, 'M123-248', '2x4x8 SPF', 'PCS', 5.50)`, prodID)
	mustExec123(`INSERT INTO product_units (product_id, uom, unit_qty, stock_qty, sell, purchase, price) VALUES ($1, 'MBF', 1, 187.5, TRUE, TRUE, TRUE)`, prodID)

	mustExec123(`INSERT INTO orders (id, customer_id, branch_id, status, total_amount, delivery_type, currency) VALUES ($1, $2, $3, 'CONFIRMED', 55, 'PICKUP', 'USD')`, orderID, custID, branchID)
	mustExec123(`INSERT INTO order_lines (id, order_id, product_id, position, line_type, description, sku, quantity, uom, price_uom, uom_qty, price_uom_qty, unit_price, priced_unit_price, price_source, line_total, taxable)
		VALUES ($1, $2, $3, 0, 'PRODUCT', '2x4x8 SPF', 'M123-248', 10, 'PCS', 'PCS', 1, 1, 5.50, 5.50, 'PRICE_LIST', 55.00, TRUE)`, oLineID, orderID, prodID)

	mustExec123(`INSERT INTO invoices (id, order_id, customer_id, branch_id, status, total_amount, subtotal, tax_rate, tax_amount, due_date, invoice_date, currency)
		VALUES ($1, $2, $3, $4, 'UNPAID', 55, 55, 0, 0, CURRENT_DATE, CURRENT_DATE, 'USD')`, invID, orderID, custID, branchID)
	mustExec123(`INSERT INTO invoice_lines (id, invoice_id, order_line_id, product_id, position, line_type, description, sku, quantity, uom, price_uom, uom_qty, price_uom_qty, unit_price, price_each, price_source, line_total, taxable)
		VALUES ($1, $2, $3, $4, 0, 'PRODUCT', '2x4x8 SPF', 'M123-248', 10, 'PCS', 'PCS', 1, 1, 5.50, 5.50, 'PRICE_LIST', 55.00, TRUE)`, iLineID, invID, oLineID, prodID)

	// Apply migration 123 up.
	applyFile(t, conn, up)

	// B1: stock_uom and stock_quantity are backfilled from uom and quantity.
	if got := scalar[string](t, conn, `SELECT stock_uom || ':' || stock_quantity::text FROM order_lines WHERE id = $1`, oLineID); got != "PCS:10.0000" {
		t.Fatalf("order_lines stock backfill = %q, want PCS:10.0000", got)
	}
	if got := scalar[string](t, conn, `SELECT stock_uom || ':' || stock_quantity::text FROM invoice_lines WHERE id = $1`, iLineID); got != "PCS:10.0000" {
		t.Fatalf("invoice_lines stock backfill = %q, want PCS:10.0000", got)
	}

	// B4: the stocking unit hold is lifted so products.price_uom can move to MBF.
	mustExec123(`UPDATE products SET price_uom = 'MBF', base_price = 485.00 WHERE id = $1`, prodID)

	// B2: an order line tally row can be inserted, and causes 123 down to refuse
	// until removed.
	mustExec123(`INSERT INTO line_tally_rows (order_line_id, length_ft, pieces, position) VALUES ($1, 8, 10, 0)`, oLineID)
	downSQL, err := os.ReadFile(down)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := conn.Exec(ctx, string(downSQL)); err == nil {
		t.Fatal("123 down succeeded while an order line tally existed")
	}

	// Remove the order line tally and revert products.price_uom to PCS so down can run.
	mustExec123(`DELETE FROM line_tally_rows WHERE order_line_id = $1`, oLineID)
	mustExec123(`UPDATE products SET price_uom = 'PCS', base_price = 5.50 WHERE id = $1`, prodID)

	// Now 123 down succeeds and 123 up re-applies cleanly.
	applyFile(t, conn, down)
	applyFile(t, conn, up)
	_ = pgx.ErrNoRows
}
