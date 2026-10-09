// SPDX-License-Identifier: LicenseRef-OpenLBM-Surface-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

import type { Activity, ActivityPage, CreateActivityRequest } from '../types/crm';
import { parseApiError } from './apiError';
import { fetchWithAuth } from './fetchClient';

const API_BASE = import.meta.env.VITE_API_URL || '';

/** The most activities listAllActivities collects (pages of 50), so a broken cursor cannot loop forever. */
export const LIST_ALL_ACTIVITIES_CAP = 2000;

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

    /** Pages by cursor until the last page or LIST_ALL_ACTIVITIES_CAP activities, for the activity feed. */
    async listAllActivities(customerId: string, opts: { limit?: number } = {}): Promise<Activity[]> {
        const all: Activity[] = [];
        let cursor: string | undefined;
        while (all.length < LIST_ALL_ACTIVITIES_CAP) {
            const page = await this.listActivities(customerId, { limit: opts.limit ?? 50, cursor });
            all.push(...page.items);
            if (!page.next_cursor) break;
            cursor = page.next_cursor;
        }
        return all.slice(0, LIST_ALL_ACTIVITIES_CAP);
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
