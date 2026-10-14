-- SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
-- SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

-- Migration: 115_dealer_profile_email_deliveries
-- Description: two tables for the printed forms and the real email sender.
--
-- dealer_profile: the one dealer wide profile every printed form shows (legal
-- and trading name, contact lines, the remit to address and the logo). It is a
-- single row: the id is pinned to 1 by a CHECK, so a second profile cannot
-- exist and the write is always an update of the one row. The row is created
-- empty here so a read never finds nothing; the demo seed sets an invented
-- one. revision follows the wire contract (ADR 0001), so a write needs the
-- caller's precondition.
--
-- email_deliveries: one row per customer or vendor facing send, written by the
-- route that sent it: the recipient, the subject, the document it carried
-- (type, id and number), the sender's answer and any error. branch_id is the
-- carried document's branch where it has one, which is what walls the read.

CREATE TABLE IF NOT EXISTS dealer_profile (
    id                SMALLINT    PRIMARY KEY DEFAULT 1 CHECK (id = 1),
    legal_name        TEXT        NOT NULL DEFAULT '',
    trading_name      TEXT        NOT NULL DEFAULT '',
    phone             TEXT        NOT NULL DEFAULT '',
    email             TEXT        NOT NULL DEFAULT '',
    website           TEXT        NOT NULL DEFAULT '',
    remit_to          TEXT        NOT NULL DEFAULT '',
    logo              BYTEA       NULL,
    logo_content_type TEXT        NULL CHECK (logo_content_type IN ('image/png', 'image/jpeg')),
    revision          BIGINT      NOT NULL DEFAULT 1,
    created_at        TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at        TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    CHECK ((logo IS NULL) = (logo_content_type IS NULL))
);

INSERT INTO dealer_profile (id) VALUES (1) ON CONFLICT (id) DO NOTHING;

CREATE TABLE IF NOT EXISTS email_deliveries (
    id              UUID        PRIMARY KEY DEFAULT gen_random_uuid(),
    created_at      TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    branch_id       UUID        NULL REFERENCES locations (id),
    recipient       TEXT        NOT NULL,
    subject         TEXT        NOT NULL,
    document_type   TEXT        NOT NULL CHECK (document_type IN ('invoice', 'statement', 'quote', 'purchase_order')),
    document_id     UUID        NULL,
    document_number TEXT        NULL,
    status          TEXT        NOT NULL CHECK (status IN ('sent', 'failed')),
    sender_answer   TEXT        NULL,
    error           TEXT        NULL,
    sent_by         TEXT        NULL
);

CREATE INDEX IF NOT EXISTS email_deliveries_created_idx ON email_deliveries (created_at DESC, id DESC);
CREATE INDEX IF NOT EXISTS email_deliveries_branch_created_idx ON email_deliveries (branch_id, created_at DESC, id DESC);
CREATE INDEX IF NOT EXISTS email_deliveries_document_idx ON email_deliveries (document_type, document_id);
