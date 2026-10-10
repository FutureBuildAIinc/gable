// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

// PR 80 review round 2 P3-1: a pool-4, three-contender concurrent
// completion test. An IN_TRANSIT route whose only stop is terminal
// (the allowed path through the state machine) is completed by
// three goroutines at once. Exactly one must answer 200 and write
// one route.completed event; the other two must answer 409. All
// three send the same revision, so the losers are refused by the
// revision precondition once the winner has moved it; this test pins
// once only under contention, and the status check itself is pinned
// by the service table in tx_complete_refused_test.go.
package delivery_test

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

// seedInTransitRoute plants a DRAFT customer, vehicle, driver, route
// and one stop, then drives the stop to delivered and the route to
// IN_TRANSIT. The test runs after the helper returns; the route is
// in_transit with one terminal stop and revision 2.
func seedInTransitRoute(t *testing.T, f *txFixture) (route, stop uuid.UUID) {
	t.Helper()
	ctx := context.Background()
	var customer uuid.UUID
	if err := f.db.Pool.QueryRow(ctx, `
		INSERT INTO customers (id, name, account_number, primary_branch_id)
		VALUES ($1, $2, $3, $4) RETURNING id`,
		uuid.New(), "Tx P3-3 "+f.prefix, f.prefix+uuid.NewString()[:8], f.branch).Scan(&customer); err != nil {
		t.Fatal(err)
	}
	if _, err := f.db.Pool.Exec(ctx,
		`INSERT INTO customer_branches (customer_id, branch_id) VALUES ($1, $2)`, customer, f.branch); err != nil {
		t.Fatal(err)
	}
	var order uuid.UUID
	if err := f.db.Pool.QueryRow(ctx, `
		INSERT INTO orders (id, customer_id, branch_id, status, total_amount, delivery_type, currency)
		VALUES ($1, $2, $3, 'CONFIRMED', 10, 'DELIVERY', 'USD') RETURNING id`,
		uuid.New(), customer, f.branch).Scan(&order); err != nil {
		t.Fatal(err)
	}
	vehicle, driver := uuid.New(), uuid.New()
	if _, err := f.db.Pool.Exec(ctx,
		`INSERT INTO vehicles (id, name, vehicle_type, license_plate) VALUES ($1, 'Tx P3-3', 'VAN', $2)`,
		vehicle, "P33"+uuid.NewString()[:6]); err != nil {
		t.Fatal(err)
	}
	if _, err := f.db.Pool.Exec(ctx,
		`INSERT INTO drivers (id, name, license_number) VALUES ($1, 'Tx P3-3', $2)`,
		driver, "P33"+uuid.NewString()[:6]); err != nil {
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
		uuid.New(), route, order).Scan(&stop); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		c := context.Background()
		_, _ = f.db.Pool.Exec(c, `DELETE FROM deliveries WHERE route_id = $1`, route)
		_, _ = f.db.Pool.Exec(c, `DELETE FROM events_outbox WHERE entity_id IN ($1, $2)`, route, stop)
		_, _ = f.db.Pool.Exec(c, `DELETE FROM audit_log WHERE entity_id IN ($1, $2)`, route, stop)
		_, _ = f.db.Pool.Exec(c, `DELETE FROM delivery_routes WHERE id = $1`, route)
		_, _ = f.db.Pool.Exec(c, `DELETE FROM orders WHERE id = $1`, order)
		_, _ = f.db.Pool.Exec(c, `DELETE FROM customer_branches WHERE customer_id = $1`, customer)
		_, _ = f.db.Pool.Exec(c, `DELETE FROM customers WHERE id = $1`, customer)
		_, _ = f.db.Pool.Exec(c, `DELETE FROM vehicles WHERE id = $1`, vehicle)
		_, _ = f.db.Pool.Exec(c, `DELETE FROM drivers WHERE id = $1`, driver)
	})
	return route, stop
}

// TestRouteComplete_ConcurrentCompletionWinsExactlyOnce drives
// three concurrent TransitionRoute(to=completed) calls against the
// same in_transit route (pool 4 so the FOR UPDATE lock serializes
// them; the lock is what makes the second arrival read the already
// completed status). One must win, two must refuse with 409, and
// exactly one route.completed event must be written.
func TestRouteComplete_ConcurrentCompletionWinsExactlyOnce(t *testing.T) {
	testutil.LockOutboxTables(t)
	db := testutil.RequireDBMaxConns(t, 4)
	f := newTxFixture(t, db)
	svc := f.service(outbox.NewWriter(db, ""), db)
	route, stop := seedInTransitRoute(t, f)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	// Move the stop to terminal and the route to in_transit before
	// the race. The route's revision is 1 after the stop transition
	// (TouchDelivery only bumps the stop's revision, not the
	// route's), then dispatch moves it to 2; the completion reads 2.
	if _, err := svc.TransitionStop(ctx, stop, deliveredTransition(), rev(1), ""); err != nil {
		t.Fatalf("prep stop delivered: %v", err)
	}
	if _, err := svc.TransitionRoute(ctx, route, &delivery.RouteTransitionDraft{To: delivery.RouteStatusInTransit}, rev(1), ""); err != nil {
		t.Fatalf("prep route in_transit: %v", err)
	}

	type outcome struct {
		kind string
		err  error
	}
	const racers = 3
	results := make(chan outcome, racers)

	var wg sync.WaitGroup
	for i := 0; i < racers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := svc.TransitionRoute(ctx, route,
				&delivery.RouteTransitionDraft{To: delivery.RouteStatusCompleted},
				rev(2), "")
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
	if won != 1 {
		t.Errorf("winners = %d, want 1 (three concurrent completes must serialize under the route lock)", won)
	}
	if refused != racers-1 {
		t.Errorf("refusals = %d, want %d (the two losers must answer 409)", refused, racers-1)
	}

	// Exactly one route.completed event. PR 80 review round 2 P3-1
	// pins this count: the status check refuses the second complete
	// (the loser reads COMPLETED, not IN_TRANSIT), so the event
	// count is what the downstream de-duplicator relies on.
	var completed int
	if err := f.db.Pool.QueryRow(context.Background(),
		`SELECT count(*) FROM events_outbox WHERE entity_id = $1 AND type = 'route.completed'`,
		route).Scan(&completed); err != nil {
		t.Fatal(err)
	}
	if completed != 1 {
		t.Errorf("route.completed events = %d, want 1 (exactly one completion)", completed)
	}

	// The route is COMPLETED at the end; status check is the other
	// half of the once-only pin.
	var st string
	if err := f.db.Pool.QueryRow(context.Background(),
		`SELECT status FROM delivery_routes WHERE id = $1`, route).Scan(&st); err != nil {
		t.Fatal(err)
	}
	if st != string(delivery.RouteStatusCompleted) {
		t.Errorf("route status = %s, want %s", st, delivery.RouteStatusCompleted)
	}
}
