// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package units_test

// Migration 098 on rows that exist (recipe step 3, ADR 0006 section 8's
// steps A1, A2 and A3): the schema up to 097 is built in a scratch database
// of its own, legacy rows are written in the shape the base commit left
// them, then 098 is applied and each backfill is read back. The down
// refuses while a product holds a unit set row other than its stocking row
// and, once the row is gone, reverses its own steps; the up applies again
// after the down.

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
	name := "gv1_c32amig_" + hex.EncodeToString(suffix)
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

func migrationFiles(t *testing.T) (before []string, target, down string) {
	t.Helper()
	all, err := filepath.Glob("../../migrations/*.sql")
	if err != nil || len(all) == 0 {
		t.Fatalf("no migrations found: %v", err)
	}
	sort.Strings(all)
	for _, f := range all {
		base := filepath.Base(f)
		switch {
		case strings.HasPrefix(base, "098_"):
			target = f
		case base < "098_":
			before = append(before, f)
		}
	}
	if target == "" {
		t.Fatal("migration 098 not found")
	}
	down = "../../migrations/down/098_units_catalogue_and_sets_down.sql"
	if _, err := os.Stat(down); err != nil {
		t.Fatal("migration 098 down not found")
	}
	return before, target, down
}

func applyFile(t *testing.T, conn *pgx.Conn, file string) {
	t.Helper()
	sql, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := conn.Exec(context.Background(), string(sql)); err != nil {
		t.Fatalf("applying %s: %v", filepath.Base(file), err)
	}
}

func scalar[T any](t *testing.T, conn *pgx.Conn, sql string, args ...any) T {
	t.Helper()
	var v T
	if err := conn.QueryRow(context.Background(), sql, args...).Scan(&v); err != nil {
		t.Fatalf("%s: %v", sql, err)
	}
	return v
}

// legacyRows writes the base commit's shape: a product stocked in PCS with
// a base price, a second product free of prices, a quote with three lines
// (one in the stocking unit, one careless MBF against PCS pair 1 and 1, one
// in LF, a unit that never entered any set), and a fixed price rule.
func legacyRows(t *testing.T, conn *pgx.Conn) {
	t.Helper()
	ctx := context.Background()
	legacy := `
		INSERT INTO system_settings (key, value)
		VALUES ('default_branch_id', (SELECT id::text FROM locations WHERE type = 'BRANCH' LIMIT 1))
		ON CONFLICT (key) DO UPDATE SET value = EXCLUDED.value;
		INSERT INTO products (id, sku, description, uom_primary, base_price)
		VALUES ('00000000-0000-0000-0000-0000000000f1', 'MIG-2X4', '2x4x8', 'PCS', 5.25),
		       ('00000000-0000-0000-0000-0000000000f2', 'MIG-FREE', 'unpriced special', 'EA', 0);
		INSERT INTO quotes (id, number, branch_id, customer_id, state)
		VALUES ('00000000-0000-0000-0000-0000000000b2', 'Q-000001',
			(SELECT id FROM locations WHERE type = 'BRANCH' LIMIT 1),
			(SELECT id FROM customers LIMIT 1), 'DRAFT');
		INSERT INTO quote_lines (id, quote_id, product_id, sku, description, quantity, uom, price_uom, uom_qty, price_uom_qty, unit_price, line_total, position, created_at)
		VALUES
			-- A line in the stocking unit.
			('00000000-0000-0000-0000-0000000000c1', '00000000-0000-0000-0000-0000000000b2',
			 '00000000-0000-0000-0000-0000000000f1', 'MIG-2X4', '2x4x8', 10, 'PCS', 'PCS', 1, 1, 5.25, 52.50, 0, now()),
			-- The careless line: MBF against PCS at 1 and 1, exactly what
			-- must never become the product's conversion.
			('00000000-0000-0000-0000-0000000000c2', '00000000-0000-0000-0000-0000000000b2',
			 '00000000-0000-0000-0000-0000000000f1', 'MIG-2X4', '2x4x8', 2, 'MBF', 'PCS', 1, 1, 525.00, 1050.00, 1, now()),
			-- A line in a unit that entered no set.
			('00000000-0000-0000-0000-0000000000c3', '00000000-0000-0000-0000-0000000000b2',
			 '00000000-0000-0000-0000-0000000000f1', 'MIG-2X4', '2x4x8', 16, 'LF', 'PCS', 8, 1, 0.70, 11.20, 2, now());
		INSERT INTO pricing_rules (id, name, rule_type, discount_pct, min_quantity, product_id, fixed_price, created_at, updated_at)
		VALUES ('00000000-0000-0000-0000-0000000000d1', 'Mig fixed', 'QUANTITY_BREAK', 0, 1,
			'00000000-0000-0000-0000-0000000000f1', 5.00, now(), now());`
	if _, err := conn.Exec(ctx, legacy); err != nil {
		t.Fatalf("seed legacy rows: %v", err)
	}
}

func TestMigration098_CatalogueSetsAndTallies(t *testing.T) {
	conn := scratchDB(t)
	before, target, downFile := migrationFiles(t)
	for _, f := range before {
		applyFile(t, conn, f)
	}
	legacyRows(t, conn)
	applyFile(t, conn, target)
	ctx := context.Background()

	// A1: the catalogue is seeded with the twenty three units of section
	// 2.2, GAL's standard size in lowest terms, and the two enum columns
	// are TEXT against it.
	if n := scalar[int](t, conn, `SELECT count(*) FROM units`); n != 23 {
		t.Errorf("the catalogue holds 23 seeded units, got %d", n)
	}
	var gu, gr string
	if err := conn.QueryRow(ctx, `SELECT std_unit_qty::text, std_ref_qty::text FROM units WHERE code='GAL'`).Scan(&gu, &gr); err != nil {
		t.Fatal(err)
	}
	if gu != "576.0000" || gr != "77.0000" {
		t.Errorf("GAL's standard size is (%s, %s); want (576, 77)", gu, gr)
	}
	if n := scalar[int](t, conn, `SELECT count(*) FROM products WHERE uom_primary IS NOT NULL AND pg_typeof(uom_primary)::text = 'text'`); n != 2 {
		t.Errorf("products.uom_primary is TEXT, got %d rows", n)
	}

	// A2: one row per product, its stocking unit at (1, 1) with every use
	// flag; the defaults follow; the CHECK holds the price unit at the
	// stocking unit.
	for _, sku := range []string{"MIG-2X4", "MIG-FREE"} {
		var uom string
		var uq, sq float64
		var sell, purchase, price bool
		if err := conn.QueryRow(ctx, `
			SELECT pu.uom, pu.unit_qty, pu.stock_qty, pu.sell, pu.purchase, pu.price
			FROM product_units pu JOIN products p ON p.id = pu.product_id
			WHERE p.sku = $1 AND pu.uom = p.uom_primary`, sku).
			Scan(&uom, &uq, &sq, &sell, &purchase, &price); err != nil {
			t.Fatalf("%s has no stocking row: %v", sku, err)
		}
		if uq != 1 || sq != 1 || !sell || !purchase || !price {
			t.Errorf("%s's stocking row is (%v, %v, %v, %v, %v); want (1, 1, true, true, true)", sku, uq, sq, sell, purchase, price)
		}
	}
	if n := scalar[int](t, conn, `SELECT count(*) FROM product_units`); n != 2 {
		t.Errorf("two products hold one row each, got %d rows", n)
	}
	if bad := scalar[int](t, conn, `SELECT count(*) FROM products WHERE sale_uom <> uom_primary OR price_uom <> uom_primary OR purchase_uom <> uom_primary`); bad != 0 {
		t.Errorf("%d products carry a default unit other than the stocking unit", bad)
	}
	// The hold's CHECK refuses another price unit on a raw write.
	if _, err := conn.Exec(ctx, `UPDATE products SET price_uom = 'MBF' WHERE sku = 'MIG-2X4'`); err == nil {
		t.Errorf("the CHECK price_uom = uom_primary refuses a raw other price unit")
	}
	// The price_unit_held trigger refuses a raw stocking unit change under
	// a base price; a product with no price moves freely.
	if _, err := conn.Exec(ctx, `UPDATE products SET uom_primary = 'EA' WHERE sku = 'MIG-2X4'`); err == nil || !strings.Contains(err.Error(), "price_unit_held") {
		t.Errorf("a raw stocking unit change under a base price is refused by the trigger, got %v", err)
	}
	if _, err := conn.Exec(ctx, `UPDATE products SET uom_primary = 'PCS' WHERE sku = 'MIG-FREE'`); err != nil {
		t.Errorf("a product with no price changes its stocking unit freely: %v", err)
	}
	// The old stocking unit's row stays behind on a raw change; the unit
	// set PUT manages the rows itself. Drop it so the later assertions
	// count exactly what the migration wrote.
	if _, err := conn.Exec(ctx, `DELETE FROM product_units WHERE product_id = '00000000-0000-0000-0000-0000000000f2' AND uom = 'EA'`); err != nil {
		t.Fatal(err)
	}

	// The careless line is a candidate and never a row.
	var cu, cs string
	var cnt int
	if err := conn.QueryRow(ctx, `
		SELECT uom, unit_qty::text, stock_qty::text, line_count
		FROM product_unit_candidates WHERE product_id = '00000000-0000-0000-0000-0000000000f1' AND uom = 'MBF'`).
		Scan(&cu, &cs, &cs, &cnt); err != nil {
		t.Fatalf("the careless MBF line is reported as a candidate: %v", err)
	}
	if cu != "MBF" || cs != "1.0000" || cnt < 1 {
		t.Errorf("the careless candidate is (%s, %s) on %d lines; want MBF at 1 and 1", cu, cs, cnt)
	}
	if n := scalar[int](t, conn, `SELECT count(*) FROM product_units pu JOIN products p ON p.id = pu.product_id WHERE pu.uom <> p.uom_primary`); n != 0 {
		t.Errorf("%d unit set rows were made from quote lines; only candidates are written", n)
	}

	// A3: the stocking unit line backfills exactly; the MBF and LF lines,
	// whose units entered no set, keep null stock fields.
	var stockUOM *string
	var stockQty *float64
	if err := conn.QueryRow(ctx, `SELECT stock_uom, stock_quantity::float8 FROM quote_lines WHERE id = '00000000-0000-0000-0000-0000000000c1'`).Scan(&stockUOM, &stockQty); err != nil {
		t.Fatal(err)
	}
	if stockUOM == nil || *stockUOM != "PCS" || stockQty == nil || *stockQty != 10 {
		t.Errorf("the PCS line backfills to 10 PCS of stock, got %v %v", stockUOM, stockQty)
	}
	for _, id := range []string{"00000000-0000-0000-0000-0000000000c2", "00000000-0000-0000-0000-0000000000c3"} {
		if err := conn.QueryRow(ctx, `SELECT stock_uom FROM quote_lines WHERE id = $1`, id).Scan(&stockUOM); err != nil {
			t.Fatal(err)
		}
		if stockUOM != nil {
			t.Errorf("line %s kept a stocking unit %v; a unit outside the product's set leaves it null", id, *stockUOM)
		}
	}

	// The raw writer's row defaults: a product inserted by plain SQL takes
	// its defaults and its stocking row.
	if _, err := conn.Exec(ctx, `INSERT INTO products (id, sku, description, uom_primary) VALUES ('00000000-0000-0000-0000-0000000000f3', 'MIG-RAW', 'raw insert', 'CTN')`); err != nil {
		t.Fatalf("a raw product insert works through the row defaults: %v", err)
	}
	if n := scalar[int](t, conn, `
		SELECT count(*) FROM product_units pu JOIN products p ON p.id = pu.product_id
		WHERE p.sku = 'MIG-RAW' AND pu.uom = 'CTN' AND pu.unit_qty = 1 AND pu.stock_qty = 1`); n != 1 {
		t.Errorf("a raw product insert gains its stocking row, got %d", n)
	}

	// The stocking row trigger's invariants: a random length product is
	// stocked in LF, and the stocking row carries price.
	if _, err := conn.Exec(ctx, `
		BEGIN;
		UPDATE products SET random_length = TRUE WHERE sku = 'MIG-2X4';
		COMMIT`); err == nil || !strings.Contains(err.Error(), "LF") {
		t.Errorf("a random length product stocked in PCS is refused, got %v", err)
	}
	if _, err := conn.Exec(ctx, `
		BEGIN;
		UPDATE product_units SET price = FALSE
		WHERE product_id = '00000000-0000-0000-0000-0000000000f1' AND uom = 'PCS';
		COMMIT`); err == nil || !strings.Contains(err.Error(), "price") {
		t.Errorf("a stocking row without price is refused, got %v", err)
	}

	// The down refuses while a product holds a set row other than its
	// stocking row, then reverses its own steps, and the up applies again.
	if _, err := conn.Exec(ctx, `
		INSERT INTO product_units (product_id, uom, unit_qty, stock_qty)
		VALUES ('00000000-0000-0000-0000-0000000000f2', 'CWT', 1, 110)`); err != nil {
		t.Fatal(err)
	}
	if _, err := conn.Exec(ctx, string(mustRead(t, downFile))); err == nil || !strings.Contains(err.Error(), "CWT") {
		t.Errorf("the down refuses while an extra unit set row exists, got %v", err)
	}
	if _, err := conn.Exec(ctx, `DELETE FROM product_units WHERE uom = 'CWT'`); err != nil {
		t.Fatal(err)
	}
	applyFile(t, conn, downFile)
	if exists := scalar[*string](t, conn, `SELECT to_regclass('units')::text`); exists != nil {
		t.Errorf("the down drops the catalogue, got %s", *exists)
	}
	if ty := scalar[string](t, conn, `SELECT atttypid::regtype::text FROM pg_attribute WHERE attrelid = 'products'::regclass AND attname = 'uom_primary'`); ty != "uom_type" {
		t.Errorf("the down restores the enum column, got %s", ty)
	}
	applyFile(t, conn, target)
	if n := scalar[int](t, conn, `SELECT count(*) FROM units`); n != 23 {
		t.Errorf("the up applies again after the down, got %d units", n)
	}
}

func mustRead(t *testing.T, path string) []byte {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return b
}
