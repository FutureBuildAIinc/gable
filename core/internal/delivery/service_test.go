// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package delivery

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gablelbm/gable/internal/platform/httpx"
	"github.com/google/uuid"
)

// fakeRepo is the in-memory Repository the service tests run against.
type fakeRepo struct {
	routes []Route

	// Configurable inputs for the OptimizeRoute path.
	stops       []Stop
	branchID    uuid.UUID
	branchOrg   *BranchOrigin
	orderAddrs  map[uuid.UUID]string
	orderBranch map[uuid.UUID]uuid.UUID
	routeStops  map[uuid.UUID][]Stop
	fetchedStop *Stop

	// Configurable inputs for the single-stop and assign paths.
	vehicle *Vehicle

	// createdStop captures what AssignOrderToRoute handed the repository.
	createdStop *Stop

	// Captured writes for assertions.
	reorderedIDs   []uuid.UUID
	etas           map[uuid.UUID]time.Time
	setLatLng      map[uuid.UUID]LatLng
	setBranch      map[uuid.UUID]LatLng
	setBranchCalls int
}

func (m *fakeRepo) CreateVehicle(ctx context.Context, v *Vehicle) error { return nil }
func (m *fakeRepo) GetVehicle(ctx context.Context, id uuid.UUID) (*Vehicle, error) {
	return m.vehicle, nil
}
func (m *fakeRepo) ListVehicles(ctx context.Context, f FleetListFilter, wantTotal bool) ([]Vehicle, bool, *int64, error) {
	return nil, false, nil, nil
}
func (m *fakeRepo) UpdateVehicle(ctx context.Context, v *Vehicle) error { return nil }
func (m *fakeRepo) DeleteVehicle(ctx context.Context, id uuid.UUID) error {
	return nil
}
func (m *fakeRepo) LockVehicle(ctx context.Context, id uuid.UUID) error { return nil }

func (m *fakeRepo) SetVehiclePhoto(ctx context.Context, id uuid.UUID, url string) error { return nil }
func (m *fakeRepo) CreateDriver(ctx context.Context, d *Driver) error                   { return nil }
func (m *fakeRepo) GetDriver(ctx context.Context, id uuid.UUID) (*Driver, error) {
	return nil, nil
}
func (m *fakeRepo) ListDrivers(ctx context.Context, f FleetListFilter, wantTotal bool) ([]Driver, bool, *int64, error) {
	return nil, false, nil, nil
}
func (m *fakeRepo) UpdateDriver(ctx context.Context, d *Driver) error { return nil }
func (m *fakeRepo) DeleteDriver(ctx context.Context, id uuid.UUID) error {
	return nil
}
func (m *fakeRepo) LockDriver(ctx context.Context, id uuid.UUID) error       { return nil }
func (m *fakeRepo) SetDriverPhoto(ctx context.Context, id uuid.UUID, url string) error { return nil }

func (m *fakeRepo) CreateRoute(ctx context.Context, r *Route) error { return nil }
func (m *fakeRepo) GetRoute(ctx context.Context, id uuid.UUID) (*Route, error) {
	for i := range m.routes {
		if m.routes[i].ID == id {
			return &m.routes[i], nil
		}
	}
	return nil, ErrNotFound
}
func (m *fakeRepo) ListRoutes(ctx context.Context, f RouteListFilter, wantTotal bool) ([]Route, bool, *int64, error) {
	return m.routes, false, nil, nil
}
func (m *fakeRepo) TouchRoute(ctx context.Context, id uuid.UUID) error {
	for i := range m.routes {
		if m.routes[i].ID == id {
			m.routes[i].Revision++
		}
	}
	return nil
}
func (m *fakeRepo) CountDeliveriesByRoute(ctx context.Context, routeID uuid.UUID) (int64, error) {
	return int64(len(m.stops)), nil
}
func (m *fakeRepo) UpdateRouteStatus(ctx context.Context, id uuid.UUID, status RouteStatus) error {
	for i := range m.routes {
		if m.routes[i].ID == id {
			m.routes[i].Status = status
		}
	}
	return nil
}
func (m *fakeRepo) LockRoute(ctx context.Context, id uuid.UUID) error { return nil }

func (m *fakeRepo) CreateDelivery(ctx context.Context, d *Stop) error {
	m.createdStop = d
	return nil
}
func (m *fakeRepo) GetDelivery(ctx context.Context, id uuid.UUID) (*Stop, error) {
	if m.fetchedStop != nil {
		return m.fetchedStop, nil
	}
	if m.createdStop != nil && m.createdStop.ID == id {
		return m.createdStop, nil
	}
	for i := range m.stops {
		if m.stops[i].ID == id {
			return &m.stops[i], nil
		}
	}
	return nil, ErrNotFound
}
func (m *fakeRepo) ListDeliveriesByRoute(ctx context.Context, routeID uuid.UUID, f StopListFilter) ([]Stop, bool, error) {
	if m.routeStops != nil {
		return m.routeStops[routeID], false, nil
	}
	return m.stops, false, nil
}
func (m *fakeRepo) UpdateDeliveryStatus(ctx context.Context, id uuid.UUID, status StopStatus, pod *PODUpdate) error {
	for i := range m.stops {
		if m.stops[i].ID == id {
			m.stops[i].Status = status
		}
	}
	return nil
}
func (m *fakeRepo) LockDelivery(ctx context.Context, id uuid.UUID) error { return nil }
func (m *fakeRepo) ReorderRouteDeliveries(ctx context.Context, routeID uuid.UUID, deliveryIDs []uuid.UUID) error {
	m.reorderedIDs = deliveryIDs
	return nil
}
func (m *fakeRepo) ListStopsForRoutes(ctx context.Context, routeIDs []uuid.UUID) (map[uuid.UUID][]Stop, error) {
	return m.routeStops, nil
}
func (m *fakeRepo) TouchDelivery(ctx context.Context, id uuid.UUID) error { return nil }

func (m *fakeRepo) SavePODPhoto(ctx context.Context, photo *PODPhoto) error { return nil }
func (m *fakeRepo) GetPODPhotos(ctx context.Context, deliveryID uuid.UUID, f PhotoListFilter) ([]PODPhoto, bool, error) {
	return nil, false, nil
}
func (m *fakeRepo) InsertQtyAdjustments(ctx context.Context, stopID, adjustedBy uuid.UUID, lines []Adjustment) error {
	return nil
}

func (m *fakeRepo) GetRouteLoadWeight(ctx context.Context, routeID uuid.UUID) (float64, error) {
	return 0, nil
}

func (m *fakeRepo) GetOrderEstimatedWeight(ctx context.Context, orderID uuid.UUID) (float64, error) {
	return 0, nil
}

func (m *fakeRepo) GetRouteBranchID(ctx context.Context, routeID uuid.UUID) (uuid.UUID, error) {
	return m.branchID, nil
}
func (m *fakeRepo) GetBranchOrigin(ctx context.Context, branchID uuid.UUID) (*BranchOrigin, error) {
	if m.branchOrg != nil {
		return m.branchOrg, nil
	}
	return &BranchOrigin{}, nil
}
func (m *fakeRepo) SetBranchLatLng(ctx context.Context, branchID uuid.UUID, lat, lng float64) error {
	if m.setBranch == nil {
		m.setBranch = map[uuid.UUID]LatLng{}
	}
	m.setBranch[branchID] = LatLng{Lat: lat, Lng: lng}
	m.setBranchCalls++
	return nil
}
func (m *fakeRepo) GetOrderDeliveryAddress(ctx context.Context, orderID uuid.UUID) (string, error) {
	return m.orderAddrs[orderID], nil
}
func (m *fakeRepo) GetOrderBranchID(ctx context.Context, orderID uuid.UUID) (uuid.UUID, error) {
	if b, ok := m.orderBranch[orderID]; ok {
		return b, nil
	}
	return uuid.Nil, ErrNotFound
}
func (m *fakeRepo) SetDeliveryLatLng(ctx context.Context, deliveryID uuid.UUID, lat, lng float64) error {
	if m.setLatLng == nil {
		m.setLatLng = map[uuid.UUID]LatLng{}
	}
	m.setLatLng[deliveryID] = LatLng{Lat: lat, Lng: lng}
	return nil
}
func (m *fakeRepo) SetDeliveryETA(ctx context.Context, deliveryID uuid.UUID, eta time.Time) error {
	if m.etas == nil {
		m.etas = map[uuid.UUID]time.Time{}
	}
	m.etas[deliveryID] = eta
	return nil
}

func rev1() Precondition {
	n := int64(1)
	return Precondition{Revision: &n}
}

func asPtr(s string) *string { return &s }

func fptr(f float64) *float64 { return &f }

func discardLogger() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

func equalIDs(a, b []uuid.UUID) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func statusOf(t *testing.T, err error) (int, string) {
	t.Helper()
	var he *httpx.Error
	if errors.As(err, &he) {
		return he.Status, he.Code
	}
	return 0, ""
}

// A completed stop needs its proof of delivery: delivered without one is a
// 400 naming both fields, failed needs none, and the precondition is
// enforced before any of it.
func TestTransitionStop_Validation(t *testing.T) {
	stop := Stop{ID: uuid.New(), OrderID: uuid.New(), Status: StopStatusPending, Revision: 1}
	svc := NewService(&fakeRepo{stops: []Stop{stop}})

	code, _ := statusOf(t, func() error {
		_, err := svc.TransitionStop(context.Background(), stop.ID, &StopTransitionDraft{To: StopStatusDelivered}, Precondition{}, "")
		return err
	}())
	if code != http.StatusPreconditionRequired {
		t.Errorf("a transition without a revision = %d, want 428", code)
	}

	draft := &StopTransitionDraft{To: StopStatusDelivered}
	_, err := svc.TransitionStop(context.Background(), stop.ID, draft, rev1(), "")
	var he *httpx.Error
	if !errors.As(err, &he) || he.Status != http.StatusBadRequest {
		t.Errorf("delivered without POD = %v, want 400 naming pod_proof_url and pod_signed_by", err)
	} else {
		fields := map[string]bool{}
		for _, d := range he.Details {
			fields[d.Field] = true
		}
		if !fields["pod_proof_url"] || !fields["pod_signed_by"] {
			t.Errorf("the 400 must name both pod fields: %v", he.Details)
		}
	}

	proof, signer := "https://x/p.jpg", "foreman"
	draft = &StopTransitionDraft{To: StopStatusDelivered, PODProofURL: &proof, PODSignedBy: &signer}
	if _, err := svc.TransitionStop(context.Background(), stop.ID, draft, rev1(), ""); err != nil {
		t.Errorf("delivered with POD: %v", err)
	}

	// A fresh pending stop: failed needs no POD.
	fresh := uuid.New()
	svcF := NewService(&fakeRepo{stops: []Stop{{ID: fresh, OrderID: uuid.New(), Status: StopStatusPending, Revision: 1}}})
	if _, err := svcF.TransitionStop(context.Background(), fresh, &StopTransitionDraft{To: StopStatusFailed}, rev1(), ""); err != nil {
		t.Errorf("failed without POD: %v", err)
	}

	svc2 := NewService(&fakeRepo{stops: []Stop{{ID: stop.ID, OrderID: uuid.New(), Status: StopStatusDelivered, Revision: 1}}})
	_, err = svc2.TransitionStop(context.Background(), stop.ID, &StopTransitionDraft{To: StopStatusFailed}, rev1(), "")
	if code, _ := statusOf(t, err); code != http.StatusConflict {
		t.Errorf("a terminal stop cannot transition again: %v, want 409", err)
	}
}

// fmt_Sprint keeps the assertions readable without importing fmt at the top.
func fmt_Sprint(err error) string { return err.Error() }

// TestOptimizeRoute_IndexAlignmentAndETA exercises the full service path on the
// keyed (ORS) branch and guards the plan's #1 hazard: when a stop is excluded
// for missing coordinates, the optimized order must remap to the *correct*
// deliveries and ETAs must land on the right rows (not shift by the dropped
// index). It also confirms the branch origin is resolved from the repo and
// encoded as [lng,lat].
func TestOptimizeRoute_IndexAlignmentAndETA(t *testing.T) {
	bodyCh := make(chan orsOptimizationRequest, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var req orsOptimizationRequest
		_ = json.Unmarshal(body, &req)
		bodyCh <- req
		io.WriteString(w, vroomResponse) // optimized order: job 1, then job 0
	}))
	defer srv.Close()

	// A and C are already geocoded; B has nil coords and no address, so on the
	// keyed path it geocodes to nothing and is excluded from optimization — the
	// exact situation that used to desync the indices.
	dA := Stop{ID: uuid.New(), OrderID: uuid.New(), Latitude: fptr(49.10), Longitude: fptr(-119.10)}
	dB := Stop{ID: uuid.New(), OrderID: uuid.New()}
	dC := Stop{ID: uuid.New(), OrderID: uuid.New(), Latitude: fptr(49.30), Longitude: fptr(-119.30)}
	routeID := uuid.New()

	repo := &fakeRepo{
		routes:     []Route{{ID: routeID, Revision: 1, Status: RouteStatusDraft}},
		stops:      []Stop{dA, dB, dC},
		branchOrg:  &BranchOrigin{Latitude: fptr(49.0), Longitude: fptr(-119.0)},
	}
	svc := NewService(repo)
	svc.WithRouting(NewORSClient("k", srv.URL, "driving-hgv", discardLogger()), discardLogger())

	if _, err := svc.OptimizeRoute(context.Background(), routeID, rev1(), ""); err != nil {
		t.Fatalf("OptimizeRoute: %v", err)
	}
	gotBody := <-bodyCh

	// Branch origin resolved from the repo, encoded [lng,lat].
	if len(gotBody.Vehicles) != 1 || gotBody.Vehicles[0].Start != [2]float64{-119.0, 49.0} {
		t.Errorf("vehicle start = %+v; want branch origin [lng,lat] [-119,49]", gotBody.Vehicles)
	}
	// Only A and C submitted (B excluded for lack of coords).
	if len(gotBody.Jobs) != 2 {
		t.Fatalf("want 2 jobs (B excluded), got %d", len(gotBody.Jobs))
	}

	// Index-desync fix: order [1,0] over stops [A,C] -> C then A, then the
	// excluded B appended last. Must not reorder the wrong rows.
	wantOrder := []uuid.UUID{dC.ID, dA.ID, dB.ID}
	if !equalIDs(repo.reorderedIDs, wantOrder) {
		t.Errorf("reorderedIDs = %v; want [C,A,B] = %v", repo.reorderedIDs, wantOrder)
	}
	// B stays uncoordinated on the keyed path (no address → no fake coords).
	if _, ok := repo.setLatLng[dB.ID]; ok {
		t.Errorf("B should remain uncoordinated on the keyed path (no address)")
	}
	// ETAs land on the correct deliveries and never on the excluded stop.
	etaC, okC := repo.etas[dC.ID]
	etaA, okA := repo.etas[dA.ID]
	if !okC || !okA {
		t.Fatalf("expected ETAs for C and A; got C=%v A=%v", okC, okA)
	}
	if _, ok := repo.etas[dB.ID]; ok {
		t.Errorf("excluded stop B must not get an ETA")
	}
	// C is visited first (arrival 600s), A second (arrival 1500s): A's ETA is later.
	if !etaA.After(etaC) {
		t.Errorf("ETA ordering wrong: A (visited 2nd) = %v should be after C (1st) = %v", etaA, etaC)
	}
}

// TestOptimizeRoute_BranchGeocodeBackfill exercises the lazy branch-origin
// backfill path the index-alignment test skips (it pre-stores coords): a branch
// with an address but no coordinates is geocoded once, the result is written
// back un-swapped, and that geocoded origin is what the optimizer receives.
func TestOptimizeRoute_BranchGeocodeBackfill(t *testing.T) {
	branchID := uuid.New()
	routeID := uuid.New()
	bodyCh := make(chan orsOptimizationRequest, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/geocode") {
			io.WriteString(w, `{"features":[{"geometry":{"coordinates":[-119.5,49.9]},"properties":{"confidence":0.95,"match_type":"exact","label":"branch"}}]}`)
			return
		}
		body, _ := io.ReadAll(r.Body)
		var req orsOptimizationRequest
		_ = json.Unmarshal(body, &req)
		bodyCh <- req
		io.WriteString(w, vroomResponse)
	}))
	defer srv.Close()

	// Stops already have coords (so only the branch needs geocoding).
	dA := Stop{ID: uuid.New(), OrderID: uuid.New(), Latitude: fptr(49.10), Longitude: fptr(-119.10)}
	dB := Stop{ID: uuid.New(), OrderID: uuid.New(), Latitude: fptr(49.30), Longitude: fptr(-119.30)}
	repo := &fakeRepo{
		routes:    []Route{{ID: routeID, Revision: 1}},
		stops:     []Stop{dA, dB},
		branchID:  branchID,
		branchOrg: &BranchOrigin{BranchID: branchID, Address: "123 Branch St"}, // no coords → lazy geocode
	}
	svc := NewService(repo)
	svc.WithRouting(NewORSClient("k", srv.URL, "driving-hgv", discardLogger()), discardLogger())

	if _, err := svc.OptimizeRoute(context.Background(), routeID, rev1(), ""); err != nil {
		t.Fatalf("OptimizeRoute: %v", err)
	}
	gotBody := <-bodyCh

	// (a) backfill persisted with un-swapped lat/lng.
	got, ok := repo.setBranch[branchID]
	if !ok {
		t.Fatal("branch geocode was never written back")
	}
	if got.Lat != 49.9 || got.Lng != -119.5 {
		t.Errorf("backfilled branch coord = %+v; want {Lat:49.9 Lng:-119.5} (lat/lng not swapped)", got)
	}
	// (b) written exactly once.
	if repo.setBranchCalls != 1 {
		t.Errorf("SetBranchLatLng called %d times; want 1", repo.setBranchCalls)
	}
	// (c) origin encoded [lng,lat] in the VROOM request.
	if len(gotBody.Vehicles) != 1 || gotBody.Vehicles[0].Start != [2]float64{-119.5, 49.9} {
		t.Errorf("vehicle start = %+v; want geocoded branch origin [lng,lat] [-119.5,49.9]", gotBody.Vehicles)
	}
}

// TestOptimizeRoute_CentroidOriginFallback proves the headline fallback design:
// when the branch has neither coords nor an address, the optimizer routes from
// the centroid of the submitted stops (region-correct), not a fixed demo anchor
// and not a lat/lng-swapped point.
func TestOptimizeRoute_CentroidOriginFallback(t *testing.T) {
	bodyCh := make(chan orsOptimizationRequest, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var req orsOptimizationRequest
		_ = json.Unmarshal(body, &req)
		bodyCh <- req
		io.WriteString(w, vroomResponse)
	}))
	defer srv.Close()
	routeID := uuid.New()

	dA := Stop{ID: uuid.New(), OrderID: uuid.New(), Latitude: fptr(49.10), Longitude: fptr(-119.10)}
	dB := Stop{ID: uuid.New(), OrderID: uuid.New(), Latitude: fptr(49.30), Longitude: fptr(-119.50)}
	repo := &fakeRepo{
		routes:    []Route{{ID: routeID, Revision: 1}},
		stops:     []Stop{dA, dB},
		branchOrg: &BranchOrigin{}, // no coords, no address → centroid fallback
	}
	svc := NewService(repo)
	svc.WithRouting(NewORSClient("k", srv.URL, "driving-hgv", discardLogger()), discardLogger())

	if _, err := svc.OptimizeRoute(context.Background(), routeID, rev1(), ""); err != nil {
		t.Fatalf("OptimizeRoute: %v", err)
	}
	gotBody := <-bodyCh

	// Origin = mean of the stops, encoded [lng,lat].
	wantLng := (-119.10 + -119.50) / 2 // -119.30
	wantLat := (49.10 + 49.30) / 2     // 49.20
	if len(gotBody.Vehicles) != 1 {
		t.Fatalf("want 1 vehicle, got %d", len(gotBody.Vehicles))
	}
	start := gotBody.Vehicles[0].Start
	if !approx(start[0], wantLng) || !approx(start[1], wantLat) {
		t.Errorf("vehicle start = %v; want stop centroid [lng,lat] [%v,%v] (not demo anchor, not swapped)", start, wantLng, wantLat)
	}
}

// TestOptimizeRoute_KeylessMockPath confirms the keyless fallback: no ORS client,
// so a coordinate-less stop is mock-geocoded on demand and the mock optimizer
// preserves input order with ETAs on every stop.
func TestOptimizeRoute_KeylessMockPath(t *testing.T) {
	routeID := uuid.New()
	dA := Stop{ID: uuid.New(), OrderID: uuid.New(), Latitude: fptr(49.10), Longitude: fptr(-119.10)}
	dB := Stop{ID: uuid.New(), OrderID: uuid.New()} // nil coords → mock-geocoded on demand
	repo := &fakeRepo{routes: []Route{{ID: routeID, Revision: 1}}, stops: []Stop{dA, dB}}
	svc := NewService(repo) // no WithRouting → s.routing == nil (keyless)

	if _, err := svc.OptimizeRoute(context.Background(), routeID, rev1(), ""); err != nil {
		t.Fatalf("OptimizeRoute: %v", err)
	}

	if _, ok := repo.setLatLng[dB.ID]; !ok {
		t.Errorf("keyless: B should be mock-geocoded on demand and persisted")
	}
	if !equalIDs(repo.reorderedIDs, []uuid.UUID{dA.ID, dB.ID}) {
		t.Errorf("reorderedIDs = %v; want [A,B]", repo.reorderedIDs)
	}
	if len(repo.etas) != 2 {
		t.Errorf("want ETAs for both stops, got %d", len(repo.etas))
	}
	// Mock optimizer spaces stops 15 min apart in input order: B (2nd) after A (1st).
	if etaA, etaB := repo.etas[dA.ID], repo.etas[dB.ID]; !etaB.After(etaA) {
		t.Errorf("keyless ETA ordering: B (2nd stop) %v should be after A (1st) %v", etaB, etaA)
	}
}

// TestOptimizeRoute_GeocodeDedupWithinRun verifies that two stops sharing one
// order are geocoded only once per optimize run (the per-run cache), with both
// delivery rows still receiving the coordinates.
func TestOptimizeRoute_GeocodeDedupWithinRun(t *testing.T) {
	var geocodeCalls atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/geocode") {
			geocodeCalls.Add(1)
			io.WriteString(w, `{"features":[{"geometry":{"coordinates":[-119.0,49.0]},"properties":{"confidence":0.9,"match_type":"exact","label":"x"}}]}`)
			return
		}
		io.WriteString(w, `{"code":0,"summary":{"duration":600,"distance":1000},"routes":[{"vehicle":1,"steps":[`+
			`{"type":"start","arrival":0,"duration":0,"distance":0},`+
			`{"type":"job","id":0,"arrival":300,"duration":300,"distance":500},`+
			`{"type":"job","id":1,"arrival":600,"duration":600,"distance":1000},`+
			`{"type":"end","arrival":600,"duration":600,"distance":1000}]}],"unassigned":[]}`)
	}))
	defer srv.Close()

	sharedOrder := uuid.New()
	routeID := uuid.New()
	d1 := Stop{ID: uuid.New(), OrderID: sharedOrder} // nil coords
	d2 := Stop{ID: uuid.New(), OrderID: sharedOrder} // nil coords, same order
	repo := &fakeRepo{
		routes:     []Route{{ID: routeID, Revision: 1}},
		stops:      []Stop{d1, d2},
		orderAddrs: map[uuid.UUID]string{sharedOrder: "123 Shared St"},
	}
	svc := NewService(repo)
	svc.WithRouting(NewORSClient("k", srv.URL, "driving-hgv", discardLogger()), discardLogger())

	if _, err := svc.OptimizeRoute(context.Background(), routeID, rev1(), ""); err != nil {
		t.Fatalf("OptimizeRoute: %v", err)
	}
	if n := geocodeCalls.Load(); n != 1 {
		t.Errorf("geocoded %d times for one shared order; want 1 (deduped within the run)", n)
	}
	if _, ok := repo.setLatLng[d1.ID]; !ok {
		t.Error("d1 should be geocoded and persisted")
	}
	if _, ok := repo.setLatLng[d2.ID]; !ok {
		t.Error("d2 should reuse the cached geocode and be persisted")
	}
	// Both shared-order stops survive into the route with ETAs (dedup must not drop one).
	if !equalIDs(repo.reorderedIDs, []uuid.UUID{d1.ID, d2.ID}) {
		t.Errorf("reorderedIDs = %v; want both shared-order stops [d1,d2]", repo.reorderedIDs)
	}
	if len(repo.etas) != 2 {
		t.Errorf("want ETAs for both stops, got %d", len(repo.etas))
	}
}

// TestGetDelivery_UnroutedStopSerialisesAsNull pins the wire shape of an
// unrouted stop: deliveries.route_id is nullable (migration 009) and the seed's
// dispatch day writes exactly that state on purpose, so null is the honest
// answer and the all-zero uuid is a route that does not exist.
func TestGetDelivery_UnroutedStopSerialisesAsNull(t *testing.T) {
	repo := &fakeRepo{fetchedStop: &Stop{
		ID:           uuid.New(),
		RouteID:      nil, // the unrouted stop
		OrderID:      uuid.New(),
		StopSequence: 1,
		Status:       StopStatusPending,
	}}
	svc := NewService(repo)
	mux := http.NewServeMux()
	NewHandler(svc).RegisterRoutes(mux)

	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet,
		"/api/v1/delivery/deliveries/"+repo.fetchedStop.ID.String(), nil))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body %s)", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if !strings.Contains(body, `"route_id":null`) {
		t.Errorf(`body has no "route_id":null; got %s`, body)
	}
	if strings.Contains(body, "00000000-0000-0000-0000-000000000000") {
		t.Errorf("an unrouted stop reported the zero uuid as its route: %s", body)
	}
}

// TestGetDelivery_RoutedStopKeepsItsRouteID is the other half: making the field
// nullable must not turn a real route id into null.
func TestGetDelivery_RoutedStopKeepsItsRouteID(t *testing.T) {
	routeID := uuid.New()
	repo := &fakeRepo{fetchedStop: &Stop{
		ID:           uuid.New(),
		RouteID:      &routeID,
		OrderID:      uuid.New(),
		StopSequence: 2,
		Status:       StopStatusPending,
	}}
	svc := NewService(repo)
	mux := http.NewServeMux()
	NewHandler(svc).RegisterRoutes(mux)

	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet,
		"/api/v1/delivery/deliveries/"+repo.fetchedStop.ID.String(), nil))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body %s)", rec.Code, rec.Body.String())
	}
	var got Stop
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("unmarshal: %v (body %s)", err, rec.Body.String())
	}
	if got.RouteID == nil || *got.RouteID != routeID {
		t.Errorf("RouteID = %v, want %v", got.RouteID, routeID)
	}
}

// TestAssignOrderToRoute_SetsTheRoute closes the loop: assigning an order to a
// route has to produce a stop that names it.
func TestAssignOrderToRoute_SetsTheRoute(t *testing.T) {
	routeID, vehicleID, orderID := uuid.New(), uuid.New(), uuid.New()
	branchID := uuid.New()
	repo := &fakeRepo{
		routes:      []Route{{ID: routeID, VehicleID: vehicleID, Status: RouteStatusDraft, Revision: 1}},
		vehicle:     &Vehicle{ID: vehicleID},
		orderBranch: map[uuid.UUID]uuid.UUID{orderID: branchID},
	}
	svc := NewService(repo)

	seq := 1
	d, warning, err := svc.AssignOrderToRoute(context.Background(), &AssignStopDraft{
		RouteID:      routeID,
		OrderID:      orderID,
		StopSequence: &seq,
	}, "")
	if err != nil {
		t.Fatalf("AssignOrderToRoute: %v", err)
	}
	if warning != nil {
		t.Errorf("unexpected capacity warning: %+v", warning)
	}
	if d.RouteID == nil || *d.RouteID != routeID {
		t.Fatalf("returned stop RouteID = %v, want %v", d.RouteID, routeID)
	}
	if repo.createdStop == nil || repo.createdStop.RouteID == nil || *repo.createdStop.RouteID != routeID {
		t.Errorf("the row handed to the repository does not carry the route: %+v", repo.createdStop)
	}
}
