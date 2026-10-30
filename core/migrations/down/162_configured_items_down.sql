-- SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
-- SPDX-FileCopyrightText: 2026, FutureBuild, Inc. and OpenLBM contributors

-- Rollback for 162_configured_items.sql.
--
-- Lives in migrations/down/ so cmd/migrate's `migrations/*.sql` glob cannot
-- pick it up and apply it as a forward migration. Apply by hand.
--
-- The configuration columns go with the lines that carry one: a quote,
-- order or invoice line that names its configuration loses the
-- configuration with the column, and the line keeps its price, which was
-- computed from it.
--
-- The option money columns go with them. The price of a configured item is
-- the sum over its selected options of their base and their adjustment, so
-- rolling the bases back to zero changes what every configured item costs
-- until the options are seeded again. The down script says so and stops.

-- The lines that carry a configuration must lose it first.
ALTER TABLE quote_lines DROP COLUMN IF EXISTS configuration_json;
ALTER TABLE order_lines DROP COLUMN IF EXISTS configuration_json;
ALTER TABLE invoice_lines DROP COLUMN IF EXISTS configuration_json;

-- The option money.
ALTER TABLE millwork_options DROP COLUMN IF EXISTS cost_adjustment_cents;
ALTER TABLE millwork_options DROP COLUMN IF EXISTS base_cost_cents;
ALTER TABLE millwork_options DROP COLUMN IF EXISTS base_price_cents;

-- The non-stock mark.
DROP INDEX IF EXISTS idx_products_is_non_stock;
ALTER TABLE products DROP COLUMN IF EXISTS is_non_stock;
