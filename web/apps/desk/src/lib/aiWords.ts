// SPDX-License-Identifier: LicenseRef-OpenLBM-Surface-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

/**
 * The desk's AI words, read from the provider's state (the AI settings
 * document) so no status line, banner or button names a provider that is not
 * the chosen one.
 */

export type AIProvider = 'openrouter' | 'anthropic';

/** The last test of the saved key, as the settings document reports it. */
export type AITestStatus = 'none' | 'untested' | 'passed' | 'refused' | 'failed';

/** The slice of the AI settings document the words read. */
export interface AIState {
    provider: AIProvider;
    model: string;
    configured: boolean;
    /** AI is on only after a passed test; absent reads as untested. */
    test_status?: AITestStatus;
}

/** "Anthropic" or "OpenRouter". */
export function providerLabel(provider: AIProvider): string {
    return provider === 'anthropic' ? 'Anthropic' : 'OpenRouter';
}

/** "claude-sonnet-5-5" is "Sonnet 5.5"; anything else is shown as it is. */
export function modelLabel(model: string): string {
    const m = /^claude-(sonnet|opus|fable)-(\d+)(?:-(\d{1,2}))?(?:-\d{8})?$/.exec(model.trim());
    if (!m) return model;
    const family = m[1].charAt(0).toUpperCase() + m[1].slice(1);
    return m[3] !== undefined ? `${family} ${m[2]}.${m[3]}` : `${family} ${m[2]}`;
}

/** True only for a saved key whose last test passed: the one state AI is on. */
export function aiIsOn(s: AIState | null): boolean {
    return !!s && s.configured && s.test_status === 'passed';
}

/** The Tech Admin status line: what is on, what was refused, or what to enter. */
export function aiStatusLine(s: AIState | null): string {
    if (!s) return 'AI settings are loading.';
    const name = providerLabel(s.provider);
    if (!s.configured) return `Enter your ${name} API key to enable AI-powered features.`;
    switch (s.test_status) {
        case 'passed':
            return s.provider === 'anthropic' ? `AI on: ${name}, ${modelLabel(s.model)}` : `AI on: ${name}`;
        case 'refused':
            return `AI off: ${name} refused the saved key`;
        case 'failed':
            return `AI off: the test of the saved ${name} key did not pass`;
        default:
            return 'Key saved, not tested yet. Press Test connection to turn AI on.';
    }
}

/** The status card's headline. */
export function aiHeadline(s: AIState | null): string {
    if (aiIsOn(s)) return 'AI Features Active';
    if (!s?.configured) return 'AI Features Inactive';
    return s.test_status === 'refused' || s.test_status === 'failed' ? 'AI Features Off' : 'AI Features Not Tested';
}

/**
 * The lead line over Tech Admin's AI settings. A key the provider refused (or
 * whose test failed) is said once, in the status card, so the lead line names
 * only the state; every other state keeps the full line.
 */
export function aiLeadLine(s: AIState | null): string {
    if (s?.configured && (s.test_status === 'refused' || s.test_status === 'failed')) return 'AI off';
    return aiStatusLine(s);
}

/** What PIM's image generation makes with the chosen provider. */
export interface ImageWords {
    kind: 'illustration' | 'image';
    button: string;
    busy: string;
    heading: string;
    feature: string;
    featureDescription: string;
    failed: string;
    /** A generation the provider refused the saved key for: no retry can work. */
    refused: string;
    empty: string;
}

/** Claude draws illustrations; OpenRouter's model makes photos. The UI says which. */
export function imageWords(provider: AIProvider | null | undefined): ImageWords {
    if (provider === 'anthropic') {
        return {
            kind: 'illustration',
            button: 'Generate illustration',
            busy: 'Drawing...',
            heading: 'AI Illustration',
            feature: 'Product Illustrations',
            featureDescription: 'Claude draws a flat product illustration in the catalog style for each product',
            failed: 'The illustration could not be drawn. If it keeps failing, an administrator checks the key in Tech Admin, AI Settings.',
            refused: 'Anthropic refused the saved key. An administrator can enter it again in Tech Admin, AI Settings.',
            empty: 'No images yet. Generate an illustration using AI above.',
        };
    }
    return {
        kind: 'image',
        button: 'Generate image',
        busy: 'Generating...',
        heading: 'AI Image Generation',
        feature: 'Product Image Generation',
        featureDescription: 'Generate professional product images for your catalog',
        failed: 'Image generation failed. If it keeps failing, an administrator checks the key in Tech Admin, AI Settings.',
        refused: 'OpenRouter refused the saved key. An administrator can enter it again in Tech Admin, AI Settings.',
        empty: 'No images yet. Generate one using AI above.',
    };
}
