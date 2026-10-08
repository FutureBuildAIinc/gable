-- SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
-- SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

-- 096: crm activities, projects and millwork options onto the wire contract
-- (item C5-1b, docs/adr/0001-wire-contract.md).
--
-- The migration note's steps 1, 2 and 6 for three tables, in one file:
--
--   created_at  filled and set NOT NULL first: each list's keyset ordering
--               (created_at, id) reads it, and a nullable ordering column
--               has no cursor position (ADR 0001 section 2).
--   revision    BIGINT NOT NULL DEFAULT 1: every existing row starts at
--               revision 1, so the If-Match precondition has a value to
--               check from the first write (section 11).
--   indexes     the keyset index each list's ordering needs.
--
-- No document numbers: an activity, a project and a millwork option are not
-- externally addressable documents (ADR 0001 section 8 names quotes, orders,
-- invoices and purchase orders); none of the three tables has a number
-- column to adopt or collide with. No unit price columns are widened: none
-- of the three exposes one. crm_activities.description becomes NOT NULL with
-- legacy NULLs backfilled to the empty string, the value the old Go zero
-- value already wrote on the wire.

-- 1. crm_activities.
UPDATE crm_activities SET created_at = COALESCE(activity_date, updated_at, NOW()) WHERE created_at IS NULL;
ALTER TABLE crm_activities ALTER COLUMN created_at SET NOT NULL;
UPDATE crm_activities SET description = '' WHERE description IS NULL;
ALTER TABLE crm_activities ALTER COLUMN description SET NOT NULL;
ALTER TABLE crm_activities ADD COLUMN IF NOT EXISTS revision BIGINT NOT NULL DEFAULT 1;
CREATE INDEX IF NOT EXISTS idx_crm_activities_created_at_id_desc
    ON crm_activities (created_at DESC, id DESC);

-- 2. projects (the one job table since 091; the data 091 moved stays as it is).
UPDATE projects SET created_at = COALESCE(updated_at, NOW()) WHERE created_at IS NULL;
ALTER TABLE projects ALTER COLUMN created_at SET NOT NULL;
ALTER TABLE projects ADD COLUMN IF NOT EXISTS revision BIGINT NOT NULL DEFAULT 1;
CREATE INDEX IF NOT EXISTS idx_projects_created_at_id_desc
    ON projects (created_at DESC, id DESC);

-- 3. millwork_options.
UPDATE millwork_options SET created_at = COALESCE(updated_at, NOW()) WHERE created_at IS NULL;
ALTER TABLE millwork_options ALTER COLUMN created_at SET NOT NULL;
ALTER TABLE millwork_options ADD COLUMN IF NOT EXISTS revision BIGINT NOT NULL DEFAULT 1;
CREATE INDEX IF NOT EXISTS idx_millwork_options_created_at_id_desc
    ON millwork_options (created_at DESC, id DESC);
