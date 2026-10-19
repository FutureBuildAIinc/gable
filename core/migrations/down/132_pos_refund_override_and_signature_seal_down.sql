-- SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
-- SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

DROP TRIGGER IF EXISTS pos_signatures_sealed_trg ON pos_signatures;

CREATE OR REPLACE FUNCTION pos_signatures_sealed() RETURNS trigger
LANGUAGE plpgsql AS $$
BEGIN
    RAISE EXCEPTION 'a sealed signature never changes' USING ERRCODE = 'integrity_constraint_violation';
END $$;

CREATE TRIGGER pos_signatures_sealed_trg BEFORE UPDATE ON pos_signatures
    FOR EACH ROW EXECUTE FUNCTION pos_signatures_sealed();

DELETE FROM pos_account_overrides WHERE kind = 'refund_method';

ALTER TABLE pos_account_overrides
    DROP CONSTRAINT IF EXISTS pos_account_overrides_kind_check;

ALTER TABLE pos_account_overrides
    ADD CONSTRAINT pos_account_overrides_kind_check
    CHECK (kind IN ('account', 'check'));
