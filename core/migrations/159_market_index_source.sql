-- SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
-- SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

-- 159: market index source (QE-07-b1).
--
-- A deployment chooses where its market index values come from. Manual entry
-- stays the default and the fallback; the FB Connect feed is configured here
-- and pulled by a later slice. One row holds the choice: the source, the feed
-- URL, the feed key header, the staleness threshold that drives the banner,
-- and the last pull's result (empty until the pull exists).
--
-- Two columns of this row are secrets and are sealed at rest the way the Tech
-- Admin AI keys are, and neither is ever answered by the API:
--
--   feed_url  the FB Connect feed address. The address is itself the secret:
--             it carries the feed's identity in its path, so it is written
--             like the key, never returned, and masked on screen.
--   feed_key  the value of the header the feed key rides in.
--
-- feed_key_header is the header's name. It is configuration, not a
-- credential, so it is answered like the source and the threshold. It is
-- empty by default: the header is optional, and a deployment whose feed
-- needs no key leaves both halves empty.
--
-- market_index_history gains the value's own as-of date, the source URL it
-- came from and when it was fetched, so a value for one index, day and source
-- is stored once and an import of the same file twice changes nothing.

CREATE TABLE IF NOT EXISTS market_index_source (
    singleton       BOOLEAN PRIMARY KEY DEFAULT TRUE CHECK (singleton),
    source          TEXT NOT NULL DEFAULT 'manual'
                    CHECK (source IN ('manual', 'fb_connect_feed')),
    feed_url        TEXT NOT NULL DEFAULT '',
    feed_key_header TEXT NOT NULL DEFAULT '',
    feed_key        TEXT,
    staleness_days  INTEGER NOT NULL DEFAULT 7 CHECK (staleness_days > 0),
    last_pull_at    TIMESTAMPTZ,
    last_pull_ok    BOOLEAN,
    last_pull_error TEXT,
    revision        BIGINT NOT NULL DEFAULT 1,
    updated_at      TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

INSERT INTO market_index_source (singleton) VALUES (TRUE) ON CONFLICT DO NOTHING;

ALTER TABLE market_index_history
    ADD COLUMN IF NOT EXISTS as_of_date DATE,
    ADD COLUMN IF NOT EXISTS source_url TEXT,
    ADD COLUMN IF NOT EXISTS fetched_at TIMESTAMPTZ;

CREATE UNIQUE INDEX IF NOT EXISTS uq_market_index_history_as_of_source
    ON market_index_history(market_index_id, as_of_date, source)
    WHERE as_of_date IS NOT NULL;
