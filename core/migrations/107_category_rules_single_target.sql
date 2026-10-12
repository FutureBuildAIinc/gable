-- SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
-- SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

-- Migration: 107_category_rules_single_target
-- Description: A category pricing rule targets exactly one customer scope: an
-- account rule names a customer and carries no tier, a tier rule names a tier
-- and carries no customer. The table's original check only required each
-- target kind to carry its own key, so a tier rule could also name a customer
-- and then price every branch's customers of that tier while looking
-- customer scoped to the branch walls (round 6 P1-1). Existing rows are
-- checked first: a row that breaks the invariant is reported by a NOTICE
-- naming the count and the ids, never rewritten, and the constraint goes on
-- NOT VALID when any exists so the report and the schema can be reconciled by
-- hand, and validated immediately when none does.

DO $$
DECLARE
    bad_count INTEGER;
    bad_ids   TEXT;
BEGIN
    SELECT COUNT(*), COALESCE(string_agg(id::text, ', ' ORDER BY id), '')
      INTO bad_count, bad_ids
      FROM category_pricing_rules
     WHERE (target_type = 'TIER' AND customer_id IS NOT NULL)
        OR (target_type = 'ACCOUNT' AND tier IS NOT NULL);

    IF bad_count > 0 THEN
        RAISE NOTICE '107: % category pricing rule row(s) name both a tier and a customer (or an account rule carrying a tier); not rewritten: %', bad_count, bad_ids;
        EXECUTE 'ALTER TABLE category_pricing_rules
                 ADD CONSTRAINT category_pricing_rules_single_target_check
                 CHECK ((target_type = ''ACCOUNT'' AND tier IS NULL)
                    OR (target_type = ''TIER'' AND customer_id IS NULL)) NOT VALID';
    ELSE
        RAISE NOTICE '107: no category pricing rule row names both a tier and a customer; constraint validated on add';
        EXECUTE 'ALTER TABLE category_pricing_rules
                 ADD CONSTRAINT category_pricing_rules_single_target_check
                 CHECK ((target_type = ''ACCOUNT'' AND tier IS NULL)
                    OR (target_type = ''TIER'' AND customer_id IS NULL))';
    END IF;
END $$;
