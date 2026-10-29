-- SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
-- SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

-- Migration 141 down: drop the zone tables and the columns the documents and
-- the counter sale gained.

ALTER TABLE pos_transactions
    DROP CONSTRAINT IF EXISTS pos_transactions_delivery_type_check,
    DROP COLUMN IF EXISTS delivery_type,
    DROP COLUMN IF EXISTS ship_to_snapshot,
    DROP COLUMN IF EXISTS ship_to_id;

ALTER TABLE invoices
    DROP COLUMN IF EXISTS tax_zone_source,
    DROP COLUMN IF EXISTS tax_rate_parts,
    DROP COLUMN IF EXISTS tax_zone_id;

ALTER TABLE orders
    DROP COLUMN IF EXISTS tax_zone_source,
    DROP COLUMN IF EXISTS tax_rate_parts,
    DROP COLUMN IF EXISTS tax_zone_id;

ALTER TABLE invoices DROP CONSTRAINT IF EXISTS invoices_tax_source_check;
ALTER TABLE invoices
    ADD CONSTRAINT invoices_tax_source_check
        CHECK (tax_source IN ('EXEMPT', 'PROVIDER', 'SHIP_TO_RATE', 'BRANCH_RATE', 'LEGACY'));

ALTER TABLE orders DROP CONSTRAINT IF EXISTS orders_tax_source_check;
ALTER TABLE orders
    ADD CONSTRAINT orders_tax_source_check
        CHECK (tax_source IN ('EXEMPT', 'PROVIDER', 'SHIP_TO_RATE', 'BRANCH_RATE', 'LEGACY'));

DROP INDEX IF EXISTS tax_zones_country;
DROP INDEX IF EXISTS tax_zone_boundaries_lookup;
DROP TABLE IF EXISTS tax_zone_boundaries;
DROP TABLE IF EXISTS tax_zone_rates;
DROP TABLE IF EXISTS tax_zones;
