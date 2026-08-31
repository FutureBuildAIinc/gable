// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package main

import (
	"fmt"
	"strings"

	"github.com/gablelbm/gable/internal/config"
	"github.com/gablelbm/gable/internal/payment"
	"github.com/gablelbm/gable/pkg/middleware"
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
func validatePaymentVaultStartup(cfg *config.Config) (*payment.Vault, string, error) {
	vault, err := payment.NewVault(cfg.PaymentVaultKey)
	if err != nil {
		// Deliberately not dev-exempt.
		return nil, "", fmt.Errorf("PAYMENT_VAULT_KEY is invalid; refusing to start rather than store payment credentials in plaintext: %w", err)
	}

	if !vault.Present() {
		if devMode(cfg) {
			return vault, "PAYMENT_VAULT_KEY not set — AUTH_MODE=dev: Run Payments credentials WILL be stored in plaintext. Never do this outside development.", nil
		}
		return nil, "", fmt.Errorf("PAYMENT_VAULT_KEY not set and AUTH_MODE != dev; set PAYMENT_VAULT_KEY (32-byte hex, e.g. `openssl rand -hex 32`) for production or AUTH_MODE=dev for development")
	}

	return vault, "", nil
}
