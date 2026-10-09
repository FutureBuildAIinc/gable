-- SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
-- SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

-- Rollback for 100_inventory_purchasing_wire_contract.sql.
--
-- Lives in migrations/down/ so cmd/migrate's `migrations/*.sql` glob cannot
-- pick it up and apply it as a forward migration. Apply by hand. The down
-- refuses, naming the first row it cannot map back, wherever data written
-- in the new shape has no place in the old one (a down never discards a
-- dealer's data): a reorder recommendation the desk acted on or dismissed,
-- a per branch stock level target that diverges from the product's own, a
-- purchase line quantity or unit cost too fine for the old column widths.
--
--   SET LOCAL my.check = NULL; -- psql has no early return; read the notices
--
-- The drops lose the purchase orders' document numbers, revisions and
-- currency stamps, the lines' conversion shape and line totals, and the
-- stock level and reorder tables; the documents themselves are untouched.
-- created_at stays NOT NULL on both tables (the fill is not reversible and
-- nothing relied on NULLs).

DO $$
DECLARE
    n INTEGER;
    first TEXT;
BEGIN
    -- A recommendation the desk ordered or dismissed has no old place.
    SELECT COUNT(*), MIN(id::text) INTO n, first
    FROM reorder_recommendations WHERE status <> 'OPEN';
    IF n > 0 THEN
        RAISE EXCEPTION 'reorder_recommendations holds % rows the desk acted on (first %); the old shape has no place for them', n, first;
    END IF;

    -- A per branch target that diverges from the product's own columns has
    -- no old place: the old shape keeps one pair per product.
    SELECT COUNT(*), MIN(product_id::text) INTO n, first
    FROM stock_levels s JOIN products p ON p.id = s.product_id
    WHERE s.reorder_point IS DISTINCT FROM p.reorder_point
       OR s.reorder_quantity IS DISTINCT FROM p.reorder_qty;
    IF n > 0 THEN
        RAISE EXCEPTION 'stock_levels holds % per branch targets that diverge from the product''s own (first product %); the old shape has no place for them', n, first;
    END IF;

    -- The widened columns: a value too fine or too large for the old widths.
    SELECT COUNT(*), MIN(id::text) INTO n, first FROM purchase_order_lines
    WHERE unit_cost <> ROUND(unit_cost, 2)
       OR abs(quantity) >= 1000000 OR abs(COALESCE(qty_received, 0)) >= 1000000;
    IF n > 0 THEN
        RAISE EXCEPTION 'purchase_order_lines holds % rows the old widths cannot carry (first %)', n, first;
    END IF;
END
$$;

DROP INDEX IF EXISTS idx_reorder_recommendations_status;
ALTER TABLE reorder_runs DROP COLUMN IF EXISTS branch_id;
DROP TABLE IF EXISTS reorder_recommendations;
DROP TABLE IF EXISTS stock_level_dirty;
DROP TABLE IF EXISTS stock_levels;

DROP INDEX IF EXISTS idx_vendors_created_id;
ALTER TABLE vendors DROP COLUMN IF EXISTS revision;

DROP INDEX IF EXISTS idx_purchase_order_lines_po_position;
ALTER TABLE purchase_order_lines DROP COLUMN IF EXISTS line_total;
ALTER TABLE purchase_order_lines ALTER COLUMN unit_cost TYPE DECIMAL(10, 2);
ALTER TABLE purchase_order_lines RENAME COLUMN unit_cost TO cost;
ALTER TABLE purchase_order_lines ALTER COLUMN qty_received TYPE DECIMAL(10, 4);
ALTER TABLE purchase_order_lines ALTER COLUMN quantity TYPE DECIMAL(10, 4);
ALTER TABLE purchase_order_lines DROP CONSTRAINT IF EXISTS purchase_order_lines_conversion_positive;
ALTER TABLE purchase_order_lines DROP COLUMN IF EXISTS stock_quantity;
ALTER TABLE purchase_order_lines DROP COLUMN IF EXISTS stock_uom;
ALTER TABLE purchase_order_lines DROP COLUMN IF EXISTS price_uom_qty;
ALTER TABLE purchase_order_lines DROP COLUMN IF EXISTS uom_qty;
ALTER TABLE purchase_order_lines DROP COLUMN IF EXISTS price_uom;
ALTER TABLE purchase_order_lines DROP COLUMN IF EXISTS uom;
ALTER TABLE purchase_order_lines DROP COLUMN IF EXISTS position;

DROP INDEX IF EXISTS idx_purchase_orders_created_id;
ALTER TABLE purchase_orders DROP CONSTRAINT IF EXISTS purchase_orders_status_check;
ALTER TABLE purchase_orders ADD CONSTRAINT purchase_orders_status_check
    CHECK (status IN ('DRAFT', 'SENT', 'PARTIAL', 'RECEIVED', 'CANCELLED'));
ALTER TABLE purchase_orders DROP COLUMN IF EXISTS sent_at;
ALTER TABLE purchase_orders DROP COLUMN IF EXISTS currency;
ALTER TABLE purchase_orders DROP CONSTRAINT IF EXISTS purchase_orders_number_key;
ALTER TABLE purchase_orders DROP COLUMN IF EXISTS number;
DROP FUNCTION IF EXISTS purchase_order_next_number();
DROP SEQUENCE IF EXISTS purchase_order_number_seq;
ALTER TABLE purchase_orders DROP COLUMN IF EXISTS revision;
