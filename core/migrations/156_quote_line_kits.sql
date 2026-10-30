-- SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
-- SPDX-FileCopyrightText: 2026, FutureBuild, Inc. and OpenLBM contributors

-- 156: a kit on a quote explodes into its components (QE-05-a).
--
-- quote_lines takes the line kind and the parent link order_lines has had
-- since 092 (ADR 0005 sections 2.1 and 2.6): a kit product on a quote is one
-- KIT line, priced as a whole, followed by one COMPONENT line per component
-- of the kit's definition, its parent the kit line. Every existing row is a
-- plain product line and backfills to PRODUCT, the default. A quote has no
-- charge or text lines (its freight rides the header), so the vocabulary is
-- the three the document writes.
--
-- The component rules match order_lines': a COMPONENT line always names its
-- parent, and no other line type does.

ALTER TABLE quote_lines ADD COLUMN IF NOT EXISTS line_type TEXT NOT NULL DEFAULT 'PRODUCT';
ALTER TABLE quote_lines ADD COLUMN IF NOT EXISTS parent_line_id UUID REFERENCES quote_lines(id) ON DELETE CASCADE;

DO $$
BEGIN
    IF NOT EXISTS (SELECT 1 FROM pg_constraint WHERE conname = 'quote_lines_line_type_check') THEN
        ALTER TABLE quote_lines
            ADD CONSTRAINT quote_lines_line_type_check
            CHECK (line_type IN ('PRODUCT', 'KIT', 'COMPONENT'));
    END IF;
    IF NOT EXISTS (SELECT 1 FROM pg_constraint WHERE conname = 'quote_lines_component_parent_check') THEN
        ALTER TABLE quote_lines
            ADD CONSTRAINT quote_lines_component_parent_check
            CHECK ((line_type = 'COMPONENT') = (parent_line_id IS NOT NULL));
    END IF;
END
$$;
