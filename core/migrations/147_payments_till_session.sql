-- SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
-- SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

-- 147: Counter deposits on orders ride the open till session (CP-07, PR 202).
--
-- A cash or check deposit taken on an order at the counter records the open
-- till_session_id on the payment row so the drawer's expected cash, the till
-- report, the close and the Z report include it alongside POS tender payments.

ALTER TABLE payments
    ADD COLUMN IF NOT EXISTS till_session_id UUID NULL REFERENCES till_sessions(id);

CREATE INDEX IF NOT EXISTS idx_payments_till_session
    ON payments (till_session_id)
    WHERE till_session_id IS NOT NULL;
