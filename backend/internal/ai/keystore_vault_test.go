// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

// This file is package ai_test rather than package ai because internal/config
// imports internal/ai, and testutil (which reaches the database) imports
// config — an in-package test importing testutil would be an import cycle.
package ai_test

import (
	"context"
	"strings"
	"testing"

	"github.com/gablelbm/gable/internal/ai"
	"github.com/gablelbm/gable/internal/testutil"
	"github.com/gablelbm/gable/pkg/secretvault"
)

const (
	testVaultKey = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef" // 32 bytes hex
	// Long enough that a chance base64 substring match is impossible.
	aiCanary = "sk-or-v1-LIVE-BILLABLE-CANARY-8f3a1c2b4d5e6f7089abcdef"
)

func mustVault(t *testing.T, keyHex string) *secretvault.Vault {
	t.Helper()
	v, err := secretvault.New(keyHex)
	if err != nil {
		t.Fatalf("secretvault.New: %v", err)
	}
	return v
}

// ACCEPTANCE I (AI/routing half): an admin-settable credential must never
// reach system_settings in plaintext when a vault key is configured. These two
// keys were written in the clear until this change — the payment vault was
// bolted onto the clone and never onto the original.
func TestAIKeyStore_SealsCredentialsAtRest(t *testing.T) {
	db := testutil.SettingsSandbox(t)
	ctx := context.Background()
	vault := mustVault(t, testVaultKey)

	for _, key := range []string{"openrouter_api_key", "openrouteservice_api_key"} {
		t.Run(key, func(t *testing.T) {
			ks := ai.NewSecretKeyStore(db.Pool, key, "", vault)
			if err := ks.Set(ctx, aiCanary); err != nil {
				t.Fatalf("Set: %v", err)
			}

			var stored string
			if err := db.Pool.QueryRow(ctx, "SELECT value FROM system_settings WHERE key=$1", key).Scan(&stored); err != nil {
				t.Fatalf("read back: %v", err)
			}
			if strings.Contains(stored, aiCanary) {
				t.Fatalf("credential stored in plaintext: %q", stored)
			}
			if !secretvault.IsSealed(stored) {
				t.Fatalf("stored value carries no seal envelope: %q", stored)
			}

			// And it must still be usable: a sealed value nobody can open is
			// just a different outage. Read through a FRESH store — the one
			// that wrote it has the plaintext in its 30s cache, so asking it
			// would prove nothing about the read path.
			fresh := ai.NewSecretKeyStore(db.Pool, key, "", vault)
			if got := fresh.Get(ctx); got != aiCanary {
				t.Fatalf("a fresh store could not read back the sealed value: Get() = %q", got)
			}
		})
	}
}

// ACCEPTANCE III: rows written before sealing existed must keep working. This
// is the claim that makes seal-on-next-write safe to deploy without a data
// migration, and it is proved by execution rather than trusted.
func TestAIKeyStore_LegacyPlaintextRowStillReadable(t *testing.T) {
	db := testutil.SettingsSandbox(t)
	ctx := context.Background()
	const key = "openrouter_api_key"

	// Write the row exactly as the OLD code did: raw, unsealed.
	if _, err := db.Pool.Exec(ctx,
		`INSERT INTO system_settings (key, value, updated_at) VALUES ($1,$2,NOW())
		 ON CONFLICT (key) DO UPDATE SET value=$2, updated_at=NOW()`, key, aiCanary); err != nil {
		t.Fatalf("seed legacy row: %v", err)
	}

	ks := ai.NewSecretKeyStore(db.Pool, key, "env-fallback", mustVault(t, testVaultKey))
	if got := ks.Get(ctx); got != aiCanary {
		t.Fatalf("legacy plaintext row did not survive the change: Get() = %q, want %q", got, aiCanary)
	}

	// The next write seals it — that is the entire migration story.
	if err := ks.Set(ctx, aiCanary); err != nil {
		t.Fatalf("Set: %v", err)
	}
	var stored string
	if err := db.Pool.QueryRow(ctx, "SELECT value FROM system_settings WHERE key=$1", key).Scan(&stored); err != nil {
		t.Fatal(err)
	}
	if !secretvault.IsSealed(stored) {
		t.Fatalf("rewriting a legacy row left it unsealed: %q", stored)
	}
}

// A value sealed under a DIFFERENT key must fail closed — resolve to "" rather
// than being handed on as if it were a plaintext API key, and rather than
// quietly falling back to a different credential than the operator configured.
func TestAIKeyStore_WrongVaultKeyFailsClosed(t *testing.T) {
	db := testutil.SettingsSandbox(t)
	ctx := context.Background()
	const key = "openrouter_api_key"

	if err := ai.NewSecretKeyStore(db.Pool, key, "", mustVault(t, testVaultKey)).Set(ctx, aiCanary); err != nil {
		t.Fatalf("Set: %v", err)
	}

	ks := ai.NewSecretKeyStore(db.Pool, key, "env-fallback-must-not-be-used", mustVault(t, strings.Repeat("ab", 32)))
	if got := ks.Get(ctx); got != "" {
		t.Fatalf("a value sealed under another key resolved to %q; want \"\" (fail closed)", got)
	}
}

// No vault key configured means no write at all. Seal is a silent passthrough
// in that state, so without the refusal an unconfigured deployment would store
// a live billable credential in the clear and report success to the admin.
//
// No database is needed: the refusal must come before the write, and asserting
// that with a nil pool is what proves the ordering — a guard placed after the
// Exec would panic here instead of returning an error.
func TestAIKeyStore_RefusesToWriteWithoutVaultKey(t *testing.T) {
	ctx := context.Background()

	for _, tc := range []struct {
		name  string
		store *ai.KeyStore
	}{
		{"nil vault", ai.NewSecretKeyStore(nil, "openrouter_api_key", "", nil)},
		{"vault from empty key", ai.NewSecretKeyStore(nil, "openrouter_api_key", "", mustVault(t, ""))},
		// The plain constructor supplies no vault at all. A secret-looking key
		// passed to it must still refuse rather than degrade to plaintext.
		{"plain constructor, secret-looking key", ai.NewKeyStore(nil, "openrouteservice_api_key", "")},
		{"plain constructor, a key nobody has added yet", ai.NewKeyStore(nil, "stripe_api_key", "")},
		{"plain constructor, refresh token", ai.NewKeyStore(nil, "some_service_refresh_token", "")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.store.Set(ctx, aiCanary)
			if err == nil {
				t.Fatal("Set succeeded with no vault key; it would have written the credential in plaintext")
			}
			if !strings.Contains(err.Error(), "plaintext") {
				t.Fatalf("error %q should name the actual hazard (plaintext)", err)
			}
		})
	}
}

// Not everything in system_settings is a credential. Sealing the base URL or a
// model slug would be pointless churn, and — because those stores are built
// with no vault — would break them outright. This is the other half of the
// classification: it must not over-reach.
func TestAIKeyStore_NonSecretSettingsStayPlaintext(t *testing.T) {
	db := testutil.SettingsSandbox(t)
	ctx := context.Background()

	for key, value := range map[string]string{
		"openrouter_base_url": "https://openrouter.ai/api/v1",
		"ai.model.text":       "anthropic/claude-sonnet-4",
	} {
		t.Run(key, func(t *testing.T) {
			ks := ai.NewKeyStore(db.Pool, key, "")
			if err := ks.Set(ctx, value); err != nil {
				t.Fatalf("Set(%s): %v — a non-secret setting must not need a vault key", key, err)
			}
			var stored string
			if err := db.Pool.QueryRow(ctx, "SELECT value FROM system_settings WHERE key=$1", key).Scan(&stored); err != nil {
				t.Fatal(err)
			}
			if stored != value {
				t.Fatalf("non-secret setting was transformed: stored %q, want %q", stored, value)
			}
			if got := ks.Get(ctx); got != value {
				t.Fatalf("Get() = %q, want %q", got, value)
			}
		})
	}
}

// Set writes the SEALED value to the database and caches the PLAINTEXT. The
// two must not be swapped: caching the sealed value made the store serve
// `enc:v1:AAAA…` as the OpenRouter bearer token for the whole 30s TTL, to every
// caller, and the suite stayed green — the existing tests all read back through
// a FRESH store, which goes to the database and never touches the cache.
//
// The row is deleted out from under the store before the read, so the value
// Get() returns can ONLY have come from the cache. Without that, a store that
// cached nothing would still pass by falling through to the database.
func TestAIKeyStore_SetCachesPlaintextNotCiphertext(t *testing.T) {
	db := testutil.SettingsSandbox(t)
	ctx := context.Background()
	const key = "openrouter_api_key"

	ks := ai.NewSecretKeyStore(db.Pool, key, "", mustVault(t, testVaultKey))
	if err := ks.Set(ctx, aiCanary); err != nil {
		t.Fatalf("Set: %v", err)
	}
	if _, err := db.Pool.Exec(ctx, "DELETE FROM system_settings WHERE key=$1", key); err != nil {
		t.Fatalf("delete row: %v", err)
	}

	got := ks.Get(ctx)
	if secretvault.IsSealed(got) {
		t.Fatalf("Set cached the SEALED value: Get() = %q — every AI call for the next 30s would send ciphertext as the bearer credential", got)
	}
	if got != aiCanary {
		t.Fatalf("Get() = %q, want the plaintext canary from the cache", got)
	}
}
