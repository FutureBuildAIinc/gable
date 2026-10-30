-- SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
-- SPDX-FileCopyrightText: 2026, FutureBuild, Inc. and OpenLBM contributors

-- Rollback for 157_quote_revisions.sql.
--
-- Lives in migrations/down/ so cmd/migrate's `migrations/*.sql` glob cannot
-- pick it up and apply it as a forward migration. Apply by hand, and delete
-- 157_quote_revisions.sql from schema_migrations with it.

DROP TRIGGER IF EXISTS quote_revisions_no_update ON quote_revisions;
DROP FUNCTION IF EXISTS quote_revisions_refuse_update();
DROP TABLE IF EXISTS quote_revisions;
ALTER TABLE quotes DROP CONSTRAINT IF EXISTS quotes_version_number_check;
ALTER TABLE quotes DROP COLUMN IF EXISTS version_number;

-- The link table's revoke vocabulary loses the word a revise used; a row
-- carrying it goes first (a live deployment rolls this back only by hand).
DELETE FROM pro_portal_share_tokens WHERE revoked_reason = 'revised';
DO $$
BEGIN
    IF EXISTS (SELECT 1 FROM pg_constraint
               WHERE conname = 'pro_portal_share_tokens_revoked_reason_check'
                 AND pg_get_constraintdef(oid) LIKE '%revised%') THEN
        ALTER TABLE pro_portal_share_tokens
            DROP CONSTRAINT pro_portal_share_tokens_revoked_reason_check;
        ALTER TABLE pro_portal_share_tokens
            ADD CONSTRAINT pro_portal_share_tokens_revoked_reason_check
            CHECK (revoked_reason IN ('revoked', 'rotated'));
    END IF;
END $$;
