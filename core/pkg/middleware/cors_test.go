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
