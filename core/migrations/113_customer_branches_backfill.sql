-- SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
-- SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

-- Migration: 113_customer_branches_backfill
-- Description: the customer wall (the customer family, the CRM and the
-- account surface) reads a customer's branches from customer_branches, and
-- the product's own customer create writes the row for the primary branch
-- beside the customer. The demo seed wrote customers with a primary branch
-- and no row, so those customers are hidden from every caller held to a
-- branch (a header, or a user's grants). This inserts the row for a
-- customer's own primary branch where the customer has none. It touches only
-- customers that have a primary_branch_id and no customer_branches row for
-- it, sets the row from the customer's own primary branch and nothing else
-- (a customer shared with other branches keeps those rows), moves no money
-- and deletes nothing, so it is idempotent: a customer it has set is never
-- visited again. A customer with no primary branch is left exactly as it is
-- rather than guessed at (the schema forbids one since 067, so the count is
-- a safeguard), and reported by a NOTICE, as is the number set.

DO $$
DECLARE
    set_count INTEGER;
    no_branch INTEGER;
BEGIN
    INSERT INTO customer_branches (customer_id, branch_id)
    SELECT c.id, c.primary_branch_id
      FROM customers c
     WHERE c.primary_branch_id IS NOT NULL
       AND NOT EXISTS (SELECT 1 FROM customer_branches cb
                        WHERE cb.customer_id = c.id AND cb.branch_id = c.primary_branch_id)
    ON CONFLICT DO NOTHING;
    GET DIAGNOSTICS set_count = ROW_COUNT;

    SELECT COUNT(*) INTO no_branch FROM customers WHERE primary_branch_id IS NULL;

    RAISE NOTICE '113: set the customer_branches row for its primary branch on % customer(s)', set_count;
    IF no_branch > 0 THEN
        RAISE NOTICE '113: % customer(s) have no primary branch and were left as they are', no_branch;
    END IF;
END $$;
