-- SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
-- SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

-- 090: quotes onto the wire contract (item R1-15, docs/adr/0001-wire-contract.md).
--
-- The migration note's steps 2b, 3 and 3a for the quote module, in one file:
--
--   created_at   filled and set NOT NULL first: the list's keyset ordering
--                (created_at, id) and the number backfill both read it, and
--                a nullable ordering column has no cursor position (ADR 0001
--                section 2).
--   revision     BIGINT NOT NULL DEFAULT 1: every existing quote starts at
--                revision 1, so the If-Match precondition has a value to
--                check from the first write (section 11).
--   number       the human readable document number Q-000123, minted from
--                quote_number_seq. The Go create path mints it through the
--                caller's transaction (httpx.NextDocumentNumber); the column
--                DEFAULT is the same mint for the raw SQL writers that
--                insert quotes without going through that path (the portal's
--                quote request, the demo seed), so no writer can leave a
--                quote unnumbered. Existing rows are numbered in
--                (created_at, id) order, then the unique constraint is
--                added and the sequence moves past the maximum.
--   quote_lines  price_uom, uom_qty and price_uom_qty: the line's conversion
--                pair (section 7a). price_uom is TEXT, not uom_type: prices
--                are quoted per units the sale enum does not carry (per M,
--                per CWT). A NULL price_uom reads as the line's own uom, so
--                the raw SQL writers need no change. position keeps a line's
--                place: every line of one write shares a created_at, so
--                created_at alone never ordered them.
--
-- Step 2b needs no column change here: quote_lines.unit_price is NUMERIC(12,4)
-- already, the scale _ten_thousandths carries.

-- 1. created_at: fill, then NOT NULL.
UPDATE quotes SET created_at = COALESCE(updated_at, NOW()) WHERE created_at IS NULL;
ALTER TABLE quotes ALTER COLUMN created_at SET NOT NULL;

-- 2. revision.
ALTER TABLE quotes ADD COLUMN IF NOT EXISTS revision BIGINT NOT NULL DEFAULT 1;

-- 3. Document numbers.
CREATE SEQUENCE IF NOT EXISTS quote_number_seq;

CREATE OR REPLACE FUNCTION quote_next_number() RETURNS TEXT
LANGUAGE sql AS $$
    SELECT 'Q-' || lpad(s.n::text, GREATEST(6, length(s.n::text)), '0')
    FROM (SELECT nextval('quote_number_seq') AS n) s
$$;

ALTER TABLE quotes ADD COLUMN IF NOT EXISTS number TEXT;

WITH ordered AS (
    SELECT id, row_number() OVER (ORDER BY created_at, id) AS n
    FROM quotes
    WHERE number IS NULL
)
UPDATE quotes q
SET number = 'Q-' || lpad(o.n::text, GREATEST(6, length(o.n::text)), '0')
FROM ordered o
WHERE q.id = o.id;

-- Past the maximum, so the next mint is max + 1 (1 on an empty table).
SELECT setval('quote_number_seq',
              COALESCE((SELECT MAX(substring(number FROM 3)::bigint) FROM quotes), 0) + 1,
              false);

ALTER TABLE quotes ALTER COLUMN number SET DEFAULT quote_next_number();
ALTER TABLE quotes ALTER COLUMN number SET NOT NULL;
ALTER TABLE quotes ADD CONSTRAINT quotes_number_key UNIQUE (number);

-- The list's keyset ordering.
CREATE INDEX IF NOT EXISTS idx_quotes_created_id ON quotes (created_at DESC, id DESC);

-- 4. Line conversion pair and position.
ALTER TABLE quote_lines ADD COLUMN IF NOT EXISTS price_uom TEXT;
ALTER TABLE quote_lines ADD COLUMN IF NOT EXISTS uom_qty NUMERIC(12, 4) NOT NULL DEFAULT 1;
ALTER TABLE quote_lines ADD COLUMN IF NOT EXISTS price_uom_qty NUMERIC(12, 4) NOT NULL DEFAULT 1;
ALTER TABLE quote_lines ADD COLUMN IF NOT EXISTS position INTEGER NOT NULL DEFAULT 0;

UPDATE quote_lines SET price_uom = uom::text WHERE price_uom IS NULL;

ALTER TABLE quote_lines
    ADD CONSTRAINT quote_lines_conversion_positive CHECK (uom_qty > 0 AND price_uom_qty > 0);
