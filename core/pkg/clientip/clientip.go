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
	"net"
	"net/http"
	"net/netip"
	"strings"
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
		if p, err := netip.ParsePrefix(part); err == nil {
			out = append(out, netip.PrefixFrom(p.Addr().Unmap(), unmapBits(p)).Masked())
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
