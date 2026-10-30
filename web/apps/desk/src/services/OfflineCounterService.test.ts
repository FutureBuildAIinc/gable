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
    { method: 'email_password', kind: 'form', label: 'Email and password', start_url: '/api/v1/auth/signin' },
    { method: 'google', kind: 'redirect', label: 'Google', start_url: '/api/v1/auth/google/start' },
  ],
};

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
    fetchFn.mockResolvedValue(new Response(JSON.stringify({ ...config, methods: [config.methods[0]] }), { status: 200 }));
    const refused = await offlineCounter.signIn().then(() => null, (e: unknown) => e);
    expect((refused as OfflineCounterError).code).toBe('no_sign_in_method');
    expect(invoke).not.toHaveBeenCalled();
  });
});
