// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package pricing_test

// The pricing routes on the wire contract (ADR 0001, ADR 0006 7.3): the
// price read answers the scaled price with its unit, the pair, the extension
// by Extend and the source lowercased; a quantity or job_id that does not
// parse is a 400 naming it (the base commit ignored both, ADR 0006 section
// 10); the rules list is the keyset envelope and the rule write validates
// its fields into one 400.

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gablelbm/gable/internal/customer"
	"github.com/gablelbm/gable/internal/platform/httpx"
	"github.com/gablelbm/gable/internal/pricing"
	"github.com/gablelbm/gable/internal/product"
	"github.com/gablelbm/gable/internal/testutil"
	"github.com/gablelbm/gable/pkg/database"
	"github.com/gablelbm/gable/pkg/middleware"
	"github.com/google/uuid"
)

type fixture struct {
	t          *testing.T
	db         *database.DB
	srv        *httptest.Server
	customerID uuid.UUID
	productID  uuid.UUID
	sku        string
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	t.Setenv("AUTH_MODE", "dev")
	db := testutil.RequireDB(t)
	f := &fixture{t: t, db: db, customerID: uuid.New(), productID: uuid.New(), sku: "WIRE-" + uuid.NewString()[:8]}
	// A leaked rule from an earlier run of this suite (an aborted create that
	// wrote an empty name) matches every product and would price the fixture
	// through it; only this suite's own rows go.
	if _, err := db.Pool.Exec(t.Context(), `DELETE FROM pricing_rules WHERE name LIKE 'WIRE-%' OR name = ''`); err != nil {
		t.Fatalf("clean leaked rules: %v", err)
	}
	branch := `(SELECT value::uuid FROM system_settings WHERE key = 'default_branch_id')`
	if _, err := db.Pool.Exec(t.Context(), `INSERT INTO customers (id, name, account_number, primary_branch_id)
		VALUES ($1, 'Pricing Wire Co', $2, `+branch+`)`, f.customerID, "PWR-"+uuid.NewString()[:8]); err != nil {
		t.Fatalf("seed customer: %v", err)
	}
	if _, err := db.Pool.Exec(t.Context(), `INSERT INTO products (id, sku, description, uom_primary, base_price)
		VALUES ($1, $2, '2x4x8 SPF', 'PCS', 23.25)`, f.productID, f.sku); err != nil {
		t.Fatalf("seed product: %v", err)
	}

	customerSvc := customer.NewService(customer.NewRepository(db))
	productSvc := product.NewService(product.NewRepository(db))
	svc := pricing.NewService(pricing.NewRepository(db))
	h := pricing.NewHandler(svc, customerSvc, productSvc)
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)
	f.srv = httptest.NewServer(middleware.Idempotency(db)(mux))
	t.Cleanup(func() {
		f.srv.Close()
		cleanupCtx := context.Background()
		_, _ = db.Pool.Exec(cleanupCtx, `DELETE FROM pricing_rules WHERE name = $1`, f.sku+" rule")
		_, _ = db.Pool.Exec(cleanupCtx, `DELETE FROM products WHERE id = $1`, f.productID)
		_, _ = db.Pool.Exec(cleanupCtx, `DELETE FROM customers WHERE id = $1`, f.customerID)
	})
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
	req.Header.Set("X-Request-ID", "req-pricing-wire")
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

// TestCalculateShape: the price read answers the scaled price in the
// product's stocking unit with the 1 and 1 pair, the extension by Extend,
// and the source lowercased. The base commit answered floats (final_price,
// original_price, discount_pct) and ignored bad inputs.
func TestCalculateShape(t *testing.T) {
	f := newFixture(t)

	res := f.do("GET", "/api/v1/pricing/calculate?customer_id="+f.customerID.String()+"&product_id="+f.productID.String()+"&quantity=7", nil)
	if res.status != http.StatusOK {
		t.Fatalf("status = %d, body %s", res.status, res.raw)
	}
	if res.body["unit_price_ten_thousandths"] != json.Number("232500") {
		t.Fatalf("unit_price_ten_thousandths = %v, want 232500 (23.25 per stocking unit)", res.body["unit_price_ten_thousandths"])
	}
	if res.body["price_uom"] != "PCS" || res.body["uom"] != "PCS" {
		t.Fatalf("units = %v/%v, want PCS/PCS", res.body["price_uom"], res.body["uom"])
	}
	if res.body["uom_qty"] != "1" || res.body["price_uom_qty"] != "1" {
		t.Fatalf("pair = %v/%v, want 1/1", res.body["uom_qty"], res.body["price_uom_qty"])
	}
	if res.body["line_total_cents"] != json.Number("16275") {
		t.Fatalf("line_total_cents = %v, want 16275 (7 x 23.25)", res.body["line_total_cents"])
	}
	if res.body["price_basis"] != "retail" {
		t.Fatalf("price_basis = %v, want retail", res.body["price_basis"])
	}
	if res.body["final_price"] != nil || res.body["source"] != nil {
		t.Fatalf("legacy fields survive on the wire: %s", res.raw)
	}

	// The engine's exact arithmetic shows through the read: a sub cent rule
	// price is the exact scale 4 value.
	if _, err := f.db.Pool.Exec(t.Context(),
		`INSERT INTO pricing_rules (id, name, rule_type, discount_pct, min_quantity, is_active)
		 VALUES ($1, $2, 'PROMOTIONAL', 5, 0, true)`, uuid.New(), f.sku+" rule"); err != nil {
		t.Fatalf("seed rule: %v", err)
	}
	res = f.do("GET", "/api/v1/pricing/calculate?customer_id="+f.customerID.String()+"&product_id="+f.productID.String(), nil)
	if res.body["unit_price_ten_thousandths"] != json.Number("220875") {
		t.Fatalf("unit_price_ten_thousandths = %v, want 220875 (exactly 5 percent off 23.25)", res.body["unit_price_ten_thousandths"])
	}
	if res.body["price_basis"] != "promotional" {
		t.Fatalf("price_basis = %v, want promotional", res.body["price_basis"])
	}
}

// TestCalculateRejectsBadInputs: a quantity or job_id that does not parse is
// a 400 naming it, where the base commit served the price with the value
// ignored (ADR 0006 section 10); the strict query guard refuses the rest.
func TestCalculateRejectsBadInputs(t *testing.T) {
	f := newFixture(t)
	base := "/api/v1/pricing/calculate?customer_id=" + f.customerID.String() + "&product_id=" + f.productID.String()

	for _, tc := range []struct {
		query string
		field string
	}{
		{base + "&quantity=abc", "quantity"},
		{base + "&quantity=1.23456", "quantity"},
		{base + "&quantity=-2", "quantity"},
		{base + "&job_id=not-a-uuid", "job_id"},
	} {
		res := f.do("GET", tc.query, nil)
		if res.status != http.StatusBadRequest {
			t.Fatalf("%s: status = %d, want 400", tc.query, res.status)
		}
		fields := map[string]bool{}
		for _, d := range res.body["error"].(map[string]any)["details"].([]any) {
			fields[d.(map[string]any)["field"].(string)] = true
		}
		if !fields[tc.field] {
			t.Fatalf("%s: details do not name %q: %s", tc.query, tc.field, res.raw)
		}
	}

	if res := f.do("GET", base+"&verbose=1", nil); res.status != http.StatusBadRequest {
		t.Fatalf("unknown parameter: status = %d, want 400", res.status)
	}
	if code := f.do("GET", base+"&verbose=1", nil).body["error"].(map[string]any)["code"]; code != "unsupported_query_parameter" {
		t.Fatalf("code = %v", code)
	}
}

// TestRulesListAndCreate: the rules list is the keyset envelope and a rule
// create validates into one 400 with every field named.
func TestRulesListAndCreate(t *testing.T) {
	f := newFixture(t)

	list := f.do("GET", "/api/v1/pricing/rules?limit=2&include=total", nil)
	if list.status != http.StatusOK || list.body["items"] == nil || list.body["total"] == nil {
		t.Fatalf("rules list is not the envelope: %d %s", list.status, list.raw)
	}

	bad := f.do("POST", "/api/v1/pricing/rules", map[string]any{
		"name":         "",
		"rule_type":    "mega_discount",
		"min_quantity": "12.5",
	}, "Idempotency-Key", uuid.NewString())
	if bad.status != http.StatusBadRequest {
		t.Fatalf("create validation: status = %d, want 400", bad.status)
	}
	fields := map[string]bool{}
	for _, d := range bad.body["error"].(map[string]any)["details"].([]any) {
		fields[d.(map[string]any)["field"].(string)] = true
	}
	for _, want := range []string{"name", "rule_type", "fixed_price_ten_thousandths"} {
		if !fields[want] {
			t.Errorf("details miss %q: %s", want, bad.raw)
		}
	}

	good := f.do("POST", "/api/v1/pricing/rules", map[string]any{
		"name":         f.sku + " rule",
		"rule_type":    "quantity_break",
		"discount_pct": "12.345",
		"min_quantity": "10",
	}, "Idempotency-Key", uuid.NewString())
	if good.status != http.StatusCreated {
		t.Fatalf("create: %d %s", good.status, good.raw)
	}
	if good.body["rule_type"] != "quantity_break" {
		t.Fatalf("rule_type = %v, want lowercase on the wire", good.body["rule_type"])
	}
	if good.body["discount_pct"] != "12.345" || good.body["min_quantity"] != "10" {
		t.Fatalf("percent and quantity fields = %v/%v, want decimal strings", good.body["discount_pct"], good.body["min_quantity"])
	}
	if loc := good.header.Get("Location"); !strings.HasPrefix(loc, "/api/v1/pricing/rules/") {
		t.Fatalf("Location = %q", loc)
	}

	// The exact percent survives the round trip through the engine: the rule
	// prices 1.0000 at exactly 0.87655 -> 0.8766.
	if _, err := f.db.Pool.Exec(t.Context(), `UPDATE products SET base_price = 1.0000 WHERE id = $1`, f.productID); err != nil {
		t.Fatalf("reprice product: %v", err)
	}
	calc := f.do("GET", "/api/v1/pricing/calculate?customer_id="+f.customerID.String()+"&product_id="+f.productID.String()+"&quantity=20", nil)
	if calc.body["unit_price_ten_thousandths"] != json.Number("8766") {
		t.Fatalf("unit_price_ten_thousandths = %v, want 8766 (12.345 percent off 1.0000)", calc.body["unit_price_ten_thousandths"])
	}
	var _ httpx.Price
}
