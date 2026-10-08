// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

// Package clientip resolves the address of the caller of an HTTP request.
//
// A forwarding header (X-Forwarded-For) is written by whoever sends it, so it
// is believed only when the connection itself came from a proxy the operator
// named. The zero value of Trusted names none: every request is attributed to
// its TCP peer and every forwarding header is ignored.
package clientip

import (
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/netip"
	"strings"
	"sync"
	"time"
)

// Trusted is the set of proxy networks whose forwarding headers are believed.
// The zero value trusts nothing.
type Trusted []netip.Prefix

// Parse reads a comma separated list of CIDRs (a bare address means a single
// host). An empty string yields the empty set. A malformed entry is an error,
// so a typo in configuration fails at boot rather than silently trusting
// nothing, or something broader than intended.
func Parse(s string) (Trusted, error) {
	var out Trusted
	for _, part := range strings.Split(s, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		if strings.Contains(part, "%") {
			return nil, fmt.Errorf("invalid trusted proxy %q: zone identifiers are not allowed", part)
		}
		if p, err := netip.ParsePrefix(part); err == nil {
			bits := unmapBits(p)
			if bits < 0 {
				return nil, fmt.Errorf("invalid trusted proxy %q: an IPv4-mapped IPv6 prefix must be at least /96", part)
			}
			out = append(out, netip.PrefixFrom(p.Addr().Unmap(), bits).Masked())
			continue
		}
		a, err := netip.ParseAddr(part)
		if err != nil {
			return nil, fmt.Errorf("invalid trusted proxy %q: want a CIDR or an IP address", part)
		}
		a = a.Unmap()
		out = append(out, netip.PrefixFrom(a, a.BitLen()))
	}
	return out, nil
}

// unmapBits keeps the prefix length meaningful when an IPv4-mapped IPv6
// prefix (::ffff:a.b.c.d/n) is unmapped.
func unmapBits(p netip.Prefix) int {
	if p.Addr().Is4In6() {
		return p.Bits() - 96
	}
	return p.Bits()
}

// LogConfigured states at INFO whether any proxy network is trusted, so an
// operator can see at boot that the setting is empty or how many it holds.
func (t Trusted) LogConfigured(logger *slog.Logger) {
	if len(t) == 0 {
		logger.Info("TRUSTED_PROXIES is empty: the TCP peer is the client and X-Forwarded-For is ignored")
		return
	}
	logger.Info("TRUSTED_PROXIES set: X-Forwarded-For is read from these peers only", "networks", len(t))
}

// ForwardingWatch warns when X-Forwarded-For arrives from a peer outside the
// trusted networks, the sign of a server behind a proxy that TRUSTED_PROXIES
// does not name (every caller then shares the proxy's rate budget). It warns
// on the first such request and then at most once an hour.
type ForwardingWatch struct {
	logger  *slog.Logger
	trusted Trusted
	now     func() time.Time

	mu   sync.Mutex
	last time.Time
	warn bool
}

const forwardingWarnEvery = time.Hour

func NewForwardingWatch(logger *slog.Logger, trusted Trusted) *ForwardingWatch {
	return &ForwardingWatch{logger: logger, trusted: trusted, now: time.Now}
}

// Observe inspects r. The log names the setting and the peer and carries the
// hop count of the header, never its contents.
func (w *ForwardingWatch) Observe(r *http.Request) {
	lines := r.Header.Values("X-Forwarded-For")
	if len(lines) == 0 {
		return
	}
	peer := peerOf(r.RemoteAddr)
	if pa, ok := parseAddr(peer); ok && w.trusted.contains(pa) {
		return
	}
	w.mu.Lock()
	now := w.now()
	if w.warn && now.Sub(w.last) < forwardingWarnEvery {
		w.mu.Unlock()
		return
	}
	w.warn, w.last = true, now
	w.mu.Unlock()
	hops := 0
	for _, l := range lines {
		hops += strings.Count(l, ",") + 1
	}
	w.logger.Warn("X-Forwarded-For arrived from a peer outside TRUSTED_PROXIES and is ignored; behind a proxy, set TRUSTED_PROXIES to its network or every caller shares one rate budget",
		"setting", "TRUSTED_PROXIES", "peer", peer, "hops", hops)
}

func (t Trusted) contains(a netip.Addr) bool {
	for _, p := range t {
		if p.Contains(a) {
			return true
		}
	}
	return false
}

// Of returns the client address of r. It is the TCP peer unless the peer is
// inside a trusted network, in which case X-Forwarded-For (all header lines,
// read as one chain) is walked from the right, skipping trusted hops, and the
// first untrusted address wins (the rightmost untrusted rule). Entries that
// do not parse are ignored; when no usable untrusted entry exists the peer is
// the answer. X-Real-IP is never read.
func (t Trusted) Of(r *http.Request) string {
	peer := peerOf(r.RemoteAddr)
	pa, ok := parseAddr(peer)
	if !ok || len(t) == 0 || !t.contains(pa) {
		return peer
	}
	var hops []string
	for _, line := range r.Header.Values("X-Forwarded-For") {
		hops = append(hops, strings.Split(line, ",")...)
	}
	for i := len(hops) - 1; i >= 0; i-- {
		a, ok := parseAddr(strings.TrimSpace(hops[i]))
		if !ok {
			continue
		}
		if t.contains(a) {
			continue
		}
		return a.String()
	}
	return peer
}

func peerOf(remoteAddr string) string {
	host, _, err := net.SplitHostPort(remoteAddr)
	if err != nil {
		return remoteAddr
	}
	return host
}

// parseAddr reads a bare IP or an IP with a port, in either family.
func parseAddr(s string) (netip.Addr, bool) {
	if a, err := netip.ParseAddr(s); err == nil {
		return a.Unmap().WithZone(""), true
	}
	if ap, err := netip.ParseAddrPort(s); err == nil {
		return ap.Addr().Unmap().WithZone(""), true
	}
	return netip.Addr{}, false
}
