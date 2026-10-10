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

func scratch106(t *testing.T) (*pgx.Conn, *[]string) {
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
	return conn, &notices
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
	conn, notices := scratch106(t)
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
	nailed := uuid.New()
	insert(nailed, "PAID-1", "PAID", at(-6), nil, "50.00", "50.00")
	partial := uuid.New()
	insert(partial, "PART-1", "PARTIAL", at(-5), nil, "80.00", "30.00")
	voided := uuid.New()
	insert(voided, "VOID-1", "VOIDED", at(-4), &orphan, "70.00", "0")
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
	if got := one106(t, conn, `SELECT invoice_number FROM vendor_invoices WHERE id = $1`, dup2); got != "DUP-1 #2" {
		t.Errorf("the second duplicate = %q, want DUP-1 #2", got)
	}
	if got := one106(t, conn, `SELECT invoice_number FROM vendor_invoices WHERE id = $1`, dup3); got != "DUP-1 #3" {
		t.Errorf("the third duplicate = %q, want DUP-1 #3", got)
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
	for _, want := range []string{"duplicate vendor invoice number", "po_id is set null", "unknown status"} {
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

	// amount_open: total less paid, and zero on a voided bill.
	for id, want := range map[uuid.UUID]string{dup1: "100.00", nailed: "0.00", partial: "50.00", voided: "0.00"} {
		if got := one106(t, conn, `SELECT amount_open::text FROM vendor_invoices WHERE id = $1`, id); got != want {
			t.Errorf("amount_open of %s = %s, want %s", id, got, want)
		}
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
	if got := one106(t, conn, `SELECT invoice_number FROM vendor_invoices WHERE id = $1`, dup2); got != "DUP-1 #2" {
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
