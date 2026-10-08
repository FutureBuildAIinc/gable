-- SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
-- SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

-- Rollback for 081_price_protection.sql
--
-- This directory is OUTSIDE the migrator's glob (backend/cmd/migrate reads
-- migrations/*.sql only). Apply by hand:
--   psql "$DATABASE_URL" -f backend/migrations/down/081_price_protection_down.sql
--   DELETE FROM schema_migrations WHERE version = '081_price_protection.sql';
--
-- DESTRUCTIVE. Dropping quote_exposure_events discards the append-only
-- acknowledgment/override ledger, which is the audit record for every
-- contractual price decision the feature made. Export it first if the
-- deployment has ever run the exposure scanner in anger.

BEGIN;

-- 8. quotes rollup
DROP INDEX IF EXISTS idx_quotes_exposure_state;
ALTER TABLE quotes DROP CONSTRAINT IF EXISTS quotes_exposure_state_check;
ALTER TABLE quotes
    DROP COLUMN IF EXISTS exposure_last_checked_at,
    DROP COLUMN IF EXISTS exposure_dollars,
    DROP COLUMN IF EXISTS exposure_state;

-- 7. products index override + commodity flag
DROP INDEX IF EXISTS idx_products_is_commodity;
DROP INDEX IF EXISTS idx_products_market_index;
ALTER TABLE products
    DROP COLUMN IF EXISTS is_commodity,
    DROP COLUMN IF EXISTS market_index_id;

-- 6. customers escalation policy
ALTER TABLE customers DROP CONSTRAINT IF EXISTS customers_auto_escalate_requires_agreement;
ALTER TABLE customers DROP CONSTRAINT IF EXISTS customers_escalation_threshold_check;
ALTER TABLE customers DROP CONSTRAINT IF EXISTS customers_escalation_policy_check;
ALTER TABLE customers
    DROP COLUMN IF EXISTS escalation_agreement_ref,
    DROP COLUMN IF EXISTS escalation_agreement_signed_at,
    DROP COLUMN IF EXISTS escalation_threshold_pct,
    DROP COLUMN IF EXISTS price_escalation_policy;

-- 5. exposure event ledger
DROP TABLE IF EXISTS quote_exposure_events;

-- 4. price_escalators snapshot lifecycle fields
DROP INDEX IF EXISTS idx_price_escalators_state;
DROP INDEX IF EXISTS idx_price_escalators_index_active;
ALTER TABLE price_escalators DROP CONSTRAINT IF EXISTS price_escalators_current_state_check;
ALTER TABLE price_escalators
    DROP COLUMN IF EXISTS threshold_pct_at_snapshot,
    DROP COLUMN IF EXISTS policy_at_snapshot,
    DROP COLUMN IF EXISTS current_state,
    DROP COLUMN IF EXISTS last_checked_at,
    DROP COLUMN IF EXISTS base_index_recorded_at;

-- 3. category -> default index mapping
DROP TABLE IF EXISTS product_category_index_defaults;

-- 2. index history time-series
DROP TABLE IF EXISTS market_index_history;

-- 1. market_indices taxonomy. Remove the four rows seeded by 081 first, so the
-- three rows seeded by migration 023 are all that survive the rollback.
-- Escalators pointing at them have market_index_id ON DELETE SET NULL.
DELETE FROM market_indices
 WHERE index_code IN ('RL_SPF_2X4','RL_SYP_2X4','RL_OSB_716','MADISONS_COMP');

DROP INDEX IF EXISTS idx_market_indices_active;
ALTER TABLE market_indices DROP CONSTRAINT IF EXISTS market_indices_index_code_unique;
ALTER TABLE market_indices
    DROP COLUMN IF EXISTS is_active,
    DROP COLUMN IF EXISTS description,
    DROP COLUMN IF EXISTS commodity_kind,
    DROP COLUMN IF EXISTS index_code;

COMMIT;
