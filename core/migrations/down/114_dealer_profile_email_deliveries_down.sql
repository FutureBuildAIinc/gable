-- SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
-- SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

-- Rollback for 114_dealer_profile_email_deliveries.sql.
--
-- Lives in migrations/down/ so cmd/migrate's `migrations/*.sql` glob cannot
-- pick it up and apply it as a forward migration. Apply by hand.
--
-- What does not come back: the dealer profile and the delivery record are
-- dropped with their rows. The profile is re-entered through the desk's
-- settings after an upgrade; the delivery record is a log of sends that cannot
-- be rebuilt.

DROP TABLE IF EXISTS email_deliveries;
DROP TABLE IF EXISTS dealer_profile;
