// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package order_test

// Release on receipt through the allocation request queue (ADR 0005 5.4): the
// subscriber only inserts requests, in the orders' confirmed_at order; the
// worker serves them oldest first, one order per transaction; a replayed
// receive allocates nothing twice; and the subscriber holds no order or
// inventory lock, so it cannot deadlock against a confirm.

import (
	"math"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/gablelbm/gable/internal/inventory"
	"github.com/gablelbm/gable/internal/order"
	"github.com/gablelbm/gable/internal/product"
	"github.com/gablelbm/gable/internal/platform/httpx"
	"github.com/gablelbm/gable/internal/purchase_order"
	"github.com/gablelbm/gable/internal/testutil"
	"github.com/gablelbm/gable/pkg/database"
	"github.com/gablelbm/gable/pkg/eventbus"
	"github.com/gablelbm/gable/pkg/outbox"
	"github.com/google/uuid"
)

// queueWorld is three back ordered orders (10 PCS each) for one product with
// no stock, confirmed in a known order, and the service the worker would use.
type queueWorld struct {
	f      *fixture
	svc    *order.Service
	ids    []string
	branch uuid.UUID
}

func newQueueWorld(t *testing.T) *queueWorld {
	t.Helper()
	testutil.LockOutboxTables(t)
	db := testutil.RequireDB(t)
	f := newFixture(t, db)
	svc := f.serveWith(f.withStock())
	w := &queueWorld{f: f, svc: svc}
	if err := db.Pool.QueryRow(context.Background(), `SELECT value::uuid FROM system_settings WHERE key = 'default_branch_id'`).Scan(&w.branch); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 3; i++ {
		r := f.create()
		id := str(t, r.body, "id")
		r = f.transition(id, 1, "confirmed")
		if str(t, r.body, "status") != "backordered" {
			t.Fatalf("order %d did not back order: %s", i, r.raw)
		}
		w.ids = append(w.ids, id)
	}
	t.Cleanup(func() {
		_, _ = db.Pool.Exec(context.Background(), `DELETE FROM order_allocation_requests WHERE order_id = ANY($1::uuid[])`, w.ids)
	})
	return w
}

func (w *queueWorld) receivedEvent() eventbus.Event {
	raw, _ := json.Marshal(map[string]any{
		"purchase_order_id": uuid.New(), "branch_id": w.branch, "product_ids": []uuid.UUID{w.f.productID},
	})
	return eventbus.NewEventWithID(uuid.NewString(), order.SubjectPurchaseOrderReceived, raw)
}

// handle runs the subscriber in a transaction, as the drain's pass does.
func (w *queueWorld) handle(t *testing.T, ev eventbus.Event) {
	t.Helper()
	if err := w.f.db.RunInTx(context.Background(), func(ctx context.Context) error {
		return w.svc.HandlePurchaseOrderReceived(ctx, ev)
	}); err != nil {
		t.Fatalf("subscriber: %v", err)
	}
}

func (w *queueWorld) queued(t *testing.T) []string {
	t.Helper()
	rows, err := w.f.db.Pool.Query(context.Background(),
		`SELECT order_id::text FROM order_allocation_requests WHERE order_id = ANY($1::uuid[]) ORDER BY position`, w.ids)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			t.Fatal(err)
		}
		out = append(out, id)
	}
	return out
}

func (w *queueWorld) allocated(t *testing.T, id string) string {
	t.Helper()
	var s string
	if err := w.f.db.Pool.QueryRow(context.Background(),
		`SELECT o.status || ':' || l.quantity_allocated::text || '/' || l.quantity_backordered::text FROM orders o JOIN order_lines l ON l.order_id = o.id WHERE o.id = $1`, id).Scan(&s); err != nil {
		t.Fatal(err)
	}
	return s
}

func (w *queueWorld) serveAll(t *testing.T) int {
	t.Helper()
	n := 0
	for {
		served, err := w.svc.ServeAllocationRequest(context.Background())
		if err != nil {
			t.Fatalf("serve: %v", err)
		}
		if !served {
			return n
		}
		n++
	}
}

// RULE (ADR 0005 5.4): the requests of one receipt are served oldest
// confirmed_at first; 14 units cover the first order whole and the second
// partly, the third stays on back order; each is served in its own
// transaction; a replay of the receive event inserts one request per order at
// most once and allocates nothing twice.
func TestBackorderReleaseServesOldestFirstAndReplayAllocatesNothingTwice(t *testing.T) {
	w := newQueueWorld(t)
	w.f.stock(w.f.productID, "14")

	ev := w.receivedEvent()
	w.handle(t, ev)
	w.handle(t, ev) // the replay
	if got := w.queued(t); fmt.Sprint(got) != fmt.Sprint(w.ids) {
		t.Fatalf("queued = %v, want the orders in confirmed_at order %v", got, w.ids)
	}
	if n := w.serveAll(t); n != 3 {
		t.Fatalf("served %d requests, want 3", n)
	}
	want := []string{"CONFIRMED:10.0000/0.0000", "BACKORDERED:4.0000/6.0000", "BACKORDERED:0.0000/10.0000"}
	for i, id := range w.ids {
		if got := w.allocated(t, id); got != want[i] {
			t.Errorf("order %d = %s, want %s", i+1, got, want[i])
		}
	}
	if got := w.f.inventoryOf(w.f.productID); got != "14.0000/14.0000" {
		t.Errorf("inventory = %s, want all 14 allocated once", got)
	}
	// order.backorder_released only for the order whose back order cleared.
	if ev := eventsFor(t, w.f.db, w.ids[0]); fmt.Sprint(ev) != "[order.created order.confirmed order.backordered order.backorder_released]" {
		t.Errorf("first order events = %v", ev)
	}
	if ev := eventsFor(t, w.f.db, w.ids[1]); fmt.Sprint(ev) != "[order.created order.confirmed order.backordered]" {
		t.Errorf("second order events = %v, want no release (still back ordered)", ev)
	}

	// The replay after everything was served: the second order is still back
	// ordered, so it is queued again, and serving it finds nothing to allocate
	// (the stock is all allocated) and deletes the request.
	w.handle(t, ev)
	if n := w.serveAll(t); n != 2 {
		t.Errorf("served %d requests on the replay, want the 2 orders still back ordered", n)
	}
	if got := w.f.inventoryOf(w.f.productID); got != "14.0000/14.0000" {
		t.Errorf("inventory after the replay = %s, want unchanged", got)
	}
	if got := w.queued(t); len(got) != 0 {
		t.Errorf("%d requests left after serving, want none", len(got))
	}
}

// RULE (ADR 0005 5.4): a request for an order that is no longer back ordered
// (it was allocated on demand, or cancelled) is deleted by the worker without
// allocating anything.
func TestLeftoverRequestIsDeleted(t *testing.T) {
	w := newQueueWorld(t)
	w.f.stock(w.f.productID, "30")
	w.handle(t, w.receivedEvent())
	// the desk allocates the first order on demand, bypassing the queue
	r := w.f.do("POST", "/api/v1/orders/"+w.ids[0]+"/allocate", nil, "If-Match", `"2"`)
	if r.status != 200 || str(t, r.body, "status") != "confirmed" {
		t.Fatalf("allocate = %d %q: %s", r.status, r.body["status"], r.raw)
	}
	if got := w.queued(t); len(got) != 3 {
		t.Fatalf("/allocate must not touch the queue: %v", got)
	}
	w.serveAll(t)
	if got := w.f.inventoryOf(w.f.productID); got != "30.0000/30.0000" {
		t.Errorf("inventory = %s, want each order allocated once (30)", got)
	}
	if got := w.queued(t); len(got) != 0 {
		t.Errorf("leftover requests = %v", got)
	}
	// /allocate on a confirmed (not back ordered) order allocates nothing more
	// and is not an error; a stale revision is a 409; neither header is a 428.
	if r := w.f.do("POST", "/api/v1/orders/"+w.ids[1]+"/allocate", nil); r.status != 428 {
		t.Errorf("allocate with no precondition = %d, want 428", r.status)
	}
	if r := w.f.do("POST", "/api/v1/orders/"+w.ids[1]+"/allocate", nil, "If-Match", `"1"`); r.status != 409 {
		t.Errorf("allocate with a stale revision = %d, want 409", r.status)
	}
}

// RULE (ADR 0005 5.4, 11): /allocate racing the worker on the same order ends
// without deadlock, allocates once, and the leftover request is deleted.
func TestAllocateRacingTheWorkerEndsWithoutDeadlock(t *testing.T) {
	w := newQueueWorld(t)
	w.f.stock(w.f.productID, "10")
	w.handle(t, w.receivedEvent())

	var wg sync.WaitGroup
	statuses := make([]int, 2)
	wg.Add(2)
	go func() {
		defer wg.Done()
		for {
			served, err := w.svc.ServeAllocationRequest(context.Background())
			if err != nil {
				t.Errorf("serve: %v", err)
				return
			}
			if !served {
				return
			}
		}
	}()
	go func() {
		defer wg.Done()
		r := w.f.do("POST", "/api/v1/orders/"+w.ids[0]+"/allocate", nil, "If-Match", `"2"`)
		statuses[1] = r.status
	}()
	done := make(chan struct{})
	go func() { wg.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(20 * time.Second):
		t.Fatal("the worker and /allocate deadlocked")
	}
	if got := w.f.inventoryOf(w.f.productID); got != "10.0000/10.0000" {
		t.Errorf("inventory = %s, want 10 allocated once, not twice", got)
	}
	if statuses[1] != 200 && statuses[1] != 409 {
		t.Errorf("/allocate answered %d, want 200 or 409 (stale after the worker)", statuses[1])
	}
	if got := w.queued(t); len(got) != 0 {
		t.Errorf("requests left: %v", got)
	}
}

// RULE (ADR 0005 5.4 and ADR 0003 section 2): the drain's pass holds no order
// or inventory lock: the subscriber runs and completes while a confirm in
// flight holds the order row and the product's inventory rows, because it only
// inserts requests. A replayed receipt next to a confirm cannot deadlock.
func TestSubscriberHoldsNoOrderOrInventoryLock(t *testing.T) {
	w := newQueueWorld(t)
	w.f.stock(w.f.productID, "5")
	ctx := context.Background()

	// A "confirm in flight": a transaction holding the orders through the real
	// LockOrder and the product's inventory rows through the real inventory
	// lock, as order.confirm does (section 11 steps 1 and 6).
	held := make(chan struct{})
	release := make(chan struct{})
	holder := make(chan error, 1)
	go func() {
		holder <- w.f.db.RunInTx(ctx, func(ctx context.Context) error {
			repo := order.NewRepository(w.f.db)
			for _, id := range w.ids {
				if err := repo.LockOrder(ctx, uuid.MustParse(id)); err != nil {
					return err
				}
			}
			if _, err := inventory.NewRepository(w.f.db).LockBranchInventory(ctx, w.f.productID, w.branch); err != nil {
				return err
			}
			close(held)
			<-release
			return nil
		})
	}()
	select {
	case <-held:
	case err := <-holder:
		t.Fatalf("the holder failed: %v", err)
	}
	defer func() { close(release); <-holder }()

	done := make(chan error, 1)
	go func() {
		done <- w.f.db.RunInTx(ctx, func(ctx context.Context) error {
			if _, err := w.f.db.GetExecutor(ctx).Exec(ctx, `SET LOCAL lock_timeout = '3s'`); err != nil {
				return err
			}
			return w.svc.HandlePurchaseOrderReceived(ctx, w.receivedEvent())
		})
	}()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("the subscriber waited on a lock the confirm holds: %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("the subscriber blocked behind the confirm's locks")
	}
	if got := w.queued(t); len(got) != 3 {
		t.Errorf("queued = %v, want the three orders", got)
	}
}

// RULE (ADR 0005 5.4 and 12; IN-2.4): the receive writes purchase_order.received
// and the whole chain works through the real drain: receive, event, drain
// subscriber, request, worker, released order.
func TestReceiveDrivesTheBackorderReleaseThroughTheDrain(t *testing.T) {
	w := newQueueWorld(t)
	db := w.f.db
	ctx := context.Background()
	// The receive path: a PO line for the product, received at a yard.
	yard := uuid.New()
	branchSQL := `(SELECT value::uuid FROM system_settings WHERE key = 'default_branch_id')`
	if _, err := db.Pool.Exec(ctx, `INSERT INTO locations (id, type, code, parent_id, branch_id) VALUES ($1, 'YARD', $2, `+branchSQL+`, `+branchSQL+`)`, yard, "RC-"+yard.String()[:8]); err != nil {
		t.Fatal(err)
	}
	po, poLine := uuid.New(), uuid.New()
	vendor := uuid.New()
	if _, err := db.Pool.Exec(ctx, `INSERT INTO vendors (id, name) VALUES ($1, $2)`, vendor, "recv-"+vendor.String()[:8]); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Pool.Exec(ctx, `INSERT INTO purchase_orders (id, vendor_id, status, source, branch_id) VALUES ($1, $2, 'SENT', 'MANUAL', `+branchSQL+`)`, po, vendor); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Pool.Exec(ctx, `INSERT INTO purchase_order_lines (id, po_id, product_id, description, quantity, unit_cost, line_total) VALUES ($1, $2, $3, 'recv', 12, 3, 36)`, poLine, po, w.f.productID); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = db.Pool.Exec(ctx, `DELETE FROM events_outbox WHERE entity_type = 'purchase_order' AND entity_id = $1`, po)
		_, _ = db.Pool.Exec(ctx, `DELETE FROM purchase_order_lines WHERE po_id = $1`, po)
		_, _ = db.Pool.Exec(ctx, `DELETE FROM purchase_orders WHERE id = $1`, po)
		_, _ = db.Pool.Exec(ctx, `DELETE FROM inventory WHERE location_id = $1`, yard)
		_, _ = db.Pool.Exec(ctx, `DELETE FROM locations WHERE id = $1`, yard)
		_, _ = db.Pool.Exec(ctx, `DELETE FROM vendors WHERE id = $1`, vendor)
	})

	drain := outbox.NewDrainRunner(db, slog.Default())
	drain.Subscribe(order.SubjectPurchaseOrderReceived, "order-allocation-requests-test-"+uuid.NewString()[:8],
		func(ctx context.Context, ev eventbus.Event) error { return w.svc.HandlePurchaseOrderReceived(ctx, ev) })
	if err := drain.Start(ctx); err != nil {
		t.Fatal(err)
	}
	defer drain.Stop()

	receive(t, db, po, poLine, yard, 12)

	// The event is in the outbox with the products.
	var data string
	if err := db.Pool.QueryRow(ctx, `SELECT data::text FROM events_outbox WHERE entity_type = 'purchase_order' AND entity_id = $1 AND type = 'purchase_order.received'`, po).Scan(&data); err != nil {
		t.Fatalf("no purchase_order.received event: %v", err)
	}
	var d struct {
		ProductIDs []uuid.UUID `json:"product_ids"`
		BranchID   uuid.UUID   `json:"branch_id"`
	}
	if err := json.Unmarshal([]byte(data), &d); err != nil || len(d.ProductIDs) != 1 || d.ProductIDs[0] != w.f.productID || d.BranchID != w.branch {
		t.Fatalf("event data = %s, want the product and the branch", data)
	}

	// The drain queues the three orders; the worker releases them.
	deadline := time.Now().Add(15 * time.Second)
	for len(w.queued(t)) < 3 {
		if time.Now().After(deadline) {
			t.Fatalf("the drain queued %v, want the three orders", w.queued(t))
		}
		time.Sleep(100 * time.Millisecond)
	}
	w.serveAll(t)
	if got := w.allocated(t, w.ids[0]); got != "CONFIRMED:10.0000/0.0000" {
		t.Errorf("first order = %s, want released", got)
	}
	if got := w.allocated(t, w.ids[1]); got != "BACKORDERED:2.0000/8.0000" {
		t.Errorf("second order = %s, want 2 of 10", got)
	}
}

// receive runs the real purchase order service's receive, wired to the outbox
// and the inventory, as serve wires it.
func receive(t *testing.T, db *database.DB, po, line, yard uuid.UUID, qty float64) {
	t.Helper()
	svc := purchase_order.NewService(purchase_order.NewRepository(db), db, nil,
		inventory.NewService(inventory.NewRepository(db)), product.NewService(product.NewRepository(db)), nil).
		WithOutbox(outbox.NewWriter(db, ""))
	draft := []purchase_order.ReceiveLineDraft{
		{LineID: line, QtyReceived: httpx.Quantity(math.Round(qty * 10000)), LocationID: yard},
	}
	if _, err := svc.ReceivePO(context.Background(), po, "", nil, draft); err != nil {
		t.Fatalf("receive: %v", err)
	}
}
