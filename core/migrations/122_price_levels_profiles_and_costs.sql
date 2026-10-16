-- SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
-- SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

-- 122: price levels, profiles and costs (C3-2A-pricing; ADR 0006 sections
-- 5 and 8, steps P1 to P6).
--
-- P1 rebuilds price_levels: a unique code backfilled from the name, a basis,
-- the exact percent of the old multiplier, a display position, revision;
-- then the multiplier is dropped. A negative multiplier has no meaning as a
-- price and aborts the migration naming the level; the units pre flight
-- report (`core migrate -report units`) names such levels first.
-- P2 creates the four fresh levels the tiers and the default land on and
-- records them in system_settings by id, never by name; the default price
-- profile is created there with them and price_profile_default_id() reads
-- the setting.
-- P3 creates one price profile per distinct (price_level_id, tier)
-- combination the customers hold, mapped in price_profile_backfill so every
-- customer's price after the migration is the price before it to the ten
-- thousandth, then drops customers.tier, customers.price_level_id and the
-- customer_tier type.
-- P4 moves the tier category rules: SILVER, GOLD and PLATINUM rules become
-- LEVEL rules on the fresh tier levels, with PROFILE copies on the (dealer
-- level, tier) profiles; the inert RETAIL and unknown tier text rules move
-- to category_pricing_rules_orphaned with a reason. Every rule this step
-- creates is recorded in category_pricing_rule_backfill, which the down
-- file reads to tell its own rows from a dealer's.
-- P5 creates product_prices, puts the stocking unit hold of 9.1 under every
-- price it creates (deferred constraint triggers on product_prices,
-- customer_contracts, pricing_rules and products, until C3-2B), moves the
-- contracts that name no customer or product to
-- customer_contracts_orphaned, and gives contracts their unit, their branch
-- and the new key.
-- P6 creates the vendor side and backfills one default LIST level per
-- existing vendor. No costs are backfilled: purchase order line costs are
-- history at scale 2 with no unit, and C4-0's feed intake is the first
-- writer.

-- P0 --------------------------------------------------------------------
-- A negative multiplier aborts the migration, naming the level.
DO $m122$
DECLARE
    v_name text;
    v_id text;
    v_mult text;
BEGIN
    FOR v_name, v_id, v_mult IN
        SELECT name, id::text, multiplier::text
        FROM price_levels WHERE multiplier < 0
        ORDER BY created_at, id
    LOOP
        RAISE EXCEPTION 'price level % (%) carries the negative multiplier %, which has no meaning as a price; correct the level before upgrading', v_name, v_id, v_mult;
    END LOOP;
END
$m122$;

-- P1: the dealer's levels ------------------------------------------------

ALTER TABLE price_levels
    ADD COLUMN IF NOT EXISTS code TEXT,
    ADD COLUMN IF NOT EXISTS basis TEXT NOT NULL DEFAULT 'BASE',
    ADD COLUMN IF NOT EXISTS adjust_percent NUMERIC(14,4),
    ADD COLUMN IF NOT EXISTS position INTEGER,
    ADD COLUMN IF NOT EXISTS is_active BOOLEAN NOT NULL DEFAULT TRUE,
    ADD COLUMN IF NOT EXISTS revision BIGINT NOT NULL DEFAULT 1;

-- The exact percent of the multiplier: 0.85 answers -15, 1.05 answers 5.
UPDATE price_levels SET adjust_percent = (multiplier - 1) * 100
WHERE adjust_percent IS NULL;

-- The code, backfilled from the name in (created_at, id) order: uppercased,
-- runs of other characters to one underscore, a numeric suffix on
-- collision. A name that would begin with a digit takes an L prefix; a code
-- is cut to the sixteen characters the vocabulary holds.
DO $m122$
DECLARE
    v_row RECORD;
    v_code text;
    v_n integer;
BEGIN
    FOR v_row IN SELECT id, name FROM price_levels WHERE code IS NULL ORDER BY created_at, id LOOP
        v_code := upper(regexp_replace(v_row.name, '[^A-Za-z0-9]+', '_', 'g'));
        v_code := regexp_replace(v_code, '^_+', '');
        IF v_code = '' OR left(v_code, 1) !~ '[A-Z]' THEN
            v_code := 'L' || v_code;
        END IF;
        v_code := left(v_code, 16);
        v_code := regexp_replace(v_code, '_+$', '');
        IF v_code = '' THEN
            v_code := 'LEVEL';
        END IF;
        IF EXISTS (SELECT 1 FROM price_levels WHERE code = v_code AND id <> v_row.id) THEN
            v_n := 1;
            WHILE EXISTS (SELECT 1 FROM price_levels
                          WHERE code = left(v_code, 16 - length(v_n::text)) || v_n::text
                          AND id <> v_row.id) LOOP
                v_n := v_n + 1;
            END LOOP;
            v_code := left(v_code, 16 - length(v_n::text)) || v_n::text;
        END IF;
        UPDATE price_levels SET code = v_code, updated_at = NOW() WHERE id = v_row.id;
    END LOOP;
END
$m122$;

-- The display position, in the same (created_at, id) order; the fresh
-- levels of P2 continue the sequence.
UPDATE price_levels
SET position = numbered.pos, updated_at = NOW()
FROM (
    SELECT id, (row_number() OVER (ORDER BY created_at, id))::integer AS pos
    FROM price_levels
    WHERE position IS NULL
) numbered
WHERE price_levels.id = numbered.id;

ALTER TABLE price_levels
    ALTER COLUMN code SET NOT NULL,
    ALTER COLUMN adjust_percent SET NOT NULL,
    ALTER COLUMN position SET NOT NULL;

DO $m122$
BEGIN
    IF NOT EXISTS (SELECT 1 FROM pg_constraint WHERE conname = 'price_levels_code_shape' AND conrelid = 'price_levels'::regclass) THEN
        ALTER TABLE price_levels ADD CONSTRAINT price_levels_code_shape
            CHECK (code ~ '^[A-Z][A-Z0-9_]{0,15}$');
    END IF;
    IF NOT EXISTS (SELECT 1 FROM pg_constraint WHERE conname = 'price_levels_basis_check' AND conrelid = 'price_levels'::regclass) THEN
        ALTER TABLE price_levels ADD CONSTRAINT price_levels_basis_check
            CHECK (basis IN ('BASE', 'AVERAGE_COST', 'REPLACEMENT_COST'));
    END IF;
    IF NOT EXISTS (SELECT 1 FROM pg_constraint WHERE conname = 'price_levels_adjust_floor' AND conrelid = 'price_levels'::regclass) THEN
        ALTER TABLE price_levels ADD CONSTRAINT price_levels_adjust_floor
            CHECK (adjust_percent >= -100);
    END IF;
    IF NOT EXISTS (SELECT 1 FROM pg_constraint WHERE conname = 'price_levels_code_key' AND conrelid = 'price_levels'::regclass) THEN
        ALTER TABLE price_levels ADD CONSTRAINT price_levels_code_key UNIQUE (code);
    END IF;
END
$m122$;

ALTER TABLE price_levels DROP COLUMN multiplier;

-- P2: fresh levels for the tiers and the default -------------------------
-- The backfill always creates four new levels and never adopts an existing
-- one by name or code: a dealer's own "Gold" at its own discount, and the
-- seed's repeatable "Retail", must keep exactly the customers they hold.

CREATE TABLE IF NOT EXISTS price_profiles (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    code TEXT NOT NULL,
    name TEXT NOT NULL,
    price_level_id UUID NOT NULL REFERENCES price_levels(id),
    is_active BOOLEAN NOT NULL DEFAULT TRUE,
    revision BIGINT NOT NULL DEFAULT 1,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

DO $m122$
BEGIN
    IF NOT EXISTS (SELECT 1 FROM pg_constraint WHERE conname = 'price_profiles_code_shape' AND conrelid = 'price_profiles'::regclass) THEN
        ALTER TABLE price_profiles ADD CONSTRAINT price_profiles_code_shape
            CHECK (code ~ '^[A-Z][A-Z0-9_]{0,15}$');
    END IF;
    IF NOT EXISTS (SELECT 1 FROM pg_constraint WHERE conname = 'price_profiles_code_key' AND conrelid = 'price_profiles'::regclass) THEN
        ALTER TABLE price_profiles ADD CONSTRAINT price_profiles_code_key UNIQUE (code);
    END IF;
END
$m122$;

-- The map the backfill and the down file both read: the profile a customer
-- combination landed on, and the combination itself (the level they held,
-- null when they held none, and the tier).
CREATE TABLE IF NOT EXISTS price_profile_backfill (
    price_profile_id UUID PRIMARY KEY REFERENCES price_profiles(id),
    price_level_id UUID,
    tier TEXT NOT NULL
);

CREATE TEMP TABLE m122_fresh (kind TEXT PRIMARY KEY, level_id UUID NOT NULL);

-- One fresh level, coded preferred when the code is free, else the
-- fallback, else the fallback with a numeric suffix.
CREATE OR REPLACE FUNCTION pg_temp.m122_fresh_level(
    v_preferred text, v_fallback text, v_name text, v_adjust numeric, v_pos integer
) RETURNS uuid AS $fn$
DECLARE
    v_code text;
    v_n integer;
    v_id uuid;
BEGIN
    v_code := v_preferred;
    IF EXISTS (SELECT 1 FROM price_levels WHERE code = v_code) THEN
        v_code := v_fallback;
    END IF;
    IF EXISTS (SELECT 1 FROM price_levels WHERE code = v_code) THEN
        v_n := 1;
        WHILE EXISTS (SELECT 1 FROM price_levels
                      WHERE code = left(v_fallback, 16 - length(v_n::text)) || v_n::text) LOOP
            v_n := v_n + 1;
        END LOOP;
        v_code := left(v_fallback, 16 - length(v_n::text)) || v_n::text;
    END IF;
    INSERT INTO price_levels (code, name, basis, adjust_percent, position, is_active)
    VALUES (v_code, v_name, 'BASE', v_adjust, v_pos, TRUE)
    RETURNING id INTO v_id;
    RETURN v_id;
END
$fn$ LANGUAGE plpgsql;

DO $m122$
DECLARE
    v_pos integer;
    v_silver uuid;
    v_gold uuid;
    v_plat uuid;
    v_def uuid;
    v_profile uuid;
    v_code text;
    v_n integer;
BEGIN
    SELECT COALESCE(MAX(position), 0) INTO v_pos FROM price_levels;
    v_silver := pg_temp.m122_fresh_level('SILVER', 'TIER_SILVER', 'Silver tier', -10, v_pos + 1);
    v_gold := pg_temp.m122_fresh_level('GOLD', 'TIER_GOLD', 'Gold tier', -15, v_pos + 2);
    v_plat := pg_temp.m122_fresh_level('PLATINUM', 'TIER_PLATINUM', 'Platinum tier', -20, v_pos + 3);
    v_def := pg_temp.m122_fresh_level('RETAIL', 'DEFAULT_RETAIL', 'Retail', 0, v_pos + 4);
    INSERT INTO m122_fresh VALUES
        ('SILVER', v_silver), ('GOLD', v_gold), ('PLATINUM', v_plat), ('DEFAULT', v_def);

    -- The default profile, on the fresh default level, recorded in
    -- system_settings beside the level: the ids, never a name, are what the
    -- reads go through.
    v_code := 'RETAIL';
    IF EXISTS (SELECT 1 FROM price_profiles WHERE code = v_code) THEN
        v_code := 'DEFAULT_RETAIL';
    END IF;
    IF EXISTS (SELECT 1 FROM price_profiles WHERE code = v_code) THEN
        v_n := 1;
        WHILE EXISTS (SELECT 1 FROM price_profiles
                      WHERE code = left('DEFAULT_RETAIL', 16 - length(v_n::text)) || v_n::text) LOOP
            v_n := v_n + 1;
        END LOOP;
        v_code := left('DEFAULT_RETAIL', 16 - length(v_n::text)) || v_n::text;
    END IF;
    INSERT INTO price_profiles (code, name, price_level_id)
    VALUES (v_code, 'Default pricing', v_def)
    RETURNING id INTO v_profile;

    INSERT INTO system_settings (key, value) VALUES
        ('pricing.default_price_level_id', v_def::text),
        ('pricing.default_price_profile_id', v_profile::text)
    ON CONFLICT (key) DO UPDATE SET value = EXCLUDED.value, updated_at = NOW();

    DROP FUNCTION IF EXISTS price_profile_default_id();
    CREATE FUNCTION price_profile_default_id() RETURNS uuid
    LANGUAGE sql STABLE AS $fn$
        SELECT value::uuid FROM system_settings WHERE key = 'pricing.default_price_profile_id'
    $fn$;
END
$m122$;

-- P3: profiles and the customers ----------------------------------------

-- One profile per distinct (price_level_id, tier) combination the customers
-- hold. (No level, RETAIL) is the default profile P2 created; (no level,
-- tier X) sits on the fresh tier level; (level L, ...) sits on L. The
-- backfill row records the combination the customers held, so the down file
-- can put every customer back exactly as it found them.
DO $m122$
DECLARE
    v_combo RECORD;
    v_profile uuid;
    v_code text;
    v_name text;
    v_n integer;
BEGIN
    FOR v_combo IN
        SELECT COALESCE(c.price_level_id::text, '') AS lvl_text,
               COALESCE(c.price_level_id,
                   (SELECT f.level_id FROM m122_fresh f WHERE f.kind =
                       CASE c.tier::text
                           WHEN 'SILVER' THEN 'SILVER'
                           WHEN 'GOLD' THEN 'GOLD'
                           WHEN 'PLATINUM' THEN 'PLATINUM'
                       END)) AS eff_level,
               c.tier::text AS tier
        FROM customers c
        GROUP BY 1, 2, 3
    LOOP
        IF v_combo.lvl_text = '' AND v_combo.tier = 'RETAIL' THEN
            v_profile := (SELECT value::uuid FROM system_settings
                          WHERE key = 'pricing.default_price_profile_id');
        ELSE
            SELECT pl.code, pl.name INTO v_code, v_name
            FROM price_levels pl WHERE pl.id = v_combo.eff_level;
            IF v_combo.tier <> 'RETAIL' THEN
                v_code := left(v_code, 16 - length(v_combo.tier) - 1) || '_' || v_combo.tier;
                v_name := v_name || ' (' || initcap(lower(v_combo.tier)) || ')';
            END IF;
            IF EXISTS (SELECT 1 FROM price_profiles WHERE code = v_code) THEN
                v_n := 1;
                WHILE EXISTS (SELECT 1 FROM price_profiles
                              WHERE code = left(v_code, 16 - length(v_n::text)) || v_n::text) LOOP
                    v_n := v_n + 1;
                END LOOP;
                v_code := left(v_code, 16 - length(v_n::text)) || v_n::text;
            END IF;
            INSERT INTO price_profiles (code, name, price_level_id)
            VALUES (v_code, v_name, v_combo.eff_level)
            RETURNING id INTO v_profile;
        END IF;
        INSERT INTO price_profile_backfill (price_profile_id, price_level_id, tier)
        VALUES (v_profile, NULLIF(v_combo.lvl_text, '')::uuid, v_combo.tier)
        ON CONFLICT (price_profile_id) DO NOTHING;
    END LOOP;
END
$m122$;

ALTER TABLE customers ADD COLUMN IF NOT EXISTS price_profile_id UUID;

UPDATE customers c
SET price_profile_id = b.price_profile_id, updated_at = NOW()
FROM price_profile_backfill b
WHERE c.price_profile_id IS NULL
  AND (COALESCE(c.price_level_id::text, ''), c.tier::text)
      = (COALESCE(b.price_level_id::text, ''), b.tier);

DO $m122$
DECLARE
    v_unmapped int;
BEGIN
    SELECT count(*) INTO v_unmapped FROM customers
    WHERE price_profile_id IS NULL
       OR NOT EXISTS (SELECT 1 FROM price_profile_backfill b WHERE b.price_profile_id = customers.price_profile_id);
    IF v_unmapped > 0 THEN
        RAISE EXCEPTION '% customer(s) hold no profile the backfill mapped', v_unmapped;
    END IF;
END
$m122$;

ALTER TABLE customers
    ALTER COLUMN price_profile_id SET NOT NULL,
    ALTER COLUMN price_profile_id SET DEFAULT price_profile_default_id();

ALTER TABLE customers ADD CONSTRAINT customers_price_profile_id_fkey
    FOREIGN KEY (price_profile_id) REFERENCES price_profiles(id);

ALTER TABLE customers DROP COLUMN tier;
ALTER TABLE customers DROP COLUMN price_level_id;
DROP TYPE customer_tier;

-- P4: the category rules -------------------------------------------------

-- The originals leave the live table for the orphaned table, which keeps
-- their columns and a reason; the down file moves them back.
CREATE TABLE category_pricing_rules_orphaned (LIKE category_pricing_rules INCLUDING ALL);
ALTER TABLE category_pricing_rules_orphaned ADD COLUMN reason TEXT NOT NULL;

-- What P4 created, so the down file can tell its own rows from a dealer's
-- later ones and refuse the latter, naming the row.
CREATE TABLE IF NOT EXISTS category_pricing_rule_backfill (
    rule_id UUID PRIMARY KEY,
    source_rule_id UUID NOT NULL,
    copy_kind TEXT NOT NULL
);

ALTER TABLE category_pricing_rules
    ADD COLUMN IF NOT EXISTS price_level_id UUID REFERENCES price_levels(id),
    ADD COLUMN IF NOT EXISTS price_profile_id UUID REFERENCES price_profiles(id);

-- The old target checks come off before the loop: the LEVEL and PROFILE
-- rows it creates have no place in them. The new check goes on after the
-- loop, over the mapped rows alone.
DO $m122$
BEGIN
    IF EXISTS (SELECT 1 FROM pg_constraint WHERE conname = 'category_pricing_rules_target_type_check' AND conrelid = 'category_pricing_rules'::regclass) THEN
        ALTER TABLE category_pricing_rules DROP CONSTRAINT category_pricing_rules_target_type_check;
    END IF;
    IF EXISTS (SELECT 1 FROM pg_constraint WHERE conname = 'category_pricing_rules_check' AND conrelid = 'category_pricing_rules'::regclass) THEN
        ALTER TABLE category_pricing_rules DROP CONSTRAINT category_pricing_rules_check;
    END IF;
    IF EXISTS (SELECT 1 FROM pg_constraint WHERE conname = 'category_pricing_rules_single_target_check' AND conrelid = 'category_pricing_rules'::regclass) THEN
        ALTER TABLE category_pricing_rules DROP CONSTRAINT category_pricing_rules_single_target_check;
    END IF;
END
$m122$;

DO $m122$
DECLARE
    v_rule RECORD;
    v_new uuid;
    v_fresh uuid;
    v_profile uuid;
    v_reason text;
    v_retail int := 0;
    v_unknown int := 0;
    v_migrated int := 0;
BEGIN
    FOR v_rule IN SELECT id FROM category_pricing_rules WHERE target_type = 'TIER' ORDER BY id LOOP
        SELECT * INTO v_rule FROM category_pricing_rules WHERE id = v_rule.id;
        IF v_rule.tier IN ('SILVER', 'GOLD', 'PLATINUM') THEN
            SELECT f.level_id INTO v_fresh FROM m122_fresh f WHERE f.kind = v_rule.tier;
            INSERT INTO category_pricing_rules
                (target_type, price_level_id, category_id, rule_type, rule_value,
                 margin_floor_pct, starts_at, expires_at, is_active, priority, created_by)
            VALUES
                ('LEVEL', v_fresh, v_rule.category_id, v_rule.rule_type, v_rule.rule_value,
                 v_rule.margin_floor_pct, v_rule.starts_at, v_rule.expires_at, v_rule.is_active,
                 v_rule.priority, 'migration 122')
            RETURNING id INTO v_new;
            v_reason := format('the %s tier rule became the LEVEL rule %s', v_rule.tier, v_new);
            INSERT INTO category_pricing_rule_backfill VALUES (v_new, v_rule.id, 'LEVEL');
            v_migrated := v_migrated + 1;

            -- One PROFILE copy per (dealer level, tier) profile; the (no
            -- level, tier) customers answer the LEVEL rule at the level arm
            -- of the category rung.
            FOR v_profile IN
                SELECT b.price_profile_id FROM price_profile_backfill b
                WHERE b.tier = v_rule.tier AND b.price_level_id IS NOT NULL
                ORDER BY 1
            LOOP
                INSERT INTO category_pricing_rules
                    (target_type, price_profile_id, category_id, rule_type, rule_value,
                     margin_floor_pct, starts_at, expires_at, is_active, priority, created_by)
                VALUES
                    ('PROFILE', v_profile, v_rule.category_id, v_rule.rule_type, v_rule.rule_value,
                     v_rule.margin_floor_pct, v_rule.starts_at, v_rule.expires_at, v_rule.is_active,
                     v_rule.priority, 'migration 122')
                RETURNING id INTO v_new;
                INSERT INTO category_pricing_rule_backfill VALUES (v_new, v_rule.id, 'PROFILE');
            END LOOP;
        ELSIF v_rule.tier = 'RETAIL' THEN
            v_reason := 'the RETAIL tier rule was inert: the resolver of its day skipped RETAIL';
            v_retail := v_retail + 1;
        ELSE
            v_reason := format('the tier text "%s" names no tier the engine priced', v_rule.tier);
            v_unknown := v_unknown + 1;
        END IF;
        INSERT INTO category_pricing_rules_orphaned
            (id, target_type, customer_id, tier, category_id, rule_type, rule_value,
             margin_floor_pct, starts_at, expires_at, is_active, priority, created_by,
             created_at, updated_at, revision, reason)
        SELECT o.id, o.target_type, o.customer_id, o.tier, o.category_id, o.rule_type,
               o.rule_value, o.margin_floor_pct, o.starts_at, o.expires_at, o.is_active,
               o.priority, o.created_by, o.created_at, o.updated_at, o.revision, v_reason
        FROM category_pricing_rules o WHERE o.id = v_rule.id;
        DELETE FROM category_pricing_rules WHERE id = v_rule.id;
    END LOOP;

    RAISE NOTICE '122: % tier rule(s) became LEVEL rules and PROFILE copies, % inert RETAIL rule(s) and % rule(s) naming an unknown tier moved to category_pricing_rules_orphaned',
        v_migrated, v_retail, v_unknown;
END
$m122$;

ALTER TABLE category_pricing_rules
    ADD CONSTRAINT category_pricing_rules_target_type_check
        CHECK (target_type IN ('ACCOUNT', 'PROFILE', 'LEVEL')),
    ADD CONSTRAINT category_pricing_rules_check
        CHECK (
            (target_type = 'ACCOUNT' AND customer_id IS NOT NULL
                AND price_profile_id IS NULL AND price_level_id IS NULL)
            OR (target_type = 'PROFILE' AND price_profile_id IS NOT NULL
                AND customer_id IS NULL AND price_level_id IS NULL)
            OR (target_type = 'LEVEL' AND price_level_id IS NOT NULL
                AND customer_id IS NULL AND price_profile_id IS NULL));

CREATE UNIQUE INDEX IF NOT EXISTS idx_cpr_unique_level
    ON category_pricing_rules (price_level_id, category_id)
    WHERE is_active = true AND target_type = 'LEVEL';
CREATE UNIQUE INDEX IF NOT EXISTS idx_cpr_unique_profile
    ON category_pricing_rules (price_profile_id, category_id)
    WHERE is_active = true AND target_type = 'PROFILE';

ALTER TABLE category_pricing_rules DROP COLUMN tier;

-- P5: product prices, the hold, the contracts and the rules --------------

-- Empty: there is no branch or level price today. One price per level per
-- branch; the row with neither a level nor a branch (the product's base)
-- has no place here, the base stays on the product.
CREATE TABLE IF NOT EXISTS product_prices (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    product_id UUID NOT NULL REFERENCES products(id) ON DELETE CASCADE,
    price_level_id UUID REFERENCES price_levels(id),
    branch_id UUID REFERENCES locations(id),
    price_uom TEXT NOT NULL,
    unit_price NUMERIC(12,4) NOT NULL CHECK (unit_price >= 0),
    revision BIGINT NOT NULL DEFAULT 1,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    CHECK (price_level_id IS NOT NULL OR branch_id IS NOT NULL),
    CONSTRAINT product_prices_product_level_branch_key
        UNIQUE NULLS NOT DISTINCT (product_id, price_level_id, branch_id),
    FOREIGN KEY (product_id, price_uom) REFERENCES product_units(product_id, uom)
);

-- The stocking unit hold (ADR 0006 section 9.1), under every price this
-- migration creates, until C3-2B: a raw write of a price in another unit is
-- refused, and the stocking unit itself cannot move under a price. The
-- service refuses first with a 400 naming price_uom; these catch the raw
-- write. C3-2B drops the triggers and A2's CHECK.
CREATE OR REPLACE FUNCTION pricing_price_uom_held() RETURNS trigger AS $$
DECLARE
    v_stock text;
BEGIN
    IF NEW.product_id IS NULL THEN
        RETURN NULL; -- a rule without a product prices per each product's own unit
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

DROP TRIGGER IF EXISTS product_prices_price_uom_held ON product_prices;
CREATE CONSTRAINT TRIGGER product_prices_price_uom_held
    AFTER INSERT OR UPDATE OF price_uom, product_id ON product_prices
    DEFERRABLE INITIALLY DEFERRED
    FOR EACH ROW EXECUTE FUNCTION pricing_price_uom_held();

-- The products side of the hold, widened from A2 (migration 099): it now
-- also fires on a change of the price unit, and it also refuses while a
-- product_prices row names the product.
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

DROP TRIGGER IF EXISTS products_price_unit_held ON products;
CREATE CONSTRAINT TRIGGER products_price_unit_held
    AFTER UPDATE OF uom_primary, price_uom ON products
    DEFERRABLE INITIALLY DEFERRED
    FOR EACH ROW EXECUTE FUNCTION products_price_unit_held();

-- The contracts: a row that names no customer or product cannot match
-- today's lookup (it filters on both); it moves aside, reported.
CREATE TABLE customer_contracts_orphaned (LIKE customer_contracts INCLUDING ALL);

DO $m122$
DECLARE
    v_n int;
BEGIN
    INSERT INTO customer_contracts_orphaned
    SELECT * FROM customer_contracts WHERE customer_id IS NULL OR product_id IS NULL;
    GET DIAGNOSTICS v_n = ROW_COUNT;
    DELETE FROM customer_contracts WHERE customer_id IS NULL OR product_id IS NULL;
    IF v_n > 0 THEN
        RAISE NOTICE '122: % contract(s) naming no customer or product moved to customer_contracts_orphaned', v_n;
    END IF;
END
$m122$;

ALTER TABLE customer_contracts
    ALTER COLUMN customer_id SET NOT NULL,
    ALTER COLUMN product_id SET NOT NULL,
    ADD COLUMN IF NOT EXISTS price_uom TEXT,
    ADD COLUMN IF NOT EXISTS branch_id UUID REFERENCES locations(id);

UPDATE customer_contracts cc
SET price_uom = p.uom_primary
FROM products p
WHERE cc.product_id = p.id AND cc.price_uom IS NULL;

ALTER TABLE customer_contracts
    ALTER COLUMN price_uom SET NOT NULL;

ALTER TABLE customer_contracts ADD CONSTRAINT customer_contracts_price_uom_fkey
    FOREIGN KEY (product_id, price_uom) REFERENCES product_units(product_id, uom);

ALTER TABLE customer_contracts DROP CONSTRAINT customer_contracts_customer_id_product_id_key;

DO $m122$
BEGIN
    IF NOT EXISTS (SELECT 1 FROM pg_constraint WHERE conname = 'customer_contracts_customer_product_branch_key' AND conrelid = 'customer_contracts'::regclass) THEN
        ALTER TABLE customer_contracts ADD CONSTRAINT customer_contracts_customer_product_branch_key
            UNIQUE NULLS NOT DISTINCT (customer_id, product_id, branch_id);
    END IF;
END
$m122$;

-- The pricing rules: a fixed price on a rule that names a product is per
-- the product's stocking unit until C3-2B, and says so.
ALTER TABLE pricing_rules ADD COLUMN IF NOT EXISTS price_uom TEXT;

UPDATE pricing_rules r
SET price_uom = p.uom_primary
FROM products p
WHERE r.product_id = p.id AND r.fixed_price IS NOT NULL AND r.price_uom IS NULL;

DO $m122$
BEGIN
    IF NOT EXISTS (SELECT 1 FROM pg_constraint WHERE conname = 'pricing_rules_price_uom_check' AND conrelid = 'pricing_rules'::regclass) THEN
        ALTER TABLE pricing_rules ADD CONSTRAINT pricing_rules_price_uom_check
            CHECK (fixed_price IS NULL OR product_id IS NULL OR price_uom IS NOT NULL);
    END IF;
END
$m122$;

DROP TRIGGER IF EXISTS customer_contracts_price_uom_held ON customer_contracts;
CREATE CONSTRAINT TRIGGER customer_contracts_price_uom_held
    AFTER INSERT OR UPDATE OF price_uom, product_id ON customer_contracts
    DEFERRABLE INITIALLY DEFERRED
    FOR EACH ROW EXECUTE FUNCTION pricing_price_uom_held();

DROP TRIGGER IF EXISTS pricing_rules_price_uom_held ON pricing_rules;
CREATE CONSTRAINT TRIGGER pricing_rules_price_uom_held
    AFTER INSERT OR UPDATE OF price_uom, product_id, fixed_price ON pricing_rules
    DEFERRABLE INITIALLY DEFERRED
    FOR EACH ROW EXECUTE FUNCTION pricing_price_uom_held();

-- P6: the vendor side ----------------------------------------------------

CREATE TABLE IF NOT EXISTS vendor_price_levels (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    vendor_id UUID NOT NULL REFERENCES vendors(id),
    code TEXT NOT NULL,
    name TEXT NOT NULL,
    is_default BOOLEAN NOT NULL DEFAULT FALSE,
    is_active BOOLEAN NOT NULL DEFAULT TRUE,
    revision BIGINT NOT NULL DEFAULT 1,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    CONSTRAINT vendor_price_levels_code_shape CHECK (code ~ '^[A-Z][A-Z0-9_]{0,15}$')
);

DO $m122$
BEGIN
    IF NOT EXISTS (SELECT 1 FROM pg_constraint WHERE conname = 'vendor_price_levels_vendor_code_key' AND conrelid = 'vendor_price_levels'::regclass) THEN
        ALTER TABLE vendor_price_levels ADD CONSTRAINT vendor_price_levels_vendor_code_key
            UNIQUE (vendor_id, code);
    END IF;
END
$m122$;

CREATE UNIQUE INDEX IF NOT EXISTS vendor_price_levels_one_default_per_vendor
    ON vendor_price_levels (vendor_id) WHERE is_default;

-- No costs are backfilled (P6): purchase order line costs are history at
-- scale 2 with no unit, and edi_catalog_entries is not linked to vendors;
-- C4-0's feed intake is the first writer.
CREATE TABLE IF NOT EXISTS vendor_product_costs (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    vendor_id UUID NOT NULL REFERENCES vendors(id),
    vendor_price_level_id UUID NOT NULL REFERENCES vendor_price_levels(id),
    product_id UUID NOT NULL REFERENCES products(id) ON DELETE CASCADE,
    branch_id UUID REFERENCES locations(id),
    purchase_uom TEXT NOT NULL,
    unit_cost NUMERIC(12,4) NOT NULL CHECK (unit_cost >= 0),
    vendor_sku TEXT,
    effective_from DATE NOT NULL,
    effective_to DATE,
    revision BIGINT NOT NULL DEFAULT 1,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    CHECK (effective_to IS NULL OR effective_to >= effective_from),
    CONSTRAINT vendor_product_costs_vendor_level_product_branch_from_key
        UNIQUE NULLS NOT DISTINCT (vendor_id, vendor_price_level_id, product_id, branch_id, effective_from),
    FOREIGN KEY (product_id, purchase_uom) REFERENCES product_units(product_id, uom)
);

INSERT INTO vendor_price_levels (vendor_id, code, name, is_default)
SELECT id, 'LIST', 'List', TRUE FROM vendors
ON CONFLICT (vendor_id, code) DO NOTHING;
