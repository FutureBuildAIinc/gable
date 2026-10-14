// SPDX-License-Identifier: LicenseRef-OpenLBM-Surface-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

import { fetchWithAuth } from '../services/fetchClient';
import { parseApiError } from '../services/apiError';

const API_URL = import.meta.env.VITE_API_URL || '';

/**
 * Shows a document the API prints (a PDF behind a GET) to a signed in person.
 * A plain new tab on the API's address carries no Authorization header and
 * lands on a 401, so the document is fetched through the authenticated client
 * and shown from a blob url in a tab opened during the click, before any await,
 * so a pop up blocker lets it through. A blocked tab answers the url for a
 * download link; a refusal closes the tab and throws the server's words.
 */
export async function openAuthedPdf(path: string, fallback: string): Promise<{ url: string; blocked: boolean }> {
    const tab = window.open('', '_blank');
    try {
        const response = await fetchWithAuth(`${API_URL}${path}`);
        if (!response.ok) throw await parseApiError(response, fallback);
        const url = URL.createObjectURL(await response.blob());
        if (tab === null) return { url, blocked: true };
        tab.location.href = url;
        // the tab has the document by then
        setTimeout(() => URL.revokeObjectURL(url), 60_000);
        return { url, blocked: false };
    } catch (err) {
        tab?.close();
        throw err;
    }
}
