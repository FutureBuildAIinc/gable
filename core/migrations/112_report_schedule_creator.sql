-- SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
-- SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

-- Migration: 112_report_schedule_creator
-- Description: a scheduled report ran on a context free of the branch
-- middleware and read every branch, so a finance user granted one branch
-- could schedule a report whose emailed run carried every branch's rows
-- (SEC-NOHEADER-1, round 3). This records the creating user on the schedule
-- row, the two facts the run resolves the creator's reach from: created_by
-- is the caller's subject, the same text user_locations keys grants by, and
-- creator_is_admin records that the creator held the administrator or owner
-- role when the schedule was made (the repository has no users table, so
-- the role cannot be re-read later). Both statements are IF NOT EXISTS and
-- the boolean carries a default, so the migration is idempotent and a rerun
-- changes nothing. A row that predates the migration has no creator
-- recorded and never runs until a caller with report access recreates the
-- schedule: that is the fail closed rule, not a backfill, because guessing
-- a creator would hand someone's report reach to a row nobody made.

ALTER TABLE report_schedules ADD COLUMN IF NOT EXISTS created_by TEXT;
ALTER TABLE report_schedules ADD COLUMN IF NOT EXISTS creator_is_admin BOOLEAN NOT NULL DEFAULT FALSE;
