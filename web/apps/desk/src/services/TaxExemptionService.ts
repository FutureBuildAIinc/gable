// SPDX-License-Identifier: LicenseRef-OpenLBM-Surface-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

/**
 * The tax exemption wire (the tax routes, CP-10): list a customer's
 * certificates, record one with its own effective and expiry days, and end
 * one (it stays on file, no longer exempting a sale). Every failure is
 * thrown as an ApiError.
 */
import type { TaxExemption, TaxExemptionRequest } from '../types/tax';
import { fetchWithAuth } from './fetchClient';
import { parseApiError } from './apiError';

const API_URL = import.meta.env.VITE_API_URL || '';
const JSON_HEADERS = { 'Content-Type': 'application/json' };

export const TaxExemptionService = {
    async list(customerId: string): Promise<TaxExemption[]> {
        const response = await fetchWithAuth(`${API_URL}/api/v1/tax/exemptions/${customerId}`);
        if (!response.ok) throw await parseApiError(response, 'Failed to load tax exemptions');
        return response.json();
    },

    async create(req: TaxExemptionRequest): Promise<TaxExemption> {
        const response = await fetchWithAuth(`${API_URL}/api/v1/tax/exemptions`, {
            method: 'POST',
            headers: JSON_HEADERS,
            body: JSON.stringify(req),
        });
        if (!response.ok) throw await parseApiError(response, 'Failed to record the tax exemption');
        return response.json();
    },

    async end(id: string): Promise<void> {
        const response = await fetchWithAuth(`${API_URL}/api/v1/tax/exemptions/${id}/end`, { method: 'POST' });
        if (!response.ok) throw await parseApiError(response, 'Failed to end the tax exemption');
    },
};
