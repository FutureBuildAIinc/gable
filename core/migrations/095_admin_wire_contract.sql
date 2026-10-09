-- SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
-- SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

-- 095: the admin group onto the wire contract (item C5-1a: techadmin,
-- governance and staff; docs/adr/0001-wire-contract.md). Seven steps:
--
--   1  rfcs.created_at and updated_at filled and NOT NULL (the list orders on
--      created_at), and rfcs.revision for the If-Match precondition.
--   1b rfcs.status backfilled to the contract enum (legacy 'published' and
--      any other value maps to 'approved') and pinned by a CHECK constraint.
--   2  rfc_number_seq, rfc_next_number() and rfcs.number (RFC-000001): the
--      quote migration's shape, backfilled in (created_at, id) order; the
--      DEFAULT keeps the seed's raw INSERTs numbered.
--   3  staff.revision for the If-Match precondition (created_at is already
--      NOT NULL).
--   4  api_keys.created_at filled and NOT NULL (the list orders on it).
--   5  admin_revisions: the revision anchor of every singleton settings
--      resource the admin routes edit (the AI settings, the routing settings,
--      each module's enable flag). A missing row means revision 1; the row
--      appears at the first write and survives the setting's deletion, so the
--      revision never moves backwards.
--   6  the keyset indexes of the three lists' ordering.
--
-- Every step is idempotent: a second apply changes nothing the first apply or
-- the application wrote since.

-- 1. rfcs timestamps and revision.
UPDATE rfcs SET created_at = COALESCE(updated_at, NOW()) WHERE created_at IS NULL;
UPDATE rfcs SET updated_at = created_at WHERE updated_at IS NULL;
ALTER TABLE rfcs ALTER COLUMN created_at SET NOT NULL;
ALTER TABLE rfcs ALTER COLUMN updated_at SET NOT NULL;
ALTER TABLE rfcs ADD COLUMN IF NOT EXISTS revision BIGINT NOT NULL DEFAULT 1;

-- 1b. rfcs.status: a database seeded before this PR holds rows outside the
-- contract's status enum (the base seed wrote 'published', which no route
-- produces). Map every non enum value to 'approved' (the same meaning, inside
-- the vocabulary) and pin the enum with a CHECK constraint, guarded like the
-- number constraint below so a second apply stays a no-op.
UPDATE rfcs SET status = 'approved' WHERE status NOT IN ('draft','review','approved','rejected');
DO $$ BEGIN
    ALTER TABLE rfcs ADD CONSTRAINT rfcs_status_check
        CHECK (status IN ('draft','review','approved','rejected'));
EXCEPTION WHEN duplicate_object OR duplicate_table THEN NULL;
END $$;

-- 2. Document numbers: RFC- from a sequence, the quote migration's shape.
CREATE SEQUENCE IF NOT EXISTS rfc_number_seq;

CREATE OR REPLACE FUNCTION rfc_next_number() RETURNS TEXT
LANGUAGE sql AS $$
    SELECT 'RFC-' || lpad(s.n::text, GREATEST(6, length(s.n::text)), '0')
    FROM (SELECT nextval('rfc_number_seq') AS n) s
$$;

ALTER TABLE rfcs ADD COLUMN IF NOT EXISTS number TEXT;

WITH ordered AS (
    SELECT id, row_number() OVER (ORDER BY created_at, id) AS n
    FROM rfcs
    WHERE number IS NULL
)
UPDATE rfcs o
SET number = 'RFC-' || lpad(r.n::text, GREATEST(6, length(r.n::text)), '0')
FROM ordered r
WHERE o.id = r.id;

SELECT setval('rfc_number_seq',
              COALESCE((SELECT MAX(substring(number FROM 5)::bigint) FROM rfcs), 0) + 1,
              false);

ALTER TABLE rfcs ALTER COLUMN number SET DEFAULT rfc_next_number();
ALTER TABLE rfcs ALTER COLUMN number SET NOT NULL;
DO $$ BEGIN
    ALTER TABLE rfcs ADD CONSTRAINT rfcs_number_key UNIQUE (number);
EXCEPTION WHEN duplicate_object OR duplicate_table THEN NULL;
END $$;

-- 3. Staff revision.
ALTER TABLE staff ADD COLUMN IF NOT EXISTS revision BIGINT NOT NULL DEFAULT 1;

-- 4. api_keys.created_at.
UPDATE api_keys SET created_at = NOW() WHERE created_at IS NULL;
ALTER TABLE api_keys ALTER COLUMN created_at SET NOT NULL;

-- 5. The settings revision anchors.
CREATE TABLE IF NOT EXISTS admin_revisions (
    resource   TEXT PRIMARY KEY,
    revision   BIGINT NOT NULL DEFAULT 1,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

-- 6. The keyset indexes.
CREATE INDEX IF NOT EXISTS idx_rfcs_created_id ON rfcs (created_at DESC, id DESC);
CREATE INDEX IF NOT EXISTS idx_staff_created_id ON staff (created_at DESC, id DESC);
CREATE INDEX IF NOT EXISTS idx_api_keys_created_id ON api_keys (created_at DESC, id DESC);
