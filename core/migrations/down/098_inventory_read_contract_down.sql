-- SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
-- SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

-- Rollback for 098_inventory_read_contract.sql.
--
-- Lives in migrations/down/ so cmd/migrate's `migrations/*.sql` glob cannot
-- pick it up and apply it as a forward migration. Apply by hand.
--
-- What does not come back: the created_at column goes entirely (it is this
-- migration's own artifact; no dealer input ever sets it, and the old shape
-- has no place for it), and updated_at stays NOT NULL (a column the up
-- migration filled is never un-filled). Rows written after the up carry a
-- creation time the old shape cannot hold; rolling back loses only that
-- server-minted timestamp, never a dealer's data.

DROP INDEX IF EXISTS idx_inventory_created_at_id;
ALTER TABLE inventory DROP COLUMN IF EXISTS created_at;
