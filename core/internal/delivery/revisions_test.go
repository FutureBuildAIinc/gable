// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package delivery_test

// The revision check on every write that takes one (PR 70 review round 1
// P2-3): a stale `If-Match` or body revision on a write that calls
// `httpx.CheckRevision` answers 409 stale_revision. The eight writes
// covered by the case are DeleteVehicle, UpdateVehicle, UpdateDriver,
// DeleteDriver, TransitionRoute on an otherwise allowed edge, TransitionStop
// on a still pending stop, ReorderStops and OptimizeRoute.
//
// Each case here kills the mutant the reviewer names: commenting out the
// `httpx.CheckRevision` call in the service makes the case green (the
// write proceeds and no error is returned) where the test expects 409.
// The PR runs the table green; the mutation test step is a hand step the
// reviewer does by hand, captured in the report.

import (
	"context"
	"errors"
	"net/http"
	"testing"

	"github.com/gablelbm/gable/internal/delivery"
	"github.com/gablelbm/gable/internal/platform/httpx"
	"github.com/gablelbm/gable/internal/testutil"
	"github.com/gablelbm/gable/pkg/audit"
	"github.com/gablelbm/gable/pkg/outbox"
	"github.com/google/uuid"
)

// newRevisionsFixture builds the same fixture the wire tests use, but the
// caller drives the service directly through the package API so this test
// can craft each precondition shape.
func newRevisionsFixture(t *testing.T) (*txFixture, *delivery.Service) {
	t.Helper()
	db := testutil.RequireDB(t)
	f := newTxFixture(t, db)
	svc := delivery.NewService(delivery.NewRepository(db)).
		WithOutbox(outbox.NewWriter(db, "")).
		WithTxRunner(db).
		WithAudit(audit.NewLogger(db))
	return f, svc
}

// expectStaleRevision calls fn and asserts the error is a 409 stale_revision.
func expectStaleRevision(t *testing.T, err error) {
	t.Helper()
	if err == nil {
		t.Fatal("expected 409 stale_revision, got nil")
	}
	var he *httpx.Error
	if !errors.As(err, &he) {
		t.Fatalf("expected httpx.Error, got %v", err)
	}
	if he.Status != http.StatusConflict || he.Code != httpx.CodeStaleRevision {
		t.Errorf("status=%d code=%s, want 409 stale_revision", he.Status, he.Code)
	}
}

// seedRevisionRoute creates a route with one stop in status PENDING so the
// stop can be transitioned, reordered and optimized.
func seedRevisionRoute(t *testing.T, f *txFixture) (route, stop uuid.UUID, vehicle, driver uuid.UUID) {
	t.Helper()
	ctx := context.Background()
	var customer uuid.UUID
	if err := f.db.Pool.QueryRow(ctx, `
		INSERT INTO customers (id, name, account_number, primary_branch_id)
		VALUES ($1, $2, $3, $4) RETURNING id`,
		uuid.New(), "Rev test "+f.prefix, f.prefix+uuid.NewString()[:8], f.branch).Scan(&customer); err != nil {
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
	vehicle = uuid.New()
	driver = uuid.New()
	if _, err := f.db.Pool.Exec(ctx,
		`INSERT INTO vehicles (id, name, vehicle_type, license_plate) VALUES ($1, 'Rev van', 'VAN', $2)`,
		vehicle, "RV"+uuid.NewString()[:6]); err != nil {
		t.Fatal(err)
	}
	if _, err := f.db.Pool.Exec(ctx,
		`INSERT INTO drivers (id, name, license_number) VALUES ($1, 'Rev driver', $2)`,
		driver, "RD"+uuid.NewString()[:6]); err != nil {
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
		_, _ = f.db.Pool.Exec(c, `DELETE FROM delivery_qty_adjustments WHERE delivery_id = $1`, stop)
		_, _ = f.db.Pool.Exec(c, `DELETE FROM delivery_pod_photos WHERE delivery_id = $1`, stop)
		_, _ = f.db.Pool.Exec(c, `DELETE FROM events_outbox WHERE entity_id IN ($1, $2, $3, $4)`, route, stop, vehicle, driver)
		_, _ = f.db.Pool.Exec(c, `DELETE FROM audit_log WHERE entity_id IN ($1, $2, $3, $4)`, route, stop, vehicle, driver)
		_, _ = f.db.Pool.Exec(c, `DELETE FROM deliveries WHERE route_id = $1`, route)
		_, _ = f.db.Pool.Exec(c, `DELETE FROM delivery_routes WHERE id = $1`, route)
		_, _ = f.db.Pool.Exec(c, `DELETE FROM orders WHERE id = $1`, order)
		_, _ = f.db.Pool.Exec(c, `DELETE FROM customer_branches WHERE customer_id = $1`, customer)
		_, _ = f.db.Pool.Exec(c, `DELETE FROM customers WHERE id = $1`, customer)
		_, _ = f.db.Pool.Exec(c, `DELETE FROM vehicles WHERE id = $1`, vehicle)
		_, _ = f.db.Pool.Exec(c, `DELETE FROM drivers WHERE id = $1`, driver)
	})
	return route, stop, vehicle, driver
}

// Each case: a stale revision on the precondition means 409 stale_revision.
// The live failure this rules out: the service wrote anyway because the
// revision check was bypassed or lost.
func TestStaleRevisionOnEveryWriteThatCallsCheckRevision(t *testing.T) {
	testutil.LockOutboxTables(t)
	f, svc := newRevisionsFixture(t)
	ctx := context.Background()

	// (1) DeleteVehicle: take a vehicle, ask for an out-of-date body revision.
	veh, err := svc.CreateVehicle(ctx, &delivery.VehicleDraft{
		Name: "Rev veh", VehicleType: delivery.VehicleTypeVan, LicensePlate: "RV-" + uuid.NewString()[:6],
	}, "")
	if err != nil {
		t.Fatal(err)
	}
	stale := int64(0)
	expectStaleRevision(t, svc.DeleteVehicle(ctx, veh.ID, delivery.Precondition{Revision: &stale}, ""))

	// (2) UpdateVehicle: stale body revision on a create that succeeded.
	veh2, err := svc.CreateVehicle(ctx, &delivery.VehicleDraft{
		Name: "Rev upd", VehicleType: delivery.VehicleTypeVan, LicensePlate: "RU-" + uuid.NewString()[:6],
	}, "")
	if err != nil {
		t.Fatal(err)
	}
	stale2 := int64(0)
	_, err = svc.UpdateVehicle(ctx, veh2.ID, &delivery.VehicleDraft{
		Name: "Renamed", VehicleType: delivery.VehicleTypeVan, LicensePlate: veh2.LicensePlate,
	}, delivery.Precondition{Revision: &stale2}, "")
	expectStaleRevision(t, err)

	// (3) UpdateDriver.
	drv, err := svc.CreateDriver(ctx, &delivery.DriverDraft{Name: "Rev driver"}, "")
	if err != nil {
		t.Fatal(err)
	}
	stale3 := int64(0)
	_, err = svc.UpdateDriver(ctx, drv.ID, &delivery.DriverDraft{
		Name: "Renamed driver", Status: delivery.DriverStatusActive,
	}, delivery.Precondition{Revision: &stale3}, "")
	expectStaleRevision(t, err)

	// (4) DeleteDriver.
	drv2, err := svc.CreateDriver(ctx, &delivery.DriverDraft{Name: "Rev driver 2"}, "")
	if err != nil {
		t.Fatal(err)
	}
	stale4 := int64(0)
	expectStaleRevision(t, svc.DeleteDriver(ctx, drv2.ID, delivery.Precondition{Revision: &stale4}, ""))

	// (5) TransitionRoute on an otherwise allowed edge: draft -> in_transit
	// asks for a stale revision. The dispatch itself is valid; the
	// revision check is what fires.
	route, stop, _, _ := seedRevisionRoute(t, f)
	stale5 := int64(0)
	_, err = svc.TransitionRoute(ctx, route, &delivery.RouteTransitionDraft{
		To: delivery.RouteStatusInTransit,
	}, delivery.Precondition{Revision: &stale5}, "")
	expectStaleRevision(t, err)

	// (6) TransitionStop on a still pending stop: stale revision.
	stale6 := int64(0)
	_, err = svc.TransitionStop(ctx, stop, deliveredTransition(), delivery.Precondition{Revision: &stale6}, "")
	expectStaleRevision(t, err)

	// (7) ReorderStops: stale If-Match on the route.
	stale7 := int64(0)
	_, err = svc.ReorderStops(ctx, route, &delivery.ReorderDraft{
		OrderedDeliveryIDs: []uuid.UUID{stop},
	}, delivery.Precondition{Revision: &stale7}, "")
	expectStaleRevision(t, err)

	// (8) OptimizeRoute: stale If-Match on the route.
	stale8 := int64(0)
	_, err = svc.OptimizeRoute(ctx, route, delivery.Precondition{Revision: &stale8}, "")
	expectStaleRevision(t, err)
}
