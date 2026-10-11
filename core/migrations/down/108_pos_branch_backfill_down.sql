-- SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
-- SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

-- Rollback for 108_pos_branch_backfill.sql.
--
-- Lives in migrations/down/ so cmd/migrate's `migrations/*.sql` glob cannot
-- pick it up and apply it as a forward migration. Apply by hand.
--
-- What does not come back: the branch each row was given. The down leaves the
-- data on purpose. The branch was read from each row's own register, so it is
-- a fact of the row's history, not a guess the rollback could undo; and
-- nulling it again would put every legacy till session, Z report and return
-- back outside the branch scoped reads, which is the regression the forward
-- migration repairs (an open drawer reading as no drawer, history invisible
-- to a caller with a branch selected). No money moves either way.

DO $$
BEGIN
    RAISE NOTICE '108 down: the backfilled branch_id values stay on the till session, Z report and return rows';
END $$;
