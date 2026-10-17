// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package purchase_order_test

import (
	"context"
	"testing"

	"github.com/gablelbm/gable/pkg/branchctx"
	"github.com/google/uuid"
)

// A reorder line never lands on a special order's draft: the draft is found
// by vendor, yard AND source.
func TestCreateReorders_ASpecialOrderDraftIsNotReused(t *testing.T) {
	s := seedCreateBranch(t)
	svc := newCreateBranchService(s)
	stockAt(t, s, s.branchA, 2)
	special := uuid.New()
	if _, err := s.db.Pool.Exec(context.Background(),
		`INSERT INTO purchase_orders (id, vendor_id, status, source, branch_id) VALUES ($1, $2, 'DRAFT', 'SPECIAL_ORDER', $3)`,
		special, s.vendor, s.branchA); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.CreateReorders(branchctx.WithSystem(context.Background())); err != nil {
		t.Fatalf("cron run: %v", err)
	}
	var onSpecial, drafts int
	_ = s.db.Pool.QueryRow(context.Background(),
		`SELECT COUNT(*) FROM purchase_order_lines WHERE po_id = $1`, special).Scan(&onSpecial)
	_ = s.db.Pool.QueryRow(context.Background(),
		`SELECT COUNT(*) FROM purchase_orders WHERE vendor_id = $1 AND status = 'DRAFT' AND source = 'REORDER' AND branch_id = $2`,
		s.vendor, s.branchA).Scan(&drafts)
	if onSpecial != 0 || drafts != 1 {
		t.Fatalf("lines on the special order's draft = %d, reorder drafts at yard A = %d, want 0 and 1", onSpecial, drafts)
	}
}

// A special order's line never lands on a reorder draft either.
func TestCreateFromSOLine_AReorderDraftIsNotReused(t *testing.T) {
	s := seedCreateBranch(t)
	ctx := context.Background()
	reorder := uuid.New()
	if _, err := s.db.Pool.Exec(ctx,
		`INSERT INTO purchase_orders (id, vendor_id, status, source, branch_id) VALUES ($1, $2, 'DRAFT', 'REORDER', $3)`,
		reorder, s.vendor, s.branchA); err != nil {
		t.Fatal(err)
	}
	cust, ord, soLine := uuid.New(), uuid.New(), uuid.New()
	exec := func(q string, args ...any) {
		t.Helper()
		if _, err := s.db.Pool.Exec(ctx, q, args...); err != nil {
			t.Fatalf("seed: %v", err)
		}
	}
	exec(`INSERT INTO customers (id, name, account_number, primary_branch_id) VALUES ($1, 'reorder r2 cust', $2, $3)`, cust, "RR-"+cust.String()[:8], s.branchA)
	exec(`INSERT INTO orders (id, customer_id, branch_id, status, total_amount, delivery_type, currency) VALUES ($1, $2, $3, 'CONFIRMED', 10, 'PICKUP', 'USD')`, ord, cust, s.branchA)
	exec(`INSERT INTO order_lines (id, order_id, product_id, quantity, uom, price_uom, uom_qty, price_uom_qty, stock_uom, stock_quantity, unit_price, line_total, description, is_special_order)
	      VALUES ($1, $2, $3, 1, 'EA', 'EA', 1, 1, 'EA', 1, 1, 1, 'r2 special', TRUE)`, soLine, ord, s.product)
	t.Cleanup(func() {
		_, _ = s.db.Pool.Exec(ctx, `DELETE FROM order_lines WHERE id = $1`, soLine)
		_, _ = s.db.Pool.Exec(ctx, `DELETE FROM orders WHERE id = $1`, ord)
		_, _ = s.db.Pool.Exec(ctx, `DELETE FROM customers WHERE id = $1`, cust)
	})
	svc := newCreateBranchService(s)
	if err := svc.CreateFromSOLine(ctx, soLine, s.branchA, &s.product, &s.vendor, "r2 special", 1, 1); err != nil {
		t.Fatalf("special order line: %v", err)
	}
	var onReorder, specials int
	_ = s.db.Pool.QueryRow(ctx, `SELECT COUNT(*) FROM purchase_order_lines WHERE po_id = $1`, reorder).Scan(&onReorder)
	_ = s.db.Pool.QueryRow(ctx,
		`SELECT COUNT(*) FROM purchase_orders WHERE vendor_id = $1 AND status = 'DRAFT' AND source = 'SPECIAL_ORDER' AND branch_id = $2`,
		s.vendor, s.branchA).Scan(&specials)
	if onReorder != 0 || specials != 1 {
		t.Fatalf("lines on the reorder draft = %d, special order drafts at yard A = %d, want 0 and 1", onReorder, specials)
	}
}
