-- SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
-- SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

-- Rollback for 131_pro_customer_quotes_and_selections.sql.
--
-- Lives in migrations/down/ so cmd/migrate's `migrations/*.sql` glob cannot
-- pick it up and apply it as a forward migration. Apply by hand. This drops
-- every customer quote, its lines, links, codes and the approval log: the
-- data is lost. Dropping a table is not a row change, so the append only
-- triggers do not stand in the way. The schema_migrations row, if you want
-- the migration to apply again, is removed by hand:
--   DELETE FROM schema_migrations WHERE version = '131_pro_customer_quotes_and_selections.sql';

DROP TABLE IF EXISTS pro_customer_quote_approval_log;
DROP FUNCTION IF EXISTS pro_customer_quote_approval_log_refuse();
DROP TABLE IF EXISTS pro_portal_verification_codes;
DROP TABLE IF EXISTS pro_portal_share_tokens;
DROP TABLE IF EXISTS pro_customer_quote_selections;
DROP TABLE IF EXISTS pro_customer_quote_overhead_lines;
DROP TABLE IF EXISTS pro_customer_quote_labour_lines;
DROP TABLE IF EXISTS pro_customer_quote_category_rules;
DROP TABLE IF EXISTS pro_customer_quote_lines;
DROP TABLE IF EXISTS pro_customer_quote_option_groups;
DROP TABLE IF EXISTS pro_customer_quotes;
DROP TABLE IF EXISTS pro_contractor_settings;
DROP SEQUENCE IF EXISTS pro_customer_quote_seq;
