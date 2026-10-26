-- SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
-- SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

-- Down of 138: the alerts are a part of the customer and nothing else holds
-- them, so the table goes whole.

DROP TABLE IF EXISTS customer_alerts;
