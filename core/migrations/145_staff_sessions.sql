-- SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
-- SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

-- Migration 145: staff sign in sessions (ADR 0012 slice 2).
--
--   1  staff_sessions: the core's own session, keyed by the hash of the
--      session id (the id itself is never stored). It holds the subject (the
--      name based UUID), the Appwrite user id kept for refresh, the client
--      kind, the roles read at the last membership read and when that read
--      was made, the creation and last use, the idle and absolute limits, a
--      revoked mark, and for a shell the hash of its current refresh token
--      and its token family.
--   2  staff.sub: the link from a roster row to the person who signs in, a
--      nullable column that is unique when set.

CREATE TABLE IF NOT EXISTS staff_sessions (
    id_hash             BYTEA       PRIMARY KEY CHECK (octet_length(id_hash) = 32),
    sub                 UUID        NOT NULL,
    appwrite_user_id    TEXT        NOT NULL CHECK (char_length(appwrite_user_id) BETWEEN 1 AND 128),
    client_kind         TEXT        NOT NULL CHECK (client_kind IN ('web', 'shell')),
    roles               TEXT[]      NOT NULL CHECK (cardinality(roles) > 0),
    roles_read_at       TIMESTAMPTZ NOT NULL,
    created_at          TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    last_used_at        TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    idle_seconds        INTEGER     NOT NULL CHECK (idle_seconds > 0),
    absolute_expires_at TIMESTAMPTZ NOT NULL,
    revoked_at          TIMESTAMPTZ NULL,
    revoked_reason      TEXT        NULL,
    refresh_token_hash  BYTEA       NULL CHECK (refresh_token_hash IS NULL OR octet_length(refresh_token_hash) = 32),
    token_family        UUID        NULL,
    CHECK (client_kind = 'shell' OR (refresh_token_hash IS NULL AND token_family IS NULL))
);

CREATE INDEX IF NOT EXISTS staff_sessions_sub_idx ON staff_sessions (sub) WHERE revoked_at IS NULL;
CREATE INDEX IF NOT EXISTS staff_sessions_user_idx ON staff_sessions (appwrite_user_id);
CREATE INDEX IF NOT EXISTS staff_sessions_family_idx ON staff_sessions (token_family) WHERE token_family IS NOT NULL;
CREATE INDEX IF NOT EXISTS staff_sessions_expiry_idx ON staff_sessions (absolute_expires_at);

COMMENT ON TABLE staff_sessions IS 'Staff sign in sessions (ADR 0012). Keyed by the SHA-256 of the session id; the id is never stored.';

ALTER TABLE staff ADD COLUMN IF NOT EXISTS sub UUID NULL;
CREATE UNIQUE INDEX IF NOT EXISTS staff_sub_uniq ON staff (sub) WHERE sub IS NOT NULL;
