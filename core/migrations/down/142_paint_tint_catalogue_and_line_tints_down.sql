-- SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
-- SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

-- Down 142: the tint tables go whole (every row they hold is this
-- migration's own), the return line's two flags go, and the two seeded
-- volume units go only while nothing names them.

ALTER TABLE pos_return_lines
    DROP COLUMN IF EXISTS mis_tint,
    DROP COLUMN IF EXISTS tinted_return;

DROP TABLE IF EXISTS paint_line_tint_components;
DROP TABLE IF EXISTS paint_line_tints;
DROP TABLE IF EXISTS paint_saved_colors;
DROP TABLE IF EXISTS paint_formula_components;
DROP TABLE IF EXISTS paint_formulas;
DROP TABLE IF EXISTS paint_colors;
DROP TABLE IF EXISTS paint_colorants;
DROP TABLE IF EXISTS paint_bases;
DROP TABLE IF EXISTS paint_tint_systems;

DO $$
BEGIN
    IF EXISTS (SELECT 1 FROM product_units WHERE uom IN ('QT', 'PT')) THEN
        RAISE EXCEPTION 'the QT or PT unit is named by a product unit set: the down cannot remove it';
    END IF;
    DELETE FROM units WHERE code IN ('QT', 'PT') AND is_system;
END $$;
