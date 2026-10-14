-- SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
-- SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

-- Down of 110_refund_intent_payback: the payback claims are this item's own
-- claims, not valid old shape data, so they go first (their refunds live on
-- in payment_refunds and the audit log, which stay), then the act
-- vocabulary narrows back. A dealer who paid credits back and then runs the
-- down loses the claim rows only, never a booked refund.

DELETE FROM pos_refund_intents WHERE act = 'payback';
ALTER TABLE pos_refund_intents DROP CONSTRAINT IF EXISTS pos_refund_intents_act_check;
ALTER TABLE pos_refund_intents
    ADD CONSTRAINT pos_refund_intents_act_check CHECK (act IN ('void', 'return'));
