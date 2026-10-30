-- SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
-- SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

-- Rollback for 159_market_index_source.sql.
--
-- Lives in migrations/down/ so cmd/migrate's `migrations/*.sql` glob cannot
-- pick it up and apply it as a forward migration. Apply by hand.

DROP INDEX IF EXISTS uq_market_index_history_as_of_source;
ALTER TABLE market_index_history
    DROP COLUMN IF EXISTS as_of_date,
    DROP COLUMN IF EXISTS source_url,
    DROP COLUMN IF EXISTS fetched_at;
DROP TABLE IF EXISTS market_index_source;
