-- SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
-- SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

-- Down of 105 (pos_wire_contract): the counter leaves the wire contract.
-- History moves back as data; no entry or subledger row the up file never
-- wrote is invented here. The migrated ACCOUNT-return credit memos are
-- deleted (their GL entries stay, owned by the returns as before), and the
-- walk-in customer goes only when nothing references it.

-- The down deletes the kit, component, charge and text lines the old shape
-- cannot hold. A completed or voided sale's lines are history the invoice
-- and the returns read, so the down refuses while any belongs to one, naming
-- the count, and changes nothing.
DO $$
DECLARE held BIGINT;
BEGIN
    IF EXISTS (SELECT 1 FROM information_schema.columns
               WHERE table_schema = current_schema() AND table_name = 'pos_line_items' AND column_name = 'line_type') THEN
        EXECUTE 'SELECT count(*) FROM pos_line_items l JOIN pos_transactions t ON t.id = l.transaction_id
                 WHERE l.line_type IN (''KIT'', ''COMPONENT'', ''CHARGE'', ''TEXT'') AND t.status IN (''COMPLETED'', ''VOIDED'')'
            INTO held;
        IF held > 0 THEN
            RAISE EXCEPTION '105 down refused: % kit, component, charge or text lines belong to completed or voided sales; the old line shape cannot hold them', held;
        END IF;
    END IF;
END $$;

-- The down drops pos_refund_intents with its rows, and a PENDING claim
-- dropped that way silently releases its tender (the next refund of the
-- card would pay it twice), so the down refuses while any claim is still
-- pending, naming the count and changing nothing.
DO $$
DECLARE pending BIGINT;
BEGIN
    IF EXISTS (SELECT 1 FROM information_schema.tables
               WHERE table_schema = current_schema() AND table_name = 'pos_refund_intents') THEN
        SELECT count(*) INTO pending FROM pos_refund_intents WHERE state = 'PENDING';
        IF pending > 0 THEN
            RAISE EXCEPTION '105 down refused: % refund claims are still pending; dropping the intents table would release their tenders', pending;
        END IF;
    END IF;
END $$;

DROP INDEX IF EXISTS idx_pos_returns_created;
DROP INDEX IF EXISTS idx_pos_transactions_created;

-- 4. the walk-in customer.
DELETE FROM system_settings WHERE key = 'pos.walk_in_customer_id';
DELETE FROM customers c
WHERE c.account_number = 'WALK-IN'
  AND NOT EXISTS (SELECT 1 FROM pos_transactions t WHERE t.customer_id = c.id)
  AND NOT EXISTS (SELECT 1 FROM pos_returns r WHERE r.customer_id = c.id)
  AND NOT EXISTS (SELECT 1 FROM invoices i WHERE i.customer_id = c.id)
  AND NOT EXISTS (SELECT 1 FROM payments p WHERE p.customer_id = c.id)
  AND NOT EXISTS (SELECT 1 FROM credit_memos m WHERE m.customer_id = c.id)
  AND NOT EXISTS (SELECT 1 FROM customer_transactions ct WHERE ct.customer_id = c.id);

-- 3. the returns. The link's foreign key drops first (it blocks the memo
-- delete), the memos are found by the up's own reason marker, and the link
-- column goes last.
DELETE FROM pos_line_items WHERE product_id IS NULL;

ALTER TABLE pos_returns DROP CONSTRAINT IF EXISTS pos_returns_credit_memo_id_fkey;
DELETE FROM credit_memo_lines WHERE credit_memo_id IN
  (SELECT id FROM credit_memos WHERE reason = 'migrated counter account return');
DELETE FROM credit_memos WHERE reason = 'migrated counter account return';
-- The counter the up's memos drew from comes back (the C2-4 down's rule), so
-- an up after this down mints the same numbers and the gapless series keeps
-- no gap.
UPDATE document_counters
SET next_value = GREATEST(1, COALESCE((SELECT MAX(substring(number FROM 4)::bigint) FROM credit_memos WHERE number ~ '^CM-[0-9]+$'), 0) + 1)
WHERE series = 'credit_memo';
ALTER TABLE pos_returns DROP COLUMN IF EXISTS credit_memo_id;

ALTER TABLE pos_return_lines DROP COLUMN IF EXISTS sale_line_id;
DELETE FROM pos_return_lines WHERE product_id IS NULL;
ALTER TABLE pos_return_lines ALTER COLUMN product_id SET NOT NULL;

ALTER TABLE pos_return_lines DROP COLUMN IF EXISTS taxable;
ALTER TABLE pos_return_lines ALTER COLUMN unit_price TYPE NUMERIC(12, 2);
ALTER TABLE pos_return_lines ALTER COLUMN quantity TYPE DECIMAL(12, 4);
ALTER TABLE pos_return_lines DROP COLUMN IF EXISTS price_uom_qty;
ALTER TABLE pos_return_lines DROP COLUMN IF EXISTS uom_qty;
ALTER TABLE pos_return_lines DROP COLUMN IF EXISTS price_uom;
ALTER TABLE pos_return_lines DROP COLUMN IF EXISTS sku;
ALTER TABLE pos_return_lines DROP COLUMN IF EXISTS line_type;
ALTER TABLE pos_return_lines DROP COLUMN IF EXISTS position;

ALTER TABLE pos_returns DROP CONSTRAINT IF EXISTS pos_returns_number_key;
ALTER TABLE pos_returns ALTER COLUMN number DROP NOT NULL;
ALTER TABLE pos_returns ALTER COLUMN number DROP DEFAULT;
ALTER TABLE pos_returns DROP COLUMN IF EXISTS number;
DROP SEQUENCE IF EXISTS pos_return_number_seq;
ALTER TABLE pos_returns DROP CONSTRAINT IF EXISTS pos_returns_currency_check;
ALTER TABLE pos_returns DROP COLUMN IF EXISTS currency;
ALTER TABLE pos_returns DROP COLUMN IF EXISTS revision;
ALTER TABLE pos_returns DROP COLUMN IF EXISTS updated_at;

-- 2. the lines and tenders. The refund intents go first (their foreign keys
-- block the tender and sale deletes): the table goes with its rows, DONE and
-- FAILED alike, which is why the refusal above holds while any claim is
-- still PENDING (a dropped PENDING claim releases its tender).
DROP TABLE IF EXISTS pos_refund_intents;
DROP INDEX IF EXISTS idx_pos_tenders_payment;
ALTER TABLE pos_tenders DROP COLUMN IF EXISTS payment_id;

DROP INDEX IF EXISTS idx_pos_line_items_invoice_line;
ALTER TABLE pos_line_items DROP COLUMN IF EXISTS invoice_line_id;

ALTER TABLE pos_line_items DROP CONSTRAINT IF EXISTS pos_line_items_discount_check;
ALTER TABLE pos_line_items DROP CONSTRAINT IF EXISTS pos_line_items_shape;
ALTER TABLE pos_line_items DROP CONSTRAINT IF EXISTS pos_line_items_pair_positive;
ALTER TABLE pos_line_items DROP CONSTRAINT IF EXISTS pos_line_items_price_source_check;
ALTER TABLE pos_line_items DROP CONSTRAINT IF EXISTS pos_line_items_line_type_check;
DELETE FROM pos_line_items WHERE line_type IN ('KIT', 'COMPONENT', 'CHARGE', 'TEXT');
DELETE FROM pos_line_items WHERE product_id IS NULL;
ALTER TABLE pos_line_items ALTER COLUMN unit_price TYPE NUMERIC(12, 2);
ALTER TABLE pos_line_items ALTER COLUMN unit_price SET NOT NULL;
ALTER TABLE pos_line_items ALTER COLUMN uom SET NOT NULL;
ALTER TABLE pos_line_items ALTER COLUMN quantity SET NOT NULL;
ALTER TABLE pos_line_items ALTER COLUMN quantity TYPE DECIMAL(12, 4);
ALTER TABLE pos_line_items ALTER COLUMN line_total SET NOT NULL;
ALTER TABLE pos_line_items ALTER COLUMN product_id SET NOT NULL;
ALTER TABLE pos_line_items DROP COLUMN IF EXISTS revenue_account_code;
ALTER TABLE pos_line_items DROP COLUMN IF EXISTS price_adjusted_by;
ALTER TABLE pos_line_items DROP COLUMN IF EXISTS discount_reason;
ALTER TABLE pos_line_items DROP COLUMN IF EXISTS discount_amount;
ALTER TABLE pos_line_items DROP COLUMN IF EXISTS discount_percent;
ALTER TABLE pos_line_items DROP COLUMN IF EXISTS override_reason;
ALTER TABLE pos_line_items DROP COLUMN IF EXISTS price_source;
ALTER TABLE pos_line_items DROP COLUMN IF EXISTS priced_unit_price;
ALTER TABLE pos_line_items DROP COLUMN IF EXISTS price_uom_qty;
ALTER TABLE pos_line_items DROP COLUMN IF EXISTS uom_qty;
ALTER TABLE pos_line_items DROP COLUMN IF EXISTS price_uom;
ALTER TABLE pos_line_items DROP COLUMN IF EXISTS sku;
ALTER TABLE pos_line_items DROP COLUMN IF EXISTS charge_code_id;
ALTER TABLE pos_line_items DROP COLUMN IF EXISTS parent_line_id;
ALTER TABLE pos_line_items DROP COLUMN IF EXISTS line_type;
ALTER TABLE pos_line_items DROP COLUMN IF EXISTS position;
ALTER TABLE pos_line_items DROP COLUMN IF EXISTS taxable;

-- 1. the sales. The invoice link column stays: the invoices themselves
-- survive this down (105 never created them), so the link is history, and
-- keeping it lets the up's backfill restore each sale line's invoice line
-- link after a down then up cycle (the r6 note on P3-3).
-- 1. the sales.
ALTER TABLE pos_transactions DROP CONSTRAINT IF EXISTS pos_transactions_status_check;
ALTER TABLE pos_transactions DROP CONSTRAINT IF EXISTS pos_transactions_number_key;
ALTER TABLE pos_transactions ALTER COLUMN number DROP NOT NULL;
ALTER TABLE pos_transactions ALTER COLUMN number DROP DEFAULT;
ALTER TABLE pos_transactions DROP COLUMN IF EXISTS number;
DROP SEQUENCE IF EXISTS pos_transaction_number_seq;
ALTER TABLE pos_transactions DROP CONSTRAINT IF EXISTS pos_transactions_currency_check;
ALTER TABLE pos_transactions DROP COLUMN IF EXISTS currency;
ALTER TABLE pos_transactions DROP COLUMN IF EXISTS revision;
ALTER TABLE pos_transactions ALTER COLUMN updated_at DROP NOT NULL;
ALTER TABLE pos_transactions ALTER COLUMN updated_at DROP DEFAULT;
ALTER TABLE pos_transactions DROP COLUMN IF EXISTS updated_at;
ALTER TABLE pos_transactions ALTER COLUMN created_at DROP DEFAULT;
