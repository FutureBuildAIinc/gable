-- SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
-- SPDX-FileCopyrightText: 2026, FutureBuild, Inc. and OpenLBM contributors

-- Rollback for 155_quote_follow_up.sql.
--
-- Lives in migrations/down/ so cmd/migrate's `migrations/*.sql` glob cannot
-- pick it up and apply it as a forward migration. Apply by hand.
--
-- A FOLLOW_UP activity is normalised back to NOTE (the read side degrades the
-- same way), and the quote link of any activity is dropped with the column.

UPDATE crm_activities SET activity_type = 'NOTE' WHERE activity_type = 'FOLLOW_UP';

ALTER TABLE crm_activities DROP CONSTRAINT IF EXISTS crm_activities_activity_type_check;
ALTER TABLE crm_activities ADD CONSTRAINT crm_activities_activity_type_check
    CHECK (activity_type IN ('CALL', 'MEETING', 'EMAIL', 'NOTE'));

DROP INDEX IF EXISTS idx_crm_activities_quote_id;
ALTER TABLE crm_activities DROP COLUMN IF EXISTS quote_id;

DROP INDEX IF EXISTS idx_quotes_follow_up_at;
ALTER TABLE quotes DROP COLUMN IF EXISTS follow_up_owner_id;
ALTER TABLE quotes DROP COLUMN IF EXISTS follow_up_at;
