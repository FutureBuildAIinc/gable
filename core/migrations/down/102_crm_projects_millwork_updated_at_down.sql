-- SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
-- SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

-- Rollback for 102_crm_projects_millwork_updated_at.sql.
--
-- Lives in migrations/down/ so cmd/migrate's `migrations/*.sql` glob cannot
-- pick it up and apply it as a forward migration. Apply by hand.
--
-- What does not come back: a row whose updated_at was filled from
-- created_at keeps its filled value (which row held a NULL is not
-- recorded), so a re-up is idempotent and a re-down is a no-op on the
-- filled rows. The column's default of NOW() is unchanged.

ALTER TABLE crm_activities ALTER COLUMN updated_at DROP NOT NULL;
ALTER TABLE projects ALTER COLUMN updated_at DROP NOT NULL;
ALTER TABLE millwork_options ALTER COLUMN updated_at DROP NOT NULL;
