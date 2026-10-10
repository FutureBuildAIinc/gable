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

-- 1b. Normalise a legacy lowercase or foreign status to the storage
--     spelling the model and the scans carry, and never revive an
--     unknown stored status to a live state (PR 70 review round 5
--     P2-1, lead decision on method). Each row is rewritten in two
--     passes; first the spelling is normalised (UPPER(BTRIM(status)))
--     and the known synonyms are mapped explicitly, so a legacy
--     `delivered` stop, an `IN_PROGRESS` route, a stored `COMPLETE`
--     route (no trailing D), a stored `CANCELED` route (one L) and a
--     stored value with stray whitespace each land on the canonical
--     value the model and the scans carry. Any stored value still
--     outside the vocabulary after that pass is mapped to a TERMINAL
--     non billing state of its table: a route becomes `'CANCELLED'`
--     and a stop becomes `'FAILED'`. TransitionRoute (dispatch from
--     DRAFT or SCHEDULED only) cannot move a CANCELLED route to
--     in_transit; TransitionStop accepts only PENDING or
--     OUT_FOR_DELIVERY as the source for delivery, so a FAILED stop
--     cannot be delivered again, cannot re queue fulfilment and
--     cannot be re billed. The mapping never sends a rewritten row
--     to `'DRAFT'` or `'PENDING'`; both are live states and a legacy
--     row of unknown spelling could have been a finished route or a
--     delivered stop. Each row the rewrite touches is named in a
--     RAISE NOTICE (the table, the id, the old value, the new value)
--     so an operator has the audit trail and a refusal to fail the
--     migration keeps an unattended up applyable on a polluted
--     legacy database. CONTRACT-CHANGES rows for this behaviour
--     cover the up migration.
DO $$
DECLARE
  r record;
  new_status text;
BEGIN
  -- Normalise spelling: trim then upper. BTRIM trims leading and
  -- trailing whitespace, UPPER brings the casing to the storage
  -- vocabulary. The set below is the closed route vocabulary, the
  -- same spelling model.go uses for the storage values and that
  -- every scan reads into a non-null string. The legacy values the
  -- route vocabulary used (IN_PROGRESS, COMPLETE, CANCELED) are
  -- mapped explicitly to their canonical forms. The rewrite covers
  -- a case or whitespace change (status != UPPER(BTRIM(status)))
  -- and a foreign synonym (UPPER(BTRIM(status)) in the synonym
  -- set, mapping to a different canonical value).
  FOR r IN
    SELECT id, status AS old_status
      FROM delivery_routes
     WHERE UPPER(BTRIM(status)) IN (
       'DRAFT', 'SCHEDULED', 'IN_TRANSIT', 'IN_PROGRESS',
       'COMPLETED', 'COMPLETE', 'CANCELLED', 'CANCELED'
     )
       AND (
         status IS DISTINCT FROM UPPER(BTRIM(status))
         OR UPPER(BTRIM(status)) IN ('IN_PROGRESS', 'COMPLETE', 'CANCELED')
       )
  LOOP
    new_status := CASE UPPER(BTRIM(r.old_status))
      WHEN 'IN_PROGRESS' THEN 'IN_TRANSIT'
      WHEN 'COMPLETE'    THEN 'COMPLETED'
      WHEN 'CANCELED'    THEN 'CANCELLED'
      ELSE UPPER(BTRIM(r.old_status))
    END;
    UPDATE delivery_routes
       SET status = new_status
     WHERE id = r.id;
    RAISE NOTICE 'migration 104: delivery_routes id=% old=% new=%',
      r.id, r.old_status, new_status;
  END LOOP;
  -- Any stored value still outside the vocabulary is a legacy
  -- artefact; the migration sends it to the terminal non billing
  -- value CANCELLED so a finished or in flight route of unknown
  -- spelling cannot reopen as dispatchable (DRAFT) and a route that
  -- already had its stops cancelled stays cancelled.
  FOR r IN
    SELECT id, status AS old_status
      FROM delivery_routes
     WHERE status NOT IN (
       'DRAFT', 'SCHEDULED', 'IN_TRANSIT', 'COMPLETED', 'CANCELLED'
     )
  LOOP
    RAISE NOTICE 'migration 104: delivery_routes id=% old=% new=CANCELLED',
      r.id, r.old_status;
    UPDATE delivery_routes
       SET status = 'CANCELLED'
     WHERE id = r.id;
  END LOOP;
END $$;

DO $$
DECLARE
  r record;
  new_status text;
BEGIN
  -- Same normalisation for stops. The legacy vocabulary had no
  -- foreign spellings of the known values; a `CANCELLED` stop is
  -- the one foreign spelling a legacy database carried, and it has
  -- no home in the stop vocabulary, so it lands on the terminal
  -- non billing value FAILED. A value with stray whitespace and a
  -- lowercase known value are handled here too.
  FOR r IN
    SELECT id, status AS old_status
      FROM deliveries
     WHERE UPPER(BTRIM(status)) IN (
       'PENDING', 'OUT_FOR_DELIVERY', 'DELIVERED', 'FAILED', 'PARTIAL'
     )
       AND UPPER(BTRIM(status)) IS DISTINCT FROM status
  LOOP
    new_status := UPPER(BTRIM(r.old_status));
    UPDATE deliveries
       SET status = new_status
     WHERE id = r.id;
    RAISE NOTICE 'migration 104: deliveries id=% old=% new=%',
      r.id, r.old_status, new_status;
  END LOOP;
  -- Any stored value still outside the vocabulary lands on the
  -- terminal non billing value FAILED so a delivered stop of
  -- unknown spelling cannot reopen as deliverable (PENDING) and
  -- cannot re queue fulfilment or re bill.
  FOR r IN
    SELECT id, status AS old_status
      FROM deliveries
     WHERE status NOT IN (
       'PENDING', 'OUT_FOR_DELIVERY', 'DELIVERED', 'FAILED', 'PARTIAL'
     )
  LOOP
    RAISE NOTICE 'migration 104: deliveries id=% old=% new=FAILED',
      r.id, r.old_status;
    UPDATE deliveries
       SET status = 'FAILED'
     WHERE id = r.id;
  END LOOP;
END $$;

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
