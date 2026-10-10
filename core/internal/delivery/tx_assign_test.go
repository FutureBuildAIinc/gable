// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package delivery_test

// The assign's transactional proof at pool size 4 with three contenders
// (PR 70 review round 2 P2-1): the route row is locked inside the
// transaction, the next stop sequence and the route status are read under
// that lock, a completed or cancelled route refuses further assigns with
// 409 invalid_state_transition, and the route's revision moves on every
// successful one. Three cases through the real wiring:
//   1. an assign racing a route completion never lands a pending stop on a
//      completed route;
//   2. two assigns onto one route take distinct sequences, each through the
//      lock;
//   3. an assign onto a route already in a terminal status is refused.

import (
	"context"
	"errors"
	"net/http"
	"sync"
	"testing"
	"time"

	"github.com/gablelbm/gable/internal/delivery"
	"github.com/gablelbm/gable/internal/platform/httpx"
	"github.com/gablelbm/gable/internal/testutil"
	"github.com/gablelbm/gable/pkg/outbox"
	"github.com/google/uuid"
)

// seedThreeOrders plants three delivery orders for the same customer so an
// assign concurrency test has three distinct orders to slot onto a route.
func (f *txFixture) seedThreeOrders(t *testing.T) (route, stop, a, b, c uuid.UUID) {
	t.Helper()
	ctx := context.Background()
	var customer uuid.UUID
	if err := f.db.Pool.QueryRow(ctx, `
		INSERT INTO customers (id, name, account_number, primary_branch_id)
		VALUES ($1, $2, $3, $4) RETURNING id`,
		uuid.New(), "Tx three "+f.prefix, f.prefix+uuid.NewString()[:8], f.branch).Scan(&customer); err != nil {
		t.Fatal(err)
	}
	if _, err := f.db.Pool.Exec(ctx,
		`INSERT INTO customer_branches (customer_id, branch_id) VALUES ($1, $2)`, customer, f.branch); err != nil {
		t.Fatal(err)
	}
	mkOrder := func() uuid.UUID {
		var id uuid.UUID
		if err := f.db.Pool.QueryRow(ctx, `
			INSERT INTO orders (id, customer_id, branch_id, status, total_amount, delivery_type, currency)
			VALUES ($1, $2, $3, 'CONFIRMED', 10, 'DELIVERY', 'USD') RETURNING id`,
			uuid.New(), customer, f.branch).Scan(&id); err != nil {
			t.Fatal(err)
		}
		return id
	}
	a, b, c = mkOrder(), mkOrder(), mkOrder()
	vehicle, driver := uuid.New(), uuid.New()
	if _, err := f.db.Pool.Exec(ctx,
		`INSERT INTO vehicles (id, name, vehicle_type, license_plate) VALUES ($1, 'Tx fleet', 'VAN', $2)`,
		vehicle, "TV"+uuid.NewString()[:6]); err != nil {
		t.Fatal(err)
	}
	if _, err := f.db.Pool.Exec(ctx,
		`INSERT INTO drivers (id, name, license_number) VALUES ($1, 'Tx driver', $2)`,
		driver, "TD"+uuid.NewString()[:6]); err != nil {
		t.Fatal(err)
	}
	if err := f.db.Pool.QueryRow(ctx, `
		INSERT INTO delivery_routes (id, vehicle_id, driver_id, scheduled_date, status)
		VALUES ($1, $2, $3, CURRENT_DATE, 'DRAFT') RETURNING id`,
		uuid.New(), vehicle, driver).Scan(&route); err != nil {
		t.Fatal(err)
	}
	if err := f.db.Pool.QueryRow(ctx, `
		INSERT INTO deliveries (id, route_id, order_id, stop_sequence, status)
		VALUES ($1, $2, $3, 1, 'PENDING') RETURNING id`,
		uuid.New(), route, a).Scan(&stop); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		c := context.Background()
		_, _ = f.db.Pool.Exec(c, `DELETE FROM delivery_qty_adjustments WHERE delivery_id IN (SELECT id FROM deliveries WHERE route_id = $1)`, route)
		_, _ = f.db.Pool.Exec(c, `DELETE FROM events_outbox WHERE entity_id = $1`, route)
		_, _ = f.db.Pool.Exec(c, `DELETE FROM audit_log WHERE entity_id = $1`, route)
		_, _ = f.db.Pool.Exec(c, `DELETE FROM deliveries WHERE route_id = $1`, route)
		_, _ = f.db.Pool.Exec(c, `DELETE FROM delivery_routes WHERE id = $1`, route)
		_, _ = f.db.Pool.Exec(c, `DELETE FROM orders WHERE id IN ($1, $2, $3)`, a, b, c)
		_, _ = f.db.Pool.Exec(c, `DELETE FROM customer_branches WHERE customer_id = $1`, customer)
		_, _ = f.db.Pool.Exec(c, `DELETE FROM customers WHERE id = $1`, customer)
		_, _ = f.db.Pool.Exec(c, `DELETE FROM vehicles WHERE id = $1`, vehicle)
		_, _ = f.db.Pool.Exec(c, `DELETE FROM drivers WHERE id = $1`, driver)
	})
	return route, stop, a, b, c
}

// (1) An assign racing a route completion never lands a pending stop on a
// completed route (the live failure: the route completes while an assign
// is mid-flight; without the FOR UPDATE lock the assign reads a stale
// route and writes a stop under the route's check, leaving a stop the
// route no longer accepts). The assign holds the route lock while the
// completion does its own status check; whichever reaches the row first
// wins. Pool size 4 keeps the assignment honest about the second lock
// inside the path.
func TestAssign_RouteCompletionRaceNeverLeavesPendingOnCompletedRoute(t *testing.T) {
	testutil.LockOutboxTables(t)
	db := testutil.RequireDBMaxConns(t, 4)
	f := newTxFixture(t, db)
	route, stop, _, b, c := f.seedThreeOrders(t)
	svc := f.service(outbox.NewWriter(db, ""), db)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	type outcome struct {
		kind string
		err  error
	}
	const racers = 3
	results := make(chan outcome, racers)

	// First the route prep completes the only seeded stop and then dispatches +
// completes the route. The route revision moves to 1 after the stop
// transition (TouchDelivery only bumps the stop's revision, not the
// route's), so the route transition reads 1.
	if _, err := svc.TransitionStop(ctx, stop, deliveredTransition(), rev(1), ""); err != nil {
		t.Fatalf("prep stop delivered: %v", err)
	}
	if _, err := svc.TransitionRoute(ctx, route, &delivery.RouteTransitionDraft{To: delivery.RouteStatusCompleted}, rev(1), ""); err != nil {
		t.Fatalf("prep route completed: %v", err)
	}

	// Two assigns onto the now-COMPLETED route. Both must serialize under
	// the route lock and both must read COMPLETED; both must fail with 409.

	var wg sync.WaitGroup
	for i := 0; i < racers; i++ {
		oid := b
		if i == 1 {
			oid = c
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, _, err := svc.AssignOrderToRoute(ctx, &delivery.AssignStopDraft{
				RouteID: route, OrderID: oid,
			}, "")
			switch {
			case err == nil:
				results <- outcome{kind: "won"}
			default:
				var he *httpx.Error
				if errors.As(err, &he) && he.Status == http.StatusConflict {
					results <- outcome{kind: "refused", err: err}
				} else {
					results <- outcome{kind: "other", err: err}
				}
			}
		}()
	}
	wg.Wait()
	close(results)

	var won, refused, others int
	for r := range results {
		switch r.kind {
		case "won":
			won++
		case "refused":
			refused++
		default:
			others++
			t.Errorf("unexpected error: %v", r.err)
		}
	}
	if others != 0 {
		return
	}

	// No racer may have written a pending stop onto the completed route.
	var pendingOnCompleted int
	if err := f.db.Pool.QueryRow(ctx,
		`SELECT count(*) FROM deliveries WHERE route_id = $1 AND status = 'PENDING'`,
		route).Scan(&pendingOnCompleted); err != nil {
		t.Fatal(err)
	}
	if pendingOnCompleted != 0 {
		t.Errorf("%d pending stops on a completed route; the FOR UPDATE lock failed to serialize", pendingOnCompleted)
	}
	var st string
	if err := f.db.Pool.QueryRow(ctx,
		`SELECT status FROM delivery_routes WHERE id = $1`, route).Scan(&st); err != nil {
		t.Fatal(err)
	}
	if st != "COMPLETED" {
		t.Errorf("route status = %s, want COMPLETED", st)
	}
}

// (2) Two assigns onto one route, each picking the route's next sequence
// through the FOR UPDATE lock. Two assigns racing for two distinct
// stop_sequence values land both rows with distinct sequences; neither
// overwrites the other. The revision moves exactly twice.
func TestAssign_TwoAssignsOnOneRouteTakeDistinctSequences(t *testing.T) {
	testutil.LockOutboxTables(t)
	db := testutil.RequireDBMaxConns(t, 4)
	f := newTxFixture(t, db)
	route, stop, _, b, c := f.seedThreeOrders(t)
	_ = stop
	svc := f.service(outbox.NewWriter(db, ""), db)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	var wg sync.WaitGroup
	errs := make(chan error, 2)
	for _, oid := range []uuid.UUID{b, c} {
		oid := oid
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, _, err := svc.AssignOrderToRoute(ctx, &delivery.AssignStopDraft{
				RouteID: route, OrderID: oid,
			}, ""); err != nil {
				errs <- err
			}
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatalf("concurrent assign: %v", err)
	}

	rs, err := f.db.Pool.Query(ctx,
		`SELECT stop_sequence FROM deliveries WHERE route_id = $1 ORDER BY stop_sequence ASC`, route)
	if err != nil {
		t.Fatal(err)
	}
	defer rs.Close()
	seqs := map[int]bool{}
	for rs.Next() {
		var s int
		if err := rs.Scan(&s); err != nil {
			t.Fatal(err)
		}
		seqs[s] = true
	}
	if len(seqs) != 3 {
		t.Errorf("expected 3 distinct stop_sequence values, got %v", seqs)
	}
	var rev int64
	if err := f.db.Pool.QueryRow(ctx,
		`SELECT revision FROM delivery_routes WHERE id = $1`, route).Scan(&rev); err != nil {
		t.Fatal(err)
	}
	if rev < 2 {
		t.Errorf("route revision = %d after two assigns, want 2 or more", rev)
	}
}

// (3) An assign onto a route in a terminal status is refused with 409
// invalid_state_transition. The route was completed or cancelled before the
// assign starts, so the assign sees the terminal status under the FOR UPDATE
// lock and refuses. No stop is written; the failure is exactly 409.
func TestAssign_OntoTerminalRouteIs409InvalidState(t *testing.T) {
	testutil.LockOutboxTables(t)
	db := testutil.RequireDBMaxConns(t, 4)
	f := newTxFixture(t, db)
	svc := f.service(outbox.NewWriter(db, ""), db)

	terminal := []delivery.RouteStatus{delivery.RouteStatusCompleted, delivery.RouteStatusCancelled}
	for _, target := range terminal {
		t.Run(string(target), func(t *testing.T) {
			ctx := context.Background()
			var customer uuid.UUID
			if err := f.db.Pool.QueryRow(ctx, `
				INSERT INTO customers (id, name, account_number, primary_branch_id)
				VALUES ($1, $2, $3, $4) RETURNING id`,
				uuid.New(), "Tx terminal "+f.prefix, f.prefix+uuid.NewString()[:8], f.branch).Scan(&customer); err != nil {
				t.Fatal(err)
			}
			if _, err := f.db.Pool.Exec(ctx,
				`INSERT INTO customer_branches (customer_id, branch_id) VALUES ($1, $2)`, customer, f.branch); err != nil {
				t.Fatal(err)
			}
			var orderID, vehicle, driver uuid.UUID
			if err := f.db.Pool.QueryRow(ctx, `
				INSERT INTO orders (id, customer_id, branch_id, status, total_amount, delivery_type, currency)
				VALUES ($1, $2, $3, 'CONFIRMED', 10, 'DELIVERY', 'USD') RETURNING id`,
				uuid.New(), customer, f.branch).Scan(&orderID); err != nil {
				t.Fatal(err)
			}
			vehicle = uuid.New()
			if _, err := f.db.Pool.Exec(ctx,
				`INSERT INTO vehicles (id, name, vehicle_type, license_plate) VALUES ($1, 'Tx fleet', 'VAN', $2)`,
				vehicle, "TV"+uuid.NewString()[:6]); err != nil {
				t.Fatal(err)
			}
			if _, err := f.db.Pool.Exec(ctx,
				`INSERT INTO drivers (id, name, license_number) VALUES ($1, 'Tx driver', $2)`,
				driver, "TD"+uuid.NewString()[:6]); err != nil {
				t.Fatal(err)
			}
			var route uuid.UUID
			if err := f.db.Pool.QueryRow(ctx, `
				INSERT INTO delivery_routes (id, vehicle_id, driver_id, scheduled_date, status)
				VALUES ($1, $2, $3, CURRENT_DATE, $4) RETURNING id`,
				uuid.New(), vehicle, driver, target).Scan(&route); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() {
				c := context.Background()
				_, _ = f.db.Pool.Exec(c, `DELETE FROM deliveries WHERE route_id = $1`, route)
				_, _ = f.db.Pool.Exec(c, `DELETE FROM delivery_routes WHERE id = $1`, route)
				_, _ = f.db.Pool.Exec(c, `DELETE FROM orders WHERE id = $1`, orderID)
				_, _ = f.db.Pool.Exec(c, `DELETE FROM customer_branches WHERE customer_id = $1`, customer)
				_, _ = f.db.Pool.Exec(c, `DELETE FROM customers WHERE id = $1`, customer)
				_, _ = f.db.Pool.Exec(c, `DELETE FROM vehicles WHERE id = $1`, vehicle)
				_, _ = f.db.Pool.Exec(c, `DELETE FROM drivers WHERE id = $1`, driver)
			})

			_, _, err := svc.AssignOrderToRoute(ctx, &delivery.AssignStopDraft{
				RouteID: route, OrderID: orderID,
			}, "")
			var he *httpx.Error
			if !errors.As(err, &he) {
				t.Fatalf("assign on terminal route = %v, want httpx.Error", err)
			}
			if he.Status != http.StatusConflict {
				t.Errorf("status = %d, want 409", he.Status)
			}
			if he.Code != httpx.CodeInvalidStateTransition {
				t.Errorf("code = %s, want %s", he.Code, httpx.CodeInvalidStateTransition)
			}
			var n int
			if err := f.db.Pool.QueryRow(ctx,
				`SELECT count(*) FROM deliveries WHERE route_id = $1`, route).Scan(&n); err != nil {
				t.Fatal(err)
			}
			if n != 0 {
				t.Errorf("%d deliveries on a route that refused the assign", n)
			}
		})
	}
}

// Compile-time references so unused imports keep their seat in the helpers.
var _ = http.MethodGet