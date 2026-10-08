// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package purchase_order_test

// The purchase_order.received event (ADR 0005 5.4) and the branch the stock
// landed in: the receive names a location per line, and a system caller may
// receive into another branch's location, so the event's branch is each
// received location's branch, not the purchase order's. The order module's
// subscriber queues back ordered orders of THAT branch: an event keyed on the
// purchase order's branch queues requests for a branch that got nothing, and
// the branch that got the stock waits for a manual allocate (PR 43 review
// round 1, P3-4).

import (
	"context"
	"testing"

	"github.com/gablelbm/gable/internal/purchase_order"
	"github.com/gablelbm/gable/internal/testutil"
	"github.com/gablelbm/gable/pkg/branchctx"
	"github.com/gablelbm/gable/pkg/outbox"
	"github.com/google/uuid"
)

// RULE: a receipt into another branch's location writes purchase_order.received
// carrying that branch, in the envelope and in the payload the subscriber
// reads, with the products that landed there.
func TestReceivePO_ReceivedEventCarriesTheLocationBranch(t *testing.T) {
	testutil.LockOutboxTables(t)
	db := testutil.RequireDB(t)
	ctx := branchctx.WithSystem(context.Background())

	branch := `(SELECT value::uuid FROM system_settings WHERE key = 'default_branch_id')`
	other, yard := uuid.New(), uuid.New()
	vendor, product, po, line := uuid.New(), uuid.New(), uuid.New(), uuid.New()
	must := func(sql string, args ...any) {
		t.Helper()
		if _, err := db.Pool.Exec(ctx, sql, args...); err != nil {
			t.Fatalf("%s: %v", sql, err)
		}
	}
	must(`INSERT INTO locations (id, type, code, parent_id, branch_id) VALUES ($1, 'BRANCH', $2, NULL, $1)`, other, "po-ev-"+other.String()[:6])
	must(`INSERT INTO locations (id, type, code, parent_id, branch_id) VALUES ($1, 'YARD', $2, $3, $3)`, yard, "po-ev-"+yard.String()[:6], other)
	must(`INSERT INTO vendors (id, name) VALUES ($1, $2)`, vendor, "po-ev-"+vendor.String()[:6])
	must(`INSERT INTO products (id, sku, description, uom_primary, base_price) VALUES ($1, $2, 'sheet', 'SF', 12.25)`, product, "POEV-"+uuid.NewString()[:8])
	// The purchase order hangs on the default branch; its stock is received
	// into the OTHER branch's yard.
	must(`INSERT INTO purchase_orders (id, vendor_id, status, source, branch_id) VALUES ($1, $2, 'SENT', 'MANUAL', `+branch+`)`, po, vendor)
	must(`INSERT INTO purchase_order_lines (id, po_id, product_id, description, quantity, cost) VALUES ($1, $2, $3, 'sheet', 10, 9.00)`, line, po, product)
	t.Cleanup(func() {
		_, _ = db.Pool.Exec(ctx, `DELETE FROM events_outbox WHERE entity_type = 'purchase_order' AND entity_id = $1`, po)
		_, _ = db.Pool.Exec(ctx, `DELETE FROM purchase_order_lines WHERE po_id = $1`, po)
		_, _ = db.Pool.Exec(ctx, `DELETE FROM purchase_orders WHERE id = $1`, po)
		_, _ = db.Pool.Exec(ctx, `DELETE FROM inventory WHERE location_id = $1`, yard)
		_, _ = db.Pool.Exec(ctx, `DELETE FROM products WHERE id = $1`, product)
		_, _ = db.Pool.Exec(ctx, `DELETE FROM vendors WHERE id = $1`, vendor)
		_, _ = db.Pool.Exec(ctx, `DELETE FROM locations WHERE id IN ($1, $2)`, yard, other)
	})

	svc := purchase_order.NewService(purchase_order.NewRepository(db), db, nil, nil, nil, nil).
		WithOutbox(outbox.NewWriter(db, ""))
	if err := svc.ReceivePO(ctx, po, []purchase_order.ReceiveLineInput{
		{LineID: line.String(), LocationID: yard.String(), QtyReceived: 10},
	}); err != nil {
		t.Fatalf("receive: %v", err)
	}

	var envelopeBranch, payloadBranch, products string
	if err := db.Pool.QueryRow(ctx, `
		SELECT COALESCE(branch_id::text, ''), data->>'branch_id', data->>'product_ids'
		FROM events_outbox WHERE entity_type = 'purchase_order' AND entity_id = $1`, po).
		Scan(&envelopeBranch, &payloadBranch, &products); err != nil {
		t.Fatalf("no purchase_order.received event: %v", err)
	}
	if envelopeBranch != other.String() || payloadBranch != other.String() {
		t.Errorf("received event branch = envelope %s payload %s, want the RECEIPT location's branch %s (the stock landed there, not on the purchase order's branch)", envelopeBranch, payloadBranch, other)
	}
	if products == "" || products == "[]" {
		t.Errorf("received event product_ids = %s, want the received product", products)
	}
}
