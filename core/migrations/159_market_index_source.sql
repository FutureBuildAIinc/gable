-- SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
-- SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

-- 159: market index source (QE-07-b1).
--
-- A deployment chooses where its market index values come from. Manual entry
-- stays the default and the fallback; the FB Connect feed is configured here
-- and pulled by a later slice. One row holds the choice: the source, the feed
-- URL, the feed key (sealed at rest the way the Tech Admin AI keys are), the
-- staleness threshold that drives the banner, and the last pull's result
-- (empty until the pull exists).
--
-- market_index_history gains the value's own as-of date, the source URL it
-- came from and when it was fetched, so a value for one index, day and source
-- is stored once and an import of the same file twice changes nothing.

CREATE TABLE IF NOT EXISTS market_index_source (
    singleton       BOOLEAN PRIMARY KEY DEFAULT TRUE CHECK (singleton),
    source          TEXT NOT NULL DEFAULT 'manual'
                    CHECK (source IN ('manual', 'fb_connect_feed')),
    feed_url        TEXT NOT NULL DEFAULT '',
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
