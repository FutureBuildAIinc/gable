-- SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
-- SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

-- 137: The manager step up at the counter (CP-04, CP-08, CP-11).
--
-- A manager approves at the terminal with their own counter PIN: a per staff
-- member secret set on the staff screen, hashed with argon2id, never stored or
-- logged in the clear. The staff row carries the hash, when it was set, the
-- consecutive failures and the lock five failures in a row impose until an
-- admin unlocks it.
--
-- A verified PIN mints one single use approval: an opaque token handed to the
-- cashier's screen and stored here only as its SHA-256 hash, bound to one act
-- (an override, a pickup's taker, a till sign off), one target (the sale, the
-- till session), what the manager saw and approved (an override's kind and
-- cap; a pickup's amount), the manager who approved, and an expiry a few
-- minutes ahead. The act's own transaction consumes it: one use, the right
-- act, target and terms only, never replayed.

ALTER TABLE staff
    ADD COLUMN IF NOT EXISTS counter_pin_hash TEXT,
    ADD COLUMN IF NOT EXISTS counter_pin_set_at TIMESTAMPTZ,
    ADD COLUMN IF NOT EXISTS counter_pin_failures INT NOT NULL DEFAULT 0,
    ADD COLUMN IF NOT EXISTS counter_pin_locked BOOLEAN NOT NULL DEFAULT FALSE;

CREATE TABLE IF NOT EXISTS pos_step_up_approvals (
    id               UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    act              TEXT NOT NULL CHECK (act IN ('override', 'pickup', 'signoff')),
    kind             TEXT CHECK (kind IS NULL OR kind IN ('account', 'check', 'refund_method')),
    target_id        UUID NOT NULL,
    amount_cents     BIGINT CHECK (amount_cents IS NULL OR amount_cents > 0),
    token_hash       TEXT NOT NULL CHECK (btrim(token_hash) <> ''),
    manager_staff_id UUID NOT NULL REFERENCES staff(id),
    manager_name     TEXT NOT NULL CHECK (btrim(manager_name) <> ''),
    created_at       TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    expires_at       TIMESTAMPTZ NOT NULL,
    consumed_at      TIMESTAMPTZ
);

-- One live token hash: a consumed row keeps its place in the audit trail and
-- frees the hash for none (a replayed token finds no live row either way).
CREATE UNIQUE INDEX IF NOT EXISTS idx_pos_step_up_approvals_live_token
    ON pos_step_up_approvals (token_hash)
    WHERE consumed_at IS NULL;

CREATE INDEX IF NOT EXISTS idx_pos_step_up_approvals_target
    ON pos_step_up_approvals (act, target_id)
    WHERE consumed_at IS NULL;
