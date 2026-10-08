// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package middleware

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"log"
	"net/url"
	"slices"
	"strings"
	"time"

	"github.com/gablelbm/gable/pkg/database"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// States of an idempotency_keys row (migration 087). in_progress means a
// claim is held: either a handler is running somewhere or the process that
// held the claim died mid-handler (the claim lease reclaims it). complete
// means the stored response is replayable until it expires.
const (
	idempotencyStateInProgress = "in_progress"
	idempotencyStateComplete   = "complete"
)

// The claim lease bounds how long an in_progress row can block a retry: it
// must outlive the slowest legitimate handler (the server's WriteTimeout is
// 15s; ten minutes is generous) and is short enough that a process that died
// mid-handler stops answering 409 quickly.
const idempotencyClaimLease = 10 * time.Minute

// errIdempotencyClaimRaced marks the window where the row our INSERT
// conflicted with was released before we could read it back; the caller
// retries the claim.
var errIdempotencyClaimRaced = errors.New("idempotency claim raced with a release")

// idempotencyClaimAttempts bounds the claim/retry loop. Each attempt needs a
// concurrent release on the same key to lose, so three is far beyond what any
// real request pattern produces.
const idempotencyClaimAttempts = 3

// idempotencyHolder is what another request left on a key we could not claim.
type idempotencyHolder struct {
	fingerprint string
	state       string
	statusCode  int
	contentType string
	location    string
	body        []byte
}

// idempotencyStore mediates every idempotency decision through the
// idempotency_keys table. Every statement is a short autocommitted pool
// statement: none holds a connection or a transaction across the handler,
// which is what keeps a pool of 4 deadlock-free under contention.
type idempotencyStore struct {
	db *database.DB
}

// claim inserts an in_progress row for (principal, key), or takes the row
// over when its lease has lapsed. Each claim carries a freshly minted UUID
// (claim_id): it identifies the holder that owns the row right now, and
// complete and release must match on it so a lapsed holder cannot touch a
// successor's claim. claimed=true means the caller owns the claim under the
// returned claimID and must run the handler, then complete or release it.
// claimed=false comes with the current holder's row so the caller can answer
// 409, 422 or a replay; a zero holder means the holder released between our
// conflict and our read (errIdempotencyClaimRaced wrapped in the race
// sentinel contract via acquire).
func (s *idempotencyStore) claim(ctx context.Context, principal, key, fingerprint string, leaseUntil time.Time) (bool, string, idempotencyHolder, error) {
	claimID := uuid.NewString()
	var returnedID string
	err := s.db.Pool.QueryRow(ctx, `
		INSERT INTO idempotency_keys (principal, key, fingerprint, claim_id, state, expires_at)
		VALUES ($1, $2, $3, $4, 'in_progress', $5)
		ON CONFLICT (principal, key) DO UPDATE
			SET fingerprint  = EXCLUDED.fingerprint,
			    claim_id     = EXCLUDED.claim_id,
			    state        = 'in_progress',
			    status_code  = NULL,
			    content_type = NULL,
			    location     = NULL,
			    body         = NULL,
			    created_at   = NOW(),
			    expires_at   = EXCLUDED.expires_at
			WHERE idempotency_keys.expires_at < NOW()
		RETURNING claim_id`,
		principal, key, fingerprint, claimID, leaseUntil).Scan(&returnedID)
	if errors.Is(err, pgx.ErrNoRows) {
		// The key is held by a live row. Read it to decide what to answer.
		// This runs after the claim statement committed, so it sees the
		// holder's state as of now, not as of some earlier snapshot.
		holder, lerr := s.lookup(ctx, principal, key)
		if lerr != nil {
			return false, "", idempotencyHolder{}, fmt.Errorf("read idempotency holder for key %q: %w", key, lerr)
		}
		if holder.state == "" {
			return false, "", idempotencyHolder{}, errIdempotencyClaimRaced
		}
		return false, "", holder, nil
	}
	if err != nil {
		return false, "", idempotencyHolder{}, fmt.Errorf("claim idempotency key %q: %w", key, err)
	}
	return true, returnedID, idempotencyHolder{state: idempotencyStateInProgress}, nil
}

// lookup reads the current row for a key we could not claim. The zero holder
// (no state) means no row exists.
func (s *idempotencyStore) lookup(ctx context.Context, principal, key string) (idempotencyHolder, error) {
	var h idempotencyHolder
	err := s.db.Pool.QueryRow(ctx, `
		SELECT fingerprint, state, COALESCE(status_code, 0), COALESCE(content_type, ''), COALESCE(location, ''), COALESCE(body, ''::bytea)
		FROM idempotency_keys
		WHERE principal = $1 AND key = $2`,
		principal, key).Scan(&h.fingerprint, &h.state, &h.statusCode, &h.contentType, &h.location, &h.body)
	if errors.Is(err, pgx.ErrNoRows) {
		return idempotencyHolder{}, nil //nolint:nilerr // absent row is "no holder", not a failure
	}
	if err != nil {
		return idempotencyHolder{}, err
	}
	return h, nil
}

// acquire claims the key, retrying the rare race where the holder released
// between our conflict and our read. On success it returns the claim's token,
// which the caller must hand back to complete and release.
func (s *idempotencyStore) acquire(ctx context.Context, principal, key, fingerprint string, leaseUntil time.Time) (bool, string, idempotencyHolder, error) {
	var claimed bool
	var claimID string
	var holder idempotencyHolder
	var err error
	for attempt := 0; attempt < idempotencyClaimAttempts; attempt++ {
		claimed, claimID, holder, err = s.claim(ctx, principal, key, fingerprint, leaseUntil)
		if !errors.Is(err, errIdempotencyClaimRaced) {
			return claimed, claimID, holder, err
		}
	}
	return false, "", idempotencyHolder{}, fmt.Errorf("idempotency key %q kept racing a release after %d attempts", key, idempotencyClaimAttempts)
}

// complete stores a 2xx/3xx outcome for replay. Only the row still held under
// the given claim token is updated: a claim whose lease lapsed and was taken
// over must not have an older handler's response written onto the new
// claimant's row.
func (s *idempotencyStore) complete(ctx context.Context, principal, key, claimID string, status int, contentType, location string, body []byte, retainUntil time.Time) error {
	_, err := s.db.Pool.Exec(ctx, `
		UPDATE idempotency_keys
		SET state = $3, status_code = $4, content_type = $5, location = $6, body = $7, expires_at = $8
		WHERE principal = $1 AND key = $2 AND state = $9`,
		principal, key, idempotencyStateComplete, status, contentType, location, body, retainUntil, idempotencyStateInProgress)
	return err
}

// release deletes an in_progress claim so a retry can run. Used for 4xx/5xx
// outcomes (nothing is stored for them) and when the handler panicked. Only
// the row still held under the given claim token is deleted: a lapsed holder
// must not delete a successor's live claim (that would open the exact double
// write this table exists to prevent).
func (s *idempotencyStore) release(ctx context.Context, principal, key, claimID string) error {
	_, err := s.db.Pool.Exec(ctx, `
		DELETE FROM idempotency_keys
		WHERE principal = $1 AND key = $2 AND state = 'in_progress'`,
		principal, key)
	return err
}

// PurgeExpiredIdempotencyKeys deletes expired idempotency rows in batches and
// returns how many went. Batched (FOR UPDATE SKIP LOCKED, LIMIT per batch) so
// a purge never takes one long lock on a table keyed POSTs and PUTs write to,
// and so two purges running at once help instead of blocking each other. The
// loop stops on the first short batch; an empty table costs one index scan.
func PurgeExpiredIdempotencyKeys(ctx context.Context, db *database.DB, batchSize int) (int64, error) {
	if db == nil {
		return 0, errors.New("PurgeExpiredIdempotencyKeys: nil database")
	}
	if batchSize < 1 {
		batchSize = 1
	}
	var total int64
	for {
		tag, err := db.Pool.Exec(ctx, `
			WITH expired AS (
				SELECT principal, key
				FROM idempotency_keys
				WHERE expires_at < NOW()
				ORDER BY expires_at
				LIMIT $1
				FOR UPDATE SKIP LOCKED
			)
			DELETE FROM idempotency_keys p
			USING expired e
			WHERE p.principal = e.principal AND p.key = e.key`,
			batchSize)
		if err != nil {
			return total, fmt.Errorf("purge idempotency_keys batch: %w", err)
		}
		n := tag.RowsAffected()
		total += n
		if n < int64(batchSize) {
			return total, nil
		}
	}
}

// requestFingerprint binds an idempotency key to one request: the method, the
// path, the query string (in canonical order, so two spellings of the same
// query are one request) and the body. A key reused for a different request
// is a client bug and is refused (422) rather than answered with a stored
// response for that other request.
func requestFingerprint(method, path, query string, body []byte) string {
	h := sha256.New()
	h.Write([]byte(method))
	h.Write([]byte{0})
	h.Write([]byte(path))
	h.Write([]byte{0})
	h.Write([]byte(query))
	h.Write([]byte{0})
	h.Write(body)
	return hex.EncodeToString(h.Sum(nil))
}

// canonicalQuery renders a query string ordered by key then value, so
// "?b=2&a=1" and "?a=1&b=2" are the same request while any difference in the
// parameters themselves is a different request.
func canonicalQuery(q url.Values) string {
	if len(q) == 0 {
		return ""
	}
	keys := make([]string, 0, len(q))
	for k := range q {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	var b strings.Builder
	for _, k := range keys {
		vals := slices.Clone(q[k])
		slices.Sort(vals)
		for _, v := range vals {
			b.WriteString(url.QueryEscape(k))
			b.WriteByte('=')
			b.WriteString(url.QueryEscape(v))
			b.WriteByte('&')
		}
	}
	return b.String()
}

// logIdempotencyError reports post-handler bookkeeping failures (complete or
// release writes). The request itself already succeeded or failed on its own
// merits; what is at stake is only whether the next retry replays or re-runs,
// so a warning, not an error.
func logIdempotencyError(op, key string, err error) {
	log.Printf("idempotency: %s failed for key %q: %v", op, key, err)
}
