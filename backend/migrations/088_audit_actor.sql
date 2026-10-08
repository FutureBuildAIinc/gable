-- SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
-- SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

-- R1-14: record which kind of principal wrote each audit row.
--
-- actor_kind  'user' (a JWT subject), 'key' (a scoped machine key),
--             'agent' (an agent acting for a user, identified by the
--             X-Acting-As marker) or 'anonymous' (no identity behind the
--             write: dev mode, background jobs).
-- actor_id    the principal's id: the user's subject or the key's id. For an
--             agent row this is the user the agent acted for.
-- acting_as   the agent marker (X-Acting-As) on agent rows; NULL otherwise.
-- tool        the tool name (X-Agent-Tool) on agent rows; NULL otherwise.
--
-- Existing rows predate scoped keys (R1-13) and agent traffic. Rows a user
-- wrote backfill to 'user', with the legacy user_id attribution moved into
-- actor_id. Exactly the rows with a null or empty user_id (no attribution at
-- all) backfill to 'anonymous' with a null actor_id — under the resolution
-- rule, a row with no identity behind it never claims a user. Any non-empty
-- user_id counts as its author, so the pricing scanner's literal 'system'
-- string backfills as kind 'user' with actor_id 'system', not as anonymous.

ALTER TABLE audit_log ADD COLUMN actor_kind TEXT NOT NULL DEFAULT 'user';

ALTER TABLE audit_log ADD CONSTRAINT audit_log_actor_kind_check
    CHECK (actor_kind IN ('user', 'key', 'agent', 'anonymous'));

ALTER TABLE audit_log ADD COLUMN actor_id TEXT;
ALTER TABLE audit_log ADD COLUMN acting_as TEXT;
ALTER TABLE audit_log ADD COLUMN tool TEXT;

UPDATE audit_log SET actor_id = NULLIF(user_id, '') WHERE actor_id IS NULL;
UPDATE audit_log SET actor_kind = 'anonymous' WHERE actor_id IS NULL;

-- Actor lookups (R1-13's "audit row with its key id" queries, agent-activity
-- filters) resolve by kind and id together; without this index each one
-- scans the table.
CREATE INDEX idx_audit_log_actor ON audit_log (actor_kind, actor_id);
