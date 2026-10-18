// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package portal

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gablelbm/gable/internal/account"
	"github.com/gablelbm/gable/internal/customer"
	"github.com/gablelbm/gable/internal/document"
	"github.com/gablelbm/gable/internal/inventory"
	"github.com/gablelbm/gable/internal/invoice"
	"github.com/gablelbm/gable/internal/order"
	"github.com/gablelbm/gable/internal/parsing"
	"github.com/gablelbm/gable/internal/payment"
	"github.com/gablelbm/gable/internal/pricing"
	"github.com/gablelbm/gable/internal/product"
	"github.com/gablelbm/gable/internal/quote"
	"github.com/gablelbm/gable/pkg/middleware"
	"github.com/google/uuid"
)

// countingGateway wraps payment.PaymentGateway to count Charge invocations
// and optionally inject a transport error or delay for single-flight tests.
type countingGateway struct {
	inner      payment.PaymentGateway
	charges    atomic.Int32
	chargeWait time.Duration
	chargeErr  error
}

func (g *countingGateway) Charge(ctx context.Context, req payment.ChargeRequest) (*payment.GatewayResult, error) {
	g.charges.Add(1)
	if g.chargeWait > 0 {
		time.Sleep(g.chargeWait)
	}
	if g.chargeErr != nil {
		return nil, g.chargeErr
	}
	return g.inner.Charge(ctx, req)
}

func (g *countingGateway) Capture(ctx context.Context, txID string, cents int64) (*payment.GatewayResult, error) {
	return g.inner.Capture(ctx, txID, cents)
}

func (g *countingGateway) Refund(ctx context.Context, txID string, cents int64) (*payment.GatewayResult, error) {
	return g.inner.Refund(ctx, txID, cents)
}

func (g *countingGateway) Void(ctx context.Context, txID string) (*payment.GatewayResult, error) {
	return g.inner.Void(ctx, txID)
}

// muxForPhase2 wires all Phase 2 upstream services into the portal service and handler.
func muxForPhase2(f *proFixture, customerID, userID uuid.UUID, role string, gw payment.PaymentGateway) (*http.ServeMux, *Service) {
	repo := NewRepository(f.db)
	productRepo := product.NewRepository(f.db)
	productSvc := product.NewService(productRepo)
	customerSvc := customer.NewService(customer.NewRepository(f.db))
	inventorySvc := inventory.NewService(inventory.NewRepository(f.db))
	pricingSvc := pricing.NewService(pricing.NewRepository(f.db)).
		WithRepository(pricing.NewResolveRepository(f.db))
	accountSvc := account.NewService(f.db, nil, testLogger())
	invoiceSvc := invoice.NewService(invoice.NewRepository(f.db), nil, accountSvc, f.db)
	orderSvc := order.NewService(order.NewRepository(f.db)).
		WithTxRunner(f.db).
		WithInventory(inventorySvc).
		WithInvoices(invoiceSvc)
	quoteSvc := quote.NewService(quote.NewRepository(f.db)).
		WithTxRunner(f.db).
		WithOrderCreator(orderSvc)
	parsingSvc := parsing.NewService(productRepo, nil)
	docStore := document.NewStore(f.db)
	docSvc := document.NewService(productRepo).
		WithYards(docStore).
		WithJobs(docStore).
		WithStatements(accountSvc)

	svc := NewService(repo, testSecret, testLogger(), pricingSvc, customerSvc, inventorySvc, orderSvc, productSvc).
		WithQuoteService(quoteSvc).
		WithParsingService(parsingSvc).
		WithAccountService(accountSvc).
		WithInvoiceService(invoiceSvc).
		WithDocumentService(docSvc)
	if gw != nil {
		svc.WithPaymentGateway(gw)
	}

	h := NewHandler(svc)
	mux := http.NewServeMux()
	authMw := func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			claims := &middleware.PortalClaims{
				CustomerID:     customerID,
				CustomerUserID: userID,
				Email:          "pro@example.test",
				Name:           "Pro Fixture",
				Role:           role,
			}
			ctx := context.WithValue(r.Context(), middleware.PortalClaimsKey, claims)
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
	h.RegisterRoutes(mux, authMw)
	return mux, svc
}

// TestCatalogProduct_ProjectionCarriesNoCostOrMarginFields proves the contractor
// catalog allowlist never serializes dealer cost, margin, WAC, floor, landed cost,
// reserved stock, or internal metadata fields.
func TestCatalogProduct_ProjectionCarriesNoCostOrMarginFields(t *testing.T) {
	lt := 3
	catID := uuid.New()
	detail := CatalogDetailDTO{
		CatalogProductDTO: CatalogProductDTO{
			ID:            uuid.New(),
			SKU:           "2X4-8-KD",
			Name:          "2x4x8 Kiln Dried Stud",
			Category:      "Dimensional Lumber",
			CategoryID:    &catID,
			UOM:           "EA",
			CustomerPrice: 4.25,
			BasePrice:     4.75,
			PriceSource:   "customer_tier",
			InStock:       true,
			Available:     120,
			LeadTimeDays:  &lt,
			Availability: []YardAvailabilityDTO{
				{
					YardID:       uuid.New(),
					YardCode:     "Y1",
					YardName:     "Main Yard",
					OnHand:       150,
					Available:    120,
					InStock:      true,
					LeadTimeDays: &lt,
				},
			},
		},
		VolumeBreaks: []VolumeBreakDTO{{MinQuantity: 100, UnitPrice: 3.95, PriceSource: "volume"}},
	}

	raw, err := json.Marshal(detail)
	if err != nil {
		t.Fatalf("marshal catalog detail: %v", err)
	}
	lower := strings.ToLower(string(raw))
	forbidden := []string{
		"cost",
		"margin",
		"wac",
		"floor",
		"landed",
		"reserved",
		"metadata",
		"vendor_cost",
		"vendor_id",
	}
	for _, word := range forbidden {
		if strings.Contains(lower, word) {
			t.Errorf("contractor catalog projection leaked forbidden token %q in JSON: %s", word, string(raw))
		}
	}
}

// TestPhase2_Part2_QuoteIntakeAndConvert tests pasted-list quote intake (without AI),
// photo quote intake (with AI + synthetic confirmation guard), capability checks,
// and converting an accepted/priced quote into a live order.
func TestPhase2_Part2_QuoteIntakeAndConvert(t *testing.T) {
	f := setupPro(t)
	ctx := context.Background()

	// 1. Capability refusals on quote intake: ap_clerk and site_super lack request_quotes.
	for _, role := range []string{RoleAPClerk, RoleSiteSuper} {
		mux, _ := muxForPhase2(f, f.aCustomer, f.users[role], role, nil)
		rec, _ := call(mux, http.MethodPost, "/api/portal/v1/quotes/intake",
			`{"text":"5 EA Pro fixture board","create_quote":true}`, nil)
		if rec.Code != http.StatusForbidden {
			t.Fatalf("role %s quote intake status = %d, want 403", role, rec.Code)
		}
	}

	// 2. Estimator submits pasted material list and creates a quote request in one step.
	estMux, _ := muxForPhase2(f, f.aCustomer, f.users[RoleEstimator], RoleEstimator, nil)
	rec, body := call(estMux, http.MethodPost, "/api/portal/v1/quotes/intake",
		`{"text":"5 EA Pro fixture board\n12 LF Cedar fascia","create_quote":true,"notes":"Jobsite gate"}`, nil)
	if rec.Code != http.StatusCreated {
		t.Fatalf("estimator pasted quote intake status = %d, want 201: %s", rec.Code, rec.Body.String())
	}
	if body["source"] != "text" || body["synthetic"] != false {
		t.Errorf("intake source=%v synthetic=%v, want source=text synthetic=false", body["source"], body["synthetic"])
	}
	items, _ := body["items"].([]any)
	if len(items) != 2 {
		t.Fatalf("intake items len = %d, want 2: %v", len(items), body["items"])
	}
	qMap, _ := body["quote"].(map[string]any)
	if qMap == nil || qMap["status"] != "REQUESTED" {
		t.Fatalf("expected created quote in REQUESTED status, got %v", body["quote"])
	}
	requestedQuoteID, _ := uuid.Parse(fmt.Sprint(qMap["id"]))

	// 3. Photo intake without AI key returns synthetic=true; create_quote=true without allow_synthetic refuses 409.
	fakeImg := base64.StdEncoding.EncodeToString([]byte("fake-jpeg-bytes"))
	rec, _ = call(estMux, http.MethodPost, "/api/portal/v1/quotes/intake",
		fmt.Sprintf(`{"file_base64":%q,"content_type":"image/jpeg","create_quote":true}`, fakeImg), nil)
	if rec.Code != http.StatusConflict {
		t.Fatalf("unconfirmed synthetic photo intake with create_quote=true status = %d, want 409: %s", rec.Code, rec.Body.String())
	}
	// With allow_synthetic=true, photo intake creates the quote.
	rec, body = call(estMux, http.MethodPost, "/api/portal/v1/quotes/intake",
		fmt.Sprintf(`{"file_base64":%q,"content_type":"image/jpeg","create_quote":true,"allow_synthetic":true}`, fakeImg), nil)
	if rec.Code != http.StatusCreated {
		t.Fatalf("confirmed synthetic photo intake status = %d, want 201: %s", rec.Code, rec.Body.String())
	}
	if body["source"] != "photo" || body["synthetic"] != true {
		t.Errorf("photo intake source=%v synthetic=%v, want photo/true", body["source"], body["synthetic"])
	}

	// 4. Converting an unpriced REQUESTED quote refuses with 409.
	rec, _ = call(estMux, http.MethodPost, "/api/portal/v1/quotes/"+requestedQuoteID.String()+"/convert", `{}`, nil)
	if rec.Code != http.StatusConflict {
		t.Fatalf("convert REQUESTED quote status = %d, want 409", rec.Code)
	}

	// 5. Converting a priced (SENT) quote:
	// First add a line to f.aQuote so ConvertInProcess can build an order from it.
	_, err := f.db.Pool.Exec(ctx, `
		INSERT INTO quote_lines (id, quote_id, product_id, sku, description, quantity, unit_price, line_total, uom, price_uom, uom_qty, price_uom_qty, stock_uom, stock_quantity)
		VALUES ($1, $2, $3, 'PRO-1', 'Pro fixture board', 4, 25.00, 100.00, 'EA', 'EA', 1, 1, 'EA', 4)`,
		uuid.New(), f.aQuote, f.product)
	if err != nil {
		t.Fatalf("seed quote_line: %v", err)
	}

	// ap_clerk lacks CapOrder -> 403.
	apMux, _ := muxForPhase2(f, f.aCustomer, f.users[RoleAPClerk], RoleAPClerk, nil)
	rec, _ = call(apMux, http.MethodPost, "/api/portal/v1/quotes/"+f.aQuote.String()+"/convert", `{}`, nil)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("ap_clerk convert quote status = %d, want 403", rec.Code)
	}

	// Customer B cannot convert Customer A's quote -> 404.
	bMux, _ := muxForPhase2(f, f.bCustomer, uuid.New(), RoleOwner, nil)
	rec, _ = call(bMux, http.MethodPost, "/api/portal/v1/quotes/"+f.aQuote.String()+"/convert", `{}`, nil)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("other tenant convert quote status = %d, want 404", rec.Code)
	}

	// Owner converts Customer A's priced quote -> 201 Created with order_id and order_number.
	ownerMux, _ := muxForPhase2(f, f.aCustomer, f.users[RoleOwner], RoleOwner, nil)
	rec, body = call(ownerMux, http.MethodPost, "/api/portal/v1/quotes/"+f.aQuote.String()+"/convert", `{}`, nil)
	if rec.Code != http.StatusCreated {
		t.Fatalf("owner convert quote status = %d, want 201: %s", rec.Code, rec.Body.String())
	}
	if body["order_id"] == nil || body["order_number"] == "" {
		t.Errorf("convert response missing order_id or order_number: %v", body)
	}
}

// TestPhase2_Part3_CatalogYardStockReorderPreviewAndProcurement tests per-yard stock
// and shortest lead time on catalog products, reorder price-difference preview,
// customer-priced reorder with project tagging, and named material lists (procurement board).
func TestPhase2_Part3_CatalogYardStockReorderPreviewAndProcurement(t *testing.T) {
	f := setupPro(t)
	ctx := context.Background()

	// Seed inventory at aYard (available 15) and bYard (available 0) and lead_time_days=4 on product.
	_, err := f.db.Pool.Exec(ctx, `UPDATE products SET lead_time_days = 4 WHERE id = $1`, f.product)
	if err != nil {
		t.Fatalf("update product lead_time_days: %v", err)
	}
	_, err = f.db.Pool.Exec(ctx, `
		INSERT INTO inventory (id, product_id, location_id, location, quantity, allocated)
		VALUES ($1, $2, $3, 'YARD-A', 20, 5)`, uuid.New(), f.product, f.aYard)
	if err != nil {
		t.Fatalf("insert inventory: %v", err)
	}

	// Seed a customer-specific contract price for f.aCustomer on f.product: $21.50 (2150 cents) vs $25.00 base.
	_, err = f.db.Pool.Exec(ctx, `
		INSERT INTO customer_contracts (id, customer_id, product_id, contract_price, price_uom, created_at, updated_at)
		VALUES ($1, $2, $3, 21.50, 'EA', NOW(), NOW())`,
		uuid.New(), f.aCustomer, f.product)
	if err != nil {
		t.Fatalf("insert customer_contract: %v", err)
	}

	ownerMux, _ := muxForPhase2(f, f.aCustomer, f.users[RoleOwner], RoleOwner, nil)

	// 1. Catalog product detail includes per-yard availability and customer price.
	rec, body := call(ownerMux, http.MethodGet, "/api/portal/v1/catalog/"+f.product.String(), "", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("get catalog product status = %d, want 200: %s", rec.Code, rec.Body.String())
	}
	if gotPrice, _ := body["customer_price"].(float64); gotPrice != 21.50 {
		t.Errorf("catalog customer_price = %v, want 21.50", body["customer_price"])
	}
	avail, _ := body["availability"].([]any)
	if len(avail) == 0 {
		t.Fatalf("expected non-empty per-yard availability slice on catalog product, got %v", body["availability"])
	}
	// Because it is in stock at aYard (available 15), headline lead_time_days should be 0.
	if lt, ok := body["lead_time_days"].(float64); !ok || int(lt) != 0 {
		t.Errorf("in-stock product lead_time_days = %v, want 0", body["lead_time_days"])
	}

	// 2. Reorder preview and customer-priced reorder with project tagging.
	// Seed a historical line on f.aFulfilled at $20.00 (2000 cents), qty 3.
	_, err = f.db.Pool.Exec(ctx, `
		INSERT INTO order_lines (id, order_id, position, line_type, product_id, description, quantity, unit_price, line_total, uom, price_uom, uom_qty, price_uom_qty, stock_uom, stock_quantity)
		VALUES ($1, $2, 0, 'PRODUCT', $3, 'Pro fixture board', 3, 20.00, 60.00, 'EA', 'EA', 1, 1, 'EA', 3)`,
		uuid.New(), f.aFulfilled, f.product)
	if err != nil {
		t.Fatalf("insert order_line: %v", err)
	}
	projectID := uuid.New()
	_, err = f.db.Pool.Exec(ctx, `
		INSERT INTO projects (id, customer_id, name, status)
		VALUES ($1, $2, 'Lakeside Deck', 'ACTIVE')`, projectID, f.aCustomer)
	if err != nil {
		t.Fatalf("insert project: %v", err)
	}

	rec, body = call(ownerMux, http.MethodGet, "/api/portal/v1/orders/"+f.aFulfilled.String()+"/reorder-preview", "", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("reorder-preview status = %d, want 200: %s", rec.Code, rec.Body.String())
	}
	lines, _ := body["lines"].([]any)
	if len(lines) != 1 {
		t.Fatalf("reorder-preview lines len = %d, want 1", len(lines))
	}
	line0, _ := lines[0].(map[string]any)
	if line0["original_unit_price_cents"] != float64(2000) || line0["current_unit_price_cents"] != float64(2150) || line0["price_diff_cents"] != float64(150) {
		t.Errorf("reorder-preview line pricing = %v, want original=2000 current=2150 diff=150", line0)
	}

	// Reorder uses customer price ($21.50 * 3 = $64.50) and tags with projectID.
	rec, body = call(ownerMux, http.MethodPost, "/api/portal/v1/orders/reorder",
		fmt.Sprintf(`{"order_id":%q,"project_id":%q}`, f.aFulfilled.String(), projectID.String()), nil)
	if rec.Code != http.StatusCreated {
		t.Fatalf("reorder status = %d, want 201: %s", rec.Code, rec.Body.String())
	}
	newOrderID, _ := uuid.Parse(fmt.Sprint(body["order_id"]))
	var gotTotal float64
	var gotProj *uuid.UUID
	if err := f.db.Pool.QueryRow(ctx, `SELECT total_amount, project_id FROM orders WHERE id = $1`, newOrderID).Scan(&gotTotal, &gotProj); err != nil {
		t.Fatalf("query reordered order: %v", err)
	}
	if gotTotal != 64.50 {
		t.Errorf("reordered total_amount = %.2f, want 64.50 (customer price 21.50 * 3)", gotTotal)
	}
	if gotProj == nil || *gotProj != projectID {
		t.Errorf("reordered project_id = %v, want %s", gotProj, projectID)
	}

	// 3. Named material lists (procurement board).
	apMux, _ := muxForPhase2(f, f.aCustomer, f.users[RoleAPClerk], RoleAPClerk, nil)
	rec, _ = call(apMux, http.MethodPost, "/api/portal/v1/procurement/projects", `{"name":"Framing Takeoff"}`, nil)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("ap_clerk create procurement project status = %d, want 403", rec.Code)
	}

	rec, body = call(ownerMux, http.MethodPost, "/api/portal/v1/procurement/projects", `{"name":"Framing Takeoff"}`, nil)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create procurement project status = %d, want 201: %s", rec.Code, rec.Body.String())
	}
	procID := fmt.Sprint(body["id"])

	// Add a line to the material list.
	rec, body = call(ownerMux, http.MethodPost, "/api/portal/v1/procurement/projects/"+procID+"/lines",
		fmt.Sprintf(`{"product_id":%q,"description":"Pro fixture board","qty_entered":10,"uom":"EA","notes":"Bundle A"}`, f.product.String()), nil)
	if rec.Code != http.StatusCreated {
		t.Fatalf("add procurement line status = %d, want 201: %s", rec.Code, rec.Body.String())
	}
	lineID := fmt.Sprint(body["id"])

	// Update the line quantity to 20.
	rec, _ = call(ownerMux, http.MethodPut, "/api/portal/v1/procurement/projects/"+procID+"/lines/"+lineID,
		fmt.Sprintf(`{"product_id":%q,"description":"Pro fixture board","qty_entered":20,"uom":"EA","notes":"Bundle A updated"}`, f.product.String()), nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("update procurement line status = %d, want 200: %s", rec.Code, rec.Body.String())
	}

	// Submit RFQ: prices indicatively at 2150 cents/unit (43000 cents line total), creates quote, transitions to QUOTING.
	rec, body = call(ownerMux, http.MethodPost, "/api/portal/v1/procurement/projects/"+procID+"/submit-rfq", `{}`, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("submit-rfq status = %d, want 200: %s", rec.Code, rec.Body.String())
	}
	if body["status"] != "QUOTING" || body["quote_id"] == nil {
		t.Errorf("submit-rfq status=%v quote_id=%v, want QUOTING and non-nil quote_id", body["status"], body["quote_id"])
	}
	pLines, _ := body["lines"].([]any)
	if len(pLines) != 1 {
		t.Fatalf("submit-rfq lines len = %d, want 1", len(pLines))
	}
	pl0, _ := pLines[0].(map[string]any)
	if pl0["indicative_unit_price_cents"] != float64(2150) || pl0["indicative_line_total_cents"] != float64(43000) {
		t.Errorf("submit-rfq indicative pricing = %v, want unit=2150 total=43000", pl0)
	}

	// Editing a line after QUOTING is refused with 409.
	rec, _ = call(ownerMux, http.MethodPost, "/api/portal/v1/procurement/projects/"+procID+"/lines",
		`{"description":"Extra","qty_entered":1,"uom":"EA"}`, nil)
	if rec.Code != http.StatusConflict {
		t.Fatalf("add line in QUOTING status = %d, want 409", rec.Code)
	}

	// Other tenant gets 404.
	bMux, _ := muxForPhase2(f, f.bCustomer, uuid.New(), RoleOwner, nil)
	rec, _ = call(bMux, http.MethodGet, "/api/portal/v1/procurement/projects/"+procID, "", nil)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("other tenant get procurement project status = %d, want 404", rec.Code)
	}
}

// TestPhase2_Part5_BillingStatementAgingJobSummariesAndPDFs tests see_money capability
// enforcement, balance tiles, statement & aging buckets, job summaries, and PDF generation.
func TestPhase2_Part5_BillingStatementAgingJobSummariesAndPDFs(t *testing.T) {
	f := setupPro(t)
	ctx := context.Background()

	// Seed a project and link f.aFulfilled and f.aInvoice to it so job summaries have data.
	projID := uuid.New()
	_, err := f.db.Pool.Exec(ctx, `
		INSERT INTO projects (id, customer_id, name, status)
		VALUES ($1, $2, 'Harbor Pier', 'ACTIVE')`, projID, f.aCustomer)
	if err != nil {
		t.Fatalf("seed project: %v", err)
	}
	_, _ = f.db.Pool.Exec(ctx, `UPDATE orders SET project_id = $1 WHERE id = $2`, projID, f.aFulfilled)
	_, _ = f.db.Pool.Exec(ctx, `UPDATE invoices SET project_id = $1, amount_open = 100.00, invoice_date = CURRENT_DATE - interval '10 days' WHERE id = $2`, projID, f.aInvoice)

	// 1. Roles without see_money (estimator, site_super) get 403 across all Part 5 endpoints.
	moneyPaths := []string{
		"/api/portal/v1/billing/summary",
		"/api/portal/v1/statement",
		"/api/portal/v1/statements",
		"/api/portal/v1/invoices/" + f.aInvoice.String() + "/pdf",
		"/api/portal/v1/statement/pdf",
	}
	for _, role := range []string{RoleEstimator, RoleSiteSuper} {
		mux, _ := muxForPhase2(f, f.aCustomer, f.users[role], role, nil)
		for _, p := range moneyPaths {
			rec, _ := call(mux, http.MethodGet, p, "", nil)
			if rec.Code != http.StatusForbidden {
				t.Fatalf("role %s GET %s status = %d, want 403", role, p, rec.Code)
			}
		}
	}

	// 2. Roles with see_money (owner, project_manager, ap_clerk) get 200.
	apMux, _ := muxForPhase2(f, f.aCustomer, f.users[RoleAPClerk], RoleAPClerk, nil)

	rec, body := call(apMux, http.MethodGet, "/api/portal/v1/billing/summary", "", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("billing/summary status = %d, want 200: %s", rec.Code, rec.Body.String())
	}
	if body["balance_tiles"] == nil || body["aging"] == nil || body["job_summaries"] == nil {
		t.Errorf("billing/summary missing expected sections: %v", body)
	}
	jobs, _ := body["job_summaries"].([]any)
	if len(jobs) == 0 {
		t.Errorf("expected at least 1 job summary for Harbor Pier, got 0")
	}

	rec, body = call(apMux, http.MethodGet, "/api/portal/v1/statement", "", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("statement status = %d, want 200: %s", rec.Code, rec.Body.String())
	}
	if body["aging"] == nil || body["open_invoices"] == nil {
		t.Errorf("statement missing aging or open_invoices: %v", body)
	}

	rec, _ = call(apMux, http.MethodGet, "/api/portal/v1/statements", "", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("statements list status = %d, want 200: %s", rec.Code, rec.Body.String())
	}

	// 3. Invoice PDF and Statement PDF render valid %PDF- documents.
	rec, _ = call(apMux, http.MethodGet, "/api/portal/v1/invoices/"+f.aInvoice.String()+"/pdf", "", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("invoice PDF status = %d, want 200: %s", rec.Code, rec.Body.String())
	}
	if !strings.HasPrefix(rec.Body.String(), "%PDF-") {
		t.Errorf("invoice PDF did not start with %%PDF- header (got %d bytes)", rec.Body.Len())
	}

	rec, _ = call(apMux, http.MethodGet, "/api/portal/v1/statement/pdf", "", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("statement PDF status = %d, want 200: %s", rec.Code, rec.Body.String())
	}
	if !strings.HasPrefix(rec.Body.String(), "%PDF-") {
		t.Errorf("statement PDF did not start with %%PDF- header (got %d bytes)", rec.Body.Len())
	}

	// 4. Tenancy: Customer B gets 404 on Customer A's invoice PDF.
	bMux, _ := muxForPhase2(f, f.bCustomer, uuid.New(), RoleOwner, nil)
	rec, _ = call(bMux, http.MethodGet, "/api/portal/v1/invoices/"+f.aInvoice.String()+"/pdf", "", nil)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("other tenant invoice PDF status = %d, want 404", rec.Code)
	}
}

// TestPhase2_Part6_PaymentMethodsRawDigitGuardAndSingleFlightPay tests saved payment
// methods, raw card/bank digit refusal (red-first security requirement), multi-invoice
// payment through the stub PaymentGateway + AR core, allocation history, and the
// durable single-flight idempotency guard.
func TestPhase2_Part6_PaymentMethodsRawDigitGuardAndSingleFlightPay(t *testing.T) {
	f := setupPro(t)
	ctx := context.Background()

	// Ensure f.aInvoice has amount_open = 100.00 (10000 cents) and create a second open invoice for 55.00 (5500 cents).
	inv2 := uuid.New()
	_, err := f.db.Pool.Exec(ctx, `
		UPDATE invoices SET amount_open = 100.00, total_amount = 100.00, status = 'UNPAID', invoice_date = CURRENT_DATE - interval '5 days'
		WHERE id = $1`, f.aInvoice)
	if err != nil {
		t.Fatalf("update invoice 1: %v", err)
	}
	_, err = f.db.Pool.Exec(ctx, `
		INSERT INTO invoices (id, customer_id, order_id, status, total_amount, amount_open, subtotal, tax_amount, invoice_date, due_date, created_at, updated_at, branch_id, currency)
		VALUES ($1, $2, $3, 'UNPAID', 55.00, 55.00, 50.00, 5.00, CURRENT_DATE - interval '2 days', CURRENT_DATE + interval '28 days', NOW(), NOW(), $4, 'USD')`,
		inv2, f.aCustomer, f.aFulfilled, f.aYard)
	if err != nil {
		t.Fatalf("insert invoice 2: %v", err)
	}

	// 1. Capability gate: estimator, site_super, project_manager lack CapPay -> 403.
	for _, role := range []string{RoleEstimator, RoleSiteSuper, RoleProjectManager} {
		mux, _ := muxForPhase2(f, f.aCustomer, f.users[role], role, nil)
		rec, _ := call(mux, http.MethodPost, "/api/portal/v1/payment-methods",
			`{"gateway_token":"tok_visa_4242","method_type":"CARD","brand":"VISA","last4":"4242"}`, nil)
		if rec.Code != http.StatusForbidden {
			t.Fatalf("role %s save payment method status = %d, want 403", role, rec.Code)
		}
		rec, _ = call(mux, http.MethodPost, "/api/portal/v1/payments",
			fmt.Sprintf(`{"invoice_ids":[%q],"token":"tok_visa_4242"}`, f.aInvoice.String()), nil)
		if rec.Code != http.StatusForbidden {
			t.Fatalf("role %s pay invoices status = %d, want 403", role, rec.Code)
		}
	}

	apMux, _ := muxForPhase2(f, f.aCustomer, f.users[RoleAPClerk], RoleAPClerk, nil)

	// 2. RED-FIRST SECURITY GUARD: Raw card PAN or bank account/routing numbers are refused with 400
	// and never stored in portal_payment_methods.
	rawPayloads := []string{
		`{"gateway_token":"4111111111111111","method_type":"CARD","brand":"VISA","last4":"1111"}`,
		`{"gateway_token":"4111-1111-1111-1111","method_type":"CARD","brand":"VISA","last4":"1111"}`,
		`{"gateway_token":"tok_ok","card_number":"4111111111111111","method_type":"CARD","last4":"1111"}`,
		`{"gateway_token":"tok_ok","pan":"4111111111111111","method_type":"CARD","last4":"1111"}`,
		`{"gateway_token":"tok_ok","account_number":"000123456789","method_type":"BANK","last4":"6789"}`,
		`{"gateway_token":"tok_ok","routing_number":"110000000","method_type":"BANK","last4":"6789"}`,
		`{"gateway_token":"000123456789","method_type":"BANK","last4":"6789"}`,
	}
	for _, payload := range rawPayloads {
		rec, _ := call(apMux, http.MethodPost, "/api/portal/v1/payment-methods", payload, nil)
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("raw card/bank payload %s got status %d, want 400", payload, rec.Code)
		}
	}
	var count int
	if err := f.db.Pool.QueryRow(ctx, `SELECT count(*) FROM portal_payment_methods WHERE customer_id = $1`, f.aCustomer).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatalf("expected 0 saved payment methods after raw digit refusals, got %d", count)
	}

	// Raw digits in POST /api/portal/v1/payments are also refused with 400.
	rec, _ := call(apMux, http.MethodPost, "/api/portal/v1/payments",
		fmt.Sprintf(`{"invoice_ids":[%q],"token":"4111111111111111"}`, f.aInvoice.String()), nil)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("raw PAN in payment token got status %d, want 400", rec.Code)
	}

	// 3. Save a tokenized payment method and verify gateway_token is never returned on wire.
	rec, body := call(apMux, http.MethodPost, "/api/portal/v1/payment-methods",
		`{"gateway_token":"tok_visa_4242","method_type":"CARD","brand":"VISA","last4":"4242","exp_month":12,"exp_year":2029,"is_default":true}`, nil)
	if rec.Code != http.StatusCreated {
		t.Fatalf("save tokenized payment method status = %d, want 201: %s", rec.Code, rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), "tok_visa_4242") || strings.Contains(rec.Body.String(), "gateway_token") {
		t.Fatalf("saved payment method response leaked gateway_token: %s", rec.Body.String())
	}
	pmID := fmt.Sprint(body["id"])

	// 4. Single-flight / concurrent retry guard on multi-invoice payment:
	// Fire two concurrent identical payment requests for [f.aInvoice, inv2] (total 15500 cents).
	cgw := &countingGateway{
		inner:      NewStubPaymentGateway(),
		chargeWait: 60 * time.Millisecond,
	}
	raceMux, _ := muxForPhase2(f, f.aCustomer, f.users[RoleAPClerk], RoleAPClerk, cgw)
	payBody := fmt.Sprintf(`{"invoice_ids":[%q,%q],"payment_method_id":%q}`, f.aInvoice.String(), inv2.String(), pmID)

	var wg sync.WaitGroup
	codes := make([]int, 2)
	bodies := make([]string, 2)
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			r, _ := call(raceMux, http.MethodPost, "/api/portal/v1/payments", payBody, nil)
			codes[idx] = r.Code
			bodies[idx] = r.Body.String()
		}(i)
	}
	wg.Wait()

	if gotCharges := cgw.charges.Load(); gotCharges != 1 {
		t.Fatalf("concurrent identical payments called gateway.Charge %d times, want 1 (codes=%v bodies=%v)", gotCharges, codes, bodies)
	}
	// Exactly one 201 Created and one 409 Conflict (in-flight) or 200 OK (settled replay).
	okCount, conflictCount := 0, 0
	for _, c := range codes {
		switch c {
		case http.StatusCreated, http.StatusOK:
			okCount++
		case http.StatusConflict:
			conflictCount++
		default:
			t.Errorf("unexpected status code in concurrent payment race: %d (bodies=%v)", c, bodies)
		}
	}
	if okCount == 0 {
		t.Fatalf("expected at least one successful payment in race, got codes=%v bodies=%v", codes, bodies)
	}

	// A replay after settlement returns the settled payment without calling Charge again.
	rec, body = call(raceMux, http.MethodPost, "/api/portal/v1/payments", payBody, nil)
	if rec.Code != http.StatusOK && rec.Code != http.StatusCreated {
		t.Fatalf("post-settlement replay status = %d, want 200/201: %s", rec.Code, rec.Body.String())
	}
	if gotCharges := cgw.charges.Load(); gotCharges != 1 {
		t.Fatalf("post-settlement replay called gateway.Charge (%d total), want 1", gotCharges)
	}
	if body["amount_cents"] != float64(15500) {
		t.Errorf("payment amount_cents = %v, want 15500", body["amount_cents"])
	}

	// 5. Unknown outcome guard: if gateway.Charge returns a transport error on a new invoice,
	// the intent stays CHARGING and a retry is refused with 409 rather than double-charging.
	inv3 := uuid.New()
	_, err = f.db.Pool.Exec(ctx, `
		INSERT INTO invoices (id, customer_id, order_id, status, total_amount, amount_open, subtotal, tax_amount, invoice_date, due_date, created_at, updated_at, branch_id, currency)
		VALUES ($1, $2, $3, 'UNPAID', 40.00, 40.00, 40.00, 0.00, CURRENT_DATE, CURRENT_DATE + interval '30 days', NOW(), NOW(), $4, 'USD')`,
		inv3, f.aCustomer, f.aFulfilled, f.aYard)
	if err != nil {
		t.Fatalf("insert invoice 3: %v", err)
	}
	errGw := &countingGateway{
		inner:     NewStubPaymentGateway(),
		chargeErr: errors.New("unexpected EOF from gateway"),
	}
	errMux, _ := muxForPhase2(f, f.aCustomer, f.users[RoleAPClerk], RoleAPClerk, errGw)
	inv3Body := fmt.Sprintf(`{"invoice_ids":[%q],"payment_method_id":%q}`, inv3.String(), pmID)

	rec, _ = call(errMux, http.MethodPost, "/api/portal/v1/payments", inv3Body, nil)
	if rec.Code != http.StatusBadGateway {
		t.Fatalf("unknown gateway error status = %d, want 502: %s", rec.Code, rec.Body.String())
	}
	// Retry after unknown outcome is refused with 409 and does NOT call gateway.Charge again!
	rec, _ = call(errMux, http.MethodPost, "/api/portal/v1/payments", inv3Body, nil)
	if rec.Code != http.StatusConflict {
		t.Fatalf("retry after unknown outcome status = %d, want 409: %s", rec.Code, rec.Body.String())
	}
	if got := errGw.charges.Load(); got != 1 {
		t.Fatalf("retry after unknown outcome called gateway.Charge %d times, want 1", got)
	}

	// 6. Allocation history (GET /api/portal/v1/payments) returns the payment and its 2 allocations
	// with zero staff attribution fields.
	rec, _ = call(apMux, http.MethodGet, "/api/portal/v1/payments", "", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /api/portal/v1/payments status = %d, want 200: %s", rec.Code, rec.Body.String())
	}
	var list []map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &list); err != nil {
		t.Fatalf("unmarshal payments list: %v", err)
	}
	if len(list) == 0 {
		t.Fatalf("expected at least 1 payment in history, got 0")
	}
	allocs, _ := list[0]["allocations"].([]any)
	if len(allocs) != 2 {
		t.Fatalf("expected 2 allocations on multi-invoice payment, got %d (%v)", len(allocs), list[0])
	}
	if strings.Contains(rec.Body.String(), "applied_by") || strings.Contains(rec.Body.String(), "reversed_by") {
		t.Errorf("payment allocation history leaked internal staff attribution: %s", rec.Body.String())
	}
}
