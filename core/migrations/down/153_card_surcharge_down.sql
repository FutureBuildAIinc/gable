-- SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
-- SPDX-FileCopyrightText: 2026, FutureBuild, Inc. and OpenLBM contributors

-- Rollback for 153_card_surcharge.sql.
--
-- Lives in migrations/down/ so cmd/migrate's `migrations/*.sql` glob cannot
-- pick it up and apply it as a forward migration. Apply by hand.
--
-- The surcharge income account goes only while no journal line uses it: an
-- entry that carries a surcharge leg has to be reversed before it can go.

ALTER TABLE pos_refund_intents DROP CONSTRAINT IF EXISTS pos_refund_intents_fee_check;
ALTER TABLE pos_refund_intents DROP COLUMN IF EXISTS fee_cents;
ALTER TABLE pos_tenders DROP CONSTRAINT IF EXISTS pos_tenders_surcharge_check;
ALTER TABLE pos_tenders DROP COLUMN IF EXISTS surcharge_cents;
ALTER TABLE payment_terminal_intents DROP CONSTRAINT IF EXISTS payment_terminal_intents_answer_fee_check;
ALTER TABLE payment_terminal_intents DROP COLUMN IF EXISTS answer_fee_cents;
ALTER TABLE locations DROP CONSTRAINT IF EXISTS locations_card_surcharge_bps_check;
ALTER TABLE locations DROP COLUMN IF EXISTS card_surcharge_bps;
ALTER TABLE locations DROP COLUMN IF EXISTS card_surcharge_enabled;

DELETE FROM gl_accounts a
WHERE a.code = '4070'
  AND NOT EXISTS (SELECT 1 FROM gl_journal_lines l WHERE l.account_id = a.id);
