// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package payment

import (
	"fmt"

	"github.com/gablelbm/gable/pkg/secretvault"
)

// The credential vault moved to pkg/secretvault so the AI and routing key
// stores — which write credentials to the same system_settings table — can
// seal through the same mechanism without internal/ai having to import the
// payment module. These are aliases, not a second implementation: a
// *payment.Vault and a *secretvault.Vault are the same value.

// Vault seals processor credentials at rest with AES-256-GCM.
type Vault = secretvault.Vault

// NewVault builds a vault from PAYMENT_VAULT_KEY (32-byte hex). An empty key
// yields an absent vault; a malformed key is an error the caller must surface
// loudly (cmd/server refuses to boot on it, in every mode).
func NewVault(keyHex string) (*Vault, error) {
	v, err := secretvault.New(keyHex)
	if err != nil {
		return nil, fmt.Errorf("%s is invalid: %w", secretvault.EnvVarName, err)
	}
	return v, nil
}

// IsSealed reports whether s carries the seal envelope.
func IsSealed(s string) bool { return secretvault.IsSealed(s) }
