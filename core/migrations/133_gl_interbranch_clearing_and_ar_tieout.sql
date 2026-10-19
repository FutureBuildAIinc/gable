-- SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
-- SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

-- 133: the inter-branch clearing account and the historic cross-branch AR
-- settlement backfill. The runner records migrations by file name and never
-- rereads an applied file, so a database that already applied 125 at its
-- merged state must receive these here rather than through an edit of 125.
-- Closed periods are no bar to this backfill: the newly created credit memo
-- transfer entries are dated in their original (a.applied_on) periods and
-- the existing cross-branch journal lines are updated in place even when
-- those fiscal periods are already closed. Consolidated figures are
-- unchanged (1095 is eliminated and the 1020 legs net to zero), while the
-- per-branch statements in those periods are corrected.

-- 1. The inter-branch clearing account: cross-branch settlements move value
-- between branches over it (due to and due from, ADR 0010 section 9's
-- pattern between companies, one balance sheet account here), and the
-- consolidated statements eliminate it unless asked to keep it.
INSERT INTO gl_accounts (code, name, type, subtype, normal_balance, description)
VALUES ('1095', 'Inter-Branch Clearing', 'ASSET', 'Current Asset', 'DEBIT',
        'Due to and due from branches on cross-branch settlements')
ON CONFLICT (code) DO NOTHING;

-- 2. Historic cross-branch payment applications gain their 1095 legs. A
-- payment taken at one branch and applied to another branch's invoice before
-- the branch dimension posted a two line DR 2200 / CR 1020 entry stamped
-- whole to the invoice's branch: the 2200 leg belongs to the payment's
-- branch, and the two 1095 legs carry the value across. The two line guard
-- leaves alone any entry that already carries transfer legs.
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

-- 3. A credit memo applied across branches before the branch dimension
-- posted no entry at all (the dealer wide AR figure needed none): each one
-- gains its balanced four leg transfer entry and the link that keeps the
-- subledger shift and the ledger together. The gl_entry_id IS NULL guard
-- leaves alone any application that already carries its entry.
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

-- 4. Cross-branch payment and credit memo applications (and their reversals)
-- posted by code between 125 and 133 wrote their inter-branch transfer legs
-- to 1010 Cash instead of 1095 Inter-Branch Clearing. Move those legs to 1095
-- by account and description.
UPDATE gl_journal_lines
SET account_id = (SELECT id FROM gl_accounts WHERE code = '1095')
WHERE account_id = (SELECT id FROM gl_accounts WHERE code = '1010')
  AND description IN (
      'Inter-branch cash settlement',
      'Inter-branch AR settlement',
      'Inter-branch cash settlement reversal'
  );
