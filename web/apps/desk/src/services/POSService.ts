// SPDX-License-Identifier: LicenseRef-OpenLBM-Surface-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

// The counter module's desk service on the wire contract (ADR 0005 14.2
// C2-5): tenders and floats in cents, the line wrapper, lowercase methods,
// and the revision preconditions the complete and the void carry. Money is
// parsed to cents with string arithmetic, never parseFloat.

import type {
    Sale,
    SalePage,
    SaleSummary,
    QuickSearchResult,
    TenderMethod,
    CounterReturn,
    RefundMethod,
    TillSession,
    TillReport,
} from '../types/pos.ts';
import { fetchWithAuth } from './fetchClient';
import { parseApiError, ifMatch } from './apiError';

const API_URL = import.meta.env.VITE_API_URL || '';

async function expectOk(response: Response, fallback: string): Promise<Response> {
    if (response.ok) return response;
    throw await parseApiError(response, fallback);
}

const JSON_HEADERS = { 'Content-Type': 'application/json' };

/** Dollars typed by a cashier to integer cents; an empty or bad parse is NaN. */
export function dollarsToCents(text: string): number {
    const t = text.trim();
    if (!/^-?\d+(\.\d{1,2})?$/.test(t)) return NaN;
    const [whole, fraction = ''] = t.split('.');
    const sign = whole.startsWith('-') ? -1 : 1;
    const w = whole.replace('-', '');
    const cents = Number(w) * 100 + Number(fraction.padEnd(2, '0') || '0');
    return sign * cents;
}

export interface TenderIn {
    method: TenderMethod;
    amount_cents: number;
    reference?: string;
    token_id?: string;
}

export const posService = {
    async startSale(registerID: string, customerID?: string): Promise<Sale> {
        const response = await expectOk(
            await fetchWithAuth(`${API_URL}/api/v1/pos/transactions`, {
                method: 'POST',
                headers: JSON_HEADERS,
                body: JSON.stringify({ register_id: registerID, customer_id: customerID || undefined }),
            }),
            'Failed to start the sale',
        );
        return response.json();
    },

    async getSale(id: string): Promise<Sale> {
        const response = await expectOk(
            await fetchWithAuth(`${API_URL}/api/v1/pos/transactions/${id}`),
            'Failed to read the sale',
        );
        return response.json();
    },

    async addItem(saleId: string, productID: string, quantity: string, uom?: string): Promise<Sale> {
        const response = await expectOk(
            await fetchWithAuth(`${API_URL}/api/v1/pos/transactions/${saleId}/items`, {
                method: 'POST',
                headers: JSON_HEADERS,
                body: JSON.stringify({ line: { product_id: productID, quantity, uom } }),
            }),
            'Failed to add the item',
        );
        return response.json();
    },

    async removeItem(saleId: string, itemId: string): Promise<Sale> {
        const response = await expectOk(
            await fetchWithAuth(`${API_URL}/api/v1/pos/transactions/${saleId}/items/${itemId}`, { method: 'DELETE' }),
            'Failed to remove the item',
        );
        return response.json();
    },

    /** The money moment: the tenders in cents on the sale's revision. */
    async completeSale(saleId: string, revision: number, tenders: TenderIn[]): Promise<Sale> {
        const response = await expectOk(
            await fetchWithAuth(`${API_URL}/api/v1/pos/transactions/${saleId}/complete`, {
                method: 'POST',
                headers: { ...JSON_HEADERS, 'If-Match': ifMatch(revision) },
                body: JSON.stringify({ tenders }),
            }),
            'Failed to complete the sale',
        );
        return response.json();
    },

    async voidSale(saleId: string, revision: number, reason: string): Promise<Sale> {
        const response = await expectOk(
            await fetchWithAuth(`${API_URL}/api/v1/pos/transactions/${saleId}/void`, {
                method: 'POST',
                headers: { ...JSON_HEADERS, 'If-Match': ifMatch(revision) },
                body: JSON.stringify({ reason }),
            }),
            'Failed to void the sale',
        );
        return response.json();
    },

    /** The day's sales: one page, newest first (the page is the whole day). */
    async listSales(registerID?: string, date?: string): Promise<SalePage> {
        const params = new URLSearchParams();
        if (registerID) params.append('register_id', registerID);
        if (date) params.append('date', date);
        const response = await expectOk(
            await fetchWithAuth(`${API_URL}/api/v1/pos/transactions?${params.toString()}`),
            'Failed to list the day\u2019s sales',
        );
        return response.json();
    },

    async todaySales(registerID?: string): Promise<SaleSummary[]> {
        return (await this.listSales(registerID)).items;
    },

    async searchProducts(query: string): Promise<QuickSearchResult[]> {
        if (query.length < 2) return [];
        const response = await expectOk(
            await fetchWithAuth(`${API_URL}/api/v1/pos/products/search?q=${encodeURIComponent(query)}`),
            'Failed to search products',
        );
        return response.json();
    },

    // --- Returns ---

    async createReturn(input: {
        register_id?: string;
        original_sale_id?: string;
        customer_id?: string;
        refund_method: RefundMethod;
        reason: string;
        lines: Array<{ line_id: string; quantity: string; restock?: boolean }>;
    }): Promise<CounterReturn> {
        const response = await expectOk(
            await fetchWithAuth(`${API_URL}/api/v1/pos/returns`, {
                method: 'POST',
                headers: JSON_HEADERS,
                body: JSON.stringify(input),
            }),
            'Failed to take the return',
        );
        return response.json();
    },

    // --- Till sessions (drawer lifecycle) ---

    /** The register's open session, or null when none is open. */
    async currentTill(registerID: string): Promise<TillSession | null> {
        const response = await fetchWithAuth(
            `${API_URL}/api/v1/pos/till/current?register_id=${encodeURIComponent(registerID)}`,
        );
        if (response.status === 404) return null;
        if (!response.ok) throw await parseApiError(response, 'Failed to look up the till');
        return response.json();
    },

    async openTill(registerID: string, openingFloatCents: number): Promise<TillSession> {
        const response = await expectOk(
            await fetchWithAuth(`${API_URL}/api/v1/pos/till/open`, {
                method: 'POST',
                headers: JSON_HEADERS,
                body: JSON.stringify({ register_id: registerID, opening_float_cents: openingFloatCents }),
            }),
            'Failed to open the till',
        );
        return response.json();
    },

    async tillReport(sessionID: string): Promise<TillReport> {
        const response = await expectOk(
            await fetchWithAuth(`${API_URL}/api/v1/pos/till/${sessionID}/report`),
            'Failed to load the till report',
        );
        return response.json();
    },

    async closeTill(sessionID: string, countedByMethod: Record<string, number>, notes: string): Promise<TillReport> {
        const response = await expectOk(
            await fetchWithAuth(`${API_URL}/api/v1/pos/till/${sessionID}/close`, {
                method: 'POST',
                headers: JSON_HEADERS,
                body: JSON.stringify({ counted_by_method: countedByMethod, notes }),
            }),
            'Failed to close the till',
        );
        return response.json();
    },
};
