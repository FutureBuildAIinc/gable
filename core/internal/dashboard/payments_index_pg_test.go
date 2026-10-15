// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package dashboard

import (
	"context"
	"strings"
	"testing"

	"github.com/gablelbm/gable/internal/testutil"
	"github.com/gablelbm/gable/pkg/collections"
)

// The day reads are cut on received_on and refunded_on, so each needs an index:
// a dealer's history must not be scanned in full for every look at the
// dashboard and the till. The plan is read with sequential scans disabled, the
// honest way to show an index can serve the read on a table the planner would
// otherwise scan; without the index the only plan left is a sequential scan.
func TestCollectionsReadsAreServedByAnIndex(t *testing.T) {
	db := testutil.RequireDB(t)
	ctx := context.Background()
	for name, q := range map[string]struct{ sql, table, cond string }{
		"payments received": {`SELECT COALESCE(SUM(p.amount), 0) FROM ` + collections.ReceivedFrom("$1::date", 2, 3), "payments", "idx_payments_received_on_posted"},
		"refunds paid out":  {`SELECT COALESCE(SUM(r.amount), 0) FROM ` + collections.RefundedFrom("$1::date", 2, 3), "payment_refunds", "idx_payment_refunds_refunded_on_complete"},
	} {
		t.Run(name, func(t *testing.T) {
			tx, err := db.Pool.Begin(ctx)
			if err != nil {
				t.Fatal(err)
			}
			defer tx.Rollback(ctx)
			// Sequential scans off, and plain index scans off so the join's
			// own keys cannot answer for the date: what is left for the
			// table is the date index's bitmap scan, or nothing.
			for _, set := range []string{`SET LOCAL enable_seqscan = off`, `SET LOCAL enable_indexscan = off`} {
				if _, err := tx.Exec(ctx, set); err != nil {
					t.Fatal(err)
				}
			}
			rows, err := tx.Query(ctx, `EXPLAIN `+q.sql, "2026-01-01", nil, nil)
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
			if strings.Contains(text, "Seq Scan on "+q.table) {
				t.Errorf("the read scans %s in full:\n%s", q.table, text)
			}
			if !strings.Contains(text, q.cond) {
				t.Errorf("the read does not use %s:\n%s", q.cond, text)
			}
		})
	}
}
