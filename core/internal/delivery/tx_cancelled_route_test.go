// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package delivery_test

// PR 70 review round 7 N3 (the follow-ups brief): a pending stop on a
// route that is CANCELLED (a legacy row migration 104 sets to CANCELLED
// from an unknown spelling, or any later cancellation) must not be
// deliverable: TransitionStop must refuse the transition with 409
// invalid_state_transition and a blocker naming the route, under the
// route lock the stop transition takes (the same FOR UPDATE the stop
// transition already holds in the existing path; the stop's lock is the
// route's lock through deliveries.route_id). The fulfilment enqueue
// inside the delivered branch must not run, so the order is not billed,
// and the test runs once at the service level and once through the serve
// wiring.

import (
	"context"
	"errors"
	"net/http"
	"testing"

	"github.com/gablelbm/gable/internal/platform/httpx"
	"github.com/gablelbm/gable/internal/testutil"
	"github.com/gablelbm/gable/pkg/outbox"
	"github.com/google/uuid"
)

// cancelledRouteStop plants a route on the test branch with one PENDING
// stop, then forces the route's status to CANCELLED (the value a real
// cancellation writes and migration 104 sets on unknown spellings).
func (f *txFixture) cancelledRouteStop(t *testing.T) (route, stop uuid.UUID) {
	t.Helper()
	ctx := context.Background()
	route, stop = f.seedStop(t)
	if _, err := f.db.Pool.Exec(ctx,
		`UPDATE delivery_routes SET status = 'CANCELLED' WHERE id = $1`, route); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = f.db.Pool.Exec(context.Background(),
			`UPDATE delivery_routes SET status = 'DRAFT' WHERE id = $1`, route)
	})
	return route, stop
}

// recordingFulfilment captures EnqueueFulfilment calls so the test can
// assert that a refused delivery did not queue fulfilment.
type recordingFulfilment struct {
	calls int
}

func (r *recordingFulfilment) EnqueueFulfilment(ctx context.Context, deliveryID, orderID uuid.UUID) error {
	r.calls++
	return nil
}

// Through the service, the stop on a CANCELLED route refuses a delivery
// transition with 409 invalid_state_transition and a blocker that names
// the route. The stop is unchanged in the database and no fulfilment
// request is queued.
func TestTransitionStop_RefusedWhenRouteIsCancelled(t *testing.T) {
	testutil.LockOutboxTables(t)
	db := testutil.RequireDBMaxConns(t, 4)
	f := newTxFixture(t, db)
	svc := f.service(outbox.NewWriter(db, ""), db)
	route, stop := f.cancelledRouteStop(t)
	ctx := context.Background()

	q := &recordingFulfilment{}
	svc.WithFulfilment(q, fakeOrders{})

	_, err := svc.TransitionStop(ctx, stop, deliveredTransition(), rev(1), "")
	var he *httpx.Error
	if !errors.As(err, &he) {
		t.Fatalf("delivered on a cancelled route: err = %v, want httpx.Error", err)
	}
	if he.Status != http.StatusConflict {
		t.Errorf("status = %d, want 409 (the stop on a cancelled route must be refused)", he.Status)
	}
	if he.Code != httpx.CodeInvalidStateTransition {
		t.Errorf("code = %s, want %s", he.Code, httpx.CodeInvalidStateTransition)
	}
	found := false
	for _, d := range he.Details {
		if d.Code == "route_id" && d.Message != "" {
			found = true
		}
	}
	if !found {
		t.Errorf("the 409 must carry a route_id blocker, got %v", he.Details)
	}
	if q.calls != 0 {
		t.Errorf("fulfilment was queued %d times for a refused delivery, want 0 (no bill)", q.calls)
	}
	if n := f.count(`SELECT count(*) FROM deliveries WHERE id = $1 AND status = 'DELIVERED'`, stop); n != 0 {
		t.Errorf("the stop status moved to DELIVERED though the transition was refused")
	}
	var routeStatus string
	if err := f.db.Pool.QueryRow(ctx, `SELECT status FROM delivery_routes WHERE id = $1`, route).Scan(&routeStatus); err != nil {
		t.Fatal(err)
	}
	if routeStatus != "CANCELLED" {
		t.Errorf("the route status moved to %s though the transition was refused", routeStatus)
	}
}

// The wire case: the same refusal through the serve mux the way the
// field app sees it.
func TestRoute_StopTransitionRefusedWhenRouteIsCancelled(t *testing.T) {
	f := newFixture(t)
	route, stop := f.cancelledRouteStopWire(t, f.branch)

	body := `{"to":"delivered","pod_proof_url":"https://x/p.jpg","pod_signed_by":"foreman"}`
	res := f.do(t, http.MethodPost, "/api/v1/delivery/deliveries/"+stop.String()+"/transitions", body,
		map[string]string{"If-Match": `"1"`})
	if res.status != http.StatusConflict {
		t.Fatalf("stop transition on a CANCELLED route = %d %s, want 409", res.status, res.raw)
	}
	if f.errCode(t, res) != httpx.CodeInvalidStateTransition {
		t.Errorf("code = %s, want %s", f.errCode(t, res), httpx.CodeInvalidStateTransition)
	}
	if !hasBlockerCode(t, res, "route_id") {
		t.Errorf("the 409 must carry a route_id blocker, got %v", res.body)
	}
	ctx := context.Background()
	var gotStatus string
	if err := f.db.Pool.QueryRow(ctx, `SELECT status FROM deliveries WHERE id = $1`, stop).Scan(&gotStatus); err != nil {
		t.Fatal(err)
	}
	if gotStatus != "PENDING" {
		t.Errorf("stop status = %s after refused transition, want PENDING (unchanged)", gotStatus)
	}
	var routeStatus string
	if err := f.db.Pool.QueryRow(ctx, `SELECT status FROM delivery_routes WHERE id = $1`, route).Scan(&routeStatus); err != nil {
		t.Fatal(err)
	}
	if routeStatus != "CANCELLED" {
		t.Errorf("route status = %s after refused transition, want CANCELLED (unchanged)", routeStatus)
	}
}

// cancelledRouteStopWire plants a route on the wire fixture's branch with
// one PENDING stop, then forces the route to CANCELLED.
func (f *fixture) cancelledRouteStopWire(t *testing.T, branch uuid.UUID) (route, stop uuid.UUID) {
	t.Helper()
	ctx := context.Background()
	route, stop, _ = f.seedStop(t, branch)
	if _, err := f.db.Pool.Exec(ctx,
		`UPDATE delivery_routes SET status = 'CANCELLED' WHERE id = $1`, route); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = f.db.Pool.Exec(context.Background(),
			`UPDATE delivery_routes SET status = 'DRAFT' WHERE id = $1`, route)
	})
	return route, stop
}

// hasBlockerCode walks the envelope's details and returns true if any
// detail carries the named code (a blocker is a FieldError whose `code`
// is set and whose `field` is empty; the wire's `fields` helper only
// matches `field`, so a code matcher is needed for blockers).
func hasBlockerCode(t *testing.T, r resp, code string) bool {
	t.Helper()
	env, _ := r.body["error"].(map[string]any)
	details, _ := env["details"].([]any)
	for _, d := range details {
		if m, ok := d.(map[string]any); ok {
			if c, ok := m["code"].(string); ok && c == code {
				return true
			}
		}
	}
	return false
}