-- SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
-- SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

-- Down of 126_counter_buyers_returns_and_receipts: drops what the up added,
-- in the reverse order, and puts the credit memo line shape back as it was.
--
-- Lives in migrations/down/ so cmd/migrate's `migrations/*.sql` glob cannot
-- pick it up and apply it as a forward migration. Apply by hand.
--
-- What does not map back: the up's data (buyers, overrides, check limits,
-- ID checks, print counts, sealed signatures, reason codes, restocking fees
-- and the exchange links) goes with its columns and tables. A restocking fee
-- line on a credit memo cannot exist in the old shape, so those lines are
-- deleted first; the memo's own totals and its ledger entry already carry
-- the fee, so no balance changes, only the line that showed it.

DROP INDEX IF EXISTS idx_pos_transactions_exchange_return;
DROP INDEX IF EXISTS idx_pos_returns_exchange_sale;
ALTER TABLE pos_transactions DROP COLUMN IF EXISTS exchange_return_id;
ALTER TABLE pos_returns DROP COLUMN IF EXISTS exchange_sale_id;

DELETE FROM credit_memo_lines WHERE line_type = 'CHARGE' AND quantity > 0;
ALTER TABLE credit_memo_lines DROP CONSTRAINT IF EXISTS credit_memo_lines_shape;
ALTER TABLE credit_memo_lines ADD CONSTRAINT credit_memo_lines_shape CHECK (
    (line_type = 'TEXT' AND quantity IS NULL AND uom IS NULL AND price_uom IS NULL AND uom_qty IS NULL
        AND price_uom_qty IS NULL AND unit_price IS NULL AND line_total IS NULL AND product_id IS NULL
        AND charge_code_id IS NULL)
    OR (line_type IN ('PRODUCT', 'KIT') AND quantity IS NOT NULL AND quantity < 0 AND uom IS NOT NULL
        AND price_uom IS NOT NULL AND uom_qty IS NOT NULL AND price_uom_qty IS NOT NULL
        AND unit_price IS NOT NULL AND unit_price >= 0 AND line_total IS NOT NULL AND line_total <= 0
        AND charge_code_id IS NULL)
    OR (line_type = 'COMPONENT' AND product_id IS NOT NULL AND parent_line_id IS NOT NULL AND quantity IS NOT NULL
        AND quantity < 0 AND uom IS NOT NULL AND price_uom IS NOT NULL AND uom_qty IS NOT NULL
        AND price_uom_qty IS NOT NULL AND unit_price IS NOT NULL AND unit_price = 0 AND line_total IS NOT NULL
        AND line_total = 0 AND charge_code_id IS NULL AND taxable = FALSE)
    OR (line_type = 'CHARGE' AND quantity IS NOT NULL AND quantity < 0 AND uom IS NOT NULL
        AND price_uom IS NOT NULL AND uom_qty IS NOT NULL AND price_uom_qty IS NOT NULL
        AND unit_price IS NOT NULL AND unit_price >= 0 AND line_total IS NOT NULL AND line_total <= 0
        AND charge_code_id IS NOT NULL AND product_id IS NULL)
);

ALTER TABLE pos_returns DROP CONSTRAINT IF EXISTS pos_returns_restocking_fee_check;
ALTER TABLE pos_returns DROP COLUMN IF EXISTS restocking_fee;
ALTER TABLE pos_return_lines DROP CONSTRAINT IF EXISTS pos_return_lines_restocking_fee_check;
ALTER TABLE pos_return_lines DROP COLUMN IF EXISTS restocking_fee;
ALTER TABLE pos_return_lines DROP CONSTRAINT IF EXISTS pos_return_lines_reason_code_check;
ALTER TABLE pos_return_lines DROP COLUMN IF EXISTS reason_code;

DROP TRIGGER IF EXISTS pos_signatures_sealed_trg ON pos_signatures;
DROP FUNCTION IF EXISTS pos_signatures_sealed();
DROP TABLE IF EXISTS pos_signatures;
ALTER TABLE pos_transactions DROP CONSTRAINT IF EXISTS pos_transactions_print_count_check;
ALTER TABLE pos_transactions DROP COLUMN IF EXISTS print_count;

ALTER TABLE pos_tenders DROP CONSTRAINT IF EXISTS pos_tenders_id_type_check;
ALTER TABLE pos_tenders DROP COLUMN IF EXISTS id_type;
ALTER TABLE pos_tenders DROP COLUMN IF EXISTS id_checked;
ALTER TABLE customers DROP CONSTRAINT IF EXISTS customers_check_limit_nonnegative;
ALTER TABLE customers DROP COLUMN IF EXISTS check_limit;

DROP TABLE IF EXISTS pos_account_overrides;

ALTER TABLE pos_transactions DROP COLUMN IF EXISTS buyer_contact_id;
ALTER TABLE customer_contacts DROP CONSTRAINT IF EXISTS customer_contacts_buyer_sale_cap_positive;
ALTER TABLE customer_contacts DROP COLUMN IF EXISTS buyer_sale_cap;
ALTER TABLE customer_contacts DROP COLUMN IF EXISTS authorized_buyer;
ALTER TABLE customers DROP COLUMN IF EXISTS named_buyer_required;
