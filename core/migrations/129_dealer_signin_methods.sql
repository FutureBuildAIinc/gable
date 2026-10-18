-- SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
-- SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

-- Migration: 129_dealer_signin_methods

-- The dealer's allowed sign in methods (S9): one row per method code the
-- dealer allows, in the order the door renders them. The code is a closed
-- vocabulary of six names, held by a database check so a bad write cannot
-- park a method no server knows. The table is the single dealer's (Gable is
-- a single tenant per install, like dealer_profile), so there is no tenant
-- column. The default row keeps every existing install's sign in exactly as
-- it is today: email and password, the one method the server implements
-- (the portal login). A guard trigger refuses the delete, update or truncate
-- that would leave the dealer with no allowed method at all.

CREATE TABLE dealer_signin_methods (
    method          TEXT PRIMARY KEY
                    CHECK (method IN ('email_password', 'passkey', 'magic_link', 'google', 'microsoft', 'oidc')),
    sort_order      INT NOT NULL,
    oidc_issuer_url TEXT,
    oidc_client_id  TEXT,
    oidc_label      TEXT,
    updated_at      TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- At least one method always allowed: the statement level guards count the
-- rows that remain after the change and refuse the statement (rolling the
-- transaction back) when none would remain. TRUNCATE does not fire delete
-- triggers, so it has its own guard and is always refused while any row
-- exists, and the table can never be empty while the guard exists.

CREATE FUNCTION dealer_signin_methods_keep_one() RETURNS trigger
LANGUAGE plpgsql AS $$
DECLARE
    remaining INT;
BEGIN
    SELECT count(*) INTO remaining FROM dealer_signin_methods;
    -- After a delete or update no row may remain; before a truncate rows
    -- still stand, so any row at all means the truncate must be refused.
    IF (TG_OP IN ('DELETE', 'UPDATE') AND remaining = 0)
       OR (TG_OP = 'TRUNCATE' AND remaining > 0) THEN
        RAISE EXCEPTION 'the dealer must allow at least one sign in method';
    END IF;
    RETURN NULL;
END;
$$;

CREATE TRIGGER dealer_signin_methods_delete_guard
    AFTER DELETE ON dealer_signin_methods
    FOR EACH STATEMENT EXECUTE FUNCTION dealer_signin_methods_keep_one();

CREATE TRIGGER dealer_signin_methods_update_guard
    AFTER UPDATE ON dealer_signin_methods
    FOR EACH STATEMENT EXECUTE FUNCTION dealer_signin_methods_keep_one();

CREATE TRIGGER dealer_signin_methods_truncate_guard
    BEFORE TRUNCATE ON dealer_signin_methods
    FOR EACH STATEMENT EXECUTE FUNCTION dealer_signin_methods_keep_one();

-- The safe default for every existing tenant: the one method the server
-- implements today, so nothing about any existing install's sign in changes.
INSERT INTO dealer_signin_methods (method, sort_order)
VALUES ('email_password', 1);
