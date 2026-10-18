-- SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
-- SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

-- Down file for 122_price_levels_profiles_and_costs (C3-2A-pricing). It
-- reverses the migration's own steps and refuses, naming the first row it
-- cannot map back, wherever data written in the new shape has no place in
-- the old one: a level on a basis other than BASE, a customer on a profile
-- the backfill map does not hold, a LEVEL or PROFILE rule the backfill did
-- not make, a product price, a contract with a branch or a unit other than
-- its product's stocking unit, a vendor cost. A down never discards a
-- dealer's data.
--
-- It restores: the multiplier (from the percent), customers.tier and
-- customers.price_level_id (from price_profile_backfill), the TIER category
-- rules (the created LEVEL and PROFILE rules are deleted, the originals
-- come home from category_pricing_rules_orphaned), and the orphaned
-- contracts. The four fresh levels go with the profiles that name them.

-- The refusals, before anything moves.
DO $m122d$
DECLARE
    v_id text;
BEGIN
    FOR v_id IN
        SELECT 'level ' || code || ' (' || name || ') is on the basis ' || basis
        FROM price_levels WHERE basis <> 'BASE' ORDER BY code LIMIT 1
    LOOP
        RAISE EXCEPTION '122 down: %; a down can only restore BASE levels', v_id;
    END LOOP;

    FOR v_id IN
        SELECT 'customer ' || account_number || ' (' || name || ') sits on profile ' || price_profile_id::text ||
               ', which the backfill map does not hold'
        FROM customers c
        WHERE NOT EXISTS (SELECT 1 FROM price_profile_backfill b WHERE b.price_profile_id = c.price_profile_id)
        ORDER BY account_number LIMIT 1
    LOOP
        RAISE EXCEPTION '122 down: %', v_id;
    END LOOP;

    FOR v_id IN
        SELECT 'category rule ' || r.id::text || ' (' || r.created_by || ') is a ' || r.target_type ||
               ' rule the backfill did not make'
        FROM category_pricing_rules r
        WHERE r.target_type IN ('LEVEL', 'PROFILE')
          AND NOT EXISTS (SELECT 1 FROM category_pricing_rule_backfill b WHERE b.rule_id = r.id)
        ORDER BY r.id LIMIT 1
    LOOP
        RAISE EXCEPTION '122 down: %', v_id;
    END LOOP;

    FOR v_id IN
        SELECT 'product price ' || id::text || ' for product ' || product_id::text
        FROM product_prices ORDER BY id LIMIT 1
    LOOP
        RAISE EXCEPTION '122 down: %; branch and level prices have no place in the old shape', v_id;
    END LOOP;

    FOR v_id IN
        SELECT 'contract ' || cc.id::text || ' for customer ' || COALESCE(cc.customer_id::text, 'none') ||
               CASE WHEN cc.branch_id IS NOT NULL THEN ' names a branch' ELSE '' END ||
               CASE WHEN cc.price_uom IS DISTINCT FROM p.uom_primary
                    THEN ' is priced per ' || cc.price_uom || ' while the product is stocked in ' || p.uom_primary
                    ELSE '' END
        FROM customer_contracts cc
        LEFT JOIN products p ON p.id = cc.product_id
        WHERE cc.branch_id IS NOT NULL OR cc.price_uom IS DISTINCT FROM p.uom_primary
        ORDER BY cc.id LIMIT 1
    LOOP
        RAISE EXCEPTION '122 down: %', v_id;
    END LOOP;

    FOR v_id IN
        SELECT 'vendor cost ' || id::text || ' for product ' || product_id::text
        FROM vendor_product_costs ORDER BY id LIMIT 1
    LOOP
        RAISE EXCEPTION '122 down: %; vendor costs have no place in the old shape', v_id;
    END LOOP;
END
$m122d$;

-- The vendor side goes whole: the LIST levels P6 backfilled carried no
-- costs (the refusal above holds the down to that).
DROP TABLE IF EXISTS vendor_product_costs;
DROP TABLE IF EXISTS vendor_price_levels;

-- The contracts: the new columns and key go, the old key comes back, the
-- columns that became NOT NULL answer NULLs again, the orphaned rows come
-- home. The hold triggers go first: they name the columns being dropped.
DROP TRIGGER IF EXISTS customer_contracts_price_uom_held ON customer_contracts;
DROP TRIGGER IF EXISTS pricing_rules_price_uom_held ON pricing_rules;
DROP TRIGGER IF EXISTS product_prices_price_uom_held ON product_prices;
DROP FUNCTION IF EXISTS pricing_price_uom_held();

ALTER TABLE customer_contracts DROP CONSTRAINT IF EXISTS customer_contracts_price_uom_fkey;
ALTER TABLE customer_contracts DROP CONSTRAINT IF EXISTS customer_contracts_customer_product_branch_key;
ALTER TABLE customer_contracts
    DROP COLUMN IF EXISTS price_uom,
    DROP COLUMN IF EXISTS branch_id,
    ALTER COLUMN customer_id DROP NOT NULL,
    ALTER COLUMN product_id DROP NOT NULL;
ALTER TABLE customer_contracts ADD CONSTRAINT customer_contracts_customer_id_product_id_key
    UNIQUE (customer_id, product_id);

INSERT INTO customer_contracts (id, customer_id, product_id, contract_price, created_at, updated_at, revision)
SELECT id, customer_id, product_id, contract_price, created_at, updated_at, revision
FROM customer_contracts_orphaned
ON CONFLICT (id) DO NOTHING;

DROP TABLE IF EXISTS customer_contracts_orphaned;

DROP TABLE IF EXISTS product_prices;

-- The pricing rules: the unit column and its check go, and the products
-- side of the hold returns to the A2 shape (migration 099).
ALTER TABLE pricing_rules DROP CONSTRAINT IF EXISTS pricing_rules_price_uom_check;
ALTER TABLE pricing_rules DROP COLUMN IF EXISTS price_uom;

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
END
$$ LANGUAGE plpgsql;

DROP TRIGGER IF EXISTS products_price_unit_held ON products;
CREATE CONSTRAINT TRIGGER products_price_unit_held
    AFTER UPDATE OF uom_primary ON products
    DEFERRABLE INITIALLY DEFERRED
    FOR EACH ROW EXECUTE FUNCTION products_price_unit_held();

-- The category rules: the created LEVEL and PROFILE rules are deleted, the
-- originals come home from the orphaned table with their tier, and the old
-- shape's targets and constraints return. The new shape's checks carry the
-- same names as the old ones; they come off first, so the TIER rows can
-- come home, and the old ones go on after.
DROP INDEX IF EXISTS idx_cpr_unique_level;
DROP INDEX IF EXISTS idx_cpr_unique_profile;

ALTER TABLE category_pricing_rules DROP CONSTRAINT IF EXISTS category_pricing_rules_target_type_check;
ALTER TABLE category_pricing_rules DROP CONSTRAINT IF EXISTS category_pricing_rules_check;

DELETE FROM category_pricing_rules
WHERE target_type IN ('LEVEL', 'PROFILE')
  AND id IN (SELECT rule_id FROM category_pricing_rule_backfill);

ALTER TABLE category_pricing_rules ADD COLUMN IF NOT EXISTS tier TEXT;

INSERT INTO category_pricing_rules
    (id, target_type, customer_id, tier, category_id, rule_type, rule_value,
     margin_floor_pct, starts_at, expires_at, is_active, priority, created_by,
     created_at, updated_at, revision)
SELECT id, target_type, customer_id, tier, category_id, rule_type, rule_value,
       margin_floor_pct, starts_at, expires_at, is_active, priority, created_by,
       created_at, updated_at, revision
FROM category_pricing_rules_orphaned;

DROP TABLE IF EXISTS category_pricing_rules_orphaned;
DROP TABLE IF EXISTS category_pricing_rule_backfill;

ALTER TABLE category_pricing_rules
    ADD CONSTRAINT category_pricing_rules_target_type_check
        CHECK (target_type IN ('ACCOUNT', 'TIER')),
    ADD CONSTRAINT category_pricing_rules_check
        CHECK (
            (target_type = 'ACCOUNT' AND customer_id IS NOT NULL)
            OR (target_type = 'TIER' AND tier IS NOT NULL));

-- The single target check of migration 107 returns the way 107 put it:
-- NOT VALID when a restored legacy row still breaks it (named in a NOTICE),
-- validated when none does.
DO $m122d$
DECLARE
    v_bad int;
    v_ids text;
BEGIN
    SELECT count(*), COALESCE(string_agg(id::text, ', ' ORDER BY id), '')
      INTO v_bad, v_ids
      FROM category_pricing_rules
     WHERE (target_type = 'TIER' AND customer_id IS NOT NULL)
        OR (target_type = 'ACCOUNT' AND tier IS NOT NULL);
    IF v_bad > 0 THEN
        RAISE NOTICE '122 down: % category pricing rule row(s) name both a tier and a customer; the single target check goes on NOT VALID: %', v_bad, v_ids;
        EXECUTE 'ALTER TABLE category_pricing_rules
                 ADD CONSTRAINT category_pricing_rules_single_target_check
                 CHECK ((target_type = ''ACCOUNT'' AND tier IS NULL)
                    OR (target_type = ''TIER'' AND customer_id IS NULL)) NOT VALID';
    ELSE
        EXECUTE 'ALTER TABLE category_pricing_rules
                 ADD CONSTRAINT category_pricing_rules_single_target_check
                 CHECK ((target_type = ''ACCOUNT'' AND tier IS NULL)
                    OR (target_type = ''TIER'' AND customer_id IS NULL))';
    END IF;
END
$m122d$;

CREATE INDEX IF NOT EXISTS idx_cat_pricing_tier_category
    ON category_pricing_rules (tier, category_id)
    WHERE is_active = true AND target_type = 'TIER';
CREATE UNIQUE INDEX IF NOT EXISTS idx_cpr_unique_tier
    ON category_pricing_rules (tier, category_id)
    WHERE is_active = true AND target_type = 'TIER';

-- The customers: the enum, the columns, the original values from the map,
-- and only then the profile column goes.
DROP TYPE IF EXISTS customer_tier;
CREATE TYPE customer_tier AS ENUM ('RETAIL', 'SILVER', 'GOLD', 'PLATINUM');

ALTER TABLE customers
    ADD COLUMN IF NOT EXISTS tier customer_tier,
    ADD COLUMN IF NOT EXISTS price_level_id UUID;

UPDATE customers c
SET tier = b.tier::customer_tier,
    price_level_id = b.price_level_id
FROM price_profile_backfill b
WHERE b.price_profile_id = c.price_profile_id;

ALTER TABLE customers
    ALTER COLUMN tier SET DEFAULT 'RETAIL',
    ALTER COLUMN tier SET NOT NULL;

ALTER TABLE customers ADD CONSTRAINT customers_price_level_id_fkey
    FOREIGN KEY (price_level_id) REFERENCES price_levels(id) ON DELETE SET NULL;

ALTER TABLE customers
    DROP CONSTRAINT IF EXISTS customers_price_profile_id_fkey,
    DROP COLUMN IF EXISTS price_profile_id;

DROP FUNCTION IF EXISTS price_profile_default_id();
DELETE FROM system_settings WHERE key IN ('pricing.default_price_level_id', 'pricing.default_price_profile_id');

-- The profiles go first (the customers no longer name them), then the
-- fresh levels the map's profiles name. A fresh level for a combination no
-- customer held is not in the map; it survives the down as an unused level,
-- which the old shape can hold.
CREATE TEMP TABLE m122d_fresh AS
    SELECT DISTINCT p.price_level_id AS level_id
    FROM price_profiles p
    JOIN price_profile_backfill b ON b.price_profile_id = p.id
    WHERE b.price_level_id IS NULL;

ALTER TABLE category_pricing_rules
    DROP COLUMN IF EXISTS price_level_id,
    DROP COLUMN IF EXISTS price_profile_id;

ALTER TABLE category_pricing_audit
    DROP COLUMN IF EXISTS price_level_id,
    DROP COLUMN IF EXISTS price_profile_id;

DROP TABLE IF EXISTS price_profile_backfill;
DROP TABLE IF EXISTS price_profiles;

DELETE FROM price_levels WHERE id IN (SELECT level_id FROM m122d_fresh);
DROP TABLE IF EXISTS m122d_fresh;

-- price_levels: the old shape, the multiplier from the percent.
ALTER TABLE price_levels DROP CONSTRAINT IF EXISTS price_levels_code_shape;
ALTER TABLE price_levels DROP CONSTRAINT IF EXISTS price_levels_basis_check;
ALTER TABLE price_levels DROP CONSTRAINT IF EXISTS price_levels_adjust_floor;
ALTER TABLE price_levels DROP CONSTRAINT IF EXISTS price_levels_code_key;

ALTER TABLE price_levels
    ADD COLUMN IF NOT EXISTS multiplier NUMERIC(12, 4) NOT NULL DEFAULT 1.0000;

UPDATE price_levels
SET multiplier = 1 + (adjust_percent / 100)
WHERE adjust_percent IS NOT NULL;

ALTER TABLE price_levels
    DROP COLUMN IF EXISTS code,
    DROP COLUMN IF EXISTS basis,
    DROP COLUMN IF EXISTS adjust_percent,
    DROP COLUMN IF EXISTS position,
    DROP COLUMN IF EXISTS is_active,
    DROP COLUMN IF EXISTS revision;
