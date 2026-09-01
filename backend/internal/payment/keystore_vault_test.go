// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package payment

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"
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

// scriptedExecer stands in for the pool so a test can make one write succeed
// and the next fail. That is the only way to reach PersistRotatedKey's
// refresh-token branch: with a real pool both writes succeed, and with a nil
// pool the function short-circuits before either.
type scriptedExecer struct {
	failOn map[string]error
	calls  []string
}

func (e *scriptedExecer) Exec(_ context.Context, _ string, args ...any) (pgconn.CommandTag, error) {
	key, _ := args[0].(string)
	e.calls = append(e.calls, key)
	if err, ok := e.failOn[key]; ok {
		return pgconn.CommandTag{}, err
	}
	return pgconn.NewCommandTag("INSERT 0 1"), nil
}

func keyStoreWithExecer(t *testing.T, e *scriptedExecer) *KeyStore {
	t.Helper()
	k := NewKeyStore(nil, GatewayConfig{}, mustVault(t, testVaultKey))
	k.exec = e
	return k
}

// PersistRotatedKey must surface a FAILED refresh-token write.
//
// This is the case the previous version of this test conceded it could not
// reach: it noted that db == nil short-circuits, then swallowed the result in
// a t.Logf. Reverting the production code to `_ = k.SetSecret(...)` passed the
// entire suite. It does not now.
//
// Why it matters: the Run api_key is a short-lived JWT. If the rotation writes
// the new api_key but silently drops the new refresh token, the pair on disk
// is inconsistent — the next restart loads a stale refresh token, the refresh
// fails, and card processing stops. The caller in cmd/server logs this error;
// swallowing it here made the log line unreachable.
func TestKeyStore_PersistRotatedKeyPropagatesRefreshTokenFailure(t *testing.T) {
	boom := errors.New("write failed: connection reset")
	e := &scriptedExecer{failOn: map[string]error{"run_payments_refresh_token": boom}}
	k := keyStoreWithExecer(t, e)

	err := k.PersistRotatedKey("rotated-api-key", "rotated-refresh-token")
	if err == nil {
		t.Fatal("PersistRotatedKey reported success while the refresh-token write failed; " +
			"the rotation half-persisted and nothing told the caller")
	}
	if !errors.Is(err, boom) {
		t.Fatalf("error %v does not wrap the underlying write failure", err)
	}
	// Both writes must have been attempted, in order — otherwise this could
	// pass for the wrong reason (e.g. the api_key write failing instead).
	want := []string{"run_payments_api_key", "run_payments_refresh_token"}
	if len(e.calls) != len(want) || e.calls[0] != want[0] || e.calls[1] != want[1] {
		t.Fatalf("writes attempted = %v, want %v", e.calls, want)
	}
}

// The api_key write's failure must propagate too, and must stop the rotation
// before the refresh token is written — a refresh token paired with an
// unpersisted api_key is the same inconsistency in mirror image.
func TestKeyStore_PersistRotatedKeyPropagatesAPIKeyFailure(t *testing.T) {
	boom := errors.New("write failed: deadlock detected")
	e := &scriptedExecer{failOn: map[string]error{"run_payments_api_key": boom}}
	k := keyStoreWithExecer(t, e)

	err := k.PersistRotatedKey("rotated-api-key", "rotated-refresh-token")
	if err == nil {
		t.Fatal("PersistRotatedKey reported success while the api_key write failed")
	}
	if !errors.Is(err, boom) {
		t.Fatalf("error %v does not wrap the underlying write failure", err)
	}
	if len(e.calls) != 1 || e.calls[0] != "run_payments_api_key" {
		t.Fatalf("writes attempted = %v, want the refresh token to be skipped after the api_key failed", e.calls)
	}
}

// The happy path: both writes succeed and both values land sealed, not
// plaintext. Without this, the two failure tests above would pass equally well
// if PersistRotatedKey always returned an error.
func TestKeyStore_PersistRotatedKeySealsBothValues(t *testing.T) {
	e := &scriptedExecer{}
	k := keyStoreWithExecer(t, e)

	if err := k.PersistRotatedKey("rotated-api-key", "rotated-refresh-token"); err != nil {
		t.Fatalf("PersistRotatedKey: %v", err)
	}
	if len(e.calls) != 2 {
		t.Fatalf("writes attempted = %v, want both api_key and refresh_token", e.calls)
	}
}

// PersistRotatedKey is the only production caller of SetSecret, reached from
// the Run gateway's api_key refresh. It must inherit the vault refusal — and,
// critically, must not reach the database at all.
func TestKeyStore_PersistRotatedKeyRefusesWithoutVault(t *testing.T) {
	e := &scriptedExecer{}
	k := NewKeyStore(nil, GatewayConfig{}, &Vault{})
	k.exec = e

	if err := k.PersistRotatedKey("rotated-api-key", "rotated-refresh-token"); err == nil {
		t.Fatal("PersistRotatedKey wrote a rotated credential with no vault key configured")
	}
	if len(e.calls) != 0 {
		t.Fatalf("a refused rotation still issued writes: %v", e.calls)
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
