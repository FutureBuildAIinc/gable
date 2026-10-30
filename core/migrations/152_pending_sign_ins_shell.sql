-- SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
-- SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

-- Migration 152: the shell's leg of a pending sign in (ADR 0012 slice 5).
--
-- A shell is a public client of the core. Its pending row also holds the
-- PKCE challenge (S256) and the validated return target across the provider
-- redirect, and then the hash of the one time code the callback hands the
-- shell, with that code's own expiry and its own used flag, because the row
-- is claimed twice: once for the state and once for the code.

ALTER TABLE pending_sign_ins
    ADD COLUMN IF NOT EXISTS code_challenge   TEXT        NULL,
    ADD COLUMN IF NOT EXISTS return_target    TEXT        NULL,
    ADD COLUMN IF NOT EXISTS code_hash        BYTEA       NULL,
    ADD COLUMN IF NOT EXISTS code_expires_at  TIMESTAMPTZ NULL,
    ADD COLUMN IF NOT EXISTS code_used        BOOLEAN     NOT NULL DEFAULT FALSE;

ALTER TABLE pending_sign_ins
    ADD CONSTRAINT pending_sign_ins_code_hash_len CHECK (code_hash IS NULL OR octet_length(code_hash) = 32),
    ADD CONSTRAINT pending_sign_ins_shell_leg CHECK (
        client_kind = 'shell' OR (code_challenge IS NULL AND return_target IS NULL AND code_hash IS NULL));

CREATE UNIQUE INDEX IF NOT EXISTS pending_sign_ins_code_hash_uniq ON pending_sign_ins (code_hash) WHERE code_hash IS NOT NULL;
