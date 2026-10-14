// SPDX-License-Identifier: LicenseRef-OpenLBM-Surface-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

import { describe, it, expect } from 'vitest';
import { aiHeadline, aiStatusLine, imageWords, modelLabel, providerLabel } from './aiWords.ts';

describe('aiWords', () => {
    it('names the chosen provider and model once the saved key passed its test', () => {
        const passed = { configured: true, test_status: 'passed' as const };
        expect(aiStatusLine({ provider: 'anthropic', model: 'claude-sonnet-5-5', ...passed })).toBe('AI on: Anthropic, Sonnet 5.5');
        expect(aiStatusLine({ provider: 'anthropic', model: 'claude-opus-5-5', ...passed })).toBe('AI on: Anthropic, Opus 5.5');
        expect(aiStatusLine({ provider: 'openrouter', model: 'claude-sonnet-5-5', ...passed })).toBe('AI on: OpenRouter');
    });

    it('says AI is off when the provider refused the saved key', () => {
        const refused = { configured: true, test_status: 'refused' as const };
        expect(aiStatusLine({ provider: 'anthropic', model: 'claude-sonnet-5-5', ...refused })).toBe('AI off: Anthropic refused the saved key');
        expect(aiStatusLine({ provider: 'openrouter', model: '', ...refused })).toBe('AI off: OpenRouter refused the saved key');
        expect(aiHeadline({ provider: 'anthropic', model: '', ...refused })).toBe('AI Features Off');
    });

    it('says a saved key was not tested yet, and that a failed test is not a pass', () => {
        const untested = { configured: true, test_status: 'untested' as const };
        expect(aiStatusLine({ provider: 'anthropic', model: 'claude-sonnet-5-5', ...untested })).toBe('Key saved, not tested yet. Press Test connection to turn AI on.');
        expect(aiHeadline({ provider: 'anthropic', model: '', ...untested })).toBe('AI Features Not Tested');
        const failed = { configured: true, test_status: 'failed' as const };
        expect(aiStatusLine({ provider: 'anthropic', model: 'claude-sonnet-5-5', ...failed })).toBe('AI off: the test of the saved Anthropic key did not pass');
    });

    it('asks for the chosen provider\'s key, never the other one\'s', () => {
        expect(aiStatusLine({ provider: 'anthropic', model: 'claude-sonnet-5-5', configured: false })).toBe('Enter your Anthropic API key to enable AI-powered features.');
        expect(aiStatusLine({ provider: 'openrouter', model: '', configured: false })).toBe('Enter your OpenRouter API key to enable AI-powered features.');
        expect(aiStatusLine({ provider: 'anthropic', model: 'claude-sonnet-5-5', configured: false })).not.toContain('OpenRouter');
    });

    it('reads the headline from the state', () => {
        expect(aiHeadline({ provider: 'anthropic', model: '', configured: true, test_status: 'passed' })).toBe('AI Features Active');
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
