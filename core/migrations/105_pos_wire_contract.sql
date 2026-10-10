-- SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
-- SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

-- 105: the counter (POS and till) onto the wire contract (item C2-5,
-- docs/adr/0001-wire-contract.md and docs/adr/0005-sales-and-money-core.md
-- sections 13 C2-5 and 14.2 C2-5). The steps are the ADR's C2-5 list, in
-- order:
--
--   1  pos_transactions: created_at NOT NULL, revision, the number (POS-),
--      currency, invoice_id FK invoices.
--   2  pos_line_items: the shared line columns of ADR 0005 section 2.2
--      (unit_price widened to NUMERIC(12,4) in place; uom stays the sale
--      unit), product_id NULL-able with the per line type CHECKs.
--      pos_tenders: payment_id FK payments.
--   3  pos_returns: the number (RTN-), credit_memo_id FK credit_memos;
--      pos_return_lines widened like the lines, plus the sale_line_id link
--      that bounds a return by the line it names. Each historic return with
--      refund_method ACCOUNT lowered AR (its GL entry and subledger row
--      exist) with no document: it is migrated as an OPEN credit memo
--      (reason 'migrated counter account return', its gl_entry_id the
--      return's, no new entry), linked through pos_returns.credit_memo_id.
--   4  the walk-in customer: a customers row (account_number WALK-IN, name
--      Walk-in) and system_settings pos.walk_in_customer_id, when absent.
--
-- No migration writes a journal entry or a subledger row: history moves as
-- data, and GET /api/v1/ar/reconciliation shows where it never agreed.
-- Constraint adds are guarded so a second apply is a no-op.

-- 1. pos_transactions.

UPDATE pos_transactions SET created_at = NOW() WHERE created_at IS NULL;
ALTER TABLE pos_transactions ALTER COLUMN created_at SET NOT NULL;
ALTER TABLE pos_transactions ALTER COLUMN created_at SET DEFAULT NOW();
ALTER TABLE pos_transactions ADD COLUMN IF NOT EXISTS updated_at TIMESTAMPTZ;
UPDATE pos_transactions SET updated_at = created_at WHERE updated_at IS NULL;
ALTER TABLE pos_transactions ALTER COLUMN updated_at SET NOT NULL;
ALTER TABLE pos_transactions ALTER COLUMN updated_at SET DEFAULT NOW();
ALTER TABLE pos_transactions ADD COLUMN IF NOT EXISTS revision BIGINT NOT NULL DEFAULT 1;

ALTER TABLE pos_transactions ADD COLUMN IF NOT EXISTS currency CHAR(3) NULL;
UPDATE pos_transactions t SET currency = COALESCE((SELECT c.currency FROM customers c WHERE c.id = t.customer_id),
                                                  (SELECT value FROM system_settings WHERE key = 'currency.default'))
WHERE currency IS NULL;
UPDATE pos_transactions SET currency = 'USD' WHERE currency IS NULL;
ALTER TABLE pos_transactions ALTER COLUMN currency SET NOT NULL;
DO $$ BEGIN
    ALTER TABLE pos_transactions ADD CONSTRAINT pos_transactions_currency_check CHECK (currency ~ '^[A-Z]{3}$');
EXCEPTION WHEN duplicate_object OR duplicate_table THEN NULL;
END $$;

ALTER TABLE pos_transactions ADD COLUMN IF NOT EXISTS invoice_id UUID NULL REFERENCES invoices (id);

CREATE SEQUENCE IF NOT EXISTS pos_transaction_number_seq;
ALTER TABLE pos_transactions ADD COLUMN IF NOT EXISTS number TEXT;

WITH ordered AS (
    SELECT id, row_number() OVER (ORDER BY created_at, id) AS n
    FROM pos_transactions
    WHERE number IS NULL
)
UPDATE pos_transactions t
SET number = 'POS-' || lpad(o.n::text, GREATEST(6, length(o.n::text)), '0')
FROM ordered o
WHERE t.id = o.id;

SELECT setval('pos_transaction_number_seq',
              GREATEST((SELECT COALESCE(MAX(substring(number FROM 5)::bigint), 0) + 1 FROM pos_transactions), 1),
              false);

ALTER TABLE pos_transactions ALTER COLUMN number SET DEFAULT 'POS-' || lpad(nextval('pos_transaction_number_seq')::text, 6, '0');
ALTER TABLE pos_transactions ALTER COLUMN number SET NOT NULL;
DO $$ BEGIN
    ALTER TABLE pos_transactions ADD CONSTRAINT pos_transactions_number_key UNIQUE (number);
EXCEPTION WHEN duplicate_object OR duplicate_table THEN NULL;
END $$;

DO $$ BEGIN
    ALTER TABLE pos_transactions ADD CONSTRAINT pos_transactions_status_check
        CHECK (status IN ('OPEN', 'HELD', 'COMPLETED', 'VOIDED', 'RETURNED'));
EXCEPTION WHEN duplicate_object OR duplicate_table THEN NULL;
END $$;

-- 2. pos_line_items: the shared line columns of ADR 0005 section 2.2.

ALTER TABLE pos_line_items ADD COLUMN IF NOT EXISTS position INTEGER;
WITH ordered AS (
    SELECT id, row_number() OVER (PARTITION BY transaction_id ORDER BY created_at, id) - 1 AS n
    FROM pos_line_items
    WHERE position IS NULL
)
UPDATE pos_line_items l SET position = o.n FROM ordered o WHERE l.id = o.id;
ALTER TABLE pos_line_items ALTER COLUMN position SET NOT NULL;
ALTER TABLE pos_line_items ALTER COLUMN position SET DEFAULT 0;

ALTER TABLE pos_line_items ADD COLUMN IF NOT EXISTS line_type TEXT;
UPDATE pos_line_items SET line_type = 'PRODUCT' WHERE line_type IS NULL;
ALTER TABLE pos_line_items ALTER COLUMN line_type SET NOT NULL;

ALTER TABLE pos_line_items ADD COLUMN IF NOT EXISTS parent_line_id UUID NULL REFERENCES pos_line_items (id) ON DELETE CASCADE;
ALTER TABLE pos_line_items ADD COLUMN IF NOT EXISTS charge_code_id UUID NULL REFERENCES charge_codes (id);
ALTER TABLE pos_line_items ADD COLUMN IF NOT EXISTS sku TEXT;
ALTER TABLE pos_line_items ADD COLUMN IF NOT EXISTS price_uom TEXT;
UPDATE pos_line_items SET price_uom = uom WHERE price_uom IS NULL AND line_type <> 'TEXT';
ALTER TABLE pos_line_items ADD COLUMN IF NOT EXISTS uom_qty NUMERIC(12, 4);
UPDATE pos_line_items SET uom_qty = 1 WHERE uom_qty IS NULL AND line_type <> 'TEXT';
ALTER TABLE pos_line_items ADD COLUMN IF NOT EXISTS price_uom_qty NUMERIC(12, 4);
UPDATE pos_line_items SET price_uom_qty = 1 WHERE price_uom_qty IS NULL AND line_type <> 'TEXT';

ALTER TABLE pos_line_items ALTER COLUMN quantity TYPE NUMERIC(12, 4);
ALTER TABLE pos_line_items ALTER COLUMN unit_price TYPE NUMERIC(12, 4);
ALTER TABLE pos_line_items ADD COLUMN IF NOT EXISTS priced_unit_price NUMERIC(12, 4);
UPDATE pos_line_items SET priced_unit_price = unit_price WHERE priced_unit_price IS NULL;
ALTER TABLE pos_line_items ADD COLUMN IF NOT EXISTS price_source TEXT;
UPDATE pos_line_items SET price_source = 'PRICE_LIST' WHERE price_source IS NULL;
ALTER TABLE pos_line_items ALTER COLUMN price_source SET NOT NULL;
DO $$ BEGIN
    ALTER TABLE pos_line_items ADD CONSTRAINT pos_line_items_price_source_check
        CHECK (price_source IN ('PRICE_LIST', 'QUOTE', 'OVERRIDE', 'MANUAL', 'NONE'));
EXCEPTION WHEN duplicate_object OR duplicate_table THEN NULL;
END $$;

ALTER TABLE pos_line_items ADD COLUMN IF NOT EXISTS override_reason TEXT;
ALTER TABLE pos_line_items ADD COLUMN IF NOT EXISTS discount_percent NUMERIC(7, 4);
ALTER TABLE pos_line_items ADD COLUMN IF NOT EXISTS discount_amount NUMERIC(12, 2);
ALTER TABLE pos_line_items ADD COLUMN IF NOT EXISTS discount_reason TEXT;
ALTER TABLE pos_line_items ADD COLUMN IF NOT EXISTS price_adjusted_by TEXT;
ALTER TABLE pos_line_items ADD COLUMN IF NOT EXISTS taxable BOOLEAN;
UPDATE pos_line_items l SET taxable = COALESCE((SELECT p.taxable FROM products p WHERE p.id = l.product_id), TRUE)
WHERE taxable IS NULL;
ALTER TABLE pos_line_items ALTER COLUMN taxable SET NOT NULL;
ALTER TABLE pos_line_items ALTER COLUMN taxable SET DEFAULT TRUE;
ALTER TABLE pos_line_items ADD COLUMN IF NOT EXISTS revenue_account_code TEXT;

ALTER TABLE pos_line_items ALTER COLUMN product_id DROP NOT NULL;
ALTER TABLE pos_line_items ALTER COLUMN quantity DROP NOT NULL;
ALTER TABLE pos_line_items ALTER COLUMN uom DROP NOT NULL;
ALTER TABLE pos_line_items ALTER COLUMN unit_price DROP NOT NULL;
ALTER TABLE pos_line_items ALTER COLUMN line_total DROP NOT NULL;

DO $$ BEGIN
    ALTER TABLE pos_line_items ADD CONSTRAINT pos_line_items_line_type_check
        CHECK (line_type IN ('PRODUCT', 'KIT', 'COMPONENT', 'CHARGE', 'TEXT'));
EXCEPTION WHEN duplicate_object OR duplicate_table THEN NULL;
END $$;
DO $$ BEGIN
    ALTER TABLE pos_line_items ADD CONSTRAINT pos_line_items_pair_positive
        CHECK ((uom_qty IS NULL AND price_uom_qty IS NULL) OR (uom_qty > 0 AND price_uom_qty > 0));
EXCEPTION WHEN duplicate_object OR duplicate_table THEN NULL;
END $$;
DO $$ BEGIN
    ALTER TABLE pos_line_items ADD CONSTRAINT pos_line_items_shape
        CHECK (
            -- a text line: a note, no amount, nothing priced
            (line_type = 'TEXT'
                AND quantity IS NULL AND uom IS NULL AND price_uom IS NULL
                AND uom_qty IS NULL AND price_uom_qty IS NULL
                AND unit_price IS NULL AND line_total IS NULL
                AND product_id IS NULL AND charge_code_id IS NULL)
            -- product (stocked or non stock) and kit lines: fully priced
            OR (line_type IN ('PRODUCT', 'KIT')
                AND quantity IS NOT NULL AND quantity > 0
                AND price_uom IS NOT NULL
                AND uom_qty IS NOT NULL AND price_uom_qty IS NOT NULL
                AND unit_price IS NOT NULL AND unit_price >= 0
                AND line_total IS NOT NULL
                AND charge_code_id IS NULL)
            -- a component: priced fields present at zero, always a product
            OR (line_type = 'COMPONENT'
                AND product_id IS NOT NULL AND parent_line_id IS NOT NULL
                AND quantity IS NOT NULL AND quantity > 0
                AND price_uom IS NOT NULL
                AND uom_qty IS NOT NULL AND price_uom_qty IS NOT NULL
                AND unit_price IS NOT NULL AND unit_price = 0
                AND line_total IS NOT NULL AND line_total = 0
                AND charge_code_id IS NULL AND taxable = FALSE)
            -- a charge: priced, always a code, never a product
            OR (line_type = 'CHARGE'
                AND quantity IS NOT NULL AND quantity > 0
                AND price_uom IS NOT NULL
                AND uom_qty IS NOT NULL AND price_uom_qty IS NOT NULL
                AND unit_price IS NOT NULL AND unit_price >= 0
                AND line_total IS NOT NULL
                AND charge_code_id IS NOT NULL AND product_id IS NULL)
        );
EXCEPTION WHEN duplicate_object OR duplicate_table THEN NULL;
END $$;
DO $$ BEGIN
    ALTER TABLE pos_line_items ADD CONSTRAINT pos_line_items_discount_check
        CHECK (discount_percent IS NULL OR discount_amount IS NULL);
EXCEPTION WHEN duplicate_object OR duplicate_table THEN NULL;
END $$;

-- pos_tenders: the payment each tender became (and, from this item, is).
ALTER TABLE pos_tenders ADD COLUMN IF NOT EXISTS payment_id UUID NULL REFERENCES payments (id);
CREATE INDEX IF NOT EXISTS idx_pos_tenders_payment ON pos_tenders (payment_id);

-- 3. pos_returns and their lines.

ALTER TABLE pos_returns ADD COLUMN IF NOT EXISTS updated_at TIMESTAMPTZ;
UPDATE pos_returns SET updated_at = created_at WHERE updated_at IS NULL;
ALTER TABLE pos_returns ALTER COLUMN updated_at SET NOT NULL;
ALTER TABLE pos_returns ALTER COLUMN updated_at SET DEFAULT NOW();
ALTER TABLE pos_returns ADD COLUMN IF NOT EXISTS revision BIGINT NOT NULL DEFAULT 1;
ALTER TABLE pos_returns ADD COLUMN IF NOT EXISTS currency CHAR(3) NULL;
UPDATE pos_returns t SET currency = COALESCE((SELECT c.currency FROM customers c WHERE c.id = t.customer_id),
                                              (SELECT value FROM system_settings WHERE key = 'currency.default'))
WHERE currency IS NULL;
UPDATE pos_returns SET currency = 'USD' WHERE currency IS NULL;
ALTER TABLE pos_returns ALTER COLUMN currency SET NOT NULL;
DO $$ BEGIN
    ALTER TABLE pos_returns ADD CONSTRAINT pos_returns_currency_check CHECK (currency ~ '^[A-Z]{3}$');
EXCEPTION WHEN duplicate_object OR duplicate_table THEN NULL;
END $$;

ALTER TABLE pos_returns ADD COLUMN IF NOT EXISTS credit_memo_id UUID NULL REFERENCES credit_memos (id);

CREATE SEQUENCE IF NOT EXISTS pos_return_number_seq;
ALTER TABLE pos_returns ADD COLUMN IF NOT EXISTS number TEXT;

WITH ordered AS (
    SELECT id, row_number() OVER (ORDER BY created_at, id) AS n
    FROM pos_returns
    WHERE number IS NULL
)
UPDATE pos_returns t
SET number = 'RTN-' || lpad(o.n::text, GREATEST(6, length(o.n::text)), '0')
FROM ordered o
WHERE t.id = o.id;

SELECT setval('pos_return_number_seq',
              GREATEST((SELECT COALESCE(MAX(substring(number FROM 5)::bigint), 0) + 1 FROM pos_returns), 1),
              false);

ALTER TABLE pos_returns ALTER COLUMN number SET DEFAULT 'RTN-' || lpad(nextval('pos_return_number_seq')::text, 6, '0');
ALTER TABLE pos_returns ALTER COLUMN number SET NOT NULL;
DO $$ BEGIN
    ALTER TABLE pos_returns ADD CONSTRAINT pos_returns_number_key UNIQUE (number);
EXCEPTION WHEN duplicate_object OR duplicate_table THEN NULL;
END $$;

ALTER TABLE pos_return_lines ADD COLUMN IF NOT EXISTS position INTEGER;
WITH ordered AS (
    SELECT id, row_number() OVER (PARTITION BY return_id ORDER BY created_at, id) - 1 AS n
    FROM pos_return_lines
    WHERE position IS NULL
)
UPDATE pos_return_lines l SET position = o.n FROM ordered o WHERE l.id = o.id;
ALTER TABLE pos_return_lines ALTER COLUMN position SET NOT NULL;
ALTER TABLE pos_return_lines ALTER COLUMN position SET DEFAULT 0;
ALTER TABLE pos_return_lines ADD COLUMN IF NOT EXISTS line_type TEXT;
UPDATE pos_return_lines SET line_type = 'PRODUCT' WHERE line_type IS NULL;
ALTER TABLE pos_return_lines ALTER COLUMN line_type SET NOT NULL;
ALTER TABLE pos_return_lines ADD COLUMN IF NOT EXISTS sku TEXT;
ALTER TABLE pos_return_lines ADD COLUMN IF NOT EXISTS price_uom TEXT;
UPDATE pos_return_lines SET price_uom = uom WHERE price_uom IS NULL AND line_type <> 'TEXT';
ALTER TABLE pos_return_lines ADD COLUMN IF NOT EXISTS uom_qty NUMERIC(12, 4);
UPDATE pos_return_lines SET uom_qty = 1 WHERE uom_qty IS NULL AND line_type <> 'TEXT';
ALTER TABLE pos_return_lines ADD COLUMN IF NOT EXISTS price_uom_qty NUMERIC(12, 4);
UPDATE pos_return_lines SET price_uom_qty = 1 WHERE price_uom_qty IS NULL AND line_type <> 'TEXT';
ALTER TABLE pos_return_lines ALTER COLUMN quantity TYPE NUMERIC(12, 4);
ALTER TABLE pos_return_lines ALTER COLUMN unit_price TYPE NUMERIC(12, 4);
ALTER TABLE pos_return_lines ADD COLUMN IF NOT EXISTS taxable BOOLEAN;
UPDATE pos_return_lines l SET taxable = COALESCE((SELECT p.taxable FROM products p WHERE p.id = l.product_id), TRUE)
WHERE taxable IS NULL;
ALTER TABLE pos_return_lines ALTER COLUMN taxable SET NOT NULL;
ALTER TABLE pos_return_lines ALTER COLUMN taxable SET DEFAULT TRUE;
-- The sale line each return line came from: the return's quantity cap and its
-- restock cost read it (a linked return is bounded by the line it names).
ALTER TABLE pos_return_lines ADD COLUMN IF NOT EXISTS sale_line_id UUID NULL REFERENCES pos_line_items (id);
CREATE INDEX IF NOT EXISTS idx_pos_return_lines_sale_line ON pos_return_lines (sale_line_id)
    WHERE sale_line_id IS NOT NULL;

CREATE OR REPLACE FUNCTION pg_temp.mig105_local_date(ts TIMESTAMPTZ, branch UUID) RETURNS DATE
LANGUAGE sql STABLE AS $$
    SELECT (ts AT TIME ZONE COALESCE((SELECT l.timezone FROM locations l WHERE l.id = branch), 'UTC'))::date
$$;

-- Each historic return refunded to ACCOUNT lowered AR with no document: it
-- becomes an OPEN credit memo (ADR 0005 section 13 C2-5 step 3): the return's
-- own GL entry and subledger row already exist, so the memo carries the entry
-- and no new one is written; its lines mirror the return's, negative.
DO $$
DECLARE
    r RECORD;
    memo UUID;
BEGIN
    FOR r IN SELECT * FROM pos_returns WHERE refund_method = 'ACCOUNT' AND credit_memo_id IS NULL ORDER BY created_at, id LOOP
        memo := gen_random_uuid();
        INSERT INTO credit_memos (id, customer_id, amount, reason, status, created_at, updated_at, number, currency,
                                  branch_id, reason_code, subtotal, tax_amount, total_amount, amount_open, memo_date,
                                  gl_entry_id)
        VALUES (memo, r.customer_id, r.total, 'migrated counter account return', 'OPEN', r.created_at, r.created_at,
                credit_memo_next_number(), r.currency, r.branch_id, 'RETURN', -r.subtotal, -r.tax_amount, -r.total,
                -r.total, pg_temp.mig105_local_date(r.created_at, r.branch_id), r.gl_entry_id);
        INSERT INTO credit_memo_lines (credit_memo_id, position, line_type, product_id, description, quantity, uom,
                                       price_uom, uom_qty, price_uom_qty, unit_price, price_source, line_total,
                                       taxable, restock, invoice_line_id, created_at)
        SELECT memo, l.position, 'PRODUCT', l.product_id, l.description, -l.quantity, l.uom, l.price_uom, l.uom_qty,
               l.price_uom_qty, l.unit_price, 'MANUAL', -l.line_total, l.taxable, l.restock, NULL, r.created_at
        FROM pos_return_lines l WHERE l.return_id = r.id;
        IF NOT EXISTS (SELECT 1 FROM credit_memo_lines WHERE credit_memo_id = memo) THEN
            INSERT INTO credit_memo_lines (credit_memo_id, position, line_type, charge_code_id, description, quantity,
                                           uom, price_uom, uom_qty, price_uom_qty, unit_price, price_source,
                                           line_total, taxable, revenue_account_code, restock, created_at)
            SELECT memo, 0, 'CHARGE', cc.id, 'migrated counter account return', -1, 'EA', 'EA', 1, 1, r.total,
                   'MANUAL', -r.total, FALSE, cc.revenue_account_code, FALSE, r.created_at
            FROM charge_codes cc WHERE cc.code = 'ADJUST';
        END IF;
        UPDATE pos_returns SET credit_memo_id = memo WHERE id = r.id;
    END LOOP;
END $$;

-- 4. The walk-in customer (ADR 0005 section 13 C2-5 step 4): the branch the
-- sales fall back to is the default branch, the same one the raw insert path
-- names.
INSERT INTO customers (account_number, name, is_active, primary_branch_id, created_at, updated_at)
SELECT 'WALK-IN', 'Walk-in', TRUE,
       COALESCE((SELECT value::uuid FROM system_settings WHERE key = 'default_branch_id'),
                (SELECT l.id FROM locations l ORDER BY l.created_at, l.id LIMIT 1)),
       NOW(), NOW()
WHERE NOT EXISTS (SELECT 1 FROM customers WHERE account_number = 'WALK-IN');

INSERT INTO system_settings (key, value)
SELECT 'pos.walk_in_customer_id', c.id::text
FROM customers c
WHERE c.account_number = 'WALK-IN'
  AND NOT EXISTS (SELECT 1 FROM system_settings WHERE key = 'pos.walk_in_customer_id');

-- The keyset indexes the lists order on.
CREATE INDEX IF NOT EXISTS idx_pos_transactions_created ON pos_transactions (created_at DESC, id DESC);
CREATE INDEX IF NOT EXISTS idx_pos_returns_created ON pos_returns (created_at DESC, id DESC);
