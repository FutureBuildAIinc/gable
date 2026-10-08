-- SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
-- SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

-- 093: catalog and pricing onto the wire contract (item C3-1, ADR 0006
-- sections 7.1 to 7.3 and 8's C3-1 steps, ADR 0001).
--
--   1  created_at filled and NOT NULL on products, price_levels,
--      customer_contracts, pricing_rules and category_pricing_rules (the
--      latter two already carry it); locations.created_at joins them, because
--      step 3's keyset index on locations orders on it and a keyset position
--      needs a NOT NULL column (ADR 0001 section 2). revision on products,
--      locations, pricing_rules, category_pricing_rules and customer_contracts.
--   2  widened to NUMERIC(12,4), the bound the wire's Quantity enforces, so a
--      quantity the wire accepts is one the column holds: products.reorder_point
--      and reorder_qty, and inventory.allocated (DECIMAL(10,4) since migration
--      004; stocking in the finest unit, LF or EA, makes a million plausible).
--      The percentage columns of pricing_rules and category_pricing_rules
--      (NUMERIC(6,4) since migrations 016 and 050, so 100 or 150 overflowed
--      with a 500) widen to the same bound, because the wire reads a
--      percentage as a decimal string at scale 4.
--   3  the keyset indexes for the converted lists, each ordering on
--      (created_at DESC, id DESC).
--
-- Every step is idempotent; every backfill reads only columns earlier steps
-- made NOT NULL.

-- 1. created_at and revision.
UPDATE products SET created_at = COALESCE(updated_at, NOW()) WHERE created_at IS NULL;
ALTER TABLE products ALTER COLUMN created_at SET NOT NULL;
UPDATE products SET updated_at = created_at WHERE updated_at IS NULL;

UPDATE price_levels SET created_at = COALESCE(updated_at, NOW()) WHERE created_at IS NULL;
ALTER TABLE price_levels ALTER COLUMN created_at SET NOT NULL;
UPDATE price_levels SET updated_at = created_at WHERE updated_at IS NULL;

UPDATE customer_contracts SET created_at = COALESCE(updated_at, NOW()) WHERE created_at IS NULL;
ALTER TABLE customer_contracts ALTER COLUMN created_at SET NOT NULL;
UPDATE customer_contracts SET updated_at = created_at WHERE updated_at IS NULL;

UPDATE locations SET created_at = COALESCE(updated_at, NOW()) WHERE created_at IS NULL;
ALTER TABLE locations ALTER COLUMN created_at SET NOT NULL;
UPDATE locations SET updated_at = created_at WHERE updated_at IS NULL;

UPDATE pricing_rules SET created_at = COALESCE(updated_at, NOW()) WHERE created_at IS NULL;
UPDATE category_pricing_rules SET created_at = COALESCE(updated_at, NOW()) WHERE created_at IS NULL;

ALTER TABLE products ADD COLUMN IF NOT EXISTS revision BIGINT NOT NULL DEFAULT 1;
ALTER TABLE locations ADD COLUMN IF NOT EXISTS revision BIGINT NOT NULL DEFAULT 1;
ALTER TABLE pricing_rules ADD COLUMN IF NOT EXISTS revision BIGINT NOT NULL DEFAULT 1;
ALTER TABLE category_pricing_rules ADD COLUMN IF NOT EXISTS revision BIGINT NOT NULL DEFAULT 1;
ALTER TABLE customer_contracts ADD COLUMN IF NOT EXISTS revision BIGINT NOT NULL DEFAULT 1;

-- 2. Widened to NUMERIC(12,4). v_inventory_with_branch selects inventory.*,
-- so it steps aside for the alteration and comes back unchanged (migration
-- 068's definition, verbatim).
DROP VIEW IF EXISTS v_inventory_with_branch;
ALTER TABLE products
    ALTER COLUMN reorder_point TYPE NUMERIC(12,4),
    ALTER COLUMN reorder_qty TYPE NUMERIC(12,4);
ALTER TABLE inventory
    ALTER COLUMN allocated TYPE NUMERIC(12,4);
CREATE VIEW v_inventory_with_branch AS
SELECT i.*, l.branch_id AS branch_id
  FROM inventory i
  JOIN locations l ON l.id = i.location_id;

ALTER TABLE pricing_rules
    ALTER COLUMN discount_pct TYPE NUMERIC(12,4),
    ALTER COLUMN markup_pct TYPE NUMERIC(12,4),
    ALTER COLUMN margin_floor_pct TYPE NUMERIC(12,4);
ALTER TABLE category_pricing_rules
    ALTER COLUMN margin_floor_pct TYPE NUMERIC(12,4);

-- 3. Keyset indexes for the converted lists.
CREATE INDEX IF NOT EXISTS idx_products_created_at_id ON products (created_at DESC, id DESC);
CREATE INDEX IF NOT EXISTS idx_locations_created_at_id ON locations (created_at DESC, id DESC);
CREATE INDEX IF NOT EXISTS idx_pricing_rules_created_at_id ON pricing_rules (created_at DESC, id DESC);
CREATE INDEX IF NOT EXISTS idx_category_pricing_rules_created_at_id ON category_pricing_rules (created_at DESC, id DESC);
