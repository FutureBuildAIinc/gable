// SPDX-License-Identifier: LicenseRef-OpenLBM-Surface-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

/** The one dealer wide profile every printed form shows (GET and PUT /api/v1/dealer-profile). */
import { fetchWithAuth } from './fetchClient';
import { parseApiError, ifMatch } from './apiError';

const API_URL = import.meta.env.VITE_API_URL || '';

export interface DealerProfile {
    legal_name: string;
    trading_name: string;
    phone: string;
    email: string;
    website: string;
    remit_to: string;
    locale: string;
    /** Stored normalized (123456789RT0001); empty when none is on file. */
    gst_hst_number: string;
    /** The small supplier switch: on, no GST is charged or printed. */
    gst_hst_not_registered: boolean;
    logo_url: string | null;
    revision: number;
    updated_at: string;
}

/** Prints a stored number the way CRA writes it: 123456789 RT0001. */
export function formatGSTHSTNumber(n: string): string {
    return n.length === 15 ? `${n.slice(0, 9)} ${n.slice(9)}` : n;
}

export const dealerProfileService = {
    async get(): Promise<DealerProfile> {
        const response = await fetchWithAuth(`${API_URL}/api/v1/dealer-profile`);
        if (!response.ok) throw await parseApiError(response, 'Could not read the dealer profile');
        return response.json();
    },

    /** Saves the GST/HST registration, sending the rest of the profile back as it was read. */
    async saveGSTHST(current: DealerProfile, number: string, notRegistered: boolean): Promise<DealerProfile> {
        const body = {
            legal_name: current.legal_name, trading_name: current.trading_name, phone: current.phone,
            email: current.email, website: current.website, remit_to: current.remit_to, locale: current.locale,
            gst_hst_number: number, gst_hst_not_registered: notRegistered,
        };
        const response = await fetchWithAuth(`${API_URL}/api/v1/dealer-profile`, {
            method: 'PUT',
            headers: { 'Content-Type': 'application/json', 'If-Match': ifMatch(current.revision) },
            body: JSON.stringify(body),
        });
        if (!response.ok) throw await parseApiError(response, 'Could not save the GST/HST registration');
        return response.json();
    },
};
