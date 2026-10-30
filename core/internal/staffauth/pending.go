// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package staffauth

import (
	"context"
	"errors"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// Pending is one pending_sign_ins row: a redirect sign in in flight.
type Pending struct {
	StateHash  []byte
	Method     string
	ClientKind string
	ExpiresAt  time.Time
}

// CreatePending files a redirect sign in under the hash of its state.
func (s *Store) CreatePending(ctx context.Context, p Pending, now time.Time) error {
	_, err := s.db.GetExecutor(ctx).Exec(ctx,
		`INSERT INTO pending_sign_ins (state_hash, method, client_kind, created_at, expires_at)
		 VALUES ($1, $2, $3, $4, $5)`,
		p.StateHash, p.Method, p.ClientKind, now, p.ExpiresAt)
	return err
}

// ClaimPending spends a state: one conditional update flips the used flag on
// a row that is unused and not past its expiry, and only the call that
// changed the row gets it back. A state that comes back twice, or late, or
// that was never filed, finds nothing. The claim commits before the caller
// spends anything with Appwrite, which is what makes the state single use.
func (s *Store) ClaimPending(ctx context.Context, stateHash []byte, now time.Time) (Pending, bool, error) {
	p := Pending{StateHash: stateHash}
	err := s.db.GetExecutor(ctx).QueryRow(ctx,
		`UPDATE pending_sign_ins SET used = TRUE
		  WHERE state_hash = $1 AND NOT used AND expires_at > $2
		  RETURNING method, client_kind, expires_at`, stateHash, now).
		Scan(&p.Method, &p.ClientKind, &p.ExpiresAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return Pending{}, false, nil
	}
	if err != nil {
		return Pending{}, false, err
	}
	return p, true, nil
}

// AttachPerson records on a claimed row whose sign in it belongs to.
func (s *Store) AttachPerson(ctx context.Context, stateHash []byte, sub uuid.UUID, appwriteUserID string) error {
	_, err := s.db.GetExecutor(ctx).Exec(ctx,
		`UPDATE pending_sign_ins SET sub = $2, appwrite_user_id = $3 WHERE state_hash = $1 AND used`,
		stateHash, sub, appwriteUserID)
	return err
}
