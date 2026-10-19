-- SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
-- SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

-- Down for 125: drop the branch dimension, the statement indexes and the
-- period reopen columns the migration added. Nothing else in between.

DROP INDEX IF EXISTS idx_gl_lines_account_branch;
DROP INDEX IF EXISTS idx_gl_entries_branch_date;

ALTER TABLE gl_journal_lines DROP COLUMN IF EXISTS branch_id;
ALTER TABLE gl_journal_entries DROP COLUMN IF EXISTS branch_id;

ALTER TABLE gl_accounts DROP COLUMN IF EXISTS revision;
ALTER TABLE gl_fiscal_periods DROP COLUMN IF EXISTS revision;
ALTER TABLE gl_fiscal_periods DROP COLUMN IF EXISTS reopen_reason;
ALTER TABLE gl_fiscal_periods DROP COLUMN IF EXISTS reopened_by;
ALTER TABLE gl_fiscal_periods DROP COLUMN IF EXISTS reopened_at;
