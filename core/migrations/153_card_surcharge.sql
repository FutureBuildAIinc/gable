-- SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
-- SPDX-FileCopyrightText: 2026, FutureBuild, Inc. and OpenLBM contributors

-- 153: card surcharges (CP-15).
--
-- The processor decides who is surcharged and says so in its answer; Gable
-- keeps the branch's switch (off by default, a rate from 0 to 3 percent in
-- basis points, the card brands' cap) and books the fee the answer carries.
--
--   locations.card_surcharge_enabled / card_surcharge_bps   the branch's rule
--   payment_terminal_intents.answer_fee_cents               the device's fee
--   pos_tenders.surcharge_cents                             the fee on the tender
--   pos_refund_intents.fee_cents                            the fee a card refund returns
--   gl_accounts 4070 Card Surcharge Income                  where the fee books
--
-- The fee is never part of the sale: its total, tax and sales revenue do not
-- move. Every step is guarded so a second apply changes nothing.

ALTER TABLE locations
    ADD COLUMN IF NOT EXISTS card_surcharge_enabled BOOLEAN NOT NULL DEFAULT FALSE,
    ADD COLUMN IF NOT EXISTS card_surcharge_bps INTEGER NOT NULL DEFAULT 0;

ALTER TABLE payment_terminal_intents
    ADD COLUMN IF NOT EXISTS answer_fee_cents BIGINT NOT NULL DEFAULT 0;
ALTER TABLE pos_tenders
    ADD COLUMN IF NOT EXISTS surcharge_cents BIGINT NOT NULL DEFAULT 0;
ALTER TABLE pos_refund_intents
    ADD COLUMN IF NOT EXISTS fee_cents BIGINT NOT NULL DEFAULT 0;

DO $$
BEGIN
    IF NOT EXISTS (SELECT 1 FROM pg_constraint WHERE conname = 'locations_card_surcharge_bps_check') THEN
        ALTER TABLE locations
            ADD CONSTRAINT locations_card_surcharge_bps_check
            CHECK (card_surcharge_bps BETWEEN 0 AND 300);
    END IF;
    IF NOT EXISTS (SELECT 1 FROM pg_constraint WHERE conname = 'payment_terminal_intents_answer_fee_check') THEN
        ALTER TABLE payment_terminal_intents
            ADD CONSTRAINT payment_terminal_intents_answer_fee_check
            CHECK (answer_fee_cents >= 0);
    END IF;
    IF NOT EXISTS (SELECT 1 FROM pg_constraint WHERE conname = 'pos_refund_intents_fee_check') THEN
        ALTER TABLE pos_refund_intents
            ADD CONSTRAINT pos_refund_intents_fee_check
            CHECK (fee_cents >= 0);
    END IF;
    IF NOT EXISTS (SELECT 1 FROM pg_constraint WHERE conname = 'pos_tenders_surcharge_check') THEN
        ALTER TABLE pos_tenders
            ADD CONSTRAINT pos_tenders_surcharge_check
            CHECK (surcharge_cents >= 0);
    END IF;
END $$;

INSERT INTO gl_accounts (code, name, type, subtype, normal_balance, description) VALUES
    ('4070', 'Card Surcharge Income', 'REVENUE', 'Operating', 'CREDIT',
     'The card fee the processor added on top of a sale: never sales revenue, never taxed')
ON CONFLICT (code) DO NOTHING;
