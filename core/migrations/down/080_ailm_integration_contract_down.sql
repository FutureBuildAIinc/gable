-- SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
-- SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

-- Reverse of migrations/080_ailm_integration_contract.sql.
--
-- WHY THIS FILE IS NOT A SIBLING OF THE UP MIGRATION
-- `cmd/migrate` applies every file matched by `migrations/*.sql` in sorted
-- order and records it in schema_migrations. A `080_..._down.sql` placed next
-- to the up migration would therefore be executed as a FORWARD migration
-- immediately after 080 and would drop the columns 080 had just added.
-- `migrations/down/` is outside that glob, so down scripts live here.
--
-- This is NOT run automatically. Apply it by hand, then delete the tracking row:
--
--   psql "$DATABASE_URL" -f migrations/down/080_ailm_integration_contract_down.sql
--   psql "$DATABASE_URL" -c "DELETE FROM schema_migrations WHERE version = '080_ailm_integration_contract.sql';"
--
-- DESTRUCTIVE: dropping the product geometry columns discards every dimension
-- an operator has entered, and dropping `staff` cascades to `module_grants`,
-- discarding AI_LM entitlements. Take a backup first.

BEGIN;

-- 4. Module registration.
DELETE FROM system_settings WHERE key = 'modules.ai_lm.enabled';

-- 3. Staff roster + grants. module_grants.staff_id is ON DELETE CASCADE, but it
--    is dropped explicitly first so the intent is not implicit.
DROP INDEX IF EXISTS idx_module_grants_module;
DROP TABLE IF EXISTS module_grants;
DROP TABLE IF EXISTS staff;

-- 2. Dispatch seam on orders.
DROP INDEX IF EXISTS idx_orders_scheduled_delivery_date;
ALTER TABLE orders DROP COLUMN IF EXISTS packing_manifest;
ALTER TABLE orders DROP COLUMN IF EXISTS scheduled_delivery_date;

-- 1. Product geometry.
ALTER TABLE products DROP COLUMN IF EXISTS geometry_source;
ALTER TABLE products DROP COLUMN IF EXISTS stackable;
ALTER TABLE products DROP COLUMN IF EXISTS height_in;
ALTER TABLE products DROP COLUMN IF EXISTS width_in;
ALTER TABLE products DROP COLUMN IF EXISTS length_in;

COMMIT;
