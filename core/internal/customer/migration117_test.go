// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package customer_test

// Migration 117 (ruling A): a customer with OPEN documents at a yard it holds
// no customer_branches row for gets the link, with one system audit row each;
// closed documents, a customer already linked and a document with no branch
// add nothing. The schema up to 115 is built in a scratch database, rows are
// made the old way (no link written), and 117 is applied. Replacing the file
// with SELECT 1 fails every count here.

import (
	"context"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/google/uuid"
)

func notice117(notices []string, fragment string) bool {
	for _, n := range notices {
		if strings.HasPrefix(n, "117:") && strings.Contains(n, fragment) {
			return true
		}
	}
	return false
}

func TestMigration117_LinksOpenDocumentsOnly(t *testing.T) {
	conn, notices := scratch113(t)
	all, err := filepath.Glob("../../migrations/*.sql")
	if err != nil || len(all) == 0 {
		t.Fatalf("no migrations found: %v", err)
	}
	sort.Strings(all)
	var target string
	for _, f := range all {
		base := filepath.Base(f)
		switch {
		case strings.HasSuffix(base, "_down.sql"):
		case strings.HasPrefix(base, "117_"):
			target = f
		case base < "117_":
			applyFile113(t, conn, f)
		}
	}
	if target == "" {
		t.Fatal("migration 117 not found")
	}
	ctx := context.Background()
	home, yard := uuid.New(), uuid.New()
	if _, err := conn.Exec(ctx, `INSERT INTO locations (id, type, code, name) VALUES
		($1, 'BRANCH', 'M117H', 'm117 home'), ($2, 'BRANCH', 'M117Y', 'm117 yard')`, home, yard); err != nil {
		t.Fatal(err)
	}
	// The migrated schema forbids a quote with no branch; the legacy shape the
	// migration reports is planted by lifting that rule here.
	if _, err := conn.Exec(ctx, `ALTER TABLE quotes ALTER COLUMN branch_id DROP NOT NULL`); err != nil {
		t.Fatal(err)
	}
	mk := func(label string) uuid.UUID {
		id := uuid.New()
		if _, err := conn.Exec(ctx, `INSERT INTO customers (id, name, account_number, primary_branch_id, balance_due)
			VALUES ($1, $2, $3, $4, 10)`, id, "m117 "+label, "M117-"+id.String()[:8], home); err != nil {
			t.Fatal(err)
		}
		if _, err := conn.Exec(ctx, `DELETE FROM customer_branches WHERE customer_id = $1`, id); err != nil {
			t.Fatal(err)
		}
		if _, err := conn.Exec(ctx, `INSERT INTO customer_branches (customer_id, branch_id) VALUES ($1, $2)`, id, home); err != nil {
			t.Fatal(err)
		}
		return id
	}
	openQuote, openOrder, openInvoice, openMemo, openPayment := mk("quote"), mk("order"), mk("invoice"), mk("memo"), mk("payment")
	closedOnly, linked, noBranch := mk("closed"), mk("linked"), mk("nobranch")
	exec := func(sql string, args ...any) {
		t.Helper()
		if _, err := conn.Exec(ctx, sql, args...); err != nil {
			t.Fatalf("%v: %s", err, sql)
		}
	}
	quote := func(c uuid.UUID, state string, branch any) {
		exec(`INSERT INTO quotes (id, customer_id, state, total_amount, branch_id) VALUES ($1, $2, $3::quote_state, 0, $4)`, uuid.New(), c, state, branch)
	}
	order := func(c uuid.UUID, status string, branch any) {
		exec(`INSERT INTO orders (id, customer_id, status, total_amount, subtotal, currency, branch_id) VALUES ($1, $2, $3, 0, 0, 'USD', $4)`, uuid.New(), c, status, branch)
	}
	quote(openQuote, "SENT", yard)
	quote(closedOnly, "EXPIRED", yard)
	order(openOrder, "CONFIRMED", yard)
	order(closedOnly, "FULFILLED", yard)
	order(linked, "CONFIRMED", home)
	quote(noBranch, "DRAFT", nil)
	exec(`INSERT INTO invoices (id, customer_id, status, total_amount, subtotal, tax_rate, tax_amount, branch_id, amount_open)
		VALUES ($1, $2, 'UNPAID', 50, 50, 0, 0, $3, 50)`, uuid.New(), openInvoice, yard)
	exec(`INSERT INTO invoices (id, customer_id, status, total_amount, subtotal, tax_rate, tax_amount, branch_id, amount_open)
		VALUES ($1, $2, 'PAID', 50, 50, 0, 0, $3, 0)`, uuid.New(), closedOnly, yard)
	exec(`INSERT INTO credit_memos (id, customer_id, branch_id, currency, reason_code, reason, amount, status, subtotal, tax_amount, total_amount, tax_rate, memo_date)
		VALUES ($1, $2, $3, 'USD', 'OTHER', 'm117', -5, 'DRAFT', -5, 0, -5, 0, CURRENT_DATE)`, uuid.New(), openMemo, yard)
	exec(`INSERT INTO payments (id, customer_id, branch_id, currency, method, amount, amount_unapplied, received_on)
		VALUES ($1, $2, $3, 'USD', 'CASH', 20, 20, CURRENT_DATE)`, uuid.New(), openPayment, yard)

	balances := scalar113[int64](t, conn, `SELECT SUM(ROUND(balance_due * 100))::bigint FROM customers`)
	rows := scalar113[int64](t, conn, `SELECT COUNT(*) FROM customer_branches`)
	docs := scalar113[int64](t, conn, `SELECT (SELECT COUNT(*) FROM quotes) + (SELECT COUNT(*) FROM orders) + (SELECT COUNT(*) FROM invoices)
		+ (SELECT COUNT(*) FROM credit_memos) + (SELECT COUNT(*) FROM payments)`)

	*notices = nil
	applyFile113(t, conn, target)
	linkedAtYard := func(c uuid.UUID) bool {
		return scalar113[int64](t, conn, `SELECT COUNT(*) FROM customer_branches WHERE customer_id = $1 AND branch_id = $2`, c, yard) == 1
	}
	for label, c := range map[string]uuid.UUID{"quote": openQuote, "order": openOrder, "invoice": openInvoice, "memo": openMemo, "payment": openPayment} {
		if !linkedAtYard(c) {
			t.Errorf("the customer with an open %s at the yard was not linked", label)
		}
	}
	for label, c := range map[string]uuid.UUID{"closed only": closedOnly, "already linked": linked, "no branch": noBranch} {
		if linkedAtYard(c) {
			t.Errorf("the %s customer was linked to the yard", label)
		}
	}
	if got := scalar113[int64](t, conn, `SELECT COUNT(*) FROM customer_branches`); got != rows+5 {
		t.Errorf("customer_branches holds %d rows, want %d", got, rows+5)
	}
	if got := scalar113[int64](t, conn, `SELECT COUNT(*) FROM audit_log WHERE action = 'customer.branch_linked' AND actor_id = 'system'
		AND changes->>'branch_id' = $1`, yard.String()); got != 5 {
		t.Errorf("%d audit rows name the yard, want 5", got)
	}
	if !notice117(*notices, "set 5 customer_branches link(s)") || !notice117(*notices, "1 open document(s) name a customer and no branch") {
		t.Errorf("the NOTICEs do not count the five links and the one branchless document: %v", *notices)
	}
	if got := scalar113[int64](t, conn, `SELECT SUM(ROUND(balance_due * 100))::bigint FROM customers`); got != balances {
		t.Errorf("customer balances moved from %d to %d", balances, got)
	}
	if got := scalar113[int64](t, conn, `SELECT (SELECT COUNT(*) FROM quotes) + (SELECT COUNT(*) FROM orders) + (SELECT COUNT(*) FROM invoices)
		+ (SELECT COUNT(*) FROM credit_memos) + (SELECT COUNT(*) FROM payments)`); got != docs {
		t.Errorf("the document count moved from %d to %d", docs, got)
	}

	// A second run, the down file and up again add nothing.
	*notices = nil
	applyFile113(t, conn, target)
	if !notice117(*notices, "set 0 customer_branches link(s)") {
		t.Errorf("the second run does not report zero links: %v", *notices)
	}
	down, err := filepath.Glob("../../migrations/down/117_*_down.sql")
	if err != nil || len(down) != 1 {
		t.Fatalf("migration 117 down file: %v %v", down, err)
	}
	applyFile113(t, conn, down[0])
	applyFile113(t, conn, target)
	if got := scalar113[int64](t, conn, `SELECT COUNT(*) FROM customer_branches`); got != rows+5 {
		t.Errorf("the rows moved to %d after down and up, want %d", got, rows+5)
	}
	if got := scalar113[int64](t, conn, `SELECT COUNT(*) FROM audit_log WHERE action = 'customer.branch_linked' AND actor_id = 'system'`); got != 5 {
		t.Errorf("%d audit rows after down and up, want 5", got)
	}
}
