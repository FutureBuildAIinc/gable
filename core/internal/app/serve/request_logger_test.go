// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package serve

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gablelbm/gable/pkg/clientip"
)

func logged(t *testing.T, trusted clientip.Trusted, remote, xff string) (remoteAddr, all string) {
	t.Helper()
	var buf bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&buf, nil))
	h := RequestLogger(logger, trusted, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {}))
	r := httptest.NewRequest(http.MethodGet, "/x", nil)
	r.RemoteAddr = remote
	r.Header.Set("X-Forwarded-For", xff)
	h.ServeHTTP(httptest.NewRecorder(), r)
	for _, line := range strings.Split(strings.TrimSpace(buf.String()), "\n") {
		var rec map[string]any
		if err := json.Unmarshal([]byte(line), &rec); err != nil {
			t.Fatal(err)
		}
		if v, ok := rec["remote_addr"].(string); ok {
			remoteAddr = v
		}
	}
	return remoteAddr, buf.String()
}

func TestRequestLoggerRemoteAddrFollowsTrustedProxy(t *testing.T) {
	trusted, err := clientip.Parse("10.0.0.0/8")
	if err != nil {
		t.Fatal(err)
	}
	got, out := logged(t, trusted, "10.0.0.1:4000", "198.51.100.7")
	if got != "198.51.100.7" {
		t.Fatalf("trusted peer: remote_addr=%q want the forwarded client", got)
	}
	if strings.Contains(out, "TRUSTED_PROXIES") {
		t.Fatalf("a trusted peer must not warn: %s", out)
	}

	got, out = logged(t, trusted, "203.0.113.9:4000", "198.51.100.7")
	if got != "203.0.113.9" {
		t.Fatalf("untrusted peer: remote_addr=%q want the peer", got)
	}
	if !strings.Contains(out, "TRUSTED_PROXIES") {
		t.Fatalf("an untrusted peer sending X-Forwarded-For must warn: %s", out)
	}
}
