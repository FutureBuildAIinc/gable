// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package pos_test

// Migration 127 (till movements, variance sign off, the branch day close) up,
// down and up again on a scratch database holding every migration: the down
// refuses while a drawer waits for sign off, reverses the schema it added and
// keeps an account a journal line uses, and the up applies cleanly after it.

import (
	"context"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
)

func mig127Files(t *testing.T) (all []string, up, down string) {
	t.Helper()
	files, err := filepath.Glob("../../migrations/*.sql")
	if err != nil || len(files) == 0 {
		t.Fatalf("no migrations found: %v", err)
	}
	sort.Strings(files)
	for _, f := range files {
		all = append(all, f)
		if strings.HasPrefix(filepath.Base(f), "127_") {
			up = f
		}
	}
	down = "../../migrations/down/127_till_movements_and_eod_down.sql"
	if up == "" {
		t.Fatal("migration 127 not found")
	}
	return all, up, down
}

// mig127Present counts what migration 127 leaves in the schema and the chart.
func mig127Present(t *testing.T, c *pgx.Conn) (tables, columns, accounts, settings int) {
	t.Helper()
	tables = mig105Scalar[int](t, c, `SELECT count(*) FROM information_schema.tables WHERE table_schema = 'public'
		AND table_name IN ('till_movements', 'till_variance_policy', 'till_day_closes')`)
	columns = mig105Scalar[int](t, c, `SELECT count(*) FROM information_schema.columns WHERE table_name = 'till_sessions'
		AND column_name IN ('closed_by', 'variance_threshold_cents', 'signoff_by', 'signoff_reason', 'signed_off_at')`)
	accounts = mig105Scalar[int](t, c, `SELECT count(*) FROM gl_accounts WHERE code IN ('1015', '4090')`)
	settings = mig105Scalar[int](t, c, `SELECT count(*) FROM system_settings WHERE key = 'till.variance_threshold_cents'`)
	return tables, columns, accounts, settings
}

func TestMigration127_DownRefusesWhileADrawerWaitsAndReversesCleanly(t *testing.T) {
	c, _ := mig105ScratchDB(t)
	all, up, down := mig127Files(t)
	for _, f := range all {
		mig105Apply(t, c, f)
	}
	ctx := context.Background()
	if tables, columns, accounts, settings := mig127Present(t, c); tables != 3 || columns != 5 || accounts != 2 || settings != 1 {
		t.Fatalf("after the up: %d tables, %d columns, %d accounts, %d settings, want 3, 5, 2, 1", tables, columns, accounts, settings)
	}
	downSQL := mustRead(t, down)

	// a drawer waiting for sign off holds the down, naming the count, changing nothing
	if _, err := c.Exec(ctx, `INSERT INTO till_sessions (id, register_id, branch_id, cashier_id, status, opening_float, closed_at)
		VALUES ('00000000-0000-0000-0000-0000000000f1', 'REG-01', (SELECT id FROM locations WHERE type = 'BRANCH' LIMIT 1),
		        '00000000-0000-0000-0000-0000000000f2', 'PENDING_SIGNOFF', 10, NOW())`); err != nil {
		t.Fatalf("seed the waiting drawer: %v", err)
	}
	_, err := c.Exec(ctx, downSQL)
	if err == nil {
		t.Fatal("the down ran while a drawer waited for sign off")
	}
	if !strings.Contains(err.Error(), "1 till session") || !strings.Contains(err.Error(), "sign off") {
		t.Errorf("the down's refusal = %v, want it to name the count and the sign off", err)
	}
	if tables, columns, accounts, settings := mig127Present(t, c); tables != 3 || columns != 5 || accounts != 2 || settings != 1 {
		t.Errorf("the refused down changed the schema: %d tables, %d columns, %d accounts, %d settings", tables, columns, accounts, settings)
	}

	// settled, the down reverses everything the up added
	if _, err := c.Exec(ctx, `UPDATE till_sessions SET status = 'CLOSED' WHERE id = '00000000-0000-0000-0000-0000000000f1'`); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Exec(ctx, downSQL); err != nil {
		t.Fatalf("the down refused with nothing waiting: %v", err)
	}
	if tables, columns, accounts, settings := mig127Present(t, c); tables != 0 || columns != 0 || accounts != 0 || settings != 0 {
		t.Errorf("after the down: %d tables, %d columns, %d accounts, %d settings, want none", tables, columns, accounts, settings)
	}
	if n := mig105Scalar[int](t, c, `SELECT count(*) FROM till_sessions WHERE id = '00000000-0000-0000-0000-0000000000f1'`); n != 1 {
		t.Errorf("the down lost the session row")
	}

	// the up applies cleanly again, and a second apply is a no-op
	mig105Apply(t, c, up)
	mig105Apply(t, c, up)
	if tables, columns, accounts, settings := mig127Present(t, c); tables != 3 || columns != 5 || accounts != 2 || settings != 1 {
		t.Errorf("after the second up: %d tables, %d columns, %d accounts, %d settings, want 3, 5, 2, 1", tables, columns, accounts, settings)
	}

	// an account a journal line uses stays through the down
	if _, err := c.Exec(ctx, `
		INSERT INTO gl_journal_entries (id, entry_date, memo, source, status, posted_by)
		VALUES ('00000000-0000-0000-0000-0000000000e1', CURRENT_DATE, 'mig127', 'MANUAL', 'POSTED', 'test');
		INSERT INTO gl_journal_lines (journal_entry_id, account_id, debit, credit)
		VALUES ('00000000-0000-0000-0000-0000000000e1', (SELECT id FROM gl_accounts WHERE code = '1015'), 1, 0),
		       ('00000000-0000-0000-0000-0000000000e1', (SELECT id FROM gl_accounts WHERE code = '1010'), 0, 1);`); err != nil {
		t.Fatalf("seed the journal line: %v", err)
	}
	if _, err := c.Exec(ctx, downSQL); err != nil {
		t.Fatalf("the down with a used account: %v", err)
	}
	if n := mig105Scalar[int](t, c, `SELECT count(*) FROM gl_accounts WHERE code = '1015'`); n != 1 {
		t.Errorf("the down removed an account a journal line uses")
	}
	if n := mig105Scalar[int](t, c, `SELECT count(*) FROM gl_accounts WHERE code = '4090'`); n != 0 {
		t.Errorf("the down kept the unused account 4090")
	}
	mig105Apply(t, c, up)
	if n := mig105Scalar[int](t, c, `SELECT count(*) FROM gl_accounts WHERE code IN ('1015', '4090')`); n != 2 {
		t.Errorf("the up after the down holds %d of its two accounts, want 2", n)
	}
}
