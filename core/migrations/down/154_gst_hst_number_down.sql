-- SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
-- SPDX-FileCopyrightText: 2026, FutureBuild, Inc. and OpenLBM contributors

-- Rollback for 154_gst_hst_number.sql.
--
-- Lives in migrations/down/ so cmd/migrate's `migrations/*.sql` glob cannot
-- pick it up and apply it as a forward migration. Apply by hand.

ALTER TABLE dealer_profile DROP CONSTRAINT IF EXISTS dealer_profile_gst_hst_registration;
ALTER TABLE dealer_profile DROP CONSTRAINT IF EXISTS dealer_profile_gst_hst_number_shape;
ALTER TABLE dealer_profile DROP COLUMN IF EXISTS gst_hst_not_registered;
ALTER TABLE dealer_profile DROP COLUMN IF EXISTS gst_hst_number;
