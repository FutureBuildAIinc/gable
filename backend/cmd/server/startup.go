// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package main

import (
	"fmt"
	"log/slog"
	"strings"

	"github.com/gablelbm/gable/internal/ai"
	"github.com/gablelbm/gable/internal/config"
	"github.com/gablelbm/gable/internal/payment"
	"github.com/gablelbm/gable/pkg/database"
	"github.com/gablelbm/gable/pkg/middleware"
	"github.com/jackc/pgx/v5/pgxpool"
)

// devMode reports whether AUTH_MODE=dev was explicitly set. It is the single
// escape hatch for every fail-closed startup gate below, and it must never be
// set on a production or customer deploy.
func devMode(cfg *config.Config) bool {
	return strings.EqualFold(cfg.AuthMode, "dev")
}

// authStartup is the validated auth configuration for this boot.
type authStartup struct {
	// Enabled reports whether JWKS-backed auth should be mounted. False only
	// in dev mode with no JWKS_URL.
	Enabled    bool
	JWKSURL    string
	Issuer     string
	Audience   string
	Algorithms []string
}

// validateAuthStartup applies the fail-closed auth policy.
//
// Whenever JWKS auth is on, the issuer and audience are mandatory and the
// algorithm allowlist must be asymmetric-only. Absence of configuration must
// not mean absence of verification: an unset AUTH_ISSUER or AUTH_AUDIENCE is
// a boot failure outside dev, not a silently skipped check. Without the
// audience check in particular, a token minted for a *different* service in
// the same IdP verifies here — it is signed by the same JWKS.
//
// Returns an error the caller should treat as fatal.
func validateAuthStartup(cfg *config.Config) (*authStartup, error) {
	dev := devMode(cfg)

	if cfg.JWKSURL == "" {
		if dev {
			return &authStartup{Enabled: false}, nil
		}
		return nil, fmt.Errorf("JWKS_URL not set and AUTH_MODE != dev; set JWKS_URL for production or AUTH_MODE=dev for development")
	}

	if cfg.AuthIssuer == "" {
		return nil, fmt.Errorf("AUTH_ISSUER not set and AUTH_MODE != dev; set AUTH_ISSUER for production or AUTH_MODE=dev for development")
	}
	if cfg.AuthAudience == "" {
		return nil, fmt.Errorf("AUTH_AUDIENCE not set and AUTH_MODE != dev; set AUTH_AUDIENCE for production or AUTH_MODE=dev for development")
	}

	algs, err := middleware.ParseAlgorithms(cfg.AuthAlgorithms)
	if err != nil {
		return nil, fmt.Errorf("invalid AUTH_ALGORITHMS: %w", err)
	}

	return &authStartup{
		Enabled:    true,
		JWKSURL:    cfg.JWKSURL,
		Issuer:     cfg.AuthIssuer,
		Audience:   cfg.AuthAudience,
		Algorithms: algs,
	}, nil
}

// validatePaymentVaultStartup builds the payment credential vault under the
// fail-closed policy.
//
//   - A MALFORMED PAYMENT_VAULT_KEY is fatal in EVERY mode, dev included. A
//     typo must never silently downgrade encryption at rest for payment
//     credentials to plaintext — the operator asked for encryption and got
//     something else, which is worse than being told to fix the key.
//   - An ABSENT key is fatal outside dev. In dev it returns a warning and an
//     absent vault, so local work needs no key.
//
// The returned warning (when non-empty) must be logged by the caller.
func validatePaymentVaultStartup(cfg *config.Config) (*credentialVault, string, error) {
	vault, err := payment.NewVault(cfg.PaymentVaultKey)
	if err != nil {
		// Deliberately not dev-exempt.
		return nil, "", fmt.Errorf("PAYMENT_VAULT_KEY is invalid; refusing to start rather than store payment credentials in plaintext: %w", err)
	}

	if !vault.Present() {
		if devMode(cfg) {
			// Do not say "credentials will be stored in plaintext" — they will
			// not. Every credential store refuses the write outright without a
			// vault key, so what an operator actually sees is a failed save in
			// Tech Admin. Naming the real symptom is the difference between a
			// two-minute fix and an afternoon.
			return &credentialVault{vault: vault}, "PAYMENT_VAULT_KEY not set — AUTH_MODE=dev: saving any credential (Run Payments, OpenRouter, OpenRouteService) will FAIL rather than be stored in plaintext. Set PAYMENT_VAULT_KEY (openssl rand -hex 32) to enable credential storage.", nil
		}
		return nil, "", fmt.Errorf("PAYMENT_VAULT_KEY not set and AUTH_MODE != dev; set PAYMENT_VAULT_KEY (32-byte hex, e.g. `openssl rand -hex 32`) for production or AUTH_MODE=dev for development")
	}

	return &credentialVault{vault: vault}, "", nil
}

// credentialVault is the boot-validated secret vault AND the only constructor
// for the stores that write credentials into system_settings.
//
// It is a type with constructors rather than a bare *payment.Vault passed
// around because the AI and routing stores used to be naked
// `ai.NewSecretKeyStore(..., paymentVault)` calls three hundred lines apart
// inside main(). Replacing either vault argument with nil passed the entire
// test suite: the whole effect of sealing rested on two arguments that nothing
// checked and nothing could reach. There is no vault argument at the wiring
// sites any more — the vault is bound here, once, to the value boot validated.
//
// A nil *credentialVault is the "boot refused" signal from
// validatePaymentVaultStartup; the methods below are not reachable from it,
// because main exits before wiring when err != nil.
type credentialVault struct {
	vault *payment.Vault
}

// Present reports whether a real key is configured, i.e. whether sealing is
// active. False in dev with no PAYMENT_VAULT_KEY — in which case every store
// built below REFUSES credential writes rather than degrading to plaintext.
func (c *credentialVault) Present() bool { return c != nil && c.vault.Present() }

// SettingStore builds the store for one credential setting key in
// system_settings — openrouter_api_key, openrouteservice_api_key — sealed at
// rest by this vault.
//
// Keys that are NOT credentials (openrouter_base_url, the ai.model.* slugs) do
// not come through here; they use ai.NewKeyStore directly and stay plaintext,
// deliberately.
func (c *credentialVault) SettingStore(pool *pgxpool.Pool, settingKey, envDefault string, logger *slog.Logger) *ai.KeyStore {
	return ai.NewSecretKeyStore(pool, settingKey, envDefault, c.vault).WithLogger(logger)
}

// PaymentKeyStore builds the Run Payments credential store against the same
// vault, so all three credential classes are sealed by one key.
func (c *credentialVault) PaymentKeyStore(db *database.DB, env payment.GatewayConfig, logger *slog.Logger) *payment.KeyStore {
	return payment.NewKeyStore(db, env, c.vault).WithLogger(logger)
}

// configureAuthBypass declares process-wide whether middleware.RequireRole may
// pass an unauthenticated request through, deriving it from the SAME flag that
// decides whether the JWT middleware is mounted. Returns what it declared.
//
// RequireRole fails closed by default, so this is the only thing in the tree
// that turns the guard off — and deleting the single call site in main() left
// the whole suite green while silently 401-ing the dev demo. Declaring it
// unconditionally, from authStartup.Enabled, means the bypass and the
// middleware cannot disagree: there is no branch to forget it in.
func configureAuthBypass(authEnabled bool) bool {
	bypass := !authEnabled
	middleware.SetDevAuthBypass(bypass)
	return bypass
}
