// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package invoice_test

// Migration 097 on rows that exist (recipe step 3, ADR 0005 section 13, C2-3):
// the schema up to 096 is built in a scratch database, invoices and credit memos
// are written in the shape the base left them (OVERDUE statuses, free text
// terms, a timestamp due date, PENDING and APPLIED credit memos), then 097 is
// applied and each backfill is read back: the gapless numbers in (created_at,
// id) order and the counter past them, OVERDUE mapped by the payments recorded,
// the due date to a branch date, the terms by id with the discount snapshot, the
// text columns dropped, the void columns, the credit memo statuses, totals,
// currency, branch, numbers and the ADJUST line. The down file then rolls the
// shape back and the migration applies again.

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
	"github.com/jackc/pgx/v5/pgconn"
)

func scratch097(t *testing.T) (*pgx.Conn, *[]string) {
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
	name := "gv1_c23mig_" + hex.EncodeToString(suffix)
	if _, err := admin.Exec(ctx, fmt.Sprintf(`CREATE DATABASE %q`, name)); err != nil {
		admin.Close(ctx)
		t.Skipf("cannot create a scratch database: %v", err)
	}
	t.Cleanup(func() {
		_, _ = admin.Exec(context.Background(), fmt.Sprintf(`DROP DATABASE %q WITH (FORCE)`, name))
		admin.Close(ctx)
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

func files097(t *testing.T) (before []string, target string) {
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
		case strings.HasPrefix(base, "097_"):
			target = f
		case base < "097_":
			before = append(before, f)
		}
	}
	if target == "" {
		t.Fatal("migration 097 not found")
	}
	return before, target
}

func apply097(t *testing.T, conn *pgx.Conn, file string) {
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

const (
	m97Cust    = "00000000-0000-0000-0000-0000000097c1"
	m97CustCAD = "00000000-0000-0000-0000-0000000097c2"
	m97Prod    = "00000000-0000-0000-0000-0000000097d1"
	m97OLine   = "00000000-0000-0000-0000-0000000097b1"
	m97Order   = "00000000-0000-0000-0000-0000000097a1"
	m97Unpaid  = "00000000-0000-0000-0000-0000000097e1"
	m97Over    = "00000000-0000-0000-0000-0000000097e2" // OVERDUE with a payment recorded
	m97Late    = "00000000-0000-0000-0000-0000000097e3" // OVERDUE with none
	m97Paid    = "00000000-0000-0000-0000-0000000097e4"
	m97Void    = "00000000-0000-0000-0000-0000000097e5"
	m97Own     = "00000000-0000-0000-0000-0000000097e6" // free text terms that match nothing
	m97Pending = "00000000-0000-0000-0000-0000000097f1"
	m97Applied = "00000000-0000-0000-0000-0000000097f2" // names an invoice
	m97Loose   = "00000000-0000-0000-0000-0000000097f3" // names none
	m97VoidCM  = "00000000-0000-0000-0000-0000000097f4"
)

func seed097(t *testing.T, conn *pgx.Conn) {
	t.Helper()
	branch := `(SELECT id FROM locations WHERE type = 'BRANCH' LIMIT 1)`
	sql := fmt.Sprintf(`
		UPDATE payment_terms SET discount_percent = 2, discount_days = 10 WHERE code = 'NET30';
		UPDATE system_settings SET value = 'USD' WHERE key = 'currency.default';
		INSERT INTO customers (id, name, account_number, primary_branch_id, currency) VALUES
			('%[1]s','Legacy Co','M97A',%[2]s,NULL), ('%[3]s','Canadian Co','M97B',%[2]s,'CAD');
		INSERT INTO products (id, sku, description, uom_primary, base_price) VALUES ('%[4]s','M97-STUD','stud','PCS',5.5);
		INSERT INTO orders (id, customer_id, status, total_amount, branch_id, currency, delivery_type, created_at)
			VALUES ('%[5]s','%[1]s','FULFILLED',55,%[2]s,'USD','PICKUP', now() - interval '10 days');
		INSERT INTO order_lines (id, order_id, product_id, quantity, unit_price, description, uom, price_uom, uom_qty, price_uom_qty, line_total, priced_unit_price, override_reason, price_adjusted_by, position, created_at)
			VALUES ('%[6]s','%[5]s','%[4]s',10,5.5,'stud','PCS','PCS',1,1,55,5.25,'matched a quote','clerk',0,now());
		INSERT INTO invoices (id, order_id, customer_id, status, total_amount, subtotal, tax_amount, payment_terms, due_date, branch_id, created_at, updated_at) VALUES
			('%[7]s','%[5]s','%[1]s','UNPAID',100,100,0,'NET30', now() + interval '20 days', %[2]s, now() - interval '9 days', now()),
			('%[8]s',NULL,'%[1]s','OVERDUE',100,100,0,'Net 45', now() - interval '10 days', %[2]s, now() - interval '8 days', now()),
			('%[9]s',NULL,'%[1]s','OVERDUE',100,100,0,'NET30', now() - interval '10 days', %[2]s, now() - interval '7 days', now()),
			('%[10]s',NULL,'%[1]s','PAID',100,100,0,'COD', now() - interval '30 days', %[2]s, now() - interval '6 days', now()),
			('%[11]s',NULL,'%[1]s','VOID',100,100,0,'NET30', NULL, %[2]s, now() - interval '5 days', now()),
			('%[12]s',NULL,'%[3]s','UNPAID',100,100,0,'2%% 10 net 45 eom', now() + interval '5 days', %[2]s, now() - interval '4 days', now());
		INSERT INTO invoice_lines (invoice_id, order_line_id, product_id, quantity, price_each, created_at, description, uom, price_uom, uom_qty, price_uom_qty, unit_price, line_total)
			VALUES ('%[7]s','%[6]s','%[4]s',10,5.5,now(),'stud','PCS','PCS',1,1,5.5,55);
		INSERT INTO payments (invoice_id, amount, method, reference) VALUES ('%[8]s', 40, 'CHECK', 'P1');
		INSERT INTO credit_memos (id, invoice_id, customer_id, amount, reason, status, created_at, applied_at) VALUES
			('%[13]s',NULL,'%[1]s',12.5,'pending memo','PENDING', now() - interval '3 days', NULL),
			('%[14]s','%[7]s','%[1]s',20,'applied to an invoice','APPLIED', now() - interval '2 days', now()),
			('%[15]s',NULL,'%[3]s',30,'applied, no invoice','APPLIED', now() - interval '1 days', now()),
			('%[16]s',NULL,'%[1]s',5,'void memo','VOID', now() - interval '12 hours', NULL);
	`, m97Cust, branch, m97CustCAD, m97Prod, m97Order, m97OLine, m97Unpaid, m97Over, m97Late, m97Paid, m97Void, m97Own, m97Pending, m97Applied, m97Loose, m97VoidCM)
	if _, err := conn.Exec(context.Background(), sql); err != nil {
		t.Fatalf("legacy rows: %v", err)
	}
}

func TestMigration097_BackfillsRowsThatExist(t *testing.T) {
	conn, notices := scratch097(t)
	before, target := files097(t)
	for _, f := range before {
		apply097(t, conn, f)
	}
	seed097(t, conn)
	apply097(t, conn, target)
	_ = notices

	// the numbers: IN- in (created_at, id) order, the counter set past them
	var numbers []string
	rows, err := conn.Query(context.Background(), `SELECT number FROM invoices ORDER BY created_at, id`)
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var n string
		_ = rows.Scan(&n)
		numbers = append(numbers, n)
	}
	rows.Close()
	if want := "[IN-000001 IN-000002 IN-000003 IN-000004 IN-000005 IN-000006]"; fmt.Sprint(numbers) != want {
		t.Errorf("invoice numbers = %v, want %s in creation order", numbers, want)
	}
	if got := scalar[int64](t, conn, `SELECT next_value FROM document_counters WHERE series = 'invoice'`); got != 7 {
		t.Errorf("invoice counter = %d, want 7", got)
	}
	if got := scalar[string](t, conn, `SELECT invoice_next_number()`); got != "IN-000007" {
		t.Errorf("the next mint = %s, want IN-000007", got)
	}

	// OVERDUE is gone: PARTIAL where a payment is recorded, else UNPAID
	for id, want := range map[string]string{m97Unpaid: "UNPAID", m97Over: "PARTIAL", m97Late: "UNPAID", m97Paid: "PAID", m97Void: "VOID"} {
		if got := scalar[string](t, conn, `SELECT status FROM invoices WHERE id = $1`, id); got != want {
			t.Errorf("invoice %s status = %s, want %s", id[len(id)-2:], got, want)
		}
	}
	if _, err := conn.Exec(context.Background(), `UPDATE invoices SET status = 'OVERDUE' WHERE id = $1`, m97Unpaid); err == nil {
		t.Error("the status CHECK still accepts OVERDUE")
	}

	// due_date is a date in the branch's calendar; the void row has none
	if got := scalar[string](t, conn, `SELECT pg_typeof(due_date)::text FROM invoices WHERE id = $1`, m97Unpaid); got != "date" {
		t.Errorf("due_date type = %s, want date", got)
	}
	if got := scalar[*string](t, conn, `SELECT due_date::text FROM invoices WHERE id = $1`, m97Void); got != nil {
		t.Errorf("a null due date became %v", *got)
	}

	// terms by id: the legacy text mapped without changing meaning, the discount snapshot
	terms := func(id string) string {
		return scalar[string](t, conn, `SELECT t.code FROM invoices i JOIN payment_terms t ON t.id = i.payment_terms_id WHERE i.id = $1`, id)
	}
	if terms(m97Unpaid) != "NET30" || terms(m97Over) != "NET45" || terms(m97Paid) != "COD" {
		t.Errorf("terms = %s %s %s, want NET30 NET45 COD", terms(m97Unpaid), terms(m97Over), terms(m97Paid))
	}
	if own := terms(m97Own); own == "NET30" || own == "NET45" {
		t.Errorf("text that matches nothing took %s, want a term of its own", own)
	}
	if got := scalar[string](t, conn, `SELECT discount_percent::text || ' ' || (discount_due_date - invoice_date)::text FROM invoices WHERE id = $1`, m97Unpaid); got != "2.0000 10" {
		t.Errorf("discount snapshot = %q, want 2.0000 10 (NET30 carried a 2 percent 10 day discount)", got)
	}
	if got := scalar[*string](t, conn, `SELECT discount_percent::text FROM invoices WHERE id = $1`, m97Paid); got != nil {
		t.Errorf("COD took a discount %v", *got)
	}
	for _, c := range []struct{ table, col string }{{"invoices", "payment_terms"}, {"customers", "payment_terms"}} {
		if n := scalar[int](t, conn, `SELECT count(*) FROM information_schema.columns WHERE table_name = $1 AND column_name = $2`, c.table, c.col); n != 0 {
			t.Errorf("%s.%s survived the migration", c.table, c.col)
		}
	}

	// void columns, revision, the price audit trail on the invoice line
	if got := scalar[bool](t, conn, `SELECT voided_at IS NOT NULL AND voided_on IS NOT NULL AND revision = 1 FROM invoices WHERE id = $1`, m97Void); !got {
		t.Error("the void invoice has no void columns")
	}
	if got := scalar[string](t, conn, `SELECT priced_unit_price::text || '/' || override_reason || '/' || price_adjusted_by FROM invoice_lines WHERE invoice_id = $1`, m97Unpaid); got != "5.2500/matched a quote/clerk" {
		t.Errorf("invoice line audit trail = %q, want it copied from the order line", got)
	}

	// credit memos
	cm := func(id string) string {
		return scalar[string](t, conn, `SELECT status || '|' || COALESCE(number, '-') || '|' || currency || '|' || total_amount::text || '|' || subtotal::text || '|' || reason_code FROM credit_memos WHERE id = $1`, id)
	}
	for id, want := range map[string]string{
		m97Pending: "DRAFT|-|USD|-12.50|-12.50|OTHER",
		m97Applied: "APPLIED|CM-000001|USD|-20.00|-20.00|OTHER",
		m97Loose:   "OPEN|CM-000002|CAD|-30.00|-30.00|OTHER",
		m97VoidCM:  "VOID|CM-000003|USD|-5.00|-5.00|OTHER",
	} {
		if got := cm(id); got != want {
			t.Errorf("credit memo %s = %q, want %q", id[len(id)-2:], got, want)
		}
	}
	if got := scalar[int64](t, conn, `SELECT next_value FROM document_counters WHERE series = 'credit_memo'`); got != 4 {
		t.Errorf("credit memo counter = %d, want 4 (the draft took no number)", got)
	}
	if got := scalar[bool](t, conn, `SELECT branch_id = (SELECT branch_id FROM invoices WHERE id = $2) AND memo_date IS NOT NULL FROM credit_memos WHERE id = $1`, m97Applied, m97Unpaid); !got {
		t.Error("an invoice's credit memo did not take its branch")
	}
	if got := scalar[bool](t, conn, `SELECT voided_at IS NOT NULL AND voided_on IS NOT NULL FROM credit_memos WHERE id = $1`, m97VoidCM); !got {
		t.Error("the void credit memo has no void columns")
	}
	// one ADJUST charge line each: quantity -1 EA, unit price the amount, untaxed
	if got := scalar[string](t, conn, `SELECT l.line_type || '|' || cc.code || '|' || l.quantity::text || '|' || l.uom || '|' || l.unit_price::text || '|' || l.line_total::text || '|' || l.taxable::text
		FROM credit_memo_lines l JOIN charge_codes cc ON cc.id = l.charge_code_id WHERE l.credit_memo_id = $1`, m97Applied); got != "CHARGE|ADJUST|-1.0000|EA|20.0000|-20.00|false" {
		t.Errorf("the migrated credit memo line = %q", got)
	}
	if n := scalar[int](t, conn, `SELECT count(*) FROM credit_memo_lines`); n != 4 {
		t.Errorf("%d credit memo lines, want one per memo (4)", n)
	}
	// a draft carries no number, a posted memo does (the CHECK)
	if _, err := conn.Exec(context.Background(), `UPDATE credit_memos SET number = 'CM-9' WHERE id = $1`, m97Pending); err == nil {
		t.Error("a draft was given a number")
	}
	if _, err := conn.Exec(context.Background(), `UPDATE credit_memos SET number = NULL WHERE id = $1`, m97Applied); err == nil {
		t.Error("a posted memo lost its number")
	}

	// the down file rolls the shape back; the migration then applies again
	down, err := os.ReadFile("../../migrations/down/097_invoices_wire_contract_down.sql")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := conn.Exec(context.Background(), string(down)); err != nil {
		t.Fatalf("down: %v", err)
	}
	for _, c := range []struct{ table, col string }{{"invoices", "number"}, {"credit_memos", "number"}, {"invoices", "payment_terms_id"}} {
		if n := scalar[int](t, conn, `SELECT count(*) FROM information_schema.columns WHERE table_name = $1 AND column_name = $2`, c.table, c.col); n != 0 {
			t.Errorf("%s.%s survived the down file", c.table, c.col)
		}
	}
	if got := scalar[string](t, conn, `SELECT payment_terms FROM invoices WHERE id = $1`, m97Over); got != "NET45" {
		t.Errorf("the down file restored the terms text as %q, want NET45", got)
	}
	if got := scalar[string](t, conn, `SELECT status FROM credit_memos WHERE id = $1`, m97Pending); got != "PENDING" {
		t.Errorf("the down file restored a draft as %s, want PENDING", got)
	}
	apply097(t, conn, target)
	if got := scalar[int64](t, conn, `SELECT count(*) FROM invoices WHERE number LIKE 'IN-%'`); got != 6 {
		t.Errorf("after down and up again %d invoices are numbered, want 6", got)
	}
}
