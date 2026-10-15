-- SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
-- SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

-- Rollback for 121_invoices_invoice_date_indexes.sql.
--
-- Lives in migrations/down/ so cmd/migrate's `migrations/*.sql` glob cannot
-- pick it up and apply it as a forward migration. Apply by hand. Dropping the
-- indexes loses no data; the Sales Summary's invoice reads fall back to
-- scanning the table.

DROP INDEX IF EXISTS idx_invoices_invoice_date;
DROP INDEX IF EXISTS idx_invoices_branch_invoice_date;
