// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package gl_test

// Migration 133 against a database already upgraded past 125: the runner
// keys schema_migrations by file name and never rereads an applied file,
// so the 1095 Inter-Branch Clearing account and the historic cross-branch
// settlement backfill must arrive as a new migration, not as an edit of
// 125. The database here has 125 applied and recorded, holds a legacy
// two line cross-branch payment application entry stamped whole to the
// invoice's branch, and an unposted cross-branch credit memo application
// with no entry at all; running the pending migrations applies exactly 133,
// which seeds 1095, backfills the four leg transfers, links the memo
// application's entry and brings both branches' AR tie out to zero, and
// the down migration reverses all of it and drops 1095 while unused.

import (
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/gablelbm/gable/internal/gl"
	"github.com/gablelbm/gable/pkg/database"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// runPendingMigrations mirrors internal/app/migrate's runner on one
// connection: every migrations/*.sql not yet recorded in schema_migrations
// runs inside its own transaction and is then recorded, so an applied file
// is skipped by name whatever its content. skipPrefixes holds the file
// name prefixes to pretend do not exist yet, and the applied names are
// answered in run order.
func runPendingMigrations(t *testing.T, conn *pgx.Conn, skipPrefixes ...string) []string {
	t.Helper()
	ctx := context.Background()
	if _, err := conn.Exec(ctx, `CREATE TABLE IF NOT EXISTS schema_migrations (
		version TEXT PRIMARY KEY,
		applied_at TIMESTAMP WITH TIME ZONE DEFAULT NOW())`); err != nil {
		t.Fatalf("create schema_migrations: %v", err)
	}
	files, err := filepath.Glob("../../migrations/*.sql")
	if err != nil {
		t.Fatal(err)
	}
	sort.Strings(files)
	var applied []string
	for _, f := range files {
		base := filepath.Base(f)
		if strings.HasSuffix(base, "_down.sql") {
			continue
		}
		skip := false
		for _, p := range skipPrefixes {
			if strings.HasPrefix(base, p) {
				skip = true
			}
		}
		if skip {
			continue
		}
		var exists bool
		if err := conn.QueryRow(ctx,
			`SELECT EXISTS(SELECT 1 FROM schema_migrations WHERE version = $1)`, base).Scan(&exists); err != nil {
			t.Fatalf("check %s: %v", base, err)
		}
		if exists {
			continue
		}
		sql, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		tx, err := conn.Begin(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := tx.Exec(ctx, string(sql)); err != nil {
			_ = tx.Rollback(ctx)
			t.Fatalf("apply %s: %v", base, err)
		}
		if _, err := tx.Exec(ctx, `INSERT INTO schema_migrations (version) VALUES ($1)`, base); err != nil {
			_ = tx.Rollback(ctx)
			t.Fatalf("record %s: %v", base, err)
		}
		if err := tx.Commit(ctx); err != nil {
			t.Fatal(err)
		}
		applied = append(applied, base)
	}
	return applied
}

// TestMigration133SeedsInterBranchClearingOnUpgradedDatabase: a database
// with 125 already applied and recorded never rereads 125, so migration 133
// alone must seed 1095, backfill the historic cross-branch payment and
// credit memo applications, and tie both branches' AR out; the down
// migration reverses the backfill and removes 1095 when nothing uses it.
func TestMigration133SeedsInterBranchClearingOnUpgradedDatabase(t *testing.T) {
	scratchURL := sweepScratchDatabase(t, "gv1_m133mig")
	base, err := pgx.Connect(context.Background(), scratchURL)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { base.Close(context.Background()) })
	ctx := context.Background()

	// The database a demo already upgraded past 125 holds: every migration
	// through 131 applied and recorded, 133 not yet present.
	if got := runPendingMigrations(t, base, "133_"); len(got) == 0 {
		t.Fatal("the first migration run applied nothing")
	}

	var def, other uuid.UUID
	if err := base.QueryRow(ctx, `SELECT value::uuid FROM system_settings WHERE key = 'default_branch_id'`).Scan(&def); err != nil {
		t.Fatal(err)
	}
	other = uuid.New()
	if _, err := base.Exec(ctx, `INSERT INTO locations (id, type, code, name) VALUES ($1, 'BRANCH', 'M133', 'M133 Second')`, other); err != nil {
		t.Fatal(err)
	}

	var cust uuid.UUID
	if err := base.QueryRow(ctx, `INSERT INTO customers (name, account_number, primary_branch_id) VALUES ('m133 mig', $1, $2) RETURNING id`, "M133-"+uuid.NewString()[:8], def).Scan(&cust); err != nil {
		t.Fatal(err)
	}

	// The historic cross-branch documents a database upgraded past 125
	// holds: a memo of 100.00 on the default branch applied to an invoice
	// of 100.00 on the second branch (no entry, no link), and a payment of
	// 150.00 on the default branch applied to an invoice of 150.00 on the
	// second branch (a legacy two line entry stamped whole to the invoice's
	// branch by 125's source document backfill).
	invCM := uuid.New()
	invPay := uuid.New()
	cm := uuid.New()
	pay := uuid.New()
	stmts := []struct {
		sql  string
		args []any
	}{
		{`INSERT INTO invoices (id, customer_id, branch_id, number, status, total_amount, amount_open, currency) VALUES ($1, $2, $3, 'M133-I1', 'UNPAID', 100.00, 0, 'USD')`, []any{invCM, cust, other}},
		{`INSERT INTO invoices (id, customer_id, branch_id, number, status, total_amount, amount_open, currency) VALUES ($1, $2, $3, 'M133-I2', 'UNPAID', 150.00, 0, 'USD')`, []any{invPay, cust, other}},
		{`INSERT INTO credit_memos (id, customer_id, branch_id, number, amount, subtotal, tax_amount, total_amount, amount_open, memo_date, status, reason, reason_code, currency) VALUES ($1, $2, $3, 'M133-CM', -100.00, -100.00, 0, -100.00, 0, CURRENT_DATE, 'OPEN', 'm133', 'OTHER', 'USD')`, []any{cm, cust, def}},
		{`INSERT INTO payments (id, customer_id, branch_id, amount, amount_unapplied, method, received_on, currency) VALUES ($1, $2, $3, 150.00, 0, 'CASH', CURRENT_DATE, 'USD')`, []any{pay, cust, def}},
	}
	for _, s := range stmts {
		if _, err := base.Exec(ctx, s.sql, s.args...); err != nil {
			t.Fatalf("seed: %v", err)
		}
	}

	cmApp := uuid.New()
	payApp := uuid.New()
	if _, err := base.Exec(ctx, `INSERT INTO ar_applications (id, customer_id, currency, kind, credit_memo_id, invoice_id, amount, applied_on, applied_by, act_id)
		VALUES ($1, $2, 'USD', 'CREDIT_MEMO', $3, $4, 100.00, CURRENT_DATE, 't', $5)`, cmApp, cust, cm, invCM, uuid.New()); err != nil {
		t.Fatal(err)
	}
	if _, err := base.Exec(ctx, `INSERT INTO ar_applications (id, customer_id, currency, kind, payment_id, invoice_id, amount, applied_on, applied_by, act_id)
		VALUES ($1, $2, 'USD', 'PAYMENT', $3, $4, 150.00, CURRENT_DATE, 't', $5)`, payApp, cust, pay, invPay, uuid.New()); err != nil {
		t.Fatal(err)
	}

	// The entries such a database holds: each source's own two line entry
	// stamped by 125's backfill, the payment application's DR 2200 / CR
	// 1020 entry whole on the invoice's branch, and no transfer entry for
	// the memo application.
	entryDesc := func(source string, ref uuid.UUID, memo, desc string, reverses *uuid.UUID, lines [][4]any) uuid.UUID {
		t.Helper()
		id := uuid.New()
		if _, err := base.Exec(ctx, `INSERT INTO gl_journal_entries (id, entry_date, memo, source, source_ref_id, branch_id, status, posted_by, currency, reverses_entry_id)
			VALUES ($1, CURRENT_DATE, $2, $3, $4, $5, 'POSTED', 't', 'USD', $6)`, id, memo, source, ref, other, reverses); err != nil {
			t.Fatalf("seed entry %s: %v", source, err)
		}
		for _, l := range lines {
			var lineID uuid.UUID
			if err := base.QueryRow(ctx, `SELECT id FROM gl_accounts WHERE code = $1`, l[0]).Scan(&lineID); err != nil {
				t.Fatal(err)
			}
			if _, err := base.Exec(ctx, `INSERT INTO gl_journal_lines (id, journal_entry_id, account_id, description, debit, credit, branch_id)
				VALUES ($1, $2, $3, $4, $5, $6, $7)`, uuid.New(), id, lineID, desc, l[1], l[2], l[3]); err != nil {
				t.Fatalf("seed line: %v", err)
			}
		}
		return id
	}
	entry := func(source string, ref uuid.UUID, lines [][4]any) uuid.UUID {
		t.Helper()
		return entryDesc(source, ref, "m133 mig", "m133", nil, lines)
	}
	_ = entry("INVOICE", invCM, [][4]any{{"1020", 100.00, 0, other}, {"4010", 0, 100.00, other}})
	_ = entry("INVOICE", invPay, [][4]any{{"1020", 150.00, 0, other}, {"4010", 0, 150.00, other}})
	_ = entry("CREDIT_MEMO", cm, [][4]any{{"4010", 100.00, 0, other}, {"1020", 0, 100.00, other}})
	payEntry := entry("PAYMENT", payApp, [][4]any{{"2200", 150.00, 0, other}, {"1020", 0, 150.00, other}})

	// The four leg cross-branch entries the code between 125 and 133 wrote:
	// cross-branch payment and credit memo applications and their reversals
	// carried their inter-branch transfer legs on 1010 Cash with the three
	// settlement descriptions, each balanced per branch (the payment pair's
	// 1020 legs and subledger rows net to zero, the memo entry's 1020 legs
	// net to zero on their own), so migration 133 must move those 1010 legs
	// to 1095 by account and description: its two line and link guards skip
	// these four line entries on their own.
	ibPayRef := uuid.New()
	ibEntry := entryDesc("PAYMENT", ibPayRef, "m133 mig", "Inter-branch cash settlement", nil, [][4]any{
		{"2200", 150.00, 0, def}, {"1010", 0, 150.00, def},
		{"1010", 150.00, 0, other}, {"1020", 0, 150.00, other},
	})
	_ = entryDesc("CREDIT_MEMO", uuid.New(), "m133 mig", "Inter-branch AR settlement", nil, [][4]any{
		{"1020", 100.00, 0, def}, {"1010", 0, 100.00, def},
		{"1010", 100.00, 0, other}, {"1020", 0, 100.00, other},
	})
	_ = entryDesc("REVERSAL", ibPayRef, "m133 mig", "Inter-branch cash settlement reversal", &ibEntry, [][4]any{
		{"1020", 150.00, 0, other}, {"1010", 0, 150.00, other},
		{"1010", 150.00, 0, def}, {"2200", 0, 150.00, def},
	})

	// The customer transactions the subledger reads.
	tx := func(amount int64, ref uuid.UUID, typ string) {
		t.Helper()
		if _, err := base.Exec(ctx, `INSERT INTO customer_transactions (customer_id, amount, balance_after, reference_id, description, type, currency, source_kind)
			VALUES ($1, $2, 0, $3, 'm133', $4, 'USD', 'core')`, cust, amount, ref, typ); err != nil {
			t.Fatal(err)
		}
	}
	tx(10000, invCM, "INVOICE")
	tx(15000, invPay, "INVOICE")
	tx(-10000, cm, "CREDIT_MEMO")
	tx(-15000, payApp, "PAYMENT")
	// The inter-branch payment entry and its reversal net to zero on 1020
	// together; their subledger rows mirror them so the tie out holds.
	tx(-15000, ibPayRef, "PAYMENT")
	tx(15000, ibPayRef, "PAYMENT")

	// The upgraded database before 133 exists holds no clearing account.
	var seeded int
	if err := base.QueryRow(ctx, `SELECT count(*) FROM gl_accounts WHERE code = '1095'`).Scan(&seeded); err != nil {
		t.Fatal(err)
	}
	if seeded != 0 {
		t.Fatalf("the fixture database already holds %d 1095 accounts, want none", seeded)
	}

	// Running the pending migrations applies exactly 133 and skips 125 by
	// name: the backfill must come from the new migration.
	got := runPendingMigrations(t, base)
	if len(got) != 1 || got[0] != "133_gl_interbranch_clearing_and_ar_tieout.sql" {
		t.Fatalf("the upgrade run applied %v, want exactly [133_gl_interbranch_clearing_and_ar_tieout.sql]", got)
	}
	var skipped bool
	if err := base.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM schema_migrations WHERE version = '125_gl_branch_and_finstmt.sql')`).Scan(&skipped); err != nil {
		t.Fatal(err)
	}
	if !skipped {
		t.Fatal("125 was not recorded as applied before the upgrade run")
	}

	if err := base.QueryRow(ctx, `SELECT count(*) FROM gl_accounts WHERE code = '1095'`).Scan(&seeded); err != nil {
		t.Fatal(err)
	}
	if seeded != 1 {
		t.Fatalf("migration 133 seeded %d 1095 accounts, want 1", seeded)
	}

	// The settlement legs the code between 125 and 133 wrote on 1010 Cash
	// moved to 1095 Inter-Branch Clearing by account and description.
	legsOn := func(code, descList string) int {
		t.Helper()
		var n int
		if err := base.QueryRow(ctx, `SELECT count(*) FROM gl_journal_lines l
			JOIN gl_accounts a ON a.id = l.account_id
			WHERE a.code = $1 AND l.description IN (`+descList+`)`, code).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	settlementDescs := `'Inter-branch cash settlement', 'Inter-branch AR settlement', 'Inter-branch cash settlement reversal'`
	if got := legsOn("1010", settlementDescs); got != 0 {
		t.Errorf("%d settlement legs remain on 1010 Cash after migration 133, want 0", got)
	}
	if got := legsOn("1095", settlementDescs); got != 6 {
		t.Errorf("%d settlement legs on 1095 after migration 133, want the 6 moved legs", got)
	}

	legsOf := func(entryID uuid.UUID) map[string]map[uuid.UUID][2]float64 {
		t.Helper()
		out := map[string]map[uuid.UUID][2]float64{}
		rows, err := base.Query(ctx, `SELECT a.code, l.branch_id, l.debit, l.credit
			FROM gl_journal_lines l JOIN gl_accounts a ON a.id = l.account_id
			WHERE l.journal_entry_id = $1 ORDER BY a.code`, entryID)
		if err != nil {
			t.Fatal(err)
		}
		defer rows.Close()
		for rows.Next() {
			var code string
			var branch uuid.UUID
			var dr, cr float64
			if err := rows.Scan(&code, &branch, &dr, &cr); err != nil {
				t.Fatal(err)
			}
			if out[code] == nil {
				out[code] = map[uuid.UUID][2]float64{}
			}
			out[code][branch] = [2]float64{dr, cr}
		}
		if err := rows.Err(); err != nil {
			t.Fatal(err)
		}
		return out
	}

	// The credit memo application gained its balanced four leg transfer
	// entry and the link that ties subledger and ledger together.
	var cmEntry uuid.UUID
	if err := base.QueryRow(ctx, `SELECT gl_entry_id FROM ar_applications WHERE id = $1`, cmApp).Scan(&cmEntry); err != nil {
		t.Fatalf("credit memo application has no backfilled entry: %v", err)
	}
	cmLegs := legsOf(cmEntry)
	wantCMLegs := []struct {
		code   string
		branch uuid.UUID
		dr, cr float64
	}{
		{"1020", def, 100.00, 0}, {"1095", def, 0, 100.00},
		{"1095", other, 100.00, 0}, {"1020", other, 0, 100.00},
	}
	for _, w := range wantCMLegs {
		legs, ok := cmLegs[w.code][w.branch]
		if !ok {
			t.Errorf("memo transfer entry has no %s leg on branch %v", w.code, w.branch)
			continue
		}
		if legs[0] != w.dr || legs[1] != w.cr {
			t.Errorf("memo transfer entry %s leg on branch %v = DR %v CR %v, want DR %v CR %v", w.code, w.branch, legs[0], legs[1], w.dr, w.cr)
		}
	}

	// The payment application entry grew its two 1095 legs and moved its
	// 2200 leg to the payment's branch.
	payLegs := legsOf(payEntry)
	if legs, ok := payLegs["2200"][def]; !ok || legs[0] != 150.00 {
		t.Errorf("payment entry 2200 leg on the payment branch = %v, want DR 150.00", payLegs["2200"][def])
	}
	if legs, ok := payLegs["1095"][def]; !ok || legs[0] != 0 || legs[1] != 150.00 {
		t.Errorf("payment entry 1095 leg on the payment branch = %v, want CR 150.00", payLegs["1095"][def])
	}
	if legs, ok := payLegs["1095"][other]; !ok || legs[0] != 150.00 {
		t.Errorf("payment entry 1095 leg on the invoice branch = %v, want DR 150.00", payLegs["1095"][other])
	}

	// Every entry balances per branch.
	rows, err := base.Query(ctx, `SELECT e.id, l.branch_id, SUM(l.debit), SUM(l.credit)
		FROM gl_journal_entries e JOIN gl_journal_lines l ON l.journal_entry_id = e.id
		GROUP BY e.id, l.branch_id HAVING SUM(l.debit) <> SUM(l.credit)`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	for rows.Next() {
		var id, branch uuid.UUID
		var dr, cr float64
		if err := rows.Scan(&id, &branch, &dr, &cr); err != nil {
			t.Fatal(err)
		}
		t.Errorf("entry %s branch %v debits %v != credits %v after the backfill", id, branch, dr, cr)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}

	// Both branches tie out: control 1020 balance against subledger.
	scratchDB, err := database.Connect(scratchURL)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { scratchDB.Pool.Close() })
	svc := gl.NewService(gl.NewRepository(scratchDB), nil, slog.Default())
	for _, b := range []uuid.UUID{def, other} {
		tie, err := svc.TieOut(ctx, time.Time{}, "USD", &b)
		if err != nil {
			t.Fatal(err)
		}
		if tie.AR.DifferenceCents != 0 {
			t.Errorf("branch %v AR tie out after migration 133 = %+v, want DifferenceCents 0", b, tie.AR)
		}
	}

	// The down migration reverses the backfill and removes 1095 while
	// nothing uses it.
	down, err := os.ReadFile("../../migrations/down/133_gl_interbranch_clearing_and_ar_tieout_down.sql")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := base.Exec(ctx, string(down)); err != nil {
		t.Fatalf("apply down/133: %v", err)
	}
	var linked int
	if err := base.QueryRow(ctx, `SELECT count(*) FROM ar_applications WHERE id = $1 AND gl_entry_id IS NOT NULL`, cmApp).Scan(&linked); err != nil {
		t.Fatal(err)
	}
	if linked != 0 {
		t.Errorf("credit memo application still linked after the down migration")
	}
	var orphanLegs int
	if err := base.QueryRow(ctx, `SELECT count(*) FROM gl_journal_lines l
		JOIN gl_accounts a ON a.id = l.account_id
		WHERE a.code = '1095'`).Scan(&orphanLegs); err != nil {
		t.Fatal(err)
	}
	if orphanLegs != 0 {
		t.Errorf("%d 1095 legs survive the down migration, want 0", orphanLegs)
	}
	if err := base.QueryRow(ctx, `SELECT count(*) FROM gl_accounts WHERE code = '1095'`).Scan(&seeded); err != nil {
		t.Fatal(err)
	}
	if seeded != 0 {
		t.Errorf("1095 still seeded after the down migration")
	}
	// The payment application's 2200 leg is back on the entry's branch.
	var pay2200Branch uuid.UUID
	if err := base.QueryRow(ctx, `SELECT l.branch_id FROM gl_journal_lines l
		JOIN gl_accounts a ON a.id = l.account_id
		WHERE l.journal_entry_id = $1 AND a.code = '2200'`, payEntry).Scan(&pay2200Branch); err != nil {
		t.Fatal(err)
	}
	if pay2200Branch != other {
		t.Errorf("payment entry 2200 leg on branch %v after the down migration, want the entry's branch %v", pay2200Branch, other)
	}
	// The settlement legs the up migration moved return to 1010 Cash, so
	// nothing references 1095 and the account deletes cleanly.
	if got := legsOn("1095", settlementDescs); got != 0 {
		t.Errorf("%d settlement legs still on 1095 after the down migration, want 0", got)
	}
	if got := legsOn("1010", settlementDescs); got != 6 {
		t.Errorf("%d settlement legs back on 1010 after the down migration, want the 6 original legs", got)
	}
	// A second up lands on the same state as the first.
	up, err := os.ReadFile("../../migrations/133_gl_interbranch_clearing_and_ar_tieout.sql")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := base.Exec(ctx, string(up)); err != nil {
		t.Fatalf("reapply 133: %v", err)
	}
	var redone uuid.UUID
	if err := base.QueryRow(ctx, `SELECT gl_entry_id FROM ar_applications WHERE id = $1`, cmApp).Scan(&redone); err != nil {
		t.Fatalf("second up left no backfilled entry: %v", err)
	}
	if legs, ok := legsOf(redone)["1095"][other]; !ok || legs[0] != 100.00 {
		t.Errorf("second up 1095 leg on the invoice branch = %v, want DR 100.00", legsOf(redone)["1095"][other])
	}
	// The second up moves the settlement legs to 1095 again.
	if got := legsOn("1010", settlementDescs); got != 0 {
		t.Errorf("%d settlement legs back on 1010 after the second up, want 0", got)
	}
	if got := legsOn("1095", settlementDescs); got != 6 {
		t.Errorf("%d settlement legs on 1095 after the second up, want the 6 moved legs", got)
	}
}
