// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package order_test

// Delivery completion and the fulfilment request queue (ADR 0005 5.5): a
// completed delivery queues its request in the transaction that writes the
// delivered status, the worker bills the remainder once, a failing tax
// provider leaves the request for a later attempt, ten failures park it, and a
// pickup order is never routed.

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/gablelbm/gable/internal/delivery"
	"github.com/gablelbm/gable/internal/order"
	"github.com/gablelbm/gable/internal/testutil"
	"github.com/google/uuid"
)

type deliveryWorld struct {
	f      *fixture
	svc    *order.Service
	deliv  *delivery.Service
	prov   *fakeTax
	orders []string
}

// newDeliveryWorld serves delivery orders with stock, money and a tax
// provider the test can switch.
func newDeliveryWorld(t *testing.T) *deliveryWorld {
	t.Helper()
	testutil.LockOutboxTables(t)
	db := testutil.RequireDB(t)
	f := newFixture(t, db)
	w := &deliveryWorld{f: f, prov: &fakeTax{tax: 0}}
	w.svc = f.serveWith(f.withStock(), f.withMoney(), func(s *order.Service) *order.Service { return s.WithTaxProvider(w.prov) })
	f.cleanMoney()
	f.stock(f.productID, "100")
	f.setCost(f.productID, "2")
	w.deliv = delivery.NewService(delivery.NewRepository(db))
	w.deliv.WithFulfilment(w.svc, w.svc)
	w.deliv.WithTxRunner(db)
	t.Cleanup(func() {
		ctx := context.Background()
		_, _ = db.Pool.Exec(ctx, `DELETE FROM order_fulfillment_requests WHERE order_id IN (SELECT id FROM orders WHERE customer_id = $1)`, f.customerID)
		_, _ = db.Pool.Exec(ctx, `DELETE FROM deliveries WHERE order_id IN (SELECT id FROM orders WHERE customer_id = $1)`, f.customerID)
	})
	return w
}

// confirmedDeliveryOrder creates and confirms an order for qty PCS to be
// delivered, and returns its id and a pending delivery for it.
func (w *deliveryWorld) confirmedDeliveryOrder(t *testing.T, qty string) (orderID string, deliveryID uuid.UUID) {
	t.Helper()
	body := w.f.createBody(map[string]any{"product_id": w.f.productID.String(), "quantity": qty})
	body["delivery_type"] = "delivery"
	r := w.f.do("POST", "/api/v1/orders", body)
	if r.status != 201 {
		t.Fatalf("create = %d: %s", r.status, r.raw)
	}
	orderID = str(t, r.body, "id")
	r = w.f.transition(orderID, 1, "confirmed")
	if r.status != 200 || str(t, r.body, "status") != "confirmed" {
		t.Fatalf("confirm = %d %q: %s", r.status, r.body["status"], r.raw)
	}
	deliveryID = uuid.New()
	if _, err := w.f.db.Pool.Exec(context.Background(), `INSERT INTO deliveries (id, order_id, stop_sequence, status) VALUES ($1, $2, 1, 'PENDING')`, deliveryID, orderID); err != nil {
		t.Fatal(err)
	}
	w.orders = append(w.orders, orderID)
	return orderID, deliveryID
}

func (w *deliveryWorld) complete(t *testing.T, deliveryID uuid.UUID) error {
	t.Helper()
	proof, by := "https://pod.example/p.jpg", "Site foreman"
	one := int64(1)
	_, err := w.deliv.TransitionStop(context.Background(), deliveryID, &delivery.StopTransitionDraft{
		To: delivery.StopStatusDelivered, PODProofURL: &proof, PODSignedBy: &by},
		delivery.Precondition{Revision: &one}, "")
	return err
}

func (w *deliveryWorld) requests(t *testing.T, orderID string) (n int, attempts int, parked bool, lastErr string) {
	t.Helper()
	rows, err := w.f.db.Pool.Query(context.Background(),
		`SELECT attempts, parked_at IS NOT NULL, COALESCE(last_error, '') FROM order_fulfillment_requests WHERE order_id = $1`, orderID)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	for rows.Next() {
		n++
		if err := rows.Scan(&attempts, &parked, &lastErr); err != nil {
			t.Fatal(err)
		}
	}
	return
}

func (w *deliveryWorld) invoices(t *testing.T, orderID string) int {
	t.Helper()
	var n int
	if err := w.f.db.Pool.QueryRow(context.Background(), `SELECT count(*) FROM invoices WHERE order_id = $1`, orderID).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

// serve runs the worker's serve until the queue is empty or a serve fails;
// it answers the first error.
func (w *deliveryWorld) serve(t *testing.T) error {
	t.Helper()
	for i := 0; i < 50; i++ {
		served, err := w.svc.ServeFulfilmentRequest(context.Background())
		if err != nil {
			return err
		}
		if !served {
			return nil
		}
	}
	t.Fatal("the fulfilment queue never emptied")
	return nil
}

// RULE (ADR 0005 5.5 and 14.2): a delivery completion fulfils the remainder
// and never invoices billed quantity twice. The desk bills 4 of 10, the
// delivery completes: the worker bills the other 6 only.
func TestDeliveryCompletionBillsTheRemainderOnce(t *testing.T) {
	w := newDeliveryWorld(t)
	orderID, deliveryID := w.confirmedDeliveryOrder(t, "10")
	g := w.f.do("GET", "/api/v1/orders/"+orderID, nil)
	lineID := str(t, g.body["lines"].([]any)[0].(map[string]any), "id")
	r := w.f.fulfil(orderID, revision(t, g), map[string]any{"lines": []map[string]any{{"order_line_id": lineID, "quantity": "4"}}})
	if r.status != 201 {
		t.Fatalf("desk fulfilment = %d: %s", r.status, r.raw)
	}

	if err := w.complete(t, deliveryID); err != nil {
		t.Fatal(err)
	}
	if n, _, _, _ := w.requests(t, orderID); n != 1 {
		t.Fatalf("%d requests after the completion, want 1", n)
	}
	if err := w.serve(t); err != nil {
		t.Fatalf("serve: %v", err)
	}
	if n, _, _, _ := w.requests(t, orderID); n != 0 {
		t.Errorf("%d requests left after billing", n)
	}
	var sum string
	if err := w.f.db.Pool.QueryRow(context.Background(), `SELECT string_agg(l.quantity::text, ',' ORDER BY i.created_at, i.id) FROM invoice_lines l JOIN invoices i ON i.id = l.invoice_id WHERE i.order_id = $1`, orderID).Scan(&sum); err != nil {
		t.Fatal(err)
	}
	if sum != "4.0000,6.0000" {
		t.Errorf("billed quantities = %s, want 4 by the desk then 6 by the delivery", sum)
	}
	if got := w.f.lineQuantities(orderID); fmt.Sprint(got) != "[PRODUCT:10.0000/0.0000/0.0000/10.0000]" {
		t.Errorf("line quantities = %v, want 10 fulfilled", got)
	}
	var status, method string
	if err := w.f.db.Pool.QueryRow(context.Background(), `SELECT o.status, COALESCE(i.delivery_id::text, '') FROM orders o JOIN invoices i ON i.order_id = o.id WHERE o.id = $1 ORDER BY i.created_at DESC, i.id LIMIT 1`, orderID).Scan(&status, &method); err != nil || status != "FULFILLED" || method != deliveryID.String() {
		t.Errorf("order %s, last invoice's delivery %s (%v), want FULFILLED and the delivery's id", status, method, err)
	}
	// The completion skipped the credit and price exposure re-checks with one
	// audit row naming them.
	var skipped int
	if err := w.f.db.Pool.QueryRow(context.Background(), `SELECT count(*) FROM audit_log WHERE action = 'order.fulfillment_checks_skipped' AND entity_id = $1`, orderID).Scan(&skipped); err != nil || skipped != 1 {
		t.Errorf("%d fulfillment_checks_skipped audit rows (%v), want 1", skipped, err)
	}
	// A second completion of the same delivery (a replayed POD) is refused
	// under the revision precondition (the stop's revision moved with its
	// first completion) and queues nothing new either way.
	if err := w.complete(t, deliveryID); err == nil {
		t.Fatal("a replayed completion succeeded; it must be refused")
	}
	if err := w.serve(t); err != nil {
		t.Fatal(err)
	}
	if n := w.invoices(t, orderID); n != 2 {
		t.Errorf("%d invoices after a replayed completion, want 2: billed quantity is never invoiced twice", n)
	}
}

// RULE (ADR 0005 14.2): delivery completion records a delivered load for a
// customer now over the limit, skipping both re-checks with one audit row that
// says the credit would have refused.
func TestDeliveryCompletionSkipsTheCreditRecheckAndSaysSo(t *testing.T) {
	w := newDeliveryWorld(t)
	orderID, deliveryID := w.confirmedDeliveryOrder(t, "10")
	if _, err := w.f.db.Pool.Exec(context.Background(), `UPDATE customers SET credit_limit = 1.00 WHERE id = $1`, w.f.customerID); err != nil {
		t.Fatal(err)
	}
	// The desk's fulfilment is refused for the same customer.
	g := w.f.do("GET", "/api/v1/orders/"+orderID, nil)
	if r := w.f.fulfil(orderID, revision(t, g), nil); r.status != 409 {
		t.Fatalf("desk fulfilment over the limit = %d, want 409", r.status)
	}
	if err := w.complete(t, deliveryID); err != nil {
		t.Fatal(err)
	}
	if err := w.serve(t); err != nil {
		t.Fatalf("serve: %v", err)
	}
	if n := w.invoices(t, orderID); n != 1 {
		t.Errorf("%d invoices, want the delivered load billed", n)
	}
	var credit string
	if err := w.f.db.Pool.QueryRow(context.Background(), `SELECT changes->>'credit_limit_refuse' FROM audit_log WHERE action = 'order.fulfillment_checks_skipped' AND entity_id = $1`, orderID).Scan(&credit); err != nil || credit != "true" {
		t.Errorf("audit credit_limit_refuse = %q (%v), want true", credit, err)
	}
}

// RULE (ADR 0005 5.5 and 14.2): a delivery completed while the tax provider
// fails leaves its request, which a later attempt bills once the provider
// answers.
func TestFailedProviderLeavesTheRequestAndALaterAttemptBills(t *testing.T) {
	w := newDeliveryWorld(t)
	orderID, deliveryID := w.confirmedDeliveryOrder(t, "10")
	w.prov.err = errors.New("avalara down")
	if err := w.complete(t, deliveryID); err != nil {
		t.Fatal(err)
	}
	if err := w.serve(t); err == nil {
		t.Fatal("serve with the provider down succeeded")
	}
	n, attempts, parked, lastErr := w.requests(t, orderID)
	if n != 1 || attempts != 1 || parked || !strings.Contains(lastErr, "tax provider") {
		t.Fatalf("request = %d rows, %d attempts, parked %v, last error %q; want one request, 1 attempt, the provider's error", n, attempts, parked, lastErr)
	}
	if w.invoices(t, orderID) != 0 || w.f.inventoryOf(w.f.productID) != "100.0000/10.0000" {
		t.Fatalf("a failed attempt moved money or stock: invoices %d, inventory %s", w.invoices(t, orderID), w.f.inventoryOf(w.f.productID))
	}
	w.prov.err = nil
	w.prov.tax = 123
	if err := w.serve(t); err != nil {
		t.Fatalf("later attempt: %v", err)
	}
	if n, _, _, _ := w.requests(t, orderID); n != 0 {
		t.Errorf("request survived the billing")
	}
	var tax string
	if err := w.f.db.Pool.QueryRow(context.Background(), `SELECT ROUND(tax_amount * 100)::text || '/' || tax_source FROM invoices WHERE order_id = $1`, orderID).Scan(&tax); err != nil || tax != "123/PROVIDER" {
		t.Errorf("invoice tax = %s (%v), want the provider's 123 once", tax, err)
	}
	if w.invoices(t, orderID) != 1 {
		t.Errorf("%d invoices, want exactly 1", w.invoices(t, orderID))
	}
}

// RULE (ADR 0005 14.2): a tax_quote_stale failure is retried with a fresh price
// and bills. The provider's answer is in flight while the order's price moves.
func TestTaxQuoteStaleIsRetriedWithAFreshPrice(t *testing.T) {
	w := newDeliveryWorld(t)
	orderID, deliveryID := w.confirmedDeliveryOrder(t, "10")
	moved := false
	w.prov.hook = func() {
		if moved {
			return
		}
		moved = true
		if _, err := w.f.db.Pool.Exec(context.Background(), `UPDATE order_lines SET discount_amount = 1.00, discount_reason = 'late', line_total = line_total - 1 WHERE order_id = $1`, orderID); err != nil {
			t.Errorf("move the order under the provider: %v", err)
		}
	}
	if err := w.complete(t, deliveryID); err != nil {
		t.Fatal(err)
	}
	err := w.serve(t)
	if err == nil || !strings.Contains(err.Error(), "tax_quote_stale") && !strings.Contains(fmt.Sprint(err), "moved between") {
		t.Fatalf("first attempt = %v, want tax_quote_stale", err)
	}
	if _, attempts, parked, lastErr := w.requests(t, orderID); attempts != 1 || parked || !strings.Contains(lastErr, "tax_quote_stale") {
		t.Fatalf("after the stale attempt: %d attempts parked %v error %q", attempts, parked, lastErr)
	}
	if err := w.serve(t); err != nil {
		t.Fatalf("the retry with a fresh price: %v", err)
	}
	if w.invoices(t, orderID) != 1 {
		t.Errorf("%d invoices, want the retry to bill once", w.invoices(t, orderID))
	}
}

// RULE (ADR 0005 5.5 and 14.2): a request failing 10 times parks, stays
// readable at GET /orders/fulfillment-requests?parked=true with its last_error,
// is never deleted, is not retried automatically, and bills after the retry
// route. order.fulfillment_parked is written when it parks.
func TestTenFailuresParkTheRequestAndRetryBills(t *testing.T) {
	w := newDeliveryWorld(t)
	orderID, deliveryID := w.confirmedDeliveryOrder(t, "10")
	w.prov.err = errors.New("avalara down")
	if err := w.complete(t, deliveryID); err != nil {
		t.Fatal(err)
	}
	for i := 1; i <= 10; i++ {
		if err := w.serve(t); err == nil {
			t.Fatalf("attempt %d succeeded with the provider down", i)
		}
	}
	n, attempts, parked, _ := w.requests(t, orderID)
	if n != 1 || attempts != 10 || !parked {
		t.Fatalf("after ten failures: %d rows, %d attempts, parked %v; want one parked request at 10", n, attempts, parked)
	}
	if ev := eventsFor(t, w.f.db, orderID); ev[len(ev)-1] != "order.fulfillment_parked" {
		t.Errorf("events = %v, want order.fulfillment_parked last", ev)
	}
	// Never retried automatically: with the provider healed, a serve does nothing.
	w.prov.err = nil
	if err := w.serve(t); err != nil {
		t.Fatal(err)
	}
	if w.invoices(t, orderID) != 0 {
		t.Fatal("a parked request was retried automatically")
	}
	// The desk reads it.
	r := w.f.do("GET", "/api/v1/orders/fulfillment-requests?parked=true&order_id="+orderID, nil)
	if r.status != 200 {
		t.Fatalf("list = %d: %s", r.status, r.raw)
	}
	items := r.body["items"].([]any)
	if len(items) != 1 {
		t.Fatalf("%d parked requests listed, want 1: %s", len(items), r.raw)
	}
	it := items[0].(map[string]any)
	if it["delivery_id"] != deliveryID.String() || num(t, it, "attempts") != 10 || !strings.Contains(str(t, it, "last_error"), "tax provider") || it["parked_at"] == nil {
		t.Errorf("listed request = %v, want the delivery, 10 attempts, its last_error and parked_at", it)
	}
	if r := w.f.do("GET", "/api/v1/orders/fulfillment-requests?parked=false&order_id="+orderID, nil); len(r.body["items"].([]any)) != 0 {
		t.Errorf("a parked request listed under parked=false")
	}
	if r := w.f.do("GET", "/api/v1/orders/fulfillment-requests?bogus=1", nil); r.status != 400 {
		t.Errorf("an unknown parameter = %d, want 400", r.status)
	}
	// The retry route clears it and the worker bills.
	if r := w.f.do("POST", "/api/v1/orders/fulfillment-requests/"+deliveryID.String()+"/retry", nil); r.status != 204 {
		t.Fatalf("retry = %d: %s", r.status, r.raw)
	}
	if r := w.f.do("POST", "/api/v1/orders/fulfillment-requests/"+uuid.NewString()+"/retry", nil); r.status != 404 {
		t.Errorf("retry of an unknown request = %d, want 404", r.status)
	}
	if err := w.serve(t); err != nil {
		t.Fatalf("serve after the retry: %v", err)
	}
	if w.invoices(t, orderID) != 1 {
		t.Errorf("%d invoices after the retry, want 1", w.invoices(t, orderID))
	}
}

// failingQueue fails the enqueue.
type failingQueue struct{}

func (failingQueue) EnqueueFulfilment(context.Context, uuid.UUID, uuid.UUID) error {
	return errors.New("request insert failed")
}

// RULE (ADR 0005 5.5, 14.2): a delivered status whose request insert fails
// rolls back with it.
func TestDeliveredStatusRollsBackWithItsRequest(t *testing.T) {
	w := newDeliveryWorld(t)
	_, deliveryID := w.confirmedDeliveryOrder(t, "10")
	w.deliv.WithFulfilment(failingQueue{}, w.svc)
	if err := w.complete(t, deliveryID); err == nil {
		t.Fatal("completion succeeded though its request could not be written")
	}
	var status string
	if err := w.f.db.Pool.QueryRow(context.Background(), `SELECT status FROM deliveries WHERE id = $1`, deliveryID).Scan(&status); err != nil || status != "PENDING" {
		t.Errorf("delivery status = %q (%v), want PENDING: the delivered write rolled back", status, err)
	}
}

// RULE (ADR 0005 14.2): the migration case. An order a pre-094 delivery
// completion invoiced and the migration marked fulfilled bills nothing on a
// later completion: the request is deleted, no invoice is added, and the desk's
// fulfilment is refused.
func TestMigratedFulfilledOrderBillsNothingAfterward(t *testing.T) {
	w := newDeliveryWorld(t)
	orderID, deliveryID := w.confirmedDeliveryOrder(t, "10")
	ctx := context.Background()
	// the state the migration leaves: fulfilled lines, FULFILLED, nothing allocated
	if _, err := w.f.db.Pool.Exec(ctx, `UPDATE order_lines SET quantity_fulfilled = quantity, quantity_allocated = 0 WHERE order_id = $1`, orderID); err != nil {
		t.Fatal(err)
	}
	if _, err := w.f.db.Pool.Exec(ctx, `UPDATE orders SET status = 'FULFILLED' WHERE id = $1`, orderID); err != nil {
		t.Fatal(err)
	}
	if err := w.complete(t, deliveryID); err != nil {
		t.Fatal(err)
	}
	if err := w.serve(t); err != nil {
		t.Fatalf("serve: %v", err)
	}
	if n, _, _, _ := w.requests(t, orderID); n != 0 || w.invoices(t, orderID) != 0 {
		t.Errorf("requests %d invoices %d, want both 0: nothing left to bill", n, w.invoices(t, orderID))
	}
	g := w.f.do("GET", "/api/v1/orders/"+orderID, nil)
	if r := w.f.fulfil(orderID, revision(t, g), nil); r.status != 409 {
		t.Errorf("a desk fulfilment of a fulfilled order = %d, want 409", r.status)
	}
}

// RULE (ADR 0005 5.5): a pickup order is never routed: the delivery module's
// stop creation refuses it with the blocker pickup_order.
func TestPickupOrderIsNeverRouted(t *testing.T) {
	w := newDeliveryWorld(t)
	r := w.f.create() // a pickup order
	orderID := str(t, r.body, "id")
	vehicle, route := uuid.New(), uuid.New()
	ctx := context.Background()
	if _, err := w.f.db.Pool.Exec(ctx, `INSERT INTO vehicles (id, name, vehicle_type, license_plate) VALUES ($1, $2, 'FLATBED', $3)`, vehicle, "pk-"+vehicle.String()[:6], "PK"+vehicle.String()[:5]); err != nil {
		t.Fatalf("vehicle fixture: %v", err)
	}
	if _, err := w.f.db.Pool.Exec(ctx, `INSERT INTO delivery_routes (id, vehicle_id, scheduled_date, status) VALUES ($1, $2, CURRENT_DATE, 'DRAFT')`, route, vehicle); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = w.f.db.Pool.Exec(ctx, `DELETE FROM deliveries WHERE route_id = $1`, route)
		_, _ = w.f.db.Pool.Exec(ctx, `DELETE FROM delivery_routes WHERE id = $1`, route)
		_, _ = w.f.db.Pool.Exec(ctx, `DELETE FROM vehicles WHERE id = $1`, vehicle)
	})
	seq := 1
	_, _, err := w.deliv.AssignOrderToRoute(ctx, &delivery.AssignStopDraft{RouteID: route, OrderID: uuid.MustParse(orderID), StopSequence: &seq}, "")
	if err == nil || !strings.Contains(err.Error(), "never routed") {
		t.Fatalf("assigning a pickup order = %v, want the pickup_order refusal", err)
	}
	var n int
	if err := w.f.db.Pool.QueryRow(ctx, `SELECT count(*) FROM deliveries WHERE order_id = $1`, orderID).Scan(&n); err != nil || n != 0 {
		t.Errorf("%d deliveries created for a pickup order", n)
	}
}
