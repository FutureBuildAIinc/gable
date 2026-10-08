-- SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
-- SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

-- Rollback for 090_quotes_wire_contract.sql.
--
-- Lives in migrations/down/ so cmd/migrate's `migrations/*.sql` glob cannot
-- pick it up and apply it as a forward migration. Apply by hand:
--
--   ALTER TABLE quote_lines DROP CONSTRAINT IF EXISTS quote_lines_conversion_positive;
--   ALTER TABLE quote_lines DROP COLUMN IF EXISTS position;
--   ALTER TABLE quote_lines DROP COLUMN IF EXISTS price_uom_qty;
--   ALTER TABLE quote_lines DROP COLUMN IF EXISTS uom_qty;
--   ALTER TABLE quote_lines DROP COLUMN IF EXISTS price_uom;
--   DROP INDEX IF EXISTS idx_quotes_created_id;
--   ALTER TABLE quotes DROP CONSTRAINT IF EXISTS quotes_number_key;
--   ALTER TABLE quotes DROP COLUMN IF EXISTS number;
--   DROP FUNCTION IF EXISTS quote_next_number();
--   DROP SEQUENCE IF EXISTS quote_number_seq;
--   ALTER TABLE quotes DROP COLUMN IF EXISTS revision;
--
-- created_at stays NOT NULL (the fill is not reversible and nothing relied on
-- NULLs). The drop loses every quote's document number and revision; the
-- quote documents themselves are untouched.

ALTER TABLE quote_lines DROP CONSTRAINT IF EXISTS quote_lines_conversion_positive;
ALTER TABLE quote_lines DROP COLUMN IF EXISTS position;
ALTER TABLE quote_lines DROP COLUMN IF EXISTS price_uom_qty;
ALTER TABLE quote_lines DROP COLUMN IF EXISTS uom_qty;
ALTER TABLE quote_lines DROP COLUMN IF EXISTS price_uom;
DROP INDEX IF EXISTS idx_quotes_created_id;
ALTER TABLE quotes DROP CONSTRAINT IF EXISTS quotes_number_key;
ALTER TABLE quotes DROP COLUMN IF EXISTS number;
DROP FUNCTION IF EXISTS quote_next_number();
DROP SEQUENCE IF EXISTS quote_number_seq;
ALTER TABLE quotes DROP COLUMN IF EXISTS revision;
