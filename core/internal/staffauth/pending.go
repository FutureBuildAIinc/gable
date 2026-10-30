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
	// The shell's leg: the PKCE challenge (S256) and the validated return
	// target. Empty for a web sign in.
	CodeChallenge string
	ReturnTarget  string
}

// CreatePending files a redirect sign in under the hash of its state.
func (s *Store) CreatePending(ctx context.Context, p Pending, now time.Time) error {
	_, err := s.db.GetExecutor(ctx).Exec(ctx,
		`INSERT INTO pending_sign_ins (state_hash, method, client_kind, created_at, expires_at, code_challenge, return_target)
		 VALUES ($1, $2, $3, $4, $5, NULLIF($6, ''), NULLIF($7, ''))`,
		p.StateHash, p.Method, p.ClientKind, now, p.ExpiresAt, p.CodeChallenge, p.ReturnTarget)
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
		  RETURNING method, client_kind, expires_at, COALESCE(code_challenge, ''), COALESCE(return_target, '')`, stateHash, now).
		Scan(&p.Method, &p.ClientKind, &p.ExpiresAt, &p.CodeChallenge, &p.ReturnTarget)
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

// SetCode files the hash of the one time code the callback hands a shell,
// with its expiry, on a claimed shell row that holds none yet.
func (s *Store) SetCode(ctx context.Context, stateHash, codeHash []byte, expires time.Time) (bool, error) {
	tag, err := s.db.GetExecutor(ctx).Exec(ctx,
		`UPDATE pending_sign_ins SET code_hash = $2, code_expires_at = $3
		  WHERE state_hash = $1 AND used AND client_kind = 'shell' AND code_hash IS NULL`,
		stateHash, codeHash, expires)
	return tag.RowsAffected() == 1, err
}

// ClaimCode spends a shell's one time code: one conditional update flips
// code_used on a row whose code is unused, unexpired, and whose PKCE
// challenge is the one the presented verifier derives, and only the call that
// changed the row gets the person back. A wrong verifier changes nothing, so
// a stolen code cannot be burned by guessing.
func (s *Store) ClaimCode(ctx context.Context, codeHash []byte, challenge string, now time.Time) (sub uuid.UUID, appwriteUserID string, ok bool, err error) {
	err = s.db.GetExecutor(ctx).QueryRow(ctx,
		`UPDATE pending_sign_ins SET code_used = TRUE
		  WHERE code_hash = $1 AND NOT code_used AND code_expires_at > $2
		    AND code_challenge = $3 AND sub IS NOT NULL AND appwrite_user_id IS NOT NULL
		  RETURNING sub, appwrite_user_id`, codeHash, now, challenge).Scan(&sub, &appwriteUserID)
	if errors.Is(err, pgx.ErrNoRows) {
		return uuid.Nil, "", false, nil
	}
	if err != nil {
		return uuid.Nil, "", false, err
	}
	return sub, appwriteUserID, true, nil
}
