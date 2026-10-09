// SPDX-License-Identifier: LicenseRef-OpenLBM-Surface-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

// The AR reads of ADR 0005 section 10: the aging by customer, job or ship-to,
// its summary, and the customer's statement. Cents on every amount.

import type { ArAgingItem, ArAgingPage, ArAgingSummary, ArStatement, AgingGroupBy } from '../types/account';
import { walkCursor } from '../lib/cursorWalk';
import { fetchWithAuth } from './fetchClient';

const API_BASE_URL = import.meta.env.VITE_API_URL || '';

export const ArService = {
    aging: async (params: { groupBy?: AgingGroupBy; asOf?: string; basis?: 'due_date' | 'invoice_date'; customerId?: string; cursor?: string } = {}): Promise<ArAgingPage> => {
        const qs = new URLSearchParams();
        if (params.groupBy) qs.set('group_by', params.groupBy);
        if (params.asOf) qs.set('as_of', params.asOf);
        if (params.basis) qs.set('basis', params.basis);
        if (params.customerId) qs.set('customer_id', params.customerId);
        if (params.cursor) qs.set('cursor', params.cursor);
        const response = await fetchWithAuth(`${API_BASE_URL}/api/v1/ar/aging?${qs.toString()}`);
        if (!response.ok) {
            throw new Error('Failed to load the aging');
        }
        return response.json();
    },

    // Every page of the aging, so its rows sum to the summary beside them.
    agingAll: async (params: { groupBy?: AgingGroupBy; asOf?: string; basis?: 'due_date' | 'invoice_date'; customerId?: string } = {}): Promise<ArAgingItem[]> =>
        walkCursor((cursor) => ArService.aging({ ...params, cursor })),

    agingSummary: async (params: { asOf?: string; basis?: 'due_date' | 'invoice_date'; customerId?: string } = {}): Promise<ArAgingSummary> => {
        const qs = new URLSearchParams();
        if (params.asOf) qs.set('as_of', params.asOf);
        if (params.basis) qs.set('basis', params.basis);
        if (params.customerId) qs.set('customer_id', params.customerId);
        const response = await fetchWithAuth(`${API_BASE_URL}/api/v1/ar/aging/summary?${qs.toString()}`);
        if (!response.ok) {
            throw new Error('Failed to load the aging summary');
        }
        return response.json();
    },

    statement: async (customerId: string, from?: string, to?: string): Promise<ArStatement> => {
        const qs = new URLSearchParams();
        if (from) qs.set('from', from);
        if (to) qs.set('to', to);
        const suffix = qs.toString() ? `?${qs.toString()}` : '';
        const response = await fetchWithAuth(`${API_BASE_URL}/api/v1/ar/customers/${customerId}/statement${suffix}`);
        if (!response.ok) {
            throw new Error('Failed to load the statement');
        }
        return response.json();
    },
};
