-- SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
-- SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

-- Rollback for 088_audit_actor.sql.
--
-- Lives in migrations/down/ so cmd/migrate's `migrations/*.sql` glob cannot
-- pick it up and apply it as a forward migration. Apply by hand:
--
--   ALTER TABLE audit_log DROP CONSTRAINT IF EXISTS audit_log_actor_kind_check;
--   ALTER TABLE audit_log DROP INDEX IF EXISTS idx_audit_log_actor;
--   ALTER TABLE audit_log DROP COLUMN IF EXISTS actor_kind;
--   ALTER TABLE audit_log DROP COLUMN IF EXISTS actor_id;
--   ALTER TABLE audit_log DROP COLUMN IF EXISTS acting_as;
--   ALTER TABLE audit_log DROP COLUMN IF EXISTS tool;
--
-- The drop loses the actor attribution on rows written since 088 (the kind,
-- the agent marker and the tool name are not recoverable from the surviving
-- columns; actor_id duplicates user_id only for plain user rows). Apply only
-- when the attribution columns themselves are being removed from the product.

ALTER TABLE audit_log DROP CONSTRAINT IF EXISTS audit_log_actor_kind_check;
DROP INDEX IF EXISTS idx_audit_log_actor;
ALTER TABLE audit_log DROP COLUMN IF EXISTS actor_kind;
ALTER TABLE audit_log DROP COLUMN IF EXISTS actor_id;
ALTER TABLE audit_log DROP COLUMN IF EXISTS acting_as;
ALTER TABLE audit_log DROP COLUMN IF EXISTS tool;
