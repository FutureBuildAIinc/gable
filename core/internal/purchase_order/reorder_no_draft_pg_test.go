// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package purchase_order_test

import (
	"context"
	"testing"

	"github.com/gablelbm/gable/pkg/branchctx"
)

// onlyYardBAtItsPoint leaves the seeded product held at yard B exactly at its
// point (5) with no reorder quantity, and nowhere else: the group for the
// vendor and yard B has no line to write.
func onlyYardBAtItsPoint(t *testing.T, s *createBranchSeed) {
	t.Helper()
	ctx := context.Background()
	if _, err := s.db.Pool.Exec(ctx, `DELETE FROM inventory WHERE product_id = $1`, s.product); err != nil {
		t.Fatal(err)
	}
	setReorderQty(t, s, 0)
	stockAt(t, s, s.branchB, 5)
}

func draftCount(t *testing.T, s *createBranchSeed) int {
	t.Helper()
	var n int
	if err := s.db.Pool.QueryRow(context.Background(),
		`SELECT COUNT(*) FROM purchase_orders WHERE vendor_id = $1`, s.vendor).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

// A vendor and yard group with no line to write writes no draft and is not
// counted as created (no empty purchase order for a yard at its point).
func TestCreateReorders_NoDraftWithoutALine(t *testing.T) {
	s := seedCreateBranch(t)
	svc := newCreateBranchService(s)
	onlyYardBAtItsPoint(t, s)
	res, err := svc.CreateReordersRun(branchctx.WithSystem(context.Background()))
	if err != nil {
		t.Fatalf("cron run: %v", err)
	}
	if n := draftCount(t, s); n != 0 {
		t.Fatalf("purchase orders for the vendor = %d, want none (no line to write)", n)
	}
	if res.Created != 0 {
		t.Fatalf("created = %d, want 0 for a group with no lines", res.Created)
	}
	if len(res.NoReorderQty) != 1 {
		t.Fatalf("left alone = %v, want the one product named", res.NoReorderQty)
	}
}
