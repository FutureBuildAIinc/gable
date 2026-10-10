// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package ap_test

// Migration 106 on rows that exist (the module recipe's step 3, ADR 0008
// section 12): the schema up to 102 is built in a scratch database, awkward
// legacy bills are written in the shape the base left them (duplicate vendor
// numbers, an orphan po_id, a NULL created_at, odd statuses, paid and partial
// rows), then 106 is applied and every backfill is read back: the duplicate
// suffixes in (created_at, id) order, the orphan set null, the NULL created_at
// filled, the statuses normalized, the numbers minted and unique, the branch
// and currency filled, amount_open computed, the constraints in force. The
// migration applies a second time (a no-op), the down file rolls the shape
// back (refusing while a unit price is finer than cents), and the migration
// applies again to the same result.

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/gablelbm/gable/internal/testutil"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// scratch106 builds a scratch database and returns its connection, its URL
// (for the seed command) and the notices the connection has collected.
func scratch106(t *testing.T) (*pgx.Conn, string, *[]string) {
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
		admin.Close(ctx)
		t.Skipf("cannot create a scratch database: %v", err)
	}
	name := "gv1_c41bmig_" + hex.EncodeToString(suffix)
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
	cfg, err := pgx.ParseConfig(scratch.String())
	if err != nil {
		t.Fatal(err)
	}
	var notices []string
	cfg.OnNotice = func(_ *pgconn.PgConn, n *pgconn.Notice) { notices = append(notices, n.Message) }
	conn, err := pgx.ConnectConfig(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { conn.Close(context.Background()) })
	return conn, scratch.String(), &notices
}

func files106(t *testing.T) (before []string, target, down string) {
	t.Helper()
	all, err := filepath.Glob("../../migrations/*.sql")
	if err != nil || len(all) == 0 {
		t.Fatalf("no migrations found: %v", err)
	}
	sort.Strings(all)
	for _, f := range all {
		base := filepath.Base(f)
		switch {
		case strings.HasSuffix(base, "_down.sql"):
		case strings.HasPrefix(base, "106_"):
			target = f
		case base < "106_":
			before = append(before, f)
		}
	}
	if target == "" {
		t.Fatal("migration 106 not found")
	}
	return before, target, "../../migrations/down/106_ap_wire_contract_down.sql"
}

func apply106(t *testing.T, conn *pgx.Conn, file string) {
	t.Helper()
	sql, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := conn.Exec(context.Background(), string(sql)); err != nil {
		t.Fatalf("applying %s: %v", filepath.Base(file), err)
	}
}

func one106(t *testing.T, conn *pgx.Conn, sql string, args ...any) string {
	t.Helper()
	var v string
	if err := conn.QueryRow(context.Background(), sql, args...).Scan(&v); err != nil {
		t.Fatalf("%s: %v", sql, err)
	}
	return v
}

func count106(t *testing.T, conn *pgx.Conn, sql string, args ...any) int {
	t.Helper()
	var n int
	if err := conn.QueryRow(context.Background(), sql, args...).Scan(&n); err != nil {
		t.Fatalf("%s: %v", sql, err)
	}
	return n
}

// refuses asserts the named constraint refuses the statement.
func refuses106(t *testing.T, conn *pgx.Conn, constraint, sql string, args ...any) {
	t.Helper()
	ctx := context.Background()
	tx, err := conn.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(ctx)
	_, err = tx.Exec(ctx, sql, args...)
	var pg *pgconn.PgError
	if err == nil || !errors.As(err, &pg) || pg.ConstraintName != constraint {
		t.Errorf("expected constraint %s to refuse, got %v", constraint, err)
	}
}

func TestMigration106_OnAwkwardLegacyRows(t *testing.T) {
	conn, _, notices := scratch106(t)
	before, target, down := files106(t)
	for _, f := range before {
		apply106(t, conn, f)
	}

	// The awkward legacy rows: duplicates of one vendor number (three, with
	// distinct created_at so the suffix order is checkable), an orphan po_id,
	// a NULL created_at (the base column was NOT NULL, so the NOT NULL comes
	// off first to write one), odd statuses, and paid, partial and voided rows.
	ctx := context.Background()
	var vendor uuid.UUID
	if err := conn.QueryRow(ctx, `INSERT INTO vendors (id, name) VALUES ($1, 'Mig 106 Vendor') RETURNING id`, uuid.New()).Scan(&vendor); err != nil {
		t.Fatal(err)
	}
	insert := func(id uuid.UUID, number, status, createdAt string, poID *uuid.UUID, total, paid string) {
		t.Helper()
		var po any
		if poID != nil {
			po = *poID
		}
		if _, err := conn.Exec(ctx, `INSERT INTO vendor_invoices (id, vendor_id, invoice_number, invoice_date, due_date, po_id,
			subtotal, tax_amount, total, amount_paid, status, created_at)
			VALUES ($1, $2, $3, '2026-01-10', '2026-02-10', $4, $5, 0, $5, $6, $7, $8)`,
			id, vendor, number, po, total, paid, status, createdAt); err != nil {
			t.Fatalf("seed %s: %v", number, err)
		}
		if _, err := conn.Exec(ctx, `INSERT INTO vendor_invoice_lines (id, invoice_id, description, quantity, unit_price, line_total, created_at)
			VALUES ($1, $2, 'legacy line', 1, $3, $3, $4)`, uuid.New(), id, total, createdAt); err != nil {
			t.Fatalf("seed line %s: %v", number, err)
		}
	}
	at := func(days int) string { return time.Now().AddDate(0, 0, days).Format(time.RFC3339) }
	orphan := uuid.New()
	dup1, dup2, dup3 := uuid.New(), uuid.New(), uuid.New()
	insert(dup1, "DUP-1", "PENDING", at(-9), nil, "100.00", "0")
	insert(dup2, "DUP-1", "pending", at(-8), nil, "100.00", "0") // lowercase: normalized
	insert(dup3, "DUP-1", "WEIRD", at(-7), nil, "100.00", "0")   // unknown: becomes PENDING
	// A genuine suffixed number the vendor already held: the suffix pass must
	// step past it (#3, #4) rather than collide and roll the migration back.
	genuine := uuid.New()
	insert(genuine, "DUP-1 #2", "PENDING", at(-6), nil, "100.00", "0")
	nailed := uuid.New()
	insert(nailed, "PAID-1", "PAID", at(-6), nil, "50.00", "50.00")
	partial := uuid.New()
	insert(partial, "PART-1", "PARTIAL", at(-5), nil, "80.00", "30.00")
	voided := uuid.New()
	insert(voided, "VOID-1", "VOIDED", at(-4), &orphan, "70.00", "0")
	// A hand-written cancel-like status maps to the module's voided state, not
	// to pending (a cancelled bill owes nothing); a short-paid PAID bill and an
	// overpaid PARTIAL one take the insert trigger's amount_open rule (a PAID
	// bill owes nothing), each named by a notice.
	cancelled := uuid.New()
	insert(cancelled, "CANC-1", "CANCELLED", at(-4), nil, "60.00", "0")
	paidShort := uuid.New()
	insert(paidShort, "PAIDSHORT-1", "PAID", at(-4), nil, "50.00", "44.00")
	over := uuid.New()
	insert(over, "OVER-1", "PARTIAL", at(-4), nil, "80.00", "90.00")
	// A negative total: the base validated no sign, so a vendor credit
	// entered as a negative bill is a plausible legacy row. It owes nothing
	// as a bill: it becomes VOIDED with open 0, named by a notice, and a
	// negative total already VOIDED passes the amount_open range CHECK; one
	// of either shape must not abort the migration (review r4 P2-2).
	negative := uuid.New()
	insert(negative, "NEG-1", "PENDING", at(-3), nil, "-10.00", "0")
	negVoided := uuid.New()
	insert(negVoided, "NEG-2", "VOIDED", at(-3), nil, "-5.00", "0")
	// A NULL created_at: the base column was NOT NULL, so it comes off first
	// (the migration's own fill-then-set step then has a row to fill).
	if _, err := conn.Exec(ctx, `ALTER TABLE vendor_invoices ALTER COLUMN created_at DROP NOT NULL`); err != nil {
		t.Fatal(err)
	}
	nulled := uuid.New()
	insert(nulled, "NULL-CREATED", "PENDING", at(-3), nil, "10.00", "0")
	if _, err := conn.Exec(ctx, `UPDATE vendor_invoices SET created_at = NULL WHERE id = $1`, nulled); err != nil {
		t.Fatal(err)
	}

	rowsBefore := count106(t, conn, `SELECT count(*) FROM vendor_invoices`)

	apply106(t, conn, target)

	// Counts before and after: no row lost or gained.
	if rowsAfter := count106(t, conn, `SELECT count(*) FROM vendor_invoices`); rowsAfter != rowsBefore {
		t.Fatalf("%d rows before, %d after", rowsBefore, rowsAfter)
	}

	// The duplicates are suffixed in (created_at, id) order: the oldest keeps
	// the bare number.
	if got := one106(t, conn, `SELECT invoice_number FROM vendor_invoices WHERE id = $1`, dup1); got != "DUP-1" {
		t.Errorf("the oldest duplicate = %q, want the bare DUP-1", got)
	}
	if got := one106(t, conn, `SELECT invoice_number FROM vendor_invoices WHERE id = $1`, dup2); got != "DUP-1 #3" {
		t.Errorf("the second duplicate = %q, want DUP-1 #3, the next free suffix", got)
	}
	if got := one106(t, conn, `SELECT invoice_number FROM vendor_invoices WHERE id = $1`, dup3); got != "DUP-1 #4" {
		t.Errorf("the third duplicate = %q, want DUP-1 #4", got)
	}
	if got := one106(t, conn, `SELECT invoice_number FROM vendor_invoices WHERE id = $1`, genuine); got != "DUP-1 #2" {
		t.Errorf("the genuine suffixed number = %q, want it kept as DUP-1 #2", got)
	}

	// The orphan po_id is set null; the others keep theirs.
	if got := one106(t, conn, `SELECT COALESCE(po_id::text, 'null') FROM vendor_invoices WHERE id = $1`, voided); got != "null" {
		t.Errorf("the orphan po_id = %s, want null", got)
	}

	// The NULL created_at is filled.
	if got := count106(t, conn, `SELECT count(*) FROM vendor_invoices WHERE created_at IS NULL`); got != 0 {
		t.Errorf("%d rows still hold a NULL created_at", got)
	}

	// The odd statuses are normalized, with notices.
	if got := one106(t, conn, `SELECT status FROM vendor_invoices WHERE id = $1`, dup2); got != "PENDING" {
		t.Errorf("the lowercase status = %s, want PENDING", got)
	}
	if got := one106(t, conn, `SELECT status FROM vendor_invoices WHERE id = $1`, dup3); got != "PENDING" {
		t.Errorf("the unknown status = %s, want PENDING", got)
	}
	joined := strings.Join(*notices, " | ")
	for _, want := range []string{"duplicate vendor invoice number", "po_id is set null", "unknown status",
		"cancel-like status", "paid less than their total", "paid past their total", "negative total"} {
		if !strings.Contains(joined, want) {
			t.Errorf("the notices (%s) do not mention %q", joined, want)
		}
	}

	// The numbers: unique, AP-, and the sequence sits past the backfill.
	if got := count106(t, conn, `SELECT count(*) FROM vendor_invoices WHERE number !~ '^AP-[0-9]+$'`); got != 0 {
		t.Errorf("%d rows hold no AP- number", got)
	}
	if got := count106(t, conn, `SELECT count(DISTINCT number) - count(*) FROM vendor_invoices`); got != 0 {
		t.Errorf("%d duplicate numbers", got)
	}
	// The backfill order is (created_at, id): the oldest bill holds the
	// smallest number.
	if got := one106(t, conn, `SELECT number FROM vendor_invoices WHERE id = $1`, dup1); got != "AP-000001" {
		t.Errorf("the oldest bill's number = %s, want AP-000001", got)
	}
	if _, err := conn.Exec(ctx, `INSERT INTO vendor_invoices (vendor_id, invoice_number, invoice_date, due_date, subtotal, tax_amount, total)
		VALUES ($1, 'AFTER-1', '2026-01-10', '2026-02-10', 1, 0, 1) RETURNING id`, vendor); err != nil {
		t.Fatal(err)
	}
	if got := one106(t, conn, `SELECT number FROM vendor_invoices WHERE invoice_number = 'AFTER-1'`); got != fmt.Sprintf("AP-%06d", rowsBefore+1) {
		t.Errorf("a bill created after the migration = %s, want the next number past the backfill", got)
	}

	// The branch and the currency are filled.
	if got := count106(t, conn, `SELECT count(*) FROM vendor_invoices WHERE branch_id IS NULL OR currency IS NULL`); got != 0 {
		t.Errorf("%d rows hold no branch or currency", got)
	}
	def := one106(t, conn, `SELECT value FROM system_settings WHERE key = 'default_branch_id'`)
	if got := count106(t, conn, `SELECT count(*) FROM vendor_invoices WHERE branch_id::text <> $1`, def); got != 0 {
		t.Errorf("%d rows are not at the default branch (no purchase order branch exists yet)", got)
	}

	// amount_open: total less paid, zero on a voided bill and on a paid one
	// (the insert trigger's rule), clamped at zero for an overpaid partial.
	// A negative total bill owes nothing: it is VOIDED with open 0 (the
	// range CHECK admits a voided or paid bill at any total).
	for id, want := range map[uuid.UUID]string{dup1: "100.00", nailed: "0.00", partial: "50.00", voided: "0.00",
		cancelled: "0.00", paidShort: "0.00", over: "0.00", negative: "0.00", negVoided: "0.00"} {
		if got := one106(t, conn, `SELECT amount_open::text FROM vendor_invoices WHERE id = $1`, id); got != want {
			t.Errorf("amount_open of %s = %s, want %s", id, got, want)
		}
	}
	// The cancel-like status becomes VOIDED, not PENDING.
	if got := one106(t, conn, `SELECT status FROM vendor_invoices WHERE id = $1`, cancelled); got != "VOIDED" {
		t.Errorf("the cancel-like status = %s, want VOIDED", got)
	}
	// The negative total bill becomes VOIDED too.
	if got := one106(t, conn, `SELECT status FROM vendor_invoices WHERE id = $1`, negative); got != "VOIDED" {
		t.Errorf("the negative total bill = %s, want VOIDED", got)
	}

	// The lines: positioned in (created_at, id) order, and the price widened.
	if got := one106(t, conn, `SELECT position::text FROM vendor_invoice_lines l JOIN vendor_invoices v ON v.id = l.invoice_id WHERE v.id = $1`, dup1); got != "0" {
		t.Errorf("the single legacy line's position = %s, want 0", got)
	}

	// The constraints refuse what the old column took.
	refuses106(t, conn, "vendor_invoices_vendor_number_key",
		`INSERT INTO vendor_invoices (vendor_id, invoice_number, invoice_date, due_date, subtotal, tax_amount, total) VALUES ($1, 'DUP-1', '2026-01-10', '2026-02-10', 1, 0, 1)`, vendor)
	refuses106(t, conn, "vendor_invoices_status_check",
		`UPDATE vendor_invoices SET status = 'WEIRD' WHERE id = $1`, dup1)
	refuses106(t, conn, "vendor_invoices_po_id_fkey",
		`UPDATE vendor_invoices SET po_id = $2 WHERE id = $1`, dup1, uuid.New())
	if got := count106(t, conn, `SELECT count(*) FROM vendor_invoice_lines WHERE purchase_order_line_id IS NOT NULL OR product_id IS NOT NULL OR po_freight_charge_id IS NOT NULL`); got != 0 {
		t.Errorf("%d legacy lines were linked to something", got)
	}

	// A second up is a no-op: every count and value stands.
	numbersBefore := one106(t, conn, `SELECT string_agg(number, ',' ORDER BY number) FROM vendor_invoices`)
	apply106(t, conn, target)
	if numbersAfter := one106(t, conn, `SELECT string_agg(number, ',' ORDER BY number) FROM vendor_invoices`); numbersAfter != numbersBefore {
		t.Errorf("a second apply changed the numbers: %s -> %s", numbersBefore, numbersAfter)
	}

	// The down rolls the shape back.
	apply106(t, conn, down)
	for _, column := range []string{"number", "revision", "branch_id", "currency", "amount_open"} {
		if got := count106(t, conn, `SELECT count(*) FROM information_schema.columns WHERE table_name = 'vendor_invoices' AND column_name = $1`, column); got != 0 {
			t.Errorf("the down left %s on vendor_invoices", column)
		}
	}
	if got := count106(t, conn, `SELECT count(*) FROM information_schema.columns WHERE table_name = 'vendor_invoice_lines' AND column_name = 'position'`); got != 0 {
		t.Error("the down left position on vendor_invoice_lines")
	}
	if got := one106(t, conn, `SELECT data_type FROM information_schema.columns WHERE table_name = 'vendor_invoice_lines' AND column_name = 'unit_price'`); got != "numeric" {
		t.Error("the down left unit_price's type changed")
	}
	if got := one106(t, conn, `SELECT numeric_scale::text FROM information_schema.columns WHERE table_name = 'vendor_invoice_lines' AND column_name = 'unit_price'`); got != "2" {
		t.Errorf("the down left unit_price at scale %s, want 2", got)
	}
	// Three of the up's writes are kept, each valid old shape data.
	if got := one106(t, conn, `SELECT invoice_number FROM vendor_invoices WHERE id = $1`, dup2); got != "DUP-1 #3" {
		t.Errorf("the down un-suffixed a duplicate: %s", got)
	}

	// The down refuses while a unit price is finer than cents.
	apply106(t, conn, target)
	if _, err := conn.Exec(ctx, `UPDATE vendor_invoice_lines SET unit_price = 1.0005`); err != nil {
		t.Fatal(err)
	}
	if _, err := conn.Exec(context.Background(), func() string {
		b, _ := os.ReadFile(down)
		return string(b)
	}()); err == nil {
		t.Error("the down applied though a unit price is finer than cents")
	}
	// Clean that row, and the down applies; the up then applies again to the
	// same result.
	if _, err := conn.Exec(ctx, `UPDATE vendor_invoice_lines SET unit_price = 1.00`); err != nil {
		t.Fatal(err)
	}
	apply106(t, conn, down)
	apply106(t, conn, target)
	if got := count106(t, conn, `SELECT count(*) FROM vendor_invoices WHERE number IS NULL OR amount_open IS NULL`); got != 0 {
		t.Errorf("%d rows incomplete after the down-up cycle", got)
	}
}

// TestMigration106_LinksLegacyApprovedEntries proves the gl_entry_id backfill:
// a bill the base approved posted its entry through the removed sync (source
// VENDOR_INVOICE, source_ref_id the bill), and 106 links the newest posted,
// unreversed of those entries to every approved or partial bill. A bill whose
// only entry is a reversal, or that has none, stays unlinked and is counted by
// the notice, and a pending bill is never linked (nothing was approved). A
// bill whose entry is the base's real one leg shape (the Accounts Payable
// credit with no debit: the base loaded no lines) is never linked either, and
// its own notice names it, for the void would mirror the one leg entry into
// another unbalanced one.
func TestMigration106_LinksLegacyApprovedEntries(t *testing.T) {
	conn, _, notices := scratch106(t)
	before, target, _ := files106(t)
	for _, f := range before {
		apply106(t, conn, f)
	}

	ctx := context.Background()
	var vendor uuid.UUID
	if err := conn.QueryRow(ctx, `INSERT INTO vendors (id, name) VALUES ($1, 'Mig 106 Legacy Vendor') RETURNING id`, uuid.New()).Scan(&vendor); err != nil {
		t.Fatal(err)
	}
	var expense, payable string
	if err := conn.QueryRow(ctx, `SELECT id::text FROM gl_accounts WHERE code = '5020'`).Scan(&expense); err != nil {
		t.Fatalf("expense account: %v", err)
	}
	if err := conn.QueryRow(ctx, `SELECT id::text FROM gl_accounts WHERE code = '2010'`).Scan(&payable); err != nil {
		t.Fatalf("payable account: %v", err)
	}
	// entry writes a posted VENDOR_INVOICE entry of a flat 10.00 for the bill,
	// the shape the base's removed sync left, at the given time.
	entry := func(bill uuid.UUID, at string, reverses *uuid.UUID) uuid.UUID {
		t.Helper()
		id := uuid.New()
		var rev any
		if reverses != nil {
			rev = *reverses
		}
		if _, err := conn.Exec(ctx, `INSERT INTO gl_journal_entries (id, entry_date, memo, source, source_ref_id, status, posted_by, reverses_entry_id, created_at, updated_at, currency)
			VALUES ($1, '2026-01-10', 'legacy approve', 'VENDOR_INVOICE', $2, 'POSTED', 'legacy', $3, $4, $4, 'USD')`,
			id, bill, rev, at); err != nil {
			t.Fatalf("seed entry: %v", err)
		}
		for _, l := range []struct {
			account, debit, credit string
		}{{expense, "10.00", "0"}, {payable, "0", "10.00"}} {
			if _, err := conn.Exec(ctx, `INSERT INTO gl_journal_lines (id, journal_entry_id, account_id, description, debit, credit)
				VALUES ($1, $2, $3::uuid, 'legacy leg', $4, $5)`, uuid.New(), id, l.account, l.debit, l.credit); err != nil {
				t.Fatalf("seed entry line: %v", err)
			}
		}
		return id
	}
	// oneLegEntry writes the base's real approve entry: one leg, the Accounts
	// Payable credit for the total and no debit (the base read the bill without
	// its lines, so the removed sync posted a single credit leg, and nothing
	// stopped it: chk_debit_or_credit accepts one leg). This, not a balanced
	// two leg entry, is the shape every base approved bill holds.
	oneLegEntry := func(bill uuid.UUID, at string) uuid.UUID {
		t.Helper()
		id := uuid.New()
		if _, err := conn.Exec(ctx, `INSERT INTO gl_journal_entries (id, entry_date, memo, source, source_ref_id, status, posted_by, created_at, updated_at, currency)
			VALUES ($1, '2026-01-10', 'legacy approve', 'VENDOR_INVOICE', $2, 'POSTED', 'legacy', $3, $3, 'USD')`,
			id, bill, at); err != nil {
			t.Fatalf("seed one leg entry: %v", err)
		}
		if _, err := conn.Exec(ctx, `INSERT INTO gl_journal_lines (id, journal_entry_id, account_id, description, debit, credit)
			VALUES ($1, $2, $3::uuid, 'legacy leg', 0, '10.00')`, uuid.New(), id, payable); err != nil {
			t.Fatalf("seed one leg entry line: %v", err)
		}
		return id
	}
	insert := func(id uuid.UUID, number, status string) {
		t.Helper()
		if _, err := conn.Exec(ctx, `INSERT INTO vendor_invoices (id, vendor_id, invoice_number, invoice_date, due_date,
			subtotal, tax_amount, total, amount_paid, status, created_at)
			VALUES ($1, $2, $3, '2026-01-10', '2026-02-10', '10.00', 0, '10.00', '4.00', $4, NOW())`,
			id, vendor, number, status); err != nil {
			t.Fatalf("seed %s: %v", number, err)
		}
	}
	oldTime := time.Now().AddDate(0, 0, -10).Format(time.RFC3339)
	newTime := time.Now().AddDate(0, 0, -2).Format(time.RFC3339)

	// An approved bill with two posted entries: the newest is linked.
	linked, unlinked, pending := uuid.New(), uuid.New(), uuid.New()
	insert(linked, "LEG-LINKED", "APPROVED")
	older := entry(linked, oldTime, nil)
	newest := entry(linked, newTime, nil)
	// A partial bill whose only entry is a reversal of another bill's entry:
	// not linkable, so it stays unlinked and is counted.
	insert(unlinked, "LEG-UNLINKED", "PARTIAL")
	elsewhere := uuid.New()
	stranger := entry(elsewhere, oldTime, nil)
	entry(unlinked, newTime, &stranger)
	// A pending bill with a posted entry: never linked, and not counted.
	insert(pending, "LEG-PENDING", "PENDING")
	entry(pending, newTime, nil)
	// The base's real approved bill: a one leg entry, the Accounts Payable
	// credit with no debit. Linking it would let the void mirror it into
	// another unbalanced entry, so it stays unlinked and its own notice
	// names it, apart from the bills with nothing to link.
	onelegged := uuid.New()
	insert(onelegged, "LEG-ONELEG", "APPROVED")
	oneLegEntry(onelegged, newTime)

	// A hand cancelled bill still holding a posted entry: it becomes VOIDED
	// with its credit standing, and a notice counts it.
	cancelled := uuid.New()
	insert(cancelled, "LEG-CANCELLED", "CANCELLED")
	entry(cancelled, newTime, nil)

	apply106(t, conn, target)

	if got := one106(t, conn, `SELECT COALESCE(gl_entry_id::text, 'null') FROM vendor_invoices WHERE id = $1`, linked); got != newest.String() {
		t.Errorf("the approved bill links %s, want the newest entry %s", got, newest)
	}
	if older == newest {
		t.Fatal("the two entries share an id")
	}
	if got := one106(t, conn, `SELECT COALESCE(gl_entry_id::text, 'null') FROM vendor_invoices WHERE id = $1`, unlinked); got != "null" {
		t.Errorf("the bill whose only entry is a reversal links %s, want none", got)
	}
	if got := one106(t, conn, `SELECT COALESCE(gl_entry_id::text, 'null') FROM vendor_invoices WHERE id = $1`, pending); got != "null" {
		t.Errorf("the pending bill links %s, want none", got)
	}
	if got := one106(t, conn, `SELECT COALESCE(gl_entry_id::text, 'null') FROM vendor_invoices WHERE id = $1`, onelegged); got != "null" {
		t.Errorf("the bill whose entry has one leg links %s, want none: a one leg entry must not be linked", got)
	}
	joined := strings.Join(*notices, " | ")
	if !strings.Contains(joined, "no journal entry to link") {
		t.Errorf("the notices (%s) do not name the unlinked approved bills", joined)
	}
	if !strings.Contains(joined, "1 voided or cancelled vendor invoice(s) still hold a posted, unreversed journal entry") {
		t.Errorf("the notices (%s) do not count the cancelled bill that holds an entry", joined)
	}
	if !strings.Contains(joined, "1 vendor invoice(s) were posted more than once") {
		t.Errorf("the notices (%s) do not count the bill posted twice", joined)
	}
	if !strings.Contains(joined, "one leg entry") {
		t.Errorf("the notices (%s) do not name the one leg entry bills", joined)
	}
	// A second apply changes nothing.
	apply106(t, conn, target)
	if got := one106(t, conn, `SELECT COALESCE(gl_entry_id::text, 'null') FROM vendor_invoices WHERE id = $1`, linked); got != newest.String() {
		t.Errorf("a second apply moved the link to %s", got)
	}
}

// TestMigration106_OnTheSeededDatabase applies 106 to a database seeded by
// the repository's own seed (the recipe's rule: a migration is proved on a
// seeded database, not only on fixtures), with awkward legacy bills written
// over the seed. Only this proof reaches the branch backfill's purchase
// order source: the seed writes purchase orders on the yards, so a bill
// tied to a real purchase order takes that order's branch, not the default
// branch every fixture row falls to. Then the down rolls the shape back,
// the up applies again to the same result, and a second up is a no-op.
func TestMigration106_OnTheSeededDatabase(t *testing.T) {
	conn, scratchURL, _ := scratch106(t)
	before, target, down := files106(t)
	for _, f := range before {
		apply106(t, conn, f)
	}
	// The seed, as an operator runs it: the same binary, the scratch
	// database's URL on its own command.
	cmd := exec.Command("go", "run", "./cmd/seed")
	cmd.Dir = "../.."
	cmd.Env = append(os.Environ(), "DEMO_SEED=1", "DATABASE_URL="+scratchURL)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Skipf("the seed did not run here (%v): %s", err, out)
	}

	ctx := context.Background()
	// A seeded vendor and a seeded purchase order on a branch other than the
	// default (the seed rotates the yards; a run that mints none makes this
	// proof vacuous, so it refuses instead).
	var vendor, poID, poBranch, def string
	if err := conn.QueryRow(ctx, `SELECT id::text FROM vendors ORDER BY name LIMIT 1`).Scan(&vendor); err != nil {
		t.Fatal(err)
	}
	if err := conn.QueryRow(ctx, `
		SELECT po.id::text, po.branch_id::text FROM purchase_orders po
		JOIN locations l ON l.id = po.branch_id
		WHERE po.branch_id::text <> (SELECT value FROM system_settings WHERE key = 'default_branch_id')
		ORDER BY po.created_at, po.id LIMIT 1`).Scan(&poID, &poBranch); err != nil {
		t.Fatalf("no seeded purchase order sits on a branch other than the default: %v", err)
	}
	def = one106(t, conn, `SELECT value FROM system_settings WHERE key = 'default_branch_id'`)
	if poBranch == def {
		t.Fatal("the chosen purchase order sits on the default branch")
	}

	// The awkward legacy rows over the seed: a bill tied to the real
	// purchase order (its branch comes from the order), a duplicate pair,
	// an orphan po_id and a lowercase status.
	insert := func(id uuid.UUID, number, status, poRef, createdAt string) {
		t.Helper()
		var po any
		if poRef != "" {
			po = poRef
		}
		if _, err := conn.Exec(ctx, `INSERT INTO vendor_invoices (id, vendor_id, invoice_number, invoice_date, due_date, po_id,
			subtotal, tax_amount, total, amount_paid, status, created_at)
			VALUES ($1, $2, $3, '2026-01-10', '2026-02-10', $4, '40.00', 0, '40.00', 0, $5, $6)`,
			id, vendor, number, po, status, createdAt); err != nil {
			t.Fatalf("seed %s: %v", number, err)
		}
		if _, err := conn.Exec(ctx, `INSERT INTO vendor_invoice_lines (id, invoice_id, description, quantity, unit_price, line_total, created_at)
			VALUES ($1, $2, 'seeded legacy line', 1, '40.00', '40.00', $3)`, uuid.New(), id, createdAt); err != nil {
			t.Fatalf("seed line %s: %v", number, err)
		}
	}
	at := func(days int) string { return time.Now().AddDate(0, 0, days).Format(time.RFC3339) }
	poBill, dupA, dupB, orphan, lower := uuid.New(), uuid.New(), uuid.New(), uuid.New(), uuid.New()
	insert(poBill, "SEED-PO", "PENDING", poID, at(-6))
	insert(dupA, "SEED-DUP", "PENDING", "", at(-5))
	insert(dupB, "SEED-DUP", "pending", "", at(-4))
	insert(orphan, "SEED-ORPHAN", "PENDING", uuid.New().String(), at(-3))
	insert(lower, "SEED-LOWER", "partial", "", at(-2))
	rowsBefore := count106(t, conn, `SELECT count(*) FROM vendor_invoices`)

	apply106(t, conn, target)

	if rowsAfter := count106(t, conn, `SELECT count(*) FROM vendor_invoices`); rowsAfter != rowsBefore {
		t.Fatalf("%d rows before, %d after", rowsBefore, rowsAfter)
	}
	// The bill tied to the purchase order takes the order's branch, not the
	// default branch.
	if got := one106(t, conn, `SELECT branch_id::text FROM vendor_invoices WHERE id = $1`, poBill); got != poBranch {
		t.Errorf("the purchase order bill's branch = %s, want the order's %s", got, poBranch)
	}
	// Every other row still holds a branch and a currency.
	if got := count106(t, conn, `SELECT count(*) FROM vendor_invoices WHERE branch_id IS NULL OR currency IS NULL`); got != 0 {
		t.Errorf("%d rows hold no branch or currency", got)
	}
	// The duplicate pair is suffixed in (created_at, id) order.
	if got := one106(t, conn, `SELECT invoice_number FROM vendor_invoices WHERE id = $1`, dupA); got != "SEED-DUP" {
		t.Errorf("the older duplicate = %q, want the bare SEED-DUP", got)
	}
	if got := one106(t, conn, `SELECT invoice_number FROM vendor_invoices WHERE id = $1`, dupB); got != "SEED-DUP #2" {
		t.Errorf("the younger duplicate = %q, want SEED-DUP #2", got)
	}
	// The orphan po_id is set null.
	if got := one106(t, conn, `SELECT COALESCE(po_id::text, 'null') FROM vendor_invoices WHERE id = $1`, orphan); got != "null" {
		t.Errorf("the orphan po_id = %s, want null", got)
	}
	// The statuses are normalized to today's vocabulary.
	if got := one106(t, conn, `SELECT status FROM vendor_invoices WHERE id = $1`, dupB); got != "PENDING" {
		t.Errorf("the lowercase status = %s, want PENDING", got)
	}
	if got := one106(t, conn, `SELECT status FROM vendor_invoices WHERE id = $1`, lower); got != "PARTIAL" {
		t.Errorf("the lowercase partial = %s, want PARTIAL", got)
	}
	// The numbers and the open amounts cover every row.
	if got := count106(t, conn, `SELECT count(*) FROM vendor_invoices WHERE number IS NULL OR number !~ '^AP-[0-9]+$'`); got != 0 {
		t.Errorf("%d rows hold no AP- number", got)
	}
	if got := count106(t, conn, `SELECT count(DISTINCT number) - count(*) FROM vendor_invoices`); got != 0 {
		t.Errorf("%d duplicate numbers", got)
	}
	if got := count106(t, conn, `SELECT count(*) FROM vendor_invoices WHERE amount_open IS NULL OR amount_open <> total - amount_paid`); got != 0 {
		t.Errorf("%d rows hold a wrong amount_open", got)
	}

	// A second up is a no-op.
	numbersBefore := one106(t, conn, `SELECT string_agg(number, ',' ORDER BY number) FROM vendor_invoices`)
	apply106(t, conn, target)
	if numbersAfter := one106(t, conn, `SELECT string_agg(number, ',' ORDER BY number) FROM vendor_invoices`); numbersAfter != numbersBefore {
		t.Errorf("a second apply changed the numbers: %s -> %s", numbersBefore, numbersAfter)
	}

	// The down rolls the shape back, and the up applies again to the same
	// result over the seeded database.
	apply106(t, conn, down)
	apply106(t, conn, target)
	if got := count106(t, conn, `SELECT count(*) FROM vendor_invoices`); got != rowsBefore {
		t.Fatalf("%d rows after the down-up cycle, want %d", got, rowsBefore)
	}
	if got := count106(t, conn, `SELECT count(*) FROM vendor_invoices WHERE number IS NULL OR amount_open IS NULL OR branch_id IS NULL OR currency IS NULL`); got != 0 {
		t.Errorf("%d rows incomplete after the down-up cycle", got)
	}
	if got := one106(t, conn, `SELECT branch_id::text FROM vendor_invoices WHERE id = $1`, poBill); got != poBranch {
		t.Errorf("the purchase order bill's branch after the cycle = %s, want %s", got, poBranch)
	}
}
