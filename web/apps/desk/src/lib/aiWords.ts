// SPDX-License-Identifier: LicenseRef-OpenLBM-Surface-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

/**
 * The desk's AI words, read from the provider's state (the AI settings
 * document) so no status line, banner or button names a provider that is not
 * the chosen one.
 */

export type AIProvider = 'openrouter' | 'anthropic';

/** The slice of the AI settings document the words read. */
export interface AIState {
    provider: AIProvider;
    model: string;
    configured: boolean;
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

/** The Tech Admin status line: what is on, or what to enter. */
export function aiStatusLine(s: AIState | null): string {
    if (!s) return 'AI settings are loading.';
    const name = providerLabel(s.provider);
    if (!s.configured) return `Enter your ${name} API key to enable AI-powered features.`;
    return s.provider === 'anthropic' ? `AI on: ${name}, ${modelLabel(s.model)}` : `AI on: ${name}`;
}

/** The status card's headline. */
export function aiHeadline(s: AIState | null): string {
    return s?.configured ? 'AI Features Active' : 'AI Features Inactive';
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
            failed: 'The illustration could not be drawn or was refused. Please try again.',
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
        failed: 'Image generation failed. Please try again.',
        empty: 'No images yet. Generate one using AI above.',
    };
}
