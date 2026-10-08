-- SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
-- SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

-- R1-14: record which kind of principal wrote each audit row.
--
-- actor_kind  'user' (a JWT subject), 'key' (a scoped machine key) or
--             'agent' (an agent acting for a user, identified by the
--             X-Acting-As marker).
-- actor_id    the principal's id: the user's subject or the key's id. For an
--             agent row this is the user the agent acted for.
-- acting_as   the agent marker (X-Acting-As) on agent rows; NULL otherwise.
-- tool        the tool name (X-Agent-Tool) on agent rows; NULL otherwise.
--
-- Existing rows predate scoped keys (R1-13) and agent traffic, so every one
-- was written under a user's request context or an unattributed system one:
-- 'user' is the truthful backfill, and the legacy user_id attribution moves
-- into actor_id so the two columns agree on old rows.

ALTER TABLE audit_log ADD COLUMN actor_kind TEXT NOT NULL DEFAULT 'user';

ALTER TABLE audit_log ADD CONSTRAINT audit_log_actor_kind_check
    CHECK (actor_kind IN ('user', 'key', 'agent'));

ALTER TABLE audit_log ADD COLUMN actor_id TEXT;
ALTER TABLE audit_log ADD COLUMN acting_as TEXT;
ALTER TABLE audit_log ADD COLUMN tool TEXT;

UPDATE audit_log SET actor_id = user_id WHERE actor_id IS NULL;
