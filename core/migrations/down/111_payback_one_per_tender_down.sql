-- SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
-- SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

-- Down of 111_payback_one_per_tender: the index was this item's own rule
-- (one payback per tender), not a shape older rows depend on, and the claim
-- rows themselves survive untouched with their refunds, so the down drops
-- only the index and applies again cleanly.

DROP INDEX IF EXISTS uq_pos_refund_intents_payback_tender;
