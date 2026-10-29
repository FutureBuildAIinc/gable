-- SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
-- SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

-- 140: Cash rounding without pennies (CP-18).
--
-- The branch's own rule for the cash it takes: OFF, the default, or the
-- nearest five cents. The column lives on locations because the branch is
-- the unit that answers the question, the pattern being the branch's own
-- default_tax_rate column: a column with a default cannot be left unset,
-- and every branch can be set on its own.
--
-- The two rounding accounts: a cash total rounded up is income to the
-- branch, one rounded down is its loss. Two accounts, because a round-up and
-- a round-down are different in kind, and one account whose normal balance
-- is sometimes a debit and sometimes a credit would make the trial balance
-- and the profit and loss read wrongly.

ALTER TABLE locations
    ADD COLUMN IF NOT EXISTS cash_rounding_mode TEXT NOT NULL DEFAULT 'OFF';

DO $$
BEGIN
    IF NOT EXISTS (SELECT 1 FROM pg_constraint WHERE conname = 'locations_cash_rounding_mode_check') THEN
        ALTER TABLE locations
            ADD CONSTRAINT locations_cash_rounding_mode_check
            CHECK (cash_rounding_mode IN ('OFF', 'NEAREST_5_CENTS'));
    END IF;
END $$;

INSERT INTO gl_accounts (code, name, type, subtype, normal_balance, description) VALUES
    ('4095', 'Cash Rounding Income', 'REVENUE', 'Operating', 'CREDIT',
     'Cash rounded up at the counter: the difference between the rounded cash and the exact total'),
    ('5045', 'Cash Rounding Loss', 'EXPENSE', 'Operating', 'DEBIT',
     'Cash rounded down at the counter: the difference between the rounded cash and the exact total')
ON CONFLICT (code) DO NOTHING;
