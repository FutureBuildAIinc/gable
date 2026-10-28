-- SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
-- SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

-- Down of 147: drop the counter till session link and index from payments.

DROP INDEX IF EXISTS idx_payments_till_session;

ALTER TABLE payments
    DROP COLUMN IF EXISTS till_session_id;
