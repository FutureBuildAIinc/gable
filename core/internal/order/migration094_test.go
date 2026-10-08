// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package order_test

// Migration 094 on rows that exist (recipe step 3, ADR 0005 section 13 step
// 8): the schema up to 092 is built in a scratch database, orders and
// invoices are written in the shape delivery completion left them (an order
// invoiced but never fulfilled), then 094 is applied and read back. The
// cycle 2 migration case: such an order lands FULFILLED with its allocation
// consumed, its invoice lines linked, the count reported, and a fulfilment
// after the migration bills nothing (that half is in the fulfilment tests).

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
)

func files094(t *testing.T) (before []string, target string) {
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
		case strings.HasPrefix(base, "094_"):
			target = f
		case base < "094_":
			before = append(before, f)
		}
	}
	if target == "" {
		t.Fatal("migration 094 not found")
	}
	return before, target
}

const (
	m94Cust  = "00000000-0000-0000-0000-0000000094c1"
	m94Prod  = "00000000-0000-0000-0000-0000000094d1"
	m94Prod2 = "00000000-0000-0000-0000-0000000094d2"
	m94OInv  = "00000000-0000-0000-0000-0000000094a1" // confirmed, invoiced by delivery completion
	m94OOpen = "00000000-0000-0000-0000-0000000094a2" // confirmed, not invoiced
	m94OTwin = "00000000-0000-0000-0000-0000000094a3" // confirmed, invoiced, one product on two lines
	m94L1    = "00000000-0000-0000-0000-0000000094b1"
	m94L2    = "00000000-0000-0000-0000-0000000094b2"
	m94L3a   = "00000000-0000-0000-0000-0000000094b3"
	m94L3b   = "00000000-0000-0000-0000-0000000094b4"
	m94Inv1  = "00000000-0000-0000-0000-0000000094e1"
	m94Inv3  = "00000000-0000-0000-0000-0000000094e3"
	m94Pos   = "00000000-0000-0000-0000-0000000094e9" // a counter invoice with no order
)

func seed094(t *testing.T, conn *pgx.Conn) {
	t.Helper()
	branch := `(SELECT id FROM locations WHERE type = 'BRANCH' LIMIT 1)`
	sql := fmt.Sprintf(`
		INSERT INTO customers (id, name, account_number, primary_branch_id) VALUES ('%[1]s','Fulfil Co','M94',%[7]s);
		INSERT INTO products (id, sku, description, uom_primary, base_price) VALUES
			('%[2]s','M94-STUD','2x4 stud','PCS',5.50),('%[3]s','M94-SHEET','plywood','SF',12.25);
		INSERT INTO locations (id, type, code, parent_id, branch_id) VALUES
			('00000000-0000-0000-0000-0000000094f1','YARD','M94-Y1',%[7]s,%[7]s);
		-- 40 studs on hand, 30 allocated (10 + 10 + 10 across the three orders)
		INSERT INTO inventory (product_id, location_id, location, quantity, allocated) VALUES
			('%[2]s','00000000-0000-0000-0000-0000000094f1','Y1',40,30),
			('%[3]s','00000000-0000-0000-0000-0000000094f1','Y1',2,5);
		INSERT INTO orders (id, customer_id, status, total_amount, branch_id, currency, delivery_type, created_at) VALUES
			('%[4]s','%[1]s','CONFIRMED',110,%[7]s,'USD','DELIVERY', now() - interval '3 hours'),
			('%[5]s','%[1]s','CONFIRMED',55,%[7]s,'USD','DELIVERY', now() - interval '2 hours'),
			('%[6]s','%[1]s','CONFIRMED',110,%[7]s,'USD','DELIVERY', now() - interval '1 hours');
		INSERT INTO order_lines (id, order_id, product_id, quantity, unit_price, description, uom, price_uom, uom_qty, price_uom_qty, line_total, quantity_allocated, created_at, position) VALUES
			('%[8]s','%[4]s','%[2]s',10,5.50,'2x4 stud','PCS','PCS',1,1,55.00,0,now(),0),
			('%[9]s','%[5]s','%[2]s',10,5.50,'2x4 stud','PCS','PCS',1,1,55.00,10,now(),0),
			('%[10]s','%[6]s','%[2]s',5,5.50,'2x4 stud','PCS','PCS',1,1,27.50,0,now(),0),
			('%[11]s','%[6]s','%[2]s',5,5.50,'2x4 stud','PCS','PCS',1,1,27.50,0,now(),1),
			('00000000-0000-0000-0000-0000000094b5','%[4]s','%[3]s',4,12.25,'plywood','SF','SF',1,1,49.00,0,now(),1);
		INSERT INTO invoices (id, order_id, customer_id, status, total_amount, subtotal, tax_amount, branch_id, created_at) VALUES
			('%[12]s','%[4]s','%[1]s','UNPAID',119.76,110,9.76,%[7]s, now()),
			('%[13]s','%[6]s','%[1]s','UNPAID',119.76,110,9.76,%[7]s, now()),
			('%[14]s',NULL,'%[1]s','UNPAID',10,10,0,%[7]s, now());
		INSERT INTO invoice_lines (invoice_id, product_id, quantity, price_each, created_at) VALUES
			('%[12]s','%[2]s',10,5.50,now()),
			('%[12]s','%[3]s',4,12.25,now()),
			('%[13]s','%[2]s',5,5.50,now()),
			('%[13]s','%[2]s',5,5.50,now() + interval '1 second');
	`, m94Cust, m94Prod, m94Prod2, m94OInv, m94OOpen, m94OTwin, branch, m94L1, m94L2, m94L3a, m94L3b, m94Inv1, m94Inv3, m94Pos)
	if _, err := conn.Exec(context.Background(), sql); err != nil {
		t.Fatalf("legacy rows: %v", err)
	}
}

func TestMigration094_MigratesInvoicedButNeverFulfilledOrders(t *testing.T) {
	conn, notices := scratchDB(t)
	before, target := files094(t)
	for _, f := range before {
		apply(t, conn, f)
	}
	seed094(t, conn)
	ctx := context.Background()
	apply(t, conn, target)

	// The order delivery completion invoiced lands FULFILLED, its lines
	// fulfilled in full with nothing allocated or backordered.
	if st := scalar[string](t, conn, `SELECT status FROM orders WHERE id = $1`, m94OInv); st != "FULFILLED" {
		t.Errorf("invoiced order status = %s, want FULFILLED", st)
	}
	if got := scalar[string](t, conn, `SELECT quantity_fulfilled::text || '/' || quantity_allocated::text || '/' || quantity_backordered::text FROM order_lines WHERE id = $1`, m94L1); got != "10.0000/0.0000/0.0000" {
		t.Errorf("invoiced line fulfilled/allocated/backordered = %s, want 10.0000/0.0000/0.0000", got)
	}
	// An order with no invoice is untouched: still CONFIRMED, its allocation kept.
	if st := scalar[string](t, conn, `SELECT status FROM orders WHERE id = $1`, m94OOpen); st != "CONFIRMED" {
		t.Errorf("uninvoiced order status = %s, want CONFIRMED", st)
	}
	if got := scalar[string](t, conn, `SELECT quantity_allocated::text FROM order_lines WHERE id = $1`, m94L2); got != "10.0000" {
		t.Errorf("uninvoiced line allocated = %s, want 10.0000", got)
	}

	// The allocation it held is consumed in inventory: 20 studs (10 + 10 of
	// the two invoiced orders) left on hand and the allocation; the uninvoiced
	// order's 10 stay allocated (30 held, 20 consumed, 10 left).
	if got := scalar[string](t, conn, `SELECT quantity::text || '/' || allocated::text FROM inventory WHERE product_id = $1`, m94Prod); got != "20.0000/10.0000" {
		t.Errorf("stud inventory quantity/allocated = %s, want 20.0000/10.0000", got)
	}
	// Plywood: 4 billed, only 5 allocated but 2 on hand: never below zero on
	// either column, the shortfall reported and not forced.
	if got := scalar[string](t, conn, `SELECT quantity::text || '/' || allocated::text FROM inventory WHERE product_id = $1`, m94Prod2); got != "0.0000/3.0000" {
		t.Errorf("plywood inventory quantity/allocated = %s, want 0.0000/3.0000", got)
	}
	joined := strings.Join(*notices, "\n")
	if !strings.Contains(joined, "094: 2 order(s) invoiced but never fulfilled migrated as fulfilled") {
		t.Errorf("the count is not reported: %s", joined)
	}
	if !strings.Contains(joined, m94Prod2) {
		t.Errorf("the plywood shortfall is not reported: %s", joined)
	}
	// No journal entry was invented.
	if n := scalar[int](t, conn, `SELECT count(*) FROM gl_journal_entries`); n != 0 {
		t.Errorf("%d journal entries after the migration, want none (COGS is not invented)", n)
	}

	// The invoice lines are linked to the order lines they bill: one product on
	// two lines links earliest first.
	if got := scalar[string](t, conn, `SELECT order_line_id::text FROM invoice_lines WHERE invoice_id = $1 AND product_id = $2`, m94Inv1, m94Prod); got != m94L1 {
		t.Errorf("invoice line links to %s, want order line %s", got, m94L1)
	}
	links := scalar[string](t, conn, `SELECT string_agg(order_line_id::text, ',' ORDER BY created_at) FROM invoice_lines WHERE invoice_id = $1`, m94Inv3)
	if links != m94L3a+","+m94L3b {
		t.Errorf("twin invoice lines link %s, want the two order lines earliest first", links)
	}

	// The invoice header columns: currency from the order, delivery type, the
	// business date, LEGACY tax, origin ORDER, and POS where no order.
	if got := scalar[string](t, conn, `SELECT currency || '/' || delivery_type || '/' || tax_source || '/' || origin FROM invoices WHERE id = $1`, m94Inv1); got != "USD/DELIVERY/LEGACY/ORDER" {
		t.Errorf("invoice header = %s, want USD/DELIVERY/LEGACY/ORDER", got)
	}
	if got := scalar[string](t, conn, `SELECT origin || '/' || delivery_type FROM invoices WHERE id = $1`, m94Pos); got != "POS/PICKUP" {
		t.Errorf("counter invoice = %s, want POS/PICKUP", got)
	}
	if d := scalar[bool](t, conn, `SELECT invoice_date IS NOT NULL FROM invoices WHERE id = $1`, m94Inv1); !d {
		t.Error("invoice_date is null")
	}
	// The invoice line shape: line_total from quantity x price, the pair 1/1,
	// the unit from the product, cost 0 and unit_cost null (history posted no COGS).
	if got := scalar[string](t, conn, `SELECT line_total::text || '/' || uom || '/' || uom_qty::text || '/' || cost::text || '/' || COALESCE(unit_cost::text, 'null') FROM invoice_lines WHERE invoice_id = $1 AND product_id = $2`, m94Inv1, m94Prod2); got != "49.00/SF/1.0000/0.00/null" {
		t.Errorf("invoice line = %s, want 49.00/SF/1.0000/0.00/null", got)
	}
	// The tax rate holds 0.08875 now.
	if _, err := conn.Exec(ctx, `UPDATE invoices SET tax_rate = 0.088750 WHERE id = $1`, m94Inv1); err != nil {
		t.Errorf("a 0.08875 rate does not fit the widened column: %v", err)
	}
	// The queue tables exist with the columns the workers read.
	for _, q := range []string{
		`SELECT order_id, position, requested_at FROM order_allocation_requests`,
		`SELECT delivery_id, order_id, position, attempts, last_error, parked_at, created_at FROM order_fulfillment_requests`,
	} {
		if _, err := conn.Exec(ctx, q); err != nil {
			t.Errorf("%s: %v", q, err)
		}
	}
	// The per type CHECK bites on invoice lines: a text line cannot carry a quantity.
	if _, err := conn.Exec(ctx, `INSERT INTO invoice_lines (invoice_id, line_type, description, quantity) VALUES ($1, 'TEXT', 'note', 1)`, m94Inv1); err == nil {
		t.Error("an invoice text line with a quantity was accepted")
	}
}

// RULE (ADR 0005 13: every step is idempotent): a second apply changes nothing
// the first apply or the application wrote since: an order invoiced after the
// first apply is not migrated as fulfilled by the second, a raw invoice keeps
// its origin, and the second apply reports no migrated orders.
func TestMigration094_IsIdempotent(t *testing.T) {
	conn, notices := scratchDB(t)
	before, target := files094(t)
	for _, f := range before {
		apply(t, conn, f)
	}
	seed094(t, conn)
	ctx := context.Background()
	apply(t, conn, target)

	branch := `(SELECT id FROM locations WHERE type = 'BRANCH' LIMIT 1)`
	// What the application writes afterwards: the open order is invoiced by a
	// fulfilment that has not moved it to FULFILLED yet (partial), and a counter
	// invoice with an order id is written.
	if _, err := conn.Exec(ctx, `
		INSERT INTO invoices (id, order_id, customer_id, status, total_amount, branch_id, origin)
		VALUES ('00000000-0000-0000-0000-0000000094e5', $1, $2, 'UNPAID', 10, `+branch+`, 'ORDER')`, m94OOpen, m94Cust); err != nil {
		t.Fatal(err)
	}
	*notices = nil
	apply(t, conn, target)
	if st := scalar[string](t, conn, `SELECT status FROM orders WHERE id = $1`, m94OOpen); st != "CONFIRMED" {
		t.Errorf("the second apply moved a freshly invoiced order to %s, want CONFIRMED", st)
	}
	if got := scalar[string](t, conn, `SELECT quantity::text || '/' || allocated::text FROM inventory WHERE product_id = $1`, m94Prod); got != "20.0000/10.0000" {
		t.Errorf("the second apply consumed more inventory: %s", got)
	}
	if strings.Contains(strings.Join(*notices, "\n"), "migrated as fulfilled") {
		t.Errorf("the second apply reported migrated orders: %v", *notices)
	}
	for _, name := range []string{"invoices_origin_check", "invoice_lines_shape", "invoices_tax_source_check"} {
		if n := scalar[int](t, conn, `SELECT count(*) FROM pg_constraint WHERE conname = $1`, name); n != 1 {
			t.Errorf("%d %s constraints after the second apply, want 1", n, name)
		}
	}

	// The down file rolls the shape back and the migration applies again.
	down, err := os.ReadFile("../../migrations/down/094_fulfilment_and_allocation_down.sql")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := conn.Exec(ctx, string(down)); err != nil {
		t.Fatalf("down: %v", err)
	}
	if _, err := conn.Exec(ctx, `SELECT currency FROM invoices LIMIT 1`); err == nil {
		t.Error("invoices.currency survived the rollback")
	}
	apply(t, conn, target)
	if n := scalar[int](t, conn, `SELECT count(*) FROM order_allocation_requests`); n != 0 {
		t.Errorf("%d allocation requests after re-applying", n)
	}
}

// RULE: an invoice line the new shape cannot hold (a quantity of zero or
// below, a negative price) stops 094 with a message naming the rows, and the
// migration rolls back whole.
func TestMigration094_NamesLegacyInvoiceLinesItCannotHold(t *testing.T) {
	conn, _ := scratchDB(t)
	before, target := files094(t)
	for _, f := range before {
		apply(t, conn, f)
	}
	seed094(t, conn)
	ctx := context.Background()
	if _, err := conn.Exec(ctx, `UPDATE invoice_lines SET quantity = 0 WHERE invoice_id = $1 AND product_id = $2`, m94Inv1, m94Prod2); err != nil {
		t.Fatal(err)
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
		t.Fatal("094 applied over a zero quantity invoice line")
	}
	for _, want := range []string{"094", "invoice_lines", "quantity"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the error does not name %q: %v", want, err)
		}
	}
	if strings.Contains(err.Error(), "invoice_lines_shape") {
		t.Errorf("the error is the bare shape violation: %v", err)
	}
}
