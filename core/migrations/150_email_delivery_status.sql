-- SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
-- SPDX-FileCopyrightText: 2026, FutureBuild, Inc. and OpenLBM contributors

-- 150: the email delivery status vocabulary (CP-14, S09).
--
--   1  queued: the row a sending route writes the moment it answers 202,
--      before the send resolves. Its id rides the 202 body, so the desk can
--      follow the row and say what happened instead of guessing on the 202.
--   2  logged: the send a log sender took (a server with no SMTP configured).
--      The mail never left the process; the row must never say sent.
--
-- Every step is guarded so a second apply changes nothing.

ALTER TABLE email_deliveries DROP CONSTRAINT IF EXISTS email_deliveries_status_check;
ALTER TABLE email_deliveries ADD CONSTRAINT email_deliveries_status_check
    CHECK (status IN ('queued', 'sent', 'logged', 'failed'));
