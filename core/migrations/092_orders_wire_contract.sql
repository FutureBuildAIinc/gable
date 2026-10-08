-- SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
-- SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

-- 092: orders onto the wire contract (item C2-2, docs/adr/0005-sales-and-money-core.md
-- sections 2, 3, 5 and 13; docs/adr/0001-wire-contract.md). This file is the
-- C2-2a half of the item's migration: everything the orders conversion, the
-- charge codes, the kit components, the convert lift and the credit hold
-- need. The C2-2b half (the allocation and fulfilment request queues of
-- sections 5.4 and 5.5, and the invoice columns fulfilment writes) lands with
-- its own migration when that half merges.
--
--   1  orders.created_at filled and NOT NULL, revision: the keyset list and
--      the If-Match precondition both need a value on every row.
--   2  order_number_seq, order_next_number() and orders.number (SO-000001):
--      the quote migration's shape, backfilled in (created_at, id) order.
--   3  currency: orders gets the customer's effective currency; journal
--      entries get the dealer default (every insert of C2-2 names it; the
--      DEFAULT stays until then); the journal source CHECK gains CREDIT_MEMO
--      and WRITE_OFF; customer_transactions.type moves from the enum to TEXT
--      with the eight value CHECK of ADR 0005 9.3, because a value added to
--      an enum cannot be used in the transaction that adds it and C2-3
--      already writes CREDIT_MEMO and REVERSAL rows.
--   4  orders.delivery_type, backfilled from the delivery history.
--   5  the order header columns of ADR 0005 5.1: the ship-to, the PO, the
--      contact, the hold columns, the totals and tax estimate (historic rows
--      carry no tax estimate: tax 0, source LEGACY), BACKORDERED in the
--      status CHECK, total_amount widened.
--   6  locations.default_tax_rate widened to NUMERIC(9,6) (0.08875 does not
--      fit NUMERIC(7,4)); products.is_kit and products.taxable;
--      product_kit_components; charge_codes with the seed; account 4030.
--   7  order_lines: the columns of ADR 0005 2.2. price_each becomes
--      unit_price NUMERIC(12,4) (the value keeps its meaning: per sale
--      unit, which is the price unit while the pair is 1 and 1); the line
--      type, position, pair, discount and audit columns arrive backfilled;
--      product_id drops NOT NULL behind the per type CHECKs.
--   8  the keyset index for the list's ordering.
--
-- Every step is idempotent; every backfill reads only columns an earlier step
-- made NOT NULL.

-- 1. created_at and revision.
UPDATE orders SET created_at = COALESCE(updated_at, NOW()) WHERE created_at IS NULL;
UPDATE orders SET updated_at = created_at WHERE updated_at IS NULL;
ALTER TABLE orders ALTER COLUMN created_at SET NOT NULL;
ALTER TABLE orders ADD COLUMN IF NOT EXISTS revision BIGINT NOT NULL DEFAULT 1;

-- 2. Document numbers: SO- from a sequence, the quote migration's shape.
CREATE SEQUENCE IF NOT EXISTS order_number_seq;

CREATE OR REPLACE FUNCTION order_next_number() RETURNS TEXT
LANGUAGE sql AS $$
    SELECT 'SO-' || lpad(s.n::text, GREATEST(6, length(s.n::text)), '0')
    FROM (SELECT nextval('order_number_seq') AS n) s
$$;

ALTER TABLE orders ADD COLUMN IF NOT EXISTS number TEXT;

WITH ordered AS (
    SELECT id, row_number() OVER (ORDER BY created_at, id) AS n
    FROM orders
    WHERE number IS NULL
)
UPDATE orders o
SET number = 'SO-' || lpad(r.n::text, GREATEST(6, length(r.n::text)), '0')
FROM ordered r
WHERE o.id = r.id;

SELECT setval('order_number_seq',
              COALESCE((SELECT MAX(substring(number FROM 4)::bigint) FROM orders), 0) + 1,
              false);

ALTER TABLE orders ALTER COLUMN number SET DEFAULT order_next_number();
ALTER TABLE orders ALTER COLUMN number SET NOT NULL;
ALTER TABLE orders ADD CONSTRAINT orders_number_key UNIQUE (number);

-- 3. Currency and the ledger vocabulary.
ALTER TABLE orders ADD COLUMN IF NOT EXISTS currency CHAR(3);
UPDATE orders o
SET currency = COALESCE(c.currency, s.value)
FROM customers c
CROSS JOIN (SELECT value FROM system_settings WHERE key = 'currency.default') s
WHERE o.customer_id = c.id AND o.currency IS NULL;
UPDATE orders o
SET currency = s.value
FROM (SELECT value FROM system_settings WHERE key = 'currency.default') s
WHERE o.currency IS NULL;
DO $$ BEGIN
    ALTER TABLE orders ADD CONSTRAINT orders_currency_format CHECK (currency ~ '^[A-Z]{3}$');
EXCEPTION WHEN duplicate_object THEN NULL;
END $$;
ALTER TABLE orders ALTER COLUMN currency SET NOT NULL;

DO $$
DECLARE default_code TEXT;
BEGIN
    SELECT value INTO default_code FROM system_settings WHERE key = 'currency.default';
    IF default_code IS NULL THEN
        default_code := 'USD';
    END IF;
    EXECUTE format('ALTER TABLE gl_journal_entries ADD COLUMN IF NOT EXISTS currency CHAR(3) NOT NULL DEFAULT %L', default_code);
END $$;
UPDATE gl_journal_entries SET currency = COALESCE(currency, (SELECT value FROM system_settings WHERE key = 'currency.default'));

ALTER TABLE gl_journal_entries DROP CONSTRAINT IF EXISTS gl_journal_entries_source_check;
ALTER TABLE gl_journal_entries ADD CONSTRAINT gl_journal_entries_source_check
    CHECK (source IN ('MANUAL','INVOICE','PAYMENT','ADJUSTMENT','CLOSING','REVERSAL','VENDOR_INVOICE','VENDOR_PAYMENT','RETURN','DEPOSIT','CREDIT_MEMO','WRITE_OFF'));

-- customer_transactions.type: the enum cannot grow inside the transaction
-- that would use its new value, so the CHECK becomes text (ADR 0005 9.3).
ALTER TABLE customer_transactions RENAME COLUMN type TO type_old;
ALTER TABLE customer_transactions ADD COLUMN IF NOT EXISTS type TEXT;
UPDATE customer_transactions SET type = type_old::text WHERE type IS NULL;
ALTER TABLE customer_transactions ALTER COLUMN type SET NOT NULL;
DO $$ BEGIN
    ALTER TABLE customer_transactions ADD CONSTRAINT customer_transactions_type_check
        CHECK (type IN ('INVOICE','PAYMENT','ADJUSTMENT','REFUND','CREDIT_MEMO','DISCOUNT','WRITE_OFF','REVERSAL'));
EXCEPTION WHEN duplicate_object THEN NULL;
END $$;
ALTER TABLE customer_transactions DROP COLUMN type_old;
DROP TYPE IF EXISTS transaction_type;
ALTER TABLE customer_transactions ADD COLUMN IF NOT EXISTS currency CHAR(3);
UPDATE customer_transactions SET currency = (SELECT value FROM system_settings WHERE key = 'currency.default') WHERE currency IS NULL;
ALTER TABLE customer_transactions ALTER COLUMN currency SET NOT NULL;
ALTER TABLE customer_transactions ADD COLUMN IF NOT EXISTS source_kind TEXT;

-- 4. delivery_type: DELIVERY where a deliveries row or a scheduled date
-- exists, else PICKUP (a will-call history is the absence of a route).
ALTER TABLE orders ADD COLUMN IF NOT EXISTS delivery_type TEXT;
UPDATE orders o
SET delivery_type = CASE WHEN EXISTS (
        SELECT 1 FROM deliveries d WHERE d.order_id = o.id
    ) OR o.scheduled_delivery_date IS NOT NULL THEN 'DELIVERY' ELSE 'PICKUP' END
WHERE o.delivery_type IS NULL;
ALTER TABLE orders ALTER COLUMN delivery_type SET DEFAULT 'DELIVERY';
DO $$ BEGIN
    ALTER TABLE orders ADD CONSTRAINT orders_delivery_type_check
        CHECK (delivery_type IN ('DELIVERY', 'PICKUP'));
EXCEPTION WHEN duplicate_object THEN NULL;
END $$;
ALTER TABLE orders ALTER COLUMN delivery_type SET NOT NULL;

-- 5. The order header columns of ADR 0005 5.1.
ALTER TABLE orders ADD COLUMN IF NOT EXISTS ship_to_id UUID REFERENCES customer_ship_tos(id);
ALTER TABLE orders ADD COLUMN IF NOT EXISTS ship_to_snapshot JSONB;
ALTER TABLE orders ADD COLUMN IF NOT EXISTS customer_po TEXT;
ALTER TABLE orders ADD COLUMN IF NOT EXISTS ordered_by_contact_id UUID REFERENCES customer_contacts(id);
ALTER TABLE orders ADD COLUMN IF NOT EXISTS hold_reason TEXT;
DO $$ BEGIN
    ALTER TABLE orders ADD CONSTRAINT orders_hold_reason_check CHECK (hold_reason IN ('CREDIT_LIMIT', 'MANUAL'));
EXCEPTION WHEN duplicate_object THEN NULL;
END $$;
ALTER TABLE orders ADD COLUMN IF NOT EXISTS hold_note TEXT;
ALTER TABLE orders ADD COLUMN IF NOT EXISTS subtotal NUMERIC(12, 2) NOT NULL DEFAULT 0;
ALTER TABLE orders ADD COLUMN IF NOT EXISTS tax_amount NUMERIC(12, 2) NOT NULL DEFAULT 0;
ALTER TABLE orders ADD COLUMN IF NOT EXISTS tax_rate NUMERIC(9, 6);
ALTER TABLE orders ADD COLUMN IF NOT EXISTS tax_exempt BOOLEAN NOT NULL DEFAULT FALSE;
ALTER TABLE orders ADD COLUMN IF NOT EXISTS tax_source TEXT NOT NULL DEFAULT 'LEGACY';
DO $$ BEGIN
    ALTER TABLE orders ADD CONSTRAINT orders_tax_source_check
        CHECK (tax_source IN ('EXEMPT', 'PROVIDER', 'SHIP_TO_RATE', 'BRANCH_RATE', 'LEGACY'));
EXCEPTION WHEN duplicate_object THEN NULL;
END $$;
ALTER TABLE orders ADD COLUMN IF NOT EXISTS confirmed_at TIMESTAMPTZ;

-- Historic rows carry no tax estimate (nothing computed one): the subtotal
-- is what the total was, tax 0, and the source says LEGACY so no rule is
-- read from a stored zero.
UPDATE orders SET subtotal = total_amount WHERE subtotal = 0 AND tax_amount = 0;
UPDATE orders SET tax_rate = 0 WHERE tax_rate IS NULL;

ALTER TABLE orders ALTER COLUMN total_amount TYPE NUMERIC(12, 2);
ALTER TABLE orders DROP CONSTRAINT IF EXISTS orders_status_check;
ALTER TABLE orders ADD CONSTRAINT orders_status_check
    CHECK (status IN ('DRAFT', 'ON_HOLD', 'CONFIRMED', 'BACKORDERED', 'FULFILLED', 'CANCELLED'));

-- 6. The masters this conversion adds.
ALTER TABLE locations ALTER COLUMN default_tax_rate TYPE NUMERIC(9, 6);
ALTER TABLE products ADD COLUMN IF NOT EXISTS is_kit BOOLEAN NOT NULL DEFAULT FALSE;
ALTER TABLE products ADD COLUMN IF NOT EXISTS taxable BOOLEAN NOT NULL DEFAULT TRUE;

CREATE TABLE IF NOT EXISTS product_kit_components (
    kit_product_id UUID NOT NULL REFERENCES products(id) ON DELETE CASCADE,
    component_product_id UUID NOT NULL REFERENCES products(id) ON DELETE RESTRICT,
    quantity NUMERIC(12, 4) NOT NULL CHECK (quantity > 0),
    position INTEGER NOT NULL,
    PRIMARY KEY (kit_product_id, component_product_id),
    CONSTRAINT product_kit_components_not_self CHECK (kit_product_id <> component_product_id)
);

CREATE TABLE IF NOT EXISTS charge_codes (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    code TEXT UNIQUE NOT NULL,
    name TEXT NOT NULL,
    revenue_account_code TEXT NOT NULL REFERENCES gl_accounts(code),
    taxable BOOLEAN NOT NULL,
    default_unit_price NUMERIC(12, 4),
    is_active BOOLEAN NOT NULL DEFAULT TRUE,
    revision BIGINT NOT NULL DEFAULT 1,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
DO $$ BEGIN
    ALTER TABLE charge_codes ADD CONSTRAINT charge_codes_code_format
        CHECK (code ~ '^[A-Z0-9_]{1,16}$');
EXCEPTION WHEN duplicate_object THEN NULL;
END $$;

-- Account 4030 (REVENUE, CREDIT), then the seeded codes. The seed only
-- inserts missing codes; the dealer sets taxability per jurisdiction.
INSERT INTO gl_accounts (code, name, type, normal_balance, is_active, description)
SELECT '4030', 'Fees and Charges Revenue', 'REVENUE', 'CREDIT', TRUE,
       'Restocking and other fees (ADR 0005 2.5)'
WHERE NOT EXISTS (SELECT 1 FROM gl_accounts WHERE code = '4030');

INSERT INTO charge_codes (code, name, revenue_account_code, taxable)
SELECT v.code, v.name, v.account, v.taxable
FROM (VALUES
    ('FREIGHT', 'Freight', '4020', FALSE),
    ('FUEL', 'Fuel surcharge', '4020', FALSE),
    ('RESTOCK', 'Restocking fee', '4030', FALSE),
    ('ADJUST', 'Adjustment', '4010', FALSE)
) AS v(code, name, account, taxable)
ON CONFLICT (code) DO NOTHING;

-- 7. order_lines: the shared line shape of ADR 0005 2.2.
UPDATE order_lines SET created_at = NOW() WHERE created_at IS NULL;
ALTER TABLE order_lines ALTER COLUMN created_at SET NOT NULL;

ALTER TABLE order_lines ADD COLUMN IF NOT EXISTS line_type TEXT NOT NULL DEFAULT 'PRODUCT';
ALTER TABLE order_lines ADD COLUMN IF NOT EXISTS position INTEGER NOT NULL DEFAULT 0;
ALTER TABLE order_lines ADD COLUMN IF NOT EXISTS parent_line_id UUID REFERENCES order_lines(id) ON DELETE CASCADE;
ALTER TABLE order_lines ADD COLUMN IF NOT EXISTS charge_code_id UUID REFERENCES charge_codes(id);
ALTER TABLE order_lines ADD COLUMN IF NOT EXISTS sku TEXT;
ALTER TABLE order_lines ADD COLUMN IF NOT EXISTS description TEXT;
ALTER TABLE order_lines ADD COLUMN IF NOT EXISTS uom TEXT;
ALTER TABLE order_lines ADD COLUMN IF NOT EXISTS price_uom TEXT;
ALTER TABLE order_lines ADD COLUMN IF NOT EXISTS uom_qty NUMERIC(12, 4);
ALTER TABLE order_lines ADD COLUMN IF NOT EXISTS price_uom_qty NUMERIC(12, 4);
ALTER TABLE order_lines ADD COLUMN IF NOT EXISTS priced_unit_price NUMERIC(12, 4);
ALTER TABLE order_lines ADD COLUMN IF NOT EXISTS price_source TEXT NOT NULL DEFAULT 'PRICE_LIST';
ALTER TABLE order_lines ADD COLUMN IF NOT EXISTS override_reason TEXT;
ALTER TABLE order_lines ADD COLUMN IF NOT EXISTS discount_percent NUMERIC(7, 4);
ALTER TABLE order_lines ADD COLUMN IF NOT EXISTS discount_amount NUMERIC(12, 2);
ALTER TABLE order_lines ADD COLUMN IF NOT EXISTS discount_reason TEXT;
ALTER TABLE order_lines ADD COLUMN IF NOT EXISTS price_adjusted_by TEXT;
ALTER TABLE order_lines ADD COLUMN IF NOT EXISTS line_total NUMERIC(12, 2);
ALTER TABLE order_lines ADD COLUMN IF NOT EXISTS taxable BOOLEAN NOT NULL DEFAULT TRUE;
ALTER TABLE order_lines ADD COLUMN IF NOT EXISTS revenue_account_code TEXT;
ALTER TABLE order_lines ADD COLUMN IF NOT EXISTS quote_line_id UUID REFERENCES quote_lines(id) ON DELETE SET NULL;
ALTER TABLE order_lines ADD COLUMN IF NOT EXISTS quantity_allocated NUMERIC(12, 4) NOT NULL DEFAULT 0;
ALTER TABLE order_lines ADD COLUMN IF NOT EXISTS quantity_backordered NUMERIC(12, 4) NOT NULL DEFAULT 0;
ALTER TABLE order_lines ADD COLUMN IF NOT EXISTS quantity_fulfilled NUMERIC(12, 4) NOT NULL DEFAULT 0;

-- price_each becomes unit_price, widened to the wire's scale 4: the value
-- keeps its meaning (per sale unit, which the pair holds as the price unit
-- while it is 1 and 1).
ALTER TABLE order_lines RENAME COLUMN price_each TO unit_price;
ALTER TABLE order_lines ALTER COLUMN unit_price TYPE NUMERIC(12, 4);
ALTER TABLE order_lines ALTER COLUMN quantity TYPE NUMERIC(12, 4);
ALTER TABLE order_lines ALTER COLUMN special_order_cost TYPE NUMERIC(12, 4);

-- Backfills: description and sku from the product; the units from the
-- product's own stocking unit, the pair 1 and 1; the priced price is what
-- the line held; the source is QUOTE where the order came from a quote;
-- the extension is what today's order total summed.
UPDATE order_lines l
SET sku = COALESCE(l.sku, p.sku),
    description = COALESCE(l.description, p.description, p.sku, ''),
    uom = COALESCE(l.uom, p.uom_primary::text),
    price_uom = COALESCE(l.price_uom, p.uom_primary::text)
FROM products p
WHERE l.product_id = p.id
  AND (l.sku IS NULL OR l.description IS NULL OR l.uom IS NULL OR l.price_uom IS NULL);

UPDATE order_lines SET uom_qty = 1 WHERE uom_qty IS NULL;
UPDATE order_lines SET price_uom_qty = 1 WHERE price_uom_qty IS NULL;
UPDATE order_lines SET priced_unit_price = unit_price WHERE priced_unit_price IS NULL;
UPDATE order_lines SET line_total = ROUND(quantity * unit_price, 2) WHERE line_total IS NULL;
UPDATE order_lines l
SET price_source = 'QUOTE'
FROM orders o
WHERE l.order_id = o.id AND o.quote_id IS NOT NULL AND l.price_source = 'PRICE_LIST';

-- position by (created_at, id) within the order, so lines keep their order.
WITH ordered AS (
    SELECT id, row_number() OVER (PARTITION BY order_id ORDER BY created_at, id) - 1 AS n
    FROM order_lines
)
UPDATE order_lines l SET position = o.n FROM ordered o WHERE l.id = o.id;

-- Today's confirm allocates the whole order in one act and FULFILLED means
-- it left: an existing CONFIRMED order with no invoice holds its quantity
-- allocated, a FULFILLED order has fulfilled it. Today's ON_HOLD is set
-- before allocating, so it holds none.
UPDATE order_lines l
SET quantity_allocated = l.quantity
FROM orders o
WHERE l.order_id = o.id AND o.status = 'CONFIRMED'
  AND NOT EXISTS (SELECT 1 FROM invoices i WHERE i.order_id = o.id);
UPDATE order_lines l
SET quantity_fulfilled = l.quantity
FROM orders o
WHERE l.order_id = o.id AND o.status = 'FULFILLED';

-- product_id drops NOT NULL behind the per type CHECKs of ADR 0005 2.2.
ALTER TABLE order_lines ALTER COLUMN product_id DROP NOT NULL;
ALTER TABLE order_lines ALTER COLUMN quantity DROP NOT NULL;
ALTER TABLE order_lines ALTER COLUMN unit_price DROP NOT NULL;

DO $$ BEGIN
    ALTER TABLE order_lines ADD CONSTRAINT order_lines_line_type_check
        CHECK (line_type IN ('PRODUCT', 'KIT', 'COMPONENT', 'CHARGE', 'TEXT'));
EXCEPTION WHEN duplicate_object THEN NULL;
END $$;
DO $$ BEGIN
    ALTER TABLE order_lines ADD CONSTRAINT order_lines_price_source_check
        CHECK (price_source IN ('PRICE_LIST', 'QUOTE', 'OVERRIDE', 'MANUAL', 'NONE'));
EXCEPTION WHEN duplicate_object THEN NULL;
END $$;
DO $$ BEGIN
    ALTER TABLE order_lines ADD CONSTRAINT order_lines_pair_positive
        CHECK ((uom_qty IS NULL AND price_uom_qty IS NULL) OR (uom_qty > 0 AND price_uom_qty > 0));
EXCEPTION WHEN duplicate_object THEN NULL;
END $$;
DO $$ BEGIN
    ALTER TABLE order_lines ADD CONSTRAINT order_lines_shape
        CHECK (
            -- a text line: a note, no amount, nothing priced
            (line_type = 'TEXT'
                AND quantity IS NULL AND uom IS NULL AND price_uom IS NULL
                AND uom_qty IS NULL AND price_uom_qty IS NULL
                AND unit_price IS NULL AND line_total IS NULL
                AND product_id IS NULL AND charge_code_id IS NULL)
            -- product (stocked or non stock) and kit lines: fully priced
            OR (line_type IN ('PRODUCT', 'KIT')
                AND quantity IS NOT NULL AND quantity > 0
                AND uom IS NOT NULL AND price_uom IS NOT NULL
                AND uom_qty IS NOT NULL AND price_uom_qty IS NOT NULL
                AND unit_price IS NOT NULL AND unit_price >= 0
                AND line_total IS NOT NULL
                AND charge_code_id IS NULL)
            -- a component: priced fields present at zero, always a product
            OR (line_type = 'COMPONENT'
                AND product_id IS NOT NULL AND parent_line_id IS NOT NULL
                AND quantity IS NOT NULL AND quantity > 0
                AND uom IS NOT NULL AND price_uom IS NOT NULL
                AND uom_qty IS NOT NULL AND price_uom_qty IS NOT NULL
                AND unit_price IS NOT NULL AND unit_price = 0
                AND line_total IS NOT NULL AND line_total = 0
                AND charge_code_id IS NULL AND taxable = FALSE)
            -- a charge: priced, always a code, never a product
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
    ALTER TABLE order_lines ADD CONSTRAINT order_lines_parent_only_on_component
        CHECK ((line_type = 'COMPONENT') = (parent_line_id IS NOT NULL));
EXCEPTION WHEN duplicate_object THEN NULL;
END $$;
DO $$ BEGIN
    ALTER TABLE order_lines ADD CONSTRAINT order_lines_kit_product_required
        CHECK ((line_type IN ('KIT', 'COMPONENT') AND product_id IS NOT NULL)
            OR line_type IN ('PRODUCT', 'CHARGE', 'TEXT'));
EXCEPTION WHEN duplicate_object THEN NULL;
END $$;

-- 8. The keyset index for the list's ordering.
CREATE INDEX IF NOT EXISTS idx_orders_created_id ON orders (created_at DESC, id DESC);
