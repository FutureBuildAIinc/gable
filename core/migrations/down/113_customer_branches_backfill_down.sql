-- SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
-- SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

-- Rollback for 113_customer_branches_backfill.sql.
--
-- Lives in migrations/down/ so cmd/migrate's `migrations/*.sql` glob cannot
-- pick it up and apply it as a forward migration. Apply by hand.
--
-- What does not come back: the customer_branches rows the backfill added. The
-- down removes nothing on purpose. A row the backfill set cannot be told from
-- one the product's own customer create wrote (both hold a customer and its
-- primary branch, and both carry the time they were inserted), so deleting
-- by guess could remove a real row, and removing a backfilled one would hide
-- that customer again from every caller held to a branch, which is the
-- regression the forward migration repairs. No money moves either way.

DO $$
BEGIN
    RAISE NOTICE '113 down: the backfilled customer_branches rows stay';
END $$;
