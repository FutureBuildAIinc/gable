// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package portal

// Reproduction tests for the cut 1 security sweep of the contractor portal
// (review file sweep-pr182-opus.md, findings P1-1, P1-2 and P2-1). Each test
// asserts the secure behaviour, so a FAIL at the reviewed commit is the
// finding reproduced. They need a real database (testutil.RequireDB).

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gablelbm/gable/internal/account"
	"github.com/gablelbm/gable/internal/customer"
	"github.com/gablelbm/gable/internal/document"
	"github.com/gablelbm/gable/internal/inventory"
	"github.com/gablelbm/gable/internal/invoice"
	"github.com/gablelbm/gable/internal/order"
	"github.com/gablelbm/gable/internal/product"
	"github.com/gablelbm/gable/internal/quote"
	"github.com/gablelbm/gable/pkg/middleware"
	"github.com/google/uuid"
	"golang.org/x/crypto/bcrypt"
)

// sweepClaimsAuth injects fixed claims the way the Phase 2 tests do; the
// live-session wrapper still re-reads the user row on every request.
func sweepClaimsAuth(customerID, userID uuid.UUID, role string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			claims := &middleware.PortalClaims{
				CustomerID:     customerID,
				CustomerUserID: userID,
				Email:          "sweep@example.test",
				Name:           "Sweep Fixture",
				Role:           role,
			}
			ctx := context.WithValue(r.Context(), middleware.PortalClaimsKey, claims)
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

// sweepMux builds the full portal stack the way serve.go does, with one
// difference the caller controls: no payment gateway is wired unless
// withGateway wires one. Login works against the fixture users.
func sweepMux(f *proFixture) (*http.ServeMux, *Service) {
	repo := NewRepository(f.db)
	productRepo := product.NewRepository(f.db)
	productSvc := product.NewService(productRepo)
	customerSvc := customer.NewService(customer.NewRepository(f.db))
	inventorySvc := inventory.NewService(inventory.NewRepository(f.db))
	invoiceSvc := invoice.NewService(invoice.NewRepository(f.db), nil, account.NewService(f.db, nil, testLogger()), f.db)
	orderSvc := order.NewService(order.NewRepository(f.db)).
		WithTxRunner(f.db).
		WithInvoices(invoiceSvc)
	quoteSvc := quote.NewService(quote.NewRepository(f.db)).
		WithTxRunner(f.db).
		WithOrderCreator(orderSvc)
	docSvc := document.NewService(productRepo).
		WithYards(document.NewStore(f.db)).
		WithStatements(account.NewService(f.db, nil, testLogger()))

	svc := NewService(repo, testSecret, testLogger(), nil, customerSvc, inventorySvc, orderSvc, productSvc).
		WithQuoteService(quoteSvc).
		WithAccountService(account.NewService(f.db, nil, testLogger())).
		WithInvoiceService(invoiceSvc).
		WithDocumentService(docSvc)
	return nil, svc
}

// sweepRegister mounts the sweep service with the given auth middleware.
func sweepRegister(svc *Service, authMw func(http.Handler) http.Handler) *http.ServeMux {
	mux := http.NewServeMux()
	NewHandler(svc).RegisterRoutes(mux, authMw)
	return mux
}

func sweepCall(mux *http.ServeMux, method, path, body string, cookie string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	if cookie != "" {
		req.Header.Set("Cookie", cookie)
	}
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	return rec
}

func sweepExec(t *testing.T, f *proFixture, sql string, args ...any) {
	t.Helper()
	if _, err := f.db.Pool.Exec(context.Background(), sql, args...); err != nil {
		t.Fatalf("exec %q: %v", sql, err)
	}
}

// TestSweepPR182_P1_1_NoDefaultGatewayRefusesPaymentAndSavedMethod proves a
// service built the way serve.go builds it (no WithPaymentGateway call) fails
// closed: pay and saved-method routes answer 503, no receipt is posted, and
// the invoice keeps its full open amount.
func TestSweepPR182_P1_1_NoDefaultGatewayRefusesPaymentAndSavedMethod(t *testing.T) {
	f := setupPro(t)
	ctx := context.Background()

	_, svc := sweepMux(f)
	mux := sweepRegister(svc, sweepClaimsAuth(f.aCustomer, f.users[RoleOwner], RoleOwner))

	rec := sweepCall(mux, http.MethodPost, "/api/portal/v1/payments",
		fmt.Sprintf(`{"invoice_ids":["%s"],"token":"tok_made_up_by_caller"}`, f.aInvoice), "")
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("POST /payments with no gateway wired answered %d, want 503: %s", rec.Code, rec.Body.String())
	}

	rec = sweepCall(mux, http.MethodPost, "/api/portal/v1/payment-methods",
		`{"gateway_token":"tok_made_up_by_caller","last4":"4242"}`, "")
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("POST /payment-methods with no gateway wired answered %d, want 503: %s", rec.Code, rec.Body.String())
	}

	var status string
	var open float64
	if err := f.db.Pool.QueryRow(ctx,
		`SELECT status, COALESCE(amount_open, total_amount) FROM invoices WHERE id = $1`, f.aInvoice).
		Scan(&status, &open); err != nil {
		t.Fatalf("read invoice: %v", err)
	}
	if status != "UNPAID" || open != 100.00 {
		t.Errorf("invoice after refused payment = %s / %.2f, want UNPAID / 100.00", status, open)
	}

	var n int
	if err := f.db.Pool.QueryRow(ctx,
		`SELECT COUNT(*) FROM payments WHERE customer_id = $1`, f.aCustomer).Scan(&n); err != nil {
		t.Fatalf("count payments: %v", err)
	}
	if n != 0 {
		t.Errorf("recorded %d payments after a refused charge, want 0", n)
	}
	if err := f.db.Pool.QueryRow(ctx,
		`SELECT COUNT(*) FROM portal_payment_methods WHERE customer_id = $1`, f.aCustomer).Scan(&n); err != nil {
		t.Fatalf("count payment methods: %v", err)
	}
	if n != 0 {
		t.Errorf("saved %d payment methods after a refused request, want 0", n)
	}
}

// TestSweepPR182_P1_2a_InactiveUserCannotLogin proves Login refuses a user
// whose row is Inactive with the same 401 as a bad password, and no cookie
// is set.
func TestSweepPR182_P1_2a_InactiveUserCannotLogin(t *testing.T) {
	f := setupPro(t)

	sweepExec(t, f, `INSERT INTO portal_config (dealer_name) SELECT 'Sweep Dealer' WHERE NOT EXISTS (SELECT 1 FROM portal_config)`)

	hash, err := bcrypt.GenerateFromPassword([]byte("sweep-inactive-pass"), bcrypt.MinCost)
	if err != nil {
		t.Fatalf("bcrypt: %v", err)
	}
	email := "sweep-inactive@example.test"
	sweepExec(t, f, `INSERT INTO customer_users (id, customer_id, email, password_hash, name, role, status)
		VALUES ($1, $2, $3, $4, 'Sweep Inactive', 'ap_clerk', 'Inactive')`,
		uuid.New(), f.aCustomer, email, string(hash))

	_, svc := sweepMux(f)
	mux := sweepRegister(svc, func(next http.Handler) http.Handler { return next })

	rec := sweepCall(mux, http.MethodPost, "/api/portal/v1/login",
		fmt.Sprintf(`{"email":%q,"password":"sweep-inactive-pass"}`, email), "")
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("login as Inactive user answered %d, want 401: %s", rec.Code, rec.Body.String())
	}
	if c := rec.Header().Get("Set-Cookie"); strings.Contains(c, "portal_token=") && !strings.Contains(c, "portal_token=;") {
		t.Errorf("login refusal set a portal_token cookie: %q", c)
	}
}

// TestSweepPR182_P1_2b_DemotedOrDeactivatedUserLosesCapabilitiesAndCannotSelfMutate
// proves the portal resolves role and status from the database on every
// request: an owner cannot change their own role or status, a demotion to
// site_super takes effect immediately despite the cookie's role claim, and a
// deactivation revokes the session entirely.
func TestSweepPR182_P1_2b_DemotedOrDeactivatedUserLosesCapabilitiesAndCannotSelfMutate(t *testing.T) {
	f := setupPro(t)
	ctx := context.Background()

	hash, err := bcrypt.GenerateFromPassword([]byte("sweep-owner-pass"), bcrypt.MinCost)
	if err != nil {
		t.Fatalf("bcrypt: %v", err)
	}
	sweepExec(t, f, `UPDATE customer_users SET password_hash = $1 WHERE id = $2`, string(hash), f.users[RoleOwner])

	_, svc := sweepMux(f)
	authMw := middleware.NewPortalAuthMiddleware([]byte(testSecret), testLogger()).Handler
	mux := sweepRegister(svc, authMw)

	var email string
	if err := f.db.Pool.QueryRow(ctx, `SELECT email FROM customer_users WHERE id = $1`, f.users[RoleOwner]).Scan(&email); err != nil {
		t.Fatalf("read owner email: %v", err)
	}
	login, err := svc.Login(ctx, LoginRequest{Email: email, Password: "sweep-owner-pass"})
	if err != nil {
		t.Fatalf("owner login: %v", err)
	}
	cookie := "portal_token=" + login.Token
	selfID := login.Response.User.ID

	// An owner cannot change their own role or status.
	rec := sweepCall(mux, http.MethodPut, "/api/portal/v1/users/"+selfID.String()+"/role", `{"role":"site_super"}`, cookie)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("self role change answered %d, want 403: %s", rec.Code, rec.Body.String())
	}
	rec = sweepCall(mux, http.MethodPut, "/api/portal/v1/users/"+selfID.String()+"/status", `{"status":"Inactive"}`, cookie)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("self status change answered %d, want 403: %s", rec.Code, rec.Body.String())
	}

	// Demote the row in the database; the cookie still says owner.
	sweepExec(t, f, `UPDATE customer_users SET role = 'site_super' WHERE id = $1`, selfID)
	rec = sweepCall(mux, http.MethodGet, "/api/portal/v1/users", "", cookie)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("GET /users after demotion answered %d, want 403: %s", rec.Code, rec.Body.String())
	}
	rec = sweepCall(mux, http.MethodPost, "/api/portal/v1/payment-methods",
		`{"gateway_token":"tok_made_up_by_caller","last4":"4242"}`, cookie)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("POST /payment-methods after demotion answered %d, want 403: %s", rec.Code, rec.Body.String())
	}

	// Deactivate the row; every protected route answers 401.
	sweepExec(t, f, `UPDATE customer_users SET status = 'Inactive' WHERE id = $1`, selfID)
	for _, path := range []string{"/api/portal/v1/me", "/api/portal/v1/orders", "/api/portal/v1/users"} {
		rec = sweepCall(mux, http.MethodGet, path, "", cookie)
		if rec.Code != http.StatusUnauthorized {
			t.Fatalf("GET %s after deactivation answered %d, want 401: %s", path, rec.Code, rec.Body.String())
		}
	}
}

// sweepSentQuote seeds a SENT quote with one priced line the fixture product.
func sweepSentQuote(t *testing.T, f *proFixture, customerID uuid.UUID) uuid.UUID {
	t.Helper()
	id := uuid.New()
	sweepExec(t, f, `INSERT INTO quotes (id, customer_id, state, total_amount, source, branch_id, sent_at, created_at, updated_at)
		VALUES ($1, $2, 'SENT', 100.00, 'portal', $3, NOW(), NOW(), NOW())`, id, customerID, f.aYard)
	sweepExec(t, f, `INSERT INTO quote_lines (id, quote_id, product_id, sku, description, quantity, unit_price, line_total, uom, price_uom, uom_qty, price_uom_qty, stock_uom, stock_quantity)
		VALUES ($1, $2, $3, 'PRO-1', 'Pro fixture board', 4, 25.00, 100.00, 'EA', 'EA', 1, 1, 'EA', 4)`,
		uuid.New(), id, f.product)
	return id
}

func sweepQuoteState(t *testing.T, f *proFixture, id uuid.UUID) string {
	t.Helper()
	var state string
	if err := f.db.Pool.QueryRow(context.Background(), `SELECT state FROM quotes WHERE id = $1`, id).Scan(&state); err != nil {
		t.Fatalf("read quote state: %v", err)
	}
	return state
}

func sweepOrderCount(t *testing.T, f *proFixture, customerID uuid.UUID) int {
	t.Helper()
	var n int
	if err := f.db.Pool.QueryRow(context.Background(),
		`SELECT COUNT(*) FROM orders WHERE customer_id = $1`, customerID).Scan(&n); err != nil {
		t.Fatalf("count orders: %v", err)
	}
	return n
}

// TestSweepPR182_P2_1_ConvertSentQuoteRequiresApproveQuotesCapability proves
// a role with CapOrder but without CapApproveQuotes (site_super) cannot
// convert a SENT quote, while it may convert one already ACCEPTED.
func TestSweepPR182_P2_1_ConvertSentQuoteRequiresApproveQuotesCapability(t *testing.T) {
	f := setupPro(t)

	siteMux := sweepRegister(mustSweepSvc(t, f), sweepClaimsAuth(f.aCustomer, f.users[RoleSiteSuper], RoleSiteSuper))
	ownerMux := sweepRegister(mustSweepSvc(t, f), sweepClaimsAuth(f.aCustomer, f.users[RoleOwner], RoleOwner))

	before := sweepOrderCount(t, f, f.aCustomer)
	sentID := sweepSentQuote(t, f, f.aCustomer)

	// site_super converting a SENT quote is the accept route's refusal bypassed.
	rec := sweepCall(siteMux, http.MethodPost, "/api/portal/v1/quotes/"+sentID.String()+"/convert", `{}`, "")
	if rec.Code != http.StatusForbidden {
		t.Fatalf("site_super convert SENT quote answered %d, want 403: %s", rec.Code, rec.Body.String())
	}
	if got := sweepQuoteState(t, f, sentID); got != "SENT" {
		t.Errorf("quote state after refused convert = %s, want SENT", got)
	}
	if got := sweepOrderCount(t, f, f.aCustomer); got != before {
		t.Errorf("order count after refused convert = %d, want %d", got, before)
	}

	// An owner (CapApproveQuotes) converts the SENT quote directly.
	rec = sweepCall(ownerMux, http.MethodPost, "/api/portal/v1/quotes/"+sentID.String()+"/convert", `{}`, "")
	if rec.Code != http.StatusCreated {
		t.Fatalf("owner convert SENT quote answered %d, want 201: %s", rec.Code, rec.Body.String())
	}

	// Once accepted, CapOrder alone is enough for site_super to convert.
	acceptedID := sweepSentQuote(t, f, f.aCustomer)
	rec = sweepCall(ownerMux, http.MethodPost, "/api/portal/v1/quotes/"+acceptedID.String()+"/accept", `{}`, "")
	if rec.Code != http.StatusOK {
		t.Fatalf("owner accept quote answered %d, want 200: %s", rec.Code, rec.Body.String())
	}
	rec = sweepCall(siteMux, http.MethodPost, "/api/portal/v1/quotes/"+acceptedID.String()+"/convert", `{}`, "")
	if rec.Code != http.StatusCreated {
		t.Fatalf("site_super convert ACCEPTED quote answered %d, want 201: %s", rec.Code, rec.Body.String())
	}
}

// mustSweepSvc builds the sweep service so each mux gets its own session.
func mustSweepSvc(t *testing.T, f *proFixture) *Service {
	t.Helper()
	_, svc := sweepMux(f)
	return svc
}
