// SPDX-License-Identifier: LicenseRef-OpenLBM-Surface-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

import type { RFC, RFCSummary, CreateRFCInput } from '../types/governance';
import { fetchWithAuth } from './fetchClient';

const API_URL = import.meta.env.VITE_API_URL || '';

interface ListPage<T> {
    items: T[];
    next_cursor: string | null;
    limit: number;
    total?: number;
}

interface WireErrorBody {
    error?: { code?: string; message?: string; details?: { field?: string; message?: string }[] };
}

async function readError(response: Response): Promise<string> {
    try {
        const body = (await response.json()) as WireErrorBody;
        const parts = (body.error?.details ?? []).map((d) => (d.field ? `${d.field}: ${d.message}` : d.message));
        return [body.error?.message ?? `Request failed (${response.status})`, ...parts].join('; ');
    } catch {
        return `Request failed (${response.status})`;
    }
}

async function failOn(response: Response): Promise<void> {
    if (!response.ok) throw new Error(await readError(response));
}

export const GovernanceService = {
    /** Every RFC, newest first; the admin surface is small, so the pages walk. */
    async listRFCs(): Promise<RFCSummary[]> {
        const out: RFCSummary[] = [];
        let cursor = '';
        for (let page = 0; page < 50; page++) {
            const url = `${API_URL}/api/v1/governance/rfcs?limit=200${cursor ? `&cursor=${encodeURIComponent(cursor)}` : ''}`;
            const response = await fetchWithAuth(url);
            await failOn(response);
            const data = (await response.json()) as ListPage<RFCSummary>;
            out.push(...data.items);
            if (!data.next_cursor) break;
            cursor = data.next_cursor;
        }
        return out;
    },

    async getRFC(id: string): Promise<RFC> {
        const response = await fetchWithAuth(`${API_URL}/api/v1/governance/rfcs/${id}`);
        await failOn(response);
        return response.json();
    },

    async createRFC(input: CreateRFCInput): Promise<RFC> {
        const response = await fetchWithAuth(`${API_URL}/api/v1/governance/rfcs`, {
            method: 'POST',
            headers: {
                'Content-Type': 'application/json',
            },
            body: JSON.stringify(input),
        });
        await failOn(response);
        return response.json();
    },

    /**
     * Edit an RFC's body on the revision it holds. Status never travels here:
     * the lifecycle moves through transitionRFC. A stale write (someone else
     * saved first) is retried once on the fresh revision, then surfaces.
     */
    async updateRFC(
        id: string,
        input: { title?: string; problem_statement?: string; proposed_solution?: string; content?: string },
    ): Promise<RFC> {
        for (let attempt = 0; attempt < 2; attempt++) {
            const current = await this.getRFC(id);
            const response = await fetchWithAuth(`${API_URL}/api/v1/governance/rfcs/${id}`, {
                method: 'PUT',
                headers: {
                    'Content-Type': 'application/json',
                    'If-Match': `"${current.revision}"`,
                },
                body: JSON.stringify(input),
            });
            if (response.status === 409 && attempt === 0) continue;
            await failOn(response);
            return response.json();
        }
        throw new Error('The RFC kept moving; reload and try again');
    },

    /**
     * Move an RFC along its lifecycle (draft to review, review to approved or
     * rejected, rejected reopened to draft) on the revision it holds.
     */
    async transitionRFC(id: string, to: RFCSummary['status']): Promise<RFC> {
        for (let attempt = 0; attempt < 2; attempt++) {
            const current = await this.getRFC(id);
            const response = await fetchWithAuth(`${API_URL}/api/v1/governance/rfcs/${id}/transitions`, {
                method: 'POST',
                headers: {
                    'Content-Type': 'application/json',
                    'If-Match': `"${current.revision}"`,
                },
                body: JSON.stringify({ to }),
            });
            if (response.status === 409 && attempt === 0) continue;
            await failOn(response);
            return response.json();
        }
        throw new Error('The RFC kept moving; reload and try again');
    },
};
