-- SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
-- SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

-- Rollback for 120_collections_date_indexes.sql.
--
-- Lives in migrations/down/ so cmd/migrate's `migrations/*.sql` glob cannot
-- pick it up and apply it as a forward migration. Apply by hand. Dropping the
-- indexes loses no data; the collected reads fall back to scanning the tables.

DROP INDEX IF EXISTS idx_payments_received_on_posted;
DROP INDEX IF EXISTS idx_payment_refunds_refunded_on_complete;
