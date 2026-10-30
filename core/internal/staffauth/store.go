// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package staffauth

import (
	"context"
	"errors"
	"time"

	"github.com/gablelbm/gable/pkg/database"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// Session is one staff_sessions row.
type Session struct {
	IDHash            []byte
	Sub               uuid.UUID
	AppwriteUserID    string
	ClientKind        string
	Roles             []string
	RolesReadAt       time.Time
	CreatedAt         time.Time
	LastUsedAt        time.Time
	IdleSeconds       int
	AbsoluteExpiresAt time.Time
	RevokedAt         *time.Time
	RevokedReason     string
	// A shell's session also holds the hash of its current refresh token and
	// its token family (nil for a web session).
	RefreshHash []byte
	TokenFamily *uuid.UUID
}

// Store is the session table. Every write is a single conditional statement,
// so concurrent requests on one session cannot interleave into a state the
// rules forbid (a revoked row is never reanimated).
type Store struct {
	db *database.DB
}

// NewStore returns the store over db.
func NewStore(db *database.DB) *Store { return &Store{db: db} }

const sessionCols = `id_hash, sub, appwrite_user_id, client_kind, roles, roles_read_at, created_at,
	last_used_at, idle_seconds, absolute_expires_at, revoked_at, COALESCE(revoked_reason, '')`

// Create inserts a session row.
func (s *Store) Create(ctx context.Context, x Session) error {
	_, err := s.db.GetExecutor(ctx).Exec(ctx,
		`INSERT INTO staff_sessions (id_hash, sub, appwrite_user_id, client_kind, roles, roles_read_at,
		                             created_at, last_used_at, idle_seconds, absolute_expires_at,
		                             refresh_token_hash, token_family)
		 VALUES ($1, $2, $3, $4, $5, $6, $7, $7, $8, $9, $10, $11)`,
		x.IDHash, x.Sub, x.AppwriteUserID, x.ClientKind, x.Roles, x.RolesReadAt, x.CreatedAt, x.IdleSeconds, x.AbsoluteExpiresAt,
		x.RefreshHash, x.TokenFamily)
	return err
}

// ErrNotFound marks an absent session row.
var ErrNotFound = errors.New("staffauth: session not found")

// Get reads a session by the hash of its id.
func (s *Store) Get(ctx context.Context, idHash []byte) (Session, error) {
	var x Session
	err := s.db.GetExecutor(ctx).QueryRow(ctx,
		`SELECT `+sessionCols+` FROM staff_sessions WHERE id_hash = $1`, idHash).
		Scan(&x.IDHash, &x.Sub, &x.AppwriteUserID, &x.ClientKind, &x.Roles, &x.RolesReadAt, &x.CreatedAt,
			&x.LastUsedAt, &x.IdleSeconds, &x.AbsoluteExpiresAt, &x.RevokedAt, &x.RevokedReason)
	if errors.Is(err, pgx.ErrNoRows) {
		return Session{}, ErrNotFound
	}
	return x, err
}

// Touch records a use, only when the last one is older than minGap, so a
// busy counter does not write on every request. A revoked row is left alone.
func (s *Store) Touch(ctx context.Context, idHash []byte, now time.Time, minGap time.Duration) error {
	_, err := s.db.GetExecutor(ctx).Exec(ctx,
		`UPDATE staff_sessions SET last_used_at = $2::timestamptz
		 WHERE id_hash = $1 AND revoked_at IS NULL AND last_used_at <= $2::timestamptz - make_interval(secs => $3)`,
		idHash, now, minGap.Seconds())
	return err
}

// Revoke marks a session revoked; it reports whether this call did it.
func (s *Store) Revoke(ctx context.Context, idHash []byte, reason string, now time.Time) (bool, error) {
	tag, err := s.db.GetExecutor(ctx).Exec(ctx,
		`UPDATE staff_sessions SET revoked_at = $2, revoked_reason = $3
		 WHERE id_hash = $1 AND revoked_at IS NULL`, idHash, now, reason)
	return tag.RowsAffected() == 1, err
}

// ReplaceRoles replaces the role snapshot and the time it was read, on a row
// that is not revoked; it reports whether a row changed.
func (s *Store) ReplaceRoles(ctx context.Context, idHash []byte, roles []string, now time.Time) (bool, error) {
	tag, err := s.db.GetExecutor(ctx).Exec(ctx,
		`UPDATE staff_sessions SET roles = $2, roles_read_at = $3
		 WHERE id_hash = $1 AND revoked_at IS NULL AND roles_read_at <= $3`, idHash, roles, now)
	return tag.RowsAffected() == 1, err
}

// RevokeSubject revokes every live session of a subject.
func (s *Store) RevokeSubject(ctx context.Context, sub uuid.UUID, reason string, now time.Time) (int64, error) {
	tag, err := s.db.GetExecutor(ctx).Exec(ctx,
		`UPDATE staff_sessions SET revoked_at = $2, revoked_reason = $3
		 WHERE sub = $1 AND revoked_at IS NULL`, sub, now, reason)
	return tag.RowsAffected(), err
}

// Sweep deletes sessions that are past their absolute limit or were revoked
// before cutoff, and pending sign ins past their expiry.
func (s *Store) Sweep(ctx context.Context, now, revokedBefore time.Time) (int64, error) {
	tag, err := s.db.GetExecutor(ctx).Exec(ctx,
		`DELETE FROM staff_sessions WHERE absolute_expires_at < $1 OR (revoked_at IS NOT NULL AND revoked_at < $2)`,
		now, revokedBefore)
	if err != nil {
		return 0, err
	}
	// Pending sign ins live minutes; one past its expiry is garbage whether
	// or not it was used.
	if _, err := s.db.GetExecutor(ctx).Exec(ctx, `DELETE FROM pending_sign_ins WHERE expires_at < $1`, now); err != nil {
		return tag.RowsAffected(), err
	}
	return tag.RowsAffected(), nil
}

// RotateShell swaps a shell session's access token and refresh token for new
// ones in one conditional update, only when the presented refresh token is the
// family's current one and the row is live. It reports whether it rotated.
func (s *Store) RotateShell(ctx context.Context, family uuid.UUID, oldRefresh, newIDHash, newRefresh []byte, now time.Time) (Session, bool, error) {
	var x Session
	err := s.db.GetExecutor(ctx).QueryRow(ctx,
		`UPDATE staff_sessions SET id_hash = $3, refresh_token_hash = $4, last_used_at = $5
		  WHERE token_family = $1 AND refresh_token_hash = $2 AND client_kind = 'shell'
		    AND revoked_at IS NULL AND absolute_expires_at > $5
		  RETURNING `+sessionCols, family, oldRefresh, newIDHash, newRefresh, now).
		Scan(&x.IDHash, &x.Sub, &x.AppwriteUserID, &x.ClientKind, &x.Roles, &x.RolesReadAt, &x.CreatedAt,
			&x.LastUsedAt, &x.IdleSeconds, &x.AbsoluteExpiresAt, &x.RevokedAt, &x.RevokedReason)
	if errors.Is(err, pgx.ErrNoRows) {
		return Session{}, false, nil
	}
	if err != nil {
		return Session{}, false, err
	}
	return x, true, nil
}

// RevokeFamily revokes every live session of a token family and reports how
// many it revoked.
func (s *Store) RevokeFamily(ctx context.Context, family uuid.UUID, reason string, now time.Time) (int64, error) {
	tag, err := s.db.GetExecutor(ctx).Exec(ctx,
		`UPDATE staff_sessions SET revoked_at = $2, revoked_reason = $3
		  WHERE token_family = $1 AND revoked_at IS NULL`, family, now, reason)
	return tag.RowsAffected(), err
}
