-- SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
-- SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

-- Migration: 042_fleet_photos
-- Description: Add photo_url column to vehicles and drivers for fleet management.

ALTER TABLE vehicles ADD COLUMN IF NOT EXISTS photo_url TEXT;
ALTER TABLE drivers  ADD COLUMN IF NOT EXISTS photo_url TEXT;
