-- SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
-- SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

-- Rollback for 118_staff_demo_domain.sql.
--
-- Lives in migrations/down/ so cmd/migrate's `migrations/*.sql` glob cannot
-- pick it up and apply it as a forward migration. Apply by hand.
--
-- The addresses stay on the reserved domain on purpose: the old values were a
-- real domain, and an operator may have edited a row since.

DO $$
BEGIN
    RAISE NOTICE '118 down: the demo staff addresses stay on the reserved domain';
END $$;
