// PR 80 review round 1 P3-1: a service-level test over the four
// statuses that must refuse completion (DRAFT, SCHEDULED, COMPLETED
// and CANCELLED). Each is seeded by SQL into the named status, then
// TransitionRoute(to=completed) is called. Every call must answer
// 409 invalid_state_transition with the invalid_state blocker; no
// route.completed event must be written for any of them.
//
// SCHEDULED is the only mildly interesting case: the wire-level
// lifecycle never touches SCHEDULED (it dispatches directly from
// DRAFT), so a mutant 'if status != IN_TRANSIT && status !=
// SCHEDULED' would be invisible to the wire tests and only this
// service-level table kills it.
package delivery_test

import (
	"context"
	"testing"
	"time"

	"github.com/gablelbm/gable/internal/delivery"
	"github.com/gablelbm/gable/internal/platform/httpx"
	"github.com/gablelbm/gable/internal/testutil"
	"github.com/gablelbm/gable/pkg/outbox"
	"github.com/google/uuid"
)

// seedRouteInStatus mirrors seedStop but lets the test pick the route
// status (DRAFT, SCHEDULED, COMPLETED, CANCELLED). The stop is in
// PENDING so the route's GetRoute stop read returns at least one
// row; the status refusal is reached before the count checks, so
// the PENDING stop never affects the assertion.
func seedRouteInStatus(t *testing.T, f *txFixture, ipostgres string) (uuid.UUID, uuid.UUID) {
	t.Helper()
	ctx := context.Background()
	customer := uuid.New()
	if err := f.db.Pool.QueryRow(ctx, `
		INSERT INTO customers (id, name, account_number, primary_branch_id)
		VALUES ($1, $2, $3, $4) RETURNING id`,
		customer, "Tx P3-1 "+f.prefix, f.prefix+uuid.NewString()[:8], f.branch).Scan(&customer); err != nil {
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
		`INSERT INTO vehicles (id, name, vehicle_type, license_plate) VALUES ($1, 'Tx P3-1', 'VAN', $2)`,
		vehicle, "P31"+uuid.NewString()[:6]); err != nil {
		t.Fatal(err)
	}
	if _, err := f.db.Pool.Exec(ctx,
		`INSERT INTO drivers (id, name, license_number) VALUES ($1, 'Tx P3-1', $2)`,
		driver, "P31"+uuid.NewString()[:6]); err != nil {
		t.Fatal(err)
	}
	var route uuid.UUID
	if err := f.db.Pool.QueryRow(ctx, `
		INSERT INTO delivery_routes (id, vehicle_id, driver_id, scheduled_date, status)
		VALUES ($1, $2, $3, CURRENT_DATE, $4) RETURNING id`,
		uuid.New(), vehicle, driver, ipostgres).Scan(&route); err != nil {
		t.Fatal(err)
	}
	var stop uuid.UUID
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

// TestRouteComplete_RefusedFromNonInTransitStatuses covers DRAFT,
// SCHEDULED, COMPLETED and CANCELLED, each seeded by SQL into the
// named status. The complete must refuse every case with 409
// invalid_state_transition carrying the invalid_state blocker, and
// no route.completed event must be written for any case.
func TestRouteComplete_RefusedFromNonInTransitStatuses(t *testing.T) {
	db := testutil.RequireDBMaxConns(t, 4)
	f := newTxFixture(t, db)
	svc := f.service(outbox.NewWriter(db, ""), db)
	cases := []struct {
		status delivery.RouteStatus
	}{
		{delivery.RouteStatusDraft},
		{delivery.RouteStatusScheduled},
		{delivery.RouteStatusCompleted},
		{delivery.RouteStatusCancelled},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(string(tc.status), func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			route, _ := seedRouteInStatus(t, f, string(tc.status))
			// The seeded route is at revision 1; pass that as the
			// precondition. The refusal must be invalid_state, not
			// stale_revision.
			_, err := svc.TransitionRoute(ctx, route,
				&delivery.RouteTransitionDraft{To: delivery.RouteStatusCompleted},
				rev(1), "")
			if err == nil {
				t.Fatalf("complete from %s = nil, want 409 invalid_state_transition", tc.status)
			}
			e, ok := err.(*httpx.Error)
			if !ok {
				t.Fatalf("complete from %s err = %T, want *httpx.Error", tc.status, err)
			}
			if e.Code != httpx.CodeInvalidStateTransition {
				t.Errorf("complete from %s code = %s, want %s", tc.status, e.Code, httpx.CodeInvalidStateTransition)
			}
			found := false
			for _, d := range e.Details {
				if d.Code == "invalid_state" {
					found = true
					break
				}
			}
			if !found {
				t.Errorf("complete from %s blockers = %v, want invalid_state", tc.status, e.Details)
			}
			// The refused complete writes no route.completed event
			// for the route. countEvents reads events_outbox for one
			// (entity, type).
			var n int
			if err := f.db.Pool.QueryRow(context.Background(),
				`SELECT count(*) FROM events_outbox WHERE entity_id = $1 AND type = $2`,
				route, "route.completed").Scan(&n); err != nil {
				t.Fatal(err)
			}
			if n != 0 {
				t.Errorf("complete from %s wrote %d route.completed events, want 0", tc.status, n)
			}
		})
	}
}
