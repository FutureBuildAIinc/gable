-- SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
-- SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

-- Migration: 146_pos_offline_counter
-- Description: the offline counter schema (CP-13 slice O1, ADR 0011):
--   1. pos_offline_policy: per branch offline selling policy (enabled,
--      allowed tenders from CASH, CHECK, ACCOUNT, caps in integer cents and
--      counts, updated_by, revision), resolved over database wide defaults in
--      system_settings (pos.offline.*).
--   2. pos_register_devices: enrolled register Ed25519 keys, epoch, the
--      synced high sequence and chain head, the device's reported mark and
--      the mark the server last confirmed to it, with at most one active
--      (unrevoked) enrolment per register.
--   3. pos_offline_snapshots: branch catalogue, price, customer and tax
--      snapshots issued to enrolled registers per epoch.
--   4. pos_offline_exceptions: the manager's offline replay exception queue,
--      with partial unique indexes on sale_id and (register_id, device_epoch,
--      device_seq) that exclude sequence_reused, bad_signature and
--      replay_mismatch rows so duplicate or tampered replay attempts can be
--      recorded without displacing the original sale or exception. The
--      warning_alert_id column holds the customer warning a sale was sold
--      past offline (the CP-13 OW exception rides this table; no second
--      review table is added later).
--   5. pos_transactions: the offline provenance columns of ADR 0011 section 8
--      row O1 (epoch, sequence, offline reference, snapshot id, signature
--      digest, offline_session_id, replaces_sale_id with its replaced cart
--      lookup index, the declaration columns, the device's queue count and
--      total, the confirmed mark), the unique index on (register_id,
--      device_epoch, device_seq), and ABANDONED in the sale status CHECK.
--      The device's own clock is client_created_at (migration 044).
--   6. pos_sync_log: the refused and gap counts of ADR 0011 section 6.4.
--   7. OFFLINE_RATE in the orders and invoices tax_source CHECK constraints.

-- 1. Per branch offline counter policy (ADR 0011 section 4).
CREATE TABLE IF NOT EXISTS pos_offline_policy (
    branch_id             UUID PRIMARY KEY REFERENCES locations(id),
    enabled               BOOLEAN NOT NULL DEFAULT FALSE,
    tenders               TEXT[] NOT NULL DEFAULT '{}'::text[],
    sale_cap_cents        BIGINT NOT NULL DEFAULT 0 CHECK (sale_cap_cents >= 0),
    check_cap_cents       BIGINT NOT NULL DEFAULT 0 CHECK (check_cap_cents >= 0),
    account_cap_cents     BIGINT NOT NULL DEFAULT 0 CHECK (account_cap_cents >= 0),
    queue_total_cap_cents BIGINT NOT NULL DEFAULT 0 CHECK (queue_total_cap_cents >= 0),
    max_queued            INTEGER NOT NULL DEFAULT 0 CHECK (max_queued >= 0),
    updated_by            TEXT NOT NULL,
    updated_at            TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    revision              BIGINT NOT NULL DEFAULT 1,
    CONSTRAINT pos_offline_policy_tenders_check
        CHECK (tenders <@ ARRAY['CASH', 'CHECK', 'ACCOUNT']::text[])
);

-- 2. Enrolled register devices (ADR 0011 section 6.1): one active unrevoked
--    device per register; a new enrolment is a new epoch.
CREATE TABLE IF NOT EXISTS pos_register_devices (
    id            UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    register_id   VARCHAR(32) NOT NULL REFERENCES pos_registers(id),
    branch_id     UUID NOT NULL REFERENCES locations(id),
    epoch         BIGINT NOT NULL CHECK (epoch >= 1),
    public_key    TEXT NOT NULL CHECK (btrim(public_key) <> ''),
    enrolled_by   TEXT NOT NULL CHECK (btrim(enrolled_by) <> ''),
    enrolled_at   TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    revoked_by    TEXT,
    revoked_at    TIMESTAMPTZ,
    revoke_reason TEXT,
    high_seq      BIGINT NOT NULL DEFAULT 0 CHECK (high_seq >= 0),
    chain_head    TEXT NOT NULL DEFAULT '',
    reported_seq  BIGINT NOT NULL DEFAULT 0 CHECK (reported_seq >= 0),
    confirmed_seq BIGINT NOT NULL DEFAULT 0 CHECK (confirmed_seq >= 0),
    updated_at    TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    CONSTRAINT pos_register_devices_register_epoch_key UNIQUE (register_id, epoch)
);

CREATE UNIQUE INDEX IF NOT EXISTS uq_pos_register_devices_active
    ON pos_register_devices (register_id)
    WHERE revoked_at IS NULL;

CREATE INDEX IF NOT EXISTS idx_pos_register_devices_branch_active
    ON pos_register_devices (branch_id)
    WHERE revoked_at IS NULL;

-- 3. Offline snapshots issued to enrolled registers (ADR 0011 section 5.4).
CREATE TABLE IF NOT EXISTS pos_offline_snapshots (
    id            UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    register_id   VARCHAR(32) NOT NULL REFERENCES pos_registers(id),
    branch_id     UUID NOT NULL REFERENCES locations(id),
    device_epoch  BIGINT NOT NULL CHECK (device_epoch >= 1),
    business_date DATE NOT NULL,
    content       JSONB NOT NULL,
    digest        TEXT NOT NULL CHECK (btrim(digest) <> ''),
    etag          TEXT NOT NULL DEFAULT '',
    created_at    TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS idx_pos_offline_snapshots_register_epoch
    ON pos_offline_snapshots (register_id, device_epoch, created_at DESC);

CREATE INDEX IF NOT EXISTS idx_pos_offline_snapshots_branch
    ON pos_offline_snapshots (branch_id, created_at DESC);

-- 4. Offline replay exception queue (ADR 0011 sections 2 and 3.4).
CREATE TABLE IF NOT EXISTS pos_offline_exceptions (
    id                UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    sale_id           UUID NOT NULL,
    register_id       VARCHAR(32) NOT NULL REFERENCES pos_registers(id),
    branch_id         UUID NOT NULL REFERENCES locations(id),
    device_epoch      BIGINT NOT NULL CHECK (device_epoch >= 1),
    device_seq        BIGINT NOT NULL CHECK (device_seq >= 0),
    gap_from_seq      BIGINT,
    gap_to_seq        BIGINT,
    offline_ref       TEXT NOT NULL DEFAULT '',
    till_session_id   UUID REFERENCES till_sessions(id),
    business_date     DATE NOT NULL,
    snapshot_id       UUID REFERENCES pos_offline_snapshots(id),
    customer_id       UUID REFERENCES customers(id),
    warning_alert_id  UUID REFERENCES customer_alerts(id),
    signed_sale       JSONB NOT NULL DEFAULT '{}'::jsonb,
    signature         TEXT NOT NULL DEFAULT '',
    signature_digest  TEXT NOT NULL DEFAULT '',
    prev_digest       TEXT NOT NULL DEFAULT '',
    reason_code       TEXT NOT NULL,
    reason_message    TEXT NOT NULL DEFAULT '',
    cash_cents        BIGINT NOT NULL DEFAULT 0,
    check_cents       BIGINT NOT NULL DEFAULT 0,
    account_cents     BIGINT NOT NULL DEFAULT 0,
    total_cents       BIGINT NOT NULL DEFAULT 0,
    status            TEXT NOT NULL DEFAULT 'open'
        CHECK (status IN ('open', 'held', 'resolved', 'relinked', 'superseded', 'found', 'reviewed')),
    resolution_action TEXT
        CHECK (resolution_action IS NULL OR btrim(resolution_action) <> ''),
    resolution_reason TEXT,
    resolved_by       TEXT,
    resolved_at       TIMESTAMPTZ,
    posted_sale_id    UUID REFERENCES pos_transactions(id),
    revision          BIGINT NOT NULL DEFAULT 1,
    created_at        TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at        TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    CONSTRAINT pos_offline_exceptions_reason_code_check CHECK (btrim(reason_code) <> '')
);

CREATE UNIQUE INDEX IF NOT EXISTS uq_pos_offline_exceptions_sale_id
    ON pos_offline_exceptions (sale_id)
    WHERE reason_code NOT IN ('sequence_reused', 'bad_signature', 'replay_mismatch');

CREATE UNIQUE INDEX IF NOT EXISTS uq_pos_offline_exceptions_register_epoch_seq
    ON pos_offline_exceptions (register_id, device_epoch, device_seq)
    WHERE reason_code NOT IN ('sequence_reused', 'bad_signature', 'replay_mismatch');

CREATE INDEX IF NOT EXISTS idx_pos_offline_exceptions_branch_status
    ON pos_offline_exceptions (branch_id, status, created_at DESC);

CREATE INDEX IF NOT EXISTS idx_pos_offline_exceptions_till_session
    ON pos_offline_exceptions (till_session_id)
    WHERE till_session_id IS NOT NULL;

CREATE INDEX IF NOT EXISTS idx_pos_offline_exceptions_customer_open
    ON pos_offline_exceptions (customer_id)
    WHERE customer_id IS NOT NULL AND status IN ('open', 'held');

CREATE INDEX IF NOT EXISTS idx_pos_offline_exceptions_warning_alert
    ON pos_offline_exceptions (warning_alert_id)
    WHERE warning_alert_id IS NOT NULL;

-- 5. pos_transactions offline columns, unique index, and ABANDONED status
--    (ADR 0011 sections 2, 3.2, 5.3 and section 8 row O1).
ALTER TABLE pos_transactions ADD COLUMN IF NOT EXISTS device_epoch BIGINT;
ALTER TABLE pos_transactions ADD COLUMN IF NOT EXISTS device_seq BIGINT;
ALTER TABLE pos_transactions ADD COLUMN IF NOT EXISTS offline_ref TEXT;
ALTER TABLE pos_transactions ADD COLUMN IF NOT EXISTS snapshot_id UUID REFERENCES pos_offline_snapshots(id);
ALTER TABLE pos_transactions ADD COLUMN IF NOT EXISTS signature_digest TEXT;
ALTER TABLE pos_transactions ADD COLUMN IF NOT EXISTS offline_session_id UUID REFERENCES till_sessions(id);
ALTER TABLE pos_transactions ADD COLUMN IF NOT EXISTS replaces_sale_id UUID REFERENCES pos_transactions(id);
ALTER TABLE pos_transactions ADD COLUMN IF NOT EXISTS different_customer BOOLEAN NOT NULL DEFAULT FALSE;
ALTER TABLE pos_transactions ADD COLUMN IF NOT EXISTS unknown_cart_id UUID REFERENCES pos_transactions(id);
ALTER TABLE pos_transactions ADD COLUMN IF NOT EXISTS queue_count INTEGER;
ALTER TABLE pos_transactions ADD COLUMN IF NOT EXISTS queue_total_cents BIGINT;
ALTER TABLE pos_transactions ADD COLUMN IF NOT EXISTS confirmed_mark BIGINT;

DO $$ BEGIN
    ALTER TABLE pos_transactions ADD CONSTRAINT pos_transactions_device_epoch_check
        CHECK (device_epoch IS NULL OR device_epoch >= 1);
EXCEPTION WHEN duplicate_object THEN NULL;
END $$;

DO $$ BEGIN
    ALTER TABLE pos_transactions ADD CONSTRAINT pos_transactions_device_seq_check
        CHECK (device_seq IS NULL OR device_seq >= 1);
EXCEPTION WHEN duplicate_object THEN NULL;
END $$;

DO $$ BEGIN
    ALTER TABLE pos_transactions ADD CONSTRAINT pos_transactions_queue_count_check
        CHECK (queue_count IS NULL OR queue_count >= 0);
EXCEPTION WHEN duplicate_object THEN NULL;
END $$;

DO $$ BEGIN
    ALTER TABLE pos_transactions ADD CONSTRAINT pos_transactions_queue_total_cents_check
        CHECK (queue_total_cents IS NULL OR queue_total_cents >= 0);
EXCEPTION WHEN duplicate_object THEN NULL;
END $$;

DO $$ BEGIN
    ALTER TABLE pos_transactions ADD CONSTRAINT pos_transactions_confirmed_mark_check
        CHECK (confirmed_mark IS NULL OR confirmed_mark >= 0);
EXCEPTION WHEN duplicate_object THEN NULL;
END $$;

CREATE UNIQUE INDEX IF NOT EXISTS uq_pos_transactions_device_seq
    ON pos_transactions (register_id, device_epoch, device_seq)
    WHERE device_epoch IS NOT NULL AND device_seq IS NOT NULL;

CREATE INDEX IF NOT EXISTS idx_pos_transactions_replaces_sale
    ON pos_transactions (replaces_sale_id)
    WHERE replaces_sale_id IS NOT NULL;

ALTER TABLE pos_transactions DROP CONSTRAINT IF EXISTS pos_transactions_status_check;
ALTER TABLE pos_transactions ADD CONSTRAINT pos_transactions_status_check
    CHECK (status IN ('OPEN', 'HELD', 'COMPLETED', 'VOIDED', 'RETURNED', 'ABANDONED'));

-- 6. pos_sync_log additional counters (ADR 0011 section 6.4).
ALTER TABLE pos_sync_log ADD COLUMN IF NOT EXISTS refused_count INTEGER NOT NULL DEFAULT 0;
ALTER TABLE pos_sync_log ADD COLUMN IF NOT EXISTS gap_count INTEGER NOT NULL DEFAULT 0;

-- 7. OFFLINE_RATE in the orders and invoices tax_source CHECK constraints
--    (ADR 0011 section 3.3; migrations 092, 094 and 141 came before).
ALTER TABLE orders DROP CONSTRAINT IF EXISTS orders_tax_source_check;
ALTER TABLE orders ADD CONSTRAINT orders_tax_source_check
    CHECK (tax_source IN ('EXEMPT', 'PROVIDER', 'ZONE_RATE', 'SHIP_TO_RATE', 'BRANCH_RATE', 'OFFLINE_RATE', 'LEGACY'));

ALTER TABLE invoices DROP CONSTRAINT IF EXISTS invoices_tax_source_check;
ALTER TABLE invoices ADD CONSTRAINT invoices_tax_source_check
    CHECK (tax_source IN ('EXEMPT', 'PROVIDER', 'ZONE_RATE', 'SHIP_TO_RATE', 'BRANCH_RATE', 'OFFLINE_RATE', 'LEGACY'));
