-- SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
-- SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

-- Rollback for 087_idempotency_keys.sql.
--
-- Lives in migrations/down/ so cmd/migrate's `migrations/*.sql` glob cannot
-- pick it up and apply it as a forward migration. Apply by hand.
--
-- Dropping the table is clean: nothing references it, and the middleware
-- degrades to pass-through (no caching) rather than erroring when the table
-- is missing. Note the trade-off before running this: every completed key
-- row is a replayable response, and dropping the table forgets all of them,
-- so in-flight agent retries go back to double-posting until each client
-- picks a new key.
DROP TABLE IF EXISTS idempotency_keys;
