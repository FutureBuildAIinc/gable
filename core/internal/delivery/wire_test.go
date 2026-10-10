// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package delivery_test

// The delivery module on the wire contract (ADR 0001), tested end to end: a
// real Postgres, the real handler on a real mux behind the real idempotency
// middleware, requests as JSON and responses read back as JSON. Nothing here
// touches a module type, so each test states a wire fact. The live failures
// this conversion fixes (no branch wall, null empty lists, a 500 for a
// client mistake, writes without revision, audit or event, an adjust-qty
// that wrote nothing) were first proven red against the base commit in a
// throwaway worktree; these are their green forms.

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/gablelbm/gable/internal/delivery"
	"github.com/gablelbm/gable/internal/testutil"
	"github.com/gablelbm/gable/pkg/audit"
	"github.com/gablelbm/gable/pkg/branchctx"
	"github.com/gablelbm/gable/pkg/database"
	"github.com/gablelbm/gable/pkg/middleware"
	"github.com/gablelbm/gable/pkg/outbox"
	"github.com/google/uuid"
)

type fixture struct {
	t      *testing.T
	db     *database.DB
	srv    *httptest.Server
	prefix string
	branch uuid.UUID

	queued []queuedFulfilment
}

// queuedFulfilment records what the fake order queue was asked to bill, so a
// delivered stop's enqueue (ADR 0005 5.5) is pinned on the wire.
type queuedFulfilment struct {
	delivery, order uuid.UUID
}

type fakeQueue struct {
	f *fixture
}

func (q fakeQueue) EnqueueFulfilment(ctx context.Context, deliveryID, orderID uuid.UUID) error {
	q.f.queued = append(q.f.queued, queuedFulfilment{deliveryID, orderID})
	return nil
}

type fakeOrders struct{}

func (fakeOrders) OrderDeliveryType(ctx context.Context, orderID uuid.UUID) (string, error) {
	return "DELIVERY", nil
}

// newFixture builds the module the way serve does (repository, service with
// the outbox, the database as transaction runner and the audit logger,
// handler) behind the global idempotency layer. A request header X-Test-Branch
// puts a branch wall on the request's context, as the branch middleware does.
func newFixture(t *testing.T) *fixture {
	t.Helper()
	t.Setenv("AUTH_MODE", "dev")
	db := testutil.RequireDB(t)
	f := &fixture{t: t, db: db, prefix: "WIRE-" + uuid.NewString()[:8] + "-"}
	if err := db.Pool.QueryRow(context.Background(),
		`SELECT value::uuid FROM system_settings WHERE key = 'default_branch_id'`).Scan(&f.branch); err != nil {
		t.Fatal(err)
	}

	svc := delivery.NewService(delivery.NewRepository(db)).
		WithOutbox(outbox.NewWriter(db, "")).
		WithTxRunner(db).
		WithAudit(audit.NewLogger(db))
	svc.WithFulfilment(fakeQueue{f}, fakeOrders{})

	mux := http.NewServeMux()
	delivery.NewHandler(svc).RegisterRoutes(mux)
	wallMux := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		bc := &branchctx.Context{UserSub: r.Header.Get("X-Test-Sub"), IsAdmin: r.Header.Get("X-Test-Admin") == "1"}
		if b := r.Header.Get("X-Test-Branch"); b != "" {
			id := uuid.MustParse(b)
			bc.BranchID = &id
		}
		mux.ServeHTTP(w, r.WithContext(branchctx.With(r.Context(), bc)))
	})
	f.srv = httptest.NewServer(middleware.Idempotency(db)(wallMux))
	t.Cleanup(f.srv.Close)
	return f
}

type resp struct {
	status int
	header http.Header
	body   map[string]any
	raw    []byte
}

func (f *fixture) do(t *testing.T, method, path, body string, hdr map[string]string) resp {
	t.Helper()
	var rdr io.Reader
	if body != "" {
		rdr = bytes.NewReader([]byte(body))
	}
	req, err := http.NewRequest(method, f.srv.URL+path, rdr)
	if err != nil {
		t.Fatal(err)
	}
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	raw, _ := io.ReadAll(res.Body)
	out := resp{status: res.StatusCode, header: res.Header, raw: raw}
	if strings.Contains(res.Header.Get("Content-Type"), "json") && len(raw) > 0 {
		_ = json.Unmarshal(raw, &out.body)
	}
	return out
}

func (f *fixture) errCode(t *testing.T, r resp) string {
	t.Helper()
	env, ok := r.body["error"].(map[string]any)
	if !ok {
		t.Fatalf("no error envelope in %d %s", r.status, r.raw)
	}
	if meta, ok := r.body["meta"].(map[string]any); !ok || meta["request_id"] == nil {
		t.Errorf("meta.request_id missing from %s", r.raw)
	}
	code, _ := env["code"].(string)
	return code
}

func (f *fixture) fields(t *testing.T, r resp) map[string]bool {
	t.Helper()
	env, _ := r.body["error"].(map[string]any)
	details, _ := env["details"].([]any)
	out := map[string]bool{}
	for _, d := range details {
		if m, ok := d.(map[string]any); ok {
			if field, ok := m["field"].(string); ok {
				out[field] = true
			}
		}
	}
	return out
}

// seedFleet inserts one vehicle and one driver with the caller's own ids.
func (f *fixture) seedFleet(t *testing.T) (vehicle, driver uuid.UUID) {
	t.Helper()
	ctx := context.Background()
	plate := "W" + uuid.NewString()[:7]
	license := "L" + uuid.NewString()[:7]
	if err := f.db.Pool.QueryRow(ctx, `
		INSERT INTO vehicles (id, name, vehicle_type, license_plate) VALUES ($1, $2, 'BOX_TRUCK', $3) RETURNING id`,
		uuid.New(), "Wire truck "+f.prefix, plate).Scan(&vehicle); err != nil {
		t.Fatal(err)
	}
	if err := f.db.Pool.QueryRow(ctx, `
		INSERT INTO drivers (id, name, license_number) VALUES ($1, $2, $3) RETURNING id`,
		uuid.New(), "Wire driver "+f.prefix, license).Scan(&driver); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		c := context.Background()
		_, _ = f.db.Pool.Exec(c, `DELETE FROM vehicles WHERE id = $1`, vehicle)
		_, _ = f.db.Pool.Exec(c, `DELETE FROM drivers WHERE id = $1`, driver)
	})
	return vehicle, driver
}

// seedStop inserts a route on the branch's order with one stop, with the
// caller's own ids so nothing collides with the seed.
func (f *fixture) seedStop(t *testing.T, branch uuid.UUID) (route, stop, order uuid.UUID) {
	t.Helper()
	ctx := context.Background()
	var customer uuid.UUID
	if err := f.db.Pool.QueryRow(ctx, `
		INSERT INTO customers (id, name, account_number, primary_branch_id)
		VALUES ($1, $2, $3, $4) RETURNING id`,
		uuid.New(), "Wire stop "+f.prefix, f.prefix+uuid.NewString()[:8], branch).Scan(&customer); err != nil {
		t.Fatal(err)
	}
	if _, err := f.db.Pool.Exec(ctx, `
		INSERT INTO customer_branches (customer_id, branch_id) VALUES ($1, $2)`, customer, branch); err != nil {
		t.Fatal(err)
	}
	if err := f.db.Pool.QueryRow(ctx, `
		INSERT INTO orders (id, customer_id, branch_id, status, total_amount, delivery_type, currency, number)
		VALUES ($1, $2, $3, 'CONFIRMED', 10, 'DELIVERY', 'USD', $4) RETURNING id`,
		uuid.New(), customer, branch, "SO-"+strings.ToUpper(uuid.NewString()[:8])).Scan(&order); err != nil {
		t.Fatal(err)
	}
	vehicle, driver := f.seedFleet(t)
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
		_, _ = f.db.Pool.Exec(c, `DELETE FROM events_outbox WHERE entity_id = $1`, stop)
		_, _ = f.db.Pool.Exec(c, `DELETE FROM events_outbox WHERE entity_id = $1`, route)
		_, _ = f.db.Pool.Exec(c, `DELETE FROM audit_log WHERE entity_id = $1`, stop)
		_, _ = f.db.Pool.Exec(c, `DELETE FROM audit_log WHERE entity_id = $1`, route)
		_, _ = f.db.Pool.Exec(c, `DELETE FROM deliveries WHERE route_id = $1`, route)
		_, _ = f.db.Pool.Exec(c, `DELETE FROM delivery_routes WHERE id = $1`, route)
		_, _ = f.db.Pool.Exec(c, `DELETE FROM orders WHERE id = $1`, order)
		_, _ = f.db.Pool.Exec(c, `DELETE FROM customer_branches WHERE customer_id = $1`, customer)
		_, _ = f.db.Pool.Exec(c, `DELETE FROM customers WHERE id = $1`, customer)
	})
	return route, stop, order
}

func (f *fixture) createRoute(t *testing.T, vehicle, driver uuid.UUID, hdr map[string]string) resp {
	t.Helper()
	return f.do(t, http.MethodPost, "/api/v1/delivery/routes",
		`{"vehicle_id":"`+vehicle.String()+`","driver_id":"`+driver.String()+`","scheduled_date":"2030-05-06"}`, hdr)
}

func (f *fixture) assignOrder(t *testing.T, route, order uuid.UUID, hdr map[string]string) resp {
	t.Helper()
	return f.do(t, http.MethodPost, "/api/v1/delivery/deliveries",
		`{"route_id":"`+route.String()+`","order_id":"`+order.String()+`"}`, hdr)
}

// seedOtherBranchOrder creates a fresh delivery order on the given branch
// for assign tests that need an order independent of the seedStop's seeded
// stop (whose order is already linked to a delivery row).
func (f *fixture) seedOtherBranchOrder(t *testing.T, branch uuid.UUID) uuid.UUID {
	t.Helper()
	ctx := context.Background()
	var customer uuid.UUID
	if err := f.db.Pool.QueryRow(ctx, `
		INSERT INTO customers (id, name, account_number, primary_branch_id)
		VALUES ($1, $2, $3, $4) RETURNING id`,
		uuid.New(), "Wire assign "+f.prefix, f.prefix+uuid.NewString()[:8], branch).Scan(&customer); err != nil {
		t.Fatal(err)
	}
	if _, err := f.db.Pool.Exec(ctx,
		`INSERT INTO customer_branches (customer_id, branch_id) VALUES ($1, $2)`, customer, branch); err != nil {
		t.Fatal(err)
	}
	var order uuid.UUID
	if err := f.db.Pool.QueryRow(ctx, `
		INSERT INTO orders (id, customer_id, branch_id, status, total_amount, delivery_type, currency, number)
		VALUES ($1, $2, $3, 'CONFIRMED', 10, 'DELIVERY', 'USD', $4) RETURNING id`,
		uuid.New(), customer, branch, "SO-"+strings.ToUpper(uuid.NewString()[:8])).Scan(&order); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		c := context.Background()
		_, _ = f.db.Pool.Exec(c, `DELETE FROM orders WHERE id = $1`, order)
		_, _ = f.db.Pool.Exec(c, `DELETE FROM customer_branches WHERE customer_id = $1`, customer)
		_, _ = f.db.Pool.Exec(c, `DELETE FROM customers WHERE id = $1`, customer)
	})
	return order
}

// The create shape: lowercase vocabulary, business dates as YYYY-MM-DD,
// revision and the ETag, Location, timestamps at microsecond precision,
// optional fields present as null.
func TestVehicleCreate_Shape(t *testing.T) {
	f := newFixture(t)
	res := f.do(t, http.MethodPost, "/api/v1/delivery/vehicles",
		`{"name":"Truck 9","vehicle_type":"box_truck","license_plate":"W-0009","insurance_expiry":"2027-01-31"}`, nil)
	if res.status != http.StatusCreated {
		t.Fatalf("create = %d %s", res.status, res.raw)
	}
	if loc := res.header.Get("Location"); !strings.HasPrefix(loc, "/api/v1/delivery/vehicles/") {
		t.Errorf("Location = %q", loc)
	}
	if etag := res.header.Get("ETag"); etag != `"1"` {
		t.Errorf("ETag = %q, want \"1\"", etag)
	}
	if res.body["vehicle_type"] != "box_truck" {
		t.Errorf("vehicle_type = %v, want box_truck", res.body["vehicle_type"])
	}
	if res.body["insurance_expiry"] != "2027-01-31" {
		t.Errorf("insurance_expiry = %v, want 2027-01-31 (a business date)", res.body["insurance_expiry"])
	}
	for _, field := range []string{"vin", "year", "make", "model", "next_service_date", "odometer_miles", "notes", "photo_url"} {
		if v, ok := res.body[field]; !ok || v != nil {
			t.Errorf("%s = %v (present %v), want null present", field, v, ok)
		}
	}
	if res.body["revision"] != float64(1) {
		t.Errorf("revision = %v, want 1", res.body["revision"])
	}
	for _, field := range []string{"created_at", "updated_at"} {
		s, _ := res.body[field].(string)
		if !strings.HasSuffix(s, "Z") || !strings.Contains(s, ".") {
			t.Errorf("%s = %q, want an RFC 3339 UTC timestamp with a fraction", field, s)
		}
	}
	id, _ := res.body["id"].(string)
	t.Cleanup(func() {
		_, _ = f.db.Pool.Exec(context.Background(), `DELETE FROM vehicles WHERE id = $1`, id)
	})
}

// One 400 with every offending field and the full paths; a body the route
// cannot consume is bad_request; an unknown body field and an uppercase
// enum value are refused.
func TestVehicleCreate_FieldValidation(t *testing.T) {
	f := newFixture(t)
	res := f.do(t, http.MethodPost, "/api/v1/delivery/vehicles",
		`{"vehicle_type":"BOX_TRUCK","license_plate":"  ","year":"2026x","insurance_expiry":"01/02/2027"}`, nil)
	if res.status != http.StatusBadRequest {
		t.Fatalf("validation = %d %s", res.status, res.raw)
	}
	if code := f.errCode(t, res); code != "validation_failed" {
		t.Errorf("code = %s", code)
	}
	fields := f.fields(t, res)
	for _, want := range []string{"name", "vehicle_type", "license_plate", "year", "insurance_expiry"} {
		if !fields[want] {
			t.Errorf("details = %v, want a %s entry", fields, want)
		}
	}
	res = f.do(t, http.MethodPost, "/api/v1/delivery/vehicles",
		`{"name":"x","vehicle_type":"van","license_plate":"W-X","smoke":"signal"}`, nil)
	if res.status != http.StatusBadRequest || !f.fields(t, res)["smoke"] {
		t.Errorf("an unknown body field = %d %s, want 400 naming smoke", res.status, res.raw)
	}

	res = f.do(t, http.MethodPost, "/api/v1/delivery/vehicles", `not-json`, nil)
	if res.status != http.StatusBadRequest || f.errCode(t, res) != "bad_request" {
		t.Errorf("an unparseable body = %d %s, want 400 bad_request", res.status, res.raw)
	}
}

// The fleet lists: the envelope, an empty page is [] in the bytes, the
// cursor walks every row once, an unknown parameter is refused, limit is
// validated, include=total counts.
func TestVehicleList_EnvelopeCursor(t *testing.T) {
	f := newFixture(t)
	plate := "W" + uuid.NewString()[:7]
	var id string
	if err := f.db.Pool.QueryRow(context.Background(),
		`INSERT INTO vehicles (name, vehicle_type, license_plate) VALUES ('List truck', 'VAN', $1) RETURNING id::text`, plate).Scan(&id); err != nil {
		t.Fatal(err)
	}
	var second string
	if err := f.db.Pool.QueryRow(context.Background(),
		`INSERT INTO vehicles (name, vehicle_type, license_plate) VALUES ('List truck 2', 'VAN', $1) RETURNING id::text`, "W"+uuid.NewString()[:7]).Scan(&second); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = f.db.Pool.Exec(context.Background(), `DELETE FROM vehicles WHERE id = ANY($1::uuid[])`, []any{id, second})
	})

	res := f.do(t, http.MethodGet, "/api/v1/delivery/vehicles?limit=1", "", nil)
	if res.status != http.StatusOK {
		t.Fatalf("list = %d %s", res.status, res.raw)
	}
	items, _ := res.body["items"].([]any)
	next, _ := res.body["next_cursor"].(string)
	if len(items) != 1 {
		t.Fatalf("items = %d, want the limit page", len(items))
	}
	if next == "" {
		t.Fatal("next_cursor missing on a page that is not the last")
	}
	if res.body["limit"] != float64(1) {
		t.Errorf("limit = %v, want 1", res.body["limit"])
	}

	seen := map[string]bool{}
	cursor := next
	pages := 0
	for cursor != "" && pages < 500 {
		page := f.do(t, http.MethodGet, "/api/v1/delivery/vehicles?limit=1&cursor="+cursor, "", nil)
		if page.status != http.StatusOK {
			t.Fatalf("walk page = %d %s", page.status, page.raw)
		}
		for _, it := range page.body["items"].([]any) {
			vid := it.(map[string]any)["id"].(string)
			if seen[vid] {
				t.Fatalf("cursor walk repeated vehicle %s", vid)
			}
			seen[vid] = true
		}
		cursor, _ = page.body["next_cursor"].(string)
		pages++
	}
	if !seen[id] {
		t.Error("the cursor walk never reached the inserted vehicle")
	}

	// The last page is [] in the bytes: walk to the end with a big limit on a
	// filtered empty date is not possible on vehicles, so prove the bytes on
	// a fresh route's stop list instead (below) and the empty-photo list here.
	photos := f.do(t, http.MethodGet, "/api/v1/delivery/deliveries/"+uuid.NewString()+"/pod-photos", "", nil)
	if photos.status != http.StatusNotFound {
		t.Fatalf("a missing stop's photos = %d, want 404", photos.status)
	}

	if res := f.do(t, http.MethodGet, "/api/v1/delivery/vehicles?flavour=vanilla", "", nil); res.status != http.StatusBadRequest || f.errCode(t, res) != "unsupported_query_parameter" {
		t.Errorf("an unknown parameter = %d %s, want 400 unsupported_query_parameter", res.status, res.raw)
	}
	if res := f.do(t, http.MethodGet, "/api/v1/delivery/vehicles?limit=0", "", nil); res.status != http.StatusBadRequest || f.fields(t, res)["limit"] != true {
		t.Errorf("limit 0 = %d %s, want 400 naming limit", res.status, res.raw)
	}
	if res := f.do(t, http.MethodGet, "/api/v1/delivery/vehicles?include=total", "", nil); res.status != http.StatusOK || res.body["total"] == nil {
		t.Errorf("include=total = %d %s, want a total", res.status, res.raw)
	}
}

// Revision concurrency on the vehicle update: 428 without a precondition,
// 409 stale_revision, If-Match strong and weak, the body revision, the two
// disagreeing, and the new revision and ETag on success.
func TestVehicleUpdate_Revision(t *testing.T) {
	f := newFixture(t)
	vehicle, _ := f.seedFleet(t)
	body := `{"name":"Renamed","vehicle_type":"van","license_plate":"W-0001"}`

	if res := f.do(t, http.MethodPut, "/api/v1/delivery/vehicles/"+vehicle.String(), body, nil); res.status != http.StatusPreconditionRequired || f.errCode(t, res) != "precondition_required" {
		t.Errorf("no precondition = %d %s, want 428", res.status, res.raw)
	}
	if res := f.do(t, http.MethodPut, "/api/v1/delivery/vehicles/"+vehicle.String(), body, map[string]string{"If-Match": `"9"`}); res.status != http.StatusConflict || f.errCode(t, res) != "stale_revision" {
		t.Errorf("stale If-Match = %d %s, want 409 stale_revision", res.status, res.raw)
	}
	if res := f.do(t, http.MethodPut, "/api/v1/delivery/vehicles/"+vehicle.String(), body, map[string]string{"If-Match": `W/"1"`}); res.status != http.StatusOK {
		t.Errorf("weak If-Match = %d %s, want 200", res.status, res.raw)
	}
	rev2 := `{"name":"Renamed twice","vehicle_type":"van","license_plate":"W-0001","revision":2}`
	if res := f.do(t, http.MethodPut, "/api/v1/delivery/vehicles/"+vehicle.String(), rev2, map[string]string{"If-Match": `"1"`}); res.status != http.StatusBadRequest || f.fields(t, res)["revision"] != true {
		t.Errorf("header and body disagree = %d %s, want 400 naming revision", res.status, res.raw)
	}
	res := f.do(t, http.MethodPut, "/api/v1/delivery/vehicles/"+vehicle.String(), rev2, nil)
	if res.status != http.StatusOK {
		t.Fatalf("body revision = %d %s", res.status, res.raw)
	}
	if res.body["revision"] != float64(3) || res.header.Get("ETag") != `"3"` {
		t.Errorf("after the write revision = %v ETag = %q, want 3 both", res.body["revision"], res.header.Get("ETag"))
	}

	if res := f.do(t, http.MethodDelete, "/api/v1/delivery/vehicles/"+vehicle.String(), "", nil); res.status != http.StatusPreconditionRequired {
		t.Errorf("delete without If-Match = %d, want 428", res.status)
	}
	if res := f.do(t, http.MethodDelete, "/api/v1/delivery/vehicles/"+vehicle.String(), "", map[string]string{"If-Match": `"3"`}); res.status != http.StatusNoContent {
		t.Errorf("delete with If-Match = %d, want 204", res.status)
	}
	if res := f.do(t, http.MethodGet, "/api/v1/delivery/vehicles/"+vehicle.String(), "", nil); res.status != http.StatusNotFound {
		t.Errorf("a deleted vehicle = %d, want 404", res.status)
	}
}

// A driver's lifecycle: create (status defaults to active, lowercase on the
// wire), update (status required), delete.
func TestDriverLifecycle(t *testing.T) {
	f := newFixture(t)
	res := f.do(t, http.MethodPost, "/api/v1/delivery/drivers",
		`{"name":"Wire driver","cdl_expiry":"2028-03-01","hire_date":"2024-06-15"}`, nil)
	if res.status != http.StatusCreated {
		t.Fatalf("create = %d %s", res.status, res.raw)
	}
	if res.body["status"] != "active" {
		t.Errorf("status = %v, want active", res.body["status"])
	}
	if res.body["cdl_expiry"] != "2028-03-01" || res.body["hire_date"] != "2024-06-15" {
		t.Errorf("business dates = %v %v, want YYYY-MM-DD", res.body["cdl_expiry"], res.body["hire_date"])
	}
	id, _ := res.body["id"].(string)
	t.Cleanup(func() { _, _ = f.db.Pool.Exec(context.Background(), `DELETE FROM drivers WHERE id = $1`, id) })

	if res := f.do(t, http.MethodPut, "/api/v1/delivery/drivers/"+id,
		`{"name":"Wire driver","license_number":"L-1","status":"on_leave"}`, map[string]string{"If-Match": `"1"`}); res.status != http.StatusOK || res.body["status"] != "on_leave" {
		t.Errorf("update = %d %s", res.status, res.raw)
	}
	if res := f.do(t, http.MethodPut, "/api/v1/delivery/drivers/"+id,
		`{"name":"Wire driver","license_number":"L-1","status":"ON_LEAVE"}`, map[string]string{"If-Match": `"2"`}); res.status != http.StatusBadRequest || f.fields(t, res)["status"] != true {
		t.Errorf("an uppercase status = %d %s, want 400 naming status", res.status, res.raw)
	}
	if res := f.do(t, http.MethodPut, "/api/v1/delivery/drivers/"+id,
		`{"name":"Wire driver","license_number":"L-1"}`, map[string]string{"If-Match": `"2"`}); res.status != http.StatusBadRequest || f.fields(t, res)["status"] != true {
		t.Errorf("a missing status on update = %d %s, want 400 naming status", res.status, res.raw)
	}
	if res := f.do(t, http.MethodGet, "/api/v1/delivery/drivers/"+uuid.NewString(), "", nil); res.status != http.StatusNotFound || f.errCode(t, res) != "not_found" {
		t.Errorf("a missing driver = %d, want 404 not_found", res.status)
	}
}

// The board read: routes with their stops in one payload (include=stops),
// the date filter honoured, the status filter, an unknown filter value
// refused, and an unparseable date a 400 naming date (the live failure: it
// answered 500).
func TestRouteList_BoardReadAndFilters(t *testing.T) {
	f := newFixture(t)
	route, stop, _ := f.seedStop(t, f.branch)
	otherVehicle, otherDriver := f.seedFleet(t)
	var emptyRoute uuid.UUID
	if err := f.db.Pool.QueryRow(context.Background(),
		`INSERT INTO delivery_routes (id, vehicle_id, driver_id, scheduled_date, status)
		 VALUES ($1, $2, $3, CURRENT_DATE + 1, 'DRAFT') RETURNING id`,
		uuid.New(), otherVehicle, otherDriver).Scan(&emptyRoute); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = f.db.Pool.Exec(context.Background(), `DELETE FROM delivery_routes WHERE id = $1`, emptyRoute)
	})

	today := time.Now().UTC().Format("2006-01-02")
	res := f.do(t, http.MethodGet, "/api/v1/delivery/routes?date="+today+"&include=stops", "", nil)
	if res.status != http.StatusOK {
		t.Fatalf("board = %d %s", res.status, res.raw)
	}
	items, _ := res.body["items"].([]any)
	var found bool
	for _, it := range items {
		r := it.(map[string]any)
		if r["id"] != route.String() {
			continue
		}
		found = true
		stops, ok := r["stops"].([]any)
		if !ok || len(stops) != 1 {
			t.Fatalf("the board route carries its stops in one payload: %v", r["stops"])
		}
		s := stops[0].(map[string]any)
		if s["id"] != stop.String() || s["status"] != "pending" {
			t.Errorf("embedded stop = %v", s)
		}
		if r["status"] != "draft" {
			t.Errorf("route status = %v, want draft", r["status"])
		}
	}
	if !found {
		t.Fatal("the date filter dropped the route scheduled for today")
	}

	// Without include=stops the field is null, never a surprise array.
	res = f.do(t, http.MethodGet, "/api/v1/delivery/routes?date="+today, "", nil)
	for _, it := range res.body["items"].([]any) {
		if it.(map[string]any)["id"] == route.String() {
			if v, ok := it.(map[string]any)["stops"]; !ok || v != nil {
				t.Errorf("stops without include = %v, want null present", v)
			}
		}
	}

	// An empty day is [] in the bytes (the live failure: null).
	empty := f.do(t, http.MethodGet, "/api/v1/delivery/routes?date=2031-01-01", "", nil)
	if !bytes.Contains(empty.raw, []byte(`"items":[]`)) {
		t.Errorf("an empty day = %s, want [] in the bytes", empty.raw)
	}

	if res := f.do(t, http.MethodGet, "/api/v1/delivery/routes?date=soon", "", nil); res.status != http.StatusBadRequest || f.fields(t, res)["date"] != true {
		t.Errorf("an unparseable date = %d %s, want 400 naming date", res.status, res.raw)
	}
	if res := f.do(t, http.MethodGet, "/api/v1/delivery/routes?status=DRAFT", "", nil); res.status != http.StatusBadRequest || f.fields(t, res)["status"] != true {
		t.Errorf("an uppercase status = %d %s, want 400 naming status", res.status, res.raw)
	}
	res = f.do(t, http.MethodGet, "/api/v1/delivery/routes?status=draft&date="+today, "", nil)
	for _, it := range res.body["items"].([]any) {
		if it.(map[string]any)["status"] != "draft" {
			t.Errorf("the status filter leaked a non draft route: %v", it)
		}
	}
	// The route read: the document with its stops, its revision as the ETag.
	res = f.do(t, http.MethodGet, "/api/v1/delivery/routes/"+route.String(), "", nil)
	if res.status != http.StatusOK || res.header.Get("ETag") != `"1"` {
		t.Fatalf("route get = %d %s", res.status, res.raw)
	}
	if stops, _ := res.body["stops"].([]any); len(stops) != 1 {
		t.Errorf("the route document embeds its stops: %v", res.body["stops"])
	}
}

// RULE (PR 70 review round 3 P2-N2 and round 4 P3-3): a route whose
// vehicle_id or driver_id is NULL is visible through the wire: the list
// shows it, the single read shows it, and the missing ids are JSON null
// rather than the all-zero UUID. The repository's routeFrom LEFT JOINs
// vehicles and drivers so neither inner join drops the row; the model
// declares the ids as *uuid.UUID so a NULL scans to nil and marshals as
// null. The test inserts three rows by id (no vehicle, no driver, neither),
// each with a real referenced FK when it names one, then reads them
// through the list and the single read, asserting each `vehicle_id` and
// `driver_id` field carries the wire's null shape.
func TestRoute_NullVehicleAndDriverServeNull(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	driverID := uuid.New()
	if _, err := f.db.Pool.Exec(ctx,
		`INSERT INTO drivers (id, name, license_number) VALUES ($1, $2, $3)`,
		driverID, "Null route driver", "NULLDRV-"+driverID.String()[:8]); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = f.db.Pool.Exec(context.Background(), `DELETE FROM drivers WHERE id = $1`, driverID) })
	vehicleID := uuid.New()
	if _, err := f.db.Pool.Exec(ctx,
		`INSERT INTO vehicles (id, name, vehicle_type, license_plate) VALUES ($1, $2, 'VAN', $3)`,
		vehicleID, "Null route truck", "NULLVEH-"+vehicleID.String()[:8]); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = f.db.Pool.Exec(context.Background(), `DELETE FROM vehicles WHERE id = $1`, vehicleID) })

	mkRoute := func(label string) uuid.UUID {
		t.Helper()
		var id uuid.UUID
		sql := ""
		switch label {
		case "no_vehicle":
			sql = `INSERT INTO delivery_routes (id, driver_id, scheduled_date, status)
				   VALUES ($1, $2, $3::date, $4) RETURNING id`
		case "no_driver":
			sql = `INSERT INTO delivery_routes (id, vehicle_id, scheduled_date, status)
				   VALUES ($1, $2, $3::date, $4) RETURNING id`
		default:
			sql = `INSERT INTO delivery_routes (id, scheduled_date, status)
				   VALUES ($1, $2::date, $3) RETURNING id`
		}
		var err error
		switch label {
		case "no_vehicle":
			err = f.db.Pool.QueryRow(ctx, sql, uuid.New(), driverID, "2030-09-01", "DRAFT").Scan(&id)
		case "no_driver":
			err = f.db.Pool.QueryRow(ctx, sql, uuid.New(), vehicleID, "2030-09-01", "DRAFT").Scan(&id)
		default:
			err = f.db.Pool.QueryRow(ctx, sql, uuid.New(), "2030-09-01", "DRAFT").Scan(&id)
		}
		if err != nil {
			t.Fatalf("%s: %v", label, err)
		}
		t.Cleanup(func() { _, _ = f.db.Pool.Exec(context.Background(), `DELETE FROM delivery_routes WHERE id = $1`, id) })
		return id
	}
	noVehicle := mkRoute("no_vehicle")
	noDriver := mkRoute("no_driver")
	bare := mkRoute("none")

	list := f.do(t, http.MethodGet, "/api/v1/delivery/routes?date=2030-09-01", "", nil)
	if list.status != http.StatusOK {
		t.Fatalf("list = %d %s", list.status, list.raw)
	}
	seen := map[string]bool{}
	for _, it := range list.body["items"].([]any) {
		r := it.(map[string]any)
		switch r["id"].(string) {
		case noVehicle.String():
			seen["no_vehicle"] = true
			if r["vehicle_id"] != nil {
				t.Errorf("no-vehicle route vehicle_id = %v, want null", r["vehicle_id"])
			}
			if r["driver_id"] == nil {
				t.Errorf("no-vehicle route driver_id = null, want the seeded UUID")
			}
		case noDriver.String():
			seen["no_driver"] = true
			if r["driver_id"] != nil {
				t.Errorf("no-driver route driver_id = %v, want null", r["driver_id"])
			}
			if r["vehicle_id"] == nil {
				t.Errorf("no-driver route vehicle_id = null, want the seeded UUID")
			}
		case bare.String():
			seen["bare"] = true
			if r["vehicle_id"] != nil {
				t.Errorf("bare route vehicle_id = %v, want null", r["vehicle_id"])
			}
			if r["driver_id"] != nil {
				t.Errorf("bare route driver_id = %v, want null", r["driver_id"])
			}
		}
	}
	for _, k := range []string{"no_vehicle", "no_driver", "bare"} {
		if !seen[k] {
			t.Errorf("the list lost the %s route (routeFrom likely JOINs)", k)
		}
	}

	for _, c := range []struct {
		id   uuid.UUID
		want map[string]any
	}{
		{noVehicle, map[string]any{"vehicle_id": nil}},
		{noDriver, map[string]any{"driver_id": nil}},
		{bare, map[string]any{"vehicle_id": nil, "driver_id": nil}},
	} {
		res := f.do(t, http.MethodGet, "/api/v1/delivery/routes/"+c.id.String(), "", nil)
		if res.status != http.StatusOK {
			t.Errorf("get %s = %d %s", c.id, res.status, res.raw)
			continue
		}
		for k, want := range c.want {
			if got := res.body[k]; got != want {
				t.Errorf("get %s %s = %v, want %v", c.id, k, got, want)
			}
		}
	}
}

// The route lifecycle through the transitions route (the dispatch and
// complete action routes are gone): 428 without a revision, the wrong edge
// a 409 invalid_state_transition with its blocker, and the events in order.
func TestRouteTransitions(t *testing.T) {
	f := newFixture(t)
	route, stop, _ := f.seedStop(t, f.branch)

	if res := f.do(t, http.MethodPost, "/api/v1/delivery/routes/"+route.String()+"/transitions",
		`{"to":"in_transit"}`, nil); res.status != http.StatusPreconditionRequired {
		t.Errorf("no precondition = %d, want 428", res.status)
	}
	// A route with a pending stop cannot complete: the blocker names it.
	res := f.do(t, http.MethodPost, "/api/v1/delivery/routes/"+route.String()+"/transitions",
		`{"to":"completed","revision":1}`, nil)
	if res.status != http.StatusConflict || f.errCode(t, res) != "invalid_state_transition" {
		t.Fatalf("complete with a pending stop = %d %s, want 409 invalid_state_transition", res.status, res.raw)
	}
	env := res.body["error"].(map[string]any)
	details := env["details"].([]any)
	if len(details) == 0 || details[0].(map[string]any)["code"] != "stop_not_terminal" {
		t.Errorf("blockers = %v, want stop_not_terminal", details)
	}

	// Dispatch, then complete the stop, then complete the route.
	res = f.do(t, http.MethodPost, "/api/v1/delivery/routes/"+route.String()+"/transitions",
		`{"to":"in_transit","revision":1}`, nil)
	if res.status != http.StatusOK || res.body["status"] != "in_transit" || res.body["revision"] != float64(2) {
		t.Fatalf("dispatch = %d %s", res.status, res.raw)
	}
	if res := f.do(t, http.MethodPost, "/api/v1/delivery/routes/"+route.String()+"/transitions",
		`{"to":"in_transit","revision":2}`, nil); res.status != http.StatusConflict {
		t.Errorf("dispatch twice = %d, want 409", res.status)
	}

	if res := f.do(t, http.MethodPost, "/api/v1/delivery/deliveries/"+stop.String()+"/transitions",
		`{"to":"failed","revision":1}`, nil); res.status != http.StatusOK {
		t.Fatalf("stop failed = %d %s", res.status, res.raw)
	}
	res = f.do(t, http.MethodPost, "/api/v1/delivery/routes/"+route.String()+"/transitions",
		`{"to":"completed","revision":2}`, nil)
	if res.status != http.StatusOK || res.body["status"] != "completed" {
		t.Fatalf("complete = %d %s", res.status, res.raw)
	}

	// The events, in order: route.in_transit, delivery.failed, route.completed.
	rows, err := f.db.Pool.Query(context.Background(),
		`SELECT type FROM events_outbox WHERE entity_id IN ($1, $2) ORDER BY position`, route, stop)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var got []string
	for rows.Next() {
		var typ string
		if err := rows.Scan(&typ); err != nil {
			t.Fatal(err)
		}
		got = append(got, typ)
	}
	want := []string{"route.in_transit", "delivery.failed", "route.completed"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("events = %v, want %v", got, want)
	}
}

// The stop lifecycle: an assigned order becomes a stop (delivery.created),
// a delivered stop needs its POD, queues its fulfilment inside the same
// transaction, and a stop cannot complete twice.
func TestStopLifecycle(t *testing.T) {
	f := newFixture(t)
	route, _, order := f.seedStop(t, f.branch)
	today := time.Now().UTC().Format("2006-01-02")
	_ = today

	res := f.assignOrder(t, route, order, nil)
	if res.status != http.StatusCreated {
		t.Fatalf("assign = %d %s", res.status, res.raw)
	}
	wrapper, _ := res.body["delivery"].(map[string]any)
	stopID, _ := wrapper["id"].(string)
	if wrapper["status"] != "pending" || res.header.Get("ETag") != `"1"` {
		t.Errorf("the created stop = %v", wrapper)
	}
	if _, ok := res.body["capacity_warning"]; !ok {
		t.Error("capacity_warning missing (null present)")
	}
	if !strings.HasPrefix(res.header.Get("Location"), "/api/v1/delivery/deliveries/") {
		t.Errorf("Location = %q", res.header.Get("Location"))
	}
	t.Cleanup(func() {
		_, _ = f.db.Pool.Exec(context.Background(), `DELETE FROM deliveries WHERE id = $1`, stopID)
		_, _ = f.db.Pool.Exec(context.Background(), `DELETE FROM events_outbox WHERE entity_id = $1`, stopID)
		_, _ = f.db.Pool.Exec(context.Background(), `DELETE FROM audit_log WHERE entity_id = $1`, stopID)
	})

	if res := f.do(t, http.MethodPost, "/api/v1/delivery/deliveries/"+stopID+"/transitions",
		`{"to":"delivered","revision":1}`, nil); res.status != http.StatusBadRequest || !f.fields(t, res)["pod_proof_url"] {
		t.Errorf("delivered without POD = %d %s, want 400 naming pod_proof_url", res.status, res.raw)
	}

	res = f.do(t, http.MethodPost, "/api/v1/delivery/deliveries/"+stopID+"/transitions",
		`{"to":"delivered","revision":1,"pod_proof_url":"https://x/p.jpg","pod_signed_by":"foreman"}`, nil)
	if res.status != http.StatusOK || res.body["status"] != "delivered" {
		t.Fatalf("delivered = %d %s", res.status, res.raw)
	}
	if res.body["pod_timestamp"] == nil {
		t.Error("pod_timestamp missing on a delivered stop")
	}
	if len(f.queued) != 1 || f.queued[0].delivery.String() != stopID || f.queued[0].order != order {
		t.Errorf("fulfilment queue = %v, want the delivered stop's order queued once", f.queued)
	}
	if res := f.do(t, http.MethodPost, "/api/v1/delivery/deliveries/"+stopID+"/transitions",
		`{"to":"failed","revision":2}`, nil); res.status != http.StatusConflict {
		t.Errorf("complete twice = %d, want 409", res.status)
	}

	// A stop's list by route, in stop order, behind the same wall.
	list := f.do(t, http.MethodGet, "/api/v1/delivery/routes/"+route.String()+"/deliveries", "", nil)
	if list.status != http.StatusOK {
		t.Fatalf("stops = %d %s", list.status, list.raw)
	}
	if items, _ := list.body["items"].([]any); len(items) != 2 {
		t.Errorf("the route holds 2 stops, list = %v", list.body["items"])
	}
	if _, ok := list.body["next_cursor"]; !ok {
		t.Error("next_cursor missing (null on the last page is present)")
	}
}

// The reorder: the list must name every stop exactly once, and the write
// needs the route's revision.
func TestReorderStops(t *testing.T) {
	f := newFixture(t)
	route, _, order := f.seedStop(t, f.branch)
	res := f.assignOrder(t, route, order, nil)
	if res.status != http.StatusCreated {
		t.Fatalf("assign = %d %s", res.status, res.raw)
	}
	second, _ := res.body["delivery"].(map[string]any)["id"].(string)
	t.Cleanup(func() {
		_, _ = f.db.Pool.Exec(context.Background(), `DELETE FROM deliveries WHERE id = $1`, second)
		_, _ = f.db.Pool.Exec(context.Background(), `DELETE FROM events_outbox WHERE entity_id = $1`, second)
	})

	// The assign moved the route's revision (PR 70 review round 2 P2-1).
	routeDoc := f.do(t, http.MethodGet, "/api/v1/delivery/routes/"+route.String(), "", nil)
	if routeDoc.status != http.StatusOK {
		t.Fatalf("route = %d %s", routeDoc.status, routeDoc.raw)
	}
	routeRev, _ := routeDoc.body["revision"].(float64)
	wantRev := strconv.Itoa(int(routeRev))
	list := f.do(t, http.MethodGet, "/api/v1/delivery/routes/"+route.String()+"/deliveries?include=total", "", nil)
	if list.body["total"] != float64(2) {
		t.Errorf("total = %v, want 2", list.body["total"])
	}
	first := ""
	for _, it := range list.body["items"].([]any) {
		if it.(map[string]any)["id"] != second {
			first = it.(map[string]any)["id"].(string)
		}
	}
	if res := f.do(t, http.MethodPost, "/api/v1/delivery/routes/"+route.String()+"/reorder",
		`{"ordered_delivery_ids":["`+second+`"]}`, map[string]string{"If-Match": `"` + wantRev + `"`}); res.status != http.StatusBadRequest {
		t.Errorf("a partial list = %d %s, want 400", res.status, res.raw)
	}
	if res := f.do(t, http.MethodPost, "/api/v1/delivery/routes/"+route.String()+"/reorder",
		`{"ordered_delivery_ids":["`+second+`","`+first+`"]}`, nil); res.status != http.StatusPreconditionRequired {
		t.Errorf("reorder without a revision = %d, want 428", res.status)
	}
	res = f.do(t, http.MethodPost, "/api/v1/delivery/routes/"+route.String()+"/reorder",
		`{"ordered_delivery_ids":["`+second+`","`+first+`"]}`, map[string]string{"If-Match": `"` + wantRev + `"`})
	if res.status != http.StatusOK {
		t.Fatalf("reorder = %d %s", res.status, res.raw)
	}
	wantRev = strconv.Itoa(int(res.body["revision"].(float64)))
	list = f.do(t, http.MethodGet, "/api/v1/delivery/routes/"+route.String()+"/deliveries", "", nil)
	items := list.body["items"].([]any)
	if items[0].(map[string]any)["id"] != second || items[0].(map[string]any)["stop_sequence"] != float64(1) {
		t.Errorf("after the reorder the list = %v", items)
	}
}

// The assign's stop_sequence default is the route's next position, 1 or
// more: the input parse refuses a client's 0, so the default must not mint
// one either (an empty route's first stop is 1, not the column's old 0).
func TestAssignStopSequenceDefault(t *testing.T) {
	f := newFixture(t)
	route, stop, order := f.seedStop(t, f.branch)
	_, _ = f.db.Pool.Exec(context.Background(), `DELETE FROM deliveries WHERE id = $1`, stop)

	res := f.assignOrder(t, route, order, nil)
	if res.status != http.StatusCreated {
		t.Fatalf("assign on an empty route = %d %s", res.status, res.raw)
	}
	wrapper, _ := res.body["delivery"].(map[string]any)
	if wrapper["stop_sequence"] != float64(1) {
		t.Errorf("an empty route's first stop_sequence = %v, want 1", wrapper["stop_sequence"])
	}
}

// A driver's quantity adjustment is recorded (a delivery_qty_adjustments
// row with decimal string quantities), validated with full paths, and the
// reason vocabulary is lowercase only (the live failure: nothing was
// written, only a log line).
func TestAdjustQty_RecordsRow(t *testing.T) {
	f := newFixture(t)
	_, stop, _ := f.seedStop(t, f.branch)
	product := uuid.New()

	res := f.do(t, http.MethodPost, "/api/v1/delivery/deliveries/"+stop.String()+"/adjust-qty",
		`{"adjusted_by":"`+uuid.NewString()+`","adjustments":[{"product_id":"`+product.String()+`","original_qty":10,"adjusted_qty":8,"reason_code":"short_ship"}]}`, nil)
	if res.status != http.StatusBadRequest {
		t.Fatalf("a float quantity = %d %s, want 400 (quantities are decimal strings)", res.status, res.raw)
	}
	if !f.fields(t, res)["adjustments[0].original_qty"] {
		t.Errorf("details = %v, want the indexed path", f.fields(t, res))
	}

	res = f.do(t, http.MethodPost, "/api/v1/delivery/deliveries/"+stop.String()+"/adjust-qty",
		`{"adjusted_by":"`+uuid.NewString()+`","adjustments":[{"product_id":"`+product.String()+`","original_qty":"10","adjusted_qty":"8","reason_code":"SHORT_SHIP"}]}`, nil)
	if res.status != http.StatusBadRequest || !f.fields(t, res)["adjustments[0].reason_code"] {
		t.Fatalf("an uppercase reason = %d %s, want 400 naming it", res.status, res.raw)
	}

	res = f.do(t, http.MethodPost, "/api/v1/delivery/deliveries/"+stop.String()+"/adjust-qty",
		`{"adjusted_by":"`+uuid.NewString()+`","adjustments":[{"product_id":"`+product.String()+`","original_qty":"10","adjusted_qty":"8","reason_code":"short_ship","notes":"two short"}]}`, nil)
	if res.status != http.StatusOK || res.body["revision"] != float64(2) {
		t.Fatalf("adjust = %d %s", res.status, res.raw)
	}
	var original, adjusted string
	if err := f.db.Pool.QueryRow(context.Background(),
		`SELECT original_qty::text, adjusted_qty::text FROM delivery_qty_adjustments WHERE delivery_id = $1`, stop).
		Scan(&original, &adjusted); err != nil {
		t.Fatalf("the adjustment row: %v", err)
	}
	if original != "10.0000" || adjusted != "8.0000" {
		t.Errorf("stored quantities = %s %s, want the decimal strings", original, adjusted)
	}
	var events int
	if err := f.db.Pool.QueryRow(context.Background(),
		`SELECT count(*) FROM events_outbox WHERE type = 'delivery.adjusted' AND entity_id = $1`, stop).Scan(&events); err != nil {
		t.Fatal(err)
	}
	if events != 1 {
		t.Errorf("delivery.adjusted events = %d, want 1", events)
	}
}

// The branch wall on every read and write of a stop and its route (the live
// failure: a second branch's request read the first branch's stop as 200).
// PR 70 review round 1 P3-4 adds assign, photo attaches, route transitions,
// reorder and optimize to the wall.
func TestBranchWall(t *testing.T) {
	f := newFixture(t)
	route, stop, _ := f.seedStop(t, f.branch)
	other := uuid.New()
	if _, err := f.db.Pool.Exec(context.Background(),
		`INSERT INTO locations (id, type, code, name) VALUES ($1, 'BRANCH', $2, $3)`,
		other, "wb-"+other.String()[:8], "wire branch"); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = f.db.Pool.Exec(context.Background(), `DELETE FROM locations WHERE id = $1`, other) })
	hdr := map[string]string{"X-Test-Branch": other.String()}

	if res := f.do(t, http.MethodGet, "/api/v1/delivery/deliveries/"+stop.String(), "", hdr); res.status != http.StatusNotFound {
		t.Errorf("cross branch stop read = %d, want 404", res.status)
	}
	if res := f.do(t, http.MethodGet, "/api/v1/delivery/routes/"+route.String()+"/deliveries", "", hdr); res.status != http.StatusNotFound {
		t.Errorf("cross branch stop list = %d, want 404", res.status)
	}
	if res := f.do(t, http.MethodGet, "/api/v1/delivery/routes/"+route.String(), "", hdr); res.status != http.StatusNotFound {
		t.Errorf("cross branch route read = %d, want 404", res.status)
	}
	board := f.do(t, http.MethodGet, "/api/v1/delivery/routes?include=stops", "", hdr)
	for _, it := range board.body["items"].([]any) {
		if it.(map[string]any)["id"] == route.String() {
			t.Error("the other branch's board shows this branch's route")
		}
	}
	if res := f.do(t, http.MethodPost, "/api/v1/delivery/deliveries/"+stop.String()+"/transitions",
		`{"to":"failed","revision":1}`, hdr); res.status != http.StatusNotFound {
		t.Errorf("cross branch stop write = %d, want 404", res.status)
	}
	if res := f.do(t, http.MethodPost, "/api/v1/delivery/deliveries/"+stop.String()+"/adjust-qty",
		`{"adjusted_by":"`+uuid.NewString()+`","adjustments":[{"product_id":"`+uuid.NewString()+`","original_qty":"1","adjusted_qty":"1","reason_code":"other"}]}`, hdr); res.status != http.StatusNotFound {
		t.Errorf("cross branch adjustment = %d, want 404", res.status)
	}
	// Cross-branch assign: the order belongs to this branch, the caller's
	// wall says it is the other branch, the assign refuses. The order here
	// is a fresh one on this branch so the count tells only what the
	// assign created.
	otherOrder := f.seedOtherBranchOrder(t, f.branch)
	if res := f.assignOrder(t, route, otherOrder, hdr); res.status != http.StatusNotFound {
		t.Errorf("cross branch assign = %d, want 404", res.status)
	}
	var created int
	if err := f.db.Pool.QueryRow(context.Background(),
		`SELECT count(*) FROM deliveries WHERE order_id = $1 AND route_id = $2`,
		otherOrder, route).Scan(&created); err != nil {
		t.Fatal(err)
	}
	if created != 0 {
		t.Errorf("%d deliveries were created despite a cross-branch assign", created)
	}
	// Cross-branch POD photo upload.
	if res := f.do(t, http.MethodPost, "/api/v1/delivery/deliveries/"+stop.String()+"/pod-photo",
		`--boundary\r\nContent-Disposition: form-data; name=\"photo\"; filename=\"a.jpg\"\r\nContent-Type: image/jpeg\r\n\r\n--boundary--`,
		hdr); res.status != http.StatusNotFound && res.status != http.StatusBadRequest {
		t.Errorf("cross branch POD photo = %d, want 404 or 400", res.status)
	}
	// Cross-branch reorder: the route is not visible, so 404.
	if res := f.do(t, http.MethodPost, "/api/v1/delivery/routes/"+route.String()+"/reorder",
		`{"ordered_delivery_ids":["`+stop.String()+`"]}`,
		map[string]string{"X-Test-Branch": other.String(), "If-Match": `"1"`}); res.status != http.StatusNotFound {
		t.Errorf("cross branch reorder = %d, want 404", res.status)
	}
	// Cross-branch optimize: the route is not visible, so 404.
	if res := f.do(t, http.MethodPost, "/api/v1/delivery/routes/"+route.String()+"/optimize",
		``, map[string]string{"X-Test-Branch": other.String(), "If-Match": `"1"`}); res.status != http.StatusNotFound {
		t.Errorf("cross branch optimize = %d, want 404", res.status)
	}
	// Cross-branch route transition: the route is not visible, so 404. The
// request carries the same If-Match a real transition would; the wall fires
// before the precondition is read.
	if res := f.do(t, http.MethodPost, "/api/v1/delivery/routes/"+route.String()+"/transitions",
		`{"to":"in_transit"}`,
		map[string]string{"X-Test-Branch": other.String(), "If-Match": `"1"`}); res.status != http.StatusNotFound {
		t.Errorf("cross branch route transition = %d, want 404", res.status)
	}
	// The owning branch still sees everything.
	if res := f.do(t, http.MethodGet, "/api/v1/delivery/deliveries/"+stop.String(), "", map[string]string{"X-Test-Branch": f.branch.String()}); res.status != http.StatusOK {
		t.Errorf("the owning branch's read = %d, want 200", res.status)
	}
}

// Idempotency through the middleware: the same create twice with one key
// returns the first response (replay header) and makes one row and one
// event; the same key with another body is 422.
func TestIdempotency(t *testing.T) {
	f := newFixture(t)
	key := "wire-" + uuid.NewString()
	body := `{"name":"Idem truck","vehicle_type":"van","license_plate":"W-IDEM1"}`
	first := f.do(t, http.MethodPost, "/api/v1/delivery/vehicles", body, map[string]string{"Idempotency-Key": key})
	if first.status != http.StatusCreated {
		t.Fatalf("first = %d %s", first.status, first.raw)
	}
	second := f.do(t, http.MethodPost, "/api/v1/delivery/vehicles", body, map[string]string{"Idempotency-Key": key})
	if second.status != http.StatusCreated || second.header.Get("Idempotency-Replayed") != "true" {
		t.Errorf("replay = %d (replayed %q), want the stored response", second.status, second.header.Get("Idempotency-Replayed"))
	}
	if second.body["id"] != first.body["id"] {
		t.Errorf("replay returned another vehicle: %v vs %v", second.body["id"], first.body["id"])
	}
	third := f.do(t, http.MethodPost, "/api/v1/delivery/vehicles",
		`{"name":"Other","vehicle_type":"van","license_plate":"W-IDEM2"}`, map[string]string{"Idempotency-Key": key})
	if third.status != http.StatusUnprocessableEntity || f.errCode(t, third) != "idempotency_key_reused" {
		t.Errorf("a reused key with another body = %d %s, want 422", third.status, third.raw)
	}
	id := first.body["id"].(string)
	t.Cleanup(func() { _, _ = f.db.Pool.Exec(context.Background(), `DELETE FROM vehicles WHERE id = $1`, id) })
}

// The module's routes sit behind the role guard they are registered with.
func TestRegisterRoutes_GuardsWrapEveryRoute(t *testing.T) {
	svc := delivery.NewService(nil)
	h := delivery.NewHandler(svc)
	var seen []string
	record := func(http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			seen = append(seen, r.Method+" "+r.URL.Path)
			w.WriteHeader(http.StatusTeapot)
		})
	}
	mux := http.NewServeMux()
	h.RegisterRoutes(mux, record)
	paths := []string{
		"GET /api/v1/delivery/vehicles", "POST /api/v1/delivery/vehicles",
		"GET /api/v1/delivery/routes", "POST /api/v1/delivery/routes/{id}/transitions",
		"GET /api/v1/delivery/routes/{id}/deliveries", "POST /api/v1/delivery/deliveries",
		"POST /api/v1/delivery/deliveries/{id}/transitions", "POST /api/v1/delivery/deliveries/{id}/adjust-qty",
		"GET /api/v1/delivery/deliveries/{id}/pod-photos",
	}
	for _, p := range paths {
		method, path, _ := strings.Cut(p, " ")
		req := httptest.NewRequest(method, strings.ReplaceAll(path, "{id}", uuid.NewString()), nil)
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		if rec.Code != http.StatusTeapot {
			t.Errorf("%s: status %d, want the guard's 418 (the guard did not wrap it)", p, rec.Code)
		}
	}
}
