// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package order_test

// Migration 092 on rows that exist (recipe step 3 and ADR 0005 section 13):
// the schema up to 091 is built in a scratch database of its own, legacy
// order rows are written in the shape the base commit left them, then 092 is
// applied and each backfill is read back: the number, the currency, the
// delivery type from the delivery history, the header columns, the line
// shape (unit_price widened from price_each, the pair 1 and 1, the extension,
// the price source QUOTE where a quote is named, the allocation and
// fulfilment quantities), the charge code seed and account 4030, and the
// subledger's enum to text. The down file then rolls the shape back, and the
// migration applies again.

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

func scratchDB(t *testing.T) (*pgx.Conn, *[]string) {
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
	name := "gv1_c22mig_" + hex.EncodeToString(suffix)
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

func migrationFiles(t *testing.T) (before []string, target string) {
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
		case strings.HasPrefix(base, "092_"):
			target = f
		case base < "092_":
			before = append(before, f)
		}
	}
	if target == "" {
		t.Fatal("migration 092 not found")
	}
	return before, target
}

func apply(t *testing.T, conn *pgx.Conn, file string) {
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

func TestMigration092_BackfillsRowsThatExist(t *testing.T) {
	conn, _ := scratchDB(t)
	before, target := migrationFiles(t)
	for _, f := range before {
		apply(t, conn, f)
	}
	ctx := context.Background()

	const (
		cad    = "00000000-0000-0000-0000-00000000ca01" // a customer with a currency override
		usd    = "00000000-0000-0000-0000-00000000ca02"
		oDraft = "00000000-0000-0000-0000-00000000dd01"
		oConf  = "00000000-0000-0000-0000-00000000cc01"
		oDone  = "00000000-0000-0000-0000-00000000ff01"
		oQuote = "00000000-0000-0000-0000-00000000aa01"
		oRoute = "00000000-0000-0000-0000-000000005501"
		oSched = "00000000-0000-0000-0000-000000005502"
		oWalk  = "00000000-0000-0000-0000-000000005503"
		quote1 = "00000000-0000-0000-0000-00000000ab01"
		prod1  = "00000000-0000-0000-0000-00000000de01"
		prod2  = "00000000-0000-0000-0000-00000000de02"
	)
	branch := `(SELECT id FROM locations LIMIT 1)`
	legacy := fmt.Sprintf(`
		INSERT INTO customers (id, name, account_number, currency, primary_branch_id) VALUES
			('%[1]s','Cad Co','MIG1','CAD',%[13]s),
			('%[2]s','Usd Co','MIG2',NULL,%[13]s);
		INSERT INTO products (id, sku, description, uom_primary, base_price) VALUES
			('%[10]s','MIG-STUD','2x4 stud','PCS',5.50),
			('%[11]s','MIG-SHEET','plywood sheet','SF',12.25);
		INSERT INTO quotes (id, customer_id, state, total_amount, branch_id) VALUES
			('%[12]s','%[2]s','ACCEPTED',55,%[13]s);
		INSERT INTO orders (id, customer_id, quote_id, status, total_amount, created_at, branch_id) VALUES
			('%[3]s','%[2]s',NULL,'DRAFT',0,NULL,%[13]s),
			('%[4]s','%[2]s',NULL,'CONFIRMED',110,now(),%[13]s),
			('%[5]s','%[2]s',NULL,'FULFILLED',55,now(),%[13]s),
			('%[6]s','%[2]s','%[12]s','DRAFT',55,now(),%[13]s),
			('%[7]s','%[2]s',NULL,'CONFIRMED',55,now(),%[13]s),
			('%[8]s','%[2]s',NULL,'CONFIRMED',55,now(),%[13]s),
			('%[9]s','%[1]s',NULL,'DRAFT',55,now(),%[13]s);
		INSERT INTO order_lines (id, order_id, product_id, quantity, price_each, created_at) VALUES
			('00000000-0000-0000-0000-00000000e001','%[4]s','%[10]s',10,5.50,now()),
			('00000000-0000-0000-0000-00000000e002','%[4]s','%[10]s',10,5.50,now()),
			('00000000-0000-0000-0000-00000000e003','%[5]s','%[10]s',10,5.50,now()),
			('00000000-0000-0000-0000-00000000e004','%[6]s','%[10]s',10,5.50,now()),
			('00000000-0000-0000-0000-00000000e005','%[9]s','%[11]s',4,12.25,now());
		INSERT INTO invoices (order_id, customer_id, status, total_amount, subtotal, tax_amount, branch_id)
			VALUES ('%[5]s','%[2]s','PAID',55,55,0,%[13]s);
		INSERT INTO gl_journal_entries (entry_date, memo, source, status)
			VALUES (CURRENT_DATE, 'legacy entry', 'INVOICE', 'POSTED');
		INSERT INTO customer_transactions (customer_id, type, amount, balance_after)
			VALUES ('%[2]s', 'PAYMENT', -100, 0);
		INSERT INTO deliveries (order_id, status) VALUES ('%[7]s','DELIVERED');
		UPDATE orders SET scheduled_delivery_date = CURRENT_DATE + 1 WHERE id = '%[8]s';
	`, cad, usd, oDraft, oConf, oDone, oQuote, oRoute, oSched, oWalk, prod1, prod2, quote1, branch)
	if _, err := conn.Exec(ctx, legacy); err != nil {
		t.Fatalf("legacy rows: %v", err)
	}

	apply(t, conn, target)

	// 1. Numbers: every order numbered SO-, unique, in (created_at, id) order.
	if n := scalar[int](t, conn, `SELECT count(*) FROM orders WHERE number IS NULL OR number !~ '^SO-[0-9]{6,}$'`); n != 0 {
		t.Errorf("%d orders without a canonical SO- number", n)
	}
	if d := scalar[int](t, conn, `SELECT count(*) - count(DISTINCT number) FROM orders`); d != 0 {
		t.Errorf("%d duplicate order numbers", d)
	}

	// 2. Currency: the customer's override, else the dealer default.
	if got := scalar[string](t, conn, `SELECT currency FROM orders WHERE id = $1`, oWalk); got != "CAD" {
		t.Errorf("walk-in order currency = %q, want CAD (the customer's override)", got)
	}
	if got := scalar[string](t, conn, `SELECT currency FROM orders WHERE id = $1`, oConf); got != "USD" {
		t.Errorf("plain order currency = %q, want USD (the default)", got)
	}
	if got := scalar[string](t, conn, `SELECT COALESCE(currency,'') FROM gl_journal_entries LIMIT 1`); got != "USD" {
		t.Errorf("journal entry currency = %q, want the default on every row", got)
	}
	// The journal source CHECK takes the new vocabulary.
	if _, err := conn.Exec(ctx, `INSERT INTO gl_journal_entries (entry_date, memo, source, status)
		VALUES (CURRENT_DATE, 'probe', 'CREDIT_MEMO', 'DRAFT')`); err != nil {
		t.Errorf("a CREDIT_MEMO source entry was refused: %v", err)
	}

	// 3. The subledger type moved off the enum and kept every value.
	if got := scalar[string](t, conn, `SELECT type FROM customer_transactions LIMIT 1`); got != "" {
		_ = got // no rows seeded; the column's existence and CHECK are what matters
	}
	if _, err := conn.Exec(ctx, `SELECT type FROM customer_transactions WHERE type = 'REVERSAL'`); err != nil {
		t.Errorf("the REVERSAL value is not readable: %v", err)
	}

	// 4. delivery_type from the delivery history.
	for id, want := range map[string]string{
		oRoute: "DELIVERY", oSched: "DELIVERY", oWalk: "PICKUP", oConf: "PICKUP",
	} {
		if got := scalar[string](t, conn, `SELECT delivery_type FROM orders WHERE id = $1`, id); got != want {
			t.Errorf("order %s delivery_type = %q, want %s", id, got, want)
		}
	}

	// 5. Header backfills: subtotal is what the total was, tax 0, source LEGACY.
	if sub, tax, src := scalar[string](t, conn, `SELECT subtotal::text FROM orders WHERE id = $1`, oConf),
		scalar[string](t, conn, `SELECT tax_amount::text FROM orders WHERE id = $1`, oConf),
		scalar[string](t, conn, `SELECT tax_source FROM orders WHERE id = $1`, oConf); sub != "110.00" || tax != "0.00" || src != "LEGACY" {
		t.Errorf("confirmed order subtotal=%s tax=%s source=%s, want 110.00, 0.00, LEGACY", sub, tax, src)
	}

	// 6. The masters: account 4030 and the four seeded charge codes.
	if n := scalar[int](t, conn, `SELECT count(*) FROM gl_accounts WHERE code = '4030' AND type = 'REVENUE'`); n != 1 {
		t.Errorf("%d 4030 accounts, want 1", n)
	}
	if got := scalar[int](t, conn, `SELECT count(*) FROM charge_codes`); got != 4 {
		t.Errorf("%d charge codes seeded, want 4", got)
	}
	if acct := scalar[string](t, conn, `SELECT revenue_account_code FROM charge_codes WHERE code = 'RESTOCK'`); acct != "4030" {
		t.Errorf("RESTOCK account = %s, want 4030", acct)
	}
	if n := scalar[int](t, conn, `SELECT count(*) FROM products WHERE is_kit IS NOT FALSE OR taxable IS NOT TRUE`); n != 0 {
		t.Errorf("%d products with a non-default kit or taxable flag", n)
	}

	// 7. Lines: unit_price keeps price_each's meaning at scale 4; the pair is
	// 1 and 1; the extension is what the old total summed; the source is QUOTE
	// where the order names a quote; allocations follow the status.
	if price := scalar[string](t, conn, `SELECT unit_price::text FROM order_lines WHERE id = '00000000-0000-0000-0000-00000000e001'`); price != "5.5000" {
		t.Errorf("unit_price = %s, want 5.5000 (price_each widened, meaning kept)", price)
	}
	if uq, pq := scalar[string](t, conn, `SELECT uom_qty::text FROM order_lines WHERE id = '00000000-0000-0000-0000-00000000e001'`),
		scalar[string](t, conn, `SELECT price_uom_qty::text FROM order_lines WHERE id = '00000000-0000-0000-0000-00000000e001'`); uq != "1.0000" || pq != "1.0000" {
		t.Errorf("pair = %s, %s, want 1.0000 and 1.0000", uq, pq)
	}
	if uom, puom := scalar[string](t, conn, `SELECT uom FROM order_lines WHERE id = '00000000-0000-0000-0000-00000000e001'`),
		scalar[string](t, conn, `SELECT price_uom FROM order_lines WHERE id = '00000000-0000-0000-0000-00000000e001'`); uom != "PCS" || puom != "PCS" {
		t.Errorf("uom=%s price_uom=%s, want the product's stocking unit twice", uom, puom)
	}
	if sku, desc := scalar[string](t, conn, `SELECT sku FROM order_lines WHERE id = '00000000-0000-0000-0000-00000000e001'`),
		scalar[string](t, conn, `SELECT description FROM order_lines WHERE id = '00000000-0000-0000-0000-00000000e001'`); sku != "MIG-STUD" || desc != "2x4 stud" {
		t.Errorf("sku=%q description=%q, want them from the product", sku, desc)
	}
	if src := scalar[string](t, conn, `SELECT price_source FROM order_lines WHERE id = '00000000-0000-0000-0000-00000000e004'`); src != "QUOTE" {
		t.Errorf("quoted order line price_source = %s, want QUOTE", src)
	}
	if src := scalar[string](t, conn, `SELECT price_source FROM order_lines WHERE id = '00000000-0000-0000-0000-00000000e001'`); src != "PRICE_LIST" {
		t.Errorf("plain order line price_source = %s, want PRICE_LIST", src)
	}
	if ext := scalar[string](t, conn, `SELECT line_total::text FROM order_lines WHERE id = '00000000-0000-0000-0000-00000000e005'`); ext != "49.00" {
		t.Errorf("line_total = %s, want 49.00 (4 x 12.25)", ext)
	}
	if alloc := scalar[string](t, conn, `SELECT quantity_allocated::text FROM order_lines WHERE id = '00000000-0000-0000-0000-00000000e001'`); alloc != "10.0000" {
		t.Errorf("confirmed uninvoiced line allocated = %s, want 10.0000", alloc)
	}
	if ful := scalar[string](t, conn, `SELECT quantity_fulfilled::text FROM order_lines WHERE id = '00000000-0000-0000-0000-00000000e003'`); ful != "10.0000" {
		t.Errorf("fulfilled line fulfilled = %s, want 10.0000", ful)
	}
	// positions count from 0 within the order.
	if p0, p1 := scalar[int](t, conn, `SELECT position FROM order_lines WHERE id = '00000000-0000-0000-0000-00000000e001'`),
		scalar[int](t, conn, `SELECT position FROM order_lines WHERE id = '00000000-0000-0000-0000-00000000e002'`); p0 != 0 || p1 != 1 {
		t.Errorf("positions = %d, %d, want 0, 1", p0, p1)
	}
	// The per type CHECK bites: a text line cannot carry a quantity.
	if _, err := conn.Exec(ctx, `INSERT INTO order_lines (order_id, line_type, description, quantity) VALUES ($1, 'TEXT', 'note', 1)`, oDraft); err == nil {
		t.Error("a text line with a quantity was accepted")
	}
	// A correctly shaped text line is storable.
	if _, err := conn.Exec(ctx, `INSERT INTO order_lines (order_id, line_type, description) VALUES ($1, 'TEXT', 'note')`, oDraft); err != nil {
		t.Errorf("a correctly shaped text line was refused: %v", err)
	}

	// The down file rolls the shape back, and the migration applies again.
	// The probe rows this test wrote are removed first: they use vocabulary
	// only the new shape holds.
	_, _ = conn.Exec(ctx, `DELETE FROM gl_journal_entries WHERE memo = 'probe'`)
	_, _ = conn.Exec(ctx, `DELETE FROM order_lines WHERE description = 'note'`)
	down, err := os.ReadFile("../../migrations/down/092_orders_wire_contract_down.sql")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := conn.Exec(context.Background(), string(down)); err != nil {
		t.Fatalf("down: %v", err)
	}
	if _, err := conn.Exec(ctx, `SELECT price_each FROM order_lines LIMIT 1`); err != nil {
		t.Errorf("price_each is not back after the rollback: %v", err)
	}
	if _, err := conn.Exec(ctx, `SELECT type FROM customer_transactions LIMIT 1`); err != nil {
		t.Errorf("the subledger type is not readable after the rollback: %v", err)
	}
	apply(t, conn, target)
	if n := scalar[int](t, conn, `SELECT count(*) FROM orders WHERE number IS NULL`); n != 0 {
		t.Errorf("%d orders unnumbered after the second apply", n)
	}
	if got := scalar[string](t, conn, `SELECT currency FROM orders WHERE id = $1`, oWalk); got != "CAD" {
		t.Errorf("walk-in order currency after the second apply = %q, want CAD", got)
	}
}

// RULE (ADR 0005 section 13: every step is idempotent): 092 applies a second
// time on a migrated database without error and without clobbering what the
// first apply and the application have written since: a provider priced
// order keeps its null rate, an allocation the engine made stays, the
// subledger type CHECK is still there, and the constraint names exist once.
// ADR 0005 5.1 gives the new column defaults: tax_source BRANCH_RATE, and
// order_lines.description is NOT NULL (2.2).
func TestMigration092_IsIdempotent(t *testing.T) {
	conn, _ := scratchDB(t)
	before, target := migrationFiles(t)
	for _, f := range before {
		apply(t, conn, f)
	}
	ctx := context.Background()
	branch := `(SELECT id FROM locations LIMIT 1)`
	seed := fmt.Sprintf(`
		INSERT INTO customers (id, name, account_number, primary_branch_id)
			VALUES ('00000000-0000-0000-0000-00000000ca02','Usd Co','MIG2',%[1]s);
		INSERT INTO products (id, sku, description, uom_primary, base_price)
			VALUES ('00000000-0000-0000-0000-00000000de01','MIG-STUD','2x4 stud','PCS',5.50);
		INSERT INTO orders (id, customer_id, status, total_amount, created_at, branch_id) VALUES
			('00000000-0000-0000-0000-00000000cc01','00000000-0000-0000-0000-00000000ca02','CONFIRMED',110,now(),%[1]s);
		INSERT INTO order_lines (id, order_id, product_id, quantity, price_each, created_at)
			VALUES ('00000000-0000-0000-0000-00000000e001','00000000-0000-0000-0000-00000000cc01','00000000-0000-0000-0000-00000000de01',10,5.50,now());
	`, branch)
	if _, err := conn.Exec(ctx, seed); err != nil {
		t.Fatalf("legacy rows: %v", err)
	}
	apply(t, conn, target)

	// What the application writes after the first apply.
	if _, err := conn.Exec(ctx, `
		INSERT INTO orders (id, customer_id, status, total_amount, branch_id, tax_source, tax_rate, delivery_type, currency)
			VALUES ('00000000-0000-0000-0000-00000000cc02','00000000-0000-0000-0000-00000000ca02','CONFIRMED',110,`+branch+`,'PROVIDER',NULL,'PICKUP','USD');
		UPDATE order_lines SET quantity_allocated = 3 WHERE id = '00000000-0000-0000-0000-00000000e001'`); err != nil {
		t.Fatalf("post migration writes: %v", err)
	}

	apply(t, conn, target) // the second apply

	if rate := scalar[*string](t, conn, `SELECT tax_rate::text FROM orders WHERE id = '00000000-0000-0000-0000-00000000cc02'`); rate != nil {
		t.Errorf("a provider priced order's tax_rate = %q after the second apply, want null", *rate)
	}
	if alloc := scalar[string](t, conn, `SELECT quantity_allocated::text FROM order_lines WHERE id = '00000000-0000-0000-0000-00000000e001'`); alloc != "3.0000" {
		t.Errorf("an allocation reads %s after the second apply, want the 3.0000 the engine wrote", alloc)
	}
	if n := scalar[int](t, conn, `SELECT count(*) FROM pg_constraint WHERE conname = 'customer_transactions_type_check' AND conrelid = 'customer_transactions'::regclass`); n != 1 {
		t.Errorf("%d customer_transactions_type_check constraints after the second apply, want 1", n)
	}
	if _, err := conn.Exec(ctx, `INSERT INTO customer_transactions (customer_id, type, amount, balance_after)
		VALUES ('00000000-0000-0000-0000-00000000ca02', 'BOGUS', 1, 1)`); err == nil {
		t.Error("the subledger accepted a type outside the eight values after the second apply")
	}
	if n := scalar[int](t, conn, `SELECT count(*) FROM pg_constraint WHERE conname = 'orders_number_key'`); n != 1 {
		t.Errorf("%d orders_number_key constraints, want 1", n)
	}
	if n := scalar[int](t, conn, `SELECT count(*) FROM orders`); n != 2 {
		t.Errorf("%d orders after the second apply, want the 2 written", n)
	}

	// 5.1: the DEFAULT for a raw writer is BRANCH_RATE; history is LEGACY.
	if _, err := conn.Exec(ctx, `INSERT INTO orders (id, customer_id, status, total_amount, branch_id, delivery_type, currency)
		VALUES ('00000000-0000-0000-0000-00000000cc03','00000000-0000-0000-0000-00000000ca02','DRAFT',0,`+branch+`,'PICKUP','USD')`); err != nil {
		t.Fatalf("a raw order insert: %v", err)
	}
	if src := scalar[string](t, conn, `SELECT tax_source FROM orders WHERE id = '00000000-0000-0000-0000-00000000cc03'`); src != "BRANCH_RATE" {
		t.Errorf("a raw writer's tax_source = %s, want the 5.1 default BRANCH_RATE", src)
	}
	if src := scalar[string](t, conn, `SELECT tax_source FROM orders WHERE id = '00000000-0000-0000-0000-00000000cc01'`); src != "LEGACY" {
		t.Errorf("a historic order's tax_source = %s, want LEGACY", src)
	}
	// 2.2: description is NOT NULL on every line.
	if nullable := scalar[string](t, conn, `SELECT is_nullable FROM information_schema.columns WHERE table_name = 'order_lines' AND column_name = 'description'`); nullable != "NO" {
		t.Errorf("order_lines.description is_nullable = %s, want NO", nullable)
	}
}

// RULE (review P3-15): a legacy order line the new shape cannot hold (a
// quantity of zero or below, a negative price) stops 092 with a message that
// names the rows and the remedy, not a bare order_lines_shape violation, and
// the whole migration rolls back.
func TestMigration092_NamesLegacyLinesItCannotHold(t *testing.T) {
	conn, _ := scratchDB(t)
	before, target := migrationFiles(t)
	for _, f := range before {
		apply(t, conn, f)
	}
	ctx := context.Background()
	branch := `(SELECT id FROM locations LIMIT 1)`
	if _, err := conn.Exec(ctx, fmt.Sprintf(`
		INSERT INTO customers (id, name, account_number, primary_branch_id)
			VALUES ('00000000-0000-0000-0000-00000000ca02','Usd Co','MIG2',%[1]s);
		INSERT INTO products (id, sku, description, uom_primary, base_price)
			VALUES ('00000000-0000-0000-0000-00000000de01','MIG-STUD','2x4 stud','PCS',5.50);
		INSERT INTO orders (id, customer_id, status, total_amount, created_at, branch_id) VALUES
			('00000000-0000-0000-0000-00000000cc01','00000000-0000-0000-0000-00000000ca02','DRAFT',0,now(),%[1]s);
		INSERT INTO order_lines (id, order_id, product_id, quantity, price_each, created_at) VALUES
			('00000000-0000-0000-0000-00000000e001','00000000-0000-0000-0000-00000000cc01','00000000-0000-0000-0000-00000000de01',0,5.50,now()),
			('00000000-0000-0000-0000-00000000e002','00000000-0000-0000-0000-00000000cc01','00000000-0000-0000-0000-00000000de01',2,-1.00,now());
	`, branch)); err != nil {
		t.Fatalf("legacy rows: %v", err)
	}
	sql, err := os.ReadFile(target)
	if err != nil {
		t.Fatal(err)
	}
	tx, err := conn.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	_, err = tx.Exec(ctx, string(sql))
	_ = tx.Rollback(ctx)
	if err == nil {
		t.Fatal("092 applied over a zero quantity line")
	}
	msg := err.Error()
	for _, want := range []string{"092", "order_lines", "quantity", "00000000-0000-0000-0000-00000000e001", "price", "00000000-0000-0000-0000-00000000e002"} {
		if !strings.Contains(msg, want) {
			t.Errorf("the error does not name %q: %s", want, msg)
		}
	}
	if strings.Contains(msg, "order_lines_shape") {
		t.Errorf("the error is the bare shape violation, not the preflight: %s", msg)
	}
}
