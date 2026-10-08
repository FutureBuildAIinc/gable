-- SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
-- SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

-- Rollback for 097_invoices_wire_contract.sql. Lives in migrations/down/ so
-- cmd/migrate's `migrations/*.sql` glob cannot pick it up as a forward
-- migration. The drops lose the numbers, revisions, credit memo lines, voids
-- and the discount snapshot the migration wrote; the documents themselves
-- stay. The legacy text columns come back from the terms the rows now point
-- at (the term's code). A WRITTEN_OFF invoice goes back as PAID and a DRAFT or
-- OPEN credit memo as PENDING or APPLIED, the old vocabulary's nearest word;
-- the credit memo lines of a posted memo cannot be held by the old shape and
-- are dropped with their table.

DROP INDEX IF EXISTS idx_credit_memos_project;
DROP INDEX IF EXISTS idx_invoices_ship_to;
DROP INDEX IF EXISTS idx_invoices_project;
DROP INDEX IF EXISTS idx_credit_memos_created_id;
DROP INDEX IF EXISTS idx_invoices_created_id;

DROP TABLE IF EXISTS credit_memo_lines;

ALTER TABLE credit_memos DROP CONSTRAINT IF EXISTS credit_memos_void_columns;
ALTER TABLE credit_memos DROP CONSTRAINT IF EXISTS credit_memos_number_when_posted;
ALTER TABLE credit_memos DROP CONSTRAINT IF EXISTS credit_memos_number_key;
ALTER TABLE credit_memos DROP CONSTRAINT IF EXISTS credit_memos_amounts_nonpositive;
ALTER TABLE credit_memos DROP CONSTRAINT IF EXISTS credit_memos_reason_code_check;
ALTER TABLE credit_memos DROP CONSTRAINT IF EXISTS credit_memos_currency_format;
ALTER TABLE credit_memos DROP CONSTRAINT IF EXISTS credit_memos_status_check;
ALTER TABLE credit_memos ALTER COLUMN status DROP DEFAULT;
UPDATE credit_memos SET status = CASE WHEN status = 'DRAFT' THEN 'PENDING' WHEN status IN ('OPEN', 'PARTIAL', 'APPLIED') THEN 'APPLIED' ELSE status END;
ALTER TABLE credit_memos ADD CONSTRAINT credit_memos_status_check CHECK (status IN ('PENDING', 'APPLIED', 'VOID'));
ALTER TABLE credit_memos ALTER COLUMN status SET DEFAULT 'PENDING';
ALTER TABLE credit_memos ALTER COLUMN amount TYPE NUMERIC(10, 2);
ALTER TABLE credit_memos DROP COLUMN IF EXISTS voided_on;
ALTER TABLE credit_memos DROP COLUMN IF EXISTS void_reason;
ALTER TABLE credit_memos DROP COLUMN IF EXISTS voided_by;
ALTER TABLE credit_memos DROP COLUMN IF EXISTS voided_at;
ALTER TABLE credit_memos DROP COLUMN IF EXISTS memo_date;
ALTER TABLE credit_memos DROP COLUMN IF EXISTS gl_entry_id;
ALTER TABLE credit_memos DROP COLUMN IF EXISTS tax_rate;
ALTER TABLE credit_memos DROP COLUMN IF EXISTS total_amount;
ALTER TABLE credit_memos DROP COLUMN IF EXISTS tax_amount;
ALTER TABLE credit_memos DROP COLUMN IF EXISTS subtotal;
ALTER TABLE credit_memos DROP COLUMN IF EXISTS reason_code;
ALTER TABLE credit_memos DROP COLUMN IF EXISTS pos_return_id;
ALTER TABLE credit_memos DROP COLUMN IF EXISTS ship_to_id;
ALTER TABLE credit_memos DROP COLUMN IF EXISTS project_id;
ALTER TABLE credit_memos DROP COLUMN IF EXISTS branch_id;
ALTER TABLE credit_memos DROP COLUMN IF EXISTS currency;
ALTER TABLE credit_memos DROP COLUMN IF EXISTS number;
ALTER TABLE credit_memos DROP COLUMN IF EXISTS revision;
ALTER TABLE credit_memos DROP COLUMN IF EXISTS updated_at;
ALTER TABLE credit_memos ALTER COLUMN created_at DROP NOT NULL;

ALTER TABLE invoice_lines DROP COLUMN IF EXISTS price_adjusted_by;
ALTER TABLE invoice_lines DROP COLUMN IF EXISTS override_reason;
ALTER TABLE invoice_lines DROP COLUMN IF EXISTS priced_unit_price;

ALTER TABLE invoices DROP CONSTRAINT IF EXISTS invoices_void_columns;
ALTER TABLE invoices DROP COLUMN IF EXISTS voided_on;
ALTER TABLE invoices DROP COLUMN IF EXISTS void_reason;
ALTER TABLE invoices DROP COLUMN IF EXISTS voided_by;
ALTER TABLE invoices DROP COLUMN IF EXISTS voided_at;

ALTER TABLE customers ADD COLUMN IF NOT EXISTS payment_terms VARCHAR(20) DEFAULT 'NET30';
UPDATE customers c SET payment_terms = LEFT(pt.code, 20) FROM payment_terms pt WHERE pt.id = c.payment_terms_id;
ALTER TABLE invoices ADD COLUMN IF NOT EXISTS payment_terms VARCHAR(20) DEFAULT 'NET30';
UPDATE invoices i SET payment_terms = LEFT(pt.code, 20) FROM payment_terms pt WHERE pt.id = i.payment_terms_id;
ALTER TABLE invoices DROP COLUMN IF EXISTS discount_percent;
ALTER TABLE invoices DROP COLUMN IF EXISTS discount_due_date;
ALTER TABLE invoices DROP COLUMN IF EXISTS payment_terms_id;

ALTER TABLE invoices ALTER COLUMN due_date TYPE TIMESTAMPTZ USING due_date::timestamptz;
ALTER TABLE invoices DROP CONSTRAINT IF EXISTS invoices_status_check;
UPDATE invoices SET status = 'PAID' WHERE status = 'WRITTEN_OFF';
ALTER TABLE invoices ADD CONSTRAINT invoices_status_check
    CHECK (status IN ('UNPAID', 'PARTIAL', 'PAID', 'VOID', 'OVERDUE'));

ALTER TABLE invoices DROP CONSTRAINT IF EXISTS invoices_number_key;
ALTER TABLE invoices ALTER COLUMN number DROP NOT NULL;
ALTER TABLE invoices ALTER COLUMN number DROP DEFAULT;
ALTER TABLE invoices DROP COLUMN IF EXISTS number;

DROP FUNCTION IF EXISTS credit_memo_next_number();
DROP FUNCTION IF EXISTS invoice_next_number();
DROP TABLE IF EXISTS document_counters;

ALTER TABLE invoices DROP COLUMN IF EXISTS revision;
ALTER TABLE invoices ALTER COLUMN updated_at DROP NOT NULL;
ALTER TABLE invoices ALTER COLUMN created_at DROP NOT NULL;
