// SPDX-License-Identifier: LicenseRef-OpenLBM-Surface-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

import type { MillworkOption, MillworkOptionPage, CreateOptionRequest } from '../types/millwork';
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

    /** The configured door's price in integer cents; the option adjustments are cents on the wire. */
    calculateDoorPriceCents(config: import('../types/millwork').MillworkConfiguration): number {
        const BASE_PRICE_CENTS = 25000;
        const STANDARD_WIDTH = 36;
        const STANDARD_HEIGHT = 80;
        const PRICE_PER_SQFT_OVERAGE_CENTS = 1500;

        let price = BASE_PRICE_CENTS;
        if (config.doorType) price += config.doorType.price_adjustment_cents;
        if (config.material) price += config.material.price_adjustment_cents;
        if (config.glass) price += config.glass.price_adjustment_cents;

        // Simple dimension logic: +$15 for every sq ft over standard 36x80
        const area = (config.width * config.height) / 144; // sq ft
        const standardArea = (STANDARD_WIDTH * STANDARD_HEIGHT) / 144;

        if (area > standardArea) {
            price += Math.round((area - standardArea) * PRICE_PER_SQFT_OVERAGE_CENTS);
        }

        return price;
    }
};
