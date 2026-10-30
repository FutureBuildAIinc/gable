// SPDX-License-Identifier: LicenseRef-OpenLBM-Surface-1.0
// SPDX-FileCopyrightText: 2026, FutureBuild, Inc. and OpenLBM contributors

// The dealer's large cash module on the desk (CP-17a and CP-17b): the
// threshold settings (CP-17a) and the manager's worklist, the filing, the
// Form 8300 data set and the customer statement (CP-17b). The Canadian
// threshold is a dealer policy for the dealer's own records; it is not a
// FINTRAC report.

import { fetchWithAuth } from './fetchClient';
import { parseApiError } from './apiError';
import type {
    LargeCashFiling,
    LargeCashForm8300,
    LargeCashStatement,
    LargeCashStatements,
    LargeCashWorklist,
} from '../types/largecash';

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

    /** The manager's queue of the crossings the dealer owes a filing on. */
    async listWorklist(): Promise<LargeCashWorklist> {
        const response = await expectOk(
            await fetchWithAuth(`${API_URL}/api/v1/large-cash/worklist`),
            'Failed to read the large cash worklist',
        );
        return response.json();
    },

    /** The manager's record of the Form 8300 they filed for a crossing. */
    async recordFiling(
        eventID: string,
        acknowledgementNo: string,
        filedDate?: string,
        voluntary = false,
    ): Promise<LargeCashFiling> {
        const response = await expectOk(
            await fetchWithAuth(`${API_URL}/api/v1/large-cash/events/${eventID}/filing`, {
                method: 'POST',
                headers: JSON_HEADERS,
                body: JSON.stringify({
                    acknowledgement_no: acknowledgementNo,
                    filed_date: filedDate,
                    voluntary,
                }),
            }),
            'Failed to record the large cash filing',
        );
        return response.json();
    },

    /** The data set the filer types into BSA E-Filing. */
    async form8300(eventID: string): Promise<LargeCashForm8300> {
        const response = await expectOk(
            await fetchWithAuth(`${API_URL}/api/v1/large-cash/events/${eventID}/form-8300`),
            'Failed to read the Form 8300 data set',
        );
        return response.json();
    },

    /** The customer statements for a year, one per payer. */
    async listStatements(year?: number): Promise<LargeCashStatements> {
        const query = year === undefined ? '' : `?year=${year}`;
        const response = await expectOk(
            await fetchWithAuth(`${API_URL}/api/v1/large-cash/statements${query}`),
            'Failed to read the large cash statements',
        );
        return response.json();
    },

    /** Issues the customer statement for a crossing's payer and year. */
    async issueStatement(eventID: string): Promise<LargeCashStatement> {
        const response = await expectOk(
            await fetchWithAuth(`${API_URL}/api/v1/large-cash/events/${eventID}/statement`, {
                method: 'POST',
                headers: JSON_HEADERS,
                body: JSON.stringify({}),
            }),
            'Failed to issue the customer statement',
        );
        return response.json();
    },
};
