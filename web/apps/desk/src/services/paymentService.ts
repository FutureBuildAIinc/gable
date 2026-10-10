// SPDX-License-Identifier: LicenseRef-OpenLBM-Surface-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

import type {
    Payment,
    PaymentApplication,
    PaymentApplicationPage,
    PaymentApplicationRequest,
    PaymentIntentRequest,
    PaymentIntentResponse,
    PaymentPage,
    ProcessCardPaymentRequest,
    CreatePaymentRequest,
    Refund,
    RefundRequest
} from '../types/payment';
import { walkCursor } from '../lib/cursorWalk';
import { fetchWithAuth } from './fetchClient';

const API_URL = import.meta.env.VITE_API_URL || '';

async function read<T>(response: Response, fallback: string): Promise<T> {
    if (!response.ok) {
        let message = fallback;
        try {
            const body = await response.json();
            message = body?.error?.message || fallback;
        } catch { /* the wire keeps its shape; a bodyless failure keeps the fallback */ }
        throw new Error(message);
    }
    return response.json() as Promise<T>;
}

export const paymentService = {
    // Cash, check, ACH or other: recorded against the customer, applied to the
    // invoices the request names in the same act (ADR 0005 9.4). Card payments
    // go through the gateway route below.
    createPayment: async (req: CreatePaymentRequest): Promise<Payment> => {
        const response = await fetchWithAuth(`${API_URL}/api/v1/payments`, {
            method: 'POST',
            headers: { 'Content-Type': 'application/json' },
            body: JSON.stringify(req),
        });
        return read(response, 'Failed to create payment');
    },

    // The payments of one customer, newest first; unapplied: true holds the
    // cash still sitting in deposits (2200) waiting to be applied.
    list: async (params: { customer_id?: string; unapplied?: boolean; status?: string; method?: string; cursor?: string } = {}): Promise<PaymentPage> => {
        const qs = new URLSearchParams();
        if (params.customer_id) qs.set('customer_id', params.customer_id);
        if (params.unapplied !== undefined) qs.set('unapplied', String(params.unapplied));
        if (params.status) qs.set('status', params.status);
        if (params.method) qs.set('method', params.method);
        if (params.cursor) qs.set('cursor', params.cursor);
        const response = await fetchWithAuth(`${API_URL}/api/v1/payments?${qs.toString()}`);
        return read(response, 'Failed to fetch payments');
    },

    // What settled an invoice: every application against it, payments, credit
    // memos, discounts and write offs, a reversed one still listed.
    history: async (invoiceId: string, cursor?: string): Promise<PaymentApplicationPage> => {
        const qs = cursor ? `?cursor=${encodeURIComponent(cursor)}` : '';
        const response = await fetchWithAuth(`${API_URL}/api/v1/invoices/${invoiceId}/payments${qs}`);
        return read(response, 'Failed to fetch payments');
    },

    // Every page of the history, so a long one is not cut at the first.
    historyAll: (invoiceId: string): Promise<PaymentApplication[]> => walkCursor((cursor) => paymentService.history(invoiceId, cursor)),

    // Applies a payment's unapplied cash to invoices. The revision is the
    // precondition (If-Match or the body's revision).
    apply: async (paymentId: string, applications: PaymentApplicationRequest[], revision: number): Promise<Payment> => {
        const response = await fetchWithAuth(`${API_URL}/api/v1/payments/${paymentId}/applications`, {
            method: 'POST',
            headers: { 'Content-Type': 'application/json', 'If-Match': `"${revision}"` },
            body: JSON.stringify({ applications }),
        });
        return read(response, 'Failed to apply the payment');
    },

    // Pays unapplied cash back out (DR 2200 / CR 1010).
    refund: async (paymentId: string, req: RefundRequest, revision: number): Promise<Refund> => {
        const response = await fetchWithAuth(`${API_URL}/api/v1/payments/${paymentId}/refunds`, {
            method: 'POST',
            headers: { 'Content-Type': 'application/json', 'If-Match': `"${revision}"` },
            body: JSON.stringify(req),
        });
        return read(response, 'Refund failed');
    },

    // Voids a posted payment (never a card one): every application reversed,
    // the receipt undone, the invoices reopened.
    void: async (paymentId: string, reason: string, revision: number): Promise<Payment> => {
        const response = await fetchWithAuth(`${API_URL}/api/v1/payments/${paymentId}/transitions`, {
            method: 'POST',
            headers: { 'Content-Type': 'application/json', 'If-Match': `"${revision}"` },
            body: JSON.stringify({ to: 'voided', reason }),
        });
        return read(response, 'Failed to void the payment');
    },

    // ---- Run Payments: the card payment flow ----

    /** Step 1: the intent answers the public key Runner.js tokenizes with. */
    createPaymentIntent: async (req: PaymentIntentRequest): Promise<PaymentIntentResponse> => {
        const response = await fetchWithAuth(`${API_URL}/api/v1/payments/intent`, {
            method: 'POST',
            headers: { 'Content-Type': 'application/json' },
            body: JSON.stringify(req),
        });
        return read(response, 'Payment gateway not available');
    },

    /** Step 2: the charge through Run Payments with the token from Runner.js. */
    processCardPayment: async (req: ProcessCardPaymentRequest): Promise<Payment> => {
        const response = await fetchWithAuth(`${API_URL}/api/v1/payments/card`, {
            method: 'POST',
            headers: { 'Content-Type': 'application/json' },
            body: JSON.stringify(req),
        });
        return read(response, 'Card payment failed');
    },
};
