-- SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
-- SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

-- 114: the counter's charge sale names the customer's purchase order and job.
--
-- An account that asks for a purchase order (customers.po_required) is sold
-- to only with one: the counter keeps the number on the sale and the invoice
-- it becomes, and the job the sale belongs to is the invoice's project.
-- Nullable and additive: a sale that names neither is exactly what it was.

ALTER TABLE pos_transactions ADD COLUMN IF NOT EXISTS customer_po TEXT;
ALTER TABLE pos_transactions ADD COLUMN IF NOT EXISTS job_id UUID REFERENCES projects(id) ON DELETE SET NULL;
ALTER TABLE invoices ADD COLUMN IF NOT EXISTS customer_po TEXT;
