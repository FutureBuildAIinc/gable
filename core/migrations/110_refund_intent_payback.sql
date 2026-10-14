-- SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
-- SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

-- 110: the payback claim (C2-5, FROM-PRINCIPAL 60b). The counter's payback
-- route pays a declined void tender's credit back to its card through the
-- terminal rail, and its terminal call runs under the same claim discipline
-- every terminal refund runs under: one live (PENDING) claim per tender and
-- the in flight lease, so a hung call can never be paid twice. The intents
-- table's act vocabulary gains 'payback' for those claims: a claim whose act
-- is the payback itself, opened beside the void's own released claim on the
-- same tender (which stays FAILED: it is the void's own record).

ALTER TABLE pos_refund_intents DROP CONSTRAINT IF EXISTS pos_refund_intents_act_check;
ALTER TABLE pos_refund_intents
    ADD CONSTRAINT pos_refund_intents_act_check CHECK (act IN ('void', 'return', 'payback'));
