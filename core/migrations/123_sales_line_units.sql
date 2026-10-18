-- SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
-- SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

-- 123: sales_line_units (item C3-2B, ADR 0006 sections 3.2 to 3.4, 4, 7.5,
-- 7.6 and 9.1; migration steps B0 to B4 of section 8).
--
--   B0  the cycle 2 tables' units. A1's collection, normalisation and
--       refusal rules (and the pre flight report, which already lists these
--       columns) over the uom and price_uom of order_lines, invoice_lines,
--       credit_memo_lines, pos_line_items and pos_return_lines; then
--       foreign keys to units(code) on every one of them. The counter's two
--       uom columns move from VARCHAR(16) to TEXT, the catalogue's own
--       type.
--   B1  the stocking columns of section 3.4 and the line basis and cost of
--       sections 5.3 and 7.5. Every sales line table gains stock_uom,
--       stock_quantity and the nominal cross section a tallied line
--       snapshots; order_lines and pos_line_items gain price_basis (the
--       engine's answer, null on lines the engine did not price); the cost
--       a line resolves rides beside it as cost_source and unit_cost (per
--       the stocking unit, the same column invoice_lines has carried since
--       the AR core). Backfill: a line with a product stocks what it sold
--       (stock_uom = uom, stock_quantity = quantity, exact at scale 4,
--       because ADR 0005 held every such line to its product's stocking
--       unit); the safe EA / 1 to 1 defaults are these: existing rows were
--       all written in the stocking unit, so no conversion happens at all.
--       A check holds stock_uom and stock_quantity to product_id, both set
--       or both null together.
--   B2  tallies on every document. line_tally_rows gains the order, invoice,
--       credit memo and counter line columns (each an ON DELETE CASCADE
--       foreign key with its own per length unique index), quote_line_id
--       loses its NOT NULL, and a five column check holds exactly one line
--       reference per row. The tally stays a child table, one repository
--       for every document: a JSONB column could hold neither the positive
--       pieces nor the one row per length (section 4.2's Alternatives).
--   B3  the portal cart item takes the line shape's unit fields (7.6):
--       unit_price widens to NUMERIC(12,4) so a sub cent unit price survives
--       the cart (3.75 per M bought by the EA is 0.00375 a piece), and uom,
--       price_uom, uom_qty and price_uom_qty arrive, backfilled to the
--       product's stocking unit and the pair 1 and 1 (every cart item today
--       is per the stocking unit).
--   B4  the stocking unit hold is lifted (9.1): the CHECK price_uom =
--       uom_primary on products, the price unit triggers on product_prices,
--       customer_contracts, pricing_rules and products, and their functions
--       all go, in the same migration that fixes the readers (the order,
--       invoice, counter, portal and integration price paths call
--       pricing.Resolve and carry its unit and pair from this release on).
--
-- Every step is idempotent and every backfill reads only columns earlier
-- steps made NOT NULL. The down file refuses, naming the first row it
-- cannot map back, while any line carries a tally outside a quote, any line
-- stocks a unit other than its uom, or any cart item holds a sub cent
-- price or a unit pair other than 1 and 1.

-- B0 ---------------------------------------------------------------------

-- The A1 collection over the five cycle 2 line tables: a value matching the
-- code rule that is not yet in the catalogue becomes a dealer unit and is
-- reported; a value that does not match aborts the migration naming the
-- table and the value, never a guessed mapping.
DO $$
DECLARE
    rec RECORD;
BEGIN
    FOR rec IN
        SELECT src, val, count(*) AS n FROM (
            SELECT 'order_lines.uom' AS src, upper(trim(uom)) AS val
            FROM order_lines WHERE uom IS NOT NULL
            UNION ALL
            SELECT 'order_lines.price_uom', upper(trim(price_uom))
            FROM order_lines WHERE price_uom IS NOT NULL
            UNION ALL
            SELECT 'invoice_lines.uom', upper(trim(uom))
            FROM invoice_lines WHERE uom IS NOT NULL
            UNION ALL
            SELECT 'invoice_lines.price_uom', upper(trim(price_uom))
            FROM invoice_lines WHERE price_uom IS NOT NULL
            UNION ALL
            SELECT 'credit_memo_lines.uom', upper(trim(uom))
            FROM credit_memo_lines WHERE uom IS NOT NULL
            UNION ALL
            SELECT 'credit_memo_lines.price_uom', upper(trim(price_uom))
            FROM credit_memo_lines WHERE price_uom IS NOT NULL
            UNION ALL
            SELECT 'pos_line_items.uom', upper(trim(uom))
            FROM pos_line_items WHERE uom IS NOT NULL
            UNION ALL
            SELECT 'pos_return_lines.uom', upper(trim(uom))
            FROM pos_return_lines WHERE uom IS NOT NULL
        ) c
        WHERE val NOT IN (SELECT code FROM units)
        GROUP BY src, val
    LOOP
        IF rec.val !~ '^[A-Z]{1,6}$' THEN
            RAISE EXCEPTION '% holds the unit value %, which does not match ^[A-Z]{1,6}$ and cannot enter the catalogue: correct it before upgrading (core migrate -report units lists it)', rec.src, rec.val;
        END IF;
        RAISE NOTICE 'new dealer unit % on % rows of %; review it in the unit catalogue', rec.val, rec.n, rec.src;
    END LOOP;
END $$;

INSERT INTO units (code, name, dimension, is_system)
SELECT DISTINCT src.val, 'Imported unit ' || src.val, 'COUNT', FALSE
FROM (
    SELECT upper(trim(uom)) AS val FROM order_lines WHERE uom IS NOT NULL
    UNION SELECT upper(trim(price_uom)) FROM order_lines WHERE price_uom IS NOT NULL
    UNION SELECT upper(trim(uom)) FROM invoice_lines WHERE uom IS NOT NULL
    UNION SELECT upper(trim(price_uom)) FROM invoice_lines WHERE price_uom IS NOT NULL
    UNION SELECT upper(trim(uom)) FROM credit_memo_lines WHERE uom IS NOT NULL
    UNION SELECT upper(trim(price_uom)) FROM credit_memo_lines WHERE price_uom IS NOT NULL
    UNION SELECT upper(trim(uom)) FROM pos_line_items WHERE uom IS NOT NULL
    UNION SELECT upper(trim(uom)) FROM pos_return_lines WHERE uom IS NOT NULL
) src
WHERE src.val NOT IN (SELECT code FROM units)
ON CONFLICT (code) DO NOTHING;

UPDATE order_lines SET uom = upper(trim(uom)) WHERE uom IS NOT NULL AND uom <> upper(trim(uom));
UPDATE order_lines SET price_uom = upper(trim(price_uom)) WHERE price_uom IS NOT NULL AND price_uom <> upper(trim(price_uom));
UPDATE invoice_lines SET uom = upper(trim(uom)) WHERE uom IS NOT NULL AND uom <> upper(trim(uom));
UPDATE invoice_lines SET price_uom = upper(trim(price_uom)) WHERE price_uom IS NOT NULL AND price_uom <> upper(trim(price_uom));
UPDATE credit_memo_lines SET uom = upper(trim(uom)) WHERE uom IS NOT NULL AND uom <> upper(trim(uom));
UPDATE credit_memo_lines SET price_uom = upper(trim(price_uom)) WHERE price_uom IS NOT NULL AND price_uom <> upper(trim(price_uom));
UPDATE pos_line_items SET uom = upper(trim(uom)) WHERE uom IS NOT NULL AND uom <> upper(trim(uom));
UPDATE pos_return_lines SET uom = upper(trim(uom)) WHERE uom <> upper(trim(uom));

-- The counter's uom columns join the catalogue's own type; every value they
-- hold is a catalogue row now.
ALTER TABLE pos_line_items ALTER COLUMN uom TYPE TEXT;
ALTER TABLE pos_return_lines ALTER COLUMN uom TYPE TEXT;

DO $$
BEGIN
    IF NOT EXISTS (SELECT 1 FROM pg_constraint WHERE conname = 'order_lines_uom_fkey') THEN
        ALTER TABLE order_lines ADD CONSTRAINT order_lines_uom_fkey
            FOREIGN KEY (uom) REFERENCES units(code);
    END IF;
    IF NOT EXISTS (SELECT 1 FROM pg_constraint WHERE conname = 'order_lines_price_uom_fkey') THEN
        ALTER TABLE order_lines ADD CONSTRAINT order_lines_price_uom_fkey
            FOREIGN KEY (price_uom) REFERENCES units(code);
    END IF;
    IF NOT EXISTS (SELECT 1 FROM pg_constraint WHERE conname = 'invoice_lines_uom_fkey') THEN
        ALTER TABLE invoice_lines ADD CONSTRAINT invoice_lines_uom_fkey
            FOREIGN KEY (uom) REFERENCES units(code);
    END IF;
    IF NOT EXISTS (SELECT 1 FROM pg_constraint WHERE conname = 'invoice_lines_price_uom_fkey') THEN
        ALTER TABLE invoice_lines ADD CONSTRAINT invoice_lines_price_uom_fkey
            FOREIGN KEY (price_uom) REFERENCES units(code);
    END IF;
    IF NOT EXISTS (SELECT 1 FROM pg_constraint WHERE conname = 'credit_memo_lines_uom_fkey') THEN
        ALTER TABLE credit_memo_lines ADD CONSTRAINT credit_memo_lines_uom_fkey
            FOREIGN KEY (uom) REFERENCES units(code);
    END IF;
    IF NOT EXISTS (SELECT 1 FROM pg_constraint WHERE conname = 'credit_memo_lines_price_uom_fkey') THEN
        ALTER TABLE credit_memo_lines ADD CONSTRAINT credit_memo_lines_price_uom_fkey
            FOREIGN KEY (price_uom) REFERENCES units(code);
    END IF;
    IF NOT EXISTS (SELECT 1 FROM pg_constraint WHERE conname = 'pos_line_items_uom_fkey') THEN
        ALTER TABLE pos_line_items ADD CONSTRAINT pos_line_items_uom_fkey
            FOREIGN KEY (uom) REFERENCES units(code);
    END IF;
    IF NOT EXISTS (SELECT 1 FROM pg_constraint WHERE conname = 'pos_return_lines_uom_fkey') THEN
        ALTER TABLE pos_return_lines ADD CONSTRAINT pos_return_lines_uom_fkey
            FOREIGN KEY (uom) REFERENCES units(code);
    END IF;
END $$;

-- B1 ---------------------------------------------------------------------

-- The stocking columns (3.4), the cross section a tallied line snapshots
-- (4.2), the engine's basis (7.5) and the resolved cost (7.5 and 8.4: the
-- cost COGS reads, per the stocking unit, beside the source it came from).
ALTER TABLE order_lines
    ADD COLUMN IF NOT EXISTS stock_uom TEXT NULL REFERENCES units(code),
    ADD COLUMN IF NOT EXISTS stock_quantity NUMERIC(12,4) NULL,
    ADD COLUMN IF NOT EXISTS board_thickness_in NUMERIC(7,4) NULL,
    ADD COLUMN IF NOT EXISTS board_width_in NUMERIC(7,4) NULL,
    ADD COLUMN IF NOT EXISTS price_basis TEXT NULL,
    ADD COLUMN IF NOT EXISTS cost_source TEXT NULL,
    ADD COLUMN IF NOT EXISTS unit_cost NUMERIC(12,4) NULL CHECK (unit_cost IS NULL OR unit_cost >= 0);

ALTER TABLE invoice_lines
    ADD COLUMN IF NOT EXISTS stock_uom TEXT NULL REFERENCES units(code),
    ADD COLUMN IF NOT EXISTS stock_quantity NUMERIC(12,4) NULL,
    ADD COLUMN IF NOT EXISTS board_thickness_in NUMERIC(7,4) NULL,
    ADD COLUMN IF NOT EXISTS board_width_in NUMERIC(7,4) NULL,
    ADD COLUMN IF NOT EXISTS price_basis TEXT NULL,
    ADD COLUMN IF NOT EXISTS cost_source TEXT NULL;

ALTER TABLE credit_memo_lines
    ADD COLUMN IF NOT EXISTS stock_uom TEXT NULL REFERENCES units(code),
    ADD COLUMN IF NOT EXISTS stock_quantity NUMERIC(12,4) NULL,
    ADD COLUMN IF NOT EXISTS board_thickness_in NUMERIC(7,4) NULL,
    ADD COLUMN IF NOT EXISTS board_width_in NUMERIC(7,4) NULL,
    ADD COLUMN IF NOT EXISTS price_basis TEXT NULL,
    ADD COLUMN IF NOT EXISTS cost_source TEXT NULL;

ALTER TABLE pos_line_items
    ADD COLUMN IF NOT EXISTS stock_uom TEXT NULL REFERENCES units(code),
    ADD COLUMN IF NOT EXISTS stock_quantity NUMERIC(12,4) NULL,
    ADD COLUMN IF NOT EXISTS board_thickness_in NUMERIC(7,4) NULL,
    ADD COLUMN IF NOT EXISTS board_width_in NUMERIC(7,4) NULL,
    ADD COLUMN IF NOT EXISTS price_basis TEXT NULL,
    ADD COLUMN IF NOT EXISTS cost_source TEXT NULL,
    ADD COLUMN IF NOT EXISTS unit_cost NUMERIC(12,4) NULL CHECK (unit_cost IS NULL OR unit_cost >= 0);

ALTER TABLE pos_return_lines
    ADD COLUMN IF NOT EXISTS stock_uom TEXT NULL REFERENCES units(code),
    ADD COLUMN IF NOT EXISTS stock_quantity NUMERIC(12,4) NULL,
    ADD COLUMN IF NOT EXISTS board_thickness_in NUMERIC(7,4) NULL,
    ADD COLUMN IF NOT EXISTS board_width_in NUMERIC(7,4) NULL;

-- The backfill: a line with a product stocks what it sold. ADR 0005 held
-- every stocked line to its product's stocking unit, so the quantity needs
-- no conversion; the EA / 1 to 1 default of the wire is simply the line's
-- own quantity and unit carried into the stocking columns.
UPDATE order_lines SET stock_uom = uom, stock_quantity = quantity WHERE product_id IS NOT NULL;
UPDATE invoice_lines SET stock_uom = uom, stock_quantity = quantity WHERE product_id IS NOT NULL;
UPDATE credit_memo_lines SET stock_uom = uom, stock_quantity = quantity WHERE product_id IS NOT NULL;
UPDATE pos_line_items SET stock_uom = uom, stock_quantity = quantity WHERE product_id IS NOT NULL;
UPDATE pos_return_lines SET stock_uom = uom, stock_quantity = quantity WHERE product_id IS NOT NULL;

-- The line shape: the stocking columns are set exactly when the line names
-- a product. Charge and text lines carry no product and stock nothing. The
-- check holds the three tables every line of which is written by the unit
-- resolution or a bill: the order, the counter and the invoice. The credit
-- memo is out of its reach: its free lines name products without resolving
-- a set, and its credited lines carry what their invoice line carried.
DO $$
BEGIN
    IF NOT EXISTS (SELECT 1 FROM pg_constraint WHERE conname = 'order_lines_stock_follows_product') THEN
        ALTER TABLE order_lines ADD CONSTRAINT order_lines_stock_follows_product
            CHECK ((product_id IS NULL) = (stock_uom IS NULL AND stock_quantity IS NULL));
    END IF;
    IF NOT EXISTS (SELECT 1 FROM pg_constraint WHERE conname = 'invoice_lines_stock_follows_product') THEN
        ALTER TABLE invoice_lines ADD CONSTRAINT invoice_lines_stock_follows_product
            CHECK ((product_id IS NULL) = (stock_uom IS NULL AND stock_quantity IS NULL));
    END IF;
    IF NOT EXISTS (SELECT 1 FROM pg_constraint WHERE conname = 'pos_line_items_stock_follows_product') THEN
        ALTER TABLE pos_line_items ADD CONSTRAINT pos_line_items_stock_follows_product
            CHECK ((product_id IS NULL) = (stock_uom IS NULL AND stock_quantity IS NULL));
    END IF;
END $$;

-- B2 ---------------------------------------------------------------------

-- Tallies on every document: the four other line columns, the five column
-- exactly one check, and a per length unique index each.
ALTER TABLE line_tally_rows
    ALTER COLUMN quote_line_id DROP NOT NULL,
    ADD COLUMN IF NOT EXISTS order_line_id UUID NULL REFERENCES order_lines(id) ON DELETE CASCADE,
    ADD COLUMN IF NOT EXISTS invoice_line_id UUID NULL REFERENCES invoice_lines(id) ON DELETE CASCADE,
    ADD COLUMN IF NOT EXISTS credit_memo_line_id UUID NULL REFERENCES credit_memo_lines(id) ON DELETE CASCADE,
    ADD COLUMN IF NOT EXISTS pos_line_item_id UUID NULL REFERENCES pos_line_items(id) ON DELETE CASCADE;

DO $$
BEGIN
    IF NOT EXISTS (SELECT 1 FROM pg_constraint WHERE conname = 'line_tally_rows_one_line') THEN
        ALTER TABLE line_tally_rows ADD CONSTRAINT line_tally_rows_one_line
            CHECK (num_nonnulls(quote_line_id, order_line_id, invoice_line_id, credit_memo_line_id, pos_line_item_id) = 1);
    END IF;
END $$;

CREATE UNIQUE INDEX IF NOT EXISTS line_tally_rows_order_line_length
    ON line_tally_rows (order_line_id, length_ft) WHERE order_line_id IS NOT NULL;
CREATE UNIQUE INDEX IF NOT EXISTS line_tally_rows_invoice_line_length
    ON line_tally_rows (invoice_line_id, length_ft) WHERE invoice_line_id IS NOT NULL;
CREATE UNIQUE INDEX IF NOT EXISTS line_tally_rows_credit_memo_line_length
    ON line_tally_rows (credit_memo_line_id, length_ft) WHERE credit_memo_line_id IS NOT NULL;
CREATE UNIQUE INDEX IF NOT EXISTS line_tally_rows_pos_line_item_length
    ON line_tally_rows (pos_line_item_id, length_ft) WHERE pos_line_item_id IS NOT NULL;

-- B3 ---------------------------------------------------------------------

-- The portal cart item takes the line shape's unit fields (7.6). The price
-- widens in place: every stored value has two decimals, so the widening
-- loses nothing. The pair is backfilled to 1 and 1 because every cart item
-- today prices per the stocking unit.
ALTER TABLE portal_cart_items ALTER COLUMN unit_price TYPE NUMERIC(12,4);

ALTER TABLE portal_cart_items
    ADD COLUMN IF NOT EXISTS uom TEXT NULL REFERENCES units(code),
    ADD COLUMN IF NOT EXISTS price_uom TEXT NULL REFERENCES units(code),
    ADD COLUMN IF NOT EXISTS uom_qty NUMERIC(12,4) NULL,
    ADD COLUMN IF NOT EXISTS price_uom_qty NUMERIC(12,4) NULL;

UPDATE portal_cart_items ci
SET uom = p.uom_primary,
    price_uom = p.uom_primary,
    uom_qty = 1,
    price_uom_qty = 1
FROM products p
WHERE ci.product_id = p.id
  AND (ci.uom IS NULL OR ci.price_uom IS NULL OR ci.uom_qty IS NULL OR ci.price_uom_qty IS NULL);

DO $$
BEGIN
    IF NOT EXISTS (SELECT 1 FROM pg_constraint WHERE conname = 'portal_cart_items_pair_positive') THEN
        ALTER TABLE portal_cart_items ADD CONSTRAINT portal_cart_items_pair_positive
            CHECK (uom_qty IS NULL AND price_uom_qty IS NULL OR uom_qty > 0 AND price_uom_qty > 0);
    END IF;
END $$;

-- B4 ---------------------------------------------------------------------

-- The stocking unit hold is lifted (ADR 0006 section 9.1): the readers this
-- release ships (order, invoice, counter, portal and the integration price
-- read, each on pricing.Resolve and carrying the answer's unit and pair)
-- are what made every price safe in its own unit. The CHECK of A2 and the
-- triggers of P5 go.
DROP TRIGGER IF EXISTS product_prices_price_uom_held ON product_prices;
DROP TRIGGER IF EXISTS customer_contracts_price_uom_held ON customer_contracts;
DROP TRIGGER IF EXISTS pricing_rules_price_uom_held ON pricing_rules;
DROP TRIGGER IF EXISTS products_price_unit_held ON products;
DROP FUNCTION IF EXISTS pricing_price_uom_held();
DROP FUNCTION IF EXISTS products_price_unit_held();

ALTER TABLE products DROP CONSTRAINT IF EXISTS products_price_uom_is_stocking;
