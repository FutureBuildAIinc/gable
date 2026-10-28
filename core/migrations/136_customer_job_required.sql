-- SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
-- SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

-- 136: an account can require a job on a charge sale.
--
-- The purchase order rule (customers.po_required) refuses a charge sale
-- without a PO where the account asks for one; this column gives the job the
-- same footing: the counter refuses a charge sale with any amount on the
-- account and no job picked. Additive and defaulted: an account that does not
-- ask is exactly what it was.

ALTER TABLE customers ADD COLUMN IF NOT EXISTS job_required BOOLEAN NOT NULL DEFAULT FALSE;
