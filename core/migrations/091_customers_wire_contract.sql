-- SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
-- SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

-- 091: customers onto the wire contract (item C2-1, docs/adr/0005-sales-and-money-core.md
-- sections 7 and 13, docs/adr/0001-wire-contract.md).
--
--   1  created_at filled and NOT NULL, revision: the keyset list and the If-Match
--      precondition both need a value on every row from the first write.
--   2  the currency settings and customers.currency (the override; null means the
--      dealer default). One enabled code in v1, guarded by a trigger on the setting.
--   3  payment_terms, the master that replaces free text; customers.payment_terms_id.
--   4  customer_ship_tos, with one default MAIN ship-to per customer that has an address.
--   5  contact order authority, the PO required flag, the credit limit (0 is "no limit"
--      today and becomes NULL; a limit of 0 means no credit from now on) and a NOT NULL
--      balance_due.
--   6  one job table: customer_jobs is copied into projects (ids kept), the quotes and
--      pricing rules that named a job are re-pointed, then customer_jobs goes.
--   7  the keyset indexes.
--
-- Every step is idempotent; every backfill reads only columns an earlier step made
-- NOT NULL.

-- 1. created_at and revision.
UPDATE customers SET created_at = COALESCE(updated_at, NOW()) WHERE created_at IS NULL;
ALTER TABLE customers ALTER COLUMN created_at SET NOT NULL;
UPDATE customer_contacts SET created_at = COALESCE(updated_at, NOW()) WHERE created_at IS NULL;
ALTER TABLE customer_contacts ALTER COLUMN created_at SET NOT NULL;
UPDATE customers SET updated_at = created_at WHERE updated_at IS NULL;
UPDATE customer_contacts SET updated_at = created_at WHERE updated_at IS NULL;

ALTER TABLE customers ADD COLUMN IF NOT EXISTS revision BIGINT NOT NULL DEFAULT 1;
ALTER TABLE customer_contacts ADD COLUMN IF NOT EXISTS revision BIGINT NOT NULL DEFAULT 1;

-- 2. Currency. The dealer default and the enabled list live in system_settings; a
-- customer may override the default with an enabled code.
INSERT INTO system_settings (key, value) VALUES ('currency.default', 'USD')
ON CONFLICT (key) DO NOTHING;
INSERT INTO system_settings (key, value)
SELECT 'currency.enabled', value FROM system_settings WHERE key = 'currency.default'
ON CONFLICT (key) DO NOTHING;

ALTER TABLE customers ADD COLUMN IF NOT EXISTS currency CHAR(3) NULL;
DO $$ BEGIN
    ALTER TABLE customers ADD CONSTRAINT customers_currency_format CHECK (currency ~ '^[A-Z]{3}$');
EXCEPTION WHEN duplicate_object THEN NULL;
END $$;

-- Until the GL reports group by currency (C2-4) the enabled list holds one code, and
-- both settings are three capital letters. A trigger guards every writer, not only
-- the one that exists today.
CREATE OR REPLACE FUNCTION system_settings_currency_guard() RETURNS trigger
LANGUAGE plpgsql AS $$
BEGIN
    IF NEW.key = 'currency.default' AND NEW.value !~ '^[A-Z]{3}$' THEN
        RAISE EXCEPTION 'currency.default must be an ISO 4217 code of three capital letters'
            USING ERRCODE = '23514';
    END IF;
    IF NEW.key = 'currency.enabled' AND NEW.value !~ '^[A-Z]{3}$' THEN
        RAISE EXCEPTION 'currency.enabled holds exactly one code until the ledger groups by currency'
            USING ERRCODE = '23514';
    END IF;
    RETURN NEW;
END $$;

DROP TRIGGER IF EXISTS system_settings_currency_guard ON system_settings;
CREATE TRIGGER system_settings_currency_guard
    BEFORE INSERT OR UPDATE ON system_settings
    FOR EACH ROW EXECUTE FUNCTION system_settings_currency_guard();

-- 3. The payment terms master.
CREATE TABLE IF NOT EXISTS payment_terms (
    id               UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    code             TEXT NOT NULL UNIQUE,
    name             TEXT NOT NULL,
    kind             TEXT NOT NULL CHECK (kind IN ('NET_DAYS', 'DAY_OF_MONTH', 'DUE_ON_RECEIPT')),
    net_days         INTEGER NULL CHECK (net_days IS NULL OR net_days BETWEEN 0 AND 3650),
    day_of_month     INTEGER NULL CHECK (day_of_month IS NULL OR day_of_month BETWEEN 1 AND 31),
    discount_percent NUMERIC(7, 4) NULL CHECK (discount_percent IS NULL OR (discount_percent > 0 AND discount_percent <= 100)),
    discount_days    INTEGER NULL CHECK (discount_days IS NULL OR discount_days BETWEEN 0 AND 3650),
    is_active        BOOLEAN NOT NULL DEFAULT TRUE,
    revision         BIGINT NOT NULL DEFAULT 1,
    created_at       TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at       TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    CONSTRAINT payment_terms_kind_fields CHECK (
        (kind = 'NET_DAYS' AND net_days IS NOT NULL AND day_of_month IS NULL)
        OR (kind = 'DAY_OF_MONTH' AND day_of_month IS NOT NULL AND net_days IS NULL)
        OR (kind = 'DUE_ON_RECEIPT' AND net_days IS NULL AND day_of_month IS NULL)),
    CONSTRAINT payment_terms_discount_pair CHECK ((discount_percent IS NULL) = (discount_days IS NULL))
);

-- The seeded rows get distinct created_at values one millisecond apart: the list
-- orders on (created_at, id), and rows made by one statement would otherwise tie
-- and fall back to a random id order.
INSERT INTO payment_terms (code, name, kind, net_days, created_at, updated_at)
SELECT v.code, v.name, v.kind, v.net_days, NOW() + v.n * INTERVAL '1 millisecond', NOW() + v.n * INTERVAL '1 millisecond'
FROM (VALUES
    (0, 'NET30', 'Net 30', 'NET_DAYS', 30),
    (1, 'NET60', 'Net 60', 'NET_DAYS', 60),
    (2, 'NET90', 'Net 90', 'NET_DAYS', 90),
    (3, 'DUE_ON_RECEIPT', 'Due on receipt', 'DUE_ON_RECEIPT', NULL::integer),
    (4, 'COD', 'Cash on delivery', 'DUE_ON_RECEIPT', NULL::integer)
) AS v(n, code, name, kind, net_days)
ON CONFLICT (code) DO NOTHING;

-- Any other text found on a customer or an invoice becomes a NET_DAYS 30 term named by
-- its text, so no row loses its terms.
INSERT INTO payment_terms (code, name, kind, net_days)
SELECT DISTINCT t, t, 'NET_DAYS', 30
FROM (
    SELECT btrim(payment_terms::text) AS t FROM customers
    UNION
    SELECT btrim(payment_terms::text) FROM invoices
) legacy
WHERE t IS NOT NULL AND t <> ''
ON CONFLICT (code) DO NOTHING;

CREATE OR REPLACE FUNCTION payment_terms_default_id() RETURNS UUID
LANGUAGE sql STABLE AS $$ SELECT id FROM payment_terms WHERE code = 'NET30' $$;

ALTER TABLE customers ADD COLUMN IF NOT EXISTS payment_terms_id UUID NULL REFERENCES payment_terms (id);
UPDATE customers c
SET payment_terms_id = COALESCE(
    (SELECT pt.id FROM payment_terms pt WHERE pt.code = btrim(c.payment_terms::text)),
    payment_terms_default_id())
WHERE c.payment_terms_id IS NULL;
ALTER TABLE customers ALTER COLUMN payment_terms_id SET DEFAULT payment_terms_default_id();
ALTER TABLE customers ALTER COLUMN payment_terms_id SET NOT NULL;

-- 4. Ship-to addresses.
CREATE TABLE IF NOT EXISTS customer_ship_tos (
    id                    UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    customer_id           UUID NOT NULL REFERENCES customers (id) ON DELETE RESTRICT,
    code                  TEXT NOT NULL,
    name                  TEXT NOT NULL,
    line1                 TEXT NOT NULL,
    line2                 TEXT NULL,
    city                  TEXT NULL,
    region                TEXT NULL,
    postal_code           TEXT NULL,
    country               CHAR(2) NULL CHECK (country IS NULL OR country ~ '^[A-Z]{2}$'),
    phone                 TEXT NULL,
    delivery_instructions TEXT NULL,
    tax_rate              NUMERIC(9, 6) NULL CHECK (tax_rate IS NULL OR (tax_rate >= 0 AND tax_rate <= 1)),
    is_default            BOOLEAN NOT NULL DEFAULT FALSE,
    is_active             BOOLEAN NOT NULL DEFAULT TRUE,
    revision              BIGINT NOT NULL DEFAULT 1,
    created_at            TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at            TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    CONSTRAINT customer_ship_tos_code_key UNIQUE (customer_id, code)
);
CREATE UNIQUE INDEX IF NOT EXISTS customer_ship_tos_one_default
    ON customer_ship_tos (customer_id) WHERE is_default;

INSERT INTO customer_ship_tos (customer_id, code, name, line1, is_default, created_at, updated_at)
SELECT c.id, 'MAIN', c.name, btrim(c.address), TRUE, c.created_at, c.created_at
FROM customers c
WHERE btrim(COALESCE(c.address, '')) <> ''
ON CONFLICT (customer_id, code) DO NOTHING;

-- 5. Contacts, PO required, credit limit, balance.
ALTER TABLE customer_contacts ADD COLUMN IF NOT EXISTS can_place_orders BOOLEAN NOT NULL DEFAULT TRUE;
ALTER TABLE customer_contacts ADD COLUMN IF NOT EXISTS order_limit NUMERIC(12, 2) NULL;
DO $$ BEGIN
    ALTER TABLE customer_contacts ADD CONSTRAINT customer_contacts_order_limit_positive
        CHECK (order_limit IS NULL OR order_limit >= 0);
EXCEPTION WHEN duplicate_object THEN NULL;
END $$;

ALTER TABLE customers ADD COLUMN IF NOT EXISTS po_required BOOLEAN NOT NULL DEFAULT FALSE;

-- Today a limit of 0 means "no limit". The wire says so with null; a limit of 0 now
-- means no credit.
ALTER TABLE customers ALTER COLUMN credit_limit DROP DEFAULT;
UPDATE customers SET credit_limit = NULL WHERE credit_limit = 0;
DO $$ BEGIN
    ALTER TABLE customers ADD CONSTRAINT customers_credit_limit_nonnegative
        CHECK (credit_limit IS NULL OR credit_limit >= 0);
EXCEPTION WHEN duplicate_object THEN NULL;
END $$;

UPDATE customers SET balance_due = 0 WHERE balance_due IS NULL;
ALTER TABLE customers ALTER COLUMN balance_due SET DEFAULT 0;
ALTER TABLE customers ALTER COLUMN balance_due SET NOT NULL;

-- 6. One job table: projects. customer_jobs rows move over with their ids.
DO $$
DECLARE
    not_copied INTEGER := 0;
BEGIN
    IF to_regclass('public.customer_jobs') IS NULL THEN
        RETURN;
    END IF;

    -- A job with no customer takes the customer of the quotes that name it, when
    -- exactly one customer's quotes do.
    UPDATE customer_jobs cj
    SET customer_id = one.customer_id
    FROM (
        SELECT q.job_id, MIN(q.customer_id::text)::uuid AS customer_id
        FROM quotes q
        WHERE q.job_id IS NOT NULL
        GROUP BY q.job_id
        HAVING COUNT(DISTINCT q.customer_id) = 1
    ) one
    WHERE cj.id = one.job_id AND cj.customer_id IS NULL;

    INSERT INTO projects (id, customer_id, name, status, created_at, updated_at)
    SELECT cj.id, cj.customer_id, cj.name,
           CASE WHEN COALESCE(cj.is_active, TRUE) THEN 'Active' ELSE 'Inactive' END,
           COALESCE(cj.created_at, NOW()), COALESCE(cj.updated_at, cj.created_at, NOW())
    FROM customer_jobs cj
    WHERE cj.customer_id IS NOT NULL
    ON CONFLICT (id) DO NOTHING;

    SELECT COUNT(*) INTO not_copied FROM customer_jobs WHERE customer_id IS NULL;
    IF not_copied > 0 THEN
        RAISE NOTICE 'customer_jobs: % row(s) with no customer were not copied into projects', not_copied;
    END IF;

    -- Quotes keep the project they already name; otherwise they take the copied job.
    UPDATE quotes q
    SET project_id = q.job_id
    WHERE q.project_id IS NULL AND q.job_id IS NOT NULL
      AND EXISTS (SELECT 1 FROM projects p WHERE p.id = q.job_id);

    -- Pricing rules scoped to a job that was not copied are switched off, never
    -- widened to every job.
    UPDATE pricing_rules pr
    SET is_active = FALSE
    WHERE pr.job_id IS NOT NULL AND NOT EXISTS (SELECT 1 FROM projects p WHERE p.id = pr.job_id);
    UPDATE pricing_rules pr
    SET job_id = NULL
    WHERE pr.job_id IS NOT NULL AND NOT EXISTS (SELECT 1 FROM projects p WHERE p.id = pr.job_id);
END $$;

ALTER TABLE pricing_rules DROP CONSTRAINT IF EXISTS pricing_rules_job_id_fkey;
ALTER TABLE pricing_rules
    ADD CONSTRAINT pricing_rules_job_id_fkey FOREIGN KEY (job_id) REFERENCES projects (id);

ALTER TABLE quotes DROP CONSTRAINT IF EXISTS quotes_job_id_fkey;
ALTER TABLE quotes DROP COLUMN IF EXISTS job_id;
DROP TABLE IF EXISTS customer_jobs;

-- 7. Keyset indexes.
CREATE INDEX IF NOT EXISTS idx_customers_created_id ON customers (created_at DESC, id DESC);
CREATE INDEX IF NOT EXISTS idx_customer_ship_tos_customer ON customer_ship_tos (customer_id, created_at DESC, id DESC);
CREATE INDEX IF NOT EXISTS idx_payment_terms_created_id ON payment_terms (created_at DESC, id DESC);
CREATE INDEX IF NOT EXISTS idx_customers_payment_terms ON customers (payment_terms_id);
