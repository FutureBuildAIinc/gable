-- SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
-- SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

-- C5-2a: drafts, links and confirm gated scopes (ADR 0007 section 9).
--
-- Five steps, every one idempotent, no backfill (every table is new and the
-- one new column is nullable with today's meaning):
--
--   1. drafts, one generic table for every draft kind. The payload is the
--      module's own request body as JSONB; the module's code decodes and
--      validates it. The module vocabulary is not a CHECK: it lives in code
--      (the kind registry) and a test holds it against the routes and the
--      scope vocabulary, the same reasoning as ADR 0002 section 7. The
--      *_kind columns carry the audit_log.actor_kind vocabulary. The status
--      shape constraints are the database's side of the lifecycle: PROMOTED
--      requires its provenance columns, DISCARDED its time, and an edit
--      draft (subject_id set) requires the subject revision it was built on.
--   2. draft_events, the draft change feed's rows, with a commit ordered
--      position drawn from its own sequence under its own transaction
--      scoped advisory lock (lock key 1685218678, the ASCII bytes 'drev',
--      distinct from the outbox's 'outb'), the 089 trigger's shape. Draft
--      saves are frequent and are not business facts, so they never share
--      the outbox's one lock (ADR 0007 section 3.1).
--   3. draft_events_purged, one row recording the highest purged position,
--      so a client resuming from a cursor at or below it can be told to
--      re-read (event: reset) instead of silently missing changes.
--   4. api_keys.branch_id, the branch bound keys column (ADR 0007 section
--      5.5): NULL is today's behaviour (unbound), a branch pins the key.
--   5. A read only report: a NOTICE naming, by id and prefix, every
--      unrevoked key whose scopes fall outside the section 5.1 grammar. It
--      changes nothing; the operator revokes and re-mints such a key, which
--      keeps exactly the reach it had (the scope check matches exact
--      strings).
--
-- Rollback: migrations/down/103_drafts_links_scopes_down.sql.

-- 1. drafts ------------------------------------------------------------

CREATE TABLE IF NOT EXISTS drafts (
    id                UUID PRIMARY KEY DEFAULT uuid_generate_v4(),
    module            TEXT NOT NULL,
    branch_id         UUID NOT NULL REFERENCES locations(id),
    subject_id        UUID,
    subject_revision  BIGINT,
    status            TEXT NOT NULL DEFAULT 'OPEN'
                      CONSTRAINT drafts_status_vocabulary
                      CHECK (status IN ('OPEN', 'PROMOTED', 'DISCARDED')),
    revision          BIGINT NOT NULL DEFAULT 1,
    payload           JSONB NOT NULL CONSTRAINT drafts_payload_is_object CHECK (jsonb_typeof(payload) = 'object'),
    created_by_kind   TEXT NOT NULL
                      CONSTRAINT drafts_created_by_kind_vocabulary
                      CHECK (created_by_kind IN ('user', 'key', 'agent', 'anonymous')),
    created_by_id     TEXT,
    created_acting_as TEXT,
    created_tool      TEXT,
    updated_by_kind   TEXT NOT NULL
                      CONSTRAINT drafts_updated_by_kind_vocabulary
                      CHECK (updated_by_kind IN ('user', 'key', 'agent', 'anonymous')),
    updated_by_id     TEXT,
    updated_acting_as TEXT,
    updated_tool      TEXT,
    promoted_entity_id UUID,
    promoted_number   TEXT,
    promoted_at       TIMESTAMPTZ,
    promoted_by_kind  TEXT
                      CONSTRAINT drafts_promoted_by_kind_vocabulary
                      CHECK (promoted_by_kind IS NULL OR promoted_by_kind IN ('user', 'key', 'agent', 'anonymous')),
    promoted_by_id    TEXT,
    promoted_acting_as TEXT,
    promoted_tool     TEXT,
    discarded_at      TIMESTAMPTZ,
    discarded_by_kind TEXT
                      CONSTRAINT drafts_discarded_by_kind_vocabulary
                      CHECK (discarded_by_kind IS NULL OR discarded_by_kind IN ('user', 'key', 'agent', 'anonymous')),
    discarded_by_id   TEXT,
    discarded_acting_as TEXT,
    discarded_tool    TEXT,
    created_at        TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at        TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    CONSTRAINT drafts_promoted_shape CHECK (
        status <> 'PROMOTED' OR (promoted_entity_id IS NOT NULL AND promoted_at IS NOT NULL)
    ),
    CONSTRAINT drafts_discarded_shape CHECK (
        status <> 'DISCARDED' OR discarded_at IS NOT NULL
    ),
    CONSTRAINT drafts_subject_shape CHECK (
        (subject_id IS NULL AND subject_revision IS NULL)
        OR (subject_id IS NOT NULL AND subject_revision IS NOT NULL)
    )
);

CREATE INDEX IF NOT EXISTS idx_drafts_module_created ON drafts (module, created_at DESC, id DESC);
CREATE INDEX IF NOT EXISTS idx_drafts_open_subject ON drafts (module, subject_id)
    WHERE status = 'OPEN' AND subject_id IS NOT NULL;
CREATE INDEX IF NOT EXISTS idx_drafts_promoted_entity ON drafts (promoted_entity_id)
    WHERE promoted_entity_id IS NOT NULL;

-- 2. draft_events --------------------------------------------------------

CREATE SEQUENCE IF NOT EXISTS draft_events_position_seq;

CREATE TABLE IF NOT EXISTS draft_events (
    position           BIGINT PRIMARY KEY,
    draft_id           UUID NOT NULL,
    module             TEXT NOT NULL,
    branch_id          UUID NOT NULL,
    subject_id         UUID,
    op                 TEXT NOT NULL
                       CONSTRAINT draft_events_op_vocabulary
                       CHECK (op IN ('created', 'updated', 'discarded', 'reopened', 'promoted')),
    revision           BIGINT NOT NULL,
    status             TEXT NOT NULL
                       CONSTRAINT draft_events_status_vocabulary
                       CHECK (status IN ('OPEN', 'PROMOTED', 'DISCARDED')),
    actor_kind         TEXT NOT NULL
                       CONSTRAINT draft_events_actor_kind_vocabulary
                       CHECK (actor_kind IN ('user', 'key', 'agent', 'anonymous')),
    actor_id           TEXT,
    acting_as          TEXT,
    tool               TEXT,
    promoted_entity_id UUID,
    promoted_number    TEXT,
    at                 TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

-- The commit ordered position rule (ADR 0003 section 2) with this feed's own
-- lock key, so draft autosaves never queue behind money writers: the
-- transaction scoped advisory lock first, then the draw, always, for every
-- insert, with no column default.
CREATE OR REPLACE FUNCTION draft_events_assign_position() RETURNS trigger AS $$
BEGIN
    PERFORM pg_advisory_xact_lock(1685218678);
    NEW.position := nextval('draft_events_position_seq');
    RETURN NEW;
END;
$$ LANGUAGE plpgsql;

DROP TRIGGER IF EXISTS draft_events_assign_position_before_insert ON draft_events;
CREATE TRIGGER draft_events_assign_position_before_insert
    BEFORE INSERT ON draft_events
    FOR EACH ROW EXECUTE FUNCTION draft_events_assign_position();

CREATE INDEX IF NOT EXISTS idx_draft_events_module_position ON draft_events (module, position);
CREATE INDEX IF NOT EXISTS idx_draft_events_subject_position ON draft_events (subject_id, position)
    WHERE subject_id IS NOT NULL;

-- 3. draft_events_purged --------------------------------------------------
-- One row: the highest position the retention purge has deleted through. A
-- client resuming from a cursor at or below it has missed changes that aged
-- out and must re-read (the feed answers event: reset first).

CREATE TABLE IF NOT EXISTS draft_events_purged (
    id              BOOLEAN PRIMARY KEY DEFAULT TRUE CONSTRAINT draft_events_purged_single_row CHECK (id),
    through_position BIGINT NOT NULL DEFAULT 0
);

INSERT INTO draft_events_purged (id, through_position) VALUES (TRUE, 0)
    ON CONFLICT (id) DO NOTHING;

-- 4. api_keys.branch_id ---------------------------------------------------

ALTER TABLE api_keys ADD COLUMN IF NOT EXISTS branch_id UUID REFERENCES locations(id);

-- 5. The read only report --------------------------------------------------
-- Names every unrevoked key whose scopes fall outside the grammar (ADR 0007
-- section 5.3): <module>:<verb> with verb read, write, propose or commit,
-- propose and commit only on a confirm gated module (quotes at this
-- migration), and the finer names ADR 0009 put in place of the coarse ones
-- they narrow (users:grants, admin:settings, admin:staff, admin:modules).
-- The module list mirrors the scope vocabulary at this migration; it is a
-- one shot report, not a constraint, so a later module joining the
-- vocabulary does not need this file to change. The `migrate` command logs
-- the NOTICE. Nothing is changed; such a key keeps exactly the reach it
-- had, since the scope check matches exact strings.

DO $$
DECLARE
    k RECORD;
BEGIN
    FOR k IN
        SELECT id, key_prefix, scopes
        FROM api_keys
        WHERE revoked_at IS NULL
    LOOP
        IF EXISTS (
            SELECT 1
            FROM unnest(k.scopes) AS s(scope)
            WHERE NOT (
                s.scope ~ '^[a-z0-9-]+:(read|write|propose|commit)$'
                AND split_part(s.scope, ':', 1) IN (
                    'accounts','activities','admin','ap','apps','bankrecon','branches',
                    'configurator','contacts','credit-memos','customers','dashboard',
                    'delivery','deposits','documents','edi','events','gl','governance',
                    'inventory','invoices','locations','market-indices','matching','me',
                    'millwork','orders','parsing','payment-terms','payments','pos',
                    'price_levels','pricing','products','purchase-orders','quotes',
                    'reports','reporting','sales-team','charge-codes','ship-tos','tax',
                    'users','vendors','vision')
                AND (
                    split_part(s.scope, ':', 2) NOT IN ('propose', 'commit')
                    OR split_part(s.scope, ':', 1) = 'quotes'
                )
                AND s.scope NOT IN ('users:write')
            )
        ) THEN
            RAISE NOTICE 'api key % (prefix %) carries a scope outside the grant grammar: %; it keeps its stored reach; revoke and re-mint it',
                k.id, k.key_prefix, array_to_string(k.scopes, ', ');
        END IF;
    END LOOP;
END
$$;
