// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gablelbm/gable/internal/customer"
	"github.com/gablelbm/gable/internal/order"
)

// exposureAPISurface is the full HTTP contract of the lumber index-aware
// price-protection feature. The frontend pages (quotes/Exposure.ts,
// reports/ExposureReport.ts, admin/MarketIndices.ts) and the ExposureService
// client call exactly these; dropping one is a silent 404 in the UI rather
// than a build failure, so it is pinned here.
var exposureAPISurface = []struct{ method, path string }{
	// Owned by registerExposureRoutes (internal/pricing).
	{http.MethodGet, "/api/v1/quotes/exposure"},
	{http.MethodGet, "/api/v1/quotes/11111111-1111-1111-1111-111111111111/exposure"},
	{http.MethodPost, "/api/v1/quotes/11111111-1111-1111-1111-111111111111/exposure/acknowledge"},
	{http.MethodPost, "/api/v1/quotes/11111111-1111-1111-1111-111111111111/exposure/escalate-now"},
	{http.MethodPost, "/api/v1/quotes/11111111-1111-1111-1111-111111111111/exposure/override"},
	{http.MethodPost, "/api/v1/quotes/11111111-1111-1111-1111-111111111111/exposure/request-ack"},
	{http.MethodGet, "/api/v1/reports/exposure"},
	{http.MethodPost, "/api/v1/admin/exposure-scan"},
	{http.MethodGet, "/api/v1/market-indices/22222222-2222-2222-2222-222222222222/history"},
	{http.MethodPut, "/api/v1/market-indices/22222222-2222-2222-2222-222222222222"},
	{http.MethodPost, "/api/v1/market-indices/22222222-2222-2222-2222-222222222222/refresh"},
	{http.MethodPost, "/api/v1/market-indices/22222222-2222-2222-2222-222222222222/refresh/preview"},
	// Owned by internal/order — the pre-ship gate lives with the order.
	{http.MethodGet, "/api/v1/orders/33333333-3333-3333-3333-333333333333/exposure-gate"},
	{http.MethodPost, "/api/v1/orders/33333333-3333-3333-3333-333333333333/exposure-override"},
	// Owned by internal/customer — the policy is an attribute of the account.
	{http.MethodGet, "/api/v1/customers/44444444-4444-4444-4444-444444444444/escalation-policy"},
	{http.MethodPut, "/api/v1/customers/44444444-4444-4444-4444-444444444444/escalation-policy"},
}

// newExposureSurfaceMux registers every module that contributes to the
// price-protection HTTP surface. Collaborators are nil: ServeMux pattern
// registration never touches them, and the test only resolves routes, it does
// not serve them.
func newExposureSurfaceMux() *http.ServeMux {
	mux := http.NewServeMux()
	registerExposureRoutes(mux, exposureRoutes{})
	order.NewHandler(nil).RegisterRoutes(mux)
	customer.NewHandler(nil).RegisterRoutes(mux)
	return mux
}

// TestExposureAPISurfaceIsRegistered fails if any of the sixteen
// price-protection endpoints stops resolving.
func TestExposureAPISurfaceIsRegistered(t *testing.T) {
	mux := newExposureSurfaceMux()

	if len(exposureAPISurface) != 16 {
		t.Fatalf("surface has %d routes, want the documented 16", len(exposureAPISurface))
	}

	for _, rt := range exposureAPISurface {
		t.Run(rt.method+" "+rt.path, func(t *testing.T) {
			req := httptest.NewRequest(rt.method, rt.path, nil)
			_, pattern := mux.Handler(req)
			if pattern == "" {
				t.Fatalf("%s %s does not resolve — route is missing from the mux", rt.method, rt.path)
			}
			// A pattern with no method prefix would answer every verb; the
			// exposure surface registers method-scoped patterns so that, e.g.,
			// GET on the override endpoint is a 405 rather than a mutation.
			if !strings.HasPrefix(pattern, rt.method+" ") {
				t.Errorf("%s %s resolved to pattern %q, want a %s-scoped pattern",
					rt.method, rt.path, pattern, rt.method)
			}
		})
	}
}

// TestExposureRoutesDoNotShadowMarketIndexList guards the ServeMux collision
// noted in registerExposureRoutes: PUT /market-indices/{id} must not swallow
// GET /api/v1/market-indices, which pricing.EscalatorHandler owns.
func TestExposureRoutesDoNotShadowMarketIndexList(t *testing.T) {
	mux := newExposureSurfaceMux()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/market-indices", nil)
	if _, pattern := mux.Handler(req); pattern != "" {
		t.Errorf("GET /api/v1/market-indices resolved to %q; the exposure wiring must leave "+
			"that route to pricing.EscalatorHandler", pattern)
	}
}

// TestExposureShutdownIsNilSafe: main.go's shutdown path calls Shutdown
// unconditionally, so a wiring that never ran must not panic there.
func TestExposureShutdownIsNilSafe(t *testing.T) {
	var w *ExposureWiring
	w.Shutdown(t.Context())
	(&ExposureWiring{}).Shutdown(t.Context())
}
