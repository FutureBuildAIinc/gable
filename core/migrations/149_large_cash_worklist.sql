-- SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
-- SPDX-FileCopyrightText: 2026, FutureBuild, Inc. and OpenLBM contributors

-- 149: the large cash worklist's filing and statement state (CP-17b, item
-- 299). The crossings themselves (large_cash_events) and the captured payers
-- are migration 139's; this migration adds the two clocks that hang off a
-- crossing:
--
--   1  large_cash_filings: one row per crossing, the filing state. The row
--      starts as not_filed and is claimed in place when the manager records
--      the filing, so an event has one filing row and a second manager cannot
--      claim the same event. The state carries the fields that belong to it:
--      a filed or voluntary filing names the date it was filed, the BSA
--      E-Filing acknowledgement number the manager typed and who recorded it.
--      past_deadline is the sweep's mark: the filing is past its deadline and
--      still not filed.
--   2  large_cash_statements: the customer statement state per payer and
--      year. A statement is a separate obligation from the filing and runs on
--      its own clock (the year boundary, not the 15 day deadline), so it has
--      its own standing row per payer and year.
--
-- Every step is guarded so a second apply changes nothing.

-- 1. The filing state per crossing.

CREATE TABLE IF NOT EXISTS large_cash_filings (
    id                 UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    event_id           UUID NOT NULL REFERENCES large_cash_events (id) ON DELETE CASCADE,
    state              TEXT NOT NULL DEFAULT 'not_filed'
                       CHECK (state IN ('not_filed', 'filed', 'voluntary')),
    filed_date         DATE NULL,
    acknowledgement_no TEXT NOT NULL DEFAULT '',
    recorded_by        TEXT NOT NULL DEFAULT '',
    recorded_at        TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    past_deadline      BOOLEAN NOT NULL DEFAULT FALSE,
    -- The state ties to its own fields: a filing that has not happened
    -- carries no date, no acknowledgement number and no recorder, and a
    -- filing that has happened carries the date and the recorder. A required
    -- filing carries the BSA E-Filing acknowledgement number and a voluntary
    -- one does not: a voluntarily filed Form 8300 below the threshold is
    -- filed on paper. A filed row is never past its deadline: the sweep only
    -- marks the filings that are still open.
    CONSTRAINT large_cash_filings_state_fields CHECK (
        (state = 'not_filed'
            AND filed_date IS NULL
            AND acknowledgement_no = ''
            AND recorded_by = ''
            AND past_deadline IN (FALSE, TRUE))
        OR
        (state = 'filed'
            AND filed_date IS NOT NULL
            AND acknowledgement_no <> ''
            AND recorded_by <> ''
            AND past_deadline = FALSE)
        OR
        (state = 'voluntary'
            AND filed_date IS NOT NULL
            AND acknowledgement_no = ''
            AND recorded_by <> ''
            AND past_deadline = FALSE)
    ),
    CONSTRAINT large_cash_filings_ack_shape CHECK (length(acknowledgement_no) <= 64)
);

-- One filing row per crossing, under concurrency: the partial unique index is
-- the house's standing index shape (the credit holds' own). Its predicate
-- names the closed set of states, so every row of the table is covered and
-- two managers claiming one event at the same time cannot both land a row.
CREATE UNIQUE INDEX IF NOT EXISTS ux_large_cash_filings_one_per_event
    ON large_cash_filings (event_id) WHERE state IN ('not_filed', 'filed', 'voluntary');

-- The sweep's query: the filings that are still open, oldest first.
CREATE INDEX IF NOT EXISTS idx_large_cash_filings_open
    ON large_cash_filings (state, recorded_at) WHERE state = 'not_filed';

-- 2. The statement state per payer and year.

CREATE TABLE IF NOT EXISTS large_cash_statements (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    payer_key       TEXT NOT NULL,
    year            INT NOT NULL CHECK (year BETWEEN 2000 AND 2100),
    state           TEXT NOT NULL DEFAULT 'not_issued'
                    CHECK (state IN ('not_issued', 'issued')),
    issued_date     DATE NULL,
    issued_by       TEXT NOT NULL DEFAULT '',
    issued_at       TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    -- The state ties to its own fields: an issued statement names the day it
    -- went out and who issued it, and one not yet issued carries neither.
    CONSTRAINT large_cash_statements_state_fields CHECK (
        (state = 'not_issued' AND issued_date IS NULL AND issued_by = '')
        OR
        (state = 'issued' AND issued_date IS NOT NULL AND issued_by <> '')
    )
);

-- One statement per payer and year.
CREATE UNIQUE INDEX IF NOT EXISTS ux_large_cash_statements_one_per_payer_year
    ON large_cash_statements (payer_key, year);
