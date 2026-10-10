// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package delivery_test

// The rollback rule (PR 70 review round 1 P3-2) for every remaining write:
// a mutation whose event cannot be recorded does not happen (no row, no
// audit row, no event), and the same for an audit row that cannot be
// written. The existing tx_test covers vehicle writes, stop transitions and
// adjustments. This file extends the proof to driver writes, route creation,
// route transitions, reorder, optimize, assign and the photo attaches.

import (
	"context"
	"errors"
	"testing"

	"github.com/gablelbm/gable/internal/delivery"
	"github.com/gablelbm/gable/internal/testutil"
	"github.com/gablelbm/gable/pkg/outbox"
	"github.com/google/uuid"
)

// photoProofURL is a stand-in proof URL for the photo attach tests.
const photoProofURL = "https://x/p.jpg"

// driverDraft returns a parsed driver write with a fresh license number.
func driverDraft(name string) *delivery.DriverDraft {
	return &delivery.DriverDraft{Name: name, Status: delivery.DriverStatusActive}
}

// routeDraft returns a parsed route write for the test fleet.
func routeDraft(vehicle, driver uuid.UUID) *delivery.RouteDraft {
	return &delivery.RouteDraft{VehicleID: vehicle, DriverID: driver, ScheduledDate: "2030-01-15"}
}

// RULE (ADR 0003 section 3): the event is the last write of the
// transaction. Every kind of write that the existing tx_test did not cover
// is here: driver create, update, delete; route create; route transition
// (dispatch, complete); reorder; optimize; assign; and the photo attaches
// (vehicle photo, POD photo). A failing outbox rolls each back: no row,
// no audit row, no event.
func TestFailedEventWriteRollsBackEveryWrite(t *testing.T) {
	testutil.LockOutboxTables(t)
	f := newTxFixture(t, testutil.RequireDB(t))
	f.sweepVehicles(t, "RF-")
	f.sweepDrivers(t, "RF-")
	t.Cleanup(func() { f.sweepVehicles(t, "RF-"); f.sweepDrivers(t, "RF-") })
	svcFail := f.service(failingEvents{}, f.db)
	plain := f.good()
	ctx := context.Background()

	// (1) Driver create: no driver row, no audit row, no event.
	drvName := "RF-" + uuid.NewString()[:8]
	if _, err := svcFail.CreateDriver(ctx, driverDraft(drvName), ""); err == nil {
		t.Error("driver create succeeded though its event could not be written")
	}
	if n := f.count(`SELECT count(*) FROM drivers WHERE name = $1`, drvName); n != 0 {
		t.Errorf("%d driver survived a rolled back create", n)
	}

	// (2) Driver update.
	drv, err := plain.CreateDriver(ctx, driverDraft("RG-"+uuid.NewString()[:8]), "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svcFail.UpdateDriver(ctx, drv.ID, &delivery.DriverDraft{
		Name: "Renamed", Status: delivery.DriverStatusActive,
	}, rev(1), ""); err == nil {
		t.Error("driver update succeeded though its event could not be written")
	}
	if n := f.count(`SELECT count(*) FROM drivers WHERE id = $1 AND name = 'Renamed'`, drv.ID); n != 0 {
		t.Error("an update survived a rolled back event")
	}

	// (3) Driver delete.
	if err := svcFail.DeleteDriver(ctx, drv.ID, rev(1), ""); err == nil {
		t.Error("driver delete succeeded though its event could not be written")
	}
	if n := f.count(`SELECT count(*) FROM drivers WHERE id = $1 AND deleted_at IS NULL`, drv.ID); n != 1 {
		t.Error("a delete survived a rolled back event")
	}

	// (4) Route create.
	vehicle := f.seedVehicle(t)
	driver := f.seedDriver(t)
	if _, err := svcFail.CreateRoute(ctx, routeDraft(vehicle, driver), ""); err == nil {
		t.Error("route create succeeded though its event could not be written")
	}
	if n := f.count(`SELECT count(*) FROM delivery_routes WHERE vehicle_id = $1`, vehicle); n != 0 {
		t.Errorf("%d routes survived a rolled back create", n)
	}

	// (5) Route transition (dispatch): seed a route, ask the failing service
	// to dispatch it; no status change, no event, no audit row.
	route, stop := f.seedRouteWithStop(t, vehicle, driver)
	if _, err := svcFail.TransitionRoute(ctx, route, &delivery.RouteTransitionDraft{
		To: delivery.RouteStatusInTransit,
	}, rev(1), ""); err == nil {
		t.Error("route dispatch succeeded though its event could not be written")
	}
	if n := f.count(`SELECT count(*) FROM delivery_routes WHERE id = $1 AND status = 'IN_TRANSIT'`, route); n != 0 {
		t.Error("a route dispatch survived a rolled back event")
	}

	// (6) ReorderStops: a route with stops, the failing service tries to
	// renumber them; the route's revision must NOT move, no audit row,
	// no event.
	routeRev0 := f.routeRevision(t, route)
	if _, err := svcFail.ReorderStops(ctx, route, &delivery.ReorderDraft{
		OrderedDeliveryIDs: []uuid.UUID{stop},
	}, rev(routeRev0), ""); err == nil {
		t.Error("reorder succeeded though its event could not be written")
	}
	if f.routeRevision(t, route) != routeRev0 {
		t.Error("a reorder moved the route revision despite the rolled back event")
	}

	// (7) OptimizeRoute: same route; the revision must NOT move.
	if _, err := svcFail.OptimizeRoute(ctx, route, rev(routeRev0), ""); err == nil {
		t.Error("optimize succeeded though its event could not be written")
	}
	if f.routeRevision(t, route) != routeRev0 {
		t.Error("an optimize moved the route revision despite the rolled back event")
	}

	// (8) AssignOrderToRoute: a fresh route and a fresh order, the failing
	// service tries to assign; the route revision must NOT move and no
	// delivery row may exist on the route.
	fRoute, fOrder := f.seedNextRoute(t)
	fRouteRev0 := f.routeRevision(t, fRoute)
	if _, _, err := svcFail.AssignOrderToRoute(ctx, &delivery.AssignStopDraft{
		RouteID: fRoute, OrderID: fOrder,
	}, ""); err == nil {
		t.Error("assign succeeded though its event could not be written")
	}
	if f.routeRevision(t, fRoute) != fRouteRev0 {
		t.Error("an assign moved the route revision despite the rolled back event")
	}
	if n := f.count(`SELECT count(*) FROM deliveries WHERE route_id = $1`, fRoute); n != 0 {
		t.Errorf("%d deliveries survived a rolled back assign", n)
	}

	// (9) SetVehiclePhoto: attach a photo to a vehicle. The photo URL is
	// updated, the revision moves. The failing event must roll the photo
	// write back too.
	photoVeh, err := plain.CreateVehicle(ctx, vehicleDraft("RP-"+uuid.NewString()[:8]), "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svcFail.SetVehiclePhoto(ctx, photoVeh.ID, photoProofURL, ""); err == nil {
		t.Error("vehicle photo attach succeeded though its event could not be written")
	}
	if n := f.count(`SELECT count(*) FROM vehicles WHERE id = $1 AND photo_url = $2`, photoVeh.ID, photoProofURL); n != 0 {
		t.Error("a photo attach survived a rolled back event")
	}

	// (10) UploadPODPhoto: attach a POD photo to a stop.
	if _, _, err := svcFail.UploadPODPhoto(ctx, stop, photoProofURL, "site", ""); err == nil {
		t.Error("POD photo attach succeeded though its event could not be written")
	}
	if n := f.count(`SELECT count(*) FROM delivery_pod_photos WHERE delivery_id = $1`, stop); n != 0 {
		t.Errorf("%d POD photos survived a rolled back event", n)
	}
}

// The audit row is the same rule for the remaining writes: a mutation whose
// audit row cannot be written does not happen.
func TestFailedAuditWriteRollsBackEveryWrite(t *testing.T) {
	testutil.LockOutboxTables(t)
	f := newTxFixture(t, testutil.RequireDB(t))
	f.sweepVehicles(t, "RF-")
	f.sweepDrivers(t, "RF-")
	t.Cleanup(func() { f.sweepVehicles(t, "RF-"); f.sweepDrivers(t, "RF-") })

	svcBad := delivery.NewService(delivery.NewRepository(f.db)).
		WithOutbox(outbox.NewWriter(f.db, "")).
		WithTxRunner(f.db).
		WithAudit(failingAudit{})
	plain := f.good()
	ctx := context.Background()

	// (1) Driver create.
	drvName := "RF-" + uuid.NewString()[:8]
	if _, err := svcBad.CreateDriver(ctx, driverDraft(drvName), ""); err == nil {
		t.Error("driver create succeeded though its audit row could not be written")
	}
	if n := f.count(`SELECT count(*) FROM drivers WHERE name = $1`, drvName); n != 0 {
		t.Errorf("%d drivers survived a rolled back create", n)
	}

	// (2) Driver update.
	drv, err := plain.CreateDriver(ctx, driverDraft("RG-"+uuid.NewString()[:8]), "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svcBad.UpdateDriver(ctx, drv.ID, &delivery.DriverDraft{
		Name: "Renamed", Status: delivery.DriverStatusActive,
	}, rev(1), ""); err == nil {
		t.Error("driver update succeeded though its audit row could not be written")
	}
	if n := f.count(`SELECT count(*) FROM drivers WHERE id = $1 AND name = 'Renamed'`, drv.ID); n != 0 {
		t.Error("an update survived a rolled back audit row")
	}

	// (3) Driver delete.
	if err := svcBad.DeleteDriver(ctx, drv.ID, rev(1), ""); err == nil {
		t.Error("driver delete succeeded though its audit row could not be written")
	}
	if n := f.count(`SELECT count(*) FROM drivers WHERE id = $1 AND deleted_at IS NULL`, drv.ID); n != 1 {
		t.Error("a delete survived a rolled back audit row")
	}

	// (4) Route create.
	vehicle := f.seedVehicle(t)
	driver := f.seedDriver(t)
	if _, err := svcBad.CreateRoute(ctx, routeDraft(vehicle, driver), ""); err == nil {
		t.Error("route create succeeded though its audit row could not be written")
	}
	if n := f.count(`SELECT count(*) FROM delivery_routes WHERE vehicle_id = $1`, vehicle); n != 0 {
		t.Errorf("%d routes survived a rolled back create", n)
	}

	// (5) Route transition.
	route, stop := f.seedRouteWithStop(t, vehicle, driver)
	if _, err := svcBad.TransitionRoute(ctx, route, &delivery.RouteTransitionDraft{
		To: delivery.RouteStatusInTransit,
	}, rev(1), ""); err == nil {
		t.Error("route dispatch succeeded though its audit row could not be written")
	}
	if n := f.count(`SELECT count(*) FROM delivery_routes WHERE id = $1 AND status = 'IN_TRANSIT'`, route); n != 0 {
		t.Error("a route dispatch survived a rolled back audit row")
	}

	// (6) ReorderStops.
	routeRev0 := f.routeRevision(t, route)
	if _, err := svcBad.ReorderStops(ctx, route, &delivery.ReorderDraft{
		OrderedDeliveryIDs: []uuid.UUID{stop},
	}, rev(routeRev0), ""); err == nil {
		t.Error("reorder succeeded though its audit row could not be written")
	}
	if f.routeRevision(t, route) != routeRev0 {
		t.Error("a reorder moved the route revision despite the rolled back audit row")
	}

	// (7) OptimizeRoute.
	if _, err := svcBad.OptimizeRoute(ctx, route, rev(routeRev0), ""); err == nil {
		t.Error("optimize succeeded though its audit row could not be written")
	}
	if f.routeRevision(t, route) != routeRev0 {
		t.Error("an optimize moved the route revision despite the rolled back audit row")
	}

	// (8) AssignOrderToRoute.
	fRoute, fOrder := f.seedNextRoute(t)
	fRouteRev0 := f.routeRevision(t, fRoute)
	if _, _, err := svcBad.AssignOrderToRoute(ctx, &delivery.AssignStopDraft{
		RouteID: fRoute, OrderID: fOrder,
	}, ""); err == nil {
		t.Error("assign succeeded though its audit row could not be written")
	}
	if f.routeRevision(t, fRoute) != fRouteRev0 {
		t.Error("an assign moved the route revision despite the rolled back audit row")
	}
	if n := f.count(`SELECT count(*) FROM deliveries WHERE route_id = $1`, fRoute); n != 0 {
		t.Errorf("%d deliveries survived a rolled back assign", n)
	}

	// (9) SetVehiclePhoto.
	photoVeh, err := plain.CreateVehicle(ctx, vehicleDraft("RP-"+uuid.NewString()[:8]), "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svcBad.SetVehiclePhoto(ctx, photoVeh.ID, photoProofURL, ""); err == nil {
		t.Error("vehicle photo attach succeeded though its audit row could not be written")
	}
	if n := f.count(`SELECT count(*) FROM vehicles WHERE id = $1 AND photo_url = $2`, photoVeh.ID, photoProofURL); n != 0 {
		t.Error("a photo attach survived a rolled back audit row")
	}

	// (10) UploadPODPhoto.
	if _, _, err := svcBad.UploadPODPhoto(ctx, stop, photoProofURL, "site", ""); err == nil {
		t.Error("POD photo attach succeeded though its audit row could not be written")
	}
	if n := f.count(`SELECT count(*) FROM delivery_pod_photos WHERE delivery_id = $1`, stop); n != 0 {
		t.Errorf("%d POD photos survived a rolled back audit row", n)
	}
}

// routeRevision returns the route's current revision. A test helper.
func (f *txFixture) routeRevision(t *testing.T, route uuid.UUID) int64 {
	t.Helper()
	var rev int64
	if err := f.db.Pool.QueryRow(context.Background(),
		`SELECT revision FROM delivery_routes WHERE id = $1`, route).Scan(&rev); err != nil {
		t.Fatal(err)
	}
	return rev
}

// seedRouteWithStop returns a fresh route with one stop, on a brand-new
// vehicle and driver, so each test phase starts clean.
func (f *txFixture) seedRouteWithStop(t *testing.T, vehicle, driver uuid.UUID) (route, stop uuid.UUID) {
	t.Helper()
	ctx := context.Background()
	var customer, order uuid.UUID
	if err := f.db.Pool.QueryRow(ctx, `
		INSERT INTO customers (id, name, account_number, primary_branch_id)
		VALUES ($1, $2, $3, $4) RETURNING id`,
		uuid.New(), "Tx rb "+f.prefix, f.prefix+uuid.NewString()[:8], f.branch).Scan(&customer); err != nil {
		t.Fatal(err)
	}
	if _, err := f.db.Pool.Exec(ctx,
		`INSERT INTO customer_branches (customer_id, branch_id) VALUES ($1, $2)`, customer, f.branch); err != nil {
		t.Fatal(err)
	}
	if err := f.db.Pool.QueryRow(ctx, `
		INSERT INTO orders (id, customer_id, branch_id, status, total_amount, delivery_type, currency)
		VALUES ($1, $2, $3, 'CONFIRMED', 10, 'DELIVERY', 'USD') RETURNING id`,
		uuid.New(), customer, f.branch).Scan(&order); err != nil {
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
		_, _ = f.db.Pool.Exec(c, `DELETE FROM delivery_pod_photos WHERE delivery_id = $1`, stop)
		_, _ = f.db.Pool.Exec(c, `DELETE FROM delivery_qty_adjustments WHERE delivery_id = $1`, stop)
		_, _ = f.db.Pool.Exec(c, `DELETE FROM events_outbox WHERE entity_id IN ($1, $2)`, route, stop)
		_, _ = f.db.Pool.Exec(c, `DELETE FROM audit_log WHERE entity_id IN ($1, $2)`, route, stop)
		_, _ = f.db.Pool.Exec(c, `DELETE FROM deliveries WHERE route_id = $1`, route)
		_, _ = f.db.Pool.Exec(c, `DELETE FROM delivery_routes WHERE id = $1`, route)
		_, _ = f.db.Pool.Exec(c, `DELETE FROM orders WHERE id = $1`, order)
		_, _ = f.db.Pool.Exec(c, `DELETE FROM customer_branches WHERE customer_id = $1`, customer)
		_, _ = f.db.Pool.Exec(c, `DELETE FROM customers WHERE id = $1`, customer)
	})
	return route, stop
}

// seedNextRoute returns a fresh route and a fresh order that has not been
// assigned; used by the rollback assign checks.
func (f *txFixture) seedNextRoute(t *testing.T) (route, order uuid.UUID) {
	t.Helper()
	ctx := context.Background()
	var customer uuid.UUID
	if err := f.db.Pool.QueryRow(ctx, `
		INSERT INTO customers (id, name, account_number, primary_branch_id)
		VALUES ($1, $2, $3, $4) RETURNING id`,
		uuid.New(), "Tx next "+f.prefix, f.prefix+uuid.NewString()[:8], f.branch).Scan(&customer); err != nil {
		t.Fatal(err)
	}
	if _, err := f.db.Pool.Exec(ctx,
		`INSERT INTO customer_branches (customer_id, branch_id) VALUES ($1, $2)`, customer, f.branch); err != nil {
		t.Fatal(err)
	}
	if err := f.db.Pool.QueryRow(ctx, `
		INSERT INTO orders (id, customer_id, branch_id, status, total_amount, delivery_type, currency)
		VALUES ($1, $2, $3, 'CONFIRMED', 10, 'DELIVERY', 'USD') RETURNING id`,
		uuid.New(), customer, f.branch).Scan(&order); err != nil {
		t.Fatal(err)
	}
	vehicle := f.seedVehicle(t)
	driver := f.seedDriver(t)
	if err := f.db.Pool.QueryRow(ctx, `
		INSERT INTO delivery_routes (id, vehicle_id, driver_id, scheduled_date, status)
		VALUES ($1, $2, $3, CURRENT_DATE, 'DRAFT') RETURNING id`,
		uuid.New(), vehicle, driver).Scan(&route); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		c := context.Background()
		_, _ = f.db.Pool.Exec(c, `DELETE FROM deliveries WHERE route_id = $1`, route)
		_, _ = f.db.Pool.Exec(c, `DELETE FROM delivery_routes WHERE id = $1`, route)
		_, _ = f.db.Pool.Exec(c, `DELETE FROM orders WHERE id = $1`, order)
		_, _ = f.db.Pool.Exec(c, `DELETE FROM customer_branches WHERE customer_id = $1`, customer)
		_, _ = f.db.Pool.Exec(c, `DELETE FROM customers WHERE id = $1`, customer)
	})
	return route, order
}

// seedDriver inserts a fresh driver and returns its id.
func (f *txFixture) seedDriver(t *testing.T) uuid.UUID {
	t.Helper()
	id := uuid.New()
	if _, err := f.db.Pool.Exec(context.Background(),
		`INSERT INTO drivers (id, name, license_number) VALUES ($1, $2, $3)`,
		id, "Tx rollback seed", f.prefix+" "+uuid.NewString()[:6]); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = f.db.Pool.Exec(context.Background(), `DELETE FROM drivers WHERE id = $1`, id)
	})
	return id
}

// sweepDrivers removes this test run's drivers, so a unique license number
// never leaks into the next run.
func (f *txFixture) sweepDrivers(t *testing.T, prefix string) {
	t.Helper()
	_, _ = f.db.Pool.Exec(context.Background(),
		`DELETE FROM events_outbox WHERE entity_type = 'driver' AND entity_id IN (SELECT id FROM drivers WHERE license_number LIKE $1)`, prefix+"%")
	_, _ = f.db.Pool.Exec(context.Background(),
		`DELETE FROM audit_log WHERE entity_type = 'driver' AND entity_id IN (SELECT id FROM drivers WHERE license_number LIKE $1)`, prefix+"%")
	_, _ = f.db.Pool.Exec(context.Background(),
		`DELETE FROM drivers WHERE license_number LIKE $1`, prefix+"%")
}

// Compile-time anchor: the tx test uses errors via failingEvents / failingAudit
// already declared in tx_test.go; this file needs nothing from errors.
var _ = errors.New
