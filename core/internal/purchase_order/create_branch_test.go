// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package purchase_order_test

import (
	"context"
	"strings"
	"testing"

	"github.com/gablelbm/gable/internal/product"
	"github.com/gablelbm/gable/internal/purchase_order"
	"github.com/gablelbm/gable/internal/testutil"
	"github.com/gablelbm/gable/internal/vendor"
	"github.com/gablelbm/gable/pkg/branchctx"
	"github.com/gablelbm/gable/pkg/database"
	"github.com/gablelbm/gable/pkg/middleware"
	"github.com/google/uuid"
)

// createBranchSeed is the shared ground of the create legs below: two
// branches, a vendor and a product below its reorder point (no stock
// anywhere, so the alert fires for every caller), cleaned up after the
// test. Each leg grants its own user so the arms cannot read each other's
// grants.
type createBranchSeed struct {
	db      *database.DB
	branchA uuid.UUID
	branchB uuid.UUID
	vendor  uuid.UUID
	product uuid.UUID
}

func seedCreateBranch(t *testing.T) *createBranchSeed {
	t.Helper()
	db := testutil.RequireDB(t)
	ctx := context.Background()
	s := &createBranchSeed{db: db, branchA: uuid.New(), branchB: uuid.New(), vendor: uuid.New(), product: uuid.New()}
	for _, id := range []uuid.UUID{s.branchA, s.branchB} {
		if _, err := db.Pool.Exec(ctx, `INSERT INTO locations (id, type, code) VALUES ($1, 'BRANCH', $2)`, id, "cb-"+id.String()[:8]); err != nil {
			t.Fatalf("seed branch: %v", err)
		}
	}
	if _, err := db.Pool.Exec(ctx, `INSERT INTO vendors (id, name) VALUES ($1, 'pin create branch vendor')`, s.vendor); err != nil {
		t.Fatalf("seed vendor: %v", err)
	}
	if _, err := db.Pool.Exec(ctx,
		`INSERT INTO products (id, sku, description, uom_primary, base_price, vendor_id, reorder_point, reorder_qty)
		 VALUES ($1, $2, 'pin create branch', 'EA', 1, $3, 5, 10)`, s.product, "CB-"+s.product.String()[:8], s.vendor); err != nil {
		t.Fatalf("seed product: %v", err)
	}
	t.Cleanup(func() {
		_, _ = db.Pool.Exec(ctx, `DELETE FROM purchase_order_lines WHERE po_id IN (SELECT id FROM purchase_orders WHERE vendor_id = $1)`, s.vendor)
		_, _ = db.Pool.Exec(ctx, `DELETE FROM purchase_orders WHERE vendor_id = $1`, s.vendor)
		_, _ = db.Pool.Exec(ctx, `DELETE FROM products WHERE id = $1`, s.product)
		_, _ = db.Pool.Exec(ctx, `DELETE FROM vendors WHERE id = $1`, s.vendor)
		_, _ = db.Pool.Exec(ctx, `DELETE FROM locations WHERE id IN ($1, $2)`, s.branchA, s.branchB)
	})
	return s
}

func (s *createBranchSeed) grant(t *testing.T, sub string, branches ...uuid.UUID) {
	t.Helper()
	for i, b := range branches {
		home := i == 0 // one home branch per user, the constraint's own shape
		if _, err := s.db.Pool.Exec(context.Background(),
			`INSERT INTO user_locations (user_sub, branch_id, is_home, granted_by) VALUES ($1, $2, $3, 'test')`, sub, b, home); err != nil {
			t.Fatalf("seed grant: %v", err)
		}
	}
	t.Cleanup(func() {
		_, _ = s.db.Pool.Exec(context.Background(), `DELETE FROM user_locations WHERE user_sub = $1`, sub)
	})
}

func (s *createBranchSeed) defaultBranch(t *testing.T) uuid.UUID {
	t.Helper()
	var def uuid.UUID
	if err := s.db.Pool.QueryRow(context.Background(),
		`SELECT value::uuid FROM system_settings WHERE key = 'default_branch_id'`).Scan(&def); err != nil {
		t.Fatalf("read default branch: %v", err)
	}
	return def
}

func (s *createBranchSeed) poBranch(t *testing.T) (uuid.UUID, bool) {
	t.Helper()
	var branch uuid.UUID
	err := s.db.Pool.QueryRow(context.Background(),
		`SELECT branch_id FROM purchase_orders WHERE vendor_id = $1`, s.vendor).Scan(&branch)
	if err != nil {
		if strings.Contains(err.Error(), "no rows") {
			return uuid.Nil, false
		}
		t.Fatalf("read po branch: %v", err)
	}
	return branch, true
}

func newCreateBranchService(s *createBranchSeed) *purchase_order.Service {
	return purchase_order.NewService(purchase_order.NewRepository(s.db), s.db, nil, nil,
		product.NewService(product.NewRepository(s.db)), vendor.NewService(vendor.NewRepository(s.db))).
		WithBranchGuard(middleware.NewBranchGuard(s.db))
}

// clearReorderDrafts removes the vendor's drafts so each arm starts empty
// even on a tree where an earlier arm wrote what it should not have.
func clearReorderDrafts(t *testing.T, s *createBranchSeed) {
	t.Helper()
	ctx := context.Background()
	_, _ = s.db.Pool.Exec(ctx, `DELETE FROM purchase_order_lines WHERE po_id IN (SELECT id FROM purchase_orders WHERE vendor_id = $1)`, s.vendor)
	_, _ = s.db.Pool.Exec(ctx, `DELETE FROM purchase_orders WHERE vendor_id = $1`, s.vendor)
}

// TestCreateReorders_TakesTheGrantsBranch is the reorder run of the create
// sweep (SEC-NOHEADER-1, item 2): the route a purchasing user reaches with
// no header used to write its reorder draft at the deployment default
// branch, a branch no grant of the creator holds, so the creator could not
// read the order back. The run now settles the branch the way every other
// create does: one granted branch writes there, several answer the choice
// refusal naming branch_id, none fail closed, and the cron engine's system
// caller keeps the default resolution it always had.
func TestCreateReorders_TakesTheGrantsBranch(t *testing.T) {
	s := seedCreateBranch(t)
	svc := newCreateBranchService(s)

	// Exactly one granted branch: the reorder draft lands there and nowhere
	// else.
	one := "cb-one-" + uuid.NewString()
	s.grant(t, one, s.branchA)
	count, err := svc.CreateReorders(branchctx.With(context.Background(), &branchctx.Context{UserSub: one}))
	if err != nil {
		t.Fatalf("one grant reorder run: %v", err)
	}
	if count < 1 {
		t.Fatalf("one grant reorder run created %d orders, want at least the seeded product's", count)
	}
	branch, ok := s.poBranch(t)
	if !ok || branch != s.branchA {
		t.Errorf("one grant reorder draft sits at branch %v (found %t), want the granted %s", branch, ok, s.branchA)
	}
	clearReorderDrafts(t, s)

	// No grants at all: the run refuses, nothing written.
	none := "cb-none-" + uuid.NewString()
	if _, err := svc.CreateReorders(branchctx.With(context.Background(), &branchctx.Context{UserSub: none})); err == nil || !strings.Contains(err.Error(), "branch_id") {
		t.Errorf("no grants reorder run: %v, want the fail closed refusal naming branch_id", err)
	}
	if _, ok := s.poBranch(t); ok {
		t.Errorf("no grants reorder run wrote a draft, want none")
	}
	clearReorderDrafts(t, s)

	// Several granted branches: no grant alone decides, the run answers the
	// choice refusal naming branch_id, nothing written.
	many := "cb-many-" + uuid.NewString()
	s.grant(t, many, s.branchA, s.branchB)
	if _, err := svc.CreateReorders(branchctx.With(context.Background(), &branchctx.Context{UserSub: many})); err == nil || !strings.Contains(err.Error(), "branch_id") {
		t.Errorf("two grants reorder run: %v, want the choice refusal naming branch_id", err)
	}
	if _, ok := s.poBranch(t); ok {
		t.Errorf("two grants reorder run wrote a draft, want none")
	}
	clearReorderDrafts(t, s)

	// The cron engine's system caller keeps the default resolution it always
	// had: the reorder timer is not a user act, and its drafts stay where the
	// run has always put them.
	if _, err := svc.CreateReorders(branchctx.WithSystem(context.Background())); err != nil {
		t.Fatalf("system reorder run: %v", err)
	}
	branch, ok = s.poBranch(t)
	if !ok || branch != s.defaultBranch(t) {
		t.Errorf("system reorder draft sits at branch %v (found %t), want the deployment default %s", branch, ok, s.defaultBranch(t))
	}
}

// TestCreateFromSOLine_TakesTheGrantsBranch is the special order draft of
// the create sweep (SEC-NOHEADER-1, item 2): accepting a quote with a
// special order line fires a draft purchase order for it, and a no header
// caller's draft used to land at the deployment default branch, a branch no
// grant of the creator holds. The draft now settles the branch the way
// every other create does: one granted branch writes there (the same branch
// the accepted quote's order took), several answer the choice refusal,
// none fail closed, and a context that carries a branch (the header caller,
// the A2A receiver's injected inbound branch) keeps its own resolution.
func TestCreateFromSOLine_TakesTheGrantsBranch(t *testing.T) {
	s := seedCreateBranch(t)
	ctx := context.Background()

	// The linked order line the draft's line points back at.
	cust := uuid.New()
	ord := uuid.New()
	soLine := uuid.New()
	if _, err := s.db.Pool.Exec(ctx, `INSERT INTO customers (id, name, account_number, primary_branch_id) VALUES ($1, 'pin create branch cust', $2, $3)`, cust, "CB-"+cust.String()[:8], s.branchA); err != nil {
		t.Fatalf("seed customer: %v", err)
	}
	if _, err := s.db.Pool.Exec(ctx,
		`INSERT INTO orders (id, customer_id, branch_id, status, total_amount, delivery_type, currency)
		 VALUES ($1, $2, $3, 'CONFIRMED', 10, 'PICKUP', 'USD')`, ord, cust, s.branchA); err != nil {
		t.Fatalf("seed order: %v", err)
	}
	if _, err := s.db.Pool.Exec(ctx,
		`INSERT INTO order_lines (id, order_id, product_id, quantity, uom, price_uom, uom_qty, price_uom_qty, unit_price, line_total, description, is_special_order)
		 VALUES ($1, $2, $3, 1, 'EA', 'EA', 1, 1, 1, 1, 'pin special', TRUE)`, soLine, ord, s.product); err != nil {
		t.Fatalf("seed order line: %v", err)
	}
	t.Cleanup(func() {
		_, _ = s.db.Pool.Exec(ctx, `DELETE FROM order_lines WHERE id = $1`, soLine)
		_, _ = s.db.Pool.Exec(ctx, `DELETE FROM orders WHERE id = $1`, ord)
		_, _ = s.db.Pool.Exec(ctx, `DELETE FROM customers WHERE id = $1`, cust)
	})

	svc := newCreateBranchService(s)
	clearDraft := func() { clearReorderDrafts(t, s) }

	// Exactly one granted branch: the special order draft lands there.
	one := "so-one-" + uuid.NewString()
	s.grant(t, one, s.branchA)
	if err := svc.CreateFromSOLine(branchctx.With(ctx, &branchctx.Context{UserSub: one}), soLine, s.branchA, &s.product, &s.vendor, "pin special", 1, 1); err != nil {
		t.Fatalf("one grant special order draft: %v", err)
	}
	branch, ok := s.poBranch(t)
	if !ok || branch != s.branchA {
		t.Errorf("one grant special order draft sits at branch %v (found %t), want the granted %s", branch, ok, s.branchA)
	}
	clearDraft()

	// A user holding two grants, with no header, accepts a quote whose
	// order sits at A: the draft exists at the order's own branch (r5 P3-6,
	// r6 P3-5), not the choice refusal the caller's grants used to answer.
	many := "so-many-" + uuid.NewString()
	s.grant(t, many, s.branchA, s.branchB)
	if err := svc.CreateFromSOLine(branchctx.With(ctx, &branchctx.Context{UserSub: many}), soLine, s.branchA, &s.product, &s.vendor, "pin special", 1, 1); err != nil {
		t.Fatalf("two grants special order draft: %v", err)
	}
	branch, ok = s.poBranch(t)
	if !ok || branch != s.branchA {
		t.Errorf("two grants special order draft sits at branch %v (found %t), want the order's branch %s", branch, ok, s.branchA)
	}
	clearDraft()

	// A context that carries a branch lands the draft at the order's branch
	// too (the header caller and the A2A receiver's injected inbound branch
	// both arrive here with the order's own branch).
	at := s.branchA
	if err := svc.CreateFromSOLine(branchctx.With(ctx, &branchctx.Context{BranchID: &at}), soLine, s.branchA, &s.product, &s.vendor, "pin special", 1, 1); err != nil {
		t.Fatalf("context branch special order draft: %v", err)
	}
	branch, ok = s.poBranch(t)
	if !ok || branch != s.branchA {
		t.Errorf("context branch special order draft sits at branch %v (found %t), want the order's branch %s", branch, ok, s.branchA)
	}
}
