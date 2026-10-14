-- SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
-- SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

-- Rollback for 116_customer_branches_open_documents.sql.
--
-- Lives in migrations/down/ so cmd/migrate's `migrations/*.sql` glob cannot
-- pick it up and apply it as a forward migration. Apply by hand.
--
-- What does not come back: the customer_branches rows the backfill added and
-- the audit rows that name them. The down removes nothing on purpose. A link
-- the backfill set cannot be told from one a document's own create wrote
-- (both hold a customer and a yard), so deleting by guess could remove a real
-- row, and removing a backfilled one would hide that customer again from the
-- walled reads of the yard it has open work at. No money moves either way.

DO $$
BEGIN
    RAISE NOTICE '116 down: the backfilled customer_branches rows and their audit rows stay';
END $$;
