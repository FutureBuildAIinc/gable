// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026, FutureBuild, Inc. and OpenLBM contributors

package largecash_test

// Migration 149 (CP-17b): the filing state per crossing and the statement
// state per payer and year. The migration goes up, down and up again on a
// scratch database, and the shape it leaves is the shape the worklist reads:
// one filing row per crossing under concurrency, the state tied to its own
// fields, and one statement per payer and year.

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/gablelbm/gable/internal/testutil"
	_ "github.com/jackc/pgx/v5/stdlib"
)

// scratch149 builds a scratch database with every migration before 149
// applied.
func scratch149(t *testing.T) *sql.DB {
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
	name := "gv1_m149_" + hex.EncodeToString(suffix)
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
	if err := applyMigrationsBefore149(db); err != nil {
		t.Fatal(err)
	}
	return db
}

// applyMigrationsBefore149 applies every migration file that sorts before
// migration 149, oldest first.
func applyMigrationsBefore149(db *sql.DB) error {
	files, err := filepath.Glob("../../migrations/*.sql")
	if err != nil || len(files) == 0 {
		return fmt.Errorf("no migrations found: %w", err)
	}
	sort.Strings(files)
	ctx := context.Background()
	for _, f := range files {
		base := filepath.Base(f)
		if strings.HasSuffix(base, "_down.sql") || strings.Compare(base, "149_") >= 0 {
			continue
		}
		sqlText, err := os.ReadFile(f)
		if err != nil {
			return err
		}
		if _, err := db.ExecContext(ctx, string(sqlText)); err != nil {
			return fmt.Errorf("applying %s: %w", base, err)
		}
	}
	return nil
}

// applyMigration149 applies the migration or its rollback file.
func applyMigration149(t *testing.T, db *sql.DB, pattern string) {
	t.Helper()
	files, err := filepath.Glob("../../migrations/" + pattern)
	if err != nil || len(files) != 1 {
		t.Fatalf("migration %s not found: %v", pattern, err)
	}
	sqlText, err := os.ReadFile(files[0])
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(context.Background(), string(sqlText)); err != nil {
		t.Fatalf("applying %s: %v", filepath.Base(files[0]), err)
	}
}

// TestMigration149GoesUpDownAndUp: the exit test's migration line. The
// migration applies, its rollback removes what it added, and a second apply
// leaves the same shape.
func TestMigration149GoesUpDownAndUp(t *testing.T) {
	db := scratch149(t)

	applyMigration149(t, db, "149_large_cash_worklist.sql")
	assertTables(t, db, true)

	applyMigration149(t, db, "down/149_large_cash_worklist_down.sql")
	assertTables(t, db, false)

	applyMigration149(t, db, "149_large_cash_worklist.sql")
	assertTables(t, db, true)
}

// assertTables answers whether the migration's two tables are there.
func assertTables(t *testing.T, db *sql.DB, want bool) {
	t.Helper()
	for _, table := range []string{"large_cash_filings", "large_cash_statements"} {
		var n int
		if err := db.QueryRowContext(context.Background(),
			`SELECT count(*) FROM information_schema.tables WHERE table_name = $1`, table).Scan(&n); err != nil {
			t.Fatal(err)
		}
		if want && n != 1 {
			t.Fatalf("the table %s is not there after the migration", table)
		}
		if !want && n != 0 {
			t.Fatalf("the table %s is still there after the rollback", table)
		}
	}
}

// TestMigration149KeepsOneFilingRowPerCrossing: the partial unique index is
// the house's standing index shape, and it is what stops a second filing row
// for one crossing under concurrency.
func TestMigration149KeepsOneFilingRowPerCrossing(t *testing.T) {
	db := scratch149(t)
	ctx := context.Background()
	applyMigration149(t, db, "149_large_cash_worklist.sql")

	// a branch and a crossing to hang the filing rows on
	var branch string
	if err := db.QueryRowContext(ctx,
		`INSERT INTO locations (id, type, code, name, country_code, default_tax_rate)
		 VALUES (gen_random_uuid(), 'BRANCH', 'M149', 'migration fixture', 'US', 0) RETURNING id`).Scan(&branch); err != nil {
		t.Fatal(err)
	}
	var event string
	if err := db.QueryRowContext(ctx,
		`INSERT INTO large_cash_events (id, payer_key, branch_id, rule_family, currency, amount_cents,
			total_cents, transaction_ids, tender_count, state)
		 VALUES (gen_random_uuid(), 'm149:fixture', $1, 'US_8300', 'USD', 1200000, 1200000, ARRAY[gen_random_uuid()], 1, 'reportable')
		 RETURNING id`, branch).Scan(&event); err != nil {
		t.Fatal(err)
	}
	// the first filing row lands
	if _, err := db.ExecContext(ctx,
		`INSERT INTO large_cash_filings (event_id, state, filed_date, acknowledgement_no, recorded_by)
		 VALUES ($1, 'filed', CURRENT_DATE, 'ACK-1', 'manager-a')`, event); err != nil {
		t.Fatal(err)
	}
	// and a second one for the same crossing does not
	if _, err := db.ExecContext(ctx,
		`INSERT INTO large_cash_filings (event_id, state, filed_date, acknowledgement_no, recorded_by)
		 VALUES ($1, 'filed', CURRENT_DATE, 'ACK-2', 'manager-b')`, event); err == nil {
		t.Fatal("a second filing row for one crossing was accepted")
	}
	// the state ties to its own fields: a filed row with no acknowledgement
	// is refused
	if _, err := db.ExecContext(ctx,
		`INSERT INTO large_cash_filings (event_id, state, filed_date, recorded_by)
		 VALUES ($1, 'filed', CURRENT_DATE, 'manager-c')`, event); err == nil {
		t.Fatal("a filing row with no acknowledgement number was accepted")
	}
	// and a voluntary filing carries no acknowledgement, because a voluntary
	// filing below the threshold is filed on paper
	var voluntary string
	if err := db.QueryRowContext(ctx,
		`INSERT INTO large_cash_events (id, payer_key, branch_id, rule_family, currency, amount_cents,
			total_cents, transaction_ids, tender_count, state)
		 VALUES (gen_random_uuid(), 'm149:voluntary', $1, 'US_8300', 'USD', 1200000, 1200000, ARRAY[gen_random_uuid()], 1, 'reportable')
		 RETURNING id`, branch).Scan(&voluntary); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx,
		`INSERT INTO large_cash_filings (event_id, state, filed_date, recorded_by)
		 VALUES ($1, 'voluntary', CURRENT_DATE, 'manager-a')`, voluntary); err != nil {
		t.Fatalf("a voluntary filing row was refused: %v", err)
	}
}

// TestMigration149KeepsOneStatementPerPayerAndYear: the statement's standing
// row is per payer and year, and its state ties to its own fields.
func TestMigration149KeepsOneStatementPerPayerAndYear(t *testing.T) {
	db := scratch149(t)
	ctx := context.Background()
	applyMigration149(t, db, "149_large_cash_worklist.sql")

	if _, err := db.ExecContext(ctx,
		`INSERT INTO large_cash_statements (payer_key, year, state, issued_date, issued_by)
		 VALUES ('payer-a', 2026, 'issued', CURRENT_DATE, 'manager-a')`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx,
		`INSERT INTO large_cash_statements (payer_key, year, state, issued_date, issued_by)
		 VALUES ('payer-a', 2026, 'issued', CURRENT_DATE, 'manager-b')`); err == nil {
		t.Fatal("a second statement for one payer and year was accepted")
	}
	if _, err := db.ExecContext(ctx,
		`INSERT INTO large_cash_statements (payer_key, year, state, issued_by)
		 VALUES ('payer-b', 2026, 'issued', 'manager-a')`); err == nil {
		t.Fatal("a statement with no issue date was accepted")
	}
	// another year for the same payer is its own statement
	if _, err := db.ExecContext(ctx,
		`INSERT INTO large_cash_statements (payer_key, year, state, issued_date, issued_by)
		 VALUES ('payer-a', 2027, 'issued', CURRENT_DATE, 'manager-a')`); err != nil {
		t.Fatalf("the next year's statement for one payer was refused: %v", err)
	}
}
