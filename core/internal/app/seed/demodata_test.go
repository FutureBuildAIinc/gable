// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package seed

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gablelbm/gable/internal/account"
	"github.com/gablelbm/gable/internal/dashboard"
	"github.com/gablelbm/gable/internal/testutil"
)

// Tests for the demo data import (DEMO_SEED_IMPORT).
//
// TestDemoImport seeds a scratch database of its own, never the shared test
// database: it creates the database, applies every migration and runs the
// seed with the import, each through its command in a subprocess as an
// operator runs them, then checks what the import promises:
//
//  0. Every catalogue row and every customer row lands (a refused row is
//     only logged by the seed, so the count is the check).
//  1. Margin: every imported product has a nonzero target_margin and a
//     nonzero commission_rate.
//  2. Domain: no vendor, staff or customer row names the dealer on its legacy
//     "@gablelumber.ca" domain; the seed writes the reserved ".example" one.
//  3. Revenue trend: payments spread over many days, at each of the three
//     yards, so the dashboard's trend has more than one period.
//  4. Idempotency: a second seed run leaves every reference data row count
//     unchanged.
//  5. The history is real AR: every order and invoice has lines that sum to
//     its subtotal, every payment is numbered by the AR core, the subledger,
//     the statement, the ledger and the aging agree with the documents.

// TestDemoImportRequested pins the opt in: the import runs only when
// DEMO_SEED_IMPORT asks for it, so the default seed (the one the
// characterization goldens record) never carries it.
func TestDemoImportRequested(t *testing.T) {
	for _, tc := range []struct {
		value string
		want  bool
	}{
		{"", false},
		{"0", false},
		{"no", false},
		{"1", true},
		{"true", true},
		{" Yes ", true},
		{"on", true},
	} {
		t.Setenv(demoImportEnv, tc.value)
		if got := demoImportRequested(); got != tc.want {
			t.Errorf("%s=%q: demoImportRequested() = %v, want %v", demoImportEnv, tc.value, got, tc.want)
		}
	}
	// The history is on with the import unless it is turned off.
	for _, tc := range []struct {
		value string
		want  bool
	}{
		{"", true},
		{"1", true},
		{"0", false},
		{" Off ", false},
		{"false", false},
	} {
		t.Setenv(demoHistoryEnv, tc.value)
		if got := demoHistoryRequested(); got != tc.want {
			t.Errorf("%s=%q: demoHistoryRequested() = %v, want %v", demoHistoryEnv, tc.value, got, tc.want)
		}
	}
}

// bannedSeedWords are real brands and a real domain the seeds and the import
// never carry: a product is named for what it is, and a demo address ends in
// a reserved ".example" domain.
var bannedSeedWords = []string{
	"simpson", "strong-tie", "hardie", "cemtrim", "channellock", "romex",
	"dryline", "firecode", "brownwood", "turtle", "gable.com",
	"nordstrom", "kellogg", "zellweger",
	"quikrete", "tyvek", "proframe",
	"simp-", "ice & water shield", "speed square",
}

// TestSeedDataHoldsNoBrandWord reads the embedded import and fails on any
// banned word, so the check needs no database.
func TestSeedDataHoldsNoBrandWord(t *testing.T) {
	for name, data := range map[string][]byte{
		"catalog":    catalogData,
		"customers":  customersData,
		"staff":      staffData,
		"vendors":    vendorsData,
		"vendorkeys": vendorKeysData,
	} {
		lower := strings.ToLower(string(data))
		for _, word := range bannedSeedWords {
			if strings.Contains(lower, word) {
				t.Errorf("%s import holds %q", name, word)
			}
		}
	}
}

func TestDemoImport(t *testing.T) {
	if testing.Short() {
		t.Skip("seeds a scratch database of its own; skipped under -short")
	}
	db, dbURL := demoImportDB(t)
	runCommand(t, dbURL, "./cmd/migrate")
	// Migrations 080 and 118 together: a migrated database, before any seed
	// run, holds no row on the real domain.
	var onRealDomain int
	if err := db.QueryRow(`SELECT COUNT(*) FROM staff WHERE email ILIKE '%@gable.com'`).Scan(&onRealDomain); err != nil {
		t.Fatalf("count staff on the real domain: %v", err)
	}
	if onRealDomain != 0 {
		t.Errorf("a migrated database holds %d staff rows on @gable.com", onRealDomain)
	}
	// The tenant takes a currency other than the documents' column default
	// (USD), so a history document that fell back to the default would show.
	res, err := db.Exec(`UPDATE system_settings SET value = 'CAD' WHERE key IN ('currency.default', 'currency.enabled')`)
	if err != nil {
		t.Fatalf("set the tenant's currency: %v", err)
	}
	if n, err := res.RowsAffected(); err != nil || n != 2 {
		t.Fatalf("set the tenant's currency: %d settings changed (%v), want 2", n, err)
	}
	runCommand(t, dbURL, "./cmd/seed")
	first := snapshotCounts(t, db)
	if first.products == 0 || first.customers == 0 || first.vendors == 0 {
		t.Fatalf("the import wrote nothing: %+v", first)
	}

	t.Run("every catalogue and customer row lands", func(t *testing.T) {
		for _, c := range []struct {
			name, query, key string
			data             []byte
		}{
			{"catalogue", `SELECT COUNT(*) FROM products WHERE sku = ANY($1)`, "sku", catalogData},
			{"customers", `SELECT COUNT(*) FROM customers WHERE account_number = ANY($1)`, "account_number", customersData},
		} {
			keys := jsonlKeys(t, c.data, c.key)
			var n int
			if err := db.QueryRow(c.query, keys).Scan(&n); err != nil {
				t.Fatalf("%s: %v", c.name, err)
			}
			if n != len(keys) {
				t.Errorf("%s: %d of %d rows landed (the seed logs each refused row)", c.name, n, len(keys))
			}
		}
	})

	t.Run("every imported product carries its picture and the setting is on", func(t *testing.T) {
		var withPath, total int
		if err := db.QueryRow(`SELECT COUNT(*) FILTER (WHERE image_url LIKE '/catalog-illustrations/%.svg'), COUNT(*)
			FROM products WHERE sku = ANY($1)`, jsonlKeys(t, catalogData, "sku")).Scan(&withPath, &total); err != nil {
			t.Fatalf("pictures: %v", err)
		}
		if total == 0 || withPath != total {
			t.Errorf("%d of %d imported products carry a picture path", withPath, total)
		}
		var path string
		if err := db.QueryRow(`SELECT image_url FROM products WHERE sku = 'DL_SPF_2X4_L08_S'`).Scan(&path); err != nil ||
			path != "/catalog-illustrations/spf_framing_lumber.svg" {
			t.Errorf("DL_SPF_2X4_L08_S picture = %q (%v)", path, err)
		}
		for _, sku := range []string{"LUM-248-PREM", "OSB-12"} {
			var p *string
			if err := db.QueryRow(`SELECT image_url FROM products WHERE sku = $1`, sku).Scan(&p); err != nil || p == nil {
				t.Errorf("base product %s has no picture (%v)", sku, err)
			}
		}
		var setting string
		if err := db.QueryRow(`SELECT value FROM system_settings WHERE key = 'catalog.product_images'`).Scan(&setting); err != nil || setting != "on" {
			t.Errorf("catalog.product_images = %q (%v), want on", setting, err)
		}
	})

	t.Run("every imported product has a margin", func(t *testing.T) {
		rows, err := db.Query(`
			SELECT sku, target_margin, commission_rate
			FROM products
			WHERE sku NOT LIKE 'LUM-%'
			  AND sku NOT LIKE 'PLY-%'
			  AND sku NOT LIKE 'OSB-%'
			  AND sku NOT LIKE 'DW-%'
			  AND sku NOT LIKE 'NAIL-%'
			  AND sku NOT LIKE 'SCR-%'
			  AND sku NOT LIKE 'HANGER-%'
			  AND sku NOT LIKE 'TIE-%'
			  AND sku NOT LIKE 'RF-%'
			  AND sku NOT LIKE 'INS-%'
			  AND sku NOT LIKE 'DR-%'
			  AND sku NOT LIKE 'MLD-%'
			  AND sku NOT LIKE 'CORN%'
			  AND sku NOT LIKE 'LUMB%'`)
		if err != nil {
			t.Fatalf("query imported products: %v", err)
		}
		defer rows.Close()
		var bad []string
		for rows.Next() {
			var sku string
			var margin, commission float64
			if err := rows.Scan(&sku, &margin, &commission); err != nil {
				t.Fatalf("scan: %v", err)
			}
			if margin <= 0 {
				bad = append(bad, sku+" margin=0")
			}
			if commission <= 0 {
				bad = append(bad, sku+" commission=0")
			}
		}
		if err := rows.Err(); err != nil {
			t.Fatal(err)
		}
		if len(bad) > 0 {
			t.Fatalf("%d imported products miss a margin or commission: %s", len(bad), strings.Join(bad, ", "))
		}
	})

	t.Run("no row names the legacy dealer domain", func(t *testing.T) {
		for _, c := range []struct{ table, column string }{
			{"vendors", "name"},
			{"vendors", "contact_email"},
			{"staff", "email"},
			{"customers", "email"},
			{"sales_team", "email"},
		} {
			var n int
			q := fmt.Sprintf(`SELECT COUNT(*) FROM %s WHERE %s ILIKE '%%gablelumber.ca%%'`, c.table, c.column)
			if err := db.QueryRow(q).Scan(&n); err != nil {
				t.Fatalf("%s.%s: %v", c.table, c.column, err)
			}
			if n > 0 {
				t.Errorf("%s.%s: %d rows carry the legacy dealer domain", c.table, c.column, n)
			}
		}
	})

	t.Run("no row carries a brand word or the real domain", func(t *testing.T) {
		for _, c := range []struct{ table, column string }{
			{"products", "sku"},
			{"products", "description"},
			{"products", "vendor"},
			{"products", "category"},
			{"vendors", "name"},
			{"staff", "email"},
			{"staff", "full_name"},
		} {
			for _, word := range bannedSeedWords {
				var n int
				q := fmt.Sprintf(`SELECT COUNT(*) FROM %s WHERE %s ILIKE $1`, c.table, c.column)
				if err := db.QueryRow(q, "%"+word+"%").Scan(&n); err != nil {
					t.Fatalf("%s.%s: %v", c.table, c.column, err)
				}
				if n > 0 {
					t.Errorf("%s.%s: %d rows hold %q", c.table, c.column, n, word)
				}
			}
		}
	})

	t.Run("revenue trends across many days at each yard", func(t *testing.T) {
		var days int
		if err := db.QueryRow(`
			SELECT COUNT(DISTINCT DATE(p.created_at))
			FROM payments p
			WHERE p.created_at >= NOW() - interval '130 days'
			  AND p.amount > 0`).Scan(&days); err != nil {
			t.Fatalf("revenue trend count: %v", err)
		}
		if days < 30 {
			t.Fatalf("revenue trend has %d distinct days, want at least 30", days)
		}
		for _, yard := range []string{"KEL-MAIN", "WK", "LK"} {
			var yardDays int
			if err := db.QueryRow(`
				SELECT COUNT(DISTINCT DATE(p.created_at))
				FROM payments p
				JOIN orders o ON o.id = p.order_id
				JOIN locations l ON l.id = o.branch_id
				WHERE l.code = $1
				  AND p.created_at >= NOW() - interval '130 days'`, yard).Scan(&yardDays); err != nil {
				t.Fatalf("revenue trend %s: %v", yard, err)
			}
			if yardDays < 5 {
				t.Errorf("yard %s has %d days of revenue in the window, want at least 5", yard, yardDays)
			}
		}
	})

	t.Run("the history trades today, stamped before the seed ran", func(t *testing.T) {
		var now time.Time
		if err := db.QueryRow(`SELECT NOW()`).Scan(&now); err != nil {
			t.Fatal(err)
		}
		now = now.UTC()
		dayStart := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.UTC)
		if now.Weekday() == time.Sunday || now.Sub(dayStart) < 30*time.Minute {
			t.Skip("the yards are closed today, or the day is too young for the history to hold it")
		}
		var branchDay string
		if err := db.QueryRow(`SELECT (NOW() AT TIME ZONE COALESCE((SELECT l.timezone FROM locations l
			WHERE l.id::text = (SELECT value FROM system_settings WHERE key = 'default_branch_id')), 'UTC'))::date::text`).Scan(&branchDay); err != nil {
			t.Fatal(err)
		}
		if branchDay != now.Format("2006-01-02") {
			t.Skip("the server's UTC day is ahead of the branch's: the history trades the branch's day, which this check does not read")
		}
		keys := jsonlKeys(t, customersData, "account_number")
		var today int64
		var future int
		if err := db.QueryRow(`
			SELECT COALESCE(SUM(CASE WHEN p.created_at >= $2 THEN ROUND(p.amount * 100) ELSE 0 END), 0)::bigint,
				COUNT(*) FILTER (WHERE p.created_at > NOW())
			FROM payments p JOIN customers c ON c.id = p.customer_id
			WHERE c.account_number = ANY($1)`, keys, dayStart).Scan(&today, &future); err != nil {
			t.Fatalf("today's history payments: %v", err)
		}
		if today <= 0 {
			t.Fatalf("the history took no money today: the dashboard's day would read nothing")
		}
		if future != 0 {
			t.Fatalf("%d history payments are stamped after the seed ran", future)
		}
	})

	t.Run("history documents carry lines that sum to their subtotal", func(t *testing.T) {
		keys := jsonlKeys(t, customersData, "account_number")
		for _, c := range []struct{ name, docs, lines, fk string }{
			{"orders", "orders", "order_lines", "order_id"},
			{"invoices", "invoices", "invoice_lines", "invoice_id"},
		} {
			var total, bad int
			q := fmt.Sprintf(`
				SELECT COUNT(*),
				       COUNT(*) FILTER (WHERE d.subtotal IS DISTINCT FROM (SELECT SUM(l.line_total) FROM %s l WHERE l.%s = d.id))
				FROM %s d JOIN customers c ON c.id = d.customer_id
				WHERE c.account_number = ANY($1)`, c.lines, c.fk, c.docs)
			if err := db.QueryRow(q, keys).Scan(&total, &bad); err != nil {
				t.Fatalf("%s: %v", c.name, err)
			}
			if total == 0 {
				t.Fatalf("%s: the imported customers have none", c.name)
			}
			if bad > 0 {
				t.Errorf("%s: %d of %d have no lines or lines that do not sum to the subtotal", c.name, bad, total)
			}
		}
		var off int
		if err := db.QueryRow(`
			SELECT COUNT(*) FROM invoices i JOIN customers c ON c.id = i.customer_id
			WHERE c.account_number = ANY($1) AND i.total_amount <> i.subtotal + i.tax_amount`, keys).Scan(&off); err != nil {
			t.Fatal(err)
		}
		if off > 0 {
			t.Errorf("%d invoices whose total is not subtotal plus tax", off)
		}
		// The order carries the same tax as its invoice, so the order page's
		// tax line and total agree (its tax rate shows as a percentage).
		var orderOff int
		if err := db.QueryRow(`
			SELECT COUNT(*) FROM orders o JOIN invoices i ON i.order_id = o.id JOIN customers c ON c.id = o.customer_id
			WHERE c.account_number = ANY($1)
			  AND (o.tax_amount <> i.tax_amount OR o.total_amount <> o.subtotal + o.tax_amount OR o.tax_rate IS NULL)`,
			keys).Scan(&orderOff); err != nil {
			t.Fatal(err)
		}
		if orderOff > 0 {
			t.Errorf("%d history orders whose tax or total disagrees with the invoice", orderOff)
		}
	})

	t.Run("every payment is numbered by the AR core", func(t *testing.T) {
		var n int
		if err := db.QueryRow(`SELECT COUNT(*) FROM payments WHERE number !~ '^PAY-[0-9]{6}$'`).Scan(&n); err != nil {
			t.Fatal(err)
		}
		if n > 0 {
			t.Errorf("%d payments carry a number outside the PAY- scheme", n)
		}
	})

	t.Run("the subledger and the statement carry the history", func(t *testing.T) {
		keys := jsonlKeys(t, customersData, "account_number")
		var drift int
		if err := db.QueryRow(`
			SELECT COUNT(*) FROM customers c
			WHERE c.account_number = ANY($1)
			  AND COALESCE((SELECT SUM(t.amount) FROM customer_transactions t WHERE t.customer_id = c.id), 0)
			   <> COALESCE((SELECT SUM(ROUND(i.amount_open * 100)) FROM invoices i
			                WHERE i.customer_id = c.id AND i.status IN ('UNPAID', 'PARTIAL')), 0)`, keys).Scan(&drift); err != nil {
			t.Fatal(err)
		}
		if drift > 0 {
			t.Errorf("%d imported customers whose subledger differs from their open invoices", drift)
		}

		var cust string
		var invoices, payments int
		if err := db.QueryRow(`
			SELECT c.id, COUNT(DISTINCT i.id), COUNT(DISTINCT p.id)
			FROM customers c JOIN invoices i ON i.customer_id = c.id AND i.status = 'PAID'
			JOIN payments p ON p.customer_id = c.id
			WHERE c.account_number = ANY($1)
			GROUP BY c.id ORDER BY 2 DESC, 1 LIMIT 1`, keys).Scan(&cust, &invoices, &payments); err != nil {
			t.Fatalf("an imported customer with paid history: %v", err)
		}
		today := time.Now().UTC()
		var st account.Statement
		getJSON(t, accountMux(t, dbURL), fmt.Sprintf("/api/v1/ar/customers/%s/statement?from=%s&to=%s", cust,
			today.AddDate(0, 0, -130).Format("2006-01-02"), today.Format("2006-01-02")), &st)
		if len(st.Currencies) != 1 {
			t.Fatalf("statement has %d currencies, want 1", len(st.Currencies))
		}
		cur := st.Currencies[0]
		var inv, pay int
		days := map[string]bool{}
		for _, l := range cur.Lines {
			switch l.Type {
			case account.TransactionTypeInvoice:
				inv++
			case account.TransactionTypePayment:
				pay++
			}
			days[l.Date] = true
		}
		if inv != invoices || pay != invoices {
			t.Errorf("statement lists %d invoices and %d payments, want %d of each", inv, pay, invoices)
		}
		if invoices > 3 && len(days) < 3 {
			t.Errorf("statement lines fall on %d days: the history day is not on them", len(days))
		}
		for i := 1; i < len(cur.Lines); i++ {
			if cur.Lines[i].Date < cur.Lines[i-1].Date {
				t.Fatalf("statement lines out of day order at %d", i)
			}
		}
		if cur.ClosingBalanceCents != 0 {
			t.Errorf("closing balance %d, want 0 (every invoice is paid)", cur.ClosingBalanceCents)
		}
		_ = payments
	})

	t.Run("the ledger balances and carries the history's revenue", func(t *testing.T) {
		keys := jsonlKeys(t, customersData, "account_number")
		var debit, credit int64
		if err := db.QueryRow(`SELECT COALESCE(SUM(ROUND(debit * 100)), 0), COALESCE(SUM(ROUND(credit * 100)), 0) FROM gl_journal_lines`).Scan(&debit, &credit); err != nil {
			t.Fatal(err)
		}
		if debit != credit {
			t.Errorf("ledger debits %d differ from credits %d", debit, credit)
		}
		var subtotals, revenue int64
		if err := db.QueryRow(`
			SELECT COALESCE(SUM(ROUND(i.subtotal * 100)), 0) FROM invoices i
			JOIN customers c ON c.id = i.customer_id WHERE c.account_number = ANY($1)`, keys).Scan(&subtotals); err != nil {
			t.Fatal(err)
		}
		if err := db.QueryRow(`
			SELECT COALESCE(SUM(ROUND(l.credit * 100)), 0) FROM gl_journal_lines l
			JOIN gl_accounts a ON a.id = l.account_id AND a.code = '4010'
			JOIN gl_journal_entries e ON e.id = l.journal_entry_id AND e.source = 'INVOICE'
			JOIN invoices i ON i.id = e.source_ref_id
			JOIN customers c ON c.id = i.customer_id WHERE c.account_number = ANY($1)`, keys).Scan(&revenue); err != nil {
			t.Fatal(err)
		}
		if subtotals == 0 || revenue != subtotals {
			t.Errorf("Sales Revenue credits %d cents for the history, its invoices' subtotals are %d", revenue, subtotals)
		}
	})

	t.Run("the aging answers through every cursor with no history cash unapplied", func(t *testing.T) {
		keys := jsonlKeys(t, customersData, "account_number")
		mux := accountMux(t, dbURL)
		path := "/api/v1/ar/aging?group_by=customer&basis=due_date"
		cursor, pages := "", 0
		var items []account.AgingItem
		for {
			var page struct {
				Items      []account.AgingItem `json:"items"`
				NextCursor *string             `json:"next_cursor"`
			}
			next := path
			if cursor != "" {
				next += "&cursor=" + url.QueryEscape(cursor)
			}
			getJSON(t, mux, next, &page)
			items = append(items, page.Items...)
			pages++
			if page.NextCursor == nil || *page.NextCursor == "" {
				break
			}
			cursor = *page.NextCursor
		}
		if len(items) == 0 {
			t.Fatalf("the aging lists nothing (%d pages)", pages)
		}
		imported := map[string]bool{}
		rows, err := db.Query(`SELECT id FROM customers WHERE account_number = ANY($1)`, keys)
		if err != nil {
			t.Fatal(err)
		}
		for rows.Next() {
			var id string
			if err := rows.Scan(&id); err != nil {
				t.Fatal(err)
			}
			imported[id] = true
		}
		rows.Close()
		for _, it := range items {
			if imported[it.CustomerID.String()] && it.UnappliedCents != 0 {
				t.Fatalf("customer %s shows %d cents unapplied from the history", it.CustomerName, it.UnappliedCents)
			}
		}
	})

	t.Run("no money is dated after the seed's day and the aging agrees with the documents", func(t *testing.T) {
		var today string
		if err := db.QueryRow(`SELECT (NOW() AT TIME ZONE COALESCE((SELECT l.timezone FROM locations l
			WHERE l.id::text = (SELECT value FROM system_settings WHERE key = 'default_branch_id')), 'UTC'))::date::text`).Scan(&today); err != nil {
			t.Fatal(err)
		}
		var later, laterApps int
		if err := db.QueryRow(`SELECT COUNT(*) FROM payments WHERE received_on > $1::date`, today).Scan(&later); err != nil {
			t.Fatal(err)
		}
		if err := db.QueryRow(`SELECT COUNT(*) FROM ar_applications WHERE applied_on > $1::date`, today).Scan(&laterApps); err != nil {
			t.Fatal(err)
		}
		if later != 0 || laterApps != 0 {
			t.Errorf("%d payments are received and %d applications applied after the seed's day %s", later, laterApps, today)
		}

		// Each customer's aging total as of the seed's day is its open
		// invoices less its open credits and unapplied cash, read from the
		// documents themselves.
		want := map[string]int64{}
		rows, err := db.Query(`
			SELECT customer_id::text, SUM(cents)::bigint FROM (
				SELECT customer_id, ROUND(amount_open * 100) AS cents FROM invoices WHERE status <> 'VOID'
				UNION ALL
				SELECT customer_id, ROUND(amount_open * 100) FROM credit_memos WHERE status NOT IN ('DRAFT', 'VOID')
				UNION ALL
				SELECT customer_id, -ROUND(amount_unapplied * 100) FROM payments WHERE status = 'POSTED'
			) d GROUP BY customer_id`)
		if err != nil {
			t.Fatal(err)
		}
		for rows.Next() {
			var id string
			var cents int64
			if err := rows.Scan(&id, &cents); err != nil {
				t.Fatal(err)
			}
			want[id] = cents
		}
		rows.Close()
		var aging struct {
			Items []account.AgingItem `json:"items"`
		}
		mux := accountMux(t, dbURL)
		got := map[string]int64{}
		cursor := ""
		for {
			var page struct {
				Items      []account.AgingItem `json:"items"`
				NextCursor *string             `json:"next_cursor"`
			}
			next := "/api/v1/ar/aging?group_by=customer&basis=due_date&as_of=" + today
			if cursor != "" {
				next += "&cursor=" + url.QueryEscape(cursor)
			}
			getJSON(t, mux, next, &page)
			aging.Items = append(aging.Items, page.Items...)
			if page.NextCursor == nil || *page.NextCursor == "" {
				break
			}
			cursor = *page.NextCursor
		}
		for _, it := range aging.Items {
			got[it.CustomerID.String()] += int64(it.TotalCents)
		}
		for id, w := range want {
			if got[id] != w {
				t.Errorf("customer %s: the aging totals %d, its documents say %d", id, got[id], w)
			}
		}
		for id, g := range got {
			if _, ok := want[id]; !ok && g != 0 {
				t.Errorf("customer %s: the aging totals %d, it has no document", id, g)
			}
		}
	})

	t.Run("every customer's statement net balance equals its aging total", func(t *testing.T) {
		var today string
		if err := db.QueryRow(`SELECT (NOW() AT TIME ZONE COALESCE((SELECT l.timezone FROM locations l
			WHERE l.id::text = (SELECT value FROM system_settings WHERE key = 'default_branch_id')), 'UTC'))::date::text`).Scan(&today); err != nil {
			t.Fatal(err)
		}
		mux := accountMux(t, dbURL)
		rows, err := db.Query(`SELECT DISTINCT customer_id::text FROM customer_transactions`)
		if err != nil {
			t.Fatal(err)
		}
		var ids []string
		for rows.Next() {
			var id string
			if err := rows.Scan(&id); err != nil {
				t.Fatal(err)
			}
			ids = append(ids, id)
		}
		rows.Close()
		if len(ids) == 0 {
			t.Fatal("no customer has subledger rows")
		}
		for _, id := range ids {
			var st account.Statement
			getJSON(t, mux, fmt.Sprintf("/api/v1/ar/customers/%s/statement?from=2000-01-01&to=%s", id, today), &st)
			var net int64
			for _, c := range st.Currencies {
				net += int64(c.NetBalanceCents)
			}
			var ag account.AgingSummary
			getJSON(t, mux, fmt.Sprintf("/api/v1/ar/aging/summary?customer_id=%s&as_of=%s", id, today), &ag)
			var total int64
			for _, c := range ag.Totals {
				total += int64(c.TotalCents)
			}
			if net != total {
				t.Errorf("customer %s: the statement's net balance %d, the aging's total %d", id, net, total)
			}
		}
	})

	t.Run("the dashboard's Outstanding AR equals the aging summary's total", func(t *testing.T) {
		ar, err := newReceivables(dbURL)
		if err != nil {
			t.Fatal(err)
		}
		defer ar.close()
		ctx := context.Background()
		var today time.Time
		if err := db.QueryRow(`SELECT (NOW() AT TIME ZONE COALESCE((SELECT l.timezone FROM locations l
			WHERE l.id::text = (SELECT value FROM system_settings WHERE key = 'default_branch_id')), 'UTC'))::date`).Scan(&today); err != nil {
			t.Fatal(err)
		}
		aging, err := ar.acct.AgingSummary(ctx, account.AgingQuery{AsOf: today, Basis: "due_date"})
		if err != nil {
			t.Fatal(err)
		}
		var total int64
		for _, c := range aging.Totals {
			total += int64(c.TotalCents)
		}
		// The seed's invoices and the aging both read the document day; the
		// dashboard must read the same receivable the aging does.
		summary, err := dashboard.NewRepository(ar.db).GetDashboardSummary(ctx, nil, nil)
		if err != nil {
			t.Fatal(err)
		}
		if summary.OutstandingAR != total {
			t.Errorf("Outstanding AR %d, aging summary total %d", summary.OutstandingAR, total)
		}
		// And both equal the documents: open invoices less open credits and
		// unapplied cash.
		var docs int64
		if err := db.QueryRow(`
			SELECT COALESCE(SUM(cents), 0)::bigint FROM (
				SELECT ROUND(amount_open * 100) AS cents FROM invoices WHERE status <> 'VOID'
				UNION ALL
				SELECT ROUND(amount_open * 100) FROM credit_memos WHERE status NOT IN ('DRAFT', 'VOID')
				UNION ALL
				SELECT -ROUND(amount_unapplied * 100) FROM payments WHERE status = 'POSTED'
			) d`).Scan(&docs); err != nil {
			t.Fatal(err)
		}
		if docs <= 0 || summary.OutstandingAR != docs {
			t.Errorf("Outstanding AR %d, the documents say %d", summary.OutstandingAR, docs)
		}
	})

	t.Run("the tenant's currency is on every imported customer and history document", func(t *testing.T) {
		keys := jsonlKeys(t, customersData, "account_number")
		var bad int
		for _, q := range []string{
			`SELECT COUNT(*) FROM customers c WHERE c.account_number = ANY($1)
			   AND c.currency IS DISTINCT FROM (SELECT value FROM system_settings WHERE key = 'currency.default')`,
			`SELECT COUNT(*) FROM orders d JOIN customers c ON c.id = d.customer_id WHERE c.account_number = ANY($1)
			   AND d.currency IS DISTINCT FROM (SELECT value FROM system_settings WHERE key = 'currency.default')`,
			`SELECT COUNT(*) FROM invoices d JOIN customers c ON c.id = d.customer_id WHERE c.account_number = ANY($1)
			   AND d.currency IS DISTINCT FROM (SELECT value FROM system_settings WHERE key = 'currency.default')`,
			`SELECT COUNT(*) FROM payments d JOIN customers c ON c.id = d.customer_id WHERE c.account_number = ANY($1)
			   AND d.currency IS DISTINCT FROM (SELECT value FROM system_settings WHERE key = 'currency.default')`,
		} {
			if err := db.QueryRow(q, keys).Scan(&bad); err != nil {
				t.Fatal(err)
			}
			if bad > 0 {
				t.Errorf("%d rows are not in the tenant's currency: %s", bad, strings.Join(strings.Fields(q)[:6], " "))
			}
		}
	})

	t.Run("every imported product names its vendor", func(t *testing.T) {
		var n int
		if err := db.QueryRow(`SELECT COUNT(*) FROM products p JOIN vendors v ON v.id = p.vendor_id
			WHERE p.vendor IS DISTINCT FROM v.name`).Scan(&n); err != nil {
			t.Fatal(err)
		}
		if n > 0 {
			t.Errorf("%d products with a vendor carry no vendor name in the vendor column", n)
		}
	})

	t.Run("a second run repaves the same history and changes no reference data", func(t *testing.T) {
		history := func() (count int, cents int64, invoices int) {
			// The imported customers only: the base seed's own documents are
			// drawn without the fixed seed.
			keys := jsonlKeys(t, customersData, "account_number")
			if err := db.QueryRow(`SELECT COUNT(*), COALESCE(SUM(ROUND(p.amount * 100)), 0)::bigint FROM payments p
				JOIN customers c ON c.id = p.customer_id WHERE c.account_number = ANY($1)`, keys).Scan(&count, &cents); err != nil {
				t.Fatal(err)
			}
			if err := db.QueryRow(`SELECT COUNT(*) FROM invoices i
				JOIN customers c ON c.id = i.customer_id WHERE c.account_number = ANY($1)`, keys).Scan(&invoices); err != nil {
				t.Fatal(err)
			}
			return
		}
		c1, s1, i1 := history()
		runCommand(t, dbURL, "./cmd/seed")
		if second := snapshotCounts(t, db); second != first {
			t.Errorf("reference data moved on a second run: first %+v, second %+v", first, second)
		}
		// The fixed random seed gives the same history for the same day, and
		// the run truncates before it writes, so nothing doubles.
		if c2, s2, i2 := history(); c2 != c1 || s2 != s1 || i2 != i1 {
			t.Errorf("the second run's history differs: payments %d (%d cents) and %d invoices, then %d (%d cents) and %d", c1, s1, i1, c2, s2, i2)
		}
	})
}

// demoImportDB creates a scratch database beside the shared test database
// and drops it when the test ends. It answers a handle and the URL the
// commands run against.
func demoImportDB(t *testing.T) (*sql.DB, string) {
	t.Helper()
	base, err := url.Parse(testutil.RequireDB(t).Pool.Config().ConnString())
	if err != nil {
		t.Fatal(err)
	}
	admin, err := sql.Open("pgx", base.String())
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	suffix := make([]byte, 6)
	if _, err := rand.Read(suffix); err != nil {
		t.Fatal(err)
	}
	name := "seed_demoimport_" + hex.EncodeToString(suffix)
	if _, err := admin.ExecContext(ctx, fmt.Sprintf(`CREATE DATABASE %q`, name)); err != nil {
		admin.Close()
		t.Skipf("cannot create a scratch database: %v", err)
	}
	t.Cleanup(func() {
		_, _ = admin.ExecContext(context.Background(), fmt.Sprintf(`DROP DATABASE %q WITH (FORCE)`, name))
		admin.Close()
	})

	scratch := *base
	scratch.Path = "/" + name
	db, err := sql.Open("pgx", scratch.String())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	return db, scratch.String()
}

const demoSeedInstantEnv = "SEED_NOW"

var demoInstant struct {
	sync.Once
	at string
}

// demoSeedInstant is the one instant the package's seed runs read: the first
// time a test asks.
func demoSeedInstant(t *testing.T) string {
	t.Helper()
	demoInstant.Do(func() { demoInstant.at = time.Now().UTC().Format(time.RFC3339) })
	return demoInstant.at
}

// runCommand runs one of the repository's commands (by its package path
// under core/, where the commands run so migrate finds migrations/) on the
// scratch database with the demo seed and its import asked for.
func runCommand(t *testing.T, dbURL, pkg string) {
	t.Helper()
	cmd := exec.Command("go", "run", pkg)
	cmd.Dir = "../../.."
	cmd.Env = append(os.Environ(),
		// every seed run of one test reads the same instant, so a boundary
		// between two runs cannot move the second history
		demoSeedInstantEnv+"="+demoSeedInstant(t),
		"DATABASE_URL="+dbURL,
		demoSeedEnv+"=1",
		demoImportEnv+"=1",
	)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("go run %s: %v\n%s", pkg, err, out)
	}
}

// jsonlKeys answers one string field of every row of an embedded jsonl file.
func jsonlKeys(t *testing.T, data []byte, field string) []string {
	t.Helper()
	var keys []string
	for _, line := range strings.Split(string(data), "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		var row map[string]any
		if err := json.Unmarshal([]byte(line), &row); err != nil {
			t.Fatalf("%s: %v", field, err)
		}
		key, _ := row[field].(string)
		if key == "" {
			t.Fatalf("a row has no %s: %s", field, line)
		}
		keys = append(keys, key)
	}
	return keys
}

// importCounts are the reference data row counts a second run must keep.
type importCounts struct{ products, vendors, customers, categories, staff int }

func snapshotCounts(t *testing.T, db *sql.DB) importCounts {
	t.Helper()
	var c importCounts
	for _, q := range []struct {
		dst   *int
		query string
	}{
		{&c.products, `SELECT COUNT(*) FROM products`},
		{&c.vendors, `SELECT COUNT(*) FROM vendors`},
		{&c.customers, `SELECT COUNT(*) FROM customers`},
		{&c.categories, `SELECT COUNT(*) FROM product_categories WHERE slug <> 'general'`},
		{&c.staff, `SELECT COUNT(*) FROM staff`},
	} {
		if err := db.QueryRow(q.query).Scan(q.dst); err != nil {
			t.Fatalf("%s: %v", q.query, err)
		}
	}
	return c
}

// accountMux serves the account module's routes over the scratch database,
// unguarded, as the package's tests call them.
func accountMux(t *testing.T, dbURL string) *http.ServeMux {
	t.Helper()
	ar, err := newReceivables(dbURL)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(ar.close)
	mux := http.NewServeMux()
	account.NewHandler(ar.acct).RegisterRoutes(mux, nil, nil)
	return mux
}

// getJSON answers 200 or fails with the body, and decodes it into dst.
func getJSON(t *testing.T, mux http.Handler, path string, dst any) {
	t.Helper()
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("GET %s: %d %s", path, rec.Code, rec.Body.String())
	}
	if err := json.Unmarshal(rec.Body.Bytes(), dst); err != nil {
		t.Fatalf("GET %s: %v", path, err)
	}
}

// removedDesignations are one maker's model designations the demo copied
// (FROM-PRINCIPAL item 135 a). A product is named for its kind and size, so no
// whole token of these appears in the import, the base seed, the picture keys
// or the sample list, in any case.
var removedDesignations = []string{
	"h1", "h2.5", "hd88", "a35", "lus28", "lus210", "h10a", "tp35", "phd2", "cs16",
	"ht1", "ht2", "tie-h1",
}

// holdsDesignation reports the first removed designation that appears in text
// as a whole token (the characters around it are not letters or digits).
func holdsDesignation(text string) (string, bool) {
	lower := strings.ToLower(text)
	for _, d := range removedDesignations {
		for from := 0; ; {
			i := strings.Index(lower[from:], d)
			if i < 0 {
				break
			}
			start, end := from+i, from+i+len(d)
			isWord := func(b byte) bool { return b >= 'a' && b <= 'z' || b >= '0' && b <= '9' }
			if (start == 0 || !isWord(lower[start-1])) && (end == len(lower) || !isWord(lower[end])) {
				return d, true
			}
			from = start + 1
		}
	}
	return "", false
}

// designationSources are the texts the designation guard reads: the import,
// the base seed's source and picture keys, and the illustrations' text and
// file names (the SVG titles and drawn words).
func designationSources(t *testing.T) map[string]string {
	t.Helper()
	sources := map[string]string{
		"catalog": string(catalogData), "vendors": string(vendorsData), "vendorkeys": string(vendorKeysData),
	}
	for _, dir := range []string{".", "../../../cmd/seed"} {
		entries, err := os.ReadDir(dir)
		if err != nil {
			t.Fatal(err)
		}
		for _, e := range entries {
			name := e.Name()
			if e.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
				continue
			}
			data, err := os.ReadFile(filepath.Join(dir, name))
			if err != nil {
				t.Fatal(err)
			}
			key := name
			if dir != "." {
				key = dir + "/" + name
			}
			sources[key] = string(data)
		}
	}
	files, err := filepath.Glob(filepath.Join("..", "..", "..", "..", "web", "apps", "desk", "public", "catalog-illustrations", "*.svg"))
	if err != nil || len(files) == 0 {
		t.Fatalf("no illustrations found: %v", err)
	}
	for _, f := range files {
		data, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		// the file's name is part of what a person can read of it
		sources["illustration/"+filepath.Base(f)] = filepath.Base(f) + "\n" + string(data)
	}
	return sources
}

// TestSeedDataHoldsNoModelDesignation reads the import, the base seed's own
// source and its picture keys for a removed designation as a whole token.
func TestSeedDataHoldsNoModelDesignation(t *testing.T) {
	for name, text := range designationSources(t) {
		if d, ok := holdsDesignation(text); ok {
			t.Errorf("%s holds the model designation %q", name, d)
		}
	}
}

// TestDesignationGuardReadsEverySeedSource: the guard's reach is every non
// test Go file of the seed and of cmd/seed and every illustration, so a
// designation cannot return through a file nobody listed.
func TestDesignationGuardReadsEverySeedSource(t *testing.T) {
	read := designationSources(t)
	for _, dir := range []string{".", "../../../cmd/seed"} {
		entries, err := os.ReadDir(dir)
		if err != nil {
			t.Fatal(err)
		}
		for _, e := range entries {
			name := e.Name()
			if e.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
				continue
			}
			key := name
			if dir != "." {
				key = dir + "/" + name
			}
			if _, ok := read[key]; !ok {
				t.Errorf("the designation guard does not read %s", key)
			}
		}
	}
	files, err := filepath.Glob(filepath.Join("..", "..", "..", "..", "web", "apps", "desk", "public", "catalog-illustrations", "*.svg"))
	if err != nil || len(files) == 0 {
		t.Fatalf("no illustrations found: %v", err)
	}
	for _, f := range files {
		if _, ok := read["illustration/"+filepath.Base(f)]; !ok {
			t.Errorf("the designation guard does not read illustration %s", filepath.Base(f))
		}
	}
}

// TestHoldsDesignationFindsAToken proves the check itself on text that holds
// one: an illustration title and a file name.
func TestHoldsDesignationFindsAToken(t *testing.T) {
	for _, text := range []string{`<title>Framing angle A35</title>`, "lus28_hanger.svg", "CN_H10A"} {
		if _, ok := holdsDesignation(text); !ok {
			t.Errorf("%q holds a designation and the check missed it", text)
		}
	}
	if _, ok := holdsDesignation("joist hanger 2x8 face mount"); ok {
		t.Errorf("a plain name was flagged")
	}
}

// TestSeedKeysAndVendorsNameNoProFrame: "proframe" in any field of the import,
// the keys and the email domains (item 134), not only in descriptions.
func TestSeedKeysAndVendorsNameNoProFrame(t *testing.T) {
	for name, data := range map[string][]byte{
		"catalog": catalogData, "vendors": vendorsData, "vendorkeys": vendorKeysData,
		"customers": customersData, "staff": staffData, "categories": categoriesData,
	} {
		if strings.Contains(strings.ToLower(string(data)), "proframe") {
			t.Errorf("%s names ProFrame", name)
		}
	}
}

// emailPattern finds an email address in text.
var emailPattern = regexp.MustCompile(`[A-Za-z0-9._%+-]+@([A-Za-z0-9-]+\.)+[A-Za-z]{2,}`)

// TestSeedEmailsAreOnReservedDomains (item 108 b's rule): every email address
// in the import files and in the seed's own source ends in ".example" or is on
// "example.com", never on a real top level domain an invented customer or
// vendor could be mistaken to own.
func TestSeedEmailsAreOnReservedDomains(t *testing.T) {
	sources := map[string]string{
		"catalog": string(catalogData), "customers": string(customersData), "staff": string(staffData),
		"vendors": string(vendorsData), "vendorkeys": string(vendorKeysData),
	}
	for _, dir := range []string{".", "../../../cmd/seed"} {
		entries, err := os.ReadDir(dir)
		if err != nil {
			t.Fatal(err)
		}
		for _, e := range entries {
			name := e.Name()
			if e.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
				continue
			}
			data, err := os.ReadFile(dir + "/" + name)
			if err != nil {
				t.Fatal(err)
			}
			sources[dir+"/"+name] = string(data)
		}
	}
	for name, text := range sources {
		for _, addr := range emailPattern.FindAllString(text, -1) {
			domain := strings.ToLower(addr[strings.LastIndex(addr, "@")+1:])
			if strings.HasSuffix(domain, ".example") || domain == "example.com" {
				continue
			}
			t.Errorf("%s holds the email address %q on a real domain", name, addr)
		}
	}
}

// FROM-PRINCIPAL 118 a (review round 1): a product is named for what it is, so
// no product description in the import names a maker. The invented vendor
// records and their keys stay; only the descriptions are checked.
func TestImportDescriptionsNameNoMaker(t *testing.T) {
	for i, line := range strings.Split(string(catalogData), "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		var row struct {
			SKU         string `json:"sku"`
			Description string `json:"description"`
		}
		if err := json.Unmarshal([]byte(line), &row); err != nil {
			t.Fatalf("catalog line %d: %v", i+1, err)
		}
		if strings.Contains(strings.ToLower(row.Description), "proframe") {
			t.Errorf("%s: description %q names a maker", row.SKU, row.Description)
		}
	}
}

// FROM-PRINCIPAL 119 names "Ridgeline" and "Stone as a brand" among the brand
// words, so no product is named for an invented brand. The word stone stays
// where it is a material ("Landscape and Stone", a stone product); only a
// product led by it as a mark, and the vendor named for it, are refused.
func TestCatalogNamesNoInventedBrand(t *testing.T) {
	for i, line := range strings.Split(string(catalogData), "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		var row map[string]any
		if err := json.Unmarshal([]byte(line), &row); err != nil {
			t.Fatalf("catalog line %d: %v", i+1, err)
		}
		sku, _ := row["sku"].(string)
		for field, v := range row {
			if s, ok := v.(string); ok && strings.Contains(strings.ToLower(s), "ridgeline") {
				t.Errorf("%s: %s %q names the invented brand Ridgeline", sku, field, s)
			}
		}
		if d, _ := row["description"].(string); strings.HasPrefix(strings.ToLower(d), "stone ") {
			t.Errorf("%s: %q is led by the invented brand Stone", sku, d)
		}
	}
	var keys struct {
		Vendors []struct {
			Key   string `json:"key"`
			Name  string `json:"name"`
			Notes string `json:"notes"`
		} `json:"vendors"`
	}
	if err := json.Unmarshal(vendorKeysData, &keys); err != nil {
		t.Fatal(err)
	}
	for _, v := range keys.Vendors {
		if strings.Contains(strings.ToLower(v.Notes), "ridgeline") {
			t.Errorf("vendor %s: the notes name the invented brand Ridgeline", v.Key)
		}
		if strings.Contains(strings.ToLower(v.Name), "stone hardware") {
			t.Errorf("vendor %s: %q is the invented brand Stone", v.Key, v.Name)
		}
	}
}

// TestVendorNamesSitBesideNoRealBusiness: two vendor names read next to real
// businesses of the same trade and region (the review of PR 161).
func TestVendorNamesSitBesideNoRealBusiness(t *testing.T) {
	for _, name := range []string{"fraser valley engineered", "okanagan power tool"} {
		for src, data := range map[string][]byte{"vendors": vendorsData, "vendorkeys": vendorKeysData} {
			if strings.Contains(strings.ToLower(string(data)), name) {
				t.Errorf("%s holds the name %q, which sits beside a real business", src, name)
			}
		}
	}
}

// TestEngineeredWoodAndHangersCarryTheirOwnKind: the LVL and I-joist rows are
// not graded with a dimension lumber grade, and the face mount hangers draw as
// joist hangers.
func TestEngineeredWoodAndHangersCarryTheirOwnKind(t *testing.T) {
	for i, line := range strings.Split(string(catalogData), "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		var row struct {
			SKU         string `json:"sku"`
			Category    string `json:"category_slug"`
			Grade       string `json:"grade"`
			ImageFamily string `json:"image_family"`
		}
		if err := json.Unmarshal([]byte(line), &row); err != nil {
			t.Fatalf("catalog line %d: %v", i+1, err)
		}
		if row.Category == "lumber.engineered_wood" && strings.EqualFold(row.Grade, "standard") {
			t.Errorf("%s: the engineered wood grade %q is a dimension lumber grade", row.SKU, row.Grade)
		}
		if (row.SKU == "CN_FMH28" || row.SKU == "CN_FMH210") && row.ImageFamily != "joist_hanger" {
			t.Errorf("%s draws as %q, want joist_hanger", row.SKU, row.ImageFamily)
		}
	}
}
