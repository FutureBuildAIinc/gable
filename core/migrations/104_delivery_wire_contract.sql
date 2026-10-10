-- SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
-- SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

-- Migration: 104_delivery_wire_contract
-- Description: Carry the delivery module onto the wire contract (ADR 0001):
-- NOT NULL timestamps the keyset lists order and scan, a revision on each
-- mutable document, and the keyset indexes for the module's list orderings.

-- 1. Fill NULL created_at / updated_at and set both NOT NULL. The vehicle and
--    driver lists order on (created_at, id); every repository scan reads
--    updated_at into a non-null value, so one NULL would take the list down.
UPDATE vehicles SET created_at = NOW() WHERE created_at IS NULL;
UPDATE vehicles SET updated_at = created_at WHERE updated_at IS NULL;
ALTER TABLE vehicles ALTER COLUMN created_at SET NOT NULL;
ALTER TABLE vehicles ALTER COLUMN updated_at SET NOT NULL;

UPDATE drivers SET created_at = NOW() WHERE created_at IS NULL;
UPDATE drivers SET updated_at = created_at WHERE updated_at IS NULL;
ALTER TABLE drivers ALTER COLUMN created_at SET NOT NULL;
ALTER TABLE drivers ALTER COLUMN updated_at SET NOT NULL;

UPDATE delivery_routes SET created_at = NOW() WHERE created_at IS NULL;
UPDATE delivery_routes SET updated_at = created_at WHERE updated_at IS NULL;
ALTER TABLE delivery_routes ALTER COLUMN created_at SET NOT NULL;
ALTER TABLE delivery_routes ALTER COLUMN updated_at SET NOT NULL;

UPDATE deliveries SET created_at = NOW() WHERE created_at IS NULL;
UPDATE deliveries SET updated_at = created_at WHERE updated_at IS NULL;
ALTER TABLE deliveries ALTER COLUMN created_at SET NOT NULL;
ALTER TABLE deliveries ALTER COLUMN updated_at SET NOT NULL;

-- 1a. Fill NULL route and stop statuses with the start state the model and
--     the legacy scans treat as the only one for fresh rows, then set both
--     NOT NULL: a legacy row with no status leaves every reader reading it
--     as a NULL and one 500 too many.
UPDATE delivery_routes SET status = 'DRAFT' WHERE status IS NULL;
ALTER TABLE delivery_routes ALTER COLUMN status SET NOT NULL;

UPDATE deliveries SET status = 'PENDING' WHERE status IS NULL;
ALTER TABLE deliveries ALTER COLUMN status SET NOT NULL;

-- 1b. Normalise a legacy lowercase or mixed-case status to the storage
--     spelling the model and the scans carry. Without this rewrite a stored
--     lowercase or foreign value (a `delivered` stop, an `in_progress`
--     route) walks through ModelText's map lookup, hits no key, and the
--     reader emits the start-state name instead of the row's own status:
--     the read and the write then disagree about the same row and a
--     dispatch against `in_progress` answers 409 while the read still
--     prints `draft`. The legacy vocabulary comes from the start-state
--     fill (a NULL was filled with `'DRAFT'` for routes and `'PENDING'`
--     for stops) and the storage spelling; the model's map keys are all
--     UPPERCASE, so uppercasing the known lowercase values brings them
--     into the same vocabulary. Any value outside the known list is a
--     legacy artefact: route rows map to `'DRAFT'`, stop rows to
--     `'PENDING'` (a tracked started value, the row visible but stalled
--     until an operator rewrites it). CONTRACT-CHANGES rows for this
--     behaviour cover the up migration.
UPDATE delivery_routes
   SET status = UPPER(status)
 WHERE LOWER(status) IN ('draft', 'scheduled', 'in_transit', 'completed', 'cancelled');
UPDATE delivery_routes
   SET status = 'DRAFT'
 WHERE status NOT IN ('DRAFT', 'SCHEDULED', 'IN_TRANSIT', 'COMPLETED', 'CANCELLED');

UPDATE deliveries
   SET status = UPPER(status)
 WHERE LOWER(status) IN ('pending', 'out_for_delivery', 'delivered', 'failed', 'partial');
UPDATE deliveries
   SET status = 'PENDING'
 WHERE status NOT IN ('PENDING', 'OUT_FOR_DELIVERY', 'DELIVERED', 'FAILED', 'PARTIAL');

-- 2. The revision every mutable document carries (ADR 0001 section 11).
--    Existing rows start at 1; the DEFAULT serves raw writers (the seed, the
--    frozen integration seam) so their rows carry one too.
ALTER TABLE vehicles ADD COLUMN IF NOT EXISTS revision BIGINT NOT NULL DEFAULT 1;
ALTER TABLE drivers ADD COLUMN IF NOT EXISTS revision BIGINT NOT NULL DEFAULT 1;
ALTER TABLE delivery_routes ADD COLUMN IF NOT EXISTS revision BIGINT NOT NULL DEFAULT 1;
ALTER TABLE deliveries ADD COLUMN IF NOT EXISTS revision BIGINT NOT NULL DEFAULT 1;

-- 3. Keyset indexes for the list orderings: vehicles and drivers newest
--    first, routes by scheduled date then id, a route's stops in stop order.
CREATE INDEX IF NOT EXISTS idx_vehicles_created_id ON vehicles (created_at DESC, id DESC);
CREATE INDEX IF NOT EXISTS idx_drivers_created_id ON drivers (created_at DESC, id DESC);
CREATE INDEX IF NOT EXISTS idx_delivery_routes_scheduled_id ON delivery_routes (scheduled_date DESC, id DESC);
CREATE INDEX IF NOT EXISTS idx_deliveries_route_sequence_id ON deliveries (route_id, stop_sequence ASC, id ASC);
