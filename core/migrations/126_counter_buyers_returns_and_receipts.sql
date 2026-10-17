-- SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
-- SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

-- 126: the counter's account sales, tenders and documents (M24) and the
-- returns M0b leaves (M28), one migration for both items.
--
--   1  Who may buy on the account (CP-02): customer_contacts.authorized_buyer
--      and buyer_sale_cap, customers.named_buyer_required, and the sale's own
--      buyer_contact_id.
--   2  The manager override (CP-11): pos_account_overrides, one act for one
--      sale, consumed by that sale's completion.
--   3  Tenders (CP-03, CP-04): customers.check_limit (a null limit falls to
--      the database wide pos.check_limit_cents setting, and no setting means
--      no limit), and "ID checked" with the ID type on a tender (never an ID
--      number).
--   4  Receipts (CP-14): pos_transactions.print_count and the sealed
--      signature, pos_signatures, whose rows a trigger refuses to update.
--   5  Returns (CP-06): the closed reason code and the restocking fee on each
--      returned line, the fee on the return, the credit memo line shape that
--      admits a restocking fee line, and the exchange link between a return
--      and the sale that replaced it.
--
-- Every step is guarded so a second apply changes nothing.

-- 1. Buyers.

ALTER TABLE customers ADD COLUMN IF NOT EXISTS named_buyer_required BOOLEAN NOT NULL DEFAULT FALSE;
ALTER TABLE customer_contacts ADD COLUMN IF NOT EXISTS authorized_buyer BOOLEAN NOT NULL DEFAULT FALSE;
ALTER TABLE customer_contacts ADD COLUMN IF NOT EXISTS buyer_sale_cap NUMERIC(12, 2) NULL;
DO $$ BEGIN
    ALTER TABLE customer_contacts ADD CONSTRAINT customer_contacts_buyer_sale_cap_positive
        CHECK (buyer_sale_cap IS NULL OR buyer_sale_cap > 0);
EXCEPTION WHEN duplicate_object OR duplicate_table THEN NULL;
END $$;
ALTER TABLE pos_transactions ADD COLUMN IF NOT EXISTS buyer_contact_id UUID NULL
    REFERENCES customer_contacts (id) ON DELETE SET NULL;

-- 2. The manager override. One live (unconsumed) override per sale and kind.

CREATE TABLE IF NOT EXISTS pos_account_overrides (
    id           UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    sale_id      UUID NOT NULL REFERENCES pos_transactions (id) ON DELETE CASCADE,
    kind         TEXT NOT NULL,
    approved_by  TEXT NOT NULL,
    reason       TEXT NOT NULL,
    cap          NUMERIC(12, 2) NOT NULL,
    created_at   TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    consumed_at  TIMESTAMPTZ NULL,
    CONSTRAINT pos_account_overrides_kind_check CHECK (kind IN ('account', 'check')),
    CONSTRAINT pos_account_overrides_approver_named CHECK (approved_by <> ''),
    CONSTRAINT pos_account_overrides_reason_given CHECK (btrim(reason) <> ''),
    CONSTRAINT pos_account_overrides_cap_positive CHECK (cap > 0)
);
CREATE UNIQUE INDEX IF NOT EXISTS uq_pos_account_overrides_live
    ON pos_account_overrides (sale_id, kind) WHERE consumed_at IS NULL;
CREATE INDEX IF NOT EXISTS idx_pos_account_overrides_sale ON pos_account_overrides (sale_id);

-- 3. Tenders: customer check limit, "ID checked" and the ID type.

ALTER TABLE customers ADD COLUMN IF NOT EXISTS check_limit NUMERIC(12, 2) NULL;
DO $$ BEGIN
    ALTER TABLE customers ADD CONSTRAINT customers_check_limit_nonnegative
        CHECK (check_limit IS NULL OR check_limit >= 0);
EXCEPTION WHEN duplicate_object OR duplicate_table THEN NULL;
END $$;
ALTER TABLE pos_tenders ADD COLUMN IF NOT EXISTS id_checked BOOLEAN NOT NULL DEFAULT FALSE;
ALTER TABLE pos_tenders ADD COLUMN IF NOT EXISTS id_type TEXT NULL;
DO $$ BEGIN
    ALTER TABLE pos_tenders ADD CONSTRAINT pos_tenders_id_type_check
        CHECK (id_type IS NULL OR (id_checked AND id_type IN ('drivers_license', 'state_id', 'passport', 'military_id', 'other')));
EXCEPTION WHEN duplicate_object OR duplicate_table THEN NULL;
END $$;

-- 4. Receipts: the print count and the sealed signature.

ALTER TABLE pos_transactions ADD COLUMN IF NOT EXISTS print_count INTEGER NOT NULL DEFAULT 0;
DO $$ BEGIN
    ALTER TABLE pos_transactions ADD CONSTRAINT pos_transactions_print_count_check CHECK (print_count >= 0);
EXCEPTION WHEN duplicate_object OR duplicate_table THEN NULL;
END $$;

CREATE TABLE IF NOT EXISTS pos_signatures (
    id            UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    sale_id       UUID NOT NULL UNIQUE REFERENCES pos_transactions (id) ON DELETE CASCADE,
    signer_name   TEXT NOT NULL,
    image         BYTEA NOT NULL,
    image_sha256  TEXT NOT NULL,
    total_cents   BIGINT NOT NULL,
    signed_at     TIMESTAMPTZ NOT NULL,
    seal          TEXT NOT NULL,
    captured_by   TEXT NOT NULL DEFAULT '',
    created_at    TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    CONSTRAINT pos_signatures_signer_named CHECK (btrim(signer_name) <> ''),
    CONSTRAINT pos_signatures_image_capped CHECK (octet_length(image) BETWEEN 1 AND 131072),
    CONSTRAINT pos_signatures_hashes_hex CHECK (image_sha256 ~ '^[0-9a-f]{64}$' AND seal ~ '^[0-9a-f]{64}$')
);

CREATE OR REPLACE FUNCTION pos_signatures_sealed() RETURNS trigger
LANGUAGE plpgsql AS $$
BEGIN
    RAISE EXCEPTION 'a sealed signature never changes' USING ERRCODE = 'integrity_constraint_violation';
END $$;
DROP TRIGGER IF EXISTS pos_signatures_sealed_trg ON pos_signatures;
CREATE TRIGGER pos_signatures_sealed_trg BEFORE UPDATE ON pos_signatures
    FOR EACH ROW EXECUTE FUNCTION pos_signatures_sealed();

-- 5. Returns: reason code, restocking fee, the memo line that carries the
-- fee, and the exchange link.

ALTER TABLE pos_return_lines ADD COLUMN IF NOT EXISTS reason_code TEXT NOT NULL DEFAULT 'other';
DO $$ BEGIN
    ALTER TABLE pos_return_lines ADD CONSTRAINT pos_return_lines_reason_code_check
        CHECK (reason_code IN ('defective', 'damaged', 'wrong_item', 'ordered_wrong', 'changed_mind', 'overbought', 'other'));
EXCEPTION WHEN duplicate_object OR duplicate_table THEN NULL;
END $$;
ALTER TABLE pos_return_lines ADD COLUMN IF NOT EXISTS restocking_fee NUMERIC(12, 2) NOT NULL DEFAULT 0;
DO $$ BEGIN
    ALTER TABLE pos_return_lines ADD CONSTRAINT pos_return_lines_restocking_fee_check CHECK (restocking_fee >= 0);
EXCEPTION WHEN duplicate_object OR duplicate_table THEN NULL;
END $$;
ALTER TABLE pos_returns ADD COLUMN IF NOT EXISTS restocking_fee NUMERIC(12, 2) NOT NULL DEFAULT 0;
DO $$ BEGIN
    ALTER TABLE pos_returns ADD CONSTRAINT pos_returns_restocking_fee_check CHECK (restocking_fee >= 0);
EXCEPTION WHEN duplicate_object OR duplicate_table THEN NULL;
END $$;

-- The credit memo line shape admits one more form: a restocking fee line, a
-- charge of positive quantity and amount that lowers the credit. Every other
-- shape is exactly as it was.
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
    OR (line_type = 'CHARGE' AND quantity IS NOT NULL AND quantity > 0 AND uom IS NOT NULL
        AND price_uom IS NOT NULL AND uom_qty IS NOT NULL AND price_uom_qty IS NOT NULL
        AND unit_price IS NOT NULL AND unit_price >= 0 AND line_total IS NOT NULL AND line_total >= 0
        AND charge_code_id IS NOT NULL AND product_id IS NULL AND taxable = FALSE)
);

-- The exchange: a return and the sale that replaced its goods point at each
-- other. Either side leaves the other standing when it goes.
ALTER TABLE pos_returns ADD COLUMN IF NOT EXISTS exchange_sale_id UUID NULL
    REFERENCES pos_transactions (id) ON DELETE SET NULL;
ALTER TABLE pos_transactions ADD COLUMN IF NOT EXISTS exchange_return_id UUID NULL
    REFERENCES pos_returns (id) ON DELETE SET NULL;
CREATE INDEX IF NOT EXISTS idx_pos_returns_exchange_sale ON pos_returns (exchange_sale_id)
    WHERE exchange_sale_id IS NOT NULL;
CREATE INDEX IF NOT EXISTS idx_pos_transactions_exchange_return ON pos_transactions (exchange_return_id)
    WHERE exchange_return_id IS NOT NULL;
