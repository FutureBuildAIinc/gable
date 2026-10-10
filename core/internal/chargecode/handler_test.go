// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package chargecode

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

// tagGuard answers for the route itself, naming which guard held it, so the
// test needs no service and no database.
func tagGuard(name string) func(http.Handler) http.Handler {
	return func(http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("X-Guard", name)
			w.WriteHeader(http.StatusForbidden)
		})
	}
}

// The serve wiring passes a read guard (admin, owner, sales, finance) and a
// write guard (admin, owner, finance). The writes must take the second, or a
// sales user creates and edits the master.
func TestRegisterRoutesWritesTakeTheWriteGuard(t *testing.T) {
	mux := http.NewServeMux()
	NewHandler(nil).RegisterRoutes(mux, tagGuard("read"), tagGuard("write"))

	const id = "00000000-0000-0000-0000-000000000001"
	for _, c := range []struct{ method, path, want string }{
		{"GET", "/api/v1/charge-codes", "read"},
		{"GET", "/api/v1/charge-codes/" + id, "read"},
		{"POST", "/api/v1/charge-codes", "write"},
		{"PUT", "/api/v1/charge-codes/" + id, "write"},
	} {
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, httptest.NewRequest(c.method, c.path, nil))
		if got := rec.Header().Get("X-Guard"); got != c.want {
			t.Errorf("%s %s: held by guard %q, want %q", c.method, c.path, got, c.want)
		}
	}
}

// With one guard it holds every route, reads and writes alike.
func TestRegisterRoutesOneGuardHoldsEveryRoute(t *testing.T) {
	mux := http.NewServeMux()
	NewHandler(nil).RegisterRoutes(mux, tagGuard("only"))

	const id = "00000000-0000-0000-0000-000000000001"
	for _, c := range []struct{ method, path string }{
		{"GET", "/api/v1/charge-codes"},
		{"GET", "/api/v1/charge-codes/" + id},
		{"POST", "/api/v1/charge-codes"},
		{"PUT", "/api/v1/charge-codes/" + id},
	} {
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, httptest.NewRequest(c.method, c.path, nil))
		if got := rec.Header().Get("X-Guard"); got != "only" {
			t.Errorf("%s %s: held by guard %q, want %q", c.method, c.path, got, "only")
		}
	}
}
