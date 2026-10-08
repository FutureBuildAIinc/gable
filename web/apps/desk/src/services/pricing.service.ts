// SPDX-License-Identifier: LicenseRef-OpenLBM-Surface-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

import type { CalculatedPrice, MarketIndex, EscalationRequest, EscalationResult } from '../types/pricing';
import { fetchWithAuth } from './fetchClient';
import { parseApiError } from './apiError';
import { isPositiveQuantity } from '../lib/money';

const API_URL = import.meta.env.VITE_API_URL || '';

export const PricingService = {
    /**
     * The price a customer pays for a product (ADR 0006 section 7.3). The quantity is a decimal
     * string; one that is not positive is refused here rather than ignored. Failures are ApiErrors.
     */
    calculatePrice: async (customerId: string, productId: string, quantity?: string, jobId?: string): Promise<CalculatedPrice> => {
        const params = new URLSearchParams({
            customer_id: customerId,
            product_id: productId,
        });
        if (quantity !== undefined) {
            if (!isPositiveQuantity(quantity)) throw new Error('The quantity must be a positive number');
            params.set('quantity', quantity);
        }
        if (jobId) {
            params.set('job_id', jobId);
        }
        const response = await fetchWithAuth(`${API_URL}/api/v1/pricing/calculate?${params.toString()}`);
        if (!response.ok) {
            throw await parseApiError(response, 'Failed to calculate price');
        }
        return response.json() as Promise<CalculatedPrice>;
    },

    getMarketIndices: async (): Promise<MarketIndex[]> => {
        const response = await fetchWithAuth(`${API_URL}/api/v1/market-indices`);
        if (!response.ok) {
            throw new Error('Failed to fetch market indices');
        }
        return response.json() as Promise<MarketIndex[]>;
    },

    calculateEscalation: async (request: EscalationRequest): Promise<EscalationResult> => {
        const response = await fetchWithAuth(`${API_URL}/api/v1/pricing/calculate-escalation`, {
            method: 'POST',
            headers: { 'Content-Type': 'application/json' },
            body: JSON.stringify(request),
        });
        if (!response.ok) {
            throw new Error('Failed to calculate escalation');
        }
        return response.json() as Promise<EscalationResult>;
    },
};
