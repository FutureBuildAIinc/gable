// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package payment

import (
	"context"
	"testing"

	"github.com/gablelbm/gable/internal/testutil"
)

// The two key stores handle a write's effect on the 30s cache differently —
// ai.KeyStore.Set installs the new plaintext, payment.KeyStore.SetSecret
// invalidates — and only the AI side was covered. Deleting the three lines
// that reset cachedAt left the whole suite green.
//
// What that admits is a stale credential window, and the worst case is the
// automatic one: the Run api_key is an expiring JWT, so the gateway rotates it
// and PersistRotatedKey writes the refreshed value through SetSecret. Without
// the invalidation, Resolve keeps handing the EXPIRED key to the live gateway
// for up to 30 more seconds — every card charge in that window fails, for a
// credential that was already successfully refreshed and stored.
func TestKeyStore_SetSecretInvalidatesTheCache(t *testing.T) {
	db := testutil.SettingsSandbox(t)
	ctx := context.Background()

	env := GatewayConfig{MID: "MID-12345", BaseURL: "https://gateway.invalid/v1"}
	ks := NewKeyStore(db, env, mustVault(t, testVaultKey))

	if err := ks.SetSecret(ctx, "run_payments_api_key", "first-api-key"); err != nil {
		t.Fatalf("SetSecret: %v", err)
	}
	// Warm the cache through the store under test, so the second read has
	// something stale to serve.
	if got := ks.Resolve().APIKey; got != "first-api-key" {
		t.Fatalf("Resolve().APIKey = %q, want the first key", got)
	}

	// The rotation.
	if err := ks.SetSecret(ctx, "run_payments_api_key", "rotated-api-key"); err != nil {
		t.Fatalf("SetSecret (rotation): %v", err)
	}
	if got := ks.Resolve().APIKey; got != "rotated-api-key" {
		t.Fatalf("Resolve().APIKey = %q after a rotation; the store is still serving the superseded credential to the live gateway", got)
	}
}
