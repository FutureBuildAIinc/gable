-- SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
-- SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

-- Rollback for 116_purchase_order_numbers.sql.
--
-- Lives in migrations/down/ so cmd/migrate's `migrations/*.sql` glob cannot
-- pick it up and apply it as a forward migration. Apply by hand.
--
-- The numbers are lost; they are re-derived in creation order by a second
-- forward apply. No money moves.

ALTER TABLE purchase_orders DROP CONSTRAINT IF EXISTS purchase_orders_number_key;
ALTER TABLE purchase_orders DROP COLUMN IF EXISTS number;
DROP FUNCTION IF EXISTS purchase_order_next_number();
DROP SEQUENCE IF EXISTS po_number_seq;
