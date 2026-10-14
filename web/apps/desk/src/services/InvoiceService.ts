// SPDX-License-Identifier: LicenseRef-OpenLBM-Surface-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

// The invoice module on the wire contract (ADR 0001; ADR 0005 section 6): the
// cursor list envelope, the transitions route (a void takes the revision and a
// reason), integer cents money and the one error envelope.

import type { Invoice, InvoicePage, InvoiceStatus, InvoiceSummary } from '../types/invoice.ts';
import { fetchWithAuth } from './fetchClient';
import { parseApiError, ifMatch } from './apiError';

const API_URL = import.meta.env.VITE_API_URL || '';

export interface ListInvoicesParams {
    status?: InvoiceStatus | InvoiceStatus[];
    customerId?: string;
    jobId?: string;
    shipToId?: string;
    orderId?: string;
    overdue?: boolean;
    limit?: number;
    cursor?: string | null;
    includeTotal?: boolean;
}

export function buildInvoiceQuery(params: ListInvoicesParams = {}): string {
    const q = new URLSearchParams();
    const statuses = Array.isArray(params.status) ? params.status : params.status ? [params.status] : [];
    if (statuses.length > 0) q.set('status', statuses.join(','));
    if (params.customerId) q.set('customer_id', params.customerId);
    if (params.jobId) q.set('job_id', params.jobId);
    if (params.shipToId) q.set('ship_to_id', params.shipToId);
    if (params.orderId) q.set('order_id', params.orderId);
    if (params.overdue !== undefined) q.set('overdue', String(params.overdue));
    if (params.limit !== undefined) q.set('limit', String(params.limit));
    if (params.cursor) q.set('cursor', params.cursor);
    if (params.includeTotal) q.set('include', 'total');
    return q.toString();
}

async function expectOk(response: Response, fallback: string): Promise<Response> {
    if (response.ok) return response;
    throw await parseApiError(response, fallback);
}

export const InvoiceService = {
    /** One page of invoices, newest first; the cursor walks forward. */
    async listInvoices(params: ListInvoicesParams = {}): Promise<InvoicePage> {
        const query = buildInvoiceQuery(params);
        const response = await expectOk(
            await fetchWithAuth(`${API_URL}/api/v1/invoices${query ? `?${query}` : ''}`),
            'Failed to fetch invoices',
        );
        return response.json();
    },

    /** Every invoice of a filter, walking the cursor. */
    async allInvoices(params: Omit<ListInvoicesParams, 'cursor'> = {}): Promise<InvoiceSummary[]> {
        const out: InvoiceSummary[] = [];
        let cursor: string | null = null;
        do {
            const page: InvoicePage = await this.listInvoices({ ...params, cursor, limit: params.limit ?? 200 });
            out.push(...page.items);
            cursor = page.next_cursor;
        } while (cursor);
        return out;
    },

    async getInvoice(id: string): Promise<Invoice> {
        const response = await expectOk(
            await fetchWithAuth(`${API_URL}/api/v1/invoices/${id}`),
            'Failed to fetch invoice',
        );
        return response.json();
    },

    /**
     * Voids an unpaid invoice on the client's revision (roles admin, owner,
     * finance). A payment or an applied credit memo refuses it with a 409 whose
     * blocker code (has_applications, has_credit_memos) is in the error details.
     */
    async voidInvoice(id: string, revision: number, reason: string): Promise<Invoice> {
        const response = await expectOk(
            await fetchWithAuth(`${API_URL}/api/v1/invoices/${id}/transitions`, {
                method: 'POST',
                headers: { 'Content-Type': 'application/json', 'If-Match': ifMatch(revision) },
                body: JSON.stringify({ to: 'void', revision, reason }),
            }),
            'Failed to void invoice',
        );
        return response.json();
    },

    async emailInvoice(id: string): Promise<void> {
        await expectOk(
            await fetchWithAuth(`${API_URL}/api/v1/invoices/${id}/email`, { method: 'POST' }),
            'Failed to email invoice',
        );
    },
};
