// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package portal

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gablelbm/gable/internal/customer"
	"github.com/gablelbm/gable/internal/order"
	"github.com/gablelbm/gable/internal/product"
	"github.com/gablelbm/gable/internal/testutil"
	"github.com/gablelbm/gable/pkg/database"
	"github.com/gablelbm/gable/pkg/middleware"
	"github.com/google/uuid"
)

// The pro portal's core on the wire (M35 Parts 1, 4 and 7): the frame's /me
// and its capability resolution, the Today board, scheduling with its
// capacity race and cutoff, the order timeline and site instructions, and
// the change feeds. Every case runs through the real handler stack with
// claims injected by the same middleware shape serve mounts, against a real
// database, and skips cleanly when Postgres is unreachable.

// proFixture is two tenants with yards, users in every role, documents and
// scheduling windows.
type proFixture struct {
	db                                    *database.DB
	aCustomer, bCustomer                  uuid.UUID
	aYard, bYard                          uuid.UUID
	users                                 map[string]uuid.UUID // role -> user id (customer A)
	aDraft, aConfirmed, aFulfilled, bOrder uuid.UUID
	aInvoice, aQuote, aDelivery           uuid.UUID
	product                               uuid.UUID
	winCap1, winCutoff, winPickup         uuid.UUID
	winDate                               string // a date >= 2 days out the windows run on
}

// muxFor builds the handler stack over the repository for one caller.
func muxFor(repo *PostgresRepository, customerID, userID uuid.UUID, role string) *http.ServeMux {
	customerSvc := customer.NewService(customer.NewRepository(repo.db))
	orderSvc := order.NewService(order.NewRepository(repo.db)).WithTxRunner(repo.db)
	productSvc := product.NewService(product.NewRepository(repo.db))
	svc := NewService(repo, testSecret, testLogger(), nil, customerSvc, nil, orderSvc, productSvc)
	h := NewHandler(svc)
	mux := http.NewServeMux()
	authMw := func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			claims := &middleware.PortalClaims{
				CustomerID:     customerID,
				CustomerUserID: userID,
				Email:          "pro@example.test",
				Name:           "Pro Fixture",
				Role:           role,
			}
			ctx := context.WithValue(r.Context(), middleware.PortalClaimsKey, claims)
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
	h.RegisterRoutes(mux, authMw)
	return mux
}

func call(mux *http.ServeMux, method, path, body string, hdr map[string]string) (*httptest.ResponseRecorder, map[string]any) {
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	var out map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	return rec, out
}

// setupPro builds the fixture. Namespaced by fresh UUIDs and torn down with
// t.Cleanup, so it can run against a seeded demo database.
func setupPro(t *testing.T) *proFixture {
	t.Helper()
	db := testutil.RequireDB(t)
	ctx := context.Background()

	f := &proFixture{
		db:         db,
		aCustomer:  uuid.New(), bCustomer: uuid.New(),
		aYard:      uuid.New(), bYard: uuid.New(),
		aDraft:     uuid.New(), aConfirmed: uuid.New(), aFulfilled: uuid.New(), bOrder: uuid.New(),
		aInvoice:   uuid.New(), aQuote: uuid.New(), aDelivery: uuid.New(),
		product:    uuid.New(),
	}

	exec := func(sql string, args ...any) {
		t.Helper()
		if _, err := db.Pool.Exec(ctx, sql, args...); err != nil {
			t.Fatalf("fixture %q: %v", sql, err)
		}
	}

	var defBranch uuid.UUID
	if err := db.Pool.QueryRow(ctx,
		`SELECT value::uuid FROM system_settings WHERE key = 'default_branch_id'`).Scan(&defBranch); err != nil {
		t.Fatalf("default branch: %v", err)
	}

	exec(`INSERT INTO products (id, sku, description, uom_primary, base_price)
		VALUES ($1, $2, 'Pro fixture board', 'EA', 25.00)`, f.product, "PRO-"+f.product.String()[:8])

	exec(`INSERT INTO locations (id, type, code, name, timezone, default_tax_rate) VALUES
		($1, 'BRANCH', 'PROA', 'Pro Yard A', 'America/Vancouver', 0.0825),
		($2, 'BRANCH', 'PROB', 'Pro Yard B', 'America/Vancouver', 0.0825)`, f.aYard, f.bYard)
	for _, c := range []struct {
		id, yard uuid.UUID
		name     string
	}{{f.aCustomer, f.aYard, "Pro A"}, {f.bCustomer, f.bYard, "Pro B"}} {
		exec(`INSERT INTO customers (id, name, account_number, primary_branch_id)
			VALUES ($1, $2, $3, $4)`, c.id, c.name, "PRO-"+c.id.String()[:8], defBranch)
		exec(`INSERT INTO customer_branches (customer_id, branch_id) VALUES ($1, $2)`, c.id, c.yard)
	}

	// One user per role on account A.
	f.users = map[string]uuid.UUID{}
	for _, role := range PortalRoles() {
		id := uuid.New()
		f.users[role] = id
		exec(`INSERT INTO customer_users (id, customer_id, email, password_hash, name, role, status)
			VALUES ($1, $2, $3, 'x', $4, $5, 'Active')`,
			id, f.aCustomer, "pro-"+role+"-"+id.String()[:8]+"@example.test", role, role)
	}

	mkOrder := func(id, cust, yard uuid.UUID, status, deliveryType string) {
		exec(`INSERT INTO orders (id, customer_id, status, total_amount, branch_id, created_at, updated_at, delivery_type, currency)
			VALUES ($1, $2, $3, 123.45, $4, NOW(), NOW(), $5, 'USD')`, id, cust, status, yard, deliveryType)
	}
	mkOrder(f.aDraft, f.aCustomer, f.aYard, "DRAFT", "PICKUP")
	mkOrder(f.aConfirmed, f.aCustomer, f.aYard, "CONFIRMED", "DELIVERY")
	mkOrder(f.aFulfilled, f.aCustomer, f.aYard, "FULFILLED", "DELIVERY")
	mkOrder(f.bOrder, f.bCustomer, f.bYard, "CONFIRMED", "PICKUP")

	// A delivered stop with POD on the fulfilled order, and a stop scheduled
	// today on the confirmed order (for the Today board).
	routeDone, routeToday := uuid.New(), uuid.New()
	exec(`INSERT INTO delivery_routes (id, status, scheduled_date) VALUES ($1, 'COMPLETED', NOW() - interval '2 days')`, routeDone)
	exec(`INSERT INTO deliveries (id, order_id, route_id, status, pod_signed_by, pod_proof_url, pod_timestamp, created_at, updated_at, stop_sequence)
		VALUES ($1, $2, $3, 'DELIVERED', 'Site Contact', 'https://pod.example.test/p.jpg', NOW() - interval '2 days', NOW() - interval '3 days', NOW() - interval '2 days', 1)`,
		f.aDelivery, f.aFulfilled, routeDone)
	exec(`INSERT INTO delivery_routes (id, status, scheduled_date) VALUES ($1, 'SCHEDULED', CURRENT_DATE + interval '5 hours')`, routeToday)
	exec(`INSERT INTO deliveries (id, order_id, route_id, status, created_at, updated_at)
		VALUES ($1, $2, $3, 'PENDING', NOW(), NOW())`, uuid.New(), f.aConfirmed, routeToday)

	exec(`INSERT INTO invoices (id, customer_id, order_id, status, total_amount, subtotal, tax_amount, due_date, created_at, updated_at, branch_id)
		VALUES ($1, $2, $3, 'UNPAID', 100.00, 90.00, 10.00, CURRENT_DATE - interval '3 days', NOW(), NOW(), $4)`,
		f.aInvoice, f.aCustomer, f.aFulfilled, f.aYard)
	exec(`INSERT INTO quotes (id, customer_id, state, total_amount, source, branch_id, sent_at, created_at, updated_at)
		VALUES ($1, $2, 'SENT', 500.00, 'portal', $3, NOW(), NOW(), NOW())`, f.aQuote, f.aCustomer, f.aYard)

	// A date at least two days out; the windows below run on its weekday.
	far := time.Now().UTC().AddDate(0, 0, 3)
	f.winDate = far.Format("2006-01-02")
	wd := int(far.Weekday())
	f.winCap1 = uuid.New()
	exec(`INSERT INTO yard_schedule_windows (id, yard_id, kind, weekday, start_time, end_time, capacity, cutoff_minutes)
		VALUES ($1, $2, 'delivery', $3, '08:00', '12:00', 1, 0)`, f.winCap1, f.aYard, wd)
	f.winCutoff = uuid.New()
	exec(`INSERT INTO yard_schedule_windows (id, yard_id, kind, weekday, start_time, end_time, capacity, cutoff_minutes)
		VALUES ($1, $2, 'delivery', $3, '08:00', '12:00', 5, 1000000)`, f.winCutoff, f.aYard, wd)
	f.winPickup = uuid.New()
	exec(`INSERT INTO yard_schedule_windows (id, yard_id, kind, weekday, start_time, end_time, capacity, cutoff_minutes)
		VALUES ($1, $2, 'pickup', $3, '07:00', '10:00', 3, 0)`, f.winPickup, f.aYard, wd)

	both := []uuid.UUID{f.aCustomer, f.bCustomer}
	bothYards := []uuid.UUID{f.aYard, f.bYard}
	t.Cleanup(func() {
		c := context.Background()
		for _, stmt := range []func() (string, any){
			func() (string, any) { return `DELETE FROM portal_payment_intents WHERE customer_id = ANY($1)`, both },
			func() (string, any) { return `DELETE FROM portal_payment_methods WHERE customer_id = ANY($1)`, both },
			func() (string, any) { return `DELETE FROM procurement_projects WHERE customer_id = ANY($1)`, both },
			func() (string, any) { return `DELETE FROM ar_applications WHERE customer_id = ANY($1)`, both },
			func() (string, any) { return `DELETE FROM customer_transactions WHERE customer_id = ANY($1)`, both },
			func() (string, any) { return `DELETE FROM payments WHERE customer_id = ANY($1)`, both },
			func() (string, any) { return `DELETE FROM portal_schedule_holds WHERE customer_id = ANY($1)`, both },
			func() (string, any) { return `DELETE FROM yard_schedule_windows WHERE yard_id = ANY($1)`, bothYards },
			func() (string, any) { return `DELETE FROM deliveries WHERE order_id IN (SELECT id FROM orders WHERE customer_id = ANY($1))`, both },
			func() (string, any) { return `DELETE FROM delivery_routes WHERE id = ANY($1)`, []uuid.UUID{routeDone, routeToday} },
			func() (string, any) { return `DELETE FROM invoices WHERE customer_id = ANY($1)`, both },
			func() (string, any) { return `DELETE FROM quote_lines WHERE quote_id IN (SELECT id FROM quotes WHERE customer_id = ANY($1))`, both },
			func() (string, any) { return `DELETE FROM quotes WHERE customer_id = ANY($1)`, both },
			func() (string, any) { return `DELETE FROM portal_cart_items WHERE cart_id IN (SELECT id FROM portal_carts WHERE customer_id = ANY($1))`, both },
			func() (string, any) { return `DELETE FROM portal_carts WHERE customer_id = ANY($1)`, both },
			func() (string, any) { return `DELETE FROM order_lines WHERE order_id IN (SELECT id FROM orders WHERE customer_id = ANY($1))`, both },
			func() (string, any) { return `DELETE FROM orders WHERE customer_id = ANY($1)`, both },
			func() (string, any) { return `DELETE FROM projects WHERE customer_id = ANY($1)`, both },
			func() (string, any) { return `DELETE FROM customer_contracts WHERE customer_id = ANY($1)`, both },
			func() (string, any) { return `DELETE FROM inventory WHERE product_id = $1`, f.product },
			func() (string, any) { return `DELETE FROM products WHERE id = $1`, f.product },
			func() (string, any) { return `DELETE FROM customer_users WHERE customer_id = ANY($1)`, both },
			func() (string, any) { return `DELETE FROM customer_branches WHERE customer_id = ANY($1)`, both },
			func() (string, any) { return `DELETE FROM customers WHERE id = ANY($1)`, both },
			func() (string, any) { return `DELETE FROM locations WHERE id = ANY($1)`, bothYards },
		} {
			sql, arg := stmt()
			_, _ = db.Pool.Exec(c, sql, arg)
		}
	})

	return f
}

// --- Part 1: /me and capabilities -------------------------------------------

func TestMe_CapabilitiesPerRole(t *testing.T) {
	f := setupPro(t)
	repo := NewRepository(f.db)

	want := map[string]map[string]bool{
		RoleOwner:          {CapManageTeam: true, CapSeeMoney: true, CapPay: true, CapOrder: true, CapRequestQuotes: true, CapApproveQuotes: true, CapSchedule: true, CapEditDeliveryInstruction: true},
		RoleEstimator:      {CapSeeMoney: false, CapManageTeam: false, CapPay: false, CapOrder: true, CapApproveQuotes: true, CapSchedule: true},
		RoleProjectManager: {CapSeeMoney: true, CapOrder: true, CapApproveQuotes: true, CapPay: false, CapManageTeam: false},
		RoleSiteSuper:      {CapOrder: true, CapSchedule: true, CapSeeMoney: false, CapApproveQuotes: false, CapPay: false},
		RoleAPClerk:        {CapSeeMoney: true, CapPay: true, CapOrder: false, CapSchedule: false, CapApproveQuotes: false},
	}
	for role, caps := range want {
		mux := muxFor(repo, f.aCustomer, f.users[role], role)
		rec, body := call(mux, "GET", "/api/portal/v1/me", "", nil)
		if rec.Code != http.StatusOK {
			t.Fatalf("%s: /me answered %d", role, rec.Code)
		}
		got, _ := body["capabilities"].(map[string]any)
		if got == nil {
			t.Fatalf("%s: no capabilities in %v", role, body)
		}
		for cap, wantVal := range caps {
			if got[cap] != wantVal {
				t.Errorf("%s: capability %s = %v, want %v", role, cap, got[cap], wantVal)
			}
		}
		yards, _ := body["yards"].([]any)
		if len(yards) != 1 {
			t.Errorf("%s: /me listed %d yards, want the account's 1", role, len(yards))
		}
	}
}

// The legacy role spellings and any unknown string grant nothing: a token
// from before the migration keeps its reads and loses its writes.
func TestCapabilities_LegacyAndUnknownRolesGrantNothing(t *testing.T) {
	for _, role := range []string{"Admin", "Buyer", "View-Only", "", "owner "} {
		c := CapabilitiesFor(role)
		for _, cap := range []string{CapManageTeam, CapSeeMoney, CapPay, CapOrder, CapRequestQuotes, CapApproveQuotes, CapSchedule, CapEditDeliveryInstruction} {
			if c.Has(cap) {
				t.Errorf("role %q must not grant %s", role, cap)
			}
		}
	}
}

// A capability refusal on each gated route: the role without the capability
// meets a 403 before anything is read.
func TestCapabilityRefusals_OnEveryGatedRoute(t *testing.T) {
	f := setupPro(t)
	repo := NewRepository(f.db)
	// ap_clerk cannot order, request quotes, schedule or edit instructions.
	mux := muxFor(repo, f.aCustomer, f.users[RoleAPClerk], RoleAPClerk)

	refusals := []struct {
		method, path, body string
	}{
		{"POST", "/api/portal/v1/orders/reorder", `{"order_id":"` + f.aFulfilled.String() + `"}`},
		{"POST", "/api/portal/v1/orders/" + f.aDraft.String() + "/cancel", "{}"},
		{"PUT", "/api/portal/v1/orders/" + f.aDraft.String() + "/project", `{"project_id":null}`},
		{"PUT", "/api/portal/v1/orders/" + f.aDraft.String() + "/delivery-instructions", `{"instructions":"Gate side"}`},
		{"POST", "/api/portal/v1/deliveries/" + f.aDelivery.String() + "/reschedule", `{"requested_date":"` + f.winDate + `"}`},
		{"POST", "/api/portal/v1/quotes", `{"lines":[{"description":"Boards","quantity":10,"uom":"EA"}]}`},
		{"POST", "/api/portal/v1/quotes/" + f.aQuote.String() + "/accept", "{}"},
		{"POST", "/api/portal/v1/quotes/" + f.aQuote.String() + "/decline", "{}"},
		{"POST", "/api/portal/v1/schedule/holds", `{"window_id":"` + f.winCap1.String() + `","date":"` + f.winDate + `"}`},
		{"PUT", "/api/portal/v1/schedule/holds/" + uuid.NewString(), `{"window_id":"` + f.winCap1.String() + `","date":"` + f.winDate + `"}`},
		{"DELETE", "/api/portal/v1/schedule/holds/" + uuid.NewString(), ""},
		{"POST", "/api/portal/v1/cart/items", `{"product_id":"` + uuid.NewString() + `","quantity":1}`},
		{"PUT", "/api/portal/v1/cart/items/" + uuid.NewString(), `{"quantity":2}`},
		{"DELETE", "/api/portal/v1/cart/items/" + uuid.NewString(), ""},
		{"POST", "/api/portal/v1/checkout", `{"delivery_method":"PICKUP"}`},
		{"GET", "/api/portal/v1/users", ""},
	}
	for _, r := range refusals {
		rec, _ := call(mux, r.method, r.path, r.body, nil)
		if rec.Code != http.StatusForbidden {
			t.Errorf("%s %s answered %d, want 403 (ap_clerk lacks the capability)", r.method, r.path, rec.Code)
		}
	}

	// site_super cannot see money, approve quotes or manage the team.
	mux = muxFor(repo, f.aCustomer, f.users[RoleSiteSuper], RoleSiteSuper)
	for _, path := range []string{"/api/portal/v1/dashboard", "/api/portal/v1/invoices", "/api/portal/v1/invoices/" + f.aInvoice.String(), "/api/portal/v1/users", "/api/portal/v1/invites"} {
		rec, _ := call(mux, "GET", path, "", nil)
		if rec.Code != http.StatusForbidden {
			t.Errorf("site_super GET %s answered %d, want 403", path, rec.Code)
		}
	}
	for _, path := range []string{"/api/portal/v1/quotes/" + f.aQuote.String() + "/accept", "/api/portal/v1/quotes/" + f.aQuote.String() + "/decline"} {
		rec, _ := call(mux, "POST", path, "{}", nil)
		if rec.Code != http.StatusForbidden {
			t.Errorf("site_super POST %s answered %d, want 403", path, rec.Code)
		}
	}
}

// --- Part 1: the Today board --------------------------------------------------

func TestToday_Board(t *testing.T) {
	f := setupPro(t)
	repo := NewRepository(f.db)
	mux := muxFor(repo, f.aCustomer, f.users[RoleOwner], RoleOwner)

	rec, body := call(mux, "GET", "/api/portal/v1/today", "", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("today answered %d: %s", rec.Code, rec.Body.String())
	}
	ownerETag := rec.Header().Get("ETag")
	if ownerETag == "" {
		t.Error("today carries no ETag")
	}

	quotes, _ := body["quotes_awaiting_pro"].(map[string]any)
	if n, _ := quotes["count"].(float64); n != 1 {
		t.Errorf("quotes_awaiting_pro.count = %v, want 1", quotes["count"])
	}
	orders, _ := body["open_orders"].(map[string]any)
	if n, _ := orders["count"].(float64); n != 2 {
		t.Errorf("open_orders.count = %v, want 2 (the draft and the confirmed)", orders["count"])
	}
	invoices, _ := body["invoices_due"].(map[string]any)
	if n, _ := invoices["count"].(float64); n != 1 {
		t.Errorf("invoices_due.count = %v, want 1", invoices["count"])
	}
	first, _ := invoices["first"].([]any)
	row, _ := first[0].(map[string]any)
	if row["total_cents"] != float64(10000) {
		t.Errorf("invoice total_cents = %v, want integer 10000 (100.00 dollars)", row["total_cents"])
	}
	deliveries, _ := body["deliveries_today"].(map[string]any)
	if deliveries["count"] != float64(1) {
		t.Errorf("deliveries_today.count = %v, want 1", deliveries["count"])
	}
	delivFirst, _ := deliveries["first"].([]any)
	if len(delivFirst) != 1 {
		t.Errorf("deliveries_today.first has %d rows, want 1", len(delivFirst))
	}

	// A role without see_money still reads the board, with the invoice
	// section zeroed and every quote and open order total_cents hidden,
	// and never shares an ETag with a role that has see_money.
	for _, role := range []string{RoleSiteSuper, RoleEstimator} {
		mux = muxFor(repo, f.aCustomer, f.users[role], role)
		rec, body = call(mux, "GET", "/api/portal/v1/today", "", map[string]string{"If-None-Match": ownerETag})
		if rec.Code != http.StatusOK {
			t.Fatalf("%s today answered %d", role, rec.Code)
		}
		if rec.Header().Get("ETag") == ownerETag {
			t.Errorf("%s ETag %s matched owner ETag %s", role, rec.Header().Get("ETag"), ownerETag)
		}
		invoices, _ = body["invoices_due"].(map[string]any)
		if n, _ := invoices["count"].(float64); n != 0 {
			t.Errorf("%s invoices_due.count = %v, want 0", role, invoices["count"])
		}
		qSection, _ := body["quotes_awaiting_pro"].(map[string]any)
		qFirst, _ := qSection["first"].([]any)
		if len(qFirst) == 0 {
			t.Fatalf("%s quotes_awaiting_pro.first is empty", role)
		}
		for i, item := range qFirst {
			qm, _ := item.(map[string]any)
			if qm["total_cents"] != float64(0) {
				t.Errorf("%s quotes_awaiting_pro.first[%d].total_cents = %v, want 0", role, i, qm["total_cents"])
			}
		}
		oSection, _ := body["open_orders"].(map[string]any)
		oFirst, _ := oSection["first"].([]any)
		if len(oFirst) == 0 {
			t.Fatalf("%s open_orders.first is empty", role)
		}
		for i, item := range oFirst {
			om, _ := item.(map[string]any)
			if om["total_cents"] != float64(0) {
				t.Errorf("%s open_orders.first[%d].total_cents = %v, want 0", role, i, om["total_cents"])
			}
		}
	}

	// The conditional GET is cheap: an unchanged board answers 304, while a
	// since cursor produces a distinct ETag even when every row passes the
	// cursor, and owner vs site_super produce distinct ETags even when open
	// invoice count is zero.
	mux = muxFor(repo, f.aCustomer, f.users[RoleOwner], RoleOwner)
	rec, _ = call(mux, "GET", "/api/portal/v1/today", "", nil)
	etag := rec.Header().Get("ETag")
	rec, _ = call(mux, "GET", "/api/portal/v1/today", "", map[string]string{"If-None-Match": etag})
	if rec.Code != http.StatusNotModified {
		t.Errorf("unchanged today answered %d, want 304", rec.Code)
	}
	epoch := "2000-01-01T00:00:00Z"
	rec, _ = call(mux, "GET", "/api/portal/v1/today?since="+epoch, "", map[string]string{"If-None-Match": etag})
	if rec.Code != http.StatusOK {
		t.Errorf("today with since cursor and unfiltered ETag answered %d, want 200", rec.Code)
	}
	if rec.Header().Get("ETag") == etag {
		t.Errorf("today with since cursor returned same ETag %s as unfiltered board", etag)
	}

	// NextDeliveries: when there are 6 upcoming deliveries and only the 6th
	// was updated after since, count reports all 6 while since filters in SQL
	// before LIMIT 5 so first contains that 6th delivery.
	ctx := context.Background()
	cutoffSince := time.Now().UTC()
	var sixthDelivID uuid.UUID
	for i := 1; i <= 6; i++ {
		rtID := uuid.New()
		dID := uuid.New()
		upd := cutoffSince.Add(-10 * time.Minute)
		if i == 6 {
			upd = cutoffSince.Add(10 * time.Second)
			sixthDelivID = dID
		}
		if _, err := f.db.Pool.Exec(ctx,
			`INSERT INTO delivery_routes (id, status, scheduled_date) VALUES ($1, 'SCHEDULED', CURRENT_DATE + ($2 || ' days')::interval)`,
			rtID, fmt.Sprintf("%d", i+1)); err != nil {
			t.Fatalf("insert next route %d: %v", i, err)
		}
		t.Cleanup(func() {
			_, _ = f.db.Pool.Exec(context.Background(), `DELETE FROM delivery_routes WHERE id = $1`, rtID)
		})
		if _, err := f.db.Pool.Exec(ctx,
			`INSERT INTO deliveries (id, order_id, route_id, status, stop_sequence, created_at, updated_at) VALUES ($1, $2, $3, 'PENDING', $4, $5, $5)`,
			dID, f.aConfirmed, rtID, i, upd); err != nil {
			t.Fatalf("insert next delivery %d: %v", i, err)
		}
	}
	rec, body = call(mux, "GET", "/api/portal/v1/today?since="+cutoffSince.Format(time.RFC3339Nano), "", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("today with next deliveries since answered %d", rec.Code)
	}
	nextSec, _ := body["next_deliveries"].(map[string]any)
	if nextSec["count"] != float64(6) {
		t.Errorf("next_deliveries.count = %v, want 6 (not capped at 5)", nextSec["count"])
	}
	nextFirst, _ := nextSec["first"].([]any)
	if len(nextFirst) != 1 {
		t.Fatalf("next_deliveries.first with since has %d rows, want 1 (the 6th delivery updated after since)", len(nextFirst))
	}
	if gotID, _ := nextFirst[0].(map[string]any)["delivery_id"].(string); gotID != sixthDelivID.String() {
		t.Errorf("next_deliveries.first[0].delivery_id = %s, want %s", gotID, sixthDelivID)
	}
}

// --- Part 4: scheduling ---------------------------------------------------------

func TestSchedule_ListHoldChangeRelease(t *testing.T) {
	f := setupPro(t)
	repo := NewRepository(f.db)
	mux := muxFor(repo, f.aCustomer, f.users[RoleProjectManager], RoleProjectManager)

	// The open windows for the account's own yard, over the fixture's week.
	to := time.Now().UTC().AddDate(0, 0, 8).Format("2006-01-02")
	rec, body := call(mux, "GET",
		"/api/portal/v1/schedule/windows?yard_id="+f.aYard.String()+"&from="+f.winDate+"&to="+to, "", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("windows answered %d: %s", rec.Code, rec.Body.String())
	}
	slots, _ := body["delivery_and_pickup"].([]any)
	_ = slots
	// The body is a bare array; decode again.
	var views []map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &views)
	if len(views) < 3 {
		t.Fatalf("expected the three windows over the range, got %d", len(views))
	}
	var pickupSlot map[string]any
	for _, v := range views {
		if v["kind"] == "pickup" && v["remaining"] == float64(3) {
			pickupSlot = v
		}
	}
	if pickupSlot == nil {
		t.Fatal("the pickup window's slot with 3 places is missing")
	}
	if pickupSlot["start_local"] != "07:00" {
		t.Errorf("pickup start_local = %v, want 07:00 (the yard's own zone)", pickupSlot["start_local"])
	}

	// Hold the pickup slot riding on the confirmed order.
	rec, hold := call(mux, "POST", "/api/portal/v1/schedule/holds", fmt.Sprintf(
		`{"window_id":"%s","date":"%s","order_id":"%s"}`, f.winPickup, f.winDate, f.aConfirmed), nil)
	if rec.Code != http.StatusCreated {
		t.Fatalf("hold answered %d: %s", rec.Code, rec.Body.String())
	}
	firstID, _ := hold["id"].(string)

	// A second hold on the same order supersedes the first.
	rec, hold = call(mux, "POST", "/api/portal/v1/schedule/holds", fmt.Sprintf(
		`{"window_id":"%s","date":"%s","order_id":"%s"}`, f.winPickup, f.winDate, f.aConfirmed), nil)
	if rec.Code != http.StatusCreated {
		t.Fatalf("second hold answered %d: %s", rec.Code, rec.Body.String())
	}
	holdID, _ := hold["id"].(string)
	// The superseded hold is history: changing it is a 404.
	rec, _ = call(mux, "PUT", "/api/portal/v1/schedule/holds/"+firstID, fmt.Sprintf(
		`{"window_id":"%s","date":"%s"}`, f.winCap1, f.winDate), nil)
	if rec.Code != http.StatusNotFound {
		t.Errorf("changing the superseded hold answered %d, want 404", rec.Code)
	}

	// Change the active hold to the capacity 1 delivery window.
	rec, _ = call(mux, "PUT", "/api/portal/v1/schedule/holds/"+holdID, fmt.Sprintf(
		`{"window_id":"%s","date":"%s"}`, f.winCap1, f.winDate), nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("change answered %d: %s", rec.Code, rec.Body.String())
	}

	// Both the order and Dispatch (the order's open delivery stop) show the chosen window.
	rec, ordBody := call(mux, "GET", "/api/portal/v1/orders/"+f.aConfirmed.String(), "", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("get order answered %d", rec.Code)
	}
	ordHold, _ := ordBody["schedule_hold"].(map[string]any)
	if ordHold == nil || ordHold["id"] != holdID || ordHold["window_id"] != f.winCap1.String() {
		t.Errorf("order schedule_hold = %v, want hold %s on window %s", ordBody["schedule_hold"], holdID, f.winCap1)
	}
	var schedStart, schedEnd *time.Time
	if err := f.db.Pool.QueryRow(context.Background(),
		`SELECT scheduled_start, scheduled_end FROM deliveries WHERE order_id = $1`, f.aConfirmed).Scan(&schedStart, &schedEnd); err != nil {
		t.Fatalf("query stop scheduled bounds: %v", err)
	}
	if schedStart == nil || schedEnd == nil {
		t.Errorf("dispatch stop scheduled_start=%v scheduled_end=%v, want the chosen window bounds", schedStart, schedEnd)
	}

	// If the delivery un-stamping update fails during ReleaseScheduleHold,
	// the error is returned and the hold remains 'held' (atomic transaction).
	constraintName := "chk_test_p1_2_release_" + strings.ReplaceAll(f.aConfirmed.String()[:8], "-", "")
	if _, err := f.db.Pool.Exec(context.Background(), fmt.Sprintf(
		`ALTER TABLE deliveries ADD CONSTRAINT %s CHECK (order_id <> '%s' OR scheduled_start IS NOT NULL) NOT VALID`,
		constraintName, f.aConfirmed,
	)); err != nil {
		t.Fatalf("add test constraint: %v", err)
	}
	rec, _ = call(mux, "DELETE", "/api/portal/v1/schedule/holds/"+holdID, "", nil)
	_, _ = f.db.Pool.Exec(context.Background(), fmt.Sprintf(`ALTER TABLE deliveries DROP CONSTRAINT IF EXISTS %s`, constraintName))
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("release with failing delivery update answered %d, want 500", rec.Code)
	}
	var holdStatus string
	if err := f.db.Pool.QueryRow(context.Background(),
		`SELECT status FROM portal_schedule_holds WHERE id = $1`, holdID).Scan(&holdStatus); err != nil {
		t.Fatalf("query hold status after failed release: %v", err)
	}
	if holdStatus != "held" {
		t.Errorf("hold status after failed release = %q, want held (rolled back)", holdStatus)
	}

	// Release it across three concurrent contenders on a pool of four connections:
	// exactly one wins with 204 and the other two answer 404.
	db4 := testutil.RequireDBMaxConns(t, 4)
	repo4 := NewRepository(db4)
	const relContenders = 3
	relCodes := make(chan int, relContenders)
	relStart := make(chan struct{})
	var relWG sync.WaitGroup
	for i := 0; i < relContenders; i++ {
		relWG.Add(1)
		go func() {
			defer relWG.Done()
			m := muxFor(repo4, f.aCustomer, f.users[RoleProjectManager], RoleProjectManager)
			<-relStart
			r, _ := call(m, "DELETE", "/api/portal/v1/schedule/holds/"+holdID, "", nil)
			relCodes <- r.Code
		}()
	}
	close(relStart)
	relWG.Wait()
	close(relCodes)
	noContent, notFound := 0, 0
	for c := range relCodes {
		switch c {
		case http.StatusNoContent:
			noContent++
		case http.StatusNotFound:
			notFound++
		default:
			t.Errorf("concurrent release answered %d, want 204 or 404", c)
		}
	}
	if noContent != 1 || notFound != relContenders-1 {
		t.Errorf("concurrent release settled %d x 204 and %d x 404, want 1 and %d", noContent, notFound, relContenders-1)
	}
	var ordSchedDate *time.Time
	if err := f.db.Pool.QueryRow(context.Background(),
		`SELECT scheduled_delivery_date FROM orders WHERE id = $1`, f.aConfirmed).Scan(&ordSchedDate); err != nil {
		t.Fatalf("query order scheduled_delivery_date after release: %v", err)
	}
	if ordSchedDate != nil {
		t.Errorf("order scheduled_delivery_date after release = %v, want nil", ordSchedDate)
	}
	if err := f.db.Pool.QueryRow(context.Background(),
		`SELECT scheduled_start, scheduled_end FROM deliveries WHERE order_id = $1`, f.aConfirmed).Scan(&schedStart, &schedEnd); err != nil {
		t.Fatalf("query stop scheduled bounds after release: %v", err)
	}
	if schedStart != nil || schedEnd != nil {
		t.Errorf("dispatch stop after release scheduled_start=%v scheduled_end=%v, want nil", schedStart, schedEnd)
	}
}

// The cutoff: a window whose booking cutoff has already passed refuses the
// hold with the 409 refusal envelope.
func TestSchedule_CutoffRefusal(t *testing.T) {
	f := setupPro(t)
	repo := NewRepository(f.db)
	mux := muxFor(repo, f.aCustomer, f.users[RoleOwner], RoleOwner)

	rec, body := call(mux, "POST", "/api/portal/v1/schedule/holds", fmt.Sprintf(
		`{"window_id":"%s","date":"%s"}`, f.winCutoff, f.winDate), nil)
	if rec.Code != http.StatusConflict {
		t.Fatalf("cutoff hold answered %d, want 409", rec.Code)
	}
	errObj, _ := body["error"].(map[string]any)
	if errObj == nil || errObj["code"] != "SCHEDULE_CUTOFF_PASSED" {
		t.Errorf("refusal code = %v, want SCHEDULE_CUTOFF_PASSED", body)
	}
}

// The current calendar date for listing open windows and placing a hold is
// taken in the yard's own time zone, never in UTC: a yard in a zone behind UTC
// (testutil.AlwaysBehindUTC) can list and hold a late evening slot on its own
// current local day even after UTC midnight has advanced to the next date.
func TestSchedule_CurrentDateInYardTimezone(t *testing.T) {
	f := setupPro(t)
	ctx := context.Background()
	if _, err := f.db.Pool.Exec(ctx,
		`UPDATE locations SET timezone = $1 WHERE id = $2`, testutil.AlwaysBehindUTC, f.aYard); err != nil {
		t.Fatalf("set yard timezone: %v", err)
	}
	nowInYard := time.Now().In(time.FixedZone("-25", -25*3600))
	yardToday := nowInYard.Format("2006-01-02")
	yardWeekday := int(nowInYard.Weekday())
	winToday := uuid.New()
	if _, err := f.db.Pool.Exec(ctx, `
		INSERT INTO yard_schedule_windows (id, yard_id, kind, weekday, start_time, end_time, capacity, cutoff_minutes)
		VALUES ($1, $2, 'delivery', $3, '23:58', '23:59', 2, 0)
	`, winToday, f.aYard, yardWeekday); err != nil {
		t.Fatalf("insert today window: %v", err)
	}

	repo := NewRepository(f.db)
	mux := muxFor(repo, f.aCustomer, f.users[RoleOwner], RoleOwner)

	// Default range on GET /schedule/windows (no from or to) starts on the
	// yard's own current date.
	rec, _ := call(mux, "GET", "/api/portal/v1/schedule/windows?yard_id="+f.aYard.String(), "", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("list windows answered %d: %s", rec.Code, rec.Body.String())
	}
	var views []map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &views)
	foundToday := false
	for _, v := range views {
		if v["window_id"] == winToday.String() && v["date"] == yardToday {
			foundToday = true
			break
		}
	}
	if !foundToday {
		t.Errorf("default windows list omitted yard local today (%s) for window %s", yardToday, winToday)
	}

	// Holding that slot on the yard's local today succeeds (201), not 400
	// "the date is in the past".
	rec, _ = call(mux, "POST", "/api/portal/v1/schedule/holds", fmt.Sprintf(
		`{"window_id":"%s","date":"%s"}`, winToday, yardToday), nil)
	if rec.Code != http.StatusCreated {
		t.Fatalf("hold on yard local today (%s) answered %d: %s", yardToday, rec.Code, rec.Body.String())
	}
}

// Unattached checkout holds (order_id IS NULL) expire after the checkout hold
// TTL and free their window capacity so abandoned checkouts do not permanently
// consume slots.
func TestSchedule_ExpiredUnattachedHoldFreesCapacity(t *testing.T) {
	f := setupPro(t)
	ctx := context.Background()
	repo := NewRepository(f.db)
	mux := muxFor(repo, f.aCustomer, f.users[RoleOwner], RoleOwner)

	// Place an unattached checkout hold on winCap1 (capacity 1).
	rec, holdBody := call(mux, "POST", "/api/portal/v1/schedule/holds", fmt.Sprintf(
		`{"window_id":"%s","date":"%s"}`, f.winCap1, f.winDate), nil)
	if rec.Code != http.StatusCreated {
		t.Fatalf("initial checkout hold answered %d: %s", rec.Code, rec.Body.String())
	}
	staleHoldID, _ := holdBody["id"].(string)

	// Age the unattached hold past the checkout hold TTL (31 minutes old).
	if _, err := f.db.Pool.Exec(ctx,
		`UPDATE portal_schedule_holds SET created_at = NOW() - interval '31 minutes' WHERE id = $1`, staleHoldID); err != nil {
		t.Fatalf("age checkout hold: %v", err)
	}

	// Open windows list frees the capacity (remaining == 1).
	rec, _ = call(mux, "GET", fmt.Sprintf(
		"/api/portal/v1/schedule/windows?yard_id=%s&from=%s&to=%s", f.aYard, f.winDate, f.winDate), "", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("list windows answered %d: %s", rec.Code, rec.Body.String())
	}
	var views []map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &views)
	foundCap1 := false
	for _, v := range views {
		if v["window_id"] == f.winCap1.String() {
			foundCap1 = true
			if rem, _ := v["remaining"].(float64); rem != 1 {
				t.Errorf("winCap1 remaining after stale checkout hold = %v, want 1", v["remaining"])
			}
		}
	}
	if !foundCap1 {
		t.Fatalf("winCap1 missing from windows list")
	}

	// Reading the stale hold reports status = "expired".
	rec, readBody := call(mux, "GET", "/api/portal/v1/schedule/holds/"+staleHoldID, "", nil)
	if rec.Code != http.StatusOK || readBody["status"] != "expired" {
		t.Errorf("stale hold status = %d / %v, want 200 / expired", rec.Code, readBody["status"])
	}

	// A new hold on winCap1 succeeds (201 Created).
	rec, _ = call(mux, "POST", "/api/portal/v1/schedule/holds", fmt.Sprintf(
		`{"window_id":"%s","date":"%s"}`, f.winCap1, f.winDate), nil)
	if rec.Code != http.StatusCreated {
		t.Fatalf("hold after stale hold expiry answered %d: %s", rec.Code, rec.Body.String())
	}
}

// Checkout with a schedule hold creates the order and attaches the hold in one
// transaction (a failure leaves neither), and ChangeScheduleHold neither
// releases the existing hold before the new one is secured nor re-applies the
// booking cutoff to an unchanged window.
func TestCheckout_WithScheduleHoldAndChangeCutoff(t *testing.T) {
	f := setupPro(t)
	repo := NewRepository(f.db)
	mux := muxFor(repo, f.aCustomer, f.users[RoleOwner], RoleOwner)
	ctx := context.Background()

	// 1. ChangeScheduleHold on an unchanged window after the booking cutoff
	// has passed does not refuse the hold or release it before securing the slot.
	rec, holdBody := call(mux, "POST", "/api/portal/v1/schedule/holds", fmt.Sprintf(
		`{"window_id":"%s","date":"%s"}`, f.winPickup, f.winDate), nil)
	if rec.Code != http.StatusCreated {
		t.Fatalf("initial hold answered %d: %s", rec.Code, rec.Body.String())
	}
	holdID, _ := holdBody["id"].(string)

	// Advance cutoff_minutes on winPickup so the booking cutoff is now in the past.
	if _, err := f.db.Pool.Exec(ctx,
		`UPDATE yard_schedule_windows SET cutoff_minutes = 1000000 WHERE id = $1`, f.winPickup); err != nil {
		t.Fatalf("update cutoff: %v", err)
	}
	// Changing the hold with the same window_id and date (attaching to aDraft)
	// must succeed without re-applying the booking cutoff.
	rec, _ = call(mux, "PUT", "/api/portal/v1/schedule/holds/"+holdID, fmt.Sprintf(
		`{"window_id":"%s","date":"%s","order_id":"%s"}`, f.winPickup, f.winDate, f.aDraft), nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("ChangeScheduleHold on unchanged window after cutoff answered %d: %s", rec.Code, rec.Body.String())
	}
	// Reset cutoff_minutes on winPickup for the next sub-cases.
	if _, err := f.db.Pool.Exec(ctx,
		`UPDATE yard_schedule_windows SET cutoff_minutes = 0 WHERE id = $1`, f.winPickup); err != nil {
		t.Fatalf("reset cutoff: %v", err)
	}

	// 2. Checkout with a schedule_hold_id whose window cutoff crossed after the
	// hold was placed still succeeds, attaches the hold, and stamps the order.
	rec, chkHoldBody := call(mux, "POST", "/api/portal/v1/schedule/holds", fmt.Sprintf(
		`{"window_id":"%s","date":"%s"}`, f.winPickup, f.winDate), nil)
	if rec.Code != http.StatusCreated {
		t.Fatalf("checkout hold answered %d: %s", rec.Code, rec.Body.String())
	}
	chkHoldID, _ := chkHoldBody["id"].(string)
	if _, err := f.db.Pool.Exec(ctx,
		`UPDATE yard_schedule_windows SET cutoff_minutes = 1000000 WHERE id = $1`, f.winPickup); err != nil {
		t.Fatalf("advance cutoff for checkout: %v", err)
	}
	rec, _ = call(mux, "POST", "/api/portal/v1/cart/items", fmt.Sprintf(
		`{"product_id":"%s","quantity":2}`, f.product), nil)
	if rec.Code != http.StatusCreated {
		t.Fatalf("add to cart answered %d: %s", rec.Code, rec.Body.String())
	}
	rec, chkBody := call(mux, "POST", "/api/portal/v1/checkout", fmt.Sprintf(
		`{"delivery_method":"PICKUP","schedule_hold_id":"%s"}`, chkHoldID), nil)
	if rec.Code != http.StatusCreated {
		t.Fatalf("checkout with held slot after cutoff answered %d: %s", rec.Code, rec.Body.String())
	}
	newOrderIDStr, _ := chkBody["order_id"].(string)
	var attachedOrderID *uuid.UUID
	var schedDate *string
	if err := f.db.Pool.QueryRow(ctx,
		`SELECT order_id FROM portal_schedule_holds WHERE id = $1`, chkHoldID).Scan(&attachedOrderID); err != nil {
		t.Fatalf("query attached hold: %v", err)
	}
	if attachedOrderID == nil || attachedOrderID.String() != newOrderIDStr {
		t.Errorf("hold order_id = %v, want %s", attachedOrderID, newOrderIDStr)
	}
	if err := f.db.Pool.QueryRow(ctx,
		`SELECT scheduled_delivery_date::text FROM orders WHERE id = $1`, newOrderIDStr).Scan(&schedDate); err != nil {
		t.Fatalf("query order scheduled_delivery_date: %v", err)
	}
	if schedDate == nil || *schedDate != f.winDate {
		t.Errorf("order scheduled_delivery_date = %v, want %s", schedDate, f.winDate)
	}
	if _, err := f.db.Pool.Exec(ctx,
		`UPDATE yard_schedule_windows SET cutoff_minutes = 0 WHERE id = $1`, f.winPickup); err != nil {
		t.Fatalf("reset cutoff: %v", err)
	}

	// 3. Checkout creates the order and attaches the hold in one transaction:
	// when attaching the hold fails after order creation, neither an orphaned
	// order nor a modified hold is left behind.
	rec, failHoldBody := call(mux, "POST", "/api/portal/v1/schedule/holds", fmt.Sprintf(
		`{"window_id":"%s","date":"%s"}`, f.winPickup, f.winDate), nil)
	if rec.Code != http.StatusCreated {
		t.Fatalf("fail hold answered %d: %s", rec.Code, rec.Body.String())
	}
	failHoldID, _ := failHoldBody["id"].(string)
	rec, _ = call(mux, "POST", "/api/portal/v1/cart/items", fmt.Sprintf(
		`{"product_id":"%s","quantity":1}`, f.product), nil)
	if rec.Code != http.StatusCreated {
		t.Fatalf("add to cart answered %d: %s", rec.Code, rec.Body.String())
	}
	var ordersBefore int
	if err := f.db.Pool.QueryRow(ctx,
		`SELECT COUNT(*) FROM orders WHERE customer_id = $1`, f.aCustomer).Scan(&ordersBefore); err != nil {
		t.Fatalf("count orders before: %v", err)
	}
	constraintName := "chk_test_p1_3_attach_" + strings.ReplaceAll(failHoldID[:8], "-", "")
	if _, err := f.db.Pool.Exec(ctx, fmt.Sprintf(
		`ALTER TABLE portal_schedule_holds ADD CONSTRAINT %s CHECK (id <> '%s' OR order_id IS NULL) NOT VALID`,
		constraintName, failHoldID,
	)); err != nil {
		t.Fatalf("add attach constraint: %v", err)
	}
	rec, _ = call(mux, "POST", "/api/portal/v1/checkout", fmt.Sprintf(
		`{"delivery_method":"PICKUP","schedule_hold_id":"%s"}`, failHoldID), nil)
	_, _ = f.db.Pool.Exec(ctx, fmt.Sprintf(`ALTER TABLE portal_schedule_holds DROP CONSTRAINT IF EXISTS %s`, constraintName))
	if rec.Code == http.StatusCreated || rec.Code == http.StatusOK {
		t.Fatalf("checkout with failing hold attachment answered %d, want error", rec.Code)
	}
	var ordersAfter int
	if err := f.db.Pool.QueryRow(ctx,
		`SELECT COUNT(*) FROM orders WHERE customer_id = $1`, f.aCustomer).Scan(&ordersAfter); err != nil {
		t.Fatalf("count orders after: %v", err)
	}
	if ordersAfter != ordersBefore {
		t.Errorf("orders count after failed checkout = %d, want %d (no orphaned order)", ordersAfter, ordersBefore)
	}

	// 4. Three concurrent contenders on a pool of four connections racing
	// Checkout with the same unattached hold: exactly one wins and creates
	// one order; the other two fail cleanly without leaving orphaned orders.
	db4 := testutil.RequireDBMaxConns(t, 4)
	repo4 := NewRepository(db4)
	const chkContenders = 3
	chkCodes := make(chan int, chkContenders)
	chkStart := make(chan struct{})
	var chkWG sync.WaitGroup
	for i := 0; i < chkContenders; i++ {
		chkWG.Add(1)
		go func() {
			defer chkWG.Done()
			m := muxFor(repo4, f.aCustomer, f.users[RoleOwner], RoleOwner)
			<-chkStart
			r, _ := call(m, "POST", "/api/portal/v1/checkout", fmt.Sprintf(
				`{"delivery_method":"PICKUP","schedule_hold_id":"%s"}`, failHoldID), nil)
			chkCodes <- r.Code
		}()
	}
	close(chkStart)
	chkWG.Wait()
	close(chkCodes)
	chkWon := 0
	for c := range chkCodes {
		if c == http.StatusCreated {
			chkWon++
		}
	}
	if chkWon != 1 {
		t.Errorf("concurrent checkout with same hold settled %d winners, want 1", chkWon)
	}
	if err := f.db.Pool.QueryRow(ctx,
		`SELECT COUNT(*) FROM orders WHERE customer_id = $1`, f.aCustomer).Scan(&ordersAfter); err != nil {
		t.Fatalf("count orders after race: %v", err)
	}
	if ordersAfter != ordersBefore+1 {
		t.Errorf("orders count after checkout race = %d, want %d (exactly 1 order created)", ordersAfter, ordersBefore+1)
	}
}

// The capacity race: three contenders for the one place of a capacity one
// window, on a pool of four connections. Exactly one wins; the transaction
// holds the window row, so nobody counts a stale capacity.
func TestSchedule_CapacityRaceAtPoolSize4(t *testing.T) {
	f := setupPro(t)
	db := testutil.RequireDBMaxConns(t, 4)
	t.Cleanup(func() {
		_, _ = db.Pool.Exec(context.Background(),
			`DELETE FROM portal_schedule_holds WHERE window_id = $1`, f.winCap1)
	})

	repo := NewRepository(db)
	const contenders = 3
	results := make(chan int, contenders)
	start := make(chan struct{})
	var wg sync.WaitGroup
	for i := 0; i < contenders; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			mux := muxFor(repo, f.aCustomer, f.users[RoleOwner], RoleOwner)
			<-start
			rec, _ := call(mux, "POST", "/api/portal/v1/schedule/holds", fmt.Sprintf(
				`{"window_id":"%s","date":"%s"}`, f.winCap1, f.winDate), nil)
			results <- rec.Code
		}(i)
	}
	close(start)
	wg.Wait()
	close(results)

	won, refused := 0, 0
	for code := range results {
		switch code {
		case http.StatusCreated:
			won++
		case http.StatusConflict:
			refused++
		default:
			t.Errorf("a contender answered %d, want 201 or 409", code)
		}
	}
	if won != 1 || refused != contenders-1 {
		t.Errorf("race settled %d winners and %d refusals, want exactly 1 winner of the capacity 1 window", won, refused)
	}
}

// Tenancy: another account's yard, order and hold are all 404s, never 403s,
// and writes by tenant B leave tenant A's rows untouched in the database.
func TestSchedule_TenancyRefusals(t *testing.T) {
	f := setupPro(t)
	ctx := context.Background()
	repo := NewRepository(f.db)

	// A yard the account does not hold.
	mux := muxFor(repo, f.aCustomer, f.users[RoleOwner], RoleOwner)
	rec, _ := call(mux, "GET", "/api/portal/v1/schedule/windows?yard_id="+f.bYard.String(), "", nil)
	if rec.Code != http.StatusNotFound {
		t.Errorf("yard B's windows answered %d, want 404", rec.Code)
	}
	// A hold riding another account's order.
	rec, _ = call(mux, "POST", "/api/portal/v1/schedule/holds", fmt.Sprintf(
		`{"window_id":"%s","date":"%s","order_id":"%s"}`, f.winPickup, f.winDate, f.bOrder), nil)
	if rec.Code != http.StatusNotFound {
		t.Errorf("hold on B's order answered %d, want 404", rec.Code)
	}
	// Create a valid hold for tenant A so tenant B can attempt to read, change, and release it.
	rec, aHoldBody := call(mux, "POST", "/api/portal/v1/schedule/holds", fmt.Sprintf(
		`{"window_id":"%s","date":"%s"}`, f.winPickup, f.winDate), nil)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create A's hold answered %d: %s", rec.Code, rec.Body.String())
	}
	aHoldID, _ := aHoldBody["id"].(string)

	// B holding A's window: the yard is not B's.
	muxB := muxFor(repo, f.bCustomer, uuid.New(), RoleOwner)
	rec, _ = call(muxB, "POST", "/api/portal/v1/schedule/holds", fmt.Sprintf(
		`{"window_id":"%s","date":"%s"}`, f.winPickup, f.winDate), nil)
	if rec.Code != http.StatusNotFound {
		t.Errorf("B holding A's window answered %d, want 404", rec.Code)
	}
	// B reading A's hold.
	rec, _ = call(muxB, "GET", "/api/portal/v1/schedule/holds/"+aHoldID, "", nil)
	if rec.Code != http.StatusNotFound {
		t.Errorf("B reading A's hold answered %d, want 404", rec.Code)
	}
	// B changing A's hold.
	rec, _ = call(muxB, "PUT", "/api/portal/v1/schedule/holds/"+aHoldID, fmt.Sprintf(
		`{"window_id":"%s","date":"%s"}`, f.winCap1, f.winDate), nil)
	if rec.Code != http.StatusNotFound {
		t.Errorf("B changing A's hold answered %d, want 404", rec.Code)
	}
	// B releasing A's hold.
	rec, _ = call(muxB, "DELETE", "/api/portal/v1/schedule/holds/"+aHoldID, "", nil)
	if rec.Code != http.StatusNotFound {
		t.Errorf("B releasing A's hold answered %d, want 404", rec.Code)
	}
	var holdStatus string
	var holdWin uuid.UUID
	if err := f.db.Pool.QueryRow(ctx,
		`SELECT status, window_id FROM portal_schedule_holds WHERE id = $1`, aHoldID).Scan(&holdStatus, &holdWin); err != nil {
		t.Fatalf("verify A's hold in DB: %v", err)
	}
	if holdStatus != "held" || holdWin != f.winPickup {
		t.Errorf("A's hold after B's mutations = (%q, %s), want (held, %s)", holdStatus, holdWin, f.winPickup)
	}

	// B reading A's timeline.
	rec, _ = call(muxB, "GET", "/api/portal/v1/orders/"+f.aFulfilled.String()+"/timeline", "", nil)
	if rec.Code != http.StatusNotFound {
		t.Errorf("B reading A's timeline answered %d, want 404", rec.Code)
	}
	// B setting delivery instructions on A's order.
	rec, _ = call(muxB, "PUT", "/api/portal/v1/orders/"+f.aConfirmed.String()+"/delivery-instructions",
		`{"instructions":"cross-tenant write"}`, nil)
	if rec.Code != http.StatusNotFound {
		t.Errorf("B updating A's delivery instructions answered %d, want 404", rec.Code)
	}
	var aInstr *string
	if err := f.db.Pool.QueryRow(ctx,
		`SELECT site_delivery_instructions FROM orders WHERE id = $1`, f.aConfirmed).Scan(&aInstr); err != nil {
		t.Fatalf("verify A's order instructions in DB: %v", err)
	}
	if aInstr != nil {
		t.Errorf("A's order instructions after B's write = %v, want nil", *aInstr)
	}

	// B reading Today sees none of A's quotes, deliveries, or invoices, and only B's own open order.
	rec, todayB := call(muxB, "GET", "/api/portal/v1/today", "", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("B's today answered %d: %s", rec.Code, rec.Body.String())
	}
	if q, _ := todayB["quotes_awaiting_pro"].(map[string]any); q["count"] != float64(0) {
		t.Errorf("B's today quotes_awaiting_pro.count = %v, want 0", q["count"])
	}
	if d, _ := todayB["next_deliveries"].(map[string]any); d["count"] != float64(0) {
		t.Errorf("B's today next_deliveries.count = %v, want 0", d["count"])
	}
	if inv, _ := todayB["invoices_due"].(map[string]any); inv["count"] != float64(0) {
		t.Errorf("B's today invoices_due.count = %v, want 0", inv["count"])
	}
	if oo, _ := todayB["open_orders"].(map[string]any); oo["count"] != float64(1) {
		t.Errorf("B's today open_orders.count = %v, want 1 (only B's own order)", oo["count"])
	}
}

// --- Part 4 of the order view: the timeline and site instructions --------------

func TestOrderTimeline_States(t *testing.T) {
	f := setupPro(t)
	repo := NewRepository(f.db)
	mux := muxFor(repo, f.aCustomer, f.users[RoleOwner], RoleOwner)

	steps := func(t *testing.T, orderID uuid.UUID) ([]any, map[string]any) {
		t.Helper()
		rec, body := call(mux, "GET", "/api/portal/v1/orders/"+orderID.String()+"/timeline", "", nil)
		if rec.Code != http.StatusOK {
			t.Fatalf("timeline for %s answered %d: %s", orderID, rec.Code, rec.Body.String())
		}
		s, _ := body["steps"].([]any)
		return s, body
	}

	// A DRAFT order promises nothing.
	s, _ := steps(t, f.aDraft)
	for _, raw := range s {
		step, _ := raw.(map[string]any)
		if step["state"] != "upcoming" {
			t.Errorf("draft order step %v is %v, want upcoming", step["key"], step["state"])
		}
	}

	// A CONFIRMED delivery order with a scheduled stop: the dispatcher has
	// the job, so Confirmed and Preparing are done and Loaded is current.
	s, body := steps(t, f.aConfirmed)
	if body["delivery_type"] != "DELIVERY" {
		t.Errorf("confirmed order delivery_type = %v, want DELIVERY", body["delivery_type"])
	}
	if s[0].(map[string]any)["state"] != "done" || s[1].(map[string]any)["state"] != "done" ||
		s[2].(map[string]any)["state"] != "current" {
		t.Errorf("confirmed order steps: %v", s)
	}

	// A FULFILLED delivery order with a delivered stop and POD: every step
	// done, the proof carried.
	s, body = steps(t, f.aFulfilled)
	for _, raw := range s {
		step, _ := raw.(map[string]any)
		if step["state"] != "done" {
			t.Errorf("fulfilled order step %v is %v, want done", step["key"], step["state"])
		}
	}
	pod, _ := body["pod"].(map[string]any)
	if pod == nil || pod["signed_by"] != "Site Contact" {
		t.Errorf("fulfilled order pod = %v, want the signer from the stop", body["pod"])
	}
}

func TestOrderTimeline_PickupFlowSpeaksPickup(t *testing.T) {
	f := setupPro(t)
	repo := NewRepository(f.db)
	mux := muxFor(repo, f.aCustomer, f.users[RoleOwner], RoleOwner)
	rec, body := call(mux, "GET", "/api/portal/v1/orders/"+f.aDraft.String()+"/timeline", "", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("timeline answered %d", rec.Code)
	}
	if body["delivery_type"] != "PICKUP" {
		t.Fatalf("pickup order's delivery_type = %v, want PICKUP", body["delivery_type"])
	}
	s, _ := body["steps"].([]any)
	if len(s) != 5 {
		t.Fatalf("pickup flow has %d steps, want 5", len(s))
	}
	if s[2].(map[string]any)["key"] != "loaded" || s[3].(map[string]any)["key"] != "ready_for_pickup" || s[4].(map[string]any)["key"] != "picked_up" {
		t.Errorf("pickup flow steps = %v, want loaded -> ready_for_pickup -> picked_up", s)
	}
}

// Site delivery instructions: editable on an unfulfilled order, read only on
// a fulfilled one, and the write feeds the order change feed (updated_at
// moves).
func TestSiteDeliveryInstructions(t *testing.T) {
	f := setupPro(t)
	repo := NewRepository(f.db)
	mux := muxFor(repo, f.aCustomer, f.users[RoleSiteSuper], RoleSiteSuper)

	rec, ordBody := call(mux, "PUT", "/api/portal/v1/orders/"+f.aConfirmed.String()+"/delivery-instructions",
		`{"instructions":"Leave at the gate, code 1234"}`, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("instructions answered %d: %s", rec.Code, rec.Body.String())
	}
	if ordBody["site_delivery_instructions"] != "Leave at the gate, code 1234" {
		t.Errorf("order DTO site_delivery_instructions = %v, want Leave at the gate, code 1234", ordBody["site_delivery_instructions"])
	}

	// The dealer's own read is scoped the same way: both the order and its open
	// delivery stop carry the instructions.
	var got *string
	if err := f.db.Pool.QueryRow(context.Background(),
		`SELECT site_delivery_instructions FROM orders WHERE id = $1`, f.aConfirmed).Scan(&got); err != nil {
		t.Fatalf("read back order: %v", err)
	}
	if got == nil || *got != "Leave at the gate, code 1234" {
		t.Errorf("stored order instructions = %v", got)
	}
	var stopInstr *string
	if err := f.db.Pool.QueryRow(context.Background(),
		`SELECT delivery_instructions FROM deliveries WHERE order_id = $1`, f.aConfirmed).Scan(&stopInstr); err != nil {
		t.Fatalf("read back stop: %v", err)
	}
	if stopInstr == nil || *stopInstr != "Leave at the gate, code 1234" {
		t.Errorf("stored delivery stop instructions = %v, want Leave at the gate, code 1234", stopInstr)
	}

	// If the delivery mirror update fails, the error is returned and the order
	// row rolls back so order and delivery never diverge.
	constraintName := "chk_test_p1_2_instr_" + strings.ReplaceAll(f.aConfirmed.String()[:8], "-", "")
	if _, err := f.db.Pool.Exec(context.Background(), fmt.Sprintf(
		`ALTER TABLE deliveries ADD CONSTRAINT %s CHECK (order_id <> '%s' OR delivery_instructions <> 'REJECT_MIRROR') NOT VALID`,
		constraintName, f.aConfirmed,
	)); err != nil {
		t.Fatalf("add test constraint: %v", err)
	}
	rec, _ = call(mux, "PUT", "/api/portal/v1/orders/"+f.aConfirmed.String()+"/delivery-instructions",
		`{"instructions":"REJECT_MIRROR"}`, nil)
	_, _ = f.db.Pool.Exec(context.Background(), fmt.Sprintf(`ALTER TABLE deliveries DROP CONSTRAINT IF EXISTS %s`, constraintName))
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("instructions with failing delivery mirror answered %d, want 500", rec.Code)
	}
	if err := f.db.Pool.QueryRow(context.Background(),
		`SELECT site_delivery_instructions FROM orders WHERE id = $1`, f.aConfirmed).Scan(&got); err != nil {
		t.Fatalf("read back order after failed mirror: %v", err)
	}
	if got == nil || *got != "Leave at the gate, code 1234" {
		t.Errorf("order instructions after failed mirror = %v, want Leave at the gate, code 1234 (rolled back)", got)
	}

	// Three concurrent writers on a pool of four connections all complete cleanly
	// and leave the order and its open delivery stop with the same winning value.
	db4 := testutil.RequireDBMaxConns(t, 4)
	repo4 := NewRepository(db4)
	const instrContenders = 3
	instrStart := make(chan struct{})
	var instrWG sync.WaitGroup
	for i := 0; i < instrContenders; i++ {
		instrWG.Add(1)
		go func(i int) {
			defer instrWG.Done()
			m := muxFor(repo4, f.aCustomer, f.users[RoleSiteSuper], RoleSiteSuper)
			<-instrStart
			r, _ := call(m, "PUT", "/api/portal/v1/orders/"+f.aConfirmed.String()+"/delivery-instructions",
				fmt.Sprintf(`{"instructions":"Gate note %d"}`, i), nil)
			if r.Code != http.StatusOK {
				t.Errorf("concurrent instructions writer %d answered %d", i, r.Code)
			}
		}(i)
	}
	close(instrStart)
	instrWG.Wait()
	if err := f.db.Pool.QueryRow(context.Background(),
		`SELECT site_delivery_instructions FROM orders WHERE id = $1`, f.aConfirmed).Scan(&got); err != nil {
		t.Fatalf("read back order after concurrent writes: %v", err)
	}
	if err := f.db.Pool.QueryRow(context.Background(),
		`SELECT delivery_instructions FROM deliveries WHERE order_id = $1`, f.aConfirmed).Scan(&stopInstr); err != nil {
		t.Fatalf("read back stop after concurrent writes: %v", err)
	}
	if got == nil || stopInstr == nil || *got != *stopInstr {
		t.Errorf("order (%v) and stop (%v) diverged after concurrent writes", got, stopInstr)
	}

	// A fulfilled order's instructions are read only.
	rec, _ = call(mux, "PUT", "/api/portal/v1/orders/"+f.aFulfilled.String()+"/delivery-instructions",
		`{"instructions":"Too late"}`, nil)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("fulfilled order instructions answered %d, want 400", rec.Code)
	}
}

// --- Part 7: the change feeds ----------------------------------------------------

func TestChangeFeed_InvoicesQuotesDeliveriesToday(t *testing.T) {
	f := setupPro(t)
	repo := NewRepository(f.db)
	mux := muxFor(repo, f.aCustomer, f.users[RoleOwner], RoleOwner)

	for _, path := range []string{"/api/portal/v1/invoices", "/api/portal/v1/quotes", "/api/portal/v1/deliveries"} {
		rec, _ := call(mux, "GET", path, "", nil)
		if rec.Code != http.StatusOK {
			t.Fatalf("%s answered %d", path, rec.Code)
		}
		etag := rec.Header().Get("ETag")
		latest := rec.Header().Get("X-Portal-Latest-Change")
		if etag == "" || latest == "" {
			t.Fatalf("%s carries no feed pair (etag %q latest %q)", path, etag, latest)
		}
		// The conditional GET is a 304.
		rec, _ = call(mux, "GET", path, "", map[string]string{"If-None-Match": etag})
		if rec.Code != http.StatusNotModified {
			t.Errorf("%s unchanged answered %d, want 304", path, rec.Code)
		}
		// The since cursor narrows to strictly newer rows; one microsecond
		// past the newest change delivers nothing new.
		future := time.Now().UTC().Add(time.Hour).Format(time.RFC3339Nano)
		rec, _ = call(mux, "GET", path+"?since="+future, "", nil)
		if rec.Code != http.StatusOK {
			t.Fatalf("%s with since answered %d", path, rec.Code)
		}
		var rows []map[string]any
		_ = json.Unmarshal(rec.Body.Bytes(), &rows)
		if len(rows) != 0 {
			t.Errorf("%s with a future since returned %d rows, want 0", path, len(rows))
		}
	}

	// Every change feed is scoped to the customer even with a since cursor.
	muxB := muxFor(repo, f.bCustomer, uuid.New(), RoleOwner)
	for _, feedPath := range []string{
		"/api/portal/v1/invoices?since=2000-01-01T00:00:00Z",
		"/api/portal/v1/quotes?since=2000-01-01T00:00:00Z",
		"/api/portal/v1/deliveries?since=2000-01-01T00:00:00Z",
	} {
		rec, _ := call(muxB, "GET", feedPath, "", nil)
		if rec.Code != http.StatusOK {
			t.Fatalf("customer B %s answered %d", feedPath, rec.Code)
		}
		var rows []map[string]any
		_ = json.Unmarshal(rec.Body.Bytes(), &rows)
		if len(rows) != 0 {
			t.Errorf("customer B %s holds %d of A's rows, want 0", feedPath, len(rows))
		}
	}
}
