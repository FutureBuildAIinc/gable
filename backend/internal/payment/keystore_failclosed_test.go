// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package payment

import (
	"context"
	"strings"
	"testing"

	"github.com/gablelbm/gable/internal/testutil"
	"github.com/gablelbm/gable/pkg/secretvault"
)

// The canary is long enough that a chance substring match against random
// base64 ciphertext is impossible.
const failClosedCanary = "run-live-BILLABLE-CANARY-8f3a1c2b4d5e6f7089abcdef"

// otherVaultKey is a DIFFERENT 32-byte key: the state an operator lands in by
// rotating PAYMENT_VAULT_KEY without re-sealing the stored rows.
const otherVaultKey = "fedcba9876543210fedcba9876543210fedcba9876543210fedcba9876543210"

// payment.KeyStore.open is the exact twin of ai.KeyStore.open, and until now
// only the AI side had a fail-closed test — the code was symmetric and the
// coverage was not. Changing `return ""` to `return value` in
// keystore.go:open left the entire suite green.
//
// What that admits: rotate PAYMENT_VAULT_KEY without re-sealing, and
// Resolve().APIKey hands back the base64 CIPHERTEXT while Configured() reports
// true. Gable then sends `enc:v1:AAAA…` as the bearer credential to the LIVE
// Run Payments gateway — card processing looks configured and fails, instead
// of reporting "not configured" and failing over.
//
// The env fallback is deliberately populated with a DIFFERENT key: fail-closed
// also means it must not quietly resolve to a credential the operator did not
// configure for this row.
func TestKeyStore_WrongVaultKeyFailsClosed(t *testing.T) {
	db := testutil.SettingsSandbox(t)
	ctx := context.Background()

	env := GatewayConfig{
		APIKey:  "env-fallback-must-not-be-used",
		MID:     "MID-12345",
		BaseURL: "https://gateway.invalid/v1",
	}

	// Seal under the original key.
	if err := NewKeyStore(db, env, mustVault(t, testVaultKey)).
		SetSecret(ctx, "run_payments_api_key", failClosedCanary); err != nil {
		t.Fatalf("SetSecret: %v", err)
	}

	// Positive control: with the RIGHT key it still resolves and is configured.
	// Without this, a Resolve() that returned "" for any reason would "pass".
	if got := NewKeyStore(db, env, mustVault(t, testVaultKey)).Resolve().APIKey; got != failClosedCanary {
		t.Fatalf("positive control: Resolve().APIKey = %q, want the canary back", got)
	}

	// The rotated-key deployment.
	rotated := NewKeyStore(db, env, mustVault(t, otherVaultKey))
	cfg := rotated.Resolve()

	if secretvault.IsSealed(cfg.APIKey) || strings.Contains(cfg.APIKey, "enc:v1:") {
		t.Fatalf("Resolve().APIKey handed back the CIPHERTEXT %q — Gable would send that to the live gateway as a bearer credential", cfg.APIKey)
	}
	if cfg.APIKey == env.APIKey {
		t.Fatalf("Resolve().APIKey silently fell back to the env credential %q; the operator configured a different one for this row", cfg.APIKey)
	}
	if cfg.APIKey != "" {
		t.Fatalf("Resolve().APIKey = %q; a value sealed under another key must resolve to \"\" (fail closed)", cfg.APIKey)
	}
	if rotated.Configured() {
		t.Fatal("Configured() reported true with an unopenable api_key; card processing would look configured and fail instead of reporting not-configured")
	}
}
