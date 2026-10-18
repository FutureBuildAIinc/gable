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

-- At least one method always allowed: a before-statement guard takes a
-- self-conflicting SHARE UPDATE EXCLUSIVE table lock (compatible with the
-- ROW EXCLUSIVE lock Postgres already took when opening the relation for
-- DELETE or UPDATE, so two concurrent statements never deadlock on lock
-- upgrade) to serialise concurrent modifications before any tuple is
-- scanned. The after-statement guard then locks at least one surviving row
-- with FOR KEY SHARE so EvalPlanQual re-evaluates tuples deleted by an
-- earlier committed transaction instead of trusting a pre-lock MVCC
-- snapshot, and refuses the statement when no live row remains. TRUNCATE
-- does not fire delete triggers, so its before-statement guard refuses
-- whenever any row exists.

CREATE FUNCTION dealer_signin_methods_keep_one() RETURNS trigger
LANGUAGE plpgsql AS $$
BEGIN
    LOCK TABLE dealer_signin_methods IN SHARE UPDATE EXCLUSIVE MODE;
    IF TG_WHEN = 'BEFORE' THEN
        IF TG_OP = 'TRUNCATE' THEN
            PERFORM 1 FROM dealer_signin_methods FOR KEY SHARE;
            IF FOUND THEN
                RAISE EXCEPTION 'the dealer must allow at least one sign in method';
            END IF;
        END IF;
        RETURN NULL;
    END IF;
    PERFORM 1 FROM dealer_signin_methods FOR KEY SHARE;
    IF NOT FOUND THEN
        RAISE EXCEPTION 'the dealer must allow at least one sign in method';
    END IF;
    RETURN NULL;
END;
$$;

CREATE TRIGGER dealer_signin_methods_serialize_guard
    BEFORE DELETE OR UPDATE ON dealer_signin_methods
    FOR EACH STATEMENT EXECUTE FUNCTION dealer_signin_methods_keep_one();

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
