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
