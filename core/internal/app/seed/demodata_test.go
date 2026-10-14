// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package seed

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"strings"
	"testing"

	"github.com/gablelbm/gable/internal/testutil"
)

// Tests for the demo data import (DEMO_SEED_IMPORT).
//
// TestDemoImport seeds a scratch database of its own, never the shared test
// database: it creates the database, applies every migration and runs the
// seed with the import, each through its command in a subprocess as an
// operator runs them, then checks what the import promises:
//
//  0. Every catalogue row and every customer row lands (a refused row is
//     only logged by the seed, so the count is the check).
//  1. Margin: every imported product has a nonzero target_margin and a
//     nonzero commission_rate.
//  2. Domain: no vendor, staff or customer row names the dealer on its legacy
//     "@gablelumber.ca" domain; the seed writes the reserved ".example" one.
//  3. Revenue trend: payments spread over many days, at each of the three
//     yards, so the dashboard's trend has more than one period.
//  4. Idempotency: a second seed run leaves every reference data row count
//     unchanged.

// TestDemoImportRequested pins the opt in: the import runs only when
// DEMO_SEED_IMPORT asks for it, so the default seed (the one the
// characterization goldens record) never carries it.
func TestDemoImportRequested(t *testing.T) {
	for _, tc := range []struct {
		value string
		want  bool
	}{
		{"", false},
		{"0", false},
		{"no", false},
		{"1", true},
		{"true", true},
		{" Yes ", true},
		{"on", true},
	} {
		t.Setenv(demoImportEnv, tc.value)
		if got := demoImportRequested(); got != tc.want {
			t.Errorf("%s=%q: demoImportRequested() = %v, want %v", demoImportEnv, tc.value, got, tc.want)
		}
	}
	// The history is on with the import unless it is turned off.
	for _, tc := range []struct {
		value string
		want  bool
	}{
		{"", true},
		{"1", true},
		{"0", false},
		{" Off ", false},
		{"false", false},
	} {
		t.Setenv(demoHistoryEnv, tc.value)
		if got := demoHistoryRequested(); got != tc.want {
			t.Errorf("%s=%q: demoHistoryRequested() = %v, want %v", demoHistoryEnv, tc.value, got, tc.want)
		}
	}
}

func TestDemoImport(t *testing.T) {
	if testing.Short() {
		t.Skip("seeds a scratch database of its own; skipped under -short")
	}
	db, dbURL := demoImportDB(t)
	runCommand(t, dbURL, "./cmd/migrate")
	runCommand(t, dbURL, "./cmd/seed")
	first := snapshotCounts(t, db)
	if first.products == 0 || first.customers == 0 || first.vendors == 0 {
		t.Fatalf("the import wrote nothing: %+v", first)
	}

	t.Run("every catalogue and customer row lands", func(t *testing.T) {
		for _, c := range []struct {
			name, query, key string
			data             []byte
		}{
			{"catalogue", `SELECT COUNT(*) FROM products WHERE sku = ANY($1)`, "sku", catalogData},
			{"customers", `SELECT COUNT(*) FROM customers WHERE account_number = ANY($1)`, "account_number", customersData},
		} {
			keys := jsonlKeys(t, c.data, c.key)
			var n int
			if err := db.QueryRow(c.query, keys).Scan(&n); err != nil {
				t.Fatalf("%s: %v", c.name, err)
			}
			if n != len(keys) {
				t.Errorf("%s: %d of %d rows landed (the seed logs each refused row)", c.name, n, len(keys))
			}
		}
	})

	t.Run("every imported product has a margin", func(t *testing.T) {
		rows, err := db.Query(`
			SELECT sku, target_margin, commission_rate
			FROM products
			WHERE sku NOT LIKE 'LUM-%'
			  AND sku NOT LIKE 'PLY-%'
			  AND sku NOT LIKE 'OSB-%'
			  AND sku NOT LIKE 'DW-%'
			  AND sku NOT LIKE 'NAIL-%'
			  AND sku NOT LIKE 'SCR-%'
			  AND sku NOT LIKE 'HANGER-%'
			  AND sku NOT LIKE 'TIE-%'
			  AND sku NOT LIKE 'SIMP-%'
			  AND sku NOT LIKE 'RF-%'
			  AND sku NOT LIKE 'INS-%'
			  AND sku NOT LIKE 'DR-%'
			  AND sku NOT LIKE 'MLD-%'
			  AND sku NOT LIKE 'CORN%'
			  AND sku NOT LIKE 'LUMB%'`)
		if err != nil {
			t.Fatalf("query imported products: %v", err)
		}
		defer rows.Close()
		var bad []string
		for rows.Next() {
			var sku string
			var margin, commission float64
			if err := rows.Scan(&sku, &margin, &commission); err != nil {
				t.Fatalf("scan: %v", err)
			}
			if margin <= 0 {
				bad = append(bad, sku+" margin=0")
			}
			if commission <= 0 {
				bad = append(bad, sku+" commission=0")
			}
		}
		if err := rows.Err(); err != nil {
			t.Fatal(err)
		}
		if len(bad) > 0 {
			t.Fatalf("%d imported products miss a margin or commission: %s", len(bad), strings.Join(bad, ", "))
		}
	})

	t.Run("no row names the legacy dealer domain", func(t *testing.T) {
		for _, c := range []struct{ table, column string }{
			{"vendors", "name"},
			{"vendors", "contact_email"},
			{"staff", "email"},
			{"customers", "email"},
			{"sales_team", "email"},
		} {
			var n int
			q := fmt.Sprintf(`SELECT COUNT(*) FROM %s WHERE %s ILIKE '%%gablelumber.ca%%'`, c.table, c.column)
			if err := db.QueryRow(q).Scan(&n); err != nil {
				t.Fatalf("%s.%s: %v", c.table, c.column, err)
			}
			if n > 0 {
				t.Errorf("%s.%s: %d rows carry the legacy dealer domain", c.table, c.column, n)
			}
		}
	})

	t.Run("revenue trends across many days at each yard", func(t *testing.T) {
		var days int
		if err := db.QueryRow(`
			SELECT COUNT(DISTINCT DATE(p.created_at))
			FROM payments p
			WHERE p.created_at >= NOW() - interval '130 days'
			  AND p.amount > 0`).Scan(&days); err != nil {
			t.Fatalf("revenue trend count: %v", err)
		}
		if days < 30 {
			t.Fatalf("revenue trend has %d distinct days, want at least 30", days)
		}
		for _, yard := range []string{"KEL-MAIN", "WK", "LK"} {
			var yardDays int
			if err := db.QueryRow(`
				SELECT COUNT(DISTINCT DATE(p.created_at))
				FROM payments p
				JOIN orders o ON o.id = p.order_id
				JOIN locations l ON l.id = o.branch_id
				WHERE l.code = $1
				  AND p.created_at >= NOW() - interval '130 days'`, yard).Scan(&yardDays); err != nil {
				t.Fatalf("revenue trend %s: %v", yard, err)
			}
			if yardDays < 5 {
				t.Errorf("yard %s has %d days of revenue in the window, want at least 5", yard, yardDays)
			}
		}
	})

	t.Run("a second run changes no reference data", func(t *testing.T) {
		runCommand(t, dbURL, "./cmd/seed")
		if second := snapshotCounts(t, db); second != first {
			t.Errorf("reference data moved on a second run: first %+v, second %+v", first, second)
		}
	})
}

// demoImportDB creates a scratch database beside the shared test database
// and drops it when the test ends. It answers a handle and the URL the
// commands run against.
func demoImportDB(t *testing.T) (*sql.DB, string) {
	t.Helper()
	base, err := url.Parse(testutil.RequireDB(t).Pool.Config().ConnString())
	if err != nil {
		t.Fatal(err)
	}
	admin, err := sql.Open("pgx", base.String())
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	suffix := make([]byte, 6)
	if _, err := rand.Read(suffix); err != nil {
		t.Fatal(err)
	}
	name := "seed_demoimport_" + hex.EncodeToString(suffix)
	if _, err := admin.ExecContext(ctx, fmt.Sprintf(`CREATE DATABASE %q`, name)); err != nil {
		admin.Close()
		t.Skipf("cannot create a scratch database: %v", err)
	}
	t.Cleanup(func() {
		_, _ = admin.ExecContext(context.Background(), fmt.Sprintf(`DROP DATABASE %q WITH (FORCE)`, name))
		admin.Close()
	})

	scratch := *base
	scratch.Path = "/" + name
	db, err := sql.Open("pgx", scratch.String())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	return db, scratch.String()
}

// runCommand runs one of the repository's commands (by its package path
// under core/, where the commands run so migrate finds migrations/) on the
// scratch database with the demo seed and its import asked for.
func runCommand(t *testing.T, dbURL, pkg string) {
	t.Helper()
	cmd := exec.Command("go", "run", pkg)
	cmd.Dir = "../../.."
	cmd.Env = append(os.Environ(),
		"DATABASE_URL="+dbURL,
		demoSeedEnv+"=1",
		demoImportEnv+"=1",
	)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("go run %s: %v\n%s", pkg, err, out)
	}
}

// jsonlKeys answers one string field of every row of an embedded jsonl file.
func jsonlKeys(t *testing.T, data []byte, field string) []string {
	t.Helper()
	var keys []string
	for _, line := range strings.Split(string(data), "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		var row map[string]any
		if err := json.Unmarshal([]byte(line), &row); err != nil {
			t.Fatalf("%s: %v", field, err)
		}
		key, _ := row[field].(string)
		if key == "" {
			t.Fatalf("a row has no %s: %s", field, line)
		}
		keys = append(keys, key)
	}
	return keys
}

// importCounts are the reference data row counts a second run must keep.
type importCounts struct{ products, vendors, customers, categories, staff int }

func snapshotCounts(t *testing.T, db *sql.DB) importCounts {
	t.Helper()
	var c importCounts
	for _, q := range []struct {
		dst   *int
		query string
	}{
		{&c.products, `SELECT COUNT(*) FROM products`},
		{&c.vendors, `SELECT COUNT(*) FROM vendors`},
		{&c.customers, `SELECT COUNT(*) FROM customers`},
		{&c.categories, `SELECT COUNT(*) FROM product_categories WHERE slug <> 'general'`},
		{&c.staff, `SELECT COUNT(*) FROM staff`},
	} {
		if err := db.QueryRow(q.query).Scan(q.dst); err != nil {
			t.Fatalf("%s: %v", q.query, err)
		}
	}
	return c
}
