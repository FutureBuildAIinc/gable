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

func TestRateLimit_IPv6ClientsShareTheirSlash64(t *testing.T) {
	h := RateLimit(3, clientip.Trusted{})(okHandler())
	var limited int
	for i := 0; i < 8; i++ {
		// A caller rotating the low 64 bits inside its own /64.
		if hit(h, "[2001:db8:1:2::"+strconv.Itoa(i+1)+"]:5000", "") == http.StatusTooManyRequests {
			limited++
		}
	}
	if limited != 5 {
		t.Fatalf("addresses inside one /64 must share a budget: limited=%d want 5", limited)
	}
	// Another /64 and an IPv4 caller each have their own.
	if c := hit(h, "[2001:db8:1:3::1]:5000", ""); c != http.StatusOK {
		t.Fatalf("other /64: %d", c)
	}
	if c := hit(h, "203.0.113.9:5000", ""); c != http.StatusOK {
		t.Fatalf("ipv4: %d", c)
	}
}

func TestRateLimit_IPv4MappedIPv6SharesIPv4Budget(t *testing.T) {
	h := RateLimit(1, clientip.Trusted{})(okHandler())
	if c := hit(h, "203.0.113.9:1", ""); c != http.StatusOK {
		t.Fatal(c)
	}
	if c := hit(h, "[::ffff:203.0.113.9]:1", ""); c != http.StatusTooManyRequests {
		t.Fatalf("mapped form of the same host: %d", c)
	}
}
