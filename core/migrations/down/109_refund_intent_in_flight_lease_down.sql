-- SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
-- SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

-- Down of 107_refund_intent_in_flight_lease: drops the lease columns. The
-- up wrote no data beyond the columns themselves (every existing row's
-- answer_state stays IN_FLIGHT and every lease expiry is recomputed by the
-- default on a re-apply), so nothing of a dealer's data maps back.

ALTER TABLE pos_refund_intents DROP COLUMN IF EXISTS answer_state;
ALTER TABLE pos_refund_intents DROP COLUMN IF EXISTS answered_at;
ALTER TABLE pos_refund_intents DROP COLUMN IF EXISTS answer_due_at;
