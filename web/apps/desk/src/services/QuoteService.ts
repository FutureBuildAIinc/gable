// SPDX-License-Identifier: LicenseRef-OpenLBM-Surface-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

import type {
    Quote,
    QuoteAnalytics,
    QuoteOrderPayload,
    QuotePage,
    QuoteRequest,
    QuoteStatus,
    QuoteTransitionRequest,
} from '../types/quote';
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

    async convert(id: string, revision: number): Promise<QuoteOrderPayload> {
        const response = await expectOk(
            await fetchWithAuth(`${API_URL}/api/v1/quotes/${id}/convert`, {
                method: 'POST',
                headers: { 'If-Match': ifMatch(revision) },
            }),
            'Failed to convert quote to order',
        );
        return response.json();
    },

    async analytics(): Promise<QuoteAnalytics> {
        const response = await expectOk(
            await fetchWithAuth(`${API_URL}/api/v1/quotes/analytics`),
            'Failed to fetch quote analytics',
        );
        return response.json();
    },

    getOriginalFileUrl(quoteId: string): string {
        return `${API_URL}/api/v1/quotes/${quoteId}/file`;
    },
};

/**
 * The orders route is not converted yet: it reads a numeric quantity and
 * price_each in cents. This is the one place the quote payload is mapped onto it.
 */
export function orderRequestFromQuotePayload(payload: QuoteOrderPayload): {
    customer_id: string;
    quote_id: string;
    lines: { product_id: string; quantity: number; price_each: number }[];
} {
    if (payload.lines.some(l => l.product_id === null)) {
        throw new Error('The quote was accepted, but it has special order lines with no catalog product. Create those order lines by hand.');
    }
    return {
        customer_id: payload.customer_id,
        quote_id: payload.quote_id,
        lines: payload.lines.map(l => ({
            product_id: l.product_id as string,
            quantity: Number(l.quantity),
            price_each: l.price_each_cents,
        })),
    };
}
