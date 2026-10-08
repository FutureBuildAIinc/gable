// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package dashboard

import (
	"context"
	"sort"
	"testing"

	"github.com/gablelbm/gable/internal/testutil"
	"github.com/google/uuid"
)

// CORRECTNESS: orders created in one transaction share now(), so created_at
// alone cannot rank them. GetOrderActivity must break the tie on the order id
// so the recent-orders list a customer sees is the same on every call, which
// rows make the cut at the limit included, and survives a rewrite of the
// rows' physical layout.
//
// Skips cleanly when Postgres is unreachable (testutil.RequireDB).
func TestGetOrderActivity_TiedCreatedAtHasStableOrder(t *testing.T) {
	db := testutil.RequireDB(t)
	repo := NewRepository(db)
	ctx := context.Background()

	// A private branch keeps other tests' orders out of the list.
	branchID := uuid.New()
	if _, err := db.Pool.Exec(ctx,
		`INSERT INTO locations (id, type, code) VALUES ($1, 'BRANCH', $2)`,
		branchID, "DASH-"+branchID.String()[:8]); err != nil {
		t.Fatalf("seed branch: %v", err)
	}
	customerID := uuid.New()
	if _, err := db.Pool.Exec(ctx,
		`INSERT INTO customers (id, name, account_number, primary_branch_id) VALUES ($1, $2, $3, $4)`,
		customerID, "Dash PGTest "+customerID.String()[:8], "DASH-"+customerID.String()[:8], branchID); err != nil {
		t.Fatalf("seed customer: %v", err)
	}
	t.Cleanup(func() {
		bg := context.Background()
		_, _ = db.Pool.Exec(bg, `DELETE FROM orders WHERE branch_id = $1`, branchID)
		_, _ = db.Pool.Exec(bg, `DELETE FROM customers WHERE id = $1`, customerID)
		_, _ = db.Pool.Exec(bg, `DELETE FROM locations WHERE id = $1`, branchID)
	})

	const total, limit = 12, 5
	ids := make([]uuid.UUID, total)
	for i := range ids {
		ids[i] = uuid.New()
	}
	tx, err := db.Pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	for _, id := range ids {
		// created_at is left to its default, now(), which is the transaction
		// start time: identical for every row inserted here.
		if _, err := tx.Exec(ctx,
			`INSERT INTO orders (id, customer_id, branch_id, status, total_amount) VALUES ($1, $2, $3, 'CONFIRMED', 10)`,
			id, customerID, branchID); err != nil {
			_ = tx.Rollback(ctx)
			t.Fatalf("seed order: %v", err)
		}
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatalf("commit: %v", err)
	}

	// The tie break is the order id, newest (largest) first.
	sorted := append([]uuid.UUID(nil), ids...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].String() > sorted[j].String() })
	want := sorted[:limit]

	check := func(label string) {
		t.Helper()
		act, err := repo.GetOrderActivity(ctx, &branchID, limit)
		if err != nil {
			t.Fatalf("%s: GetOrderActivity: %v", label, err)
		}
		if len(act.RecentOrders) != limit {
			t.Fatalf("%s: got %d recent orders, want %d", label, len(act.RecentOrders), limit)
		}
		for i, o := range act.RecentOrders {
			if o.OrderID != want[i].String() {
				t.Fatalf("%s: position %d is %s, want %s (tied created_at must order by id DESC)", label, i, o.OrderID, want[i])
			}
		}
	}

	for i := 0; i < 5; i++ {
		check("repeat call")
	}

	// Rewrite the physical layout: each UPDATE writes a new tuple version, so
	// the heap order of the tied rows changes (reverse order here, one by one).
	for i := len(ids) - 1; i >= 0; i-- {
		if _, err := db.Pool.Exec(ctx, `UPDATE orders SET total_amount = total_amount + 1 WHERE id = $1`, ids[i]); err != nil {
			t.Fatalf("rewrite row: %v", err)
		}
	}
	check("after UPDATE rewrite")
	for i := 0; i < 5; i++ {
		check("repeat call after rewrite")
	}
}
