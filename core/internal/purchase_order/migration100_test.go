// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package purchase_order_test

// Migration 100 on rows that exist (the recipe's migration step): the schema
// up to 098 is built in a scratch database of its own, legacy rows are
// written in the shape the base commit left them (purchase orders with no
// created_at, lines with a two-decimal cost, vendors with no created_at,
// stock on two branches), then 100 is applied and each backfill is read
// back. The down refuses where new-shape data has no old place and drops
// what the up wrote; the up applies again.

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/gablelbm/gable/internal/testutil"
	"github.com/jackc/pgx/v5"
)

func scratchDB(t *testing.T) *pgx.Conn {
	t.Helper()
	db := testutil.RequireDB(t)
	base, err := url.Parse(db.Pool.Config().ConnString())
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	admin, err := pgx.Connect(ctx, base.String())
	if err != nil {
		t.Fatal(err)
	}
	suffix := make([]byte, 6)
	if _, err := rand.Read(suffix); err != nil {
		t.Fatal(err)
	}
	name := "gv1_c41amig_" + hex.EncodeToString(suffix)
	if _, err := admin.Exec(ctx, fmt.Sprintf(`CREATE DATABASE %q`, name)); err != nil {
		admin.Close(ctx)
		t.Skipf("cannot create a scratch database: %v", err)
	}
	t.Cleanup(func() {
		_, _ = admin.Exec(context.Background(), fmt.Sprintf(`DROP DATABASE %q WITH (FORCE)`, name))
		admin.Close(context.Background())
	})

	scratch := *base
	scratch.Path = "/" + name
	conn, err := pgx.Connect(ctx, scratch.String())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { conn.Close(context.Background()) })
	return conn
}

func migration100Files(t *testing.T) (before []string, target, down string) {
	t.Helper()
	all, err := filepath.Glob("../../migrations/*.sql")
	if err != nil || len(all) == 0 {
		t.Fatalf("no migrations found: %v", err)
	}
	sort.Strings(all)
	for _, f := range all {
		base := filepath.Base(f)
		switch {
		case strings.HasPrefix(base, "100_"):
			target = f
		case base < "100_":
			before = append(before, f)
		}
	}
	if target == "" {
		t.Fatal("migration 100 not found")
	}
	down = "../../migrations/down/100_inventory_purchasing_wire_contract_down.sql"
	if _, err := os.Stat(down); err != nil {
		t.Fatal("migration 100 down not found")
	}
	return before, target, down
}

func applyMigrationFile(t *testing.T, conn *pgx.Conn, file string) {
	t.Helper()
	sql, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := conn.Exec(context.Background(), string(sql)); err != nil {
		t.Fatalf("applying %s: %v", filepath.Base(file), err)
	}
}

func TestMigration100_BackfillsRowsThatExist(t *testing.T) {
	conn := scratchDB(t)
	before, target, downFile := migration100Files(t)
	for _, f := range before {
		applyMigrationFile(t, conn, f)
	}
	ctx := context.Background()

	// Legacy rows in the base commit's shape: a branch with two yards on two
	// branches, a product with reorder targets, a vendor with no created_at,
	// and two purchase orders with no created_at (the older one first by id
	// order after the fill) whose lines carry a two decimal cost, one line
	// with no product.
	branchA, branchB := "3f0d6b6e-1111-4e6a-9b7d-100000000001", "3f0d6b6e-1111-4e6a-9b7d-100000000002"
	yardA, yardB := "3f0d6b6e-1111-4e6a-9b7d-100000000003", "3f0d6b6e-1111-4e6a-9b7d-100000000004"
	product := "3f0d6b6e-1111-4e6a-9b7d-000000000001"
	vendor := "3f0d6b6e-1111-4e6a-9b7d-000000000002"
	po1, po2 := "3f0d6b6e-1111-4e6a-9b7d-000000000003", "3f0d6b6e-1111-4e6a-9b7d-000000000004"
	for _, l := range []struct{ id, typ, parent string }{
		{branchA, "BRANCH", ""}, {branchB, "BRANCH", ""},
		{yardA, "YARD", branchA}, {yardB, "YARD", branchB},
	} {
		if _, err := conn.Exec(ctx, `INSERT INTO locations (id, type, code, parent_id) VALUES ($1, $2, $3, NULLIF($4, '')::uuid)`,
			l.id, l.typ, "m1-"+l.id[:13], l.parent); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := conn.Exec(ctx, `INSERT INTO products (id, sku, description, uom_primary, base_price, reorder_point, reorder_qty)
		VALUES ($1, 'M100-1', 'migration 100', 'EA', 1, 4, 9)`, product); err != nil {
		t.Fatal(err)
	}
	if _, err := conn.Exec(ctx, `INSERT INTO vendors (id, name) VALUES ($1, 'm100 vendor')`, vendor); err != nil {
		t.Fatal(err)
	}
	// Stock on both branches: the stock_levels backfill covers each.
	for _, y := range []string{yardA, yardB} {
		if _, err := conn.Exec(ctx, `INSERT INTO inventory (product_id, location_id, location, quantity) VALUES ($1, $2, 'm100', 3)`,
			product, y); err != nil {
			t.Fatal(err)
		}
	}
	// Two purchase orders: po1 carries updated_at (so created_at takes it),
	// po2 neither. The number backfill orders on (created_at, id): po1 is
	// older (its filled created_at is the fixed date) and takes PO-000001.
	if _, err := conn.Exec(ctx, `INSERT INTO purchase_orders (id, vendor_id, status, source, branch_id, created_at, updated_at)
		VALUES ($1, $2, 'SENT', 'MANUAL', $3, NULL, '2026-01-06T10:00:00Z')`, po1, vendor, branchA); err != nil {
		t.Fatal(err)
	}
	if _, err := conn.Exec(ctx, `INSERT INTO purchase_orders (id, vendor_id, status, source, branch_id, created_at)
		VALUES ($1, $2, 'DRAFT', 'MANUAL', $3, NULL)`, po2, vendor, branchB); err != nil {
		t.Fatal(err)
	}
	// po1's lines: one with a product and a cents-carrying cost, one with no
	// product. Inserted in reverse created_at so the position backfill is
	// exercised (the later row first).
	if _, err := conn.Exec(ctx, `INSERT INTO purchase_order_lines (id, po_id, product_id, description, quantity, cost, created_at)
		VALUES ('3f0d6b6e-1111-4e6a-9b7d-000000000011', $1, $2, 'm100 second', 2.5, 20.55, '2026-01-06T11:00:00Z')`, po1, product); err != nil {
		t.Fatal(err)
	}
	if _, err := conn.Exec(ctx, `INSERT INTO purchase_order_lines (id, po_id, product_id, description, quantity, cost, created_at)
		VALUES ('3f0d6b6e-1111-4e6a-9b7d-000000000012', $1, NULL, 'm100 first no product', 1, 3.5, '2026-01-06T10:30:00Z')`, po1); err != nil {
		t.Fatal(err)
	}

	applyMigrationFile(t, conn, target)

	// created_at is NOT NULL everywhere; po1's row kept its updated_at.
	var nulls int
	if err := conn.QueryRow(ctx, `SELECT COUNT(*) FROM purchase_orders WHERE created_at IS NULL`).Scan(&nulls); err != nil {
		t.Fatal(err)
	}
	if nulls != 0 {
		t.Fatalf("%d purchase orders still hold a NULL created_at", nulls)
	}
	if err := conn.QueryRow(ctx, `SELECT COUNT(*) FROM vendors WHERE created_at IS NULL`).Scan(&nulls); err != nil {
		t.Fatal(err)
	}
	if nulls != 0 {
		t.Fatalf("%d vendors still hold a NULL created_at", nulls)
	}

	// Numbers: po1 (older) is PO-000001, po2 is PO-000002; revisions at 1;
	// currency the deployment default; the status CHECK holds today's five.
	var n1, n2 string
	var rev int
	var currency string
	if err := conn.QueryRow(ctx, `SELECT number, revision, currency FROM purchase_orders WHERE id = $1`, po1).Scan(&n1, &rev, &currency); err != nil {
		t.Fatal(err)
	}
	if err := conn.QueryRow(ctx, `SELECT number FROM purchase_orders WHERE id = $1`, po2).Scan(&n2); err != nil {
		t.Fatal(err)
	}
	if n1 != "PO-000001" || n2 != "PO-000002" {
		t.Fatalf("numbers = %q, %q, want PO-000001, PO-000002 (backfill in (created_at, id) order)", n1, n2)
	}
	if rev != 1 || currency != "USD" {
		t.Fatalf("revision %d currency %q, want 1 and USD", rev, currency)
	}

	// The line shape: position by (created_at, id) within the purchase
	// order, the product's stocking unit on the product line (pair 1 and 1,
	// stock_quantity = quantity), nothing on the no-product line, and
	// line_total the extension at cents scale.
	var pos int
	var uom, priceUOM, stockUOM string
	var uomQty, priceUOMQty, stockQty, quantity, lineTotal string
	if err := conn.QueryRow(ctx, `
		SELECT position, COALESCE(uom, ''), COALESCE(price_uom, ''), COALESCE(stock_uom, ''),
		       uom_qty::text, price_uom_qty::text, stock_quantity::text, quantity::text, line_total::text
		FROM purchase_order_lines WHERE id = '3f0d6b6e-1111-4e6a-9b7d-000000000011'`).Scan(
		&pos, &uom, &priceUOM, &stockUOM, &uomQty, &priceUOMQty, &stockQty, &quantity, &lineTotal); err != nil {
		t.Fatal(err)
	}
	if pos != 2 || uom != "EA" || priceUOM != "EA" || stockUOM != "EA" {
		t.Fatalf("product line position %d uom %q/%q/%q, want 2 and EA three times", pos, uom, priceUOM, stockUOM)
	}
	if uomQty != "1.0000" || priceUOMQty != "1.0000" || stockQty != "2.5000" {
		t.Fatalf("pair %q/%q stock_quantity %q, want 1.0000, 1.0000, 2.5000", uomQty, priceUOMQty, stockQty)
	}
	if lineTotal != "51.38" { // 2.5 x 20.55, rounded once
		t.Fatalf("line_total = %q, want 51.38", lineTotal)
	}
	var noProductUOMNull bool
	if err := conn.QueryRow(ctx, `SELECT position, uom IS NULL FROM purchase_order_lines WHERE id = '3f0d6b6e-1111-4e6a-9b7d-000000000012'`).Scan(&pos, &noProductUOMNull); err != nil {
		t.Fatal(err)
	}
	if pos != 1 || !noProductUOMNull {
		t.Fatalf("no-product line position %d null-unit %v, want 1 and true", pos, noProductUOMNull)
	}

	// stock_levels: one row per branch holding the product, the product's
	// own targets.
	var levels int
	if err := conn.QueryRow(ctx, `SELECT COUNT(*) FROM stock_levels WHERE product_id = $1 AND reorder_point = 4 AND reorder_quantity = 9`, product).Scan(&levels); err != nil {
		t.Fatal(err)
	}
	if levels != 2 {
		t.Fatalf("stock_levels backfilled %d rows, want 2 (one per holding branch)", levels)
	}
	for _, table := range []string{"stock_level_dirty", "reorder_recommendations"} {
		if err := conn.QueryRow(ctx, `SELECT COUNT(*) FROM ` + table).Scan(&nulls); err != nil {
			t.Fatalf("%s: %v", table, err)
		}
	}
	if err := conn.QueryRow(ctx, `SELECT COUNT(*) FROM information_schema.columns WHERE table_name = 'reorder_runs' AND column_name = 'branch_id'`).Scan(&nulls); err != nil {
		t.Fatal(err)
	}
	if nulls != 1 {
		t.Fatal("reorder_runs did not gain branch_id")
	}
	for _, idx := range []string{"idx_purchase_orders_created_id", "idx_vendors_created_id"} {
		if err := conn.QueryRow(ctx, `SELECT COUNT(*) FROM pg_indexes WHERE indexname = $1`, idx).Scan(&nulls); err != nil {
			t.Fatal(err)
		}
		if nulls != 1 {
			t.Fatalf("the keyset index %s is missing", idx)
		}
	}

	// The down: the up's own backfill maps back (no divergent targets, no
	// acted-on recommendations, no too-fine values), so it drops everything
	// the up added; the up applies again on a fresh round trip.
	applyMigrationFile(t, conn, downFile)
	if err := conn.QueryRow(ctx, `SELECT COUNT(*) FROM information_schema.columns WHERE table_name = 'purchase_orders' AND column_name IN ('number', 'revision', 'currency', 'sent_at')`).Scan(&nulls); err != nil {
		t.Fatal(err)
	}
	if nulls != 0 {
		t.Fatal("the down left a purchase_orders column behind")
	}
	if err := conn.QueryRow(ctx, `SELECT COUNT(*) FROM information_schema.columns WHERE table_name = 'purchase_order_lines' AND column_name IN ('unit_cost', 'position', 'uom', 'line_total')`).Scan(&nulls); err != nil {
		t.Fatal(err)
	}
	if nulls != 0 {
		t.Fatal("the down left a purchase_order_lines column behind")
	}
	if err := conn.QueryRow(ctx, `SELECT COUNT(*) FROM information_schema.columns WHERE table_name = 'purchase_order_lines' AND column_name = 'cost'`).Scan(&nulls); err != nil {
		t.Fatal(err)
	}
	if nulls != 1 {
		t.Fatal("the down did not restore the cost column")
	}
	if err := conn.QueryRow(ctx, `SELECT to_regclass('stock_levels')::text`).Scan(&nulls); err == nil {
		_ = nulls
		t.Fatal("the down left the stock_levels table behind")
	}
	applyMigrationFile(t, conn, target)
	if err := conn.QueryRow(ctx, `SELECT COUNT(*) FROM purchase_orders WHERE number IS NULL OR revision IS NULL`).Scan(&nulls); err != nil {
		t.Fatal(err)
	}
	if nulls != 0 {
		t.Fatalf("the second up left %d rows unnumbered", nulls)
	}
	var again string
	if err := conn.QueryRow(ctx, `SELECT number FROM purchase_orders WHERE id = $1`, po1).Scan(&again); err != nil {
		t.Fatal(err)
	}
	if again != "PO-000001" {
		t.Fatalf("the second up renumbered po1 to %q, want PO-000001 (the backfill is stable)", again)
	}
}

// TestMigration100_DownRefusesNewShapeData: a per branch stock level target
// that diverges from the product's own has no old place, and the down says
// so instead of discarding it.
func TestMigration100_DownRefusesNewShapeData(t *testing.T) {
	conn := scratchDB(t)
	before, target, downFile := migration100Files(t)
	for _, f := range before {
		applyMigrationFile(t, conn, f)
	}
	ctx := context.Background()
	branch := "3f0d6b6e-2111-4e6a-9b7d-100000000001"
	product := "3f0d6b6e-2111-4e6a-9b7d-000000000001"
	if _, err := conn.Exec(ctx, `INSERT INTO locations (id, type, code) VALUES ($1, 'BRANCH', 'm100b')`, branch); err != nil {
		t.Fatal(err)
	}
	if _, err := conn.Exec(ctx, `INSERT INTO products (id, sku, description, uom_primary, base_price, reorder_point, reorder_qty)
		VALUES ($1, 'M100-2', 'migration 100 refuse', 'EA', 1, 4, 9)`, product); err != nil {
		t.Fatal(err)
	}
	if _, err := conn.Exec(ctx, `INSERT INTO inventory (product_id, location_id, location, quantity) VALUES ($1, $2, 'm100b', 3)`,
		product, branch); err != nil {
		t.Fatal(err)
	}
	applyMigrationFile(t, conn, target)
	if _, err := conn.Exec(ctx, `UPDATE stock_levels SET reorder_point = 40 WHERE product_id = $1`, product); err != nil {
		t.Fatal(err)
	}
	sql, err := os.ReadFile(downFile)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := conn.Exec(ctx, string(sql)); err == nil {
		t.Fatal("the down applied over a divergent per branch target, want the refusal")
	} else if !strings.Contains(err.Error(), "diverge") {
		t.Fatalf("the down refused for another reason: %v", err)
	}
}
