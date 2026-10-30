-- SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
-- SPDX-FileCopyrightText: 2026, FutureBuild, Inc. and OpenLBM contributors

-- Rollback for 150_email_delivery_status.sql.
--
-- Lives in migrations/down/ so cmd/migrate's `migrations/*.sql` glob cannot
-- pick it up and apply it as a forward migration. Apply by hand. The old
-- constraint holds only sent and failed, so the queued rows (sends that never
-- resolved) and the logged rows (sends a log sender took, never transported)
-- become failed with the reason in error: no delivery row is lost, and neither
-- is ever read as sent. The sent and failed history is untouched.

ALTER TABLE email_deliveries DROP CONSTRAINT IF EXISTS email_deliveries_status_check;

UPDATE email_deliveries
   SET status = 'failed',
       error = COALESCE(error, 'rolled back from queued: the send never resolved')
 WHERE status = 'queued';

UPDATE email_deliveries
   SET status = 'failed',
       error = COALESCE(error, 'rolled back from logged: no mail server was set up, the mail was not sent')
 WHERE status = 'logged';

ALTER TABLE email_deliveries ADD CONSTRAINT email_deliveries_status_check
    CHECK (status IN ('sent', 'failed'));
