// SPDX-License-Identifier: LicenseRef-OpenLBM-Surface-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

/**
 * The runtime auth methods loader: the server's answer parses field by field
 * into the strict type, a failure of any kind fails closed to email and
 * password with the visible notice, and the fetch happens once for the
 * page's life however many callers ask.
 */
import { describe, it, expect, beforeEach, vi } from 'vitest';
import {
  authMethodsConfig,
  parseAuthMethodsConfig,
  resetAuthMethodsCache,
  degradedAuthMethodsConfig,
} from './runtime-config.ts';

const TWO_METHODS = {
  auth_mode: 'production',
  methods: [
    { method: 'google', kind: 'redirect', label: 'Google', start_url: '/api/v1/auth/start/google', issuer_url: null, client_id: null },
    { method: 'email_password', kind: 'form', label: 'Email and password', start_url: '/api/portal/v1/login', issuer_url: null, client_id: null },
  ],
};

beforeEach(() => {
  resetAuthMethodsCache();
});

describe('parseAuthMethodsConfig', () => {
  it('keeps the methods in the server\'s order with exactly the type\'s fields', () => {
    const cfg = parseAuthMethodsConfig(TWO_METHODS);
    expect(cfg.degraded).toBe(false);
    expect(cfg.notice).toBeNull();
    expect(cfg.methods.map((m) => m.method)).toEqual(['google', 'email_password']);
    expect(Object.keys(cfg.methods[0]).sort()).toEqual(['client_id', 'issuer_url', 'kind', 'label', 'method', 'start_url']);
  });

  it('carries the oidc issuer and client id on the oidc method alone', () => {
    const cfg = parseAuthMethodsConfig({
      auth_mode: 'production',
      methods: [
        { method: 'oidc', kind: 'redirect', label: 'Dealer SSO', start_url: '/api/v1/auth/start/oidc', issuer_url: 'https://id.example.com', client_id: 'gable' },
      ],
    });
    expect(cfg.methods[0].issuer_url).toBe('https://id.example.com');
    expect(cfg.methods[0].client_id).toBe('gable');
    expect(cfg.methods[0].label).toBe('Dealer SSO');
  });

  it('fails closed on a non object, a missing list, an empty list or unknown methods', () => {
    const degraded = degradedAuthMethodsConfig();
    for (const bad of [null, 3, {}, { auth_mode: 'production', methods: [] }, { auth_mode: 'production', methods: [{ method: 'sms', kind: 'form' }] }]) {
      expect(parseAuthMethodsConfig(bad)).toEqual(degraded);
    }
  });
});

describe('authMethodsConfig', () => {
  it('fetches the config from the route on the given base', async () => {
    const fetchFn = vi.fn(async () => new Response(JSON.stringify(TWO_METHODS), { status: 200 }));
    const cfg = await authMethodsConfig('http://api.example.test', fetchFn);
    expect(fetchFn).toHaveBeenCalledTimes(1);
    expect(fetchFn.mock.calls[0][0]).toBe('http://api.example.test/api/v1/auth/config');
    const init = fetchFn.mock.calls[0][1] as RequestInit;
    expect((init.headers as Record<string, string>).Accept).toBe('application/json');
    expect(cfg.methods.map((m) => m.method)).toEqual(['google', 'email_password']);
  });

  it('fails closed with the notice when the call fails or the answer is not 200', async () => {
    const unreachable = vi.fn(async () => { throw new Error('network down'); });
    const cfg = await authMethodsConfig('', unreachable);
    expect(cfg.degraded).toBe(true);
    expect(cfg.notice).toContain('email and password');
    expect(cfg.methods).toHaveLength(1);
    expect(cfg.methods[0].method).toBe('email_password');

    resetAuthMethodsCache();
    const notFound = vi.fn(async () => new Response('no', { status: 404 }));
    const cfg404 = await authMethodsConfig('', notFound);
    expect(cfg404.degraded).toBe(true);
  });

  it('caches: a second caller gets the first promise and the fetch runs once', async () => {
    const fetchFn = vi.fn(async () => new Response(JSON.stringify(TWO_METHODS), { status: 200 }));
    const [a, b] = await Promise.all([authMethodsConfig('', fetchFn), authMethodsConfig('', fetchFn)]);
    expect(fetchFn).toHaveBeenCalledTimes(1);
    expect(a).toBe(b);
    // After the cache is set, even a failing fetch cannot change the answer.
    const again = await authMethodsConfig('', vi.fn(async () => { throw new Error('later failure'); }));
    expect(again).toBe(a);
  });
});
