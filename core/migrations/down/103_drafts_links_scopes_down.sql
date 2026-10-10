-- SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
-- SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

-- Down of 103_drafts_links_scopes, in reverse order. Everything here is this
-- migration's own artifact (no table existed before it, no backfill ran), so
-- the drops lose no row the base ever owned: drafts and their change rows
-- are products of this feature alone, and api_keys.branch_id was NULL on
-- every pre-existing key.
--
-- One exception is not a row but a confinement: a key minted bound to a
-- branch after 103 keeps its row and its scopes when branch_id is dropped,
-- and the base has no notion of a pin, so it would reach every branch its
-- scopes allow. Each such key still active is revoked first, with a notice
-- naming it, so a roll back never widens a key's reach.

DROP INDEX IF EXISTS idx_draft_events_subject_position;
DROP INDEX IF EXISTS idx_draft_events_module_position;
DROP TRIGGER IF EXISTS draft_events_assign_position_before_insert ON draft_events;
DROP FUNCTION IF EXISTS draft_events_assign_position();
DROP TABLE IF EXISTS draft_events;
DROP SEQUENCE IF EXISTS draft_events_position_seq;

DROP TABLE IF EXISTS draft_events_purged;

DO $$
DECLARE
    k RECORD;
BEGIN
    IF EXISTS (SELECT 1 FROM information_schema.columns
               WHERE table_name = 'api_keys' AND column_name = 'branch_id') THEN
        FOR k IN EXECUTE 'SELECT id, key_prefix FROM api_keys WHERE branch_id IS NOT NULL AND revoked_at IS NULL ORDER BY id' LOOP
            RAISE NOTICE 'api key % (prefix %) is bound to a branch the base cannot enforce: revoked; re-mint it after the roll back', k.id, k.key_prefix;
        END LOOP;
        EXECUTE 'UPDATE api_keys SET revoked_at = now() WHERE branch_id IS NOT NULL AND revoked_at IS NULL';
    END IF;
END $$;

ALTER TABLE api_keys DROP COLUMN IF EXISTS branch_id;

DROP INDEX IF EXISTS idx_drafts_promoted_entity;
DROP INDEX IF EXISTS idx_drafts_open_subject;
DROP INDEX IF EXISTS idx_drafts_module_created;
DROP TABLE IF EXISTS drafts;
