-- SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
-- SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

-- Migration: 108_pos_branch_backfill
-- Description: the till session, Z report and return reads filter on their
-- branch_id, which hides every row written before that column was stamped
-- from any caller with a branch context: an open drawer left at upgrade reads
-- as no drawer (it can be neither closed nor replaced) and the legacy Z
-- reports and returns vanish from the branch scoped reads. This backfills the
-- branch on the legacy rows of the three tables the filter reads, each row
-- from its own register the way the writes already resolve it (the round 6
-- rule: COALESCE(the register's location's branch, pos_registers.branch_id)).
-- It touches only rows whose branch_id IS NULL, sets nothing else, moves no
-- money and deletes nothing, so it is idempotent: a row it has set is never
-- visited again. A row whose register names no branch is left exactly as it
-- is rather than guessed at, and reported by a NOTICE naming the count and
-- the first ids, so an operator can reconcile it by hand.

DO $$
DECLARE
    tbl       TEXT;
    resolved  TEXT;
    set_count INTEGER;
    stuck     INTEGER;
    stuck_ids TEXT;
BEGIN
    FOREACH tbl IN ARRAY ARRAY['till_sessions', 'till_z_reports', 'pos_returns'] LOOP
        resolved := 'SELECT COALESCE(l.branch_id, r.branch_id) FROM pos_registers r '
                    || 'LEFT JOIN locations l ON l.id = r.location_id WHERE r.id = x.register_id';
        EXECUTE format('UPDATE %I x SET branch_id = (%s) WHERE x.branch_id IS NULL AND (%s) IS NOT NULL',
                       tbl, resolved, resolved);
        GET DIAGNOSTICS set_count = ROW_COUNT;
        EXECUTE format('SELECT COUNT(*), COALESCE((SELECT string_agg(s.id, '', '')
                        FROM (SELECT x.id::text AS id FROM %I x WHERE x.branch_id IS NULL AND (%s) IS NULL
                              ORDER BY x.id LIMIT 5) s), '''')
                       FROM %I x WHERE x.branch_id IS NULL AND (%s) IS NULL',
                       tbl, resolved, tbl, resolved) INTO stuck, stuck_ids;
        RAISE NOTICE '108: %: set the branch from its register on % row(s)', tbl, set_count;
        IF stuck > 0 THEN
            RAISE NOTICE '108: %: % row(s) name a register with no branch and were left as they are, first ids: %', tbl, stuck, stuck_ids;
        END IF;
    END LOOP;
END $$;
