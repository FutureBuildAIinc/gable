-- SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
-- SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

-- 084: Headless portal core.
--
-- Schema for the capabilities a portal consumer needs and could not get from
-- /api/portal/v1 before: a quote request, a project association it can read
-- back, a lead time on a catalog product, and a delivery reschedule that is
-- recorded as a request rather than written straight onto a dispatched route.
--
-- Everything here is additive. No existing column changes meaning, and every
-- new column is nullable or defaulted so existing rows stay valid.
--
-- Rollback: migrations/down/084_portal_headless_core_down.sql.

-- ---------------------------------------------------------------------------
-- 1. Lead time on a product.
-- ---------------------------------------------------------------------------
-- NULL means "the dealer has not told us", and the portal DTO renders it as
-- null rather than as a number. This column is deliberately NOT backfilled:
-- a guessed lead time is worse than a missing one, because a crew gets
-- scheduled around it.
ALTER TABLE products ADD COLUMN IF NOT EXISTS lead_time_days INTEGER;

DO $$
BEGIN
    ALTER TABLE products ADD CONSTRAINT products_lead_time_days_nonneg
        CHECK (lead_time_days IS NULL OR lead_time_days >= 0);
EXCEPTION
    WHEN duplicate_object THEN NULL;
END $$;

COMMENT ON COLUMN products.lead_time_days IS
    'Dealer-published lead time in days. NULL = unknown; never defaulted or guessed.';

-- ---------------------------------------------------------------------------
-- 2. Portal quote requests.
-- ---------------------------------------------------------------------------
-- A portal user sends a scope to the dealer for pricing. That is an ordinary
-- DRAFT quote on the ERP side; these two columns carry the parts of the ask
-- the ERP quote header had nowhere to put. `source = 'portal'` marks the
-- origin (the column already exists and has no CHECK constraint).
--
-- Note what is NOT here: no markup, no labour, no overhead, no homeowner
-- identity, no signature. Those are contractor-owned and belong in the
-- consumer's own store, not in a dealer's ERP.
ALTER TABLE quotes ADD COLUMN IF NOT EXISTS project_id     UUID REFERENCES projects(id);
ALTER TABLE quotes ADD COLUMN IF NOT EXISTS customer_notes TEXT;

CREATE INDEX IF NOT EXISTS idx_quotes_project_id      ON quotes(project_id);
CREATE INDEX IF NOT EXISTS idx_quotes_customer_source ON quotes(customer_id, source);

-- A special-order request line names a product the dealer does not stock, so
-- it has no product_id (already nullable) and needs somewhere to carry the
-- contractor's description of what they want.
ALTER TABLE quote_lines ADD COLUMN IF NOT EXISTS customer_note TEXT;

-- ---------------------------------------------------------------------------
-- 3. Project association and the order change feed.
-- ---------------------------------------------------------------------------
-- orders.project_id already exists (migration 035, re-applied by 070). What
-- was missing was an index for the by-project read and a composite index for
-- the `?since=` change feed, which orders by (customer_id, updated_at).
CREATE INDEX IF NOT EXISTS idx_orders_project_id       ON orders(project_id);
CREATE INDEX IF NOT EXISTS idx_orders_customer_updated ON orders(customer_id, updated_at DESC);

-- ---------------------------------------------------------------------------
-- 4. Delivery reschedule requests.
-- ---------------------------------------------------------------------------
-- This is a request queue, not a mutation of the schedule, and that is
-- deliberate. `delivery_routes.scheduled_date` is write-once, shared by every
-- stop on the route, and AI_LM's route push (internal/integrations,
-- ReplaceDeliveryRoute) deletes and re-inserts any route still in DRAFT or
-- SCHEDULED for the same (vehicle_id, scheduled_date). A portal write onto
-- delivery_routes would therefore either be silently destroyed by the next
-- push or move every other contractor's stop on that truck. Recording the ask
-- and leaving the schedule to the dispatcher is the only version of this that
-- is not a hazard. See the note in internal/portal/reschedule.go.
CREATE TABLE IF NOT EXISTS portal_delivery_reschedule_requests (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    delivery_id     UUID NOT NULL REFERENCES deliveries(id) ON DELETE CASCADE,
    customer_id     UUID NOT NULL REFERENCES customers(id) ON DELETE RESTRICT,
    requested_date  DATE NOT NULL,
    reason          TEXT NOT NULL DEFAULT '',
    status          TEXT NOT NULL DEFAULT 'PENDING'
                        CHECK (status IN ('PENDING', 'APPLIED', 'DECLINED', 'SUPERSEDED')),
    resolution_note TEXT,
    requested_by    UUID REFERENCES customer_users(id),
    created_at      TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at      TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS idx_pdrr_delivery
    ON portal_delivery_reschedule_requests(delivery_id);
CREATE INDEX IF NOT EXISTS idx_pdrr_customer_status
    ON portal_delivery_reschedule_requests(customer_id, status);

-- One open ask per delivery. A contractor who changes their mind supersedes
-- their previous request rather than queueing a second contradictory one for
-- the dispatcher to guess between.
CREATE UNIQUE INDEX IF NOT EXISTS idx_pdrr_one_open_per_delivery
    ON portal_delivery_reschedule_requests(delivery_id)
    WHERE status = 'PENDING';
