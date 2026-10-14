// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package marketcheck

import (
	"errors"
	"net"
	"net/http"
	"syscall"
	"time"
)

// errDialRefused marks a connection the guard refused; callBatch logs it as
// blocked_address and never keeps the transport's text (it holds the address).
var errDialRefused = errors.New("marketcheck: address refused")

var (
	cgnat       = &net.IPNet{IP: net.IPv4(100, 64, 0, 0), Mask: net.CIDRMask(10, 32)}
	awsIPv6Meta = net.ParseIP("fd00:ec2::254")
)

// isPrivateOrLoopback reports the ranges a self hosted FB Connect lives in.
func isPrivateOrLoopback(ip net.IP) bool {
	return ip.IsLoopback() || ip.IsPrivate() || cgnat.Contains(ip)
}

// allowPrivateFor decides, from the stored address's own host, whether
// private and loopback ranges may be dialled: only when that host is itself
// a loopback or private literal (or localhost). A public name that resolves
// inward is refused, which is also what defeats DNS rebinding.
func allowPrivateFor(host string) bool {
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && isPrivateOrLoopback(ip)
}

// checkDialAddress judges one resolved "ip:port". Link local (the cloud
// metadata address among them), unspecified and multicast addresses are
// refused always; loopback, private and CGNAT only when allowPrivate is false.
func checkDialAddress(addr string, allowPrivate bool) error {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return errDialRefused
	}
	ip := net.ParseIP(host)
	if ip == nil {
		return errDialRefused
	}
	if ip.IsUnspecified() || ip.IsMulticast() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() ||
		ip.IsInterfaceLocalMulticast() || ip.Equal(awsIPv6Meta) {
		return errDialRefused
	}
	if !allowPrivate && isPrivateOrLoopback(ip) {
		return errDialRefused
	}
	return nil
}

// newGuardedClient is the run's HTTP client: no environment proxy, a dial
// time guard (after resolution, per connection), and no redirects followed.
func newGuardedClient(allowPrivate bool) *http.Client {
	dialer := &net.Dialer{
		Timeout: 10 * time.Second,
		Control: func(_, address string, _ syscall.RawConn) error {
			return checkDialAddress(address, allowPrivate)
		},
	}
	return &http.Client{
		Transport: &http.Transport{
			Proxy:                 nil,
			DialContext:           dialer.DialContext,
			TLSHandshakeTimeout:   10 * time.Second,
			ResponseHeaderTimeout: 0,
			DisableKeepAlives:     true,
		},
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
}
