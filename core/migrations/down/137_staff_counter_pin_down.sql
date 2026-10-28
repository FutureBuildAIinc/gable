-- SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
-- SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

-- Down of 137: the step up approvals are a control's confirmation records,
-- not money, so nothing references them and the table goes whole; the staff
-- columns follow. The PIN hash never existed anywhere else.

DROP TABLE IF EXISTS pos_step_up_approvals;

ALTER TABLE staff
    DROP COLUMN IF EXISTS counter_pin_hash,
    DROP COLUMN IF EXISTS counter_pin_set_at,
    DROP COLUMN IF EXISTS counter_pin_failures,
    DROP COLUMN IF EXISTS counter_pin_locked;
