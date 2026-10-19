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
	if gw == nil {
		// The service has no default gateway any more; the Phase 2 suite
		// exercises the stub flow, so a nil gateway here means the stub.
		gw = NewStubPaymentGateway()
	}
	svc.WithPaymentGateway(gw)

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

	// 3. Photo intake without AI key returns synthetic=true; create_quote=true without allow_synthetic refuses 409 with SYNTHETIC_INTAKE_REFUSED.
	fakeImg := base64.StdEncoding.EncodeToString([]byte("fake-jpeg-bytes"))
	rec, errBody := call(estMux, http.MethodPost, "/api/portal/v1/quotes/intake",
		fmt.Sprintf(`{"file_base64":%q,"content_type":"image/jpeg","create_quote":true}`, fakeImg), nil)
	if rec.Code != http.StatusConflict {
		t.Fatalf("unconfirmed synthetic photo intake with create_quote=true status = %d, want 409: %s", rec.Code, rec.Body.String())
	}
	errObj, _ := errBody["error"].(map[string]any)
	if errObj == nil || errObj["code"] != "SYNTHETIC_INTAKE_REFUSED" {
		t.Errorf("unconfirmed synthetic intake error.code = %v, want SYNTHETIC_INTAKE_REFUSED (full=%s)", errObj, rec.Body.String())
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
	// First add a line and a project to f.aQuote so ConvertInProcess can build an order from it and preserve project_id.
	quoteProjID := uuid.New()
	if _, err := f.db.Pool.Exec(ctx, `
		INSERT INTO projects (id, customer_id, name, status)
		VALUES ($1, $2, 'Quote Convert Job', 'ACTIVE')`, quoteProjID, f.aCustomer); err != nil {
		t.Fatalf("seed quote project: %v", err)
	}
	if _, err := f.db.Pool.Exec(ctx, `UPDATE quotes SET project_id = $1 WHERE id = $2`, quoteProjID, f.aQuote); err != nil {
		t.Fatalf("attach project to quote: %v", err)
	}
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
	bMux, _ := muxForPhase2(f, f.bCustomer, f.bUser, RoleOwner, nil)
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
	convertedOrderID, _ := uuid.Parse(fmt.Sprint(body["order_id"]))
	var convertedProjID *uuid.UUID
	if err := f.db.Pool.QueryRow(ctx, `SELECT project_id FROM orders WHERE id = $1`, convertedOrderID).Scan(&convertedProjID); err != nil {
		t.Fatalf("query converted order project_id: %v", err)
	}
	if convertedProjID == nil || *convertedProjID != quoteProjID {
		t.Errorf("converted order project_id = %v, want %s", convertedProjID, quoteProjID)
	}
}

// TestPhase2_Part3_CatalogYardStockReorderPreviewAndProcurement tests per-yard stock
// and shortest lead time on catalog products, reorder price-difference preview,
// customer-priced reorder with project tagging, and named material lists (procurement board).
func TestPhase2_Part3_CatalogYardStockReorderPreviewAndProcurement(t *testing.T) {
	f := setupPro(t)
	ctx := context.Background()

	// Seed inventory at aYard (available 15) while lead_time_days is still NULL and yard_schedule_windows is empty (P2-4).
	_, err := f.db.Pool.Exec(ctx, `
		INSERT INTO inventory (id, product_id, location_id, location, quantity, allocated)
		VALUES ($1, $2, $3, 'YARD-A', 20, 5)`, uuid.New(), f.product, f.aYard)
	if err != nil {
		t.Fatalf("insert inventory: %v", err)
	}

	ownerMux, _ := muxForPhase2(f, f.aCustomer, f.users[RoleOwner], RoleOwner, nil)

	// P2-4: Even when lead_time_days IS NULL and yard_schedule_windows is empty, per-yard availability is returned.
	rec, body := call(ownerMux, http.MethodGet, "/api/portal/v1/catalog/"+f.product.String(), "", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("get catalog product (null lead_time_days) status = %d, want 200: %s", rec.Code, rec.Body.String())
	}
	if availNullLT, _ := body["availability"].([]any); len(availNullLT) == 0 {
		t.Errorf("expected per-yard availability even when lead_time_days IS NULL and yard_schedule_windows is empty, got %v", body["availability"])
	}

	// Now set lead_time_days=4 on product.
	_, err = f.db.Pool.Exec(ctx, `UPDATE products SET lead_time_days = 4 WHERE id = $1`, f.product)
	if err != nil {
		t.Fatalf("update product lead_time_days: %v", err)
	}

	// Seed a customer-specific contract price for f.aCustomer on f.product: $21.50 (2150 cents) vs $25.00 base.
	_, err = f.db.Pool.Exec(ctx, `
		INSERT INTO customer_contracts (id, customer_id, product_id, contract_price, price_uom, created_at, updated_at)
		VALUES ($1, $2, $3, 21.50, 'EA', NOW(), NOW())`,
		uuid.New(), f.aCustomer, f.product)
	if err != nil {
		t.Fatalf("insert customer_contract: %v", err)
	}

	// 1. Catalog product detail includes per-yard availability and customer price.
	rec, body = call(ownerMux, http.MethodGet, "/api/portal/v1/catalog/"+f.product.String(), "", nil)
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

	// 3. Named material lists (procurement board - P1-5 contract alignment).
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

	// Add a line to the material list: returns 201 PortalProcurementProject with lines.
	rec, body = call(ownerMux, http.MethodPost, "/api/portal/v1/procurement/projects/"+procID+"/lines",
		fmt.Sprintf(`{"product_id":%q,"description":"Pro fixture board","qty_entered":10,"uom":"EA","notes":"Bundle A"}`, f.product.String()), nil)
	if rec.Code != http.StatusCreated {
		t.Fatalf("add procurement line status = %d, want 201: %s", rec.Code, rec.Body.String())
	}
	addedLines, _ := body["lines"].([]any)
	if len(addedLines) != 1 {
		t.Fatalf("add procurement line expected PortalProcurementProject with 1 line, got %v", body)
	}
	firstLine, _ := addedLines[0].(map[string]any)
	lineID := fmt.Sprint(firstLine["id"])

	// Update the line quantity to 20: returns 200 PortalProcurementProject.
	rec, body = call(ownerMux, http.MethodPut, "/api/portal/v1/procurement/projects/"+procID+"/lines/"+lineID,
		fmt.Sprintf(`{"product_id":%q,"description":"Pro fixture board","qty_entered":20,"uom":"EA","notes":"Bundle A updated"}`, f.product.String()), nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("update procurement line status = %d, want 200: %s", rec.Code, rec.Body.String())
	}
	updatedLines, _ := body["lines"].([]any)
	if len(updatedLines) != 1 {
		t.Fatalf("update procurement line expected PortalProcurementProject with 1 line, got %v", body)
	}

	// Add a second line and delete it: DELETE returns 200 PortalProcurementProject with 1 remaining line.
	rec, body = call(ownerMux, http.MethodPost, "/api/portal/v1/procurement/projects/"+procID+"/lines",
		`{"description":"Temp Extra","qty_entered":5,"uom":"EA"}`, nil)
	if rec.Code != http.StatusCreated {
		t.Fatalf("add second procurement line status = %d, want 201: %s", rec.Code, rec.Body.String())
	}
	twoLines, _ := body["lines"].([]any)
	if len(twoLines) != 2 {
		t.Fatalf("expected 2 lines after second add, got %d", len(twoLines))
	}
	secondLine, _ := twoLines[1].(map[string]any)
	line2ID := fmt.Sprint(secondLine["id"])

	rec, body = call(ownerMux, http.MethodDelete, "/api/portal/v1/procurement/projects/"+procID+"/lines/"+line2ID, "", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("delete procurement line status = %d, want 200: %s", rec.Code, rec.Body.String())
	}
	afterDelLines, _ := body["lines"].([]any)
	if len(afterDelLines) != 1 {
		t.Fatalf("expected 1 line after delete, got %d", len(afterDelLines))
	}

	// Submit RFQ with request body {project_id, notes, delivery_type}: returns 201 Created,
	// prices indicatively at 2150 cents/unit (43000 cents line total), creates quote, transitions to QUOTING.
	rec, body = call(ownerMux, http.MethodPost, "/api/portal/v1/procurement/projects/"+procID+"/submit-rfq",
		fmt.Sprintf(`{"project_id":%q,"notes":"Rush framing","delivery_type":"DELIVERY"}`, projectID.String()), nil)
	if rec.Code != http.StatusCreated {
		t.Fatalf("submit-rfq status = %d, want 201: %s", rec.Code, rec.Body.String())
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
	rfqQuoteID, _ := uuid.Parse(fmt.Sprint(body["quote_id"]))
	var qNotes, qDeliv string
	var qProj *uuid.UUID
	if err := f.db.Pool.QueryRow(ctx, `SELECT COALESCE(customer_notes, ''), COALESCE(delivery_type, ''), project_id FROM quotes WHERE id = $1`, rfqQuoteID).Scan(&qNotes, &qDeliv, &qProj); err != nil {
		t.Fatalf("query rfq quote: %v", err)
	}
	if qNotes != "Rush framing" || qDeliv != "DELIVERY" || qProj == nil || *qProj != projectID {
		t.Errorf("rfq quote notes=%q delivery_type=%q project_id=%v, want Rush framing / DELIVERY / %s", qNotes, qDeliv, qProj, projectID)
	}

	// Editing a line after QUOTING is refused with 409.
	rec, _ = call(ownerMux, http.MethodPost, "/api/portal/v1/procurement/projects/"+procID+"/lines",
		`{"description":"Extra","qty_entered":1,"uom":"EA"}`, nil)
	if rec.Code != http.StatusConflict {
		t.Fatalf("add line in QUOTING status = %d, want 409", rec.Code)
	}

	// Other tenant gets 404.
	bMux, _ := muxForPhase2(f, f.bCustomer, f.bUser, RoleOwner, nil)
	rec, _ = call(bMux, http.MethodGet, "/api/portal/v1/procurement/projects/"+procID, "", nil)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("other tenant get procurement project status = %d, want 404", rec.Code)
	}
}

// TestPhase2_P1_2_BranchPricingAndSaleUOMQuantityBreak verifies that:
//  1. Catalog, Cart, Procurement, ReorderPreview, and CreateReorder pass the
//     contractor's BranchID (or the source order's branch_id) to pricing.Resolve
//     so branch-scoped pricing is honored.
//  2. ReorderPreview and CreateReorder pass the line's sale UOM (rl.uom) and
//     sale quantity (rl.qty) rather than price_uom, so quantity breaks are
//     evaluated against the true stocking quantity when uom != price_uom.
func TestPhase2_P1_2_BranchPricingAndSaleUOMQuantityBreak(t *testing.T) {
	f := setupPro(t)
	ctx := context.Background()

	// Create a cross-unit product: stocked & sold in EA, priced in MBF (1000 EA = 1 MBF),
	// all-branch base_price = $500.00 / MBF ($0.50 / EA).
	prodID := uuid.New()
	sku := "BR-MBF-" + prodID.String()[:8]
	if _, err := f.db.Pool.Exec(ctx, `
		INSERT INTO products (id, sku, description, uom_primary, sale_uom, price_uom, purchase_uom, base_price, average_unit_cost)
		VALUES ($1, $2, 'Branch MBF Stud', 'EA', 'EA', 'EA', 'EA', 500.00, 0.25)`, prodID, sku); err != nil {
		t.Fatalf("insert cross-unit product: %v", err)
	}
	if _, err := f.db.Pool.Exec(ctx, `
		INSERT INTO product_units (product_id, uom, unit_qty, stock_qty, sell, purchase, price)
		VALUES ($1, 'MBF', 1, 1000, FALSE, TRUE, TRUE)
		ON CONFLICT (product_id, uom) DO NOTHING`, prodID); err != nil {
		t.Fatalf("insert product_units MBF: %v", err)
	}
	if _, err := f.db.Pool.Exec(ctx, `SELECT set_config('gable.unit_set_write', 'on', true)`); err != nil {
		t.Fatal(err)
	}
	if _, err := f.db.Pool.Exec(ctx, `UPDATE products SET price_uom = 'MBF' WHERE id = $1`, prodID); err != nil {
		t.Fatalf("update product price_uom: %v", err)
	}

	// Set f.aCustomer's primary_branch_id to f.aYard and seed a branch-specific base price on product_prices for f.aYard: $400.00 / MBF ($0.40 / EA).
	if _, err := f.db.Pool.Exec(ctx, `UPDATE customers SET primary_branch_id = $1 WHERE id = $2`, f.aYard, f.aCustomer); err != nil {
		t.Fatalf("update primary_branch_id: %v", err)
	}
	if _, err := f.db.Pool.Exec(ctx, `
		INSERT INTO product_prices (id, product_id, branch_id, price_level_id, unit_price, price_uom)
		VALUES ($1, $2, $3, NULL, 400.00, 'MBF')`, uuid.New(), prodID, f.aYard); err != nil {
		t.Fatalf("insert branch product_price: %v", err)
	}

	// Seed a quantity-break pricing rule at min_quantity = 1000 (stocking unit EA): $300.00 / MBF ($0.30 / EA).
	if _, err := f.db.Pool.Exec(ctx, `
		INSERT INTO pricing_rules (id, name, rule_type, product_id, customer_id, min_quantity, fixed_price, price_uom, is_active, priority)
		VALUES ($1, '1000 EA Bulk Break', 'QUANTITY_BREAK', $2, $3, 1000, 300.00, 'MBF', TRUE, 10)`,
		uuid.New(), prodID, f.aCustomer); err != nil {
		t.Fatalf("insert quantity_break rule: %v", err)
	}

	t.Cleanup(func() {
		_, _ = f.db.Pool.Exec(ctx, `DELETE FROM portal_cart_items WHERE product_id = $1`, prodID)
		_, _ = f.db.Pool.Exec(ctx, `DELETE FROM order_lines WHERE product_id = $1`, prodID)
		_, _ = f.db.Pool.Exec(ctx, `DELETE FROM pricing_rules WHERE product_id = $1`, prodID)
		_, _ = f.db.Pool.Exec(ctx, `DELETE FROM product_prices WHERE product_id = $1`, prodID)
		_, _ = f.db.Pool.Exec(ctx, `DELETE FROM products WHERE id = $1`, prodID)
	})

	ownerMux, _ := muxForPhase2(f, f.aCustomer, f.users[RoleOwner], RoleOwner, nil)

	// 1. Catalog product detail at qty 1 EA uses f.aCustomer's PrimaryBranchID (f.aYard) -> $0.40 / EA (derived from branch reference price $400/MBF, vs $0.50/EA without BranchID).
	rec, body := call(ownerMux, http.MethodGet, "/api/portal/v1/catalog/"+prodID.String(), "", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("catalog detail status = %d, want 200: %s", rec.Code, rec.Body.String())
	}
	if gotPrice, _ := body["customer_price"].(float64); gotPrice != 0.40 {
		t.Errorf("catalog customer_price = %v, want 0.40 (branch price $400/MBF / 1000 EA)", body["customer_price"])
	}

	// 2. AddToCart with 500 EA (< 1000 EA break) uses branch price $400.00 / MBF ($200.00 line total).
	rec, body = call(ownerMux, http.MethodPost, "/api/portal/v1/cart/items",
		fmt.Sprintf(`{"product_id":%q,"quantity":500}`, prodID.String()), nil)
	if rec.Code != http.StatusCreated {
		t.Fatalf("add to cart status = %d, want 201: %s", rec.Code, rec.Body.String())
	}
	cItems, _ := body["items"].([]any)
	if len(cItems) != 1 {
		t.Fatalf("cart items len = %d, want 1", len(cItems))
	}
	ci0, _ := cItems[0].(map[string]any)
	if ci0["unit_price"] != float64(400) || ci0["line_total"] != float64(200) {
		t.Errorf("cart item pricing for 500 EA = unit_price:%v line_total:%v, want 400 / 200", ci0["unit_price"], ci0["line_total"])
	}

	// 3. Reorder preview and execution on an order with 500 EA (below 1000 EA break) vs 1200 EA (at/above 1000 EA break).
	srcOrder := uuid.New()
	if _, err := f.db.Pool.Exec(ctx, `
		INSERT INTO orders (id, customer_id, status, currency, total_amount, subtotal, tax_amount, branch_id)
		VALUES ($1, $2, 'FULFILLED', 'USD', 850.00, 850.00, 0.00, $3)`,
		srcOrder, f.aCustomer, f.aYard); err != nil {
		t.Fatalf("insert source order: %v", err)
	}
	// Line 1: 500 EA priced per MBF (1000 EA = 1 MBF), historical $500/MBF ($250 total).
	// Current price must be $400/MBF (40000 cents, $200 / 20000 cents total) because 500 EA < 1000 EA!
	if _, err := f.db.Pool.Exec(ctx, `
		INSERT INTO order_lines (id, order_id, position, line_type, product_id, sku, description, quantity, uom, price_uom, uom_qty, price_uom_qty, stock_uom, stock_quantity, unit_price, priced_unit_price, price_source, line_total)
		VALUES ($1, $2, 0, 'PRODUCT', $3, $4, 'Branch MBF Stud', 500, 'EA', 'MBF', 1000, 1, 'EA', 500, 500.00, 500.00, 'PRICE_LIST', 250.00)`,
		uuid.New(), srcOrder, prodID, sku); err != nil {
		t.Fatalf("insert source line 1 (500 EA): %v", err)
	}
	// Line 2: 1200 EA priced per MBF (1000 EA = 1 MBF), historical $500/MBF ($600 total).
	// Current price must be $300/MBF (30000 cents, $360 / 36000 cents total) because 1200 EA >= 1000 EA!
	if _, err := f.db.Pool.Exec(ctx, `
		INSERT INTO order_lines (id, order_id, position, line_type, product_id, sku, description, quantity, uom, price_uom, uom_qty, price_uom_qty, stock_uom, stock_quantity, unit_price, priced_unit_price, price_source, line_total)
		VALUES ($1, $2, 1, 'PRODUCT', $3, $4, 'Branch MBF Stud', 1200, 'EA', 'MBF', 1000, 1, 'EA', 1200, 500.00, 500.00, 'PRICE_LIST', 600.00)`,
		uuid.New(), srcOrder, prodID, sku); err != nil {
		t.Fatalf("insert source line 2 (1200 EA): %v", err)
	}

	rec, body = call(ownerMux, http.MethodGet, "/api/portal/v1/orders/"+srcOrder.String()+"/reorder-preview", "", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("reorder-preview status = %d, want 200: %s", rec.Code, rec.Body.String())
	}
	rLines, _ := body["lines"].([]any)
	if len(rLines) != 2 {
		t.Fatalf("reorder-preview lines = %d, want 2", len(rLines))
	}
	rl0, _ := rLines[0].(map[string]any)
	rl1, _ := rLines[1].(map[string]any)
	if rl0["current_unit_price_cents"] != float64(40000) || rl0["current_line_total_cents"] != float64(20000) {
		t.Errorf("500 EA line reorder-preview = %v, want current_unit_price_cents=40000 current_line_total_cents=20000 (not 1000-EA break)", rl0)
	}
	if rl1["current_unit_price_cents"] != float64(30000) || rl1["current_line_total_cents"] != float64(36000) {
		t.Errorf("1200 EA line reorder-preview = %v, want current_unit_price_cents=30000 current_line_total_cents=36000 (1000-EA break)", rl1)
	}

	// Execute reorder: total must be $200.00 + $360.00 = $560.00.
	rec, body = call(ownerMux, http.MethodPost, "/api/portal/v1/orders/reorder",
		fmt.Sprintf(`{"order_id":%q}`, srcOrder.String()), nil)
	if rec.Code != http.StatusCreated {
		t.Fatalf("reorder status = %d, want 201: %s", rec.Code, rec.Body.String())
	}
	reorderedID, _ := uuid.Parse(fmt.Sprint(body["order_id"]))
	t.Cleanup(func() {
		_, _ = f.db.Pool.Exec(ctx, `DELETE FROM order_lines WHERE order_id IN ($1, $2)`, srcOrder, reorderedID)
		_, _ = f.db.Pool.Exec(ctx, `DELETE FROM orders WHERE id IN ($1, $2)`, srcOrder, reorderedID)
	})
	var reorderedTotal float64
	if err := f.db.Pool.QueryRow(ctx, `SELECT total_amount::float8 FROM orders WHERE id = $1`, reorderedID).Scan(&reorderedTotal); err != nil {
		t.Fatalf("query reordered total: %v", err)
	}
	if reorderedTotal != 560.00 {
		t.Errorf("reordered total_amount = %.2f, want 560.00 (200.00 + 360.00)", reorderedTotal)
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

	// Seed an unapplied payment of $15.00 (1500 cents) and a historical statement run item (P2-3).
	unappliedPayID := uuid.New()
	_, err = f.db.Pool.Exec(ctx, `
		INSERT INTO payments (id, number, customer_id, branch_id, currency, method, status, amount, amount_unapplied, received_on, created_at, updated_at)
		VALUES ($1, $2, $3, $4, 'USD', 'ACH', 'POSTED', 15.00, 15.00, CURRENT_DATE - interval '3 days', NOW(), NOW())`,
		unappliedPayID, "PM-UNAPP-"+unappliedPayID.String()[:6], f.aCustomer, f.aYard)
	if err != nil {
		t.Fatalf("seed unapplied payment: %v", err)
	}
	runID := uuid.New()
	runItemID := uuid.New()
	_, err = f.db.Pool.Exec(ctx, `
		INSERT INTO ar_statement_runs (id, period_start, period_end, as_of_date, status, total_customers, queued_count, sent_count, total_balance_cents, created_at, completed_at)
		VALUES ($1, CURRENT_DATE - interval '30 days', CURRENT_DATE, CURRENT_DATE, 'COMPLETED', 1, 0, 0, 10000, NOW(), NOW())`, runID)
	if err != nil {
		t.Fatalf("seed ar_statement_run: %v", err)
	}
	_, err = f.db.Pool.Exec(ctx, `
		INSERT INTO ar_statement_run_items (id, run_id, customer_id, currency, opening_balance_cents, invoices_cents, payments_cents, finance_charges_cents, closing_balance_cents, delivery_status, aging_buckets)
		VALUES ($1, $2, $3, 'USD', 2000, 9000, 1500, 500, 10000, 'SKIPPED', '{"current_cents":10000,"days_1_30_cents":0,"days_31_60_cents":0,"days_61_90_cents":0,"over_90_cents":0}'::jsonb)`,
		runItemID, runID, f.aCustomer)
	if err != nil {
		t.Fatalf("seed ar_statement_run_item: %v", err)
	}
	t.Cleanup(func() {
		_, _ = f.db.Pool.Exec(ctx, `DELETE FROM ar_statement_run_items WHERE id = $1`, runItemID)
		_, _ = f.db.Pool.Exec(ctx, `DELETE FROM ar_statement_runs WHERE id = $1`, runID)
		_, _ = f.db.Pool.Exec(ctx, `DELETE FROM payments WHERE id = $1`, unappliedPayID)
	})

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
	stAging, _ := body["aging"].(map[string]any)
	if body["unapplied_cents"] != float64(1500) || stAging["unapplied_cents"] != float64(1500) {
		t.Errorf("statement unapplied_cents=%v aging.unapplied_cents=%v, want both 1500", body["unapplied_cents"], stAging["unapplied_cents"])
	}

	// P2-3: ?to=2025-01-15 without from defaults from to 30 days prior (2024-12-16); from > to returns 400.
	rec, body = call(apMux, http.MethodGet, "/api/portal/v1/statement?to=2025-01-15", "", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("statement with past ?to only status = %d, want 200: %s", rec.Code, rec.Body.String())
	}
	if body["from"] != "2024-12-16" || body["to"] != "2025-01-15" {
		t.Errorf("statement with ?to=2025-01-15 got from=%v to=%v, want 2024-12-16..2025-01-15", body["from"], body["to"])
	}
	rec, _ = call(apMux, http.MethodGet, "/api/portal/v1/statement?from=2025-02-01&to=2025-01-15", "", nil)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("statement with from > to status = %d, want 400", rec.Code)
	}

	rec, _ = call(apMux, http.MethodGet, "/api/portal/v1/statements", "", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("statements list status = %d, want 200: %s", rec.Code, rec.Body.String())
	}
	var stRuns []map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &stRuns); err != nil || len(stRuns) == 0 {
		t.Fatalf("expected non-empty statements list, got err=%v body=%s", err, rec.Body.String())
	}
	sr0 := stRuns[0]
	if sr0["invoices_cents"] != float64(9000) || sr0["payments_cents"] != float64(1500) || sr0["finance_charges_cents"] != float64(500) {
		t.Errorf("statement run item period figures = %v, want invoices=9000 payments=1500 finance_charges=500", sr0)
	}
	if sr0["unapplied_cents"] != float64(1500) || sr0["net_balance_cents"] != float64(8500) {
		t.Errorf("statement run item unapplied=%v net=%v, want 1500 and 8500", sr0["unapplied_cents"], sr0["net_balance_cents"])
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
	bMux, _ := muxForPhase2(f, f.bCustomer, f.bUser, RoleOwner, nil)
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

	// 3. Save tokenized CARD and ACH payment methods (P1-3) and verify gateway_token is never returned on wire.
	rec, body := call(apMux, http.MethodPost, "/api/portal/v1/payment-methods",
		`{"gateway_token":"tok_visa_4242","method_type":"CARD","brand":"VISA","last4":"4242","exp_month":12,"exp_year":2029,"is_default":true}`, nil)
	if rec.Code != http.StatusCreated {
		t.Fatalf("save tokenized payment method status = %d, want 201: %s", rec.Code, rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), "tok_visa_4242") || strings.Contains(rec.Body.String(), "gateway_token") {
		t.Fatalf("saved payment method response leaked gateway_token: %s", rec.Body.String())
	}
	if body["status"] != "ACTIVE" {
		t.Errorf("saved payment method status = %v, want ACTIVE", body["status"])
	}
	pmID := fmt.Sprint(body["id"])

	rec, achBody := call(apMux, http.MethodPost, "/api/portal/v1/payment-methods",
		`{"gateway_token":"tok_ach_9876","method_type":"ACH","brand":"CHASE","last4":"9876"}`, nil)
	if rec.Code != http.StatusCreated {
		t.Fatalf("save ACH payment method status = %d, want 201: %s", rec.Code, rec.Body.String())
	}
	if achBody["method_type"] != "ACH" || achBody["status"] != "ACTIVE" {
		t.Errorf("saved ACH payment method method_type=%v status=%v, want ACH/ACTIVE", achBody["method_type"], achBody["status"])
	}
	achPmID := fmt.Sprint(achBody["id"])

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

	// 5. Unknown outcome guard (P1-1): if gateway.Charge returns a transport error on inv3,
	// the intent stays CHARGING and retries with the same key, a new Idempotency-Key,
	// a different payment method, or an overlapping invoice batch [inv3, inv4] are all
	// refused with 409 Conflict without calling gateway.Charge again!
	inv3 := uuid.New()
	inv4 := uuid.New()
	inv5 := uuid.New()
	for _, id := range []uuid.UUID{inv3, inv4, inv5} {
		_, err = f.db.Pool.Exec(ctx, `
			INSERT INTO invoices (id, customer_id, order_id, status, total_amount, amount_open, subtotal, tax_amount, invoice_date, due_date, created_at, updated_at, branch_id, currency)
			VALUES ($1, $2, $3, 'UNPAID', 40.00, 40.00, 40.00, 0.00, CURRENT_DATE, CURRENT_DATE + interval '30 days', NOW(), NOW(), $4, 'USD')`,
			id, f.aCustomer, f.aFulfilled, f.aYard)
		if err != nil {
			t.Fatalf("insert invoice %s: %v", id, err)
		}
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
	// 5a. Retry with identical payload -> 409.
	rec, _ = call(errMux, http.MethodPost, "/api/portal/v1/payments", inv3Body, nil)
	if rec.Code != http.StatusConflict {
		t.Fatalf("retry after unknown outcome status = %d, want 409: %s", rec.Code, rec.Body.String())
	}
	// 5b. Retry with a brand-new Idempotency-Key -> 409, charges still 1!
	rec, _ = call(errMux, http.MethodPost, "/api/portal/v1/payments", inv3Body, map[string]string{"Idempotency-Key": "new-key-after-timeout"})
	if rec.Code != http.StatusConflict {
		t.Fatalf("retry with new Idempotency-Key after unknown outcome status = %d, want 409: %s", rec.Code, rec.Body.String())
	}
	// 5c. Retry with a different saved payment method -> 409, charges still 1!
	rec, _ = call(errMux, http.MethodPost, "/api/portal/v1/payments",
		fmt.Sprintf(`{"invoice_ids":[%q],"payment_method_id":%q}`, inv3.String(), achPmID), nil)
	if rec.Code != http.StatusConflict {
		t.Fatalf("retry with alternate payment method after unknown outcome status = %d, want 409: %s", rec.Code, rec.Body.String())
	}
	// 5d. Retry with an overlapping invoice batch [inv3, inv4] -> 409, charges still 1!
	rec, _ = call(errMux, http.MethodPost, "/api/portal/v1/payments",
		fmt.Sprintf(`{"invoice_ids":[%q,%q],"payment_method_id":%q}`, inv3.String(), inv4.String(), pmID), nil)
	if rec.Code != http.StatusConflict {
		t.Fatalf("retry with overlapping batch [inv3, inv4] after unknown outcome status = %d, want 409: %s", rec.Code, rec.Body.String())
	}
	if got := errGw.charges.Load(); got != 1 {
		t.Fatalf("retries after unknown outcome called gateway.Charge %d times, want 1", got)
	}

	// 6. Concurrent overlapping requests with distinct Idempotency-Key values ([inv4] vs [inv4, inv5])
	// result in at most 1 gateway charge (P1-1).
	overlapGw := &countingGateway{
		inner:      NewStubPaymentGateway(),
		chargeWait: 60 * time.Millisecond,
	}
	overlapMux, _ := muxForPhase2(f, f.aCustomer, f.users[RoleAPClerk], RoleAPClerk, overlapGw)
	reqPayloads := []string{
		fmt.Sprintf(`{"invoice_ids":[%q],"payment_method_id":%q,"idempotency_key":"distinct-key-1"}`, inv4.String(), pmID),
		fmt.Sprintf(`{"invoice_ids":[%q,%q],"payment_method_id":%q,"idempotency_key":"distinct-key-2"}`, inv4.String(), inv5.String(), achPmID),
	}
	var wg2 sync.WaitGroup
	overlapCodes := make([]int, 2)
	overlapBodies := make([]string, 2)
	for i := 0; i < 2; i++ {
		wg2.Add(1)
		go func(idx int) {
			defer wg2.Done()
			r, _ := call(overlapMux, http.MethodPost, "/api/portal/v1/payments", reqPayloads[idx], nil)
			overlapCodes[idx] = r.Code
			overlapBodies[idx] = r.Body.String()
		}(i)
	}
	wg2.Wait()
	if got := overlapGw.charges.Load(); got != 1 {
		t.Fatalf("concurrent overlapping payments with distinct keys called gateway.Charge %d times, want 1 (codes=%v bodies=%v)", got, overlapCodes, overlapBodies)
	}
	if !((overlapCodes[0] == http.StatusCreated && overlapCodes[1] == http.StatusConflict) ||
		(overlapCodes[0] == http.StatusConflict && overlapCodes[1] == http.StatusCreated)) {
		t.Errorf("concurrent overlapping payments codes = %v, want one 201 and one 409 (bodies=%v)", overlapCodes, overlapBodies)
	}

	// 7. P2-1: Multi-currency invoice batch is rejected before calling gateway.Charge.
	invCAD := uuid.New()
	invUSD := uuid.New()
	_, _ = f.db.Pool.Exec(ctx, `
		INSERT INTO invoices (id, customer_id, order_id, status, total_amount, amount_open, subtotal, tax_amount, invoice_date, due_date, created_at, updated_at, branch_id, currency)
		VALUES
			($1, $3, $4, 'UNPAID', 30.00, 30.00, 30.00, 0.00, CURRENT_DATE, CURRENT_DATE + interval '30 days', NOW(), NOW(), $5, 'USD'),
			($2, $3, $4, 'UNPAID', 30.00, 30.00, 30.00, 0.00, CURRENT_DATE, CURRENT_DATE + interval '30 days', NOW(), NOW(), $5, 'CAD')`,
		invUSD, invCAD, f.aCustomer, f.aFulfilled, f.aYard)
	curGw := &countingGateway{inner: NewStubPaymentGateway()}
	curMux, _ := muxForPhase2(f, f.aCustomer, f.users[RoleAPClerk], RoleAPClerk, curGw)
	rec, _ = call(curMux, http.MethodPost, "/api/portal/v1/payments",
		fmt.Sprintf(`{"invoice_ids":[%q,%q],"payment_method_id":%q}`, invUSD.String(), invCAD.String(), pmID), nil)
	if rec.Code != http.StatusBadRequest && rec.Code != http.StatusConflict {
		t.Fatalf("mixed-currency payment status = %d, want 400 or 409: %s", rec.Code, rec.Body.String())
	}
	if got := curGw.charges.Load(); got != 0 {
		t.Fatalf("mixed-currency payment called gateway.Charge %d times, want 0", got)
	}

	// 8. Allocation history (GET /api/portal/v1/payments) returns payments and allocations
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
	if strings.Contains(rec.Body.String(), "applied_by") || strings.Contains(rec.Body.String(), "reversed_by") {
		t.Errorf("payment allocation history leaked internal staff attribution: %s", rec.Body.String())
	}
}

