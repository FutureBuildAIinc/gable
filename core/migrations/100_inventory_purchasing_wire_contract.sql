-- SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
-- SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

-- 100: inventory, purchase orders, vendors and EDI partners onto the wire
-- contract (item C4-1a, docs/adr/0008-inventory-identity-and-vendor-intake.md
-- section 12; the number planned as 097 there was taken, so this takes the
-- next free). The migration note of ADR 0001 for the purchasing modules:
--
--   purchase_orders  created_at filled and NOT NULL first (the list's keyset
--                    ordering and the number backfill read it), then
--                    revision, the PO- document number (sequence, backfill
--                    in (created_at, id) order, DEFAULT for raw SQL writers,
--                    UNIQUE), currency backfilled from the deployment's
--                    default, sent_at, and the status CHECK restated on
--                    today's five values.
--   purchase_order_lines  position by (created_at, id) within the purchase
--                    order, quantity and qty_received widened to the wire's
--                    NUMERIC(12,4), cost renamed unit_cost and widened to
--                    NUMERIC(12,4) per price_uom, the section 1 line shape
--                    (uom, price_uom, uom_qty, price_uom_qty, stock_uom,
--                    stock_quantity) backfilled to the product's stocking
--                    unit with the pair 1 and 1 (the units(code) foreign
--                    keys arrive with the first cycle 4 migration that lands
--                    after the unit catalogue), and line_total by ADR 0001
--                    section 7a's one rounding.
--   vendors          created_at filled and NOT NULL, revision.
--   stock_levels     per product and branch, backfilled from the products
--                    row for every branch that holds the product; the
--                    stock_level_dirty queue the inventory service feeds;
--                    reorder_recommendations of ADR 0008 section 10.3; the
--                    reorder run gains its branch.
--
-- inventory.created_at the record also names is already in place: C3-1b's
-- migration 098 added and backfilled it.

-- 1. purchase_orders.

-- 1a. created_at: fill, then NOT NULL.
UPDATE purchase_orders SET created_at = COALESCE(updated_at, NOW()) WHERE created_at IS NULL;
ALTER TABLE purchase_orders ALTER COLUMN created_at SET NOT NULL;

-- 1b. revision.
ALTER TABLE purchase_orders ADD COLUMN IF NOT EXISTS revision BIGINT NOT NULL DEFAULT 1;

-- 1c. The PO- document number.
CREATE SEQUENCE IF NOT EXISTS purchase_order_number_seq;

CREATE OR REPLACE FUNCTION purchase_order_next_number() RETURNS TEXT
LANGUAGE sql AS $$
    SELECT 'PO-' || lpad(s.n::text, GREATEST(6, length(s.n::text)), '0')
    FROM (SELECT nextval('purchase_order_number_seq') AS n) s
$$;

ALTER TABLE purchase_orders ADD COLUMN IF NOT EXISTS number TEXT;

WITH ordered AS (
    SELECT id, row_number() OVER (ORDER BY created_at, id) AS n
    FROM purchase_orders
    WHERE number IS NULL
)
UPDATE purchase_orders po
SET number = 'PO-' || lpad(o.n::text, GREATEST(6, length(o.n::text)), '0')
FROM ordered o
WHERE po.id = o.id;

SELECT setval('purchase_order_number_seq',
              COALESCE((SELECT MAX(substring(number FROM 4)::bigint) FROM purchase_orders), 0) + 1,
              false);

ALTER TABLE purchase_orders ALTER COLUMN number SET DEFAULT purchase_order_next_number();
ALTER TABLE purchase_orders ALTER COLUMN number SET NOT NULL;
ALTER TABLE purchase_orders ADD CONSTRAINT purchase_orders_number_key UNIQUE (number);

-- 1d. currency and sent_at. The currency backfill reads the deployment's
--     currency.default setting, falling back to USD, the single currency of
--     cycle 1 (ADR 0001 section 7).
ALTER TABLE purchase_orders ADD COLUMN IF NOT EXISTS currency CHAR(3);
UPDATE purchase_orders SET currency = COALESCE(
    (SELECT value FROM system_settings WHERE key = 'currency.default'), 'USD')
WHERE currency IS NULL;
ALTER TABLE purchase_orders ALTER COLUMN currency SET NOT NULL;
ALTER TABLE purchase_orders ALTER COLUMN currency SET DEFAULT 'USD';
ALTER TABLE purchase_orders ADD COLUMN IF NOT EXISTS sent_at TIMESTAMPTZ;

-- 1e. The status CHECK restated on today's values (ADR 0008 section 12
--     step 1; the approval values arrive with C4-2 D's migration).
ALTER TABLE purchase_orders DROP CONSTRAINT IF EXISTS purchase_orders_status_check;
ALTER TABLE purchase_orders ADD CONSTRAINT purchase_orders_status_check
    CHECK (status IN ('DRAFT', 'SENT', 'PARTIAL', 'RECEIVED', 'CANCELLED'));

-- 1f. The list's keyset ordering.
CREATE INDEX IF NOT EXISTS idx_purchase_orders_created_id
    ON purchase_orders (created_at DESC, id DESC);

-- 2. purchase_order_lines.

-- 2a. position by (created_at, id) within the purchase order. The DEFAULT 0
--     is for raw SQL writers elsewhere (the demo seed), the pattern of
--     quotes' migration 090; the Go create path numbers its lines.
ALTER TABLE purchase_order_lines ADD COLUMN IF NOT EXISTS position INTEGER NOT NULL DEFAULT 0;
WITH ordered AS (
    SELECT id, row_number() OVER (PARTITION BY po_id ORDER BY created_at, id) AS n
    FROM purchase_order_lines
)
UPDATE purchase_order_lines l
SET position = o.n
FROM ordered o
WHERE l.id = o.id;

-- 2b. The section 1 line shape, backfilled to the product's stocking unit
--     with the pair 1 and 1 (stock_quantity = quantity), then the widenings
--     and the rename.
ALTER TABLE purchase_order_lines ADD COLUMN IF NOT EXISTS uom TEXT;
ALTER TABLE purchase_order_lines ADD COLUMN IF NOT EXISTS price_uom TEXT;
ALTER TABLE purchase_order_lines ADD COLUMN IF NOT EXISTS uom_qty NUMERIC(12, 4) NOT NULL DEFAULT 1;
ALTER TABLE purchase_order_lines ADD COLUMN IF NOT EXISTS price_uom_qty NUMERIC(12, 4) NOT NULL DEFAULT 1;
ALTER TABLE purchase_order_lines ADD COLUMN IF NOT EXISTS stock_uom TEXT;
ALTER TABLE purchase_order_lines ADD COLUMN IF NOT EXISTS stock_quantity NUMERIC(12, 4);

UPDATE purchase_order_lines l
SET uom = p.uom_primary::text,
    price_uom = COALESCE(l.price_uom, p.uom_primary::text),
    stock_uom = p.uom_primary::text
FROM products p
WHERE l.product_id = p.id
  AND (l.uom IS NULL OR l.stock_uom IS NULL OR l.price_uom IS NULL);

UPDATE purchase_order_lines SET stock_quantity = quantity WHERE stock_quantity IS NULL;

-- The pair is 1 and 1 in the stocking unit hold (ADR 0008 section 1); a row
-- a dealer's raw SQL wrote with another pair keeps it, and the service
-- refuses what it cannot carry.
ALTER TABLE purchase_order_lines
    ADD CONSTRAINT purchase_order_lines_conversion_positive CHECK (uom_qty > 0 AND price_uom_qty > 0);

-- 2c. Widen quantity and qty_received; rename cost to unit_cost and widen.
ALTER TABLE purchase_order_lines ALTER COLUMN quantity TYPE NUMERIC(12, 4);
ALTER TABLE purchase_order_lines ALTER COLUMN qty_received TYPE NUMERIC(12, 4);
ALTER TABLE purchase_order_lines RENAME COLUMN cost TO unit_cost;
ALTER TABLE purchase_order_lines ALTER COLUMN unit_cost TYPE NUMERIC(12, 4);

-- 2d. line_total, the line's own extension in cents scale (ADR 0001 7a:
--     quantity x unit_cost x price_uom_qty / uom_qty, rounded once).
ALTER TABLE purchase_order_lines ADD COLUMN IF NOT EXISTS line_total NUMERIC(12, 2);
UPDATE purchase_order_lines
SET line_total = ROUND(quantity * unit_cost * price_uom_qty / uom_qty, 2)
WHERE line_total IS NULL;
ALTER TABLE purchase_order_lines ALTER COLUMN line_total SET NOT NULL;

CREATE INDEX IF NOT EXISTS idx_purchase_order_lines_po_position
    ON purchase_order_lines (po_id, position);

-- 3. vendors: created_at filled and NOT NULL, revision, the keyset index.
UPDATE vendors SET created_at = COALESCE(updated_at, NOW()) WHERE created_at IS NULL;
ALTER TABLE vendors ALTER COLUMN created_at SET NOT NULL;
ALTER TABLE vendors ADD COLUMN IF NOT EXISTS revision BIGINT NOT NULL DEFAULT 1;
CREATE INDEX IF NOT EXISTS idx_vendors_created_id ON vendors (created_at DESC, id DESC);

-- 4. stock_levels, the stock level dirty queue, the reorder tables
--    (ADR 0008 sections 10.2 and 10.3).
CREATE TABLE IF NOT EXISTS stock_levels (
    product_id       UUID NOT NULL REFERENCES products(id),
    branch_id        UUID NOT NULL REFERENCES locations(id),
    reorder_point    NUMERIC(12, 4),
    reorder_quantity NUMERIC(12, 4),
    low_since        TIMESTAMPTZ,
    revision         BIGINT NOT NULL DEFAULT 1,
    updated_at       TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    PRIMARY KEY (product_id, branch_id)
);

-- Backfill: every branch that holds the product takes the product's own
-- targets, the per branch targets the refresh job writes from now on.
INSERT INTO stock_levels (product_id, branch_id, reorder_point, reorder_quantity)
SELECT DISTINCT i.product_id, l.branch_id, p.reorder_point, p.reorder_qty
FROM inventory i
JOIN locations l ON l.id = i.location_id
JOIN products p ON p.id = i.product_id
WHERE l.branch_id IS NOT NULL
ON CONFLICT (product_id, branch_id) DO NOTHING;

CREATE TABLE IF NOT EXISTS stock_level_dirty (
    product_id UUID NOT NULL,
    branch_id  UUID NOT NULL,
    PRIMARY KEY (product_id, branch_id)
);

CREATE TABLE IF NOT EXISTS reorder_recommendations (
    id                    UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    run_id                UUID NOT NULL REFERENCES reorder_runs(id),
    product_id            UUID NOT NULL REFERENCES products(id),
    branch_id             UUID NOT NULL REFERENCES locations(id),
    vendor_id             UUID REFERENCES vendors(id),
    on_hand               NUMERIC(12, 4) NOT NULL,
    allocated             NUMERIC(12, 4) NOT NULL,
    available             NUMERIC(12, 4) NOT NULL,
    on_order              NUMERIC(12, 4) NOT NULL,
    backordered           NUMERIC(12, 4) NOT NULL,
    velocity              NUMERIC(12, 4) NOT NULL,
    lookback_days         INTEGER NOT NULL,
    lead_time_days        NUMERIC(12, 4) NOT NULL,
    lead_time_source      TEXT NOT NULL CHECK (lead_time_source IN ('vendor_item', 'measured', 'default')),
    reorder_point         NUMERIC(12, 4),
    reorder_quantity      NUMERIC(12, 4),
    suggested_quantity    NUMERIC(12, 4) NOT NULL,
    vendor_item_id        UUID,
    unit                  TEXT,
    status                TEXT NOT NULL DEFAULT 'OPEN' CHECK (status IN ('OPEN', 'ORDERED', 'DISMISSED')),
    purchase_order_line_id UUID,
    created_at            TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS idx_reorder_recommendations_status
    ON reorder_recommendations (status, branch_id);

ALTER TABLE reorder_runs ADD COLUMN IF NOT EXISTS branch_id UUID REFERENCES locations(id);
