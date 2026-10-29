// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package pos_test

// Migration 105 on rows that exist (recipe step 3, ADR 0005 section 13's
// C2-5 list): the schema up to 104 is built in a scratch database of its
// own, legacy rows are written in the shape the base commit left them,
// then 105 is applied and each backfill is read back. The historic
// ACCOUNT return becomes an OPEN credit memo carrying the return's own
// entry with no new journal or subledger row; the down reverses the steps
// (the memo goes, the walk-in customer goes, the columns go) and the up
// applies again after the down.

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/gablelbm/gable/internal/testutil"
	"github.com/gablelbm/gable/pkg/database"
	"github.com/jackc/pgx/v5"
)

func mig105ScratchDB(t *testing.T) (*pgx.Conn, string) {
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
		t.Fatal(err)
	}
	name := "gv1_c25mig_" + hex.EncodeToString(suffix)
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
	conn, err := pgx.Connect(ctx, scratch.String())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { conn.Close(context.Background()) })
	return conn, scratch.String()
}

func mig105Files(t *testing.T) (before []string, target, down string) {
	t.Helper()
	all, err := filepath.Glob("../../migrations/*.sql")
	if err != nil || len(all) == 0 {
		t.Fatalf("no migrations found: %v", err)
	}
	sort.Strings(all)
	for _, f := range all {
		base := filepath.Base(f)
		switch {
		case strings.HasPrefix(base, "105_"):
			target = f
		case base < "105_":
			before = append(before, f)
		}
	}
	if target == "" {
		t.Fatal("migration 105 not found")
	}
	down = "../../migrations/down/105_pos_wire_contract_down.sql"
	if _, err := os.Stat(down); err != nil {
		t.Fatal("migration 105 down not found")
	}
	return before, target, down
}

func mig105Apply(t *testing.T, conn *pgx.Conn, file string) {
	t.Helper()
	sql, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := conn.Exec(context.Background(), string(sql)); err != nil {
		t.Fatalf("applying %s: %v", filepath.Base(file), err)
	}
}

func mig105Scalar[T any](t *testing.T, conn *pgx.Conn, sql string, args ...any) T {
	t.Helper()
	var v T
	if err := conn.QueryRow(context.Background(), sql, args...).Scan(&v); err != nil {
		t.Fatalf("%s: %v", sql, err)
	}
	return v
}

const mig105Branch = `(SELECT value::uuid FROM system_settings WHERE key = 'default_branch_id')`

// mig105Legacy writes the base commit's shape: two sales in the old column
// set (one for a customer whose currency is null, so the default fills it),
// two old lines, a cash and a card tender, a return refunded to ACCOUNT
// whose GL reversal entry exists, its line, and a cash return for no
// customer.
func mig105Legacy(t *testing.T, conn *pgx.Conn) {
	t.Helper()
	legacy := `
		INSERT INTO system_settings (key, value)
		VALUES ('default_branch_id', (SELECT id::text FROM locations WHERE type = 'BRANCH' ORDER BY created_at, id LIMIT 1))
		ON CONFLICT (key) DO UPDATE SET value = EXCLUDED.value;
		UPDATE pos_registers SET branch_id = ` + mig105Branch + ` WHERE id = 'REG-01';
		INSERT INTO customers (id, name, account_number, primary_branch_id)
		VALUES ('00000000-0000-0000-0000-0000000000a1', 'Mig Counter Co', 'MIGCUST', ` + mig105Branch + `);
		INSERT INTO products (id, sku, description, uom_primary, base_price, average_unit_cost)
		VALUES ('00000000-0000-0000-0000-0000000000f1', 'MIG-2X4', '2x4x8', 'PCS', 5.50, 3.25);

		INSERT INTO pos_transactions (id, register_id, cashier_id, customer_id, subtotal, tax_amount, total,
		                              status, completed_at, created_at, branch_id)
		VALUES
			('00000000-0000-0000-0000-0000000000b1', 'REG-01', '00000000-0000-0000-0000-0000000000e1', NULL,
			 11.00, 0.98, 11.98, 'COMPLETED', '2026-01-01T11:02:00Z', '2026-01-01T11:01:00Z', ` + mig105Branch + `),
			('00000000-0000-0000-0000-0000000000b2', 'REG-01', '00000000-0000-0000-0000-0000000000e1',
			 '00000000-0000-0000-0000-0000000000a1',
			 5.50, 0.49, 5.99, 'OPEN', NULL, '2026-01-02T11:01:00Z', ` + mig105Branch + `);

		INSERT INTO pos_line_items (id, transaction_id, product_id, description, quantity, uom, unit_price,
		                            line_total, created_at)
		VALUES
			('00000000-0000-0000-0000-0000000000c1', '00000000-0000-0000-0000-0000000000b1',
			 '00000000-0000-0000-0000-0000000000f1', '2x4x8', 2, 'PCS', 5.50, 11.00, '2026-01-01T11:01:10Z'),
			('00000000-0000-0000-0000-0000000000c2', '00000000-0000-0000-0000-0000000000b1',
			 '00000000-0000-0000-0000-0000000000f1', '2x4x8', 1, 'PCS', 5.50, 5.50, '2026-01-01T11:01:20Z');

		INSERT INTO pos_tenders (id, transaction_id, method, amount, card_last4, card_brand, created_at)
		VALUES
			('00000000-0000-0000-0000-0000000000d1', '00000000-0000-0000-0000-0000000000b1', 'CASH', 2.00, NULL, NULL, '2026-01-01T11:02:00Z'),
			('00000000-0000-0000-0000-0000000000d2', '00000000-0000-0000-0000-0000000000b1', 'CARD', 9.98, '4242', 'VISA', '2026-01-01T11:02:00Z');

		INSERT INTO gl_journal_entries (id, entry_date, memo, source, source_ref_id, status, currency)
		VALUES ('00000000-0000-0000-0000-0000000000a2', '2026-02-01', 'counter account return', 'RETURN',
		        '00000000-0000-0000-0000-0000000000a3', 'POSTED', 'USD');

		INSERT INTO pos_returns (id, register_id, customer_id, branch_id, cashier_id, subtotal, tax_amount,
		                         total, refund_method, reason, status, gl_entry_id, created_at)
		VALUES
			('00000000-0000-0000-0000-0000000000a3', 'REG-01', '00000000-0000-0000-0000-0000000000a1',
			 ` + mig105Branch + `, '00000000-0000-0000-0000-0000000000e1', 18.00, 1.59, 19.59, 'ACCOUNT',
			 'wrong length', 'COMPLETED', '00000000-0000-0000-0000-0000000000a2', '2026-02-01T12:00:00Z'),
			('00000000-0000-0000-0000-0000000000a4', 'REG-01', NULL, ` + mig105Branch + `,
			 '00000000-0000-0000-0000-0000000000e1', 5.50, 0.49, 5.99, 'CASH', 'no receipt', 'COMPLETED', NULL,
			 '2026-02-02T12:00:00Z');

		INSERT INTO pos_return_lines (id, return_id, product_id, description, quantity, uom, unit_price,
		                              line_total, restock, created_at)
		VALUES ('00000000-0000-0000-0000-0000000000a5', '00000000-0000-0000-0000-0000000000a3',
		        '00000000-0000-0000-0000-0000000000f1', '2x4x8', 3, 'PCS', 6.00, 18.00, TRUE, '2026-02-01T12:00:10Z');`
	if _, err := conn.Exec(context.Background(), legacy); err != nil {
		t.Fatalf("seed legacy rows: %v", err)
	}
}

func TestMigration105_POSWireContract(t *testing.T) {
	conn, _ := mig105ScratchDB(t)
	before, target, downFile := mig105Files(t)
	for _, f := range before {
		mig105Apply(t, conn, f)
	}
	mig105Legacy(t, conn)
	mig105Apply(t, conn, target)
	ctx := context.Background()

	// Step 1: the sales numbered POS- in (created_at, id) order, revision 1,
	// the currency from the customer or the default, the invoice link column
	// present and null.
	for id, want := range map[string]string{
		"00000000-0000-0000-0000-0000000000b1": "POS-000001",
		"00000000-0000-0000-0000-0000000000b2": "POS-000002",
	} {
		var number, currency string
		var revision int64
		var invoice *string
		if err := conn.QueryRow(ctx, `SELECT number, currency, revision, invoice_id FROM pos_transactions WHERE id = $1`, id).
			Scan(&number, &currency, &revision, &invoice); err != nil {
			t.Fatal(err)
		}
		if number != want || currency != "USD" || revision != 1 || invoice != nil {
			t.Errorf("sale %s = (%s, %s, %d, %v); want (%s, USD, 1, nil)", id, number, currency, revision, invoice, want)
		}
	}
	// A raw insert numbers itself past the maximum: the column DEFAULT (the
	// currency it names itself; the column carries no default, the pattern
	// every cycle 2 document column follows).
	if _, err := conn.Exec(ctx, `INSERT INTO pos_transactions (id, register_id, cashier_id, branch_id, currency)
		VALUES ('00000000-0000-0000-0000-0000000000b3', 'REG-01', '00000000-0000-0000-0000-0000000000e1', `+mig105Branch+`, 'USD')`); err != nil {
		t.Fatal(err)
	}
	if got := mig105Scalar[string](t, conn, `SELECT number FROM pos_transactions WHERE id = '00000000-0000-0000-0000-0000000000b3'`); got != "POS-000003" {
		t.Errorf("raw insert number = %s, want POS-000003", got)
	}

	// Step 2: the lines in the shared shape. The taxable flag follows the
	// product, the pair is 1 and 1, price_uom the sale unit, the priced
	// price the old one, the position the (created_at, id) order.
	var pos int
	var lineType, priceUOM, priceSource string
	var uomQty, priceUOMQty, priced float64
	var taxable bool
	if err := conn.QueryRow(ctx, `
		SELECT position, line_type, price_uom, uom_qty::float8, price_uom_qty::float8, priced_unit_price::float8,
		       price_source, taxable
		FROM pos_line_items WHERE id = '00000000-0000-0000-0000-0000000000c2'`).
		Scan(&pos, &lineType, &priceUOM, &uomQty, &priceUOMQty, &priced, &priceSource, &taxable); err != nil {
		t.Fatal(err)
	}
	if pos != 1 || lineType != "PRODUCT" || priceUOM != "PCS" || uomQty != 1 || priceUOMQty != 1 || priced != 5.5 || priceSource != "PRICE_LIST" || !taxable {
		t.Errorf("line l2 = (%d, %s, %s, %v, %v, %v, %s, %v); want (1, PRODUCT, PCS, 1, 1, 5.5, PRICE_LIST, true)",
			pos, lineType, priceUOM, uomQty, priceUOMQty, priced, priceSource, taxable)
	}
	// The per type CHECKs bite: a text line without amounts is stored, one
	// with a price is refused, a product line without quantity is refused,
	// and a pair side of zero is refused.
	if _, err := conn.Exec(ctx, `INSERT INTO pos_line_items (transaction_id, line_type, description, price_source)
		VALUES ('00000000-0000-0000-0000-0000000000b2', 'TEXT', 'delivery note', 'NONE')`); err != nil {
		t.Errorf("a text line without amounts is stored: %v", err)
	}
	for sql, what := range map[string]string{
		`INSERT INTO pos_line_items (transaction_id, line_type, description, unit_price)
		 VALUES ('00000000-0000-0000-0000-0000000000b2', 'TEXT', 'x', 5.00)`: "a text line with a price",
		`INSERT INTO pos_line_items (transaction_id, line_type, product_id, description, uom, unit_price, line_total, uom_qty, price_uom_qty, price_uom)
		 VALUES ('00000000-0000-0000-0000-0000000000b2', 'PRODUCT', '00000000-0000-0000-0000-0000000000f1', 'x', 'PCS', 5.00, 5.00, 0, 1, 'PCS')`: "a pair side of zero",
		`INSERT INTO pos_line_items (transaction_id, product_id, description, uom, unit_price, uom_qty, price_uom_qty, price_uom, discount_percent, discount_amount)
		 VALUES ('00000000-0000-0000-0000-0000000000b2', '00000000-0000-0000-0000-0000000000f1', 'x', 'PCS', 5.00, 1, 1, 'PCS', 10, 0.5)`: "a discount percent and amount together",
	} {
		if _, err := conn.Exec(ctx, sql); err == nil {
			t.Errorf("the CHECK lets %s in", what)
		}
	}
	// The tender's payment link column exists.
	if n := mig105Scalar[int](t, conn, `SELECT count(*) FROM pos_tenders WHERE payment_id IS NULL`); n != 2 {
		t.Errorf("%d legacy tenders carry a payment link; want 2 without one", n)
	}

	// Step 3: the returns numbered RTN-, and the historic ACCOUNT return an
	// OPEN credit memo carrying the return's own entry, its lines mirrored
	// negative, and no new journal or subledger row anywhere.
	if got := mig105Scalar[string](t, conn, `SELECT number FROM pos_returns WHERE id = '00000000-0000-0000-0000-0000000000a3'`); got != "RTN-000001" {
		t.Errorf("return r1 number = %s, want RTN-000001", got)
	}
	if got := mig105Scalar[string](t, conn, `SELECT number FROM pos_returns WHERE id = '00000000-0000-0000-0000-0000000000a4'`); got != "RTN-000002" {
		t.Errorf("return r2 number = %s, want RTN-000002", got)
	}
	var memoID string
	if err := conn.QueryRow(ctx, `SELECT credit_memo_id FROM pos_returns WHERE id = '00000000-0000-0000-0000-0000000000a3'`).Scan(&memoID); err != nil {
		t.Fatalf("the ACCOUNT return carries no memo link: %v", err)
	}
	var status, reason, reasonCode string
	var amount, open, subtotal, tax, total float64
	var entry *string
	if err := conn.QueryRow(ctx, `
		SELECT m.status, m.reason, m.reason_code, m.amount::float8, m.amount_open::float8, m.subtotal::float8,
		       m.tax_amount::float8, m.total_amount::float8, m.gl_entry_id
		FROM credit_memos m WHERE m.id = $1`, memoID).Scan(&status, &reason, &reasonCode, &amount, &open, &subtotal, &tax, &total, &entry); err != nil {
		t.Fatal(err)
	}
	// The memo's amount keeps the credit memos' own convention (positive
	// amount, negative totals).
	if status != "OPEN" || reason != "migrated counter account return" || reasonCode != "RETURN" ||
		amount != 19.59 || open != -19.59 || subtotal != -18 || tax != -1.59 || total != -19.59 ||
		entry == nil || *entry != "00000000-0000-0000-0000-0000000000a2" {
		t.Errorf("the migrated memo = (%s, %s, %s, %v, %v, %v, %v, %v, %v)", status, reason, reasonCode, amount, open, subtotal, tax, total, entry)
	}
	if got := mig105Scalar[int](t, conn, `SELECT count(*) FROM credit_memo_lines WHERE credit_memo_id = $1 AND quantity = -3 AND line_total = -18.00`, memoID); got != 1 {
		t.Errorf("%d mirrored memo lines; want 1", got)
	}
	// The memo date is the return's business date in the branch's zone, and
	// the CASH return has no memo.
	if got := mig105Scalar[int](t, conn, `
		SELECT count(*) FROM credit_memos m JOIN pos_returns r ON r.credit_memo_id = m.id
		WHERE m.memo_date = (r.created_at AT TIME ZONE COALESCE((SELECT l.timezone FROM locations l WHERE l.id = r.branch_id), 'UTC'))::date`); got != 1 {
		t.Errorf("%d migrated memos carry the return's local date; want 1", got)
	}
	if got := mig105Scalar[int](t, conn, `SELECT count(*) FROM pos_returns WHERE id = '00000000-0000-0000-0000-0000000000a4' AND credit_memo_id IS NULL`); got != 1 {
		t.Errorf("the CASH return gained a memo")
	}
	// History moved as data: the entry count and the subledger count are
	// what the legacy rows left.
	if got := mig105Scalar[int](t, conn, `SELECT count(*) FROM gl_journal_entries`); got != 1 {
		t.Errorf("%d journal entries after the up; want the 1 legacy entry", got)
	}
	if got := mig105Scalar[int](t, conn, `SELECT count(*) FROM customer_transactions`); got != 0 {
		t.Errorf("%d subledger rows after the up; the migration writes none", got)
	}

	// Step 4: the walk-in customer and its setting, and idempotence: a
	// second apply adds nothing.
	walkIn := mig105Scalar[string](t, conn, `SELECT id::text FROM customers WHERE account_number = 'WALK-IN'`)
	if got := mig105Scalar[string](t, conn, `SELECT value FROM system_settings WHERE key = 'pos.walk_in_customer_id'`); got != walkIn {
		t.Errorf("pos.walk_in_customer_id = %s; want the walk-in customer %s", got, walkIn)
	}
	mig105Apply(t, conn, target)
	for sql, want := range map[string]int{
		`SELECT count(*) FROM credit_memos WHERE reason = 'migrated counter account return'`: 1,
		`SELECT count(*) FROM customers WHERE account_number = 'WALK-IN'`:                    1,
		`SELECT count(*) FROM gl_journal_entries`:                                            1,
	} {
		if got := mig105Scalar[int](t, conn, sql); got != want {
			t.Errorf("after a second apply, %s = %d, want %d", sql, got, want)
		}
	}

	// A completion under 105 stores its links: an invoice behind the
	// completed sale, its lines, and each sale line's invoice line id (the
	// sixth review's P3-3 cycle).
	if _, err := conn.Exec(ctx, `
		INSERT INTO invoices (id, customer_id, branch_id, number, status, invoice_date, currency,
		                      subtotal, tax_amount, total_amount, amount_open, created_at, updated_at)
		VALUES ('00000000-0000-0000-0000-0000000000b5', '00000000-0000-0000-0000-0000000000a1',
		        `+mig105Branch+`, 'INV-MIG-1', 'UNPAID', '2026-01-01', 'USD', 16.50, 0.98, 17.48, 17.48,
		        '2026-01-01T11:02:00Z', '2026-01-01T11:02:00Z');
		INSERT INTO invoice_lines (id, invoice_id, product_id, description, quantity, uom, price_uom,
		                           uom_qty, price_uom_qty, unit_price, line_total, unit_cost, position, created_at)
		VALUES
			('00000000-0000-0000-0000-0000000000c5', '00000000-0000-0000-0000-0000000000b5',
			 '00000000-0000-0000-0000-0000000000f1', '2x4x8', 2, 'PCS', 'PCS', 1, 1, 5.50, 11.00, 3.25, 0, '2026-01-01T11:02:00Z'),
			('00000000-0000-0000-0000-0000000000c6', '00000000-0000-0000-0000-0000000000b5',
			 '00000000-0000-0000-0000-0000000000f1', '2x4x8', 1, 'PCS', 'PCS', 1, 1, 5.50, 5.50, 3.25, 1, '2026-01-01T11:02:00Z');
		UPDATE pos_transactions SET invoice_id = '00000000-0000-0000-0000-0000000000b5'
		 WHERE id = '00000000-0000-0000-0000-0000000000b1';
		UPDATE pos_line_items SET invoice_line_id = '00000000-0000-0000-0000-0000000000c5'
		 WHERE id = '00000000-0000-0000-0000-0000000000c1';
		UPDATE pos_line_items SET invoice_line_id = '00000000-0000-0000-0000-0000000000c6'
		 WHERE id = '00000000-0000-0000-0000-0000000000c2';`); err != nil {
		t.Fatalf("seed the completion's links: %v", err)
	}

	// The down: the memo and its lines go, the walk-in customer goes (its
	// setting first), the new columns go, the line shape returns to the
	// base commit's, and the text line the up's shape allowed is deleted.
	memoNumber := mig105Scalar[string](t, conn, `SELECT number FROM credit_memos WHERE reason = 'migrated counter account return'`)
	mig105Apply(t, conn, downFile)
	for _, c := range [][2]string{
		{"pos_transactions", "number"}, {"pos_transactions", "currency"}, {"pos_transactions", "revision"},
		{"pos_returns", "number"}, {"pos_returns", "credit_memo_id"},
		{"pos_line_items", "line_type"}, {"pos_line_items", "invoice_line_id"}, {"pos_tenders", "payment_id"},
		{"pos_return_lines", "sale_line_id"},
	} {
		if n := mig105Scalar[int](t, conn, `SELECT count(*) FROM information_schema.columns WHERE table_name = $1 AND column_name = $2`, c[0], c[1]); n != 0 {
			t.Errorf("after the down, %s.%s remains", c[0], c[1])
		}
	}
	if got := mig105Scalar[int](t, conn, `SELECT count(*) FROM credit_memos WHERE reason = 'migrated counter account return'`); got != 0 {
		t.Errorf("%d migrated memos survive the down", got)
	}
	if got := mig105Scalar[int](t, conn, `SELECT count(*) FROM customers WHERE account_number = 'WALK-IN'`); got != 0 {
		t.Errorf("the walk-in customer survives the down")
	}
	if got := mig105Scalar[int](t, conn, `SELECT count(*) FROM system_settings WHERE key = 'pos.walk_in_customer_id'`); got != 0 {
		t.Errorf("the walk-in setting survives the down")
	}
	if got := mig105Scalar[int](t, conn, `SELECT count(*) FROM pos_line_items`); got != 2 {
		t.Errorf("%d lines after the down; want the 2 legacy product lines", got)
	}
	// the invoice link column survives the down (the invoices do: 105 never
	// created them), so the up can backfill the line links again
	if got := mig105Scalar[int](t, conn, `SELECT count(*) FROM pos_transactions
		WHERE id = '00000000-0000-0000-0000-0000000000b1' AND invoice_id = '00000000-0000-0000-0000-0000000000b5'`); got != 1 {
		t.Errorf("the completed sale lost its invoice link in the down")
	}
	if scale := mig105Scalar[int](t, conn, `SELECT numeric_scale FROM information_schema.columns WHERE table_name = 'pos_line_items' AND column_name = 'unit_price'`); scale != 2 {
		t.Errorf("unit_price scale after the down = %d, want 2", scale)
	}

	// And up again after the down: the memo returns, the numbers return,
	// and nothing duplicates. The down returns the credit memo counter, so
	// the re-up mints the same CM number again and the gapless series keeps
	// no gap.
	mig105Apply(t, conn, target)
	if got := mig105Scalar[int](t, conn, `SELECT count(*) FROM credit_memos WHERE reason = 'migrated counter account return'`); got != 1 {
		t.Errorf("%d migrated memos after the re-up, want 1", got)
	}
	if got := mig105Scalar[string](t, conn, `SELECT number FROM credit_memos WHERE reason = 'migrated counter account return'`); got != memoNumber {
		t.Errorf("the re-up mints %s after the down minted %s: the down did not return the counter", got, memoNumber)
	}
	if got := mig105Scalar[int](t, conn, `SELECT count(DISTINCT number) FROM pos_transactions`); got != mig105Scalar[int](t, conn, `SELECT count(*) FROM pos_transactions WHERE number IS NOT NULL`) {
		t.Errorf("the re-up duplicated a sale number")
	}
	// the backfill restored each sale line's own invoice line, by product
	// and order within the invoice: the linked return of a sale completed
	// before, or downed and upped under, this item keeps its cost link
	for line, want := range map[string]string{
		"00000000-0000-0000-0000-0000000000c1": "00000000-0000-0000-0000-0000000000c5",
		"00000000-0000-0000-0000-0000000000c2": "00000000-0000-0000-0000-0000000000c6",
	} {
		if got := mig105Scalar[string](t, conn, `SELECT invoice_line_id::text FROM pos_line_items WHERE id = $1`, line); got != want {
			t.Errorf("sale line %s links invoice line %s after the re-up, want its own %s (the backfill)", line, got, want)
		}
	}
}

// RULE (second review P3-2): the up refuses to migrate a legacy ACCOUNT
// return with no customer, naming the rows, instead of dying on the memo's
// NOT NULL halfway.
func TestMigration105_RefusesALegacyAccountReturnWithNoCustomer(t *testing.T) {
	conn, _ := mig105ScratchDB(t)
	before, target, _ := mig105Files(t)
	for _, f := range before {
		mig105Apply(t, conn, f)
	}
	mig105Legacy(t, conn)
	if _, err := conn.Exec(context.Background(), `UPDATE pos_returns SET customer_id = NULL
		WHERE id = '00000000-0000-0000-0000-0000000000a3'`); err != nil {
		t.Fatal(err)
	}
	sql, err := os.ReadFile(target)
	if err != nil {
		t.Fatal(err)
	}
	_, err = conn.Exec(context.Background(), string(sql))
	if err == nil {
		t.Fatal("the up migrated an ACCOUNT return with no customer")
	}
	if !strings.Contains(err.Error(), "no customer") {
		t.Errorf("the up's refusal = %v, want it to name the customerless rows", err)
	}
}

// mig105ServiceDB builds the schema to 104 in a scratch database, writes the
// legacy rows in the base shape, applies 105, and answers a live database
// handle the fixture can serve on (sixth review P1-1: the seeded database
// test through 105).
func mig105ServiceDB(t *testing.T) *database.DB {
	t.Helper()
	conn, url := mig105ScratchDB(t)
	before, target, _ := mig105Files(t)
	for _, f := range before {
		mig105Apply(t, conn, f)
	}
	mig105Legacy(t, conn)
	// a completed sale of lines 2 and 1 of the legacy product with an
	// unlinked legacy return of half a unit: the exact rows a dealer's
	// pre contract database holds, in the positive shape the base commit
	// wrote them (and 105 leaves: it negates only while copying into the
	// credit memos). The seeded CASH return a4 becomes that half return.
	legacy := `
		INSERT INTO pos_return_lines (id, return_id, product_id, description, quantity, uom, unit_price,
		                              line_total, restock, created_at)
		VALUES ('00000000-0000-0000-0000-0000000000a6', '00000000-0000-0000-0000-0000000000a4',
		        '00000000-0000-0000-0000-0000000000f1', '2x4x8', 0.5, 'PCS', 5.50, 2.75, FALSE, '2026-02-03T12:00:00Z');
		UPDATE pos_returns SET original_transaction_id = '00000000-0000-0000-0000-0000000000b1',
		                       subtotal = 2.75, tax_amount = 0.24, total = 2.99
		 WHERE id = '00000000-0000-0000-0000-0000000000a4';`
	if _, err := conn.Exec(context.Background(), legacy); err != nil {
		t.Fatalf("seed the unlinked legacy half: %v", err)
	}
	mig105Apply(t, conn, target)
	// the service reads the columns migrations 114 and 123 added to the sale,
	// its lines and the invoice; the database stops at 105 for the legacy rows only
	mig105Apply(t, conn, "../../migrations/114_pos_sale_account_refs.sql")
	mig105Apply(t, conn, "../../migrations/123_sales_line_units.sql")
	// the ledger columns the service stamps on every posting (migration 125
	// also reads tables this stage of the schema has not built yet, and there
	// is nothing to backfill here)
	if _, err := conn.Exec(context.Background(), `
		ALTER TABLE gl_journal_entries ADD COLUMN branch_id UUID REFERENCES locations(id);
		ALTER TABLE gl_journal_lines ADD COLUMN branch_id UUID REFERENCES locations(id);`); err != nil {
		t.Fatalf("add the ledger branch columns: %v", err)
	}
	// and the counter's account sale columns of migration 126
	mig105Apply(t, conn, "../../migrations/126_counter_buyers_returns_and_receipts.sql")
	// and the till columns and tables 127 added (the session reads name them)
	mig105Apply(t, conn, "../../migrations/127_till_movements_and_eod.sql")
	// and the credit holds 124 added (the counter's account facts read the
	// customer's standing hold record beside the credit limit)
	mig105Apply(t, conn, "../../migrations/124_ar_credit_holds_worklist_statements.sql")
	// and the customer job requirement the account facts read (136)
	mig105Apply(t, conn, "../../migrations/136_customer_job_required.sql")
	// and the zone columns 141 added to the sale (the sale read names them)
	mig105Apply(t, conn, "../../migrations/141_tax_zone_tables.sql")
	conn.Close(context.Background())
	db, err := database.Connect(url)
	if err != nil {
		t.Fatalf("connect the migrated scratch database: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

// RULE (sixth review P1-1): on a database migration 105 really migrated, a
// sale partly returned before the contract cannot be returned for more than
// it sold: the legacy half the migration leaves positive counts against the
// cap, and the later linked returns bring back no more than the rest.
func TestMigration105_ALegacyReturnMigratedBoundsTheLaterReturns(t *testing.T) {
	db := mig105ServiceDB(t)
	f := newFixture(t, db)
	f.openTill(0)
	saleID := "00000000-0000-0000-0000-0000000000b1"
	lines := f.getSale(t, saleID)["lines"].([]any)
	first := lines[0].(map[string]any)["id"].(string)
	second := lines[1].(map[string]any)["id"].(string)
	// the second line's one unit comes back (the legacy half bears on the
	// first line): 5.50 of line with its tax share of the sale's 98
	r := f.returnOn(t, saleID, second, "1", map[string]any{"restock": false})
	if r.status != http.StatusCreated {
		t.Fatalf("return of the second line = %d: %s", r.status, r.raw)
	}
	if got := num(t, r.body, "total_cents"); got != -583 {
		t.Errorf("return of the second line total = %d, want -583", got)
	}
	// the first line's whole two units are refused: half already came home
	r = f.returnOn(t, saleID, first, "2", map[string]any{"restock": false})
	if r.status != http.StatusConflict {
		t.Fatalf("return of the first line whole = %d, want 409: %s", r.status, r.raw)
	}
	_, blockers, _ := errorOf(t, r)
	if len(blockers) == 0 || blockers[0] != "exceeds_sold" {
		t.Errorf("blockers = %v, want exceeds_sold", blockers)
	}
	// one and a half of it still come back, the line's remainder less the
	// legacy refund: 1100 less the 275 the legacy half took
	r = f.returnOn(t, saleID, first, "1.5", map[string]any{"restock": false})
	if r.status != http.StatusCreated {
		t.Fatalf("return of the first line's remainder = %d: %s", r.status, r.raw)
	}
	if got := num(t, r.body, "subtotal_cents"); got != -825 {
		t.Errorf("return of the first line's remainder subtotal = %d, want -825", got)
	}
	f.assertARInvariants(t)
}
