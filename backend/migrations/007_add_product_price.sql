-- SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
-- SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

-- Add Base Price to Products
ALTER TABLE products ADD COLUMN base_price NUMERIC(12, 4) NOT NULL DEFAULT 0.0000;
