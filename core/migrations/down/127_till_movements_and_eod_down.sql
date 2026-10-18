-- SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
-- SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

-- Rollback for 127_till_movements_and_eod.sql.
--
-- Lives in migrations/down/ so cmd/migrate's `migrations/*.sql` glob cannot
-- pick it up and apply it as a forward migration. Apply by hand.
--
-- It refuses while a session waits for sign off: that drawer was counted but
-- its variance is not on the ledger yet, and the old shape has no state to
-- hold it in. Sign it off (or reopen the decision) first. The movements and
-- the day closes are dropped with their tables; the journal entries the
-- movements posted stay on the ledger. The two accounts go only while no
-- journal line uses them.

DO $$
DECLARE
    pending BIGINT;
BEGIN
    SELECT count(*) INTO pending FROM till_sessions WHERE status = 'PENDING_SIGNOFF';
    IF pending > 0 THEN
        RAISE EXCEPTION '127 down refused: % till session(s) still wait for variance sign off', pending;
    END IF;
END $$;

DROP TABLE IF EXISTS till_day_closes;
DROP TABLE IF EXISTS till_variance_policy;
DROP TABLE IF EXISTS till_movements;

DROP INDEX IF EXISTS idx_till_sessions_pending;
DROP INDEX IF EXISTS idx_till_sessions_branch_opened;
ALTER TABLE till_sessions DROP COLUMN IF EXISTS signed_off_at;
ALTER TABLE till_sessions DROP COLUMN IF EXISTS signoff_reason;
ALTER TABLE till_sessions DROP COLUMN IF EXISTS signoff_by;
ALTER TABLE till_sessions DROP COLUMN IF EXISTS variance_threshold_cents;
ALTER TABLE till_sessions DROP COLUMN IF EXISTS closed_by;

DELETE FROM system_settings WHERE key = 'till.variance_threshold_cents';

DELETE FROM gl_accounts a
WHERE a.code IN ('1015', '4090')
  AND NOT EXISTS (SELECT 1 FROM gl_journal_lines l WHERE l.account_id = a.id);
