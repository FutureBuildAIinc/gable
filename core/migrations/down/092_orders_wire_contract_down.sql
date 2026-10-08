-- SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
-- SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

-- Rollback for 092_orders_wire_contract.sql (the C2-2a half). Lives in
-- migrations/down/ so cmd/migrate's `migrations/*.sql` glob cannot pick it
-- up as a forward migration. The drops lose the numbers, revisions, line
-- shapes and tax estimates the migration wrote; the documents themselves
-- stay. Line rows written as KIT, COMPONENT, CHARGE or TEXT cannot go back
-- to the old shape (price_each was NOT NULL and product_id NOT NULL): they
-- are deleted, which is the rollback's honest answer for types the old
-- schema could not hold.

DROP INDEX IF EXISTS idx_orders_created_id;

ALTER TABLE order_lines DROP CONSTRAINT IF EXISTS order_lines_kit_product_required;
ALTER TABLE order_lines DROP CONSTRAINT IF EXISTS order_lines_parent_only_on_component;
ALTER TABLE order_lines DROP CONSTRAINT IF EXISTS order_lines_shape;
ALTER TABLE order_lines DROP CONSTRAINT IF EXISTS order_lines_pair_positive;
ALTER TABLE order_lines DROP CONSTRAINT IF EXISTS order_lines_price_source_check;
ALTER TABLE order_lines DROP CONSTRAINT IF EXISTS order_lines_line_type_check;
DELETE FROM order_lines WHERE line_type IN ('KIT', 'COMPONENT', 'CHARGE', 'TEXT');
ALTER TABLE order_lines ALTER COLUMN unit_price TYPE NUMERIC(10, 2);
ALTER TABLE order_lines ALTER COLUMN unit_price SET NOT NULL;
ALTER TABLE order_lines RENAME COLUMN unit_price TO price_each;
ALTER TABLE order_lines ALTER COLUMN product_id SET NOT NULL;
ALTER TABLE order_lines ALTER COLUMN quantity SET NOT NULL;
ALTER TABLE order_lines ALTER COLUMN quantity TYPE NUMERIC(10, 4);
ALTER TABLE order_lines ALTER COLUMN special_order_cost TYPE DECIMAL(10, 2);
ALTER TABLE order_lines DROP COLUMN IF EXISTS quantity_backordered;
ALTER TABLE order_lines DROP COLUMN IF EXISTS quantity_allocated;
ALTER TABLE order_lines DROP COLUMN IF EXISTS quantity_fulfilled;
ALTER TABLE order_lines DROP COLUMN IF EXISTS quote_line_id;
ALTER TABLE order_lines DROP COLUMN IF EXISTS revenue_account_code;
ALTER TABLE order_lines DROP COLUMN IF EXISTS taxable;
ALTER TABLE order_lines DROP COLUMN IF EXISTS line_total;
ALTER TABLE order_lines DROP COLUMN IF EXISTS price_adjusted_by;
ALTER TABLE order_lines DROP COLUMN IF EXISTS discount_reason;
ALTER TABLE order_lines DROP COLUMN IF EXISTS discount_amount;
ALTER TABLE order_lines DROP COLUMN IF EXISTS discount_percent;
ALTER TABLE order_lines DROP COLUMN IF EXISTS override_reason;
ALTER TABLE order_lines DROP COLUMN IF EXISTS price_source;
ALTER TABLE order_lines DROP COLUMN IF EXISTS priced_unit_price;
ALTER TABLE order_lines DROP COLUMN IF EXISTS price_uom_qty;
ALTER TABLE order_lines DROP COLUMN IF EXISTS uom_qty;
ALTER TABLE order_lines DROP COLUMN IF EXISTS price_uom;
ALTER TABLE order_lines DROP COLUMN IF EXISTS uom;
ALTER TABLE order_lines DROP COLUMN IF EXISTS description;
ALTER TABLE order_lines DROP COLUMN IF EXISTS sku;
ALTER TABLE order_lines DROP COLUMN IF EXISTS charge_code_id;
ALTER TABLE order_lines DROP COLUMN IF EXISTS parent_line_id;
ALTER TABLE order_lines DROP COLUMN IF EXISTS position;
ALTER TABLE order_lines DROP COLUMN IF EXISTS line_type;
ALTER TABLE order_lines ALTER COLUMN created_at DROP NOT NULL;

DROP TABLE IF EXISTS charge_codes;
DELETE FROM gl_accounts WHERE code = '4030';
DROP TABLE IF EXISTS product_kit_components;
ALTER TABLE products DROP COLUMN IF EXISTS taxable;
ALTER TABLE products DROP COLUMN IF EXISTS is_kit;
ALTER TABLE locations ALTER COLUMN default_tax_rate TYPE NUMERIC(7, 4);

ALTER TABLE orders DROP CONSTRAINT IF EXISTS orders_status_check;
ALTER TABLE orders ADD CONSTRAINT orders_status_check
    CHECK (status IN ('DRAFT', 'CONFIRMED', 'FULFILLED', 'CANCELLED', 'ON_HOLD'));
ALTER TABLE orders ALTER COLUMN total_amount TYPE DECIMAL(10, 2);
ALTER TABLE orders DROP COLUMN IF EXISTS confirmed_at;
ALTER TABLE orders DROP CONSTRAINT IF EXISTS orders_tax_source_check;
ALTER TABLE orders DROP COLUMN IF EXISTS tax_source;
ALTER TABLE orders DROP COLUMN IF EXISTS tax_exempt;
ALTER TABLE orders DROP COLUMN IF EXISTS tax_rate;
ALTER TABLE orders DROP COLUMN IF EXISTS tax_amount;
ALTER TABLE orders DROP COLUMN IF EXISTS subtotal;
ALTER TABLE orders DROP COLUMN IF EXISTS hold_note;
ALTER TABLE orders DROP CONSTRAINT IF EXISTS orders_hold_reason_check;
ALTER TABLE orders DROP COLUMN IF EXISTS hold_reason;
ALTER TABLE orders DROP COLUMN IF EXISTS ordered_by_contact_id;
ALTER TABLE orders DROP COLUMN IF EXISTS customer_po;
ALTER TABLE orders DROP COLUMN IF EXISTS ship_to_snapshot;
ALTER TABLE orders DROP COLUMN IF EXISTS ship_to_id;

ALTER TABLE orders DROP CONSTRAINT IF EXISTS orders_delivery_type_check;
ALTER TABLE orders ALTER COLUMN delivery_type DROP DEFAULT;
ALTER TABLE orders DROP COLUMN IF EXISTS delivery_type;

-- The subledger's type goes back to the four value enum the base knew.
ALTER TABLE customer_transactions DROP COLUMN IF EXISTS source_kind;
ALTER TABLE customer_transactions ALTER COLUMN currency DROP DEFAULT;
ALTER TABLE customer_transactions DROP COLUMN IF EXISTS currency;
CREATE TYPE transaction_type AS ENUM ('INVOICE', 'PAYMENT', 'ADJUSTMENT', 'REFUND');
ALTER TABLE customer_transactions RENAME COLUMN type TO type_text;
ALTER TABLE customer_transactions ADD COLUMN type transaction_type;
UPDATE customer_transactions SET type = type_text::transaction_type;
ALTER TABLE customer_transactions ALTER COLUMN type SET NOT NULL;
ALTER TABLE customer_transactions DROP COLUMN type_text;

ALTER TABLE gl_journal_entries DROP CONSTRAINT IF EXISTS gl_journal_entries_source_check;
ALTER TABLE gl_journal_entries ADD CONSTRAINT gl_journal_entries_source_check
    CHECK (source IN ('MANUAL','INVOICE','PAYMENT','ADJUSTMENT','CLOSING','REVERSAL','VENDOR_INVOICE','VENDOR_PAYMENT','RETURN','DEPOSIT'));
ALTER TABLE gl_journal_entries DROP COLUMN IF EXISTS currency;

ALTER TABLE orders DROP CONSTRAINT IF EXISTS orders_currency_format;
ALTER TABLE orders DROP COLUMN IF EXISTS currency;

ALTER TABLE orders DROP CONSTRAINT IF EXISTS orders_number_key;
ALTER TABLE orders ALTER COLUMN number DROP DEFAULT;
ALTER TABLE orders ALTER COLUMN number DROP NOT NULL;
ALTER TABLE orders DROP COLUMN IF EXISTS number;
DROP FUNCTION IF EXISTS order_next_number();
DROP SEQUENCE IF EXISTS order_number_seq;

ALTER TABLE orders DROP COLUMN IF EXISTS revision;
ALTER TABLE orders ALTER COLUMN created_at DROP NOT NULL;
