-- SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
-- SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

-- 101: payments, deposits and the AR subledger onto the wire contract (item
-- C2-4, docs/adr/0001-wire-contract.md and docs/adr/0005-sales-and-money-core.md
-- sections 9, 10 and 13). The steps are the ADR's C2-4 list, in order:
--
--   1  payments: created_at NOT NULL, customer_id from the invoice, invoice_id
--      NULL-able, the number (PAY-), revision, status, currency, branch,
--      received_on, the void columns, order_id, project_id, amount_unapplied,
--      migrated_excess and gl_entry_id; the method CHECK gains ACH and OTHER.
--      credit_memos.source_payment_id.
--   2  ar_applications, and the backfill of every legacy payment, applied
--      APPLIED credit memo and legacy refund into applications (a legacy payment
--      was never capped: its excess becomes an OPEN credit memo, not cash in
--      2200; a refund consumes that credit first, then reopens the invoice).
--      A void or written off invoice takes no application, so cash a legacy
--      record put against one is excess too. Only a COMPLETE refund counts as
--      money returned. A deposit with no invoice named goes to the customer's
--      invoices by room left, whatever their stored status: a PAID invoice that no
--      payment, memo or deposit names has no cash behind it, so it is owed, and
--      step 4 reopens it the same way (the down file cannot tell it from an
--      invoice that was always unpaid, so deciding by the stored status would make
--      a second apply differ).
--   3  customer_deposits and their applications become payments and
--      applications (same id), the two tables renamed *_legacy.
--   4  invoices.amount_open and credit_memos.amount_open from the applications;
--      statuses re-derived.
--   5  payment_refunds columns; accounts 4050 and 5040.
--
-- No migration writes a journal entry or a subledger row: history moves as
-- data, and GET /api/v1/ar/reconciliation shows where it never agreed.
-- Every statement is guarded so a second apply is a no-op.

CREATE OR REPLACE FUNCTION pg_temp.mig101_local_date(ts TIMESTAMPTZ, branch UUID) RETURNS DATE
LANGUAGE sql STABLE AS $$
    SELECT (ts AT TIME ZONE COALESCE((SELECT l.timezone FROM locations l WHERE l.id = branch), 'UTC'))::date
$$;

-- 1. payments.
UPDATE payments SET created_at = NOW() WHERE created_at IS NULL;
ALTER TABLE payments ALTER COLUMN created_at SET NOT NULL;
ALTER TABLE payments ALTER COLUMN created_at SET DEFAULT NOW();
ALTER TABLE payments ADD COLUMN IF NOT EXISTS updated_at TIMESTAMPTZ;
UPDATE payments SET updated_at = created_at WHERE updated_at IS NULL;
ALTER TABLE payments ALTER COLUMN updated_at SET NOT NULL;
ALTER TABLE payments ALTER COLUMN updated_at SET DEFAULT NOW();
ALTER TABLE payments ADD COLUMN IF NOT EXISTS revision BIGINT NOT NULL DEFAULT 1;
ALTER TABLE payments ALTER COLUMN amount TYPE NUMERIC(12, 2);

ALTER TABLE payments ADD COLUMN IF NOT EXISTS customer_id UUID NULL REFERENCES customers (id);
UPDATE payments p SET customer_id = i.customer_id FROM invoices i WHERE i.id = p.invoice_id AND p.customer_id IS NULL;
ALTER TABLE payments ALTER COLUMN customer_id SET NOT NULL;
ALTER TABLE payments ALTER COLUMN invoice_id DROP NOT NULL;

ALTER TABLE payments ADD COLUMN IF NOT EXISTS branch_id UUID NULL REFERENCES locations (id);
UPDATE payments p SET branch_id = COALESCE((SELECT i.branch_id FROM invoices i WHERE i.id = p.invoice_id),
                                           (SELECT c.primary_branch_id FROM customers c WHERE c.id = p.customer_id))
WHERE p.branch_id IS NULL;
ALTER TABLE payments ALTER COLUMN branch_id SET NOT NULL;

ALTER TABLE payments ADD COLUMN IF NOT EXISTS currency CHAR(3) NULL;
UPDATE payments p SET currency = COALESCE((SELECT i.currency FROM invoices i WHERE i.id = p.invoice_id),
                                          (SELECT c.currency FROM customers c WHERE c.id = p.customer_id),
                                          (SELECT s.value FROM system_settings s WHERE s.key = 'currency.default'),
                                          'USD')
WHERE p.currency IS NULL;
ALTER TABLE payments ALTER COLUMN currency SET NOT NULL;
DO $$ BEGIN
    ALTER TABLE payments ADD CONSTRAINT payments_currency_format CHECK (currency ~ '^[A-Z]{3}$');
EXCEPTION WHEN duplicate_object THEN NULL;
END $$;

ALTER TABLE payments ADD COLUMN IF NOT EXISTS status TEXT NOT NULL DEFAULT 'POSTED';
ALTER TABLE payments ADD COLUMN IF NOT EXISTS voided_at TIMESTAMPTZ NULL;
ALTER TABLE payments ADD COLUMN IF NOT EXISTS voided_by TEXT NULL;
ALTER TABLE payments ADD COLUMN IF NOT EXISTS void_reason TEXT NULL;
ALTER TABLE payments ADD COLUMN IF NOT EXISTS voided_on DATE NULL;
ALTER TABLE payments ADD COLUMN IF NOT EXISTS received_on DATE NULL;
UPDATE payments p SET received_on = pg_temp.mig101_local_date(p.created_at, p.branch_id) WHERE p.received_on IS NULL;
ALTER TABLE payments ALTER COLUMN received_on SET NOT NULL;
ALTER TABLE payments ADD COLUMN IF NOT EXISTS order_id UUID NULL REFERENCES orders (id);
ALTER TABLE payments ADD COLUMN IF NOT EXISTS project_id UUID NULL REFERENCES projects (id);
ALTER TABLE payments ADD COLUMN IF NOT EXISTS amount_unapplied NUMERIC(12, 2) NULL;
ALTER TABLE payments ADD COLUMN IF NOT EXISTS migrated_excess NUMERIC(12, 2) NOT NULL DEFAULT 0;
ALTER TABLE payments ADD COLUMN IF NOT EXISTS gl_entry_id UUID NULL REFERENCES gl_journal_entries (id);

-- The job of a migrated payment is its invoice's job.
UPDATE payments p SET project_id = i.project_id FROM invoices i
WHERE i.id = p.invoice_id AND p.project_id IS NULL AND i.project_id IS NOT NULL;

-- Numbers: PAY-, from a sequence, the quote migration's shape.
CREATE SEQUENCE IF NOT EXISTS payment_number_seq;
CREATE OR REPLACE FUNCTION payment_next_number() RETURNS TEXT
LANGUAGE sql AS $$
    SELECT 'PAY-' || lpad(s.n::text, GREATEST(6, length(s.n::text)), '0')
    FROM (SELECT nextval('payment_number_seq') AS n) s
$$;
ALTER TABLE payments ADD COLUMN IF NOT EXISTS number TEXT;
WITH ordered AS (
    SELECT id, row_number() OVER (ORDER BY created_at, id) AS n FROM payments WHERE number IS NULL
)
UPDATE payments p
SET number = 'PAY-' || lpad((o.n + COALESCE((SELECT MAX(substring(number FROM 5)::bigint) FROM payments WHERE number ~ '^PAY-[0-9]+$'), 0))::text,
                            GREATEST(6, length((o.n + COALESCE((SELECT MAX(substring(number FROM 5)::bigint) FROM payments WHERE number ~ '^PAY-[0-9]+$'), 0))::text)), '0')
FROM ordered o WHERE p.id = o.id;
SELECT setval('payment_number_seq',
              GREATEST(COALESCE((SELECT MAX(substring(number FROM 5)::bigint) FROM payments WHERE number ~ '^PAY-[0-9]+$'), 0) + 1,
                       (SELECT CASE WHEN is_called THEN last_value + 1 ELSE last_value END FROM payment_number_seq)),
              false);
ALTER TABLE payments ALTER COLUMN number SET DEFAULT payment_next_number();
ALTER TABLE payments ALTER COLUMN number SET NOT NULL;
DO $$ BEGIN
    ALTER TABLE payments ADD CONSTRAINT payments_number_key UNIQUE (number);
EXCEPTION WHEN duplicate_object OR duplicate_table THEN NULL;
END $$;

ALTER TABLE payments DROP CONSTRAINT IF EXISTS payments_method_check;
ALTER TABLE payments ADD CONSTRAINT payments_method_check
    CHECK (method IN ('CASH', 'CARD', 'CHECK', 'ACCOUNT', 'ACH', 'OTHER'));
ALTER TABLE payments DROP CONSTRAINT IF EXISTS payments_status_check;
ALTER TABLE payments ADD CONSTRAINT payments_status_check CHECK (status IN ('POSTED', 'VOIDED'));
ALTER TABLE payments DROP CONSTRAINT IF EXISTS payments_void_columns;
ALTER TABLE payments ADD CONSTRAINT payments_void_columns CHECK ((status = 'VOIDED') = (voided_at IS NOT NULL));

ALTER TABLE credit_memos ADD COLUMN IF NOT EXISTS source_payment_id UUID NULL REFERENCES payments (id);

-- amount_open on both documents, nullable until step 4 computes it.
ALTER TABLE invoices ADD COLUMN IF NOT EXISTS amount_open NUMERIC(12, 2) NULL;
ALTER TABLE credit_memos ADD COLUMN IF NOT EXISTS amount_open NUMERIC(12, 2) NULL;

-- 2. ar_applications.
CREATE TABLE IF NOT EXISTS ar_applications (
    id                   UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    customer_id          UUID NOT NULL REFERENCES customers (id),
    currency             CHAR(3) NOT NULL,
    kind                 TEXT NOT NULL,
    payment_id           UUID NULL REFERENCES payments (id),
    credit_memo_id       UUID NULL REFERENCES credit_memos (id),
    invoice_id           UUID NOT NULL REFERENCES invoices (id),
    amount               NUMERIC(12, 2) NOT NULL,
    reason               TEXT NULL,
    applied_on           DATE NOT NULL,
    applied_by           TEXT NULL,
    act_id               UUID NOT NULL,
    gl_entry_id          UUID NULL REFERENCES gl_journal_entries (id),
    reversed_at          TIMESTAMPTZ NULL,
    reversed_by          TEXT NULL,
    reversal_reason      TEXT NULL,
    reversal_gl_entry_id UUID NULL REFERENCES gl_journal_entries (id),
    reversed_on          DATE NULL,
    created_at           TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    CONSTRAINT ar_applications_kind_check CHECK (kind IN ('PAYMENT', 'CREDIT_MEMO', 'DISCOUNT', 'WRITE_OFF')),
    CONSTRAINT ar_applications_amount_positive CHECK (amount > 0),
    CONSTRAINT ar_applications_currency_format CHECK (currency ~ '^[A-Z]{3}$'),
    CONSTRAINT ar_applications_source CHECK (
        (kind IN ('PAYMENT', 'DISCOUNT') AND payment_id IS NOT NULL AND credit_memo_id IS NULL)
        OR (kind = 'CREDIT_MEMO' AND credit_memo_id IS NOT NULL AND payment_id IS NULL)
        OR (kind = 'WRITE_OFF' AND payment_id IS NULL AND credit_memo_id IS NULL)),
    CONSTRAINT ar_applications_write_off_reason CHECK (kind <> 'WRITE_OFF' OR NULLIF(btrim(reason), '') IS NOT NULL),
    CONSTRAINT ar_applications_reversal_columns CHECK (
        (reversed_at IS NULL) = (reversed_on IS NULL) AND (reversed_at IS NOT NULL OR reversal_gl_entry_id IS NULL))
);
CREATE INDEX IF NOT EXISTS idx_ar_applications_invoice ON ar_applications (invoice_id);
CREATE INDEX IF NOT EXISTS idx_ar_applications_payment ON ar_applications (payment_id) WHERE payment_id IS NOT NULL;
CREATE INDEX IF NOT EXISTS idx_ar_applications_credit_memo ON ar_applications (credit_memo_id) WHERE credit_memo_id IS NOT NULL;
CREATE INDEX IF NOT EXISTS idx_ar_applications_customer ON ar_applications (customer_id, applied_on);
CREATE INDEX IF NOT EXISTS idx_ar_applications_act ON ar_applications (act_id);
CREATE UNIQUE INDEX IF NOT EXISTS ux_ar_applications_entry ON ar_applications (gl_entry_id) WHERE gl_entry_id IS NOT NULL;

-- payment_refunds first: the backfill below splits legacy refund rows.
ALTER TABLE payment_refunds ALTER COLUMN payment_id DROP NOT NULL;
ALTER TABLE payment_refunds ADD COLUMN IF NOT EXISTS credit_memo_id UUID NULL REFERENCES credit_memos (id);
ALTER TABLE payment_refunds ADD COLUMN IF NOT EXISTS method TEXT NULL;
ALTER TABLE payment_refunds ADD COLUMN IF NOT EXISTS gl_entry_id UUID NULL REFERENCES gl_journal_entries (id);
ALTER TABLE payment_refunds ADD COLUMN IF NOT EXISTS refunded_on DATE NULL;
ALTER TABLE payment_refunds ADD COLUMN IF NOT EXISTS refunded_by TEXT NULL;
ALTER TABLE payment_refunds ALTER COLUMN amount TYPE NUMERIC(12, 2);
CREATE INDEX IF NOT EXISTS idx_payment_refunds_credit_memo ON payment_refunds (credit_memo_id) WHERE credit_memo_id IS NOT NULL;
UPDATE payment_refunds r SET refunded_on = pg_temp.mig101_local_date(r.created_at, p.branch_id), method = COALESCE(r.method, p.method)
FROM payments p WHERE p.id = r.payment_id AND (r.refunded_on IS NULL OR r.method IS NULL);

-- The helper that gives a migrated excess its own open credit memo: numbered
-- through the gapless counter, reason as given, no entry (the ledger already
-- moved), the ADJUST charge line every migrated memo carries. Its amount_open is
-- left to step 4, which counts the refunds that consumed it.
CREATE OR REPLACE FUNCTION pg_temp.mig101_excess_memo(
    p_customer UUID, p_currency CHAR(3), p_branch UUID, p_amount NUMERIC, p_reason TEXT,
    p_payment UUID, p_date DATE, p_created TIMESTAMPTZ) RETURNS UUID
LANGUAGE plpgsql AS $$
DECLARE
    memo_id UUID := gen_random_uuid();
BEGIN
    INSERT INTO credit_memos (id, customer_id, amount, reason, status, created_at, updated_at, number, currency, branch_id,
                              reason_code, subtotal, tax_amount, total_amount, memo_date, source_payment_id)
    VALUES (memo_id, p_customer, p_amount, p_reason, 'OPEN', p_created, p_created, credit_memo_next_number(), p_currency, p_branch,
            'OTHER', -p_amount, 0, -p_amount, p_date, p_payment);
    INSERT INTO credit_memo_lines (credit_memo_id, position, line_type, charge_code_id, description, quantity, uom, price_uom,
                                   uom_qty, price_uom_qty, unit_price, price_source, line_total, taxable, revenue_account_code)
    SELECT memo_id, 0, 'CHARGE', cc.id, LEFT(p_reason, 500), -1, 'EA', 'EA', 1, 1, p_amount, 'MANUAL', -p_amount, FALSE,
           cc.revenue_account_code
    FROM charge_codes cc WHERE cc.code = 'ADJUST';
    RETURN memo_id;
END $$;

-- Applying a sum to an invoice: capped at what the invoice still owes. A void or
-- written off invoice takes no application (ADR 0005 9.2), so its room is 0 and
-- cash a legacy record put against one takes the excess path: an open credit memo.
CREATE OR REPLACE FUNCTION pg_temp.mig101_invoice_room(p_invoice UUID) RETURNS NUMERIC
LANGUAGE sql AS $$
    SELECT CASE WHEN i.status IN ('VOID', 'WRITTEN_OFF') THEN 0
                ELSE GREATEST(0, i.total_amount - COALESCE((SELECT SUM(a.amount) FROM ar_applications a
                                                            WHERE a.invoice_id = p_invoice AND a.reversed_at IS NULL), 0)) END
    FROM invoices i WHERE i.id = p_invoice
$$;

DO $$
DECLARE
    r RECORD;
    a RECORD;
    memo UUID;
    room NUMERIC;
    take NUMERIC;
    excess NUMERIC;
    memo_left NUMERIC;
    rest NUMERIC;
    consume NUMERIC;
    live RECORD;
    new_amount NUMERIC;
    act UUID;
    kept_entry UUID;
    remaining NUMERIC;
    inv RECORD;
    skipped BIGINT;
BEGIN
    -- 2a. Each legacy payment applies min(amount, what its invoice still owes), in
    -- (created_at, id) order per invoice, as its own act with no entry. The part
    -- the invoice cannot take is AR credit (the legacy payment lowered balance_due
    -- by its full amount): an OPEN credit memo, and migrated_excess on the payment.
    IF NOT EXISTS (SELECT 1 FROM ar_applications) THEN
        FOR r IN
            SELECT p.id, p.customer_id, p.currency, p.branch_id, p.amount, p.received_on, p.created_at, p.invoice_id
            FROM payments p
            WHERE p.invoice_id IS NOT NULL
            ORDER BY p.invoice_id, p.created_at, p.id
        LOOP
            room := pg_temp.mig101_invoice_room(r.invoice_id);
            take := LEAST(r.amount, room);
            IF take > 0 THEN
                INSERT INTO ar_applications (customer_id, currency, kind, payment_id, invoice_id, amount, applied_on, applied_by,
                                             act_id, created_at)
                VALUES (r.customer_id, r.currency, 'PAYMENT', r.id, r.invoice_id, take, r.received_on, 'migration',
                        gen_random_uuid(), r.created_at);
            END IF;
            excess := r.amount - take;
            IF excess > 0 THEN
                PERFORM pg_temp.mig101_excess_memo(r.customer_id, r.currency, r.branch_id, excess, 'migrated payment excess',
                                                   r.id, r.received_on, r.created_at);
                UPDATE payments SET migrated_excess = migrated_excess + excess WHERE id = r.id;
            END IF;
        END LOOP;

        -- 2b. Each APPLIED credit memo that names an invoice applies its amount the
        -- same way, capped the same; whatever the invoice cannot take stays the memo's
        -- own open credit. (PARTIAL is taken too: it is what step 4 makes of such a
        -- memo, so the file reads the same rows again after the down file.)
        FOR r IN
            SELECT m.id, m.customer_id, m.currency, m.invoice_id, m.amount, m.memo_date, m.created_at
            FROM credit_memos m
            WHERE m.status IN ('APPLIED', 'PARTIAL') AND m.invoice_id IS NOT NULL
            ORDER BY m.invoice_id, m.created_at, m.id
        LOOP
            take := LEAST(r.amount, pg_temp.mig101_invoice_room(r.invoice_id));
            IF take > 0 THEN
                INSERT INTO ar_applications (customer_id, currency, kind, credit_memo_id, invoice_id, amount, applied_on, applied_by,
                                             act_id, created_at)
                VALUES (r.customer_id, r.currency, 'CREDIT_MEMO', r.id, r.invoice_id, take, r.memo_date, 'migration',
                        gen_random_uuid(), r.created_at);
            END IF;
        END LOOP;

        -- 2c. Legacy refunds (a positive PAYMENT subledger row, the invoice untouched).
        -- Each is split: the part that consumes the payment's excess credit memo is
        -- re-pointed to that memo; the rest stays on the payment and is taken from its
        -- application, which is reversed whole (dated the refund) and recorded again for
        -- what remains, same invoice, same act, applied_on the original's, no entry.
        FOR r IN
            SELECT f.id AS refund_id, f.payment_id, f.amount AS refund_amount, f.created_at AS refund_at, f.refunded_on
            FROM payment_refunds f
            WHERE f.payment_id IS NOT NULL AND f.credit_memo_id IS NULL AND f.status = 'COMPLETE'
            ORDER BY f.payment_id, f.created_at, f.id
        LOOP
            rest := r.refund_amount;
            memo := NULL;
            SELECT id INTO memo FROM credit_memos WHERE source_payment_id = r.payment_id AND reason = 'migrated payment excess';
            IF memo IS NOT NULL THEN
                SELECT -m.total_amount - COALESCE((SELECT SUM(f2.amount) FROM payment_refunds f2 WHERE f2.credit_memo_id = memo), 0)
                INTO memo_left FROM credit_memos m WHERE m.id = memo;
                consume := LEAST(r.refund_amount, GREATEST(memo_left, 0));
                IF consume > 0 THEN
                    IF consume = r.refund_amount THEN
                        UPDATE payment_refunds SET credit_memo_id = memo, payment_id = NULL WHERE id = r.refund_id;
                    ELSE
                        INSERT INTO payment_refunds (payment_id, credit_memo_id, amount, reason, gateway_refund_id, status, created_at,
                                                     method, refunded_on)
                        SELECT NULL, memo, consume, reason, gateway_refund_id, status, created_at, method, refunded_on
                        FROM payment_refunds WHERE id = r.refund_id;
                        UPDATE payment_refunds SET amount = amount - consume WHERE id = r.refund_id;
                    END IF;
                    rest := r.refund_amount - consume;
                END IF;
            END IF;
            IF rest > 0 THEN
                SELECT * INTO live FROM ar_applications
                WHERE payment_id = r.payment_id AND kind = 'PAYMENT' AND reversed_at IS NULL
                ORDER BY created_at, id LIMIT 1;
                IF FOUND THEN
                    consume := LEAST(rest, live.amount);
                    UPDATE ar_applications SET reversed_at = r.refund_at, reversed_on = r.refunded_on, reversed_by = 'migration',
                           reversal_reason = 'migrated refund' WHERE id = live.id;
                    new_amount := live.amount - consume;
                    IF new_amount > 0 THEN
                        INSERT INTO ar_applications (customer_id, currency, kind, payment_id, invoice_id, amount, applied_on, applied_by,
                                                     act_id, created_at)
                        VALUES (live.customer_id, live.currency, 'PAYMENT', live.payment_id, live.invoice_id, new_amount, live.applied_on,
                                'migration', live.act_id, r.refund_at);
                    END IF;
                END IF;
            END IF;
        END LOOP;
    END IF;

    -- 3. Deposits become payments (same id), numbered after the payments in their own
    -- (created_at, id) order, and their applications become PAYMENT applications.
    IF to_regclass('customer_deposits') IS NOT NULL THEN
        SELECT count(*) INTO skipped FROM customer_deposits d WHERE NOT EXISTS (SELECT 1 FROM customers c WHERE c.id = d.customer_id);
        IF skipped > 0 THEN
            RAISE NOTICE 'migration 101: % deposit(s) name no customer and stay in customer_deposits_legacy only', skipped;
        END IF;
        FOR r IN
            SELECT d.*, c.currency AS customer_currency, c.primary_branch_id
            FROM customer_deposits d JOIN customers c ON c.id = d.customer_id
            WHERE NOT EXISTS (SELECT 1 FROM payments p WHERE p.id = d.id)
            ORDER BY d.created_at, d.id
        LOOP
            INSERT INTO payments (id, amount, method, reference, notes, created_at, updated_at, customer_id, branch_id, currency,
                                  received_on, gl_entry_id, number)
            VALUES (r.id, r.amount,
                    CASE WHEN upper(r.method) IN ('CASH', 'CARD', 'CHECK', 'ACCOUNT', 'ACH') THEN upper(r.method) ELSE 'OTHER' END,
                    NULLIF(r.reference, ''), NULLIF(r.note, ''), r.created_at, r.updated_at, r.customer_id,
                    COALESCE(r.branch_id, r.primary_branch_id),
                    COALESCE(r.customer_currency, (SELECT s.value FROM system_settings s WHERE s.key = 'currency.default'), 'USD'),
                    pg_temp.mig101_local_date(r.created_at, COALESCE(r.branch_id, r.primary_branch_id)),
                    r.gl_entry_id, payment_next_number());
        END LOOP;

        FOR a IN
            SELECT da.*, p.currency, p.branch_id, p.received_on
            FROM customer_deposit_applications da JOIN payments p ON p.id = da.deposit_id
            ORDER BY da.deposit_id, da.created_at, da.id
        LOOP
            act := gen_random_uuid();
            IF a.invoice_id IS NOT NULL AND EXISTS (SELECT 1 FROM invoices WHERE id = a.invoice_id) THEN
                take := LEAST(a.amount, pg_temp.mig101_invoice_room(a.invoice_id));
                kept_entry := NULL;
                IF a.gl_entry_id IS NOT NULL AND take = a.amount
                   AND (SELECT COALESCE(SUM(l.debit), 0) FROM gl_journal_lines l WHERE l.journal_entry_id = a.gl_entry_id) = a.amount
                   AND NOT EXISTS (SELECT 1 FROM ar_applications x WHERE x.gl_entry_id = a.gl_entry_id)
                   AND (SELECT count(*) FROM customer_deposit_applications y WHERE y.gl_entry_id = a.gl_entry_id) = 1 THEN
                    kept_entry := a.gl_entry_id;
                END IF;
                IF take > 0 THEN
                    INSERT INTO ar_applications (customer_id, currency, kind, payment_id, invoice_id, amount, applied_on, applied_by,
                                                 act_id, gl_entry_id, created_at)
                    VALUES (a.customer_id, a.currency, 'PAYMENT', a.deposit_id, a.invoice_id, take,
                            pg_temp.mig101_local_date(a.created_at, a.branch_id), 'migration', act, kept_entry, a.created_at);
                END IF;
                excess := a.amount - take;
                IF excess > 0 THEN
                    PERFORM pg_temp.mig101_excess_memo(a.customer_id, a.currency, a.branch_id, excess, 'migrated deposit application',
                                                       a.deposit_id, pg_temp.mig101_local_date(a.created_at, a.branch_id), a.created_at);
                    UPDATE payments SET migrated_excess = migrated_excess + excess WHERE id = a.deposit_id;
                END IF;
            ELSE
                -- No invoice named: the customer's open invoices, oldest due first. "Open" is
        -- room left, not the stored status, which step 4 re-derives (and the down
        -- file does not undo).
                remaining := a.amount;
                FOR inv IN
                    SELECT i.id FROM invoices i
                    WHERE i.customer_id = a.customer_id AND i.currency = a.currency AND i.status NOT IN ('VOID', 'WRITTEN_OFF')
                    ORDER BY i.due_date NULLS LAST, i.invoice_date, i.id
                LOOP
                    EXIT WHEN remaining <= 0;
                    take := LEAST(remaining, pg_temp.mig101_invoice_room(inv.id));
                    CONTINUE WHEN take <= 0;
                    INSERT INTO ar_applications (customer_id, currency, kind, payment_id, invoice_id, amount, applied_on, applied_by,
                                                 act_id, created_at)
                    VALUES (a.customer_id, a.currency, 'PAYMENT', a.deposit_id, inv.id, take,
                            pg_temp.mig101_local_date(a.created_at, a.branch_id), 'migration', act, a.created_at);
                    remaining := remaining - take;
                END LOOP;
                IF remaining > 0 THEN
                    PERFORM pg_temp.mig101_excess_memo(a.customer_id, a.currency, a.branch_id, remaining, 'migrated deposit application',
                                                       a.deposit_id, pg_temp.mig101_local_date(a.created_at, a.branch_id), a.created_at);
                    UPDATE payments SET migrated_excess = migrated_excess + remaining WHERE id = a.deposit_id;
                END IF;
            END IF;
        END LOOP;

        -- A REFUNDED deposit returned its unapplied rest to the customer.
        INSERT INTO payment_refunds (payment_id, amount, reason, status, created_at, method, refunded_on)
        SELECT d.id, d.amount - d.applied_amount, 'migrated deposit refund', 'COMPLETE', d.updated_at, p.method,
               pg_temp.mig101_local_date(d.updated_at, p.branch_id)
        FROM customer_deposits d JOIN payments p ON p.id = d.id
        WHERE d.status = 'REFUNDED' AND d.amount - d.applied_amount > 0
          AND NOT EXISTS (SELECT 1 FROM payment_refunds f WHERE f.payment_id = d.id);

        ALTER TABLE customer_deposits RENAME TO customer_deposits_legacy;
        ALTER TABLE customer_deposit_applications RENAME TO customer_deposit_applications_legacy;
    END IF;
END $$;

-- amount_unapplied: amount less live PAYMENT applications, refunds and the migrated
-- excess. Clamped at zero so a legacy over-refund cannot abort the migration (the
-- reconciliation read shows it).
UPDATE payments p
SET amount_unapplied = GREATEST(0, p.amount
        - COALESCE((SELECT SUM(a.amount) FROM ar_applications a
                    WHERE a.payment_id = p.id AND a.kind = 'PAYMENT' AND a.reversed_at IS NULL), 0)
        - COALESCE((SELECT SUM(f.amount) FROM payment_refunds f WHERE f.payment_id = p.id AND f.status = 'COMPLETE'), 0)
        - p.migrated_excess)
WHERE p.amount_unapplied IS NULL;
ALTER TABLE payments ALTER COLUMN amount_unapplied SET NOT NULL;
ALTER TABLE payments DROP CONSTRAINT IF EXISTS payments_unapplied_range;
ALTER TABLE payments ADD CONSTRAINT payments_unapplied_range CHECK (amount_unapplied >= 0 AND amount_unapplied <= amount);

-- 4. amount_open, from the applications, and the statuses re-derived.
UPDATE invoices i
SET amount_open = CASE WHEN i.status IN ('VOID', 'WRITTEN_OFF') THEN 0
                       ELSE GREATEST(0, i.total_amount - COALESCE((SELECT SUM(a.amount) FROM ar_applications a
                                                                   WHERE a.invoice_id = i.id AND a.reversed_at IS NULL), 0)) END
WHERE i.amount_open IS NULL;
UPDATE invoices i
SET status = CASE WHEN i.amount_open = 0 THEN 'PAID' WHEN i.amount_open = i.total_amount THEN 'UNPAID' ELSE 'PARTIAL' END,
    paid_at = CASE WHEN i.amount_open = 0 THEN COALESCE(i.paid_at, i.updated_at) ELSE NULL END
WHERE i.status IN ('UNPAID', 'PARTIAL', 'PAID')
  AND i.status IS DISTINCT FROM CASE WHEN i.amount_open = 0 THEN 'PAID' WHEN i.amount_open = i.total_amount THEN 'UNPAID' ELSE 'PARTIAL' END;
ALTER TABLE invoices ALTER COLUMN amount_open SET NOT NULL;
ALTER TABLE invoices DROP CONSTRAINT IF EXISTS invoices_amount_open_range;
ALTER TABLE invoices ADD CONSTRAINT invoices_amount_open_range CHECK (amount_open >= 0);

UPDATE credit_memos m
SET amount_open = CASE WHEN m.status IN ('DRAFT', 'VOID') THEN 0
                       ELSE LEAST(0, m.total_amount
                                  + COALESCE((SELECT SUM(a.amount) FROM ar_applications a
                                              WHERE a.credit_memo_id = m.id AND a.reversed_at IS NULL), 0)
                                  + COALESCE((SELECT SUM(f.amount) FROM payment_refunds f WHERE f.credit_memo_id = m.id), 0)) END
WHERE m.amount_open IS NULL;
UPDATE credit_memos m
SET status = CASE WHEN m.amount_open = 0 THEN 'APPLIED' WHEN m.amount_open = m.total_amount THEN 'OPEN' ELSE 'PARTIAL' END
WHERE m.status IN ('OPEN', 'PARTIAL', 'APPLIED')
  AND m.status IS DISTINCT FROM CASE WHEN m.amount_open = 0 THEN 'APPLIED' WHEN m.amount_open = m.total_amount THEN 'OPEN' ELSE 'PARTIAL' END;
ALTER TABLE credit_memos ALTER COLUMN amount_open SET NOT NULL;
ALTER TABLE credit_memos ALTER COLUMN amount_open SET DEFAULT 0;
ALTER TABLE credit_memos DROP CONSTRAINT IF EXISTS credit_memos_amount_open_range;
ALTER TABLE credit_memos ADD CONSTRAINT credit_memos_amount_open_range CHECK (amount_open <= 0);

-- A raw invoice insert (the seed, the counter's account charge) carries no
-- amount_open: it opens at the total, as the AR core would have set it.
CREATE OR REPLACE FUNCTION invoices_default_amount_open() RETURNS trigger
LANGUAGE plpgsql AS $$
BEGIN
    IF NEW.amount_open IS NULL THEN
        NEW.amount_open := CASE WHEN NEW.status IN ('VOID', 'WRITTEN_OFF', 'PAID') THEN 0 ELSE NEW.total_amount END;
    END IF;
    RETURN NEW;
END $$;
DROP TRIGGER IF EXISTS trg_invoices_default_amount_open ON invoices;
CREATE TRIGGER trg_invoices_default_amount_open BEFORE INSERT ON invoices
    FOR EACH ROW EXECUTE FUNCTION invoices_default_amount_open();

-- payment_refunds: exactly one of a payment and a credit memo.
ALTER TABLE payment_refunds DROP CONSTRAINT IF EXISTS payment_refunds_one_target;
ALTER TABLE payment_refunds ADD CONSTRAINT payment_refunds_one_target
    CHECK ((payment_id IS NOT NULL) <> (credit_memo_id IS NOT NULL));
UPDATE payment_refunds SET method = 'OTHER' WHERE method IS NULL;
UPDATE payment_refunds r SET refunded_on = r.created_at::date WHERE r.refunded_on IS NULL;
ALTER TABLE payment_refunds ALTER COLUMN refunded_on SET NOT NULL;
ALTER TABLE payment_refunds ALTER COLUMN method SET NOT NULL;

-- 5. Accounts: 4050 Sales Discounts (REVENUE, normal DEBIT), 5040 Bad Debt Expense.
INSERT INTO gl_accounts (code, name, type, subtype, normal_balance, description)
SELECT '4050', 'Sales Discounts', 'REVENUE', 'Operating', 'DEBIT', 'Early pay discounts taken on invoices (contra revenue)'
WHERE NOT EXISTS (SELECT 1 FROM gl_accounts WHERE code = '4050');
INSERT INTO gl_accounts (code, name, type, subtype, normal_balance, description)
SELECT '5040', 'Bad Debt Expense', 'EXPENSE', 'Operating', 'DEBIT', 'Receivables written off as uncollectable'
WHERE NOT EXISTS (SELECT 1 FROM gl_accounts WHERE code = '5040');

-- 6. The GL reports group by currency (the reports of internal/gl never add two
-- currencies), so C2-1's one-code guard on currency.enabled lifts: the setting
-- is a comma separated list of ISO 4217 codes.
CREATE OR REPLACE FUNCTION system_settings_currency_guard() RETURNS trigger
LANGUAGE plpgsql AS $$
BEGIN
    IF NEW.key = 'currency.default' AND NEW.value !~ '^[A-Z]{3}$' THEN
        RAISE EXCEPTION 'currency.default must be an ISO 4217 code of three capital letters'
            USING ERRCODE = '23514';
    END IF;
    IF NEW.key = 'currency.enabled' AND NEW.value !~ '^[A-Z]{3}(,[A-Z]{3})*$' THEN
        RAISE EXCEPTION 'currency.enabled is a comma separated list of ISO 4217 codes of three capital letters'
            USING ERRCODE = '23514';
    END IF;
    RETURN NEW;
END $$;

-- Indexes: the keyset list, the unapplied filter, the order's deposits.
CREATE INDEX IF NOT EXISTS idx_payments_created_id ON payments (created_at DESC, id DESC);
CREATE INDEX IF NOT EXISTS idx_payments_customer ON payments (customer_id);
CREATE INDEX IF NOT EXISTS idx_payments_unapplied ON payments (customer_id) WHERE amount_unapplied > 0 AND status = 'POSTED';
CREATE INDEX IF NOT EXISTS idx_payments_order ON payments (order_id) WHERE order_id IS NOT NULL;
CREATE INDEX IF NOT EXISTS idx_payments_project ON payments (project_id) WHERE project_id IS NOT NULL;
CREATE INDEX IF NOT EXISTS idx_customer_transactions_reference ON customer_transactions (reference_id) WHERE reference_id IS NOT NULL;
