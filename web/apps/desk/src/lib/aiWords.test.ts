// SPDX-License-Identifier: LicenseRef-OpenLBM-Surface-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

import { describe, it, expect } from 'vitest';
import { aiHeadline, aiStatusLine, imageWords, modelLabel, providerLabel } from './aiWords.ts';

describe('aiWords', () => {
    it('names the chosen provider and model when a key is saved', () => {
        expect(aiStatusLine({ provider: 'anthropic', model: 'claude-sonnet-5-5', configured: true })).toBe('AI on: Anthropic, Sonnet 5.5');
        expect(aiStatusLine({ provider: 'anthropic', model: 'claude-opus-5-5', configured: true })).toBe('AI on: Anthropic, Opus 5.5');
        expect(aiStatusLine({ provider: 'openrouter', model: 'claude-sonnet-5-5', configured: true })).toBe('AI on: OpenRouter');
    });

    it('asks for the chosen provider\'s key, never the other one\'s', () => {
        expect(aiStatusLine({ provider: 'anthropic', model: 'claude-sonnet-5-5', configured: false })).toBe('Enter your Anthropic API key to enable AI-powered features.');
        expect(aiStatusLine({ provider: 'openrouter', model: '', configured: false })).toBe('Enter your OpenRouter API key to enable AI-powered features.');
        expect(aiStatusLine({ provider: 'anthropic', model: 'claude-sonnet-5-5', configured: false })).not.toContain('OpenRouter');
    });

    it('reads the headline from the state', () => {
        expect(aiHeadline({ provider: 'anthropic', model: '', configured: true })).toBe('AI Features Active');
        expect(aiHeadline(null)).toBe('AI Features Inactive');
    });

    it('formats model ids and falls back to the id', () => {
        expect(modelLabel('claude-sonnet-5-5-20260101')).toBe('Sonnet 5.5');
        expect(modelLabel('claude-fable-5-1')).toBe('Fable 5.1');
        expect(modelLabel('some-other-model')).toBe('some-other-model');
        expect(providerLabel('anthropic')).toBe('Anthropic');
    });

    it('says which image kind the button makes', () => {
        expect(imageWords('anthropic').button).toBe('Generate illustration');
        expect(imageWords('anthropic').kind).toBe('illustration');
        expect(imageWords('openrouter').button).toBe('Generate image');
        expect(imageWords(null).button).toBe('Generate image');
    });
});
