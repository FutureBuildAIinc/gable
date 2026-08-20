-- SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
-- SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors
--
-- Repair bank-reconciliation amounts that were written in dollars into columns
-- declared as cents.
--
-- The columns below have always been BIGINT and their schema comments have
-- always said "cents" (migration 060). But the repository wrote
-- float64(cents)/100.0 and read the value back multiplied by 100, so Postgres
-- truncated the float to a whole number on the way in. The round trip was:
--
--     12345 cents ($123.45) -> 123.45 -> BIGINT 123 -> read 123 -> 12300 cents
--
-- Every imported bank line was silently rounded down to the whole dollar, and
-- any line under $1.00 became zero. The reconciliation difference was computed
-- from those truncated values, so a statement could reconcile to zero while the
-- real cents did not agree.
--
-- The repository now passes and scans cents directly. This migration converts
-- rows written by the old code so they mean the same thing as new rows.
--
-- WHAT THIS CAN AND CANNOT RECOVER
--
-- Multiplying by 100 restores the magnitude a reader saw in the UI ($123.00),
-- because that is what the old read path reported. It does NOT restore the
-- original cents — 12345 was destroyed by the truncation before it ever reached
-- the database, and no migration can bring it back. Rows affected by this were
-- already wrong in every report; this makes them consistently wrong-to-the-
-- dollar rather than wrong by a factor of 100 against the corrected code.
--
-- Any reconciliation session that closed on truncated figures should be
-- re-imported from the source statement if the cents matter.
--
-- IDEMPOTENCY
--
-- Guarded by a marker row in schema_repairs so a re-run cannot multiply twice.
-- The migrator applies each file once by filename, but a hand-run or a restored
-- database could re-enter this, and a second pass would inflate every amount
-- a hundredfold.

CREATE TABLE IF NOT EXISTS schema_repairs (
    id          TEXT PRIMARY KEY,
    applied_at  TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    note        TEXT NOT NULL DEFAULT ''
);

DO $$
BEGIN
    IF EXISTS (SELECT 1 FROM schema_repairs WHERE id = '083_bankrecon_cents') THEN
        RAISE NOTICE '083: bankrecon cents repair already applied, skipping';
        RETURN;
    END IF;

    UPDATE bank_transactions
       SET amount = amount * 100
     WHERE amount <> 0;

    UPDATE reconciliation_sessions
       SET statement_balance  = statement_balance  * 100,
           gl_balance         = gl_balance         * 100,
           cleared_total      = cleared_total      * 100,
           outstanding_total  = outstanding_total  * 100,
           difference         = difference         * 100;

    INSERT INTO schema_repairs (id, note)
    VALUES ('083_bankrecon_cents',
            'Converted dollar-valued rows to cents after the repository stopped dividing by 100.');
END $$;
