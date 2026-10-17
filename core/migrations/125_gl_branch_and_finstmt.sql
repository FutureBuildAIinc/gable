-- SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
-- SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

-- 125: the branch dimension on the ledger, and the columns the GL contract
-- and period administration need. Every journal entry and every line carries
-- the branch it belongs to (a line may name another branch than its entry:
-- an inter-branch leg), so every statement can be cut by branch or
-- consolidated. Accounts and fiscal periods gain a revision for If-Match,
-- and a reopened period records the reason it was reopened.

-- 1. branch_id on entries, backfilled from each entry's source document.
ALTER TABLE gl_journal_entries ADD COLUMN IF NOT EXISTS branch_id UUID NULL REFERENCES locations(id);

UPDATE gl_journal_entries e
SET branch_id = d.branch
FROM (
    SELECT e.id,
           COALESCE(
               i.branch_id,                                   -- INVOICE
               m.branch_id,                                   -- CREDIT_MEMO
               p.branch_id,                                   -- PAYMENT
               ai.branch_id,                                  -- PAYMENT applied / WRITE_OFF (via the invoice)
               r.branch_id,                                   -- RETURN
               dep.branch_id,                                 -- DEPOSIT
               ts.branch_id,                                  -- ADJUSTMENT (till over/short)
               vi.branch_id,                                  -- VENDOR_INVOICE
               vpi.branch_id,                                 -- VENDOR_PAYMENT (via the bills it pays)
               (SELECT value::uuid FROM system_settings WHERE key = 'default_branch_id')
           ) AS branch
    FROM gl_journal_entries e
    LEFT JOIN invoices i         ON e.source = 'INVOICE'        AND i.id = e.source_ref_id
    LEFT JOIN credit_memos m     ON e.source = 'CREDIT_MEMO'    AND m.id = e.source_ref_id
    LEFT JOIN payments p         ON e.source = 'PAYMENT'        AND p.id = e.source_ref_id
    LEFT JOIN ar_applications aar ON e.source IN ('PAYMENT', 'WRITE_OFF') AND aar.id = e.source_ref_id
    LEFT JOIN invoices ai ON ai.id = aar.invoice_id
    LEFT JOIN pos_returns r      ON e.source = 'RETURN'         AND r.id = e.source_ref_id
    LEFT JOIN customer_deposits_legacy dep ON e.source = 'DEPOSIT' AND dep.id = e.source_ref_id
    LEFT JOIN till_sessions ts   ON e.source = 'ADJUSTMENT'     AND ts.id = e.source_ref_id
    LEFT JOIN vendor_invoices vi ON e.source = 'VENDOR_INVOICE' AND vi.id = e.source_ref_id
    LEFT JOIN LATERAL (
        SELECT vi2.branch_id
        FROM ap_payments ap
        JOIN ap_payment_applications app ON app.payment_id = ap.id
        JOIN vendor_invoices vi2 ON vi2.id = app.invoice_id
        WHERE e.source = 'VENDOR_PAYMENT' AND ap.id = e.source_ref_id
        ORDER BY vi2.id
        LIMIT 1
    ) vpi ON TRUE
) d
WHERE e.id = d.id AND e.branch_id IS NULL;

-- A reversal belongs to the branch of the entry it reverses (the default the
-- source pass leaves it is not its branch).
UPDATE gl_journal_entries e
SET branch_id = orig.branch_id
FROM gl_journal_entries orig
WHERE e.reverses_entry_id = orig.id;

-- Whatever no source named lands on the default branch, so the column can go
-- NOT NULL: an entry with no derivable branch is the default branch's.
UPDATE gl_journal_entries e
SET branch_id = (SELECT value::uuid FROM system_settings WHERE key = 'default_branch_id')
WHERE e.branch_id IS NULL;

ALTER TABLE gl_journal_entries ALTER COLUMN branch_id SET NOT NULL;

-- 2. branch_id on lines: each line its entry's branch (a posting may name
-- another branch for one line; that is set per line at post time).
ALTER TABLE gl_journal_lines ADD COLUMN IF NOT EXISTS branch_id UUID NULL REFERENCES locations(id);

UPDATE gl_journal_lines l
SET branch_id = e.branch_id
FROM gl_journal_entries e
WHERE e.id = l.journal_entry_id AND l.branch_id IS NULL;

ALTER TABLE gl_journal_lines ALTER COLUMN branch_id SET NOT NULL;

-- 3. Indexes the statement reads and the drill down need.
CREATE INDEX IF NOT EXISTS idx_gl_entries_branch_date ON gl_journal_entries(branch_id, entry_date);
CREATE INDEX IF NOT EXISTS idx_gl_lines_account_branch ON gl_journal_lines(account_id, branch_id);

-- 4. Revision for If-Match on the two mutable GL masters.
ALTER TABLE gl_accounts ADD COLUMN IF NOT EXISTS revision INTEGER NOT NULL DEFAULT 1;
ALTER TABLE gl_fiscal_periods ADD COLUMN IF NOT EXISTS revision INTEGER NOT NULL DEFAULT 1;

-- 5. A reopened period records why.
ALTER TABLE gl_fiscal_periods ADD COLUMN IF NOT EXISTS reopen_reason TEXT NOT NULL DEFAULT '';
ALTER TABLE gl_fiscal_periods ADD COLUMN IF NOT EXISTS reopened_by VARCHAR(100);
ALTER TABLE gl_fiscal_periods ADD COLUMN IF NOT EXISTS reopened_at TIMESTAMP WITH TIME ZONE;
