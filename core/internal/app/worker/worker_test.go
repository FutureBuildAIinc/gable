// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package worker

// The worker's own wiring (newOrderService), driven end to end against a real
// database: the order service the queue jobs run on must bill with the same
// configured tax provider serve wires, so a delivery completion invoice
// carries the provider's tax and not the branch rate (PR 43 review round 1,
// P2-1: the worker built the service with neither the provider nor the
// exposure gate).

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gablelbm/gable/internal/config"
	"github.com/gablelbm/gable/internal/order"
	"github.com/gablelbm/gable/internal/payment"
	"github.com/gablelbm/gable/internal/platform/httpx"
	"github.com/gablelbm/gable/internal/salesdoc"
	"github.com/gablelbm/gable/internal/testutil"
	"github.com/gablelbm/gable/pkg/branchctx"
	"github.com/google/uuid"
)

// RULE (ADR 0005 section 3, PR 43 review round 1 P2-1): with a tax provider
// configured, the invoice a delivery completion's fulfilment request writes
// carries the provider's tax (tax_source provider), because the worker builds
// its order service with the same provider wiring as serve. Against the
// branch's 8.25 percent rate the provider's 450 cents is unmistakable.
func TestWorkerOrderServiceBillsWithTheConfiguredTaxProvider(t *testing.T) {
	testutil.LockOutboxTables(t)
	db := testutil.RequireDB(t)
	ctx := branchctx.WithSystem(context.Background())

	// A local stand-in for the provider's API, answering the AvaTax create
	// transaction shape; its total tax is what the invoice must carry.
	stub := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"code":"wiring-stub","totalAmount":55.00,"totalTax":4.50,"status":"Saved"}`)
	}))
	t.Cleanup(stub.Close)

	cfg := &config.Config{
		AvalaraAccountID:   "wiring-test-account",
		AvalaraLicenseKey:  "wiring-test-license",
		AvalaraCompanyCode: "WIRING",
		AvalaraBaseURL:     stub.URL,
	}
	svc := newOrderService(db, cfg)

	// Seed: a customer on the default branch (whose rate is 8.25 percent, a
	// figure the provider never answers), a product at 5.50 costing 2, and
	// 100 on hand at a yard.
	branch := `(SELECT value::uuid FROM system_settings WHERE key = 'default_branch_id')`
	customer, product, yard := uuid.New(), uuid.New(), uuid.New()
	if _, err := db.Pool.Exec(ctx, `INSERT INTO customers (id, name, account_number, primary_branch_id)
		VALUES ($1, 'Worker Wiring Co', 'WWIR-' || $2, `+branch+`)`, customer, uuid.NewString()[:8]); err != nil {
		t.Fatalf("seed customer: %v", err)
	}
	if _, err := db.Pool.Exec(ctx, `INSERT INTO products (id, sku, description, uom_primary, base_price, average_unit_cost)
		VALUES ($1, $2, '2x4x8 SPF', 'PCS', 5.5, 2)`, product, "WWIR-"+uuid.NewString()[:8]); err != nil {
		t.Fatalf("seed product: %v", err)
	}
	if _, err := db.Pool.Exec(ctx, `UPDATE locations SET default_tax_rate = 0.0825 WHERE id = `+branch); err != nil {
		t.Fatalf("seed branch rate: %v", err)
	}
	if _, err := db.Pool.Exec(ctx, `INSERT INTO locations (id, type, code, parent_id, branch_id) VALUES ($1, 'YARD', 'WW-' || $2, `+branch+`, `+branch+`)`,
		yard, yard.String()[:8]); err != nil {
		t.Fatalf("seed yard: %v", err)
	}
	if _, err := db.Pool.Exec(ctx, `INSERT INTO inventory (product_id, location_id, location, quantity, allocated) VALUES ($1, $2, 'Y', 100, 0)`,
		product, yard); err != nil {
		t.Fatalf("seed inventory: %v", err)
	}
	var orderID uuid.UUID
	t.Cleanup(func() {
		_, _ = db.Pool.Exec(ctx, `DELETE FROM order_fulfillment_requests WHERE order_id = $1`, orderID)
		_, _ = db.Pool.Exec(ctx, `DELETE FROM deliveries WHERE order_id = $1`, orderID)
		_, _ = db.Pool.Exec(ctx, `DELETE FROM inventory WHERE location_id = $1`, yard)
		_, _ = db.Pool.Exec(ctx, `DELETE FROM locations WHERE id = $1`, yard)
		_, _ = db.Pool.Exec(ctx, `DELETE FROM gl_journal_lines WHERE journal_entry_id IN (SELECT e.id FROM gl_journal_entries e JOIN invoices i ON e.source_ref_id = i.id WHERE i.customer_id = $1)`, customer)
		_, _ = db.Pool.Exec(ctx, `DELETE FROM gl_journal_entries WHERE source_ref_id IN (SELECT id FROM invoices WHERE customer_id = $1)`, customer)
		_, _ = db.Pool.Exec(ctx, `DELETE FROM events_outbox WHERE entity_id IN (SELECT id FROM invoices WHERE customer_id = $1)`, customer)
		_, _ = db.Pool.Exec(ctx, `DELETE FROM customer_transactions WHERE customer_id = $1`, customer)
		_, _ = db.Pool.Exec(ctx, `DELETE FROM invoice_lines WHERE invoice_id IN (SELECT id FROM invoices WHERE customer_id = $1)`, customer)
		_, _ = db.Pool.Exec(ctx, `DELETE FROM invoices WHERE customer_id = $1`, customer)
		_, _ = db.Pool.Exec(ctx, `DELETE FROM events_outbox WHERE entity_type = 'order' AND entity_id IN (SELECT id FROM orders WHERE customer_id = $1)`, customer)
		_, _ = db.Pool.Exec(ctx, `DELETE FROM order_lines WHERE order_id IN (SELECT id FROM orders WHERE customer_id = $1)`, customer)
		_, _ = db.Pool.Exec(ctx, `DELETE FROM orders WHERE customer_id = $1`, customer)
		_, _ = db.Pool.Exec(ctx, `DELETE FROM products WHERE id = $1`, product)
		_, _ = db.Pool.Exec(ctx, `DELETE FROM customers WHERE id = $1`, customer)
	})

	q, _ := httpx.ParseQuantity("10")
	o, err := svc.Create(ctx, &order.Draft{
		CustomerID:   customer,
		DeliveryType: order.DeliveryDelivery,
		Lines:        []salesdoc.ParsedLine{{LineType: salesdoc.LineProduct, ProductID: &product, Quantity: q}},
	}, "worker-wiring")
	if err != nil {
		t.Fatalf("create order: %v", err)
	}
	orderID = o.ID
	rev := o.Revision
	if _, err := svc.Transition(ctx, orderID, order.StatusConfirmed, order.Precondition{Revision: &rev}, order.TransitionBody{}); err != nil {
		t.Fatalf("confirm order: %v", err)
	}
	delivery := uuid.New()
	if _, err := db.Pool.Exec(ctx, `INSERT INTO deliveries (id, order_id, stop_sequence, status) VALUES ($1, $2, 1, 'PENDING')`, delivery, orderID); err != nil {
		t.Fatalf("seed the delivery: %v", err)
	}
	if err := svc.EnqueueFulfilment(ctx, delivery, orderID); err != nil {
		t.Fatalf("queue the completed delivery: %v", err)
	}
	if served, err := svc.ServeFulfilmentRequest(ctx); err != nil || !served {
		t.Fatalf("serve the fulfilment request: served=%v err=%v", served, err)
	}

	var invoices int
	var tax string
	if err := db.Pool.QueryRow(ctx,
		`SELECT count(*), COALESCE(string_agg(ROUND(tax_amount * 100)::text || '/' || tax_source, ','), '') FROM invoices WHERE order_id = $1`, orderID).
		Scan(&invoices, &tax); err != nil {
		t.Fatal(err)
	}
	if invoices != 1 {
		t.Fatalf("%d invoices for the delivered order, want 1", invoices)
	}
	if tax != "450/PROVIDER" {
		t.Errorf("delivery completion invoice tax = %q, want the provider's 450 with tax_source PROVIDER", tax)
	}
}

// N5 (CP-05): the worker role runs the periodic terminal intent sweep over
// every branch, closing expired claimed intents with no manager item and
// moving expired pending_at_device intents to lost with one open manager item.
func TestWorkerSweepsExpiredTerminalIntentsAcrossBranches(t *testing.T) {
	testutil.LockTerminalTables(t)
	db := testutil.RequireDB(t)
	ctx := context.Background()

	branchA, branchB := uuid.New(), uuid.New()
	for _, b := range []uuid.UUID{branchA, branchB} {
		if _, err := db.Pool.Exec(ctx,
			`INSERT INTO locations (id, type, code, name, branch_id) VALUES ($1, 'BRANCH', $2, 'Worker Sweep Yard', $1)`,
			b, "WSW-"+b.String()[:8]); err != nil {
			t.Fatalf("seed branch: %v", err)
		}
	}
	claimedID, pendingID := uuid.New(), uuid.New()
	txA, txB := uuid.New(), uuid.New()
	for i, pair := range []struct {
		tx     uuid.UUID
		branch uuid.UUID
	}{{txA, branchA}, {txB, branchB}} {
		if _, err := db.Pool.Exec(ctx, `INSERT INTO pos_transactions
			(id, register_id, cashier_id, subtotal, tax_amount, total, status, branch_id, change_due, revision, currency, number, created_at, updated_at)
			VALUES ($1, $2, $3, 0, 0, 0, 'OPEN', $4, 0, 1, 'USD', $5, NOW(), NOW())`,
			pair.tx, fmt.Sprintf("REG-WSW-%d", i), uuid.New(), pair.branch, "POS-"+pair.tx.String()[:8]); err != nil {
			t.Fatalf("seed sale: %v", err)
		}
	}
	t.Cleanup(func() {
		_, _ = db.Pool.Exec(ctx, `DELETE FROM audit_log WHERE action = 'pos.terminal_intents.swept'`)
		_, _ = db.Pool.Exec(ctx, `DELETE FROM payment_terminal_lost_items WHERE intent_id IN ($1, $2)`, claimedID, pendingID)
		_, _ = db.Pool.Exec(ctx, `DELETE FROM payment_terminal_intents WHERE id IN ($1, $2)`, claimedID, pendingID)
		_, _ = db.Pool.Exec(ctx, `DELETE FROM pos_transactions WHERE id IN ($1, $2)`, txA, txB)
		_, _ = db.Pool.Exec(ctx, `DELETE FROM locations WHERE id IN ($1, $2)`, branchA, branchB)
	})

	ledger := payment.NewTerminalLedger(db)
	pastLease := time.Now().UTC().Add(-time.Minute)
	if err := ledger.Claim(ctx, &payment.TerminalIntent{
		ID: claimedID, TransactionID: txA, LegKey: "card-0", RegisterID: "REG-WA",
		BranchID: branchA, AmountCents: 1200, Kind: payment.TerminalKindSale,
		DeviceID: "DEV-WA", IdempotencyKey: "wsw-claimed-" + claimedID.String(),
		LeaseUntil: pastLease,
	}); err != nil {
		t.Fatalf("claim A: %v", err)
	}
	if err := ledger.Claim(ctx, &payment.TerminalIntent{
		ID: pendingID, TransactionID: txB, LegKey: "card-0", RegisterID: "REG-WB",
		BranchID: branchB, AmountCents: 3400, Kind: payment.TerminalKindSale,
		DeviceID: "DEV-WB", IdempotencyKey: "wsw-pending-" + pendingID.String(),
		LeaseUntil: time.Now().UTC().Add(time.Minute),
	}); err != nil {
		t.Fatalf("claim B: %v", err)
	}
	if ok, err := ledger.MarkDispatched(ctx, pendingID, "devref-wsw", pastLease); err != nil || !ok {
		t.Fatalf("mark dispatched B: ok=%v err=%v", ok, err)
	}

	runner := newTerminalSweepRunner(db, slog.Default())
	runner.interval = 10 * time.Millisecond
	runner.Start()
	defer runner.Stop()

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		inA, err := ledger.Get(ctx, claimedID)
		if err != nil {
			t.Fatalf("get A: %v", err)
		}
		inB, err := ledger.Get(ctx, pendingID)
		if err != nil {
			t.Fatalf("get B: %v", err)
		}
		if inA.State == payment.TerminalLost && inB.State == payment.TerminalLost {
			break
		}
		time.Sleep(15 * time.Millisecond)
	}
	runner.Stop()

	inA, err := ledger.Get(ctx, claimedID)
	if err != nil {
		t.Fatalf("get A: %v", err)
	}
	if inA.State != payment.TerminalLost {
		t.Fatalf("claimed intent state = %q, wanted lost", inA.State)
	}
	if itemA, err := ledger.LostItemOfIntent(ctx, claimedID); err != nil || itemA != nil {
		t.Fatalf("claimed intent lost item = %+v, err=%v, wanted nil", itemA, err)
	}

	inB, err := ledger.Get(ctx, pendingID)
	if err != nil {
		t.Fatalf("get B: %v", err)
	}
	if inB.State != payment.TerminalLost {
		t.Fatalf("pending_at_device intent state = %q, wanted lost", inB.State)
	}
	itemB, err := ledger.LostItemOfIntent(ctx, pendingID)
	if err != nil || itemB == nil {
		t.Fatalf("pending_at_device lost item = %+v, err=%v, wanted open item", itemB, err)
	}
	if itemB.State != "open" || itemB.ProviderRef != "devref-wsw" || itemB.AmountCents != 3400 {
		t.Fatalf("lost item B = %+v", itemB)
	}
}
