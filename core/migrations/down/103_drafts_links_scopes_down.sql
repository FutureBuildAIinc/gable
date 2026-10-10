-- SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
-- SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

-- Down of 103_drafts_links_scopes, in reverse order. Everything here is this
-- migration's own artifact (no table existed before it, no backfill ran), so
-- the drops lose no row the base ever owned: drafts and their change rows
-- are products of this feature alone, and api_keys.branch_id was NULL on
-- every pre-existing key.

DROP INDEX IF EXISTS idx_draft_events_subject_position;
DROP INDEX IF EXISTS idx_draft_events_module_position;
DROP TRIGGER IF EXISTS draft_events_assign_position_before_insert ON draft_events;
DROP FUNCTION IF EXISTS draft_events_assign_position();
DROP TABLE IF EXISTS draft_events;
DROP SEQUENCE IF EXISTS draft_events_position_seq;

DROP TABLE IF EXISTS draft_events_purged;

ALTER TABLE api_keys DROP COLUMN IF EXISTS branch_id;

DROP INDEX IF EXISTS idx_drafts_promoted_entity;
DROP INDEX IF EXISTS idx_drafts_open_subject;
DROP INDEX IF EXISTS idx_drafts_module_created;
DROP TABLE IF EXISTS drafts;
