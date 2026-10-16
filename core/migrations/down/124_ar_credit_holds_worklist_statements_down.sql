-- SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
-- SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

-- Rollback for 124_ar_credit_holds_worklist_statements.sql.
--
-- Lives in migrations/down/ so cmd/migrate's `migrations/*.sql` glob cannot
-- pick it up and apply it as a forward migration. Apply by hand. Dropping
-- the tables loses the hold, limit change, statement run and finance charge
-- history; the finance charge invoices and their entries stay (they are AR
-- documents the core posted, not rows of these tables).

DROP TABLE IF EXISTS ar_finance_charge_items;
DROP TABLE IF EXISTS ar_finance_charge_runs;
DROP TABLE IF EXISTS ar_finance_charge_rules;
DROP TABLE IF EXISTS ar_statement_run_items;
DROP TABLE IF EXISTS ar_statement_runs;
DROP TABLE IF EXISTS credit_limit_changes;
DROP TABLE IF EXISTS credit_holds;

ALTER TABLE invoices DROP CONSTRAINT IF EXISTS invoices_origin_check;
ALTER TABLE invoices ADD CONSTRAINT invoices_origin_check CHECK (origin IN ('ORDER', 'POS'));

DELETE FROM gl_accounts WHERE code = '4060';

ALTER TABLE customers DROP COLUMN IF EXISTS credit_hold;
