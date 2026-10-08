// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package order_test

// Allocation, back orders and the stock side of the lifecycle (ADR 0005 5.4),
// on the wire against a real database: a confirm allocates min(available,
// quantity) and puts the rest on back order, a kit allocates in whole kits, a
// cancel and a reopen give the stock back, and a release of a credit hold
// allocates an order that never was.

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gablelbm/gable/internal/inventory"
	"github.com/gablelbm/gable/internal/order"
	"github.com/gablelbm/gable/internal/testutil"
	"github.com/gablelbm/gable/pkg/audit"
	"github.com/gablelbm/gable/pkg/middleware"
	"github.com/gablelbm/gable/pkg/outbox"
	"github.com/google/uuid"
)

// serveWith serves the order routes again with extra wiring on the service.
func (f *fixture) serveWith(mods ...func(*order.Service) *order.Service) *order.Service {
	f.t.Helper()
	svc := order.NewService(order.NewRepository(f.db)).WithOutbox(outbox.NewWriter(f.db, "")).WithTxRunner(f.db).
		WithAuditLog(audit.NewLogger(f.db))
	for _, m := range mods {
		svc = m(svc)
	}
	mux := http.NewServeMux()
	order.NewHandler(svc).RegisterRoutes(mux)
	srv := httptest.NewServer(middleware.Idempotency(f.db)(roleClaims(middleware.NewBranchMiddleware(f.db).Handler(mux))))
	f.t.Cleanup(srv.Close)
	f.srv = srv
	return svc
}

// withStock wires the real inventory service.
func (f *fixture) withStock() func(*order.Service) *order.Service {
	return func(s *order.Service) *order.Service {
		return s.WithInventory(inventory.NewService(inventory.NewRepository(f.db)))
	}
}

// stock puts qty of a product on hand at a yard of the default branch,
// replacing what the fixture left, and removes it with the test.
func (f *fixture) stock(productID uuid.UUID, qty string) {
	f.t.Helper()
	ctx := context.Background()
	yard := uuid.New()
	branch := `(SELECT value::uuid FROM system_settings WHERE key = 'default_branch_id')`
	if _, err := f.db.Pool.Exec(ctx, `INSERT INTO locations (id, type, code, parent_id, branch_id) VALUES ($1, 'YARD', $2, `+branch+`, `+branch+`)`,
		yard, "ST-"+yard.String()[:8]); err != nil {
		f.t.Fatalf("seed yard: %v", err)
	}
	if _, err := f.db.Pool.Exec(ctx, `INSERT INTO inventory (product_id, location_id, location, quantity, allocated) VALUES ($1, $2, 'Y', $3::numeric, 0)`,
		productID, yard, qty); err != nil {
		f.t.Fatalf("seed inventory: %v", err)
	}
	f.t.Cleanup(func() {
		_, _ = f.db.Pool.Exec(ctx, `DELETE FROM inventory WHERE location_id = $1`, yard)
		_, _ = f.db.Pool.Exec(ctx, `DELETE FROM locations WHERE id = $1`, yard)
	})
}

func (f *fixture) lineQuantities(orderID string) []string {
	f.t.Helper()
	rows, err := f.db.Pool.Query(context.Background(),
		`SELECT line_type || ':' || quantity::text || '/' || quantity_allocated::text || '/' || quantity_backordered::text || '/' || quantity_fulfilled::text
		 FROM order_lines WHERE order_id = $1 ORDER BY position`, orderID)
	if err != nil {
		f.t.Fatal(err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var s string
		if err := rows.Scan(&s); err != nil {
			f.t.Fatal(err)
		}
		out = append(out, s)
	}
	return out
}

func (f *fixture) inventoryOf(productID uuid.UUID) string {
	f.t.Helper()
	var s string
	if err := f.db.Pool.QueryRow(context.Background(),
		`SELECT COALESCE(sum(quantity), 0)::text || '/' || COALESCE(sum(allocated), 0)::text FROM inventory WHERE product_id = $1`, productID).Scan(&s); err != nil {
		f.t.Fatal(err)
	}
	return s
}

func (f *fixture) transition(id string, rev int64, to string, extra ...any) resp {
	f.t.Helper()
	body := map[string]any{"to": to, "revision": rev}
	for i := 0; i+1 < len(extra); i += 2 {
		body[extra[i].(string)] = extra[i+1]
	}
	return f.do("POST", "/api/v1/orders/"+id+"/transitions", body)
}

// RULE (ADR 0005 5.4): a confirm allocates each stocked line min(available,
// quantity) from the branch and records the rest as quantity_backordered; the
// whole order no longer fails when one line is short, it lands backordered
// with order.confirmed then order.backordered.
func TestConfirmAllocatesAndBackordersTheShort(t *testing.T) {
	testutil.LockOutboxTables(t)
	db := testutil.RequireDB(t)
	f := newFixture(t, db)
	f.serveWith(f.withStock())
	f.stock(f.productID, "6")

	r := f.create() // 10 PCS
	id := str(t, r.body, "id")
	r = f.transition(id, 1, "confirmed")
	if r.status != 200 || str(t, r.body, "status") != "backordered" {
		t.Fatalf("confirm with 6 of 10 on hand = %d %q: %s", r.status, r.body["status"], r.raw)
	}
	if got := f.lineQuantities(id); fmt.Sprint(got) != "[PRODUCT:10.0000/6.0000/4.0000/0.0000]" {
		t.Errorf("line quantities = %v, want 6 allocated and 4 backordered", got)
	}
	if got := f.inventoryOf(f.productID); got != "6.0000/6.0000" {
		t.Errorf("inventory on hand/allocated = %s, want 6.0000/6.0000", got)
	}
	if ev := eventsFor(t, db, id); fmt.Sprint(ev) != "[order.created order.confirmed order.backordered]" {
		t.Errorf("events = %v, want created, confirmed, backordered", ev)
	}
	lines := r.body["lines"].([]any)
	l := lines[0].(map[string]any)
	if str(t, l, "quantity_allocated") != "6" || str(t, l, "quantity_backordered") != "4" || str(t, l, "quantity_fulfilled") != "0" {
		t.Errorf("wire quantities = %s/%s/%s, want 6/4/0", str(t, l, "quantity_allocated"), str(t, l, "quantity_backordered"), str(t, l, "quantity_fulfilled"))
	}

	// A fully stocked order lands confirmed.
	f.stock(f.productID, "100")
	r2 := f.create()
	r2 = f.transition(str(t, r2.body, "id"), 1, "confirmed")
	if str(t, r2.body, "status") != "confirmed" {
		t.Errorf("a fully stocked confirm = %q, want confirmed", r2.body["status"])
	}
}

// RULE (ADR 0005 5.4): a cancel gives its allocation back and zeroes its back
// orders; so does a reopen to draft, which can then be confirmed again.
func TestCancelAndReopenGiveTheStockBack(t *testing.T) {
	testutil.LockOutboxTables(t)
	db := testutil.RequireDB(t)
	f := newFixture(t, db)
	f.serveWith(f.withStock())
	f.stock(f.productID, "6")

	r := f.create()
	id := str(t, r.body, "id")
	r = f.transition(id, 1, "confirmed")
	if f.inventoryOf(f.productID) != "6.0000/6.0000" {
		t.Fatalf("not allocated: %s", f.inventoryOf(f.productID))
	}
	r = f.transition(id, revision(t, r), "draft")
	if r.status != 200 {
		t.Fatalf("reopen = %d: %s", r.status, r.raw)
	}
	if got := f.inventoryOf(f.productID); got != "6.0000/0.0000" {
		t.Errorf("inventory after a reopen = %s, want the allocation back", got)
	}
	if got := f.lineQuantities(id); fmt.Sprint(got) != "[PRODUCT:10.0000/0.0000/0.0000/0.0000]" {
		t.Errorf("line quantities after reopen = %v, want zeros", got)
	}
	r = f.transition(id, revision(t, r), "confirmed")
	if r.status != 200 || f.inventoryOf(f.productID) != "6.0000/6.0000" {
		t.Fatalf("reconfirm = %d, inventory %s", r.status, f.inventoryOf(f.productID))
	}
	r = f.transition(id, revision(t, r), "cancelled", "reason", "customer left")
	if r.status != 200 {
		t.Fatalf("cancel = %d: %s", r.status, r.raw)
	}
	if got := f.inventoryOf(f.productID); got != "6.0000/0.0000" {
		t.Errorf("inventory after a cancel = %s, want the allocation back", got)
	}
}

// RULE (ADR 0005 5.4 and 2.6): a kit's components allocate in whole kits. A
// fence kit of 4 posts, 2 kits ordered, 6 posts on hand: one whole kit (4
// posts) allocates, the second kit's 4 posts are all backordered.
func TestKitAllocatesInWholeKits(t *testing.T) {
	testutil.LockOutboxTables(t)
	db := testutil.RequireDB(t)
	f := newFixture(t, db)
	ctx := context.Background()
	f.serveWith(f.withStock())
	kit, post := uuid.New(), uuid.New()
	if _, err := db.Pool.Exec(ctx, `INSERT INTO products (id, sku, description, uom_primary, base_price, is_kit, taxable)
		VALUES ($1, $2, 'A fence kit', 'EA', 100.00, TRUE, TRUE)`, kit, "KAL-KIT-"+uuid.NewString()[:6]); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Pool.Exec(ctx, `INSERT INTO products (id, sku, description, uom_primary, base_price) VALUES ($1, $2, 'A fence post', 'EA', 12.50)`,
		post, "KAL-POST-"+uuid.NewString()[:6]); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Pool.Exec(ctx, `INSERT INTO product_kit_components (kit_product_id, component_product_id, quantity, position) VALUES ($1, $2, 4, 0)`, kit, post); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = db.Pool.Exec(ctx, `DELETE FROM product_kit_components WHERE kit_product_id = $1`, kit)
		_, _ = db.Pool.Exec(ctx, `DELETE FROM inventory WHERE product_id = $1`, post)
		_, _ = db.Pool.Exec(ctx, `DELETE FROM order_lines WHERE product_id IN ($1, $2)`, kit, post)
		_, _ = db.Pool.Exec(ctx, `DELETE FROM products WHERE id IN ($1, $2)`, kit, post)
	})
	f.stock(post, "6")

	r := f.do("POST", "/api/v1/orders", f.createBody(map[string]any{"product_id": kit.String(), "quantity": "2"}))
	if r.status != 201 {
		t.Fatalf("create = %d: %s", r.status, r.raw)
	}
	id := str(t, r.body, "id")
	r = f.transition(id, 1, "confirmed")
	if r.status != 200 || str(t, r.body, "status") != "backordered" {
		t.Fatalf("confirm = %d %q: %s", r.status, r.body["status"], r.raw)
	}
	// The kit line carries fulfilled only; the component holds one whole kit.
	if got := f.lineQuantities(id); fmt.Sprint(got) != "[KIT:2.0000/0.0000/0.0000/0.0000 COMPONENT:8.0000/4.0000/4.0000/0.0000]" {
		t.Errorf("line quantities = %v, want one whole kit (4 posts) allocated and 4 backordered", got)
	}
	if got := f.inventoryOf(post); got != "6.0000/4.0000" {
		t.Errorf("post inventory = %s, want 6.0000/4.0000 (2 posts left unallocated, not a partial kit)", got)
	}
}

// RULE (ADR 0005 5.2 and 5.4): the release of a credit hold allocates an order
// that was never allocated, derives its status, and keeps 5.2's events: the
// hold_released, order.confirmed (first confirm) and order.backordered when it
// lands backordered.
func TestReleaseOfACreditHoldAllocates(t *testing.T) {
	testutil.LockOutboxTables(t)
	db := testutil.RequireDB(t)
	f := newFixture(t, db)
	f.serveWith(f.withStock())
	f.stock(f.productID, "6")
	if _, err := db.Pool.Exec(context.Background(), `UPDATE customers SET credit_limit = 10.00 WHERE id = $1`, f.customerID); err != nil {
		t.Fatal(err)
	}
	r := f.create()
	id := str(t, r.body, "id")
	r = f.transition(id, 1, "confirmed")
	if str(t, r.body, "status") != "on_hold" || f.inventoryOf(f.productID) != "6.0000/0.0000" {
		t.Fatalf("credit hold = %q, inventory %s: a held order allocates nothing", r.body["status"], f.inventoryOf(f.productID))
	}
	r = f.transition(id, revision(t, r), "confirmed")
	if r.status != 200 || str(t, r.body, "status") != "backordered" {
		t.Fatalf("release = %d %q: %s", r.status, r.body["status"], r.raw)
	}
	if got := f.lineQuantities(id); fmt.Sprint(got) != "[PRODUCT:10.0000/6.0000/4.0000/0.0000]" {
		t.Errorf("line quantities after the release = %v", got)
	}
	want := "[order.created order.hold<draft order.hold_released<on_hold order.confirmed<on_hold order.backordered<on_hold]"
	if got := fmt.Sprint(eventsWithFrom(t, db, id)); got != want {
		t.Errorf("events = %s, want %s", got, want)
	}
}
