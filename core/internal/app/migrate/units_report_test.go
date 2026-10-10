// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package migrate

import (
	"context"
	"database/sql"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/gablelbm/gable/internal/testutil"
	_ "github.com/jackc/pgx/v5/stdlib"
)

// TestUnitsReportNamesWhatTheMigrationWouldRefuse pins the pre flight
// report's one job (ADR 0006 section 8, A1): a stored unit value that would
// enter the catalogue as a new dealer unit is listed with its row count,
// and a value that would abort the migration is named, with a non zero
// exit so an operator's script sees it. The report answers the database as
// it stands before 099, so the fixture runs in a scratch database built to
// 098: the catalogue table does not exist, and the report still must not
// list the section 2.2 seed codes (099 inserts them) as new dealer units;
// only a code outside both the live catalogue and the seed is listed.
func TestUnitsReportNamesWhatTheMigrationWouldRefuse(t *testing.T) {
	scratch := pre099DB(t)
	ctx := context.Background()
	if _, err := scratch.ExecContext(ctx, `
		INSERT INTO quotes (id, number, branch_id, customer_id, state)
		VALUES ('44444444-4444-4444-4444-444444444444', 'Q-TEST-UNITS',
			(SELECT value::uuid FROM system_settings WHERE key = 'default_branch_id'),
			(SELECT id FROM customers LIMIT 1), 'DRAFT')`); err != nil {
		t.Fatalf("cannot write the report's fixture quote: %v", err)
	}
	if _, err := scratch.ExecContext(ctx, `
		INSERT INTO quote_lines (id, quote_id, sku, description, quantity, uom, price_uom, uom_qty, price_uom_qty, unit_price, line_total)
		VALUES ('55555555-5555-5555-5555-555555555555', '44444444-4444-4444-4444-444444444444',
			'REPORT', 'report fixture', 1, 'PCS', 'per skid', 1, 1, 1, 1),
		       ('66666666-6666-6666-6666-666666666666', '44444444-4444-4444-4444-444444444444',
			'REPORT', 'dealer unit fixture', 1, 'MBF', 'SKID', 1, 1, 1, 1)`); err != nil {
		t.Fatalf("cannot write the report's fixture lines: %v", err)
	}

	restore := captureStdout(t)
	code := unitsReport(scratch)
	out := restore()

	if code != 1 {
		t.Errorf("the report exits 1 when a value would abort the migration, got %d", code)
	}
	if !strings.Contains(out, `quote_lines.price_uom = "PER SKID" (1 rows)`) {
		t.Errorf("the report names the refusing value and its row count; got:\n%s", out)
	}
	if !strings.Contains(out, "new dealer units") || !strings.Contains(out, "SKID (1 rows") {
		t.Errorf("the report lists the dealer's own code as a new dealer unit; got:\n%s", out)
	}
	for _, seeded := range []string{"PCS (", "MBF (", "EA ("} {
		if strings.Contains(out, seeded) {
			t.Errorf("the report must not list the section 2.2 seed code %s as a new dealer unit before 099; got:\n%s",
				strings.TrimSuffix(seeded, " ("), out)
		}
	}
}

// pre099DB builds a scratch database with every migration before 099
// applied and the fixture rows the report reads.
func pre099DB(t *testing.T) *sql.DB {
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
	name := "gv1_unitsreport_" + hex.EncodeToString(suffix)
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

	files, err := filepath.Glob("../../../migrations/*.sql")
	if err != nil || len(files) == 0 {
		t.Fatalf("no migrations found: %v", err)
	}
	sort.Strings(files)
	for _, f := range files {
		if strings.Compare(filepath.Base(f), "099_") >= 0 {
			break
		}
		sqlText, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := db.ExecContext(ctx, string(sqlText)); err != nil {
			t.Fatalf("applying %s: %v", filepath.Base(f), err)
		}
	}
	return db
}

// captureStdout swaps the process's standard output for a pipe and answers
// the function that restores it and hands back what was printed.
func captureStdout(t *testing.T) func() string {
	t.Helper()
	real := os.Stdout
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stdout = w
	return func() string {
		os.Stdout = real
		w.Close()
		raw, _ := io.ReadAll(r)
		return string(raw)
	}
}
