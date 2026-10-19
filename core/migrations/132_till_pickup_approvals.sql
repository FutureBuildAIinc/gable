-- SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
-- SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

-- 132: Till pickup approvals (CP-08, PR 178 sweep P2-2).
--
-- A pickup is cash taken by a second person. The taker confirms under their
-- own signed-in subject (an unconsumed row here, nothing moves yet), and the
-- cashier's later record of the same session, amount and taker consumes that
-- approval: the stored taken_by is then the taker's authenticated subject,
-- never a free text name the cashier typed. One confirmation backs one
-- pickup; a consumed row is kept for the audit trail.

CREATE TABLE IF NOT EXISTS till_pickup_approvals (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    till_session_id UUID NOT NULL REFERENCES till_sessions(id),
    amount_cents    BIGINT NOT NULL CHECK (amount_cents > 0),
    taken_by        TEXT NOT NULL CHECK (btrim(taken_by) <> ''),
    created_at      TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    consumed_at     TIMESTAMPTZ
);

CREATE INDEX IF NOT EXISTS idx_till_pickup_approvals_pending
    ON till_pickup_approvals (till_session_id, amount_cents, LOWER(taken_by))
    WHERE consumed_at IS NULL;
