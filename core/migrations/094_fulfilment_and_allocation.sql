-- SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
-- SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

-- 094: the C2-2b half of the orders item (docs/adr/0005-sales-and-money-core.md
-- sections 5.4 to 5.6, 6.1 and 13 step 8): everything allocation, back
-- orders, fulfilment and its invoice need beyond 092.
--
--   1  order_allocation_requests (5.4) and order_fulfillment_requests (5.5):
--      the two worker queues.
--   2  invoices: the C2-2 rows of 6.1 (currency, delivery type, picked up
--      by, delivery, ship-to, job, tax columns widened, the invoice entry,
--      the business date, the origin).
--   3  invoice_lines: the shared line shape of 2.2 plus the order line it
--      bills, its unit cost and cost. price_each is kept, now holding the
--      effective price per sale unit at scale 4, for the readers C2-3
--      converts (the portal's invoice view, the print).
--   4  history: an order that delivery completion invoiced but never
--      fulfilled is migrated as fulfilled (13 step 8), its allocation
--      consumed in inventory; its invoice lines are linked to its order
--      lines.
--
-- Every step is idempotent. The one time backfills run only on the apply that
-- adds their columns, so a second apply never overwrites what the application
-- has written since.

-- Preflight: the new line shape needs a billed quantity above zero and a
-- price of zero or more. Named rows, the whole migration rolled back.
DO $$
DECLARE bad TEXT;
BEGIN
    SELECT string_agg(b.id::text || ' (quantity ' || b.quantity::text || ', price ' || b.price_each::text || ')', '; ' ORDER BY b.id)
    INTO bad
    FROM (SELECT id, quantity, price_each FROM invoice_lines
          WHERE (quantity <= 0 OR price_each < 0)
            AND NOT EXISTS (SELECT 1 FROM information_schema.columns
                            WHERE table_schema = current_schema() AND table_name = 'invoice_lines' AND column_name = 'line_type')
          ORDER BY id LIMIT 20) b;
    IF bad IS NOT NULL THEN
        RAISE EXCEPTION '094: invoice_lines rows that the new line shape cannot hold (a quantity of zero or below, or a negative price): %. Correct or delete them, then migrate again.', bad;
    END IF;
END $$;

DROP TABLE IF EXISTS _m094_first_apply;
CREATE TEMP TABLE _m094_first_apply AS
SELECT NOT EXISTS (SELECT 1 FROM information_schema.columns
                   WHERE table_schema = current_schema() AND table_name = 'invoices'
                     AND column_name = 'origin') AS first_apply;

-- 1. The queues.
CREATE TABLE IF NOT EXISTS order_allocation_requests (
    order_id UUID PRIMARY KEY REFERENCES orders(id) ON DELETE CASCADE,
    position BIGSERIAL NOT NULL UNIQUE,
    requested_at TIMESTAMPTZ NOT NULL DEFAULT clock_timestamp()
);

CREATE TABLE IF NOT EXISTS order_fulfillment_requests (
    delivery_id UUID PRIMARY KEY REFERENCES deliveries(id) ON DELETE CASCADE,
    order_id UUID NOT NULL REFERENCES orders(id) ON DELETE CASCADE,
    position BIGSERIAL NOT NULL UNIQUE,
    attempts INTEGER NOT NULL DEFAULT 0,
    last_error TEXT,
    parked_at TIMESTAMPTZ,
    created_at TIMESTAMPTZ NOT NULL DEFAULT clock_timestamp()
);
CREATE INDEX IF NOT EXISTS idx_order_fulfillment_requests_order ON order_fulfillment_requests (order_id);
CREATE INDEX IF NOT EXISTS idx_order_lines_backordered ON order_lines (product_id) WHERE quantity_backordered > 0;

-- 2. invoices: the C2-2 rows of ADR 0005 6.1.
ALTER TABLE invoices ADD COLUMN IF NOT EXISTS currency CHAR(3);
UPDATE invoices i
SET currency = COALESCE((SELECT o.currency FROM orders o WHERE o.id = i.order_id),
                        (SELECT c.currency FROM customers c WHERE c.id = i.customer_id),
                        (SELECT value FROM system_settings WHERE key = 'currency.default'), 'USD')
WHERE i.currency IS NULL;
UPDATE invoices SET currency = COALESCE((SELECT value FROM system_settings WHERE key = 'currency.default'), 'USD')
WHERE currency IS NULL;
DO $$
DECLARE default_code TEXT;
BEGIN
    SELECT value INTO default_code FROM system_settings WHERE key = 'currency.default';
    IF default_code IS NULL THEN
        default_code := 'USD';
    END IF;
    -- raw writers (the counter, the seed) keep inserting until C2-3 and C2-5
    -- name the currency
    EXECUTE format('ALTER TABLE invoices ALTER COLUMN currency SET DEFAULT %L', default_code);
END $$;
ALTER TABLE invoices ALTER COLUMN currency SET NOT NULL;
DO $$ BEGIN
    ALTER TABLE invoices ADD CONSTRAINT invoices_currency_format CHECK (currency ~ '^[A-Z]{3}$');
EXCEPTION WHEN duplicate_object THEN NULL;
END $$;

ALTER TABLE invoices ADD COLUMN IF NOT EXISTS delivery_type TEXT;
UPDATE invoices i
SET delivery_type = COALESCE((SELECT o.delivery_type FROM orders o WHERE o.id = i.order_id), 'PICKUP')
WHERE i.delivery_type IS NULL;
ALTER TABLE invoices ALTER COLUMN delivery_type SET DEFAULT 'PICKUP';
ALTER TABLE invoices ALTER COLUMN delivery_type SET NOT NULL;
DO $$ BEGIN
    ALTER TABLE invoices ADD CONSTRAINT invoices_delivery_type_check CHECK (delivery_type IN ('DELIVERY', 'PICKUP'));
EXCEPTION WHEN duplicate_object THEN NULL;
END $$;

ALTER TABLE invoices ADD COLUMN IF NOT EXISTS picked_up_by TEXT;
ALTER TABLE invoices ADD COLUMN IF NOT EXISTS delivery_id UUID REFERENCES deliveries(id) ON DELETE SET NULL;
ALTER TABLE invoices ADD COLUMN IF NOT EXISTS ship_to_id UUID REFERENCES customer_ship_tos(id);
ALTER TABLE invoices ADD COLUMN IF NOT EXISTS ship_to_snapshot JSONB;
ALTER TABLE invoices ADD COLUMN IF NOT EXISTS project_id UUID REFERENCES projects(id);
UPDATE invoices i SET ship_to_id = o.ship_to_id, ship_to_snapshot = o.ship_to_snapshot, project_id = o.project_id
FROM orders o
WHERE o.id = i.order_id AND i.ship_to_id IS NULL AND i.project_id IS NULL AND i.ship_to_snapshot IS NULL
  AND (SELECT first_apply FROM _m094_first_apply);

-- tax: the rate widens (0.08875 does not fit NUMERIC(5,4)) and is null when a
-- provider priced the invoice; the amounts widen with the order's.
ALTER TABLE invoices ALTER COLUMN tax_rate TYPE NUMERIC(9, 6);
ALTER TABLE invoices ALTER COLUMN tax_rate DROP DEFAULT;
ALTER TABLE invoices ALTER COLUMN subtotal TYPE NUMERIC(12, 2);
ALTER TABLE invoices ALTER COLUMN tax_amount TYPE NUMERIC(12, 2);
ALTER TABLE invoices ALTER COLUMN total_amount TYPE NUMERIC(12, 2);
ALTER TABLE invoices ADD COLUMN IF NOT EXISTS tax_exempt BOOLEAN NOT NULL DEFAULT FALSE;
-- history is LEGACY: no rule is read from a stored zero. A raw writer's
-- DEFAULT stays LEGACY until the counter and invoice modules (C2-5, C2-3)
-- name the source themselves.
ALTER TABLE invoices ADD COLUMN IF NOT EXISTS tax_source TEXT NOT NULL DEFAULT 'LEGACY';
DO $$ BEGIN
    ALTER TABLE invoices ADD CONSTRAINT invoices_tax_source_check
        CHECK (tax_source IN ('EXEMPT', 'PROVIDER', 'SHIP_TO_RATE', 'BRANCH_RATE', 'LEGACY'));
EXCEPTION WHEN duplicate_object THEN NULL;
END $$;

ALTER TABLE invoices ADD COLUMN IF NOT EXISTS gl_entry_id UUID REFERENCES gl_journal_entries(id);

ALTER TABLE invoices ADD COLUMN IF NOT EXISTS invoice_date DATE;
UPDATE invoices i
SET invoice_date = (COALESCE(i.created_at, NOW()) AT TIME ZONE
                    COALESCE((SELECT l.timezone FROM locations l WHERE l.id = i.branch_id), 'UTC'))::date
WHERE i.invoice_date IS NULL;
ALTER TABLE invoices ALTER COLUMN invoice_date SET DEFAULT CURRENT_DATE;
ALTER TABLE invoices ALTER COLUMN invoice_date SET NOT NULL;

ALTER TABLE invoices ADD COLUMN IF NOT EXISTS origin TEXT NOT NULL DEFAULT 'ORDER';
DO $$ BEGIN
    ALTER TABLE invoices ADD CONSTRAINT invoices_origin_check CHECK (origin IN ('ORDER', 'POS'));
EXCEPTION WHEN duplicate_object THEN NULL;
END $$;
-- counter account charges are the only invoices without an order today
UPDATE invoices SET origin = 'POS' WHERE order_id IS NULL AND (SELECT first_apply FROM _m094_first_apply);

CREATE INDEX IF NOT EXISTS idx_invoices_delivery ON invoices (delivery_id) WHERE delivery_id IS NOT NULL;

-- 3. invoice_lines: the shared line shape (ADR 0005 2.2).
ALTER TABLE invoice_lines ADD COLUMN IF NOT EXISTS line_type TEXT NOT NULL DEFAULT 'PRODUCT';
ALTER TABLE invoice_lines ADD COLUMN IF NOT EXISTS position INTEGER NOT NULL DEFAULT 0;
ALTER TABLE invoice_lines ADD COLUMN IF NOT EXISTS parent_line_id UUID REFERENCES invoice_lines(id) ON DELETE CASCADE;
ALTER TABLE invoice_lines ADD COLUMN IF NOT EXISTS charge_code_id UUID REFERENCES charge_codes(id);
ALTER TABLE invoice_lines ADD COLUMN IF NOT EXISTS sku TEXT;
ALTER TABLE invoice_lines ADD COLUMN IF NOT EXISTS description TEXT;
ALTER TABLE invoice_lines ADD COLUMN IF NOT EXISTS uom TEXT;
ALTER TABLE invoice_lines ADD COLUMN IF NOT EXISTS price_uom TEXT;
ALTER TABLE invoice_lines ADD COLUMN IF NOT EXISTS uom_qty NUMERIC(12, 4);
ALTER TABLE invoice_lines ADD COLUMN IF NOT EXISTS price_uom_qty NUMERIC(12, 4);
ALTER TABLE invoice_lines ADD COLUMN IF NOT EXISTS unit_price NUMERIC(12, 4);
ALTER TABLE invoice_lines ADD COLUMN IF NOT EXISTS price_source TEXT NOT NULL DEFAULT 'PRICE_LIST';
ALTER TABLE invoice_lines ADD COLUMN IF NOT EXISTS discount_percent NUMERIC(7, 4);
ALTER TABLE invoice_lines ADD COLUMN IF NOT EXISTS discount_amount NUMERIC(12, 2);
ALTER TABLE invoice_lines ADD COLUMN IF NOT EXISTS discount_reason TEXT;
ALTER TABLE invoice_lines ADD COLUMN IF NOT EXISTS line_total NUMERIC(12, 2);
ALTER TABLE invoice_lines ADD COLUMN IF NOT EXISTS taxable BOOLEAN NOT NULL DEFAULT TRUE;
ALTER TABLE invoice_lines ADD COLUMN IF NOT EXISTS revenue_account_code TEXT;
ALTER TABLE invoice_lines ADD COLUMN IF NOT EXISTS order_line_id UUID REFERENCES order_lines(id) ON DELETE SET NULL;
-- cost: historic invoices posted no COGS and this does not invent it (cost 0,
-- unit_cost null).
ALTER TABLE invoice_lines ADD COLUMN IF NOT EXISTS unit_cost NUMERIC(12, 4);
ALTER TABLE invoice_lines ADD COLUMN IF NOT EXISTS cost NUMERIC(12, 2) NOT NULL DEFAULT 0;

ALTER TABLE invoice_lines ALTER COLUMN quantity TYPE NUMERIC(12, 4);
ALTER TABLE invoice_lines ALTER COLUMN price_each TYPE NUMERIC(12, 4);

UPDATE invoice_lines l
SET sku = COALESCE(l.sku, p.sku),
    description = COALESCE(l.description, p.description, p.sku, ''),
    uom = COALESCE(l.uom, p.uom_primary::text),
    price_uom = COALESCE(l.price_uom, p.uom_primary::text)
FROM products p
WHERE l.product_id = p.id
  AND (l.sku IS NULL OR l.description IS NULL OR l.uom IS NULL OR l.price_uom IS NULL);
UPDATE invoice_lines SET description = '' WHERE description IS NULL;
ALTER TABLE invoice_lines ALTER COLUMN description SET NOT NULL;
UPDATE invoice_lines SET uom_qty = 1 WHERE uom_qty IS NULL;
UPDATE invoice_lines SET price_uom_qty = 1 WHERE price_uom_qty IS NULL;
UPDATE invoice_lines SET unit_price = price_each WHERE unit_price IS NULL;
UPDATE invoice_lines SET line_total = ROUND(quantity * price_each, 2) WHERE line_total IS NULL;

WITH ordered AS (
    SELECT id, row_number() OVER (PARTITION BY invoice_id ORDER BY created_at, id) - 1 AS n
    FROM invoice_lines
)
UPDATE invoice_lines l SET position = o.n FROM ordered o
WHERE l.id = o.id AND (SELECT first_apply FROM _m094_first_apply);

-- Each invoice line is linked to the order line it bills: by (order, product),
-- the earliest order line first when one product appears twice.
WITH ranked_il AS (
    SELECT il.id, i.order_id, il.product_id,
           row_number() OVER (PARTITION BY i.order_id, il.product_id ORDER BY il.created_at, il.id) AS rn
    FROM invoice_lines il JOIN invoices i ON i.id = il.invoice_id
    WHERE i.order_id IS NOT NULL AND il.product_id IS NOT NULL AND il.order_line_id IS NULL
),
ranked_ol AS (
    SELECT ol.id, ol.order_id, ol.product_id,
           row_number() OVER (PARTITION BY ol.order_id, ol.product_id ORDER BY ol.position, ol.created_at, ol.id) AS rn
    FROM order_lines ol WHERE ol.product_id IS NOT NULL
)
UPDATE invoice_lines il SET order_line_id = ol.id
FROM ranked_il r
JOIN ranked_ol ol ON ol.order_id = r.order_id AND ol.product_id = r.product_id AND ol.rn = LEAST(r.rn, (
        SELECT count(*) FROM order_lines x WHERE x.order_id = r.order_id AND x.product_id = r.product_id))
WHERE il.id = r.id AND (SELECT first_apply FROM _m094_first_apply);

ALTER TABLE invoice_lines ALTER COLUMN product_id DROP NOT NULL;

DO $$ BEGIN
    ALTER TABLE invoice_lines ADD CONSTRAINT invoice_lines_line_type_check
        CHECK (line_type IN ('PRODUCT', 'KIT', 'COMPONENT', 'CHARGE', 'TEXT'));
EXCEPTION WHEN duplicate_object THEN NULL;
END $$;
DO $$ BEGIN
    ALTER TABLE invoice_lines ADD CONSTRAINT invoice_lines_price_source_check
        CHECK (price_source IN ('PRICE_LIST', 'QUOTE', 'OVERRIDE', 'MANUAL', 'NONE'));
EXCEPTION WHEN duplicate_object THEN NULL;
END $$;
DO $$ BEGIN
    ALTER TABLE invoice_lines ADD CONSTRAINT invoice_lines_pair_positive
        CHECK ((uom_qty IS NULL AND price_uom_qty IS NULL) OR (uom_qty > 0 AND price_uom_qty > 0));
EXCEPTION WHEN duplicate_object THEN NULL;
END $$;
-- The per type shape. A billed quantity is positive (credit memo lines are the
-- negative ones, in their own table); a component prices at zero; a text
-- line carries no amount.
DO $$ BEGIN
    ALTER TABLE invoice_lines ADD CONSTRAINT invoice_lines_shape
        CHECK (
            (line_type = 'TEXT'
                AND quantity IS NULL AND uom IS NULL AND price_uom IS NULL
                AND uom_qty IS NULL AND price_uom_qty IS NULL
                AND unit_price IS NULL AND line_total IS NULL
                AND product_id IS NULL AND charge_code_id IS NULL)
            OR (line_type IN ('PRODUCT', 'KIT')
                AND quantity IS NOT NULL AND quantity > 0
                AND uom IS NOT NULL AND price_uom IS NOT NULL
                AND uom_qty IS NOT NULL AND price_uom_qty IS NOT NULL
                AND unit_price IS NOT NULL AND unit_price >= 0
                AND line_total IS NOT NULL
                AND charge_code_id IS NULL)
            OR (line_type = 'COMPONENT'
                AND product_id IS NOT NULL AND parent_line_id IS NOT NULL
                AND quantity IS NOT NULL AND quantity > 0
                AND uom IS NOT NULL AND price_uom IS NOT NULL
                AND uom_qty IS NOT NULL AND price_uom_qty IS NOT NULL
                AND unit_price IS NOT NULL AND unit_price = 0
                AND line_total IS NOT NULL AND line_total = 0
                AND charge_code_id IS NULL AND taxable = FALSE)
            OR (line_type = 'CHARGE'
                AND quantity IS NOT NULL AND quantity > 0
                AND uom IS NOT NULL AND price_uom IS NOT NULL
                AND uom_qty IS NOT NULL AND price_uom_qty IS NOT NULL
                AND unit_price IS NOT NULL AND unit_price >= 0
                AND line_total IS NOT NULL
                AND charge_code_id IS NOT NULL AND product_id IS NULL)
        );
EXCEPTION WHEN duplicate_object THEN NULL;
END $$;
DO $$ BEGIN
    ALTER TABLE invoice_lines ADD CONSTRAINT invoice_lines_parent_only_on_component
        CHECK ((line_type = 'COMPONENT') = (parent_line_id IS NOT NULL));
EXCEPTION WHEN duplicate_object THEN NULL;
END $$;
CREATE INDEX IF NOT EXISTS idx_invoice_lines_order_line ON invoice_lines (order_line_id) WHERE order_line_id IS NOT NULL;

-- 4. Orders invoiced but never fulfilled (ADR 0005 13 step 8). Today the
-- delivery completion adapter invoices an order and posts AR without moving
-- stock or status, so a CONFIRMED order can already carry an invoice. Each is
-- migrated as fulfilled, so the next fulfilment cannot bill it again:
-- quantity_fulfilled = quantity, quantity_allocated = 0, status FULFILLED; the
-- allocation it held is consumed in inventory as FulfillOrder would have
-- (allocated and quantity both reduced at the order's branch, the row with the
-- most allocation first), never below zero on either column. No journal entry
-- is written: COGS for those orders was never posted and is not invented.
DO $$
DECLARE
    o RECORD;
    l RECORD;
    inv RECORD;
    remaining NUMERIC;
    take NUMERIC;
    n_orders INTEGER := 0;
    short RECORD;
BEGIN
    IF NOT (SELECT first_apply FROM _m094_first_apply) THEN
        RETURN;
    END IF;
    CREATE TEMP TABLE _m094_shortfall (product_id UUID, shortfall NUMERIC) ON COMMIT DROP;
    FOR o IN
        SELECT ord.id, ord.branch_id FROM orders ord
        WHERE ord.status IN ('CONFIRMED', 'BACKORDERED')
          AND EXISTS (SELECT 1 FROM invoices i WHERE i.order_id = ord.id AND i.status <> 'VOID')
        ORDER BY ord.created_at, ord.id
    LOOP
        n_orders := n_orders + 1;
        FOR l IN
            SELECT id, product_id, quantity FROM order_lines
            WHERE order_id = o.id AND line_type IN ('PRODUCT', 'COMPONENT') AND product_id IS NOT NULL
            ORDER BY product_id, id
        LOOP
            remaining := l.quantity;
            FOR inv IN
                SELECT i.id, i.allocated, i.quantity FROM inventory i
                JOIN locations loc ON loc.id = i.location_id
                WHERE i.product_id = l.product_id AND loc.branch_id = o.branch_id
                ORDER BY i.allocated DESC, i.id
                FOR UPDATE OF i
            LOOP
                EXIT WHEN remaining <= 0;
                take := LEAST(remaining, inv.allocated, inv.quantity);
                IF take > 0 THEN
                    UPDATE inventory SET quantity = quantity - take, allocated = allocated - take, updated_at = NOW()
                    WHERE id = inv.id;
                    remaining := remaining - take;
                END IF;
            END LOOP;
            IF remaining > 0 THEN
                INSERT INTO _m094_shortfall VALUES (l.product_id, remaining);
            END IF;
        END LOOP;
        UPDATE order_lines SET quantity_fulfilled = quantity, quantity_allocated = 0, quantity_backordered = 0
        WHERE order_id = o.id AND quantity IS NOT NULL;
        UPDATE orders SET status = 'FULFILLED', updated_at = NOW(), revision = revision + 1 WHERE id = o.id;
    END LOOP;
    RAISE NOTICE '094: % order(s) invoiced but never fulfilled migrated as fulfilled', n_orders;
    FOR short IN SELECT product_id, SUM(shortfall) AS total FROM _m094_shortfall GROUP BY product_id ORDER BY product_id LOOP
        RAISE NOTICE '094: product % consumed % less than the allocation it held (not forced)', short.product_id, short.total;
    END LOOP;
END $$;

DROP TABLE _m094_first_apply;
