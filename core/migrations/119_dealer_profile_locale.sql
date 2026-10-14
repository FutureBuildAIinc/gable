-- SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
-- SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

-- Migration: 119_dealer_profile_locale
-- Description: the dealer's locale on the one dealer wide profile (migration
-- 115). Every date the app shows is formatted for this locale; it defaults to
-- en-US and the demo seed sets en-CA. The shape check here is only the tag's
-- outline; the write route validates the tag itself.

ALTER TABLE dealer_profile
    ADD COLUMN IF NOT EXISTS locale TEXT NOT NULL DEFAULT 'en-US';

ALTER TABLE dealer_profile
    DROP CONSTRAINT IF EXISTS dealer_profile_locale_shape;

ALTER TABLE dealer_profile
    ADD CONSTRAINT dealer_profile_locale_shape
    CHECK (locale ~ '^[a-zA-Z]{2,3}(-[a-zA-Z0-9]{2,8})*$');
