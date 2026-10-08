-- SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
-- SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

-- Rollback for 093_catalog_pricing_wire_contract.sql.
--
-- Lives in migrations/down/ so cmd/migrate's `migrations/*.sql` glob cannot
-- pick it up and apply it as a forward migration. Apply by hand.
--
-- What does not come back: every revision column and every keyset index go,
-- and created_at stays NOT NULL (a column the up migration filled is never
-- un-filled). The two quantity columns narrow back to DECIMAL(10,4) only
-- when no row holds a value DECIMAL(10,4) cannot; a row that does is named
-- and stops the rollback, because narrowing it would silently round stock
-- and reorder points, which the wire contract refuses everywhere else too.

-- 3. Keyset indexes.
DROP INDEX IF EXISTS idx_category_pricing_rules_created_at_id;
DROP INDEX IF EXISTS idx_pricing_rules_created_at_id;
DROP INDEX IF EXISTS idx_locations_created_at_id;
DROP INDEX IF EXISTS idx_products_created_at_id;

-- 2. Narrowed back, refusing a value the narrower column cannot hold.
DO $$
DECLARE
    offender RECORD;
BEGIN
    SELECT p.sku AS sku, p.reorder_point::text AS reorder_point
      INTO offender
      FROM products p
     WHERE p.reorder_point > 999999.9999 OR p.reorder_point < -999999.9999
       OR p.reorder_qty > 999999.9999 OR p.reorder_qty < -999999.9999
     LIMIT 1;
    IF FOUND THEN
        RAISE EXCEPTION 'product % holds reorder quantities beyond DECIMAL(10,4) (%)', offender.sku, offender.reorder_point;
    END IF;
    SELECT i.id::text AS id, i.allocated::text AS allocated
      INTO offender
      FROM inventory i
     WHERE i.allocated > 999999.9999 OR i.allocated < -999999.9999
     LIMIT 1;
    IF FOUND THEN
        RAISE EXCEPTION 'inventory row % holds an allocation beyond DECIMAL(10,4) (%)', offender.id, offender.allocated;
    END IF;
END $$;

DROP VIEW IF EXISTS v_inventory_with_branch;
ALTER TABLE products
    ALTER COLUMN reorder_point TYPE DECIMAL(10,4),
    ALTER COLUMN reorder_qty TYPE DECIMAL(10,4);
ALTER TABLE inventory
    ALTER COLUMN allocated TYPE DECIMAL(10,4);
CREATE VIEW v_inventory_with_branch AS
SELECT i.*, l.branch_id AS branch_id
  FROM inventory i
  JOIN locations l ON l.id = i.location_id;

-- 1. Revisions.
ALTER TABLE customer_contracts DROP COLUMN IF EXISTS revision;
ALTER TABLE category_pricing_rules DROP COLUMN IF EXISTS revision;
ALTER TABLE pricing_rules DROP COLUMN IF EXISTS revision;
ALTER TABLE locations DROP COLUMN IF EXISTS revision;
ALTER TABLE products DROP COLUMN IF EXISTS revision;
