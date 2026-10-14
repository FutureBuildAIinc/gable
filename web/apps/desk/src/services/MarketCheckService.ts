// SPDX-License-Identifier: LicenseRef-OpenLBM-Surface-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

/**
 * The Market check wire (item 112 c): the Tech Admin settings and the run. The webhook id
 * is write only: no answer of these routes carries it. Every failure is an ApiError.
 */
import { fetchWithAuth } from './fetchClient';
import { parseApiError } from './apiError';

const API_URL = import.meta.env.VITE_API_URL || '';

export interface MarketCheckSettings {
    connect_url: string;
    webhook_id_saved: boolean;
    flow_name: string;
    store_label: string;
    store_id: string;
    store_zip: string;
    band_pct: string;
    margin_floor_pct: string;
    configured: boolean;
}

/** A field left out keeps what is saved; webhook_id '' clears the saved id. */
export type MarketCheckSettingsInput = Partial<Omit<MarketCheckSettings, 'webhook_id_saved' | 'configured'>> & {
    webhook_id?: string;
};

export type MarketCheckPosition = '' | 'above' | 'below' | 'in_line';
export type MarketCheckReason = '' | 'lower_to_band' | 'raise_to_band' | 'held_at_floor' | 'keep' | 'no_cost';

export interface MarketCheckCompetitor {
    title: string;
    price_cents: number;
    listed_price_cents: number;
    link: string;
    confidence: number;
    unit_note: string;
}

export interface MarketCheckRow {
    product_id: string;
    sku: string;
    description: string;
    uom: string;
    our_price_cents: number;
    cost_known: boolean;
    status: 'matched' | 'unmatched' | 'error';
    competitor: MarketCheckCompetitor | null;
    diff_pct: number | null;
    position: MarketCheckPosition;
    suggested_price_cents: number | null;
    reason: MarketCheckReason;
    note: string;
}

export interface MarketCheckResult {
    flow_name: string;
    store_label: string;
    store_seen: string | null;
    band_pct: string;
    margin_floor_pct: string;
    rows: MarketCheckRow[];
}

/**
 * A run can take a while: the server reads in batches of up to 30 seconds each, and a cold FB Connect
 * run takes longer than the fetch client's default 10 seconds, so the run opts into a longer budget.
 */
export const RUN_TIMEOUT_MS = 90_000;

export const TOO_SLOW_WORDS = 'FB Connect took too long to answer; try again';

const JSON_HEADERS = { 'Content-Type': 'application/json' };

export const marketCheckService = {
    async getSettings(): Promise<MarketCheckSettings> {
        const response = await fetchWithAuth(`${API_URL}/api/v1/admin/market-check/settings`);
        if (!response.ok) throw await parseApiError(response, 'Could not read the Market check settings');
        return response.json();
    },

    async saveSettings(input: MarketCheckSettingsInput): Promise<MarketCheckSettings> {
        const response = await fetchWithAuth(`${API_URL}/api/v1/admin/market-check/settings`, {
            method: 'PUT',
            headers: JSON_HEADERS,
            body: JSON.stringify(input),
        });
        if (!response.ok) throw await parseApiError(response, 'Could not save the Market check settings');
        return response.json();
    },

    async run(productIds: string[]): Promise<MarketCheckResult> {
        let response: Response;
        try {
            response = await fetchWithAuth(`${API_URL}/api/v1/pricing/market-check`, {
                method: 'POST',
                headers: JSON_HEADERS,
                body: JSON.stringify({ product_ids: productIds }),
                timeout: RUN_TIMEOUT_MS,
            });
        } catch (err) {
            // The fetch client aborts at its timeout; an abort reads in words, not as "signal is aborted".
            if (err instanceof Error && (err.name === 'AbortError' || err.name === 'TimeoutError')) {
                throw new Error(TOO_SLOW_WORDS);
            }
            throw err;
        }
        if (!response.ok) throw await parseApiError(response, 'The market check could not run');
        return response.json();
    },
};
