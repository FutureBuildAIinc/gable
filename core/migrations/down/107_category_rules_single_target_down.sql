-- SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
-- SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

-- Rollback for 107_category_rules_single_target.sql.
--
-- Lives in migrations/down/ so cmd/migrate's `migrations/*.sql` glob cannot
-- pick it up and apply it as a forward migration. Apply by hand.
--
-- What does not come back: nothing. The constraint goes entirely; no row was
-- rewritten by the up migration, so there is no data to restore.

ALTER TABLE category_pricing_rules
    DROP CONSTRAINT IF EXISTS category_pricing_rules_single_target_check;
