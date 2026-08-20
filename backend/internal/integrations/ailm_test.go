// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package integrations

// Handler tests for the AI_LM integration surface. These run entirely over
// httptest against a fake ailmStore — no Postgres — and assert the exact JSON
// AI_LM's client decodes, not merely that a request returned 200.

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
)

const testKey = "test-integration-key"

// --- fake store -------------------------------------------------------------

type fakeAILMStore struct {
	vehicles []VehicleResponse
	drivers  []DriverResponse
	products []ProductResponse
	orders   []IntegrationOrderResponse
	route    *DeliveryRouteResponse
	staff    map[string]*staffLookup

	// err, when set, is returned by every method so the 500 paths are covered.
	err error

	// Captured inputs, so tests can assert the handler translated the query
	// string into the store call correctly.
	gotProductFilter productFilter
	gotOrdersDate    string
	gotOrdersStatus  string
	gotRoute         *DeliveryRouteRequest
}

func (f *fakeAILMStore) ListVehicles(context.Context) ([]VehicleResponse, error) {
	return f.vehicles, f.err
}

func (f *fakeAILMStore) ListDrivers(context.Context) ([]DriverResponse, error) {
	return f.drivers, f.err
}

func (f *fakeAILMStore) ListProducts(_ context.Context, filter productFilter) ([]ProductResponse, error) {
	f.gotProductFilter = filter
	return f.products, f.err
}

func (f *fakeAILMStore) ListOrders(_ context.Context, date, status string) ([]IntegrationOrderResponse, error) {
	f.gotOrdersDate, f.gotOrdersStatus = date, status
	return f.orders, f.err
}

func (f *fakeAILMStore) ReplaceDeliveryRoute(_ context.Context, req DeliveryRouteRequest) (*DeliveryRouteResponse, error) {
	f.gotRoute = &req
	if f.err != nil {
		return nil, f.err
	}
	if f.route != nil {
		return f.route, nil
	}
	return &DeliveryRouteResponse{RouteID: routeUUID, StopCount: len(req.Stops), Created: true}, nil
}

func (f *fakeAILMStore) LookupStaff(_ context.Context, email, staffNo string) (*staffLookup, error) {
	if f.err != nil {
		return nil, f.err
	}
	key := strings.ToLower(email)
	if key == "" {
		key = staffNo
	}
	if s, ok := f.staff[key]; ok {
		return s, nil
	}
	return &staffLookup{Found: false, Modules: []string{}}, nil
}

// --- fixtures ---------------------------------------------------------------
//
// Deterministic UUIDs so the golden fixtures in ailm_contract_test.go are
// stable. Two of everything where it matters: one fully-populated row and one
// sparse row, because the sparse row is what proves nullability survives.

const (
	vehicleUUID  = "11111111-1111-4111-8111-111111111111"
	vehicle2UUID = "11111111-1111-4111-8111-222222222222"
	driverUUID   = "22222222-2222-4222-8222-111111111111"
	productUUID  = "33333333-3333-4333-8333-111111111111"
	product2UUID = "33333333-3333-4333-8333-222222222222"
	orderUUID    = "44444444-4444-4444-8444-111111111111"
	order2UUID   = "44444444-4444-4444-8444-222222222222"
	routeUUID    = "55555555-5555-4555-8555-111111111111"
	staffUUID    = "66666666-6666-4666-8666-111111111111"
)

func f64(v float64) *float64 { return &v }
func boolp(v bool) *bool     { return &v }
func intp(v int) *int        { return &v }

func fixtureVehicles() []VehicleResponse {
	return []VehicleResponse{
		{
			ID:                vehicleUUID,
			Name:              "Flatbed A",
			VehicleType:       "Flatbed",
			LicensePlate:      "BC-1234",
			CapacityWeightLbs: intp(26000),
			Make:              "Freightliner",
			Model:             "M2 106",
			Year:              2021,
		},
		{
			// Capacity/make/model/year unknown — every optional field must
			// vanish from the payload rather than be asserted as a zero.
			ID:          vehicle2UUID,
			Name:        "Yard Pickup",
			VehicleType: "Pickup",
		},
	}
}

func fixtureDrivers() []DriverResponse {
	return []DriverResponse{
		{ID: driverUUID, Name: "Dana Ramirez", Status: "ACTIVE"},
	}
}

func fixtureProducts() []ProductResponse {
	return []ProductResponse{
		{
			ID:             productUUID,
			SKU:            "LUM-248-PREM",
			Name:           "2x4x8 SPF Premium",
			Category:       "Framing Lumber",
			UOM:            "PCS",
			Price:          79900,
			WeightLbs:      9.5,
			LengthIn:       f64(96),
			WidthIn:        f64(3.5),
			HeightIn:       f64(1.5),
			Stackable:      boolp(true),
			GeometrySource: "parametric",
		},
		{
			// No PIM geometry at all. length_in/width_in/height_in/stackable
			// MUST serialize as JSON null: a 0 here would tell AI_LM this SKU
			// is a real zero-volume, non-stackable box instead of "unknown".
			ID:        product2UUID,
			SKU:       "HW-NAIL-16D",
			Name:      "16d Common Nails 50lb",
			Category:  "Hardware",
			UOM:       "BOX",
			Price:     6500,
			WeightLbs: 50,
		},
	}
}

func fixtureOrders() []IntegrationOrderResponse {
	return []IntegrationOrderResponse{
		{
			ID:            orderUUID,
			Status:        "CONFIRMED",
			CustomerName:  "Kelbrook Homes",
			Address:       "1885 Spall Rd, Kelowna BC",
			Latitude:      f64(49.8801),
			Longitude:     f64(-119.4436),
			ScheduledDate: "2026-08-21",
			Lines: []IntegrationOrderLine{
				{ProductID: productUUID, SKU: "LUM-248-PREM", Quantity: 128, WeightLbs: 9.5},
				{ProductID: product2UUID, SKU: "HW-NAIL-16D", Quantity: 2, WeightLbs: 50},
			},
		},
		{
			// Never geocoded: latitude/longitude must be ABSENT, not 0/0 —
			// null island is in the Gulf of Guinea and would wreck the route.
			ID:            order2UUID,
			Status:        "CONFIRMED",
			CustomerName:  "Okanagan Builders",
			Address:       "9000 Summit Pkwy, Kelowna BC",
			ScheduledDate: "2026-08-21",
			Lines: []IntegrationOrderLine{
				{ProductID: productUUID, SKU: "LUM-248-PREM", Quantity: 40, WeightLbs: 9.5},
			},
		},
	}
}

func fixtureStaff() map[string]*staffLookup {
	return map[string]*staffLookup{
		"dispatcher@gable.com": {
			Found: true, ID: staffUUID, Email: "dispatcher@gable.com",
			Name: "Dana Ramirez", Role: "dispatcher", Active: true,
			Modules: []string{"ai_lm"},
		},
		"onleave@gable.com": {
			// Granted the module but deactivated: entitlement must be false.
			Found: true, ID: staffUUID, Email: "onleave@gable.com",
			Name: "Sam Vega", Role: "dispatcher", Active: false,
			Modules: []string{"ai_lm"},
		},
		"yard@gable.com": {
			// Active, but AI_LM is not among their effective modules.
			Found: true, ID: staffUUID, Email: "yard@gable.com",
			Name: "Yuki Tan", Role: "yard", Active: true,
			Modules: []string{"pos"},
		},
		"noroles@gable.com": {
			Found: true, ID: staffUUID, Email: "noroles@gable.com",
			Name: "Pat Doe", Role: "", Active: true, Modules: nil,
		},
	}
}

func fullFakeStore() *fakeAILMStore {
	return &fakeAILMStore{
		vehicles: fixtureVehicles(),
		drivers:  fixtureDrivers(),
		products: fixtureProducts(),
		orders:   fixtureOrders(),
		staff:    fixtureStaff(),
	}
}

// --- helpers ----------------------------------------------------------------

// newTestServer builds the real route table (so method+path patterns and the
// auth middleware are exercised) over a fake store.
func newTestServer(store ailmStore, apiKey string) http.Handler {
	h := &Handler{apiKey: apiKey, ailm: store}
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)
	return mux
}

// do issues an authenticated request unless key is empty.
func do(t *testing.T, srv http.Handler, method, target, key, body string) *httptest.ResponseRecorder {
	t.Helper()
	var r *http.Request
	if body == "" {
		r = httptest.NewRequest(method, target, nil)
	} else {
		r = httptest.NewRequest(method, target, bytes.NewBufferString(body))
		r.Header.Set("Content-Type", "application/json")
	}
	if key != "" {
		r.Header.Set("X-Integration-Key", key)
	}
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, r)
	return w
}

// assertJSON compares the response body against want by structural equality of
// the decoded values. Comparing decoded JSON (rather than raw bytes) keeps the
// assertion insensitive to key order while staying strict about key NAMES,
// about null-vs-absent, and about array-vs-object.
func assertJSON(t *testing.T, got []byte, want string) {
	t.Helper()
	var gotV, wantV any
	if err := json.Unmarshal(got, &gotV); err != nil {
		t.Fatalf("response is not valid JSON: %v\nbody: %s", err, got)
	}
	if err := json.Unmarshal([]byte(want), &wantV); err != nil {
		t.Fatalf("want fixture is not valid JSON: %v", err)
	}
	if !reflect.DeepEqual(gotV, wantV) {
		t.Errorf("wire payload mismatch\n got: %s\nwant: %s", bytes.TrimSpace(got), want)
	}
}

// assertBareArray fails if the payload is enveloped. AI_LM decodes list
// endpoints straight into a Go slice, so {"data":[...]} is a breaking change
// even though it is still valid JSON.
func assertBareArray(t *testing.T, got []byte) {
	t.Helper()
	if trimmed := bytes.TrimSpace(got); len(trimmed) == 0 || trimmed[0] != '[' {
		t.Errorf("expected a bare JSON array, got: %s", trimmed)
	}
}

// --- auth -------------------------------------------------------------------

// Every AI_LM endpoint must be behind X-Integration-Key. A missing or wrong key
// is 401; an unconfigured server is 503 (the operator never set
// INTEGRATION_API_KEY, so this is not the caller's fault).
func TestAILMEndpointsRequireIntegrationKey(t *testing.T) {
	t.Parallel()

	endpoints := []struct {
		method, target, body string
	}{
		{http.MethodGet, "/api/integration/products", ""},
		{http.MethodGet, "/api/integration/vehicles", ""},
		{http.MethodGet, "/api/integration/drivers", ""},
		{http.MethodGet, "/api/integration/orders?date=2026-08-21&status=CONFIRMED", ""},
		{http.MethodPost, "/api/integration/delivery-routes", `{"vehicle_id":"` + vehicleUUID + `"}`},
		{http.MethodPost, "/api/integration/validate-staff", `{"email":"dispatcher@gable.com"}`},
	}

	cases := []struct {
		name     string
		apiKey   string
		sendKey  string
		wantCode int
	}{
		{"no key sent", testKey, "", http.StatusUnauthorized},
		{"wrong key sent", testKey, "not-the-key", http.StatusUnauthorized},
		{"key prefix only", testKey, testKey[:5], http.StatusUnauthorized},
		{"server not configured", "", testKey, http.StatusServiceUnavailable},
		{"correct key", testKey, testKey, -1}, // -1: assert only that it is NOT 401/503
	}

	for _, tc := range cases {
		for _, ep := range endpoints {
			t.Run(tc.name+" "+ep.method+" "+ep.target, func(t *testing.T) {
				srv := newTestServer(fullFakeStore(), tc.apiKey)
				w := do(t, srv, ep.method, ep.target, tc.sendKey, ep.body)
				if tc.wantCode == -1 {
					if w.Code == http.StatusUnauthorized || w.Code == http.StatusServiceUnavailable {
						t.Fatalf("valid key rejected: status %d body %s", w.Code, w.Body)
					}
					return
				}
				if w.Code != tc.wantCode {
					t.Fatalf("status = %d, want %d (body %s)", w.Code, tc.wantCode, w.Body)
				}
			})
		}
	}
}

// --- vehicles ---------------------------------------------------------------

func TestListVehiclesWireShape(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name     string
		store    *fakeAILMStore
		wantCode int
		wantJSON string
	}{
		{
			name:     "populated and sparse rows",
			store:    fullFakeStore(),
			wantCode: http.StatusOK,
			wantJSON: `[
			  {"id":"` + vehicleUUID + `","name":"Flatbed A","vehicle_type":"Flatbed",
			   "license_plate":"BC-1234","capacity_weight_lbs":26000,
			   "make":"Freightliner","model":"M2 106","year":2021},
			  {"id":"` + vehicle2UUID + `","name":"Yard Pickup","vehicle_type":"Pickup"}
			]`,
		},
		{
			name:     "empty fleet is [] not null",
			store:    &fakeAILMStore{},
			wantCode: http.StatusOK,
			wantJSON: `[]`,
		},
		{
			name:     "store failure is 500",
			store:    &fakeAILMStore{err: errors.New("boom")},
			wantCode: http.StatusInternalServerError,
			wantJSON: `{"error":"failed to query vehicles"}`,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv := newTestServer(tc.store, testKey)
			w := do(t, srv, http.MethodGet, "/api/integration/vehicles", testKey, "")
			if w.Code != tc.wantCode {
				t.Fatalf("status = %d, want %d (body %s)", w.Code, tc.wantCode, w.Body)
			}
			if tc.wantCode == http.StatusOK {
				assertBareArray(t, w.Body.Bytes())
			}
			assertJSON(t, w.Body.Bytes(), tc.wantJSON)
		})
	}
}

// --- drivers ----------------------------------------------------------------

func TestListDriversWireShape(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name     string
		store    *fakeAILMStore
		wantCode int
		wantJSON string
	}{
		{
			name:     "one driver",
			store:    fullFakeStore(),
			wantCode: http.StatusOK,
			wantJSON: `[{"id":"` + driverUUID + `","name":"Dana Ramirez","status":"ACTIVE"}]`,
		},
		{
			name:     "no drivers is [] not null",
			store:    &fakeAILMStore{},
			wantCode: http.StatusOK,
			wantJSON: `[]`,
		},
		{
			name:     "store failure is 500",
			store:    &fakeAILMStore{err: errors.New("boom")},
			wantCode: http.StatusInternalServerError,
			wantJSON: `{"error":"failed to query drivers"}`,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv := newTestServer(tc.store, testKey)
			w := do(t, srv, http.MethodGet, "/api/integration/drivers", testKey, "")
			if w.Code != tc.wantCode {
				t.Fatalf("status = %d, want %d (body %s)", w.Code, tc.wantCode, w.Body)
			}
			if tc.wantCode == http.StatusOK {
				assertBareArray(t, w.Body.Bytes())
			}
			assertJSON(t, w.Body.Bytes(), tc.wantJSON)
		})
	}
}

// --- products ---------------------------------------------------------------

// The bare bulk pull is the regression this endpoint existed to break: AI_LM's
// GetProductsWithWeight() sends no query parameters, and the old handler
// answered 400 unless `category` or `q` was supplied.
func TestListProductsFilterIsOptional(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name       string
		target     string
		wantFilter productFilter
	}{
		{
			name:       "bare bulk pull (AI_LM's call)",
			target:     "/api/integration/products",
			wantFilter: productFilter{Limit: productBulkLimit},
		},
		{
			name:       "category filter uses the search cap",
			target:     "/api/integration/products?category=Framing+Lumber",
			wantFilter: productFilter{Category: "Framing Lumber", Limit: productSearchLimit},
		},
		{
			name:       "text search uses the search cap",
			target:     "/api/integration/products?q=2x4",
			wantFilter: productFilter{Query: "2x4", Limit: productSearchLimit},
		},
		{
			name:       "both filters",
			target:     "/api/integration/products?category=Hardware&q=nail",
			wantFilter: productFilter{Category: "Hardware", Query: "nail", Limit: productSearchLimit},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			store := fullFakeStore()
			srv := newTestServer(store, testKey)
			w := do(t, srv, http.MethodGet, tc.target, testKey, "")
			if w.Code != http.StatusOK {
				t.Fatalf("status = %d, want 200 (body %s)", w.Code, w.Body)
			}
			if store.gotProductFilter != tc.wantFilter {
				t.Errorf("store filter = %+v, want %+v", store.gotProductFilter, tc.wantFilter)
			}
		})
	}
}

func TestListProductsWireShape(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name     string
		store    *fakeAILMStore
		wantCode int
		wantJSON string
	}{
		{
			name:     "geometry present and geometry absent",
			store:    fullFakeStore(),
			wantCode: http.StatusOK,
			wantJSON: `[
			  {"id":"` + productUUID + `","sku":"LUM-248-PREM","name":"2x4x8 SPF Premium",
			   "category":"Framing Lumber","uom":"PCS","price":79900,"weight_lbs":9.5,
			   "length_in":96,"width_in":3.5,"height_in":1.5,"stackable":true,
			   "geometry_source":"parametric"},
			  {"id":"` + product2UUID + `","sku":"HW-NAIL-16D","name":"16d Common Nails 50lb",
			   "category":"Hardware","uom":"BOX","price":6500,"weight_lbs":50,
			   "length_in":null,"width_in":null,"height_in":null,"stackable":null,
			   "geometry_source":""}
			]`,
		},
		{
			name:     "empty catalog is [] not null",
			store:    &fakeAILMStore{},
			wantCode: http.StatusOK,
			wantJSON: `[]`,
		},
		{
			name:     "store failure is 500",
			store:    &fakeAILMStore{err: errors.New("boom")},
			wantCode: http.StatusInternalServerError,
			wantJSON: `{"error":"failed to query products"}`,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv := newTestServer(tc.store, testKey)
			w := do(t, srv, http.MethodGet, "/api/integration/products", testKey, "")
			if w.Code != tc.wantCode {
				t.Fatalf("status = %d, want %d (body %s)", w.Code, tc.wantCode, w.Body)
			}
			if tc.wantCode == http.StatusOK {
				assertBareArray(t, w.Body.Bytes())
			}
			assertJSON(t, w.Body.Bytes(), tc.wantJSON)
		})
	}
}

// Absent geometry must reach AI_LM as nil. On the wire that means the key is
// either omitted or explicitly null — AI_LM decodes both to a nil pointer and
// falls back to its own override table.
//
// What must NEVER happen is a zero: `"length_in":0` tells AI_LM the SKU is a
// real zero-length box, and `"stackable":false` asserts a fact nobody entered.
// That is the failure mode a COALESCE(p.length_in, 0) in the SQL would cause,
// and it is silent — the payload is still valid JSON and still decodes.
func TestProductAbsentGeometryIsNeverZero(t *testing.T) {
	t.Parallel()

	srv := newTestServer(fullFakeStore(), testKey)
	w := do(t, srv, http.MethodGet, "/api/integration/products", testKey, "")

	var raw []map[string]json.RawMessage
	if err := json.Unmarshal(w.Body.Bytes(), &raw); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(raw) != 2 {
		t.Fatalf("got %d products, want 2", len(raw))
	}
	for _, field := range []string{"length_in", "width_in", "height_in", "stackable"} {
		v, ok := raw[1][field]
		if !ok {
			continue // omitted — decodes to nil, which is the intended meaning
		}
		if string(v) != "null" {
			t.Errorf("%s = %s for a product with no PIM geometry; want null or the key omitted "+
				"(a zero/false is read as a real, entered value)", field, v)
		}
	}

	// The populated product must still carry real values, so the test above
	// cannot pass by emitting nothing at all.
	if got := string(raw[0]["length_in"]); got != "96" {
		t.Errorf("length_in = %s on the populated product, want 96", got)
	}
	if got := string(raw[0]["stackable"]); got != "true" {
		t.Errorf("stackable = %s on the populated product, want true", got)
	}
}

// --- orders -----------------------------------------------------------------

func TestListOrdersWireShape(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name       string
		target     string
		store      *fakeAILMStore
		wantCode   int
		wantJSON   string
		wantDate   string
		wantStatus string
	}{
		{
			name:       "AI_LM's call: geocoded and un-geocoded orders",
			target:     "/api/integration/orders?date=2026-08-21&status=CONFIRMED",
			store:      fullFakeStore(),
			wantCode:   http.StatusOK,
			wantDate:   "2026-08-21",
			wantStatus: "CONFIRMED",
			wantJSON: `[
			  {"id":"` + orderUUID + `","status":"CONFIRMED","customer_name":"Kelbrook Homes",
			   "address":"1885 Spall Rd, Kelowna BC","latitude":49.8801,"longitude":-119.4436,
			   "scheduled_date":"2026-08-21",
			   "lines":[
			     {"product_id":"` + productUUID + `","sku":"LUM-248-PREM","quantity":128,"weight_lbs":9.5},
			     {"product_id":"` + product2UUID + `","sku":"HW-NAIL-16D","quantity":2,"weight_lbs":50}
			   ]},
			  {"id":"` + order2UUID + `","status":"CONFIRMED","customer_name":"Okanagan Builders",
			   "address":"9000 Summit Pkwy, Kelowna BC","scheduled_date":"2026-08-21",
			   "lines":[
			     {"product_id":"` + productUUID + `","sku":"LUM-248-PREM","quantity":40,"weight_lbs":9.5}
			   ]}
			]`,
		},
		{
			name:     "unfiltered pull passes empty filters through",
			target:   "/api/integration/orders",
			store:    &fakeAILMStore{},
			wantCode: http.StatusOK,
			wantJSON: `[]`,
		},
		{
			name:     "malformed date is rejected before it reaches SQL",
			target:   "/api/integration/orders?date=21-08-2026",
			store:    fullFakeStore(),
			wantCode: http.StatusBadRequest,
			wantJSON: `{"error":"invalid date; expected YYYY-MM-DD"}`,
		},
		{
			name:     "store failure is 500",
			target:   "/api/integration/orders?date=2026-08-21",
			store:    &fakeAILMStore{err: errors.New("boom")},
			wantCode: http.StatusInternalServerError,
			wantJSON: `{"error":"failed to query orders"}`,
			wantDate: "2026-08-21",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv := newTestServer(tc.store, testKey)
			w := do(t, srv, http.MethodGet, tc.target, testKey, "")
			if w.Code != tc.wantCode {
				t.Fatalf("status = %d, want %d (body %s)", w.Code, tc.wantCode, w.Body)
			}
			if tc.wantCode == http.StatusOK {
				assertBareArray(t, w.Body.Bytes())
			}
			assertJSON(t, w.Body.Bytes(), tc.wantJSON)
			if tc.store.gotOrdersDate != tc.wantDate {
				t.Errorf("store date = %q, want %q", tc.store.gotOrdersDate, tc.wantDate)
			}
			if tc.store.gotOrdersStatus != tc.wantStatus {
				t.Errorf("store status = %q, want %q", tc.store.gotOrdersStatus, tc.wantStatus)
			}
		})
	}
}

// An order that was never geocoded must omit latitude/longitude entirely rather
// than send 0/0.
func TestOrderMissingCoordinatesAreOmitted(t *testing.T) {
	t.Parallel()

	srv := newTestServer(fullFakeStore(), testKey)
	w := do(t, srv, http.MethodGet, "/api/integration/orders", testKey, "")

	var raw []map[string]json.RawMessage
	if err := json.Unmarshal(w.Body.Bytes(), &raw); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(raw) != 2 {
		t.Fatalf("got %d orders, want 2", len(raw))
	}
	for _, field := range []string{"latitude", "longitude"} {
		if v, ok := raw[1][field]; ok {
			t.Errorf("%s = %s on an un-geocoded order; want the key absent so AI_LM sees nil, not null island", field, v)
		}
	}
}

// --- delivery route write-back ----------------------------------------------

func TestCreateDeliveryRoute(t *testing.T) {
	t.Parallel()

	validBody := `{
	  "vehicle_id":"` + vehicleUUID + `",
	  "driver_id":"` + driverUUID + `",
	  "scheduled_date":"2026-08-21",
	  "stops":[{"order_id":"` + orderUUID + `","sequence":1,"lat":49.88,"lng":-119.44}],
	  "load_manifest":{"steps":[{"sku":"LUM-248-PREM","slot":1}]}
	}`

	cases := []struct {
		name     string
		body     string
		wantCode int
		wantJSON string
	}{
		{
			name:     "approved plan is accepted",
			body:     validBody,
			wantCode: http.StatusCreated,
			wantJSON: `{"route_id":"` + routeUUID + `","stop_count":1,"created":true,"replaced":false}`,
		},
		{
			name: "driver_id is optional (AI_LM omits it for an unassigned truck)",
			body: `{"vehicle_id":"` + vehicleUUID + `","scheduled_date":"2026-08-21",
			        "stops":[{"order_id":"` + orderUUID + `","sequence":1,"lat":49.88,"lng":-119.44}]}`,
			wantCode: http.StatusCreated,
			wantJSON: `{"route_id":"` + routeUUID + `","stop_count":1,"created":true,"replaced":false}`,
		},
		{
			name:     "malformed body",
			body:     `{not json`,
			wantCode: http.StatusBadRequest,
			wantJSON: `{"error":"invalid request body"}`,
		},
		{
			name:     "missing vehicle_id",
			body:     `{"scheduled_date":"2026-08-21","stops":[{"order_id":"` + orderUUID + `"}]}`,
			wantCode: http.StatusBadRequest,
			wantJSON: `{"error":"invalid vehicle_id"}`,
		},
		{
			name:     "non-uuid driver_id",
			body:     `{"vehicle_id":"` + vehicleUUID + `","driver_id":"dana","scheduled_date":"2026-08-21","stops":[{"order_id":"` + orderUUID + `"}]}`,
			wantCode: http.StatusBadRequest,
			wantJSON: `{"error":"invalid driver_id"}`,
		},
		{
			name:     "missing scheduled_date",
			body:     `{"vehicle_id":"` + vehicleUUID + `","stops":[{"order_id":"` + orderUUID + `"}]}`,
			wantCode: http.StatusBadRequest,
			wantJSON: `{"error":"invalid scheduled_date; expected YYYY-MM-DD"}`,
		},
		{
			name:     "non-ISO scheduled_date",
			body:     `{"vehicle_id":"` + vehicleUUID + `","scheduled_date":"08/21/2026","stops":[{"order_id":"` + orderUUID + `"}]}`,
			wantCode: http.StatusBadRequest,
			wantJSON: `{"error":"invalid scheduled_date; expected YYYY-MM-DD"}`,
		},
		{
			name:     "empty stop list",
			body:     `{"vehicle_id":"` + vehicleUUID + `","scheduled_date":"2026-08-21","stops":[]}`,
			wantCode: http.StatusBadRequest,
			wantJSON: `{"error":"route must have at least one stop"}`,
		},
		{
			name:     "non-uuid order_id in a stop",
			body:     `{"vehicle_id":"` + vehicleUUID + `","scheduled_date":"2026-08-21","stops":[{"order_id":"order-7","sequence":1}]}`,
			wantCode: http.StatusBadRequest,
			wantJSON: `{"error":"invalid order_id in stops"}`,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv := newTestServer(fullFakeStore(), testKey)
			w := do(t, srv, http.MethodPost, "/api/integration/delivery-routes", testKey, tc.body)
			if w.Code != tc.wantCode {
				t.Fatalf("status = %d, want %d (body %s)", w.Code, tc.wantCode, w.Body)
			}
			assertJSON(t, w.Body.Bytes(), tc.wantJSON)
		})
	}
}

// The write-back body must reach the store intact — in particular the opaque
// load_manifest, which powers the yard "Pack Trucks" surface.
func TestCreateDeliveryRoutePassesManifestThrough(t *testing.T) {
	t.Parallel()

	store := fullFakeStore()
	srv := newTestServer(store, testKey)
	body := `{
	  "vehicle_id":"` + vehicleUUID + `",
	  "driver_id":"` + driverUUID + `",
	  "scheduled_date":"2026-08-21",
	  "stops":[
	    {"order_id":"` + orderUUID + `","sequence":1,"lat":49.88,"lng":-119.44},
	    {"order_id":"` + order2UUID + `","sequence":2,"lat":49.94,"lng":-119.39}
	  ],
	  "load_manifest":{"steps":[{"sku":"LUM-248-PREM","slot":1}]}
	}`

	if w := do(t, srv, http.MethodPost, "/api/integration/delivery-routes", testKey, body); w.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201 (body %s)", w.Code, w.Body)
	}
	if store.gotRoute == nil {
		t.Fatal("store never received the route")
	}
	if got := len(store.gotRoute.Stops); got != 2 {
		t.Errorf("stops = %d, want 2", got)
	}
	if store.gotRoute.Stops[1].Sequence != 2 {
		t.Errorf("stop sequence = %d, want 2", store.gotRoute.Stops[1].Sequence)
	}
	if store.gotRoute.Stops[0].Lat == nil || *store.gotRoute.Stops[0].Lat != 49.88 {
		t.Errorf("stop lat = %v, want 49.88", store.gotRoute.Stops[0].Lat)
	}
	assertJSON(t, store.gotRoute.LoadManifest, `{"steps":[{"sku":"LUM-248-PREM","slot":1}]}`)
}

func TestCreateDeliveryRouteStoreFailureIs500(t *testing.T) {
	t.Parallel()

	srv := newTestServer(&fakeAILMStore{err: errors.New("boom")}, testKey)
	body := `{"vehicle_id":"` + vehicleUUID + `","scheduled_date":"2026-08-21",
	          "stops":[{"order_id":"` + orderUUID + `","sequence":1,"lat":1,"lng":2}]}`
	w := do(t, srv, http.MethodPost, "/api/integration/delivery-routes", testKey, body)
	if w.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500 (body %s)", w.Code, w.Body)
	}
	assertJSON(t, w.Body.Bytes(), `{"error":"failed to create delivery route"}`)
}

// --- validate-staff (AI_LM's login path) ------------------------------------

func TestValidateStaff(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name     string
		body     string
		wantCode int
		wantJSON string
	}{
		{
			name:     "entitled staff member",
			body:     `{"email":"dispatcher@gable.com"}`,
			wantCode: http.StatusOK,
			wantJSON: `{"staff_id":"` + staffUUID + `","email":"dispatcher@gable.com",
			            "name":"Dana Ramirez","entitled":true,
			            "roles":["dispatcher"],"modules":["ai_lm"]}`,
		},
		{
			name:     "email lookup is case-insensitive",
			body:     `{"email":"Dispatcher@Gable.com"}`,
			wantCode: http.StatusOK,
			wantJSON: `{"staff_id":"` + staffUUID + `","email":"dispatcher@gable.com",
			            "name":"Dana Ramirez","entitled":true,
			            "roles":["dispatcher"],"modules":["ai_lm"]}`,
		},
		{
			name: "granted but deactivated staff member is not entitled",
			body: `{"email":"onleave@gable.com"}`,
			// 200, not 401: AI_LM's client turns any non-2xx into a transport
			// error and would report "GableLBM is down" for a failed login.
			wantCode: http.StatusOK,
			wantJSON: `{"staff_id":"` + staffUUID + `","email":"onleave@gable.com",
			            "name":"Sam Vega","entitled":false,
			            "roles":["dispatcher"],"modules":["ai_lm"]}`,
		},
		{
			name:     "active staff member without the ai_lm module is not entitled",
			body:     `{"email":"yard@gable.com"}`,
			wantCode: http.StatusOK,
			wantJSON: `{"staff_id":"` + staffUUID + `","email":"yard@gable.com",
			            "name":"Yuki Tan","entitled":false,
			            "roles":["yard"],"modules":["pos"]}`,
		},
		{
			name: "roles and modules are never null",
			body: `{"email":"noroles@gable.com"}`,
			// AI_LM copies Roles straight into a JWT claim; a null would
			// marshal as `null` in the token instead of an empty array.
			wantCode: http.StatusOK,
			wantJSON: `{"staff_id":"` + staffUUID + `","email":"noroles@gable.com",
			            "name":"Pat Doe","entitled":false,"roles":[],"modules":[]}`,
		},
		{
			name:     "unknown email is a non-entitled result, not an error",
			body:     `{"email":"stranger@example.com"}`,
			wantCode: http.StatusOK,
			wantJSON: `{"entitled":false,"roles":[],"modules":[]}`,
		},
		{
			name:     "staff_no lookup",
			body:     `{"staff_no":"STF-001"}`,
			wantCode: http.StatusOK,
			wantJSON: `{"entitled":false,"roles":[],"modules":[]}`,
		},
		{
			name:     "malformed body",
			body:     `{nope`,
			wantCode: http.StatusBadRequest,
			wantJSON: `{"error":"invalid request body"}`,
		},
		{
			name:     "no identifier supplied",
			body:     `{}`,
			wantCode: http.StatusBadRequest,
			wantJSON: `{"error":"email or staff_no required"}`,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv := newTestServer(fullFakeStore(), testKey)
			w := do(t, srv, http.MethodPost, "/api/integration/validate-staff", testKey, tc.body)
			if w.Code != tc.wantCode {
				t.Fatalf("status = %d, want %d (body %s)", w.Code, tc.wantCode, w.Body)
			}
			assertJSON(t, w.Body.Bytes(), tc.wantJSON)
		})
	}
}

func TestValidateStaffStoreFailureIs500(t *testing.T) {
	t.Parallel()

	srv := newTestServer(&fakeAILMStore{err: errors.New("boom")}, testKey)
	w := do(t, srv, http.MethodPost, "/api/integration/validate-staff", testKey, `{"email":"a@b.com"}`)
	if w.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500 (body %s)", w.Code, w.Body)
	}
	assertJSON(t, w.Body.Bytes(), `{"error":"failed to validate staff"}`)
}

// The unknown-staff response must not echo the submitted address back, so an
// unauthenticated prober cannot use it to confirm what it sent.
func TestValidateStaffUnknownOmitsIdentity(t *testing.T) {
	t.Parallel()

	srv := newTestServer(fullFakeStore(), testKey)
	w := do(t, srv, http.MethodPost, "/api/integration/validate-staff", testKey,
		`{"email":"stranger@example.com"}`)

	var got map[string]json.RawMessage
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	for _, field := range []string{"staff_id", "email", "name"} {
		if v, ok := got[field]; ok {
			t.Errorf("%s = %s present for an unknown staff member; want the key omitted", field, v)
		}
	}
}

// --- helper unit tests ------------------------------------------------------

func TestResolveGeometrySource(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		in   ProductResponse
		want string
	}{
		{"explicit source wins", ProductResponse{GeometrySource: "MANUAL", LengthIn: f64(96)}, "MANUAL"},
		{"dimensions imply parametric", ProductResponse{LengthIn: f64(96)}, "parametric"},
		{"partial dimensions still imply parametric", ProductResponse{HeightIn: f64(1.5)}, "parametric"},
		{"no geometry reports nothing", ProductResponse{}, ""},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := resolveGeometrySource(tc.in); got != tc.want {
				t.Errorf("resolveGeometrySource = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestNonNilRendersEmptyArray(t *testing.T) {
	t.Parallel()

	var nilSlice []VehicleResponse
	b, err := json.Marshal(nonNil(nilSlice))
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if string(b) != "[]" {
		t.Errorf("nonNil(nil) marshalled to %s, want []", b)
	}
}
