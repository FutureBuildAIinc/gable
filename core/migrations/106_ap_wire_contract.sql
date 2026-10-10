-- SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
-- SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

-- 106: the AP fixes onto the wire contract (item C4-1b, ADR 0008 sections
-- 7.4 and 12, on ADR 0001). The steps are the record's C4-1b list, in order:
--
--   1  vendor_invoices: created_at NOT NULL, revision, the number (AP-), the
--      branch (from the purchase order's branch when that column exists,
--      else the default branch), the currency, a status CHECK on today's
--      values (unknown values become PENDING, with a notice), po_id as a
--      real foreign key (orphans are set null, with a notice), UNIQUE
--      (vendor_id, invoice_number) after duplicates are suffixed #2, #3 in
--      (created_at, id) order (with a notice), and amount_open.
--   2  vendor_invoice_lines: position, purchase_order_line_id, product_id,
--      unit_price widened to NUMERIC(12,4), po_freight_charge_id (the
--      carrier freight line link of 7.3, written from package C's freight
--      rule on). Existing lines are not linked: today's lines have no
--      reliable key to their purchase line, and the old positional pairing
--      is exactly the defect.
--   3  Keyset indexes on vendor invoices.
--
-- Every statement is guarded so a second apply is a no-op.

-- 1. vendor_invoices --------------------------------------------------------

UPDATE vendor_invoices SET created_at = NOW() WHERE created_at IS NULL;
ALTER TABLE vendor_invoices ALTER COLUMN created_at SET NOT NULL;
ALTER TABLE vendor_invoices ADD COLUMN IF NOT EXISTS revision BIGINT NOT NULL DEFAULT 1;

-- The branch: the purchase order's when C4-1a's column is already there,
-- else the default branch for every row.
ALTER TABLE vendor_invoices ADD COLUMN IF NOT EXISTS branch_id UUID NULL REFERENCES locations (id);
DO $$
BEGIN
    IF EXISTS (SELECT 1 FROM information_schema.columns
               WHERE table_schema = current_schema() AND table_name = 'purchase_orders' AND column_name = 'branch_id') THEN
        EXECUTE $sql$UPDATE vendor_invoices vi SET branch_id = po.branch_id
        FROM purchase_orders po
        WHERE po.id = vi.po_id AND vi.branch_id IS NULL AND po.branch_id IS NOT NULL$sql$;
    END IF;
END $$;
DO $$
DECLARE missing BIGINT;
BEGIN
    SELECT count(*) INTO missing FROM vendor_invoices WHERE branch_id IS NULL;
    UPDATE vendor_invoices
    SET branch_id = (SELECT value::uuid FROM system_settings WHERE key = 'default_branch_id')
    WHERE branch_id IS NULL;
    IF missing > 0 THEN
        RAISE NOTICE 'migration 106: % vendor invoice(s) had no branch and take the default branch', missing;
    END IF;
END $$;
ALTER TABLE vendor_invoices ALTER COLUMN branch_id SET NOT NULL;

-- A raw insert (the seed, the characterization fixtures) carries no branch,
-- currency or open amount: it opens at the default branch, in the default
-- currency, owing its whole total, as the service would set it (the invoices
-- migration's amount_open trigger is the shape).
CREATE OR REPLACE FUNCTION vendor_invoices_defaults() RETURNS trigger
LANGUAGE plpgsql AS $$
BEGIN
    IF NEW.branch_id IS NULL THEN
        NEW.branch_id := (SELECT value::uuid FROM system_settings WHERE key = 'default_branch_id');
    END IF;
    IF NEW.currency IS NULL THEN
        NEW.currency := COALESCE((SELECT value FROM system_settings WHERE key = 'currency.default'), 'USD');
    END IF;
    IF NEW.amount_open IS NULL THEN
        NEW.amount_open := CASE WHEN NEW.status IN ('VOIDED', 'PAID') THEN 0 ELSE NEW.total END;
    END IF;
    RETURN NEW;
END $$;
DROP TRIGGER IF EXISTS trg_vendor_invoices_default_branch ON vendor_invoices;
DROP TRIGGER IF EXISTS trg_vendor_invoices_defaults ON vendor_invoices;
CREATE TRIGGER trg_vendor_invoices_defaults BEFORE INSERT ON vendor_invoices
    FOR EACH ROW EXECUTE FUNCTION vendor_invoices_defaults();

ALTER TABLE vendor_invoices ADD COLUMN IF NOT EXISTS currency CHAR(3) NULL;
UPDATE vendor_invoices SET currency = COALESCE((SELECT s.value FROM system_settings s WHERE s.key = 'currency.default'), 'USD')
WHERE currency IS NULL;
ALTER TABLE vendor_invoices ALTER COLUMN currency SET NOT NULL;
DO $$ BEGIN
    ALTER TABLE vendor_invoices ADD CONSTRAINT vendor_invoices_currency_format CHECK (currency ~ '^[A-Z]{3}$');
EXCEPTION WHEN duplicate_object THEN NULL;
END $$;

-- The status CHECK on today's values: lowercase spellings are uppercased and
-- anything unknown becomes PENDING, each with a notice.
DO $$
DECLARE lower BIGINT; unknown BIGINT;
BEGIN
    SELECT count(*) INTO lower FROM vendor_invoices WHERE status <> UPPER(btrim(status));
    IF lower > 0 THEN
        UPDATE vendor_invoices SET status = UPPER(btrim(status)) WHERE status <> UPPER(btrim(status));
        RAISE NOTICE 'migration 106: % vendor invoice status value(s) were not uppercase and are uppercased', lower;
    END IF;
    SELECT count(*) INTO unknown FROM vendor_invoices
    WHERE status NOT IN ('PENDING', 'APPROVED', 'PARTIAL', 'PAID', 'VOIDED');
    IF unknown > 0 THEN
        UPDATE vendor_invoices SET status = 'PENDING'
        WHERE status NOT IN ('PENDING', 'APPROVED', 'PARTIAL', 'PAID', 'VOIDED');
        RAISE NOTICE 'migration 106: % vendor invoice(s) held an unknown status and become PENDING', unknown;
    END IF;
END $$;
ALTER TABLE vendor_invoices DROP CONSTRAINT IF EXISTS vendor_invoices_status_check;
ALTER TABLE vendor_invoices ADD CONSTRAINT vendor_invoices_status_check
    CHECK (status IN ('PENDING', 'APPROVED', 'PARTIAL', 'PAID', 'VOIDED'));

-- po_id becomes a real foreign key; an orphan is set null, with a notice.
DO $$
DECLARE orphans BIGINT;
BEGIN
    SELECT count(*) INTO orphans FROM vendor_invoices vi
    WHERE vi.po_id IS NOT NULL AND NOT EXISTS (SELECT 1 FROM purchase_orders po WHERE po.id = vi.po_id);
    IF orphans > 0 THEN
        UPDATE vendor_invoices vi SET po_id = NULL
        WHERE vi.po_id IS NOT NULL AND NOT EXISTS (SELECT 1 FROM purchase_orders po WHERE po.id = vi.po_id);
        RAISE NOTICE 'migration 106: % vendor invoice(s) named no purchase order and their po_id is set null', orphans;
    END IF;
END $$;
DO $$ BEGIN
    ALTER TABLE vendor_invoices DROP CONSTRAINT IF EXISTS vendor_invoices_po_id_fkey;
    ALTER TABLE vendor_invoices ADD CONSTRAINT vendor_invoices_po_id_fkey
        FOREIGN KEY (po_id) REFERENCES purchase_orders (id) ON DELETE SET NULL;
EXCEPTION WHEN duplicate_object THEN NULL;
END $$;

-- Gable's own number, AP-, from a sequence (gapped: a create that rolls back
-- abandons its number), the payment migration's shape.
CREATE SEQUENCE IF NOT EXISTS vendor_invoice_number_seq;
CREATE OR REPLACE FUNCTION vendor_invoice_next_number() RETURNS TEXT
LANGUAGE sql AS $$
    SELECT 'AP-' || lpad(s.n::text, GREATEST(6, length(s.n::text)), '0')
    FROM (SELECT nextval('vendor_invoice_number_seq') AS n) s
$$;
ALTER TABLE vendor_invoices ADD COLUMN IF NOT EXISTS number TEXT;
WITH ordered AS (
    SELECT id, row_number() OVER (ORDER BY created_at, id) AS n FROM vendor_invoices WHERE number IS NULL
)
UPDATE vendor_invoices p
SET number = 'AP-' || lpad((o.n + COALESCE((SELECT MAX(substring(number FROM 4)::bigint) FROM vendor_invoices WHERE number ~ '^AP-[0-9]+$'), 0))::text,
                           GREATEST(6, length((o.n + COALESCE((SELECT MAX(substring(number FROM 4)::bigint) FROM vendor_invoices WHERE number ~ '^AP-[0-9]+$'), 0))::text)), '0')
FROM ordered o WHERE p.id = o.id;
SELECT setval('vendor_invoice_number_seq',
              GREATEST(COALESCE((SELECT MAX(substring(number FROM 4)::bigint) FROM vendor_invoices WHERE number ~ '^AP-[0-9]+$'), 0) + 1,
                       (SELECT CASE WHEN is_called THEN last_value + 1 ELSE last_value END FROM vendor_invoice_number_seq)),
              false);
ALTER TABLE vendor_invoices ALTER COLUMN number SET DEFAULT vendor_invoice_next_number();
ALTER TABLE vendor_invoices ALTER COLUMN number SET NOT NULL;
DO $$ BEGIN
    ALTER TABLE vendor_invoices ADD CONSTRAINT vendor_invoices_number_key UNIQUE (number);
EXCEPTION WHEN duplicate_object OR duplicate_table THEN NULL;
END $$;

-- The vendor's own number is unique per vendor once duplicates are suffixed
-- #2, #3 in (created_at, id) order, the oldest keeping the bare number.
DO $$
DECLARE dups BIGINT;
BEGIN
    SELECT count(*) - count(DISTINCT (vendor_id, invoice_number)) INTO dups FROM vendor_invoices;
    IF dups > 0 THEN
        WITH ranked AS (
            SELECT id, row_number() OVER (PARTITION BY vendor_id, invoice_number ORDER BY created_at, id) AS rn
            FROM vendor_invoices
        )
        UPDATE vendor_invoices vi
        SET invoice_number = left(vi.invoice_number, 64 - length(' #' || r.rn)) || ' #' || r.rn
        FROM ranked r
        WHERE r.id = vi.id AND r.rn > 1
          AND vi.invoice_number <> left(vi.invoice_number, 64 - length(' #' || r.rn)) || ' #' || r.rn;
        RAISE NOTICE 'migration 106: % duplicate vendor invoice number(s) suffixed #2, #3 in (created_at, id) order', dups;
    END IF;
END $$;
DO $$ BEGIN
    ALTER TABLE vendor_invoices ADD CONSTRAINT vendor_invoices_vendor_number_key UNIQUE (vendor_id, invoice_number);
EXCEPTION WHEN duplicate_object OR duplicate_table THEN NULL;
END $$;

-- The entry the approval posts, which the void transition reverses.
ALTER TABLE vendor_invoices ADD COLUMN IF NOT EXISTS gl_entry_id UUID NULL REFERENCES gl_journal_entries (id);

-- amount_open: what the vendor is still owed, zero on a voided bill.
ALTER TABLE vendor_invoices ADD COLUMN IF NOT EXISTS amount_open NUMERIC(12, 2) NULL;
UPDATE vendor_invoices SET amount_open = CASE WHEN status = 'VOIDED' THEN 0 ELSE GREATEST(0, total - amount_paid) END
WHERE amount_open IS NULL;
ALTER TABLE vendor_invoices ALTER COLUMN amount_open SET NOT NULL;
ALTER TABLE vendor_invoices DROP CONSTRAINT IF EXISTS vendor_invoices_amount_open_range;
ALTER TABLE vendor_invoices ADD CONSTRAINT vendor_invoices_amount_open_range
    CHECK (amount_open >= 0 AND amount_open <= total);

-- 2. vendor_invoice_lines ---------------------------------------------------

ALTER TABLE vendor_invoice_lines ADD COLUMN IF NOT EXISTS position INTEGER NULL;

-- A raw line insert (the characterization fixtures) carries no position: it
-- takes the next position of its bill.
CREATE OR REPLACE FUNCTION vendor_invoice_lines_default_position() RETURNS trigger
LANGUAGE plpgsql AS $$
BEGIN
    IF NEW.position IS NULL THEN
        NEW.position := (SELECT COALESCE(MAX(position) + 1, 0) FROM vendor_invoice_lines WHERE invoice_id = NEW.invoice_id);
    END IF;
    RETURN NEW;
END $$;
DROP TRIGGER IF EXISTS trg_vendor_invoice_lines_default_position ON vendor_invoice_lines;
CREATE TRIGGER trg_vendor_invoice_lines_default_position BEFORE INSERT ON vendor_invoice_lines
    FOR EACH ROW EXECUTE FUNCTION vendor_invoice_lines_default_position();
WITH ordered AS (
    SELECT id, row_number() OVER (PARTITION BY invoice_id ORDER BY created_at, id) - 1 AS pos
    FROM vendor_invoice_lines
)
UPDATE vendor_invoice_lines l SET position = o.pos FROM ordered o WHERE l.id = o.id AND l.position IS NULL;
ALTER TABLE vendor_invoice_lines ALTER COLUMN position SET NOT NULL;
ALTER TABLE vendor_invoice_lines ADD COLUMN IF NOT EXISTS purchase_order_line_id UUID NULL
    REFERENCES purchase_order_lines (id) ON DELETE SET NULL;
ALTER TABLE vendor_invoice_lines ADD COLUMN IF NOT EXISTS product_id UUID NULL
    REFERENCES products (id) ON DELETE SET NULL;
ALTER TABLE vendor_invoice_lines ALTER COLUMN unit_price TYPE NUMERIC(12, 4);
ALTER TABLE vendor_invoice_lines ADD COLUMN IF NOT EXISTS po_freight_charge_id UUID NULL
    REFERENCES po_freight_charges (id) ON DELETE SET NULL;

-- 3. Keyset indexes ---------------------------------------------------------

CREATE INDEX IF NOT EXISTS idx_vendor_invoices_created_id ON vendor_invoices (created_at DESC, id DESC);
CREATE INDEX IF NOT EXISTS idx_vendor_invoices_vendor_created ON vendor_invoices (vendor_id, created_at DESC, id DESC);
CREATE INDEX IF NOT EXISTS idx_vendor_invoices_po ON vendor_invoices (po_id) WHERE po_id IS NOT NULL;
