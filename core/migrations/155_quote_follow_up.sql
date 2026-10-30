-- SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
-- SPDX-FileCopyrightText: 2026, FutureBuild, Inc. and OpenLBM contributors

-- 155: quote follow up (QE-04-a).
--
-- A salesperson can park a quote in a follow up queue: the date they promised
-- to chase the customer and the salesperson who owns the chase. Completing a
-- follow up clears it; what happened is logged as an activity, which can now
-- attach to the quote itself and still carries the customer, so it shows on
-- the customer's feed too.
--
--   quotes.follow_up_at            when to chase (null: no follow up)
--   quotes.follow_up_owner_id      the salesperson who owns it (sales_team)
--   crm_activities.quote_id        the quote an activity is about
--   crm_activities activity type   gains FOLLOW_UP

ALTER TABLE quotes
    ADD COLUMN IF NOT EXISTS follow_up_at TIMESTAMPTZ,
    ADD COLUMN IF NOT EXISTS follow_up_owner_id UUID REFERENCES sales_team(id);

CREATE INDEX IF NOT EXISTS idx_quotes_follow_up_at
    ON quotes (follow_up_at)
    WHERE follow_up_at IS NOT NULL;

ALTER TABLE crm_activities
    ADD COLUMN IF NOT EXISTS quote_id UUID REFERENCES quotes(id) ON DELETE SET NULL;

CREATE INDEX IF NOT EXISTS idx_crm_activities_quote_id
    ON crm_activities (quote_id, created_at DESC, id DESC)
    WHERE quote_id IS NOT NULL;

-- Rebuild the activity type check the way migration 096 did, with FOLLOW_UP
-- added to the vocabulary. Existing rows already satisfy the wider check.
ALTER TABLE crm_activities DROP CONSTRAINT IF EXISTS crm_activities_activity_type_check;
ALTER TABLE crm_activities ADD CONSTRAINT crm_activities_activity_type_check
    CHECK (activity_type IN ('CALL', 'MEETING', 'EMAIL', 'NOTE', 'FOLLOW_UP'));
