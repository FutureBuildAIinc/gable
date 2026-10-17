-- SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
-- SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

-- Down of 130_pro_portal_schedules_and_boards.
--
-- The role mapping back is the stated inverse for the three roles that had a
-- legacy spelling. site_super and ap_clerk have no legacy equivalent, so they
-- fall back to Buyer (a transacting user without team reach): the legacy
-- vocabulary cannot express them, and Buyer is the least surprising home.

DROP TABLE IF EXISTS portal_schedule_holds;
DROP TABLE IF EXISTS yard_schedule_windows;

ALTER TABLE orders DROP COLUMN IF EXISTS site_delivery_instructions;

ALTER TABLE customer_users DROP CONSTRAINT IF EXISTS customer_users_role_check;
UPDATE customer_users
SET role = CASE role
        WHEN 'owner' THEN 'Admin'
        WHEN 'estimator' THEN 'View-Only'
        WHEN 'project_manager' THEN 'Buyer'
        WHEN 'site_super' THEN 'Buyer'
        WHEN 'ap_clerk' THEN 'Buyer'
        ELSE role
    END,
    updated_at = NOW()
WHERE role IN ('owner', 'estimator', 'project_manager', 'site_super', 'ap_clerk');

ALTER TABLE portal_invites DROP CONSTRAINT IF EXISTS portal_invites_role_check;
UPDATE portal_invites
SET role = CASE role
        WHEN 'owner' THEN 'Admin'
        WHEN 'estimator' THEN 'View-Only'
        WHEN 'project_manager' THEN 'Buyer'
        WHEN 'site_super' THEN 'Buyer'
        WHEN 'ap_clerk' THEN 'Buyer'
        ELSE role
    END
WHERE role IN ('owner', 'estimator', 'project_manager', 'site_super', 'ap_clerk');
