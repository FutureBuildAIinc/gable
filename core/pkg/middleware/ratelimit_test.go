// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package middleware

import (
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"

	"github.com/gablelbm/gable/pkg/clientip"
)

func okHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })
}

func hit(h http.Handler, remote, xff string) int {
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	r.RemoteAddr = remote
	if xff != "" {
		r.Header.Set("X-Forwarded-For", xff)
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w.Code
}

func TestRateLimit_SpoofedForwardingHeaderDoesNotMintBudget(t *testing.T) {
	h := RateLimit(3, clientip.Trusted{})(okHandler())
	var limited int
	for i := 0; i < 10; i++ {
		if hit(h, "203.0.113.9:5000", "7.7.7."+strconv.Itoa(i)) == http.StatusTooManyRequests {
			limited++
		}
	}
	if limited != 7 {
		t.Fatalf("a direct caller varying X-Forwarded-For must share one budget: limited=%d want 7", limited)
	}
}

func TestRateLimit_BehindTrustedProxyKeysOnRealClient(t *testing.T) {
	tr, err := clientip.Parse("10.0.0.0/8")
	if err != nil {
		t.Fatal(err)
	}
	h := RateLimit(2, tr)(okHandler())
	for i := 0; i < 2; i++ {
		if c := hit(h, "10.0.0.1:1", "198.51.100.1"); c != http.StatusOK {
			t.Fatalf("client A request %d: %d", i, c)
		}
	}
	if c := hit(h, "10.0.0.1:1", "198.51.100.1"); c != http.StatusTooManyRequests {
		t.Fatalf("client A third request: %d", c)
	}
	// A different real client behind the same proxy has its own budget.
	if c := hit(h, "10.0.0.1:1", "198.51.100.2"); c != http.StatusOK {
		t.Fatalf("client B: %d", c)
	}
}
