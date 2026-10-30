// SPDX-License-Identifier: LicenseRef-OpenLBM-Surface-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

// The register's own sign in during a drop (PR 246's final read, P3-3). The
// core answers no_session from the keychain before any network call, so the
// clerk can tap Sign in this register while the server is gone. The dealer's
// methods then fail closed to a degraded answer: the register says the server
// cannot be reached, not that the dealer allows no method, and asks again on
// the next tap instead of keeping the degraded answer for the page's life.

import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { resetAuthMethodsCache } from '@gable/auth';
import { OfflineCounterError, offlineCounter, setOfflineInvoke } from './OfflineCounterService';

const config = {
  auth_mode: 'production',
  methods: [
    { method: 'google', kind: 'redirect', label: 'Google', start_url: '/api/v1/auth/google/start' },
    { method: 'email_password', kind: 'form', label: 'Email and password', start_url: '/api/v1/auth/signin' },
  ],
};
const passwordFirst = { ...config, methods: [config.methods[1], config.methods[0]] };

describe('offlineCounter.signIn during a drop', () => {
  const invoke = vi.fn(async () => undefined);
  const fetchFn = vi.fn();

  beforeEach(() => {
    resetAuthMethodsCache();
    invoke.mockClear();
    fetchFn.mockReset();
    setOfflineInvoke(invoke);
    vi.stubGlobal('fetch', fetchFn);
  });

  afterEach(() => {
    setOfflineInvoke(undefined);
    vi.unstubAllGlobals();
    resetAuthMethodsCache();
  });

  it('says the server cannot be reached, then reads the methods again once it answers', async () => {
    fetchFn.mockRejectedValueOnce(new TypeError('Failed to fetch'));

    const refused = await offlineCounter.signIn().then(() => null, (e: unknown) => e);
    expect(refused).toBeInstanceOf(OfflineCounterError);
    expect((refused as OfflineCounterError).code).toBe('server_unreachable');
    expect((refused as OfflineCounterError).message).toContain('The server cannot be reached');
    expect((refused as OfflineCounterError).message).not.toContain('no sign in method');
    expect(invoke).not.toHaveBeenCalled();

    fetchFn.mockResolvedValueOnce(new Response(JSON.stringify(config), { status: 200 }));
    await offlineCounter.signIn();
    expect(fetchFn).toHaveBeenCalledTimes(2);
    expect(invoke).toHaveBeenCalledWith('begin_sign_in', { method: 'google' });
  });

  it('keeps the no method answer for a dealer that answered and allows no redirect method', async () => {
    fetchFn.mockResolvedValue(new Response(JSON.stringify({ ...config, methods: [{ method: 'magic_link', kind: 'link', label: 'Email link', start_url: '/x' }] }), { status: 200 }));
    const refused = await offlineCounter.signIn().then(() => null, (e: unknown) => e);
    expect((refused as OfflineCounterError).code).toBe('no_sign_in_method');
    expect(invoke).not.toHaveBeenCalled();
  });

  it('opens the form, and starts nothing, when the dealer lists email and password first', async () => {
    fetchFn.mockResolvedValue(new Response(JSON.stringify(passwordFirst), { status: 200 }));
    await expect(offlineCounter.signIn()).resolves.toBe('form');
    expect(invoke).not.toHaveBeenCalled();
  });

  it('opens the form when email and password is the dealer\'s only method', async () => {
    fetchFn.mockResolvedValue(new Response(JSON.stringify({ ...config, methods: [config.methods[1]] }), { status: 200 }));
    await expect(offlineCounter.signIn()).resolves.toBe('form');
    expect(invoke).not.toHaveBeenCalled();
  });

  it('starts the first redirect method when a redirect method comes first', async () => {
    fetchFn.mockResolvedValue(new Response(JSON.stringify(config), { status: 200 }));
    await expect(offlineCounter.signIn()).resolves.toBe('started');
    expect(invoke).toHaveBeenCalledWith('begin_sign_in', { method: 'google' });
  });
});

describe('offlineCounter.signInWithPassword', () => {
  const invoke = vi.fn(async (_c: string, _a?: Record<string, unknown>) => undefined);
  beforeEach(() => { invoke.mockReset(); setOfflineInvoke(invoke); });
  afterEach(() => setOfflineInvoke(undefined));

  it('sends the email and password to the shell command once and answers nothing', async () => {
    invoke.mockResolvedValue(undefined);
    await expect(offlineCounter.signInWithPassword('a@b.example', 'hunter2')).resolves.toBeUndefined();
    expect(invoke).toHaveBeenCalledTimes(1);
    expect(invoke).toHaveBeenCalledWith('sign_in_with_password', { email: 'a@b.example', password: 'hunter2' });
  });

  it.each([
    ['sign_in_refused', 'That email and password did not sign this register in. Type the password again.'],
    ['sign_in_limited', 'Too many tries. Wait a little, then try again.'],
    ['sign_in_not_allowed', 'Ask a manager'],
    ['sign_in_unavailable', 'The server cannot be reached'],
    ['no_server', 'This register has no server set, so it cannot sign in. Ask a manager.'],
  ])('maps the shell code %s to its own words', async (code, words) => {
    invoke.mockRejectedValue(code);
    const e = await offlineCounter.signInWithPassword('a@b.example', 'hunter2').then(() => null, (x: unknown) => x);
    expect(e).toBeInstanceOf(OfflineCounterError);
    expect((e as OfflineCounterError).code).toBe(code);
    expect((e as OfflineCounterError).message).toContain(words);
  });

  it('answers sign_in_failed with the generic words for a code it does not know', async () => {
    for (const raw of ['something_new', { code: 'something_new' }, 42, null]) {
      invoke.mockRejectedValue(raw);
      const e = await offlineCounter.signInWithPassword('a@b.example', 'hunter2').then(() => null, (x: unknown) => x);
      expect(e).toBeInstanceOf(OfflineCounterError);
      expect((e as OfflineCounterError).code).toBe('sign_in_failed');
      expect((e as OfflineCounterError).message).toBe('The register could not be signed in. Ask a manager.');
    }
  });

  it('never puts the password in an error, whatever the shell answers', async () => {
    for (const raw of ['sign_in_refused', 'weird hunter2 text', { code: 'x', message: 'hunter2' }]) {
      invoke.mockRejectedValue(raw);
      const e = await offlineCounter.signInWithPassword('a@b.example', 'hunter2').then(() => null, (x: unknown) => x);
      expect((e as OfflineCounterError).message).not.toContain('hunter2');
      expect(JSON.stringify(e)).not.toContain('hunter2');
    }
  });

  it('says a browser tab has no core', async () => {
    setOfflineInvoke(null);
    const e = await offlineCounter.signInWithPassword('a@b.example', 'hunter2').then(() => null, (x: unknown) => x);
    expect((e as OfflineCounterError).code).toBe('no_core');
  });
});
