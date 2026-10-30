-- SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
-- SPDX-FileCopyrightText: 2026, FutureBuild, Inc. and OpenLBM contributors

-- 157: quote revisions (QE-01-b).
--
-- A sent quote revised stays Q-NNNNNN and becomes the next version: the
-- quote keeps its number and its id, the order keeps converting from the one
-- quote id, and every sent version is kept as an immutable snapshot the
-- dealer can read on the quote page.
--
--   quotes.version_number   the version of the quote's current body; 1 until
--                           the first revise moves it to 2 (quotes.revision
--                           stays the ETag concurrency counter it always was)
--   quote_revisions         one row per sent version: the header and the
--                           lines exactly as sent, money in the wire's
--                           integer units (_cents, _ten_thousandths)
--
-- A send writes its version's one row (a re-send of the same version adds
-- none), inside the send's transaction (ADR 0003). The rows are immutable:
-- the trigger refuses any update, and no code path updates them.
--
-- A revise kills the quote's live share link so the contractor cannot accept
-- a version the dealer has taken back; the link table's revoke vocabulary
-- gains the word for it.

ALTER TABLE quotes
    ADD COLUMN IF NOT EXISTS version_number INTEGER NOT NULL DEFAULT 1;

DO $$
BEGIN
    IF NOT EXISTS (SELECT 1 FROM pg_constraint WHERE conname = 'quotes_version_number_check') THEN
        ALTER TABLE quotes
            ADD CONSTRAINT quotes_version_number_check CHECK (version_number >= 1);
    END IF;
END $$;

CREATE TABLE IF NOT EXISTS quote_revisions (
    id             UUID PRIMARY KEY DEFAULT uuid_generate_v4(),
    quote_id       UUID NOT NULL REFERENCES quotes(id) ON DELETE CASCADE,
    version_number INTEGER NOT NULL,
    snapshot       JSONB NOT NULL,
    created_by     TEXT NOT NULL DEFAULT '',
    created_at     TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    reason         TEXT NOT NULL DEFAULT '',
    CONSTRAINT quote_revisions_version_number_check CHECK (version_number >= 1)
);

CREATE UNIQUE INDEX IF NOT EXISTS quote_revisions_quote_version_key
    ON quote_revisions (quote_id, version_number);

-- A sent version is a fact about what the customer saw: never rewritten.
CREATE OR REPLACE FUNCTION quote_revisions_refuse_update() RETURNS trigger AS $$
BEGIN
    RAISE EXCEPTION 'a sent version is never rewritten: quote_revisions is immutable'
        USING ERRCODE = 'restrict_violation';
END;
$$ LANGUAGE plpgsql;

DROP TRIGGER IF EXISTS quote_revisions_no_update ON quote_revisions;
CREATE TRIGGER quote_revisions_no_update
    BEFORE UPDATE ON quote_revisions
    FOR EACH ROW EXECUTE FUNCTION quote_revisions_refuse_update();

DO $$
BEGIN
    IF EXISTS (SELECT 1 FROM pg_constraint WHERE conname = 'pro_portal_share_tokens_revoked_reason_check')
       AND NOT EXISTS (
           SELECT 1 FROM pg_constraint
           WHERE conname = 'pro_portal_share_tokens_revoked_reason_check'
             AND pg_get_constraintdef(oid) LIKE '%revised%'
       ) THEN
        ALTER TABLE pro_portal_share_tokens
            DROP CONSTRAINT pro_portal_share_tokens_revoked_reason_check;
        ALTER TABLE pro_portal_share_tokens
            ADD CONSTRAINT pro_portal_share_tokens_revoked_reason_check
            CHECK (revoked_reason IN ('revoked', 'rotated', 'revised'));
    END IF;
END $$;
