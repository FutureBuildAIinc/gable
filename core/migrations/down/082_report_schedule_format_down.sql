-- SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
-- SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

-- Reverse of migrations/082_report_schedule_format.sql.
--
-- WHY THIS FILE IS NOT A SIBLING OF THE UP MIGRATION
-- `cmd/migrate` applies every file matched by `migrations/*.sql` in sorted
-- order and records it in schema_migrations. An `082_..._down.sql` placed next
-- to the up migration would therefore be executed as a FORWARD migration
-- immediately after 082 and would drop the column 082 had just added.
-- `migrations/down/` is outside that glob, so down scripts live here.
--
-- This is NOT run automatically. Apply it by hand, then delete the tracking row:
--
--   psql "$DATABASE_URL" -f migrations/down/082_report_schedule_format_down.sql
--   psql "$DATABASE_URL" -c "DELETE FROM schema_migrations WHERE version = '082_report_schedule_format.sql';"
--
-- DESTRUCTIVE: dropping the column discards the output format chosen for every
-- existing schedule. Take a backup first.

BEGIN;

ALTER TABLE report_schedules DROP COLUMN IF EXISTS format;

COMMIT;
