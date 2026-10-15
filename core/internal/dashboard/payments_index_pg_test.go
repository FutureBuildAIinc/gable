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
		"payments received": {`SELECT COALESCE(SUM(p.amount), 0) FROM ` + collections.ReceivedFrom("$1::date", 2, 3), "payments", "Index Cond: ((received_on"},
		// The refunds read joins its payment, whose own key the planner may
		// walk on an empty table; the date predicate is read on its table.
		"refunds paid out": {`SELECT COALESCE(SUM(r.amount), 0) FROM payment_refunds r
			WHERE r.status = 'COMPLETE' AND r.refunded_on BETWEEN $1::date AND $1::date
			  AND $2::uuid IS NULL AND $3::text IS NULL`, "payment_refunds", "Index Cond: ((refunded_on"},
	} {
		t.Run(name, func(t *testing.T) {
			tx, err := db.Pool.Begin(ctx)
			if err != nil {
				t.Fatal(err)
			}
			defer tx.Rollback(ctx)
			if _, err := tx.Exec(ctx, `SET LOCAL enable_seqscan = off`); err != nil {
				t.Fatal(err)
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
				t.Errorf("the date condition is not an index condition (want %q):\n%s", q.cond, text)
			}
		})
	}
}
