-- SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
-- SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

-- Rollback for 101_payments_and_ar.sql. Lives in migrations/down/ so
-- cmd/migrate's `migrations/*.sql` glob cannot pick it up as a forward
-- migration. What it keeps: every payment, refund and amount the forward
-- migration found, deposits back in their own tables. What it loses, by
-- nature: the applications (a payment goes back to naming its invoice, which
-- the forward migration never cleared on a migrated row), the excess credit
-- memos the forward migration wrote (a refund that consumed one goes back to
-- its payment), the invoice and credit memo statuses it re-derived, and any
-- payment recorded without an invoice since (its invoice_id stays NULL, so
-- NOT NULL is restored only when no such row exists). A credit memo number the
-- forward migration drew is not returned to the counter unless it was the
-- newest.

-- The one-code guard of 091 returns; a list of several enabled currencies is cut
-- to its first code first, so the guard does not refuse an existing row.
UPDATE system_settings SET value = split_part(value, ',', 1) WHERE key = 'currency.enabled' AND value LIKE '%,%';
CREATE OR REPLACE FUNCTION system_settings_currency_guard() RETURNS trigger
LANGUAGE plpgsql AS $$
BEGIN
    IF NEW.key = 'currency.default' AND NEW.value !~ '^[A-Z]{3}$' THEN
        RAISE EXCEPTION 'currency.default must be an ISO 4217 code of three capital letters'
            USING ERRCODE = '23514';
    END IF;
    IF NEW.key = 'currency.enabled' AND NEW.value !~ '^[A-Z]{3}$' THEN
        RAISE EXCEPTION 'currency.enabled holds exactly one code until the ledger groups by currency'
            USING ERRCODE = '23514';
    END IF;
    RETURN NEW;
END $$;

DROP INDEX IF EXISTS idx_customer_transactions_reference;
DROP INDEX IF EXISTS idx_payments_project;
DROP INDEX IF EXISTS idx_payments_order;
DROP INDEX IF EXISTS idx_payments_unapplied;
DROP INDEX IF EXISTS idx_payments_customer;
DROP INDEX IF EXISTS idx_payments_created_id;

DROP TRIGGER IF EXISTS trg_invoices_default_amount_open ON invoices;
DROP FUNCTION IF EXISTS invoices_default_amount_open();

DO $$
BEGIN
    -- Deposits come back as deposits: their payments and refunds go, the tables
    -- are renamed back.
    IF to_regclass('customer_deposits_legacy') IS NOT NULL THEN
        DELETE FROM ar_applications WHERE payment_id IN (SELECT id FROM customer_deposits_legacy);
        DELETE FROM payment_refunds WHERE payment_id IN (SELECT id FROM customer_deposits_legacy);
        -- the excess memos a deposit's applications left go with it (their lines cascade)
        DELETE FROM ar_applications WHERE credit_memo_id IN
            (SELECT id FROM credit_memos WHERE source_payment_id IN (SELECT id FROM customer_deposits_legacy));
        DELETE FROM payment_refunds WHERE credit_memo_id IN
            (SELECT id FROM credit_memos WHERE source_payment_id IN (SELECT id FROM customer_deposits_legacy));
        DELETE FROM credit_memos WHERE source_payment_id IN (SELECT id FROM customer_deposits_legacy);
        DELETE FROM payments WHERE id IN (SELECT id FROM customer_deposits_legacy);
        ALTER TABLE customer_deposits_legacy RENAME TO customer_deposits;
        ALTER TABLE customer_deposit_applications_legacy RENAME TO customer_deposit_applications;
    END IF;
END $$;

-- A refund that consumed a migrated excess credit memo goes back to its payment;
-- the memos the migration wrote are removed with their lines. A refund the
-- migration split in two (the part that consumed the memo, the rest) is merged
-- back into the row it was split from: same payment, time, reason, gateway
-- reference and status.
DO $$
DECLARE
    moved RECORD;
    kept UUID;
BEGIN
    FOR moved IN
        SELECT f.id, f.amount, f.reason, f.gateway_refund_id, f.status, f.created_at, m.source_payment_id
        FROM payment_refunds f JOIN credit_memos m ON m.id = f.credit_memo_id
        WHERE m.source_payment_id IS NOT NULL
        ORDER BY f.created_at, f.id
    LOOP
        SELECT s.id INTO kept FROM payment_refunds s
        WHERE s.payment_id = moved.source_payment_id AND s.credit_memo_id IS NULL AND s.created_at = moved.created_at
          AND s.reason IS NOT DISTINCT FROM moved.reason AND s.gateway_refund_id IS NOT DISTINCT FROM moved.gateway_refund_id
          AND s.status = moved.status
        ORDER BY s.id LIMIT 1;
        IF kept IS NOT NULL THEN
            UPDATE payment_refunds SET amount = amount + moved.amount WHERE id = kept;
            DELETE FROM payment_refunds WHERE id = moved.id;
        ELSE
            UPDATE payment_refunds SET payment_id = moved.source_payment_id, credit_memo_id = NULL WHERE id = moved.id;
        END IF;
    END LOOP;
END $$;
DELETE FROM payment_refunds WHERE credit_memo_id IS NOT NULL;
DELETE FROM ar_applications WHERE credit_memo_id IN (SELECT id FROM credit_memos WHERE source_payment_id IS NOT NULL);
DELETE FROM credit_memos WHERE source_payment_id IS NOT NULL;
UPDATE document_counters
SET next_value = GREATEST(1, COALESCE((SELECT MAX(substring(number FROM 4)::bigint) FROM credit_memos WHERE number ~ '^CM-[0-9]+$'), 0) + 1)
WHERE series = 'credit_memo';

-- A payment recorded without an invoice names the invoice of its newest application.
UPDATE payments p SET invoice_id = (SELECT a.invoice_id FROM ar_applications a WHERE a.payment_id = p.id AND a.kind = 'PAYMENT'
                                    ORDER BY a.created_at DESC, a.id DESC LIMIT 1)
WHERE p.invoice_id IS NULL;

DROP TABLE IF EXISTS ar_applications;

ALTER TABLE credit_memos DROP CONSTRAINT IF EXISTS credit_memos_amount_open_range;
ALTER TABLE credit_memos DROP COLUMN IF EXISTS amount_open;
ALTER TABLE credit_memos DROP COLUMN IF EXISTS source_payment_id;
ALTER TABLE invoices DROP CONSTRAINT IF EXISTS invoices_amount_open_range;
ALTER TABLE invoices DROP COLUMN IF EXISTS amount_open;

ALTER TABLE payment_refunds DROP CONSTRAINT IF EXISTS payment_refunds_one_target;
DROP INDEX IF EXISTS idx_payment_refunds_credit_memo;
ALTER TABLE payment_refunds DROP COLUMN IF EXISTS refunded_by;
ALTER TABLE payment_refunds DROP COLUMN IF EXISTS refunded_on;
ALTER TABLE payment_refunds DROP COLUMN IF EXISTS gl_entry_id;
ALTER TABLE payment_refunds DROP COLUMN IF EXISTS method;
ALTER TABLE payment_refunds DROP COLUMN IF EXISTS credit_memo_id;
ALTER TABLE payment_refunds ALTER COLUMN payment_id SET NOT NULL;

ALTER TABLE payments DROP CONSTRAINT IF EXISTS payments_unapplied_range;
ALTER TABLE payments DROP CONSTRAINT IF EXISTS payments_void_columns;
ALTER TABLE payments DROP CONSTRAINT IF EXISTS payments_status_check;
ALTER TABLE payments DROP CONSTRAINT IF EXISTS payments_number_key;
ALTER TABLE payments DROP CONSTRAINT IF EXISTS payments_currency_format;
ALTER TABLE payments DROP CONSTRAINT IF EXISTS payments_method_check;
UPDATE payments SET method = 'ACCOUNT' WHERE method IN ('ACH', 'OTHER');
ALTER TABLE payments ADD CONSTRAINT payments_method_check CHECK (method IN ('CASH', 'CARD', 'CHECK', 'ACCOUNT'));
ALTER TABLE payments ALTER COLUMN number DROP DEFAULT;
ALTER TABLE payments DROP COLUMN IF EXISTS number;
DROP FUNCTION IF EXISTS payment_next_number();
DROP SEQUENCE IF EXISTS payment_number_seq;
ALTER TABLE payments DROP COLUMN IF EXISTS gl_entry_id;
ALTER TABLE payments DROP COLUMN IF EXISTS migrated_excess;
ALTER TABLE payments DROP COLUMN IF EXISTS amount_unapplied;
ALTER TABLE payments DROP COLUMN IF EXISTS project_id;
ALTER TABLE payments DROP COLUMN IF EXISTS order_id;
ALTER TABLE payments DROP COLUMN IF EXISTS received_on;
ALTER TABLE payments DROP COLUMN IF EXISTS voided_on;
ALTER TABLE payments DROP COLUMN IF EXISTS void_reason;
ALTER TABLE payments DROP COLUMN IF EXISTS voided_by;
ALTER TABLE payments DROP COLUMN IF EXISTS voided_at;
ALTER TABLE payments DROP COLUMN IF EXISTS status;
ALTER TABLE payments DROP COLUMN IF EXISTS currency;
ALTER TABLE payments DROP COLUMN IF EXISTS branch_id;
ALTER TABLE payments DROP COLUMN IF EXISTS customer_id;
ALTER TABLE payments DROP COLUMN IF EXISTS revision;
ALTER TABLE payments DROP COLUMN IF EXISTS updated_at;
ALTER TABLE payments ALTER COLUMN amount TYPE NUMERIC(10, 2);
DO $$
BEGIN
    IF NOT EXISTS (SELECT 1 FROM payments WHERE invoice_id IS NULL) THEN
        ALTER TABLE payments ALTER COLUMN invoice_id SET NOT NULL;
    ELSE
        RAISE NOTICE 'payments recorded without an invoice exist: payments.invoice_id stays nullable';
    END IF;
END $$;

DELETE FROM gl_accounts a WHERE a.code IN ('4050', '5040')
  AND NOT EXISTS (SELECT 1 FROM gl_journal_lines l WHERE l.account_id = a.id);
