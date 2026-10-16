// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package purchase_order_test

import (
	"context"
	"testing"

	"github.com/gablelbm/gable/pkg/branchctx"
)

func setReorderQty(t *testing.T, s *createBranchSeed, qty int) {
	t.Helper()
	if _, err := s.db.Pool.Exec(context.Background(),
		`UPDATE products SET reorder_qty = $2 WHERE id = $1`, s.product, qty); err != nil {
		t.Fatal(err)
	}
}

// With no reorder quantity set, a yard exactly at its point has a deficit of
// zero and gets no line: never a one unit line.
func TestCreateReorders_NoReorderQtyAtThePointOrdersNothing(t *testing.T) {
	s := seedCreateBranch(t)
	svc := newCreateBranchService(s)
	setReorderQty(t, s, 0)
	stockAt(t, s, s.branchB, 5) // the point is 5
	if _, err := svc.CreateReorders(branchctx.WithSystem(context.Background())); err != nil {
		t.Fatalf("cron run: %v", err)
	}
	var lines int
	if err := s.db.Pool.QueryRow(context.Background(),
		`SELECT COUNT(*) FROM purchase_order_lines l JOIN purchase_orders po ON po.id = l.po_id
		  WHERE po.vendor_id = $1 AND l.product_id = $2 AND po.branch_id = $3`, s.vendor, s.product, s.branchB).Scan(&lines); err != nil {
		t.Fatal(err)
	}
	if lines != 0 {
		t.Fatalf("lines = %d at yard B, want none for a product at its point with no reorder quantity", lines)
	}
}

// Below its point with no reorder quantity, a yard orders the deficit (up to
// the point), not one unit.
func TestCreateReorders_NoReorderQtyBelowThePointOrdersTheDeficit(t *testing.T) {
	s := seedCreateBranch(t)
	svc := newCreateBranchService(s)
	setReorderQty(t, s, 0)
	stockAt(t, s, s.branchB, 2) // the point is 5: a deficit of 3
	if _, err := svc.CreateReorders(branchctx.WithSystem(context.Background())); err != nil {
		t.Fatalf("cron run: %v", err)
	}
	var qty float64
	if err := s.db.Pool.QueryRow(context.Background(),
		`SELECT l.quantity::float8 FROM purchase_order_lines l JOIN purchase_orders po ON po.id = l.po_id
		  WHERE po.vendor_id = $1 AND l.product_id = $2 AND po.branch_id = $3`, s.vendor, s.product, s.branchB).Scan(&qty); err != nil {
		t.Fatalf("line at yard B: %v", err)
	}
	if qty != 3 {
		t.Fatalf("quantity = %g, want the deficit 3", qty)
	}
}
