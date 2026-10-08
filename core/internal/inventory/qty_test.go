// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package inventory_test

// The scale 4 quantity functions (ADR 0005 5.4) against a real database:
// allocate up to what is available, release, fulfil from allocation and
// restock, all inside one transaction that a failing step rolls back.

import (
	"context"
	"errors"
	"testing"

	"github.com/gablelbm/gable/internal/inventory"
	"github.com/gablelbm/gable/internal/platform/httpx"
	"github.com/gablelbm/gable/internal/testutil"
	"github.com/google/uuid"
)

type qtyWorld struct {
	svc       *inventory.Service
	productID uuid.UUID
	branchID  uuid.UUID
	rowA      uuid.UUID // the larger row
	rowB      uuid.UUID
}

func newQtyWorld(t *testing.T) (*qtyWorld, func(sql string, args ...any) string) {
	t.Helper()
	db := testutil.RequireDB(t)
	ctx := context.Background()
	w := &qtyWorld{svc: inventory.NewService(inventory.NewRepository(db)), productID: uuid.New(), branchID: uuid.New()}
	yardA, yardB := uuid.New(), uuid.New()
	w.rowA, w.rowB = uuid.New(), uuid.New()
	must := func(sql string, args ...any) {
		if _, err := db.Pool.Exec(ctx, sql, args...); err != nil {
			t.Fatalf("%s: %v", sql, err)
		}
	}
	must(`INSERT INTO locations (id, type, code) VALUES ($1, 'BRANCH', $2)`, w.branchID, "Q-"+w.branchID.String()[:8])
	must(`INSERT INTO locations (id, type, code, parent_id, branch_id) VALUES ($1, 'YARD', $2, $3, $3), ($4, 'YARD', $5, $3, $3)`,
		yardA, "QA-"+yardA.String()[:8], w.branchID, yardB, "QB-"+yardB.String()[:8])
	must(`INSERT INTO products (id, sku, description, uom_primary, base_price) VALUES ($1, $2, 'qty stud', 'PCS', 1)`, w.productID, "QTY-"+w.productID.String()[:8])
	must(`INSERT INTO inventory (id, product_id, location_id, location, quantity, allocated) VALUES ($1, $3, $4, 'A', 10, 2), ($2, $3, $5, 'B', 6, 0)`,
		w.rowA, w.rowB, w.productID, yardA, yardB)
	t.Cleanup(func() {
		_, _ = db.Pool.Exec(ctx, `DELETE FROM inventory WHERE product_id = $1`, w.productID)
		_, _ = db.Pool.Exec(ctx, `DELETE FROM products WHERE id = $1`, w.productID)
		_, _ = db.Pool.Exec(ctx, `DELETE FROM locations WHERE branch_id = $1 OR id = $1`, w.branchID)
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

func q(t *testing.T, s string) httpx.Quantity {
	t.Helper()
	v, err := httpx.ParseQuantity(s)
	if err != nil {
		t.Fatal(err)
	}
	return v
}

func TestQty_AllocateUpToWhatIsAvailable(t *testing.T) {
	w, read := newQtyWorld(t)
	ctx := context.Background()
	state := func() string {
		return read(`SELECT string_agg(quantity::text || '/' || allocated::text, ' ' ORDER BY location) FROM inventory WHERE product_id = $1`, w.productID)
	}
	if got := state(); got != "10.0000/2.0000 6.0000/0.0000" {
		t.Fatalf("fixture = %s", got)
	}
	avail, err := w.svc.AvailableQty(ctx, w.productID, w.branchID)
	if err != nil || avail != q(t, "14") {
		t.Fatalf("available = %v (%v), want 14", avail, err)
	}
	// A request inside what is available is allocated whole, fullest row first.
	got, err := w.svc.AllocateQty(ctx, w.productID, w.branchID, q(t, "7.5"))
	if err != nil || got != q(t, "7.5") {
		t.Fatalf("AllocateQty(7.5) = %v (%v), want 7.5", got, err)
	}
	// A request past what is left allocates what is there and no error: the
	// rest is the caller's back order.
	got, err = w.svc.AllocateQty(ctx, w.productID, w.branchID, q(t, "20"))
	if err != nil || got != q(t, "6.5") {
		t.Fatalf("AllocateQty(20) = %v (%v), want the 6.5 left", got, err)
	}
	if total := read(`SELECT sum(allocated)::text FROM inventory WHERE product_id = $1`, w.productID); total != "16.0000" {
		t.Errorf("total allocated = %s, want every unit (16.0000)", total)
	}
	if got, err := w.svc.AllocateQty(ctx, w.productID, w.branchID, q(t, "1")); err != nil || got != 0 {
		t.Errorf("AllocateQty with nothing available = %v (%v), want 0 and no error", got, err)
	}
}

func TestQty_ReleaseFulfillRestock(t *testing.T) {
	w, read := newQtyWorld(t)
	ctx := context.Background()
	if _, err := w.svc.AllocateQty(ctx, w.productID, w.branchID, q(t, "8")); err != nil {
		t.Fatal(err)
	}
	// allocated is now 10 across the rows
	if err := w.svc.ReleaseQty(ctx, w.productID, w.branchID, q(t, "3")); err != nil {
		t.Fatalf("release: %v", err)
	}
	if total := read(`SELECT sum(allocated)::text FROM inventory WHERE product_id = $1`, w.productID); total != "7.0000" {
		t.Errorf("allocated after release = %s, want 7.0000", total)
	}
	if err := w.svc.ReleaseQty(ctx, w.productID, w.branchID, q(t, "9")); !errors.Is(err, inventory.ErrInsufficientAllocated) {
		t.Errorf("release past the allocation = %v, want ErrInsufficientAllocated", err)
	}
	if total := read(`SELECT sum(allocated)::text FROM inventory WHERE product_id = $1`, w.productID); total != "7.0000" {
		t.Errorf("a refused release moved the allocation: %s", total)
	}

	if err := w.svc.FulfillQty(ctx, w.productID, w.branchID, q(t, "5")); err != nil {
		t.Fatalf("fulfil: %v", err)
	}
	if got := read(`SELECT sum(quantity)::text || '/' || sum(allocated)::text FROM inventory WHERE product_id = $1`, w.productID); got != "11.0000/2.0000" {
		t.Errorf("on hand/allocated after fulfilling 5 = %s, want 11.0000/2.0000", got)
	}
	if err := w.svc.FulfillQty(ctx, w.productID, w.branchID, q(t, "2.0001")); !errors.Is(err, inventory.ErrInsufficientAllocated) {
		t.Errorf("fulfil past the allocation = %v, want ErrInsufficientAllocated", err)
	}
	if err := w.svc.RestockQty(ctx, w.productID, w.branchID, q(t, "4")); err != nil {
		t.Fatalf("restock: %v", err)
	}
	if got := read(`SELECT sum(quantity)::text || '/' || sum(allocated)::text FROM inventory WHERE product_id = $1`, w.productID); got != "15.0000/2.0000" {
		t.Errorf("on hand/allocated after restocking 4 = %s, want 15.0000/2.0000", got)
	}
	// Another branch's stock is not this branch's: nothing to restock there.
	if err := w.svc.RestockQty(ctx, w.productID, uuid.New(), q(t, "1")); err == nil {
		t.Error("restock at a branch with no row succeeded")
	}
}
