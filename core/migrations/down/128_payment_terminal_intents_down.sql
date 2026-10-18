-- SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
-- SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

-- Rollback for 128_payment_terminal_intents.sql.
--
-- Lives in migrations/down/ so cmd/migrate's `migrations/*.sql` glob cannot
-- pick it up and apply it as a forward migration. Apply by hand. Dropping the
-- two tables loses the terminal intent ledger and its lost answer queue; the
-- documents and payments they pointed at stay as they are.

DROP TABLE IF EXISTS payment_terminal_lost_items;
DROP TABLE IF EXISTS payment_terminal_intents;
