// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package delivery_test

// PR 79 review round 1 P1: TransitionStop used to lock the stop first and
// then the route through LockRouteForStopTransition; ReorderStops and
// OptimizeRoute lock the route first and then take the row lock on each
// stop the reorder or optimize touches (ReorderRouteDeliveries runs one
// UPDATE per stop). The two lock orders form a cycle and the pool exhausts
// to a deadlock 40P01 once the routes carry one stop and the test pool is
// capped at 4: the probe in the brief, pool 4, three contenders (two
// TransitionStop and one ReorderStops) over many iterations, asserting
// zero 40P01 and no lost update.
//
// The probe is committed alongside the lock-order fix (which it drove) so
// the regression is locked in. A second probe with the same shape uses
// OptimizeRoute in place of ReorderStops; OptimizeRoute reads the stops
// via AllDeliveriesByRoute before its transaction and then
// ReorderRouteDeliveries under the route lock, so its lock order matches
// ReorderStops on the writes.

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gablelbm/gable/internal/delivery"
	"github.com/gablelbm/gable/internal/testutil"
	"github.com/gablelbm/gable/pkg/outbox"
	"github.com/google/uuid"
)

// TestTransitionStopVsReorder_NoDeadlock is the committed form of the
// PR 79 review round 1 P1 probe. The pool is capped at four connections
// (the brief's bound) so the deadlock surfaces immediately on the unfixed
// code. With the lock-order fix the probe runs to completion with zero
// 40P01s and every iteration commits exactly one writer (revision 2).
func TestTransitionStopVsReorder_NoDeadlock(t *testing.T) {
	testutil.LockOutboxTables(t)
	db := testutil.RequireDBMaxConns(t, 4)
	f := newTxFixture(t, db)
	svc := f.service(outbox.NewWriter(db, ""), db)
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()

	const iters = 60
	var bad, lost int
	for i := 0; i < iters; i++ {
		route, stop := f.seedStop(t)
		var wg sync.WaitGroup
		wg.Add(3)
		var e1, e2, e3 error
		go func() {
			defer wg.Done()
			_, e1 = svc.TransitionStop(ctx, stop, deliveredTransition(), rev(1), "")
		}()
		go func() {
			defer wg.Done()
			_, e2 = svc.TransitionStop(ctx, stop, deliveredTransition(), rev(1), "")
		}()
		go func() {
			defer wg.Done()
			_, e3 = svc.ReorderStops(ctx, route, &delivery.ReorderDraft{OrderedDeliveryIDs: []uuid.UUID{stop}}, rev(1), "")
		}()
		wg.Wait()
		for _, e := range []error{e1, e2, e3} {
			if e == nil {
				continue
			}
			msg := e.Error()
			if strings.Contains(msg, "deadlock") || strings.Contains(msg, "40P01") {
				bad++
				t.Logf("iter %d: %v", i, e)
			}
		}
		var r int64
		if err := db.Pool.QueryRow(context.Background(),
			`SELECT revision FROM deliveries WHERE id = $1`, stop).Scan(&r); err != nil {
			t.Fatal(err)
		}
		if r == 1 {
			lost++
		}
	}
	if bad > 0 {
		t.Fatalf("%d iterations surfaced a deadlock 40P01; the lock order is still stop-first / route-second in TransitionStop", bad)
	}
	if lost > 0 {
		t.Fatalf("%d iterations saw the stop stay at revision 1; every iteration must commit exactly one writer", lost)
	}
}

// TestTransitionStopVsOptimize_NoDeadlock mirrors the probe with
// OptimizeRoute in place of ReorderStops. The OptimizeRoute path reads
// every stop via AllDeliveriesByRoute before its transaction and then
// takes the route lock and ReorderRouteDeliveries inside it, so the
// optimize's lock order (route first, then the stops it touches) is the
// same shape as the reorder's. The probe checks the same deadlock and
// lost-update property against optimize.
func TestTransitionStopVsOptimize_NoDeadlock(t *testing.T) {
	testutil.LockOutboxTables(t)
	db := testutil.RequireDBMaxConns(t, 4)
	f := newTxFixture(t, db)
	svc := f.service(outbox.NewWriter(db, ""), db)
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()

	const iters = 30
	var bad, lost int
	for i := 0; i < iters; i++ {
		route, stop := f.seedStop(t)
		var wg sync.WaitGroup
		wg.Add(2)
		var e1, e2 error
		go func() {
			defer wg.Done()
			_, e1 = svc.TransitionStop(ctx, stop, deliveredTransition(), rev(1), "")
		}()
		go func() {
			defer wg.Done()
			// Optimizing a one-stop route is a no-op reorder, but the
			// route lock is still taken first and that is the lock the
			// contended TransitionStop used to take second.
			_, e2 = svc.OptimizeRoute(ctx, route, rev(1), "")
		}()
		wg.Wait()
		for _, e := range []error{e1, e2} {
			if e == nil {
				continue
			}
			msg := e.Error()
			if strings.Contains(msg, "deadlock") || strings.Contains(msg, "40P01") {
				bad++
				t.Logf("iter %d: %v", i, e)
			}
		}
		var r int64
		if err := db.Pool.QueryRow(context.Background(),
			`SELECT revision FROM deliveries WHERE id = $1`, stop).Scan(&r); err != nil {
			t.Fatal(err)
		}
		if r == 1 {
			lost++
		}
	}
	if bad > 0 {
		t.Fatalf("%d iterations surfaced a deadlock 40P01 against OptimizeRoute", bad)
	}
	if lost > 0 {
		t.Fatalf("%d iterations saw the stop stay at revision 1 against OptimizeRoute", lost)
	}
}