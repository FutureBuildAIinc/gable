// SPDX-License-Identifier: LicenseRef-OpenLBM-Surface-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

import type {
    MillworkOption,
    MillworkOptionPage,
    CreateOptionRequest,
    UpdateOptionRequest,
    EstimateRequest,
    EstimatedItem,
} from '../types/millwork';
import { parseApiError } from './apiError';
import { fetchWithAuth } from './fetchClient';

const API_URL = import.meta.env.VITE_API_URL || '';

/** The most options one category read collects (default pages), so a broken cursor cannot loop forever. */
export const MILLWORK_OPTIONS_CAP = 2000;

export const MillworkService = {
    /**
     * Every option of one category, following the page cursor to the end and
     * sorted by name: the wire orders the catalog newest first, and the
     * configurator's pickers read it alphabetically.
     */
    async getOptionsByCategory(category: string, opts: { limit?: number } = {}): Promise<MillworkOption[]> {
        const all: MillworkOption[] = [];
        let cursor: string | undefined;
        while (all.length < MILLWORK_OPTIONS_CAP) {
            const params = new URLSearchParams({ category });
            if (opts.limit) params.set('limit', String(opts.limit));
            if (cursor) params.set('cursor', cursor);
            const response = await fetchWithAuth(`${API_URL}/api/v1/millwork/options?${params.toString()}`);
            if (!response.ok) {
                throw await parseApiError(response, 'Failed to fetch millwork options');
            }
            const page = await response.json() as MillworkOptionPage;
            all.push(...page.items);
            if (!page.next_cursor) break;
            cursor = page.next_cursor;
        }
        return all.slice(0, MILLWORK_OPTIONS_CAP)
            .sort((a, b) => a.name.localeCompare(b.name));
    },

    async createOption(option: CreateOptionRequest): Promise<MillworkOption> {
        const response = await fetchWithAuth(`${API_URL}/api/v1/millwork/options`, {
            method: 'POST',
            headers: {
                'Content-Type': 'application/json',
            },
            body: JSON.stringify(option),
        });
        if (!response.ok) {
            throw await parseApiError(response, 'Failed to create millwork option');
        }
        return response.json();
    },

    /**
     * Moves an option's money on the revision the caller read. The If-Match
     * header carries it, so a write that lost the race is a 409 the dealer
     * sees, not a silent overwrite.
     */
    async updateOption(id: string, option: UpdateOptionRequest): Promise<MillworkOption> {
        const response = await fetchWithAuth(`${API_URL}/api/v1/millwork/options/${id}`, {
            method: 'PUT',
            headers: {
                'Content-Type': 'application/json',
                'If-Match': `"${option.revision ?? 0}"`,
            },
            body: JSON.stringify(option),
        });
        if (!response.ok) {
            throw await parseApiError(response, 'Failed to update millwork option');
        }
        return response.json();
    },

    /**
     * Prices a configuration on the server. The client sends the choices and
     * reads the money: the price is the server's, computed from the stored
     * options, and the response carries the deterministic SKU, the
     * description, the product row and the breakdown that made the price.
     */
    async estimate(request: EstimateRequest): Promise<EstimatedItem> {
        const response = await fetchWithAuth(`${API_URL}/api/v1/millwork/estimate`, {
            method: 'POST',
            headers: { 'Content-Type': 'application/json' },
            body: JSON.stringify(request),
        });
        if (!response.ok) {
            throw await parseApiError(response, 'Failed to price the configuration');
        }
        return response.json();
    },
};
