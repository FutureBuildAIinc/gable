// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package serve

// The pricing catalogue's WALLED routes (ADR 0006 section 5.6) get their
// bound key legs: the coverage test fails for any WALLED allowlist entry no
// driver sent a bound key call to, so each new wall needs its leg here.

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/google/uuid"
)

func TestKeyBranchPin_PricingCatalogLegs(t *testing.T) {
	f := newKeyPinFixture(t)
	f.record()
	defer driverDone(t, "catalog")

	A := f.branchA.String()
	prod := f.posProd.String()

	// A level and profile to name in the price rows and the vendor costs.
	levelCode := "PC" + strings.ToUpper(strings.ReplaceAll(uuid.NewString()[:8], "-", ""))
	var levelID string
	if err := f.db.Pool.QueryRow(t.Context(), `
		INSERT INTO price_levels (code, name, basis, adjust_percent, position)
		VALUES ($1, 'pin catalog level', 'BASE', -5, 9700) RETURNING id::text`, levelCode).Scan(&levelID); err != nil {
		t.Fatalf("seed level: %v", err)
	}
	t.Cleanup(func() {
		_, _ = f.db.Pool.Exec(t.Context(), `DELETE FROM price_levels WHERE code = $1`, levelCode)
	})
	// A vendor of the test's own with a default LIST level, so the cost read
	// resolves the default level without naming one.
	var vendorLevel, vendor string
	if err := f.db.Pool.QueryRow(t.Context(), `
		WITH v AS (
			INSERT INTO vendors (id, name) VALUES ($1::uuid, 'pin catalog vendor ' || $1::text) RETURNING id
		), lvl AS (
			INSERT INTO vendor_price_levels (vendor_id, code, name, is_default)
			SELECT v.id, $2, 'pin catalog list', TRUE FROM v RETURNING id::text
		)
		SELECT lvl.id::text, v.id::text FROM lvl, v`,
		uuid.New(), "PC"+strings.ToUpper(strings.ReplaceAll(uuid.NewString()[:8], "-", ""))).Scan(&vendorLevel, &vendor); err != nil {
		t.Fatalf("seed vendor and level: %v", err)
	}
	t.Cleanup(func() {
		_, _ = f.db.Pool.Exec(t.Context(), `DELETE FROM vendor_product_costs WHERE vendor_id = $1`, vendor)
		_, _ = f.db.Pool.Exec(t.Context(), `DELETE FROM vendor_price_levels WHERE id = $1`, vendorLevel)
		_, _ = f.db.Pool.Exec(t.Context(), `DELETE FROM customer_contracts WHERE branch_id = $1 AND product_id = $2`, f.branchA, f.posProd)
		_, _ = f.db.Pool.Exec(t.Context(), `DELETE FROM product_prices WHERE product_id = $1 AND branch_id = $2`, f.posProd, f.branchA)
		_, _ = f.db.Pool.Exec(t.Context(), `DELETE FROM yard_schedule_windows WHERE yard_id IN ($1, $2)`, f.branchA, f.branchB)
		_, _ = f.db.Pool.Exec(t.Context(), `DELETE FROM events_outbox WHERE entity_type IN ('contract', 'vendor_cost', 'price_level', 'price_profile', 'product') AND entity_id::text IN (SELECT id::text FROM customer_contracts WHERE branch_id = $1)`, f.branchA)
	})

	post := func(path, body string) string {
		status, buf := f.key(t, f.bound, "POST", path, body, nil)
		if status != http.StatusCreated {
			t.Fatalf("bound POST %s: %d %s", path, status, buf)
		}
		return string(buf)
	}
	put := func(path, body string) {
		if status, buf := f.key(t, f.bound, "PUT", path, body, nil); status != http.StatusOK {
			t.Fatalf("bound PUT %s: %d %s", path, status, buf)
		}
	}
	get := func(path string) {
		if status, buf := f.key(t, f.bound, "GET", path, "", nil); status != http.StatusOK {
			t.Fatalf("bound GET %s: %d %s", path, status, buf)
		}
	}

	var rev string
	if err := f.db.Pool.QueryRow(t.Context(), `SELECT revision::text FROM products WHERE id = $1`, f.posProd).Scan(&rev); err != nil {
		t.Fatal(err)
	}

	// The product's branch A slice: the PUT names branch A, so the payload
	// rule holds, and the GET is walled to branch A's rows plus the every
	// branch rows.
	put("/api/v1/pricing/products/"+prod+"/prices?branch_id="+A,
		`{"prices":[{"unit_price_ten_thousandths":80000}],"revision":`+rev+`}`)
	f.twinLeg("PUT", "/api/v1/pricing/products/"+prod+"/prices")
	get("/api/v1/pricing/products/" + prod + "/prices")
	f.twinLeg("GET", "/api/v1/pricing/products/"+prod+"/prices")

	// A contract owned by branch A, then its row legs.
	id := post("/api/v1/pricing/contracts",
		`{"customer_id":"`+f.custID.String()+`","product_id":"`+prod+`","branch_id":"`+A+`","contract_price_ten_thousandths":90000}`)
	contractID := extractID(t, id)
	f.twinLeg("POST", "/api/v1/pricing/contracts")
	get("/api/v1/pricing/contracts/" + contractID)
	f.twinLeg("GET", "/api/v1/pricing/contracts")
	get("/api/v1/pricing/contracts/" + contractID)
	f.twinLeg("GET", "/api/v1/pricing/contracts/{id}")
	put("/api/v1/pricing/contracts/"+contractID, `{"contract_price_ten_thousandths":85000,"revision":1}`)
	f.twinLeg("PUT", "/api/v1/pricing/contracts/{id}")
	status, buf := f.key(t, f.bound, "DELETE", "/api/v1/pricing/contracts/"+contractID, `{"revision":2}`, nil)
	if status != http.StatusNoContent {
		t.Fatalf("bound DELETE contract: %d %s", status, buf)
	}
	f.twinLeg("DELETE", "/api/v1/pricing/contracts/{id}")

	// A vendor cost of branch A, then its read.
	post("/api/v1/pricing/vendor-costs",
		`{"vendor_id":"`+vendor+`","vendor_price_level_id":"`+vendorLevel+`","product_id":"`+prod+`","branch_id":"`+A+`","purchase_uom":"EA","unit_cost_ten_thousandths":40000,"effective_from":"2020-01-01"}`)
	f.twinLeg("POST", "/api/v1/pricing/vendor-costs")
	var costID string
	if err := f.db.Pool.QueryRow(t.Context(),
		`SELECT id::text FROM vendor_product_costs WHERE product_id = $1 AND branch_id = $2 LIMIT 1`, f.posProd, f.branchA).Scan(&costID); err != nil {
		t.Fatal(err)
	}
	get("/api/v1/pricing/vendor-costs")
	f.twinLeg("GET", "/api/v1/pricing/vendor-costs")
	get("/api/v1/pricing/vendor-costs/" + costID)
	f.twinLeg("GET", "/api/v1/pricing/vendor-costs/{id}")
	put("/api/v1/pricing/vendor-costs/"+costID,
		`{"vendor_id":"`+vendor+`","vendor_price_level_id":"`+vendorLevel+`","product_id":"`+prod+`","branch_id":"`+A+`","purchase_uom":"EA","unit_cost_ten_thousandths":42000,"effective_from":"2020-01-01","revision":1}`)
	f.twinLeg("PUT", "/api/v1/pricing/vendor-costs/{id}")

	// The cost read at the pin's branch.
	get("/api/v1/pricing/cost?vendor_id=" + vendor + "&product_id=" + prod + "&branch_id=" + A)
	f.twinLeg("GET", "/api/v1/pricing/cost")

	// Scheduling windows (PE-08): branch A's yard window succeeds, while branch
	// B's foreign twin is walled off (403 on payload create, 404 on by-id
	// reads/writes, excluded from list).
	B := f.branchB.String()
	var winBID string
	if err := f.db.Pool.QueryRow(t.Context(), `
		INSERT INTO yard_schedule_windows (yard_id, kind, weekday, start_time, end_time, capacity, cutoff_minutes)
		VALUES ($1, 'delivery', 2, '08:00', '12:00', 4, 60) RETURNING id::text`, f.branchB).Scan(&winBID); err != nil {
		t.Fatalf("seed branch B window: %v", err)
	}
	winBody := post("/api/v1/scheduling/windows",
		`{"yard_id":"`+A+`","kind":"delivery","weekday":1,"start_time":"07:00","end_time":"11:00","capacity":4,"cutoff_minutes":60}`)
	winAID := extractID(t, winBody)
	if st, b := f.key(t, f.bound, "POST", "/api/v1/scheduling/windows",
		`{"yard_id":"`+B+`","kind":"delivery","weekday":3,"start_time":"07:00","end_time":"11:00","capacity":4,"cutoff_minutes":60}`, nil); st != http.StatusForbidden {
		t.Fatalf("bound POST scheduling window on branch B: %d %s, want 403", st, b)
	}
	f.twinLeg("POST", "/api/v1/scheduling/windows")

	st, listBuf := f.key(t, f.bound, "GET", "/api/v1/scheduling/windows", "", nil)
	if st != http.StatusOK || !strings.Contains(string(listBuf), winAID) || strings.Contains(string(listBuf), winBID) {
		t.Fatalf("bound GET scheduling windows: %d %s", st, listBuf)
	}
	f.twinLeg("GET", "/api/v1/scheduling/windows")

	get("/api/v1/scheduling/windows/" + winAID)
	if st, b := f.key(t, f.bound, "GET", "/api/v1/scheduling/windows/"+winBID, "", nil); st != http.StatusNotFound {
		t.Fatalf("bound GET scheduling window B: %d %s, want 404", st, b)
	}
	f.twinLeg("GET", "/api/v1/scheduling/windows/{id}")

	put("/api/v1/scheduling/windows/"+winAID, `{"capacity":6}`)
	if st, b := f.key(t, f.bound, "PUT", "/api/v1/scheduling/windows/"+winBID, `{"capacity":6}`, nil); st != http.StatusNotFound {
		t.Fatalf("bound PUT scheduling window B: %d %s, want 404", st, b)
	}
	f.twinLeg("PUT", "/api/v1/scheduling/windows/{id}")

	if st, b := f.key(t, f.bound, "DELETE", "/api/v1/scheduling/windows/"+winBID, "", nil); st != http.StatusNotFound {
		t.Fatalf("bound DELETE scheduling window B: %d %s, want 404", st, b)
	}
	status, buf = f.key(t, f.bound, "DELETE", "/api/v1/scheduling/windows/"+winAID, "", nil)
	if status != http.StatusNoContent {
		t.Fatalf("bound DELETE scheduling window A: %d %s", status, buf)
	}
	f.twinLeg("DELETE", "/api/v1/scheduling/windows/{id}")
}

// extractID reads the id field of a created row's answer.
func extractID(t *testing.T, body string) string {
	t.Helper()
	var out struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal([]byte(body), &out); err != nil {
		t.Fatalf("the answer holds no id: %s", body)
	}
	return out.ID
}
