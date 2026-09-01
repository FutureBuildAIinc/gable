// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package ai

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/gablelbm/gable/pkg/secretvault"
	"github.com/jackc/pgx/v5/pgxpool"
)

// KeyStore provides a centralized, dynamically-refreshable API key store.
// It checks the system_settings DB table first, falling back to an env var default.
// The value is cached in memory and refreshed periodically.
//
// Not every setting this store holds is a secret: openrouter_base_url and the
// ai.model.* slugs are configuration and stay plaintext. The ones that ARE
// secrets — openrouter_api_key, openrouteservice_api_key — are live billable
// credentials an admin can set at runtime from the Tech Admin UI, and they are
// sealed at rest with the same secretvault the payment KeyStore uses (see
// NewSecretKeyStore). Secret-ness is decided in one place, by isSecretSettingKey.
type KeyStore struct {
	pool       *pgxpool.Pool
	envDefault string
	settingKey string
	// secret marks this store as holding a credential: writes are sealed and
	// are refused outright when no vault key is configured.
	secret bool
	vault  *secretvault.Vault
	logger *slog.Logger

	mu       sync.RWMutex
	cached   string
	cachedAt time.Time
	ttl      time.Duration
}

// secretKeySuffixes classifies a system_settings key as a credential by name.
//
// This is deliberately a name rule rather than a list of the two keys we know
// about today. The defect this closes was not that somebody chose the wrong
// constructor — it was that a *plain* constructor existed and silently wrote a
// live credential in the clear. With this rule, a future
// ai.NewKeyStore(pool, "stripe_api_key", ...) fails closed on first write with
// an error naming the hazard, instead of quietly adding a third plaintext
// credential to the table.
var secretKeySuffixes = []string{"api_key", "_secret", "_token", "_password", "_credential"}

func isSecretSettingKey(key string) bool {
	k := strings.ToLower(key)
	for _, suffix := range secretKeySuffixes {
		if strings.HasSuffix(k, suffix) {
			return true
		}
	}
	return false
}

// NewKeyStore creates a key store for a NON-SECRET setting (base URLs, model
// slugs). If settingKey nonetheless looks like a credential, the store is
// still marked secret and — having been handed no vault — will refuse to write
// rather than store it in plaintext. Use NewSecretKeyStore for credentials.
func NewKeyStore(pool *pgxpool.Pool, settingKey, envDefault string) *KeyStore {
	return NewSecretKeyStore(pool, settingKey, envDefault, nil)
}

// NewSecretKeyStore creates a key store whose value is sealed at rest with
// vault. The vault is a positional parameter rather than an option so the
// compiler checks every construction site; an optional .WithVault() only means
// the caller was trusted to remember, which is exactly how the payment vault
// came to be bolted onto the clone and not the original.
func NewSecretKeyStore(pool *pgxpool.Pool, settingKey, envDefault string, vault *secretvault.Vault) *KeyStore {
	return &KeyStore{
		pool:       pool,
		envDefault: envDefault,
		settingKey: settingKey,
		secret:     isSecretSettingKey(settingKey),
		vault:      vault,
		logger:     slog.Default(),
		ttl:        30 * time.Second,
	}
}

// WithLogger attaches a logger for seal/open diagnostics.
func (ks *KeyStore) WithLogger(l *slog.Logger) *KeyStore {
	if l != nil {
		ks.logger = l
	}
	return ks
}

// Get returns the current API key, checking DB first then env var fallback.
func (ks *KeyStore) Get(ctx context.Context) string {
	ks.mu.RLock()
	if ks.cached != "" && time.Since(ks.cachedAt) < ks.ttl {
		val := ks.cached
		ks.mu.RUnlock()
		return val
	}
	ks.mu.RUnlock()

	// Refresh from DB
	val := ks.loadFromDB(ctx)

	ks.mu.Lock()
	ks.cached = val
	ks.cachedAt = time.Now()
	ks.mu.Unlock()

	return val
}

// Set stores a new key in the DB and updates the cache immediately. For a
// secret store the value is sealed before it is written, and the write is
// REFUSED when no vault key is configured — Seal is a silent passthrough in
// that state, so without this guard an absent vault would write a live
// billable credential to system_settings as plaintext and report success.
// Refusing is the safer failure: the admin sees the save fail and the
// credential never reaches the disk.
func (ks *KeyStore) Set(ctx context.Context, value string) error {
	stored := value
	if ks.secret && value != "" {
		if !ks.vault.Present() {
			return fmt.Errorf("refusing to write secret setting %q: no %s configured, so it would be stored in plaintext",
				ks.settingKey, secretvault.EnvVarName)
		}
		sealed, err := ks.vault.Seal(value)
		if err != nil {
			return fmt.Errorf("failed to seal %q: %w", ks.settingKey, err)
		}
		stored = sealed
	}

	query := `
		INSERT INTO system_settings (key, value, updated_at)
		VALUES ($1, $2, NOW())
		ON CONFLICT (key) DO UPDATE SET value = $2, updated_at = NOW()
	`
	_, err := ks.pool.Exec(ctx, query, ks.settingKey, stored)
	if err != nil {
		return err
	}

	ks.mu.Lock()
	if value != "" {
		ks.cached = value
	} else {
		ks.cached = ks.envDefault
	}
	ks.cachedAt = time.Now()
	ks.mu.Unlock()

	return nil
}

// Delete removes the key from DB, reverting to env var.
func (ks *KeyStore) Delete(ctx context.Context) error {
	_, err := ks.pool.Exec(ctx, "DELETE FROM system_settings WHERE key = $1", ks.settingKey)
	if err != nil {
		return err
	}

	ks.mu.Lock()
	ks.cached = ks.envDefault
	ks.cachedAt = time.Now()
	ks.mu.Unlock()

	return nil
}

// IsConfigured returns true if a key is available (from DB or env).
func (ks *KeyStore) IsConfigured(ctx context.Context) bool {
	return ks.Get(ctx) != ""
}

// HasDBOverride returns true if a key has been saved in the DB.
func (ks *KeyStore) HasDBOverride(ctx context.Context) bool {
	var count int
	err := ks.pool.QueryRow(ctx, "SELECT COUNT(*) FROM system_settings WHERE key = $1", ks.settingKey).Scan(&count)
	if err != nil {
		return false
	}
	return count > 0
}

func (ks *KeyStore) loadFromDB(ctx context.Context) string {
	var val string
	err := ks.pool.QueryRow(ctx, "SELECT value FROM system_settings WHERE key = $1", ks.settingKey).Scan(&val)
	if err == nil && val != "" {
		if ks.secret {
			return ks.open(val)
		}
		return val
	}
	if err != nil {
		slog.Debug("No DB setting found, using env fallback", "key", ks.settingKey)
	}
	return ks.envDefault
}

// open transparently decrypts a sealed value. Rows written before the vault
// existed carry no envelope and pass through unchanged, which is what makes
// this change safe to deploy without a data migration.
//
// A decrypt failure yields "" (fail closed) rather than the env fallback: a
// mis-keyed or corrupt credential must not silently resolve to a *different*
// credential than the operator configured, and it must never be handed on as
// if it were plaintext.
func (ks *KeyStore) open(value string) string {
	if !secretvault.IsSealed(value) {
		return value // legacy plaintext row, written before sealing existed
	}
	pt, err := ks.vault.Open(value)
	if err != nil {
		ks.logger.Error("failed to open sealed setting", "setting", ks.settingKey, "error", err)
		return ""
	}
	return pt
}
