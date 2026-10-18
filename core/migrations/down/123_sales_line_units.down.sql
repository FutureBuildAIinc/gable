-- SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
-- SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

-- Down of 123_sales_line_units (item C3-2B). It reverses the migration's
-- own steps and refuses, naming the first row it cannot map back, wherever
-- data written in the new shape has no place in the old one (a down never
-- discards a dealer's data):
--
--   a line of any document carries a tally: line_tally_rows held quote
--   rows only before 123;
--   a line stocks a unit other than the unit it sold in: the old shape had
--   no stocking columns, and a changed stocking unit cannot be undone;
--   a portal cart item holds a sub cent unit price, a unit of its own or a
--   pair other than 1 and 1: the old cart priced per the stocking unit at
--   two decimals.
--
-- It restores: the quote only tally table, the tables without the stocking,
-- cross section, basis and cost columns, the free text unit columns, the
-- cart's NUMERIC(12,2) price, and the stocking unit hold (the CHECK and the
-- four triggers of 099 and 122, word for word).

-- The refusals, before anything moves.
DO $$
DECLARE
    v_id text;
BEGIN
    FOR v_id IN
        (SELECT 'an order line tally of ' || count(*)::text || ' row(s)' FROM line_tally_rows WHERE order_line_id IS NOT NULL HAVING count(*) > 0)
        UNION ALL
        (SELECT 'an invoice line tally of ' || count(*)::text || ' row(s)' FROM line_tally_rows WHERE invoice_line_id IS NOT NULL HAVING count(*) > 0)
        UNION ALL
        (SELECT 'a credit memo line tally of ' || count(*)::text || ' row(s)' FROM line_tally_rows WHERE credit_memo_line_id IS NOT NULL HAVING count(*) > 0)
        UNION ALL
        (SELECT 'a counter line tally of ' || count(*)::text || ' row(s)' FROM line_tally_rows WHERE pos_line_item_id IS NOT NULL HAVING count(*) > 0)
        UNION ALL
        (SELECT 'order line ' || id::text || ' stocks ' || stock_uom || ', not ' || uom
        FROM order_lines WHERE stock_uom IS NOT NULL AND stock_uom <> uom LIMIT 1)
        UNION ALL
        (SELECT 'invoice line ' || id::text || ' stocks ' || stock_uom || ', not ' || uom
        FROM invoice_lines WHERE stock_uom IS NOT NULL AND stock_uom <> uom LIMIT 1)
        UNION ALL
        (SELECT 'credit memo line ' || id::text || ' stocks ' || stock_uom || ', not ' || uom
        FROM credit_memo_lines WHERE stock_uom IS NOT NULL AND stock_uom <> uom LIMIT 1)
        UNION ALL
        (SELECT 'counter line ' || id::text || ' stocks ' || stock_uom || ', not ' || uom
        FROM pos_line_items WHERE stock_uom IS NOT NULL AND stock_uom <> uom LIMIT 1)
        UNION ALL
        (SELECT 'counter return line ' || id::text || ' stocks ' || stock_uom || ', not ' || uom
        FROM pos_return_lines WHERE stock_uom IS NOT NULL AND stock_uom <> uom LIMIT 1)
        UNION ALL
        (SELECT 'portal cart item ' || id::text || ' carries the sub cent price ' || unit_price::text
        FROM portal_cart_items WHERE unit_price * 100 <> floor(unit_price * 100) LIMIT 1)
        UNION ALL
        (SELECT 'portal cart item ' || id::text || ' prices per ' || price_uom || ', not its unit ' || uom
        FROM portal_cart_items WHERE price_uom IS NOT NULL AND price_uom <> uom LIMIT 1)
        UNION ALL
        (SELECT 'portal cart item ' || id::text || ' carries the pair ' || uom_qty::text || ' and ' || price_uom_qty::text
        FROM portal_cart_items WHERE uom_qty IS NOT NULL AND price_uom_qty IS NOT NULL
            AND (uom_qty <> 1 OR price_uom_qty <> 1) LIMIT 1)
    LOOP
        RAISE EXCEPTION '123 down: %; a down can only restore the shape every row already fits', v_id;
    END LOOP;
END $$;

-- B4 reversed: the stocking unit hold comes back, word for word from 099
-- and 122 (the price unit CHECK, the raw write triggers and their
-- functions).
ALTER TABLE products ADD CONSTRAINT products_price_uom_is_stocking
    CHECK (price_uom = uom_primary);

CREATE OR REPLACE FUNCTION pricing_price_uom_held() RETURNS trigger AS $$
DECLARE
    v_stock text;
BEGIN
    IF NEW.product_id IS NULL THEN
        RETURN NULL; -- a rule without a product prices per each product's own unit
    END IF;
    IF NEW.price_uom IS NULL THEN
        RETURN NULL; -- a rule with no fixed price names no unit; the CHECK holds the pairing
    END IF;
    SELECT uom_primary INTO v_stock FROM products WHERE id = NEW.product_id;
    IF NOT FOUND THEN
        RETURN NULL; -- the product is going away; its rows cascade
    END IF;
    IF NEW.price_uom IS DISTINCT FROM v_stock THEN
        RAISE EXCEPTION 'price_unit_held: product % is stocked in % and the price names %; a price unit other than the stocking unit arrives with C3-2B',
            NEW.product_id, v_stock, NEW.price_uom;
    END IF;
    RETURN NULL;
END
$$ LANGUAGE plpgsql;

CREATE CONSTRAINT TRIGGER product_prices_price_uom_held
    AFTER INSERT OR UPDATE OF price_uom, product_id ON product_prices
    DEFERRABLE INITIALLY DEFERRED
    FOR EACH ROW EXECUTE FUNCTION pricing_price_uom_held();
CREATE CONSTRAINT TRIGGER customer_contracts_price_uom_held
    AFTER INSERT OR UPDATE OF price_uom, product_id ON customer_contracts
    DEFERRABLE INITIALLY DEFERRED
    FOR EACH ROW EXECUTE FUNCTION pricing_price_uom_held();
CREATE CONSTRAINT TRIGGER pricing_rules_price_uom_held
    AFTER INSERT OR UPDATE OF price_uom, product_id, fixed_price ON pricing_rules
    DEFERRABLE INITIALLY DEFERRED
    FOR EACH ROW EXECUTE FUNCTION pricing_price_uom_held();

CREATE OR REPLACE FUNCTION products_price_unit_held() RETURNS trigger AS $$
BEGIN
    IF NEW.uom_primary IS DISTINCT FROM OLD.uom_primary
       OR NEW.price_uom IS DISTINCT FROM OLD.price_uom THEN
        IF NEW.price_uom IS DISTINCT FROM NEW.uom_primary THEN
            RAISE EXCEPTION 'price_unit_held: product %: a price unit other than the stocking unit arrives with C3-2B', OLD.id;
        END IF;
        IF NEW.uom_primary IS DISTINCT FROM OLD.uom_primary
           AND COALESCE(NEW.base_price, 0) <> 0 THEN
            RAISE EXCEPTION 'price_unit_held: product % has a nonzero base price in %; zero the base price before changing the stocking unit', OLD.id, OLD.uom_primary;
        END IF;
        IF EXISTS (SELECT 1 FROM product_prices pp WHERE pp.product_id = OLD.id)
           OR EXISTS (SELECT 1 FROM customer_contracts cc WHERE cc.product_id = OLD.id)
           OR EXISTS (SELECT 1 FROM pricing_rules r WHERE r.product_id = OLD.id AND r.fixed_price IS NOT NULL) THEN
            RAISE EXCEPTION 'price_unit_held: product % has a price, contract or rule priced per %; a price unit other than the stocking unit arrives with C3-2B', OLD.id, OLD.uom_primary;
        END IF;
    END IF;
    RETURN NULL;
END
$$ LANGUAGE plpgsql;

CREATE CONSTRAINT TRIGGER products_price_unit_held
    AFTER UPDATE OF uom_primary, price_uom ON products
    DEFERRABLE INITIALLY DEFERRED
    FOR EACH ROW EXECUTE FUNCTION products_price_unit_held();

-- B3 reversed: the cart item loses its unit fields and its price narrows.
ALTER TABLE portal_cart_items DROP CONSTRAINT IF EXISTS portal_cart_items_pair_positive;
ALTER TABLE portal_cart_items
    DROP COLUMN IF EXISTS uom,
    DROP COLUMN IF EXISTS price_uom,
    DROP COLUMN IF EXISTS uom_qty,
    DROP COLUMN IF EXISTS price_uom_qty;
ALTER TABLE portal_cart_items ALTER COLUMN unit_price TYPE NUMERIC(12,2);

-- B2 reversed: the tally table returns to its quote only shape of 099.
DROP INDEX IF EXISTS line_tally_rows_order_line_length;
DROP INDEX IF EXISTS line_tally_rows_invoice_line_length;
DROP INDEX IF EXISTS line_tally_rows_credit_memo_line_length;
DROP INDEX IF EXISTS line_tally_rows_pos_line_item_length;
ALTER TABLE line_tally_rows DROP CONSTRAINT IF EXISTS line_tally_rows_one_line;
ALTER TABLE line_tally_rows
    DROP COLUMN IF EXISTS order_line_id,
    DROP COLUMN IF EXISTS invoice_line_id,
    DROP COLUMN IF EXISTS credit_memo_line_id,
    DROP COLUMN IF EXISTS pos_line_item_id;
ALTER TABLE line_tally_rows ALTER COLUMN quote_line_id SET NOT NULL;

-- B1 reversed: the stocking, cross section, basis and cost columns go.
ALTER TABLE order_lines
    DROP CONSTRAINT IF EXISTS order_lines_stock_follows_product,
    DROP COLUMN IF EXISTS stock_uom,
    DROP COLUMN IF EXISTS stock_quantity,
    DROP COLUMN IF EXISTS board_thickness_in,
    DROP COLUMN IF EXISTS board_width_in,
    DROP COLUMN IF EXISTS price_basis,
    DROP COLUMN IF EXISTS cost_source,
    DROP COLUMN IF EXISTS unit_cost;
ALTER TABLE invoice_lines
    DROP CONSTRAINT IF EXISTS invoice_lines_stock_follows_product,
    DROP COLUMN IF EXISTS stock_uom,
    DROP COLUMN IF EXISTS stock_quantity,
    DROP COLUMN IF EXISTS board_thickness_in,
    DROP COLUMN IF EXISTS board_width_in,
    DROP COLUMN IF EXISTS price_basis,
    DROP COLUMN IF EXISTS cost_source;
ALTER TABLE credit_memo_lines
    DROP COLUMN IF EXISTS stock_uom,
    DROP COLUMN IF EXISTS stock_quantity,
    DROP COLUMN IF EXISTS board_thickness_in,
    DROP COLUMN IF EXISTS board_width_in,
    DROP COLUMN IF EXISTS price_basis,
    DROP COLUMN IF EXISTS cost_source;
ALTER TABLE pos_line_items
    DROP CONSTRAINT IF EXISTS pos_line_items_stock_follows_product,
    DROP COLUMN IF EXISTS stock_uom,
    DROP COLUMN IF EXISTS stock_quantity,
    DROP COLUMN IF EXISTS board_thickness_in,
    DROP COLUMN IF EXISTS board_width_in,
    DROP COLUMN IF EXISTS price_basis,
    DROP COLUMN IF EXISTS cost_source,
    DROP COLUMN IF EXISTS unit_cost;
ALTER TABLE pos_return_lines
    DROP COLUMN IF EXISTS stock_uom,
    DROP COLUMN IF EXISTS stock_quantity,
    DROP COLUMN IF EXISTS board_thickness_in,
    DROP COLUMN IF EXISTS board_width_in;

-- B0 reversed: the unit foreign keys go (the columns stay, as the free text
-- they were), and the counter's two columns return to VARCHAR(16) only
-- when every value they hold fits.
DO $$
DECLARE
    v_val text;
BEGIN
    SELECT max(uom) INTO v_val FROM pos_line_items WHERE uom IS NOT NULL AND length(uom) > 16;
    IF v_val IS NOT NULL THEN
        RAISE EXCEPTION '123 down: pos_line_items holds the unit value %, which is longer than the sixteen characters the old column held', v_val;
    END IF;
    SELECT max(uom) INTO v_val FROM pos_return_lines WHERE length(uom) > 16;
    IF v_val IS NOT NULL THEN
        RAISE EXCEPTION '123 down: pos_return_lines holds the unit value %, which is longer than the sixteen characters the old column held', v_val;
    END IF;
END $$;

ALTER TABLE order_lines DROP CONSTRAINT IF EXISTS order_lines_uom_fkey;
ALTER TABLE order_lines DROP CONSTRAINT IF EXISTS order_lines_price_uom_fkey;
ALTER TABLE invoice_lines DROP CONSTRAINT IF EXISTS invoice_lines_uom_fkey;
ALTER TABLE invoice_lines DROP CONSTRAINT IF EXISTS invoice_lines_price_uom_fkey;
ALTER TABLE credit_memo_lines DROP CONSTRAINT IF EXISTS credit_memo_lines_uom_fkey;
ALTER TABLE credit_memo_lines DROP CONSTRAINT IF EXISTS credit_memo_lines_price_uom_fkey;
ALTER TABLE pos_line_items DROP CONSTRAINT IF EXISTS pos_line_items_uom_fkey;
ALTER TABLE pos_return_lines DROP CONSTRAINT IF EXISTS pos_return_lines_uom_fkey;

ALTER TABLE pos_line_items ALTER COLUMN uom TYPE VARCHAR(16);
ALTER TABLE pos_return_lines ALTER COLUMN uom TYPE VARCHAR(16);
