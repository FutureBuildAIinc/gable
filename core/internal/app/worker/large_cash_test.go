// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026, FutureBuild, Inc. and OpenLBM contributors

package worker

import (
	"context"
	"log/slog"
	"testing"
	"time"

	"github.com/gablelbm/gable/internal/testutil"
	"github.com/google/uuid"
)

// TestLargeCashDeadlineSweepMarksPastFilings: the deadline sweep marks the
// filings that are past their fifteen day deadline and still not filed, and
// a second pass marks nothing. The sweep is the worker's own job, so it is
// driven through the runner the worker starts.
func TestLargeCashDeadlineSweepMarksPastFilings(t *testing.T) {
	db := testutil.RequireDB(t)
	ctx := context.Background()
	branch, eventID := uuid.New(), uuid.New()
	exec := func(sql string, args ...any) {
		t.Helper()
		if _, err := db.Pool.Exec(ctx, sql, args...); err != nil {
			t.Fatalf("%s: %v", sql, err)
		}
	}
	exec(`INSERT INTO locations (id, type, code, name, country_code, default_tax_rate) VALUES ($1, 'BRANCH', $2, 'sweep fixture', 'US', 0)`,
		branch, "SW-"+branch.String()[:8])
	t.Cleanup(func() {
		_, _ = db.Pool.Exec(ctx, `DELETE FROM large_cash_filings WHERE event_id = $1`, eventID)
		_, _ = db.Pool.Exec(ctx, `DELETE FROM large_cash_events WHERE id = $1`, eventID)
		_, _ = db.Pool.Exec(ctx, `DELETE FROM locations WHERE id = $1`, branch)
	})
	// the crossing's receipt, twenty days ago: its deadline has passed
	exec(`INSERT INTO large_cash_events (id, payer_key, branch_id, rule_family, currency, amount_cents,
		total_cents, transaction_ids, tender_count, state, created_at)
		VALUES ($1, 'sweep:fixture', $2, 'US_8300', 'USD', 1200000, 1200000, ARRAY[$3::uuid], 1, 'reportable', $4)`,
		eventID, branch, uuid.New(), time.Now().UTC().AddDate(0, 0, -20))

	runner := newLargeCashDeadlineRunner(db, slog.Default())
	marked, err := runner.SweepOnce(ctx, time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	if marked < 1 {
		t.Fatalf("the sweep marked %d filings, want at least the fixture's one", marked)
	}
	var flag bool
	if err := db.Pool.QueryRow(ctx, `SELECT past_deadline FROM large_cash_filings WHERE event_id = $1`, eventID).Scan(&flag); err != nil {
		t.Fatal(err)
	}
	if !flag {
		t.Fatal("the sweep did not mark the filing past its deadline")
	}
	again, err := runner.SweepOnce(ctx, time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	if again != 0 {
		t.Fatalf("the second sweep marked %d filings, want none", again)
	}
}
