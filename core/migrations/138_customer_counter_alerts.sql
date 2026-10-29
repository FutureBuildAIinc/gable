-- SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
-- SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

-- 138: Customer counter and order alerts (CP-11).
--
-- An alert is what the office leaves on a customer for the counter and the
-- order desk: a note (read it, carry on) or a warning (it may stop the sale).
-- It is a part of the customer, so it is added, seen and ended on the
-- customer's own routes, guarded as the customer's edits are and audited with
-- them.
--
-- Two flags say where the alert speaks: show_at_counter puts it in the
-- counter's customer panel, show_at_orders puts it on the order page. A
-- warning may ask to be acknowledged (acknowledge_required): the counter then
-- has to read it and say so before the sale is taken, and the sale carries the
-- acknowledgement. A note never asks: only a warning may, so the flag on a
-- note is a mistake the table refuses.
--
-- An alert is ended, not deleted: ended_at and ended_by keep the record of
-- who called it off, and the row stays for the audit trail. The revision moves
-- when it is ended, so an end of an alert that has already been ended is
-- refused as stale.

CREATE TABLE IF NOT EXISTS customer_alerts (
    id                   UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    customer_id          UUID NOT NULL REFERENCES customers(id) ON DELETE CASCADE,
    kind                 TEXT NOT NULL CHECK (kind IN ('note', 'warning')),
    text                 TEXT NOT NULL CHECK (btrim(text) <> '' AND length(text) <= 500),
    show_at_counter      BOOLEAN NOT NULL DEFAULT FALSE,
    show_at_orders       BOOLEAN NOT NULL DEFAULT FALSE,
    acknowledge_required BOOLEAN NOT NULL DEFAULT FALSE,
    created_by           TEXT NOT NULL CHECK (btrim(created_by) <> ''),
    created_at           TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    ended_by             TEXT,
    ended_at             TIMESTAMPTZ,
    revision             BIGINT NOT NULL DEFAULT 1 CHECK (revision >= 1),
    CONSTRAINT customer_alerts_ack_is_a_warning
        CHECK (NOT acknowledge_required OR kind = 'warning'),
    CONSTRAINT customer_alerts_ended_together
        CHECK ((ended_at IS NULL) = (ended_by IS NULL))
);

-- The customer's own list, newest first.
CREATE INDEX IF NOT EXISTS idx_customer_alerts_customer
    ON customer_alerts (customer_id, created_at DESC, id);

-- The counter's read: the alerts still open that speak at the counter.
CREATE INDEX IF NOT EXISTS idx_customer_alerts_open_counter
    ON customer_alerts (customer_id, created_at DESC, id)
    WHERE ended_at IS NULL AND show_at_counter;
