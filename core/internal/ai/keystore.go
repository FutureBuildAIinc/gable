// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package ai

import (
	"context"
	"errors"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// sealedPrefix marks a value sealed by the payment vault (AES-256-GCM); the
// envelope is shared so one vault key serves every sealed setting.
const sealedPrefix = "enc:v1:"

// Sealer is the at-rest sealing the key store uses; *payment.Vault satisfies it.
type Sealer interface {
	Present() bool
	Seal(plaintext string) (string, error)
	Open(sealed string) (string, error)
}

// ErrNoVault is returned when a key must be sealed and no vault key is set:
// a key is never stored in plain text.
var ErrNoVault = errors.New("no vault key is configured (set PAYMENT_VAULT_KEY), so a key cannot be stored")

// IsSealedValue reports whether s carries the seal envelope.
func IsSealedValue(s string) bool { return strings.HasPrefix(s, sealedPrefix) }

// openValue opens a sealed value; a legacy plaintext value passes through.
func openValue(v Sealer, s string) (string, error) {
	if !IsSealedValue(s) {
		return s, nil
	}
	if v == nil || !v.Present() {
		return "", errors.New("value is sealed but no vault key is configured")
	}
	return v.Open(s)
}

// OpenValue opens a stored value: sealed values are opened with the vault,
// legacy plaintext passes through.
func OpenValue(v Sealer, s string) (string, error) { return openValue(v, s) }

// SealValue seals a plaintext secret for storage, refusing when no vault key
// is configured.
func SealValue(v Sealer, plaintext string) (string, error) {
	if v == nil || !v.Present() {
		return "", ErrNoVault
	}
	return v.Seal(plaintext)
}

// KeyStore provides a centralized, dynamically-refreshable API key store.
// It checks the system_settings DB table first, falling back to an env var default.
// The value is cached in memory and refreshed periodically.
type KeyStore struct {
	pool       *pgxpool.Pool
	envDefault string
	settingKey string

	mu       sync.RWMutex
	cached   string
	cachedAt time.Time
	ttl      time.Duration

	sealer Sealer // optional: opens values sealed at rest

	unreadableLogged bool // the "cannot be opened" line is written once per unreadable value
}

// WithSealer attaches the vault that opens sealed values on read. Values that
// are not sealed (legacy rows) still read as they are.
func (ks *KeyStore) WithSealer(s Sealer) *KeyStore {
	ks.sealer = s
	return ks
}

// NewKeyStore creates a key store that reads from system_settings table.
// envDefault is the fallback value (from ANTHROPIC_API_KEY env var).
func NewKeyStore(pool *pgxpool.Pool, settingKey, envDefault string) *KeyStore {
	return &KeyStore{
		pool:       pool,
		envDefault: envDefault,
		settingKey: settingKey,
		ttl:        30 * time.Second,
	}
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

// Invalidate drops the cached value so the next read goes to the database. A
// save or delete of the setting calls it, so the very next call uses what was
// saved rather than what was cached.
func (ks *KeyStore) Invalidate() {
	ks.mu.Lock()
	ks.cached = ""
	ks.cachedAt = time.Time{}
	ks.unreadableLogged = false
	ks.mu.Unlock()
}

// Set stores a new key in the DB and updates the cache immediately.
func (ks *KeyStore) Set(ctx context.Context, value string) error {
	query := `
		INSERT INTO system_settings (key, value, updated_at)
		VALUES ($1, $2, NOW())
		ON CONFLICT (key) DO UPDATE SET value = $2, updated_at = NOW()
	`
	_, err := ks.pool.Exec(ctx, query, ks.settingKey, value)
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
		opened, oerr := openValue(ks.sealer, val)
		if oerr != nil {
			// Never fall back to the sealed text, and never log the value.
			// An unreadable stored key reads as AI off; the line is written
			// once, not on every read.
			ks.mu.Lock()
			first := !ks.unreadableLogged
			ks.unreadableLogged = true
			ks.mu.Unlock()
			if first {
				slog.Error("a stored AI setting cannot be opened with the vault key (the vault key changed or is missing); AI reads as off for it until it is entered again", "setting", ks.settingKey)
			}
			return ks.envDefault
		}
		return opened
	}
	if err != nil {
		slog.Debug("No DB setting found, using env fallback", "key", ks.settingKey)
	}
	return ks.envDefault
}
