// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package portal

// Round 3 reproduction tests for the cut 1 security sweep of the contractor
// portal (review file sweep-fix2-pr185-opus.md, findings P2-1 and P3-B). Each
// test asserts the secure behaviour using only APIs that already exist, so a
// behavioural FAIL at the reviewed commit is the finding reproduced. They need
// a real database (testutil.RequireDB).

import (
	"context"
	"fmt"
	"net/http"
	"testing"

	"github.com/gablelbm/gable/internal/customerquote"
	"github.com/gablelbm/gable/internal/payment"
	"github.com/gablelbm/gable/pkg/middleware"
	"github.com/google/uuid"
)

// sweepR3ChainMux mounts the portal and customer quote route sets on one
// chain wired the way serve.go wires it (claims, then the live customer_users
// re-read, then the portal idempotency layer) for one fixture user.
func sweepR3ChainMux(f *proFixture, userID uuid.UUID, role string) *http.ServeMux {
	_, svc := sweepMux(f)
	portalHandler := NewHandler(svc)
	portalIdem := middleware.IdempotencyForPortalAuth(f.db)
	claimsMw := sweepClaimsAuth(f.aCustomer, userID, role)
	chain := func(next http.Handler) http.Handler {
		return claimsMw(portalHandler.LiveUserMiddleware(portalIdem(next)))
	}
	mux := http.NewServeMux()
	portalHandler.RegisterRoutes(mux, chain)
	cqSvc := customerquote.NewService(f.db, customerquote.NewStore(f.db), nil, testLogger())
	customerquote.NewHandler(cqSvc).RegisterRoutes(mux, chain)
	return mux
}

// TestSweepPR185R3_P2_1_CustomerQuoteFiveRoleMatrix proves the customer quote
// capabilities follow the portal's five role model (migration 130's CHECK
// set): owner, estimator and project_manager may build customer quotes,
// site_super and ap_clerk may not (even though the ap_clerk sees account
// money), no removed legacy role name grants anything, and the estimator
// still cannot read the account money surfaces.
func TestSweepPR185R3_P2_1_CustomerQuoteFiveRoleMatrix(t *testing.T) {
	quoteOK := map[string]bool{
		RoleOwner: true, RoleEstimator: true, RoleProjectManager: true,
		RoleSiteSuper: false, RoleAPClerk: false,
	}
	for role, want := range quoteOK {
		a := customerquote.Actor{CustomerID: uuid.New(), Role: role}
		for _, c := range []customerquote.Capability{customerquote.CapQuoteCustomers, customerquote.CapSeeMoney} {
			if got := (customerquote.RoleCapabilities{}).Has(a, c); got != want {
				t.Errorf("role %s: RoleCapabilities.Has(%s) = %v, want %v", role, c, got, want)
			}
		}
	}
	for _, role := range []string{"admin", "purchaser", "buyer", "accounting", "viewer"} {
		a := customerquote.Actor{CustomerID: uuid.New(), Role: role}
		for _, c := range []customerquote.Capability{customerquote.CapQuoteCustomers, customerquote.CapSeeMoney} {
			if (customerquote.RoleCapabilities{}).Has(a, c) {
				t.Errorf("removed legacy role %s must not grant %s", role, c)
			}
		}
	}

	// The portal's own model: the estimator prices work without account
	// money, the ap_clerk sees account money without customer quoting.
	if CapabilitiesFor(RoleEstimator).SeeMoney {
		t.Errorf("portal model: estimator SeeMoney = true, want false")
	}
	if !CapabilitiesFor(RoleAPClerk).SeeMoney {
		t.Errorf("portal model: ap_clerk SeeMoney = false, want true")
	}

	// The same matrix through the routes the fixture's per role users hit.
	f := setupPro(t)
	for role, want := range quoteOK {
		mux := sweepR3ChainMux(f, f.users[role], role)
		rec := sweepCall(mux, http.MethodGet, "/api/portal/v1/customer-quotes", "", "")
		wantCode := http.StatusForbidden
		if want {
			wantCode = http.StatusOK
		}
		if rec.Code != wantCode {
			t.Errorf("role %s: GET /customer-quotes answered %d, want %d: %s", role, rec.Code, wantCode, rec.Body.String())
		}
	}
	estMux := sweepR3ChainMux(f, f.users[RoleEstimator], RoleEstimator)
	for _, path := range []string{"/api/portal/v1/statements", "/api/portal/v1/payments"} {
		rec := sweepCall(estMux, http.MethodGet, path, "", "")
		if rec.Code != http.StatusForbidden {
			t.Errorf("estimator GET %s answered %d, want 403: %s", path, rec.Code, rec.Body.String())
		}
	}
}

// TestSweepPR185R3_P3_B_GatewayResolvedOncePerPayInvoicesCall proves
// PayInvoices resolves the gateway once per call: a resolver whose keys are
// cleared after the first resolution (the readiness check) still settles the
// payment through the gateway it resolved instead of panicking at the charge
// with an intent already claimed CHARGING.
func TestSweepPR185R3_P3_B_GatewayResolvedOncePerPayInvoicesCall(t *testing.T) {
	f := setupPro(t)
	ctx := context.Background()

	calls := 0
	_, svc := sweepMux(f)
	svc.WithPaymentGatewayResolver(func() payment.PaymentGateway {
		calls++
		if calls == 1 {
			return NewStubPaymentGateway()
		}
		return nil
	})
	mux := sweepRegister(svc, sweepClaimsAuth(f.aCustomer, f.users[RoleOwner], RoleOwner))

	func() {
		defer func() {
			if r := recover(); r != nil {
				t.Fatalf("POST /payments panicked after the intent was claimed: %v", r)
			}
		}()
		rec := sweepCall(mux, http.MethodPost, "/api/portal/v1/payments",
			fmt.Sprintf(`{"invoice_ids":["%s"],"token":"tok_made_up_by_caller"}`, f.aInvoice), "")
		if rec.Code != http.StatusCreated {
			t.Fatalf("POST /payments answered %d, want 201: %s", rec.Code, rec.Body.String())
		}
	}()
	var gw, status string
	if err := f.db.Pool.QueryRow(ctx, `
		SELECT gateway, status FROM portal_payment_intents
		WHERE customer_id = $1 ORDER BY created_at DESC LIMIT 1`, f.aCustomer).Scan(&gw, &status); err != nil {
		t.Fatalf("read intent: %v", err)
	}
	if gw != "stub" {
		t.Errorf("intent gateway = %q, want \"stub\"", gw)
	}
	if status == "CHARGING" {
		t.Errorf("intent stuck in CHARGING after a stub settlement")
	}
}
