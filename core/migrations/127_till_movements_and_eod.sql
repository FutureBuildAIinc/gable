-- SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
-- SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

-- 127: Till control and end of day by branch (CP-08, CP-09).
--
-- 1. till_movements: safe drops, pickups, paid in and paid out against an
--    open till session. Each carries the session's register and branch, its
--    actor, a reason code from a closed list for the paid kinds, and the
--    second person who takes the cash for a pickup. Its journal entry posts
--    in the act's own transaction and is named here.
-- 2. till_sessions: a blind close over the branch's variance threshold waits
--    in PENDING_SIGNOFF for a manager; who closed it, the threshold in force
--    and who signed off, when and why are kept on the session.
-- 3. till_variance_policy: the threshold per branch, over the database wide
--    default in system_settings (till.variance_threshold_cents).
-- 4. till_day_closes: one close per branch per business date, with the
--    deposit slip reference, a frozen totals snapshot, the bank account the
--    deposit goes to with the journal entry that posts it (debit the bank
--    account's GL account; credit 1015 for the day's drops and pickups and
--    1010 for the rest) and the bank statement line it was matched to.
-- 5. Two accounts the chart lacks: 1015 Undeposited Funds (cash taken out of a
--    drawer and not yet banked) and 4090 Other Income (cash received at the
--    counter outside a sale). Paid out and the fee and refund paid ins use
--    accounts that exist (5020, 4030).
--
-- Money columns are integer cents. Every statement is guarded so a second
-- apply is a no-op.

INSERT INTO gl_accounts (code, name, type, subtype, normal_balance, description) VALUES
    ('1015', 'Undeposited Funds', 'ASSET', 'Current Asset', 'DEBIT',
        'Cash taken out of a till drawer (drops and pickups) and not yet banked'),
    ('4090', 'Other Income', 'REVENUE', 'Operating', 'CREDIT',
        'Cash received at the counter outside a sale')
ON CONFLICT (code) DO NOTHING;

-- 1. Movements.
CREATE TABLE IF NOT EXISTS till_movements (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    till_session_id UUID NOT NULL REFERENCES till_sessions(id),
    register_id     VARCHAR(32) NOT NULL REFERENCES pos_registers(id),
    branch_id       UUID NOT NULL REFERENCES locations(id),
    kind            VARCHAR(16) NOT NULL CHECK (kind IN ('safe_drop', 'pickup', 'paid_in', 'paid_out')),
    amount_cents    BIGINT NOT NULL CHECK (amount_cents > 0),
    reason_code     VARCHAR(24) CHECK (reason_code IN
        ('supplies', 'freight', 'fuel', 'postage', 'meals', 'repairs', 'fee_received', 'vendor_refund', 'other')),
    note            TEXT NOT NULL DEFAULT '',
    actor           TEXT NOT NULL,
    taken_by        TEXT,
    gl_entry_id     UUID REFERENCES gl_journal_entries(id),
    created_at      TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    -- a paid kind names its reason, a drop or pickup names none
    CONSTRAINT till_movements_reason_kind CHECK ((kind IN ('paid_in', 'paid_out')) = (reason_code IS NOT NULL)),
    -- the code other is explained
    CONSTRAINT till_movements_other_note CHECK (reason_code IS DISTINCT FROM 'other' OR btrim(note) <> ''),
    -- a pickup names the person taking the cash, and only a pickup does
    CONSTRAINT till_movements_taker_kind CHECK ((kind = 'pickup') = (taken_by IS NOT NULL AND btrim(taken_by) <> ''))
);
CREATE INDEX IF NOT EXISTS idx_till_movements_session ON till_movements (till_session_id, created_at, id);
CREATE INDEX IF NOT EXISTS idx_till_movements_branch_created ON till_movements (branch_id, created_at);

-- 2. The sign off state on the session.
ALTER TABLE till_sessions ADD COLUMN IF NOT EXISTS closed_by TEXT;
ALTER TABLE till_sessions ADD COLUMN IF NOT EXISTS variance_threshold_cents BIGINT;
ALTER TABLE till_sessions ADD COLUMN IF NOT EXISTS signoff_by TEXT;
ALTER TABLE till_sessions ADD COLUMN IF NOT EXISTS signoff_reason TEXT;
ALTER TABLE till_sessions ADD COLUMN IF NOT EXISTS signed_off_at TIMESTAMPTZ;
CREATE INDEX IF NOT EXISTS idx_till_sessions_pending ON till_sessions (branch_id, closed_at, id)
    WHERE status = 'PENDING_SIGNOFF';
CREATE INDEX IF NOT EXISTS idx_till_sessions_branch_opened ON till_sessions (branch_id, opened_at);

-- 3. The variance threshold.
CREATE TABLE IF NOT EXISTS till_variance_policy (
    branch_id       UUID PRIMARY KEY REFERENCES locations(id),
    threshold_cents BIGINT NOT NULL CHECK (threshold_cents >= 0),
    updated_by      TEXT NOT NULL DEFAULT '',
    updated_at      TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
INSERT INTO system_settings (key, value) VALUES ('till.variance_threshold_cents', '500')
ON CONFLICT (key) DO NOTHING;

-- 4. The branch day close.
CREATE TABLE IF NOT EXISTS till_day_closes (
    id                       UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    branch_id                UUID NOT NULL REFERENCES locations(id),
    business_date            DATE NOT NULL,
    status                   TEXT NOT NULL DEFAULT 'CLOSED' CHECK (status IN ('CLOSED', 'REOPENED')),
    deposit_slip_ref         TEXT NOT NULL CHECK (btrim(deposit_slip_ref) <> ''),
    closed_by                TEXT NOT NULL,
    closed_at                TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    cash_to_deposit_cents    BIGINT NOT NULL,
    checks_to_deposit_cents  BIGINT NOT NULL,
    card_total_cents         BIGINT NOT NULL,
    totals                   JSONB NOT NULL,
    bank_account_id          UUID REFERENCES bank_accounts(id),
    gl_entry_id              UUID REFERENCES gl_journal_entries(id),
    bank_transaction_id      UUID REFERENCES bank_transactions(id),
    deposit_difference_cents BIGINT,
    deposit_matched_by       TEXT,
    deposit_matched_at       TIMESTAMPTZ,
    reopened_by              TEXT,
    reopened_at              TIMESTAMPTZ,
    reopen_reason            TEXT,
    reversal_gl_entry_id     UUID REFERENCES gl_journal_entries(id),
    CONSTRAINT till_day_closes_match_whole CHECK (
        (bank_transaction_id IS NULL) = (deposit_difference_cents IS NULL)
        AND (bank_transaction_id IS NULL) = (deposit_matched_at IS NULL)),
    CONSTRAINT till_day_closes_reopen_whole CHECK (
        (status = 'CLOSED' AND reopened_by IS NULL AND reopened_at IS NULL AND reopen_reason IS NULL AND reversal_gl_entry_id IS NULL)
        OR (status = 'REOPENED' AND reopened_by IS NOT NULL AND reopened_at IS NOT NULL AND btrim(reopen_reason) <> ''))
);
-- at most one active close per branch per business date; reopened rows stay for history
CREATE UNIQUE INDEX IF NOT EXISTS till_day_closes_one_per_day
    ON till_day_closes (branch_id, business_date) WHERE status = 'CLOSED';
-- a bank line backs at most one branch day
CREATE UNIQUE INDEX IF NOT EXISTS ux_till_day_closes_bank_line
    ON till_day_closes (bank_transaction_id) WHERE bank_transaction_id IS NOT NULL;
