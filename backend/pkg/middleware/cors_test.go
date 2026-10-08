// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package middleware

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

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
