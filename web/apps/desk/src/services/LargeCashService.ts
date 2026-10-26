// SPDX-License-Identifier: LicenseRef-OpenLBM-Surface-1.0
// SPDX-FileCopyrightText: 2026, FutureBuild, Inc. and OpenLBM contributors

// The dealer's large cash settings (CP-17a): the default country a branch
// with none of its own falls back to, and the Canadian dealer threshold, off
// by default and never a legal duty. The Canadian threshold is a dealer
// policy for the dealer's own records; it is not a FINTRAC report.

import { fetchWithAuth } from './fetchClient';
import { parseApiError } from './apiError';

const API_URL = import.meta.env.VITE_API_URL || '';
const JSON_HEADERS = { 'Content-Type': 'application/json' };

async function expectOk(response: Response, fallback: string): Promise<Response> {
    if (!response.ok) {
        throw await parseApiError(response, fallback);
    }
    return response;
}

export interface LargeCashSettings {
    country_default: string;
    ca_enabled: boolean;
    ca_threshold_cents: number;
    ca_default_threshold_cents: number;
}

export const LargeCashService = {
    async read(): Promise<LargeCashSettings> {
        const response = await expectOk(
            await fetchWithAuth(`${API_URL}/api/v1/admin/large-cash/settings`),
            'Failed to load the large cash settings',
        );
        return response.json();
    },

    async save(settings: LargeCashSettings): Promise<LargeCashSettings> {
        const response = await expectOk(
            await fetchWithAuth(`${API_URL}/api/v1/admin/large-cash/settings`, {
                method: 'PUT',
                headers: JSON_HEADERS,
                body: JSON.stringify(settings),
            }),
            'Failed to save the large cash settings',
        );
        return response.json();
    },
};
