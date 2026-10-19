// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package portal

// Round 2 reproduction tests for the cut 1 security sweep of the contractor
// portal (review file sweep-fix-pr185-opus.md, findings P2-1, P2-2, P3-1 and
// P3-2). Each test wires the stack the way serve.go wires it after the fix,
// so a FAIL (or a build failure, before the fix's new seams exist) at the
// reviewed commit is the finding reproduced. They need a real database
// (testutil.RequireDB).

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gablelbm/gable/internal/customerquote"
	"github.com/gablelbm/gable/internal/payment"
	"github.com/gablelbm/gable/internal/project"
	"github.com/gablelbm/gable/pkg/middleware"
	"github.com/google/uuid"
)

// TestSweepPR185R2_P2_1_UnconfiguredGatewayFailsClosedBeforeClaimingIntent
// proves the gateway is resolved per call the way serve.go resolves it: with
// no Run Payments keys and no dev stub, POST /payments answers 503 and never
// claims a portal_payment_intents row (an unconfigured install cannot strand
// an invoice in CHARGING); in dev mode without keys the stub gateway settles
// and the intent is recorded with gateway "stub".
func TestSweepPR185R2_P2_1_UnconfiguredGatewayFailsClosedBeforeClaimingIntent(t *testing.T) {
	f := setupPro(t)
	ctx := context.Background()

	// Production mode without Run Payments keys: the resolver returns nil.
	_, svc := sweepMux(f)
	svc.WithPaymentGatewayResolver(func() payment.PaymentGateway { return nil })
	mux := sweepRegister(svc, sweepClaimsAuth(f.aCustomer, f.users[RoleOwner], RoleOwner))

	rec := sweepCall(mux, http.MethodPost, "/api/portal/v1/payments",
		fmt.Sprintf(`{"invoice_ids":["%s"],"token":"tok_made_up_by_caller"}`, f.aInvoice), "")
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("POST /payments with no configured gateway answered %d, want 503: %s", rec.Code, rec.Body.String())
	}
	var n int
	if err := f.db.Pool.QueryRow(ctx,
		`SELECT COUNT(*) FROM portal_payment_intents WHERE customer_id = $1`, f.aCustomer).Scan(&n); err != nil {
		t.Fatalf("count intents: %v", err)
	}
	if n != 0 {
		t.Errorf("claimed %d portal_payment_intents rows on a refused payment, want 0", n)
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

	// Dev mode without keys: the resolver returns the stub gateway.
	_, devSvc := sweepMux(f)
	devSvc.WithPaymentGatewayResolver(func() payment.PaymentGateway { return NewStubPaymentGateway() })
	devMux := sweepRegister(devSvc, sweepClaimsAuth(f.aCustomer, f.users[RoleOwner], RoleOwner))

	rec = sweepCall(devMux, http.MethodPost, "/api/portal/v1/payments",
		fmt.Sprintf(`{"invoice_ids":["%s"],"token":"tok_made_up_by_caller"}`, f.aInvoice), "")
	if rec.Code != http.StatusCreated {
		t.Fatalf("dev mode POST /payments through the stub gateway answered %d, want 201: %s", rec.Code, rec.Body.String())
	}
	var gw, intentStatus string
	if err := f.db.Pool.QueryRow(ctx, `
		SELECT gateway, status FROM portal_payment_intents
		WHERE customer_id = $1 ORDER BY created_at DESC LIMIT 1`, f.aCustomer).Scan(&gw, &intentStatus); err != nil {
		t.Fatalf("read intent: %v", err)
	}
	if gw != "stub" {
		t.Errorf("dev mode intent gateway = %q, want \"stub\"", gw)
	}
	if intentStatus == "CHARGING" {
		t.Errorf("dev mode intent stuck in CHARGING after a stub settlement")
	}
}

// sweepR2ChainMux mounts the portal, project and customer quote route sets
// on one chain wired the way serve.go wires it after the round 2 fix: the
// portal auth middleware, then the live customer_users re-read, then the
// portal idempotency layer, so every route set enforces live status and role
// ahead of any cached response replay.
func sweepR2ChainMux(f *proFixture, userID uuid.UUID) *http.ServeMux {
	_, svc := sweepMux(f)
	portalHandler := NewHandler(svc)
	portalIdem := middleware.IdempotencyForPortalAuth(f.db)
	claimsMw := sweepClaimsAuth(f.aCustomer, userID, RoleOwner)
	chain := func(next http.Handler) http.Handler {
		return claimsMw(portalHandler.LiveUserMiddleware(portalIdem(next)))
	}
	mux := http.NewServeMux()
	portalHandler.RegisterRoutes(mux, chain)
	project.NewHandler(project.NewService(project.NewRepository(f.db))).RegisterRoutes(mux, chain)
	cqSvc := customerquote.NewService(f.db, customerquote.NewStore(f.db), nil, testLogger())
	customerquote.NewHandler(cqSvc).RegisterRoutes(mux, chain)
	return mux
}

// TestSweepPR185R2_P2_2_LiveSessionEnforcedAcrossAllPortalRouteSets proves
// the project and customer quote routes resolve identity from the database on
// every request: an owner keeps them, a demotion to site_super takes the
// customer quote routes away immediately despite the token's role claim, and a
// deactivation revokes every route set including a replay of the caller's own
// earlier idempotent write.
func TestSweepPR185R2_P2_2_LiveSessionEnforcedAcrossAllPortalRouteSets(t *testing.T) {
	f := setupPro(t)
	owner := f.users[RoleOwner]
	mux := sweepR2ChainMux(f, owner)

	// An owner can read both route sets outside the portal handler.
	rec := sweepCall(mux, http.MethodGet, "/api/portal/v1/projects", "", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("owner GET /projects answered %d, want 200: %s", rec.Code, rec.Body.String())
	}
	rec = sweepCall(mux, http.MethodGet, "/api/portal/v1/customer-quotes", "", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("owner GET /customer-quotes answered %d, want 200: %s", rec.Code, rec.Body.String())
	}

	// A successful idempotent write, replayed later after deactivation.
	create := func() *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPost, "/api/portal/v1/projects", strings.NewReader(`{"name":"Sweep R2 Project"}`))
		req.Header.Set("Idempotency-Key", "sweep-r2-replay-key")
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, req)
		return w
	}
	if rec := create(); rec.Code != http.StatusCreated {
		t.Fatalf("owner POST /projects answered %d, want 201: %s", rec.Code, rec.Body.String())
	}

	// Demote the row to site_super (a role the customer quote capabilities
	// deny); the token still says owner.
	sweepExec(t, f, `UPDATE customer_users SET role = 'site_super' WHERE id = $1`, owner)
	rec = sweepCall(mux, http.MethodGet, "/api/portal/v1/customer-quotes", "", "")
	if rec.Code != http.StatusForbidden {
		t.Fatalf("GET /customer-quotes after demotion to site_super answered %d, want 403: %s", rec.Code, rec.Body.String())
	}

	// Deactivate the row; every route set refuses, and the earlier
	// idempotent write replays as 401, not as its cached 201.
	sweepExec(t, f, `UPDATE customer_users SET status = 'Inactive' WHERE id = $1`, owner)
	for _, path := range []string{"/api/portal/v1/projects", "/api/portal/v1/customer-quotes", "/api/portal/v1/me"} {
		rec = sweepCall(mux, http.MethodGet, path, "", "")
		if rec.Code != http.StatusUnauthorized {
			t.Fatalf("GET %s after deactivation answered %d, want 401: %s", path, rec.Code, rec.Body.String())
		}
	}
	if rec := create(); rec.Code != http.StatusUnauthorized {
		t.Fatalf("idempotent POST /projects replay after deactivation answered %d, want 401: %s", rec.Code, rec.Body.String())
	}
}
