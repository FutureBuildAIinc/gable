// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package main

import (
	"slices"
	"strings"
	"testing"

	"github.com/gablelbm/gable/internal/config"
)

const (
	validVaultKey = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef" // 32 bytes hex
	testJWKSURL   = "https://brain.futurebuild.test/.well-known/jwks.json"
	testIssuer    = "https://brain.futurebuild.test/"
	testAudience  = "gable-erp"
)

// --- Auth startup gate ------------------------------------------------------

func TestValidateAuthStartup_ProductionRequiresFullConfig(t *testing.T) {
	cases := []struct {
		name    string
		cfg     config.Config
		wantErr string // substring the operator will grep for
	}{
		{
			name:    "no JWKS_URL",
			cfg:     config.Config{AuthMode: "production"},
			wantErr: "JWKS_URL not set and AUTH_MODE != dev",
		},
		{
			name:    "JWKS_URL but no issuer",
			cfg:     config.Config{AuthMode: "production", JWKSURL: testJWKSURL, AuthAudience: testAudience},
			wantErr: "AUTH_ISSUER not set and AUTH_MODE != dev",
		},
		{
			name:    "issuer but no audience",
			cfg:     config.Config{AuthMode: "production", JWKSURL: testJWKSURL, AuthIssuer: testIssuer},
			wantErr: "AUTH_AUDIENCE not set and AUTH_MODE != dev",
		},
		{
			name:    "AUTH_MODE unset behaves as production",
			cfg:     config.Config{JWKSURL: testJWKSURL, AuthIssuer: testIssuer},
			wantErr: "AUTH_AUDIENCE not set and AUTH_MODE != dev",
		},
		{
			name:    "hmac algorithm rejected",
			cfg:     config.Config{AuthMode: "production", JWKSURL: testJWKSURL, AuthIssuer: testIssuer, AuthAudience: testAudience, AuthAlgorithms: "HS256"},
			wantErr: "AUTH_ALGORITHMS",
		},
		{
			name:    "none algorithm rejected",
			cfg:     config.Config{AuthMode: "production", JWKSURL: testJWKSURL, AuthIssuer: testIssuer, AuthAudience: testAudience, AuthAlgorithms: "none"},
			wantErr: "AUTH_ALGORITHMS",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := validateAuthStartup(&tc.cfg)
			if err == nil {
				t.Fatalf("validateAuthStartup returned no error (%+v); boot must refuse", got)
			}
			if !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("error %q does not contain %q (operators grep for this phrasing)", err, tc.wantErr)
			}
			if got != nil {
				t.Fatal("a rejected auth config must not yield a usable authStartup")
			}
		})
	}
}

// TestValidateAuthStartup_DevExemptions pins the one escape hatch: dev mode,
// and only dev mode, may boot without JWKS auth.
func TestValidateAuthStartup_DevExemptions(t *testing.T) {
	t.Run("dev with no config boots with auth disabled", func(t *testing.T) {
		got, err := validateAuthStartup(&config.Config{AuthMode: "dev"})
		if err != nil {
			t.Fatalf("dev mode must boot: %v", err)
		}
		if got.Enabled {
			t.Fatal("dev mode with no JWKS_URL should disable auth, not enable it")
		}
	})

	t.Run("DEV is case insensitive", func(t *testing.T) {
		if _, err := validateAuthStartup(&config.Config{AuthMode: "DEV"}); err != nil {
			t.Fatalf("AUTH_MODE=DEV must be honoured: %v", err)
		}
	})

	// The important half: dev does NOT weaken verification once JWKS auth is
	// actually switched on. If you configure a JWKS you get the full check.
	t.Run("dev with JWKS_URL still requires issuer and audience", func(t *testing.T) {
		if _, err := validateAuthStartup(&config.Config{AuthMode: "dev", JWKSURL: testJWKSURL}); err == nil {
			t.Fatal("dev mode with JWKS_URL but no AUTH_ISSUER must still refuse; dev is an exemption from having auth, not from verifying it")
		}
	})
}

func TestValidateAuthStartup_ValidProductionConfig(t *testing.T) {
	t.Run("defaults to the asymmetric allowlist", func(t *testing.T) {
		got, err := validateAuthStartup(&config.Config{
			AuthMode: "production", JWKSURL: testJWKSURL,
			AuthIssuer: testIssuer, AuthAudience: testAudience,
		})
		if err != nil {
			t.Fatalf("valid config rejected: %v", err)
		}
		if !got.Enabled || got.Issuer != testIssuer || got.Audience != testAudience {
			t.Fatalf("unexpected authStartup: %+v", got)
		}
		if len(got.Algorithms) == 0 {
			t.Fatal("no algorithm allowlist produced")
		}
		for _, alg := range got.Algorithms {
			upper := strings.ToUpper(alg)
			if upper == "NONE" || strings.HasPrefix(upper, "HS") {
				t.Fatalf("default allowlist leaked a symmetric/none algorithm: %q", alg)
			}
		}
	})

	t.Run("AUTH_ALGORITHMS narrows the allowlist", func(t *testing.T) {
		got, err := validateAuthStartup(&config.Config{
			AuthMode: "production", JWKSURL: testJWKSURL,
			AuthIssuer: testIssuer, AuthAudience: testAudience,
			AuthAlgorithms: "RS256",
		})
		if err != nil {
			t.Fatalf("valid config rejected: %v", err)
		}
		if !slices.Equal(got.Algorithms, []string{"RS256"}) {
			t.Fatalf("Algorithms = %v, want [RS256]", got.Algorithms)
		}
	})
}

// --- Payment vault startup gate --------------------------------------------

// TestValidatePaymentVaultStartup_MalformedKeyIsFatalInEveryMode is the
// dev-inclusive case, and the one most likely to be "helpfully" softened
// later. A typo must never silently downgrade encryption at rest.
func TestValidatePaymentVaultStartup_MalformedKeyIsFatalInEveryMode(t *testing.T) {
	malformed := []struct{ name, key string }{
		{"not hex", "nothex"},
		{"odd length hex", "abc"},
		{"too short", "0123456789abcdef"},
		{"too long", validVaultKey + "00"},
		{"placeholder text", "changeme"},
	}
	modes := []string{"dev", "DEV", "production", ""}

	for _, m := range malformed {
		for _, mode := range modes {
			t.Run(m.name+"/AUTH_MODE="+mode, func(t *testing.T) {
				vault, warning, err := validatePaymentVaultStartup(&config.Config{
					AuthMode: mode, PaymentVaultKey: m.key,
				})
				if err == nil {
					t.Fatalf("malformed PAYMENT_VAULT_KEY %q accepted in AUTH_MODE=%q; boot must refuse in EVERY mode", m.key, mode)
				}
				if vault != nil {
					t.Fatal("a rejected key must not yield a vault (an absent vault stores plaintext)")
				}
				if warning != "" {
					t.Fatal("a fatal key error must not be downgraded to a warning")
				}
			})
		}
	}
}

func TestValidatePaymentVaultStartup_AbsentKey(t *testing.T) {
	t.Run("refuses outside dev", func(t *testing.T) {
		for _, mode := range []string{"production", "staging", ""} {
			vault, _, err := validatePaymentVaultStartup(&config.Config{AuthMode: mode})
			if err == nil {
				t.Fatalf("absent PAYMENT_VAULT_KEY accepted in AUTH_MODE=%q; boot must refuse", mode)
			}
			if vault != nil {
				t.Fatal("a rejected config must not yield a vault")
			}
		}
	})

	t.Run("warns and continues in dev", func(t *testing.T) {
		vault, warning, err := validatePaymentVaultStartup(&config.Config{AuthMode: "dev"})
		if err != nil {
			t.Fatalf("dev mode with no key must boot: %v", err)
		}
		if vault == nil {
			t.Fatal("dev mode must still yield a vault")
		}
		if vault.Present() {
			t.Fatal("no key configured, yet the vault reports itself present")
		}
		if warning == "" {
			t.Fatal("dev mode with no vault key must warn loudly; silence is how this reaches production")
		}
		assertDevVaultWarningSaysSavesFail(t, warning)
	})
}

// assertDevVaultWarningSaysSavesFail pins the MEANING of the dev warning, not
// merely a keyword in it.
//
// The previous assertion was `strings.Contains(lower(warning), "plaintext")`.
// The message this commit replaced —
//
//	"PAYMENT_VAULT_KEY not set — AUTH_MODE=dev: Run Payments credentials WILL
//	 be stored in plaintext. Never do this outside development."
//
// — also contains "plaintext", so restoring it verbatim passed. That message is
// FALSE: with no vault key every credential store refuses the write, so nothing
// is stored at all. The commit argued that a wrong message is why nobody looked
// at this code, and then left the corrected message unpinned by anything.
//
// What an operator must be told is the symptom they will actually see: saving a
// credential in Tech Admin FAILS. That is the difference between a two-minute
// fix and an afternoon, and it is what is asserted here.
func assertDevVaultWarningSaysSavesFail(t *testing.T, warning string) {
	t.Helper()
	lower := strings.ToLower(warning)

	if !strings.Contains(lower, "plaintext") {
		t.Errorf("warning %q must say plaintext outright", warning)
	}
	if !strings.Contains(lower, "fail") {
		t.Errorf("warning %q must say that saving a credential FAILS — that is the symptom the operator sees", warning)
	}
	// The exact claim of the false message, and of any paraphrase of it.
	for _, lie := range []string{
		"will be stored in plaintext",
		"are stored in plaintext",
		"credentials will be stored",
	} {
		if strings.Contains(lower, lie) {
			t.Errorf("warning %q claims credentials get stored in plaintext; they do not — every store refuses the write", warning)
		}
	}
	// And it must point at the knob, by name.
	if !strings.Contains(warning, "PAYMENT_VAULT_KEY") {
		t.Errorf("warning %q must name PAYMENT_VAULT_KEY so the operator knows what to set", warning)
	}
}

func TestValidatePaymentVaultStartup_ValidKey(t *testing.T) {
	for _, mode := range []string{"dev", "production"} {
		vault, warning, err := validatePaymentVaultStartup(&config.Config{
			AuthMode: mode, PaymentVaultKey: validVaultKey,
		})
		if err != nil {
			t.Fatalf("AUTH_MODE=%q: valid key rejected: %v", mode, err)
		}
		if !vault.Present() {
			t.Fatalf("AUTH_MODE=%q: valid key produced an absent vault", mode)
		}
		if warning != "" {
			t.Fatalf("AUTH_MODE=%q: unexpected warning %q", mode, warning)
		}
	}
}

// A key with surrounding whitespace is a copy-paste artifact, not a typo —
// NewVault trims it. Pinned so the trim is not mistaken for laxity later.
func TestValidatePaymentVaultStartup_TrimsWhitespace(t *testing.T) {
	vault, _, err := validatePaymentVaultStartup(&config.Config{
		AuthMode: "production", PaymentVaultKey: "  " + validVaultKey + "\n",
	})
	if err != nil {
		t.Fatalf("whitespace-padded key rejected: %v", err)
	}
	if !vault.Present() {
		t.Fatal("whitespace-padded key produced an absent vault")
	}
}
