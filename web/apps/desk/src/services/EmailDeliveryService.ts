// SPDX-License-Identifier: LicenseRef-OpenLBM-Surface-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

// The email delivery record (item M3, CP-14): one row per send, written
// queued the moment the sending route answers 202 and resolved by the send to
// sent, logged or failed. The desk reads it to say what actually happened to
// an emailed receipt or invoice; the 202 alone never says the mail went out.

import { fetchWithAuth } from './fetchClient';
import { ApiError, parseApiError } from './apiError';
import { OUTCOME_UNKNOWN } from '../lib/emailOutcome.ts';

const API_URL = import.meta.env.VITE_API_URL || '';

export type EmailDeliveryStatus = 'queued' | 'sent' | 'logged' | 'failed';

export interface EmailDelivery {
    id: string;
    created_at: string;
    branch_id: string | null;
    recipient: string;
    subject: string;
    document_type: string;
    document_id: string | null;
    document_number: string | null;
    status: EmailDeliveryStatus;
    sender_answer: string | null;
    error: string | null;
    sent_by: string | null;
}

async function expectOk(response: Response, fallback: string): Promise<Response> {
    if (response.ok) return response;
    throw await parseApiError(response, fallback);
}

function sleep(ms: number): Promise<void> {
    return new Promise((resolve) => setTimeout(resolve, ms));
}

export const EmailDeliveryService = {
    /** The document's sends, newest first. */
    async listForDocument(documentId: string, limit = 20): Promise<EmailDelivery[]> {
        const response = await expectOk(
            await fetchWithAuth(`${API_URL}/api/v1/email-deliveries?document_id=${documentId}&limit=${limit}`),
            'Failed to read the email sends',
        );
        const page = await response.json();
        return page.items ?? [];
    },

    /**
     * Follows one queued delivery until the send resolves it, bounded: a slow
     * mail server keeps the row queued, and the bound running out returns the
     * queued row (still sending), never a made up outcome. A refused read
     * (403) returns OUTCOME_UNKNOWN: the send was accepted, the outcome is
     * just not this sign in's to see, and that is never a failure. The wait
     * between reads doubles up to a second, so a slow send costs few reads.
     */
    async follow(
        deliveryId: string,
        documentId: string,
        boundMs = 6000,
        stepMs = 250,
    ): Promise<EmailDelivery | typeof OUTCOME_UNKNOWN | undefined> {
        const deadline = Date.now() + boundMs;
        let wait = stepMs;
        for (;;) {
            let rows: EmailDelivery[];
            try {
                rows = await this.listForDocument(documentId);
            } catch (err) {
                if (err instanceof ApiError && err.status === 403) return OUTCOME_UNKNOWN;
                throw err;
            }
            const row = rows.find((d) => d.id === deliveryId);
            if (row && row.status !== 'queued') return row;
            if (Date.now() + wait > deadline) return row;
            await sleep(wait);
            wait = Math.min(wait * 2, 1000);
        }
    },
};
