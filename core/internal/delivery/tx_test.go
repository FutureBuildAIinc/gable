// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package delivery_test

// The transaction proofs of the delivery module (ADR 0003 section 2 and the
// lane rule on transactions): a mutation, its audit row and its event are
// one fact, and every transaction runs on its own connection, never
// reaching for a second one from the pool. The concurrency tests run the
// whole service at pool size 4, so a pool use inside a transaction
// deadlocks the test instead of passing on a roomy pool.

import (
	"context"
	"errors"
	"net/http"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gablelbm/gable/internal/delivery"
	"github.com/gablelbm/gable/internal/platform/httpx"
	"github.com/gablelbm/gable/internal/testutil"
	"github.com/gablelbm/gable/pkg/audit"
	"github.com/gablelbm/gable/pkg/database"
	"github.com/gablelbm/gable/pkg/outbox"
	"github.com/google/uuid"
)

type failingEvents struct{}

func (failingEvents) Write(context.Context, outbox.Event) error {
	return errors.New("outbox insert failed")
}

type failingAudit struct{}

func (failingAudit) Log(context.Context, audit.Entry) error {
	return errors.New("audit insert failed")
}

type txFixture struct {
	t      *testing.T
	db     *database.DB
	prefix string
	branch uuid.UUID
}

func newTxFixture(t *testing.T, db *database.DB) *txFixture {
	t.Helper()
	f := &txFixture{t: t, db: db, prefix: "TXDLV-" + uuid.NewString()[:8]}
	if err := db.Pool.QueryRow(context.Background(),
		`SELECT value::uuid FROM system_settings WHERE key = 'default_branch_id'`).Scan(&f.branch); err != nil {
		t.Fatal(err)
	}
	return f
}

func (f *txFixture) service(events delivery.EventRecorder, tx delivery.TxRunner) *delivery.Service {
	svc := delivery.NewService(delivery.NewRepository(f.db)).
		WithOutbox(events).WithTxRunner(tx).WithAudit(audit.NewLogger(f.db))
	return svc
}

func (f *txFixture) good() *delivery.Service {
	return f.service(outbox.NewWriter(f.db, ""), f.db)
}

func (f *txFixture) count(sql string, args ...any) int {
	f.t.Helper()
	var n int
	if err := f.db.Pool.QueryRow(context.Background(), sql, args...).Scan(&n); err != nil {
		f.t.Fatal(err)
	}
	return n
}

func rev(n int64) delivery.Precondition { return delivery.Precondition{Revision: &n} }

func (f *txFixture) seedVehicle(t *testing.T) uuid.UUID {
	t.Helper()
	id := uuid.New()
	if _, err := f.db.Pool.Exec(context.Background(),
		`INSERT INTO vehicles (id, name, vehicle_type, license_plate) VALUES ($1, $2, 'VAN', $3)`,
		id, "Tx vehicle "+f.prefix, "TV"+uuid.NewString()[:6]); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		c := context.Background()
		_, _ = f.db.Pool.Exec(c, `DELETE FROM events_outbox WHERE entity_type = 'vehicle' AND entity_id = $1`, id)
		_, _ = f.db.Pool.Exec(c, `DELETE FROM audit_log WHERE entity_type = 'vehicle' AND entity_id = $1`, id)
		_, _ = f.db.Pool.Exec(c, `DELETE FROM vehicles WHERE id = $1`, id)
	})
	return id
}

func vehicleDraft(plate string) *delivery.VehicleDraft {
	return &delivery.VehicleDraft{Name: "Tx vehicle " + plate, VehicleType: delivery.VehicleTypeVan, LicensePlate: plate}
}

// sweepVehicles removes this test run's fleet, so a unique license plate
// never leaks into the next run.
func (f *txFixture) sweepVehicles(t *testing.T, prefix string) {
	t.Helper()
	_, _ = f.db.Pool.Exec(context.Background(),
		`DELETE FROM events_outbox WHERE entity_type = 'vehicle' AND entity_id IN (SELECT id FROM vehicles WHERE license_plate LIKE $1)`, prefix+"%")
	_, _ = f.db.Pool.Exec(context.Background(),
		`DELETE FROM audit_log WHERE entity_type = 'vehicle' AND entity_id IN (SELECT id FROM vehicles WHERE license_plate LIKE $1)`, prefix+"%")
	_, _ = f.db.Pool.Exec(context.Background(),
		`DELETE FROM vehicles WHERE license_plate LIKE $1`, prefix+"%")
}

// seedStop inserts a route on the branch's order with one stop.
func (f *txFixture) seedStop(t *testing.T) (route, stop uuid.UUID) {
	t.Helper()
	ctx := context.Background()
	var customer, order uuid.UUID
	if err := f.db.Pool.QueryRow(ctx, `
		INSERT INTO customers (id, name, account_number, primary_branch_id)
		VALUES ($1, $2, $3, $4) RETURNING id`,
		uuid.New(), "Tx stop "+f.prefix, f.prefix+uuid.NewString()[:8], f.branch).Scan(&customer); err != nil {
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
		uuid.New(), route, order).Scan(&stop); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		c := context.Background()
		_, _ = f.db.Pool.Exec(c, `DELETE FROM delivery_qty_adjustments WHERE delivery_id = $1`, stop)
		_, _ = f.db.Pool.Exec(c, `DELETE FROM events_outbox WHERE entity_id IN ($1, $2)`, route, stop)
		_, _ = f.db.Pool.Exec(c, `DELETE FROM audit_log WHERE entity_id IN ($1, $2)`, route, stop)
		_, _ = f.db.Pool.Exec(c, `DELETE FROM deliveries WHERE route_id = $1`, route)
		_, _ = f.db.Pool.Exec(c, `DELETE FROM delivery_routes WHERE id = $1`, route)
		_, _ = f.db.Pool.Exec(c, `DELETE FROM orders WHERE id = $1`, order)
		_, _ = f.db.Pool.Exec(c, `DELETE FROM customer_branches WHERE customer_id = $1`, customer)
		_, _ = f.db.Pool.Exec(c, `DELETE FROM customers WHERE id = $1`, customer)
		_, _ = f.db.Pool.Exec(c, `DELETE FROM vehicles WHERE id = $1`, vehicle)
		_, _ = f.db.Pool.Exec(c, `DELETE FROM drivers WHERE id = $1`, driver)
	})
	return route, stop
}

func stopTransition(to delivery.StopStatus) *delivery.StopTransitionDraft {
	return &delivery.StopTransitionDraft{To: to}
}

func deliveredTransition() *delivery.StopTransitionDraft {
	proof, signer := "https://x/p.jpg", "foreman"
	return &delivery.StopTransitionDraft{To: delivery.StopStatusDelivered, PODProofURL: &proof, PODSignedBy: &signer}
}

func adjustDraft() *delivery.AdjustDraft {
	notes := "short"
	return &delivery.AdjustDraft{
		AdjustedBy: uuid.New(),
		Adjustments: []delivery.Adjustment{{
			ProductID: uuid.New(), ReasonCode: "SHORT_SHIP", Notes: &notes,
		}},
	}
}

// RULE (ADR 0003 section 3): a mutation whose event cannot be recorded does
// not happen. A failing outbox rolls each kind of write back: no row, no
// audit row, no event.
func TestFailedEventWriteRollsBackEachWrite(t *testing.T) {
	testutil.LockOutboxTables(t)
	f := newTxFixture(t, testutil.RequireDB(t))
	f.sweepVehicles(t, "TF-")
	t.Cleanup(func() { f.sweepVehicles(t, "TF-") })
	svcFail := f.service(failingEvents{}, f.db)
	plain := f.good()
	ctx := context.Background()

	if _, err := svcFail.CreateVehicle(ctx, vehicleDraft("TF-"+uuid.NewString()[:8]), ""); err == nil {
		t.Error("vehicle create succeeded though its event could not be written")
	}
	if n := f.count(`SELECT count(*) FROM vehicles WHERE name LIKE 'Tx vehicle TF-%'`); n != 0 {
		t.Errorf("%d vehicles survived a rolled back create", n)
	}

	created, err := plain.CreateVehicle(ctx, vehicleDraft("TP-"+uuid.NewString()[:8]), "")
	if err != nil {
		t.Fatal(err)
	}
	next := &delivery.VehicleDraft{Name: "Tx vehicle renamed", VehicleType: delivery.VehicleTypeVan, LicensePlate: "TR-" + uuid.NewString()[:8]}
	if _, err := svcFail.UpdateVehicle(ctx, created.ID, next, rev(1), ""); err == nil {
		t.Error("vehicle update succeeded though its event could not be written")
	}
	if n := f.count(`SELECT count(*) FROM vehicles WHERE id = $1 AND name = 'Tx vehicle renamed'`, created.ID); n != 0 {
		t.Error("an update survived a rolled back event")
	}
	if n := f.count(`SELECT revision FROM vehicles WHERE id = $1`, created.ID); n != 1 {
		t.Errorf("revision moved to %d on a rolled back update", n)
	}

	route, stop := f.seedStop(t)
	if _, err := svcFail.TransitionStop(ctx, stop, deliveredTransition(), rev(1), ""); err == nil {
		t.Error("a delivered stop succeeded though its event could not be written")
	}
	if n := f.count(`SELECT count(*) FROM deliveries WHERE id = $1 AND status = 'DELIVERED'`, stop); n != 0 {
		t.Error("a stop transition survived a rolled back event")
	}

	if _, err := svcFail.AdjustDeliveryQuantity(ctx, stop, adjustDraft(), ""); err == nil {
		t.Error("an adjustment succeeded though its event could not be written")
	}
	if n := f.count(`SELECT count(*) FROM delivery_qty_adjustments WHERE delivery_id = $1`, stop); n != 0 {
		t.Error("an adjustment survived a rolled back event")
	}
	_ = route

	if err := svcFail.DeleteVehicle(ctx, created.ID, rev(1), ""); err == nil {
		t.Error("vehicle delete succeeded though its event could not be written")
	}
	if n := f.count(`SELECT count(*) FROM vehicles WHERE id = $1 AND deleted_at IS NULL`, created.ID); n != 1 {
		t.Error("a delete survived a rolled back event")
	}
}

// The same rule for the audit row, for each kind of write: a mutation whose
// audit row cannot be written does not happen.
func TestFailedAuditWriteRollsBackEachWrite(t *testing.T) {
	testutil.LockOutboxTables(t)
	f := newTxFixture(t, testutil.RequireDB(t))
	f.sweepVehicles(t, "TF-")
	t.Cleanup(func() { f.sweepVehicles(t, "TF-") })
	svc := f.service(outbox.NewWriter(f.db, ""), f.db)
	svcBad := delivery.NewService(delivery.NewRepository(f.db)).
		WithOutbox(outbox.NewWriter(f.db, "")).WithTxRunner(f.db).WithAudit(failingAudit{})

	ctx := context.Background()
	if _, err := svcBad.CreateVehicle(ctx, vehicleDraft("TF-"+uuid.NewString()[:8]), ""); err == nil {
		t.Error("vehicle create succeeded though its audit row could not be written")
	}
	if n := f.count(`SELECT count(*) FROM vehicles WHERE name LIKE 'Tx vehicle TF-%'`); n != 0 {
		t.Errorf("%d vehicles survived a rolled back create", n)
	}

	created, err := svc.CreateVehicle(ctx, vehicleDraft("TP-"+uuid.NewString()[:8]), "")
	if err != nil {
		t.Fatal(err)
	}
	next := &delivery.VehicleDraft{Name: "Tx vehicle renamed", VehicleType: delivery.VehicleTypeVan, LicensePlate: "TR-" + uuid.NewString()[:8]}
	if _, err := svcBad.UpdateVehicle(ctx, created.ID, next, rev(1), ""); err == nil {
		t.Error("vehicle update succeeded though its audit row could not be written")
	}
	if n := f.count(`SELECT count(*) FROM vehicles WHERE id = $1 AND name = 'Tx vehicle renamed'`, created.ID); n != 0 {
		t.Error("an update survived a rolled back audit row")
	}

	_, stop := f.seedStop(t)
	if _, err := svcBad.TransitionStop(ctx, stop, deliveredTransition(), rev(1), ""); err == nil {
		t.Error("a delivered stop succeeded though its audit row could not be written")
	}
	if n := f.count(`SELECT count(*) FROM deliveries WHERE id = $1 AND status = 'DELIVERED'`, stop); n != 0 {
		t.Error("a stop transition survived a rolled back audit row")
	}
}

// Three contenders at pool size 4 (the lane rule on transactions): concurrent
// creates get distinct rows and one event each, three racers on one revision
// have exactly one winner, and mixed writers beside a reader finish.
func TestConcurrency_Pool4ThreeContenders(t *testing.T) {
	testutil.LockOutboxTables(t)
	db := testutil.RequireDBMaxConns(t, 4)
	f := newTxFixture(t, db)
	svc := f.good()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	const contenders, each = 3, 4
	f.sweepVehicles(t, "TC-")
	f.sweepVehicles(t, "TR-")
	t.Cleanup(func() { f.sweepVehicles(t, "TC-"); f.sweepVehicles(t, "TR-") })

	// 1. Concurrent creates: distinct rows, one event each.
	var wg sync.WaitGroup
	errs := make(chan error, contenders*each)
	for i := 0; i < contenders; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < each; j++ {
				d := vehicleDraft("TC-" + uuid.NewString()[:8])
				if _, err := svc.CreateVehicle(ctx, d, ""); err != nil {
					errs <- err
					return
				}
			}
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatalf("concurrent create: %v", err)
	}
	if n := f.count(`SELECT count(*) FROM vehicles WHERE license_plate LIKE 'TC-%'`); n != contenders*each {
		t.Fatalf("%d vehicles created, want %d", n, contenders*each)
	}
	if n := f.count(`SELECT count(*) FROM events_outbox WHERE type = 'vehicle.created' AND entity_id IN (SELECT id FROM vehicles WHERE license_plate LIKE 'TC-%')`); n != contenders*each {
		t.Errorf("%d vehicle.created events for %d vehicles", n, contenders*each)
	}

	// 2. Three racers, one revision: exactly one wins, the others see 409.
	racer, err := svc.CreateVehicle(ctx, vehicleDraft("TR-"+uuid.NewString()[:8]), "")
	if err != nil {
		t.Fatal(err)
	}
	var won, stale, failed atomic.Int32
	var raceWG sync.WaitGroup
	for i := 0; i < contenders; i++ {
		raceWG.Add(1)
		go func() {
			defer raceWG.Done()
			d := &delivery.VehicleDraft{Name: "Winner", VehicleType: delivery.VehicleTypeVan, LicensePlate: racer.LicensePlate}
			_, err := svc.UpdateVehicle(ctx, racer.ID, d, rev(1), "")
			switch {
			case err == nil:
				won.Add(1)
			default:
				var he *httpx.Error
				if errors.As(err, &he) && he.Status == http.StatusConflict {
					stale.Add(1)
				} else {
					f.t.Errorf("racer: %v", err)
					failed.Add(1)
				}
			}
		}()
	}
	raceWG.Wait()
	if won.Load() != 1 || stale.Load() != contenders-1 || failed.Load() != 0 {
		t.Errorf("%d winners, %d stale, %d failed; want 1, %d, 0", won.Load(), stale.Load(), failed.Load(), contenders-1)
	}

	// 3. Mixed writers on distinct rows beside a reader, and three racers on
	// one stop's completion.
	route, stop := f.seedStop(t)
	_ = route
	mixedErrs := make(chan error, contenders*2)
	wg = sync.WaitGroup{}
	for i := 0; i < contenders; i++ {
		id := f.seedVehicle(t)
		wg.Add(1)
		go func() {
			defer wg.Done()
			d := &delivery.VehicleDraft{Name: "Mixed", VehicleType: delivery.VehicleTypeVan, LicensePlate: "TM-" + uuid.NewString()[:8]}
			if _, err := svc.UpdateVehicle(ctx, id, d, rev(1), ""); err != nil {
				var he *httpx.Error
				if !errors.As(err, &he) || he.Status != http.StatusConflict {
					mixedErrs <- err
				}
			}
		}()
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := svc.GetVehicle(ctx, id); err != nil {
				mixedErrs <- err
			}
		}()
	}
	wg.Wait()
	close(mixedErrs)
	for err := range mixedErrs {
		t.Fatalf("mixed writers: %v", err)
	}

	var stopWon, stopStale atomic.Int32
	var stopWG sync.WaitGroup
	for i := 0; i < contenders; i++ {
		stopWG.Add(1)
		go func() {
			defer stopWG.Done()
			_, err := svc.TransitionStop(ctx, stop, stopTransition(delivery.StopStatusFailed), rev(1), "")
			if err == nil {
				stopWon.Add(1)
			} else {
				var he *httpx.Error
				if errors.As(err, &he) && he.Status == http.StatusConflict {
					stopStale.Add(1)
				} else {
					t.Errorf("stop racer: %v", err)
				}
			}
		}()
	}
	stopWG.Wait()
	if stopWon.Load() != 1 || stopStale.Load() != contenders-1 {
		t.Errorf("stop completion: %d winners, %d stale; want 1, %d", stopWon.Load(), stopStale.Load(), contenders-1)
	}
}

// gatedTx opens as many transactions as the pool has connections and holds
// each INSIDE its transaction at a gate before fn runs. The pool is then
// empty, so any statement that goes to the pool instead of the transaction
// blocks forever. Without the gate the overlap would be luck.
type gatedTx struct {
	db      *database.DB
	want    int32
	entered int32
	gate    sync.WaitGroup
}

func newGatedTx(db *database.DB, want int) *gatedTx {
	g := &gatedTx{db: db, want: int32(want)}
	g.gate.Add(want)
	return g
}

func (g *gatedTx) RunInTx(ctx context.Context, fn func(ctx context.Context) error) error {
	return g.db.RunInTx(ctx, func(txCtx context.Context) error {
		if atomic.AddInt32(&g.entered, 1) <= g.want {
			g.gate.Done()
			g.gate.Wait()
		}
		return fn(txCtx)
	})
}

// Saturation: as many contenders as the pool has connections, held inside
// their transactions at a gate, for each kind of write. A transaction that
// reached for a second connection from the pool would leave four holders each
// waiting for a fifth that never frees, and the deadline would fire.
func TestConcurrency_Pool4SaturationNeedsNoSecondConnection(t *testing.T) {
	testutil.LockOutboxTables(t)
	db := testutil.RequireDBMaxConns(t, 4)
	f := newTxFixture(t, db)
	events := outbox.NewWriter(f.db, "")
	plain := f.service(events, f.db)

	ctx, cancel := context.WithTimeout(context.Background(), 40*time.Second)
	defer cancel()

	const contenders = 4
	f.sweepVehicles(t, "TS-")
	f.sweepVehicles(t, "TG-")
	t.Cleanup(func() { f.sweepVehicles(t, "TS-"); f.sweepVehicles(t, "TG-") })
	var vehicles []uuid.UUID
	for i := 0; i < contenders; i++ {
		v, err := plain.CreateVehicle(ctx, vehicleDraft("TS-"+uuid.NewString()[:8]), "")
		if err != nil {
			t.Fatal(err)
		}
		vehicles = append(vehicles, v.ID)
	}
	_, stop := f.seedStop(t)

	phase := func(name string, run func(svc *delivery.Service, i int) error) {
		t.Helper()
		svc := f.service(events, newGatedTx(db, contenders))
		var wg sync.WaitGroup
		errs := make(chan error, contenders)
		for i := 0; i < contenders; i++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				if err := run(svc, i); err != nil {
					errs <- err
				}
			}()
		}
		wg.Wait()
		close(errs)
		for err := range errs {
			t.Errorf("%s: %v", name, err)
		}
	}

	phase("vehicle create", func(svc *delivery.Service, _ int) error {
		_, err := svc.CreateVehicle(ctx, vehicleDraft("TG-"+uuid.NewString()[:8]), "")
		return err
	})
	phase("vehicle update", func(svc *delivery.Service, i int) error {
		d := &delivery.VehicleDraft{Name: "Gated", VehicleType: delivery.VehicleTypeVan, LicensePlate: "TS-" + uuid.NewString()[:8]}
		_, err := svc.UpdateVehicle(ctx, vehicles[i], d, rev(1), "")
		return err
	})
	phase("stop adjust", func(svc *delivery.Service, _ int) error {
		_, err := svc.AdjustDeliveryQuantity(ctx, stop, adjustDraft(), "")
		return err
	})
}
