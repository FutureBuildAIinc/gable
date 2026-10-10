-- SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
-- SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

-- 102: updated_at NOT NULL on crm_activities, projects and millwork_options
-- (C5-1b P3-1, the review round 3 follow up of PR 47).
--
-- 096 backfilled created_at, activity_date, description and price_adjustment,
-- and set them NOT NULL, but left updated_at nullable. The repositories in
-- crm, project and millwork all read updated_at into a non-nullable Go
-- time.Time, so a single row with NULL updated_at takes the whole list down
-- (GET /api/v1/activities/{id}, GET /api/v1/customers/{id}/activities,
-- GET /api/portal/v1/projects, GET /api/portal/v1/projects/{id} and
-- GET /api/v1/millwork/options each answer 500). A plain insert never
-- writes NULL there (the column defaults to NOW()), so the only path in is
-- a raw insert, but the schema leaves the door open.
--
-- For each of the three tables, a NULL updated_at takes the row's own
-- created_at: created_at is NOT NULL by 096, and the row's recorded time is
-- the right anchor for a column whose default is the same. The column is
-- then SET NOT NULL, so a later raw insert cannot reintroduce the 500. No
-- new index, no new constraint, no behaviour change for a row that never
-- held NULL.

-- 1. crm_activities.
UPDATE crm_activities SET updated_at = COALESCE(updated_at, created_at) WHERE updated_at IS NULL;
ALTER TABLE crm_activities ALTER COLUMN updated_at SET NOT NULL;

-- 2. projects.
UPDATE projects SET updated_at = COALESCE(updated_at, created_at) WHERE updated_at IS NULL;
ALTER TABLE projects ALTER COLUMN updated_at SET NOT NULL;

-- 3. millwork_options.
UPDATE millwork_options SET updated_at = COALESCE(updated_at, created_at) WHERE updated_at IS NULL;
ALTER TABLE millwork_options ALTER COLUMN updated_at SET NOT NULL;
