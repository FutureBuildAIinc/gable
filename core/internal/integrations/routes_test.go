// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package integrations

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// A wrapped route keeps its auth outermost: the X-Integration-Key check runs
// before anything the wrap carries (the idempotency layer claims keys only
// for callers that passed auth), and a caller that passes auth reaches the
// wrapper before the route handler.
func TestRegisterRoutes_WrapRunsInsideAuth(t *testing.T) {
	handler := NewHandler(nil, nil, nil, nil, nil, nil, "test-key")

	var wrapped, handled int
	wrap := func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			wrapped++
			next.ServeHTTP(w, r)
		})
	}
	mux := http.NewServeMux()
	// validate-staff answers without touching the store seams, so it works
	// over httptest with no Postgres: a bad body is enough to prove ordering.
	handler.RegisterRoutes(mux, wrap)

	// A caller with the wrong key is refused by auth; the wrap never runs.
	bad := httptest.NewRequest(http.MethodPost, "/api/integration/validate-staff", strings.NewReader(`{}`))
	bad.Header.Set("X-Integration-Key", "wrong")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, bad)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("wrong key: status = %d, want 401", rec.Code)
	}
	if wrapped != 0 {
		t.Fatalf("wrap ran %d times before auth passed, want 0 (auth is outermost)", wrapped)
	}

	// A caller with the right key reaches the wrap, then the handler.
	good := httptest.NewRequest(http.MethodPost, "/api/integration/validate-staff", strings.NewReader(`{}`))
	good.Header.Set("X-Integration-Key", "test-key")
	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, good)
	if wrapped != 1 || handled != 0 {
		t.Fatalf("after a valid key: wrap runs = %d handler runs = %d, want 1 and the handler to have run", wrapped, handled)
	}
	if rec.Code == http.StatusUnauthorized {
		t.Fatalf("valid key: still unauthorized")
	}

	// Without a wrap, routes register as before.
	plain := NewHandler(nil, nil, nil, nil, nil, nil, "test-key")
	plainMux := http.NewServeMux()
	plain.RegisterRoutes(plainMux)
	plainReq := httptest.NewRequest(http.MethodPost, "/api/integration/validate-staff", strings.NewReader(`{}`))
	plainReq.Header.Set("X-Integration-Key", "test-key")
	rec = httptest.NewRecorder()
	plainMux.ServeHTTP(rec, plainReq)
	if rec.Code == http.StatusUnauthorized {
		t.Fatalf("unwrapped route: valid key refused")
	}
}
