-- SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
-- SPDX-FileCopyrightText: 2026, FutureBuild, Inc. and OpenLBM contributors

-- 139: large cash capture, classification and aggregation (CP-17a, ADR 0005
-- sections 11 and 14.2 C2-5). The branch's country picks the rule family:
-- the full United States Form 8300 duty (capture, classify, aggregate) and,
-- for a Canadian branch, an optional dealer threshold that records the payer
-- for the dealer's own records and never produces a FINTRAC report.
--
--   1  locations.country_code: the branch's country, two letters. A branch
--      with no country and no dealer default (system_settings
--      country.default) is a configuration error the branch screen refuses.
--   2  products.consumer_durable: the Form 8300 designated reporting
--      transaction flag; a lumber yard's default is false.
--   3  large_cash_payers: the payer identity captured at the counter. The
--      taxpayer identification number and the ID number are sealed with the
--      payment vault (AES-256-GCM, PAYMENT_VAULT_KEY); tin_hash is a
--      SHA-256 digest that keys walk-in aggregation without opening the
--      sealed value.
--   4  large_cash_tenders: every tender that counted toward a rule, with the
--      classifier's recorded reason, the audit a later dispute reads.
--   5  large_cash_events: one row per threshold crossing, written inside the
--      completion's transaction. State reportable is the United States duty;
--      recorded is the Canadian dealer policy.
--
-- Every step is guarded so a second apply changes nothing.

-- 1. The branch's country.

ALTER TABLE locations ADD COLUMN IF NOT EXISTS country_code TEXT;
DO $$ BEGIN
    ALTER TABLE locations ADD CONSTRAINT locations_country_code_shape
        CHECK (country_code IS NULL OR char_length(country_code) = 2);
EXCEPTION WHEN duplicate_object OR duplicate_table THEN NULL;
END $$;

-- 2. The consumer durable flag.

ALTER TABLE products ADD COLUMN IF NOT EXISTS consumer_durable BOOLEAN NOT NULL DEFAULT FALSE;

-- 3. The payer capture.

CREATE TABLE IF NOT EXISTS large_cash_payers (
    id                UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    transaction_id    UUID NOT NULL REFERENCES pos_transactions (id) ON DELETE CASCADE,
    branch_id         UUID NOT NULL REFERENCES locations (id),
    customer_id       UUID NULL REFERENCES customers (id),
    country           TEXT NOT NULL,
    name              TEXT NOT NULL,
    address           TEXT NOT NULL,
    occupation        TEXT NOT NULL,
    date_of_birth     DATE NULL,
    id_type           TEXT NOT NULL DEFAULT '',
    id_number_sealed  TEXT NOT NULL DEFAULT '',
    tin_sealed        TEXT NOT NULL DEFAULT '',
    tin_hash          TEXT NOT NULL DEFAULT '',
    third_party_payer BOOLEAN NOT NULL DEFAULT FALSE,
    created_at        TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    CONSTRAINT large_cash_payers_one_per_sale UNIQUE (transaction_id),
    CONSTRAINT large_cash_payers_named CHECK (name <> '' AND address <> '' AND occupation <> ''),
    CONSTRAINT large_cash_payers_us_fields CHECK (country <> 'US' OR (tin_sealed <> '' AND tin_hash <> ''
        AND id_number_sealed <> '' AND id_type <> '' AND date_of_birth IS NOT NULL)),
    CONSTRAINT large_cash_payers_ca_fields CHECK (country <> 'CA' OR (id_type <> '' AND date_of_birth IS NOT NULL))
);

-- 4. The counted tenders, with the reason each counted.

CREATE TABLE IF NOT EXISTS large_cash_tenders (
    id             UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    transaction_id UUID NOT NULL REFERENCES pos_transactions (id) ON DELETE CASCADE,
    branch_id      UUID NOT NULL REFERENCES locations (id),
    rule_family    TEXT NOT NULL CHECK (rule_family IN ('US_8300', 'CA_DEALER')),
    payer_key      TEXT NOT NULL,
    counted_cents  BIGINT NOT NULL CHECK (counted_cents > 0),
    instrument     TEXT NOT NULL DEFAULT 'currency',
    reason         TEXT NOT NULL,
    tender_index   INT NOT NULL,
    created_at     TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    CONSTRAINT large_cash_tenders_one_per_tender UNIQUE (transaction_id, rule_family, tender_index)
);

CREATE INDEX IF NOT EXISTS idx_large_cash_tenders_payer_window
    ON large_cash_tenders (payer_key, rule_family, created_at);

-- 5. The crossings.

CREATE TABLE IF NOT EXISTS large_cash_events (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    payer_key       TEXT NOT NULL,
    customer_id     UUID NULL REFERENCES customers (id),
    branch_id       UUID NOT NULL REFERENCES locations (id),
    rule_family     TEXT NOT NULL CHECK (rule_family IN ('US_8300', 'CA_DEALER')),
    currency        TEXT NOT NULL,
    amount_cents    BIGINT NOT NULL CHECK (amount_cents > 0),
    total_cents     BIGINT NOT NULL CHECK (total_cents > 0),
    transaction_ids UUID[] NOT NULL,
    tender_count    INT NOT NULL CHECK (tender_count > 0),
    state           TEXT NOT NULL CHECK (state IN ('reportable', 'recorded')),
    created_at      TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS idx_large_cash_events_payer
    ON large_cash_events (payer_key, rule_family, created_at);
CREATE INDEX IF NOT EXISTS idx_large_cash_events_state
    ON large_cash_events (state, created_at);
