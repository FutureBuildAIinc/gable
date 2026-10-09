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
--   indexes     the keyset index each list's ordering needs, leading with
--               the column the list is scoped by (customer_id for the
--               activities and the portal's projects, category for the
--               options), so the planner walks one customer's or one
--               category's page from the index instead of a bitmap scan
--               plus sort over the whole table.
--
-- No document numbers: an activity, a project and a millwork option are not
-- externally addressable documents (ADR 0001 section 8 names quotes, orders,
-- invoices and purchase orders); none of the three tables has a number
-- column to adopt or collide with. No unit price columns are widened: none
-- of the three exposes one. crm_activities.description becomes NOT NULL with
-- legacy NULLs backfilled to the empty string, the value the old Go zero
-- value already wrote on the wire.
--
-- crm_activities.activity_type is normalised before it is constrained: the
-- column was unconstrained TEXT, and the wire's closed vocabulary cannot
-- read a stray spelling. Two rewrites, in order: every stored value is
-- uppercased and trimmed (a lowercase 'call' and a padded '  meeting  '
-- become their storage spellings), then whatever is still outside the four
-- storage values (an empty string, a mixed-case word, a NULL on a schema
-- that drifted nullable) maps to NOTE, the catch-all the old free-text
-- column always meant. A CHECK then holds the column to the four values,
-- so the list's exact-match activity_type filter and the wire's vocabulary
-- agree with what is stored from here on.

-- 1. crm_activities.
UPDATE crm_activities SET created_at = COALESCE(activity_date, updated_at, NOW()) WHERE created_at IS NULL;
ALTER TABLE crm_activities ALTER COLUMN created_at SET NOT NULL;
-- activity_date is read into a non-nullable Go value; a NULL failed the
-- scan and took the customer's whole activity feed down. A NULL takes
-- created_at, which the line above has just backfilled from the row's own
-- times (activity_date first, then updated_at, then NOW()), so a legacy row
-- without a date lands on its own recorded time, never on the migration's
-- clock.
UPDATE crm_activities SET activity_date = COALESCE(activity_date, created_at) WHERE activity_date IS NULL;
ALTER TABLE crm_activities ALTER COLUMN activity_date SET NOT NULL;
UPDATE crm_activities SET description = '' WHERE description IS NULL;
ALTER TABLE crm_activities ALTER COLUMN description SET NOT NULL;
UPDATE crm_activities SET activity_type = upper(btrim(activity_type));
UPDATE crm_activities SET activity_type = 'NOTE'
    WHERE activity_type IS NULL OR activity_type NOT IN ('CALL', 'MEETING', 'EMAIL', 'NOTE');
ALTER TABLE crm_activities DROP CONSTRAINT IF EXISTS crm_activities_activity_type_check;
ALTER TABLE crm_activities ADD CONSTRAINT crm_activities_activity_type_check
    CHECK (activity_type IN ('CALL', 'MEETING', 'EMAIL', 'NOTE'));
ALTER TABLE crm_activities ADD COLUMN IF NOT EXISTS revision BIGINT NOT NULL DEFAULT 1;
CREATE INDEX IF NOT EXISTS idx_crm_activities_customer_created_at_id_desc
    ON crm_activities (customer_id, created_at DESC, id DESC);

-- 2. projects (the one job table since 091; the data 091 moved stays as it is).
UPDATE projects SET created_at = COALESCE(updated_at, NOW()) WHERE created_at IS NULL;
ALTER TABLE projects ALTER COLUMN created_at SET NOT NULL;
ALTER TABLE projects ADD COLUMN IF NOT EXISTS revision BIGINT NOT NULL DEFAULT 1;
CREATE INDEX IF NOT EXISTS idx_projects_customer_created_at_id_desc
    ON projects (customer_id, created_at DESC, id DESC);

-- 3. millwork_options.
UPDATE millwork_options SET created_at = COALESCE(updated_at, NOW()) WHERE created_at IS NULL;
ALTER TABLE millwork_options ALTER COLUMN created_at SET NOT NULL;
-- price_adjustment is read into a non-nullable Go value (scaled to cents in
-- SQL); a NULL failed the scan and 500ed the whole category's list. A NULL
-- takes 0, the identity of an adjustment (no price change) and the column's
-- own default; nothing but a direct insert naming NULL can have written one.
UPDATE millwork_options SET price_adjustment = 0 WHERE price_adjustment IS NULL;
ALTER TABLE millwork_options ALTER COLUMN price_adjustment SET NOT NULL;
ALTER TABLE millwork_options ADD COLUMN IF NOT EXISTS revision BIGINT NOT NULL DEFAULT 1;
CREATE INDEX IF NOT EXISTS idx_millwork_options_category_created_at_id_desc
    ON millwork_options (category, created_at DESC, id DESC);
