-- SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
-- SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

-- Rollback for 091_customers_wire_contract.sql.
--
-- Lives in migrations/down/ so cmd/migrate's `migrations/*.sql` glob cannot
-- pick it up and apply it as a forward migration. Apply by hand.
--
-- What does not come back: a null credit limit becomes 0 again (the old "no limit");
-- payment terms master rows beyond the legacy text are lost with the table and each
-- customer's `payment_terms` text is not rewritten (it kept its old value, which the
-- up migration never changed); ship-to addresses, contact order authority, the PO
-- required flag and every revision are dropped; created_at stays NOT NULL. Jobs are
-- restored whole: every job the up migration copied (it kept the list), every job it
-- could not copy, each quote's job link (the link table, else its project when that
-- project was a copied job) and each pricing rule it moved out. A quote's own
-- project_id stays as it is.

CREATE TABLE IF NOT EXISTS customer_jobs (
    id          UUID PRIMARY KEY DEFAULT uuid_generate_v4(),
    customer_id UUID REFERENCES customers (id) ON DELETE CASCADE,
    name        TEXT NOT NULL,
    is_active   BOOLEAN DEFAULT TRUE,
    created_at  TIMESTAMPTZ DEFAULT NOW(),
    updated_at  TIMESTAMPTZ DEFAULT NOW()
);
CREATE INDEX IF NOT EXISTS idx_customer_jobs_customer_id ON customer_jobs (customer_id);

INSERT INTO customer_jobs (id, customer_id, name, is_active, created_at, updated_at)
SELECT p.id, p.customer_id, p.name, p.status <> 'Inactive', p.created_at, p.updated_at
FROM projects p
WHERE p.id IN (SELECT id FROM customer_jobs_copied)
ON CONFLICT (id) DO NOTHING;
INSERT INTO customer_jobs (id, customer_id, name, is_active, created_at, updated_at)
SELECT id, customer_id, name, is_active, created_at, updated_at FROM customer_jobs_unmigrated
ON CONFLICT (id) DO NOTHING;

ALTER TABLE pricing_rules DROP CONSTRAINT IF EXISTS pricing_rules_job_id_fkey;
INSERT INTO pricing_rules SELECT * FROM pricing_rules_unmigrated;
ALTER TABLE pricing_rules
    ADD CONSTRAINT pricing_rules_job_id_fkey FOREIGN KEY (job_id) REFERENCES customer_jobs (id);

ALTER TABLE quotes ADD COLUMN IF NOT EXISTS job_id UUID NULL;
UPDATE quotes q SET job_id = l.job_id FROM quote_jobs_unmigrated l WHERE l.quote_id = q.id;
UPDATE quotes q SET job_id = q.project_id
WHERE q.job_id IS NULL AND q.project_id IN (SELECT id FROM customer_jobs_copied);
ALTER TABLE quotes
    ADD CONSTRAINT quotes_job_id_fkey FOREIGN KEY (job_id) REFERENCES customer_jobs (id) ON DELETE SET NULL;

DROP TABLE IF EXISTS pricing_rules_unmigrated;
DROP TABLE IF EXISTS quote_jobs_unmigrated;
DROP TABLE IF EXISTS customer_jobs_unmigrated;
DROP TABLE IF EXISTS customer_jobs_copied;

DROP INDEX IF EXISTS idx_customers_payment_terms;
DROP INDEX IF EXISTS idx_payment_terms_created_id;
DROP INDEX IF EXISTS idx_customer_ship_tos_customer;
DROP INDEX IF EXISTS idx_customers_created_id;

ALTER TABLE customers ALTER COLUMN balance_due DROP NOT NULL;
ALTER TABLE customers DROP CONSTRAINT IF EXISTS customers_credit_limit_nonnegative;
UPDATE customers SET credit_limit = 0 WHERE credit_limit IS NULL;
ALTER TABLE customers ALTER COLUMN credit_limit SET DEFAULT 0;
ALTER TABLE customers DROP COLUMN IF EXISTS po_required;
ALTER TABLE customer_contacts DROP CONSTRAINT IF EXISTS customer_contacts_order_limit_positive;
ALTER TABLE customer_contacts DROP COLUMN IF EXISTS order_limit;
ALTER TABLE customer_contacts DROP COLUMN IF EXISTS can_place_orders;

DROP TABLE IF EXISTS customer_ship_tos;

ALTER TABLE customers DROP COLUMN IF EXISTS payment_terms_id;
DROP FUNCTION IF EXISTS payment_terms_default_id();
DROP TABLE IF EXISTS payment_terms;

DROP TRIGGER IF EXISTS system_settings_currency_guard ON system_settings;
DROP FUNCTION IF EXISTS system_settings_currency_guard();
ALTER TABLE customers DROP CONSTRAINT IF EXISTS customers_currency_format;
ALTER TABLE customers DROP COLUMN IF EXISTS currency;
DELETE FROM system_settings WHERE key IN ('currency.default', 'currency.enabled');

ALTER TABLE customer_contacts DROP COLUMN IF EXISTS revision;
ALTER TABLE customers DROP COLUMN IF EXISTS revision;
