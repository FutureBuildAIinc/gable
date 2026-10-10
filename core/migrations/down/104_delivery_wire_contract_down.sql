-- SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
-- SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

-- Rollback for 104_delivery_wire_contract.sql.
--
-- Lives in migrations/down/ so cmd/migrate's `migrations/*.sql` glob cannot
-- pick it up and apply it as a forward migration. Apply by hand.
--
-- What does not come back: the NULL timestamps this migration filled stay
-- filled (a column the up migration filled is never un-filled), and the
-- revision columns go entirely with their values (the old wire had no
-- revision to keep).

DROP INDEX IF EXISTS idx_deliveries_route_sequence_id;
DROP INDEX IF EXISTS idx_delivery_routes_scheduled_id;
DROP INDEX IF EXISTS idx_drivers_created_id;
DROP INDEX IF EXISTS idx_vehicles_created_id;

ALTER TABLE deliveries DROP COLUMN IF EXISTS revision;
ALTER TABLE delivery_routes DROP COLUMN IF EXISTS revision;
ALTER TABLE drivers DROP COLUMN IF EXISTS revision;
ALTER TABLE vehicles DROP COLUMN IF EXISTS revision;

ALTER TABLE deliveries ALTER COLUMN updated_at DROP NOT NULL;
ALTER TABLE deliveries ALTER COLUMN created_at DROP NOT NULL;
ALTER TABLE deliveries ALTER COLUMN status DROP NOT NULL;
ALTER TABLE delivery_routes ALTER COLUMN updated_at DROP NOT NULL;
ALTER TABLE delivery_routes ALTER COLUMN created_at DROP NOT NULL;
ALTER TABLE delivery_routes ALTER COLUMN status DROP NOT NULL;
ALTER TABLE drivers ALTER COLUMN updated_at DROP NOT NULL;
ALTER TABLE drivers ALTER COLUMN created_at DROP NOT NULL;
ALTER TABLE vehicles ALTER COLUMN updated_at DROP NOT NULL;
ALTER TABLE vehicles ALTER COLUMN created_at DROP NOT NULL;
