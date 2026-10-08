// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package techadmin

import (
	"context"
	"errors"
	"strconv"
	"time"

	"github.com/gablelbm/gable/internal/platform/httpx"
	"github.com/gablelbm/gable/pkg/database"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// ErrNotFound is the repository's sentinel for a row the caller named that
// does not exist.
var ErrNotFound = errors.New("not found")

// ListFilter is the key list's query: the keyset page plus the opt in total.
type ListFilter struct {
	Limit    int
	AfterAt  *time.Time
	AfterID  uuid.UUID
}

// Repository is the store the service reads and writes through. Every
// statement goes through GetExecutor: inside a transaction it is the
// transaction, never the pool beside it.
type Repository interface {
	CreateKey(ctx context.Context, key *APIKey) error
	LockKey(ctx context.Context, id uuid.UUID) error
	GetKey(ctx context.Context, id uuid.UUID) (*APIKey, error)
	ListKeys(ctx context.Context, f ListFilter) ([]APIKey, error)
	CountKeys(ctx context.Context) (int64, error)
	RevokeKey(ctx context.Context, id uuid.UUID) error
	UpdateLastUsed(ctx context.Context, id uuid.UUID) error
	GetKeysByPrefix(ctx context.Context, prefix string) ([]*APIKey, error)

	ReadSetting(ctx context.Context, key string) (string, bool, error)
	WriteSetting(ctx context.Context, key, value string) error
	DeleteSetting(ctx context.Context, key string) error
	ReadRevision(ctx context.Context, resource string) (int64, error)
	LockRevision(ctx context.Context, resource string) (int64, error)
	BumpRevision(ctx context.Context, resource string, to int64) error
}

type PostgresRepository struct {
	db *database.DB
}

func NewRepository(db *database.DB) *PostgresRepository {
	return &PostgresRepository{db: db}
}

func (r *PostgresRepository) CreateKey(ctx context.Context, key *APIKey) error {
	const q = `INSERT INTO api_keys (id, name, key_hash, key_prefix, scopes, created_at)
		VALUES ($1, $2, $3, $4, $5, $6)`
	_, err := r.db.GetExecutor(ctx).Exec(ctx, q,
		key.ID, key.Name, key.KeyHash, key.KeyPrefix, key.Scopes, key.CreatedAt.Time)
	return err
}

func (r *PostgresRepository) LockKey(ctx context.Context, id uuid.UUID) error {
	var one int
	err := r.db.GetExecutor(ctx).QueryRow(ctx,
		`SELECT 1 FROM api_keys WHERE id = $1 FOR UPDATE`, id).Scan(&one)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrNotFound
	}
	return err
}

func scanKey(scan func(dest ...any) error) (*APIKey, error) {
	var k APIKey
	var created time.Time
	var lastUsed, revoked *time.Time
	if err := scan(&k.ID, &k.Name, &k.KeyPrefix, &k.Scopes, &created, &lastUsed, &revoked); err != nil {
		return nil, err
	}
	k.CreatedAt = httpx.TimestampOf(created)
	k.LastUsed = httpx.PtrTimestamp(lastUsed)
	k.Revoked = httpx.PtrTimestamp(revoked)
	return &k, nil
}

func (r *PostgresRepository) GetKey(ctx context.Context, id uuid.UUID) (*APIKey, error) {
	const q = `SELECT id, name, key_prefix, COALESCE(scopes, '{}'), created_at, last_used_at, revoked_at
		FROM api_keys WHERE id = $1`
	k, err := scanKey(func(dest ...any) error {
		return r.db.GetExecutor(ctx).QueryRow(ctx, q, id).Scan(dest...)
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return k, nil
}

func (r *PostgresRepository) ListKeys(ctx context.Context, f ListFilter) ([]APIKey, error) {
	q := `SELECT id, name, key_prefix, COALESCE(scopes, '{}'), created_at, last_used_at, revoked_at
		FROM api_keys`
	args := []any{}
	if f.AfterAt != nil {
		args = append(args, *f.AfterAt, f.AfterID)
		q += ` WHERE (created_at, id) < ($1, $2)`
	}
	q += ` ORDER BY created_at DESC, id DESC LIMIT $` + strconv.Itoa(len(args)+1)
	args = append(args, f.Limit)

	rows, err := r.db.GetExecutor(ctx).Query(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	keys := []APIKey{}
	for rows.Next() {
		k, err := scanKey(rows.Scan)
		if err != nil {
			return nil, err
		}
		keys = append(keys, *k)
	}
	return keys, rows.Err()
}

func (r *PostgresRepository) CountKeys(ctx context.Context) (int64, error) {
	var n int64
	err := r.db.GetExecutor(ctx).QueryRow(ctx, `SELECT count(*) FROM api_keys`).Scan(&n)
	return n, err
}

func (r *PostgresRepository) RevokeKey(ctx context.Context, id uuid.UUID) error {
	// The service locks and reads the row first, so a revoke that affects no
	// row is a concurrent revoke that won it: nothing left to do, and the
	// service's audit row and event are skipped by its own already-revoked
	// read.
	_, err := r.db.GetExecutor(ctx).Exec(ctx,
		`UPDATE api_keys SET revoked_at = $1 WHERE id = $2 AND revoked_at IS NULL`, time.Now(), id)
	return err
}

func (r *PostgresRepository) UpdateLastUsed(ctx context.Context, id uuid.UUID) error {
	_, err := r.db.GetExecutor(ctx).Exec(ctx,
		`UPDATE api_keys SET last_used_at = $1 WHERE id = $2`, time.Now(), id)
	return err
}

func (r *PostgresRepository) GetKeysByPrefix(ctx context.Context, prefix string) ([]*APIKey, error) {
	const q = `SELECT id, name, key_hash, key_prefix, COALESCE(scopes, '{}'), created_at, last_used_at, revoked_at
		FROM api_keys WHERE key_prefix = $1 AND revoked_at IS NULL`
	rows, err := r.db.GetExecutor(ctx).Query(ctx, q, prefix)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var keys []*APIKey
	for rows.Next() {
		k := &APIKey{}
		var created time.Time
		var lastUsed, revoked *time.Time
		if err := rows.Scan(&k.ID, &k.Name, &k.KeyHash, &k.KeyPrefix, &k.Scopes, &created, &lastUsed, &revoked); err != nil {
			return nil, err
		}
		k.CreatedAt = httpx.TimestampOf(created)
		k.LastUsed = httpx.PtrTimestamp(lastUsed)
		k.Revoked = httpx.PtrTimestamp(revoked)
		keys = append(keys, k)
	}
	return keys, rows.Err()
}

func (r *PostgresRepository) ReadSetting(ctx context.Context, key string) (string, bool, error) {
	var val string
	err := r.db.GetExecutor(ctx).QueryRow(ctx,
		`SELECT value FROM system_settings WHERE key = $1`, key).Scan(&val)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	return val, true, nil
}

func (r *PostgresRepository) WriteSetting(ctx context.Context, key, value string) error {
	const q = `INSERT INTO system_settings (key, value, updated_at)
		VALUES ($1, $2, NOW())
		ON CONFLICT (key) DO UPDATE SET value = EXCLUDED.value, updated_at = NOW()`
	_, err := r.db.GetExecutor(ctx).Exec(ctx, q, key, value)
	return err
}

func (r *PostgresRepository) DeleteSetting(ctx context.Context, key string) error {
	_, err := r.db.GetExecutor(ctx).Exec(ctx, `DELETE FROM system_settings WHERE key = $1`, key)
	return err
}

// ReadRevision reads a settings resource's current revision without locking
// (the GET path); a missing anchor row is revision 1.
func (r *PostgresRepository) ReadRevision(ctx context.Context, resource string) (int64, error) {
	var rev int64
	err := r.db.GetExecutor(ctx).QueryRow(ctx,
		`SELECT revision FROM admin_revisions WHERE resource = $1`, resource).Scan(&rev)
	if errors.Is(err, pgx.ErrNoRows) {
		return 1, nil
	}
	if err != nil {
		return 0, err
	}
	return rev, nil
}

// LockRevision takes the settings resource's revision anchor row FOR UPDATE,
// creating the anchor when this is its first write (a missing anchor reads as
// revision 1). The lock is held for the rest of the caller's transaction, so
// the revision check and the bump are one database act.
func (r *PostgresRepository) LockRevision(ctx context.Context, resource string) (int64, error) {
	ex := r.db.GetExecutor(ctx)
	if _, err := ex.Exec(ctx,
		`INSERT INTO admin_revisions (resource, revision) VALUES ($1, 1) ON CONFLICT (resource) DO NOTHING`,
		resource); err != nil {
		return 0, err
	}
	var rev int64
	err := ex.QueryRow(ctx,
		`SELECT revision FROM admin_revisions WHERE resource = $1 FOR UPDATE`, resource).Scan(&rev)
	return rev, err
}

func (r *PostgresRepository) BumpRevision(ctx context.Context, resource string, to int64) error {
	_, err := r.db.GetExecutor(ctx).Exec(ctx,
		`UPDATE admin_revisions SET revision = $2, updated_at = NOW() WHERE resource = $1`, resource, to)
	return err
}
