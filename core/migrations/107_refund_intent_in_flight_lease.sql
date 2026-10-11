-- SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
-- SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

-- 107: the refund claim's in flight lease (C2-5, the r14 review's P1-1). A
-- claim is a tender held while its act's terminal refund runs; the resolve
-- route and the drawer close must be able to tell a live claim (its call is
-- on the wire, the terminal may still move the tender's money) from a
-- stranded one (its act ended: a lost verdict, a crash, a refused
-- transaction). Three columns give the claim that record:
--
--   answer_due_at  the lease: the claim stays unresolvable until this
--                  instant even if nothing else settles it, so a crashed or
--                  hung act can never hold a drawer forever. Written by the
--                  insert as now plus the service's stated in flight timeout
--                  (90 seconds); the default carries rows that predate the
--                  column, whose lease then expires from the migration on.
--   answered_at    stamped by every path that ends the claim's own terminal
--                  call: the refund id's committed write, the decline
--                  release, and the unknown outcome record.
--   answer_state   IN_FLIGHT until the call answers, ANSWERED when it has,
--                  UNKNOWN when a resolution proceeds on the lease expiring
--                  with no answer at all (the terminal never spoke).
--
-- The resolve and the close read these: a claim with no answer inside its
-- lease answers 409 refund_in_flight, so a tender whose money has not
-- answered cannot be released and then refunded twice.

ALTER TABLE pos_refund_intents
    ADD COLUMN IF NOT EXISTS answer_due_at TIMESTAMPTZ NOT NULL DEFAULT (NOW() + INTERVAL '90 seconds'),
    ADD COLUMN IF NOT EXISTS answered_at TIMESTAMPTZ,
    ADD COLUMN IF NOT EXISTS answer_state TEXT NOT NULL DEFAULT 'IN_FLIGHT';
