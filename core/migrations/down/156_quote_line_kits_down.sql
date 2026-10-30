-- SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
-- SPDX-FileCopyrightText: 2026, FutureBuild, Inc. and OpenLBM contributors

-- Rollback for 156_quote_line_kits.sql.
--
-- Lives in migrations/down/ so cmd/migrate's `migrations/*.sql` glob cannot
-- pick it up and apply it as a forward migration. Apply by hand.
--
-- A kit quote's COMPONENT rows go with the columns that describe them; the
-- kit lines themselves stay as the flat product lines they were before the
-- kit work, which is what a rolled back deployment's code expects to read.

ALTER TABLE quote_lines DROP CONSTRAINT IF EXISTS quote_lines_component_parent_check;
ALTER TABLE quote_lines DROP CONSTRAINT IF EXISTS quote_lines_line_type_check;
ALTER TABLE quote_lines DROP COLUMN IF EXISTS parent_line_id;
ALTER TABLE quote_lines DROP COLUMN IF EXISTS line_type;
