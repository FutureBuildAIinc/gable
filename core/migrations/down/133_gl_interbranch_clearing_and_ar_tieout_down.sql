-- SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
-- SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

-- Down for 133: undo the inter-branch backfills and remove the 1095
-- account, exactly what the up migration added and nothing else.

-- The credit memo application transfer entries the up migration backfilled
-- go, and their links with them. Only the backfill's own entries (its memo)
-- are touched: a live cross-branch application's entry keeps its legs.
UPDATE ar_applications a
SET gl_entry_id = NULL
WHERE a.gl_entry_id IN (
    SELECT e.id FROM gl_journal_entries e
    WHERE e.source = 'CREDIT_MEMO' AND e.memo = 'Inter-branch credit memo application backfill');

DELETE FROM gl_journal_lines WHERE journal_entry_id IN (
    SELECT e.id FROM gl_journal_entries e
    WHERE e.source = 'CREDIT_MEMO' AND e.memo = 'Inter-branch credit memo application backfill');

DELETE FROM gl_journal_entries
WHERE source = 'CREDIT_MEMO' AND memo = 'Inter-branch credit memo application backfill';

-- The payment applications' 2200 legs return to the entry's branch, then
-- their backfilled 1095 legs go.
UPDATE gl_journal_lines l
SET branch_id = e.branch_id
FROM gl_journal_entries e, gl_accounts a
WHERE e.id = l.journal_entry_id AND e.source = 'PAYMENT'
  AND a.id = l.account_id
  AND a.code = '2200'
  AND EXISTS (SELECT 1 FROM gl_journal_lines lb
              WHERE lb.journal_entry_id = e.id AND lb.description = 'Inter-branch clearing backfill');

DELETE FROM gl_journal_lines l
USING gl_journal_entries e
WHERE e.id = l.journal_entry_id
  AND e.source = 'PAYMENT'
  AND l.description = 'Inter-branch clearing backfill';

-- Cross-branch settlement legs moved from 1010 to 1095 return to 1010.
UPDATE gl_journal_lines
SET account_id = (SELECT id FROM gl_accounts WHERE code = '1010')
WHERE account_id = (SELECT id FROM gl_accounts WHERE code = '1095')
  AND description IN (
      'Inter-branch cash settlement',
      'Inter-branch AR settlement',
      'Inter-branch cash settlement reversal'
  );

-- The account itself goes only while nothing references it.
DELETE FROM gl_accounts WHERE code = '1095'
  AND NOT EXISTS (SELECT 1 FROM gl_journal_lines l WHERE l.account_id = gl_accounts.id);
