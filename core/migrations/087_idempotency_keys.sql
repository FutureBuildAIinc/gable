-- SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
-- SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

-- 087: Durable idempotency keys.
--
-- pkg/middleware's idempotency layer kept its cached responses in an in-memory
-- map: one process, lost on restart, shared across every worker behind a load
-- balancer only by accident (i.e. not at all). An agent retrying a create
-- after a crash or a deploy got a second write. The gable-refactor inputs
-- name this as a live failure ("Idempotency is in-memory only").
--
-- This table is the durable replacement. One row per (principal, key):
--
--   principal    who is calling: "user:<JWT subject>" for the desk's tokens,
--                "portal:<customer>:<user>" for portal tokens, a machine key
--                id once scoped keys land, and the fixed "dev" principal
--                under AUTH_MODE=dev. Keyed on the caller so one principal's
--                key can never replay another's response.
--   key          the raw Idempotency-Key header value (X-Idempotency-Key is
--                accepted as a legacy alias).
--   fingerprint  sha256 of method, path, query string and request body. A key
--                reused with a different request is a client bug and gets
--                422, not a replay of an unrelated response.
--   claim_id     a UUID minted per claim. complete and release match on it,
--                so a holder whose lease lapsed and whose row was taken over
--                can no longer write its outcome onto (or delete) the new
--                holder's claim: the row belongs to whoever holds its current
--                claim_id.
--   state        "in_progress" (claimed, handler running elsewhere or the
--                process died mid-handler) or "complete" (replayable).
--   status_code / content_type / location / body
--                the stored response, replayed byte for byte on a hit.
--                location keeps a 201's or a 3xx's Location header so a
--                replayed response still points the client somewhere.
--                Set-Cookie is deliberately absent: a session cookie is
--                never replayed to a second request.
--   expires_at   doubles as the claim lease while in_progress (a claim whose
--                process died is takeable again once it lapses) and as the
--                retention horizon once complete (24h, matching the TTL the
--                in-memory store kept). The purge job deletes on this column.
--
-- The claim protocol is a single INSERT ... ON CONFLICT: an insert or the
-- takeover of an expired row hands the caller the claim; a conflict against a
-- live row returns nothing and the caller re-reads the row to decide between
-- 409 (in progress), 422 (fingerprint mismatch) and a replay (complete). No
-- transaction is held across the handler.
--
-- Idempotent: CREATE TABLE IF NOT EXISTS, guarded constraint and index.
-- Rollback: migrations/down/087_idempotency_keys_down.sql.

CREATE TABLE IF NOT EXISTS idempotency_keys (
    principal    TEXT        NOT NULL,
    key          TEXT        NOT NULL,
    fingerprint  TEXT        NOT NULL,
    claim_id     TEXT        NOT NULL,
    state        TEXT        NOT NULL DEFAULT 'in_progress'
                    CHECK (state IN ('in_progress', 'complete')),
    status_code  INT,
    content_type TEXT,
    location     TEXT,
    body         BYTEA,
    created_at   TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    expires_at   TIMESTAMPTZ NOT NULL,
    CONSTRAINT idempotency_keys_pkey PRIMARY KEY (principal, key)
);

-- Purge scans: DELETE ... WHERE expires_at < now() in batches. Without this
-- index that is a sequential scan per batch on a table every keyed POST and
-- PUT writes to.
CREATE INDEX IF NOT EXISTS idx_idempotency_keys_expires_at
    ON idempotency_keys (expires_at);
