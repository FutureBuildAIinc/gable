// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package configurator_test

// The configured item (QE-06) on the wire: the server prices a configuration
// from the stored options, the client never sends a price, and the
// configured row is a product the dealer never holds.
//
// The two modules of the millwork app are wired the way serve wires them:
// the option catalog with the product catalog behind it, and the
// configurator with the catalog as its builder.

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/gablelbm/gable/internal/configurator"
	"github.com/gablelbm/gable/internal/millwork"
	"github.com/gablelbm/gable/internal/product"
	"github.com/gablelbm/gable/internal/testutil"
	"github.com/gablelbm/gable/pkg/database"
	"github.com/google/uuid"
)

// configuredFixture builds the millwork app's two modules over one database.
type configuredFixture struct {
	t      *testing.T
	db     *database.DB
	srv    *httptest.Server
	prefix string
}

func newConfiguredFixture(t *testing.T) *configuredFixture {
	t.Helper()
	t.Setenv("AUTH_MODE", "dev")
	db := testutil.RequireDB(t)
	f := &configuredFixture{t: t, db: db, prefix: "qe6" + uuid.NewString()[:8]}

	productSvc := product.NewService(product.NewRepository(db))
	catalog := millwork.NewService(millwork.NewRepository(db)).WithProducts(productSvc)
	svc := configurator.NewService(configurator.NewRepository(db)).WithConfiguredBuilder(catalog)

	mux := http.NewServeMux()
	millwork.NewHandler(catalog).RegisterRoutes(mux)
	configurator.NewHandler(svc).RegisterRoutes(mux)
	f.srv = httptest.NewServer(mux)
	t.Cleanup(func() {
		f.srv.Close()
		ctx := context.Background()
		_, _ = db.Pool.Exec(ctx, `DELETE FROM millwork_options WHERE category LIKE $1`, f.prefix+"%")
		_, _ = db.Pool.Exec(ctx, `DELETE FROM products WHERE sku LIKE $1`, "NS-"+strings.ToUpper(f.prefix)+"%")
	})
	return f
}

// option creates one catalog option with the money the price is computed
// from. The category carries the fixture's own prefix, so the test's
// catalog never touches the seeded one.
func (f *configuredFixture) option(t *testing.T, category, name string, basePrice, baseCost, priceAdj, costAdj int64) string {
	t.Helper()
	ctx := context.Background()
	full := f.prefix + category
	if _, err := f.db.Pool.Exec(ctx, `
		INSERT INTO millwork_options (category, name, price_adjustment,
			base_price_cents, base_cost_cents, cost_adjustment_cents, attributes)
		VALUES ($1, $2, $3::numeric, $4, $5, $6, '{}'::jsonb)`,
		full, name, priceAdj/100, basePrice, baseCost, costAdj); err != nil {
		t.Fatalf("seed option %s/%s: %v", category, name, err)
	}
	t.Cleanup(func() {
		_, _ = f.db.Pool.Exec(ctx, `DELETE FROM millwork_options WHERE category = $1`, full)
	})
	return full
}

func (f *configuredFixture) post(t *testing.T, path, body string) (int, map[string]any) {
	t.Helper()
	req, err := http.NewRequest(http.MethodPost, f.srv.URL+path, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	var out map[string]any
	_ = json.NewDecoder(res.Body).Decode(&out)
	return res.StatusCode, out
}

// TestQE06_BuildSKUServerPrice proves the price is the server's: the build-sku
// route and the door estimate route both answer the sum of the stored
// options' money, to the cent, and the client sends no price at all.
func TestQE06_BuildSKUServerPrice(t *testing.T) {
	f := newConfiguredFixture(t)
	// A door type carries the base of the door; a material adjusts it.
	doorType := f.option(t, "door_type", "Panel Door", 25000, 15000, 0, 0)
	material := f.option(t, "material", "Mahogany", 0, 0, 15000, 8000)

	// The catalog's money is in cents; the wire carries ten-thousandths.
	const wantPriceCents = 40000 // 25000 base + 15000 adjustment
	const wantCostCents = 23000  // 15000 base + 8000 adjustment

	config := `{"product_type":"Door","selections":{"` + doorType + `":"Panel Door","` + material + `":"Mahogany"},"width":36,"height":80}`

	// The estimate route.
	code, out := f.post(t, "/api/v1/millwork/estimate", config)
	if code != http.StatusOK {
		t.Fatalf("estimate = %d: %v", code, out)
	}
	if got := int64(out["unit_price_ten_thousandths"].(float64)); got != wantPriceCents*100 {
		t.Errorf("estimate price = %d, want %d (the stored options' sum)", got, wantPriceCents*100)
	}
	if got := int64(out["unit_cost_ten_thousandths"].(float64)); got != wantCostCents*100 {
		t.Errorf("estimate cost = %d, want %d (the stored options' sum)", got, wantCostCents*100)
	}
	if sku, _ := out["sku"].(string); !strings.HasPrefix(sku, "NS-DR-") {
		t.Errorf("estimate sku = %q, want the NS- form", sku)
	}

	// The build-sku route: the same price, from the same catalog.
	code, out = f.post(t, "/api/v1/configurator/build-sku", config)
	if code != http.StatusOK {
		t.Fatalf("build-sku = %d: %v", code, out)
	}
	if got := int64(out["unit_price_ten_thousandths"].(float64)); got != wantPriceCents*100 {
		t.Errorf("build-sku price = %d, want %d (the stored options' sum)", got, wantPriceCents*100)
	}
	if got := int64(out["unit_cost_ten_thousandths"].(float64)); got != wantCostCents*100 {
		t.Errorf("build-sku cost = %d, want %d (the stored options' sum)", got, wantCostCents*100)
	}
	if out["configuration_json"] == nil {
		t.Error("build-sku answered no configuration")
	}
	if out["product_id"] == nil {
		t.Error("build-sku answered no product row")
	}
}

// TestQE06_SameConfigSameSKU proves the SKU is the configuration's: the same
// configuration always gives the same NS- SKU and the same product row, and
// two concurrent creates of one SKU make one row.
func TestQE06_SameConfigSameSKU(t *testing.T) {
	f := newConfiguredFixture(t)
	species := f.option(t, "species", "SYP", 800, 500, 0, 0)
	body := `{"product_type":"Lumber","selections":{"` + species + `":"SYP"}}`

	code, first := f.post(t, "/api/v1/configurator/build-sku", body)
	if code != http.StatusOK {
		t.Fatalf("build-sku = %d: %v", code, first)
	}
	code, second := f.post(t, "/api/v1/configurator/build-sku", body)
	if code != http.StatusOK {
		t.Fatalf("second build-sku = %d: %v", code, second)
	}
	if first["sku"] != second["sku"] {
		t.Errorf("the same configuration gave two SKUs: %v and %v", first["sku"], second["sku"])
	}
	if first["product_id"] != second["product_id"] {
		t.Errorf("the same configuration gave two product rows: %v and %v", first["product_id"], second["product_id"])
	}
	sku, _ := first["sku"].(string)
	if !strings.HasPrefix(sku, "NS-LBR-") {
		t.Errorf("the SKU = %q, want the NS- form", sku)
	}

	// Two concurrent creates of one SKU make one row: the SKU is unique and
	// the loser of the race reads the row the winner wrote.
	const racers = 6
	var wg sync.WaitGroup
	ids := make([]string, racers)
	codes := make([]int, racers)
	wg.Add(racers)
	for i := 0; i < racers; i++ {
		go func(i int) {
			defer wg.Done()
			code, out := f.post(t, "/api/v1/configurator/build-sku", body)
			codes[i] = code
			if id, ok := out["product_id"].(string); ok {
				ids[i] = id
			}
		}(i)
	}
	wg.Wait()
	for i := range codes {
		if codes[i] != http.StatusOK {
			t.Fatalf("racer %d = %d", i, codes[i])
		}
		if ids[i] != ids[0] {
			t.Errorf("racer %d read product row %s, want %s", i, ids[i], ids[0])
		}
	}
	var rows int
	if err := f.db.Pool.QueryRow(context.Background(),
		`SELECT COUNT(*) FROM products WHERE sku = $1`, sku).Scan(&rows); err != nil {
		t.Fatalf("count the product rows: %v", err)
	}
	if rows != 1 {
		t.Errorf("the SKU %q has %d product rows, want 1", sku, rows)
	}
}

// TestQE06_NonStockProduct proves the configured row is a product the dealer
// never holds: it carries the non-stock mark, and the stock reads leave it
// alone.
func TestQE06_NonStockProduct(t *testing.T) {
	f := newConfiguredFixture(t)
	species := f.option(t, "species", "SYP", 800, 500, 0, 0)

	code, out := f.post(t, "/api/v1/configurator/build-sku",
		`{"product_type":"Lumber","selections":{"`+species+`":"SYP"}}`)
	if code != http.StatusOK {
		t.Fatalf("build-sku = %d: %v", code, out)
	}
	productID, _ := out["product_id"].(string)
	sku, _ := out["sku"].(string)
	ctx := context.Background()

	var nonStock bool
	var price float64
	if err := f.db.Pool.QueryRow(ctx,
		`SELECT is_non_stock, base_price FROM products WHERE id = $1`, productID).
		Scan(&nonStock, &price); err != nil {
		t.Fatalf("read the configured product: %v", err)
	}
	if !nonStock {
		t.Error("the configured product row is not marked non-stock")
	}
	if price != 8 {
		t.Errorf("the configured product row's base price = %v, want 8 (the server's price)", price)
	}

	// The stock counts leave it alone: a configured item is never counted.
	var levels int
	if err := f.db.Pool.QueryRow(ctx,
		`SELECT COUNT(*) FROM inventory i JOIN products p ON p.id = i.product_id WHERE p.id = $1`,
		productID).Scan(&levels); err != nil {
		t.Fatalf("count the stock: %v", err)
	}
	if levels != 0 {
		t.Errorf("the configured product carries %d stock rows, want none", levels)
	}

	// The replenishment leaves it alone: a configured item is never
	// reordered.
	var reorderPoint float64
	if err := f.db.Pool.QueryRow(ctx,
		`SELECT COALESCE(reorder_point, 0) FROM products WHERE id = $1`, productID).
		Scan(&reorderPoint); err != nil {
		t.Fatalf("read the reorder point: %v", err)
	}
	if reorderPoint != 0 {
		t.Errorf("the configured product's reorder point = %v, want 0", reorderPoint)
	}

	// The counter's stocked search leaves it alone.
	var found int
	if err := f.db.Pool.QueryRow(ctx,
		`SELECT COUNT(*) FROM products p WHERE p.sku = $1 AND p.is_non_stock = false`, sku).
		Scan(&found); err != nil {
		t.Fatalf("search the stocked catalog: %v", err)
	}
	if found != 0 {
		t.Error("the configured product is in the stocked catalog")
	}
}
