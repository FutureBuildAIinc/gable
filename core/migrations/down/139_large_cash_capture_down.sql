-- SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
-- SPDX-FileCopyrightText: 2026, FutureBuild, Inc. and OpenLBM contributors

-- Rollback for 139_large_cash_capture.sql.
--
-- Lives in migrations/down/ so cmd/migrate's `migrations/*.sql` glob cannot
-- pick it up and apply it as a forward migration. Apply by hand. Dropping
-- the three tables loses the captured payer identities and the recorded
-- crossings; the sales and tenders they pointed at stay as they are.

DROP TABLE IF EXISTS large_cash_events;
DROP TABLE IF EXISTS large_cash_tenders;
DROP TABLE IF EXISTS large_cash_payers;
ALTER TABLE products DROP COLUMN IF EXISTS consumer_durable;
ALTER TABLE locations DROP CONSTRAINT IF EXISTS locations_country_code_shape;
ALTER TABLE locations DROP COLUMN IF EXISTS country_code;
