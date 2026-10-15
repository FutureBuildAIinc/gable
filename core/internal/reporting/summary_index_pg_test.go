// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package reporting

import (
	"context"
	"strings"
	"testing"

	"github.com/gablelbm/gable/internal/testutil"
	"github.com/google/uuid"
)

// The Sales Summary's invoice reads are cut on invoice_date, so each needs an
// index on it: neither read may scan the whole invoices table, with the wall
// open (an administrator) or under one yard. The plan is read with sequential
// and plain index scans off, so only a bitmap scan on an index of the date can
// serve it.
func TestSalesSummaryInvoiceReadsAreServedByAnIndex(t *testing.T) {
	db := testutil.RequireDB(t)
	ctx := context.Background()
	yard := uuid.New()
	for name, sql := range map[string]string{"invoiced": salesInvoicedSQL, "outstanding": salesOutstandingSQL} {
		for wall, args := range map[string][]any{
			"open wall": {"2026-01-01", "2026-01-31", nil, nil},
			"one yard":  {"2026-01-01", "2026-01-31", yard, nil},
		} {
			t.Run(name+" "+wall, func(t *testing.T) {
				tx, err := db.Pool.Begin(ctx)
				if err != nil {
					t.Fatal(err)
				}
				defer tx.Rollback(ctx)
				// A scratch history, rolled back with the transaction, so the
				// planner chooses by selectivity as it does on a dealer's
				// table (on an empty one it takes whichever index is first).
				cust, year, other := uuid.New(), 2016, uuid.New()
				for _, y := range []uuid.UUID{yard, other} {
					if _, err := tx.Exec(ctx, `INSERT INTO locations (id, type, code) VALUES ($1, 'BRANCH', $2)`, y, "IX-"+y.String()[:8]); err != nil {
						t.Fatalf("scratch yard: %v", err)
					}
				}
				if _, err := tx.Exec(ctx, `INSERT INTO customers (id, name, account_number, primary_branch_id)
					VALUES ($1, 'index leg', $2, $3)`, cust, "IX-"+cust.String()[:8], yard); err != nil {
					t.Fatalf("scratch customer: %v", err)
				}
				if _, err := tx.Exec(ctx, `INSERT INTO invoices (id, customer_id, branch_id, status, origin, number, currency,
						total_amount, subtotal, tax_rate, tax_amount, amount_open, due_date, invoice_date)
					SELECT gen_random_uuid(), $1, CASE WHEN g % 2 = 0 THEN $2::uuid ELSE $5::uuid END,
						CASE WHEN g % 5 = 0 THEN 'UNPAID' ELSE 'PAID' END, 'POS', 'IX-' || $4 || '-' || g, 'USD',
						10, 10, 0, 0, 0, make_date($3, 1, 1) + (g % 3000), make_date($3, 1, 1) + (g % 3000)
					FROM generate_series(1, 30000) g`, cust, yard, year, cust.String()[:8], other); err != nil {
					t.Fatalf("scratch invoices: %v", err)
				}
				if _, err := tx.Exec(ctx, `ANALYZE invoices`); err != nil {
					t.Fatalf("analyze: %v", err)
				}
				for _, set := range []string{`SET LOCAL enable_seqscan = off`, `SET LOCAL enable_indexscan = off`} {
					if _, err := tx.Exec(ctx, set); err != nil {
						t.Fatal(err)
					}
				}
				rows, err := tx.Query(ctx, `EXPLAIN `+sql, args...)
				if err != nil {
					t.Fatalf("explain: %v", err)
				}
				var plan []string
				for rows.Next() {
					var line string
					if err := rows.Scan(&line); err != nil {
						t.Fatal(err)
					}
					plan = append(plan, line)
				}
				rows.Close()
				text := strings.Join(plan, "\n")
				if strings.Contains(text, "Seq Scan on invoices") {
					t.Errorf("the read scans invoices in full:\n%s", text)
				}
				if !strings.Contains(text, "idx_invoices_invoice_date") && !strings.Contains(text, "idx_invoices_branch_invoice_date") {
					t.Errorf("the read does not use an index of invoice_date:\n%s", text)
				}
			})
		}
	}
}
