-- SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
-- SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

-- Rollback of 112_report_schedule_creator: drops the creator columns the
-- scheduled run resolves its reach from. Idempotent, and it deletes no
-- schedule rows: only the two columns go.

ALTER TABLE report_schedules DROP COLUMN IF EXISTS created_by;
ALTER TABLE report_schedules DROP COLUMN IF EXISTS creator_is_admin;
