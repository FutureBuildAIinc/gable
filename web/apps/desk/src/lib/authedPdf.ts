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
 * download link, or, when a file name is given, saves the document under it;
 * a refusal closes the tab and throws the server's words.
 */
export async function openAuthedPdf(path: string, fallback: string, filename?: string): Promise<{ url: string; blocked: boolean }> {
    const tab = window.open('', '_blank');
    try {
        const response = await fetchWithAuth(`${API_URL}${path}`);
        if (!response.ok) throw await parseApiError(response, fallback);
        const url = URL.createObjectURL(await response.blob());
        if (tab === null) {
            // a blocked tab: with a file name the document is saved instead of dropped
            if (filename) saveBlobUrl(url, filename);
            return { url, blocked: true };
        }
        tab.location.href = url;
        // the tab has the document by then
        setTimeout(() => URL.revokeObjectURL(url), 60_000);
        return { url, blocked: false };
    } catch (err) {
        tab?.close();
        throw err;
    }
}

/** Saves a blob url as a download under a file name, then lets the url go. */
export function saveBlobUrl(url: string, filename: string): void {
    const a = document.createElement('a');
    a.href = url;
    a.download = filename;
    a.click();
    setTimeout(() => URL.revokeObjectURL(url), 60_000);
}

/**
 * Fetches a file on an API address with the signed in token and answers a blob
 * url: an image or a pdf the page shows, or a file it saves. A plain `src` or
 * `href` on the API's address carries no token. The caller revokes the url.
 */
export async function fetchAuthedBlobUrl(path: string, fallback: string): Promise<string> {
    const response = await fetchWithAuth(`${API_URL}${path}`);
    if (!response.ok) throw await parseApiError(response, fallback);
    return URL.createObjectURL(await response.blob());
}
