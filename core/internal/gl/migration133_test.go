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
// application's entry and brings both branches' AR tie out to zero, moves
// the cross-branch PAYMENT, CREDIT_MEMO and REVERSAL settlement legs from
// 1010 to 1095 while a MANUAL entry described the same way stays on 1010,
// and the down migration reverses all of it and drops 1095 while unused.

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
// credit memo applications, tie both branches' AR out, and move exactly the
// cross-branch PAYMENT, CREDIT_MEMO and REVERSAL settlement legs (the
// PostReversal 'Reversal: ...' wordings included) from 1010 to 1095 while a
// MANUAL entry described the same way keeps its 1010 legs; the down
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

	// Every seeded date is the branch's business day in America/New_York
	// (the branches' zone), never CURRENT_DATE: the control account reads
	// entries as of the branch's own day, and a UTC date is tomorrow in
	// New York between 00:00 UTC and midnight there, which would hide the
	// seeded rows from the tie out instead of proving it.
	var bizDate time.Time
	if err := base.QueryRow(ctx, `SELECT (NOW() AT TIME ZONE 'America/New_York')::date`).Scan(&bizDate); err != nil {
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
	// branch by 125's source document backfill). The two extra unapplied
	// open invoices, one per branch, keep each branch's AR control balance
	// non-zero so the tie out cannot pass on a zero-against-zero read.
	invCM := uuid.New()
	invPay := uuid.New()
	invDef := uuid.New()
	invOther := uuid.New()
	cm := uuid.New()
	pay := uuid.New()
	stmts := []struct {
		sql  string
		args []any
	}{
		{`INSERT INTO invoices (id, customer_id, branch_id, number, status, total_amount, amount_open, currency) VALUES ($1, $2, $3, 'M133-I1', 'UNPAID', 100.00, 0, 'USD')`, []any{invCM, cust, other}},
		{`INSERT INTO invoices (id, customer_id, branch_id, number, status, total_amount, amount_open, currency) VALUES ($1, $2, $3, 'M133-I2', 'UNPAID', 150.00, 0, 'USD')`, []any{invPay, cust, other}},
		{`INSERT INTO invoices (id, customer_id, branch_id, number, status, total_amount, amount_open, currency) VALUES ($1, $2, $3, 'M133-I3', 'UNPAID', 80.00, 80.00, 'USD')`, []any{invDef, cust, def}},
		{`INSERT INTO invoices (id, customer_id, branch_id, number, status, total_amount, amount_open, currency) VALUES ($1, $2, $3, 'M133-I4', 'UNPAID', 60.00, 60.00, 'USD')`, []any{invOther, cust, other}},
		{`INSERT INTO credit_memos (id, customer_id, branch_id, number, amount, subtotal, tax_amount, total_amount, amount_open, memo_date, status, reason, reason_code, currency) VALUES ($1, $2, $3, 'M133-CM', -100.00, -100.00, 0, -100.00, 0, $4, 'OPEN', 'm133', 'OTHER', 'USD')`, []any{cm, cust, def, bizDate}},
		{`INSERT INTO payments (id, customer_id, branch_id, amount, amount_unapplied, method, received_on, currency) VALUES ($1, $2, $3, 150.00, 0, 'CASH', $4, 'USD')`, []any{pay, cust, def, bizDate}},
	}
	for _, s := range stmts {
		if _, err := base.Exec(ctx, s.sql, s.args...); err != nil {
			t.Fatalf("seed: %v", err)
		}
	}

	cmApp := uuid.New()
	payApp := uuid.New()
	if _, err := base.Exec(ctx, `INSERT INTO ar_applications (id, customer_id, currency, kind, credit_memo_id, invoice_id, amount, applied_on, applied_by, act_id)
		VALUES ($1, $2, 'USD', 'CREDIT_MEMO', $3, $4, 100.00, $5, 't', $6)`, cmApp, cust, cm, invCM, bizDate, uuid.New()); err != nil {
		t.Fatal(err)
	}
	if _, err := base.Exec(ctx, `INSERT INTO ar_applications (id, customer_id, currency, kind, payment_id, invoice_id, amount, applied_on, applied_by, act_id)
		VALUES ($1, $2, 'USD', 'PAYMENT', $3, $4, 150.00, $5, 't', $6)`, payApp, cust, pay, invPay, bizDate, uuid.New()); err != nil {
		t.Fatal(err)
	}

	// The entries such a database holds: each source's own two line entry
	// stamped by 125's backfill (the memo's own creation entry on the
	// memo's branch def, legs and all), the payment application's DR 2200
	// / CR 1020 entry whole on the invoice's branch, and no transfer entry
	// for the memo application.
	entryDesc := func(source string, ref uuid.UUID, entryBranch uuid.UUID, memo, desc string, reverses *uuid.UUID, lines [][4]any) uuid.UUID {
		t.Helper()
		id := uuid.New()
		if _, err := base.Exec(ctx, `INSERT INTO gl_journal_entries (id, entry_date, memo, source, source_ref_id, branch_id, status, posted_by, currency, reverses_entry_id)
			VALUES ($1, $2, $3, $4, $5, $6, 'POSTED', 't', 'USD', $7)`, id, bizDate, memo, source, ref, entryBranch, reverses); err != nil {
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
	entry := func(source string, ref uuid.UUID, entryBranch uuid.UUID, lines [][4]any) uuid.UUID {
		t.Helper()
		return entryDesc(source, ref, entryBranch, "m133 mig", "m133", nil, lines)
	}
	_ = entry("INVOICE", invCM, other, [][4]any{{"1020", 100.00, 0, other}, {"4010", 0, 100.00, other}})
	_ = entry("INVOICE", invPay, other, [][4]any{{"1020", 150.00, 0, other}, {"4010", 0, 150.00, other}})
	_ = entry("INVOICE", invDef, def, [][4]any{{"1020", 80.00, 0, def}, {"4010", 0, 80.00, def}})
	_ = entry("INVOICE", invOther, other, [][4]any{{"1020", 60.00, 0, other}, {"4010", 0, 60.00, other}})
	_ = entry("CREDIT_MEMO", cm, def, [][4]any{{"4010", 100.00, 0, def}, {"1020", 0, 100.00, def}})
	payEntry := entry("PAYMENT", payApp, other, [][4]any{{"2200", 150.00, 0, other}, {"1020", 0, 150.00, other}})

	// The four leg cross-branch entries the code between 125 and 133 wrote
	// on 1010 Cash: two cross-branch payment settlements, one reversed
	// through PostReversal (whose lines carry the 'Reversal: ...'
	// description) and one reversed through the unlinked fallback (whose
	// lines carry the '... reversal' description), plus a cross-branch
	// credit memo settlement reversed through PostReversal. Each entry
	// balances per branch and each pair's 1020 legs net to zero on both
	// branches, so moving the 1010 legs to 1095 must be the only change.
	ibPayRef := uuid.New()
	ibEntry := entryDesc("PAYMENT", ibPayRef, def, "m133 mig", "Inter-branch cash settlement", nil, [][4]any{
		{"2200", 150.00, 0, def}, {"1010", 0, 150.00, def},
		{"1010", 150.00, 0, other}, {"1020", 0, 150.00, other},
	})
	_ = entryDesc("REVERSAL", ibPayRef, def, "m133 mig", "Reversal: Inter-branch cash settlement", &ibEntry, [][4]any{
		{"2200", 0, 150.00, def}, {"1010", 150.00, 0, def},
		{"1010", 0, 150.00, other}, {"1020", 150.00, 0, other},
	})
	ibPayRef2 := uuid.New()
	_ = entryDesc("PAYMENT", ibPayRef2, def, "m133 mig", "Inter-branch cash settlement", nil, [][4]any{
		{"2200", 150.00, 0, def}, {"1010", 0, 150.00, def},
		{"1010", 150.00, 0, other}, {"1020", 0, 150.00, other},
	})
	_ = entryDesc("REVERSAL", ibPayRef2, def, "m133 mig", "Inter-branch cash settlement reversal", nil, [][4]any{
		{"2200", 0, 150.00, def}, {"1010", 150.00, 0, def},
		{"1010", 0, 150.00, other}, {"1020", 150.00, 0, other},
	})
	ibCMRef := uuid.New()
	ibCMEntry := entryDesc("CREDIT_MEMO", ibCMRef, def, "m133 mig", "Inter-branch AR settlement", nil, [][4]any{
		{"1020", 100.00, 0, def}, {"1010", 0, 100.00, def},
		{"1010", 100.00, 0, other}, {"1020", 0, 100.00, other},
	})
	_ = entryDesc("REVERSAL", ibCMRef, def, "m133 mig", "Reversal: Inter-branch AR settlement", &ibCMEntry, [][4]any{
		{"1020", 0, 100.00, def}, {"1010", 100.00, 0, def},
		{"1010", 0, 100.00, other}, {"1020", 100.00, 0, other},
	})

	// A manual cash move between drawers is described the same way but is
	// no code-written settlement: its 1010 legs must stay on 1010 Cash.
	_ = entryDesc("MANUAL", uuid.New(), other, "m133 mig", "Inter-branch cash settlement", nil, [][4]any{
		{"1010", 50.00, 0, other}, {"1010", 0, 50.00, def},
	})

	// The customer transactions the subledger reads. The inter-branch
	// pairs' rows mirror their 1020 legs, so each pair nets to zero on the
	// subledger exactly as it does on the control account.
	tx := func(amount int64, ref uuid.UUID, typ string) {
		t.Helper()
		if _, err := base.Exec(ctx, `INSERT INTO customer_transactions (customer_id, amount, balance_after, reference_id, description, type, currency, source_kind)
			VALUES ($1, $2, 0, $3, 'm133', $4, 'USD', 'core')`, cust, amount, ref, typ); err != nil {
			t.Fatal(err)
		}
	}
	tx(10000, invCM, "INVOICE")
	tx(15000, invPay, "INVOICE")
	tx(8000, invDef, "INVOICE")
	tx(6000, invOther, "INVOICE")
	tx(-10000, cm, "CREDIT_MEMO")
	tx(-15000, payApp, "PAYMENT")
	tx(-15000, ibPayRef, "PAYMENT")
	tx(15000, ibPayRef, "PAYMENT")
	tx(15000, ibPayRef2, "PAYMENT")
	tx(-15000, ibPayRef2, "PAYMENT")

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
	// moved to 1095 Inter-Branch Clearing: exactly the 12 legs of the 6
	// cross-branch PAYMENT, CREDIT_MEMO and REVERSAL entries (the
	// PostReversal 'Reversal: ...' wordings included), and never a MANUAL
	// entry's legs, whatever its description says.
	legsOnSource := func(code, source, descList string) int {
		t.Helper()
		var n int
		if err := base.QueryRow(ctx, `SELECT count(*) FROM gl_journal_lines l
			JOIN gl_accounts a ON a.id = l.account_id
			JOIN gl_journal_entries e ON e.id = l.journal_entry_id
			WHERE a.code = $1 AND e.source = $2 AND l.description IN (`+descList+`)`, code, source).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	movedSources := `'PAYMENT', 'CREDIT_MEMO', 'REVERSAL'`
	settlementDescs := `'Inter-branch cash settlement', 'Inter-branch AR settlement', 'Inter-branch cash settlement reversal', 'Reversal: Inter-branch cash settlement', 'Reversal: Inter-branch AR settlement'`
	movedLegs := func(code string) int {
		t.Helper()
		var n int
		if err := base.QueryRow(ctx, `SELECT count(*) FROM gl_journal_lines l
			JOIN gl_accounts a ON a.id = l.account_id
			JOIN gl_journal_entries e ON e.id = l.journal_entry_id
			WHERE a.code = $1 AND e.source IN (`+movedSources+`) AND l.description IN (`+settlementDescs+`)`, code).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	if got := movedLegs("1010"); got != 0 {
		t.Errorf("%d cross-branch settlement legs remain on 1010 Cash after migration 133, want 0", got)
	}
	if got := movedLegs("1095"); got != 12 {
		t.Errorf("%d cross-branch settlement legs on 1095 after migration 133, want the 12 moved legs", got)
	}
	if got := legsOnSource("1010", "MANUAL", `'Inter-branch cash settlement'`); got != 2 {
		t.Errorf("%d MANUAL entry legs on 1010 Cash after migration 133, want the manual move's 2", got)
	}
	if got := legsOnSource("1095", "MANUAL", `'Inter-branch cash settlement'`); got != 0 {
		t.Errorf("%d MANUAL entry legs on 1095 after migration 133, want 0: a manual drawer move is no settlement", got)
	}

	// Per-branch 1010 Cash and 1095 Clearing balances: only the manual
	// drawer move's legs stay on 1010 (a 50.00 credit on def and a 50.00
	// debit on other), the six settlement entries' own clearing legs net
	// to zero per branch, and 1095 carries the step 2 backfill pair
	// (150.00) and the step 3 memo transfer pair (100.00) on top.
	balanceOn := func(code string) map[uuid.UUID]float64 {
		t.Helper()
		out := map[uuid.UUID]float64{}
		rows, err := base.Query(ctx, `SELECT l.branch_id, SUM(l.debit) - SUM(l.credit)
			FROM gl_journal_lines l JOIN gl_accounts a ON a.id = l.account_id
			WHERE a.code = $1 GROUP BY l.branch_id`, code)
		if err != nil {
			t.Fatal(err)
		}
		defer rows.Close()
		for rows.Next() {
			var b uuid.UUID
			var bal float64
			if err := rows.Scan(&b, &bal); err != nil {
				t.Fatal(err)
			}
			out[b] = bal
		}
		if err := rows.Err(); err != nil {
			t.Fatal(err)
		}
		return out
	}
	if bal := balanceOn("1010"); bal[def] != -50.00 || bal[other] != 50.00 {
		t.Errorf("1010 Cash balances after migration 133 = def %v other %v, want def -50 other 50 (the manual move alone)", bal[def], bal[other])
	}
	if bal := balanceOn("1095"); bal[def] != -250.00 || bal[other] != 250.00 {
		t.Errorf("1095 Clearing balances after migration 133 = def %v other %v, want def -250 other 250", bal[def], bal[other])
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

	// Every entry balances in total, and every code-written settlement
	// entry balances per branch (each branch carries a paired leg set).
	// The manual drawer move is the one exception by nature: one leg per
	// branch, balanced only across the two branches together.
	rows, err := base.Query(ctx, `SELECT e.id, l.branch_id, SUM(l.debit), SUM(l.credit)
		FROM gl_journal_entries e JOIN gl_journal_lines l ON l.journal_entry_id = e.id
		WHERE e.source <> 'MANUAL'
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

	// Both branches tie out: control 1020 balance against subledger, with
	// a non-zero control on each branch (the unapplied open invoices), at
	// the branch's own business day, at that day named explicitly, and at
	// the day after it (the read must hold whatever the UTC clock says).
	scratchDB, err := database.Connect(scratchURL)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { scratchDB.Pool.Close() })
	svc := gl.NewService(gl.NewRepository(scratchDB), nil, slog.Default())
	wantControl := map[uuid.UUID]int64{def: 8000, other: 6000}
	for _, b := range []uuid.UUID{def, other} {
		for _, asOf := range []time.Time{{}, bizDate, bizDate.AddDate(0, 0, 1)} {
			tie, err := svc.TieOut(ctx, asOf, "USD", &b)
			if err != nil {
				t.Fatal(err)
			}
			if tie.AR.DifferenceCents != 0 {
				t.Errorf("branch %v AR tie out (as of %v) after migration 133 = %+v, want DifferenceCents 0", b, asOf, tie.AR)
			}
			if tie.AR.ControlCents != wantControl[b] {
				t.Errorf("branch %v AR control (as of %v) after migration 133 = %d, want %d", b, asOf, tie.AR.ControlCents, wantControl[b])
			}
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
	// The 12 settlement legs the up migration moved return to 1010 Cash,
	// and the manual move's 2 legs never left it, so nothing references
	// 1095 and the account deletes cleanly.
	if got := movedLegs("1095"); got != 0 {
		t.Errorf("%d settlement legs still on 1095 after the down migration, want 0", got)
	}
	if got := movedLegs("1010"); got != 12 {
		t.Errorf("%d settlement legs back on 1010 after the down migration, want the 12 original legs", got)
	}
	if got := legsOnSource("1010", "MANUAL", `'Inter-branch cash settlement'`); got != 2 {
		t.Errorf("%d MANUAL entry legs on 1010 Cash after the down migration, want the manual move's 2", got)
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
	// The second up moves the settlement legs to 1095 again and still
	// leaves the manual move on 1010 Cash.
	if got := movedLegs("1010"); got != 0 {
		t.Errorf("%d settlement legs back on 1010 after the second up, want 0", got)
	}
	if got := movedLegs("1095"); got != 12 {
		t.Errorf("%d settlement legs on 1095 after the second up, want the 12 moved legs", got)
	}
	if got := legsOnSource("1010", "MANUAL", `'Inter-branch cash settlement'`); got != 2 {
		t.Errorf("%d MANUAL entry legs on 1010 Cash after the second up, want the manual move's 2", got)
	}
	if bal := balanceOn("1010"); bal[def] != -50.00 || bal[other] != 50.00 {
		t.Errorf("1010 Cash balances after the second up = def %v other %v, want def -50 other 50", bal[def], bal[other])
	}
}
