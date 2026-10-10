-- SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
-- SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

-- Down of 106_ap_wire_contract (item C4-1b). Each step reverses its own, and
-- refuses, naming the rows it cannot map back, wherever data written in the
-- new shape has no place in the old one (a down never discards a dealer's
-- data):
--
--   3  drops the keyset indexes;
--   2  drops the line link columns and the position, and narrows unit_price
--      back to scale 2 only when every stored value is a whole cent, raising
--      an exception naming the count otherwise;
--   1  drops amount_open, the number and its sequence, the currency, the
--      revision and the branch, the two uniqueness constraints and the
--      status CHECK. Three of the up's writes are kept, each valid old shape
--      data: the suffixed duplicate vendor numbers stay suffixed (a dealer
--      may have had a genuine "X #2" before, so un-suffixing cannot be told
--      apart from it), the normalized statuses stay normalized, and a po_id
--      the up orphaned stays null (the purchase order it named is gone).

-- 3 -------------------------------------------------------------------------

DROP INDEX IF EXISTS idx_vendor_invoices_po;
DROP INDEX IF EXISTS idx_vendor_invoices_vendor_created;
DROP INDEX IF EXISTS idx_vendor_invoices_created_id;

-- 2 -------------------------------------------------------------------------

DROP TRIGGER IF EXISTS trg_vendor_invoice_lines_default_position ON vendor_invoice_lines;
DROP FUNCTION IF EXISTS vendor_invoice_lines_default_position();
ALTER TABLE vendor_invoice_lines DROP COLUMN IF EXISTS po_freight_charge_id;
ALTER TABLE vendor_invoice_lines DROP COLUMN IF EXISTS product_id;
ALTER TABLE vendor_invoice_lines DROP COLUMN IF EXISTS purchase_order_line_id;
ALTER TABLE vendor_invoice_lines DROP COLUMN IF EXISTS position;
DO $$
DECLARE fine BIGINT;
BEGIN
    SELECT count(*) INTO fine FROM vendor_invoice_lines WHERE unit_price <> ROUND(unit_price, 2);
    IF fine > 0 THEN
        RAISE EXCEPTION 'vendor_invoice_lines holds % row(s) with a unit price finer than cents; the scale 2 column cannot carry them', fine;
    END IF;
    ALTER TABLE vendor_invoice_lines ALTER COLUMN unit_price TYPE NUMERIC(12, 2);
END $$;

-- 1 -------------------------------------------------------------------------

ALTER TABLE vendor_invoices DROP CONSTRAINT IF EXISTS vendor_invoices_amount_open_range;
ALTER TABLE vendor_invoices DROP COLUMN IF EXISTS amount_open;
ALTER TABLE vendor_invoices DROP COLUMN IF EXISTS gl_entry_id;
ALTER TABLE vendor_invoices DROP CONSTRAINT IF EXISTS vendor_invoices_number_key;
ALTER TABLE vendor_invoices DROP CONSTRAINT IF EXISTS vendor_invoices_vendor_number_key;
ALTER TABLE vendor_invoices DROP CONSTRAINT IF EXISTS vendor_invoices_status_check;
ALTER TABLE vendor_invoices DROP CONSTRAINT IF EXISTS vendor_invoices_currency_format;
ALTER TABLE vendor_invoices DROP CONSTRAINT IF EXISTS vendor_invoices_po_id_fkey;
ALTER TABLE vendor_invoices DROP COLUMN IF EXISTS currency;
ALTER TABLE vendor_invoices DROP COLUMN IF EXISTS number;
ALTER TABLE vendor_invoices DROP COLUMN IF EXISTS revision;
DROP TRIGGER IF EXISTS trg_vendor_invoices_default_branch ON vendor_invoices;
DROP TRIGGER IF EXISTS trg_vendor_invoices_defaults ON vendor_invoices;
DROP FUNCTION IF EXISTS vendor_invoices_defaults();
DROP FUNCTION IF EXISTS vendor_invoices_default_branch();
ALTER TABLE vendor_invoices DROP COLUMN IF EXISTS branch_id;
DROP FUNCTION IF EXISTS vendor_invoice_next_number();
DROP SEQUENCE IF EXISTS vendor_invoice_number_seq;
