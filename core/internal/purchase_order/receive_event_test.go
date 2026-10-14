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
	"strings"
	"testing"

	"github.com/gablelbm/gable/internal/inventory"
	"github.com/gablelbm/gable/internal/purchase_order"
	"github.com/gablelbm/gable/internal/testutil"
	"github.com/gablelbm/gable/pkg/branchctx"
	"github.com/gablelbm/gable/pkg/outbox"
	"github.com/google/uuid"
)

// RULES: a receipt into another branch's location is refused with the
// cross_branch blocker (ADR 0008 section 11's same branch rule, C4-1a's
// fix: the base commit let an administrator receive branch A's purchase
// order into branch B), and a receipt into the purchase order's own branch
// writes purchase_order.received carrying the location's branch, with the
// products that landed there.
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
	must(`INSERT INTO purchase_order_lines (id, po_id, product_id, description, quantity, unit_cost, line_total) VALUES ($1, $2, $3, 'sheet', 10, 9.00, 90.00)`, line, po, product)
	t.Cleanup(func() {
		_, _ = db.Pool.Exec(ctx, `DELETE FROM events_outbox WHERE entity_type = 'purchase_order' AND entity_id = $1`, po)
		_, _ = db.Pool.Exec(ctx, `DELETE FROM purchase_order_lines WHERE po_id = $1`, po)
		_, _ = db.Pool.Exec(ctx, `DELETE FROM purchase_orders WHERE id = $1`, po)
		_, _ = db.Pool.Exec(ctx, `DELETE FROM inventory WHERE location_id IN ($1)`, yard)
		_, _ = db.Pool.Exec(ctx, `DELETE FROM products WHERE id = $1`, product)
		_, _ = db.Pool.Exec(ctx, `DELETE FROM vendors WHERE id = $1`, vendor)
		_, _ = db.Pool.Exec(ctx, `DELETE FROM locations WHERE id IN ($1, $2)`, yard, other)
	})

	invSvc := inventory.NewService(inventory.NewRepository(db))
	svc := purchase_order.NewService(purchase_order.NewRepository(db), db, nil, invSvc, nil, nil).
		WithOutbox(outbox.NewWriter(db, ""))
	rev := int64(1)
	otherYardDraft := func() []purchase_order.ReceiveLineDraft {
		return []purchase_order.ReceiveLineDraft{
			{LineID: line, QtyReceived: 100000, LocationID: yard},
		}
	}
	// The cross branch receipt is refused, with the blocker naming the rule.
	_, err := svc.ReceivePO(ctx, po, "", &rev, otherYardDraft())
	if err == nil {
		t.Fatal("a receipt into another branch's yard was accepted, want the cross_branch refusal")
	}
	if !strings.Contains(err.Error(), "another branch") {
		t.Fatalf("the refusal does not name the cross branch rule: %v", err)
	}

	// A yard of the purchase order's own branch: the event carries that
	// branch (which the location shares with the purchase order) and the
	// products that landed there.
	ownYard := uuid.New()
	t.Cleanup(func() {
		_, _ = db.Pool.Exec(ctx, `DELETE FROM locations WHERE id = $1`, ownYard)
	})
	must(`INSERT INTO locations (id, type, code, parent_id, branch_id) VALUES ($1, 'YARD', $2, `+branch+`, `+branch+`)`, ownYard, "po-ev-own-"+ownYard.String()[:6])
	rev = 1 // the refused act moved nothing, so the revision still stands
	ownDraft := []purchase_order.ReceiveLineDraft{
		{LineID: line, QtyReceived: 100000, LocationID: ownYard},
	}
	if _, err := svc.ReceivePO(ctx, po, "", &rev, ownDraft); err != nil {
		t.Fatalf("receive: %v", err)
	}

	// The event's branch is the receipt location's branch, which the same
	// branch rule now holds equal to the purchase order's own.
	var defaultBranch string
	if err := db.Pool.QueryRow(ctx, `SELECT value FROM system_settings WHERE key = 'default_branch_id'`).Scan(&defaultBranch); err != nil {
		t.Fatal(err)
	}
	var envelopeBranch, payloadBranch, products string
	if err := db.Pool.QueryRow(ctx, `
		SELECT COALESCE(branch_id::text, ''), data->>'branch_id', data->>'product_ids'
		FROM events_outbox WHERE entity_type = 'purchase_order' AND entity_id = $1`, po).
		Scan(&envelopeBranch, &payloadBranch, &products); err != nil {
		t.Fatalf("no purchase_order.received event: %v", err)
	}
	if envelopeBranch != defaultBranch || payloadBranch != defaultBranch {
		t.Errorf("received event branch = envelope %s payload %s, want the RECEIPT location's branch %s", envelopeBranch, payloadBranch, defaultBranch)
	}
	if products == "" || products == "[]" {
		t.Errorf("received event product_ids = %s, want the received product", products)
	}
}
