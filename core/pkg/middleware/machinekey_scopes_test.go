// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package middleware_test

import (
	"testing"

	"github.com/gablelbm/gable/pkg/middleware"
)

// TestScopeTargetClassTable pins the policy table of ADR 0007 section 5.1:
// every route class resolves from its whole method and path to the module
// and class the table names, and the admitted scopes are exactly the row's.
func TestScopeTargetClassTable(t *testing.T) {
	cases := []struct {
		method   string
		path     string
		module   string
		class    middleware.ScopeClass
		admitted []string
	}{
		// entity read and write
		{"GET", "/api/v1/quotes", "quotes", middleware.ScopeEntityRead, []string{"quotes:read"}},
		{"HEAD", "/api/v1/quotes/{id}", "quotes", middleware.ScopeEntityRead, []string{"quotes:read"}},
		{"POST", "/api/v1/quotes", "quotes", middleware.ScopeEntityWrite, []string{"quotes:write"}},
		{"PUT", "/api/v1/quotes/{id}", "quotes", middleware.ScopeEntityWrite, []string{"quotes:write"}},
		{"DELETE", "/api/v1/products/{id}", "products", middleware.ScopeEntityWrite, []string{"products:write"}},
		// the finer names keep their place
		{"GET", "/api/v1/admin/settings/ai", "admin/settings", middleware.ScopeEntityRead, []string{"admin:settings"}},
		{"PUT", "/api/v1/admin/settings/ai", "admin/settings", middleware.ScopeEntityWrite, []string{"admin:settings"}},
		{"GET", "/api/v1/admin/exposure-scan", "admin", middleware.ScopeEntityRead, []string{"admin:read"}},
		{"POST", "/api/v1/users/{sub}/branches", "users", middleware.ScopeEntityWrite, []string{"users:grants"}},
		{"GET", "/api/v1/users", "users", middleware.ScopeEntityRead, []string{"users:read"}},
		// the seven draft shapes
		{"GET", "/api/v1/drafts/quotes", "quotes", middleware.ScopeDraftRead, []string{"quotes:propose", "quotes:commit"}},
		{"GET", "/api/v1/drafts/quotes/{id}", "quotes", middleware.ScopeDraftRead, []string{"quotes:propose", "quotes:commit"}},
		{"GET", "/api/v1/drafts/quotes/feed", "quotes", middleware.ScopeDraftRead, []string{"quotes:propose", "quotes:commit"}},
		{"POST", "/api/v1/drafts/quotes", "quotes", middleware.ScopeDraftWrite, []string{"quotes:propose", "quotes:commit"}},
		{"PUT", "/api/v1/drafts/quotes/{id}", "quotes", middleware.ScopeDraftWrite, []string{"quotes:propose", "quotes:commit"}},
		{"POST", "/api/v1/drafts/quotes/{id}/transitions", "quotes", middleware.ScopeDraftWrite, []string{"quotes:propose", "quotes:commit"}},
		{"POST", "/api/v1/drafts/quotes/{id}/promote", "quotes", middleware.ScopePromotion, []string{"quotes:commit"}},
		// the orders kind, the same seven shapes (ADR 0007 section 10)
		{"GET", "/api/v1/drafts/orders", "orders", middleware.ScopeDraftRead, []string{"orders:propose", "orders:commit"}},
		{"GET", "/api/v1/drafts/orders/{id}", "orders", middleware.ScopeDraftRead, []string{"orders:propose", "orders:commit"}},
		{"GET", "/api/v1/drafts/orders/feed", "orders", middleware.ScopeDraftRead, []string{"orders:propose", "orders:commit"}},
		{"POST", "/api/v1/drafts/orders", "orders", middleware.ScopeDraftWrite, []string{"orders:propose", "orders:commit"}},
		{"PUT", "/api/v1/drafts/orders/{id}", "orders", middleware.ScopeDraftWrite, []string{"orders:propose", "orders:commit"}},
		{"POST", "/api/v1/drafts/orders/{id}/transitions", "orders", middleware.ScopeDraftWrite, []string{"orders:propose", "orders:commit"}},
		{"POST", "/api/v1/drafts/orders/{id}/promote", "orders", middleware.ScopePromotion, []string{"orders:commit"}},
		// the link shapes: an entity link needs the module read scope, a
		// draft link the confirm verbs
		{"GET", "/api/v1/links/quotes/{id}", "quotes", middleware.ScopeLink, []string{"quotes:read"}},
		{"GET", "/api/v1/links/drafts/quotes/{id}", "quotes", middleware.ScopeDraftLink, []string{"quotes:propose", "quotes:commit"}},
		{"GET", "/api/v1/links/orders/{id}", "orders", middleware.ScopeLink, []string{"orders:read"}},
		{"GET", "/api/v1/links/drafts/orders/{id}", "orders", middleware.ScopeDraftLink, []string{"orders:propose", "orders:commit"}},
		{"GET", "/api/v1/links/invoices/{id}", "invoices", middleware.ScopeLink, []string{"invoices:read"}},
		{"GET", "/api/v1/links/customers/{id}", "customers", middleware.ScopeLink, []string{"customers:read"}},
		{"GET", "/api/v1/links/products/{id}", "products", middleware.ScopeLink, []string{"products:read"}},
	}
	for _, tc := range cases {
		module, class, ok := middleware.ScopeTarget(tc.method, tc.path)
		if !ok || module != tc.module || class != tc.class {
			t.Errorf("ScopeTarget(%s %s) = (%q, %s, %v), want (%q, %s, true)", tc.method, tc.path, module, class, ok, tc.module, tc.class)
			continue
		}
		admitted := middleware.AdmittedScopes(module, class)
		if len(admitted) != len(tc.admitted) {
			t.Errorf("AdmittedScopes(%s %s) = %v, want %v", tc.method, tc.path, admitted, tc.admitted)
			continue
		}
		for i := range admitted {
			if admitted[i] != tc.admitted[i] {
				t.Errorf("AdmittedScopes(%s %s) = %v, want %v", tc.method, tc.path, admitted, tc.admitted)
				break
			}
		}
	}
}

// TestScopeTargetFailsClosed pins the refusals: any method and shape under
// the delegating segments the policy table does not name, a path outside
// /api/v1, and doubled slashes or dot segments resolve to nothing, so a key
// is refused there (the router's own cleaning redirects the dirty spellings
// for every caller, and the auth layer never admits one).
func TestScopeTargetFailsClosed(t *testing.T) {
	refused := []struct{ method, path string }{
		{"GET", "/api/v1/drafts/quotes/{id}/file"},               // a future route shape nobody named
		{"POST", "/api/v1/drafts/quotes/{id}"},                   // POST to the read path
		{"DELETE", "/api/v1/drafts/quotes/{id}"},                 // no draft delete shape
		{"GET", "/api/v1/drafts/quotes/{id}/promote"},            // promotion is POST
		{"POST", "/api/v1/drafts/quotes/{id}/publish"},           // an unlisted subroute
		{"GET", "/api/v1/drafts/quotes/{id}/transitions"},        // transitions are POST
		{"PATCH", "/api/v1/drafts/quotes/{id}"},                  // no patch shape
		{"POST", "/api/v1/drafts/quotes/feed"},                   // the feed is GET
		{"GET", "/api/v1/drafts"},                                // no module named
		{"GET", "/api/v1/links"},                                 // no module named
		{"GET", "/api/v1/links/quotes"},                          // no id named
		{"POST", "/api/v1/links/quotes/{id}"},                    // links are GET
		{"GET", "/api/v1/links/drafts/quotes"},                   // no id named
		{"GET", "/api/v1/links/drafts/{id}"},                     // no module named
		{"GET", "/api/v1/admin"},                                 // no area named
		{"GET", "/api/health"},                                   // not a module route
		{"GET", "//api/v1/quotes"},                               // doubled slash
		{"GET", "/api/v1//quotes"},                               // doubled slash
		{"GET", "/api/v1/drafts//quotes/{id}"},                   // doubled slash
		{"GET", "/api/v1/drafts/../quotes"},                      // dot segment
		{"GET", "/api/v1/admin/../quotes/{id}"},                  // dot segment
		{"POST", "/api/v1/drafts/quotes/{id}/transitions/extra"}, // deeper than any shape
	}
	for _, tc := range refused {
		if _, _, ok := middleware.ScopeTarget(tc.method, tc.path); ok {
			t.Errorf("ScopeTarget(%s %s) resolved, want the fail-closed refusal", tc.method, tc.path)
		}
	}
}
