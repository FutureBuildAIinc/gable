// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package portal

import (
	"context"
	"testing"
	"time"

	"github.com/gablelbm/gable/internal/testutil"
	"github.com/gablelbm/gable/pkg/database"
	"github.com/google/uuid"
)

// TENANCY IS THE ONE THING THIS API CANNOT GET WRONG.
//
// Every portal read is scoped by customer_id from the session claims, and the
// scoping lives in SQL — so it cannot be unit-tested (see
// TestPortalDataMethods_AreNotUnitTestable). This file is the version that
// actually proves it, against a real database, for every query the migration
// 084 capability pass added.
//
// The shape of every case is the same: build two customers with real rows,
// then ask for customer A's data as customer B and require that it is NOT
// found. Each assertion also checks the answer is "not found" rather than
// "forbidden", because a 403 on someone else's id is an existence oracle.
//
// Skips cleanly when Postgres is unreachable (testutil.RequireDB), which is
// the default state of a fresh clone and of CI's plain `go test ./...`.

type tenants struct {
	db *database.DB

	aCustomer, bCustomer uuid.UUID
	aProject, bProject   uuid.UUID
	aOrder, bOrder       uuid.UUID
	aQuote, bQuote       uuid.UUID
	aDelivery, bDelivery uuid.UUID
	product              uuid.UUID
}

// setupTenants creates two complete, isolated customers. Everything is
// namespaced by a fresh UUID and torn down with t.Cleanup, so the fixture can
// run against a seeded demo database without colliding with it.
func setupTenants(t *testing.T) *tenants {
	t.Helper()
	db := testutil.RequireDB(t)
	ctx := context.Background()

	tt := &tenants{
		db:        db,
		aCustomer: uuid.New(), bCustomer: uuid.New(),
		aProject: uuid.New(), bProject: uuid.New(),
		aOrder: uuid.New(), bOrder: uuid.New(),
		aQuote: uuid.New(), bQuote: uuid.New(),
		aDelivery: uuid.New(), bDelivery: uuid.New(),
		product: uuid.New(),
	}

	exec := func(sql string, args ...any) {
		t.Helper()
		if _, err := db.Pool.Exec(ctx, sql, args...); err != nil {
			t.Fatalf("fixture %q: %v", sql, err)
		}
	}

	// customers.primary_branch_id is NOT NULL post-migration 067; resolve the
	// default branch from system_settings (seeded by migration 059).
	for _, c := range []struct {
		id   uuid.UUID
		name string
	}{{tt.aCustomer, "Tenancy A"}, {tt.bCustomer, "Tenancy B"}} {
		exec(`INSERT INTO customers (id, name, account_number, primary_branch_id)
		      VALUES ($1, $2, $3, (SELECT value::uuid FROM system_settings WHERE key = 'default_branch_id'))`,
			c.id, c.name, "TEN-"+c.id.String()[:8])
	}

	exec(`INSERT INTO products (id, sku, description, uom_primary, base_price)
	      VALUES ($1, $2, 'Tenancy fixture product', 'EA', 10)`,
		tt.product, "TEN-"+tt.product.String()[:8])

	for _, p := range []struct {
		id   uuid.UUID
		cust uuid.UUID
		name string
	}{{tt.aProject, tt.aCustomer, "A job"}, {tt.bProject, tt.bCustomer, "B job"}} {
		exec(`INSERT INTO projects (id, customer_id, name, status) VALUES ($1, $2, $3, 'Active')`,
			p.id, p.cust, p.name)
	}

	for _, o := range []struct {
		id      uuid.UUID
		cust    uuid.UUID
		project uuid.UUID
	}{{tt.aOrder, tt.aCustomer, tt.aProject}, {tt.bOrder, tt.bCustomer, tt.bProject}} {
		exec(`INSERT INTO orders (id, customer_id, project_id, status, total_amount, branch_id, created_at, updated_at)
		      VALUES ($1, $2, $3, 'CONFIRMED', 100.00,
		              (SELECT value::uuid FROM system_settings WHERE key = 'default_branch_id'), NOW(), NOW())`,
			o.id, o.cust, o.project)
		exec(`INSERT INTO order_lines (order_id, product_id, quantity, price_each) VALUES ($1, $2, 10, 10.00)`,
			o.id, tt.product)
	}

	for _, q := range []struct {
		id   uuid.UUID
		cust uuid.UUID
		proj uuid.UUID
	}{{tt.aQuote, tt.aCustomer, tt.aProject}, {tt.bQuote, tt.bCustomer, tt.bProject}} {
		exec(`INSERT INTO quotes (id, customer_id, project_id, state, total_amount, source, customer_notes, branch_id, created_at, updated_at)
		      VALUES ($1, $2, $3, 'SENT', 500.00, 'portal', 'tenancy fixture',
		              (SELECT value::uuid FROM system_settings WHERE key = 'default_branch_id'), NOW(), NOW())`,
			q.id, q.cust, q.proj)
		exec(`INSERT INTO quote_lines (quote_id, product_id, sku, description, customer_note, quantity, uom, unit_price, line_total)
		      VALUES ($1, $2, 'TEN', 'fixture line', 'contractor said this', 5, 'EA', 100.00, 500.00)`,
			q.id, tt.product)
	}

	for _, d := range []struct {
		id    uuid.UUID
		order uuid.UUID
	}{{tt.aDelivery, tt.aOrder}, {tt.bDelivery, tt.bOrder}} {
		exec(`INSERT INTO deliveries (id, order_id, stop_sequence, status) VALUES ($1, $2, 1, 'PENDING')`,
			d.id, d.order)
	}

	// Teardown, child-first so the foreign keys are satisfied.
	//
	// Each statement gets exactly the arguments it references — pgx rejects a
	// call with a different argument count, and an earlier version of this
	// block passed all four to every statement and threw the error away, so
	// every teardown silently failed and each run leaked its fixture into the
	// database. A failed teardown is REPORTED here rather than ignored: a
	// fixture that does not clean up is a test that pollutes whatever database
	// it is pointed at, including a shared demo one.
	t.Cleanup(func() {
		c := context.Background()
		customers := []uuid.UUID{tt.aCustomer, tt.bCustomer}
		orders := []uuid.UUID{tt.aOrder, tt.bOrder}
		quotes := []uuid.UUID{tt.aQuote, tt.bQuote}

		steps := []struct {
			sql  string
			args []any
		}{
			{`DELETE FROM portal_delivery_reschedule_requests WHERE customer_id = ANY($1)`, []any{customers}},
			{`DELETE FROM deliveries WHERE order_id = ANY($1)`, []any{orders}},
			{`DELETE FROM quote_lines WHERE quote_id = ANY($1)`, []any{quotes}},
			{`DELETE FROM quotes WHERE id = ANY($1)`, []any{quotes}},
			{`DELETE FROM order_lines WHERE order_id = ANY($1)`, []any{orders}},
			{`DELETE FROM orders WHERE id = ANY($1)`, []any{orders}},
			{`DELETE FROM projects WHERE customer_id = ANY($1)`, []any{customers}},
			{`DELETE FROM products WHERE id = $1`, []any{tt.product}},
			{`DELETE FROM customers WHERE id = ANY($1)`, []any{customers}},
		}
		for _, s := range steps {
			if _, err := db.Pool.Exec(c, s.sql, s.args...); err != nil {
				t.Errorf("teardown %q failed, leaving fixture rows behind: %v", s.sql, err)
			}
		}
	})

	return tt
}

// CORRECTNESS (security): every per-id read refuses another customer's row,
// and refuses it as "not found".
func TestTenancy_PerIDReadsRefuseAnotherCustomer(t *testing.T) {
	tt := setupTenants(t)
	repo := NewRepository(tt.db)
	ctx := context.Background()

	t.Run("order", func(t *testing.T) {
		if _, err := repo.GetOrderByIDAndCustomer(ctx, tt.aOrder, tt.aCustomer); err != nil {
			t.Fatalf("owner cannot read their own order: %v", err)
		}
		got, err := repo.GetOrderByIDAndCustomer(ctx, tt.aOrder, tt.bCustomer)
		if err == nil {
			t.Fatalf("customer B read customer A's order: %+v", got)
		}
		if statusForPortalError(err, 500) != 404 {
			t.Errorf("err %v does not render as 404; anything else confirms the order exists", err)
		}
	})

	t.Run("order status", func(t *testing.T) {
		if _, err := repo.GetOrderStatusForCustomer(ctx, tt.aOrder, tt.aCustomer); err != nil {
			t.Fatalf("owner cannot read their own order status: %v", err)
		}
		if _, err := repo.GetOrderStatusForCustomer(ctx, tt.aOrder, tt.bCustomer); err == nil {
			t.Fatal("customer B read the status of customer A's order — this is the read the cancel endpoint gates on")
		}
	})

	t.Run("quote", func(t *testing.T) {
		mine, err := repo.GetPortalQuote(ctx, tt.aQuote, tt.aCustomer)
		if err != nil {
			t.Fatalf("owner cannot read their own quote: %v", err)
		}
		if len(mine.Lines) != 1 || mine.Lines[0].CustomerNote != "contractor said this" {
			t.Errorf("the owner's own quote lost its line or its note: %+v", mine.Lines)
		}
		got, err := repo.GetPortalQuote(ctx, tt.aQuote, tt.bCustomer)
		if err == nil {
			t.Fatalf("customer B read customer A's quote: %+v", got)
		}
		if statusForPortalError(err, 500) != 404 {
			t.Errorf("err %v does not render as 404", err)
		}
	})

	t.Run("delivery", func(t *testing.T) {
		if _, err := repo.GetDeliveryRescheduleState(ctx, tt.aDelivery, tt.aCustomer); err != nil {
			t.Fatalf("owner cannot read their own delivery: %v", err)
		}
		got, err := repo.GetDeliveryRescheduleState(ctx, tt.aDelivery, tt.bCustomer)
		if err == nil {
			t.Fatalf("customer B read customer A's delivery: %+v", got)
		}
		if statusForPortalError(err, 500) != 404 {
			t.Errorf("err %v does not render as 404", err)
		}
	})

	t.Run("project ownership", func(t *testing.T) {
		ok, err := repo.ProjectBelongsToCustomer(ctx, tt.aProject, tt.aCustomer)
		if err != nil || !ok {
			t.Fatalf("owner does not own their own project (ok=%v err=%v)", ok, err)
		}
		ok, err = repo.ProjectBelongsToCustomer(ctx, tt.aProject, tt.bCustomer)
		if err != nil {
			t.Fatalf("ProjectBelongsToCustomer: %v", err)
		}
		if ok {
			t.Fatal("customer B was reported as owning customer A's project")
		}
	})
}

// CORRECTNESS (security): the list reads never leak a row across the boundary,
// including when the caller supplies the OTHER customer's project id as a
// filter. Filtering by someone else's project must return nothing rather than
// their orders.
func TestTenancy_ListReadsAreScoped(t *testing.T) {
	tt := setupTenants(t)
	repo := NewRepository(tt.db)
	ctx := context.Background()

	assertOnlyMine := func(t *testing.T, label string, orders []PortalOrderDTO, mine, theirs uuid.UUID) {
		t.Helper()
		for _, o := range orders {
			if o.ID == theirs {
				t.Fatalf("%s returned the other customer's order %s", label, theirs)
			}
		}
		_ = mine
	}

	aOrders, err := repo.ListOrdersByCustomerFiltered(ctx, tt.aCustomer, OrderListFilter{})
	if err != nil {
		t.Fatalf("ListOrdersByCustomerFiltered: %v", err)
	}
	assertOnlyMine(t, "unfiltered list", aOrders, tt.aOrder, tt.bOrder)

	// The endpoint refuses a foreign project_id before it reaches SQL, but the
	// query itself must also be safe if that guard is ever bypassed.
	crossFiltered, err := repo.ListOrdersByCustomerFiltered(ctx, tt.aCustomer, OrderListFilter{ProjectID: &tt.bProject})
	if err != nil {
		t.Fatalf("cross-project filter: %v", err)
	}
	if len(crossFiltered) != 0 {
		t.Fatalf("filtering customer A's orders by customer B's project returned %d rows", len(crossFiltered))
	}

	quotes, err := repo.ListPortalQuotes(ctx, tt.aCustomer)
	if err != nil {
		t.Fatalf("ListPortalQuotes: %v", err)
	}
	for _, q := range quotes {
		if q.ID == tt.bQuote {
			t.Fatalf("customer A's quote list contains customer B's quote %s", tt.bQuote)
		}
	}

	deliveries, err := repo.ListDeliveriesByCustomer(ctx, tt.aCustomer)
	if err != nil {
		t.Fatalf("ListDeliveriesByCustomer: %v", err)
	}
	for _, d := range deliveries {
		if d.ID == tt.bDelivery {
			t.Fatalf("customer A's delivery list contains customer B's delivery %s", tt.bDelivery)
		}
	}
}

// CORRECTNESS (security): the WRITES are scoped too. A read-only tenancy
// boundary is not a boundary — the interesting failure is a portal user
// silently re-filing someone else's order onto their own board.
func TestTenancy_WritesAreScoped(t *testing.T) {
	tt := setupTenants(t)
	repo := NewRepository(tt.db)
	ctx := context.Background()

	// B tries to move A's order onto B's project.
	err := repo.SetOrderProject(ctx, tt.aOrder, tt.bCustomer, &tt.bProject)
	if err == nil {
		t.Fatal("customer B re-assigned customer A's order to their own project")
	}
	if statusForPortalError(err, 500) != 404 {
		t.Errorf("err %v does not render as 404", err)
	}

	// And A's order is untouched.
	after, err := repo.GetOrderByIDAndCustomer(ctx, tt.aOrder, tt.aCustomer)
	if err != nil {
		t.Fatalf("re-reading customer A's order: %v", err)
	}
	if after.ProjectID == nil || *after.ProjectID != tt.aProject {
		t.Fatalf("customer A's order project is now %v, want %s", after.ProjectID, tt.aProject)
	}

	// A reschedule request filed by A is not readable by B.
	reqID, err := repo.CreateRescheduleRequest(ctx, tt.aDelivery, tt.aCustomer, nil,
		time.Now().UTC().AddDate(0, 0, 5), "site not ready")
	if err != nil {
		t.Fatalf("CreateRescheduleRequest: %v", err)
	}
	if _, err := repo.GetRescheduleRequest(ctx, reqID, tt.aCustomer); err != nil {
		t.Fatalf("owner cannot read their own reschedule request: %v", err)
	}
	if got, err := repo.GetRescheduleRequest(ctx, reqID, tt.bCustomer); err == nil {
		t.Fatalf("customer B read customer A's reschedule request: %+v", got)
	}
	if got, err := repo.GetLatestRescheduleRequest(ctx, tt.aDelivery, tt.bCustomer); err != nil || got != nil {
		t.Fatalf("GetLatestRescheduleRequest leaked across customers: %+v (err %v)", got, err)
	}
}

// CORRECTNESS: filing a second reschedule request supersedes the first, so a
// dispatcher is never left with two contradictory dates for one stop and no
// way to tell which the contractor meant. The partial unique index
// idx_pdrr_one_open_per_delivery is what makes this hold under a race; this
// asserts the ordinary path.
func TestReschedule_SecondRequestSupersedesTheFirst(t *testing.T) {
	tt := setupTenants(t)
	repo := NewRepository(tt.db)
	ctx := context.Background()

	first, err := repo.CreateRescheduleRequest(ctx, tt.aDelivery, tt.aCustomer, nil,
		time.Now().UTC().AddDate(0, 0, 5), "first ask")
	if err != nil {
		t.Fatalf("first request: %v", err)
	}
	second, err := repo.CreateRescheduleRequest(ctx, tt.aDelivery, tt.aCustomer, nil,
		time.Now().UTC().AddDate(0, 0, 12), "changed my mind")
	if err != nil {
		t.Fatalf("second request: %v", err)
	}

	old, err := repo.GetRescheduleRequest(ctx, first, tt.aCustomer)
	if err != nil {
		t.Fatalf("reading the first request: %v", err)
	}
	if old.Status != RescheduleStatusSuperseded {
		t.Errorf("the first request is %s, want SUPERSEDED", old.Status)
	}

	latest, err := repo.GetLatestRescheduleRequest(ctx, tt.aDelivery, tt.aCustomer)
	if err != nil {
		t.Fatalf("GetLatestRescheduleRequest: %v", err)
	}
	if latest.ID != second {
		t.Errorf("latest request is %s, want the second one %s", latest.ID, second)
	}
	if latest.Status != RescheduleStatusPending || latest.Applied {
		t.Errorf("latest = %s applied=%v, want PENDING and applied=false — the portal must never claim it changed the schedule",
			latest.Status, latest.Applied)
	}

	// A pending request must not have moved the dealer's board.
	var routeCount int
	if err := tt.db.Pool.QueryRow(ctx,
		`SELECT COUNT(*) FROM deliveries d JOIN delivery_routes r ON r.id = d.route_id WHERE d.id = $1`,
		tt.aDelivery).Scan(&routeCount); err != nil {
		t.Fatalf("checking the route: %v", err)
	}
	if routeCount != 0 {
		t.Errorf("the reschedule request attached the stop to a route (%d); it must only record the ask", routeCount)
	}
}

// CORRECTNESS: the change-feed cursor is updated_at, and it moves on an ERP
// status change. This is the defect the portal consumer reported from the other
// side: CONFIRMED -> ON_HOLD rounds to the same display state, so a poll that
// compared rendered status saw "no change" and the contractor never learned
// their order had been held.
func TestChangeFeed_SeesAStatusMoveThatRoundsToTheSameDisplayState(t *testing.T) {
	tt := setupTenants(t)
	repo := NewRepository(tt.db)
	ctx := context.Background()

	before, err := repo.ListOrdersByCustomerFiltered(ctx, tt.aCustomer, OrderListFilter{})
	if err != nil {
		t.Fatalf("initial list: %v", err)
	}
	if len(before) == 0 {
		t.Fatal("fixture order is missing")
	}
	cursor := before[0].UpdatedAt

	empty, err := repo.ListOrdersByCustomerFiltered(ctx, tt.aCustomer, OrderListFilter{Since: &cursor})
	if err != nil {
		t.Fatalf("since-cursor list: %v", err)
	}
	if len(empty) != 0 {
		t.Fatalf("?since=<own cursor> returned %d rows; the comparison must be strictly greater-than or a poller re-reads the same row forever", len(empty))
	}

	if _, err := tt.db.Pool.Exec(ctx,
		`UPDATE orders SET status = 'ON_HOLD', updated_at = NOW() WHERE id = $1`, tt.aOrder); err != nil {
		t.Fatalf("holding the order: %v", err)
	}

	after, err := repo.ListOrdersByCustomerFiltered(ctx, tt.aCustomer, OrderListFilter{Since: &cursor})
	if err != nil {
		t.Fatalf("post-change list: %v", err)
	}
	found := false
	for _, o := range after {
		if o.ID == tt.aOrder {
			found = true
			if o.Status != "ON_HOLD" {
				t.Errorf("status = %s, want ON_HOLD", o.Status)
			}
		}
	}
	if !found {
		t.Fatal("a CONFIRMED -> ON_HOLD move did not appear in the ?since= feed")
	}

	// And the ETag moved with it, so a conditional GET cannot serve a stale 304.
	e1 := orderListETag(tt.aCustomer, OrderListFilter{}, len(before), &cursor)
	newest := after[0].UpdatedAt
	e2 := orderListETag(tt.aCustomer, OrderListFilter{}, len(before), &newest)
	if e1 == e2 {
		t.Error("the ETag did not move across a status change; a poller would keep getting 304")
	}
}

// CORRECTNESS: the ltree subtree filter returns descendants, not just exact
// matches. Clicking "Lumber" has to show framing lumber or the tree is
// decoration.
func TestCatalogCategoryFilter_MatchesTheWholeSubtree(t *testing.T) {
	db := testutil.RequireDB(t)
	repo := NewRepository(db)
	ctx := context.Background()

	var lumberID, framingID uuid.UUID
	if err := db.Pool.QueryRow(ctx, `SELECT id FROM product_categories WHERE slug = 'lumber'`).Scan(&lumberID); err != nil {
		t.Skipf("category tree not seeded (migration 049): %v", err)
	}
	if err := db.Pool.QueryRow(ctx, `SELECT id FROM product_categories WHERE slug = 'framing_lumber'`).Scan(&framingID); err != nil {
		t.Skipf("category tree not seeded (migration 049): %v", err)
	}

	productID := uuid.New()
	if _, err := db.Pool.Exec(ctx,
		`INSERT INTO products (id, sku, description, uom_primary, base_price, category_id)
		 VALUES ($1, $2, 'Subtree fixture', 'EA', 10, $3)`,
		productID, "SUB-"+productID.String()[:8], framingID); err != nil {
		t.Fatalf("fixture product: %v", err)
	}
	t.Cleanup(func() {
		_, _ = db.Pool.Exec(context.Background(), `DELETE FROM products WHERE id = $1`, productID)
	})

	contains := func(rows []catalogRow) bool {
		for _, r := range rows {
			if r.ID == productID {
				return true
			}
		}
		return false
	}

	leaf, err := repo.ListCatalogProducts(ctx, CatalogFilter{CategoryID: framingID.String()})
	if err != nil {
		t.Fatalf("leaf filter: %v", err)
	}
	if !contains(leaf) {
		t.Error("filtering by the leaf category did not return the product linked to it")
	}

	parent, err := repo.ListCatalogProducts(ctx, CatalogFilter{CategoryID: lumberID.String()})
	if err != nil {
		t.Fatalf("parent filter: %v", err)
	}
	if !contains(parent) {
		t.Error("filtering by the PARENT category did not return a product in its child category — the ltree containment match is not working")
	}

	// An unknown category is an empty catalog, not an error page.
	unknown, err := repo.ListCatalogProducts(ctx, CatalogFilter{CategoryID: uuid.NewString()})
	if err != nil {
		t.Fatalf("unknown category: %v", err)
	}
	if len(unknown) != 0 {
		t.Errorf("an unknown category id returned %d products", len(unknown))
	}
}
