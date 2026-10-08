// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package product_test

// Migration 093 on rows that exist (recipe step 3, ADR 0006 section 8's
// C3-1 steps): the schema up to 092 is built in a scratch database of its
// own, legacy rows are written in the shape the base commit left them, then
// 093 is applied and each backfill is read back. The down narrows the
// quantity columns back and refuses a value DECIMAL(10,4) cannot hold; the
// up applies again after the down (idempotence on a fresh round trip).

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
	name := "gv1_c31mig_" + hex.EncodeToString(suffix)
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
		case strings.HasPrefix(base, "093_"):
			target = f
		case base < "093_":
			before = append(before, f)
		}
	}
	if target == "" {
		t.Fatal("migration 093 not found")
	}
	down = "../../migrations/down/093_catalog_pricing_wire_contract_down.sql"
	if _, err := os.Stat(down); err != nil {
		t.Fatal("migration 093 down not found")
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

func TestMigration093_BackfillsRowsThatExist(t *testing.T) {
	conn := scratchDB(t)
	before, target, downFile := migrationFiles(t)
	for _, f := range before {
		applyFile(t, conn, f)
	}
	ctx := context.Background()

	// Legacy rows in the base commit's shape: nullable created_ats, no
	// revisions, DECIMAL(10,4) reorder targets and an allocation.
	legacy := `
		INSERT INTO system_settings (key, value)
		VALUES ('default_branch_id', (SELECT id::text FROM locations WHERE type = 'BRANCH' LIMIT 1))
		ON CONFLICT (key) DO UPDATE SET value = EXCLUDED.value;
		INSERT INTO products (id, sku, description, uom_primary, base_price, reorder_point, reorder_qty, created_at, updated_at)
		VALUES ('00000000-0000-0000-0000-0000000000f1', 'MIG-2X4', '2x4x8', 'PCS', 5.25, 40, 200, NULL, now());
		INSERT INTO price_levels (id, name, multiplier, created_at, updated_at)
		VALUES ('00000000-0000-0000-0000-0000000000e1', 'Contractor', 0.92, NULL, now());
		INSERT INTO customers (id, name, account_number, primary_branch_id, created_at)
		VALUES ('00000000-0000-0000-0000-0000000000a1', 'Mig Co', 'MIG-1',
			(SELECT id FROM locations WHERE type = 'BRANCH' LIMIT 1), now());
		INSERT INTO customer_contracts (id, customer_id, product_id, contract_price, created_at, updated_at)
		VALUES ('00000000-0000-0000-0000-0000000000b1',
			'00000000-0000-0000-0000-0000000000a1',
			'00000000-0000-0000-0000-0000000000f1', 4.75, NULL, now());
		INSERT INTO pricing_rules (id, name, rule_type, discount_pct, min_quantity, created_at, updated_at)
		VALUES ('00000000-0000-0000-0000-0000000000d1', 'Mig break', 'QUANTITY_BREAK', 10, 20, now(), now());
		INSERT INTO locations (id, parent_id, path, type, code, created_at, updated_at)
		VALUES ('00000000-0000-0000-0000-0000000000c9', NULL, 'Mig Yard', 'BRANCH', 'MIG', NULL, now());
		INSERT INTO inventory (id, product_id, location, location_id, quantity, allocated)
		VALUES ('00000000-0000-0000-0000-000000000019',
			'00000000-0000-0000-0000-0000000000f1', 'MIG',
			'00000000-0000-0000-0000-0000000000c9', 120, 30);`
	if _, err := conn.Exec(ctx, legacy); err != nil {
		t.Fatalf("seed legacy rows: %v", err)
	}

	applyFile(t, conn, target)

	// 1: the fills. A product with a NULL created_at takes its updated_at;
	// every listed table ends NOT NULL with a revision.
	prodCreatedNull := scalar[int](t, conn, `SELECT COUNT(*) FROM products WHERE created_at IS NULL`)
	if prodCreatedNull != 0 {
		t.Errorf("%d products still hold a NULL created_at", prodCreatedNull)
	}
	for _, table := range []string{"products", "price_levels", "customer_contracts", "locations"} {
		if n := scalar[int](t, conn, fmt.Sprintf(`SELECT COUNT(*) FROM %s WHERE created_at IS NULL`, table)); n != 0 {
			t.Errorf("%s holds %d NULL created_at rows after the fill", table, n)
		}
	}
	// ADR 0006 section 8 gives price_levels its columns with C3-2A-pricing;
	// C3-1's revision set is the five tables below.
	for _, table := range []string{"products", "customer_contracts", "locations", "pricing_rules", "category_pricing_rules"} {
		if n := scalar[int](t, conn, fmt.Sprintf(`SELECT COUNT(*) FROM %s WHERE revision IS NULL OR revision < 1`, table)); n != 0 {
			t.Errorf("%s holds %d rows without a revision", table, n)
		}
	}
	for _, table := range []string{"pricing_rules", "category_pricing_rules"} {
		if n := scalar[int](t, conn, fmt.Sprintf(`SELECT COUNT(*) FROM %s WHERE revision IS NULL OR revision < 1`, table)); n != 0 {
			t.Errorf("%s holds %d rows without a revision", table, n)
		}
	}

	// 2: the widened columns hold a value the wire's Quantity accepts.
	if got := scalar[string](t, conn, `SELECT reorder_point::text FROM products WHERE id = '00000000-0000-0000-0000-0000000000f1'`); got != "40.0000" {
		t.Errorf("reorder_point = %s, want 40.0000 unchanged", got)
	}
	if got := scalar[string](t, conn, `SELECT allocated::text FROM inventory WHERE id = '00000000-0000-0000-0000-000000000019'`); got != "30.0000" {
		t.Errorf("allocated = %s, want 30.0000 unchanged", got)
	}
	// A million and one fits the widened column: the point of step 2.
	if _, err := conn.Exec(ctx, `UPDATE products SET reorder_point = 1000000.5, reorder_qty = 999999.9999 WHERE id = '00000000-0000-0000-0000-0000000000f1'`); err != nil {
		t.Fatalf("a seven figure reorder point no longer fits: %v", err)
	}

	// 3: the keyset indexes exist.
	for _, index := range []string{"idx_products_created_at_id", "idx_locations_created_at_id", "idx_pricing_rules_created_at_id", "idx_category_pricing_rules_created_at_id"} {
		if n := scalar[int](t, conn, `SELECT COUNT(*) FROM pg_indexes WHERE indexname = $1`, index); n != 1 {
			t.Errorf("index %s count = %d, want 1", index, n)
		}
	}

	// The helper view survived the alteration.
	if n := scalar[int](t, conn, `SELECT COUNT(*) FROM v_inventory_with_branch`); n != 1 {
		t.Errorf("v_inventory_with_branch answers %d rows, want 1", n)
	}

	// The down refuses while a row holds a value DECIMAL(10,4) cannot hold,
	// naming the product (the wide reorder point written above).
	downSQL, _ := os.ReadFile(downFile)
	if !strings.Contains(string(downSQL), "RAISE EXCEPTION") {
		t.Fatal("the down carries no refusal guard")
	}
	if _, err := conn.Exec(ctx, string(downSQL)); err == nil {
		t.Fatal("the down narrowed past a reorder point beyond DECIMAL(10,4)")
	} else if !strings.Contains(err.Error(), "MIG-2X4") {
		t.Fatalf("the refusal does not name the row: %v", err)
	}

	// Inside the bound, the down applies: revisions go, the columns narrow,
	// and the up applies again.
	if _, err := conn.Exec(ctx, `UPDATE products SET reorder_point = 40, reorder_qty = 200 WHERE id = '00000000-0000-0000-0000-0000000000f1'`); err != nil {
		t.Fatal(err)
	}
	applyFile(t, conn, downFile)
	for _, table := range []string{"products", "locations", "pricing_rules", "category_pricing_rules", "customer_contracts"} {
		if n := scalar[int](t, conn, `SELECT COUNT(*) FROM information_schema.columns WHERE table_name = $1 AND column_name = 'revision'`, table); n != 0 {
			t.Errorf("%s still carries its revision after the down", table)
		}
	}
	applyFile(t, conn, target)
	if n := scalar[int](t, conn, `SELECT COUNT(*) FROM products WHERE revision IS NULL`); n != 0 {
		t.Fatalf("the second up left %d rows without a revision", n)
	}
}
