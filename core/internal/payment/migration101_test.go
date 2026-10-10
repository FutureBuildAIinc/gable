// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package payment_test

// Migration 101 on rows that exist (recipe step 3, ADR 0005 section 13, C2-4):
// the schema up to 098 is built in a scratch database, legacy payments, refunds,
// credit memos, deposits and their applications are written in the shape the
// base left them, then 101 is applied and every backfill is read back: the
// worked case (150 paid, 100 applied, 70 refunded), the over-applied invoice,
// void and mis-stated invoices, credit memos that name an invoice, deposits in
// every state, the totals against a seeded subledger, the constraints and the
// trigger. The migration is applied a second time (a no-op), the down file
// rolls the shape back, and the migration applies again to the same result.

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

	"github.com/gablelbm/gable/internal/testutil"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

func scratch101(t *testing.T) (*pgx.Conn, *[]string) {
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
	name := "gv1_c24mig_" + hex.EncodeToString(suffix)
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

func files101(t *testing.T) (before []string, target, down string) {
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
		case strings.HasPrefix(base, "101_"):
			target = f
		case base < "101_":
			before = append(before, f)
		}
	}
	if target == "" {
		t.Fatal("migration 101 not found")
	}
	return before, target, "../../migrations/down/101_payments_and_ar_down.sql"
}

func apply101(t *testing.T, conn *pgx.Conn, file string) {
	t.Helper()
	sql, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := conn.Exec(context.Background(), string(sql)); err != nil {
		t.Fatalf("applying %s: %v", filepath.Base(file), err)
	}
}

func one101[T any](t *testing.T, conn *pgx.Conn, sql string, args ...any) T {
	t.Helper()
	var v T
	if err := conn.QueryRow(context.Background(), sql, args...).Scan(&v); err != nil {
		t.Fatalf("%s: %v", sql, err)
	}
	return v
}

// strs runs a query whose every row is one text value.
func strs101(t *testing.T, conn *pgx.Conn, sql string, args ...any) []string {
	t.Helper()
	rows, err := conn.Query(context.Background(), sql, args...)
	if err != nil {
		t.Fatalf("%s: %v", sql, err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var s string
		if err := rows.Scan(&s); err != nil {
			t.Fatal(err)
		}
		out = append(out, s)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return out
}

func refuses101(t *testing.T, conn *pgx.Conn, constraint, sql string) {
	t.Helper()
	ctx := context.Background()
	tx, err := conn.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(ctx)
	_, err = tx.Exec(ctx, sql)
	var pg *pgconn.PgError
	if !errors.As(err, &pg) || pg.ConstraintName != constraint {
		t.Errorf("expected constraint %s to refuse %q, got %v", constraint, sql, err)
	}
}

func accepts101(t *testing.T, conn *pgx.Conn, sql string) {
	t.Helper()
	ctx := context.Background()
	tx, err := conn.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(ctx)
	if _, err := tx.Exec(ctx, sql); err != nil {
		t.Errorf("expected %q to be accepted: %v", sql, err)
	}
}

// ids: a name per seeded row. The order of the list is the order of the ids,
// which the tie breaks below rely on (P2b sorts before P2a, D5h before D5g).
var m101Names = []string{
	"B1", "B2", "C1", "C2", "C3", "C4", "C5", "C5e", "C5f", "C6", "C7", "C9", "CX",
	"I1", "I1b", "I2", "I3v", "I3p", "I3u", "I3z", "I4a", "I4b", "I4c", "I5a", "I5c", "I5d", "I5h", "I5e2", "I5e1", "I5f", "I6", "I7", "I9",
	"P1", "P6", "P2b", "P2a", "P3v", "P3u1", "P3u2", "P4b", "P4c", "P5d",
	"R1",
	"M4a", "M4b", "M4c", "M4d", "M4e", "M4f",
	"D1", "D5a", "D5b", "D5c", "D5d", "D5e", "D5f", "D5h", "D5g", "D5m", "DX",
	"E1r", "E1a", "E5ar", "E5b", "E5c", "E5d",
	"I3w", "P3w", "I5e0", "R2",
}

var m101ID = func() map[string]string {
	m := map[string]string{}
	for i, n := range m101Names {
		m[n] = fmt.Sprintf("00000000-0000-0000-0000-%012x", 0x101000+i)
	}
	return m
}()

func nm101(id string) string {
	for n, v := range m101ID {
		if v == id {
			return n
		}
	}
	if id == "" {
		return ""
	}
	return "excess"
}

func fill101(sql string) string {
	var pairs []string
	for n, id := range m101ID {
		pairs = append(pairs, "{"+n+"}", id)
	}
	return strings.NewReplacer(pairs...).Replace(sql)
}

const seed101SQL = `
INSERT INTO locations (id, type, code, name, timezone) VALUES
	('{B1}','BRANCH','M101NZ','Auckland yard','Pacific/Auckland'),
	('{B2}','BRANCH','M101LA','Los Angeles yard','America/Los_Angeles');
INSERT INTO customers (id, name, account_number, primary_branch_id, currency) VALUES
	('{C1}','Mig One','M101-1','{B1}',NULL), ('{C2}','Mig Two','M101-2','{B1}',NULL),
	('{C3}','Mig Three','M101-3','{B1}',NULL), ('{C4}','Mig Four','M101-4','{B1}',NULL),
	('{C5}','Mig Five','M101-5','{B1}',NULL), ('{C5e}','Mig Five E','M101-5E','{B1}',NULL),
	('{C5f}','Mig Five F','M101-5F','{B1}',NULL), ('{C6}','Mig Six','M101-6','{B2}',NULL),
	('{C7}','Mig Seven','M101-7','{B1}','CAD'), ('{C9}','Mig Nine','M101-9','{B1}',NULL);

INSERT INTO invoices (id, customer_id, status, total_amount, subtotal, tax_amount, branch_id, currency, created_at, updated_at, invoice_date, due_date, paid_at, voided_at, voided_on) VALUES
	('{I1}','{C1}','PAID',100,100,0,'{B1}','USD','2031-03-09 08:00:00+00','2031-03-09 08:00:00+00','2031-03-09','2031-04-08','2031-03-10 20:30:00+00',NULL,NULL),
	('{I1b}','{C1}','PAID',60,60,0,'{B1}','USD','2031-03-09 08:00:00+00','2031-03-09 08:00:00+00','2031-03-09','2031-04-08','2031-03-09 08:00:00+00',NULL,NULL),
	('{I2}','{C2}','UNPAID',100,100,0,'{B1}','USD','2031-03-09 08:00:00+00','2031-03-09 08:00:00+00','2031-03-09','2031-04-08',NULL,NULL,NULL),
	('{I3v}','{C3}','VOID',100,100,0,'{B1}','USD','2031-03-09 08:00:00+00','2031-03-09 08:00:00+00','2031-03-09','2031-04-08',NULL,'2031-03-09 09:00:00+00','2031-03-09'),
	('{I3w}','{C3}','WRITTEN_OFF',60,60,0,'{B1}','USD','2031-03-09 08:00:00+00','2031-03-09 08:00:00+00','2031-03-09','2031-04-08',NULL,NULL,NULL),
	('{I5e0}','{C5e}','PAID',40,40,0,'{B1}','USD','2031-03-09 08:00:00+00','2031-03-09 08:00:00+00','2031-03-09','2031-03-01','2031-03-09 08:30:00+00',NULL,NULL),
	('{I3p}','{C3}','PAID',80,80,0,'{B1}','USD','2031-03-09 08:00:00+00','2031-03-09 08:00:00+00','2031-03-09','2031-04-08','2031-03-09 08:30:00+00',NULL,NULL),
	('{I3u}','{C3}','UNPAID',90,90,0,'{B1}','USD','2031-03-09 08:00:00+00','2031-03-09 08:00:00+00','2031-03-09','2031-04-08',NULL,NULL,NULL),
	('{I3z}','{C3}','UNPAID',0,0,0,'{B1}','USD','2031-03-09 08:00:00+00','2031-03-09 08:00:00+00','2031-03-09','2031-04-08',NULL,NULL,NULL),
	('{I4a}','{C4}','UNPAID',100,100,0,'{B1}','USD','2031-03-09 08:00:00+00','2031-03-09 08:00:00+00','2031-03-09','2031-04-08',NULL,NULL,NULL),
	('{I4b}','{C4}','UNPAID',100,100,0,'{B1}','USD','2031-03-09 08:00:00+00','2031-03-09 08:00:00+00','2031-03-09','2031-04-08',NULL,NULL,NULL),
	('{I4c}','{C4}','UNPAID',40,40,0,'{B1}','USD','2031-03-09 08:00:00+00','2031-03-09 08:00:00+00','2031-03-09','2031-04-08',NULL,NULL,NULL),
	('{I5a}','{C5}','UNPAID',100,100,0,'{B1}','USD','2031-03-09 08:00:00+00','2031-03-09 08:00:00+00','2031-03-09','2031-04-08',NULL,NULL,NULL),
	('{I5c}','{C5}','UNPAID',100,100,0,'{B1}','USD','2031-03-09 08:00:00+00','2031-03-09 08:00:00+00','2031-03-09','2031-04-08',NULL,NULL,NULL),
	('{I5d}','{C5}','UNPAID',100,100,0,'{B1}','USD','2031-03-09 08:00:00+00','2031-03-09 08:00:00+00','2031-03-09','2031-04-08',NULL,NULL,NULL),
	('{I5h}','{C5}','UNPAID',100,100,0,'{B1}','USD','2031-03-09 08:00:00+00','2031-03-09 08:00:00+00','2031-03-09','2031-04-08',NULL,NULL,NULL),
	('{I5e2}','{C5e}','UNPAID',50,50,0,'{B1}','USD','2031-03-09 08:00:00+00','2031-03-09 08:00:00+00','2031-03-09','2031-05-01',NULL,NULL,NULL),
	('{I5e1}','{C5e}','UNPAID',60,60,0,'{B1}','USD','2031-03-09 08:00:00+00','2031-03-09 08:00:00+00','2031-03-09','2031-04-01',NULL,NULL,NULL),
	('{I5f}','{C5f}','UNPAID',50,50,0,'{B1}','USD','2031-03-09 08:00:00+00','2031-03-09 08:00:00+00','2031-03-09','2031-04-08',NULL,NULL,NULL),
	('{I6}','{C6}','UNPAID',100,100,0,'{B2}','USD','2031-03-09 08:00:00+00','2031-03-09 08:00:00+00','2031-03-09','2031-04-08',NULL,NULL,NULL),
	('{I7}','{C7}','UNPAID',100,100,0,'{B1}','CAD','2031-03-09 08:00:00+00','2031-03-09 08:00:00+00','2031-03-09','2031-04-08',NULL,NULL,NULL),
	('{I9}','{C9}','UNPAID',50,50,0,'{B1}','USD','2031-03-09 08:00:00+00','2031-03-09 08:00:00+00','2031-03-09','2031-04-08',NULL,NULL,NULL);

INSERT INTO payments (id, invoice_id, amount, method, reference, created_at) VALUES
	('{P1}','{I1}',150,'CASH','P1','2031-03-10 20:30:00+00'),
	('{P6}','{I6}',100,'CHECK','P6','2031-03-11 03:00:00+00'),
	('{P2a}','{I2}',100,'CASH','P2a','2031-03-12 10:00:00+00'),
	('{P2b}','{I2}',25,'CHECK','P2b','2031-03-12 10:01:00+00'),
	('{P3v}','{I3v}',30,'ACCOUNT','P3v','2031-03-13 10:00:00+00'),
	('{P3u1}','{I3u}',50,'CASH','P3u1','2031-03-13 10:01:00+00'),
	('{P3u2}','{I3u}',40,'CASH','P3u2','2031-03-13 10:02:00+00'),
	('{P4b}','{I4b}',70,'CASH','P4b','2031-03-14 10:00:00+00'),
	('{P4c}','{I4c}',40,'CASH','P4c','2031-03-14 10:01:00+00'),
	('{P5d}','{I5d}',60,'CARD','P5d','2031-03-14 10:02:00+00'),
	('{P3w}','{I3w}',20,'CASH','P3w','2031-03-14 10:03:00+00');
INSERT INTO payment_refunds (id, payment_id, amount, reason, status, created_at) VALUES
	('{R1}','{P1}',70,'goods returned','COMPLETE','2031-03-10 22:00:00+00'),
	('{R2}','{P4b}',25,'refund asked, never completed','PENDING','2031-03-14 11:00:00+00');

INSERT INTO gl_journal_entries (id, entry_date, memo, source, status) VALUES
	('{E1r}','2031-03-21','deposit receipt','DEPOSIT','POSTED'), ('{E1a}','2031-03-22','deposit applied','DEPOSIT','POSTED'),
	('{E5ar}','2031-03-21','deposit receipt','DEPOSIT','POSTED'), ('{E5b}','2031-03-22','deposit applied','DEPOSIT','POSTED'),
	('{E5c}','2031-03-22','deposit applied twice','DEPOSIT','POSTED'), ('{E5d}','2031-03-22','deposit applied over','DEPOSIT','POSTED');
INSERT INTO gl_journal_lines (journal_entry_id, account_id, debit, credit)
SELECT v.entry, a.id, v.dr, v.cr
FROM (VALUES
	('{E1r}'::uuid,'1010',6000,0), ('{E1r}','2200',0,6000), ('{E1a}','2200',6000,0), ('{E1a}','1020',0,6000),
	('{E5ar}','1010',10000,0), ('{E5ar}','2200',0,10000), ('{E5b}','2200',3000,0), ('{E5b}','1020',0,3000),
	('{E5c}','2200',5000,0), ('{E5c}','1020',0,5000), ('{E5d}','2200',8000,0), ('{E5d}','1020',0,8000)
) v(entry, code, dr, cr) JOIN gl_accounts a ON a.code = v.code;

INSERT INTO credit_memos (id, invoice_id, customer_id, amount, reason, status, created_at, applied_at, number, currency, branch_id, reason_code, subtotal, tax_amount, total_amount, memo_date, voided_at, voided_on) VALUES
	('{M4a}','{I4a}','{C4}',100,'applied, invoice takes all','APPLIED','2031-03-15 10:00:00+00','2031-03-15 10:00:00+00','CM-000001','USD','{B1}','OTHER',-100,0,-100,'2031-03-16',NULL,NULL),
	('{M4b}','{I4b}','{C4}',50,'applied, invoice takes part','APPLIED','2031-03-15 10:01:00+00','2031-03-15 10:01:00+00','CM-000002','USD','{B1}','OTHER',-50,0,-50,'2031-03-16',NULL,NULL),
	('{M4c}','{I4c}','{C4}',15,'applied, invoice takes none','APPLIED','2031-03-15 10:02:00+00','2031-03-15 10:02:00+00','CM-000003','USD','{B1}','OTHER',-15,0,-15,'2031-03-16',NULL,NULL),
	('{M4d}',NULL,'{C4}',30,'open, no invoice','OPEN','2031-03-15 10:03:00+00',NULL,'CM-000004','USD','{B1}','OTHER',-30,0,-30,'2031-03-16',NULL,NULL),
	('{M4e}',NULL,'{C4}',12.5,'draft','DRAFT','2031-03-15 10:04:00+00',NULL,NULL,'USD','{B1}','OTHER',-12.5,0,-12.5,'2031-03-16',NULL,NULL),
	('{M4f}',NULL,'{C4}',5,'void','VOID','2031-03-15 10:05:00+00',NULL,'CM-000005','USD','{B1}','OTHER',-5,0,-5,'2031-03-16','2031-03-16 10:00:00+00','2031-03-16');
UPDATE document_counters SET next_value = 6 WHERE series = 'credit_memo';

INSERT INTO customer_deposits (id, customer_id, branch_id, amount, applied_amount, status, method, reference, note, gl_entry_id, created_at, updated_at) VALUES
	('{D1}','{C1}','{B1}',60,60,'APPLIED','CASH','REF-D1','','{E1r}','2031-03-20 12:00:00+00','2031-03-20 12:00:00+00'),
	('{D5a}','{C5}','{B1}',100,0,'OPEN','CASH','','front counter','{E5ar}','2031-03-20 12:01:00+00','2031-03-20 12:01:00+00'),
	('{D5b}','{C5}','{B1}',100,30,'OPEN','CASH','','',NULL,'2031-03-20 12:02:00+00','2031-03-20 12:02:00+00'),
	('{D5c}','{C5}','{B1}',50,50,'APPLIED','CASH','','',NULL,'2031-03-20 12:03:00+00','2031-03-20 12:03:00+00'),
	('{D5d}','{C5}','{B1}',80,80,'APPLIED','CASH','','',NULL,'2031-03-20 12:04:00+00','2031-03-20 12:04:00+00'),
	('{D5e}','{C5e}','{B1}',90,90,'APPLIED','CASH','','',NULL,'2031-03-20 12:05:00+00','2031-03-20 12:05:00+00'),
	('{D5f}','{C5f}','{B1}',70,70,'APPLIED','CASH','','',NULL,'2031-03-20 12:06:00+00','2031-03-20 12:06:00+00'),
	('{D5g}','{C5}','{B1}',100,0,'REFUNDED','check','','',NULL,'2031-03-20 12:07:00+00','2031-03-20 12:30:00+00'),
	('{D5h}','{C5}','{B1}',100,40,'REFUNDED','CARD','','',NULL,'2031-03-20 12:07:00+00','2031-03-20 12:31:00+00'),
	('{D5m}','{C7}',NULL,25,0,'OPEN','wire','','',NULL,'2031-03-20 12:08:00+00','2031-03-20 12:08:00+00'),
	('{DX}','{CX}','{B1}',10,0,'OPEN','CASH','','',NULL,'2031-03-20 12:09:00+00','2031-03-20 12:09:00+00');
INSERT INTO customer_deposit_applications (deposit_id, customer_id, amount, invoice_id, gl_entry_id, created_at) VALUES
	('{D1}','{C1}',60,'{I1b}','{E1a}','2031-03-21 12:30:00+00'),
	('{D5b}','{C5}',30,'{I5a}','{E5b}','2031-03-21 12:30:00+00'),
	('{D5c}','{C5}',20,'{I5c}','{E5c}','2031-03-21 12:30:00+00'),
	('{D5c}','{C5}',30,'{I5c}','{E5c}','2031-03-21 12:31:00+00'),
	('{D5d}','{C5}',80,'{I5d}','{E5d}','2031-03-21 12:30:00+00'),
	('{D5e}','{C5e}',90,NULL,NULL,'2031-03-21 12:30:00+00'),
	('{D5f}','{C5f}',70,NULL,NULL,'2031-03-21 12:30:00+00'),
	('{D5h}','{C5}',40,'{I5h}',NULL,'2031-03-21 12:30:00+00');

-- the subledger as the base wrote it: consistent for C1 (invoice, payment, refund, deposit application), drifted for C9
INSERT INTO customer_transactions (customer_id, type, amount, balance_after, reference_id, description, created_at) VALUES
	('{C1}','INVOICE',10000,10000,'{I1}','invoice','2031-03-09 09:00:00+00'),
	('{C1}','INVOICE',6000,16000,'{I1b}','invoice','2031-03-09 09:01:00+00'),
	('{C1}','PAYMENT',-15000,1000,'{P1}','payment','2031-03-10 20:30:00+00'),
	('{C1}','PAYMENT',7000,8000,'{R1}','refund','2031-03-10 22:00:00+00'),
	('{C1}','PAYMENT',-6000,2000,'{D1}','deposit applied','2031-03-21 12:30:00+00'),
	('{C9}','INVOICE',5000,5000,'{I9}','invoice','2031-03-09 09:02:00+00'),
	('{C9}','PAYMENT',-1000,4000,NULL,'payment with no document','2031-03-10 09:00:00+00');
UPDATE customers SET balance_due = 20 WHERE id = '{C1}';
UPDATE customers SET balance_due = 40 WHERE id = '{C9}';
`

type mig101 struct {
	conn    *pgx.Conn
	notices *[]string
	target  string
	down    string
}

// built101 builds the schema up to 098 and seeds the legacy rows; the
// migration is not applied.
func built101(t *testing.T) *mig101 {
	t.Helper()
	conn, notices := scratch101(t)
	before, target, down := files101(t)
	for _, f := range before {
		apply101(t, conn, f)
	}
	if _, err := conn.Exec(context.Background(), fill101(seed101SQL)); err != nil {
		t.Fatalf("legacy rows: %v", err)
	}
	return &mig101{conn: conn, notices: notices, target: target, down: down}
}

func migrated101(t *testing.T) *mig101 {
	t.Helper()
	m := built101(t)
	apply101(t, m.conn, m.target)
	return m
}

func (m *mig101) applications(t *testing.T) []string {
	t.Helper()
	rows, err := m.conn.Query(context.Background(), `SELECT kind, COALESCE(payment_id::text, ''), COALESCE(credit_memo_id::text, ''), invoice_id::text,
		amount::text, reversed_at IS NOT NULL, gl_entry_id IS NOT NULL FROM ar_applications`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var kind, pay, memo, inv, amt string
		var rev, entry bool
		if err := rows.Scan(&kind, &pay, &memo, &inv, &amt, &rev, &entry); err != nil {
			t.Fatal(err)
		}
		state := "live"
		if rev {
			state = "reversed"
		}
		s := fmt.Sprintf("%s %s>%s %s %s", kind, nm101(pay)+nm101(memo), nm101(inv), amt, state)
		if entry {
			s += " entry"
		}
		out = append(out, s)
	}
	sort.Strings(out)
	return out
}

func TestMigration101_BackfillsRowsThatExist(t *testing.T) {
	m := built101(t)
	conn := m.conn
	ctx := context.Background()
	apply101(t, conn, m.target)

	// the deposit whose customer does not exist is skipped, with a notice
	var skipNotices []string
	for _, n := range *m.notices {
		if strings.Contains(n, "migration 101") {
			skipNotices = append(skipNotices, n)
		}
	}
	if len(skipNotices) != 1 || !strings.Contains(skipNotices[0], "1 deposit(s) name no customer") {
		t.Errorf("notices = %v, want exactly one skipped deposit notice", skipNotices)
	}
	if n := one101[int](t, conn, `SELECT count(*) FROM payments WHERE id = $1`, m101ID["DX"]); n != 0 {
		t.Error("the deposit with no customer became a payment")
	}
	if n := one101[int](t, conn, `SELECT count(*) FROM customer_deposits_legacy`); n != 11 {
		t.Errorf("customer_deposits_legacy holds %d rows, want all 11 (the skipped one included)", n)
	}
	if n := one101[int](t, conn, `SELECT count(*) FROM customer_deposit_applications_legacy`); n != 8 {
		t.Errorf("customer_deposit_applications_legacy holds %d rows, want 8", n)
	}
	if one101[bool](t, conn, `SELECT to_regclass('customer_deposits') IS NOT NULL OR to_regclass('customer_deposit_applications') IS NOT NULL`) {
		t.Error("the old deposit table names still exist")
	}

	// every application, exactly (case 1 to 5): the worked case reversed 100 and live 80
	wantApps := []string{
		"CREDIT_MEMO M4a>I4a 100.00 live",
		"CREDIT_MEMO M4b>I4b 30.00 live",
		"PAYMENT D1>I1b 60.00 live entry",
		"PAYMENT D5b>I5a 30.00 live entry",
		"PAYMENT D5c>I5c 20.00 live",
		"PAYMENT D5c>I5c 30.00 live",
		"PAYMENT D5d>I5d 40.00 live",
		"PAYMENT D5e>I5e0 40.00 live",
		"PAYMENT D5e>I5e1 50.00 live",
		"PAYMENT D5f>I5f 50.00 live",
		"PAYMENT D5h>I5h 40.00 live",
		"PAYMENT P1>I1 100.00 reversed",
		"PAYMENT P1>I1 80.00 live",
		"PAYMENT P2a>I2 100.00 live",
		"PAYMENT P3u1>I3u 50.00 live",
		"PAYMENT P3u2>I3u 40.00 live",
		"PAYMENT P4b>I4b 70.00 live",
		"PAYMENT P4c>I4c 40.00 live",
		"PAYMENT P5d>I5d 60.00 live",
		"PAYMENT P6>I6 100.00 live",
	}
	if got := m.applications(t); fmt.Sprint(got) != fmt.Sprint(wantApps) {
		t.Errorf("applications =\n%s\nwant\n%s", strings.Join(got, "\n"), strings.Join(wantApps, "\n"))
	}
	// the reversed and the remaining application of the worked case share one act, same invoice, applied_on the original's
	if got := one101[string](t, conn, `SELECT count(DISTINCT act_id)::text || '|' || count(DISTINCT applied_on)::text || '|' || count(DISTINCT invoice_id)::text
		FROM ar_applications WHERE payment_id = $1`, m101ID["P1"]); got != "1|1|1" {
		t.Errorf("worked case acts|applied_on|invoices = %s, want 1|1|1", got)
	}
	if got := one101[string](t, conn, `SELECT reversed_on::text || '|' || reversed_by || '|' || reversal_reason FROM ar_applications WHERE payment_id = $1 AND reversed_at IS NOT NULL`, m101ID["P1"]); got != "2031-03-11|migration|migrated refund" {
		t.Errorf("the reversed application = %q, want it dated the refund's branch date", got)
	}
	if n := one101[int](t, conn, `SELECT count(DISTINCT act_id) FROM ar_applications WHERE kind = 'PAYMENT' AND payment_id <> $1`, m101ID["P1"]); n < 15 {
		t.Errorf("only %d distinct acts among the other applications, want each its own", n)
	}
	// applied_on: a payment's is its branch-local received date, a deposit application's the local date of its own timestamp
	if got := one101[string](t, conn, `SELECT applied_on::text FROM ar_applications WHERE payment_id = $1`, m101ID["P6"]); got != "2031-03-10" {
		t.Errorf("P6 applied_on = %s, want 2031-03-10 (Los Angeles)", got)
	}
	if got := one101[string](t, conn, `SELECT applied_on::text FROM ar_applications WHERE payment_id = $1`, m101ID["D1"]); got != "2031-03-22" {
		t.Errorf("D1 applied_on = %s, want 2031-03-22 (Auckland)", got)
	}
	// a kept entry is the deposit application's own; a split or capped one is dropped
	if got := one101[string](t, conn, `SELECT gl_entry_id::text FROM ar_applications WHERE payment_id = $1`, m101ID["D1"]); got != m101ID["E1a"] {
		t.Errorf("D1 application entry = %s, want the exact entry", got)
	}

	// invoices: status, amount_open, paid_at
	invWant := map[string]string{
		"I1":   "PARTIAL|20.00|cleared",
		"I1b":  "PAID|0.00|kept",
		"I2":   "PAID|0.00|updated_at",
		"I3v":  "VOID|0.00|none",
		"I3w":  "WRITTEN_OFF|0.00|cleared",
		"I5e0": "PAID|0.00|kept",
		"I3p":  "UNPAID|80.00|cleared",
		"I3u":  "PAID|0.00|updated_at",
		"I3z":  "PAID|0.00|updated_at",
		"I4a":  "PAID|0.00|updated_at",
		"I4b":  "PAID|0.00|updated_at",
		"I4c":  "PAID|0.00|updated_at",
		"I5a":  "PARTIAL|70.00|cleared",
		"I5c":  "PARTIAL|50.00|cleared",
		"I5d":  "PAID|0.00|updated_at",
		"I5h":  "PARTIAL|60.00|cleared",
		"I5e1": "PARTIAL|10.00|cleared",
		"I5e2": "UNPAID|50.00|cleared",
		"I5f":  "PAID|0.00|updated_at",
		"I6":   "PAID|0.00|updated_at",
		"I7":   "UNPAID|100.00|cleared",
		"I9":   "UNPAID|50.00|cleared",
	}
	for name, want := range invWant {
		got := one101[string](t, conn, `SELECT status || '|' || amount_open::text || '|' || CASE
				WHEN paid_at IS NULL THEN CASE WHEN status = 'VOID' THEN 'none' ELSE 'cleared' END
				WHEN paid_at = updated_at THEN 'updated_at' ELSE 'kept' END FROM invoices WHERE id = $1`, m101ID[name])
		if name == "I1b" { // already PAID and unchanged: its paid_at is the seeded one
			got = strings.Replace(got, "updated_at", "kept", 1)
		}
		if got != want {
			t.Errorf("invoice %s = %s, want %s", name, got, want)
		}
	}

	// credit memos: seeded and migrated, by name or by source and reason
	memoWant := map[string]string{
		"M4a": "APPLIED|-100.00|0.00",
		"M4b": "PARTIAL|-50.00|-20.00",
		"M4c": "OPEN|-15.00|-15.00",
		"M4d": "OPEN|-30.00|-30.00",
		"M4e": "DRAFT|-12.50|0.00",
		"M4f": "VOID|-5.00|0.00",
	}
	for name, want := range memoWant {
		got := one101[string](t, conn, `SELECT status || '|' || total_amount::text || '|' || amount_open::text FROM credit_memos WHERE id = $1`, m101ID[name])
		if got != want {
			t.Errorf("credit memo %s = %s, want %s", name, got, want)
		}
	}
	excessWant := map[string]string{
		"P1":  "migrated payment excess|APPLIED|-50.00|0.00",
		"P3v": "migrated payment excess|OPEN|-30.00|-30.00",
		"P3w": "migrated payment excess|OPEN|-20.00|-20.00",
		"P2b": "migrated payment excess|OPEN|-25.00|-25.00",
		"D5d": "migrated deposit application|OPEN|-40.00|-40.00",
		"D5f": "migrated deposit application|OPEN|-20.00|-20.00",
	}
	for name, want := range excessWant {
		got := one101[string](t, conn, `SELECT reason || '|' || status || '|' || total_amount::text || '|' || amount_open::text FROM credit_memos WHERE source_payment_id = $1`, m101ID[name])
		if got != want {
			t.Errorf("excess memo of %s = %s, want %s", name, got, want)
		}
	}
	if n := one101[int](t, conn, `SELECT count(*) FROM credit_memos`); n != 12 {
		t.Errorf("%d credit memos, want 6 seeded + 6 excess", n)
	}
	if n := one101[int](t, conn, `SELECT count(*) FROM credit_memos WHERE source_payment_id IS NOT NULL`); n != 6 {
		t.Errorf("%d memos name a source payment, want only the 6 excess memos", n)
	}
	// each excess memo: numbered from the CM counter, no entry, branch and currency, one ADJUST line
	if got := one101[string](t, conn, `SELECT count(*)::text || '|' || count(DISTINCT number)::text || '|' || min(number) FROM credit_memos WHERE source_payment_id IS NOT NULL AND number ~ '^CM-[0-9]{6}$'`); got != "6|6|CM-000006" {
		t.Errorf("excess memo numbers = %s, want 6 distinct from CM-000006", got)
	}
	if got := one101[int64](t, conn, `SELECT next_value FROM document_counters WHERE series = 'credit_memo'`); got != 12 {
		t.Errorf("credit memo counter = %d, want 12", got)
	}
	if got := one101[string](t, conn, `SELECT (gl_entry_id IS NULL)::text || '|' || currency || '|' || reason_code || '|' || subtotal::text || '|' || tax_amount::text || '|' || memo_date::text || '|' || (branch_id = $2)::text
		FROM credit_memos WHERE source_payment_id = $1`, m101ID["P1"], m101ID["B1"]); got != "true|USD|OTHER|-50.00|0.00|2031-03-11|true" {
		t.Errorf("worked case memo shape = %s", got)
	}
	if got := one101[string](t, conn, `SELECT l.line_type || '|' || cc.code || '|' || l.quantity::text || '|' || l.unit_price::text || '|' || l.line_total::text || '|' || l.taxable::text
		FROM credit_memo_lines l JOIN credit_memos m ON m.id = l.credit_memo_id JOIN charge_codes cc ON cc.id = l.charge_code_id WHERE m.source_payment_id = $1`, m101ID["P1"]); got != "CHARGE|ADJUST|-1.0000|50.0000|-50.00|false" {
		t.Errorf("worked case memo line = %s, want one ADJUST line", got)
	}
	if n := one101[int](t, conn, `SELECT count(*) FROM credit_memo_lines l JOIN credit_memos m ON m.id = l.credit_memo_id WHERE m.source_payment_id IS NOT NULL`); n != 6 {
		t.Errorf("%d lines on excess memos, want one each", n)
	}
	if got := one101[string](t, conn, `SELECT currency FROM credit_memos WHERE source_payment_id = $1`, m101ID["D5d"]); got != "USD" {
		t.Errorf("deposit excess memo currency = %s", got)
	}

	// payments: amount_unapplied and migrated_excess, and the recompute leaves them
	payWant := map[string]string{
		"P1": "0.00|50.00", "P6": "0.00|0.00", "P2a": "0.00|0.00", "P2b": "0.00|25.00", "P3v": "0.00|30.00", "P3w": "0.00|20.00",
		"P3u1": "0.00|0.00", "P3u2": "0.00|0.00", "P4b": "0.00|0.00", "P4c": "0.00|0.00", "P5d": "0.00|0.00",
		"D1": "0.00|0.00", "D5a": "100.00|0.00", "D5b": "70.00|0.00", "D5c": "0.00|0.00", "D5d": "0.00|40.00",
		"D5e": "0.00|0.00", "D5f": "0.00|20.00", "D5g": "0.00|0.00", "D5h": "0.00|0.00", "D5m": "25.00|0.00",
	}
	for name, want := range payWant {
		got := one101[string](t, conn, `SELECT amount_unapplied::text || '|' || migrated_excess::text FROM payments WHERE id = $1`, m101ID[name])
		if got != want {
			t.Errorf("payment %s unapplied|migrated_excess = %s, want %s", name, got, want)
		}
	}
	if n := one101[int](t, conn, `SELECT count(*) FROM payments`); n != 21 {
		t.Errorf("%d payments, want 11 legacy + 10 deposits", n)
	}
	if n := one101[int](t, conn, `SELECT count(*) FROM payments p WHERE p.amount_unapplied IS DISTINCT FROM GREATEST(0, p.amount
		- COALESCE((SELECT SUM(a.amount) FROM ar_applications a WHERE a.payment_id = p.id AND a.kind = 'PAYMENT' AND a.reversed_at IS NULL), 0)
		- COALESCE((SELECT SUM(f.amount) FROM payment_refunds f WHERE f.payment_id = p.id AND f.status = 'COMPLETE'), 0) - p.migrated_excess)`); n != 0 {
		t.Errorf("%d payments change on a recompute of amount_unapplied", n)
	}
	// the worked case in full
	if got := one101[string](t, conn, `SELECT p.amount::text || '|' || p.amount_unapplied::text || '|' || p.migrated_excess::text || '|' || p.invoice_id::text FROM payments p WHERE p.id = $1`, m101ID["P1"]); got != "150.00|0.00|50.00|"+m101ID["I1"] {
		t.Errorf("worked case payment = %s", got)
	}

	// refunds: split 50 to the memo and 20 kept on the payment, both rows of the original, deposit refunds for the rest
	refunds := strs101(t, conn, `SELECT COALESCE(payment_id::text, 'memo') || '|' || COALESCE(credit_memo_id::text, '-') || '|' || amount::text || '|' || method || '|' || refunded_on::text || '|' || reason
		FROM payment_refunds WHERE payment_id = $1 OR credit_memo_id IN (SELECT id FROM credit_memos WHERE source_payment_id = $1) ORDER BY amount`, m101ID["P1"])
	memoP1 := one101[string](t, conn, `SELECT id::text FROM credit_memos WHERE source_payment_id = $1`, m101ID["P1"])
	wantRef := []string{
		m101ID["P1"] + "|-|20.00|CASH|2031-03-11|goods returned",
		"memo|" + memoP1 + "|50.00|CASH|2031-03-11|goods returned",
	}
	if fmt.Sprint(refunds) != fmt.Sprint(wantRef) {
		t.Errorf("worked case refunds = %v, want %v", refunds, wantRef)
	}
	if got := one101[string](t, conn, `SELECT id::text FROM payment_refunds WHERE payment_id = $1`, m101ID["P1"]); got != m101ID["R1"] {
		t.Errorf("the remaining 20 is on row %s, want the original row", got)
	}
	for name, want := range map[string]string{
		"D5g": "100.00|CHECK|2031-03-21|migrated deposit refund|COMPLETE",
		"D5h": "60.00|CARD|2031-03-21|migrated deposit refund|COMPLETE",
	} {
		got := one101[string](t, conn, `SELECT amount::text || '|' || method || '|' || refunded_on::text || '|' || reason || '|' || status FROM payment_refunds WHERE payment_id = $1`, m101ID[name])
		if got != want {
			t.Errorf("refund of deposit %s = %s, want %s", name, got, want)
		}
	}
	if n := one101[int](t, conn, `SELECT count(*) FROM payment_refunds`); n != 5 {
		t.Errorf("%d refund rows, want 5 (20, 50, the pending one untouched, and two deposit refunds; no refund for unrefunded deposits)", n)
	}
	// a refund that never completed returned nothing: the payment keeps its whole application and the row stays as it was
	if got := one101[string](t, conn, `SELECT payment_id::text || '|' || amount::text || '|' || status FROM payment_refunds WHERE id = $1`, m101ID["R2"]); got != m101ID["P4b"]+"|25.00|PENDING" {
		t.Errorf("the pending refund = %s, want it untouched on its payment", got)
	}
	if n := one101[int](t, conn, `SELECT count(*) FROM payment_refunds WHERE method IS NULL OR refunded_on IS NULL`); n != 0 {
		t.Errorf("%d refunds lack a method or a date", n)
	}

	// numbering: PAY- in (created_at, id) order, legacy payments first, deposits after, ties by id
	order := strs101(t, conn, `SELECT id::text FROM payments ORDER BY number`)
	var wantOrder []string
	for _, n := range []string{"P1", "P6", "P2a", "P2b", "P3v", "P3u1", "P3u2", "P4b", "P4c", "P5d", "P3w", "D1", "D5a", "D5b", "D5c", "D5d", "D5e", "D5f", "D5h", "D5g", "D5m"} {
		wantOrder = append(wantOrder, m101ID[n])
	}
	if fmt.Sprint(order) != fmt.Sprint(wantOrder) {
		var names []string
		for _, id := range order {
			names = append(names, nm101(id))
		}
		t.Errorf("payments by number = %v", names)
	}
	if got := one101[string](t, conn, `SELECT min(number) || ' ' || max(number) || ' ' || count(DISTINCT number)::text FROM payments`); got != "PAY-000001 PAY-000021 21" {
		t.Errorf("payment numbers = %s, want PAY-000001 to PAY-000021", got)
	}
	if got := one101[string](t, conn, `SELECT number FROM payments WHERE id = $1`, m101ID["D5h"]); got != "PAY-000019" {
		t.Errorf("D5h number = %s, want PAY-000019 (it sorts before D5g on the tie)", got)
	}
	if got := one101[string](t, conn, `SELECT payment_next_number()`); got != "PAY-000022" {
		t.Errorf("the next payment number = %s, want PAY-000022", got)
	}

	// received_on is the branch-local date of created_at; branch and currency are backfilled
	for name, want := range map[string]string{
		"P1": "2031-03-11|USD|B1", "P6": "2031-03-10|USD|B2", "P2a": "2031-03-12|USD|B1",
		"D1": "2031-03-21|USD|B1", "D5m": "2031-03-21|CAD|B1", "D5c": "2031-03-21|USD|B1",
	} {
		got := one101[string](t, conn, `SELECT p.received_on::text || '|' || p.currency || '|' || (SELECT CASE WHEN p.branch_id = $2 THEN 'B1' WHEN p.branch_id = $3 THEN 'B2' ELSE 'other' END)
			FROM payments p WHERE p.id = $1`, m101ID[name], m101ID["B1"], m101ID["B2"])
		if got != want {
			t.Errorf("payment %s received_on|currency|branch = %s, want %s", name, got, want)
		}
	}
	if got := one101[string](t, conn, `SELECT created_at::date::text FROM payments WHERE id = $1`, m101ID["P1"]); got != "2031-03-10" {
		t.Errorf("setup check: P1 UTC date = %s", got)
	}
	// customer_id comes from the invoice; invoice_id is kept and the column is nullable
	for name, cust := range map[string]string{"P1": "C1", "P2b": "C2", "P5d": "C5", "P6": "C6"} {
		if got := one101[string](t, conn, `SELECT customer_id::text FROM payments WHERE id = $1`, m101ID[name]); got != m101ID[cust] {
			t.Errorf("payment %s customer = %s, want %s", name, nm101(got), cust)
		}
	}
	if n := one101[int](t, conn, `SELECT count(*) FROM payments WHERE invoice_id IS NOT NULL`); n != 11 {
		t.Errorf("%d payments keep an invoice_id, want the 11 legacy ones", n)
	}
	if got := one101[string](t, conn, `SELECT is_nullable FROM information_schema.columns WHERE table_name = 'payments' AND column_name = 'invoice_id'`); got != "YES" {
		t.Errorf("payments.invoice_id nullable = %s", got)
	}

	// deposits became payments with the same id: method, reference, note, entry, order
	for name, want := range map[string]string{
		"D1":  "CASH|REF-D1|-|" + m101ID["E1r"],
		"D5a": "CASH|-|front counter|" + m101ID["E5ar"],
		"D5g": "CHECK|-|-|-",
		"D5m": "OTHER|-|-|-",
	} {
		got := one101[string](t, conn, `SELECT method || '|' || COALESCE(reference, '-') || '|' || COALESCE(notes, '-') || '|' || COALESCE(gl_entry_id::text, '-') || '|' || COALESCE(order_id::text, 'noorder') FROM payments WHERE id = $1`, m101ID[name])
		if got != want+"|noorder" {
			t.Errorf("deposit payment %s = %s, want %s|noorder", name, got, want)
		}
		if got := one101[string](t, conn, `SELECT status || '|' || revision::text || '|' || amount::text FROM payments WHERE id = $1`, m101ID[name]); !strings.HasPrefix(got, "POSTED|1|") {
			t.Errorf("deposit payment %s status|revision = %s", name, got)
		}
	}

	// totals (case 6): C1 agrees document to subledger, C9 was seeded to drift
	for cust, want := range map[string]string{"C1": "2000|2000", "C9": "5000|4000"} {
		got := one101[string](t, conn, `SELECT ((SELECT COALESCE(SUM(amount_open), 0) FROM invoices WHERE customer_id = $1)
			+ (SELECT COALESCE(SUM(amount_open), 0) FROM credit_memos WHERE customer_id = $1))::numeric * 100 || '|'
			|| (SELECT SUM(amount) FROM customer_transactions WHERE customer_id = $1)`, m101ID[cust])
		got = strings.ReplaceAll(got, ".00", "")
		if got != want {
			t.Errorf("customer %s documents|subledger = %s, want %s", cust, got, want)
		}
	}
	drift := strs101(t, conn, `SELECT c.name FROM customers c WHERE EXISTS (SELECT 1 FROM customer_transactions x WHERE x.customer_id = c.id)
		AND (SELECT COALESCE(SUM(amount_open), 0) FROM invoices WHERE customer_id = c.id) * 100
		  + (SELECT COALESCE(SUM(amount_open), 0) FROM credit_memos WHERE customer_id = c.id) * 100
		  <> (SELECT SUM(amount) FROM customer_transactions WHERE customer_id = c.id) ORDER BY c.name`)
	if fmt.Sprint(drift) != "[Mig Nine]" {
		t.Errorf("customers whose documents disagree with the subledger = %v, want only the seeded drift", drift)
	}
	// the migration wrote no subledger row and no journal entry
	if n := one101[int](t, conn, `SELECT count(*) FROM customer_transactions`); n != 7 {
		t.Errorf("%d subledger rows, want the 7 seeded (history moves as data)", n)
	}
	if n := one101[int](t, conn, `SELECT count(*) FROM gl_journal_entries`); n != 6 {
		t.Errorf("%d journal entries, want the 6 seeded", n)
	}

	// accounts
	for code, want := range map[string]string{"4050": "REVENUE|DEBIT", "5040": "EXPENSE|DEBIT"} {
		if got := one101[string](t, conn, `SELECT type || '|' || normal_balance FROM gl_accounts WHERE code = $1`, code); got != want {
			t.Errorf("account %s = %s, want %s", code, got, want)
		}
	}
	_ = ctx
}

// A second apply of the guarded file changes nothing and does not error; the
// down file then rolls the shape back as its header promises; the migration
// applies again to the same applications, memos, balances and open amounts.
func TestMigration101_IdempotentThenDownThenUpAgain(t *testing.T) {
	m := built101(t)
	conn := m.conn

	// what the base held, to compare the rolled-back shape against
	legacy := func() map[string][]string {
		return map[string][]string{
			"payments": strs101(t, conn, `SELECT id::text || '|' || invoice_id::text || '|' || amount::text || '|' || method FROM payments ORDER BY id`),
			"refunds":  strs101(t, conn, `SELECT payment_id::text || '|' || sum(amount)::text FROM payment_refunds GROUP BY payment_id ORDER BY 1`),
			"invoices": strs101(t, conn, `SELECT id::text || '|' || total_amount::text FROM invoices ORDER BY id`),
			"deposits": strs101(t, conn, `SELECT id::text || '|' || customer_id::text || '|' || amount::text || '|' || applied_amount::text || '|' || status || '|' || method || '|' || COALESCE(gl_entry_id::text, '-') FROM customer_deposits ORDER BY id`),
			"depapps":  strs101(t, conn, `SELECT id::text || '|' || deposit_id::text || '|' || amount::text || '|' || COALESCE(invoice_id::text, '-') || '|' || COALESCE(gl_entry_id::text, '-') FROM customer_deposit_applications ORDER BY id`),
			"memos":    strs101(t, conn, `SELECT id::text || '|' || total_amount::text FROM credit_memos ORDER BY id`),
		}
	}
	// a legacy memo number that is not CM-<digits> must not stop the down file (the counter reads only the CM- numbers)
	if _, err := conn.Exec(context.Background(), `UPDATE credit_memos SET number = 'AWK-CM1' WHERE id = $1`, m101ID["M4a"]); err != nil {
		t.Fatal(err)
	}
	before := legacy()
	refundRows := one101[int](t, conn, `SELECT count(*) FROM payment_refunds`)

	// the migrated state, normalised so rows the migration mints fresh (new ids) compare by what they are
	state := func() []string {
		var s []string
		s = append(s, strs101(t, conn, `SELECT 'app|' || a.kind || '|' || COALESCE(a.payment_id::text, '') || '|'
			|| COALESCE(CASE WHEN m.source_payment_id IS NOT NULL THEN 'excess:' || m.source_payment_id::text || ':' || m.reason ELSE m.id::text END, '')
			|| '|' || a.invoice_id::text || '|' || a.amount::text || '|' || (a.reversed_at IS NOT NULL)::text || '|' || (a.gl_entry_id IS NOT NULL)::text
			FROM ar_applications a LEFT JOIN credit_memos m ON m.id = a.credit_memo_id ORDER BY 1`)...)
		s = append(s, strs101(t, conn, `SELECT 'memo|' || CASE WHEN source_payment_id IS NOT NULL THEN 'excess:' || source_payment_id::text || ':' || reason ELSE id::text END
			|| '|' || COALESCE(number, '-') || '|' || status || '|' || total_amount::text || '|' || amount_open::text FROM credit_memos ORDER BY 1`)...)
		s = append(s, strs101(t, conn, `SELECT 'pay|' || id::text || '|' || number || '|' || amount::text || '|' || amount_unapplied::text || '|' || migrated_excess::text
			|| '|' || status || '|' || received_on::text || '|' || COALESCE(invoice_id::text, '-') FROM payments ORDER BY 1`)...)
		s = append(s, strs101(t, conn, `SELECT 'inv|' || id::text || '|' || status || '|' || total_amount::text || '|' || amount_open::text || '|' || (paid_at IS NULL)::text FROM invoices ORDER BY 1`)...)
		s = append(s, strs101(t, conn, `SELECT 'ref|' || k || '|' || sum(amount)::text FROM (
				SELECT COALESCE(f.payment_id::text, 'memo:' || m.source_payment_id::text) AS k, f.amount
				FROM payment_refunds f LEFT JOIN credit_memos m ON m.id = f.credit_memo_id) x GROUP BY k ORDER BY 1`)...)
		s = append(s, strs101(t, conn, `SELECT 'counter|' || next_value::text FROM document_counters WHERE series = 'credit_memo'`)...)
		s = append(s, strs101(t, conn, `SELECT 'seq|' || payment_next_number()`)...)
		return s
	}
	raw := func() string { // every column of the tables the migration touched, nothing normalised
		var all []string
		for _, q := range []string{
			`SELECT to_jsonb(a)::text FROM ar_applications a ORDER BY id`,
			`SELECT to_jsonb(p)::text FROM payments p ORDER BY id`,
			`SELECT to_jsonb(i)::text FROM invoices i ORDER BY id`,
			`SELECT to_jsonb(c)::text FROM credit_memos c ORDER BY id`,
			`SELECT to_jsonb(f)::text FROM payment_refunds f ORDER BY id`,
			`SELECT to_jsonb(d)::text FROM document_counters d ORDER BY series`,
			`SELECT to_jsonb(l)::text FROM credit_memo_lines l ORDER BY id`,
			`SELECT to_jsonb(g)::text FROM gl_accounts g ORDER BY id`,
		} {
			all = append(all, strs101(t, conn, q)...)
		}
		return strings.Join(all, "\n")
	}

	apply101(t, conn, m.target)
	first := state()
	firstRaw := raw()
	if len(first) < 40 {
		t.Fatalf("first apply produced only %d state rows", len(first))
	}
	seq1 := one101[int64](t, conn, `SELECT last_value + CASE WHEN is_called THEN 1 ELSE 0 END FROM payment_number_seq`)

	// second apply: a no-op, no error
	apply101(t, conn, m.target)
	if again := raw(); again != firstRaw {
		t.Error("a second apply changed rows")
	}
	if seq2 := one101[int64](t, conn, `SELECT last_value + CASE WHEN is_called THEN 1 ELSE 0 END FROM payment_number_seq`); seq2 != seq1 {
		t.Errorf("a second apply moved payment_number_seq from %d to %d", seq1, seq2)
	}

	// down
	apply101(t, conn, m.down)
	after := legacy()
	// refunds that were re-pointed to a memo are back on their payments; the rows may be more than before, the totals are not
	for _, key := range []string{"payments", "refunds", "invoices", "deposits", "depapps"} {
		if fmt.Sprint(before[key]) != fmt.Sprint(after[key]) {
			t.Errorf("after down, %s =\n%v\nwant (as the base held them)\n%v", key, after[key], before[key])
		}
	}
	if n := one101[int](t, conn, `SELECT count(*) FROM payment_refunds WHERE payment_id = $1`, m101ID["P1"]); n != 1 {
		t.Errorf("worked case refund rows after down = %d, want the 20 and the 50 merged back into the one legacy row", n)
	}
	if got := one101[string](t, conn, `SELECT id::text FROM payment_refunds WHERE payment_id = $1`, m101ID["P1"]); got != m101ID["R1"] {
		t.Errorf("the merged refund is row %s, want the original row", got)
	}
	if n := one101[int](t, conn, `SELECT count(*) FROM payment_refunds`); n != refundRows {
		t.Errorf("%d refund rows after down, want the %d the base held", n, refundRows)
	}
	if got := one101[string](t, conn, `SELECT sum(amount)::text FROM payment_refunds WHERE payment_id = $1`, m101ID["P1"]); got != "70.00" {
		t.Errorf("worked case refunds after down total %s, want 70.00", got)
	}
	// the seeded memos remain, every excess memo (payment's and deposit's) is gone
	if fmt.Sprint(before["memos"]) != fmt.Sprint(after["memos"]) {
		t.Errorf("after down, credit memos =\n%v\nwant only the seeded ones\n%v", after["memos"], before["memos"])
	}
	if n := one101[int](t, conn, `SELECT count(*) FROM credit_memos`); n != 6 {
		t.Errorf("%d credit memos after down, want the 6 seeded", n)
	}
	// deposits are deposits again; no payment made from one remains; the legacy names are gone
	for _, rel := range []string{"customer_deposits_legacy", "customer_deposit_applications_legacy", "ar_applications"} {
		if one101[bool](t, conn, `SELECT to_regclass($1) IS NOT NULL`, rel) {
			t.Errorf("%s still exists after down", rel)
		}
	}
	if n := one101[int](t, conn, `SELECT count(*) FROM payments WHERE id IN (SELECT id FROM customer_deposits)`); n != 0 {
		t.Errorf("%d payments made from deposits remain after down", n)
	}
	if n := one101[int](t, conn, `SELECT count(*) FROM payments`); n != 11 {
		t.Errorf("%d payments after down, want the 11 legacy ones", n)
	}
	if got := one101[string](t, conn, `SELECT is_nullable FROM information_schema.columns WHERE table_name = 'payments' AND column_name = 'invoice_id'`); got != "NO" {
		t.Errorf("payments.invoice_id nullable after down = %s, want NO", got)
	}
	for _, c := range []struct{ table, col string }{
		{"payments", "number"}, {"payments", "customer_id"}, {"payments", "amount_unapplied"}, {"payments", "migrated_excess"},
		{"credit_memos", "amount_open"}, {"credit_memos", "source_payment_id"}, {"invoices", "amount_open"},
		{"payment_refunds", "credit_memo_id"}, {"payment_refunds", "refunded_on"}, {"payment_refunds", "method"},
	} {
		if n := one101[int](t, conn, `SELECT count(*) FROM information_schema.columns WHERE table_name = $1 AND column_name = $2`, c.table, c.col); n != 0 {
			t.Errorf("%s.%s survived the down file", c.table, c.col)
		}
	}
	if n := one101[int](t, conn, `SELECT count(*) FROM gl_accounts WHERE code IN ('4050', '5040')`); n != 0 {
		t.Errorf("%d of the accounts 4050 and 5040 survived the down file (no line names them)", n)
	}
	if one101[bool](t, conn, `SELECT to_regclass('payment_number_seq') IS NOT NULL`) || one101[bool](t, conn, `SELECT EXISTS (SELECT 1 FROM pg_trigger WHERE tgname = 'trg_invoices_default_amount_open')`) {
		t.Error("the number sequence or the amount_open trigger survived the down file")
	}
	// what it declares lost: the re-derived statuses stay re-derived, and the counter is back past the newest number
	if got := one101[string](t, conn, `SELECT status FROM invoices WHERE id = $1`, m101ID["I3p"]); got != "UNPAID" {
		t.Errorf("a re-derived status after down = %s, want it to stay UNPAID (documented as lost)", got)
	}
	if got := one101[int64](t, conn, `SELECT next_value FROM document_counters WHERE series = 'credit_memo'`); got != 6 {
		t.Errorf("credit memo counter after down = %d, want 6 (the newest seeded number plus one)", got)
	}
	// the method CHECK is the old one
	if _, err := conn.Exec(context.Background(), `UPDATE payments SET method = 'ACH' WHERE id = $1`, m101ID["P1"]); err == nil {
		t.Error("the method CHECK still accepts ACH after down")
	}

	// up again: same applications, memos, balances, open amounts, no row lost, no amount changed
	apply101(t, conn, m.target)
	second := state()
	if d := diff101(first, second); d != "" {
		t.Errorf("after down and up again the state differs (- first, + second):\n%s", d)
	}
	if got := one101[int](t, conn, `SELECT count(*) FROM customer_deposits_legacy`); got != 11 {
		t.Errorf("customer_deposits_legacy holds %d rows after up again, want 11", got)
	}
}

// The shape the migration leaves: constraints, the raw insert trigger and the
// columns' nullability, each tried on a row the CHECK must refuse and, as a
// control, one it must accept.
func TestMigration101_ConstraintsAndTrigger(t *testing.T) {
	m := migrated101(t)
	conn := m.conn
	ctx := context.Background()
	id := m101ID

	app := func(kind, payment, memo, amount, reason string) string {
		return fmt.Sprintf(`INSERT INTO ar_applications (customer_id, currency, kind, payment_id, credit_memo_id, invoice_id, amount, reason, applied_on, act_id)
			VALUES ('%s','USD','%s',%s,%s,'%s',%s,%s,'2031-04-01',gen_random_uuid())`, id["C9"], kind, payment, memo, id["I9"], amount, reason)
	}
	refuses101(t, conn, "ar_applications_write_off_reason", app("WRITE_OFF", "NULL", "NULL", "5", "NULL"))
	refuses101(t, conn, "ar_applications_write_off_reason", app("WRITE_OFF", "NULL", "NULL", "5", "'   '"))
	accepts101(t, conn, app("WRITE_OFF", "NULL", "NULL", "5", "'uncollectable'"))
	refuses101(t, conn, "ar_applications_source", app("PAYMENT", "NULL", "NULL", "5", "NULL"))
	refuses101(t, conn, "ar_applications_source", app("CREDIT_MEMO", "NULL", "NULL", "5", "NULL"))
	refuses101(t, conn, "ar_applications_source", app("WRITE_OFF", "'"+id["P1"]+"'", "NULL", "5", "'reason'"))
	accepts101(t, conn, app("PAYMENT", "'"+id["P1"]+"'", "NULL", "5", "NULL"))
	accepts101(t, conn, app("CREDIT_MEMO", "NULL", "'"+id["M4d"]+"'", "5", "NULL"))
	refuses101(t, conn, "ar_applications_amount_positive", app("PAYMENT", "'"+id["P1"]+"'", "NULL", "0", "NULL"))
	refuses101(t, conn, "ar_applications_amount_positive", app("PAYMENT", "'"+id["P1"]+"'", "NULL", "-3", "NULL"))
	refuses101(t, conn, "ar_applications_kind_check", app("REFUND", "'"+id["P1"]+"'", "NULL", "5", "NULL"))
	refuses101(t, conn, "ar_applications_reversal_columns", fmt.Sprintf(`INSERT INTO ar_applications (customer_id, currency, kind, payment_id, invoice_id, amount, applied_on, act_id, reversed_at)
		VALUES ('%s','USD','PAYMENT','%s','%s',5,'2031-04-01',gen_random_uuid(), now())`, id["C9"], id["P1"], id["I9"]))
	// one live application per entry
	accepts101(t, conn, fmt.Sprintf(`INSERT INTO ar_applications (customer_id, currency, kind, payment_id, invoice_id, amount, applied_on, act_id, gl_entry_id)
		VALUES ('%s','USD','PAYMENT','%s','%s',5,'2031-04-01',gen_random_uuid(),'%s')`, id["C9"], id["P1"], id["I9"], id["E5c"]))

	// payment_refunds names exactly one of a payment and a credit memo
	ref := func(payment, memo string) string {
		return fmt.Sprintf(`INSERT INTO payment_refunds (payment_id, credit_memo_id, amount, status, method, refunded_on) VALUES (%s, %s, 5, 'COMPLETE', 'CASH', '2031-04-01')`, payment, memo)
	}
	refuses101(t, conn, "payment_refunds_one_target", ref("'"+id["P1"]+"'", "'"+id["M4d"]+"'"))
	refuses101(t, conn, "payment_refunds_one_target", ref("NULL", "NULL"))
	accepts101(t, conn, ref("'"+id["P1"]+"'", "NULL"))
	accepts101(t, conn, ref("NULL", "'"+id["M4d"]+"'"))

	// payments: no invoice needed, the widened method set, unique number, the unapplied range, the void columns
	pay := func(extra, vals string) string {
		return fmt.Sprintf(`INSERT INTO payments (customer_id, branch_id, currency, received_on, amount, method, amount_unapplied%s)
			VALUES ('%s','%s','USD','2031-04-01',50,%s%s)`, extra, id["C9"], id["B1"], vals, "")
	}
	accepts101(t, conn, pay("", "'ACH', 50"))
	accepts101(t, conn, pay("", "'OTHER', 50"))
	accepts101(t, conn, pay("", "'ACCOUNT', 0"))
	refuses101(t, conn, "payments_method_check", pay("", "'BITCOIN', 50"))
	refuses101(t, conn, "payments_unapplied_range", pay("", "'CASH', 51"))
	refuses101(t, conn, "payments_number_key", pay(", number", "'CASH', 50, 'PAY-000001'"))
	refuses101(t, conn, "payments_void_columns", pay(", status", "'CASH', 50, 'VOIDED'"))
	refuses101(t, conn, "payments_status_check", pay(", status", "'CASH', 50, 'HELD'"))
	accepts101(t, conn, pay(", status, voided_at, voided_on", "'CASH', 50, 'VOIDED', now(), '2031-04-01'"))
	refuses101(t, conn, "payments_currency_format", strings.Replace(pay("", "'CASH', 50"), "'USD'", "'usd'", 1))
	if got := one101[string](t, conn, `WITH p AS (INSERT INTO payments (customer_id, branch_id, currency, received_on, amount, method, amount_unapplied)
		VALUES ($1,$2,'USD','2031-04-01',50,'CASH',50) RETURNING number, status, revision, invoice_id::text) SELECT (substring(number FROM 5)::int > 20)::text || '|' || status || '|' || revision::text || '|' || COALESCE(invoice_id, 'none') FROM p`, id["C9"], id["B1"]); got != "true|POSTED|1|none" {
		t.Errorf("a new payment = %s, want a number past the migrated ones|POSTED|1|none", got)
	}
	// amount_open ranges
	refuses101(t, conn, "invoices_amount_open_range", fmt.Sprintf(`UPDATE invoices SET amount_open = -1 WHERE id = '%s'`, id["I9"]))
	refuses101(t, conn, "credit_memos_amount_open_range", fmt.Sprintf(`UPDATE credit_memos SET amount_open = 1 WHERE id = '%s'`, id["M4d"]))

	// the trigger: a raw invoice insert opens at its total, or at zero when the status says it is not owed
	for status, want := range map[string]string{"UNPAID": "33.00", "PARTIAL": "33.00", "PAID": "0.00", "VOID": "0.00", "WRITTEN_OFF": "0.00"} {
		got := one101[string](t, conn, `WITH i AS (INSERT INTO invoices (customer_id, status, total_amount, subtotal, branch_id, voided_at)
			VALUES ($1, $2, 33, 33, $3, CASE WHEN $2 = 'VOID' THEN now() END) RETURNING amount_open) SELECT amount_open::text FROM i`, id["C9"], status, id["B1"])
		if got != want {
			t.Errorf("raw %s invoice opened at %s, want %s", status, got, want)
		}
	}
	if got := one101[string](t, conn, `WITH i AS (INSERT INTO invoices (customer_id, status, total_amount, subtotal, branch_id, amount_open)
		VALUES ($1, 'UNPAID', 33, 33, $2, 12) RETURNING amount_open) SELECT amount_open::text FROM i`, id["C9"], id["B1"]); got != "12.00" {
		t.Errorf("an explicit amount_open was overridden: %s", got)
	}
	if got := one101[string](t, conn, `SELECT is_nullable FROM information_schema.columns WHERE table_name = 'invoices' AND column_name = 'amount_open'`); got != "NO" {
		t.Errorf("invoices.amount_open nullable = %s", got)
	}
	_ = ctx
}

// diff101 lists the rows only one of two sorted-or-not row sets holds.
func diff101(a, b []string) string {
	in := func(set []string) map[string]int {
		m := map[string]int{}
		for _, s := range set {
			m[s]++
		}
		return m
	}
	ma, mb := in(a), in(b)
	var out []string
	for _, s := range a {
		if mb[s] == 0 {
			out = append(out, "- "+s)
		}
	}
	for _, s := range b {
		if ma[s] == 0 {
			out = append(out, "+ "+s)
		}
	}
	return strings.Join(out, "\n")
}
