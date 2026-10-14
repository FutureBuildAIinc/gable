-- SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
-- SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

ALTER TABLE invoices DROP COLUMN IF EXISTS customer_po;
ALTER TABLE pos_transactions DROP COLUMN IF EXISTS job_id;
ALTER TABLE pos_transactions DROP COLUMN IF EXISTS customer_po;
