-- SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
-- SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

-- 116: purchase order document numbers (PO-NUMBER). Every other sales
-- document carries one (Q-, SO-, IN-, CM-, PAY-); a purchase order showed the
-- first eight characters of its id. This is the order migration's number
-- step (092 section 2) for purchase_orders: po_number_seq,
-- purchase_order_next_number() (PO- and the number padded to six digits),
-- purchase_orders.number backfilled in (created_at, id) order, the sequence
-- set past the highest, the default, NOT NULL and UNIQUE. Idempotent.

CREATE SEQUENCE IF NOT EXISTS po_number_seq;

CREATE OR REPLACE FUNCTION purchase_order_next_number() RETURNS TEXT
LANGUAGE sql AS $$
    SELECT 'PO-' || lpad(s.n::text, GREATEST(6, length(s.n::text)), '0')
    FROM (SELECT nextval('po_number_seq') AS n) s
$$;

ALTER TABLE purchase_orders ADD COLUMN IF NOT EXISTS number TEXT;

WITH ordered AS (
    SELECT id, row_number() OVER (ORDER BY created_at, id) AS n
    FROM purchase_orders
    WHERE number IS NULL
)
UPDATE purchase_orders p
SET number = 'PO-' || lpad(r.n::text, GREATEST(6, length(r.n::text)), '0')
FROM ordered r
WHERE p.id = r.id;

SELECT setval('po_number_seq',
              COALESCE((SELECT MAX(substring(number FROM 4)::bigint) FROM purchase_orders), 0) + 1,
              false);

ALTER TABLE purchase_orders ALTER COLUMN number SET DEFAULT purchase_order_next_number();
ALTER TABLE purchase_orders ALTER COLUMN number SET NOT NULL;
DO $$ BEGIN
    ALTER TABLE purchase_orders ADD CONSTRAINT purchase_orders_number_key UNIQUE (number);
EXCEPTION WHEN duplicate_object OR duplicate_table THEN NULL;
END $$;
