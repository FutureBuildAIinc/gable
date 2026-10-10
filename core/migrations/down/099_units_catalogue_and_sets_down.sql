-- SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
-- SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

-- Down of 099_units_catalogue_and_sets (item C3-2A-units). Each step
-- reverses its own, and refuses, naming the first row it cannot map back,
-- wherever data written in the new shape has no place in the old one (a
-- down never discards a dealer's data):
--
--   A3  drops the quote tally columns and table;
--   A2  refuses while any product holds a unit set row other than its
--       stocking row (the conversions are the dealer's own: casting them
--       away would silently re-unit every product), then drops the unit
--       set, the board measure columns, the hold and its triggers;
--   A1  recreates uom_type and casts products.uom_primary and
--       quote_lines.uom back only when every stored value is one of its
--       sixteen codes, raising an exception naming the first value that is
--       not, then drops the catalogue.

-- A3 ---------------------------------------------------------------------

ALTER TABLE quote_lines
    DROP COLUMN IF EXISTS stock_uom,
    DROP COLUMN IF EXISTS stock_quantity,
    DROP COLUMN IF EXISTS board_thickness_in,
    DROP COLUMN IF EXISTS board_width_in;
DROP TABLE IF EXISTS line_tally_rows;

-- A2 ---------------------------------------------------------------------

DROP TABLE IF EXISTS product_unit_candidates;

DROP TRIGGER IF EXISTS products_price_unit_held ON products;
DROP FUNCTION IF EXISTS products_price_unit_held();
DROP TRIGGER IF EXISTS products_stocking_row ON products;
DROP TRIGGER IF EXISTS product_units_stocking_row ON product_units;
DROP FUNCTION IF EXISTS product_stocking_row_invariant();
DROP TRIGGER IF EXISTS products_stocking_row_default ON products;
DROP FUNCTION IF EXISTS products_stocking_row_default();
DROP TRIGGER IF EXISTS products_unit_defaults ON products;
DROP FUNCTION IF EXISTS products_unit_defaults();

ALTER TABLE products DROP CONSTRAINT IF EXISTS products_price_uom_is_stocking;
ALTER TABLE products DROP CONSTRAINT IF EXISTS products_sale_uom_fkey;
ALTER TABLE products DROP CONSTRAINT IF EXISTS products_price_uom_fkey;
ALTER TABLE products DROP CONSTRAINT IF EXISTS products_purchase_uom_fkey;

-- A dealer who gave a product units beyond its stocking row keeps them:
-- the down refuses rather than discard them.
DO $$
DECLARE
    offending RECORD;
BEGIN
    SELECT p.sku, p.uom_primary, pu.uom INTO offending
    FROM products p
    JOIN product_units pu ON pu.product_id = p.id AND pu.uom <> p.uom_primary
    LIMIT 1;
    IF offending IS NOT NULL THEN
        RAISE EXCEPTION 'product % holds a unit set row for % beside its stocking unit %; remove the extra rows before rolling back', offending.sku, offending.uom, offending.uom_primary;
    END IF;
END $$;

ALTER TABLE products
    DROP COLUMN IF EXISTS sale_uom,
    DROP COLUMN IF EXISTS price_uom,
    DROP COLUMN IF EXISTS purchase_uom;
DROP TABLE IF EXISTS product_units;
ALTER TABLE products
    DROP COLUMN IF EXISTS board_thickness_in,
    DROP COLUMN IF EXISTS board_width_in,
    DROP COLUMN IF EXISTS board_length_ft,
    DROP COLUMN IF EXISTS random_length;
ALTER TABLE products DROP CONSTRAINT IF EXISTS products_cross_section_both_or_neither;
ALTER TABLE products DROP CONSTRAINT IF EXISTS products_random_length_no_fixed_length;

-- A1 ---------------------------------------------------------------------

ALTER TABLE products DROP CONSTRAINT IF EXISTS products_uom_primary_fkey;
ALTER TABLE quote_lines DROP CONSTRAINT IF EXISTS quote_lines_uom_fkey;
ALTER TABLE quote_lines DROP CONSTRAINT IF EXISTS quote_lines_price_uom_fkey;

-- Cast back only when every stored value is one of the enum's sixteen
-- codes; otherwise name the first value that is not.
DO $$
BEGIN
    CREATE TYPE uom_type AS ENUM (
        'PCS', 'EA', 'LF', 'SF', 'BF', 'MBF', 'SQ', 'BOX', 'CTN', 'RL',
        'GAL', 'LBS', 'BAG', 'BUNDLE', 'PAIR', 'SET');
EXCEPTION
    WHEN duplicate_object THEN NULL;
END $$;

DO $$
DECLARE
    bad TEXT;
BEGIN
    SELECT val INTO bad FROM (
        SELECT uom_primary AS val FROM products
        UNION ALL SELECT uom FROM quote_lines
        UNION ALL SELECT price_uom FROM quote_lines WHERE price_uom IS NOT NULL
    ) c
    WHERE val::text !~ '^(PCS|EA|LF|SF|BF|MBF|SQ|BOX|CTN|RL|GAL|LBS|BAG|BUNDLE|PAIR|SET)$'
    LIMIT 1;
    IF bad IS NOT NULL THEN
        RAISE EXCEPTION 'the unit value % is not one of the sixteen uom_type codes; correct it before rolling back', bad;
    END IF;
END $$;

ALTER TABLE products ALTER COLUMN uom_primary TYPE uom_type USING uom_primary::uom_type;
ALTER TABLE quote_lines ALTER COLUMN uom TYPE uom_type USING uom::uom_type;
DROP TABLE IF EXISTS units;
