// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package product_test

// The product module on the wire contract (ADR 0001), tested end to end: a
// real Postgres, the real handler on a real mux, requests as JSON and
// responses read back as JSON. Nothing here touches a product type, so each
// test states a wire fact. The C3-1 list of ADR 0006 section 9.2: the
// recipe's wire set (envelope, cursor walk, include=total), the scaled base
// price, quantities as decimal strings, on_hand / allocated / available in
// the stocking unit, and revision on every write. The list's live failures
// (the offset envelope, money as float, uom_primary) each failed against
// the base commit.

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gablelbm/gable/internal/product"
	"github.com/gablelbm/gable/internal/testutil"
	"github.com/gablelbm/gable/pkg/database"
	"github.com/gablelbm/gable/pkg/middleware"
	"github.com/google/uuid"
)

type fixture struct {
	t   *testing.T
	db  *database.DB
	srv *httptest.Server
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	t.Setenv("AUTH_MODE", "dev")
	db := testutil.RequireDB(t)
	f := &fixture{t: t, db: db}
	repo := product.NewRepository(db)
	svc := product.NewService(repo)
	mux := http.NewServeMux()
	product.NewHandler(svc).RegisterRoutes(mux)
	f.srv = httptest.NewServer(middleware.Idempotency(db)(mux))
	t.Cleanup(func() { f.srv.Close() })
	return f
}

type resp struct {
	status int
	header http.Header
	body   map[string]any
	raw    []byte
}

func (f *fixture) do(method, path string, body any, headers ...string) resp {
	f.t.Helper()
	var rdr io.Reader
	switch b := body.(type) {
	case nil:
	case string:
		rdr = strings.NewReader(b)
	default:
		raw, err := json.Marshal(b)
		if err != nil {
			f.t.Fatal(err)
		}
		rdr = bytes.NewReader(raw)
	}
	req, err := http.NewRequest(method, f.srv.URL+path, rdr)
	if err != nil {
		f.t.Fatal(err)
	}
	if rdr != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	req.Header.Set("X-Request-ID", "req-product-wire")
	for i := 0; i+1 < len(headers); i += 2 {
		req.Header.Set(headers[i], headers[i+1])
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		f.t.Fatal(err)
	}
	defer res.Body.Close()
	raw, _ := io.ReadAll(res.Body)
	out := resp{status: res.StatusCode, header: res.Header, raw: raw}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	_ = dec.Decode(&out.body)
	return out
}

func (f *fixture) uniqueSKU() string { return "WIRE-" + uuid.NewString()[:12] }

// createProduct posts one product and hands back the created body.
func (f *fixture) createProduct(overrides map[string]any) resp {
	f.t.Helper()
	body := map[string]any{
		"sku":                        f.uniqueSKU(),
		"description":                "2x4x8 SPF stud",
		"stock_uom":                  "PCS",
		"base_price_ten_thousandths": 5250000,
		"reorder_point":              "40",
	}
	for k, v := range overrides {
		body[k] = v
	}
	return f.do("POST", "/api/v1/products", body, "Idempotency-Key", uuid.NewString())
}

func (f *fixture) deleteProduct(id string) {
	f.t.Helper()
	_, _ = f.db.Pool.Exec(context.Background(), `DELETE FROM products WHERE id = $1`, id)
}

// TestCreateShape: the create answers 201 with Location, the scaled base
// price, the stocking unit, quantities as decimal strings, on_hand,
// allocated and available, revision and its ETag, and microsecond
// timestamps. On the base commit the body carried uom_primary and base_price
// as floats with no revision.
func TestCreateShape(t *testing.T) {
	f := newFixture(t)
	res := f.createProduct(nil)
	defer f.deleteProduct(str(res.body["id"]))
	if res.status != http.StatusCreated {
		t.Fatalf("status = %d, body %s", res.status, res.raw)
	}
	if loc := res.header.Get("Location"); !strings.HasPrefix(loc, "/api/v1/products/") {
		t.Fatalf("Location = %q, want /api/v1/products/{id}", loc)
	}
	for _, field := range []string{"id", "sku", "description", "stock_uom", "base_price_ten_thousandths",
		"on_hand", "allocated", "available", "revision", "created_at", "updated_at"} {
		if _, ok := res.body[field]; !ok {
			t.Errorf("field %q missing from the create answer: %s", field, res.raw)
		}
	}
	if res.body["stock_uom"] != "PCS" {
		t.Errorf("stock_uom = %v, want PCS", res.body["stock_uom"])
	}
	if res.body["base_price_ten_thousandths"] != json.Number("5250000") {
		t.Errorf("base_price_ten_thousandths = %v, want 5250000 (525.00 per stocking unit)", res.body["base_price_ten_thousandths"])
	}
	if res.body["on_hand"] != "0" || res.body["allocated"] != "0" || res.body["available"] != "0" {
		t.Errorf("stock fields = %v/%v/%v, want \"0\"/\"0\"/\"0\"", res.body["on_hand"], res.body["allocated"], res.body["available"])
	}
	if res.body["uom_primary"] != nil || res.body["base_price"] != nil || res.body["total_quantity"] != nil {
		t.Errorf("legacy fields survive on the wire: %s", res.raw)
	}
	if res.body["revision"] != json.Number("1") {
		t.Errorf("revision = %v, want 1", res.body["revision"])
	}
	if etag := res.header.Get("ETag"); etag != `"1"` {
		t.Errorf("ETag = %q, want \"1\"", etag)
	}
}

// TestCreateValidation: one 400 carrying every offending field, the error
// envelope with the handler's own message, and a request_id.
func TestCreateValidation(t *testing.T) {
	f := newFixture(t)
	res := f.do("POST", "/api/v1/products", map[string]any{
		"description":                "no sku",
		"base_price_ten_thousandths": -1,
		"stock_uom":                  "pounds",
	}, "Idempotency-Key", uuid.NewString())
	if res.status != http.StatusBadRequest {
		t.Fatalf("status = %d, body %s", res.status, res.raw)
	}
	errBody := res.body["error"].(map[string]any)
	if errBody["code"] != "validation_failed" {
		t.Fatalf("code = %v, want validation_failed", errBody["code"])
	}
	fields := map[string]bool{}
	for _, d := range errBody["details"].([]any) {
		fields[d.(map[string]any)["field"].(string)] = true
	}
	for _, want := range []string{"sku", "stock_uom", "base_price_ten_thousandths"} {
		if !fields[want] {
			t.Errorf("details misses %q: %s", want, res.raw)
		}
	}
	if res.body["meta"].(map[string]any)["request_id"] != "req-product-wire" {
		t.Errorf("meta.request_id = %v", res.body["meta"])
	}
}

// TestListEnvelopeAndCursor: the list is the ADR 0001 envelope, the cursor
// walks every row exactly once, include=total counts, and an empty filter
// page is [] in the bytes. The base commit answered {data, total, limit,
// offset} with no cursor.
func TestListEnvelopeAndCursor(t *testing.T) {
	f := newFixture(t)
	var ids []string
	for i := 0; i < 3; i++ {
		res := f.createProduct(nil)
		if res.status != http.StatusCreated {
			t.Fatalf("create %d: %d %s", i, res.status, res.raw)
		}
		ids = append(ids, str(res.body["id"]))
	}
	defer func() {
		for _, id := range ids {
			f.deleteProduct(id)
		}
	}()

	seen := map[string]bool{}
	path := "/api/v1/products?limit=2"
	pages := 0
	for path != "" && pages < 10 {
		res := f.do("GET", path, nil)
		if res.status != http.StatusOK {
			t.Fatalf("list: %d %s", res.status, res.raw)
		}
		items := res.body["items"].([]any)
		if len(items) == 0 || len(items) > 2 {
			t.Fatalf("page carries %d items", len(items))
		}
		for _, it := range items {
			id := str(it.(map[string]any)["id"])
			if seen[id] {
				t.Fatalf("cursor repeated row %s", id)
			}
			seen[id] = true
		}
		next, _ := res.body["next_cursor"].(string)
		if next == "" && res.body["next_cursor"] != nil {
			t.Fatal("next_cursor must be a string or null")
		}
		if res.body["limit"] != json.Number("2") {
			t.Fatalf("limit = %v, want 2", res.body["limit"])
		}
		path = ""
		if next != "" {
			path = "/api/v1/products?limit=2&cursor=" + next
		}
		pages++
	}
	for _, id := range ids {
		if !seen[id] {
			t.Fatalf("cursor walk never reached %s", id)
		}
	}

	// include=total answers the count beside the page.
	res := f.do("GET", "/api/v1/products?limit=1&include=total", nil)
	if res.body["total"] == nil {
		t.Fatalf("include=total carries no total: %s", res.raw)
	}

	// An unsupported parameter is refused, not ignored.
	res = f.do("GET", "/api/v1/products?status=active", nil)
	if res.status != http.StatusBadRequest {
		t.Fatalf("unknown parameter: status = %d", res.status)
	}
	if code := res.body["error"].(map[string]any)["code"]; code != "unsupported_query_parameter" {
		t.Fatalf("code = %v", code)
	}
}

// TestStockFieldsFromInventory: on_hand, allocated and available read the
// inventory rows in the stocking unit: available = quantity - allocated.
func TestStockFieldsFromInventory(t *testing.T) {
	f := newFixture(t)
	res := f.createProduct(nil)
	if res.status != http.StatusCreated {
		t.Fatalf("create: %d %s", res.status, res.raw)
	}
	id := str(res.body["id"])
	defer f.deleteProduct(id)
	if _, err := f.db.Pool.Exec(context.Background(),
		`INSERT INTO inventory (product_id, location, quantity, allocated) VALUES ($1, 'WIRE', 120, 30)`, id); err != nil {
		t.Fatalf("seed inventory: %v", err)
	}
	got := f.do("GET", "/api/v1/products/"+id, nil)
	if got.status != http.StatusOK {
		t.Fatalf("get: %d %s", got.status, got.raw)
	}
	if got.body["on_hand"] != "120" || got.body["allocated"] != "30" || got.body["available"] != "90" {
		t.Fatalf("stock fields = %v/%v/%v, want 120/30/90", got.body["on_hand"], got.body["allocated"], got.body["available"])
	}
	if got.body["stock_uom"] != "PCS" {
		t.Fatalf("stock_uom = %v", got.body["stock_uom"])
	}
}

// TestRevisionPreconditions: a write without a revision is 428, a stale one
// is 409 stale_revision, If-Match strong and weak both carry it, and a
// successful write answers the new revision and ETag.
func TestRevisionPreconditions(t *testing.T) {
	f := newFixture(t)
	created := f.createProduct(nil)
	defer f.deleteProduct(str(created.body["id"]))
	id := str(created.body["id"])
	patch := func(body map[string]any, headers ...string) resp {
		return f.do("PATCH", "/api/v1/products/"+id+"/margins", body, headers...)
	}

	if res := patch(map[string]any{"target_margin": 0.3, "commission_rate": 0.05}); res.status != http.StatusPreconditionRequired {
		t.Fatalf("no precondition: status = %d, want 428", res.status)
	}
	if res := patch(map[string]any{"target_margin": 0.3, "commission_rate": 0.05, "revision": 99}); res.status != http.StatusConflict {
		t.Fatalf("stale revision: status = %d, want 409", res.status)
	}
	if code := f.do("PATCH", "/api/v1/products/"+id+"/margins", map[string]any{"revision": 99}).body["error"].(map[string]any)["code"]; code != "stale_revision" {
		t.Fatalf("code = %v, want stale_revision", code)
	}
	res := patch(map[string]any{"target_margin": 0.3, "commission_rate": 0.05, "revision": 1})
	if res.status != http.StatusOK {
		t.Fatalf("good revision: status = %d, body %s", res.status, res.raw)
	}
	if res.body["revision"] != json.Number("2") || res.header.Get("ETag") != `"2"` {
		t.Fatalf("write answered revision %v ETag %q, want 2 and \"2\"", res.body["revision"], res.header.Get("ETag"))
	}
	// If-Match weak and strong both carry the precondition; each write moves
	// the revision, so each header names the revision the last write answered.
	if res := patch(nil, `If-Match`, `W/"2"`); res.status != http.StatusOK {
		t.Fatalf("weak If-Match: status = %d, want 200", res.status)
	}
	if res := patch(nil, `If-Match`, `"3"`); res.status != http.StatusOK {
		t.Fatalf("strong If-Match: status = %d, want 200", res.status)
	}
	res = patch(map[string]any{"revision": 2}, `If-Match`, `"3"`)
	if res.status != http.StatusBadRequest {
		t.Fatalf("If-Match and body disagree: status = %d, want 400", res.status)
	}
}

// TestDimensionsAndLeadTimeWrite: the geometry and lead time writes take the
// same precondition and answer the product's revision.
func TestDimensionsAndLeadTimeWrite(t *testing.T) {
	f := newFixture(t)
	created := f.createProduct(nil)
	defer f.deleteProduct(str(created.body["id"]))
	id := str(created.body["id"])

	res := f.do("PATCH", "/api/v1/products/"+id+"/dimensions", map[string]any{
		"length_in": 96.0, "width_in": 3.5, "height_in": 1.5,
		"stackable": true, "revision": 1,
	})
	if res.status != http.StatusOK {
		t.Fatalf("dimensions: %d %s", res.status, res.raw)
	}
	if res.body["geometry_source"] != "parametric" {
		t.Fatalf("geometry_source = %v, want parametric", res.body["geometry_source"])
	}
	if res.body["length_in"] != json.Number("96") {
		t.Fatalf("length_in = %v, want 96", res.body["length_in"])
	}

	res = f.do("PATCH", "/api/v1/products/"+id+"/lead-time", map[string]any{"lead_time_days": 3, "revision": 2})
	if res.status != http.StatusOK {
		t.Fatalf("lead time: %d %s", res.status, res.raw)
	}
	// A null lead time publishes nothing, and is distinguishable from 0.
	res = f.do("PATCH", "/api/v1/products/"+id+"/lead-time", map[string]any{"lead_time_days": nil, "revision": 3})
	if res.status != http.StatusOK || res.body["lead_time_days"] != nil {
		t.Fatalf("clear lead time: %d %s", res.status, res.raw)
	}
}

// TestReorderAlertsEnvelope: the reorder list is the envelope too, with its
// quantities as decimal strings.
func TestReorderAlertsEnvelope(t *testing.T) {
	f := newFixture(t)
	res := f.createProduct(map[string]any{"reorder_point": "50"})
	if res.status != http.StatusCreated {
		t.Fatalf("create: %d %s", res.status, res.raw)
	}
	id := str(res.body["id"])
	defer f.deleteProduct(id)

	alerts := f.do("GET", "/api/v1/products/reorder-alerts", nil)
	if alerts.status != http.StatusOK {
		t.Fatalf("reorder-alerts: %d %s", alerts.status, alerts.raw)
	}
	items, _ := alerts.body["items"].([]any)
	if alerts.body["items"] == nil {
		t.Fatalf("reorder-alerts is not the envelope: %s", alerts.raw)
	}
	found := false
	for _, it := range items {
		m := it.(map[string]any)
		if str(m["product_id"]) == id {
			found = true
			if m["reorder_point"] != "50" {
				t.Errorf("reorder_point = %v, want \"50\"", m["reorder_point"])
			}
			if m["current_stock"] != "0" {
				t.Errorf("current_stock = %v, want \"0\"", m["current_stock"])
			}
		}
	}
	if !found {
		t.Fatalf("the product below its reorder point is not in the alerts: %s", alerts.raw)
	}
}

// TestGetNotFound: the error envelope on a 404.
func TestGetNotFound(t *testing.T) {
	f := newFixture(t)
	res := f.do("GET", "/api/v1/products/"+uuid.NewString(), nil)
	if res.status != http.StatusNotFound {
		t.Fatalf("status = %d", res.status)
	}
	errBody := res.body["error"].(map[string]any)
	if errBody["code"] != "not_found" {
		t.Fatalf("code = %v", errBody["code"])
	}
}

func str(v any) string {
	if s, ok := v.(string); ok {
		return s
	}
	return fmt.Sprintf("%v", v)
}

// TestMarginWriteConcurrency: three racers on one revision have exactly one
// winner (the recipe's contender proof; the revision check and the write are
// one database act, so the two losers read 409 stale_revision).
func TestMarginWriteConcurrency(t *testing.T) {
	f := newFixture(t)
	created := f.createProduct(nil)
	defer f.deleteProduct(str(created.body["id"]))
	id := str(created.body["id"])

	const racers = 3
	results := make(chan int, racers)
	for i := 0; i < racers; i++ {
		go func(i int) {
			res := f.do("PATCH", "/api/v1/products/"+id+"/margins", map[string]any{
				"target_margin": 0.1 * float64(i+1), "commission_rate": 0.01, "revision": 1,
			})
			results <- res.status
		}(i)
	}
	winners, stale := 0, 0
	for i := 0; i < racers; i++ {
		switch status := <-results; status {
		case http.StatusOK:
			winners++
		case http.StatusConflict:
			stale++
		default:
			t.Fatalf("racer answered %d", status)
		}
	}
	if winners != 1 || stale != racers-1 {
		t.Fatalf("%d winners and %d stale among %d racers; want exactly 1 winner", winners, stale, racers)
	}
}
