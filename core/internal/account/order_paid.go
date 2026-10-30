// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package account

// OrderInvoicePaidCentsSQL is the one rule for what an order's live invoices
// have been paid by something other than the order's own deposits, in cents,
// for the order whose id is $1. The deposit cap the payments route holds and
// the balance due after deposits the order answer shows both subtract it, so
// the two cannot disagree.
//
// It sums every live application of cash or a credit memo to a live invoice
// of the order, whatever payment or memo made it, less the part of a deposit
// (a payment naming the order) that fulfilment applied after receipt: that
// part is already counted as a deposit. What a payment naming the order
// applied at receipt (act_id = the payment's id) is an invoice payment, not a
// deposit, so it stays in. Discounts and write offs are not money the
// customer paid and stay out.
const OrderInvoicePaidCentsSQL = `
	SELECT COALESCE(SUM(ROUND(a.amount * 100)::bigint), 0)
	FROM ar_applications a
	JOIN invoices i ON i.id = a.invoice_id
	WHERE i.order_id = $1 AND i.status <> 'VOID'
	  AND a.reversed_at IS NULL AND a.kind IN ('PAYMENT', 'CREDIT_MEMO')
	  AND NOT (a.kind = 'PAYMENT' AND a.act_id <> a.payment_id AND EXISTS (
	      SELECT 1 FROM payments p WHERE p.id = a.payment_id AND p.order_id = $1 AND p.status = 'POSTED'))`
