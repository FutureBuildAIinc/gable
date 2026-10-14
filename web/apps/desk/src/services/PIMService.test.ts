// SPDX-License-Identifier: LicenseRef-OpenLBM-Surface-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

/** With AI off, every generate route answers the ai_off envelope and the desk shows its message. */
import { describe, it, expect, beforeEach, afterEach, vi } from 'vitest';
import { PIMService } from './PIMService';
import { ApiError, apiErrorMessage } from './apiError';

const MESSAGE = 'AI is off. An administrator turns it on in Tech Admin, AI, by choosing a provider and entering its key.';

beforeEach(() => {
    vi.stubGlobal(
        'fetch',
        vi.fn(
            async () =>
                new Response(JSON.stringify({ error: { code: 'ai_off', message: MESSAGE }, meta: { request_id: 'r' } }), {
                    status: 409,
                    headers: { 'Content-Type': 'application/json' },
                }),
        ),
    );
});

afterEach(() => vi.unstubAllGlobals());

describe('PIMService with AI off', () => {
    it.each([
        ['descriptions', () => PIMService.generateDescriptions('p', {})],
        ['seo', () => PIMService.generateSEO('p', { target_keywords: [] })],
        ['image', () => PIMService.generateImage('p', {})],
        ['collateral', () => PIMService.generateCollateral('p', { type: 'sell_sheet' })],
    ])('%s throws the ai_off message', async (_name, call) => {
        const err = await call().catch((e: unknown) => e);
        expect(err).toBeInstanceOf(ApiError);
        expect((err as ApiError).code).toBe('ai_off');
        expect(apiErrorMessage(err)).toBe(MESSAGE);
    });
});
