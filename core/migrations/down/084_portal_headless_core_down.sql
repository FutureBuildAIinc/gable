-- SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
-- SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

-- Rollback for 084_portal_headless_core.sql.
--
-- Lives in migrations/down/ so cmd/migrate's `migrations/*.sql` glob cannot
-- pick it up and apply it as a forward migration. Apply by hand.
--
-- Destructive: dropping quotes.project_id / quotes.customer_notes /
-- quote_lines.customer_note discards the scope a contractor wrote, and
-- dropping portal_delivery_reschedule_requests discards every pending ask.
-- Take a backup first.

DROP TABLE IF EXISTS portal_delivery_reschedule_requests;

DROP INDEX IF EXISTS idx_orders_customer_updated;
DROP INDEX IF EXISTS idx_orders_project_id;

ALTER TABLE quote_lines DROP COLUMN IF EXISTS customer_note;

DROP INDEX IF EXISTS idx_quotes_customer_source;
DROP INDEX IF EXISTS idx_quotes_project_id;
ALTER TABLE quotes DROP COLUMN IF EXISTS customer_notes;
ALTER TABLE quotes DROP COLUMN IF EXISTS project_id;

ALTER TABLE products DROP CONSTRAINT IF EXISTS products_lead_time_days_nonneg;
ALTER TABLE products DROP COLUMN IF EXISTS lead_time_days;
