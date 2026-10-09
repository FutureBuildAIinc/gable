// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package characterization

import (
	"database/sql"
	"fmt"
	"testing"
	"time"
)

// pinBranchZonesToUTC puts every branch on Etc/UTC, so the one place the
// server derives a business date in a branch's zone (order.BranchLocalDate,
// the invoice date and everything posted on it) agrees with the harness's UTC
// seed day at every hour. The seed leaves its branches in America/Vancouver and
// the schema defaults the rest to America/New_York; between UTC midnight and the
// zone's own midnight the branch's date is a day behind the seed day, which
// moved invoice due dates, the GL entry dates and their order by a day. The
// zone is data, not behaviour: the product still derives the date from whatever
// zone the branch holds, and TestBranchLocalDate pins that arithmetic at the
// instants the window opens and closes. Run before the fixtures and the
// scenarios so every group sees the same zones.
func pinBranchZonesToUTC(t *testing.T, dbURL string) {
	t.Helper()
	db, err := sql.Open("pgx", dbURL)
	if err != nil {
		t.Fatalf("open zone db: %v", err)
	}
	defer db.Close()
	mustExec(t, db, `UPDATE locations SET timezone = 'Etc/UTC' WHERE timezone <> 'Etc/UTC'`)
}

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
		// The shared line shape (ADR 0005 2.2): the fixture writes what the
		// invoice repository writes for a legacy line.
		mustExec(t, db, `INSERT INTO invoice_lines (id, invoice_id, product_id, quantity, price_each, created_at,
				sku, description, uom, price_uom, uom_qty, price_uom_qty, unit_price, line_total)
			SELECT $1, $2, $3, 1, $4, $5, p.sku, COALESCE(p.description, p.sku, ''), p.uom_primary::text, p.uom_primary::text, 1, 1, $4, $4
			FROM products p WHERE p.id = $3`,
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
	pinDispatchOrderRecency(t, db)
}

func mustExec(t *testing.T, db *sql.DB, query string, args ...any) {
	t.Helper()
	if _, err := db.Exec(query, args...); err != nil {
		t.Fatalf("clock window fixture: %s: %v", query, err)
	}
}

// dispatchRecencyOrder is the newest-first order the 13 dispatch-day orders are
// given distinct created_at values in, by customer name. The first five are the
// ones the dashboard's order-activity answer (the ten newest orders) shows, and
// they are in the order the golden recorded them.
var dispatchRecencyOrder = []string{
	"Peachland Framing Crew",
	"Summerland Roofers",
	"Big White Cabin Co",
	"Kelbrook Construction",
	"Mission Hill Custom",
	"Westbank Decks & Fence",
	"Glenmore Heritage Reno",
	"Vernon Valley Construction",
	"Okanagan DIY Owner",
	"Lake Country Builders",
	"Predator Ridge Renos",
	"Okanagan Homes Ltd",
	"Knox Mountain Landscapes",
}

// pinDispatchOrderRecency breaks the one tie the order-activity golden used to
// depend on. The seed writes all 13 dispatch-day orders with the same created_at
// (the dispatch date's midnight, two days back), and the dashboard's recent
// orders read is `ORDER BY created_at DESC LIMIT 10` with no tiebreak, so which
// of the 13 made the ten, and in what order, followed the physical row order
// and the plan's top-N sort: a different tuple layout (autovacuum timing, a
// reordered heap) flipped the answer and with it a total_amount in the
// transcript.
//
// The harness gives each dispatch order its own created_at, a few milliseconds
// after the shared midnight so the day offset the normaliser records (<ts-2d>)
// does not move, in the fixed newest-first order of dispatchRecencyOrder. No
// golden byte changes; the read is simply no longer left to break a tie. The
// guard fails the run loudly if the newest orders ever tie again.
func pinDispatchOrderRecency(t *testing.T, db *sql.DB) {
	t.Helper()

	for rank, name := range dispatchRecencyOrder {
		// Newest first: the earlier in the list, the larger the offset.
		offsetMS := len(dispatchRecencyOrder) - rank
		res, err := db.Exec(`UPDATE orders o
			SET created_at = o.created_at + ($2 * interval '1 millisecond')
			FROM customers c
			WHERE c.id = o.customer_id AND c.name = $1
			  AND o.status = 'CONFIRMED' AND o.scheduled_delivery_date IS NOT NULL`,
			name, offsetMS)
		if err != nil {
			t.Fatalf("pin dispatch order recency for %q: %v", name, err)
		}
		if n, _ := res.RowsAffected(); n != 1 {
			t.Fatalf("pin dispatch order recency for %q: updated %d orders, want 1", name, n)
		}
	}

	var ties int
	if err := db.QueryRow(`SELECT COUNT(*) FROM (
			SELECT created_at FROM (SELECT created_at FROM orders ORDER BY created_at DESC LIMIT 25) newest
			GROUP BY created_at HAVING COUNT(*) > 1) tied`).Scan(&ties); err != nil {
		t.Fatalf("check order recency ties: %v", err)
	}
	if ties != 0 {
		t.Fatalf("%d created_at values are shared by the newest orders: the order-activity read has no tiebreak, so its golden would depend on row layout", ties)
	}
}
