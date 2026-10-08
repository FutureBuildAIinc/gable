// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package portal

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/gablelbm/gable/pkg/httputil"
	"github.com/gablelbm/gable/pkg/middleware"
	"github.com/google/uuid"
)

// maxBodySize is the maximum request body size (1MB).
const maxBodySize = 1 << 20

// Handler provides HTTP handlers for portal endpoints.
type Handler struct {
	svc *Service
}

// NewHandler creates a new portal handler.
func NewHandler(svc *Service) *Handler {
	return &Handler{svc: svc}
}

// portalWriteError logs the internal error and returns a safe, generic JSON error to the client.
// It delegates to httputil.RespondError which sends a generic message based on status code,
// preventing internal details from leaking to clients.
//
// The one exception is a recognised 409 refusal, which carries a hand-written
// customer-safe `reason` (see writeRefusal in errors.go). A consumer building
// a stage machine needs to tell a contractor why the dealer said no, and
// "Conflict" is not something anyone can act on.
func portalWriteError(w http.ResponseWriter, r *http.Request, msg string, err error, status int) {
	if writeRefusal(w, r, err, status) {
		return
	}
	httputil.RespondError(w, r, msg, status, err)
}

func portalWriteJSON(w http.ResponseWriter, data interface{}) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(data)
}

// RegisterRoutes registers all portal API routes.
// Public routes (login, config) are registered directly on the mux.
// Protected routes are wrapped with portal auth middleware.
// An optional loginLimiter can be provided to apply stricter rate limiting to the login endpoint.
func (h *Handler) RegisterRoutes(mux *http.ServeMux, authMw func(http.Handler) http.Handler, loginLimiter ...func(http.Handler) http.Handler) {
	// Public endpoints
	if len(loginLimiter) > 0 && loginLimiter[0] != nil {
		mux.Handle("POST /api/portal/v1/login", loginLimiter[0](http.HandlerFunc(h.HandleLogin)))
	} else {
		mux.HandleFunc("POST /api/portal/v1/login", h.HandleLogin)
	}
	mux.HandleFunc("POST /api/portal/v1/logout", h.HandleLogout)
	mux.HandleFunc("GET /api/portal/v1/config", h.HandleGetConfig)

	// Protected endpoints
	mux.Handle("GET /api/portal/v1/dashboard", authMw(http.HandlerFunc(h.HandleDashboard)))
	mux.Handle("GET /api/portal/v1/orders", authMw(http.HandlerFunc(h.HandleListOrders)))
	mux.Handle("GET /api/portal/v1/orders/{id}", authMw(http.HandlerFunc(h.HandleGetOrder)))
	mux.Handle("POST /api/portal/v1/orders/reorder", authMw(http.HandlerFunc(h.HandleReorder)))
	mux.Handle("POST /api/portal/v1/orders/{id}/cancel", authMw(http.HandlerFunc(h.HandleCancelOrder)))
	mux.Handle("PUT /api/portal/v1/orders/{id}/project", authMw(http.HandlerFunc(h.HandleSetOrderProject)))
	mux.Handle("GET /api/portal/v1/invoices", authMw(http.HandlerFunc(h.HandleListInvoices)))
	mux.Handle("GET /api/portal/v1/invoices/{id}", authMw(http.HandlerFunc(h.HandleGetInvoice)))
	mux.Handle("GET /api/portal/v1/deliveries", authMw(http.HandlerFunc(h.HandleListDeliveries)))
	mux.Handle("GET /api/portal/v1/deliveries/{id}", authMw(http.HandlerFunc(h.HandleGetDelivery)))
	mux.Handle("POST /api/portal/v1/deliveries/{id}/reschedule", authMw(http.HandlerFunc(h.HandleRequestReschedule)))
	mux.Handle("GET /api/portal/v1/deliveries/{id}/reschedule", authMw(http.HandlerFunc(h.HandleGetReschedule)))

	// Catalog endpoints (Sprint 27)
	mux.Handle("GET /api/portal/v1/catalog", authMw(http.HandlerFunc(h.HandleListCatalog)))
	// Registered before the {id} pattern for readability only — Go 1.22's mux
	// resolves by specificity, so the literal segment wins regardless of order.
	mux.Handle("GET /api/portal/v1/catalog/categories", authMw(http.HandlerFunc(h.HandleListCategories)))
	mux.Handle("GET /api/portal/v1/catalog/{id}", authMw(http.HandlerFunc(h.HandleGetCatalogProduct)))
	mux.Handle("GET /api/portal/v1/catalog/{id}/volume-breaks", authMw(http.HandlerFunc(h.HandleVolumeBreaks)))

	// Quote endpoints — a portal user sends a scope, the dealer prices it.
	mux.Handle("GET /api/portal/v1/quotes", authMw(http.HandlerFunc(h.HandleListQuotes)))
	mux.Handle("POST /api/portal/v1/quotes", authMw(http.HandlerFunc(h.HandleCreateQuote)))
	mux.Handle("GET /api/portal/v1/quotes/{id}", authMw(http.HandlerFunc(h.HandleGetQuote)))
	mux.Handle("POST /api/portal/v1/quotes/{id}/accept", authMw(http.HandlerFunc(h.HandleAcceptQuote)))
	mux.Handle("POST /api/portal/v1/quotes/{id}/decline", authMw(http.HandlerFunc(h.HandleDeclineQuote)))

	// Cart endpoints (Sprint 27)
	mux.Handle("GET /api/portal/v1/cart", authMw(http.HandlerFunc(h.HandleGetCart)))
	mux.Handle("POST /api/portal/v1/cart/items", authMw(http.HandlerFunc(h.HandleAddToCart)))
	mux.Handle("PUT /api/portal/v1/cart/items/{id}", authMw(http.HandlerFunc(h.HandleUpdateCartItem)))
	mux.Handle("DELETE /api/portal/v1/cart/items/{id}", authMw(http.HandlerFunc(h.HandleRemoveCartItem)))

	// Checkout endpoint (Sprint 27)
	mux.Handle("POST /api/portal/v1/checkout", authMw(http.HandlerFunc(h.HandleCheckout)))

	// User Management endpoints (Sprint 34)
	mux.Handle("GET /api/portal/v1/users", authMw(http.HandlerFunc(h.HandleListUsers)))
	mux.Handle("GET /api/portal/v1/invites", authMw(http.HandlerFunc(h.HandleListInvites)))
	mux.Handle("POST /api/portal/v1/invites", authMw(http.HandlerFunc(h.HandleInviteUser)))
	mux.Handle("PUT /api/portal/v1/users/{id}/role", authMw(http.HandlerFunc(h.HandleUpdateUserRole)))
	mux.Handle("PUT /api/portal/v1/users/{id}/status", authMw(http.HandlerFunc(h.HandleUpdateUserStatus)))
}

// HandleLogin authenticates a contractor and returns JWT + config.
func (h *Handler) HandleLogin(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, maxBodySize)
	var req LoginRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		portalWriteError(w, r, "Invalid request body", err, http.StatusBadRequest)
		return
	}

	if req.Email == "" || req.Password == "" {
		httputil.RespondError(w, r, "Email and password are required", http.StatusBadRequest, nil)
		return
	}

	result, err := h.svc.Login(r.Context(), req)
	if err != nil {
		// Always return 401 for login failures — don't leak user existence
		httputil.RespondError(w, r, "Invalid credentials", http.StatusUnauthorized, err)
		return
	}

	// Set httpOnly cookie with the JWT token — never in the response body
	secure := os.Getenv("INSECURE_COOKIES") != "true" // Secure=true by default; disable for local dev
	http.SetCookie(w, &http.Cookie{
		Name:     "portal_token",
		Value:    result.Token,
		Path:     "/api/portal",
		MaxAge:   86400, // 24 hours
		HttpOnly: true,
		Secure:   secure,
		SameSite: http.SameSiteStrictMode,
	})

	portalWriteJSON(w, result.Response)
}

// HandleLogout clears the portal auth cookie.
func (h *Handler) HandleLogout(w http.ResponseWriter, r *http.Request) {
	http.SetCookie(w, &http.Cookie{
		Name:     "portal_token",
		Value:    "",
		Path:     "/api/portal",
		MaxAge:   -1,
		HttpOnly: true,
		Secure:   os.Getenv("INSECURE_COOKIES") != "true",
		SameSite: http.SameSiteStrictMode,
	})
	w.WriteHeader(http.StatusOK)
}

// HandleGetConfig returns portal branding config (public).
func (h *Handler) HandleGetConfig(w http.ResponseWriter, r *http.Request) {
	cfg, err := h.svc.GetConfig(r.Context())
	if err != nil {
		portalWriteError(w, r, "Failed to load portal configuration", err, http.StatusInternalServerError)
		return
	}
	portalWriteJSON(w, cfg)
}

// HandleDashboard returns contractor dashboard data.
func (h *Handler) HandleDashboard(w http.ResponseWriter, r *http.Request) {
	customerID := getPortalCustomerID(r)
	data, err := h.svc.GetDashboard(r.Context(), customerID)
	if err != nil {
		portalWriteError(w, r, "Failed to load dashboard", err, http.StatusInternalServerError)
		return
	}
	portalWriteJSON(w, data)
}

// HandleListOrders returns order history for the customer.
//
// Two optional query parameters, both additive — a caller that sends neither
// gets exactly the response it always got:
//
//	?project_id=<uuid>     only orders on that project (verified as the
//	                       caller's own; someone else's project is a 404)
//	?since=<RFC3339>       only orders whose updated_at is strictly newer
//
// And two response headers that make a 30-second poll cheap:
//
//	ETag                   send it back as If-None-Match to get a 304
//	X-Portal-Latest-Change the cursor to send as ?since= next time
//
// The `since` cursor is updated_at rather than created_at deliberately. The
// consumer's own bug report says an ERP status change it rounds to the same
// display state (CONFIRMED -> ON_HOLD) was invisible; updated_at moves on
// every order.UpdateStatus write, so that change is in the feed.
func (h *Handler) HandleListOrders(w http.ResponseWriter, r *http.Request) {
	customerID := getPortalCustomerID(r)

	var filter OrderListFilter
	if raw := r.URL.Query().Get("project_id"); raw != "" {
		projectID, err := uuid.Parse(raw)
		if err != nil {
			httputil.RespondError(w, r, "Invalid project_id", http.StatusBadRequest, err)
			return
		}
		filter.ProjectID = &projectID
	}
	if raw := r.URL.Query().Get("since"); raw != "" {
		since, err := time.Parse(time.RFC3339Nano, raw)
		if err != nil {
			httputil.RespondError(w, r, "Invalid since: expected an RFC3339 timestamp", http.StatusBadRequest, err)
			return
		}
		filter.Since = &since
	}

	result, err := h.svc.ListOrdersFiltered(r.Context(), customerID, filter)
	if err != nil {
		portalWriteError(w, r, "Failed to load orders", err, statusForPortalError(err, http.StatusInternalServerError))
		return
	}

	w.Header().Set("ETag", result.ETag)
	// A change feed must not be served from a stale shared cache, and the
	// response is per-customer. private + must-revalidate lets a browser keep
	// the body for the conditional request while forbidding a proxy from
	// handing it to anyone else.
	w.Header().Set("Cache-Control", "private, no-cache, must-revalidate")
	if result.LatestUpdatedAt != nil {
		w.Header().Set("X-Portal-Latest-Change", result.LatestUpdatedAt.UTC().Format(time.RFC3339Nano))
	}

	if etagMatches(r.Header.Get("If-None-Match"), result.ETag) {
		w.WriteHeader(http.StatusNotModified)
		return
	}

	portalWriteJSON(w, result.Orders)
}

// etagMatches implements the If-None-Match comparison for the order feed.
//
// Only what this endpoint actually needs: the `*` wildcard, a comma-separated
// list, and the weak-comparison rule (RFC 9110 §13.1.2 — If-None-Match uses
// weak comparison, so a `W/` prefix on either side is ignored). Whitespace
// around list members is stripped because real clients send ", " separators.
func etagMatches(ifNoneMatch, etag string) bool {
	if ifNoneMatch == "" {
		return false
	}
	strip := func(s string) string {
		return strings.TrimPrefix(strings.TrimSpace(s), "W/")
	}
	want := strip(etag)
	for _, candidate := range strings.Split(ifNoneMatch, ",") {
		c := strip(candidate)
		if c == "*" || c == want {
			return true
		}
	}
	return false
}

// HandleGetOrder returns a single order for the customer.
func (h *Handler) HandleGetOrder(w http.ResponseWriter, r *http.Request) {
	customerID := getPortalCustomerID(r)
	idStr := r.PathValue("id")
	orderID, err := uuid.Parse(idStr)
	if err != nil {
		httputil.RespondError(w, r, "Invalid order ID", http.StatusBadRequest, err)
		return
	}

	order, err := h.svc.GetOrder(r.Context(), orderID, customerID)
	if err != nil {
		portalWriteError(w, r, "Order not found", err, http.StatusNotFound)
		return
	}
	portalWriteJSON(w, order)
}

// HandleReorder creates a new draft order from a historical order.
func (h *Handler) HandleReorder(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, maxBodySize)
	customerID := getPortalCustomerID(r)

	var req ReorderRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		portalWriteError(w, r, "Invalid request body", err, http.StatusBadRequest)
		return
	}

	resp, err := h.svc.CreateReorder(r.Context(), customerID, req)
	if err != nil {
		portalWriteError(w, r, "Failed to create reorder", err, http.StatusInternalServerError)
		return
	}

	w.WriteHeader(http.StatusCreated)
	portalWriteJSON(w, resp)
}

// HandleListInvoices returns invoices for the customer.
func (h *Handler) HandleListInvoices(w http.ResponseWriter, r *http.Request) {
	customerID := getPortalCustomerID(r)
	invoices, err := h.svc.ListInvoices(r.Context(), customerID)
	if err != nil {
		portalWriteError(w, r, "Failed to load invoices", err, http.StatusInternalServerError)
		return
	}
	portalWriteJSON(w, invoices)
}

// HandleGetInvoice returns a single invoice for the customer.
func (h *Handler) HandleGetInvoice(w http.ResponseWriter, r *http.Request) {
	customerID := getPortalCustomerID(r)
	idStr := r.PathValue("id")
	invoiceID, err := uuid.Parse(idStr)
	if err != nil {
		httputil.RespondError(w, r, "Invalid invoice ID", http.StatusBadRequest, err)
		return
	}

	inv, err := h.svc.GetInvoice(r.Context(), invoiceID, customerID)
	if err != nil {
		portalWriteError(w, r, "Invoice not found", err, http.StatusNotFound)
		return
	}
	portalWriteJSON(w, inv)
}

// HandleListDeliveries returns deliveries for the customer.
func (h *Handler) HandleListDeliveries(w http.ResponseWriter, r *http.Request) {
	customerID := getPortalCustomerID(r)
	deliveries, err := h.svc.ListDeliveries(r.Context(), customerID)
	if err != nil {
		portalWriteError(w, r, "Failed to load deliveries", err, http.StatusInternalServerError)
		return
	}
	portalWriteJSON(w, deliveries)
}

// HandleGetDelivery returns a single delivery for the customer.
func (h *Handler) HandleGetDelivery(w http.ResponseWriter, r *http.Request) {
	customerID := getPortalCustomerID(r)
	idStr := r.PathValue("id")
	deliveryID, err := uuid.Parse(idStr)
	if err != nil {
		httputil.RespondError(w, r, "Invalid delivery ID", http.StatusBadRequest, err)
		return
	}

	del, err := h.svc.GetDelivery(r.Context(), deliveryID, customerID)
	if err != nil {
		portalWriteError(w, r, "Delivery not found", err, http.StatusNotFound)
		return
	}
	portalWriteJSON(w, del)
}

// HandleCancelOrder cancels an order on behalf of the customer.
//
// 404 for someone else's order (never 403 — that would confirm the id exists),
// 409 for a refusal the state machine or the dispatch board owns, 200 with the
// previous status on success so a consumer can show what it changed from.
func (h *Handler) HandleCancelOrder(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, maxBodySize)
	customerID := getPortalCustomerID(r)

	orderID, err := uuid.Parse(r.PathValue("id"))
	if err != nil {
		httputil.RespondError(w, r, "Invalid order ID", http.StatusBadRequest, err)
		return
	}

	var req CancelOrderRequest
	// An empty body is a valid cancellation; a reason is optional.
	_ = json.NewDecoder(r.Body).Decode(&req)

	resp, err := h.svc.CancelOrder(r.Context(), orderID, customerID, req.Reason)
	if err != nil {
		portalWriteError(w, r, err.Error(), err, statusForPortalError(err, http.StatusInternalServerError))
		return
	}
	portalWriteJSON(w, resp)
}

// HandleSetOrderProject attaches or detaches an order's project.
func (h *Handler) HandleSetOrderProject(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, maxBodySize)
	customerID := getPortalCustomerID(r)

	orderID, err := uuid.Parse(r.PathValue("id"))
	if err != nil {
		httputil.RespondError(w, r, "Invalid order ID", http.StatusBadRequest, err)
		return
	}

	var req SetOrderProjectRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		portalWriteError(w, r, "Invalid request body", err, http.StatusBadRequest)
		return
	}

	updated, err := h.svc.SetOrderProject(r.Context(), orderID, customerID, req.ProjectID)
	if err != nil {
		portalWriteError(w, r, err.Error(), err, statusForPortalError(err, http.StatusInternalServerError))
		return
	}
	portalWriteJSON(w, updated)
}

// --- Category tree and volume breaks ------------------------------------

// HandleListCategories returns the browsable category hierarchy.
func (h *Handler) HandleListCategories(w http.ResponseWriter, r *http.Request) {
	tree, err := h.svc.ListCategoryTree(r.Context())
	if err != nil {
		portalWriteError(w, r, "Failed to load categories", err, http.StatusInternalServerError)
		return
	}
	portalWriteJSON(w, tree)
}

// HandleVolumeBreaks returns this customer's quantity ladder for a product.
func (h *Handler) HandleVolumeBreaks(w http.ResponseWriter, r *http.Request) {
	customerID := getPortalCustomerID(r)
	productID, err := uuid.Parse(r.PathValue("id"))
	if err != nil {
		httputil.RespondError(w, r, "Invalid product ID", http.StatusBadRequest, err)
		return
	}

	breaks, err := h.svc.VolumeBreaksForProduct(r.Context(), customerID, productID)
	if err != nil {
		portalWriteError(w, r, "Product not found", err, http.StatusNotFound)
		return
	}
	portalWriteJSON(w, breaks)
}

// --- Quotes -------------------------------------------------------------

// HandleListQuotes returns the customer's quotes.
func (h *Handler) HandleListQuotes(w http.ResponseWriter, r *http.Request) {
	customerID := getPortalCustomerID(r)
	quotes, err := h.svc.ListQuotes(r.Context(), customerID)
	if err != nil {
		portalWriteError(w, r, "Failed to load quotes", err, http.StatusInternalServerError)
		return
	}
	portalWriteJSON(w, quotes)
}

// HandleGetQuote returns one quote, scoped to the customer.
func (h *Handler) HandleGetQuote(w http.ResponseWriter, r *http.Request) {
	customerID := getPortalCustomerID(r)
	quoteID, err := uuid.Parse(r.PathValue("id"))
	if err != nil {
		httputil.RespondError(w, r, "Invalid quote ID", http.StatusBadRequest, err)
		return
	}

	q, err := h.svc.GetQuote(r.Context(), quoteID, customerID)
	if err != nil {
		portalWriteError(w, r, "Quote not found", err, statusForPortalError(err, http.StatusNotFound))
		return
	}
	portalWriteJSON(w, q)
}

// HandleCreateQuote records a scope for the dealer to price.
//
// Validation failures are 400, not 500: "a line with no product_id needs a
// description" is the client's problem to fix and the message says which line.
func (h *Handler) HandleCreateQuote(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, maxBodySize)
	customerID := getPortalCustomerID(r)

	var req CreateQuoteRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		portalWriteError(w, r, "Invalid request body", err, http.StatusBadRequest)
		return
	}

	q, err := h.svc.CreateQuoteRequest(r.Context(), customerID, req)
	if err != nil {
		portalWriteError(w, r, err.Error(), err, statusForPortalError(err, http.StatusBadRequest))
		return
	}

	w.WriteHeader(http.StatusCreated)
	portalWriteJSON(w, q)
}

// HandleAcceptQuote accepts a priced quote.
func (h *Handler) HandleAcceptQuote(w http.ResponseWriter, r *http.Request) {
	h.decideQuote(w, r, h.svc.AcceptQuote)
}

// HandleDeclineQuote declines a priced quote.
func (h *Handler) HandleDeclineQuote(w http.ResponseWriter, r *http.Request) {
	h.decideQuote(w, r, h.svc.DeclineQuote)
}

func (h *Handler) decideQuote(w http.ResponseWriter, r *http.Request, decide func(context.Context, uuid.UUID, uuid.UUID) (*PortalQuoteDTO, error)) {
	customerID := getPortalCustomerID(r)
	quoteID, err := uuid.Parse(r.PathValue("id"))
	if err != nil {
		httputil.RespondError(w, r, "Invalid quote ID", http.StatusBadRequest, err)
		return
	}

	q, err := decide(r.Context(), quoteID, customerID)
	if err != nil {
		portalWriteError(w, r, err.Error(), err, statusForPortalError(err, http.StatusInternalServerError))
		return
	}
	portalWriteJSON(w, q)
}

// --- Delivery reschedule ------------------------------------------------

// HandleRequestReschedule records a customer's ask for a different delivery
// day.
//
// 202 Accepted, not 200 or 201: the ask is recorded, the schedule is NOT
// changed, and the dispatcher decides. A 200 here would read as "done".
func (h *Handler) HandleRequestReschedule(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, maxBodySize)
	customerID := getPortalCustomerID(r)

	deliveryID, err := uuid.Parse(r.PathValue("id"))
	if err != nil {
		httputil.RespondError(w, r, "Invalid delivery ID", http.StatusBadRequest, err)
		return
	}

	var req RescheduleDeliveryRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		portalWriteError(w, r, "Invalid request body", err, http.StatusBadRequest)
		return
	}

	dto, err := h.svc.RequestDeliveryReschedule(r.Context(), deliveryID, customerID, getPortalUserID(r), req)
	if err != nil {
		portalWriteError(w, r, err.Error(), err, statusForPortalError(err, http.StatusInternalServerError))
		return
	}

	w.WriteHeader(http.StatusAccepted)
	portalWriteJSON(w, dto)
}

// HandleGetReschedule returns the newest reschedule request for a delivery, or
// 204 when the customer has never filed one.
func (h *Handler) HandleGetReschedule(w http.ResponseWriter, r *http.Request) {
	customerID := getPortalCustomerID(r)
	deliveryID, err := uuid.Parse(r.PathValue("id"))
	if err != nil {
		httputil.RespondError(w, r, "Invalid delivery ID", http.StatusBadRequest, err)
		return
	}

	dto, err := h.svc.GetDeliveryReschedule(r.Context(), deliveryID, customerID)
	if err != nil {
		portalWriteError(w, r, err.Error(), err, statusForPortalError(err, http.StatusInternalServerError))
		return
	}
	if dto == nil {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	portalWriteJSON(w, dto)
}

// getPortalCustomerID extracts the customer UUID from the request context.
// The middleware guarantees this is present on protected routes.
func getPortalCustomerID(r *http.Request) uuid.UUID {
	claims, ok := r.Context().Value(middleware.PortalClaimsKey).(*middleware.PortalClaims)
	if !ok || claims == nil {
		return uuid.Nil
	}
	return claims.CustomerID
}

// getPortalUserID extracts the acting customer-user UUID from the request
// context, or nil when the claims carry none. It is recorded on a reschedule
// request so the dealer can see which of a contractor's people asked, without
// the portal having to trust a user id from the request body.
func getPortalUserID(r *http.Request) *uuid.UUID {
	claims, ok := r.Context().Value(middleware.PortalClaimsKey).(*middleware.PortalClaims)
	if !ok || claims == nil || claims.CustomerUserID == uuid.Nil {
		return nil
	}
	id := claims.CustomerUserID
	return &id
}

// getPortalUserRole extracts the user role from the request context.
func getPortalUserRole(r *http.Request) string {
	claims, ok := r.Context().Value(middleware.PortalClaimsKey).(*middleware.PortalClaims)
	if !ok || claims == nil {
		return ""
	}
	return claims.Role
}

// requireAdmin checks that the caller has admin role and writes 403 if not.
// Returns true if the caller is an admin.
func requireAdmin(w http.ResponseWriter, r *http.Request) bool {
	if getPortalUserRole(r) != "Admin" {
		httputil.RespondError(w, r, "Admin access required", http.StatusForbidden, nil)
		return false
	}
	return true
}

// --- Catalog Handlers (Sprint 27) ---

// HandleListCatalog returns the product catalog with customer-specific pricing.
func (h *Handler) HandleListCatalog(w http.ResponseWriter, r *http.Request) {
	customerID := getPortalCustomerID(r)
	filter := CatalogFilter{
		Query:      r.URL.Query().Get("q"),
		Category:   r.URL.Query().Get("category"),
		CategoryID: r.URL.Query().Get("category_id"),
		Species:    r.URL.Query().Get("species"),
		Grade:      r.URL.Query().Get("grade"),
	}

	products, err := h.svc.ListCatalog(r.Context(), customerID, filter)
	if err != nil {
		portalWriteError(w, r, "Failed to load catalog", err, statusForPortalError(err, http.StatusInternalServerError))
		return
	}
	portalWriteJSON(w, products)
}

// HandleGetCatalogProduct returns a single product with customer-specific pricing.
func (h *Handler) HandleGetCatalogProduct(w http.ResponseWriter, r *http.Request) {
	customerID := getPortalCustomerID(r)
	idStr := r.PathValue("id")
	productID, err := uuid.Parse(idStr)
	if err != nil {
		httputil.RespondError(w, r, "Invalid product ID", http.StatusBadRequest, err)
		return
	}

	detail, err := h.svc.GetCatalogProduct(r.Context(), customerID, productID)
	if err != nil {
		portalWriteError(w, r, "Product not found", err, http.StatusNotFound)
		return
	}
	portalWriteJSON(w, detail)
}

// --- Cart Handlers (Sprint 27) ---

// HandleGetCart returns the current customer's cart.
func (h *Handler) HandleGetCart(w http.ResponseWriter, r *http.Request) {
	customerID := getPortalCustomerID(r)
	cart, err := h.svc.GetCart(r.Context(), customerID)
	if err != nil {
		portalWriteError(w, r, "Failed to load cart", err, http.StatusInternalServerError)
		return
	}
	portalWriteJSON(w, cart)
}

// HandleAddToCart adds an item to the customer's cart.
func (h *Handler) HandleAddToCart(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, maxBodySize)
	customerID := getPortalCustomerID(r)

	var req AddToCartRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		portalWriteError(w, r, "Invalid request body", err, http.StatusBadRequest)
		return
	}

	cart, err := h.svc.AddToCart(r.Context(), customerID, req)
	if err != nil {
		portalWriteError(w, r, "Failed to add to cart", err, http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusCreated)
	portalWriteJSON(w, cart)
}

// HandleUpdateCartItem updates a cart item quantity.
func (h *Handler) HandleUpdateCartItem(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, maxBodySize)
	customerID := getPortalCustomerID(r)
	idStr := r.PathValue("id")
	itemID, err := uuid.Parse(idStr)
	if err != nil {
		httputil.RespondError(w, r, "Invalid item ID", http.StatusBadRequest, err)
		return
	}

	var req UpdateCartItemRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		portalWriteError(w, r, "Invalid request body", err, http.StatusBadRequest)
		return
	}

	cart, err := h.svc.UpdateCartItem(r.Context(), customerID, itemID, req)
	if err != nil {
		portalWriteError(w, r, "Failed to update cart item", err, http.StatusInternalServerError)
		return
	}
	portalWriteJSON(w, cart)
}

// HandleRemoveCartItem removes an item from the cart.
func (h *Handler) HandleRemoveCartItem(w http.ResponseWriter, r *http.Request) {
	customerID := getPortalCustomerID(r)
	idStr := r.PathValue("id")
	itemID, err := uuid.Parse(idStr)
	if err != nil {
		httputil.RespondError(w, r, "Invalid item ID", http.StatusBadRequest, err)
		return
	}

	cart, err := h.svc.RemoveCartItem(r.Context(), customerID, itemID)
	if err != nil {
		portalWriteError(w, r, "Failed to remove cart item", err, http.StatusInternalServerError)
		return
	}
	portalWriteJSON(w, cart)
}

// HandleCheckout places an order from the current cart.
func (h *Handler) HandleCheckout(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, maxBodySize)
	customerID := getPortalCustomerID(r)

	var req CheckoutRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		portalWriteError(w, r, "Invalid request body", err, http.StatusBadRequest)
		return
	}

	resp, err := h.svc.Checkout(r.Context(), customerID, req)
	if err != nil {
		portalWriteError(w, r, "Checkout failed", err, http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusCreated)
	portalWriteJSON(w, resp)
}

// --- User Management Handlers (Sprint 34) ---

func (h *Handler) HandleListUsers(w http.ResponseWriter, r *http.Request) {
	if !requireAdmin(w, r) {
		return
	}
	customerID := getPortalCustomerID(r)
	users, err := h.svc.ListCustomerUsers(r.Context(), customerID)
	if err != nil {
		portalWriteError(w, r, "Failed to load users", err, http.StatusInternalServerError)
		return
	}
	portalWriteJSON(w, users)
}

func (h *Handler) HandleListInvites(w http.ResponseWriter, r *http.Request) {
	if !requireAdmin(w, r) {
		return
	}
	customerID := getPortalCustomerID(r)
	invites, err := h.svc.ListPortalInvites(r.Context(), customerID)
	if err != nil {
		portalWriteError(w, r, "Failed to load invites", err, http.StatusInternalServerError)
		return
	}
	portalWriteJSON(w, invites)
}

func (h *Handler) HandleInviteUser(w http.ResponseWriter, r *http.Request) {
	if !requireAdmin(w, r) {
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxBodySize)
	customerID := getPortalCustomerID(r)

	var req InviteUserRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		portalWriteError(w, r, "Invalid request body", err, http.StatusBadRequest)
		return
	}

	invite, err := h.svc.InviteUser(r.Context(), customerID, req)
	if err != nil {
		portalWriteError(w, r, "Failed to invite user", err, http.StatusInternalServerError)
		return
	}

	w.WriteHeader(http.StatusCreated)
	portalWriteJSON(w, invite)
}

func (h *Handler) HandleUpdateUserRole(w http.ResponseWriter, r *http.Request) {
	if !requireAdmin(w, r) {
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxBodySize)
	customerID := getPortalCustomerID(r)
	idStr := r.PathValue("id")
	userID, err := uuid.Parse(idStr)
	if err != nil {
		httputil.RespondError(w, r, "Invalid user ID", http.StatusBadRequest, err)
		return
	}

	var req UpdateUserRoleRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		portalWriteError(w, r, "Invalid request body", err, http.StatusBadRequest)
		return
	}

	if err := h.svc.UpdateUserRole(r.Context(), customerID, userID, req.Role); err != nil {
		portalWriteError(w, r, "Failed to update role", err, http.StatusInternalServerError)
		return
	}

	w.WriteHeader(http.StatusOK)
}

func (h *Handler) HandleUpdateUserStatus(w http.ResponseWriter, r *http.Request) {
	if !requireAdmin(w, r) {
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxBodySize)
	customerID := getPortalCustomerID(r)
	idStr := r.PathValue("id")
	userID, err := uuid.Parse(idStr)
	if err != nil {
		httputil.RespondError(w, r, "Invalid user ID", http.StatusBadRequest, err)
		return
	}

	var req UpdateUserStatusRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		portalWriteError(w, r, "Invalid request body", err, http.StatusBadRequest)
		return
	}

	if err := h.svc.UpdateUserStatus(r.Context(), customerID, userID, req.Status); err != nil {
		portalWriteError(w, r, "Failed to update status", err, http.StatusInternalServerError)
		return
	}

	w.WriteHeader(http.StatusOK)
}
