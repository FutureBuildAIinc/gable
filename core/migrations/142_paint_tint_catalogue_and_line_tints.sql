-- SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
-- SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

-- 142: paint tinting (row CP-16). Gable's own tint formula catalogue, the
-- counter tint line that freezes the base, the colour and the formula on the
-- sale, the mis tint columns on a return line, and the paint volume units
-- (QT, PT). No dispenser or colour matching device link: the catalogue is
-- data the dealer owns or creates (a manufacturer's formula book is the
-- manufacturer's trade data, so Gable ships the tables and the entry paths,
-- never a populated book).
--
-- A colourant amount is three integer columns with the dispense denominator
-- beside them (whole ounces, a coarse 1/32 count, a fine 1/denominator
-- count), exactly as the manufacturer guides print them. It is never a line
-- quantity: the line's quantity is the container, and the scale 4 quantity
-- type cannot hold 1/384 of a fluid ounce anyway.
--
-- A formula is stored per (colour, base, container size). The sale freezes
-- the amounts actually sold in paint_line_tint_components, so a later
-- catalogue edit cannot rewrite history.

-- The tint systems ---------------------------------------------------------

CREATE TABLE IF NOT EXISTS paint_tint_systems (
    id               UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    code             TEXT NOT NULL UNIQUE CHECK (code ~ '^[A-Za-z0-9_-]{1,32}$'),
    name             TEXT NOT NULL CHECK (char_length(name) BETWEEN 1 AND 120),
    manufacturer     TEXT NOT NULL DEFAULT '' CHECK (char_length(manufacturer) <= 120),
    unit_denominator INTEGER NOT NULL CHECK (unit_denominator IN (32, 48, 64, 128, 192, 384, 1024)),
    unit_label       TEXT NOT NULL DEFAULT '' CHECK (char_length(unit_label) <= 40),
    created_at       TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

-- The bases: one row per base paint a system mixes in. The optional product
-- link is the stocked base paint can the counter sells.
CREATE TABLE IF NOT EXISTS paint_bases (
    id                   UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    tint_system_id       UUID NOT NULL REFERENCES paint_tint_systems(id) ON DELETE CASCADE,
    code                 TEXT NOT NULL CHECK (code ~ '^[A-Za-z0-9_-]{1,32}$'),
    name                 TEXT NOT NULL CHECK (char_length(name) BETWEEN 1 AND 120),
    manufacturer_base_code TEXT NOT NULL DEFAULT '' CHECK (char_length(manufacturer_base_code) <= 40),
    product_id           UUID NULL REFERENCES products(id) ON DELETE SET NULL,
    fill_volume_oz       INTEGER NOT NULL CHECK (fill_volume_oz > 0),
    is_deepest           BOOLEAN NOT NULL DEFAULT FALSE,
    created_at           TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    UNIQUE (tint_system_id, code)
);

-- The colorants of a system: the colourant set is tied to the system, not
-- to the dealer.
CREATE TABLE IF NOT EXISTS paint_colorants (
    id               UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    tint_system_id   UUID NOT NULL REFERENCES paint_tint_systems(id) ON DELETE CASCADE,
    code             TEXT NOT NULL CHECK (code ~ '^[A-Za-z0-9_-]{1,16}$'),
    name             TEXT NOT NULL CHECK (char_length(name) BETWEEN 1 AND 120),
    manufacturer_code TEXT NOT NULL DEFAULT '' CHECK (char_length(manufacturer_code) <= 40),
    manufacturer_upc  TEXT NULL CHECK (manufacturer_upc IS NULL OR manufacturer_upc ~ '^[0-9]{6,14}$'),
    created_at       TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    UNIQUE (tint_system_id, code)
);

-- The colours. A manufacturer code is an attribute, not a key: the same
-- digits are a different colour on a different base or brand. A custom
-- colour has no manufacturer code and carries the customer it was saved for.
CREATE TABLE IF NOT EXISTS paint_colors (
    id                UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    tint_system_id    UUID NOT NULL REFERENCES paint_tint_systems(id) ON DELETE CASCADE,
    manufacturer_code TEXT NULL CHECK (manufacturer_code IS NULL OR char_length(manufacturer_code) <= 40),
    name              TEXT NOT NULL CHECK (char_length(name) BETWEEN 1 AND 120),
    is_custom         BOOLEAN NOT NULL DEFAULT FALSE,
    customer_id       UUID NULL REFERENCES customers(id) ON DELETE SET NULL,
    project_id        UUID NULL REFERENCES projects(id) ON DELETE SET NULL,
    note              TEXT NOT NULL DEFAULT '' CHECK (char_length(note) <= 2000),
    created_at        TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
CREATE UNIQUE INDEX IF NOT EXISTS paint_colors_system_code
    ON paint_colors (tint_system_id, manufacturer_code) WHERE manufacturer_code IS NOT NULL;

-- The formulas: one row per (colour, base, container size), the shape the
-- manufacturer books publish. The source records whether the dealer entered
-- it from the book, from a match of its own, or by hand.
CREATE TABLE IF NOT EXISTS paint_formulas (
    id                   UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    color_id             UUID NOT NULL REFERENCES paint_colors(id) ON DELETE CASCADE,
    base_id              UUID NOT NULL REFERENCES paint_bases(id) ON DELETE CASCADE,
    product_id           UUID NULL REFERENCES products(id) ON DELETE SET NULL,
    container_size_code  TEXT NOT NULL CHECK (char_length(container_size_code) BETWEEN 1 AND 16),
    container_volume_oz  INTEGER NOT NULL CHECK (container_volume_oz > 0),
    total_colorant_oz    NUMERIC(10,4) NOT NULL DEFAULT 0 CHECK (total_colorant_oz >= 0),
    source               TEXT NOT NULL DEFAULT 'manual' CHECK (source IN ('book', 'match', 'manual')),
    created_at           TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    UNIQUE (color_id, base_id, container_size_code),
    CHECK (base_id IS NOT NULL AND container_volume_oz > 0)
);

-- The formula components, one row per colorant, in guide order. The
-- denominator is carried on each row so two systems' formulas sit in the
-- same table without colliding.
CREATE TABLE IF NOT EXISTS paint_formula_components (
    id                UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    formula_id        UUID NOT NULL REFERENCES paint_formulas(id) ON DELETE CASCADE,
    colorant_id       UUID NOT NULL REFERENCES paint_colorants(id) ON DELETE RESTRICT,
    position          INTEGER NOT NULL CHECK (position >= 0),
    whole_oz          INTEGER NOT NULL CHECK (whole_oz >= 0),
    increments_coarse INTEGER NOT NULL CHECK (increments_coarse >= 0),
    increments_fine   INTEGER NOT NULL CHECK (increments_fine >= 0),
    unit_denominator  INTEGER NOT NULL CHECK (unit_denominator IN (32, 48, 64, 128, 192, 384, 1024)),
    UNIQUE (formula_id, colorant_id)
);

-- The sale line's tint record: the child table pattern of line_tally_rows,
-- one line reference per row. The row snapshots the colour and base text and
-- names the formula it came from; the amounts actually sold live in the
-- components child table, frozen at the sale.
CREATE TABLE IF NOT EXISTS paint_line_tints (
    id                   UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    counter_line_id      UUID NULL REFERENCES pos_line_items(id) ON DELETE CASCADE,
    order_line_id        UUID NULL REFERENCES order_lines(id) ON DELETE CASCADE,
    invoice_line_id      UUID NULL REFERENCES invoice_lines(id) ON DELETE CASCADE,
    credit_memo_line_id  UUID NULL REFERENCES credit_memo_lines(id) ON DELETE CASCADE,
    formula_id           UUID NULL REFERENCES paint_formulas(id) ON DELETE SET NULL,
    color_code           TEXT NOT NULL DEFAULT '' CHECK (char_length(color_code) <= 40),
    color_name           TEXT NOT NULL CHECK (char_length(color_name) BETWEEN 1 AND 120),
    base_code            TEXT NOT NULL DEFAULT '' CHECK (char_length(base_code) <= 32),
    container_size_code  TEXT NOT NULL CHECK (char_length(container_size_code) BETWEEN 1 AND 16),
    rescaled             BOOLEAN NOT NULL DEFAULT FALSE,
    tint_charge_cents    NUMERIC(12,2) NOT NULL DEFAULT 0 CHECK (tint_charge_cents >= 0),
    created_at           TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    CONSTRAINT paint_line_tints_one_line CHECK (
        (CASE WHEN counter_line_id IS NULL THEN 0 ELSE 1 END
       + CASE WHEN order_line_id IS NULL THEN 0 ELSE 1 END
       + CASE WHEN invoice_line_id IS NULL THEN 0 ELSE 1 END
       + CASE WHEN credit_memo_line_id IS NULL THEN 0 ELSE 1 END) = 1
    )
);
CREATE UNIQUE INDEX IF NOT EXISTS paint_line_tints_counter_line
    ON paint_line_tints (counter_line_id) WHERE counter_line_id IS NOT NULL;
CREATE UNIQUE INDEX IF NOT EXISTS paint_line_tints_order_line
    ON paint_line_tints (order_line_id) WHERE order_line_id IS NOT NULL;
CREATE UNIQUE INDEX IF NOT EXISTS paint_line_tints_invoice_line
    ON paint_line_tints (invoice_line_id) WHERE invoice_line_id IS NOT NULL;
CREATE UNIQUE INDEX IF NOT EXISTS paint_line_tints_credit_memo_line
    ON paint_line_tints (credit_memo_line_id) WHERE credit_memo_line_id IS NOT NULL;

-- The amounts actually sold, one row per colorant: what the counter mixed,
-- not what the catalogue holds today.
CREATE TABLE IF NOT EXISTS paint_line_tint_components (
    id                UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    line_tint_id      UUID NOT NULL REFERENCES paint_line_tints(id) ON DELETE CASCADE,
    position          INTEGER NOT NULL CHECK (position >= 0),
    colorant_code     TEXT NOT NULL CHECK (char_length(colorant_code) BETWEEN 1 AND 16),
    colorant_name     TEXT NOT NULL DEFAULT '' CHECK (char_length(colorant_name) <= 120),
    whole_oz          INTEGER NOT NULL CHECK (whole_oz >= 0),
    increments_coarse INTEGER NOT NULL CHECK (increments_coarse >= 0),
    increments_fine   INTEGER NOT NULL CHECK (increments_fine >= 0),
    unit_denominator  INTEGER NOT NULL CHECK (unit_denominator IN (32, 48, 64, 128, 192, 384, 1024))
);

-- The saved colours per customer: the store's own record that makes a repeat
-- order possible. job_note is free text the counter writes on the spot (a
-- room or a job site), deliberately not the projects row; project_id stays
-- as an optional link nothing depends on.
CREATE TABLE IF NOT EXISTS paint_saved_colors (
    id           UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    customer_id  UUID NOT NULL REFERENCES customers(id) ON DELETE CASCADE,
    color_id     UUID NOT NULL REFERENCES paint_colors(id) ON DELETE CASCADE,
    name         TEXT NOT NULL CHECK (char_length(name) BETWEEN 1 AND 120),
    job_note     TEXT NOT NULL DEFAULT '' CHECK (char_length(job_note) <= 200),
    note         TEXT NOT NULL DEFAULT '' CHECK (char_length(note) <= 2000),
    project_id   UUID NULL REFERENCES projects(id) ON DELETE SET NULL,
    created_by   TEXT NOT NULL DEFAULT '' CHECK (char_length(created_by) <= 120),
    created_at   TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    UNIQUE (customer_id, name)
);

-- The return line's mis tint record: a mis tint return (the store's own
-- error) is accepted where a customer choice return of a tinted line is
-- refused, and a tinted can the store does take back is marked so it goes
-- to the mistint rack, never back into saleable stock.
ALTER TABLE pos_return_lines
    ADD COLUMN IF NOT EXISTS mis_tint BOOLEAN NOT NULL DEFAULT FALSE,
    ADD COLUMN IF NOT EXISTS tinted_return BOOLEAN NOT NULL DEFAULT FALSE;

-- The paint volume units (a quart is 4 to the gallon, a pint 8; the gallon's
-- own standard pair is 576 to 77, so these keep the same ratio exactly).
INSERT INTO units (code, name, dimension, std_unit_qty, std_ref_qty, is_system) VALUES
    ('QT', 'Quart (US)',  'VOLUME', 2304, 77, TRUE),
    ('PT', 'Pint (US)',   'VOLUME', 4608, 77, TRUE)
ON CONFLICT (code) DO NOTHING;
