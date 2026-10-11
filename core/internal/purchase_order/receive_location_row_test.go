// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package purchase_order_test

// The receipt's row (the r14 review's P1-2): a branch scoped caller that
// receives a line into a named yard writes that yard's row, never the
// branch's largest row. The receive goes through the same inventory read the
// stock routes use, so this pins the only other caller of the adjust.

import (
	"context"
	"testing"

	"github.com/gablelbm/gable/internal/inventory"
	"github.com/gablelbm/gable/internal/purchase_order"
	"github.com/gablelbm/gable/internal/testutil"
	"github.com/gablelbm/gable/pkg/branchctx"
	"github.com/gablelbm/gable/pkg/outbox"
	"github.com/google/uuid"
)

// RULE: a receipt of 3 at the branch's small yard lands there; the large yard
// stands untouched.
func TestAReceiptLandsInTheNamedYardNotTheBranchsLargestRow(t *testing.T) {
	testutil.LockOutboxTables(t)
	db := testutil.RequireDB(t)
	ctx := context.Background()

	branch := `(SELECT value::uuid FROM system_settings WHERE key = 'default_branch_id')`
	var branchID uuid.UUID
	if err := db.Pool.QueryRow(ctx, `SELECT `+branch).Scan(&branchID); err != nil {
		t.Fatal(err)
	}
	smallYard, bigYard := uuid.New(), uuid.New()
	vendor, product, po, line := uuid.New(), uuid.New(), uuid.New(), uuid.New()
	tag := "POR-" + uuid.NewString()[:8]
	must := func(sql string, args ...any) {
		t.Helper()
		if _, err := db.Pool.Exec(ctx, sql, args...); err != nil {
			t.Fatalf("%s: %v", sql, err)
		}
	}
	must(`INSERT INTO locations (id, type, code, parent_id, branch_id)
		VALUES ($1, 'YARD', $2, $4, $4), ($3, 'YARD', $5, $4, $4)`, smallYard, tag+"-S", bigYard, branchID, tag+"-B")
	must(`INSERT INTO vendors (id, name) VALUES ($1, $2)`, vendor, tag)
	must(`INSERT INTO products (id, sku, description, uom_primary, base_price) VALUES ($1, $2, 'sheet', 'SF', 12.25)`, product, tag+"-P")
	// the small yard holds 5, the big yard 100: a read that picks the branch's
	// largest row would land the receipt in the big yard
	must(`INSERT INTO inventory (id, product_id, location_id, location, quantity) VALUES ($1, $2, $3, 'S', 5), ($4, $2, $5, 'B', 100)`,
		uuid.New(), product, smallYard, uuid.New(), bigYard)
	must(`INSERT INTO purchase_orders (id, vendor_id, status, source, branch_id) VALUES ($1, $2, 'SENT', 'MANUAL', `+branch+`)`, po, vendor)
	must(`INSERT INTO purchase_order_lines (id, po_id, product_id, description, quantity, cost) VALUES ($1, $2, $3, 'sheet', 10, 9.00)`, line, po, product)
	t.Cleanup(func() {
		_, _ = db.Pool.Exec(ctx, `DELETE FROM events_outbox WHERE entity_type = 'purchase_order' AND entity_id = $1`, po)
		_, _ = db.Pool.Exec(ctx, `DELETE FROM purchase_order_lines WHERE po_id = $1`, po)
		_, _ = db.Pool.Exec(ctx, `DELETE FROM purchase_orders WHERE id = $1`, po)
		_, _ = db.Pool.Exec(ctx, `DELETE FROM inventory WHERE product_id = $1`, product)
		_, _ = db.Pool.Exec(ctx, `DELETE FROM products WHERE id = $1`, product)
		_, _ = db.Pool.Exec(ctx, `DELETE FROM vendors WHERE id = $1`, vendor)
		_, _ = db.Pool.Exec(ctx, `DELETE FROM locations WHERE id IN ($1, $2)`, smallYard, bigYard)
	})

	qty := func(yard uuid.UUID) string {
		var out string
		if err := db.Pool.QueryRow(ctx, `SELECT quantity::text FROM inventory WHERE product_id = $1 AND location_id = $2`, product, yard).Scan(&out); err != nil {
			t.Fatal(err)
		}
		return out
	}
	// a caller scoped to the PO's branch receives 3 into the small yard
	scoped := branchctx.With(ctx, &branchctx.Context{BranchID: &branchID})
	svc := purchase_order.NewService(purchase_order.NewRepository(db), db, nil,
		inventory.NewService(inventory.NewRepository(db)), nil, nil).
		WithOutbox(outbox.NewWriter(db, ""))
	if err := svc.ReceivePO(scoped, po, []purchase_order.ReceiveLineInput{
		{LineID: line.String(), LocationID: smallYard.String(), QtyReceived: 3},
	}); err != nil {
		t.Fatalf("receive: %v", err)
	}
	if got := qty(smallYard); got != "8.0000" {
		t.Errorf("the named small yard = %s, want 8.0000 (the receipt landed somewhere else)", got)
	}
	if got := qty(bigYard); got != "100.0000" {
		t.Errorf("the big yard = %s, want 100.0000 (the receipt touched the branch's largest row)", got)
	}
}
