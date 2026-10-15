-- SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
-- SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

-- Migration: 120_collections_date_indexes
-- Description: the dashboard's collected figures (the card, yesterday, the
-- trend), the Daily Till and its Sales Summary cut a day on the payment's
-- business date, payments.received_on, and the refund's, payment_refunds.
-- refunded_on (pkg/collections). Neither column had an index, so each read was
-- a full scan of the table, growing with a dealer's history. Both indexes are
-- partial to the rows the reads count: payments that are POSTED (a voided
-- payment is never counted) and refunds that are COMPLETE.

CREATE INDEX IF NOT EXISTS idx_payments_received_on_posted
    ON payments (received_on)
    WHERE status = 'POSTED';

CREATE INDEX IF NOT EXISTS idx_payment_refunds_refunded_on_complete
    ON payment_refunds (refunded_on)
    WHERE status = 'COMPLETE';
