// SPDX-License-Identifier: LicenseRef-OpenLBM-Surface-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

// The order module on the wire contract (ADR 0001; ADR 0005 section 5): the
// cursor list envelope, the transitions route, integer cents money and the
// revision precondition on every write.

import type { Order, OrderSummary, OrderPage, OrderStatus, CreateOrderRequest } from '../types/order.ts';
import { fetchWithAuth } from './fetchClient';
import { parseApiError, ifMatch } from './apiError';

const API_URL = import.meta.env.VITE_API_URL || '';

export interface ListOrdersParams {
    status?: OrderStatus | OrderStatus[];
    customerId?: string;
    jobId?: string;
    shipToId?: string;
    deliveryType?: 'pickup' | 'delivery';
    quoteId?: string;
    limit?: number;
    cursor?: string | null;
    includeTotal?: boolean;
}

export function buildListQuery(params: ListOrdersParams = {}): string {
    const q = new URLSearchParams();
    const statuses = Array.isArray(params.status) ? params.status : params.status ? [params.status] : [];
    if (statuses.length > 0) q.set('status', statuses.join(','));
    if (params.customerId) q.set('customer_id', params.customerId);
    if (params.jobId) q.set('job_id', params.jobId);
    if (params.shipToId) q.set('ship_to_id', params.shipToId);
    if (params.deliveryType) q.set('delivery_type', params.deliveryType);
    if (params.quoteId) q.set('quote_id', params.quoteId);
    if (params.limit !== undefined) q.set('limit', String(params.limit));
    if (params.cursor) q.set('cursor', params.cursor);
    if (params.includeTotal) q.set('include', 'total');
    return q.toString();
}

async function expectOk(response: Response, fallback: string): Promise<Response> {
    if (response.ok) return response;
    throw await parseApiError(response, fallback);
}

export const OrderService = {
    async createOrder(request: CreateOrderRequest): Promise<Order> {
        const response = await expectOk(
            await fetchWithAuth(`${API_URL}/api/v1/orders`, {
                method: 'POST',
                headers: { 'Content-Type': 'application/json' },
                body: JSON.stringify(request),
            }),
            'Failed to create order',
        );
        return response.json();
    },

    /** One page of orders, newest first; the cursor walks forward. */
    async listOrders(params: ListOrdersParams = {}): Promise<OrderPage> {
        const query = buildListQuery(params);
        const response = await expectOk(
            await fetchWithAuth(`${API_URL}/api/v1/orders${query ? `?${query}` : ''}`),
            'Failed to fetch orders',
        );
        return response.json();
    },

    /** Every order of one page-at-a-time caller, walking the cursor. */
    async allOrders(params: Omit<ListOrdersParams, 'cursor'> = {}): Promise<OrderSummary[]> {
        const out: OrderSummary[] = [];
        let cursor: string | null = null;
        do {
            const page: OrderPage = await this.listOrders({ ...params, cursor, limit: params.limit ?? 200 });
            out.push(...page.items);
            cursor = page.next_cursor;
        } while (cursor);
        return out;
    },

    async getOrder(id: string): Promise<Order> {
        const response = await expectOk(
            await fetchWithAuth(`${API_URL}/api/v1/orders/${id}`),
            'Failed to fetch order',
        );
        return response.json();
    },

    /** Replace a draft order's header and lines on the client's revision. */
    async updateOrder(id: string, request: CreateOrderRequest): Promise<Order> {
        const response = await expectOk(
            await fetchWithAuth(`${API_URL}/api/v1/orders/${id}`, {
                method: 'PUT',
                headers: { 'Content-Type': 'application/json' },
                body: JSON.stringify(request),
            }),
            'Failed to update order',
        );
        return response.json();
    },

    /**
     * One lifecycle transition (ADR 0005 5.2). A confirm that lands the
     * order on the credit hold answers with the held order, never an error:
     * check the returned status.
     */
    async transition(
        id: string,
        to: OrderStatus,
        revision: number,
        extras: { reason?: string; holdNote?: string } = {},
    ): Promise<Order> {
        const body: Record<string, unknown> = { to, revision };
        if (extras.reason !== undefined) body.reason = extras.reason;
        if (extras.holdNote !== undefined) body.hold_note = extras.holdNote;
        const response = await expectOk(
            await fetchWithAuth(`${API_URL}/api/v1/orders/${id}/transitions`, {
                method: 'POST',
                headers: { 'Content-Type': 'application/json', 'If-Match': ifMatch(revision) },
                body: JSON.stringify(body),
            }),
            'Failed to change order status',
        );
        return response.json();
    },

    async confirmOrder(id: string, revision: number): Promise<Order> {
        return this.transition(id, 'confirmed', revision);
    },

    async releaseHold(id: string, revision: number): Promise<Order> {
        return this.transition(id, 'confirmed', revision);
    },

    async holdOrder(id: string, revision: number, holdNote: string): Promise<Order> {
        return this.transition(id, 'on_hold', revision, { holdNote });
    },

    async cancelOrder(id: string, revision: number, reason: string): Promise<Order> {
        return this.transition(id, 'cancelled', revision, { reason });
    },

    /**
     * The money moment (ADR 0005 5.6): bills the allocated quantities in one
     * act. A pickup (will-call) order names who collected it. The answer is the
     * updated order and the invoice it created (the Location header).
     */
    async fulfil(
        id: string,
        revision: number,
        extras: { pickedUpBy?: string; lines?: { order_line_id: string; quantity: string }[] } = {},
    ): Promise<{ order: Order; invoiceId: string | null }> {
        const body: Record<string, unknown> = { revision };
        if (extras.pickedUpBy) body.picked_up_by = extras.pickedUpBy;
        if (extras.lines) body.lines = extras.lines;
        const response = await expectOk(
            await fetchWithAuth(`${API_URL}/api/v1/orders/${id}/fulfillments`, {
                method: 'POST',
                headers: { 'Content-Type': 'application/json', 'If-Match': ifMatch(revision) },
                body: JSON.stringify(body),
            }),
            'Failed to fulfil order',
        );
        const location = response.headers.get('Location') ?? '';
        const invoiceId = location.startsWith('/api/v1/invoices/') ? location.slice('/api/v1/invoices/'.length) : null;
        return { order: await response.json(), invoiceId };
    },

    /** The desk's retry for a back order (ADR 0005 5.4). */
    async allocate(id: string, revision: number): Promise<Order> {
        const response = await expectOk(
            await fetchWithAuth(`${API_URL}/api/v1/orders/${id}/allocate`, {
                method: 'POST',
                headers: { 'Content-Type': 'application/json', 'If-Match': ifMatch(revision) },
                body: JSON.stringify({ revision }),
            }),
            'Failed to allocate order',
        );
        return response.json();
    },

    async checkExposureGate(id: string): Promise<{ blocked: boolean }> {
        const response = await expectOk(
            await fetchWithAuth(`${API_URL}/api/v1/orders/${id}/exposure-gate`),
            'Failed to check exposure gate',
        );
        return response.json();
    },
};
