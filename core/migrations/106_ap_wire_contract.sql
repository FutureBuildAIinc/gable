-- SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
-- SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

-- 106: the AP fixes onto the wire contract (item C4-1b, ADR 0008 sections
-- 7.4 and 12, on ADR 0001). The steps are the record's C4-1b list, in order:
--
--   1  vendor_invoices: created_at NOT NULL, revision, the number (AP-), the
--      branch (from the purchase order's branch when that column exists,
--      else the default branch), the currency, a status CHECK on today's
--      values (lowercase spellings uppercased, cancel-like values and
--      negative totals become VOIDED, other unknown values become PENDING,
--      each with a notice),
--      po_id as a real foreign key (orphans are set null, with a notice),
--      UNIQUE (vendor_id, invoice_number) after duplicates are suffixed #2,
--      #3 in (created_at, id) order (with a notice), amount_open (the insert
--      trigger's rule: zero on a voided or paid bill, the total less paid
--      otherwise, with notices for short-paid PAID and overpaid PARTIAL
--      rows), and gl_entry_id linked to the newest posted, unreversed,
--      balanced VENDOR_INVOICE entry of every approved or partial bill (a one
--      leg entry, the base's approve shape, is never linked and is counted by
--      its own notice; so are the bills left without one).
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
-- anything unknown becomes PENDING, each with a notice. A hand-written
-- cancel-like value (CANCELLED, CANCELED) maps to the module's voided state,
-- never to pending: a cancelled bill owes nothing. So does a negative total
-- (the base validated no sign, so a vendor credit entered as a negative bill
-- is a plausible legacy row): it owes nothing as a bill, and the amount_open
-- range CHECK below would otherwise abort the whole migration on it.
DO $$
DECLARE lower BIGINT; unknown BIGINT; cancels BIGINT; negatives BIGINT;
BEGIN
    SELECT count(*) INTO lower FROM vendor_invoices WHERE status <> UPPER(btrim(status));
    IF lower > 0 THEN
        UPDATE vendor_invoices SET status = UPPER(btrim(status)) WHERE status <> UPPER(btrim(status));
        RAISE NOTICE 'migration 106: % vendor invoice status value(s) were not uppercase and are uppercased', lower;
    END IF;
    SELECT count(*) INTO cancels FROM vendor_invoices WHERE status IN ('CANCELLED', 'CANCELED');
    IF cancels > 0 THEN
        UPDATE vendor_invoices SET status = 'VOIDED' WHERE status IN ('CANCELLED', 'CANCELED');
        RAISE NOTICE 'migration 106: % vendor invoice(s) held a cancel-like status (CANCELLED, CANCELED) and become VOIDED owing nothing', cancels;
    END IF;
    SELECT count(*) INTO negatives FROM vendor_invoices WHERE total < 0 AND status <> 'VOIDED';
    IF negatives > 0 THEN
        UPDATE vendor_invoices SET status = 'VOIDED' WHERE total < 0 AND status <> 'VOIDED';
        RAISE NOTICE 'migration 106: % vendor invoice(s) hold a negative total (a vendor credit entered as a negative bill; re-enter it as a credit) and become VOIDED owing nothing', negatives;
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
-- #2, #3 in (created_at, id) order, the oldest keeping the bare number. A
-- vendor may already hold a genuine "X #2" beside its duplicate "X" rows, so
-- each suffixed row takes the next suffix no row of that vendor holds.
DO $$
DECLARE dups BIGINT; r RECORD; n BIGINT; candidate TEXT;
BEGIN
    SELECT count(*) - count(DISTINCT (vendor_id, invoice_number)) INTO dups FROM vendor_invoices;
    IF dups > 0 THEN
        FOR r IN (
            SELECT id, vendor_id, invoice_number,
                   row_number() OVER (PARTITION BY vendor_id, invoice_number ORDER BY created_at, id) AS rn
            FROM vendor_invoices
        ) LOOP
            IF r.rn = 1 THEN
                CONTINUE;
            END IF;
            n := r.rn;
            LOOP
                candidate := left(r.invoice_number, 64 - length(' #' || n)) || ' #' || n;
                EXIT WHEN NOT EXISTS (SELECT 1 FROM vendor_invoices vi
                                      WHERE vi.vendor_id = r.vendor_id AND vi.invoice_number = candidate AND vi.id <> r.id);
                n := n + 1;
            END LOOP;
            UPDATE vendor_invoices SET invoice_number = candidate WHERE id = r.id;
        END LOOP;
        RAISE NOTICE 'migration 106: % duplicate vendor invoice number(s) suffixed #2, #3 in (created_at, id) order, the next free suffix where a genuine one is taken', dups;
    END IF;
END $$;
DO $$ BEGIN
    ALTER TABLE vendor_invoices ADD CONSTRAINT vendor_invoices_vendor_number_key UNIQUE (vendor_id, invoice_number);
EXCEPTION WHEN duplicate_object OR duplicate_table THEN NULL;
END $$;

-- The entry the approval posts, which the void transition reverses.
ALTER TABLE vendor_invoices ADD COLUMN IF NOT EXISTS gl_entry_id UUID NULL REFERENCES gl_journal_entries (id);

-- A bill the base approved holds its entry (the removed sync posted it with
-- source VENDOR_INVOICE and source_ref_id the bill) but no pointer to it.
-- Every approved or partial bill is linked to its newest posted, unreversed,
-- balanced VENDOR_INVOICE entry (one already reversed has been undone by hand
-- and is not the void's to reverse again; one whose legs do not balance, the
-- base's one leg approve shape, would let the void mirror it into another
-- unbalanced entry, so it is never linked and its own notice names it); the
-- bills left without one are counted by a notice, and the void transition
-- refuses them until an entry is linked.
DO $$
DECLARE oneleg BIGINT; missing BIGINT;
BEGIN
    UPDATE vendor_invoices vi
    SET gl_entry_id = e.id
    FROM gl_journal_entries e
    WHERE e.source = 'VENDOR_INVOICE' AND e.source_ref_id = vi.id
      AND e.status = 'POSTED' AND e.reverses_entry_id IS NULL
      AND NOT EXISTS (SELECT 1 FROM gl_journal_entries r WHERE r.reverses_entry_id = e.id)
      AND (SELECT SUM(l.debit) FROM gl_journal_lines l WHERE l.journal_entry_id = e.id)
          = (SELECT SUM(l.credit) FROM gl_journal_lines l WHERE l.journal_entry_id = e.id)
      AND vi.status IN ('APPROVED', 'PARTIAL') AND vi.gl_entry_id IS NULL
      AND e.id = (SELECT e2.id FROM gl_journal_entries e2
                  WHERE e2.source = 'VENDOR_INVOICE' AND e2.source_ref_id = vi.id
                    AND e2.status = 'POSTED' AND e2.reverses_entry_id IS NULL
                    AND NOT EXISTS (SELECT 1 FROM gl_journal_entries r2 WHERE r2.reverses_entry_id = e2.id)
                    AND (SELECT SUM(l2.debit) FROM gl_journal_lines l2 WHERE l2.journal_entry_id = e2.id)
                        = (SELECT SUM(l2.credit) FROM gl_journal_lines l2 WHERE l2.journal_entry_id = e2.id)
                  ORDER BY e2.created_at DESC, e2.id DESC LIMIT 1);
    SELECT count(*) INTO oneleg FROM vendor_invoices vi
    WHERE vi.status IN ('APPROVED', 'PARTIAL') AND vi.gl_entry_id IS NULL
      AND EXISTS (SELECT 1 FROM gl_journal_entries e
                  WHERE e.source = 'VENDOR_INVOICE' AND e.source_ref_id = vi.id
                    AND e.status = 'POSTED' AND e.reverses_entry_id IS NULL
                    AND NOT EXISTS (SELECT 1 FROM gl_journal_entries r WHERE r.reverses_entry_id = e.id)
                    AND (SELECT SUM(l.debit) FROM gl_journal_lines l WHERE l.journal_entry_id = e.id)
                        <> (SELECT SUM(l.credit) FROM gl_journal_lines l WHERE l.journal_entry_id = e.id));
    IF oneleg > 0 THEN
        RAISE NOTICE 'migration 106: % approved or partial vendor invoice(s) hold a one leg entry from the removed sync; post the missing debit legs, then link', oneleg;
    END IF;
    SELECT count(*) INTO missing FROM vendor_invoices vi
    WHERE vi.status IN ('APPROVED', 'PARTIAL') AND vi.gl_entry_id IS NULL
      AND NOT EXISTS (SELECT 1 FROM gl_journal_entries e
                      WHERE e.source = 'VENDOR_INVOICE' AND e.source_ref_id = vi.id
                        AND e.status = 'POSTED' AND e.reverses_entry_id IS NULL
                        AND NOT EXISTS (SELECT 1 FROM gl_journal_entries r WHERE r.reverses_entry_id = e.id)
                        AND (SELECT SUM(l.debit) FROM gl_journal_lines l WHERE l.journal_entry_id = e.id)
                            <> (SELECT SUM(l.credit) FROM gl_journal_lines l WHERE l.journal_entry_id = e.id));
    IF missing > 0 THEN
        RAISE NOTICE 'migration 106: % approved or partial vendor invoice(s) have no journal entry to link; their void is refused until one is linked', missing;
    END IF;
END $$;

-- Two more legacy shapes the link cannot mend, each named by a notice (ADR
-- 0008 7.4 says nothing of reversing a cancelled bill's credit, so the
-- migration only counts them): a bill that is VOIDED here (hand cancelled, or
-- a negative total) still holding a posted, unreversed entry keeps its credit
-- in Accounts Payable, and a bill posted twice (the base read its status
-- outside the transaction, so a double click could post it twice) links only
-- its newest entry, leaving the other's credit standing after a void.
DO $$
DECLARE stranded BIGINT; doubled BIGINT;
BEGIN
    SELECT count(*) INTO stranded FROM vendor_invoices vi
    WHERE vi.status = 'VOIDED'
      AND EXISTS (SELECT 1 FROM gl_journal_entries e
                  WHERE e.source = 'VENDOR_INVOICE' AND e.source_ref_id = vi.id
                    AND e.status = 'POSTED' AND e.reverses_entry_id IS NULL
                    AND NOT EXISTS (SELECT 1 FROM gl_journal_entries r WHERE r.reverses_entry_id = e.id));
    IF stranded > 0 THEN
        RAISE NOTICE 'migration 106: % voided or cancelled vendor invoice(s) still hold a posted, unreversed journal entry; their credit stays in Accounts Payable until it is reversed by hand', stranded;
    END IF;
    SELECT count(*) INTO doubled FROM vendor_invoices vi
    WHERE (SELECT count(*) FROM gl_journal_entries e
           WHERE e.source = 'VENDOR_INVOICE' AND e.source_ref_id = vi.id
             AND e.status = 'POSTED' AND e.reverses_entry_id IS NULL
             AND NOT EXISTS (SELECT 1 FROM gl_journal_entries r WHERE r.reverses_entry_id = e.id)) > 1;
    IF doubled > 0 THEN
        RAISE NOTICE 'migration 106: % vendor invoice(s) were posted more than once (more than one posted, unreversed journal entry); only the newest is linked, reverse the others by hand', doubled;
    END IF;
END $$;

-- amount_open: what the vendor is still owed, by the insert trigger's rule:
-- zero on a voided or paid bill, the total less what was paid (clamped
-- between zero and the total) otherwise. A short-paid PAID bill, an overpaid
-- APPROVED or PARTIAL one and a negative amount paid (a hand edit: the base's
-- own pay never wrote one on a positive bill) are each named by a notice, so
-- an operator can mend the row. The range CHECK says the rule exactly: a
-- voided or paid bill owes zero at any total (a negative one included, the
-- base accepted any sign); any other owes between zero and its total.
DO $$
DECLARE shortPaid BIGINT; overpaid BIGINT; negPaid BIGINT;
BEGIN
    SELECT count(*) INTO shortPaid FROM vendor_invoices WHERE status = 'PAID' AND amount_paid < total;
    IF shortPaid > 0 THEN
        RAISE NOTICE 'migration 106: % PAID vendor invoice(s) were paid less than their total and owe nothing', shortPaid;
    END IF;
    SELECT count(*) INTO overpaid FROM vendor_invoices WHERE status IN ('APPROVED', 'PARTIAL') AND amount_paid > total;
    IF overpaid > 0 THEN
        RAISE NOTICE 'migration 106: % APPROVED or PARTIAL vendor invoice(s) were paid past their total and owe nothing', overpaid;
    END IF;
    SELECT count(*) INTO negPaid FROM vendor_invoices WHERE amount_paid < 0;
    IF negPaid > 0 THEN
        RAISE NOTICE 'migration 106: % vendor invoice(s) carry a negative amount paid; each owes at most its total', negPaid;
    END IF;
END $$;
ALTER TABLE vendor_invoices ADD COLUMN IF NOT EXISTS amount_open NUMERIC(12, 2) NULL;
UPDATE vendor_invoices SET amount_open = CASE WHEN status IN ('VOIDED', 'PAID') THEN 0
    ELSE LEAST(total, GREATEST(0, total - amount_paid)) END
WHERE amount_open IS NULL;
ALTER TABLE vendor_invoices ALTER COLUMN amount_open SET NOT NULL;
ALTER TABLE vendor_invoices DROP CONSTRAINT IF EXISTS vendor_invoices_amount_open_range;
ALTER TABLE vendor_invoices ADD CONSTRAINT vendor_invoices_amount_open_range
    CHECK (amount_open >= 0 AND CASE WHEN status IN ('VOIDED', 'PAID') THEN amount_open = 0 ELSE amount_open <= total END);

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
