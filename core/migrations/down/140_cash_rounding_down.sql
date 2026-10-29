-- SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
-- SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

-- Rollback for 140_cash_rounding.sql.
--
-- Lives in migrations/down/ so cmd/migrate's `migrations/*.sql` glob cannot
-- pick it up and apply it as a forward migration. Apply by hand.
--
-- The branch's rule goes with the column, and the two rounding accounts go
-- only while no journal line uses them: an entry that carries a rounding
-- leg has to be reversed before the account it names can go.

ALTER TABLE locations DROP CONSTRAINT IF EXISTS locations_cash_rounding_mode_check;
ALTER TABLE locations DROP COLUMN IF EXISTS cash_rounding_mode;

DELETE FROM gl_accounts a
WHERE a.code IN ('4095', '5045')
  AND NOT EXISTS (SELECT 1 FROM gl_journal_lines l WHERE l.account_id = a.id);
