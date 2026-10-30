-- SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
-- SPDX-FileCopyrightText: 2026, FutureBuild, Inc. and OpenLBM contributors

-- 154: the dealer's GST/HST registration (CP-14 A1).
--
-- Every Canadian document prints the supplier's GST/HST number under the
-- dealer block, so it lives on the one dealer profile beside the legal name
-- it belongs to.
--
--   dealer_profile.gst_hst_number          nine digits, RT, four digits
--                                          (123456789RT0001), stored without
--                                          spaces; empty is allowed
--   dealer_profile.gst_hst_not_registered  the small supplier switch: on, no
--                                          GST is charged or printed
--
-- A dealer is either registered (a number may be set) or not (no number), so
-- the two never hold together. Every step is guarded so a second apply
-- changes nothing.

ALTER TABLE dealer_profile
    ADD COLUMN IF NOT EXISTS gst_hst_number TEXT NOT NULL DEFAULT '',
    ADD COLUMN IF NOT EXISTS gst_hst_not_registered BOOLEAN NOT NULL DEFAULT FALSE;

ALTER TABLE dealer_profile
    DROP CONSTRAINT IF EXISTS dealer_profile_gst_hst_number_shape;
ALTER TABLE dealer_profile
    ADD CONSTRAINT dealer_profile_gst_hst_number_shape
    CHECK (gst_hst_number = '' OR gst_hst_number ~ '^[0-9]{9}RT[0-9]{4}$');

ALTER TABLE dealer_profile
    DROP CONSTRAINT IF EXISTS dealer_profile_gst_hst_registration;
ALTER TABLE dealer_profile
    ADD CONSTRAINT dealer_profile_gst_hst_registration
    CHECK (NOT (gst_hst_not_registered AND gst_hst_number <> ''));
