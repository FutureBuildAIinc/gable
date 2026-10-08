// SPDX-License-Identifier: LicenseRef-OpenLBM-Surface-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

// The credit memo module on the wire contract (ADR 0005 section 6.3): the cursor
// list envelope, create and update of a draft, and the transitions route (post a
// draft to open, void a draft or an open memo). Every amount is a negative
// integer of cents.

import type {
    CreditMemo,
    CreditMemoPage,
    CreditMemoRequest,
    CreditMemoStatus,
    CreditMemoSummary,
} from '../types/creditMemo.ts';
import { fetchWithAuth } from './fetchClient';
import { parseApiError, ifMatch } from './apiError';

const API_URL = import.meta.env.VITE_API_URL || '';

export interface ListCreditMemosParams {
    status?: CreditMemoStatus | CreditMemoStatus[];
    customerId?: string;
    invoiceId?: string;
    jobId?: string;
    limit?: number;
    cursor?: string | null;
    includeTotal?: boolean;
}

export function buildCreditMemoQuery(params: ListCreditMemosParams = {}): string {
    const q = new URLSearchParams();
    const statuses = Array.isArray(params.status) ? params.status : params.status ? [params.status] : [];
    if (statuses.length > 0) q.set('status', statuses.join(','));
    if (params.customerId) q.set('customer_id', params.customerId);
    if (params.invoiceId) q.set('invoice_id', params.invoiceId);
    if (params.jobId) q.set('job_id', params.jobId);
    if (params.limit !== undefined) q.set('limit', String(params.limit));
    if (params.cursor) q.set('cursor', params.cursor);
    if (params.includeTotal) q.set('include', 'total');
    return q.toString();
}

async function expectOk(response: Response, fallback: string): Promise<Response> {
    if (response.ok) return response;
    throw await parseApiError(response, fallback);
}

const JSON_HEADERS = { 'Content-Type': 'application/json' };

export const CreditMemoService = {
    /** One page of credit memos, newest first; the cursor walks forward. */
    async listCreditMemos(params: ListCreditMemosParams = {}): Promise<CreditMemoPage> {
        const query = buildCreditMemoQuery(params);
        const response = await expectOk(
            await fetchWithAuth(`${API_URL}/api/v1/credit-memos${query ? `?${query}` : ''}`),
            'Failed to fetch credit memos',
        );
        return response.json();
    },

    /** Every credit memo of a filter, walking the cursor. */
    async allCreditMemos(params: Omit<ListCreditMemosParams, 'cursor'> = {}): Promise<CreditMemoSummary[]> {
        const out: CreditMemoSummary[] = [];
        let cursor: string | null = null;
        do {
            const page: CreditMemoPage = await this.listCreditMemos({ ...params, cursor, limit: params.limit ?? 200 });
            out.push(...page.items);
            cursor = page.next_cursor;
        } while (cursor);
        return out;
    },

    async getCreditMemo(id: string): Promise<CreditMemo> {
        const response = await expectOk(
            await fetchWithAuth(`${API_URL}/api/v1/credit-memos/${id}`),
            'Failed to fetch credit memo',
        );
        return response.json();
    },

    /** Creates a draft credit memo; nothing posts until it is posted. */
    async createCreditMemo(request: CreditMemoRequest): Promise<CreditMemo> {
        const response = await expectOk(
            await fetchWithAuth(`${API_URL}/api/v1/credit-memos`, {
                method: 'POST',
                headers: JSON_HEADERS,
                body: JSON.stringify(request),
            }),
            'Failed to create credit memo',
        );
        return response.json();
    },

    /** Replaces a draft's header and lines on the client's revision (409 credit_memo_not_draft otherwise). */
    async updateCreditMemo(id: string, revision: number, request: CreditMemoRequest): Promise<CreditMemo> {
        const response = await expectOk(
            await fetchWithAuth(`${API_URL}/api/v1/credit-memos/${id}`, {
                method: 'PUT',
                headers: { ...JSON_HEADERS, 'If-Match': ifMatch(revision) },
                body: JSON.stringify({ ...request, revision }),
            }),
            'Failed to update credit memo',
        );
        return response.json();
    },

    async transition(id: string, revision: number, to: CreditMemoStatus, reason?: string): Promise<CreditMemo> {
        const body: Record<string, unknown> = { to, revision };
        if (reason !== undefined) body.reason = reason;
        const response = await expectOk(
            await fetchWithAuth(`${API_URL}/api/v1/credit-memos/${id}/transitions`, {
                method: 'POST',
                headers: { ...JSON_HEADERS, 'If-Match': ifMatch(revision) },
                body: JSON.stringify(body),
            }),
            'Failed to change credit memo status',
        );
        return response.json();
    },

    /** Posts a draft: assigns the number, restocks the restock lines and posts the entry. */
    async post(id: string, revision: number): Promise<CreditMemo> {
        return this.transition(id, revision, 'open');
    },

    /** Voids a draft or an open memo; a reason is required. */
    async voidCreditMemo(id: string, revision: number, reason: string): Promise<CreditMemo> {
        return this.transition(id, revision, 'void', reason);
    },
};
