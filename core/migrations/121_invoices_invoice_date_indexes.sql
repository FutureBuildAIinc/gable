-- SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
-- SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

-- Migration: 121_invoices_invoice_date_indexes
-- Description: the Sales Summary's invoiced and outstanding reads cut a period
-- on the invoice's own business date, invoices.invoice_date (the day the
-- collected half already uses, pkg/collections). Nothing indexed that column:
-- the reads were served by the created_at indexes until they moved to the
-- business date, and now each scans the whole invoices table. One index serves
-- the open wall (a period of dates) and one the yard wall (a branch and a
-- period of dates).

CREATE INDEX IF NOT EXISTS idx_invoices_invoice_date
    ON invoices (invoice_date);

CREATE INDEX IF NOT EXISTS idx_invoices_branch_invoice_date
    ON invoices (branch_id, invoice_date);
