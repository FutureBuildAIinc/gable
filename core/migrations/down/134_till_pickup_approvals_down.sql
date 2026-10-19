-- SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
-- SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

-- Down of 134: the pickup approvals are a control's confirmation records, not
-- money, so nothing references them and the table goes whole.

DROP TABLE IF EXISTS till_pickup_approvals;
