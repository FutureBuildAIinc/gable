// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package purchase_order_test

import (
	"context"
	"testing"

	"github.com/gablelbm/gable/pkg/branchctx"
	"github.com/google/uuid"
)

// draftsOf returns the vendor's DRAFT purchase orders as branch -> line count
// for the seeded product.
func draftsOf(t *testing.T, s *createBranchSeed) map[uuid.UUID]int {
	t.Helper()
	rows, err := s.db.Pool.Query(context.Background(),
		`SELECT po.branch_id, COUNT(l.id) FROM purchase_orders po
		   LEFT JOIN purchase_order_lines l ON l.po_id = po.id AND l.product_id = $2
		  WHERE po.vendor_id = $1 AND po.status = 'DRAFT' GROUP BY po.branch_id`, s.vendor, s.product)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	out := map[uuid.UUID]int{}
	for rows.Next() {
		var b uuid.UUID
		var n int
		if err := rows.Scan(&b, &n); err != nil {
			t.Fatal(err)
		}
		out[b] = n
	}
	return out
}

func stockAt(t *testing.T, s *createBranchSeed, branch uuid.UUID, qty int) {
	t.Helper()
	if _, err := s.db.Pool.Exec(context.Background(),
		`INSERT INTO inventory (product_id, location_id, location, quantity, allocated) VALUES ($1, $2, 'row', $3, 0)`,
		s.product, branch, qty); err != nil {
		t.Fatalf("stock: %v", err)
	}
	t.Cleanup(func() {
		_, _ = s.db.Pool.Exec(context.Background(), `DELETE FROM inventory WHERE product_id = $1`, s.product)
	})
}

// The cron engine's run with two yards below their point writes two drafts,
// each at its own yard, and never adds yard B's line to yard A's draft.
func TestCreateReorders_TheCronOrdersEachYardForItself(t *testing.T) {
	s := seedCreateBranch(t)
	svc := newCreateBranchService(s)
	stockAt(t, s, s.branchA, 2)
	stockAt(t, s, s.branchB, 3)

	if _, err := svc.CreateReorders(branchctx.WithSystem(context.Background())); err != nil {
		t.Fatalf("cron run: %v", err)
	}
	got := draftsOf(t, s)
	if len(got) != 2 || got[s.branchA] != 1 || got[s.branchB] != 1 {
		t.Fatalf("drafts by yard = %v, want one draft with one line at each of the two yards", got)
	}
}

// A vendor's draft is reused only for the same vendor AND the same yard: a
// draft already at yard B stays yard B's, and yard A gets its own.
func TestCreateReorders_ADraftIsReusedOnlyAtItsOwnYard(t *testing.T) {
	s := seedCreateBranch(t)
	svc := newCreateBranchService(s)
	stockAt(t, s, s.branchA, 2)
	stockAt(t, s, s.branchB, 3)
	if _, err := s.db.Pool.Exec(context.Background(),
		`INSERT INTO purchase_orders (id, vendor_id, status, source, branch_id) VALUES ($1, $2, 'DRAFT', 'MANUAL', $3)`,
		uuid.New(), s.vendor, s.branchB); err != nil {
		t.Fatal(err)
	}

	if _, err := svc.CreateReorders(branchctx.WithSystem(context.Background())); err != nil {
		t.Fatalf("cron run: %v", err)
	}
	got := draftsOf(t, s)
	if len(got) != 2 || got[s.branchA] != 1 || got[s.branchB] != 1 {
		t.Fatalf("drafts by yard = %v, want yard B's own draft reused (one line) and a new one at yard A (one line)", got)
	}
}

// A caller who chose yard A orders yard A only.
func TestCreateReorders_AChosenYardOrdersThatYardOnly(t *testing.T) {
	s := seedCreateBranch(t)
	svc := newCreateBranchService(s)
	stockAt(t, s, s.branchA, 2)
	stockAt(t, s, s.branchB, 3)

	ctx := branchctx.With(context.Background(), &branchctx.Context{UserSub: "u", BranchID: &s.branchA})
	if _, err := svc.CreateReorders(ctx); err != nil {
		t.Fatalf("run: %v", err)
	}
	got := draftsOf(t, s)
	if len(got) != 1 || got[s.branchA] != 1 {
		t.Fatalf("drafts by yard = %v, want only yard A's", got)
	}
}
