// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package middleware

import (
	"fmt"
	"sort"
	"strings"
)

// defaultAsymmetricAlgorithms is the algorithm allowlist used when
// AUTH_ALGORITHMS is unset. It is asymmetric-only on purpose: this service
// verifies against a JWKS holding *public* keys, so an HMAC algorithm would
// mean verifying a token with material an attacker can also read, and "none"
// would mean not verifying at all. Both are the classic algorithm-confusion
// vectors, and neither is ever correct here.
//
// The set is deliberately broader than this deployment needs (FutureBuild
// Brain OIDC signs RS256) so a self-hoster on an EC- or Ed25519-signing IdP
// still boots. Narrow it in production with AUTH_ALGORITHMS=RS256.
var defaultAsymmetricAlgorithms = []string{
	"RS256", "RS384", "RS512",
	"PS256", "PS384", "PS512",
	"ES256", "ES384", "ES512",
	"EdDSA",
}

// allowedAlgorithms is the membership set for operator-supplied values.
var allowedAlgorithms = func() map[string]string {
	m := make(map[string]string, len(defaultAsymmetricAlgorithms))
	for _, a := range defaultAsymmetricAlgorithms {
		m[strings.ToUpper(a)] = a
	}
	return m
}()

// DefaultAlgorithms returns a copy of the asymmetric default allowlist.
func DefaultAlgorithms() []string {
	out := make([]string, len(defaultAsymmetricAlgorithms))
	copy(out, defaultAsymmetricAlgorithms)
	return out
}

// ParseAlgorithms turns the AUTH_ALGORITHMS setting into a JWT signing-method
// allowlist. An empty setting yields the asymmetric default set. Any entry
// that is symmetric ("HS*"), "none", or simply unknown is an error — the
// policy that this service never accepts an HMAC alg while verifying against
// a JWKS is enforced against operator input, not just against our defaults.
//
// Callers should treat an error as fatal at boot: a misconfigured allowlist
// must not fall back to something permissive.
func ParseAlgorithms(setting string) ([]string, error) {
	setting = strings.TrimSpace(setting)
	if setting == "" {
		return DefaultAlgorithms(), nil
	}

	seen := make(map[string]bool)
	var out []string
	for _, raw := range strings.Split(setting, ",") {
		name := strings.TrimSpace(raw)
		if name == "" {
			continue
		}
		upper := strings.ToUpper(name)

		if upper == "NONE" {
			return nil, fmt.Errorf(`AUTH_ALGORITHMS contains %q: the "none" algorithm means no signature verification and is never accepted`, name)
		}
		if strings.HasPrefix(upper, "HS") {
			return nil, fmt.Errorf("AUTH_ALGORITHMS contains %q: HMAC algorithms are never accepted when verifying against a JWKS (the JWKS publishes the key material an attacker would sign with)", name)
		}

		canonical, ok := allowedAlgorithms[upper]
		if !ok {
			return nil, fmt.Errorf("AUTH_ALGORITHMS contains unsupported algorithm %q; supported: %s", name, strings.Join(sortedAllowed(), ", "))
		}
		if seen[canonical] {
			continue
		}
		seen[canonical] = true
		out = append(out, canonical)
	}

	if len(out) == 0 {
		return nil, fmt.Errorf("AUTH_ALGORITHMS is set but lists no algorithms; unset it to use the default asymmetric set")
	}
	return out, nil
}

func sortedAllowed() []string {
	out := DefaultAlgorithms()
	sort.Strings(out)
	return out
}
