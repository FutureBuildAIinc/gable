-- SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
-- SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

-- 111: one payback per tender, made structural (C2-5, the sixteenth
-- review's P1-1). The payback route opens its claim in a transaction that
-- locks the tender's payment row and rechecks the credit under that lock,
-- so a contender whose earlier read saw the credit standing cannot open a
-- second claim beside the first's booking; this index is the structure
-- behind that recheck: a tender holds at most one payback claim in the
-- states that mean money (PENDING, the claim whose terminal call is on the
-- wire, and DONE, the claim whose credit was booked). A FAILED payback
-- claim (the terminal's own decline, or a recorded no refund) stays outside
-- the index, so the counter may ask again for a credit the terminal
-- refused; and the void's and the return's claims never enter it, their own
-- (sale, tender) index governing them.

CREATE UNIQUE INDEX IF NOT EXISTS uq_pos_refund_intents_payback_tender
    ON pos_refund_intents (tender_id) WHERE act = 'payback' AND state IN ('PENDING', 'DONE');
