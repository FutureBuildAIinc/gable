// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package inventory_test

// The write path's row against a real database (the fourteenth review's P1-2): a
// branch scoped caller that names a location acts on exactly that location's
// row, a location outside the caller's branch is refused, and only a request
// that names no location lets the branch rule pick a row. The stock routes
// (the cycle count, the receipt, the transfer) and the purchase order receive
// all write through this one read, so the suites here pin the row each act
// touches.

import (
	"context"
	"errors"
	"testing"

	"github.com/gablelbm/gable/internal/inventory"
	"github.com/gablelbm/gable/internal/testutil"
	"github.com/gablelbm/gable/pkg/branchctx"
	"github.com/google/uuid"
)

// locWorld is one branch with two yards (yard1 holds the larger row) and a
// second branch with one yard, plus an unassigned row.
type locWorld struct {
	svc                    *inventory.Service
	productID, branchID    uuid.UUID
	yard1, yard2           uuid.UUID
	otherBranch, otherYard uuid.UUID
	unassigned             uuid.UUID
	ctx                    context.Context
}

func newLocWorld(t *testing.T) (*locWorld, func(sql string, args ...any) string) {
	t.Helper()
	db := testutil.RequireDB(t)
	ctx := context.Background()
	w := &locWorld{svc: inventory.NewService(inventory.NewRepository(db)),
		productID: uuid.New(), branchID: uuid.New(),
		yard1: uuid.New(), yard2: uuid.New(), otherBranch: uuid.New(), otherYard: uuid.New()}
	w.ctx = branchctx.With(ctx, &branchctx.Context{BranchID: &w.branchID})
	tag := "IL-" + uuid.NewString()[:8]
	must := func(sql string, args ...any) {
		if _, err := db.Pool.Exec(ctx, sql, args...); err != nil {
			t.Fatalf("%s: %v", sql, err)
		}
	}
	must(`INSERT INTO locations (id, type, code) VALUES ($1, 'BRANCH', $2)`, w.branchID, tag)
	must(`INSERT INTO locations (id, type, code, parent_id, branch_id)
		VALUES ($1, 'YARD', $2, $4, $4), ($3, 'YARD', $5, $4, $4)`, w.yard1, tag+"-1", w.yard2, w.branchID, tag+"-2")
	must(`INSERT INTO locations (id, type, code) VALUES ($1, 'BRANCH', $2)`, w.otherBranch, tag+"-O")
	must(`INSERT INTO locations (id, type, code, parent_id, branch_id) VALUES ($1, 'YARD', $2, $3, $3)`,
		w.otherYard, tag+"-OY", w.otherBranch)
	must(`INSERT INTO products (id, sku, description, uom_primary, base_price) VALUES ($1, $2, 'loc stud', 'PCS', 1)`,
		w.productID, tag+"-P")
	must(`INSERT INTO inventory (id, product_id, location_id, location, quantity, created_at)
		VALUES ($1, $2, $3, 'Y1', 100, NOW() - INTERVAL '2 hours'),
		       ($4, $2, $5, 'Y2', 5, NOW() - INTERVAL '1 hour'),
		       ($6, $2, $7, 'OY', 50, NOW())`,
		uuid.New(), w.productID, w.yard1, uuid.New(), w.yard2, uuid.New(), w.otherYard)
	t.Cleanup(func() {
		_, _ = db.Pool.Exec(ctx, `DELETE FROM inventory WHERE product_id = $1`, w.productID)
		_, _ = db.Pool.Exec(ctx, `DELETE FROM products WHERE id = $1`, w.productID)
		_, _ = db.Pool.Exec(ctx, `DELETE FROM locations WHERE branch_id IN ($1, $2) OR id IN ($1, $2)`, w.branchID, w.otherBranch)
	})
	read := func(sql string, args ...any) string {
		t.Helper()
		var out string
		if err := db.Pool.QueryRow(ctx, sql, args...).Scan(&out); err != nil {
			t.Fatal(err)
		}
		return out
	}
	return w, read
}

// qtyOf reads one yard's quantity, so each assertion names the row it means.
func (w *locWorld) qtyOf(t *testing.T, read func(sql string, args ...any) string, yard uuid.UUID) string {
	t.Helper()
	return read(`SELECT quantity::text FROM inventory WHERE product_id = $1 AND location_id = $2`, w.productID, yard)
}

// RULE: a cycle count or a receipt that names a yard acts on that yard's row
// only, whatever else the branch holds.
func TestAnAdjustWithANamedLocationActsOnThatLocationRowAlone(t *testing.T) {
	w, read := newLocWorld(t)
	// a cycle count of yard 2, an absolute set to 7
	if err := w.svc.AdjustStock(w.ctx, inventory.StockAdjustmentRequest{
		ProductID: w.productID, LocationID: &w.yard2, Quantity: 7}); err != nil {
		t.Fatalf("cycle count of yard 2: %v", err)
	}
	if got := w.qtyOf(t, read, w.yard1); got != "100.0000" {
		t.Errorf("yard 1 after a cycle count of yard 2 = %s, want 100.0000 (the count touched another yard's row)", got)
	}
	if got := w.qtyOf(t, read, w.yard2); got != "7.0000" {
		t.Errorf("yard 2 after its own cycle count = %s, want 7.0000", got)
	}
	// a receipt of 3 more at yard 2
	if err := w.svc.AdjustStock(w.ctx, inventory.StockAdjustmentRequest{
		ProductID: w.productID, LocationID: &w.yard2, Quantity: 3, IsDelta: true}); err != nil {
		t.Fatalf("receipt of 3 at yard 2: %v", err)
	}
	if got := w.qtyOf(t, read, w.yard1); got != "100.0000" {
		t.Errorf("yard 1 after a receipt at yard 2 = %s, want 100.0000", got)
	}
	if got := w.qtyOf(t, read, w.yard2); got != "10.0000" {
		t.Errorf("yard 2 after its receipt = %s, want 10.0000", got)
	}
}

// RULE: a transfer moves the named rows only.
func TestATransferMovesTheNamedRowsAlone(t *testing.T) {
	w, read := newLocWorld(t)
	if err := w.svc.MoveStock(w.ctx, inventory.StockMovementRequest{
		ProductID: w.productID, FromLocationID: &w.yard1, ToLocationID: w.yard2, Quantity: 10}); err != nil {
		t.Fatalf("transfer 10 from yard 1 to yard 2: %v", err)
	}
	if got := w.qtyOf(t, read, w.yard1); got != "90.0000" {
		t.Errorf("yard 1 after the transfer = %s, want 90.0000", got)
	}
	if got := w.qtyOf(t, read, w.yard2); got != "15.0000" {
		t.Errorf("yard 2 after the transfer = %s, want 15.0000", got)
	}
}

// RULE: a location outside the caller's branch is refused, never substituted:
// both the row on the other branch and the caller's own rows stand untouched.
func TestAnActOnALocationOutsideTheCallersBranchIsRefused(t *testing.T) {
	w, read := newLocWorld(t)
	err := w.svc.AdjustStock(w.ctx, inventory.StockAdjustmentRequest{
		ProductID: w.productID, LocationID: &w.otherYard, Quantity: 7})
	if !errors.Is(err, inventory.ErrLocationOutsideBranch) {
		t.Fatalf("an adjust on another branch's yard = %v, want ErrLocationOutsideBranch", err)
	}
	err = w.svc.MoveStock(w.ctx, inventory.StockMovementRequest{
		ProductID: w.productID, FromLocationID: &w.yard1, ToLocationID: w.otherYard, Quantity: 10})
	if err == nil {
		t.Fatal("a transfer into another branch's yard succeeded")
	}
	if got := w.qtyOf(t, read, w.otherYard); got != "50.0000" {
		t.Errorf("the other branch's yard moved to %s, want 50.0000 (refused, not substituted)", got)
	}
	if got := w.qtyOf(t, read, w.yard1); got != "100.0000" {
		t.Errorf("yard 1 after the refused acts = %s, want 100.0000 (a refusal must not touch the caller's own rows)", got)
	}
	if got := w.qtyOf(t, read, w.yard2); got != "5.0000" {
		t.Errorf("yard 2 after the refused acts = %s, want 5.0000", got)
	}
}

// RULE: a receipt at a yard of the caller's branch that holds no row yet
// creates the row there (the route's first receipt at a yard).
func TestAReceiptAtAYardWithNoRowCreatesItThere(t *testing.T) {
	w, read := newLocWorld(t)
	fresh := uuid.New()
	db := testutil.RequireDB(t)
	if _, err := db.Pool.Exec(context.Background(),
		`INSERT INTO locations (id, type, code, parent_id, branch_id) VALUES ($1, 'YARD', $2, $3, $3)`,
		fresh, "IL-FRESH-"+fresh.String()[:6], w.branchID); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = db.Pool.Exec(context.Background(), `DELETE FROM inventory WHERE location_id = $1`, fresh)
		_, _ = db.Pool.Exec(context.Background(), `DELETE FROM locations WHERE id = $1`, fresh)
	})
	if err := w.svc.AdjustStock(w.ctx, inventory.StockAdjustmentRequest{
		ProductID: w.productID, LocationID: &fresh, Quantity: 4, IsDelta: true}); err != nil {
		t.Fatalf("first receipt at a fresh yard: %v", err)
	}
	if got := w.qtyOf(t, read, fresh); got != "4.0000" {
		t.Errorf("the fresh yard = %s, want 4.0000", got)
	}
	if got := w.qtyOf(t, read, w.yard1); got != "100.0000" {
		t.Errorf("yard 1 after a receipt at a fresh yard = %s, want 100.0000", got)
	}
}

// RULE: a named location that holds no inventory row yet is still held to
// the branch wall (the fifteenth review's P3-1): the row's absence must not
// let a service caller seed a first row on another branch's yard, so the
// read falls back to the location's own branch and refuses, and nothing is
// created.
func TestAnActOnAnotherBranchsYardWithNoRowIsRefused(t *testing.T) {
	w, read := newLocWorld(t)
	db := testutil.RequireDB(t)
	freshOther := uuid.New()
	if _, err := db.Pool.Exec(context.Background(),
		`INSERT INTO locations (id, type, code, parent_id, branch_id) VALUES ($1, 'YARD', $2, $3, $3)`,
		freshOther, "IL-FO-"+freshOther.String()[:6], w.otherBranch); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = db.Pool.Exec(context.Background(), `DELETE FROM inventory WHERE location_id = $1`, freshOther)
		_, _ = db.Pool.Exec(context.Background(), `DELETE FROM locations WHERE id = $1`, freshOther)
	})
	err := w.svc.AdjustStock(w.ctx, inventory.StockAdjustmentRequest{
		ProductID: w.productID, LocationID: &freshOther, Quantity: 7})
	if !errors.Is(err, inventory.ErrLocationOutsideBranch) {
		t.Fatalf("an adjust on another branch's rowless yard = %v, want ErrLocationOutsideBranch (the handler's location guard covers the routes; the service caller needs the read's own hold)", err)
	}
	var n int
	if err := db.Pool.QueryRow(context.Background(),
		`SELECT count(*) FROM inventory WHERE product_id = $1 AND location_id = $2`, w.productID, freshOther).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Errorf("%d rows were created on the other branch's rowless yard, want 0 (a refusal must not seed another branch)", n)
	}
	if got := w.qtyOf(t, read, w.yard1); got != "100.0000" {
		t.Errorf("yard 1 after the refused adjust = %s, want 100.0000 (the caller's own rows stand untouched)", got)
	}
}

// RULE: a request that names no location keeps the stated picks for a delta
// (an unbound caller the legacy unassigned row, a branch scoped caller the
// branch's first row by creation, never the largest quantity by accident); an
// absolute count that names no location is refused where the product sits in
// more than one location in reach (item 130, N20: a count is for one
// location, and the oldest row would have taken the whole yard's figure).
func TestAnAdjustWithNoLocationTakesTheStatedRow(t *testing.T) {
	w, read := newLocWorld(t)
	db := testutil.RequireDB(t)
	// the unassigned row, older than every yard row
	if _, err := db.Pool.Exec(context.Background(),
		`INSERT INTO inventory (id, product_id, location_id, location, quantity, created_at)
		 VALUES ($1, $2, NULL, 'LEG', 40, NOW() - INTERVAL '3 hours')`, uuid.New(), w.productID); err != nil {
		t.Fatal(err)
	}
	// a newest yard holding the branch's largest row: the branch's first row
	// by creation is yard 1, so a pick by largest quantity lands elsewhere
	newYard := uuid.New()
	if _, err := db.Pool.Exec(context.Background(),
		`INSERT INTO locations (id, type, code, parent_id, branch_id) VALUES ($1, 'YARD', $2, $3, $3)`,
		newYard, "IL-NEW-"+newYard.String()[:6], w.branchID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Pool.Exec(context.Background(),
		`INSERT INTO inventory (id, product_id, location_id, location, quantity, created_at)
		 VALUES ($1, $2, $3, 'NEW', 900, NOW())`, uuid.New(), w.productID, newYard); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = db.Pool.Exec(context.Background(), `DELETE FROM inventory WHERE product_id = $1 AND (location_id IS NULL OR location_id = $2)`, w.productID, newYard)
		_, _ = db.Pool.Exec(context.Background(), `DELETE FROM locations WHERE id = $1`, newYard)
	})
	// an absolute count with no location is refused for both callers and
	// writes nothing
	for name, ctx := range map[string]context.Context{"unbound": context.Background(), "branch scoped": w.ctx} {
		err := w.svc.AdjustStock(ctx, inventory.StockAdjustmentRequest{ProductID: w.productID, Quantity: 41})
		if !errors.Is(err, inventory.ErrCountNeedsLocation) {
			t.Fatalf("%s absolute adjust with no location = %v, want ErrCountNeedsLocation", name, err)
		}
	}
	if got := w.qtyOf(t, read, w.yard1); got != "100.0000" {
		t.Errorf("yard 1 after the refused counts = %s, want 100.0000", got)
	}
	// a delta with no location from an unbound caller is refused too: it
	// would write a row with no location (round 3); the legacy row stands
	if err := w.svc.AdjustStock(context.Background(), inventory.StockAdjustmentRequest{
		ProductID: w.productID, Quantity: 1, IsDelta: true}); !errors.Is(err, inventory.ErrCountNeedsLocation) {
		t.Fatalf("unbound delta with no location = %v, want ErrCountNeedsLocation", err)
	}
	var legacy string
	if err := db.Pool.QueryRow(context.Background(),
		`SELECT quantity::text FROM inventory WHERE product_id = $1 AND location_id IS NULL`, w.productID).Scan(&legacy); err != nil {
		t.Fatal(err)
	}
	if legacy != "40.0000" {
		t.Errorf("the unassigned row = %s, want 40.0000 untouched", legacy)
	}
	if got := w.qtyOf(t, read, w.yard1); got != "100.0000" {
		t.Errorf("yard 1 after the unbound delta = %s, want 100.0000", got)
	}
	// a delta with no location keeps the stated picks for a branch scoped caller:
	// the branch's first row by creation (yard 1,
	// created before yard 2 and the new yard), though the new yard holds the
	// most: the pick follows creation order, never quantity
	if err := w.svc.AdjustStock(w.ctx, inventory.StockAdjustmentRequest{
		ProductID: w.productID, Quantity: 2, IsDelta: true}); err != nil {
		t.Fatalf("branch scoped delta with no location: %v", err)
	}
	if got := w.qtyOf(t, read, w.yard1); got != "102.0000" {
		t.Errorf("yard 1 (the branch's first row) = %s, want 102.0000", got)
	}
	if got := w.qtyOf(t, read, w.yard2); got != "5.0000" {
		t.Errorf("yard 2 after the branch scoped delta = %s, want 5.0000", got)
	}
	if got := w.qtyOf(t, read, newYard); got != "900.0000" {
		t.Errorf("the newest yard after the branch scoped delta = %s, want 900.0000 (a silent largest quantity pick would have taken it)", got)
	}
}
