-- SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
-- SPDX-FileCopyrightText: 2026, FutureBuild, Inc. and OpenLBM contributors

-- Rollback for 149_large_cash_worklist.sql.
--
-- Lives in migrations/down/ so cmd/migrate's `migrations/*.sql` glob cannot
-- pick it up and apply it as a forward migration. Apply by hand. Dropping the
-- tables loses the filing state and the statement state; the crossings
-- themselves (large_cash_events) stay, so the worklist falls back to showing
-- every crossing as not filed and not yet stated.

DROP TABLE IF EXISTS large_cash_statements;
DROP TABLE IF EXISTS large_cash_filings;
