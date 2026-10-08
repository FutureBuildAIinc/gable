-- SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
-- SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

-- Rollback for 096_crm_projects_millwork_wire_contract.sql.
--
-- Lives in migrations/down/ so cmd/migrate's `migrations/*.sql` glob cannot
-- pick it up and apply it as a forward migration. Apply by hand.
--
-- What does not come back: the revision columns and the keyset indexes are
-- dropped, created_at stays NOT NULL (a NULL ordering column cannot be
-- restored without inventing times), and a description backfilled from NULL
-- to the empty string stays empty (which NULL was is not recorded).

DROP INDEX IF EXISTS idx_millwork_options_created_at_id_desc;
ALTER TABLE millwork_options DROP COLUMN IF EXISTS revision;

DROP INDEX IF EXISTS idx_projects_created_at_id_desc;
ALTER TABLE projects DROP COLUMN IF EXISTS revision;

DROP INDEX IF EXISTS idx_crm_activities_created_at_id_desc;
ALTER TABLE crm_activities DROP COLUMN IF EXISTS revision;
ALTER TABLE crm_activities ALTER COLUMN description DROP NOT NULL;
