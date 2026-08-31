// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package middleware

import (
	"slices"
	"strings"
	"testing"
)

func TestParseAlgorithms_EmptyYieldsAsymmetricDefault(t *testing.T) {
	for _, setting := range []string{"", "   "} {
		got, err := ParseAlgorithms(setting)
		if err != nil {
			t.Fatalf("ParseAlgorithms(%q): unexpected error %v", setting, err)
		}
		if len(got) == 0 {
			t.Fatalf("ParseAlgorithms(%q): empty allowlist", setting)
		}
		for _, alg := range got {
			upper := strings.ToUpper(alg)
			if upper == "NONE" || strings.HasPrefix(upper, "HS") {
				t.Fatalf("default allowlist contains symmetric/none algorithm %q", alg)
			}
		}
		if !slices.Contains(got, "RS256") {
			t.Fatalf("default allowlist is missing RS256: %v", got)
		}
	}
}

func TestParseAlgorithms_DefaultIsNotAliased(t *testing.T) {
	// A caller mutating the returned slice must not poison every later boot.
	a, err := ParseAlgorithms("")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	a[0] = "HS256"

	b, err := ParseAlgorithms("")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if b[0] == "HS256" {
		t.Fatal("ParseAlgorithms returns a shared backing array; a caller mutated the default allowlist")
	}
}

func TestParseAlgorithms_Narrowing(t *testing.T) {
	cases := []struct {
		name    string
		setting string
		want    []string
	}{
		{"single", "RS256", []string{"RS256"}},
		{"multiple", "RS256,ES256", []string{"RS256", "ES256"}},
		{"whitespace tolerated", " RS256 , ES384 ", []string{"RS256", "ES384"}},
		{"lowercase tolerated", "rs256", []string{"RS256"}},
		{"mixed case tolerated", "eddsa", []string{"EdDSA"}},
		{"duplicates collapsed", "RS256,rs256", []string{"RS256"}},
		{"empty entries skipped", "RS256,,ES256", []string{"RS256", "ES256"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := ParseAlgorithms(tc.setting)
			if err != nil {
				t.Fatalf("ParseAlgorithms(%q): unexpected error %v", tc.setting, err)
			}
			if !slices.Equal(got, tc.want) {
				t.Fatalf("ParseAlgorithms(%q) = %v, want %v", tc.setting, got, tc.want)
			}
		})
	}
}

// TestParseAlgorithms_RejectsUnsafe is the operator-input half of the policy:
// "never accept an HMAC alg while verifying against a JWKS" has to hold
// against what the operator types, not only against our defaults.
func TestParseAlgorithms_RejectsUnsafe(t *testing.T) {
	cases := []struct {
		name    string
		setting string
	}{
		{"none", "none"},
		{"none uppercase", "NONE"},
		{"hmac", "HS256"},
		{"hmac lowercase", "hs512"},
		{"hmac smuggled into a valid list", "RS256,HS256"},
		{"none smuggled into a valid list", "RS256,none"},
		{"unknown algorithm", "RS255"},
		{"nonsense", "not-an-algorithm"},
		{"only separators", ",,,"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := ParseAlgorithms(tc.setting)
			if err == nil {
				t.Fatalf("ParseAlgorithms(%q) = %v, want an error", tc.setting, got)
			}
			if got != nil {
				t.Fatalf("ParseAlgorithms(%q) returned %v alongside an error; a rejected setting must not yield a usable allowlist", tc.setting, got)
			}
		})
	}
}
