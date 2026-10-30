-- SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
-- SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

-- 162: the configured item (QE-06).
--
-- A configured item is a door or a millwork piece the dealer configures in
-- the configurator rather than picking off the shelf: the options it carries
-- are named, priced and costed on the server, and the result is a product
-- row of its own that carries no stock.
--
-- Four changes, in the order the slice needs them:
--
--   1. products.is_non_stock marks such a row. The mark is what keeps it
--      out of the stock reads: a configured item is never counted, never
--      replenished and never stocked at the counter. The column defaults to
--      FALSE, so every existing row keeps its present meaning: a row with
--      no mark is a stocked product.
--
--   2. millwork_options gains the money the server prices from. The option
--      catalog was a list of names and a price adjustment; a configured
--      item's price is the sum over its selected options of that option's
--      base plus that option's adjustment. base_price_cents and
--      base_cost_cents are the base an option contributes (the door type
--      row carries the door's base price and base cost, the base of
--      nothing else), price_adjustment and cost_adjustment_cents are the
--      deltas the option adds on top. The three new columns default to
--      zero, so an option that names no money contributes nothing.
--
--   3. quote_lines.configuration_json carries the configuration on a quote
--      line: the normalised selections the dealer made, with the door's
--      width and height. A line without one is an ordinary product line.
--
--   4. order_lines.configuration_json carries the same configuration on an
--      order line, so it survives the quote becoming an order and reads
--      back on the PO and the invoice.
--
-- No new tables and no new document numbers: a configured item is not an
-- externally addressable document (ADR 0001 section 8 names quotes, orders,
-- invoices and purchase orders). The money columns are integers in cents,
-- the unit the wire already carries for the option catalog, so no money
-- here is ever a float.

-- 1. The non-stock mark on a configured product row.
ALTER TABLE products ADD COLUMN IF NOT EXISTS is_non_stock BOOLEAN NOT NULL DEFAULT FALSE;

-- The index the stock reads filter on: every stock read asks the same
-- question, so the flag gets its own index rather than a scan of the
-- catalog.
CREATE INDEX IF NOT EXISTS idx_products_is_non_stock ON products (is_non_stock);

-- 2. The money the server prices a configuration from.
ALTER TABLE millwork_options ADD COLUMN IF NOT EXISTS base_price_cents BIGINT NOT NULL DEFAULT 0;
ALTER TABLE millwork_options ADD COLUMN IF NOT EXISTS base_cost_cents BIGINT NOT NULL DEFAULT 0;
ALTER TABLE millwork_options ADD COLUMN IF NOT EXISTS cost_adjustment_cents BIGINT NOT NULL DEFAULT 0;

-- 3 and 4. The configuration on the quote line, the order line and the
-- invoice line.
--
-- The purchase order line does not carry the configuration as a column: a
-- purchase order is written to the vendor, not to the dealer, and its line
-- already shows the configured item as a short description (the SKU and the
-- description the product row carries), which is what the vendor reads.
ALTER TABLE quote_lines ADD COLUMN IF NOT EXISTS configuration_json JSONB;
ALTER TABLE order_lines ADD COLUMN IF NOT EXISTS configuration_json JSONB;
ALTER TABLE invoice_lines ADD COLUMN IF NOT EXISTS configuration_json JSONB;
