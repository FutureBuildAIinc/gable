-- SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
-- SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

-- Migration 132: widen pos_account_overrides_kind_check to include
-- refund_method and seal pos_signatures against direct DELETE as well as
-- UPDATE while allowing parent pos_transactions cascade cleanup.

ALTER TABLE pos_account_overrides
    DROP CONSTRAINT IF EXISTS pos_account_overrides_kind_check;

ALTER TABLE pos_account_overrides
    ADD CONSTRAINT pos_account_overrides_kind_check
    CHECK (kind IN ('account', 'check', 'refund_method'));

CREATE OR REPLACE FUNCTION pos_signatures_sealed() RETURNS trigger
LANGUAGE plpgsql AS $$
BEGIN
    IF TG_OP = 'DELETE' AND NOT EXISTS (SELECT 1 FROM pos_transactions t WHERE t.id = OLD.sale_id) THEN
        RETURN OLD;
    END IF;
    RAISE EXCEPTION 'a sealed signature never changes' USING ERRCODE = 'integrity_constraint_violation';
END $$;

DROP TRIGGER IF EXISTS pos_signatures_sealed_trg ON pos_signatures;
CREATE TRIGGER pos_signatures_sealed_trg BEFORE UPDATE OR DELETE ON pos_signatures
    FOR EACH ROW EXECUTE FUNCTION pos_signatures_sealed();
