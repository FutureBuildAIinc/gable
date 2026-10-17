-- SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
-- SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

-- 124: AR credit holds, the credit limit change workflow, the controller's
-- worklist inputs, batch statement runs and periodic finance charges
-- (AR-02, AR-08, AR-04, AR-05). Everything here is additive: new tables, one
-- flag on customers, one widened CHECK on invoices.origin, one income
-- account. No migration writes a subledger row, a balance or a journal
-- entry.
--
-- tenant_id is null everywhere today: Gable is one database per dealer, so
-- the deployment itself is the tenant. The column names the tenant a row
-- belongs to should a shared deployment ever host several.
--
-- customers.credit_hold is the one flag the counter and the order paths read
-- today: every hold act (place, release, override expiry) keeps it in sync
-- with the standing holds. There is no house_accounts table in this schema;
-- the credit standing of an account lives on the customer row.

-- 1. The flag the hold acts sync.
ALTER TABLE customers ADD COLUMN IF NOT EXISTS credit_hold BOOLEAN NOT NULL DEFAULT false;

-- 2. credit_holds: one standing hold per customer. A hold stands while its
-- status is ACTIVE or OVERRIDDEN: an override is the same hold with a
-- time-bounded manager's window on it, never a release. The unique partial
-- index is the guarantee the sweep and the manual route share, so a second
-- hold cannot exist under concurrency.
CREATE TABLE IF NOT EXISTS credit_holds (
    id              UUID        PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id       UUID        NULL,
    customer_id     UUID        NOT NULL REFERENCES customers (id),
    reason          TEXT        NOT NULL CHECK (btrim(reason) <> ''),
    hold_type       TEXT        NOT NULL CHECK (hold_type IN ('OVER_LIMIT', 'PAST_DUE', 'MANUAL')),
    status          TEXT        NOT NULL DEFAULT 'ACTIVE' CHECK (status IN ('ACTIVE', 'RELEASED', 'OVERRIDDEN')),
    placed_by       TEXT        NULL,
    placed_at       TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    released_by     TEXT        NULL,
    released_at     TIMESTAMPTZ NULL,
    release_reason  TEXT        NULL,
    override_until  TIMESTAMPTZ NULL,
    override_by     TEXT        NULL,
    override_reason TEXT        NULL,
    max_order_cents BIGINT      NULL,
    metadata        JSONB       NOT NULL DEFAULT '{}',
    created_at      TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at      TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    CONSTRAINT credit_holds_release_columns CHECK (
        (status = 'RELEASED') = (released_at IS NOT NULL)
        AND (released_at IS NULL OR (released_by IS NOT NULL AND NULLIF(btrim(release_reason), '') IS NOT NULL))
    ),
    CONSTRAINT credit_holds_override_columns CHECK (
        (status = 'OVERRIDDEN') = (override_until IS NOT NULL)
        AND (override_until IS NULL OR (NULLIF(btrim(override_reason), '') IS NOT NULL AND override_by IS NOT NULL))
    )
);
CREATE UNIQUE INDEX IF NOT EXISTS ux_credit_holds_one_standing ON credit_holds (customer_id) WHERE status IN ('ACTIVE', 'OVERRIDDEN');
CREATE INDEX IF NOT EXISTS idx_credit_holds_status_placed ON credit_holds (status, placed_at DESC, id DESC);
CREATE INDEX IF NOT EXISTS idx_credit_holds_customer ON credit_holds (customer_id, created_at DESC);

-- 3. credit_limit_changes: the audit and workflow history of a limit. A row
-- is born PENDING; the act that approves it applies the new limit to
-- customers.credit_limit in the same transaction and stamps approved_by and
-- decided_at.
CREATE TABLE IF NOT EXISTS credit_limit_changes (
    id              UUID        PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id       UUID        NULL,
    customer_id     UUID        NOT NULL REFERENCES customers (id),
    old_limit_cents BIGINT      NOT NULL,
    new_limit_cents BIGINT      NOT NULL CHECK (new_limit_cents >= 0),
    requested_by    TEXT        NULL,
    approved_by     TEXT        NULL,
    status          TEXT        NOT NULL DEFAULT 'PENDING' CHECK (status IN ('PENDING', 'APPROVED', 'REJECTED')),
    reason          TEXT        NULL,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    decided_at      TIMESTAMPTZ NULL,
    CONSTRAINT credit_limit_changes_decided CHECK (
        (status = 'PENDING') = (decided_at IS NULL)
        AND (status = 'PENDING' OR approved_by IS NOT NULL)
    )
);
CREATE INDEX IF NOT EXISTS idx_credit_limit_changes_customer ON credit_limit_changes (customer_id, created_at DESC, id DESC);

-- 4. Batch statement runs and their per customer items. A run is an
-- artefact: the figures it recorded stand as they stood at period_end, and a
-- second run of the same period is another artefact, never a mutation of the
-- first.
CREATE TABLE IF NOT EXISTS ar_statement_runs (
    id                  UUID        PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id           UUID        NULL,
    period_start        DATE        NOT NULL,
    period_end          DATE        NOT NULL,
    as_of_date          DATE        NOT NULL,
    status              TEXT        NOT NULL DEFAULT 'RUNNING' CHECK (status IN ('RUNNING', 'COMPLETED', 'FAILED')),
    total_customers     INT         NOT NULL DEFAULT 0,
    queued_count        INT         NOT NULL DEFAULT 0,
    sent_count          INT         NOT NULL DEFAULT 0,
    total_balance_cents BIGINT      NOT NULL DEFAULT 0,
    triggered_by        TEXT        NULL,
    created_at          TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    completed_at        TIMESTAMPTZ NULL,
    CONSTRAINT ar_statement_runs_period CHECK (period_start <= period_end),
    CONSTRAINT ar_statement_runs_completed CHECK ((status = 'RUNNING') = (completed_at IS NULL))
);
CREATE INDEX IF NOT EXISTS idx_ar_statement_runs_created ON ar_statement_runs (created_at DESC, id DESC);

CREATE TABLE IF NOT EXISTS ar_statement_run_items (
    id                     UUID        PRIMARY KEY DEFAULT gen_random_uuid(),
    run_id                 UUID        NOT NULL REFERENCES ar_statement_runs (id) ON DELETE CASCADE,
    tenant_id              UUID        NULL,
    customer_id            UUID        NOT NULL REFERENCES customers (id),
    currency               CHAR(3)     NOT NULL,
    opening_balance_cents  BIGINT      NOT NULL DEFAULT 0,
    invoices_cents         BIGINT      NOT NULL DEFAULT 0,
    payments_cents         BIGINT      NOT NULL DEFAULT 0,
    finance_charges_cents  BIGINT      NOT NULL DEFAULT 0,
    closing_balance_cents  BIGINT      NOT NULL DEFAULT 0,
    delivery_status        TEXT        NOT NULL DEFAULT 'NOT_QUEUED'
        CHECK (delivery_status IN ('NOT_QUEUED', 'QUEUED', 'SENT', 'FAILED', 'SKIPPED')),
    outbox_id              UUID        NULL,
    aging_buckets          JSONB       NOT NULL DEFAULT '{}',
    created_at             TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    CONSTRAINT ar_statement_run_items_unique_customer UNIQUE (run_id, customer_id)
);
CREATE INDEX IF NOT EXISTS idx_ar_statement_run_items_customer ON ar_statement_run_items (customer_id, created_at DESC);

-- 5. Finance charges: the tenant's rules (one row, seeded off) and the runs
-- with their per customer items. A DRY_RUN run carries the preview items with
-- no invoice and no entry; a COMPLETED one links the generated invoice and
-- its balanced journal entry.
CREATE TABLE IF NOT EXISTS ar_finance_charge_rules (
    id                  UUID        PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id           UUID        NULL UNIQUE,
    monthly_rate_bps    BIGINT      NOT NULL DEFAULT 0 CHECK (monthly_rate_bps >= 0),
    grace_days          INT         NOT NULL DEFAULT 0 CHECK (grace_days >= 0),
    min_charge_cents    BIGINT      NOT NULL DEFAULT 0 CHECK (min_charge_cents >= 0),
    min_balance_cents   BIGINT      NOT NULL DEFAULT 0 CHECK (min_balance_cents >= 0),
    compound            BOOLEAN     NOT NULL DEFAULT false,
    income_account_code TEXT        NOT NULL DEFAULT '4060',
    updated_by          TEXT        NULL,
    updated_at          TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
INSERT INTO ar_finance_charge_rules (tenant_id) VALUES (NULL) ON CONFLICT DO NOTHING;

CREATE TABLE IF NOT EXISTS ar_finance_charge_runs (
    id                  UUID        PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id           UUID        NULL,
    as_of_date          DATE        NOT NULL,
    rate_bps            BIGINT      NOT NULL,
    grace_days          INT         NOT NULL,
    min_charge_cents    BIGINT      NOT NULL,
    status              TEXT        NOT NULL DEFAULT 'RUNNING' CHECK (status IN ('RUNNING', 'COMPLETED', 'FAILED', 'DRY_RUN')),
    total_charged_cents BIGINT      NOT NULL DEFAULT 0,
    customer_count      INT         NOT NULL DEFAULT 0,
    triggered_by        TEXT        NULL,
    created_at          TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    completed_at        TIMESTAMPTZ NULL,
    CONSTRAINT ar_finance_charge_runs_completed CHECK (
        (status IN ('RUNNING', 'DRY_RUN')) = (completed_at IS NULL)
    )
);
CREATE INDEX IF NOT EXISTS idx_ar_finance_charge_runs_created ON ar_finance_charge_runs (created_at DESC, id DESC);

CREATE TABLE IF NOT EXISTS ar_finance_charge_items (
    id                UUID        PRIMARY KEY DEFAULT gen_random_uuid(),
    run_id            UUID        NOT NULL REFERENCES ar_finance_charge_runs (id) ON DELETE CASCADE,
    tenant_id         UUID        NULL,
    customer_id       UUID        NOT NULL REFERENCES customers (id),
    currency          CHAR(3)     NOT NULL,
    overdue_cents     BIGINT      NOT NULL,
    charge_cents      BIGINT      NOT NULL CHECK (charge_cents >= 0),
    invoice_id        UUID        NULL REFERENCES invoices (id),
    journal_entry_id  UUID        NULL REFERENCES gl_journal_entries (id),
    created_at        TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    CONSTRAINT ar_finance_charge_items_unique_customer UNIQUE (run_id, customer_id),
    CONSTRAINT ar_finance_charge_items_documents CHECK (
        (invoice_id IS NULL) = (journal_entry_id IS NULL)
    )
);
CREATE INDEX IF NOT EXISTS idx_ar_finance_charge_items_customer ON ar_finance_charge_items (customer_id, created_at DESC);

-- 6. The finance charge invoice is a real invoice the AR core posts, so the
-- origin CHECK learns the one value the core writes it with.
ALTER TABLE invoices DROP CONSTRAINT IF EXISTS invoices_origin_check;
ALTER TABLE invoices ADD CONSTRAINT invoices_origin_check CHECK (origin IN ('ORDER', 'POS', 'FINANCE_CHARGE'));

-- 7. The income account the charge credits (Debit AR 1020 / Credit 4060),
-- beside 4050 and 5040 from migration 101.
INSERT INTO gl_accounts (code, name, type, subtype, normal_balance, description)
SELECT '4060', 'Finance Charge Income', 'REVENUE', 'Operating', 'CREDIT', 'Periodic finance charges on overdue receivables'
WHERE NOT EXISTS (SELECT 1 FROM gl_accounts WHERE code = '4060');
