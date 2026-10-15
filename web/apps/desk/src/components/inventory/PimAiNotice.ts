// SPDX-License-Identifier: LicenseRef-OpenLBM-Surface-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

import { html, nothing, type TemplateResult } from 'lit';
import { ApiError } from '../../services/apiError';

/**
 * The words of a provider's refusal (code ai_refused) from a failed PIM
 * generate call, or null for any other failure (those stay a toast).
 */
export function aiRefusalWords(err: unknown): string | null {
    return err instanceof ApiError && err.code === 'ai_refused' ? err.message : null;
}

/** The notice a generate tab shows where the refusal happened, until the next try. */
export function renderAiNotice(words: string | null): TemplateResult | typeof nothing {
    if (!words) return nothing;
    return html`
        <div data-testid="pim-ai-notice" role="alert" class="rounded-xl border border-amber-500/40 bg-amber-500/10 p-4">
            <p class="text-sm font-medium text-amber-300">${words}</p>
        </div>
    `;
}
