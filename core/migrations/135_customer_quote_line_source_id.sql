-- SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
-- SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

-- Migration 135: persist the dealer quote line id a customer quote line
-- was built from so confirm looks up the exact source line by id.

ALTER TABLE pro_customer_quote_lines
    ADD COLUMN IF NOT EXISTS source_line_id UUID NULL;
