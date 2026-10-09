-- SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
-- SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

-- Rollback for 095_admin_wire_contract.sql.
--
-- Lives in migrations/down/ so cmd/migrate's `migrations/*.sql` glob cannot
-- pick it up and apply it as a forward migration. Apply by hand.
--
-- What does not come back: the settings revision anchors are dropped with
-- their table (the settings values themselves are untouched), the RFC numbers
-- are dropped (the rows keep their ids), the rfcs.status CHECK constraint is
-- dropped (but the rows whose status was outside the contract enum stay
-- mapped to 'approved', the same value 095 mapped them to; the down cannot
-- restore the original 'published' value without knowing it was the original),
-- and created_at stays NOT NULL on rfcs and api_keys (the backfilled values
-- are real timestamps; the column was nullable only because nothing ever
-- wrote NULL on purpose).

DROP INDEX IF EXISTS idx_api_keys_created_id;
DROP INDEX IF EXISTS idx_staff_created_id;
DROP INDEX IF EXISTS idx_rfcs_created_id;

DROP TABLE IF EXISTS admin_revisions;

ALTER TABLE rfcs DROP CONSTRAINT IF EXISTS rfcs_status_check;
ALTER TABLE rfcs ALTER COLUMN number DROP DEFAULT;
ALTER TABLE rfcs DROP CONSTRAINT IF EXISTS rfcs_number_key;
ALTER TABLE rfcs DROP COLUMN IF EXISTS number;
DROP FUNCTION IF EXISTS rfc_next_number();
DROP SEQUENCE IF EXISTS rfc_number_seq;

ALTER TABLE rfcs DROP COLUMN IF EXISTS revision;
ALTER TABLE staff DROP COLUMN IF EXISTS revision;
