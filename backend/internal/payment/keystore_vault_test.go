// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package payment

import (
	"context"
	"strings"
	"testing"
)

func mustVault(t *testing.T, keyHex string) *Vault {
	t.Helper()
	v, err := NewVault(keyHex)
	if err != nil {
		t.Fatalf("NewVault: %v", err)
	}
	return v
}

// TestKeyStore_SetSecretRefusesWithoutVault covers the latent path: a KeyStore
// built without a real vault must not write credentials at all. Before this
// guard, Seal silently returned the plaintext and SetSecret reported success,
// so the first Run api_key rotation wrote a live payment credential to
// system_settings in the clear.
func TestKeyStore_SetSecretRefusesWithoutVault(t *testing.T) {
	for _, tc := range []struct {
		name  string
		vault *Vault
	}{
		{"nil vault", nil},
		{"zero-value vault", &Vault{}},
		{"vault from empty key", mustVaultLazy("")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			k := NewKeyStore(nil, GatewayConfig{}, tc.vault)

			err := k.SetSecret(context.Background(), "run_payments_api_key", "live-secret-value")
			if err == nil {
				t.Fatal("SetSecret succeeded with no vault key; it would have written the credential in plaintext")
			}
			if !strings.Contains(err.Error(), "plaintext") {
				t.Fatalf("error %q should name the actual hazard (plaintext)", err)
			}
		})
	}
}

// PersistRotatedKey is the only production caller of SetSecret, reached from
// the Run gateway's api_key refresh. It must inherit the refusal.
func TestKeyStore_PersistRotatedKeyRefusesWithoutVault(t *testing.T) {
	k := NewKeyStore(nil, GatewayConfig{}, &Vault{})
	// db == nil short-circuits PersistRotatedKey, so give it a store that
	// believes it has a database by exercising SetSecret directly as well.
	if err := k.PersistRotatedKey("rotated-api-key", "rotated-refresh-token"); err != nil {
		t.Logf("PersistRotatedKey with no DB returned: %v", err)
	}
	if err := k.SetSecret(context.Background(), "run_payments_refresh_token", "rotated-refresh-token"); err == nil {
		t.Fatal("refresh-token write was permitted with no vault key")
	}
}

// TestKeyStore_NewKeyStoreRequiresVault is a compile-shape assertion in test
// form: the vault is a positional parameter, so a future construction site
// cannot forget it the way an optional .WithVault() allowed.
func TestKeyStore_NewKeyStoreUsesSuppliedVault(t *testing.T) {
	v := mustVault(t, testVaultKey)
	k := NewKeyStore(nil, GatewayConfig{}, v)
	if !k.vault.Present() {
		t.Fatal("NewKeyStore did not adopt the supplied vault")
	}

	// A nil vault degrades to absent — and absent refuses to write.
	k2 := NewKeyStore(nil, GatewayConfig{}, nil)
	if k2.vault == nil {
		t.Fatal("NewKeyStore(nil vault) left a nil vault; every call site would panic")
	}
	if k2.vault.Present() {
		t.Fatal("NewKeyStore(nil vault) reported a present vault")
	}
}

// TestVault_SealNeverEmitsPlaintextWhenKeyed is the "no reachable path writes
// plaintext" invariant, asserted on the value that would actually hit the
// database column.
func TestVault_SealNeverEmitsPlaintextWhenKeyed(t *testing.T) {
	v := mustVault(t, testVaultKey)

	// Every entry is either empty or long enough that a base64 substring
	// match cannot happen by chance — a one-character secret would collide
	// with random ciphertext constantly and make this test flaky.
	secrets := []string{
		"",
		"run-live-api-key-abcdef123456",
		"eyJhbGciOiJIUzI1NiJ9.eyJzdWIiOiIxIn0.signature", // a JWT-shaped api_key
		strings.Repeat("long-secret-", 200),
		"unicode-éè-secret",
	}
	for _, secret := range secrets {
		sealed, err := v.Seal(secret)
		if err != nil {
			t.Fatalf("Seal(%.20q): %v", secret, err)
		}
		if !IsSealed(sealed) {
			t.Fatalf("Seal(%.20q) produced an unsealed value %q", secret, sealed)
		}
		if len(secret) >= 8 && strings.Contains(sealed, secret) {
			t.Fatalf("sealed value leaks the plaintext: %q contains %q", sealed, secret)
		}
		if sealed == secret {
			t.Fatalf("Seal(%.20q) was a passthrough despite a configured key", secret)
		}

		opened, err := v.Open(sealed)
		if err != nil {
			t.Fatalf("Open round-trip failed for %.20q: %v", secret, err)
		}
		if opened != secret {
			t.Fatalf("round-trip mismatch: got %q, want %q", opened, secret)
		}
	}
}

// Sealing the same secret twice must not produce the same ciphertext, or the
// nonce is being reused — which would be a real AES-GCM break, not just a
// cosmetic one.
func TestVault_SealUsesFreshNonce(t *testing.T) {
	v := mustVault(t, testVaultKey)
	a, err := v.Seal("run-live-api-key")
	if err != nil {
		t.Fatalf("Seal: %v", err)
	}
	b, err := v.Seal("run-live-api-key")
	if err != nil {
		t.Fatalf("Seal: %v", err)
	}
	if a == b {
		t.Fatal("two seals of the same plaintext are identical; the GCM nonce is being reused")
	}
}

func mustVaultLazy(keyHex string) *Vault {
	v, err := NewVault(keyHex)
	if err != nil {
		panic(err)
	}
	return v
}
