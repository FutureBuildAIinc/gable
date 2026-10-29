-- SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
-- SPDX-FileCopyrightText: 2026, FutureBuild, Inc. and OpenLBM contributors

-- Rollback for 148_walk_in_identity_floor.sql.
--
-- Lives in migrations/down/ so cmd/migrate's `migrations/*.sql` glob cannot
-- pick it up and apply it as a forward migration. Apply by hand. Dropping the
-- column loses every branch's own floor; the branch country's default takes
-- over. The state constraint goes back to the two states of migration 139, so
-- the identity declined rows have to go with it.

DELETE FROM large_cash_events WHERE state = 'identity declined';

ALTER TABLE large_cash_events DROP CONSTRAINT IF EXISTS large_cash_events_state_check;
ALTER TABLE large_cash_events ADD CONSTRAINT large_cash_events_state_check
    CHECK (state IN ('reportable', 'recorded'));

ALTER TABLE locations DROP CONSTRAINT IF EXISTS locations_walk_in_identity_cents_shape;
ALTER TABLE locations DROP COLUMN IF EXISTS walk_in_identity_cents;
