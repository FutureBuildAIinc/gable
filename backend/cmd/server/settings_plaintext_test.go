// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package main

import (
	"context"
	"testing"

	"github.com/gablelbm/gable/internal/ai"
	"github.com/gablelbm/gable/internal/payment"
	"github.com/gablelbm/gable/internal/testutil"
	"github.com/gablelbm/gable/pkg/secretvault"
)

// The canary is deliberately shaped like a real OpenRouter key and is long
// enough that a chance substring match against random base64 ciphertext is
// impossible.
const settingsCanary = "sk-or-v1-LIVE-BILLABLE-CANARY-8f3a1c2b4d5e6f7089abcdef"

// ACCEPTANCE I. No credential of ANY class reaches system_settings in
// plaintext when a vault key is configured.
//
// This test lives in cmd/server because that is where the invariant is: three
// different modules write credentials into one shared table, and the defect
// was that one of them had a vault and the others did not. A per-module test
// cannot see that. The check is a whole-table LIKE scan rather than a lookup
// of the four keys we happen to know about — if a fifth writer appears and
// stores a credential in the clear, this fails.
func TestNoCredentialClassIsStoredInPlaintext(t *testing.T) {
	// A sandboxed system_settings: this test scans the WHOLE table for the
	// canary, and internal/ai deliberately writes the same canary in plaintext
	// (to prove legacy rows still read) from a concurrent test process. Sharing
	// the table made this test fail on any freshly-migrated database.
	db := testutil.SettingsSandbox(t)
	ctx := context.Background()

	vault, err := payment.NewVault(validVaultKey)
	if err != nil {
		t.Fatalf("NewVault: %v", err)
	}

	const (
		paymentAPIKey  = "run_payments_api_key"
		paymentRefresh = "run_payments_refresh_token"
		openRouterKey  = "openrouter_api_key"
		routingKey     = "openrouteservice_api_key"
	)
	sealedKeys := []string{paymentAPIKey, paymentRefresh, openRouterKey, routingKey}

	// Payment class — the one that already had a vault.
	pk := payment.NewKeyStore(db, payment.GatewayConfig{}, vault)
	if err := pk.SetSecret(ctx, paymentAPIKey, settingsCanary); err != nil {
		t.Fatalf("payment SetSecret(api_key): %v", err)
	}
	if err := pk.SetSecret(ctx, paymentRefresh, settingsCanary); err != nil {
		t.Fatalf("payment SetSecret(refresh_token): %v", err)
	}

	// AI and routing classes — the originals the vault was never wired to.
	// Constructed exactly as main.go constructs them.
	aiStore := ai.NewSecretKeyStore(db.Pool, openRouterKey, "", vault)
	orsStore := ai.NewSecretKeyStore(db.Pool, routingKey, "", vault)
	if err := aiStore.Set(ctx, settingsCanary); err != nil {
		t.Fatalf("ai Set: %v", err)
	}
	if err := orsStore.Set(ctx, settingsCanary); err != nil {
		t.Fatalf("routing Set: %v", err)
	}

	// The scan: no row anywhere in the table may contain the canary.
	rows, err := db.Pool.Query(ctx,
		`SELECT key, value FROM system_settings WHERE value LIKE '%' || $1 || '%'`, settingsCanary)
	if err != nil {
		t.Fatalf("scan: %v", err)
	}
	defer rows.Close()
	for rows.Next() {
		var k, v string
		if err := rows.Scan(&k, &v); err != nil {
			t.Fatal(err)
		}
		t.Errorf("PLAINTEXT CREDENTIAL in system_settings: key=%s value=%q", k, v)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}

	// Every one of them must actually be sealed — an empty scan would also be
	// satisfied by a write that never happened.
	for _, k := range sealedKeys {
		var stored string
		if err := db.Pool.QueryRow(ctx, "SELECT value FROM system_settings WHERE key=$1", k).Scan(&stored); err != nil {
			t.Fatalf("%s was never written: %v", k, err)
		}
		if !secretvault.IsSealed(stored) {
			t.Errorf("%s is not sealed: %q", k, stored)
		}
		opened, err := vault.Open(stored)
		if err != nil {
			t.Errorf("%s does not open: %v", k, err)
			continue
		}
		if opened != settingsCanary {
			t.Errorf("%s opened to %q, want the canary back", k, opened)
		}
	}

	// And every class still RESOLVES to the real value, so sealing did not
	// merely break the feature. Fresh stores throughout: the ones that wrote
	// these values are holding the plaintext in a 30s cache, so asking them
	// would exercise nothing but the cache.
	if got := payment.NewKeyStore(db, payment.GatewayConfig{}, vault).Resolve().APIKey; got != settingsCanary {
		t.Errorf("payment Resolve().APIKey = %q, want the canary", got)
	}
	if got := ai.NewSecretKeyStore(db.Pool, openRouterKey, "", vault).Get(ctx); got != settingsCanary {
		t.Errorf("ai Get() = %q, want the canary", got)
	}
	if got := ai.NewSecretKeyStore(db.Pool, routingKey, "", vault).Get(ctx); got != settingsCanary {
		t.Errorf("routing Get() = %q, want the canary", got)
	}
}
