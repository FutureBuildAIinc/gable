// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package middleware

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// A preflight must advertise every method the idempotency middleware claims:
// PATCH joins POST and PUT as a participating method, so a browser retrying a
// keyed PATCH needs it in Access-Control-Allow-Methods or the preflight
// blocks the request before it reaches the API.
func TestCORS_AllowMethodsAdvertisePatch(t *testing.T) {
	h := CORSMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Fatal("a preflight must be answered by the CORS middleware itself")
	}))
	r := httptest.NewRequest(http.MethodOptions, "/api/v1/quotes", nil)
	r.Header.Set("Origin", "http://localhost:5173")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != http.StatusNoContent {
		t.Fatalf("preflight: status = %d, want %d", w.Code, http.StatusNoContent)
	}
	methods := w.Header().Get("Access-Control-Allow-Methods")
	for _, want := range []string{"GET", "POST", "PUT", "PATCH", "DELETE", "OPTIONS"} {
		if !strings.Contains(methods, want) {
			t.Fatalf("Access-Control-Allow-Methods = %q, want %s listed", methods, want)
		}
	}
}

func TestCORSPreflightAllowsAgentIdentityHeaders(t *testing.T) {
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodOptions, "/api/v1/orders", nil)
	req.Header.Set("Origin", "http://localhost:5173")
	req.Header.Set("Access-Control-Request-Method", http.MethodPost)
	req.Header.Set("Access-Control-Request-Headers", "Content-Type, Authorization, X-Acting-As, X-Agent-Tool")

	CORSMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("preflight must not reach the handler")
	})).ServeHTTP(rec, req)

	allow := rec.Header().Get("Access-Control-Allow-Headers")
	for _, name := range []string{"X-Acting-As", "X-Agent-Tool"} {
		if !strings.Contains(allow, name) {
			t.Errorf("Access-Control-Allow-Headers = %q, want it to list %s: a browser-hosted agentic UI on another origin fails preflight without it", allow, name)
		}
	}
}
