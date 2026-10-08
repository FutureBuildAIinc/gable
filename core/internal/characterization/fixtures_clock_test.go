// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package characterization

import (
	"database/sql"
	"fmt"
	"testing"
	"time"
)

// seedClockWindowFixtures inserts, through the harness's own SQL on the fresh
// database, the rows that put data on both sides of every clock window the
// clock group pins. The demo seed dates every payment and invoice it writes on
// the seed day itself, so before these fixtures each window's edges had no rows
// to catch: a revenue-trend window of 6 instead of 7 days, or a yesterday
// comparison reaching two days back, answered identically.
//
// The rows mirror what the product writes (the payment, invoice and
// vendor-invoice repositories' INSERT statements: the same columns, dollar
// amounts, CHECK-constrained method and status values) rather than routing
// through product code, which would change the product to serve its test. Only
// the rows the windows read are written; the product's follow-on writes when a
// payment happens through the API (invoice status transition, GL postings) are
// not simulated, and no goldened query observes their absence.
//
// Offsets, each chosen to sit exactly on a window edge (midnight UTC, so the
// run's time of day cannot flip an edge):
//
//   - payments at day-1 and day-2: the summary's yesterday window is
//     [day-1, day0), so day-1 is its inside edge and day-2 its outside one -
//     widening the window to two days must change yesterday_revenue.
//   - payments at day-6, day-7 and day-8: the revenue trend's window is
//     `created_at >= now - 7 days`, so day-6 is the oldest day fully inside,
//     day-7 sits on the boundary (its midnight is before the request's
//     now-of-day, so it is outside, and a widened window pulls it in) and day-8
//     is clearly outside.
//   - invoices and bills at day-29 and day-31: the sales summary's default
//     window is the last 30 days, so day-29 is inside and day-31 outside; the
//     same two dates straddle the AR aging current bucket's <= 30 day edge
//     (aged by due date).
//   - invoices and bills at day-61 and day-91: the AR aging 61-90 bucket's
//     edges, and both past the AP aging 60-day boundary.
//
// The AR invoices belong to a harness-created customer, not a seeded one: the
// seed assigns its drawn invoices to customers inside a map-iteration loop, so
// a seeded customer's aging total varies per run, while a fixture customer's
// row is exactly these four invoices on every run.
func seedClockWindowFixtures(t *testing.T, dbURL string) {
	t.Helper()

	db, err := sql.Open("pgx", dbURL)
	if err != nil {
		t.Fatalf("open fixture db: %v", err)
	}
	defer db.Close()

	day := func(offset int) time.Time {
		return parseSeedDay(seedDay).AddDate(0, 0, offset)
	}
	stamp := func(offset int) string {
		return day(offset).Format("2006-01-02T15:04:05Z07:00")
	}

	// The fixture customer: same column shape the customer repository writes,
	// including the customer_branches mirror and the default-branch fallback.
	const fixtureCustomer = "11111111-1111-4111-8111-111111111111"
	mustExec(t, db, `INSERT INTO customers (
		id, name, account_number, email, phone, address,
		price_level_id, credit_limit, balance_due, is_active,
		tier,
		created_at, updated_at, primary_branch_id
	) VALUES ($1, 'Golden Window Fixture Co', 'GOLDEN-WIN-001', 'windows@example.com', '+1 250 555 0100', '1 Golden Row, Kelowna, BC',
		NULL, 0, 0, true,
		'RETAIL',
		$2, $2,
		COALESCE(NULL::uuid, (SELECT value::uuid FROM system_settings WHERE key = 'default_branch_id')))`,
		fixtureCustomer, stamp(0))
	mustExec(t, db, `INSERT INTO customer_branches (customer_id, branch_id)
		VALUES ($1, (SELECT value::uuid FROM system_settings WHERE key = 'default_branch_id'))
		ON CONFLICT DO NOTHING`,
		fixtureCustomer)

	// AR invoices, one per aging edge. Dollars, like every repository write;
	// statuses from the invoices CHECK constraint. Due date and created_at both
	// on the offset day, so AR aging (aged by due date) and the sales summary
	// (windowed by created_at) see the same edge. Each carries the one line the
	// invoice repository would write, against a seeded product.
	var productID string
	if err := db.QueryRow(`SELECT id FROM products WHERE sku = 'LUM-248-PREM'`).Scan(&productID); err != nil {
		t.Fatalf("fixture product: %v", err)
	}
	arInvoices := []struct {
		id     string
		lineID string
		days   int
		total  string
	}{
		{"11111111-1111-4111-8111-111111111201", "11111111-1111-4111-8111-111111113001", -29, "2900.29"},
		{"11111111-1111-4111-8111-111111111202", "11111111-1111-4111-8111-111111113002", -31, "3100.31"},
		{"11111111-1111-4111-8111-111111111203", "11111111-1111-4111-8111-111111113003", -61, "6100.61"},
		{"11111111-1111-4111-8111-111111111204", "11111111-1111-4111-8111-111111113004", -91, "9100.91"},
	}
	for _, inv := range arInvoices {
		at := stamp(inv.days)
		mustExec(t, db, `INSERT INTO invoices (
			id, order_id, customer_id, status, total_amount, subtotal, tax_rate, tax_amount,
			payment_terms, due_date, paid_at, created_at, updated_at, branch_id
		) VALUES ($1, NULL, $2, 'UNPAID', $3, $3, 0, 0,
			'NET30', $4, NULL, $5, $5,
			COALESCE(NULL::uuid, (SELECT value::uuid FROM system_settings WHERE key = 'default_branch_id')))`,
			inv.id, fixtureCustomer, inv.total, at, at)
		mustExec(t, db, `INSERT INTO invoice_lines (id, invoice_id, product_id, quantity, price_each, created_at)
			VALUES ($1, $2, $3, 1, $4, $5)`,
			inv.lineID, inv.id, productID, inv.total, at)
	}

	// Payments on the revenue-trend and yesterday edges, each referencing an
	// invoice created earlier than itself (as a product-written payment would),
	// with the columns and NULL gateway fields of the payment repository's
	// INSERT. Amounts are distinct per day so a shifted edge moves a number,
	// not just a row.
	payments := []struct {
		days   int
		amount string
		ref    string
		invID  string
	}{
		{-1, "101.01", "GOLD-WIN-P01", arInvoices[0].id},
		{-2, "202.02", "GOLD-WIN-P02", arInvoices[1].id},
		{-6, "606.06", "GOLD-WIN-P06", arInvoices[2].id},
		{-7, "707.07", "GOLD-WIN-P07", arInvoices[3].id},
		{-8, "808.08", "GOLD-WIN-P08", arInvoices[0].id},
	}
	for i, p := range payments {
		mustExec(t, db, `INSERT INTO payments (id, invoice_id, amount, method, reference, notes, created_at,
			gateway_tx_id, gateway_status, token_id, card_last4, card_brand, auth_code)
		VALUES ($1, $2, $3, 'CHECK', $4, 'golden clock window fixture', $5,
			NULL, NULL, NULL, NULL, NULL, NULL)`,
			fmt.Sprintf("11111111-1111-4111-8111-11111111%04d", 400+i), p.invID, p.amount, p.ref, stamp(p.days))
	}

	// AP bills on the aging edges, on the first seeded vendor (the same one the
	// vendor scenario anchor picks, deterministic across runs), in the column
	// shape of the AP repository's INSERT with the one line it would write.
	var vendorID string
	if err := db.QueryRow(`SELECT id FROM vendors ORDER BY name LIMIT 1`).Scan(&vendorID); err != nil {
		t.Fatalf("fixture vendor: %v", err)
	}
	bills := []struct {
		days  int
		total string
	}{
		{-29, "290.29"},
		{-31, "310.31"},
		{-61, "610.61"},
		{-91, "910.91"},
	}
	for i, b := range bills {
		at := stamp(b.days)
		number := fmt.Sprintf("GOLD-WIN-B%02d", -b.days)
		id := fmt.Sprintf("22222222-2222-4222-8222-22222222%04d", 100+i)
		mustExec(t, db, `INSERT INTO vendor_invoices (id, vendor_id, invoice_number, invoice_date, due_date, po_id,
			subtotal, tax_amount, total, amount_paid, status, notes, created_at)
		VALUES ($1, $2, $3, $4, $4, NULL,
			$5, 0, $5, 0, 'PENDING', 'golden clock window fixture', $6)`,
			id, vendorID, number, day(b.days).Format("2006-01-02"), b.total, at)
		mustExec(t, db, `INSERT INTO vendor_invoice_lines (id, invoice_id, description, quantity, unit_price, line_total, gl_account_id, created_at)
			VALUES ($1, $2, 'golden clock window fixture line', 1, $3, $3, NULL, $4)`,
			fmt.Sprintf("22222222-2222-4222-8222-22222222%04d", 200+i), id, b.total, at)
	}
}

func mustExec(t *testing.T, db *sql.DB, query string, args ...any) {
	t.Helper()
	if _, err := db.Exec(query, args...); err != nil {
		t.Fatalf("clock window fixture: %s: %v", query, err)
	}
}
