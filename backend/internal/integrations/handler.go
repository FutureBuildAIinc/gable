// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package integrations

import (
	"crypto/subtle"
	"encoding/json"
	"log/slog"
	"math"
	"net/http"
	"time"

	"github.com/gablelbm/gable/internal/customer"
	"github.com/gablelbm/gable/internal/order"
	"github.com/gablelbm/gable/internal/pricing"
	"github.com/gablelbm/gable/internal/product"
	"github.com/gablelbm/gable/internal/quote"
	"github.com/gablelbm/gable/pkg/database"
	"github.com/google/uuid"
)

type Handler struct {
	db          *database.DB
	pricingSvc  *pricing.Service
	quoteSvc    *quote.Service
	orderSvc    *order.Service
	customerSvc *customer.Service
	productSvc  *product.Service
	apiKey      string

	// ailm backs the AI_LM integration surface (see ailm.go). It is a seam
	// rather than a direct *database.DB use so the handlers can be driven over
	// httptest without Postgres.
	ailm ailmStore
}

func NewHandler(db *database.DB, pricingSvc *pricing.Service, quoteSvc *quote.Service, orderSvc *order.Service, customerSvc *customer.Service, productSvc *product.Service, apiKey string) *Handler {
	return &Handler{
		db:          db,
		pricingSvc:  pricingSvc,
		quoteSvc:    quoteSvc,
		orderSvc:    orderSvc,
		customerSvc: customerSvc,
		productSvc:  productSvc,
		apiKey:      apiKey,
		ailm:        newPGAILMStore(db),
	}
}

func (h *Handler) RegisterRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/integration/products", h.authMiddleware(h.ListProductsByCategory))
	mux.HandleFunc("POST /api/integration/quotes/bulk-price", h.authMiddleware(h.BulkCalculatePrice))
	mux.HandleFunc("POST /api/integration/quotes", h.authMiddleware(h.CreateQuote))
	mux.HandleFunc("POST /api/integration/quotes/{id}/accept-and-convert", h.authMiddleware(h.AcceptAndConvertQuote))

	// AI_LM load-management, routing and staff-authentication surface. See
	// ailm.go for the wire contract these satisfy.
	mux.HandleFunc("GET /api/integration/vehicles", h.authMiddleware(h.ListVehicles))
	mux.HandleFunc("GET /api/integration/drivers", h.authMiddleware(h.ListDrivers))
	mux.HandleFunc("GET /api/integration/locations", h.authMiddleware(h.ListLocations))
	mux.HandleFunc("GET /api/integration/orders", h.authMiddleware(h.ListOrdersForDate))
	mux.HandleFunc("POST /api/integration/delivery-routes", h.authMiddleware(h.CreateDeliveryRoute))
	mux.HandleFunc("POST /api/integration/validate-staff", h.authMiddleware(h.ValidateStaff))
}

func (h *Handler) authMiddleware(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if h.apiKey == "" {
			writeError(w, http.StatusServiceUnavailable, "Integration endpoints not configured")
			return
		}
		key := r.Header.Get("X-Integration-Key")
		if subtle.ConstantTimeCompare([]byte(key), []byte(h.apiKey)) != 1 {
			writeError(w, http.StatusUnauthorized, "invalid integration key")
			return
		}
		next(w, r)
	}
}

// ListProductsByCategory returns catalog products as a bare JSON array.
//
//	GET /api/integration/products[?category=...][&q=...]
//
// Both filters are OPTIONAL. An unfiltered call is a bulk catalog pull — AI_LM's
// GetProductsWithWeight() sends no query parameters at all — and gets the higher
// productBulkLimit so a dealer's SKU list is not silently truncated. A filtered
// call is a typeahead search and keeps the small productSearchLimit.
//
// (This endpoint previously answered 400 when neither filter was supplied,
// which made the bulk pull impossible. See ProductResponse in ailm.go for the
// payload, which now carries weight and PIM geometry.)
func (h *Handler) ListProductsByCategory(w http.ResponseWriter, r *http.Request) {
	f := productFilter{
		Category: r.URL.Query().Get("category"),
		Query:    r.URL.Query().Get("q"),
		Limit:    productSearchLimit,
	}
	if f.Category == "" && f.Query == "" {
		f.Limit = productBulkLimit
	}

	products, err := h.ailm.ListProducts(r.Context(), f)
	if err != nil {
		slog.Error("failed to query products", "error", err, "method", r.Method, "path", r.URL.Path)
		writeError(w, http.StatusInternalServerError, "failed to query products")
		return
	}

	writeJSON(w, http.StatusOK, nonNil(products))
}

// BulkPriceRequest is the request body for bulk pricing
type BulkPriceRequest struct {
	CustomerID string          `json:"customer_id"`
	Items      []BulkPriceItem `json:"items"`
}

type BulkPriceItem struct {
	ProductID string `json:"product_id"`
	Quantity  int    `json:"quantity"`
}

type PricedItemResponse struct {
	ProductID   string `json:"product_id"`
	ProductName string `json:"product_name"`
	SKU         string `json:"sku"`
	Quantity    int    `json:"quantity"`
	UnitPrice   int64  `json:"unit_price"`  // cents
	TotalPrice  int64  `json:"total_price"` // cents
	UOM         string `json:"uom"`
}

// BulkCalculatePrice calculates prices for multiple items for a specific customer
func (h *Handler) BulkCalculatePrice(w http.ResponseWriter, r *http.Request) {
	var req BulkPriceRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}

	customerID, err := uuid.Parse(req.CustomerID)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid customer_id")
		return
	}

	cust, err := h.customerSvc.GetCustomer(r.Context(), customerID)
	if err != nil {
		slog.Error("customer not found", "error", err, "customer_id", req.CustomerID, "method", r.Method, "path", r.URL.Path)
		writeError(w, http.StatusNotFound, "customer not found")
		return
	}

	var results []PricedItemResponse
	for _, item := range req.Items {
		productID, err := uuid.Parse(item.ProductID)
		if err != nil {
			continue
		}

		prod, err := h.productSvc.GetProduct(r.Context(), productID)
		if err != nil {
			continue
		}

		calculated, err := h.pricingSvc.CalculatePriceWithQty(r.Context(), cust, productID, prod.BasePrice, float64(item.Quantity), nil)
		if err != nil {
			continue
		}

		unitPriceCents := int64(calculated.FinalPrice * 100)
		totalPriceCents := unitPriceCents * int64(item.Quantity)

		results = append(results, PricedItemResponse{
			ProductID:   item.ProductID,
			ProductName: prod.Description,
			SKU:         prod.SKU,
			Quantity:    item.Quantity,
			UnitPrice:   unitPriceCents,
			TotalPrice:  totalPriceCents,
			UOM:         string(prod.UOMPrimary),
		})
	}

	writeJSON(w, http.StatusOK, results)
}

// CreateQuoteRequest is the request body for creating a quote
type CreateQuoteRequest struct {
	CustomerID string           `json:"customer_id"`
	Lines      []QuoteLineInput `json:"lines"`
}

type QuoteLineInput struct {
	ProductID string `json:"product_id"`
	Quantity  int    `json:"quantity"`
	UnitPrice int64  `json:"unit_price"` // cents
}

type QuoteResponse struct {
	ID         string `json:"id"`
	CustomerID string `json:"customer_id"`
	Total      int64  `json:"total"` // cents
	Status     string `json:"status"`
}

// CreateQuote creates a DRAFT quote from pre-priced line items
func (h *Handler) CreateQuote(w http.ResponseWriter, r *http.Request) {
	var req CreateQuoteRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}

	customerID, err := uuid.Parse(req.CustomerID)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid customer_id")
		return
	}

	// Build quote lines
	var lines []quote.QuoteLine
	for _, line := range req.Lines {
		productID, err := uuid.Parse(line.ProductID)
		if err != nil {
			continue
		}

		prod, err := h.productSvc.GetProduct(r.Context(), productID)
		if err != nil {
			continue
		}

		unitPriceDollars := float64(line.UnitPrice) / 100.0
		lines = append(lines, quote.QuoteLine{
			ProductID:   productID,
			SKU:         prod.SKU,
			Description: prod.Description,
			Quantity:    float64(line.Quantity),
			UOM:         prod.UOMPrimary,
			UnitPrice:   unitPriceDollars,
		})
	}

	demoCreatedBy := uuid.MustParse("00000000-0000-0000-0000-000000000001")
	expires := time.Now().AddDate(0, 0, 30)

	q := &quote.Quote{
		CustomerID: customerID,
		State:      quote.QuoteStateDraft,
		ExpiresAt:  &expires,
		Lines:      lines,
	}
	// Set CreatedBy via context or field - the service will handle totals
	_ = demoCreatedBy

	if err := h.quoteSvc.CreateQuote(r.Context(), q); err != nil {
		slog.Error("failed to create quote", "error", err, "method", r.Method, "path", r.URL.Path)
		writeError(w, http.StatusInternalServerError, "failed to create quote")
		return
	}

	totalCents := int64(q.TotalAmount * 100)

	writeJSON(w, http.StatusCreated, QuoteResponse{
		ID:         q.ID.String(),
		CustomerID: req.CustomerID,
		Total:      totalCents,
		Status:     string(q.State),
	})
}

type OrderResponse struct {
	ID      string `json:"id"`
	QuoteID string `json:"quote_id"`
	Status  string `json:"status"`
}

// AcceptAndConvertQuote accepts a quote and converts it to an order
func (h *Handler) AcceptAndConvertQuote(w http.ResponseWriter, r *http.Request) {
	idStr := r.PathValue("id")
	quoteID, err := uuid.Parse(idStr)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid quote id")
		return
	}

	ctx := r.Context()

	// 1. Load the quote first. We intentionally do NOT mark it ACCEPTED yet —
	//    QuoteStateAccepted is terminal, so accepting it before the order is
	//    created would strand the quote un-reconvertible if order creation fails.
	q, err := h.quoteSvc.GetQuote(ctx, quoteID)
	if err != nil {
		slog.Error("failed to get quote", "error", err, "quote_id", idStr, "method", r.Method, "path", r.URL.Path)
		writeError(w, http.StatusInternalServerError, "failed to get quote")
		return
	}

	// 2. Convert to order — PriceEach is now int64 cents
	// TODO: align with int64 cents — quote.UnitPrice is still float64 dollars
	var orderLines []order.OrderLineRequest
	for _, ql := range q.Lines {
		orderLines = append(orderLines, order.OrderLineRequest{
			ProductID: ql.ProductID,
			Quantity:  ql.Quantity,
			PriceEach: int64(math.Round(ql.UnitPrice * 100)),
		})
	}

	o, err := h.orderSvc.CreateOrder(ctx, order.CreateOrderRequest{
		CustomerID: q.CustomerID,
		QuoteID:    &quoteID,
		Lines:      orderLines,
	})
	if err != nil {
		slog.Error("failed to create order from quote", "error", err, "quote_id", idStr, "method", r.Method, "path", r.URL.Path)
		writeError(w, http.StatusInternalServerError, "failed to create order")
		return
	}

	// 3. Now that the order exists, mark the quote accepted.
	if err := h.quoteSvc.UpdateState(ctx, quoteID, quote.QuoteStateAccepted); err != nil {
		slog.Error("order created but quote not marked accepted", "error", err, "order_id", o.ID, "quote_id", idStr)
		writeError(w, http.StatusInternalServerError, "order "+o.ID.String()+" created but quote could not be accepted")
		return
	}

	// 4. Confirm the order. A failure here is reported (not silently masked as a
	//    200 success) — the order exists in DRAFT and confirmation can be retried.
	if err := h.orderSvc.ConfirmOrder(ctx, o.ID); err != nil {
		slog.Error("order created but not confirmed", "order_id", o.ID, "error", err)
		writeError(w, http.StatusConflict, "order "+o.ID.String()+" created from quote but could not be confirmed: "+err.Error())
		return
	}

	// Reflect the true post-confirmation status in the response.
	status := "CONFIRMED"
	if confirmed, gErr := h.orderSvc.GetOrder(ctx, o.ID); gErr == nil {
		status = string(confirmed.Status)
	}

	writeJSON(w, http.StatusOK, OrderResponse{
		ID:      o.ID.String(),
		QuoteID: quoteID.String(),
		Status:  status,
	})
}

func writeJSON(w http.ResponseWriter, status int, v interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, status int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(map[string]string{"error": msg})
}
