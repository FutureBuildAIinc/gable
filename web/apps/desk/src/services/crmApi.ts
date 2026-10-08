// SPDX-License-Identifier: LicenseRef-OpenLBM-Surface-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

import type { Activity, ActivityPage, CreateActivityRequest } from '../types/crm';
import { parseApiError } from './apiError';
import { fetchWithAuth } from './fetchClient';

const API_BASE = import.meta.env.VITE_API_URL || '';

export const crmApi = {
    /** One page of the customer's activities, newest first. */
    listActivities: async (customerId: string, opts: { limit?: number; cursor?: string } = {}): Promise<ActivityPage> => {
        const params = new URLSearchParams();
        if (opts.limit) params.set('limit', String(opts.limit));
        if (opts.cursor) params.set('cursor', opts.cursor);
        const q = params.toString();
        const res = await fetchWithAuth(`${API_BASE}/api/v1/customers/${customerId}/activities${q ? `?${q}` : ''}`);
        if (!res.ok) throw await parseApiError(res, 'Failed to fetch activities');
        return res.json();
    },

    createActivity: async (customerId: string, data: CreateActivityRequest): Promise<Activity> => {
        const res = await fetchWithAuth(`${API_BASE}/api/v1/customers/${customerId}/activities`, {
            method: 'POST',
            headers: { 'Content-Type': 'application/json' },
            body: JSON.stringify(data),
        });
        if (!res.ok) throw await parseApiError(res, 'Failed to create activity');
        return res.json();
    },
};
