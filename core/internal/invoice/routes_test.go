// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package invoice_test

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gablelbm/gable/internal/invoice"
	"github.com/gablelbm/gable/pkg/middleware"
)

// ADR 0002: the first path segment of every route of the module is a module the
// machine key vocabulary names, so a key holding invoices:read or
// credit-memos:write reaches it and any other key is a 403.
func TestMachineKeyVocabularyNamesTheInvoiceAndCreditMemoRoutes(t *testing.T) {
	for _, module := range []string{"invoices", "credit-memos"} {
		if middleware.ModuleScopePolicyFor(module) != middleware.ModuleScopeAllowed {
			t.Errorf("%s is not in the machine key scope vocabulary", module)
		}
	}
}

// Every route sits behind the guard the module is registered with.
func TestRegisterRoutesGuardsEveryRoute(t *testing.T) {
	h := invoice.NewHandler(invoice.NewService(nil, nil, nil, nil))
	seen := map[string]bool{}
	guard := func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			seen[r.Method+" "+r.URL.Path] = true
			w.WriteHeader(http.StatusTeapot)
		})
	}
	mux := http.NewServeMux()
	h.RegisterRoutes(mux, guard)
	const id = "8f14e45f-ceea-467f-a830-aacd11a4b3c1"
	routes := []struct{ method, path string }{
		{"GET", "/api/v1/invoices"}, {"GET", "/api/v1/invoices/" + id}, {"POST", "/api/v1/invoices/" + id + "/transitions"},
		{"GET", "/api/v1/credit-memos"}, {"POST", "/api/v1/credit-memos"}, {"GET", "/api/v1/credit-memos/" + id},
		{"PUT", "/api/v1/credit-memos/" + id}, {"POST", "/api/v1/credit-memos/" + id + "/transitions"},
	}
	for _, r := range routes {
		req := httptest.NewRequest(r.method, r.path, nil)
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		if rec.Code != http.StatusTeapot || !seen[r.method+" "+r.path] {
			t.Errorf("%s %s did not reach the guard (status %d)", r.method, r.path, rec.Code)
		}
	}
}
