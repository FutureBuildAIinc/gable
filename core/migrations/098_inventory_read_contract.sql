-- SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
-- SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

-- C3-1b: the inventory read onto the wire contract (ADR 0006 7.2). The levels
-- list is the ADR 0001 envelope, whose keyset ordering is (created_at, id); the
-- inventory table never had a creation timestamp, only the updated_at every
-- stock move rewrites. This migration gives each row one: existing rows take
-- their updated_at (the earliest time the row itself can name), new rows take
-- NOW() by default. updated_at itself is filled and made NOT NULL too: the wire
-- carries it on every row, and a NULL there cannot be minted onto the wire.

-- 1. created_at: add, backfill from updated_at, then NOT NULL with a default
--    for raw SQL writers elsewhere (the counter's receipt path).
ALTER TABLE inventory ADD COLUMN IF NOT EXISTS created_at TIMESTAMPTZ;
UPDATE inventory SET created_at = COALESCE(updated_at, NOW()) WHERE created_at IS NULL;
UPDATE inventory SET updated_at = NOW() WHERE updated_at IS NULL;
ALTER TABLE inventory
    ALTER COLUMN created_at SET NOT NULL,
    ALTER COLUMN created_at SET DEFAULT NOW(),
    ALTER COLUMN updated_at SET NOT NULL;

-- 2. The keyset index for the list's ordering.
CREATE INDEX IF NOT EXISTS idx_inventory_created_at_id ON inventory (created_at DESC, id DESC);
