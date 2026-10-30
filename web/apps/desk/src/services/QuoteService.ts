// SPDX-License-Identifier: LicenseRef-OpenLBM-Surface-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

import type {
    Quote,
    QuoteAnalytics,
    QuotePage,
    QuoteRequest,
    QuoteRevision,
    QuoteRevisionPage,
    QuoteStatus,
    QuoteTransitionRequest,
} from '../types/quote';
import type { Order } from '../types/order';
import { fetchWithAuth } from './fetchClient';
import { ifMatch, parseApiError } from './apiError';

const API_URL = import.meta.env.VITE_API_URL || '';

export {
    ApiError as QuoteApiError,
    apiErrorMessage as quoteErrorMessage,
    parseApiError as parseQuoteError,
    ifMatch,
} from './apiError';
export type { ApiErrorDetail as QuoteErrorDetail } from './apiError';

export interface ListQuotesParams {
    /** Server side filter; sent as a comma separated lowercase list. */
    status?: QuoteStatus | QuoteStatus[];
    customerId?: string;
    limit?: number;
    cursor?: string | null;
    /** Ask for the total row count (include=total). */
    includeTotal?: boolean;
}

export function buildListQuery(params: ListQuotesParams = {}): string {
    const q = new URLSearchParams();
    const statuses = Array.isArray(params.status) ? params.status : params.status ? [params.status] : [];
    if (statuses.length > 0) q.set('status', statuses.join(','));
    if (params.customerId) q.set('customer_id', params.customerId);
    if (params.limit) q.set('limit', String(params.limit));
    if (params.cursor) q.set('cursor', params.cursor);
    if (params.includeTotal) q.set('include', 'total');
    const s = q.toString();
    return s ? `?${s}` : '';
}

const JSON_HEADERS = { 'Content-Type': 'application/json' };

async function expectOk(response: Response, fallback: string): Promise<Response> {
    if (!response.ok) throw await parseApiError(response, fallback);
    return response;
}

export const QuoteService = {
    async list(params: ListQuotesParams = {}): Promise<QuotePage> {
        const response = await expectOk(
            await fetchWithAuth(`${API_URL}/api/v1/quotes${buildListQuery(params)}`),
            'Failed to fetch quotes',
        );
        return response.json();
    },

    async get(id: string): Promise<Quote> {
        const response = await expectOk(
            await fetchWithAuth(`${API_URL}/api/v1/quotes/${id}`),
            'Failed to fetch quote',
        );
        return response.json();
    },

    async create(request: QuoteRequest): Promise<Quote> {
        const response = await expectOk(
            await fetchWithAuth(`${API_URL}/api/v1/quotes`, {
                method: 'POST',
                headers: JSON_HEADERS,
                body: JSON.stringify(request),
            }),
            'Failed to create quote',
        );
        return response.json();
    },

    async update(id: string, request: QuoteRequest, revision: number): Promise<Quote> {
        const response = await expectOk(
            await fetchWithAuth(`${API_URL}/api/v1/quotes/${id}`, {
                method: 'PUT',
                headers: { ...JSON_HEADERS, 'If-Match': ifMatch(revision) },
                body: JSON.stringify(request),
            }),
            'Failed to update quote',
        );
        return response.json();
    },

    async transition(id: string, to: QuoteStatus, revision: number): Promise<Quote> {
        const body: QuoteTransitionRequest = { to, revision };
        const response = await expectOk(
            await fetchWithAuth(`${API_URL}/api/v1/quotes/${id}/transitions`, {
                method: 'POST',
                headers: { ...JSON_HEADERS, 'If-Match': ifMatch(revision) },
                body: JSON.stringify(body),
            }),
            'Failed to change quote status',
        );
        return response.json();
    },

    /** The convert accepts the quote and creates the order in one act
     * (ADR 0005 5.8): 201 with the order, no payload for the client to post. */
    async convert(id: string, revision: number): Promise<Order> {
        const response = await expectOk(
            await fetchWithAuth(`${API_URL}/api/v1/quotes/${id}/convert`, {
                method: 'POST',
                headers: { 'If-Match': ifMatch(revision) },
            }),
            'Failed to convert quote to order',
        );
        return response.json();
    },

    /** The revise takes a sent, rejected or expired quote back to draft as its
     * next version (QE-01-b): same number, same id, the live share link revoked. */
    async revise(id: string, revision: number, reason?: string): Promise<Quote> {
        const body: Record<string, unknown> = { revision };
        if (reason) body.reason = reason;
        const response = await expectOk(
            await fetchWithAuth(`${API_URL}/api/v1/quotes/${id}/revise`, {
                method: 'POST',
                headers: { ...JSON_HEADERS, 'If-Match': ifMatch(revision) },
                body: JSON.stringify(body),
            }),
            'Failed to revise quote',
        );
        return response.json();
    },

    /** The quote's sent versions with their snapshots, newest first. */
    async revisions(id: string, limit = 50): Promise<QuoteRevisionPage> {
        const response = await expectOk(
            await fetchWithAuth(`${API_URL}/api/v1/quotes/${id}/revisions?limit=${limit}`),
            'Failed to load quote revisions',
        );
        return response.json();
    },

    /** The sent versions as a list oldest first, ready for the history panel. */
    async revisionsOldestFirst(id: string, limit = 50): Promise<QuoteRevision[]> {
        const page = await this.revisions(id, limit);
        return [...(page.items ?? [])].reverse();
    },

    async analytics(): Promise<QuoteAnalytics> {
        const response = await expectOk(
            await fetchWithAuth(`${API_URL}/api/v1/quotes/analytics`),
            'Failed to fetch quote analytics',
        );
        return response.json();
    },

    /** The stored original of a text list (plain text or csv), read as text. */
    async getOriginalText(quoteId: string): Promise<string> {
        const response = await expectOk(
            await fetchWithAuth(`${API_URL}/api/v1/quotes/${quoteId}/file`),
            'Failed to fetch the original list',
        );
        return response.text();
    },

    /** The API path of the stored original: fetch it with the token (a bare src or href carries none). */
    originalFilePath(quoteId: string): string {
        return `/api/v1/quotes/${quoteId}/file`;
    },
};

