-- SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
-- SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

-- R1-12: the transactional outbox and per subscriber cursors.
--
-- events_outbox holds one row per domain event, written inside the mutation's
-- transaction through pkg/outbox.Write, so the event commits or rolls back
-- with the mutation it describes. Delivery happens later: a drain runner
-- republishes committed rows to the in-process bus (the exposure emails), and
-- GET /api/v1/events serves the same rows to outside consumers.
--
--   position     the ordering key, drawn from events_outbox_position_seq at
--                insert UNDER pg_advisory_xact_lock (outbox lock key
--                1869462626, the ASCII bytes 'outb'), so position order is
--                commit order among event writers. Readers page with
--                position > cursor and can never skip a row that commits
--                late, because a writer cannot draw a position until every
--                lower-positioned writer's transaction has finished. A rolled
--                back writer burns its position: gaps are expected and
--                harmless. See docs/adr/0003-events-outbox.md for why a
--                reader-side snapshot horizon alone cannot give this.
--   event_id     the logical identity consumers dedup on.
--   type         the dot-delimited event type; also the in-process bus
--                subject (quote.exposure.flagged and friends).
--   org          the deployment's org slug; one database per dealer today,
--                so the writer stamps 'default' until a deployment level org
--                identity exists.
--   branch_id    the event's branch scope, filled from the request's branch
--                context when the caller did not set one; NULL means not
--                pinned to a branch.
--   entity_type
--   entity_id    the entity the event is about, the envelope's
--                entity {kind, id}.
--   data         the small summary payload.
--   at           when the event happened.
--
-- event_subscriber_cursors holds one drain cursor per named consumer; the
-- drain locks a row FOR UPDATE SKIP LOCKED, reads past it in position order,
-- publishes, then advances it.
--
-- Idempotent: guarded creates. Rollback:
-- migrations/down/089_events_outbox_down.sql.

CREATE SEQUENCE IF NOT EXISTS events_outbox_position_seq;

CREATE TABLE IF NOT EXISTS events_outbox (
    position    BIGINT      NOT NULL DEFAULT nextval('events_outbox_position_seq'),
    event_id    UUID        NOT NULL,
    type        TEXT        NOT NULL,
    org         TEXT        NOT NULL,
    branch_id   UUID,
    entity_type TEXT        NOT NULL,
    entity_id   UUID        NOT NULL,
    data        JSONB       NOT NULL,
    at          TIMESTAMPTZ NOT NULL,
    CONSTRAINT events_outbox_pkey PRIMARY KEY (position),
    CONSTRAINT events_outbox_event_id_key UNIQUE (event_id)
);

CREATE INDEX IF NOT EXISTS idx_events_outbox_type ON events_outbox (type);

CREATE TABLE IF NOT EXISTS event_subscriber_cursors (
    subscriber TEXT        NOT NULL,
    position   BIGINT      NOT NULL DEFAULT 0,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    CONSTRAINT event_subscriber_cursors_pkey PRIMARY KEY (subscriber)
);
