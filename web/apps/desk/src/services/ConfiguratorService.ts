// SPDX-License-Identifier: LicenseRef-OpenLBM-Surface-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

import type { ValidateConfigResponse, BuildSKUResponse, AvailableOption, ConfiguratorRule, ConfiguratorPreset } from '../types/configurator';
import { parseApiError } from './apiError';
import { fetchWithAuth } from './fetchClient';

const API_URL = import.meta.env.VITE_API_URL || '';

export const ConfiguratorService = {
    async getRules(): Promise<ConfiguratorRule[]> {
        const response = await fetchWithAuth(`${API_URL}/api/v1/configurator/rules`);
        if (!response.ok) throw await parseApiError(response, 'Failed to fetch configurator rules');
        return response.json();
    },

    async validateConfig(selections: Record<string, string>): Promise<ValidateConfigResponse> {
        const response = await fetchWithAuth(`${API_URL}/api/v1/configurator/validate`, {
            method: 'POST',
            headers: { 'Content-Type': 'application/json' },
            body: JSON.stringify({ selections }),
        });
        if (!response.ok) throw await parseApiError(response, 'Validation request failed');
        return response.json();
    },

    async buildSKU(productType: string, selections: Record<string, string>): Promise<BuildSKUResponse> {
        const response = await fetchWithAuth(`${API_URL}/api/v1/configurator/build-sku`, {
            method: 'POST',
            headers: { 'Content-Type': 'application/json' },
            body: JSON.stringify({ product_type: productType, selections }),
        });
        if (!response.ok) throw await parseApiError(response, 'Failed to build SKU');
        return response.json();
    },

    /** The selections ride as one declared parameter, Type=Value pairs comma separated. */
    async getAvailableOptions(attributeType: string, selections: Record<string, string>): Promise<AvailableOption[]> {
        const params = new URLSearchParams({ attribute_type: attributeType });
        const pairs = Object.entries(selections)
            .filter(([, value]) => value)
            .map(([key, value]) => `${key}=${value}`);
        if (pairs.length > 0) params.set('selections', pairs.join(','));
        const response = await fetchWithAuth(`${API_URL}/api/v1/configurator/options?${params.toString()}`);
        if (!response.ok) throw await parseApiError(response, 'Failed to fetch options');
        return response.json();
    },

    async getPresets(productType?: string): Promise<ConfiguratorPreset[]> {
        const params = productType ? `?product_type=${encodeURIComponent(productType)}` : '';
        const response = await fetchWithAuth(`${API_URL}/api/v1/configurator/presets${params}`);
        if (!response.ok) throw await parseApiError(response, 'Failed to fetch presets');
        return response.json();
    },
};
