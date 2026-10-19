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
               pr_p.branch_id,                                -- PAYMENT refund (via payment_refunds -> payments)
               pr_cm.branch_id,                               -- CREDIT_MEMO refund (via payment_refunds -> credit_memos)
               pt.branch_id,                                  -- PAYMENT from POS cash sale (pos_transactions)
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
    LEFT JOIN payment_refunds pr ON e.source IN ('PAYMENT', 'CREDIT_MEMO') AND pr.id = e.source_ref_id
    LEFT JOIN payments pr_p      ON pr_p.id = pr.payment_id
    LEFT JOIN credit_memos pr_cm ON pr_cm.id = pr.credit_memo_id
    LEFT JOIN pos_transactions pt ON e.source = 'PAYMENT'       AND pt.id = e.source_ref_id
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

-- A reversal belongs to the branch of the root entry it reverses, walked
-- recursively along reverses_entry_id chains of any depth.
WITH RECURSIVE rev_chain AS (
    SELECT e.id, orig.branch_id
    FROM gl_journal_entries e
    JOIN gl_journal_entries orig ON orig.id = e.reverses_entry_id
    WHERE orig.reverses_entry_id IS NULL
    UNION ALL
    SELECT child.id, rc.branch_id
    FROM gl_journal_entries child
    JOIN rev_chain rc ON child.reverses_entry_id = rc.id
)
UPDATE gl_journal_entries e
SET branch_id = rc.branch_id
FROM rev_chain rc
WHERE e.id = rc.id AND rc.branch_id IS NOT NULL;

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

-- 3. The inter-branch clearing account: cross-branch settlements move value
-- between branches over it (due to and due from, ADR 0010 section 9's
-- pattern between companies, one balance sheet account here), and the
-- consolidated statements eliminate it unless asked to keep it.
INSERT INTO gl_accounts (code, name, type, subtype, normal_balance, description)
VALUES ('1095', 'Inter-Branch Clearing', 'ASSET', 'Current Asset', 'DEBIT',
        'Due to and due from branches on cross-branch settlements')
ON CONFLICT (code) DO NOTHING;

-- 4. Historic cross-branch settlements gain their 1095 legs. A payment taken
-- at one branch and applied to another branch's invoice before this
-- migration posted a two line DR 2200 / CR 1020 entry stamped whole to the
-- invoice's branch: the 2200 leg belongs to the payment's branch, and the
-- two 1095 legs carry the value across.
UPDATE gl_journal_lines l
SET branch_id = p.branch_id
FROM gl_journal_entries e
JOIN ar_applications aar ON aar.id = e.source_ref_id
JOIN payments p ON p.id = aar.payment_id
JOIN invoices ai ON ai.id = aar.invoice_id,
     gl_accounts a
WHERE e.id = l.journal_entry_id
  AND a.id = l.account_id
  AND e.source = 'PAYMENT' AND e.status = 'POSTED'
  AND aar.kind = 'PAYMENT' AND aar.reversed_at IS NULL
  AND p.branch_id <> ai.branch_id
  AND a.code = '2200' AND l.debit > 0
  AND (SELECT count(*) FROM gl_journal_lines l2 WHERE l2.journal_entry_id = e.id) = 2;

INSERT INTO gl_journal_lines (id, journal_entry_id, account_id, description, debit, credit, branch_id)
SELECT gen_random_uuid(), e.id, (SELECT id FROM gl_accounts WHERE code = '1095'),
       'Inter-branch clearing backfill', w.dr, w.cr, w.branch_id
FROM gl_journal_entries e
JOIN gl_journal_lines l ON l.journal_entry_id = e.id
JOIN gl_accounts a ON a.id = l.account_id
JOIN ar_applications aar ON aar.id = e.source_ref_id
JOIN payments p ON p.id = aar.payment_id
JOIN invoices ai ON ai.id = aar.invoice_id
CROSS JOIN LATERAL (VALUES
    (0, l.credit, p.branch_id),
    (l.credit, 0, ai.branch_id)
) AS w(dr, cr, branch_id)
WHERE e.source = 'PAYMENT' AND e.status = 'POSTED'
  AND aar.kind = 'PAYMENT' AND aar.reversed_at IS NULL
  AND p.branch_id <> ai.branch_id
  AND a.code = '1020' AND l.credit > 0
  AND (SELECT count(*) FROM gl_journal_lines l2 WHERE l2.journal_entry_id = e.id) = 2;

-- A credit memo applied across branches before this migration posted no
-- entry at all (the dealer wide AR figure needed none): each one gains its
-- balanced four leg transfer entry and the link that keeps the subledger
-- shift and the ledger together.
-- A credit memo applied across branches before this migration posted no
-- entry at all (the dealer wide AR figure needed none): each one gains its
-- balanced four leg transfer entry and the link that keeps the subledger
-- shift and the ledger together.
INSERT INTO gl_journal_entries (id, entry_date, memo, source, source_ref_id, branch_id, status, posted_by, currency)
SELECT gen_random_uuid(), a.applied_on,
       'Inter-branch credit memo application backfill',
       'CREDIT_MEMO', a.id, inv.branch_id, 'POSTED', 'system', a.currency
FROM ar_applications a
JOIN credit_memos cm ON cm.id = a.credit_memo_id
JOIN invoices inv ON inv.id = a.invoice_id
WHERE a.kind = 'CREDIT_MEMO' AND a.reversed_at IS NULL AND a.gl_entry_id IS NULL
  AND cm.branch_id <> inv.branch_id;

INSERT INTO gl_journal_lines (id, journal_entry_id, account_id, description, debit, credit, branch_id)
SELECT gen_random_uuid(), e.id,
       (SELECT id FROM gl_accounts WHERE code = CASE w.leg WHEN 'dr_ar' THEN '1020' WHEN 'cr_clear' THEN '1095' WHEN 'dr_clear' THEN '1095' ELSE '1020' END),
       'Inter-branch credit memo application', w.dr, w.cr, w.branch_id
FROM gl_journal_entries e
JOIN ar_applications a ON a.id = e.source_ref_id
JOIN credit_memos cm ON cm.id = a.credit_memo_id
JOIN invoices inv ON inv.id = a.invoice_id
CROSS JOIN LATERAL (VALUES
    ('dr_ar', a.amount, 0, cm.branch_id),
    ('cr_clear', 0, a.amount, cm.branch_id),
    ('dr_clear', a.amount, 0, inv.branch_id),
    ('cr_ar', 0, a.amount, inv.branch_id)
) AS w(leg, dr, cr, branch_id)
WHERE e.source = 'CREDIT_MEMO' AND e.status = 'POSTED'
  AND a.kind = 'CREDIT_MEMO' AND a.reversed_at IS NULL AND a.gl_entry_id IS NULL
  AND cm.branch_id <> inv.branch_id;

UPDATE ar_applications a
SET gl_entry_id = e.id
FROM gl_journal_entries e
WHERE e.source_ref_id = a.id AND e.source = 'CREDIT_MEMO'
  AND a.kind = 'CREDIT_MEMO' AND a.reversed_at IS NULL AND a.gl_entry_id IS NULL;

-- 5. Indexes the statement reads and the drill down need.
CREATE INDEX IF NOT EXISTS idx_gl_entries_branch_date ON gl_journal_entries(branch_id, entry_date);
CREATE INDEX IF NOT EXISTS idx_gl_lines_account_branch ON gl_journal_lines(account_id, branch_id);

-- 6. Revision for If-Match on the two mutable GL masters.
ALTER TABLE gl_accounts ADD COLUMN IF NOT EXISTS revision INTEGER NOT NULL DEFAULT 1;
ALTER TABLE gl_fiscal_periods ADD COLUMN IF NOT EXISTS revision INTEGER NOT NULL DEFAULT 1;

-- 7. A reopened period records why.
ALTER TABLE gl_fiscal_periods ADD COLUMN IF NOT EXISTS reopen_reason TEXT NOT NULL DEFAULT '';
ALTER TABLE gl_fiscal_periods ADD COLUMN IF NOT EXISTS reopened_by VARCHAR(100);
ALTER TABLE gl_fiscal_periods ADD COLUMN IF NOT EXISTS reopened_at TIMESTAMP WITH TIME ZONE;
