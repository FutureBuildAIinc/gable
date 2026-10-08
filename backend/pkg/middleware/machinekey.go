// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package middleware

import (
	"net/http"
	"sort"
	"strings"
)

// Machine keys. A Bearer token whose shape is a machine key (minted by
// internal/techadmin GenerateKey as "sk_live_" plus base64url; a JWT is three
// dot separated base64url segments and never carries this prefix) authenticates
// against the api_keys table through techadmin.ValidateKey and carries per
// module read and write scopes. The wire ADR's seam rules apply to it: it is a
// principal on the /api/v1 module routes only. See docs/adr/0002-machine-keys.md.

// machineKeyPrefix is the prefix every GenerateKey key carries. The shape
// check is on the prefix only; ValidateKey does the real work (length, hash
// comparison, revocation).
const machineKeyPrefix = "sk_live_"

// IsMachineKey reports whether a Bearer token is a machine key by its shape:
// the sk_live_ prefix GenerateKey mints. A JWT (three dot separated segments,
// typically beginning "eyJ") never matches, so the dispatch between the two
// Bearer credential kinds is exact.
func IsMachineKey(token string) bool {
	return len(token) > len(machineKeyPrefix) && strings.HasPrefix(token, machineKeyPrefix)
}

// Scope suffixes. A module's read scope admits GET and HEAD; its write scope
// admits every other method.
const (
	scopeReadSuffix  = ":read"
	scopeWriteSuffix = ":write"
)

// ModuleForPath returns the module a request path belongs to for scope
// purposes: the first path segment under /api/v1/, verbatim. The scope a key
// needs for a call is therefore derivable from the URL alone, with no lookup
// table: GET /api/v1/quotes/{id} needs quotes:read, POST /api/v1/quotes needs
// quotes:write. ok is false for paths that are not /api/v1 module routes
// (other seams, or a bare /api/v1 prefix), which the machine-key path refuses.
func ModuleForPath(path string) (module string, ok bool) {
	rest, found := strings.CutPrefix(path, "/api/v1/")
	if !found || rest == "" {
		return "", false
	}
	segment, _, _ := strings.Cut(rest, "/")
	if segment == "" {
		return "", false
	}
	return segment, true
}

// ModuleScopePolicy says what the machine-key system knows about a module
// segment.
type ModuleScopePolicy int

const (
	// ModuleScopeAllowed: the segment is a module whose routes a key holding
	// the module's scope may call.
	ModuleScopeAllowed ModuleScopePolicy = iota
	// ModuleScopeExcluded: the segment sits under /api/v1 but is not a module
	// route; it keeps its own authentication and a machine key is refused.
	ModuleScopeExcluded
	// ModuleScopeUnknown: the segment is declared neither in the vocabulary
	// nor in the exclusions. The census test keeps this unreachable for
	// registered routes; the middleware refuses it fail closed.
	ModuleScopeUnknown
)

// machineKeyExcludedSegments lists /api/v1 segments that are not module
// routes. a2a is the agent-to-agent JWS seam with its own published contract
// (ADR 0001): it is a public path the auth middleware never unpacks, and a
// machine key is not a principal there.
var machineKeyExcludedSegments = map[string]string{
	"a2a": "agent-to-agent JWS seam; keeps its own authentication",
}

// machineKeyModules is the scope vocabulary: every first path segment under
// /api/v1 that names a module a scoped key can address. It mirrors the route
// census (api/ROUTES.txt), and the census test fails when the two disagree in
// either direction, so a new route cannot appear without its module (and
// therefore its scope) being declared here.
var machineKeyModules = map[string]struct{}{
	"accounts":        {},
	"activities":      {},
	"admin":           {},
	"ap":              {},
	"apps":            {},
	"bankrecon":       {},
	"branches":        {},
	"configurator":    {},
	"contacts":        {},
	"credit-memos":    {},
	"customers":       {},
	"dashboard":       {},
	"delivery":        {},
	"deposits":        {},
	"documents":       {},
	"edi":             {},
	"gl":              {},
	"governance":      {},
	"inventory":       {},
	"invoices":        {},
	"locations":       {},
	"market-indices":  {},
	"matching":        {},
	"me":              {},
	"millwork":        {},
	"orders":          {},
	"parsing":         {},
	"payments":        {},
	"pos":             {},
	"price_levels":    {},
	"pricing":         {},
	"products":        {},
	"purchase-orders": {},
	"quotes":          {},
	"reports":         {},
	"reporting":       {},
	"sales-team":      {},
	"tax":             {},
	"users":           {},
	"vendors":         {},
	"vision":          {},
}

// ModuleScopePolicyFor returns the scope policy of a module segment.
func ModuleScopePolicyFor(module string) ModuleScopePolicy {
	if _, ok := machineKeyModules[module]; ok {
		return ModuleScopeAllowed
	}
	if _, ok := machineKeyExcludedSegments[module]; ok {
		return ModuleScopeExcluded
	}
	return ModuleScopeUnknown
}

// MachineKeyModules returns the scope vocabulary, sorted, for tests and
// tooling.
func MachineKeyModules() []string {
	out := make([]string, 0, len(machineKeyModules))
	for m := range machineKeyModules {
		out = append(out, m)
	}
	sort.Strings(out)
	return out
}

// RequiredScope returns the scope a machine key must hold for a method on a
// module: reads are GET and HEAD, writes are everything else.
func RequiredScope(module string, method string) string {
	if method == http.MethodGet || method == http.MethodHead {
		return module + scopeReadSuffix
	}
	return module + scopeWriteSuffix
}

// machineKeyUserOnlyPrefixes lists route prefixes a machine key may never
// reach, whatever scope it holds, because key management is a user action: a
// key that could mint or revoke keys would be a key that could grant itself
// everything. The census test fails if a prefix matches no registered route.
var machineKeyUserOnlyPrefixes = []string{
	"/api/v1/admin/keys",
}

// MachineKeyUserOnlyPrefixes returns the user-only prefixes, for tests.
func MachineKeyUserOnlyPrefixes() []string {
	out := make([]string, len(machineKeyUserOnlyPrefixes))
	copy(out, machineKeyUserOnlyPrefixes)
	return out
}
