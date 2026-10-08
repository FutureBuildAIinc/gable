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

const API_URL = import.meta.env.VITE_API_URL || '';

export interface QuoteErrorDetail {
    field?: string;
    message: string;
    code?: string;
}

/**
 * Every non 2xx answer of the quote routes is the one error envelope of ADR 0001:
 * { error: { code, message, details? }, meta: { request_id } }. This carries it.
 */
export class QuoteApiError extends Error {
    readonly status: number;
    readonly code: string;
    readonly details: QuoteErrorDetail[];
    readonly requestId: string | undefined;

    constructor(status: number, code: string, message: string, details: QuoteErrorDetail[] = [], requestId?: string) {
        super(message);
        this.name = 'QuoteApiError';
        this.status = status;
        this.code = code;
        this.details = details;
        this.requestId = requestId;
    }

    get isStaleRevision(): boolean {
        return this.code === 'stale_revision';
    }

    /** The message, with each field problem listed on its own line for validation_failed. */
    get displayMessage(): string {
        if (this.code === 'validation_failed' && this.details.length > 0) {
            const lines = this.details.map(d => (d.field ? `${d.field}: ${d.message}` : d.message));
            return `${this.message}\n${lines.join('\n')}`;
        }
        return this.message;
    }
}

/** Text for a toast or inline notice from any thrown value. */
export function quoteErrorMessage(err: unknown, fallback = 'Something went wrong'): string {
    if (err instanceof QuoteApiError) return err.displayMessage;
    if (err instanceof Error && err.message) return err.message;
    return fallback;
}

/** Parses the error envelope out of a failed response; tolerates a non-JSON body. */
export async function parseQuoteError(response: Response, fallback: string): Promise<QuoteApiError> {
    let body: unknown = null;
    try {
        body = await response.json();
    } catch {
        body = null;
    }
    const env = (body && typeof body === 'object' ? body : {}) as {
        error?: { code?: unknown; message?: unknown; details?: unknown };
        meta?: { request_id?: unknown };
    };
    const err = env.error;
    const code = typeof err?.code === 'string' ? err.code : 'unknown_error';
    const message = typeof err?.message === 'string' && err.message ? err.message : fallback;
    const details: QuoteErrorDetail[] = Array.isArray(err?.details)
        ? (err.details as unknown[])
              .filter((d): d is Record<string, unknown> => !!d && typeof d === 'object')
              .map(d => ({
                  field: typeof d.field === 'string' ? d.field : undefined,
                  message: typeof d.message === 'string' ? d.message : '',
                  code: typeof d.code === 'string' ? d.code : undefined,
              }))
        : [];
    const requestId = typeof env.meta?.request_id === 'string' ? env.meta.request_id : undefined;
    return new QuoteApiError(response.status, code, message, details, requestId);
}

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

/** The If-Match value for a revision: quoted, as an ETag is. */
export function ifMatch(revision: number): string {
    return `"${revision}"`;
}

const JSON_HEADERS = { 'Content-Type': 'application/json' };

async function expectOk(response: Response, fallback: string): Promise<Response> {
    if (!response.ok) throw await parseQuoteError(response, fallback);
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
