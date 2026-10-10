-- SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
-- SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

-- 099: units_catalogue_and_sets (item C3-2A-units, ADR 0006 sections 2, 3
-- and 4 and its migration steps A1, A2 and A3).
--
--   A1  the catalogue. `units` replaces the closed `uom_type` enum: one row
--       per unit the dealer uses, seeded with the standard sizes of section
--       2.2 (each canonical), locked on the seeded rows and extended by the
--       dealer. Every distinct unit value stored in products.uom_primary,
--       quote_lines.uom and quote_lines.price_uom is collected, normalised
--       with upper(trim()), inserted as a dealer unit (COUNT, no standard
--       size) and reported when it is a new valid code, and aborts the
--       migration naming the table and the value when it is not (never a
--       guessed mapping). The three columns become TEXT with foreign keys
--       to units(code); `core migrate -report units` is the read only pre
--       flight report an operator runs first. The counter's line tables
--       are cycle 2's and are left to C3-2B (B0); edi_catalog_entries.uom
--       holds vendors' codes and gets no FK (C4-0 maps vendor units).
--   A2  unit sets. The board measure columns and random_length on
--       products; `product_units` with one row per product (its stocking
--       unit, (1, 1), sell, purchase and price all true, exactly what the
--       backfill gives every existing product and what the row default
--       triggers give every new one, however it is inserted); the three
--       default columns sale_uom, price_uom and purchase_uom, NOT NULL and
--       backfilled to uom_primary, with DEFERRABLE INITIALLY DEFERRED
--       composite foreign keys; the deferred stocking row constraint
--       trigger (the uom_primary row exists and is (1, 1), it carries
--       price, a random length product is stocked in LF); the CHECK
--       price_uom = uom_primary of the stocking unit hold (dropped by
--       C3-2B); and the deferred price_unit_held trigger refusing a change
--       of uom_primary while base_price is nonzero or a contract or a
--       fixed price rule names the product, for raw writes. No unit set
--       row is made from quote lines: R1-15 checked a line's pair only for
--       being positive, so one careless line would become the product's
--       conversion. Instead product_unit_candidates records each distinct
--       (product, unit, pair) found on quote lines whose other unit is the
--       stocking unit, oriented to the stocking unit and canonical, and
--       the dealer confirms a candidate through the unit set PUT.
--   A3  quote tallies. `line_tally_rows` with quote_line_id only (C3-2B
--       adds the other four line columns and widens the CHECK); the cross
--       section and stock columns on quote_lines; and the stock backfill:
--       for lines with a product whose uom is in the product's set,
--       stock_uom = uom_primary and stock_quantity = quantity converted,
--       exact at scale 4. A line that does not convert exactly, or whose
--       unit never entered the set, keeps both null and is reported, and
--       its next edit meets section 3.4's refusal.
--
-- Every step is idempotent and every backfill reads only columns earlier
-- steps made NOT NULL.

-- A1 ---------------------------------------------------------------------

CREATE TABLE IF NOT EXISTS units (
    code         TEXT PRIMARY KEY CHECK (code ~ '^[A-Z]{1,6}$'),
    name         TEXT NOT NULL CHECK (char_length(name) BETWEEN 1 AND 80),
    dimension    TEXT NOT NULL CHECK (dimension IN ('COUNT','LENGTH','AREA','VOLUME','WEIGHT','BOARD_MEASURE')),
    std_unit_qty NUMERIC(12,4) NULL CHECK (std_unit_qty IS NULL OR std_unit_qty > 0),
    std_ref_qty  NUMERIC(12,4) NULL CHECK (std_ref_qty IS NULL OR std_ref_qty > 0),
    CHECK ((std_unit_qty IS NULL) = (std_ref_qty IS NULL)),
    is_system    BOOLEAN NOT NULL DEFAULT FALSE,
    is_active    BOOLEAN NOT NULL DEFAULT TRUE,
    revision     BIGINT NOT NULL DEFAULT 1,
    created_at   TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at   TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

-- The seed of section 2.2, inserted only where absent, every standard size
-- in the canonical form of R2 (the pair (std_unit_qty, std_ref_qty)).
INSERT INTO units (code, name, dimension, std_unit_qty, std_ref_qty, is_system) VALUES
    ('EA',     'Each',                'COUNT',         1,     1, TRUE),
    ('PCS',    'Pieces',              'COUNT',         1,     1, TRUE),
    ('PAIR',   'Pair',                'COUNT',         1,     2, TRUE),
    ('DOZ',    'Dozen',               'COUNT',         1,    12, TRUE),
    ('C',      'Hundred',             'COUNT',         1,   100, TRUE),
    ('M',      'Thousand',            'COUNT',         1,  1000, TRUE),
    ('SET',    'Set',                 'COUNT',    NULL,  NULL, TRUE),
    ('BOX',    'Box',                 'COUNT',    NULL,  NULL, TRUE),
    ('CTN',    'Carton',              'COUNT',    NULL,  NULL, TRUE),
    ('BAG',    'Bag',                 'COUNT',    NULL,  NULL, TRUE),
    ('BUNDLE', 'Bundle',              'COUNT',    NULL,  NULL, TRUE),
    ('RL',     'Roll',                'COUNT',    NULL,  NULL, TRUE),
    ('LF',     'Linear foot',         'LENGTH',        1,     1, TRUE),
    ('SF',     'Square foot',         'AREA',          1,     1, TRUE),
    ('SQ',     'Square (roofing)',    'AREA',          1,   100, TRUE),
    ('CF',     'Cubic foot',          'VOLUME',        1,     1, TRUE),
    ('CY',     'Cubic yard',          'VOLUME',        1,    27, TRUE),
    ('GAL',    'Gallon (US)',         'VOLUME',      576,    77, TRUE),
    ('LBS',    'Pound',               'WEIGHT',        1,     1, TRUE),
    ('CWT',    'Hundredweight',       'WEIGHT',        1,   100, TRUE),
    ('TON',    'Short ton',           'WEIGHT',        1,  2000, TRUE),
    ('BF',     'Board foot',          'BOARD_MEASURE', 1,     1, TRUE),
    ('MBF',    'Thousand board feet', 'BOARD_MEASURE', 1,  1000, TRUE)
ON CONFLICT (code) DO NOTHING;

-- Collect every distinct unit value the three columns hold, normalised. A
-- value matching the code rule that is not yet in the catalogue becomes a
-- dealer unit and is reported for review; a value that does not match
-- aborts the migration naming the table and the value, never a guessed
-- mapping. The pre flight report (`core migrate -report units`) lists both
-- before an operator upgrades.
DO $$
DECLARE
    rec RECORD;
BEGIN
    FOR rec IN
        SELECT src, val, count(*) AS n FROM (
            SELECT 'products.uom_primary' AS src, upper(trim(uom_primary::text)) AS val
            FROM products
            UNION ALL
            SELECT 'quote_lines.uom', upper(trim(uom::text))
            FROM quote_lines
            UNION ALL
            SELECT 'quote_lines.price_uom', upper(trim(price_uom))
            FROM quote_lines WHERE price_uom IS NOT NULL
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
    SELECT upper(trim(uom_primary::text)) AS val FROM products
    UNION SELECT upper(trim(uom::text)) FROM quote_lines
    UNION SELECT upper(trim(price_uom)) FROM quote_lines WHERE price_uom IS NOT NULL
) src
WHERE src.val NOT IN (SELECT code FROM units)
ON CONFLICT (code) DO NOTHING;

-- Normalise the stored values, then move the two enum columns to TEXT and
-- drop the enum. Every code the columns hold is a catalogue row now.
UPDATE products SET uom_primary = upper(trim(uom_primary::text))::uom_type
WHERE uom_primary::text <> upper(trim(uom_primary::text));
UPDATE quote_lines SET uom = upper(trim(uom::text))::uom_type
WHERE uom::text <> upper(trim(uom::text));
UPDATE quote_lines SET price_uom = upper(trim(price_uom))
WHERE price_uom IS NOT NULL AND price_uom <> upper(trim(price_uom));

ALTER TABLE products ALTER COLUMN uom_primary TYPE TEXT;
ALTER TABLE quote_lines ALTER COLUMN uom TYPE TEXT;
DROP TYPE IF EXISTS uom_type;

ALTER TABLE products ADD CONSTRAINT products_uom_primary_fkey
    FOREIGN KEY (uom_primary) REFERENCES units(code);
ALTER TABLE quote_lines ADD CONSTRAINT quote_lines_uom_fkey
    FOREIGN KEY (uom) REFERENCES units(code);
ALTER TABLE quote_lines ADD CONSTRAINT quote_lines_price_uom_fkey
    FOREIGN KEY (price_uom) REFERENCES units(code);

-- A2 ---------------------------------------------------------------------

-- The board measure columns (a 2x4 is 2 and 4; a 2x4x8 is 8 long; random
-- length leaves the length null and is sold by tally).
ALTER TABLE products
    ADD COLUMN IF NOT EXISTS board_thickness_in NUMERIC(7,4) NULL CHECK (board_thickness_in IS NULL OR board_thickness_in > 0),
    ADD COLUMN IF NOT EXISTS board_width_in NUMERIC(7,4) NULL CHECK (board_width_in IS NULL OR board_width_in > 0),
    ADD COLUMN IF NOT EXISTS board_length_ft NUMERIC(8,4) NULL CHECK (board_length_ft IS NULL OR board_length_ft > 0),
    ADD COLUMN IF NOT EXISTS random_length BOOLEAN NOT NULL DEFAULT FALSE;

DO $$
BEGIN
    -- A cross section is both halves or neither.
    IF NOT EXISTS (SELECT 1 FROM pg_constraint WHERE conname = 'products_cross_section_both_or_neither') THEN
        ALTER TABLE products ADD CONSTRAINT products_cross_section_both_or_neither
            CHECK ((board_thickness_in IS NULL) = (board_width_in IS NULL));
    END IF;
    -- A random length product has no fixed length; its stocking unit being
    -- LF is one of the stocking row trigger's three invariants, checked
    -- deferred so a write may settle the two columns together.
    IF NOT EXISTS (SELECT 1 FROM pg_constraint WHERE conname = 'products_random_length_no_fixed_length') THEN
        ALTER TABLE products ADD CONSTRAINT products_random_length_no_fixed_length
            CHECK (NOT random_length OR board_length_ft IS NULL);
    END IF;
END $$;

-- The product's unit set: unit_qty of the row's unit is stock_qty of the
-- product's stocking unit, canonical (R2), with the use flags.
CREATE TABLE IF NOT EXISTS product_units (
    product_id UUID NOT NULL REFERENCES products(id) ON DELETE CASCADE,
    uom        TEXT NOT NULL REFERENCES units(code),
    unit_qty   NUMERIC(12,4) NOT NULL CHECK (unit_qty > 0),
    stock_qty  NUMERIC(12,4) NOT NULL CHECK (stock_qty > 0),
    sell       BOOLEAN NOT NULL DEFAULT TRUE,
    purchase   BOOLEAN NOT NULL DEFAULT TRUE,
    price      BOOLEAN NOT NULL DEFAULT TRUE,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    PRIMARY KEY (product_id, uom)
);

-- One row per product: its stocking unit, (1, 1), sell, purchase and price
-- all true. No row is made from quote lines (product_unit_candidates
-- below holds what they said, for the dealer to confirm through the PUT).
INSERT INTO product_units (product_id, uom, unit_qty, stock_qty, sell, purchase, price)
SELECT id, uom_primary, 1, 1, TRUE, TRUE, TRUE FROM products
ON CONFLICT (product_id, uom) DO NOTHING;

-- The three default columns, NOT NULL and backfilled to the stocking unit.
-- A default is a row of the set with the matching flag; the composite
-- foreign keys are DEFERRABLE INITIALLY DEFERRED because the product row
-- and its set rows reference each other inside one transaction.
ALTER TABLE products
    ADD COLUMN IF NOT EXISTS sale_uom TEXT,
    ADD COLUMN IF NOT EXISTS price_uom TEXT,
    ADD COLUMN IF NOT EXISTS purchase_uom TEXT;
UPDATE products SET sale_uom = uom_primary WHERE sale_uom IS NULL;
UPDATE products SET price_uom = uom_primary WHERE price_uom IS NULL;
UPDATE products SET purchase_uom = uom_primary WHERE purchase_uom IS NULL;
ALTER TABLE products
    ALTER COLUMN sale_uom SET NOT NULL,
    ALTER COLUMN price_uom SET NOT NULL,
    ALTER COLUMN purchase_uom SET NOT NULL;

ALTER TABLE products ADD CONSTRAINT products_sale_uom_fkey
    FOREIGN KEY (id, sale_uom) REFERENCES product_units(product_id, uom) DEFERRABLE INITIALLY DEFERRED;
ALTER TABLE products ADD CONSTRAINT products_price_uom_fkey
    FOREIGN KEY (id, price_uom) REFERENCES product_units(product_id, uom) DEFERRABLE INITIALLY DEFERRED;
ALTER TABLE products ADD CONSTRAINT products_purchase_uom_fkey
    FOREIGN KEY (id, purchase_uom) REFERENCES product_units(product_id, uom) DEFERRABLE INITIALLY DEFERRED;

-- The stocking unit hold's CHECK: every price is in the stocking unit
-- until C3-2B (ADR 0006 3.2 and 9.1).
DO $$
BEGIN
    IF NOT EXISTS (SELECT 1 FROM pg_constraint WHERE conname = 'products_price_uom_is_stocking') THEN
        ALTER TABLE products ADD CONSTRAINT products_price_uom_is_stocking
            CHECK (price_uom = uom_primary);
    END IF;
END $$;

-- Raw writers of products (the seed, deployments' SQL) rely on defaults
-- the way the recipe's step 3 gives a number column its DEFAULT: the
-- default unit columns follow the stocking unit, and the stocking row is
-- created beside the product, exactly what the backfill gave every
-- existing product. Both are triggers because the value is row dependent.
-- The unit set write names all four unit columns itself, so it sets the
-- transaction local gable.unit_set_write flag before its UPDATE: a set
-- that keeps selling in the old stocking unit keeps its sale_uom (and
-- purchase_uom). The dragging branch below serves raw writers only.
CREATE OR REPLACE FUNCTION products_unit_defaults() RETURNS trigger AS $$
BEGIN
    IF TG_OP = 'INSERT' THEN
        NEW.sale_uom := COALESCE(NEW.sale_uom, NEW.uom_primary);
        NEW.price_uom := COALESCE(NEW.price_uom, NEW.uom_primary);
        NEW.purchase_uom := COALESCE(NEW.purchase_uom, NEW.uom_primary);
    ELSIF NEW.uom_primary IS DISTINCT FROM OLD.uom_primary
          AND current_setting('gable.unit_set_write', true) IS DISTINCT FROM 'on' THEN
        -- A raw change of the stocking unit carries the defaults with it
        -- when the caller did not name new ones.
        IF NEW.sale_uom IS NOT DISTINCT FROM OLD.uom_primary THEN NEW.sale_uom := NEW.uom_primary; END IF;
        IF NEW.price_uom IS NOT DISTINCT FROM OLD.uom_primary THEN NEW.price_uom := NEW.uom_primary; END IF;
        IF NEW.purchase_uom IS NOT DISTINCT FROM OLD.uom_primary THEN NEW.purchase_uom := NEW.uom_primary; END IF;
    END IF;
    RETURN NEW;
END $$ LANGUAGE plpgsql;

DROP TRIGGER IF EXISTS products_unit_defaults ON products;
CREATE TRIGGER products_unit_defaults
    BEFORE INSERT OR UPDATE OF uom_primary ON products
    FOR EACH ROW EXECUTE FUNCTION products_unit_defaults();

CREATE OR REPLACE FUNCTION products_stocking_row_default() RETURNS trigger AS $$
BEGIN
    INSERT INTO product_units (product_id, uom, unit_qty, stock_qty, sell, purchase, price)
    VALUES (NEW.id, NEW.uom_primary, 1, 1, TRUE, TRUE, TRUE)
    ON CONFLICT (product_id, uom) DO NOTHING;
    RETURN NEW;
END $$ LANGUAGE plpgsql;

DROP TRIGGER IF EXISTS products_stocking_row_default ON products;
CREATE TRIGGER products_stocking_row_default
    AFTER INSERT OR UPDATE OF uom_primary ON products
    FOR EACH ROW EXECUTE FUNCTION products_stocking_row_default();

-- The stocking row invariant (ADR 0006 3.1), deferred because the product
-- row and its set rows reference each other inside one transaction. It
-- holds three things: the uom_primary row exists and is (1, 1); that row
-- has price set (a cost derived price answers in the stocking unit and
-- must be a price unit of the product); and a random_length product's
-- uom_primary is LF.
CREATE OR REPLACE FUNCTION product_stocking_row_invariant() RETURNS trigger AS $$
DECLARE
    pid UUID;
    suom TEXT;
    uq NUMERIC;
    sq NUMERIC;
    hasprice BOOLEAN;
    rnd BOOLEAN;
BEGIN
    IF TG_TABLE_NAME = 'product_units' THEN
        pid := COALESCE(NEW.product_id, OLD.product_id);
    ELSE
        pid := NEW.id;
    END IF;
    SELECT uom_primary, random_length INTO suom, rnd FROM products WHERE id = pid;
    IF NOT FOUND THEN
        RETURN NULL; -- the product is going away; its rows cascade
    END IF;
    SELECT unit_qty, stock_qty, price INTO uq, sq, hasprice
    FROM product_units WHERE product_id = pid AND uom = suom;
    IF NOT FOUND THEN
        RAISE EXCEPTION 'the stocking unit % of product % has no row in its unit set', suom, pid;
    END IF;
    IF uq <> 1 OR sq <> 1 THEN
        RAISE EXCEPTION 'the stocking unit row of product % must be the pair 1 and 1, not % and %', pid, uq, sq;
    END IF;
    IF NOT hasprice THEN
        RAISE EXCEPTION 'the stocking unit row of product % must allow prices: a cost derived price answers in the stocking unit', pid;
    END IF;
    IF rnd AND suom <> 'LF' THEN
        RAISE EXCEPTION 'a random length product (%) is stocked in LF, not %', pid, suom;
    END IF;
    RETURN NULL;
END $$ LANGUAGE plpgsql;

DROP TRIGGER IF EXISTS product_units_stocking_row ON product_units;
CREATE CONSTRAINT TRIGGER product_units_stocking_row
    AFTER INSERT OR UPDATE OR DELETE ON product_units
    DEFERRABLE INITIALLY DEFERRED
    FOR EACH ROW EXECUTE FUNCTION product_stocking_row_invariant();

DROP TRIGGER IF EXISTS products_stocking_row ON products;
CREATE CONSTRAINT TRIGGER products_stocking_row
    AFTER UPDATE OF uom_primary, random_length ON products
    DEFERRABLE INITIALLY DEFERRED
    FOR EACH ROW EXECUTE FUNCTION product_stocking_row_invariant();

-- The stocking unit hold for raw writes (3.2's price_unit_held): the
-- stocking unit cannot move under a price. The unit set service refuses
-- first with the 409; this catches a raw UPDATE.
CREATE OR REPLACE FUNCTION products_price_unit_held() RETURNS trigger AS $$
BEGIN
    IF NEW.uom_primary IS DISTINCT FROM OLD.uom_primary THEN
        IF COALESCE(OLD.base_price, 0) <> 0 THEN
            RAISE EXCEPTION 'price_unit_held: product % has a nonzero base price in %; zero the base price before changing the stocking unit', OLD.id, OLD.uom_primary;
        END IF;
        IF EXISTS (SELECT 1 FROM customer_contracts c WHERE c.product_id = OLD.id) THEN
            RAISE EXCEPTION 'price_unit_held: product % has a customer contract priced per %; remove the contract before changing the stocking unit', OLD.id, OLD.uom_primary;
        END IF;
        IF EXISTS (SELECT 1 FROM pricing_rules r WHERE r.product_id = OLD.id AND r.fixed_price IS NOT NULL) THEN
            RAISE EXCEPTION 'price_unit_held: product % has a pricing rule with a fixed price per %; remove the rule before changing the stocking unit', OLD.id, OLD.uom_primary;
        END IF;
    END IF;
    RETURN NULL;
END $$ LANGUAGE plpgsql;

DROP TRIGGER IF EXISTS products_price_unit_held ON products;
CREATE CONSTRAINT TRIGGER products_price_unit_held
    AFTER UPDATE OF uom_primary ON products
    DEFERRABLE INITIALLY DEFERRED
    FOR EACH ROW EXECUTE FUNCTION products_price_unit_held();

-- What the quote lines said: one row per distinct (product, unit, pair)
-- whose other unit is the stocking unit, oriented to the stocking unit and
-- canonical. The dealer confirms a candidate through the unit set PUT,
-- which applies every check a sent pair meets; nothing prices or stocks
-- from this table.
CREATE TABLE IF NOT EXISTS product_unit_candidates (
    product_id    UUID NOT NULL REFERENCES products(id) ON DELETE CASCADE,
    uom           TEXT NOT NULL REFERENCES units(code),
    unit_qty      NUMERIC(12,4) NOT NULL CHECK (unit_qty > 0),
    stock_qty     NUMERIC(12,4) NOT NULL CHECK (stock_qty > 0),
    line_count    BIGINT NOT NULL,
    latest_line_id UUID,
    created_at    TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    PRIMARY KEY (product_id, uom, unit_qty, stock_qty)
);

INSERT INTO product_unit_candidates (product_id, uom, unit_qty, stock_qty, line_count, latest_line_id)
WITH line_pairs AS (
    -- A line whose price unit is the product's stocking unit names the
    -- conversion between its uom and it: uom_qty of the line's unit is
    -- price_uom_qty of the stocking unit.
    SELECT ql.id AS line_id, ql.created_at AS line_at, ql.product_id, ql.uom,
           (ql.uom_qty * 10000)::numeric AS a, (ql.price_uom_qty * 10000)::numeric AS b
    FROM quote_lines ql
    JOIN products p ON p.id = ql.product_id
    WHERE ql.product_id IS NOT NULL
      AND ql.price_uom = p.uom_primary
      AND ql.uom <> p.uom_primary
      AND ql.uom_qty > 0 AND ql.price_uom_qty > 0
),
canonical AS (
    -- R2 over each line's ratio: rule 1 and rule 2 when a side is exact at
    -- scale 4 (the 1 on the side that leaves the other at least 1), else
    -- rule 3's integer terms at the largest of 1, 0.1, 0.01, 0.001 and
    -- 0.0001 that fits the NUMERIC(12,4) bound (99999999.9999, the scale 4
    -- integer 999999999999). A ratio past the bound in every form cannot
    -- have come from these columns; the cast to numeric(12,4) refuses it.
    SELECT line_id, line_at, product_id, uom,
        (CASE
            WHEN a * 10000 % b = 0 AND b * 10000 % a = 0 AND a >= b AND a * 10000 / b <= 999999999999
                THEN (a * 10000 / b) / 10000.0
            WHEN a * 10000 % b = 0 AND b * 10000 % a = 0 AND a < b AND b * 10000 / a <= 999999999999
                THEN 1
            WHEN a * 10000 % b = 0 AND a * 10000 / b <= 999999999999
                THEN (a * 10000 / b) / 10000.0
            WHEN b * 10000 % a = 0 AND b * 10000 / a <= 999999999999
                THEN 1
            WHEN p * 10000 <= 999999999999 AND q * 10000 <= 999999999999
                THEN p
            WHEN p * 1000 <= 999999999999 AND q * 1000 <= 999999999999
                THEN p / 10.0
            WHEN p * 100 <= 999999999999 AND q * 100 <= 999999999999
                THEN p / 100.0
            WHEN p * 10 <= 999999999999 AND q * 10 <= 999999999999
                THEN p / 1000.0
            ELSE p / 10000.0
        END)::numeric(12,4) AS unit_side,
        (CASE
            WHEN a * 10000 % b = 0 AND b * 10000 % a = 0 AND a >= b AND a * 10000 / b <= 999999999999
                THEN 1
            WHEN a * 10000 % b = 0 AND b * 10000 % a = 0 AND a < b AND b * 10000 / a <= 999999999999
                THEN (b * 10000 / a) / 10000.0
            WHEN a * 10000 % b = 0 AND a * 10000 / b <= 999999999999
                THEN 1
            WHEN b * 10000 % a = 0 AND b * 10000 / a <= 999999999999
                THEN (b * 10000 / a) / 10000.0
            WHEN p * 10000 <= 999999999999 AND q * 10000 <= 999999999999
                THEN q
            WHEN p * 1000 <= 999999999999 AND q * 1000 <= 999999999999
                THEN q / 10.0
            WHEN p * 100 <= 999999999999 AND q * 100 <= 999999999999
                THEN q / 100.0
            WHEN p * 10 <= 999999999999 AND q * 10 <= 999999999999
                THEN q / 1000.0
            ELSE q / 10000.0
        END)::numeric(12,4) AS stock_side
    FROM line_pairs,
         LATERAL (SELECT gcd(a, b) AS g) gg,
         LATERAL (SELECT a / g AS p, b / g AS q) rr
)
SELECT product_id, uom, unit_side, stock_side, count(*),
       (array_agg(line_id ORDER BY line_at DESC, line_id DESC))[1] AS latest_line_id
FROM canonical
GROUP BY product_id, uom, unit_side, stock_side
ON CONFLICT (product_id, uom, unit_qty, stock_qty) DO NOTHING;

DO $$
DECLARE
    n BIGINT;
BEGIN
    SELECT count(*) INTO n FROM product_unit_candidates;
    RAISE NOTICE 'product_unit_candidates: % distinct (product, unit, pair) conversions found on quote lines; the dealer confirms each through the unit set PUT', n;
END $$;

-- A3 ---------------------------------------------------------------------

-- The tally rows table (C3-2A-units creates it with quote_line_id only;
-- C3-2B adds the other four line columns and widens the CHECK).
CREATE TABLE IF NOT EXISTS line_tally_rows (
    id            UUID PRIMARY KEY DEFAULT uuid_generate_v4(),
    quote_line_id UUID NOT NULL REFERENCES quote_lines(id) ON DELETE CASCADE,
    position      INTEGER NOT NULL,
    pieces        INTEGER NOT NULL CHECK (pieces > 0 AND pieces <= 1000000),
    length_ft     NUMERIC(12,4) NOT NULL CHECK (length_ft > 0)
);

CREATE UNIQUE INDEX IF NOT EXISTS line_tally_rows_quote_line_length
    ON line_tally_rows (quote_line_id, length_ft);

ALTER TABLE quote_lines
    ADD COLUMN IF NOT EXISTS board_thickness_in NUMERIC(7,4) NULL,
    ADD COLUMN IF NOT EXISTS board_width_in NUMERIC(7,4) NULL,
    ADD COLUMN IF NOT EXISTS stock_uom TEXT NULL REFERENCES units(code),
    ADD COLUMN IF NOT EXISTS stock_quantity NUMERIC(12,4) NULL;

-- The stock backfill: for lines with a product whose uom is in the
-- product's set, stock_uom is the stocking unit and stock_quantity is the
-- quantity converted, exact at scale 4 ((quantity x stock_qty) must be a
-- whole multiple of unit_qty, in scale 4 integers).
UPDATE quote_lines ql
SET stock_uom = p.uom_primary,
    stock_quantity = (ql.quantity * pu.stock_qty / pu.unit_qty)::numeric(12,4)
FROM products p
JOIN product_units pu ON pu.product_id = p.id AND pu.uom = p.uom_primary
WHERE ql.product_id = p.id
  AND ql.uom = pu.uom
  AND (ql.quantity * 10000 * pu.stock_qty * 10000) % (pu.unit_qty * 10000) = 0;

DO $$
DECLARE
    n BIGINT;
BEGIN
    SELECT count(*) INTO n FROM quote_lines ql
    JOIN products p ON p.id = ql.product_id
    WHERE ql.product_id IS NOT NULL AND ql.stock_uom IS NULL;
    IF n > 0 THEN
        RAISE NOTICE 'quote tallies: % product quote lines kept a null stocking quantity (their unit is outside the product''s set, or the conversion is not exact at scale 4); their next edit meets the stock conversion refusal', n;
    END IF;
END $$;
