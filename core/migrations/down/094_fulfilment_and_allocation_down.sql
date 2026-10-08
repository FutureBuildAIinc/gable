-- SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
-- SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

-- Rollback for 094_fulfilment_and_allocation.sql. Lives in migrations/down/ so
-- cmd/migrate's `migrations/*.sql` glob cannot pick it up as a forward
-- migration. The drops lose the queues, the invoice header and line columns
-- fulfilment wrote, and the line types the old shape could not hold; the
-- documents themselves stay. Orders migrated as fulfilled stay fulfilled (the
-- inventory they consumed does not come back).

DROP INDEX IF EXISTS idx_invoice_lines_order_line;
DROP INDEX IF EXISTS idx_order_lines_backordered;
ALTER TABLE invoice_lines DROP CONSTRAINT IF EXISTS invoice_lines_parent_only_on_component;
ALTER TABLE invoice_lines DROP CONSTRAINT IF EXISTS invoice_lines_shape;
ALTER TABLE invoice_lines DROP CONSTRAINT IF EXISTS invoice_lines_pair_positive;
ALTER TABLE invoice_lines DROP CONSTRAINT IF EXISTS invoice_lines_price_source_check;
ALTER TABLE invoice_lines DROP CONSTRAINT IF EXISTS invoice_lines_line_type_check;
DELETE FROM invoice_lines WHERE line_type IN ('KIT', 'COMPONENT', 'CHARGE', 'TEXT') OR product_id IS NULL;
ALTER TABLE invoice_lines ALTER COLUMN product_id SET NOT NULL;
ALTER TABLE invoice_lines ALTER COLUMN price_each TYPE NUMERIC(10, 2);
ALTER TABLE invoice_lines ALTER COLUMN quantity TYPE NUMERIC(10, 4);
ALTER TABLE invoice_lines DROP COLUMN IF EXISTS cost;
ALTER TABLE invoice_lines DROP COLUMN IF EXISTS unit_cost;
ALTER TABLE invoice_lines DROP COLUMN IF EXISTS order_line_id;
ALTER TABLE invoice_lines DROP COLUMN IF EXISTS revenue_account_code;
ALTER TABLE invoice_lines DROP COLUMN IF EXISTS taxable;
ALTER TABLE invoice_lines DROP COLUMN IF EXISTS line_total;
ALTER TABLE invoice_lines DROP COLUMN IF EXISTS discount_reason;
ALTER TABLE invoice_lines DROP COLUMN IF EXISTS discount_amount;
ALTER TABLE invoice_lines DROP COLUMN IF EXISTS discount_percent;
ALTER TABLE invoice_lines DROP COLUMN IF EXISTS price_source;
ALTER TABLE invoice_lines DROP COLUMN IF EXISTS unit_price;
ALTER TABLE invoice_lines DROP COLUMN IF EXISTS price_uom_qty;
ALTER TABLE invoice_lines DROP COLUMN IF EXISTS uom_qty;
ALTER TABLE invoice_lines DROP COLUMN IF EXISTS price_uom;
ALTER TABLE invoice_lines DROP COLUMN IF EXISTS uom;
ALTER TABLE invoice_lines DROP COLUMN IF EXISTS description;
ALTER TABLE invoice_lines DROP COLUMN IF EXISTS sku;
ALTER TABLE invoice_lines DROP COLUMN IF EXISTS charge_code_id;
ALTER TABLE invoice_lines DROP COLUMN IF EXISTS parent_line_id;
ALTER TABLE invoice_lines DROP COLUMN IF EXISTS position;
ALTER TABLE invoice_lines DROP COLUMN IF EXISTS line_type;

DROP INDEX IF EXISTS idx_invoices_delivery;
ALTER TABLE invoices DROP CONSTRAINT IF EXISTS invoices_origin_check;
ALTER TABLE invoices DROP COLUMN IF EXISTS origin;
ALTER TABLE invoices ALTER COLUMN invoice_date DROP NOT NULL;
ALTER TABLE invoices DROP COLUMN IF EXISTS invoice_date;
ALTER TABLE invoices DROP COLUMN IF EXISTS gl_entry_id;
ALTER TABLE invoices DROP CONSTRAINT IF EXISTS invoices_tax_source_check;
ALTER TABLE invoices DROP COLUMN IF EXISTS tax_source;
ALTER TABLE invoices DROP COLUMN IF EXISTS tax_exempt;
UPDATE invoices SET tax_rate = 0 WHERE tax_rate IS NULL;
ALTER TABLE invoices ALTER COLUMN tax_rate TYPE NUMERIC(5, 4);
ALTER TABLE invoices ALTER COLUMN tax_rate SET DEFAULT 0;
ALTER TABLE invoices DROP COLUMN IF EXISTS project_id;
ALTER TABLE invoices DROP COLUMN IF EXISTS ship_to_snapshot;
ALTER TABLE invoices DROP COLUMN IF EXISTS ship_to_id;
ALTER TABLE invoices DROP COLUMN IF EXISTS delivery_id;
ALTER TABLE invoices DROP COLUMN IF EXISTS picked_up_by;
ALTER TABLE invoices DROP CONSTRAINT IF EXISTS invoices_delivery_type_check;
ALTER TABLE invoices DROP COLUMN IF EXISTS delivery_type;
ALTER TABLE invoices DROP CONSTRAINT IF EXISTS invoices_currency_format;
ALTER TABLE invoices DROP COLUMN IF EXISTS currency;

DROP TABLE IF EXISTS order_fulfillment_requests;
DROP TABLE IF EXISTS order_allocation_requests;
