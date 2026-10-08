// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package integrations

// Cross-repo contract test for the AI_LM integration seam.
//
// THE PROBLEM
// GableLBM and AI_LM (github.com/gablelbm/gable-ai-lm) are two independently
// released repositories joined by seven HTTP endpoints. Until this file existed
// the contract between them was prose plus struct tags: nothing mechanical
// stopped someone renaming a JSON field, collapsing a nullable pointer to a
// zero value, or wrapping a bare array in an envelope. Every such change
// compiles, passes `go test` in both repos, and fails only in production —
// usually as silently wrong data (a null dimension read as a real 0.0) rather
// than a loud error.
//
// THE FIX — two layers, both golden-file backed:
//
//	Layer A  TestAILMClientContractUnchanged
//	         `aiLM*` below are verbatim mirrors of the wire structs in
//	         gable-ai-lm/backend/internal/gable/client.go. Reflection turns them
//	         into a machine-readable field spec (JSON name, Go kind,
//	         pointer-ness, omitempty) which is compared to
//	         testdata/ailm_client_contract.json. This layer guards the MIRROR:
//	         it makes it impossible to quietly edit the mirror so that a GableLBM
//	         change "passes" Layer B. Updating this golden is the explicit,
//	         reviewable act of saying "AI_LM's client really did change".
//
//	Layer B  TestAILMResponseContract / TestAILMRequestContract
//	         Drives the real handlers over httptest, decodes each response
//	         through the mirror types (exactly what AI_LM's client does), and
//	         compares the RE-MARSHALLED result to a golden. This is the
//	         AI_LM-visible projection of GableLBM's payload, so the golden
//	         records what AI_LM actually ends up holding in memory.
//
// WHAT THIS CATCHES
//   - renaming a JSON field (weight_lbs -> weight): decodes to the zero value,
//     projection no longer matches the golden.
//   - collapsing nullability (length_in *float64 -> float64, or COALESCE(...,0)
//     in SQL): golden has null, projection has 0.
//   - enveloping a list endpoint ({"data":[...]}): decode into a Go slice fails.
//   - dropping a field AI_LM requires, or changing its JSON type.
//   - changing the request shape AI_LM sends (Layer B request direction).
//
// WHAT IT CANNOT CATCH
// It cannot notice a change made in the AI_LM repo until someone re-syncs the
// mirror below. The mirror's provenance is recorded in aiLMClientSource; when
// AI_LM's client.go changes, copy the structs across, re-run with
// `-update-golden`, and review the golden diff.
//
// Regenerate every golden in this file with:
//
//	go test ./internal/integrations -run TestAILM -update-golden

import (
	"encoding/json"
	"flag"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

var updateGolden = flag.Bool("update-golden", false, "rewrite the AI_LM contract golden files")

// aiLMClientSource records exactly where the mirror types below were copied
// from, so a reviewer can diff them against the source of truth.
const aiLMClientSource = "gable-ai-lm/backend/internal/gable/client.go"

// --- mirrors of AI_LM's wire types ------------------------------------------
//
// Copy these verbatim from aiLMClientSource. Do NOT "fix" them to match a
// GableLBM change — that inverts the direction of the contract. Field order,
// Go types, pointer-ness and struct tags all matter.
//
// EXCEPTION, and the only kind there should ever be: a field or type added as
// half of a coordinated cross-repo change lands here at the same time as it
// lands in AI_LM's client.go, since neither repo can merge a seam that only one
// side knows about. aiLMLocation and aiLMOrder.BranchID are that case (the
// branch-depot work: AI_LM roots a route at the yard an order ships from
// instead of at one global DEPOT_LAT/DEPOT_LNG). They are written here as the
// AGREED shape and AI_LM's gable.Location / gable.Order must match them field
// for field, tag for tag, in the same order. If they ever diverge, this file is
// not the thing to edit — re-sync from aiLMClientSource.

type aiLMVehicle struct {
	ID                string `json:"id"`
	Name              string `json:"name"`
	VehicleType       string `json:"vehicle_type"`
	LicensePlate      string `json:"license_plate,omitempty"`
	CapacityWeightLbs *int   `json:"capacity_weight_lbs,omitempty"`
	Make              string `json:"make,omitempty"`
	Model             string `json:"model,omitempty"`
	Year              int    `json:"year,omitempty"`
}

type aiLMDriver struct {
	ID     string `json:"id"`
	Name   string `json:"name"`
	Status string `json:"status"`
}

// aiLMLocation mirrors gable.Location — one of the dealer's branches, which
// AI_LM matches against Order.BranchID to root a plan at the yard the load
// actually leaves from instead of at one globally configured depot.
//
// Latitude/Longitude are pointers: locations.latitude/longitude are backfilled
// lazily, and nil ("this yard has never been geocoded") must not collapse into
// 0,0. That collapse is precisely what this contract suite exists to catch.
type aiLMLocation struct {
	ID        string   `json:"id"`
	Name      string   `json:"name"`
	Address   string   `json:"address,omitempty"`
	Latitude  *float64 `json:"latitude,omitempty"`
	Longitude *float64 `json:"longitude,omitempty"`
}

type aiLMProduct struct {
	ID             string   `json:"id"`
	SKU            string   `json:"sku"`
	Name           string   `json:"name"`
	Category       string   `json:"category,omitempty"`
	UOM            string   `json:"uom,omitempty"`
	WeightLbs      float64  `json:"weight_lbs"`
	LengthIn       *float64 `json:"length_in"`
	WidthIn        *float64 `json:"width_in"`
	HeightIn       *float64 `json:"height_in"`
	Stackable      *bool    `json:"stackable"`
	GeometrySource string   `json:"geometry_source,omitempty"`
}

type aiLMOrderLine struct {
	ProductID string  `json:"product_id"`
	SKU       string  `json:"sku"`
	Quantity  float64 `json:"quantity"`
	WeightLbs float64 `json:"weight_lbs"`
}

type aiLMOrder struct {
	ID           string          `json:"id"`
	Status       string          `json:"status"`
	BranchID     string          `json:"branch_id"`
	CustomerName string          `json:"customer_name,omitempty"`
	Address      string          `json:"address,omitempty"`
	Latitude     *float64        `json:"latitude,omitempty"`
	Longitude    *float64        `json:"longitude,omitempty"`
	Lines        []aiLMOrderLine `json:"lines"`
}

type aiLMRouteStop struct {
	OrderID  string  `json:"order_id"`
	Sequence int     `json:"sequence"`
	Lat      float64 `json:"lat"`
	Lng      float64 `json:"lng"`
}

type aiLMDeliveryRoute struct {
	VehicleID     string          `json:"vehicle_id"`
	DriverID      string          `json:"driver_id,omitempty"`
	ScheduledDate string          `json:"scheduled_date"`
	Stops         []aiLMRouteStop `json:"stops"`
	LoadManifest  any             `json:"load_manifest,omitempty"`
}

type aiLMStaffValidation struct {
	StaffID  string   `json:"staff_id"`
	Email    string   `json:"email"`
	Name     string   `json:"name"`
	Entitled bool     `json:"entitled"`
	Roles    []string `json:"roles"`
	Modules  []string `json:"modules"`
}

// --- Layer A: the mirror itself is pinned -----------------------------------

// contractField is one field of an AI_LM wire struct, reduced to the properties
// that can break the seam.
type contractField struct {
	JSONName  string `json:"json_name"`
	GoType    string `json:"go_type"`
	Pointer   bool   `json:"pointer"`
	OmitEmpty bool   `json:"omitempty"`
}

func describeType(t reflect.Type) []contractField {
	out := make([]contractField, 0, t.NumField())
	for i := 0; i < t.NumField(); i++ {
		f := t.Field(i)
		tag := f.Tag.Get("json")
		parts := strings.Split(tag, ",")
		name := parts[0]
		if name == "" {
			name = f.Name
		}
		omit := false
		for _, p := range parts[1:] {
			if p == "omitempty" {
				omit = true
			}
		}
		ft := f.Type
		ptr := ft.Kind() == reflect.Pointer
		if ptr {
			ft = ft.Elem()
		}
		out = append(out, contractField{
			JSONName:  name,
			GoType:    ft.String(),
			Pointer:   ptr,
			OmitEmpty: omit,
		})
	}
	return out
}

// TestAILMClientContractUnchanged pins the mirror of AI_LM's client structs.
// It fails when someone edits the mirror — which is the only way to make a
// GableLBM-side rename look compatible in Layer B.
func TestAILMClientContractUnchanged(t *testing.T) {
	t.Parallel()

	spec := map[string]any{
		"source": aiLMClientSource,
		"types": map[string][]contractField{
			"Vehicle":         describeType(reflect.TypeOf(aiLMVehicle{})),
			"Driver":          describeType(reflect.TypeOf(aiLMDriver{})),
			"Location":        describeType(reflect.TypeOf(aiLMLocation{})),
			"Product":         describeType(reflect.TypeOf(aiLMProduct{})),
			"OrderLine":       describeType(reflect.TypeOf(aiLMOrderLine{})),
			"Order":           describeType(reflect.TypeOf(aiLMOrder{})),
			"RouteStop":       describeType(reflect.TypeOf(aiLMRouteStop{})),
			"DeliveryRoute":   describeType(reflect.TypeOf(aiLMDeliveryRoute{})),
			"StaffValidation": describeType(reflect.TypeOf(aiLMStaffValidation{})),
		},
	}

	assertGolden(t, "ailm_client_contract.json", mustIndent(t, spec))
}

// --- Layer B: GableLBM's payload as AI_LM sees it ---------------------------

// TestAILMResponseContract drives each read endpoint, decodes the body through
// AI_LM's own types, and pins the re-marshalled result. The re-marshal step is
// what makes drift visible: anything AI_LM cannot see (a renamed field, a
// collapsed null) disappears from the projection.
func TestAILMResponseContract(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name   string
		target string
		golden string
		// decode unmarshals the body into AI_LM's type and hands back the
		// decoded value for re-marshalling.
		decode func(t *testing.T, body []byte) any
	}{
		{
			name:   "vehicles",
			target: "/api/integration/vehicles",
			golden: "ailm_vehicles.json",
			decode: func(t *testing.T, body []byte) any {
				var v []aiLMVehicle
				mustDecode(t, body, &v)
				return v
			},
		},
		{
			name:   "drivers",
			target: "/api/integration/drivers",
			golden: "ailm_drivers.json",
			decode: func(t *testing.T, body []byte) any {
				var v []aiLMDriver
				mustDecode(t, body, &v)
				return v
			},
		},
		{
			name:   "locations",
			target: "/api/integration/locations",
			golden: "ailm_locations.json",
			decode: func(t *testing.T, body []byte) any {
				var v []aiLMLocation
				mustDecode(t, body, &v)
				return v
			},
		},
		{
			name:   "products",
			target: "/api/integration/products",
			golden: "ailm_products.json",
			decode: func(t *testing.T, body []byte) any {
				var v []aiLMProduct
				mustDecode(t, body, &v)
				return v
			},
		},
		{
			name:   "orders",
			target: "/api/integration/orders?date=2026-08-21&status=CONFIRMED",
			golden: "ailm_orders.json",
			decode: func(t *testing.T, body []byte) any {
				var v []aiLMOrder
				mustDecode(t, body, &v)
				return v
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv := newTestServer(fullFakeStore(), testKey)
			w := do(t, srv, http.MethodGet, tc.target, testKey, "")
			if w.Code != http.StatusOK {
				t.Fatalf("status = %d, want 200 (body %s)", w.Code, w.Body)
			}
			assertBareArray(t, w.Body.Bytes())
			projection := tc.decode(t, w.Body.Bytes())
			assertGolden(t, tc.golden, mustIndent(t, projection))
		})
	}
}

// TestAILMValidateStaffResponseContract covers the POST read endpoint, which
// AI_LM's client also decodes into one of its own types. It is separate because
// it needs a request body.
func TestAILMValidateStaffResponseContract(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name   string
		body   string
		golden string
	}{
		{"entitled", `{"email":"dispatcher@gable.com"}`, "ailm_validate_staff_entitled.json"},
		{"unknown", `{"email":"stranger@example.com"}`, "ailm_validate_staff_unknown.json"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv := newTestServer(fullFakeStore(), testKey)
			w := do(t, srv, http.MethodPost, "/api/integration/validate-staff", testKey, tc.body)
			if w.Code != http.StatusOK {
				t.Fatalf("status = %d, want 200 (body %s)", w.Code, w.Body)
			}
			var got aiLMStaffValidation
			mustDecode(t, w.Body.Bytes(), &got)
			assertGolden(t, tc.golden, mustIndent(t, got))
		})
	}
}

// TestAILMRequestContract pins the OTHER direction: the exact bytes AI_LM's
// client puts on the wire for the write-back, and GableLBM's acceptance of
// them. Marshalling AI_LM's own DeliveryRoute type (rather than a hand-written
// JSON literal) means the golden tracks AI_LM's encoder, including the
// omitempty on driver_id.
func TestAILMRequestContract(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name   string
		route  aiLMDeliveryRoute
		golden string
	}{
		{
			name:   "assigned truck with a packing manifest",
			golden: "ailm_delivery_route_request.json",
			route: aiLMDeliveryRoute{
				VehicleID:     vehicleUUID,
				DriverID:      driverUUID,
				ScheduledDate: "2026-08-21",
				Stops: []aiLMRouteStop{
					{OrderID: orderUUID, Sequence: 1, Lat: 49.8801, Lng: -119.4436},
					{OrderID: order2UUID, Sequence: 2, Lat: 49.9402, Lng: -119.3963},
				},
				LoadManifest: map[string]any{
					"steps": []any{map[string]any{"sku": "LUM-248-PREM", "slot": 1}},
				},
			},
		},
		{
			name:   "unassigned truck omits driver_id and manifest",
			golden: "ailm_delivery_route_request_unassigned.json",
			route: aiLMDeliveryRoute{
				VehicleID:     vehicleUUID,
				ScheduledDate: "2026-08-21",
				Stops:         []aiLMRouteStop{{OrderID: orderUUID, Sequence: 1, Lat: 49.8801, Lng: -119.4436}},
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			body := mustIndent(t, tc.route)
			assertGolden(t, tc.golden, body)

			// GableLBM must accept exactly those bytes.
			store := fullFakeStore()
			srv := newTestServer(store, testKey)
			w := do(t, srv, http.MethodPost, "/api/integration/delivery-routes", testKey, string(body))
			if w.Code != http.StatusCreated {
				t.Fatalf("GableLBM rejected AI_LM's write-back: status %d body %s", w.Code, w.Body)
			}

			// ...and must not lose anything on the way to the store.
			if store.gotRoute == nil {
				t.Fatal("store never received the route")
			}
			if store.gotRoute.VehicleID != tc.route.VehicleID {
				t.Errorf("vehicle_id = %q, want %q", store.gotRoute.VehicleID, tc.route.VehicleID)
			}
			if store.gotRoute.DriverID != tc.route.DriverID {
				t.Errorf("driver_id = %q, want %q", store.gotRoute.DriverID, tc.route.DriverID)
			}
			if store.gotRoute.ScheduledDate != tc.route.ScheduledDate {
				t.Errorf("scheduled_date = %q, want %q", store.gotRoute.ScheduledDate, tc.route.ScheduledDate)
			}
			if len(store.gotRoute.Stops) != len(tc.route.Stops) {
				t.Fatalf("stops = %d, want %d", len(store.gotRoute.Stops), len(tc.route.Stops))
			}
			for i, want := range tc.route.Stops {
				got := store.gotRoute.Stops[i]
				if got.OrderID != want.OrderID || got.Sequence != want.Sequence {
					t.Errorf("stop %d = %+v, want order %s seq %d", i, got, want.OrderID, want.Sequence)
				}
				if got.Lat == nil || *got.Lat != want.Lat || got.Lng == nil || *got.Lng != want.Lng {
					t.Errorf("stop %d coordinates lost: got (%v,%v), want (%v,%v)",
						i, got.Lat, got.Lng, want.Lat, want.Lng)
				}
			}
		})
	}
}

// TestAILMValidateStaffRequestContract pins the login request body. AI_LM's
// client sends map[string]string{"email": ...} — a single key, nothing else.
func TestAILMValidateStaffRequestContract(t *testing.T) {
	t.Parallel()

	body := mustIndent(t, map[string]string{"email": "dispatcher@gable.com"})
	assertGolden(t, "ailm_validate_staff_request.json", body)

	srv := newTestServer(fullFakeStore(), testKey)
	w := do(t, srv, http.MethodPost, "/api/integration/validate-staff", testKey, string(body))
	if w.Code != http.StatusOK {
		t.Fatalf("GableLBM rejected AI_LM's login body: status %d body %s", w.Code, w.Body)
	}
}

// TestAILMEndpointInventory pins the set of paths the seam is made of, so
// deleting or moving one is a test failure rather than a 404 at runtime.
func TestAILMEndpointInventory(t *testing.T) {
	t.Parallel()

	// method+path exactly as AI_LM's client calls them.
	calls := []struct{ method, path string }{
		{http.MethodGet, "/api/integration/products"},
		{http.MethodGet, "/api/integration/vehicles"},
		{http.MethodGet, "/api/integration/drivers"},
		{http.MethodGet, "/api/integration/locations"},
		{http.MethodGet, "/api/integration/orders"},
		{http.MethodPost, "/api/integration/delivery-routes"},
		{http.MethodPost, "/api/integration/validate-staff"},
	}

	srv := newTestServer(fullFakeStore(), testKey)
	for _, c := range calls {
		t.Run(c.method+" "+c.path, func(t *testing.T) {
			// No key: a registered route answers 401 from authMiddleware, an
			// unregistered one answers 404 from the mux. That distinction is
			// exactly the "does this endpoint exist" question.
			w := do(t, srv, c.method, c.path, "", "")
			if w.Code == http.StatusNotFound || w.Code == http.StatusMethodNotAllowed {
				t.Fatalf("%s %s is not registered (status %d) — AI_LM depends on it", c.method, c.path, w.Code)
			}
			if w.Code != http.StatusUnauthorized {
				t.Fatalf("%s %s answered %d without a key, want 401", c.method, c.path, w.Code)
			}
		})
	}
}

// --- golden-file plumbing ---------------------------------------------------

func mustDecode(t *testing.T, body []byte, into any) {
	t.Helper()
	if err := json.Unmarshal(body, into); err != nil {
		t.Fatalf("AI_LM's client could not decode this payload: %v\nbody: %s", err, body)
	}
}

func mustIndent(t *testing.T, v any) []byte {
	t.Helper()
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return append(b, '\n')
}

func assertGolden(t *testing.T, name string, got []byte) {
	t.Helper()
	path := filepath.Join("testdata", name)

	if *updateGolden {
		if err := os.MkdirAll("testdata", 0o755); err != nil {
			t.Fatalf("create testdata: %v", err)
		}
		if err := os.WriteFile(path, got, 0o644); err != nil {
			t.Fatalf("write golden %s: %v", path, err)
		}
		return
	}

	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read golden %s: %v (regenerate with -update-golden)", path, err)
	}
	if string(want) != string(got) {
		t.Errorf("contract drift in %s\n--- want (golden) ---\n%s\n--- got (current) ---\n%s\n"+
			"If AI_LM genuinely changed, re-sync the mirror types from %s and rerun with -update-golden.",
			path, want, got, aiLMClientSource)
	}
}
