-- SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
-- SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

-- Rollback for 096_crm_projects_millwork_wire_contract.sql.
--
-- Lives in migrations/down/ so cmd/migrate's `migrations/*.sql` glob cannot
-- pick it up and apply it as a forward migration. Apply by hand.
--
-- What does not come back: the revision columns and the keyset indexes are
-- dropped, created_at stays NOT NULL (a NULL ordering column cannot be
-- restored without inventing times), a description backfilled from NULL to
-- the empty string stays empty, an activity_date filled from created_at and
-- a price_adjustment filled with 0 keep their filled values (which NULL was
-- is not recorded), and an activity_type that was normalised stays
-- normalised (which spelling a row carried is not recorded; a NOTE mapped
-- from something else cannot be told from a NOTE that was always a NOTE).

DROP INDEX IF EXISTS idx_millwork_options_category_created_at_id_desc;
ALTER TABLE millwork_options DROP COLUMN IF EXISTS revision;
ALTER TABLE millwork_options ALTER COLUMN price_adjustment DROP NOT NULL;

DROP INDEX IF EXISTS idx_projects_customer_created_at_id_desc;
ALTER TABLE projects DROP COLUMN IF EXISTS revision;

DROP INDEX IF EXISTS idx_crm_activities_customer_created_at_id_desc;
ALTER TABLE crm_activities DROP COLUMN IF EXISTS revision;
ALTER TABLE crm_activities DROP CONSTRAINT IF EXISTS crm_activities_activity_type_check;
ALTER TABLE crm_activities ALTER COLUMN activity_date DROP NOT NULL;
ALTER TABLE crm_activities ALTER COLUMN description DROP NOT NULL;
