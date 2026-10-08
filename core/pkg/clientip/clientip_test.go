// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package clientip

import (
	"bytes"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func req(remote string, xff ...string) *http.Request {
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	r.RemoteAddr = remote
	for _, v := range xff {
		r.Header.Add("X-Forwarded-For", v)
	}
	return r
}

func mustParse(t *testing.T, s string) Trusted {
	t.Helper()
	tr, err := Parse(s)
	if err != nil {
		t.Fatalf("Parse(%q): %v", s, err)
	}
	return tr
}

func TestParse(t *testing.T) {
	for _, s := range []string{"", "  ", "10.0.0.0/8", "10.0.0.0/8, 192.168.1.1", "fd00::/8,::1", "10.0.0.0/8,,"} {
		if _, err := Parse(s); err != nil {
			t.Errorf("Parse(%q) unexpected error: %v", s, err)
		}
	}
	for _, s := range []string{"nonsense", "10.0.0.0/33", "10.0.0.0/8,bad", "10.0.0.1:80"} {
		if _, err := Parse(s); err == nil {
			t.Errorf("Parse(%q) must fail", s)
		}
	}
}

func TestDirectCallerSpoofingSharesOneBudget(t *testing.T) {
	var none Trusted // empty default: trust no forwarding header
	a := none.Of(req("203.0.113.9:4000", "1.1.1.1"))
	b := none.Of(req("203.0.113.9:4001", "2.2.2.2"))
	c := none.Of(req("203.0.113.9:4002", "9.9.9.9, 8.8.8.8"))
	if a != "203.0.113.9" || b != a || c != a {
		t.Fatalf("spoofed headers must be ignored: %q %q %q", a, b, c)
	}
}

func TestUntrustedPeerCannotSpoofEvenWhenOthersTrusted(t *testing.T) {
	tr := mustParse(t, "10.0.0.0/8")
	if got := tr.Of(req("203.0.113.9:1", "1.2.3.4")); got != "203.0.113.9" {
		t.Fatalf("got %q", got)
	}
}

func TestBehindTrustedProxyUsesRealClient(t *testing.T) {
	tr := mustParse(t, "10.0.0.0/8")
	if got := tr.Of(req("10.1.2.3:5555", "198.51.100.7")); got != "198.51.100.7" {
		t.Fatalf("got %q", got)
	}
	// The client's own forged leftmost entry is not the answer: the proxy appended the real peer.
	if got := tr.Of(req("10.1.2.3:5555", "6.6.6.6, 198.51.100.7")); got != "198.51.100.7" {
		t.Fatalf("forged leftmost must lose, got %q", got)
	}
}

func TestChainSkipsTrustedHopsFromTheRight(t *testing.T) {
	tr := mustParse(t, "10.0.0.0/8,172.16.0.0/12")
	got := tr.Of(req("10.0.0.1:1", "198.51.100.7, 172.16.0.5, 10.9.9.9"))
	if got != "198.51.100.7" {
		t.Fatalf("got %q", got)
	}
}

func TestUntrustedHopInTheMiddleStopsTheWalk(t *testing.T) {
	tr := mustParse(t, "10.0.0.0/8")
	// 203.0.113.50 is untrusted and is the first untrusted address from the right.
	got := tr.Of(req("10.0.0.1:1", "6.6.6.6, 203.0.113.50, 10.2.2.2"))
	if got != "203.0.113.50" {
		t.Fatalf("got %q", got)
	}
}

func TestMultipleHeaderLinesAreOneChain(t *testing.T) {
	tr := mustParse(t, "10.0.0.0/8")
	got := tr.Of(req("10.0.0.1:1", "6.6.6.6", "198.51.100.7, 10.3.3.3"))
	if got != "198.51.100.7" {
		t.Fatalf("got %q", got)
	}
}

func TestAllHopsTrustedFallsBackToPeer(t *testing.T) {
	tr := mustParse(t, "10.0.0.0/8")
	if got := tr.Of(req("10.0.0.1:1", "10.5.5.5, 10.6.6.6")); got != "10.0.0.1" {
		t.Fatalf("got %q", got)
	}
}

func TestIPv6(t *testing.T) {
	tr := mustParse(t, "fd00::/8,::1")
	if got := tr.Of(req("[fd00::1]:443", "2001:db8::7")); got != "2001:db8::7" {
		t.Fatalf("got %q", got)
	}
	if got := tr.Of(req("[::1]:443", "2001:db8::7, fd00::9")); got != "2001:db8::7" {
		t.Fatalf("got %q", got)
	}
	var none Trusted
	if got := none.Of(req("[2001:db8::1]:80", "1.1.1.1")); got != "2001:db8::1" {
		t.Fatalf("got %q", got)
	}
	// IPv4-mapped peer matches an IPv4 trusted network.
	t4 := mustParse(t, "10.0.0.0/8")
	if got := t4.Of(req("[::ffff:10.0.0.1]:80", "198.51.100.7")); got != "198.51.100.7" {
		t.Fatalf("mapped peer: got %q", got)
	}
}

func TestMalformedEntriesAreIgnored(t *testing.T) {
	tr := mustParse(t, "10.0.0.0/8")
	cases := []struct{ xff, want string }{
		{"garbage", "10.0.0.1"},
		{"198.51.100.7, garbage", "198.51.100.7"},
		{"198.51.100.7, , ,", "198.51.100.7"},
		{"198.51.100.7, 999.1.1.1", "198.51.100.7"},
		{"198.51.100.7:8080", "198.51.100.7"},
		{"[2001:db8::7]:99", "2001:db8::7"},
		{"unknown", "10.0.0.1"},
		{"198.51.100.7, <script>", "198.51.100.7"},
	}
	for _, c := range cases {
		if got := tr.Of(req("10.0.0.1:1", c.xff)); got != c.want {
			t.Errorf("xff %q: got %q want %q", c.xff, got, c.want)
		}
	}
}

func TestOddRemoteAddr(t *testing.T) {
	var none Trusted
	if got := none.Of(req("203.0.113.9")); got != "203.0.113.9" {
		t.Fatalf("no port: got %q", got)
	}
	if got := none.Of(req("")); got != "" {
		t.Fatalf("empty: got %q", got)
	}
	// An unparseable peer is never trusted.
	tr := mustParse(t, "10.0.0.0/8")
	if got := tr.Of(req("weird", "1.1.1.1")); got != "weird" {
		t.Fatalf("got %q", got)
	}
}

func TestXRealIPIsNeverTrusted(t *testing.T) {
	tr := mustParse(t, "10.0.0.0/8")
	r := req("10.0.0.1:1")
	r.Header.Set("X-Real-IP", "1.2.3.4")
	if got := tr.Of(r); got != "10.0.0.1" {
		t.Fatalf("got %q", got)
	}
}

func TestParseRejectsShortMappedPrefixAndZones(t *testing.T) {
	for _, bad := range []string{"::ffff:0:0/0", "::ffff:0:0/95", "fe80::1%eth0/64", "fe80::1%eth0", "10.0.0.0/8,fe80::%1/64"} {
		if _, err := Parse(bad); err == nil {
			t.Errorf("Parse(%q) accepted", bad)
		}
	}
	if _, err := Parse("::ffff:10.0.0.0/104"); err != nil {
		t.Fatalf("a /104 mapped prefix is a valid IPv4 /8: %v", err)
	}
}

func TestForwardingWatchWarnsOnceForUntrustedPeer(t *testing.T) {
	var buf bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&buf, nil))
	tr := mustParse(t, "10.0.0.0/8")
	now := time.Unix(1000, 0)
	w := NewForwardingWatch(logger, tr)
	w.now = func() time.Time { return now }

	// Trusted peer, no header from an untrusted peer, and an untrusted peer
	// without the header: silent.
	w.Observe(req("10.0.0.1:1", "1.1.1.1"))
	w.Observe(req("203.0.113.9:1"))
	if buf.Len() != 0 {
		t.Fatalf("unexpected log: %s", buf.String())
	}

	w.Observe(req("203.0.113.9:1", "9.9.9.9, 8.8.8.8", "7.7.7.7"))
	w.Observe(req("203.0.113.10:1", "9.9.9.9"))
	out := buf.String()
	if n := strings.Count(out, "level=WARN"); n != 1 {
		t.Fatalf("want one warning, got %d: %s", n, out)
	}
	if !strings.Contains(out, "level=WARN") || !strings.Contains(out, "203.0.113.9") || !strings.Contains(out, "hops=3") {
		t.Fatalf("warning lacks level, peer or hop count: %s", out)
	}
	for _, leak := range []string{"9.9.9.9", "8.8.8.8", "7.7.7.7"} {
		if strings.Contains(out, leak) {
			t.Fatalf("warning leaks header contents %q: %s", leak, out)
		}
	}

	// Again after the hour.
	buf.Reset()
	now = now.Add(time.Hour + time.Second)
	w.Observe(req("203.0.113.10:1", "9.9.9.9"))
	if !strings.Contains(buf.String(), "203.0.113.10") {
		t.Fatalf("no second warning after an hour: %s", buf.String())
	}
}

func TestLogConfigured(t *testing.T) {
	var buf bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&buf, nil))
	Trusted(nil).LogConfigured(logger)
	if !strings.Contains(buf.String(), "level=INFO") || !strings.Contains(buf.String(), "empty") {
		t.Fatalf("empty: %s", buf.String())
	}
	buf.Reset()
	mustParse(t, "10.0.0.0/8,fd00::/8").LogConfigured(logger)
	if !strings.Contains(buf.String(), "networks=2") {
		t.Fatalf("set: %s", buf.String())
	}
}
