// SPDX-License-Identifier: LicenseRef-OpenLBM-Surface-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

import type { ParseResponse } from '../types/parsing';
import { fetchWithAuth } from './fetchClient';
import { parseApiError } from './apiError';

const API_URL = import.meta.env.VITE_API_URL || '';

// AI vision OCR of a material list can take 10–60s — beyond the default 10s timeout.
const AI_PARSE_TIMEOUT = 120_000;

export const ParsingService = {
    /**
     * Upload a material list image for AI parsing.
     * Returns parsed items matched against the product catalog.
     */
    async uploadMaterialList(file: File): Promise<ParseResponse> {
        const formData = new FormData();
        formData.append('file', file);

        const response = await fetchWithAuth(`${API_URL}/api/v1/parsing/upload`, {
            method: 'POST',
            body: formData,
            timeout: AI_PARSE_TIMEOUT,
        });

        // The wire error envelope carries the words a person reads and the
        // code a caller branches on (ai_refused, unsupported_type).
        if (!response.ok) throw await parseApiError(response, 'Failed to parse material list');

        return response.json();
    },

    /**
     * Parse a pasted list. It goes to the same route as a text file, so a list
     * a person types is read by the server's own reader with no AI key.
     */
    async uploadPastedList(text: string): Promise<ParseResponse> {
        return ParsingService.uploadMaterialList(new File([text], 'pasted-list.txt', { type: 'text/plain' }));
    },
};
