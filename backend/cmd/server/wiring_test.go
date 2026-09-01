// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package main

import (
	"context"
	"go/ast"
	"go/parser"
	"go/token"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gablelbm/gable/internal/config"
	"github.com/gablelbm/gable/internal/payment"
	"github.com/gablelbm/gable/internal/testutil"
	"github.com/gablelbm/gable/pkg/middleware"
	"github.com/gablelbm/gable/pkg/secretvault"
)

const wiringCanary = "sk-or-v1-WIRING-CANARY-8f3a1c2b4d5e6f7089abcdef"

func bootVault(t *testing.T, cfg *config.Config) *credentialVault {
	t.Helper()
	cv, _, err := validatePaymentVaultStartup(cfg)
	if err != nil {
		t.Fatalf("validatePaymentVaultStartup: %v", err)
	}
	if cv == nil {
		t.Fatal("boot yielded no credential vault")
	}
	return cv
}

// The wiring is what makes the fix real, and it had no test: replacing the
// vault argument at either ai.NewSecretKeyStore call in main() with nil left
// the entire -race suite green, and the credentials would have gone back to
// being unsealed (or, at runtime, refused) with nothing to say so.
//
// The construction now happens in credentialVault.SettingStore, which this
// exercises end to end against a real database: the row main would have
// written must carry the seal envelope and must open back to the plaintext.
func TestCredentialVault_SettingStoreSealsWhatItWrites(t *testing.T) {
	db := testutil.SettingsSandbox(t)
	ctx := context.Background()
	cv := bootVault(t, &config.Config{AuthMode: "production", PaymentVaultKey: validVaultKey})

	// The two keys main.go wires, by their production names.
	for _, key := range []string{"openrouter_api_key", "openrouteservice_api_key"} {
		t.Run(key, func(t *testing.T) {
			ks := cv.SettingStore(db.Pool, key, "env-fallback-must-not-be-used", slog.Default())
			if err := ks.Set(ctx, wiringCanary); err != nil {
				t.Fatalf("Set: %v — a store built by the wiring must be able to write a credential", err)
			}

			var stored string
			if err := db.Pool.QueryRow(ctx, "SELECT value FROM system_settings WHERE key=$1", key).Scan(&stored); err != nil {
				t.Fatalf("read back: %v", err)
			}
			if !secretvault.IsSealed(stored) {
				t.Fatalf("the wiring produced an UNSEALED store: %s = %q", key, stored)
			}

			// Fresh store: the one that wrote it holds the plaintext in a 30s
			// cache, so asking it would prove nothing about the read path.
			if got := cv.SettingStore(db.Pool, key, "", slog.Default()).Get(ctx); got != wiringCanary {
				t.Fatalf("a fresh store from the same wiring read back %q, want the canary", got)
			}
		})
	}
}

// The payment sibling of the same wiring, through the same vault — one key
// seals all three credential classes, which is the claim main.go's boot log
// makes out loud.
func TestCredentialVault_PaymentKeyStoreSealsWhatItWrites(t *testing.T) {
	db := testutil.SettingsSandbox(t)
	ctx := context.Background()
	cv := bootVault(t, &config.Config{AuthMode: "production", PaymentVaultKey: validVaultKey})

	env := payment.GatewayConfig{MID: "MID-12345", BaseURL: "https://gateway.invalid/v1"}
	ks := cv.PaymentKeyStore(db, env, slog.Default())
	if err := ks.SetSecret(ctx, "run_payments_api_key", wiringCanary); err != nil {
		t.Fatalf("SetSecret: %v", err)
	}

	var stored string
	if err := db.Pool.QueryRow(ctx, "SELECT value FROM system_settings WHERE key='run_payments_api_key'").Scan(&stored); err != nil {
		t.Fatalf("read back: %v", err)
	}
	if !secretvault.IsSealed(stored) {
		t.Fatalf("the wiring produced an UNSEALED payment store: %q", stored)
	}
	if got := cv.PaymentKeyStore(db, env, slog.Default()).Resolve().APIKey; got != wiringCanary {
		t.Fatalf("a fresh payment store read back %q, want the canary", got)
	}
}

// Dev with no PAYMENT_VAULT_KEY. This is the state the boot warning describes,
// and it is the one an operator actually hits: every credential write must be
// REFUSED, and nothing may reach the table. Seal is a silent passthrough
// without a key, so "refused" is the only thing standing between this state
// and a plaintext credential on disk.
func TestCredentialVault_AbsentVaultRefusesCredentialWrites(t *testing.T) {
	db := testutil.SettingsSandbox(t)
	ctx := context.Background()

	cv, warning, err := validatePaymentVaultStartup(&config.Config{AuthMode: "dev"})
	if err != nil {
		t.Fatalf("dev with no key must boot: %v", err)
	}
	if cv.Present() {
		t.Fatal("no key configured, yet the vault reports itself present")
	}
	assertDevVaultWarningSaysSavesFail(t, warning)

	if err := cv.SettingStore(db.Pool, "openrouter_api_key", "", slog.Default()).Set(ctx, wiringCanary); err == nil {
		t.Error("SettingStore wrote a credential with no vault key")
	}
	if err := cv.PaymentKeyStore(db, payment.GatewayConfig{}, slog.Default()).
		SetSecret(ctx, "run_payments_api_key", wiringCanary); err == nil {
		t.Error("PaymentKeyStore wrote a credential with no vault key")
	}

	var n int
	if err := db.Pool.QueryRow(ctx,
		"SELECT COUNT(*) FROM system_settings WHERE value LIKE '%' || $1 || '%'", wiringCanary).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Errorf("%d row(s) reached system_settings despite the refusal", n)
	}
}

// RequireRole fails closed on nil claims, so the dev demo works only because
// exactly one call declares the bypass. Deleting that call left the suite
// green — the dev path was entirely unpinned. Both directions are asserted
// here, against the real RequireRole guard.
func TestConfigureAuthBypass_TracksAuthEnabled(t *testing.T) {
	// The bypass is process-wide state; put it back however this ends.
	t.Cleanup(func() { middleware.SetDevAuthBypass(false) })

	guardStatus := func() int {
		h := middleware.RequireRole("admin", "owner")(
			http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) }))
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/admin/staff", nil))
		return rec.Code
	}

	// Auth mounted: an unauthenticated request must be refused.
	if configureAuthBypass(true) {
		t.Error("auth is enabled, yet a bypass was declared")
	}
	if got := guardStatus(); got != http.StatusUnauthorized {
		t.Errorf("with auth enabled an unauthenticated request got %d, want 401", got)
	}

	// AUTH_MODE=dev: the demo has no tokens at all, so the guard must pass.
	if !configureAuthBypass(false) {
		t.Error("auth is disabled, yet no bypass was declared")
	}
	if got := guardStatus(); got != http.StatusOK {
		t.Errorf("with auth disabled the dev demo got %d, want 200 — every role-guarded route is 401ing", got)
	}
}

// A STRUCTURAL guard, deliberately, and the only kind available here.
//
// main() is a 700-line function that opens a database, fetches JWKS and binds a
// port; no test runs it. So the two things the tests above cover behaviourally
// —  that credential stores are built with the boot vault, and that the auth
// bypass is declared — can still be undone at their call sites in main.go
// without any behavioural test noticing. This reads main.go and asserts the
// call sites are the ones the tests cover.
//
// It is not a substitute for those tests. It is what closes the gap between
// "the function is correct" and "main calls it".
func TestMainWiresCredentialsThroughTheBootVault(t *testing.T) {
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "main.go", nil, 0)
	if err != nil {
		t.Fatalf("parse main.go: %v", err)
	}

	counts := map[string]int{}
	ast.Inspect(file, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		switch fn := call.Fun.(type) {
		case *ast.Ident:
			counts[fn.Name]++
		case *ast.SelectorExpr:
			if pkg, ok := fn.X.(*ast.Ident); ok {
				counts[pkg.Name+"."+fn.Sel.Name]++
			}
		}
		return true
	})

	// Credential stores must not be constructed directly: those constructors
	// take a vault argument, and a nil there is exactly the mutation that
	// survived. credentialVault binds the vault once, at boot.
	for name, why := range map[string]string{
		"ai.NewSecretKeyStore":        "build credential stores with credVault.SettingStore so the vault cannot be forgotten or nil'd",
		"payment.NewKeyStore":         "build the payment store with credVault.PaymentKeyStore for the same reason",
		"middleware.SetDevAuthBypass": "declare the bypass through configureAuthBypass, which derives it from authStartup.Enabled",
	} {
		if counts[name] != 0 {
			t.Errorf("main.go calls %s %d time(s): %s", name, counts[name], why)
		}
	}

	// And the wired-through calls must actually be there.
	for name, want := range map[string]int{
		"credVault.SettingStore":    2, // openrouter_api_key, openrouteservice_api_key
		"credVault.PaymentKeyStore": 1, // run_payments_*
		"configureAuthBypass":       1,
	} {
		if counts[name] != want {
			t.Errorf("main.go calls %s %d time(s), want %d — the wiring the tests in this file cover is not the wiring main uses", name, counts[name], want)
		}
	}
}
