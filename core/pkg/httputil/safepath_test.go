// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package httputil

import (
	"bytes"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestSafePath(t *testing.T) {
	tok := strings.Repeat("Ab3_-", 8) + "xyz"
	cases := map[string]string{
		"/api/public/v1/customer-quotes/" + tok:                 "/api/public/v1/customer-quotes/:token",
		"/api/public/v1/customer-quotes/" + tok + "/accept":     "/api/public/v1/customer-quotes/:token/accept",
		"/api/public/v1/customer-quotes/" + tok + "/selections": "/api/public/v1/customer-quotes/:token/selections",
		"/api/public/v1/customer-quotes/":                       "/api/public/v1/customer-quotes/",
		"/api/public/v1/dealer-quotes/" + tok:                   "/api/public/v1/dealer-quotes/:token",
		"/api/public/v1/dealer-quotes/" + tok + "/accept":       "/api/public/v1/dealer-quotes/:token/accept",
		"/api/public/v1/customer-quotes//" + tok:                "/api/public/v1/customer-quotes/:token",
		"/api/public/v1/./customer-quotes/" + tok:               "/api/public/v1/customer-quotes/:token",
		"/api/public/v1/other/" + tok + "/view":                 "/api/public/v1/other/:token/view",
		"/api/portal/v1/orders/123":                             "/api/portal/v1/orders/123",
		"/api/v1/orders":                                        "/api/v1/orders",
		"":                                                      "",
	}
	for in, want := range cases {
		if got := SafePath(in); got != want {
			t.Errorf("SafePath(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestRespondError_NeverLogsACapabilityToken(t *testing.T) {
	var buf bytes.Buffer
	old := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, nil)))
	defer slog.SetDefault(old)

	tok := strings.Repeat("Qz9_-", 8) + "abc"
	r := httptest.NewRequest(http.MethodGet, "/api/public/v1/customer-quotes/"+tok, nil)
	RespondError(httptest.NewRecorder(), r, "not found", http.StatusNotFound, nil)
	if strings.Contains(buf.String(), tok) {
		t.Fatalf("the token reached the log: %s", buf.String())
	}
	if !strings.Contains(buf.String(), ":token") {
		t.Fatalf("the log line lost the route: %s", buf.String())
	}
}
