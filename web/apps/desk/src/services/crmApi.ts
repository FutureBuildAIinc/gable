// SPDX-License-Identifier: LicenseRef-OpenLBM-Surface-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

import type { Activity, CreateActivityRequest } from '../types/crm';
import { fetchWithAuth } from './fetchClient';

const API_BASE = import.meta.env.VITE_API_URL || '';

export const crmApi = {
    // Activities
    listActivities: async (customerId: string): Promise<Activity[]> => {
        const res = await fetchWithAuth(`${API_BASE}/api/v1/customers/${customerId}/activities`);
        if (!res.ok) throw new Error('Failed to fetch activities');
        return res.json();
    },

    createActivity: async (customerId: string, data: CreateActivityRequest): Promise<Activity> => {
        const res = await fetchWithAuth(`${API_BASE}/api/v1/customers/${customerId}/activities`, {
            method: 'POST',
            headers: { 'Content-Type': 'application/json' },
            body: JSON.stringify(data),
        });
        if (!res.ok) throw new Error('Failed to create activity');
        return res.json();
    },
};
