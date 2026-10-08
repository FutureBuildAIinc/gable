-- SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
-- SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

-- 097: invoices and credit memos onto the wire contract (item C2-3,
-- docs/adr/0001-wire-contract.md and docs/adr/0005-sales-and-money-core.md
-- sections 4, 6 and 13). The steps are the ADR's C2-3 list, in order:
--
--   1  created_at / updated_at filled and set NOT NULL on invoices and credit
--      memos (the keyset ordering and the number backfill read them); revision.
--   2  document_counters with the series invoice and credit_memo, and the two
--      numbering functions (the columns' DEFAULTs, so a raw SQL writer such as
--      the seed numbers through the same counter, and a rolled back insert
--      rolls its increment back with it).
--   3  invoices.number: IN-, backfilled in (created_at, id) order, the counter
--      set past the maximum, DEFAULT, NOT NULL, UNIQUE.
--   4  OVERDUE stops being a stored status (UNPAID or PARTIAL from the payments
--      recorded against the row); the new status CHECK; due_date to DATE;
--      payment_terms_id from the legacy text with the discount snapshot; the
--      text columns invoices.payment_terms and customers.payment_terms dropped;
--      the void columns (voided_on included).
--   5  credit_memos: the columns of 6.3, with the backfill (PENDING to DRAFT;
--      APPLIED stays APPLIED when it names an invoice, else OPEN; total =
--      -amount; one ADJUST charge line each; numbers through the counter for
--      every memo not in DRAFT).
--   6  credit_memo_lines; keyset indexes on both tables.
--
-- Beside the ADR's list: invoice_lines gains priced_unit_price,
-- override_reason and price_adjusted_by (the price audit trail the order line
-- carries, copied at billing and backfilled through order_line_id), so an
-- invoice line reads in the one shared line shape.

-- 1. created_at, updated_at, revision.
UPDATE invoices SET created_at = COALESCE(updated_at, NOW()) WHERE created_at IS NULL;
UPDATE invoices SET updated_at = created_at WHERE updated_at IS NULL;
ALTER TABLE invoices ALTER COLUMN created_at SET NOT NULL;
ALTER TABLE invoices ALTER COLUMN updated_at SET NOT NULL;
ALTER TABLE invoices ADD COLUMN IF NOT EXISTS revision BIGINT NOT NULL DEFAULT 1;

UPDATE credit_memos SET created_at = NOW() WHERE created_at IS NULL;
ALTER TABLE credit_memos ALTER COLUMN created_at SET NOT NULL;
ALTER TABLE credit_memos ADD COLUMN IF NOT EXISTS updated_at TIMESTAMPTZ;
UPDATE credit_memos SET updated_at = COALESCE(applied_at, created_at) WHERE updated_at IS NULL;
ALTER TABLE credit_memos ALTER COLUMN updated_at SET NOT NULL;
ALTER TABLE credit_memos ALTER COLUMN updated_at SET DEFAULT NOW();
ALTER TABLE credit_memos ADD COLUMN IF NOT EXISTS revision BIGINT NOT NULL DEFAULT 1;

-- 2. The gapless counters and their numbering functions.
CREATE TABLE IF NOT EXISTS document_counters (
    series     TEXT PRIMARY KEY,
    next_value BIGINT NOT NULL CHECK (next_value >= 1)
);
INSERT INTO document_counters (series, next_value) VALUES ('invoice', 1), ('credit_memo', 1)
ON CONFLICT (series) DO NOTHING;

CREATE OR REPLACE FUNCTION invoice_next_number() RETURNS TEXT
LANGUAGE sql AS $$
    UPDATE document_counters SET next_value = next_value + 1 WHERE series = 'invoice'
    RETURNING 'IN-' || lpad((next_value - 1)::text, GREATEST(6, length((next_value - 1)::text)), '0')
$$;

CREATE OR REPLACE FUNCTION credit_memo_next_number() RETURNS TEXT
LANGUAGE sql AS $$
    UPDATE document_counters SET next_value = next_value + 1 WHERE series = 'credit_memo'
    RETURNING 'CM-' || lpad((next_value - 1)::text, GREATEST(6, length((next_value - 1)::text)), '0')
$$;

-- 3. invoices.number.
ALTER TABLE invoices ADD COLUMN IF NOT EXISTS number TEXT;

WITH ordered AS (
    SELECT id, row_number() OVER (ORDER BY created_at, id) AS n
    FROM invoices
    WHERE number IS NULL
)
UPDATE invoices i
SET number = 'IN-' || lpad(o.n::text, GREATEST(6, length(o.n::text)), '0')
FROM ordered o
WHERE i.id = o.id;

UPDATE document_counters
SET next_value = GREATEST(next_value, COALESCE((SELECT MAX(substring(number FROM 4)::bigint) FROM invoices), 0) + 1)
WHERE series = 'invoice';

ALTER TABLE invoices ALTER COLUMN number SET DEFAULT invoice_next_number();
ALTER TABLE invoices ALTER COLUMN number SET NOT NULL;
ALTER TABLE invoices ADD CONSTRAINT invoices_number_key UNIQUE (number);

-- 4. Status, due date, terms, void columns.
UPDATE invoices i
SET status = CASE WHEN COALESCE((SELECT SUM(p.amount) FROM payments p WHERE p.invoice_id = i.id), 0) > 0
                  THEN 'PARTIAL' ELSE 'UNPAID' END
WHERE i.status = 'OVERDUE';
ALTER TABLE invoices DROP CONSTRAINT IF EXISTS invoices_status_check;
ALTER TABLE invoices ADD CONSTRAINT invoices_status_check
    CHECK (status IN ('UNPAID', 'PARTIAL', 'PAID', 'VOID', 'WRITTEN_OFF'));

-- due_date to DATE: the business date in the branch's time zone. A column type
-- change cannot read another table, so the date goes through a new column.
ALTER TABLE invoices ADD COLUMN IF NOT EXISTS due_date_new DATE;
UPDATE invoices i
SET due_date_new = (i.due_date AT TIME ZONE COALESCE((SELECT l.timezone FROM locations l WHERE l.id = i.branch_id), 'UTC'))::date
WHERE i.due_date IS NOT NULL AND i.due_date_new IS NULL;
ALTER TABLE invoices DROP COLUMN due_date;
ALTER TABLE invoices RENAME COLUMN due_date_new TO due_date;

-- Terms by id. The legacy text maps exactly as 091 mapped it (same keying, same
-- own-term naming); terms 091 made already exist and a text it never saw is
-- made here, so no invoice loses its terms.
DROP TABLE IF EXISTS legacy_terms_map;
CREATE TEMP TABLE legacy_terms_map AS
WITH legacy AS (
    SELECT DISTINCT btrim(t) AS original
    FROM (SELECT payment_terms::text AS t FROM customers
          UNION ALL SELECT payment_terms::text FROM invoices) s
    WHERE btrim(t) <> ''
), keyed AS (
    SELECT original, upper(regexp_replace(original, '[\s_.\-]+', '', 'g')) AS key FROM legacy
), classified AS (
    SELECT original, key,
        CASE
            WHEN key ~ '^NET[0-9]{1,4}(DAYS?)?$' AND substring(key from '^NET([0-9]+)')::int <= 3650
                THEN 'NET' || (substring(key from '^NET([0-9]+)')::int)::text
            WHEN key ~ '^N[0-9]{1,4}$' AND substring(key from 2)::int <= 3650
                THEN 'NET' || (substring(key from 2)::int)::text
            WHEN key IN ('COD', 'CASHONDELIVERY') THEN 'COD'
            WHEN key IN ('DUEONRECEIPT', 'DUEUPONRECEIPT', 'UPONRECEIPT', 'ONRECEIPT') THEN 'DUE_ON_RECEIPT'
        END AS matched,
        COALESCE(NULLIF(left(trim(both '-' from regexp_replace(upper(original), '[^A-Z0-9_-]+', '-', 'g')), 36), ''), 'LEGACY') AS norm
    FROM keyed
), ranked AS (
    SELECT *, row_number() OVER (PARTITION BY norm ORDER BY original COLLATE "C") AS rn
    FROM classified WHERE matched IS NULL
)
SELECT c.original, COALESCE(c.matched, CASE WHEN r.rn = 1 THEN r.norm ELSE r.norm || '-' || r.rn END) AS code,
       (c.matched IS NULL) AS own
FROM classified c LEFT JOIN ranked r ON r.original = c.original;

INSERT INTO payment_terms (code, name, kind, net_days)
SELECT DISTINCT code, 'Net ' || substring(code from 4), 'NET_DAYS', substring(code from 4)::int
FROM legacy_terms_map WHERE NOT own AND code ~ '^NET[0-9]+$'
ON CONFLICT (code) DO NOTHING;

INSERT INTO payment_terms (code, name, kind, net_days)
SELECT code, original, 'NET_DAYS', 30 FROM legacy_terms_map WHERE own
ON CONFLICT (code) DO NOTHING;

ALTER TABLE invoices ADD COLUMN IF NOT EXISTS payment_terms_id UUID NULL REFERENCES payment_terms (id);
UPDATE invoices i
SET payment_terms_id = COALESCE(
    (SELECT pt.id FROM legacy_terms_map m JOIN payment_terms pt ON pt.code = m.code
     WHERE m.original = btrim(i.payment_terms::text)),
    (SELECT c.payment_terms_id FROM customers c WHERE c.id = i.customer_id),
    payment_terms_default_id())
WHERE i.payment_terms_id IS NULL;
ALTER TABLE invoices ALTER COLUMN payment_terms_id SET NOT NULL;
ALTER TABLE invoices ALTER COLUMN payment_terms_id SET DEFAULT payment_terms_default_id();

ALTER TABLE invoices ADD COLUMN IF NOT EXISTS discount_due_date DATE NULL;
ALTER TABLE invoices ADD COLUMN IF NOT EXISTS discount_percent NUMERIC(7, 4) NULL;
UPDATE invoices i
SET discount_percent = pt.discount_percent,
    discount_due_date = i.invoice_date + pt.discount_days
FROM payment_terms pt
WHERE pt.id = i.payment_terms_id AND pt.discount_percent IS NOT NULL AND pt.discount_days IS NOT NULL
  AND i.discount_percent IS NULL;

DROP TABLE legacy_terms_map;
ALTER TABLE invoices DROP COLUMN IF EXISTS payment_terms;
ALTER TABLE customers DROP COLUMN IF EXISTS payment_terms;

ALTER TABLE invoices ADD COLUMN IF NOT EXISTS voided_at TIMESTAMPTZ NULL;
ALTER TABLE invoices ADD COLUMN IF NOT EXISTS voided_by TEXT NULL;
ALTER TABLE invoices ADD COLUMN IF NOT EXISTS void_reason TEXT NULL;
ALTER TABLE invoices ADD COLUMN IF NOT EXISTS voided_on DATE NULL;
UPDATE invoices SET voided_at = COALESCE(updated_at, created_at), voided_on = invoice_date
WHERE status = 'VOID' AND voided_at IS NULL;
ALTER TABLE invoices ADD CONSTRAINT invoices_void_columns
    CHECK ((status = 'VOID') = (voided_at IS NOT NULL));

-- The invoice line's price audit trail, as an order line carries it.
ALTER TABLE invoice_lines ADD COLUMN IF NOT EXISTS priced_unit_price NUMERIC(12, 4) NULL;
ALTER TABLE invoice_lines ADD COLUMN IF NOT EXISTS override_reason TEXT NULL;
ALTER TABLE invoice_lines ADD COLUMN IF NOT EXISTS price_adjusted_by TEXT NULL;
UPDATE invoice_lines il
SET priced_unit_price = ol.priced_unit_price, override_reason = ol.override_reason,
    price_adjusted_by = ol.price_adjusted_by
FROM order_lines ol
WHERE il.order_line_id = ol.id AND il.priced_unit_price IS NULL;

-- 5. Credit memos.
ALTER TABLE credit_memos ALTER COLUMN status DROP DEFAULT;
ALTER TABLE credit_memos DROP CONSTRAINT IF EXISTS credit_memos_status_check;

ALTER TABLE credit_memos ADD COLUMN IF NOT EXISTS number TEXT NULL;
ALTER TABLE credit_memos ADD COLUMN IF NOT EXISTS currency CHAR(3) NULL;
ALTER TABLE credit_memos ADD COLUMN IF NOT EXISTS branch_id UUID NULL REFERENCES locations (id);
ALTER TABLE credit_memos ADD COLUMN IF NOT EXISTS project_id UUID NULL REFERENCES projects (id);
ALTER TABLE credit_memos ADD COLUMN IF NOT EXISTS ship_to_id UUID NULL REFERENCES customer_ship_tos (id);
ALTER TABLE credit_memos ADD COLUMN IF NOT EXISTS pos_return_id UUID NULL REFERENCES pos_returns (id);
ALTER TABLE credit_memos ADD COLUMN IF NOT EXISTS reason_code TEXT NULL;
ALTER TABLE credit_memos ADD COLUMN IF NOT EXISTS subtotal NUMERIC(12, 2) NULL;
ALTER TABLE credit_memos ADD COLUMN IF NOT EXISTS tax_amount NUMERIC(12, 2) NULL;
ALTER TABLE credit_memos ADD COLUMN IF NOT EXISTS total_amount NUMERIC(12, 2) NULL;
ALTER TABLE credit_memos ADD COLUMN IF NOT EXISTS tax_rate NUMERIC(9, 6) NULL;
ALTER TABLE credit_memos ADD COLUMN IF NOT EXISTS gl_entry_id UUID NULL REFERENCES gl_journal_entries (id);
ALTER TABLE credit_memos ADD COLUMN IF NOT EXISTS memo_date DATE NULL;
ALTER TABLE credit_memos ADD COLUMN IF NOT EXISTS voided_at TIMESTAMPTZ NULL;
ALTER TABLE credit_memos ADD COLUMN IF NOT EXISTS voided_by TEXT NULL;
ALTER TABLE credit_memos ADD COLUMN IF NOT EXISTS void_reason TEXT NULL;
ALTER TABLE credit_memos ADD COLUMN IF NOT EXISTS voided_on DATE NULL;

-- PENDING to DRAFT; APPLIED stays APPLIED when it names an invoice (C2-4
-- writes its application) and becomes OPEN when not (the old apply already
-- lowered the subledger, so an open credit keeps documents and subledger
-- agreeing); VOID stays.
UPDATE credit_memos SET status = CASE
        WHEN status = 'PENDING' THEN 'DRAFT'
        WHEN status = 'APPLIED' AND invoice_id IS NULL THEN 'OPEN'
        ELSE status END
WHERE status IN ('PENDING', 'APPLIED');

UPDATE credit_memos m
SET branch_id = COALESCE((SELECT i.branch_id FROM invoices i WHERE i.id = m.invoice_id),
                         (SELECT c.primary_branch_id FROM customers c WHERE c.id = m.customer_id))
WHERE m.branch_id IS NULL;
UPDATE credit_memos m
SET currency = COALESCE((SELECT i.currency FROM invoices i WHERE i.id = m.invoice_id),
                        (SELECT c.currency FROM customers c WHERE c.id = m.customer_id),
                        (SELECT s.value FROM system_settings s WHERE s.key = 'currency.default'),
                        'USD')
WHERE m.currency IS NULL;
UPDATE credit_memos SET reason_code = 'OTHER' WHERE reason_code IS NULL;
UPDATE credit_memos SET subtotal = -amount, tax_amount = 0, total_amount = -amount WHERE total_amount IS NULL;
UPDATE credit_memos m
SET memo_date = (m.created_at AT TIME ZONE COALESCE((SELECT l.timezone FROM locations l WHERE l.id = m.branch_id), 'UTC'))::date
WHERE m.memo_date IS NULL;
UPDATE credit_memos SET voided_at = COALESCE(applied_at, created_at), voided_on = memo_date
WHERE status = 'VOID' AND voided_at IS NULL;

-- Numbers through the counter for every memo not in DRAFT, in (created_at, id) order.
WITH ordered AS (
    SELECT id, row_number() OVER (ORDER BY created_at, id) AS n
    FROM credit_memos
    WHERE number IS NULL AND status <> 'DRAFT'
)
UPDATE credit_memos m
SET number = 'CM-' || lpad(o.n::text, GREATEST(6, length(o.n::text)), '0')
FROM ordered o
WHERE m.id = o.id;
UPDATE document_counters
SET next_value = GREATEST(next_value, COALESCE((SELECT MAX(substring(number FROM 4)::bigint) FROM credit_memos), 0) + 1)
WHERE series = 'credit_memo';

ALTER TABLE credit_memos ALTER COLUMN currency SET NOT NULL;
ALTER TABLE credit_memos ALTER COLUMN branch_id SET NOT NULL;
ALTER TABLE credit_memos ALTER COLUMN reason_code SET NOT NULL;
ALTER TABLE credit_memos ALTER COLUMN subtotal SET NOT NULL;
ALTER TABLE credit_memos ALTER COLUMN tax_amount SET NOT NULL;
ALTER TABLE credit_memos ALTER COLUMN total_amount SET NOT NULL;
ALTER TABLE credit_memos ALTER COLUMN memo_date SET NOT NULL;
ALTER TABLE credit_memos ALTER COLUMN status SET DEFAULT 'DRAFT';
ALTER TABLE credit_memos ALTER COLUMN amount TYPE NUMERIC(12, 2);

ALTER TABLE credit_memos ADD CONSTRAINT credit_memos_status_check
    CHECK (status IN ('DRAFT', 'OPEN', 'PARTIAL', 'APPLIED', 'VOID'));
ALTER TABLE credit_memos ADD CONSTRAINT credit_memos_currency_format CHECK (currency ~ '^[A-Z]{3}$');
ALTER TABLE credit_memos ADD CONSTRAINT credit_memos_reason_code_check
    CHECK (reason_code IN ('RETURN', 'PRICE_ADJUSTMENT', 'DAMAGE', 'OTHER'));
ALTER TABLE credit_memos ADD CONSTRAINT credit_memos_amounts_nonpositive
    CHECK (subtotal <= 0 AND tax_amount <= 0 AND total_amount <= 0);
ALTER TABLE credit_memos ADD CONSTRAINT credit_memos_number_key UNIQUE (number);
-- a draft has no number (it is minted at post); a voided draft never had one
ALTER TABLE credit_memos ADD CONSTRAINT credit_memos_number_when_posted
    CHECK (status = 'VOID' OR ((status = 'DRAFT') = (number IS NULL)));
ALTER TABLE credit_memos ADD CONSTRAINT credit_memos_void_columns
    CHECK ((status = 'VOID') = (voided_at IS NOT NULL));

-- 6. credit_memo_lines: the line columns of ADR 0005 2.2, negative quantities and extensions.
CREATE TABLE IF NOT EXISTS credit_memo_lines (
    id                   UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    credit_memo_id       UUID NOT NULL REFERENCES credit_memos (id) ON DELETE CASCADE,
    position             INTEGER NOT NULL DEFAULT 0,
    line_type            TEXT NOT NULL DEFAULT 'PRODUCT',
    parent_line_id       UUID NULL REFERENCES credit_memo_lines (id) ON DELETE CASCADE,
    product_id           UUID NULL REFERENCES products (id) ON DELETE RESTRICT,
    charge_code_id       UUID NULL REFERENCES charge_codes (id),
    invoice_line_id      UUID NULL REFERENCES invoice_lines (id) ON DELETE SET NULL,
    sku                  TEXT NULL,
    description          TEXT NOT NULL,
    quantity             NUMERIC(12, 4) NULL,
    uom                  TEXT NULL,
    price_uom            TEXT NULL,
    uom_qty              NUMERIC(12, 4) NULL,
    price_uom_qty        NUMERIC(12, 4) NULL,
    unit_price           NUMERIC(12, 4) NULL,
    priced_unit_price    NUMERIC(12, 4) NULL,
    price_source         TEXT NOT NULL DEFAULT 'PRICE_LIST',
    override_reason      TEXT NULL,
    discount_percent     NUMERIC(7, 4) NULL,
    discount_amount      NUMERIC(12, 2) NULL,
    discount_reason      TEXT NULL,
    price_adjusted_by    TEXT NULL,
    line_total           NUMERIC(12, 2) NULL,
    taxable              BOOLEAN NOT NULL DEFAULT TRUE,
    revenue_account_code TEXT NULL,
    restock              BOOLEAN NOT NULL DEFAULT FALSE,
    unit_cost            NUMERIC(12, 4) NULL,
    cost                 NUMERIC(12, 2) NOT NULL DEFAULT 0,
    created_at           TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    CONSTRAINT credit_memo_lines_line_type_check CHECK (line_type IN ('PRODUCT', 'KIT', 'COMPONENT', 'CHARGE', 'TEXT')),
    CONSTRAINT credit_memo_lines_price_source_check CHECK (price_source IN ('PRICE_LIST', 'QUOTE', 'OVERRIDE', 'MANUAL', 'NONE')),
    CONSTRAINT credit_memo_lines_pair_positive CHECK (
        (uom_qty IS NULL AND price_uom_qty IS NULL) OR (uom_qty > 0 AND price_uom_qty > 0)),
    CONSTRAINT credit_memo_lines_parent_only_on_component CHECK ((line_type = 'COMPONENT') = (parent_line_id IS NOT NULL)),
    CONSTRAINT credit_memo_lines_restock_stocked CHECK (
        NOT restock OR (line_type IN ('PRODUCT', 'COMPONENT') AND product_id IS NOT NULL)),
    CONSTRAINT credit_memo_lines_shape CHECK (
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
    )
);
CREATE INDEX IF NOT EXISTS idx_credit_memo_lines_memo ON credit_memo_lines (credit_memo_id, position);
CREATE INDEX IF NOT EXISTS idx_credit_memo_lines_invoice_line ON credit_memo_lines (invoice_line_id)
    WHERE invoice_line_id IS NOT NULL;

-- One ADJUST charge line per migrated memo (quantity -1 EA, unit price the amount, untaxed).
INSERT INTO credit_memo_lines (credit_memo_id, position, line_type, charge_code_id, description, quantity, uom, price_uom,
                               uom_qty, price_uom_qty, unit_price, price_source, line_total, taxable, revenue_account_code)
SELECT m.id, 0, 'CHARGE', cc.id, LEFT(m.reason, 500), -1, 'EA', 'EA', 1, 1, m.amount, 'MANUAL', -m.amount, FALSE,
       cc.revenue_account_code
FROM credit_memos m
CROSS JOIN LATERAL (SELECT id, revenue_account_code FROM charge_codes WHERE code = 'ADJUST') cc
WHERE NOT EXISTS (SELECT 1 FROM credit_memo_lines l WHERE l.credit_memo_id = m.id);

CREATE INDEX IF NOT EXISTS idx_invoices_created_id ON invoices (created_at DESC, id DESC);
CREATE INDEX IF NOT EXISTS idx_credit_memos_created_id ON credit_memos (created_at DESC, id DESC);
CREATE INDEX IF NOT EXISTS idx_invoices_project ON invoices (project_id) WHERE project_id IS NOT NULL;
CREATE INDEX IF NOT EXISTS idx_invoices_ship_to ON invoices (ship_to_id) WHERE ship_to_id IS NOT NULL;
CREATE INDEX IF NOT EXISTS idx_credit_memos_project ON credit_memos (project_id) WHERE project_id IS NOT NULL;
