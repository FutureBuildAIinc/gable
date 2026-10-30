// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package quote_test

// The configured item on a quote (QE-06): the line's price is the server's,
// the configuration rides the line, and the documents show the item as a
// short description.

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gablelbm/gable/internal/millwork"
	"github.com/gablelbm/gable/internal/order"
	"github.com/gablelbm/gable/internal/product"
	"github.com/gablelbm/gable/internal/purchase_order"
	"github.com/gablelbm/gable/internal/quote"
	"github.com/gablelbm/gable/internal/testutil"
	"github.com/gablelbm/gable/pkg/database"
	"github.com/gablelbm/gable/pkg/middleware"
	"github.com/gablelbm/gable/pkg/outbox"
	"github.com/google/uuid"
)

// configuredQuoteFixture builds the quote module the way serve wires it: the
// option catalog with the product catalog behind it as the pricer of a
// configured line, the order module as the converter, and the purchase order
// module behind the auto-PO.
type configuredQuoteFixture struct {
	t          *testing.T
	db         *database.DB
	srv        *httptest.Server
	customerID uuid.UUID
	branchID   uuid.UUID
	prefix     string
	poAdapter  *configuredPOAdapter
}

func newConfiguredQuoteFixture(t *testing.T) *configuredQuoteFixture {
	t.Helper()
	t.Setenv("AUTH_MODE", "dev")
	db := testutil.RequireDB(t)
	ctx := context.Background()
	f := &configuredQuoteFixture{t: t, db: db, customerID: uuid.New(), prefix: "qe6" + uuid.NewString()[:8]}

	// The customer sits on a branch of the test's own, with the default tax
	// rate the convert needs.
	branchID := uuid.New()
	if _, err := db.Pool.Exec(ctx,
		`INSERT INTO locations (id, type, code, default_tax_rate) VALUES ($1, 'BRANCH', $2, $3::numeric)`,
		branchID, "QE6-OW-"+branchID.String()[:8], "0.0825"); err != nil {
		t.Fatalf("seed branch: %v", err)
	}
	t.Cleanup(func() {
		for _, q := range []string{
			`DELETE FROM events_outbox WHERE entity_type = 'order' AND entity_id IN (SELECT id FROM orders WHERE branch_id = $1)`,
			`DELETE FROM order_lines WHERE order_id IN (SELECT id FROM orders WHERE branch_id = $1)`,
			`DELETE FROM orders WHERE branch_id = $1`,
			`DELETE FROM events_outbox WHERE entity_type = 'quote' AND entity_id IN (SELECT id FROM quotes WHERE branch_id = $1)`,
			`DELETE FROM quote_lines WHERE quote_id IN (SELECT id FROM quotes WHERE branch_id = $1)`,
			`DELETE FROM quotes WHERE branch_id = $1`,
			`DELETE FROM purchase_order_lines WHERE po_id IN (SELECT id FROM purchase_orders WHERE branch_id = $1)`,
			`DELETE FROM purchase_orders WHERE branch_id = $1`,
		} {
			if _, err := db.Pool.Exec(ctx, q, branchID); err != nil {
				t.Errorf("cleanup %q: %v", q, err)
			}
		}
		if _, err := db.Pool.Exec(ctx, `DELETE FROM customers WHERE id = $1`, f.customerID); err != nil {
			t.Errorf("cleanup the customer: %v", err)
		}
		if _, err := db.Pool.Exec(ctx, `DELETE FROM locations WHERE id = $1`, branchID); err != nil {
			t.Errorf("cleanup the branch: %v", err)
		}
	})
	if _, err := db.Pool.Exec(ctx, `INSERT INTO customers (id, name, account_number, primary_branch_id)
		VALUES ($1, 'Configured Test Co', $2, $3)`, f.customerID, "QE6-"+uuid.NewString()[:8], branchID); err != nil {
		t.Fatalf("seed customer: %v", err)
	}
	f.branchID = branchID

	productSvc := product.NewService(product.NewRepository(db))
	catalog := millwork.NewService(millwork.NewRepository(db)).WithProducts(productSvc)
	orderSvc := order.NewService(order.NewRepository(db)).
		WithOutbox(outbox.NewWriter(db, "")).WithTxRunner(db)
	poSvc := purchase_order.NewService(purchase_order.NewRepository(db), db, nil, nil, productSvc, nil).
		WithOutbox(outbox.NewWriter(db, ""))
	svc := quote.NewService(quote.NewRepository(db)).
		WithOutbox(outbox.NewWriter(db, "")).
		WithTxRunner(db).
		WithOrderCreator(orderSvc).
		WithConfiguredPricer(catalog)
	f.poAdapter = &configuredPOAdapter{poSvc: poSvc, productSvc: productSvc}
	svc.WithAutoPO(f.poAdapter)

	mux := http.NewServeMux()
	quote.NewHandler(svc).RegisterRoutes(mux)
	f.srv = httptest.NewServer(middleware.Idempotency(db)(mux))
	t.Cleanup(func() {
		f.srv.Close()
		_, _ = db.Pool.Exec(ctx, `DELETE FROM millwork_options WHERE category LIKE $1`, f.prefix+"%")
		_, _ = db.Pool.Exec(ctx, `DELETE FROM products WHERE sku LIKE $1`, "NS-"+strings.ToUpper(f.prefix)+"%")
	})
	return f
}

// configuredPOAdapter is the auto-PO bridge of serve: the purchase order line
// names the product it is for, so the vendor reads what the door is.
type configuredPOAdapter struct {
	poSvc      *purchase_order.Service
	productSvc *product.Service
}

func (a *configuredPOAdapter) CreatePOFromSpecialOrderLine(ctx context.Context, productID *uuid.UUID, vendorID *uuid.UUID, quantity float64, unitCost float64, linkedSOLineID uuid.UUID, branch uuid.UUID, description string) error {
	desc := description
	if desc == "" {
		desc = "special order"
	}
	if productID != nil && a.productSvc != nil {
		if p, err := a.productSvc.GetProduct(ctx, *productID); err == nil && p != nil {
			desc = fmt.Sprintf("%s - %s", p.SKU, p.Description)
		}
	}
	return a.poSvc.CreateFromSOLine(ctx, linkedSOLineID, branch, productID, vendorID, desc, quantity, unitCost)
}

// option creates one catalog option with the money the price is computed
// from.
func (f *configuredQuoteFixture) option(t *testing.T, category, name string, basePrice, baseCost, priceAdj, costAdj int64) string {
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

// configuredItem prices a configuration on the server and answers the item
// the configurator would have produced.
func (f *configuredQuoteFixture) configuredItem(t *testing.T, selections map[string]string) *millwork.ConfiguredItem {
	t.Helper()
	catalog := millwork.NewService(millwork.NewRepository(f.db)).
		WithProducts(product.NewService(product.NewRepository(f.db)))
	ctx := context.Background()
	item, err := catalog.BuildConfigured(ctx, "Door", selections, nil, nil)
	if err != nil {
		t.Fatalf("build the configured item: %v", err)
	}
	return item
}

func (f *configuredQuoteFixture) do(t *testing.T, method, path string, body any, headers ...string) (int, map[string]any) {
	t.Helper()
	var rdr *strings.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
		rdr = strings.NewReader(string(raw))
	}
	var req *http.Request
	var err error
	if rdr != nil {
		req, err = http.NewRequest(method, f.srv.URL+path, rdr)
	} else {
		req, err = http.NewRequest(method, f.srv.URL+path, nil)
	}
	if err != nil {
		t.Fatal(err)
	}
	if rdr != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	for i := 0; i+1 < len(headers); i += 2 {
		req.Header.Set(headers[i], headers[i+1])
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	var out map[string]any
	_ = json.NewDecoder(res.Body).Decode(&out)
	return res.StatusCode, out
}

// TestQE06_ClientPriceIgnored proves the client never sends the price of a
// configured item: the server prices the line from the configuration it
// carries, and a price the client sent on the line is read and ignored.
func TestQE06_ClientPriceIgnored(t *testing.T) {
	f := newConfiguredQuoteFixture(t)
	doorType := f.option(t, "door_type", "Panel Door", 25000, 15000, 0, 0)
	material := f.option(t, "material", "Mahogany", 0, 0, 15000, 8000)

	item := f.configuredItem(t, map[string]string{
		doorType: "Panel Door",
		material: "Mahogany",
	})
	const wantPrice = 40000 * 100 // the server's price, in ten-thousandths
	if int64(item.UnitPrice) != wantPrice {
		t.Fatalf("the server priced the item at %d, want %d", item.UnitPrice, wantPrice)
	}

	// The client sends a price of its own: one cent. The server ignores it.
	code, out := f.do(t, http.MethodPost, "/api/v1/quotes", map[string]any{
		"customer_id":   f.customerID.String(),
		"branch_id":     f.branchID.String(),
		"delivery_type": "pickup",
		"lines": []map[string]any{{
			"product_id":                item.ProductID.String(),
			"sku":                       item.SKU,
			"description":               item.Description,
			"quantity":                  "1",
			"uom":                       "EA",
			"unit_price_ten_thousandths": 1,
			"configuration_json":        json.RawMessage(item.Configuration),
		}},
	})
	if code != http.StatusCreated {
		t.Fatalf("create the quote = %d: %v", code, out)
	}
	quoteID, _ := out["id"].(string)
	code, out = f.do(t, http.MethodGet, "/api/v1/quotes/"+quoteID, nil)
	if code != http.StatusOK {
		t.Fatalf("read the quote = %d: %v", code, out)
	}
	lines, _ := out["lines"].([]any)
	if len(lines) != 1 {
		t.Fatalf("the quote has %d lines, want 1", len(lines))
	}
	line, _ := lines[0].(map[string]any)
	got, _ := line["unit_price_ten_thousandths"].(float64)
	if int64(got) != wantPrice {
		t.Errorf("the line's price = %v, want %d (the server's price; the client's was ignored)", got, wantPrice)
	}
}

// TestQE06_ConfigStoredOnLine proves the configuration rides the line: it is
// stored on the quote line and carried to the order line, and every document
// shows the item as a short description.
func TestQE06_ConfigStoredOnLine(t *testing.T) {
	f := newConfiguredQuoteFixture(t)
	doorType := f.option(t, "door_type", "Panel Door", 25000, 15000, 0, 0)
	material := f.option(t, "material", "Mahogany", 0, 0, 15000, 8000)

	item := f.configuredItem(t, map[string]string{
		doorType: "Panel Door",
		material: "Mahogany",
	})
	short := item.Description
	if !strings.Contains(short, "Panel Door") || !strings.Contains(short, "Mahogany") {
		t.Fatalf("the short description = %q, want it to name the choices", short)
	}

	code, out := f.do(t, http.MethodPost, "/api/v1/quotes", map[string]any{
		"customer_id":   f.customerID.String(),
		"branch_id":     f.branchID.String(),
		"delivery_type": "pickup",
		"lines": []map[string]any{{
			"product_id":         item.ProductID.String(),
			"sku":                item.SKU,
			"description":        short,
			"quantity":           "1",
			"uom":                "EA",
			"configuration_json": json.RawMessage(item.Configuration),
		}},
	})
	if code != http.StatusCreated {
		t.Fatalf("create the quote = %d: %v", code, out)
	}
	quoteID, _ := out["id"].(string)

	// The quote line stores the configuration and shows the short
	// description.
	code, out = f.do(t, http.MethodGet, "/api/v1/quotes/"+quoteID, nil)
	if code != http.StatusOK {
		t.Fatalf("read the quote = %d: %v", code, out)
	}
	lines, _ := out["lines"].([]any)
	line, _ := lines[0].(map[string]any)
	if line["configuration_json"] == nil {
		t.Error("the quote line carries no configuration")
	}
	if desc, _ := line["description"].(string); desc != short {
		t.Errorf("the quote line's description = %q, want the short description %q", desc, short)
	}

	// The convert carries the configuration to the order line. The convert
	// answers the order it created.
	code, out = f.do(t, http.MethodPost, "/api/v1/quotes/"+quoteID+"/convert", nil, "If-Match", `"1"`)
	if code != http.StatusCreated {
		t.Fatalf("convert the quote = %d: %v", code, out)
	}
	orderID, _ := out["id"].(string)
	orderLines, _ := out["lines"].([]any)
	if len(orderLines) != 1 {
		t.Fatalf("the order has %d lines, want 1", len(orderLines))
	}
	orderLine, _ := orderLines[0].(map[string]any)
	if orderLine["configuration_json"] == nil {
		t.Error("the order line carries no configuration")
	}
	if desc, _ := orderLine["description"].(string); desc != short {
		t.Errorf("the order line's description = %q, want the short description %q", desc, short)
	}

	// The configured product's own description is the short description, so
	// every document that shows the line's description shows what the item
	// is.
	ctx := context.Background()
	var poDescription string
	if err := f.db.Pool.QueryRow(ctx,
		`SELECT p.description FROM products p WHERE p.id = $1`, item.ProductID).Scan(&poDescription); err != nil {
		t.Fatalf("read the configured product: %v", err)
	}
	if poDescription != short {
		t.Errorf("the configured product's description = %q, want the short description %q", poDescription, short)
	}

	// The purchase order line names the item as a short description: the
	// bridge writes the product's own name on the line it raises, and the
	// configured product's name is the short description.
	var orderLineID uuid.UUID
	if err := f.db.Pool.QueryRow(ctx,
		`SELECT id FROM order_lines WHERE order_id = $1`, orderID).Scan(&orderLineID); err != nil {
		t.Fatalf("read the order line: %v", err)
	}
	productID := item.ProductID
	if err := f.poAdapter.CreatePOFromSpecialOrderLine(ctx, &productID, nil, 1, 230,
		orderLineID, f.branchID, short); err != nil {
		t.Fatalf("raise the purchase order: %v", err)
	}
	var poLine string
	if err := f.db.Pool.QueryRow(ctx,
		`SELECT description FROM purchase_order_lines WHERE linked_so_line_id = $1`,
		orderLineID).Scan(&poLine); err != nil {
		t.Fatalf("read the purchase order line: %v", err)
	}
	if !strings.Contains(poLine, short) {
		t.Errorf("the purchase order line's description = %q, want it to carry the short description %q", poLine, short)
	}
}
