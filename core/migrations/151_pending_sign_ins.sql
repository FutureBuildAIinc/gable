-- SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
-- SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

-- Migration 151: pending sign ins (ADR 0012 slice 4).
--
--   1  pending_sign_ins: one row per redirect method start, keyed by the hash
--      of the state (the raw state lives only in the browser's cookie and in
--      the query of the URL Appwrite redirects to). It holds the method, the
--      client kind, the expiry and a used flag, which the callback flips with
--      one conditional update before it calls Appwrite, so a state is single
--      use. Once the callback knows the person the row also holds the subject
--      and the Appwrite user id.
--   2  The oidc row of dealer_signin_methods keeps only its label: the issuer
--      is configured in Appwrite, so the stored issuer URL and client id go.

CREATE TABLE IF NOT EXISTS pending_sign_ins (
    state_hash       BYTEA       PRIMARY KEY CHECK (octet_length(state_hash) = 32),
    method           TEXT        NOT NULL CHECK (method IN ('magic_link', 'google', 'microsoft', 'oidc')),
    client_kind      TEXT        NOT NULL CHECK (client_kind IN ('web', 'shell')),
    created_at       TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    expires_at       TIMESTAMPTZ NOT NULL,
    used             BOOLEAN     NOT NULL DEFAULT FALSE,
    sub              UUID        NULL,
    appwrite_user_id TEXT        NULL CHECK (appwrite_user_id IS NULL OR char_length(appwrite_user_id) BETWEEN 1 AND 128)
);

CREATE INDEX IF NOT EXISTS pending_sign_ins_expiry_idx ON pending_sign_ins (expires_at);

COMMENT ON TABLE pending_sign_ins IS 'Redirect sign ins in flight (ADR 0012). Keyed by the SHA-256 of the state; the state is never stored.';

UPDATE dealer_signin_methods SET oidc_issuer_url = NULL, oidc_client_id = NULL WHERE method = 'oidc';
