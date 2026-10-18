-- SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
-- SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

-- Migration: 128_payment_terminal_intents
-- Description: the card terminal's intent ledger and its lost answer queue
-- (CP-05 and IX-01, ADR 0005 sections 11 and 14.2 C2-5). Every card leg the
-- counter sends to a terminal is one row of payment_terminal_intents: the
-- claim is committed before any device call, each state move is one guarded
-- statement, and the row carries only the answer's safe fields (brand, last
-- four, auth code, the provider's own reference). A card number, track data
-- or a CVV is never stored, not even in a fixture or a log line. An intent
-- the sweep turns lost because the device may have moved money becomes one
-- payment_terminal_lost_items row a manager resolves after reading the
-- terminal's own record.

CREATE TABLE IF NOT EXISTS payment_terminal_intents (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    transaction_id UUID NOT NULL REFERENCES pos_transactions (id),
    leg_key TEXT NOT NULL,
    register_id TEXT NOT NULL,
    branch_id UUID NOT NULL REFERENCES locations (id),
    amount_cents BIGINT NOT NULL CHECK (amount_cents > 0),
    kind TEXT NOT NULL CHECK (kind IN ('sale', 'refund', 'void')),
    state TEXT NOT NULL CHECK (state IN ('claimed', 'pending_at_device', 'authorized', 'declined', 'lost')),
    device_id TEXT NOT NULL DEFAULT '',
    provider_ref TEXT NOT NULL DEFAULT '',
    idempotency_key TEXT NOT NULL,
    lease_until TIMESTAMPTZ NOT NULL,
    claimed_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    dispatched_at TIMESTAMPTZ,
    answered_at TIMESTAMPTZ,
    answer_brand TEXT NOT NULL DEFAULT '',
    answer_last4 TEXT NOT NULL DEFAULT '',
    answer_auth_code TEXT NOT NULL DEFAULT '',
    answer_provider_ref TEXT NOT NULL DEFAULT '',
    answer_verdict TEXT NOT NULL DEFAULT '' CHECK (answer_verdict IN ('', 'authorized', 'declined')),
    -- an authorized leg is spent when its money is booked on the sale or put
    -- back at the device; an authorized leg that is not spent still backs a
    -- sale that has not completed (the completion reuses it, never charging
    -- the card again)
    spent_at TIMESTAMPTZ,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    CONSTRAINT payment_terminal_intents_one_leg UNIQUE (transaction_id, leg_key, kind),
    CONSTRAINT payment_terminal_intents_key_unique UNIQUE (idempotency_key),
    CONSTRAINT payment_terminal_intents_answer_set CHECK (
        (state IN ('authorized', 'declined')) = (answer_verdict <> '')
    ),
    CONSTRAINT payment_terminal_intents_answer_fields CHECK (
        state NOT IN ('authorized', 'declined')
        OR answered_at IS NOT NULL
    )
);

CREATE INDEX IF NOT EXISTS idx_payment_terminal_intents_state_lease
    ON payment_terminal_intents (state, lease_until);

CREATE INDEX IF NOT EXISTS idx_payment_terminal_intents_register_state
    ON payment_terminal_intents (register_id, state);

CREATE INDEX IF NOT EXISTS idx_payment_terminal_intents_provider_ref
    ON payment_terminal_intents (provider_ref) WHERE provider_ref <> '';

-- The manager's lost answer queue: one row per intent the sweep turned lost
-- from pending_at_device (a claimed intent never reached the device, so it
-- closes with no queue item). The row carries who resolves it and what the
-- terminal's own record said; the resolution applies or closes the money.

CREATE TABLE IF NOT EXISTS payment_terminal_lost_items (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    intent_id UUID NOT NULL UNIQUE REFERENCES payment_terminal_intents (id),
    branch_id UUID NOT NULL REFERENCES locations (id),
    register_id TEXT NOT NULL,
    transaction_id UUID NOT NULL REFERENCES pos_transactions (id),
    kind TEXT NOT NULL,
    amount_cents BIGINT NOT NULL,
    device_id TEXT NOT NULL DEFAULT '',
    provider_ref TEXT NOT NULL DEFAULT '',
    state TEXT NOT NULL CHECK (state IN ('open', 'resolving', 'resolved_authorized', 'resolved_declined')) DEFAULT 'open',
    resolved_by TEXT NOT NULL DEFAULT '',
    resolution_reference TEXT NOT NULL DEFAULT '',
    resolved_at TIMESTAMPTZ,
    -- a resolution that calls the gateway claims the item first (state
    -- resolving, committed) and holds it on this lease: a second or racing
    -- resolution is refused until the first ends or the lease runs out
    resolving_by TEXT NOT NULL DEFAULT '',
    resolving_until TIMESTAMPTZ,
    -- the verdict and reference of an answer that arrived after the sweep or
    -- against a recorded verdict, kept so the manager reads the device's own
    -- word; a late authorized answer after a declined resolution reopens the
    -- item
    late_verdict TEXT NOT NULL DEFAULT '' CHECK (late_verdict IN ('', 'authorized', 'declined')),
    late_provider_ref TEXT NOT NULL DEFAULT '',
    late_brand TEXT NOT NULL DEFAULT '',
    late_last4 TEXT NOT NULL DEFAULT '',
    late_auth_code TEXT NOT NULL DEFAULT '',
    late_at TIMESTAMPTZ,
    reopened_at TIMESTAMPTZ,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS idx_payment_terminal_lost_items_state
    ON payment_terminal_lost_items (state, created_at);

CREATE INDEX IF NOT EXISTS idx_payment_terminal_lost_items_branch
    ON payment_terminal_lost_items (branch_id, state);
