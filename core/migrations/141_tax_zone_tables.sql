-- SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
-- SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

-- Migration 141: tax by ship to, in Gable's own zone tables (CP-10). A zone
-- maps a place of receipt (a postal code, a postal code range, or a Canadian
-- province) to a jurisdiction and its rate parts. The parts are stored, not
-- the combined rate, and every row carries its effective window so a rate
-- that changes is never overwritten. The documents record which zone and
-- which parts answered, frozen at the sale.

CREATE TABLE IF NOT EXISTS tax_zones (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    country CHAR(2) NOT NULL,
    jurisdiction_code TEXT NOT NULL,
    jurisdiction_type TEXT NOT NULL
        CHECK (jurisdiction_type IN ('STA', 'CTY', 'CIT', 'STJ', 'PROV')),
    jurisdiction_name TEXT NOT NULL DEFAULT '',
    state_code TEXT NOT NULL DEFAULT '',
    county_code TEXT NOT NULL DEFAULT '',
    place_code TEXT NOT NULL DEFAULT '',
    province_code TEXT NOT NULL DEFAULT '',
    zone_kind TEXT NOT NULL CHECK (zone_kind IN ('POSTAL', 'PROVINCE')),
    sourcing TEXT NOT NULL CHECK (sourcing IN ('ORIGIN', 'DESTINATION', 'BOTH')),
    effective_from DATE NOT NULL,
    effective_to DATE,
    data_source TEXT NOT NULL DEFAULT '',
    data_version TEXT NOT NULL DEFAULT '',
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

COMMENT ON TABLE tax_zones IS
    'A tax jurisdiction and its effective window, resolved from a place of receipt (CP-10).';

CREATE TABLE IF NOT EXISTS tax_zone_rates (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    zone_id UUID NOT NULL REFERENCES tax_zones(id),
    part_name TEXT NOT NULL
        CHECK (part_name IN ('STATE', 'COUNTY', 'CITY', 'DISTRICT', 'GOODS_TAX', 'PROVINCIAL_SALES_TAX')),
    rate NUMERIC(9, 6) NOT NULL CHECK (rate >= 0 AND rate <= 1),
    effective_from DATE NOT NULL,
    effective_to DATE,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    UNIQUE (zone_id, part_name, effective_from)
);

COMMENT ON TABLE tax_zone_rates IS
    'One rate part of a zone: the combined rate is the sum of the live parts, never stored.';

CREATE TABLE IF NOT EXISTS tax_zone_boundaries (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    zone_id UUID NOT NULL REFERENCES tax_zones(id),
    country CHAR(2) NOT NULL,
    postal_code_low TEXT NOT NULL,
    postal_code_high TEXT NOT NULL DEFAULT '',
    extension_low TEXT NOT NULL DEFAULT '',
    extension_high TEXT NOT NULL DEFAULT '',
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

COMMENT ON TABLE tax_zone_boundaries IS
    'The postal code, or postal code range, a zone covers; low equals high for an exact code.';

CREATE INDEX IF NOT EXISTS tax_zone_boundaries_lookup
    ON tax_zone_boundaries (country, postal_code_low, postal_code_high);
CREATE INDEX IF NOT EXISTS tax_zones_country
    ON tax_zones (country, jurisdiction_type);

-- The zone arm of the resolver writes a new tax source value.
ALTER TABLE orders DROP CONSTRAINT IF EXISTS orders_tax_source_check;
ALTER TABLE orders
    ADD CONSTRAINT orders_tax_source_check
        CHECK (tax_source IN ('EXEMPT', 'PROVIDER', 'ZONE_RATE', 'SHIP_TO_RATE', 'BRANCH_RATE', 'LEGACY'));

ALTER TABLE invoices DROP CONSTRAINT IF EXISTS invoices_tax_source_check;
ALTER TABLE invoices
    ADD CONSTRAINT invoices_tax_source_check
        CHECK (tax_source IN ('EXEMPT', 'PROVIDER', 'ZONE_RATE', 'SHIP_TO_RATE', 'BRANCH_RATE', 'LEGACY'));

-- The documents keep the zone and the rate parts that answered, frozen at
-- the sale: the zone row can change later, the sale's rate may not.
ALTER TABLE orders
    ADD COLUMN IF NOT EXISTS tax_zone_id UUID NULL,
    ADD COLUMN IF NOT EXISTS tax_rate_parts JSONB NULL,
    ADD COLUMN IF NOT EXISTS tax_zone_source TEXT NULL;

ALTER TABLE invoices
    ADD COLUMN IF NOT EXISTS tax_zone_id UUID NULL,
    ADD COLUMN IF NOT EXISTS tax_rate_parts JSONB NULL,
    ADD COLUMN IF NOT EXISTS tax_zone_source TEXT NULL;

-- A counter sale can be a delivery: it names a ship to and its own delivery
-- type, so its tax is sourced to the ship to's zone instead of the branch.
ALTER TABLE pos_transactions
    ADD COLUMN IF NOT EXISTS ship_to_id UUID NULL,
    ADD COLUMN IF NOT EXISTS ship_to_snapshot JSONB NULL,
    ADD COLUMN IF NOT EXISTS delivery_type TEXT NOT NULL DEFAULT 'PICKUP'
        CHECK (delivery_type IN ('PICKUP', 'DELIVERY'));
