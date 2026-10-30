-- SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
-- SPDX-FileCopyrightText: 2026, FutureBuild, Inc. and OpenLBM contributors

-- 148: the branch's walk-in identity floor (CP-17a, item 296).
--
--   1  locations.walk_in_identity_cents: the branch's floor for asking a
--      walk-in's identity on a cash sale over it, so the walk-in's cash sales
--      are counted together. NULL is the branch country's default: 3,000
--      dollars on a United States branch, off on a Canadian one. Zero is the
--      dealer saying off, and the floor is saved through the branch act by
--      an owner or an admin only, audited.
--   2  large_cash_events.state: the worklist's identity declined state, for
--      the walk-in who declines to identify over the floor. The sale goes
--      ahead and the row lands for a manager to review.
--
-- Every step is guarded so a second apply changes nothing.

-- 1. The branch's floor.

ALTER TABLE locations ADD COLUMN IF NOT EXISTS walk_in_identity_cents BIGINT;
DO $$ BEGIN
    ALTER TABLE locations ADD CONSTRAINT locations_walk_in_identity_cents_shape
        CHECK (walk_in_identity_cents IS NULL OR walk_in_identity_cents >= 0);
EXCEPTION WHEN duplicate_object OR duplicate_table THEN NULL;
END $$;

-- 2. The worklist's identity declined state.

ALTER TABLE large_cash_events DROP CONSTRAINT IF EXISTS large_cash_events_state_check;
ALTER TABLE large_cash_events ADD CONSTRAINT large_cash_events_state_check
    CHECK (state IN ('reportable', 'recorded', 'identity declined'));
