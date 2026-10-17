-- SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
-- SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

-- Migration: 131_pro_customer_quotes_and_selections
-- Description: the contractor's own quote to their customer (a dealer's
-- customer quoting a homeowner or owner from the dealer's products) and the
-- selections loop around it: a capability link with no account, a sign off,
-- the contractor's confirmation into a dealer order, and the dealer's
-- acceptance.
--
-- Money is integer cents; a dealer unit cost is held in ten thousandths of a
-- currency unit (the engine's own scale), the extended cost in cents.
-- Markups are basis points. The rounding rule is written once, in
-- internal/customerquote/money.go.
--
-- Price walls (rulings 5a, 5b, 5c): everything about the contractor's markup,
-- labour, overhead and the customer's sell price lives in the pro_customer_*
-- tables, which no dealer surface reads except through the shared project
-- columns (the project name, job site, customer and site contact, selection
-- status and the dealer's own cost). Only the share token's SHA-256 hash is
-- stored, never the token. The approval log refuses UPDATE, DELETE and
-- TRUNCATE in the database.
--
-- Rollback lives at migrations/down/131_pro_customer_quotes_and_selections_down.sql
-- (outside the migrator's glob, applied by hand).

CREATE SEQUENCE IF NOT EXISTS pro_customer_quote_seq;

-- The contractor's own defaults and mark, one row per contractor account.
CREATE TABLE IF NOT EXISTS pro_contractor_settings (
    customer_id       UUID        PRIMARY KEY REFERENCES customers (id),
    display_name      TEXT        NOT NULL DEFAULT '' CHECK (char_length(display_name) <= 200),
    logo              BYTEA       NULL,
    logo_content_type TEXT        NULL CHECK (logo_content_type IN ('image/png', 'image/jpeg')),
    payment_terms     TEXT        NOT NULL DEFAULT '' CHECK (char_length(payment_terms) <= 1000),
    deposit_bps       INTEGER     NOT NULL DEFAULT 0 CHECK (deposit_bps BETWEEN 0 AND 10000),
    tax_mode          TEXT        NOT NULL DEFAULT 'at_checkout' CHECK (tax_mode IN ('site_rate', 'included', 'at_checkout')),
    tax_rate_bps      INTEGER     NOT NULL DEFAULT 0 CHECK (tax_rate_bps BETWEEN 0 AND 5000),
    markup_kind       TEXT        NOT NULL DEFAULT 'markup' CHECK (markup_kind IN ('markup', 'margin')),
    markup_bps        INTEGER     NOT NULL DEFAULT 0 CHECK (markup_bps BETWEEN 0 AND 1000000),
    require_code      BOOLEAN     NOT NULL DEFAULT FALSE,
    updated_at        TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    CHECK ((logo IS NULL) = (logo_content_type IS NULL)),
    CHECK (markup_kind = 'markup' OR markup_bps < 10000)
);

CREATE TABLE IF NOT EXISTS pro_customer_quotes (
    id                    UUID        PRIMARY KEY DEFAULT uuid_generate_v4(),
    customer_id           UUID        NOT NULL REFERENCES customers (id),
    reference             TEXT        NOT NULL UNIQUE,
    title                 TEXT        NOT NULL CHECK (char_length(title) BETWEEN 1 AND 200),
    project_id            UUID        NULL REFERENCES projects (id),
    source_quote_id       UUID        NULL REFERENCES quotes (id),
    job_site_address      TEXT        NOT NULL DEFAULT '' CHECK (char_length(job_site_address) <= 500),
    end_customer_name     TEXT        NOT NULL CHECK (char_length(end_customer_name) BETWEEN 1 AND 200),
    end_customer_email    TEXT        NOT NULL DEFAULT '' CHECK (char_length(end_customer_email) <= 254),
    end_customer_phone    TEXT        NOT NULL DEFAULT '' CHECK (char_length(end_customer_phone) <= 40),
    site_contact_name     TEXT        NOT NULL DEFAULT '' CHECK (char_length(site_contact_name) <= 200),
    site_contact_phone    TEXT        NOT NULL DEFAULT '' CHECK (char_length(site_contact_phone) <= 40),
    status                TEXT        NOT NULL DEFAULT 'draft'
                              CHECK (status IN ('draft', 'sent', 'approved', 'declined', 'confirmed', 'withdrawn')),
    -- tier three of the markup: the quote default
    markup_kind           TEXT        NOT NULL DEFAULT 'markup' CHECK (markup_kind IN ('markup', 'margin')),
    markup_bps            INTEGER     NOT NULL DEFAULT 0 CHECK (markup_bps BETWEEN 0 AND 1000000),
    hide_line_prices      BOOLEAN     NOT NULL DEFAULT FALSE,
    tax_mode              TEXT        NOT NULL DEFAULT 'at_checkout' CHECK (tax_mode IN ('site_rate', 'included', 'at_checkout')),
    tax_rate_bps          INTEGER     NOT NULL DEFAULT 0 CHECK (tax_rate_bps BETWEEN 0 AND 5000),
    payment_terms         TEXT        NOT NULL DEFAULT '' CHECK (char_length(payment_terms) <= 1000),
    deposit_bps           INTEGER     NOT NULL DEFAULT 0 CHECK (deposit_bps BETWEEN 0 AND 10000),
    customer_note         TEXT        NOT NULL DEFAULT '' CHECK (char_length(customer_note) <= 4000),
    require_code          BOOLEAN     NOT NULL DEFAULT FALSE,
    valid_until           TIMESTAMPTZ NOT NULL,
    revision              BIGINT      NOT NULL DEFAULT 1,
    sent_at               TIMESTAMPTZ NULL,
    approved_at           TIMESTAMPTZ NULL,
    declined_at           TIMESTAMPTZ NULL,
    confirmed_at          TIMESTAMPTZ NULL,
    confirmed_by          UUID        NULL,
    dealer_accepted_at    TIMESTAMPTZ NULL,
    order_id              UUID        NULL REFERENCES orders (id),
    created_by            UUID        NULL,
    created_at            TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at            TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    CHECK (markup_kind = 'markup' OR markup_bps < 10000)
);

CREATE INDEX IF NOT EXISTS pro_customer_quotes_customer_idx ON pro_customer_quotes (customer_id, created_at DESC, id DESC);
CREATE INDEX IF NOT EXISTS pro_customer_quotes_project_idx ON pro_customer_quotes (project_id) WHERE project_id IS NOT NULL;
CREATE INDEX IF NOT EXISTS pro_customer_quotes_order_idx ON pro_customer_quotes (order_id) WHERE order_id IS NOT NULL;

-- A choose-one-of-several set of lines.
CREATE TABLE IF NOT EXISTS pro_customer_quote_option_groups (
    id        UUID    PRIMARY KEY DEFAULT uuid_generate_v4(),
    quote_id  UUID    NOT NULL REFERENCES pro_customer_quotes (id) ON DELETE CASCADE,
    label     TEXT    NOT NULL CHECK (char_length(label) BETWEEN 1 AND 200),
    position  INTEGER NOT NULL
);

CREATE INDEX IF NOT EXISTS pro_customer_quote_option_groups_quote_idx ON pro_customer_quote_option_groups (quote_id, position);

-- A material line from the dealer's catalog. cost_cents is the dealer's price
-- to the contractor captured at pricing time; the tier that applied and the
-- resulting sell price are stored with the line so a read never recomputes
-- history.
CREATE TABLE IF NOT EXISTS pro_customer_quote_lines (
    id                        UUID          PRIMARY KEY DEFAULT uuid_generate_v4(),
    quote_id                  UUID          NOT NULL REFERENCES pro_customer_quotes (id) ON DELETE CASCADE,
    position                  INTEGER       NOT NULL,
    product_id                UUID          NOT NULL REFERENCES products (id),
    category_id               UUID          NULL REFERENCES product_categories (id),
    sku                       TEXT          NOT NULL DEFAULT '',
    description               TEXT          NOT NULL CHECK (char_length(description) BETWEEN 1 AND 500),
    uom                       TEXT          NOT NULL REFERENCES units (code),
    quantity                  NUMERIC(19,4) NOT NULL CHECK (quantity > 0),
    unit_cost_ten_thousandths BIGINT        NOT NULL CHECK (unit_cost_ten_thousandths >= 0),
    cost_cents                BIGINT        NOT NULL CHECK (cost_cents >= 0),
    -- where the cost came from: the dealer's catalog price for this account, or
    -- a line of the dealer quote the contractor was given (source_quote_id)
    price_basis               TEXT          NOT NULL DEFAULT 'catalog' CHECK (price_basis IN ('catalog', 'dealer_quote')),
    priced_at                 TIMESTAMPTZ   NOT NULL DEFAULT NOW(),
    option_group_id           UUID          NULL REFERENCES pro_customer_quote_option_groups (id) ON DELETE SET NULL,
    -- tier one: the line's own override, both or neither
    override_kind             TEXT          NULL CHECK (override_kind IN ('markup', 'margin')),
    override_bps              INTEGER       NULL CHECK (override_bps BETWEEN 0 AND 1000000),
    -- what applied, frozen: line, category or quote
    applied_tier              TEXT          NOT NULL DEFAULT 'quote' CHECK (applied_tier IN ('line', 'category', 'quote')),
    applied_kind              TEXT          NOT NULL DEFAULT 'markup' CHECK (applied_kind IN ('markup', 'margin')),
    applied_bps               INTEGER       NOT NULL DEFAULT 0,
    sell_cents                BIGINT        NOT NULL DEFAULT 0 CHECK (sell_cents >= 0),
    note                      TEXT          NOT NULL DEFAULT '' CHECK (char_length(note) <= 1000),
    CHECK ((override_kind IS NULL) = (override_bps IS NULL)),
    CHECK (override_kind IS DISTINCT FROM 'margin' OR override_bps < 10000)
);

CREATE INDEX IF NOT EXISTS pro_customer_quote_lines_quote_idx ON pro_customer_quote_lines (quote_id, position);

-- Tier two of the markup: a rule per product category on one quote.
CREATE TABLE IF NOT EXISTS pro_customer_quote_category_rules (
    quote_id    UUID    NOT NULL REFERENCES pro_customer_quotes (id) ON DELETE CASCADE,
    category_id UUID    NOT NULL REFERENCES product_categories (id),
    kind        TEXT    NOT NULL CHECK (kind IN ('markup', 'margin')),
    bps         INTEGER NOT NULL CHECK (bps BETWEEN 0 AND 1000000),
    PRIMARY KEY (quote_id, category_id),
    CHECK (kind = 'markup' OR bps < 10000)
);

CREATE TABLE IF NOT EXISTS pro_customer_quote_labour_lines (
    id              UUID          PRIMARY KEY DEFAULT uuid_generate_v4(),
    quote_id        UUID          NOT NULL REFERENCES pro_customer_quotes (id) ON DELETE CASCADE,
    position        INTEGER       NOT NULL,
    description     TEXT          NOT NULL CHECK (char_length(description) BETWEEN 1 AND 500),
    rate_type       TEXT          NOT NULL CHECK (rate_type IN ('flat', 'hourly')),
    rate_cents      BIGINT        NOT NULL CHECK (rate_cents >= 0),
    hours           NUMERIC(19,4) NULL CHECK (hours IS NULL OR hours > 0),
    contractor_only BOOLEAN       NOT NULL DEFAULT FALSE,
    CHECK ((rate_type = 'hourly') = (hours IS NOT NULL))
);

CREATE INDEX IF NOT EXISTS pro_customer_quote_labour_lines_quote_idx ON pro_customer_quote_labour_lines (quote_id, position);

CREATE TABLE IF NOT EXISTS pro_customer_quote_overhead_lines (
    id              UUID    PRIMARY KEY DEFAULT uuid_generate_v4(),
    quote_id        UUID    NOT NULL REFERENCES pro_customer_quotes (id) ON DELETE CASCADE,
    position        INTEGER NOT NULL,
    label           TEXT    NOT NULL CHECK (char_length(label) BETWEEN 1 AND 200),
    amount_type     TEXT    NOT NULL CHECK (amount_type IN ('flat', 'percent')),
    -- cents when flat, basis points when percent
    value           BIGINT  NOT NULL CHECK (value >= 0),
    contractor_only BOOLEAN NOT NULL DEFAULT FALSE,
    CHECK (amount_type = 'flat' OR value <= 1000000)
);

CREATE INDEX IF NOT EXISTS pro_customer_quote_overhead_lines_quote_idx ON pro_customer_quote_overhead_lines (quote_id, position);

-- The customer's decision on each material line.
CREATE TABLE IF NOT EXISTS pro_customer_quote_selections (
    quote_id   UUID        NOT NULL REFERENCES pro_customer_quotes (id) ON DELETE CASCADE,
    line_id    UUID        NOT NULL REFERENCES pro_customer_quote_lines (id) ON DELETE CASCADE,
    state      TEXT        NOT NULL DEFAULT 'pending' CHECK (state IN ('pending', 'approved', 'declined')),
    decided_at TIMESTAMPTZ NULL,
    PRIMARY KEY (quote_id, line_id)
);

-- The capability link. Only the SHA-256 hash of the 256 bit token is held.
CREATE TABLE IF NOT EXISTS pro_portal_share_tokens (
    id             UUID        PRIMARY KEY DEFAULT uuid_generate_v4(),
    quote_id       UUID        NOT NULL REFERENCES pro_customer_quotes (id) ON DELETE CASCADE,
    token_hash     TEXT        NOT NULL UNIQUE CHECK (token_hash ~ '^[0-9a-f]{64}$'),
    created_at     TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    expires_at     TIMESTAMPTZ NOT NULL,
    revoked_at     TIMESTAMPTZ NULL,
    revoked_reason TEXT        NULL CHECK (revoked_reason IN ('revoked', 'rotated')),
    first_viewed_at TIMESTAMPTZ NULL,
    last_viewed_at  TIMESTAMPTZ NULL,
    CHECK ((revoked_at IS NULL) = (revoked_reason IS NULL))
);

-- One live link per quote.
CREATE UNIQUE INDEX IF NOT EXISTS pro_portal_share_tokens_live_idx
    ON pro_portal_share_tokens (quote_id) WHERE revoked_at IS NULL;

-- The six digit code that may gate acceptance: hashed with a per row salt,
-- single use, short lived, attempt limited.
CREATE TABLE IF NOT EXISTS pro_portal_verification_codes (
    id           UUID        PRIMARY KEY DEFAULT uuid_generate_v4(),
    quote_id     UUID        NOT NULL REFERENCES pro_customer_quotes (id) ON DELETE CASCADE,
    token_id     UUID        NOT NULL REFERENCES pro_portal_share_tokens (id) ON DELETE CASCADE,
    channel      TEXT        NOT NULL CHECK (channel IN ('email', 'sms')),
    destination  TEXT        NOT NULL,
    salt         TEXT        NOT NULL,
    code_hash    TEXT        NOT NULL CHECK (code_hash ~ '^[0-9a-f]{64}$'),
    attempts     INTEGER     NOT NULL DEFAULT 0 CHECK (attempts >= 0),
    max_attempts INTEGER     NOT NULL DEFAULT 5 CHECK (max_attempts > 0),
    created_at   TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    expires_at   TIMESTAMPTZ NOT NULL,
    consumed_at  TIMESTAMPTZ NULL
);

CREATE INDEX IF NOT EXISTS pro_portal_verification_codes_token_idx ON pro_portal_verification_codes (token_id, created_at DESC);

-- The approval record: every step, who and when. Append only, enforced here.
CREATE TABLE IF NOT EXISTS pro_customer_quote_approval_log (
    id              UUID        PRIMARY KEY DEFAULT uuid_generate_v4(),
    -- the order the steps were taken in, whatever the clocks say
    seq             BIGINT      GENERATED ALWAYS AS IDENTITY,
    quote_id        UUID        NOT NULL REFERENCES pro_customer_quotes (id) ON DELETE RESTRICT,
    quote_revision  BIGINT      NOT NULL,
    step            TEXT        NOT NULL CHECK (step IN ('customer_approved', 'customer_declined', 'contractor_confirmed', 'dealer_accepted')),
    actor_kind      TEXT        NOT NULL CHECK (actor_kind IN ('customer', 'contractor', 'dealer')),
    actor_name      TEXT        NOT NULL,
    actor_contact   TEXT        NOT NULL DEFAULT '',
    actor_ref       TEXT        NOT NULL DEFAULT '',
    occurred_at     TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    token_hash      TEXT        NULL,
    signature_kind  TEXT        NULL CHECK (signature_kind IN ('typed', 'drawn')),
    signature_data  TEXT        NULL,
    source_address  TEXT        NOT NULL DEFAULT '',
    user_agent      TEXT        NOT NULL DEFAULT '',
    code_verified   BOOLEAN     NOT NULL DEFAULT FALSE,
    snapshot        TEXT        NULL,
    snapshot_sha256 TEXT        NULL CHECK (snapshot_sha256 IS NULL OR snapshot_sha256 ~ '^[0-9a-f]{64}$'),
    total_cents     BIGINT      NULL,
    note            TEXT        NOT NULL DEFAULT ''
);

CREATE INDEX IF NOT EXISTS pro_customer_quote_approval_log_quote_idx ON pro_customer_quote_approval_log (quote_id, seq);

-- One customer decision, one contractor confirmation and one dealer
-- acceptance per revision of a quote.
CREATE UNIQUE INDEX IF NOT EXISTS pro_customer_quote_approval_log_step_once_idx
    ON pro_customer_quote_approval_log (quote_id, quote_revision, step);

CREATE OR REPLACE FUNCTION pro_customer_quote_approval_log_refuse() RETURNS trigger AS $$
BEGIN
    RAISE EXCEPTION 'pro_customer_quote_approval_log is append only: % is refused', TG_OP
        USING ERRCODE = 'integrity_constraint_violation';
END;
$$ LANGUAGE plpgsql;

DROP TRIGGER IF EXISTS pro_customer_quote_approval_log_no_change ON pro_customer_quote_approval_log;
CREATE TRIGGER pro_customer_quote_approval_log_no_change
    BEFORE UPDATE OR DELETE ON pro_customer_quote_approval_log
    FOR EACH ROW EXECUTE FUNCTION pro_customer_quote_approval_log_refuse();

DROP TRIGGER IF EXISTS pro_customer_quote_approval_log_no_truncate ON pro_customer_quote_approval_log;
CREATE TRIGGER pro_customer_quote_approval_log_no_truncate
    BEFORE TRUNCATE ON pro_customer_quote_approval_log
    FOR EACH STATEMENT EXECUTE FUNCTION pro_customer_quote_approval_log_refuse();
